package controller

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/gjkim/axon/api/v1alpha1"
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
)

// JobBuilder constructs Kubernetes Jobs for Tasks.
type JobBuilder struct{}

// NewJobBuilder creates a new JobBuilder.
func NewJobBuilder() *JobBuilder {
	return &JobBuilder{}
}

// Build creates a Job for the given Task.
func (b *JobBuilder) Build(task *axonv1alpha1.Task) (*batchv1.Job, error) {
	switch task.Spec.Type {
	case AgentTypeClaudeCode:
		return b.buildClaudeCodeJob(task)
	default:
		return nil, fmt.Errorf("unsupported agent type: %s", task.Spec.Type)
	}
}

// buildClaudeCodeJob creates a Job for Claude Code agent.
func (b *JobBuilder) buildClaudeCodeJob(task *axonv1alpha1.Task) (*batchv1.Job, error) {
	args := []string{
		"--dangerously-skip-permissions",
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

	backoffLimit := int32(0)

	mainContainer := corev1.Container{
		Name:  "claude-code",
		Image: ClaudeCodeImage,
		Args:  args,
		Env:   envVars,
	}

	var initContainers []corev1.Container
	var volumes []corev1.Volume

	if task.Spec.Workspace != nil {
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
		if task.Spec.Workspace.Ref != "" {
			cloneArgs = append(cloneArgs, "--branch", task.Spec.Workspace.Ref)
		}
		cloneArgs = append(cloneArgs, "--single-branch", "--depth", "1", "--", task.Spec.Workspace.Repo, WorkspaceMountPath+"/repo")

		initContainers = append(initContainers, corev1.Container{
			Name:         "git-clone",
			Image:        GitCloneImage,
			Args:         cloneArgs,
			VolumeMounts: []corev1.VolumeMount{volumeMount},
		})

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
					RestartPolicy:  corev1.RestartPolicyNever,
					InitContainers: initContainers,
					Volumes:        volumes,
					Containers:     []corev1.Container{mainContainer},
				},
			},
		},
	}

	return job, nil
}
