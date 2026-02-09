package controller

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

const (
	// ClaudeCodeImage is the default image for Claude Code agent.
	ClaudeCodeImage = "gjkim42/claude-code:latest"

	// CodexImage is the default image for OpenAI Codex agent.
	CodexImage = "gjkim42/codex:latest"

	// AgentTypeClaudeCode is the agent type for Claude Code.
	AgentTypeClaudeCode = "claude-code"

	// AgentTypeCodex is the agent type for OpenAI Codex.
	AgentTypeCodex = "codex"

	// GitCloneImage is the image used for cloning git repositories.
	GitCloneImage = "alpine/git:v2.47.2"

	// WorkspaceVolumeName is the name of the workspace volume.
	WorkspaceVolumeName = "workspace"

	// WorkspaceMountPath is the mount path for the workspace volume.
	WorkspaceMountPath = "/workspace"

	// AgentUID is the UID shared between the git-clone init
	// container and the agent container. Custom agent images must run
	// as this UID so that both containers can read and write the
	// workspace. This must be kept in sync with agent Dockerfiles.
	AgentUID = int64(61100)

	// ClaudeCodeUID is an alias for AgentUID for backward compatibility.
	ClaudeCodeUID = AgentUID
)

// JobBuilder constructs Kubernetes Jobs for Tasks.
type JobBuilder struct {
	ClaudeCodeImage           string
	ClaudeCodeImagePullPolicy corev1.PullPolicy
	CodexImage                string
	CodexImagePullPolicy      corev1.PullPolicy
}

// NewJobBuilder creates a new JobBuilder.
func NewJobBuilder() *JobBuilder {
	return &JobBuilder{
		ClaudeCodeImage: ClaudeCodeImage,
		CodexImage:      CodexImage,
	}
}

// Build creates a Job for the given Task.
func (b *JobBuilder) Build(task *axonv1alpha1.Task, workspace *axonv1alpha1.WorkspaceSpec) (*batchv1.Job, error) {
	switch task.Spec.Type {
	case AgentTypeClaudeCode:
		return b.buildAgentJob(task, workspace, b.ClaudeCodeImage, b.ClaudeCodeImagePullPolicy)
	case AgentTypeCodex:
		return b.buildAgentJob(task, workspace, b.CodexImage, b.CodexImagePullPolicy)
	default:
		return nil, fmt.Errorf("unsupported agent type: %s", task.Spec.Type)
	}
}

// apiKeyEnvVar returns the environment variable name used for API key
// credentials for the given agent type.
func apiKeyEnvVar(agentType string) string {
	switch agentType {
	case AgentTypeCodex:
		// CODEX_API_KEY is the environment variable that codex exec reads
		// for non-interactive authentication.
		return "CODEX_API_KEY"
	default:
		return "ANTHROPIC_API_KEY"
	}
}

// oauthEnvVar returns the environment variable name used for OAuth
// credentials for the given agent type.
func oauthEnvVar(agentType string) string {
	switch agentType {
	case AgentTypeCodex:
		return "CODEX_API_KEY"
	default:
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
}

// buildAgentJob creates a Job for the given agent type.
func (b *JobBuilder) buildAgentJob(task *axonv1alpha1.Task, workspace *axonv1alpha1.WorkspaceSpec, defaultImage string, pullPolicy corev1.PullPolicy) (*batchv1.Job, error) {
	image := defaultImage
	if task.Spec.Image != "" {
		image = task.Spec.Image
	}

	var envVars []corev1.EnvVar

	// Set AXON_MODEL for all agent containers.
	if task.Spec.Model != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "AXON_MODEL",
			Value: task.Spec.Model,
		})
	}

	switch task.Spec.Credentials.Type {
	case axonv1alpha1.CredentialTypeAPIKey:
		keyName := apiKeyEnvVar(task.Spec.Type)
		envVars = append(envVars, corev1.EnvVar{
			Name: keyName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: task.Spec.Credentials.SecretRef.Name,
					},
					Key: keyName,
				},
			},
		})
	case axonv1alpha1.CredentialTypeOAuth:
		tokenName := oauthEnvVar(task.Spec.Type)
		envVars = append(envVars, corev1.EnvVar{
			Name: tokenName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: task.Spec.Credentials.SecretRef.Name,
					},
					Key: tokenName,
				},
			},
		})
	}

	var workspaceEnvVars []corev1.EnvVar
	var isEnterprise bool
	if workspace != nil {
		host, _, _ := parseGitHubRepo(workspace.Repo)
		isEnterprise = host != "" && host != "github.com"

		if isEnterprise {
			// Set GH_HOST for GitHub Enterprise so that gh CLI targets the correct host.
			ghHostEnv := corev1.EnvVar{Name: "GH_HOST", Value: host}
			envVars = append(envVars, ghHostEnv)
			workspaceEnvVars = append(workspaceEnvVars, ghHostEnv)
		}
	}

	if workspace != nil && workspace.SecretRef != nil {
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

		// gh CLI uses GH_TOKEN for github.com and GH_ENTERPRISE_TOKEN for
		// GitHub Enterprise Server hosts.
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
	}

	backoffLimit := int32(0)
	agentUID := AgentUID

	mainContainer := corev1.Container{
		Name:            task.Spec.Type,
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"/axon_entrypoint.sh"},
		Args:            []string{task.Spec.Prompt},
		Env:             envVars,
	}

	var initContainers []corev1.Container
	var volumes []corev1.Volume
	var podSecurityContext *corev1.PodSecurityContext

	if workspace != nil {
		podSecurityContext = &corev1.PodSecurityContext{
			FSGroup: &agentUID,
		}

		volume := corev1.Volume{
			Name: WorkspaceVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}
		volumes = append(volumes, volume)

		volumeMount := corev1.VolumeMount{
			Name:      WorkspaceVolumeName,
			MountPath: WorkspaceMountPath,
		}

		cloneArgs := []string{"clone"}
		if workspace.Ref != "" {
			cloneArgs = append(cloneArgs, "--branch", workspace.Ref)
		}
		cloneArgs = append(cloneArgs, "--no-single-branch", "--depth", "1", "--", workspace.Repo, WorkspaceMountPath+"/repo")

		initContainer := corev1.Container{
			Name:         "git-clone",
			Image:        GitCloneImage,
			Args:         cloneArgs,
			Env:          workspaceEnvVars,
			VolumeMounts: []corev1.VolumeMount{volumeMount},
			SecurityContext: &corev1.SecurityContext{
				RunAsUser: &agentUID,
			},
		}

		if workspace.SecretRef != nil {
			initContainer.Command = []string{"sh", "-c",
				`exec git -c credential.helper='!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f' "$@"`,
			}
			initContainer.Args = append([]string{"--"}, cloneArgs...)
		}

		initContainers = append(initContainers, initContainer)

		mainContainer.VolumeMounts = []corev1.VolumeMount{volumeMount}
		mainContainer.WorkingDir = WorkspaceMountPath + "/repo"
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      task.Name,
			Namespace: task.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "axon",
				"app.kubernetes.io/component":  "task",
				"app.kubernetes.io/managed-by": "axon-controller",
				"axon.io/task":                 task.Name,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name":       "axon",
						"app.kubernetes.io/component":  "task",
						"app.kubernetes.io/managed-by": "axon-controller",
						"axon.io/task":                 task.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:   corev1.RestartPolicyNever,
					SecurityContext: podSecurityContext,
					InitContainers:  initContainers,
					Volumes:         volumes,
					Containers:      []corev1.Container{mainContainer},
				},
			},
		},
	}

	return job, nil
}
