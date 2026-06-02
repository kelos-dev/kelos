/*
Copyright 2025 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	// DefaultSessionRunnerImage is the default image for the session runner binary.
	DefaultSessionRunnerImage = "ghcr.io/kelos-dev/kelos-session-runner:latest"

	// SessionRunnerVolumeName is the name of the volume used to inject the
	// session runner binary into agent containers.
	SessionRunnerVolumeName = "session-runner"

	// SessionRunnerMountPath is the mount path for the session runner binary.
	SessionRunnerMountPath = "/kelos/bin"

	// SessionComponentLabel is the component label value for session pods.
	SessionComponentLabel = "session"

	// DefaultSessionStorageSize is the default PVC size for session workspaces.
	DefaultSessionStorageSize = "10Gi"
)

// SessionStatefulSetBuilder constructs StatefulSets for persistent session pods.
type SessionStatefulSetBuilder struct {
	SessionRunnerImage           string
	SessionRunnerImagePullPolicy corev1.PullPolicy
	ClaudeCodeImage              string
	ClaudeCodeImagePullPolicy    corev1.PullPolicy
	CodexImage                   string
	CodexImagePullPolicy         corev1.PullPolicy
	GeminiImage                  string
	GeminiImagePullPolicy        corev1.PullPolicy
	OpenCodeImage                string
	OpenCodeImagePullPolicy      corev1.PullPolicy
	CursorImage                  string
	CursorImagePullPolicy        corev1.PullPolicy
}

// NewSessionStatefulSetBuilder creates a new SessionStatefulSetBuilder.
func NewSessionStatefulSetBuilder() *SessionStatefulSetBuilder {
	return &SessionStatefulSetBuilder{
		SessionRunnerImage: DefaultSessionRunnerImage,
		ClaudeCodeImage:    ClaudeCodeImage,
		CodexImage:         CodexImage,
		GeminiImage:        GeminiImage,
		OpenCodeImage:      OpenCodeImage,
		CursorImage:        CursorImage,
	}
}

// SessionStatefulSetInput holds all the inputs needed to build a session StatefulSet.
type SessionStatefulSetInput struct {
	TaskSpawner *kelosv1alpha1.TaskSpawner
	Workspace   *kelosv1alpha1.WorkspaceSpec
	AgentConfig *kelosv1alpha1.AgentConfigSpec
}

// Build creates a StatefulSet for persistent session pods.
func (b *SessionStatefulSetBuilder) Build(input SessionStatefulSetInput) (*appsv1.StatefulSet, *corev1.Service, error) {
	ts := input.TaskSpawner
	workspace := input.Workspace
	agentConfig := input.AgentConfig
	tmpl := &ts.Spec.TaskTemplate
	sessionCfg := ts.Spec.SessionConfig

	// Resolve defaults.
	replicas := int32(1)
	if sessionCfg != nil && sessionCfg.Replicas != nil {
		replicas = *sessionCfg.Replicas
	}

	storageSize := resource.MustParse(DefaultSessionStorageSize)
	if sessionCfg != nil && sessionCfg.StorageSize != nil {
		storageSize = *sessionCfg.StorageSize
	}

	agentUID := AgentUID
	name := sessionStatefulSetName(ts.Name)

	labels := map[string]string{
		"kelos.dev/name":           "kelos",
		"kelos.dev/component":      SessionComponentLabel,
		"kelos.dev/managed-by":     "kelos-controller",
		"kelos.dev/taskspawner":    ts.Name,
		"kelos.dev/execution-mode": string(kelosv1alpha1.ExecutionModePersistent),
	}

	// Build environment variables for the session runner.
	envVars := b.buildSessionEnvVars(ts, workspace)

	// Add credential env vars.
	credEnvVars := credentialEnvVars(tmpl.Credentials, tmpl.Type)
	envVars = append(envVars, credEnvVars...)

	// Add workspace-related env vars.
	var workspaceEnvVars []corev1.EnvVar
	if workspace != nil {
		host, _, _ := parseGitHubRepo(workspace.Repo)
		isEnterprise := host != "" && host != "github.com"

		if isEnterprise {
			ghHostEnv := corev1.EnvVar{Name: "GH_HOST", Value: host}
			envVars = append(envVars, ghHostEnv)
			workspaceEnvVars = append(workspaceEnvVars, ghHostEnv)
		}

		if workspace.Ref != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_BASE_BRANCH",
				Value: workspace.Ref,
			})
		}

		effectiveRemotes := effectiveWorkspaceRemotes(workspace)
		upstreamRepo := tmpl.UpstreamRepo
		if upstreamRepo == "" {
			upstreamRepo = upstreamRepoEnvValue(effectiveRemotes)
		}
		if upstreamRepo != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_UPSTREAM_REPO",
				Value: upstreamRepo,
			})
		}

		if workspace.SecretRef != nil {
			secretKeyRef := &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: workspace.SecretRef.Name,
				},
				Key: "GITHUB_TOKEN",
			}
			githubTokenEnv := corev1.EnvVar{
				Name:      "GITHUB_TOKEN",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeyRef},
			}
			envVars = append(envVars, githubTokenEnv)
			workspaceEnvVars = append(workspaceEnvVars, githubTokenEnv)

			ghTokenName := "GH_TOKEN"
			if isEnterprise {
				ghTokenName = "GH_ENTERPRISE_TOKEN"
			}
			ghTokenEnv := corev1.EnvVar{
				Name:      ghTokenName,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeyRef},
			}
			envVars = append(envVars, ghTokenEnv)
			workspaceEnvVars = append(workspaceEnvVars, ghTokenEnv)

			envVars = append(envVars, corev1.EnvVar{
				Name:  "GH_CONFIG_DIR",
				Value: GHConfigDir,
			})

			// Expose the secret name so the session runner can refresh the
			// token mid-session when the controller rotates it.
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_TOKEN_SECRET",
				Value: workspace.SecretRef.Name,
			})
		}
	}

	// AgentConfig env vars.
	if agentConfig != nil {
		if agentConfig.AgentsMD != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_AGENTS_MD",
				Value: agentConfig.AgentsMD,
			})
		}
		if len(agentConfig.MCPServers) > 0 {
			mcpJSON, err := buildMCPServersJSON(agentConfig.MCPServers)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid MCP server configuration: %w", err)
			}
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_MCP_SERVERS",
				Value: mcpJSON,
			})
		}
	}

	// Apply PodOverrides env vars.
	if po := tmpl.PodOverrides; po != nil && len(po.Env) > 0 {
		builtinNames := make(map[string]struct{}, len(envVars))
		for _, e := range envVars {
			builtinNames[e.Name] = struct{}{}
		}
		for _, e := range po.Env {
			if _, exists := builtinNames[e.Name]; !exists {
				envVars = append(envVars, e)
			}
		}
	}

	// Build init containers.
	initContainers, err := b.buildInitContainers(workspace, agentConfig, tmpl.Type, workspaceEnvVars)
	if err != nil {
		return nil, nil, err
	}

	// Build volumes (plugin volume is EmptyDir, workspace is PVC via VolumeClaimTemplate).
	volumes := b.buildVolumes(agentConfig)

	// Build volume mounts for the main container.
	volumeMounts := []corev1.VolumeMount{
		{Name: WorkspaceVolumeName, MountPath: WorkspaceMountPath},
		{Name: SessionRunnerVolumeName, MountPath: SessionRunnerMountPath},
	}
	needsPluginVolume := agentConfig != nil && (len(agentConfig.Plugins) > 0 || len(agentConfig.Skills) > 0)
	if needsPluginVolume {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name: PluginVolumeName, MountPath: PluginMountPath,
		})
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_PLUGIN_DIR",
			Value: PluginMountPath,
		})
	}

	// Resolve agent image.
	agentImage, agentImagePullPolicy := b.resolveAgentImage(tmpl)
	if agentImagePullPolicy == "" {
		agentImagePullPolicy = corev1.PullIfNotPresent
	}

	// Main container runs the session runner, which delegates to the agent entrypoint.
	mainContainer := corev1.Container{
		Name:            tmpl.Type,
		Image:           agentImage,
		ImagePullPolicy: agentImagePullPolicy,
		Command:         []string{SessionRunnerMountPath + "/kelos-session-runner"},
		Env:             envVars,
		VolumeMounts:    volumeMounts,
		WorkingDir:      WorkspaceMountPath + "/repo",
	}

	if po := tmpl.PodOverrides; po != nil && po.Resources != nil {
		mainContainer.Resources = *po.Resources
	}

	// Pod scheduling and security overrides.
	var nodeSelector map[string]string
	var tolerations []corev1.Toleration
	var affinity *corev1.Affinity
	var imagePullSecrets []corev1.LocalObjectReference
	podSecurityContext := &corev1.PodSecurityContext{
		FSGroup: &agentUID,
	}
	serviceAccountName := SessionRunnerServiceAccount
	if po := tmpl.PodOverrides; po != nil {
		nodeSelector = po.NodeSelector

		if len(po.Tolerations) > 0 {
			tolerations = po.Tolerations
		}

		if po.Affinity != nil {
			affinity = po.Affinity
		}

		if len(po.ImagePullSecrets) > 0 {
			imagePullSecrets = po.ImagePullSecrets
		}

		if po.ServiceAccountName != "" {
			serviceAccountName = po.ServiceAccountName
		}

		if len(po.Volumes) > 0 {
			if err := validateUserVolumes(po.Volumes); err != nil {
				return nil, nil, err
			}
			volumes = append(volumes, po.Volumes...)
		}

		if len(po.VolumeMounts) > 0 {
			mainContainer.VolumeMounts = append(mainContainer.VolumeMounts, po.VolumeMounts...)
		}

		if po.PodSecurityContext != nil {
			merged := po.PodSecurityContext.DeepCopy()
			if merged.FSGroup == nil {
				merged.FSGroup = &agentUID
			}
			podSecurityContext = merged
		}

		if po.ContainerSecurityContext != nil {
			mainContainer.SecurityContext = po.ContainerSecurityContext.DeepCopy()
		}
	}

	// VolumeClaimTemplate for workspace.
	pvcSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: storageSize,
			},
		},
	}
	if sessionCfg != nil && sessionCfg.StorageClassName != nil {
		pvcSpec.StorageClassName = sessionCfg.StorageClassName
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ts.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            &replicas,
			ServiceName:         name,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"kelos.dev/taskspawner": ts.Name,
					"kelos.dev/component":   SessionComponentLabel,
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: WorkspaceVolumeName,
					},
					Spec: pvcSpec,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccountName,
					SecurityContext:    podSecurityContext,
					InitContainers:     initContainers,
					Containers:         []corev1.Container{mainContainer},
					Volumes:            volumes,
					NodeSelector:       nodeSelector,
					Tolerations:        tolerations,
					Affinity:           affinity,
					ImagePullSecrets:   imagePullSecrets,
				},
			},
		},
	}

	// Headless Service required by StatefulSet.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ts.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector: map[string]string{
				"kelos.dev/taskspawner": ts.Name,
				"kelos.dev/component":   SessionComponentLabel,
			},
		},
	}

	return sts, svc, nil
}

// buildSessionEnvVars creates the environment variables specific to the
// session runner (execution mode, timeouts, etc.).
func (b *SessionStatefulSetBuilder) buildSessionEnvVars(ts *kelosv1alpha1.TaskSpawner, workspace *kelosv1alpha1.WorkspaceSpec) []corev1.EnvVar {
	tmpl := &ts.Spec.TaskTemplate
	sessionCfg := ts.Spec.SessionConfig

	envVars := []corev1.EnvVar{
		{Name: "KELOS_EXECUTION_MODE", Value: string(kelosv1alpha1.ExecutionModePersistent)},
		{Name: "KELOS_AGENT_TYPE", Value: tmpl.Type},
		{Name: "KELOS_TASKSPAWNER", Value: ts.Name},
		{Name: "KELOS_TASKSPAWNER_NAMESPACE", Value: ts.Namespace},
		// Pod name and namespace injected via downward API.
		{
			Name: "KELOS_POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
		{
			Name: "KELOS_POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
	}

	if tmpl.Model != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_MODEL",
			Value: tmpl.Model,
		})
	}

	if sessionCfg != nil {
		if sessionCfg.IdleTimeout != nil {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_IDLE_TIMEOUT",
				Value: sessionCfg.IdleTimeout.Duration.String(),
			})
		}
		if sessionCfg.MaxTasksPerSession != nil {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_MAX_TASKS_PER_SESSION",
				Value: fmt.Sprintf("%d", *sessionCfg.MaxTasksPerSession),
			})
		}
		if sessionCfg.MaxSessionDuration != nil {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_MAX_SESSION_DURATION",
				Value: sessionCfg.MaxSessionDuration.Duration.String(),
			})
		}
		if sessionCfg.WorkspaceReset != nil {
			if sessionCfg.WorkspaceReset.Git != nil {
				val := "true"
				if !*sessionCfg.WorkspaceReset.Git {
					val = "false"
				}
				envVars = append(envVars, corev1.EnvVar{
					Name:  "KELOS_WORKSPACE_RESET_GIT",
					Value: val,
				})
			}
			if len(sessionCfg.WorkspaceReset.PreserveDirectories) > 0 {
				dirs, _ := json.Marshal(sessionCfg.WorkspaceReset.PreserveDirectories)
				envVars = append(envVars, corev1.EnvVar{
					Name:  "KELOS_WORKSPACE_RESET_PRESERVE_DIRS",
					Value: string(dirs),
				})
			}
		}
	}

	return envVars
}

// buildInitContainers creates the ordered list of init containers for
// session pods. The first init is always the session-runner injector.
func (b *SessionStatefulSetBuilder) buildInitContainers(
	workspace *kelosv1alpha1.WorkspaceSpec,
	agentConfig *kelosv1alpha1.AgentConfigSpec,
	agentType string,
	workspaceEnvVars []corev1.EnvVar,
) ([]corev1.Container, error) {
	agentUID := AgentUID

	// Inject the session runner binary into the shared volume.
	initContainers := []corev1.Container{
		{
			Name:            "inject-session-runner",
			Image:           b.SessionRunnerImage,
			ImagePullPolicy: b.SessionRunnerImagePullPolicy,
			Command:         []string{"/kelos-session-runner", "--self-copy", SessionRunnerMountPath + "/kelos-session-runner"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: SessionRunnerVolumeName, MountPath: SessionRunnerMountPath},
			},
			SecurityContext: &corev1.SecurityContext{RunAsUser: &agentUID},
		},
	}

	// Git clone — skip if workspace already exists on PVC (pod restart).
	if workspace != nil {
		gitClone := buildGitCloneInitContainer(workspace, workspaceEnvVars)
		gitClone = wrapInitContainerWithExistsCheck(gitClone, WorkspaceMountPath+"/repo/.git")
		initContainers = append(initContainers, gitClone)

		effectiveRemotes := effectiveWorkspaceRemotes(workspace)
		if c := buildRemoteSetupInitContainer(effectiveRemotes); c != nil {
			initContainers = append(initContainers, *c)
		}

		if c, err := buildWorkspaceFilesInitContainer(workspace.Files); err != nil {
			return nil, err
		} else if c != nil {
			initContainers = append(initContainers, *c)
		}
	}

	if agentConfig != nil {
		if c, err := buildPluginSetupInitContainer(agentConfig.Plugins); err != nil {
			return nil, err
		} else if c != nil {
			initContainers = append(initContainers, *c)
		}

		if c, err := buildSkillsInstallInitContainer(agentConfig.Skills, agentType); err != nil {
			return nil, err
		} else if c != nil {
			initContainers = append(initContainers, *c)
		}
	}

	return initContainers, nil
}

// buildVolumes creates the non-PVC volumes for session pods.
func (b *SessionStatefulSetBuilder) buildVolumes(agentConfig *kelosv1alpha1.AgentConfigSpec) []corev1.Volume {
	// Session runner binary volume.
	volumes := []corev1.Volume{
		{
			Name:         SessionRunnerVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}

	// Plugin volume (if needed).
	needsPluginVolume := agentConfig != nil && (len(agentConfig.Plugins) > 0 || len(agentConfig.Skills) > 0)
	if needsPluginVolume {
		volumes = append(volumes, corev1.Volume{
			Name:         PluginVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}

	return volumes
}

// resolveAgentImage returns the container image and pull policy for the agent type.
func (b *SessionStatefulSetBuilder) resolveAgentImage(tmpl *kelosv1alpha1.TaskTemplate) (string, corev1.PullPolicy) {
	var image string
	var policy corev1.PullPolicy
	switch tmpl.Type {
	case AgentTypeClaudeCode:
		image, policy = b.ClaudeCodeImage, b.ClaudeCodeImagePullPolicy
	case AgentTypeCodex:
		image, policy = b.CodexImage, b.CodexImagePullPolicy
	case AgentTypeGemini:
		image, policy = b.GeminiImage, b.GeminiImagePullPolicy
	case AgentTypeOpenCode:
		image, policy = b.OpenCodeImage, b.OpenCodeImagePullPolicy
	case AgentTypeCursor:
		image, policy = b.CursorImage, b.CursorImagePullPolicy
	default:
		image, policy = b.ClaudeCodeImage, b.ClaudeCodeImagePullPolicy
	}
	if tmpl.Image != "" {
		image = tmpl.Image
	}
	return image, policy
}

// wrapInitContainerWithExistsCheck wraps an init container so it skips
// execution when the given path already exists on a persistent volume.
// Handles two forms from buildGitCloneInitContainer:
//   - No SecretRef: Command=nil, Args=[clone args] (image entrypoint is git)
//   - With SecretRef: Command=["sh","-c","<script>"], Args=["--", clone args...]
func wrapInitContainerWithExistsCheck(c corev1.Container, checkPath string) corev1.Container {
	skipPrefix := fmt.Sprintf("if [ -d '%s' ]; then echo 'Workspace already exists, skipping clone'; exit 0; fi; ", checkPath)

	if len(c.Command) > 0 && c.Command[0] == "sh" {
		// SecretRef form: Command=["sh","-c","<script>"], Args=["--", ...].
		// Prepend the skip check to the existing script; keep Args intact.
		if len(c.Command) >= 3 {
			c.Command[2] = skipPrefix + c.Command[2]
		}
	} else {
		// No SecretRef form: Command=nil, Args=[clone args].
		// The image entrypoint is "git", so we need exec git "$@".
		c.Command = []string{"sh", "-c", skipPrefix + `exec git "$@"`, "--"}
	}
	return c
}

// sessionStatefulSetName returns the name of the StatefulSet for a TaskSpawner.
// The name is truncated to the Kubernetes 253-character DNS subdomain limit.
func sessionStatefulSetName(spawnerName string) string {
	return truncateResourceName("session-" + spawnerName)
}
