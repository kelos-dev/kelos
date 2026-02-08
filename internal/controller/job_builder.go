package controller

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

const (
	// ClaudeCodeImage is the default image for Claude Code agent.
	ClaudeCodeImage = "gjkim42/claude-code:latest"

	// AgentTypeClaudeCode is the agent type for Claude Code.
	AgentTypeClaudeCode = "claude-code"

	// GitCloneImage is the image used for cloning git repositories.
	GitCloneImage = "alpine/git:v2.47.2"

	// WorkspaceVolumeName is the name of the workspace volume.
	WorkspaceVolumeName = "workspace"

	// WorkspaceMountPath is the mount path for the workspace volume.
	WorkspaceMountPath = "/workspace"

	// ClaudeCodeUID is the UID of the claude user in the claude-code
	// container image (claude-code/Dockerfile). This must be kept in sync
	// with the Dockerfile.
	ClaudeCodeUID = int64(1100)

	// MCPConfigVolumeName is the name of the volume for MCP config.
	MCPConfigVolumeName = "mcp-config"

	// MCPConfigMountPath is the mount path for the MCP config volume.
	MCPConfigMountPath = "/home/claude/.mcp"

	// MCPConfigFilePath is the full path to the MCP config file.
	MCPConfigFilePath = MCPConfigMountPath + "/mcp.json"

	// MCPInitImage is the image used by the MCP config init container.
	MCPInitImage = "busybox:1.37"
)

// JobBuilder constructs Kubernetes Jobs for Tasks.
type JobBuilder struct {
	ClaudeCodeImage           string
	ClaudeCodeImagePullPolicy corev1.PullPolicy
}

// NewJobBuilder creates a new JobBuilder.
func NewJobBuilder() *JobBuilder {
	return &JobBuilder{ClaudeCodeImage: ClaudeCodeImage}
}

// Build creates a Job for the given Task.
func (b *JobBuilder) Build(task *axonv1alpha1.Task, workspace *axonv1alpha1.WorkspaceSpec) (*batchv1.Job, error) {
	switch task.Spec.Type {
	case AgentTypeClaudeCode:
		return b.buildClaudeCodeJob(task, workspace)
	default:
		return nil, fmt.Errorf("unsupported agent type: %s", task.Spec.Type)
	}
}

// buildClaudeCodeJob creates a Job for Claude Code agent.
func (b *JobBuilder) buildClaudeCodeJob(task *axonv1alpha1.Task, workspace *axonv1alpha1.WorkspaceSpec) (*batchv1.Job, error) {
	args := []string{
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"-p", task.Spec.Prompt,
	}

	if task.Spec.Model != "" {
		args = append(args, "--model", task.Spec.Model)
	}

	var envVars []corev1.EnvVar

	switch task.Spec.Credentials.Type {
	case axonv1alpha1.CredentialTypeAPIKey:
		envVars = append(envVars, corev1.EnvVar{
			Name: "ANTHROPIC_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: task.Spec.Credentials.SecretRef.Name,
					},
					Key: "ANTHROPIC_API_KEY",
				},
			},
		})
	case axonv1alpha1.CredentialTypeOAuth:
		envVars = append(envVars, corev1.EnvVar{
			Name: "CLAUDE_CODE_OAUTH_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: task.Spec.Credentials.SecretRef.Name,
					},
					Key: "CLAUDE_CODE_OAUTH_TOKEN",
				},
			},
		})
	}

	var workspaceEnvVars []corev1.EnvVar
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
		ghTokenEnv := corev1.EnvVar{
			Name:      "GH_TOKEN",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeyRef},
		}
		envVars = append(envVars, githubTokenEnv, ghTokenEnv)
		workspaceEnvVars = append(workspaceEnvVars, githubTokenEnv, ghTokenEnv)
	}

	backoffLimit := int32(0)
	claudeCodeUID := ClaudeCodeUID

	var initContainers []corev1.Container
	var volumes []corev1.Volume
	var mainVolumeMounts []corev1.VolumeMount
	var podSecurityContext *corev1.PodSecurityContext

	if len(task.Spec.MCPServers) > 0 {
		mcpConfig, err := buildMCPConfig(task.Spec.MCPServers)
		if err != nil {
			return nil, fmt.Errorf("building MCP config: %w", err)
		}

		mcpConfigB64 := base64.StdEncoding.EncodeToString([]byte(mcpConfig))

		mcpVolume := corev1.Volume{
			Name: MCPConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}
		volumes = append(volumes, mcpVolume)

		mcpVolumeMount := corev1.VolumeMount{
			Name:      MCPConfigVolumeName,
			MountPath: MCPConfigMountPath,
		}

		mcpInitContainer := corev1.Container{
			Name:         "mcp-config",
			Image:        MCPInitImage,
			Command:      []string{"sh", "-c", fmt.Sprintf("echo '%s' | base64 -d > %s", mcpConfigB64, MCPConfigFilePath)},
			VolumeMounts: []corev1.VolumeMount{mcpVolumeMount},
			SecurityContext: &corev1.SecurityContext{
				RunAsUser: &claudeCodeUID,
			},
		}
		initContainers = append(initContainers, mcpInitContainer)

		mainVolumeMounts = append(mainVolumeMounts, mcpVolumeMount)
		args = append(args, "--mcp-config", MCPConfigFilePath)
	}

	if workspace != nil {
		podSecurityContext = &corev1.PodSecurityContext{
			FSGroup: &claudeCodeUID,
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
				RunAsUser: &claudeCodeUID,
			},
		}

		if workspace.SecretRef != nil {
			initContainer.Command = []string{"sh", "-c",
				`exec git -c credential.helper='!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f' "$@"`,
			}
			initContainer.Args = append([]string{"--"}, cloneArgs...)
		}

		initContainers = append(initContainers, initContainer)

		mainVolumeMounts = append(mainVolumeMounts, volumeMount)
	}

	var workingDir string
	if workspace != nil {
		workingDir = WorkspaceMountPath + "/repo"
	}

	mainContainer := corev1.Container{
		Name:            "claude-code",
		Image:           b.ClaudeCodeImage,
		ImagePullPolicy: b.ClaudeCodeImagePullPolicy,
		Args:            args,
		Env:             envVars,
		VolumeMounts:    mainVolumeMounts,
		WorkingDir:      workingDir,
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

// mcpConfigEntry represents a single MCP server entry in the Claude Code MCP config format.
type mcpConfigEntry struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Type    string            `json:"type,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// buildMCPConfig generates a Claude Code MCP config JSON string from a list of MCPServer specs.
func buildMCPConfig(servers []axonv1alpha1.MCPServer) (string, error) {
	mcpServers := make(map[string]mcpConfigEntry, len(servers))

	for _, s := range servers {
		if _, exists := mcpServers[s.Name]; exists {
			return "", fmt.Errorf("duplicate MCP server name: %s", s.Name)
		}

		entry := mcpConfigEntry{}

		switch s.Transport {
		case axonv1alpha1.MCPTransportStdio:
			entry.Command = s.Target
			entry.Args = s.Args
		case axonv1alpha1.MCPTransportHTTP:
			entry.Type = "http"
			entry.URL = s.Target
		case axonv1alpha1.MCPTransportSSE:
			entry.Type = "sse"
			entry.URL = s.Target
		default:
			return "", fmt.Errorf("unsupported MCP transport type: %s", s.Transport)
		}

		if len(s.Env) > 0 {
			entry.Env = s.Env
		}
		if len(s.Headers) > 0 {
			entry.Headers = s.Headers
		}

		mcpServers[s.Name] = entry
	}

	config := map[string]interface{}{
		"mcpServers": mcpServers,
	}

	data, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("marshaling MCP config: %w", err)
	}

	return string(data), nil
}
