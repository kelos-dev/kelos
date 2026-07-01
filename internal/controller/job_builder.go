package controller

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	// ClaudeCodeImage is the default image for Claude Code agent.
	ClaudeCodeImage = "ghcr.io/kelos-dev/claude-code:latest"

	// CodexImage is the default image for OpenAI Codex agent.
	CodexImage = "ghcr.io/kelos-dev/codex:latest"

	// GeminiImage is the default image for Google Gemini CLI agent.
	GeminiImage = "ghcr.io/kelos-dev/gemini:latest"

	// OpenCodeImage is the default image for OpenCode agent.
	OpenCodeImage = "ghcr.io/kelos-dev/opencode:latest"

	// CursorImage is the default image for Cursor CLI agent.
	CursorImage = "ghcr.io/kelos-dev/cursor:latest"

	// AgentTypeClaudeCode is the agent type for Claude Code.
	AgentTypeClaudeCode = "claude-code"

	// AgentTypeCodex is the agent type for OpenAI Codex.
	AgentTypeCodex = "codex"

	// AgentTypeGemini is the agent type for Google Gemini CLI.
	AgentTypeGemini = "gemini"

	// AgentTypeOpenCode is the agent type for OpenCode.
	AgentTypeOpenCode = "opencode"

	// AgentTypeCursor is the agent type for Cursor CLI.
	AgentTypeCursor = "cursor"

	// GitCloneImage is the image used for cloning git repositories.
	GitCloneImage = "alpine/git:v2.47.2"

	// WorkspaceVolumeName is the name of the workspace volume.
	WorkspaceVolumeName = "workspace"

	// WorkspaceMountPath is the mount path for the workspace volume.
	WorkspaceMountPath = "/workspace"

	// PluginVolumeName is the name of the plugin volume.
	PluginVolumeName = "kelos-plugin"

	// PluginMountPath is the mount path for the plugin volume.
	PluginMountPath = "/kelos/plugin"

	// PluginStagingVolumeName is the name of the read-only volume that
	// stages plugin content from the per-task plugin ConfigMap into the
	// plugin-setup init container.
	PluginStagingVolumeName = "kelos-plugin-src"

	// PluginStagingMountPath is the mount path for the plugin staging
	// volume inside the plugin-setup init container.
	PluginStagingMountPath = "/kelos-plugin-src"

	// SkillsShPluginName is the plugin directory name under PluginMountPath
	// that skills.sh packages are installed into, so agent entrypoints
	// discover them the same way as inline plugins.
	SkillsShPluginName = "skills-sh"

	// NodeImage is the image used for running Node.js-based init containers
	// (e.g., installing skills.sh packages).
	NodeImage = "node:22.14.0-alpine"

	// GHConfigDir is the directory used for gh CLI configuration when
	// workspace auth is enabled. It is placed on the shared workspace
	// volume so that gh does not read stale auth from the container
	// image's home directory.
	GHConfigDir = WorkspaceMountPath + "/.gh-config"

	// GitHubTokenVolumeName is the name of the volume that mounts the
	// workspace token Secret into the agent and init containers. The
	// kubelet auto-syncs Secret volume contents when the underlying
	// Secret is updated, which lets the controller refresh the token
	// in-place without restarting the pod.
	GitHubTokenVolumeName = "kelos-github-token"

	// GitHubTokenMountPath is the directory where the workspace token
	// Secret is mounted. The token is read from the file named
	// GitHubTokenSecretKey under this directory.
	GitHubTokenMountPath = "/kelos/github-token"

	// GitHubTokenSecretKey is the Secret key under which the GitHub
	// token is stored. Mounted as a file at
	// GitHubTokenMountPath + "/" + GitHubTokenSecretKey.
	GitHubTokenSecretKey = "GITHUB_TOKEN"

	// AgentUID is the UID shared between the git-clone init
	// container and the agent container. Custom agent images must run
	// as this UID so that both containers can read and write the
	// workspace. This must be kept in sync with agent Dockerfiles.
	AgentUID = int64(61100)

	// ClaudeCodeUID is an alias for AgentUID for backward compatibility.
	ClaudeCodeUID = AgentUID
)

// reservedEnvNames lists env var names that PodOverrides.Env must never
// supply, even when the controller does not populate them on this Job.
// These names drive entrypoint behavior the user is not allowed to forge,
// because doing so would amount to executing arbitrary commands or code
// inside the agent container before the user-supplied agent process runs.
var reservedEnvNames = map[string]struct{}{
	"KELOS_SETUP_COMMAND": {},
}

// JobBuilder constructs Kubernetes Jobs for Tasks.
type JobBuilder struct {
	ClaudeCodeImage           string
	ClaudeCodeImagePullPolicy corev1.PullPolicy
	CodexImage                string
	CodexImagePullPolicy      corev1.PullPolicy
	GeminiImage               string
	GeminiImagePullPolicy     corev1.PullPolicy
	OpenCodeImage             string
	OpenCodeImagePullPolicy   corev1.PullPolicy
	CursorImage               string
	CursorImagePullPolicy     corev1.PullPolicy
}

// NewJobBuilder creates a new JobBuilder.
func NewJobBuilder() *JobBuilder {
	return &JobBuilder{
		ClaudeCodeImage: ClaudeCodeImage,
		CodexImage:      CodexImage,
		GeminiImage:     GeminiImage,
		OpenCodeImage:   OpenCodeImage,
		CursorImage:     CursorImage,
	}
}

// resolveTaskType returns the effective agent type for a Task, preferring
// spec.worker.type over the legacy spec.type field.
func resolveTaskType(task *kelos.Task) string {
	if task.Spec.Worker != nil && task.Spec.Worker.Type != "" {
		return task.Spec.Worker.Type
	}
	return task.Spec.Type
}

// resolveTaskWorkspaceRef returns the effective workspace reference for a Task,
// preferring spec.worker.workspaceRef over the legacy spec.workspaceRef field.
func resolveTaskWorkspaceRef(task *kelos.Task) *kelos.WorkspaceReference {
	if task.Spec.Worker != nil && task.Spec.Worker.WorkspaceRef != nil {
		return task.Spec.Worker.WorkspaceRef
	}
	return task.Spec.WorkspaceRef
}

// resolveTaskPodOverrides returns the effective pod overrides for a Task,
// preferring spec.worker.podOverrides over the legacy spec.podOverrides field.
func resolveTaskPodOverrides(task *kelos.Task) *kelos.PodOverrides {
	if task.Spec.Worker != nil && task.Spec.Worker.PodOverrides != nil {
		return task.Spec.Worker.PodOverrides
	}
	return task.Spec.PodOverrides
}

// resolveTaskCredentials returns the effective credentials for a Task,
// preferring spec.worker.credentials over the legacy spec.credentials field.
func resolveTaskCredentials(task *kelos.Task) *kelos.Credentials {
	if task.Spec.Worker != nil && task.Spec.Worker.Credentials != nil {
		return task.Spec.Worker.Credentials
	}
	return task.Spec.Credentials
}

// resolveTaskModel returns the effective model for a Task,
// preferring spec.worker.model over the legacy spec.model field.
func resolveTaskModel(task *kelos.Task) string {
	if task.Spec.Worker != nil && task.Spec.Worker.Model != "" {
		return task.Spec.Worker.Model
	}
	return task.Spec.Model
}

// resolveTaskEffort returns the effective effort for a Task,
// preferring spec.worker.effort over the legacy spec.effort field.
func resolveTaskEffort(task *kelos.Task) string {
	if task.Spec.Worker != nil && task.Spec.Worker.Effort != "" {
		return task.Spec.Worker.Effort
	}
	return task.Spec.Effort
}

// resolveTaskImage returns the effective image override for a Task,
// preferring spec.worker.image over the legacy spec.image field.
func resolveTaskImage(task *kelos.Task) string {
	if task.Spec.Worker != nil && task.Spec.Worker.Image != "" {
		return task.Spec.Worker.Image
	}
	return task.Spec.Image
}

// Build creates a Job for the given Task. The prompt parameter is the
// resolved prompt text (which may have been expanded from a template).
func (b *JobBuilder) Build(task *kelos.Task, workspace *kelos.WorkspaceSpec, agentConfig *kelos.AgentConfigSpec, prompt string) (*batchv1.Job, error) {
	agentType := resolveTaskType(task)
	switch agentType {
	case AgentTypeClaudeCode:
		return b.buildAgentJob(task, workspace, agentConfig, b.ClaudeCodeImage, b.ClaudeCodeImagePullPolicy, prompt)
	case AgentTypeCodex:
		return b.buildAgentJob(task, workspace, agentConfig, b.CodexImage, b.CodexImagePullPolicy, prompt)
	case AgentTypeGemini:
		return b.buildAgentJob(task, workspace, agentConfig, b.GeminiImage, b.GeminiImagePullPolicy, prompt)
	case AgentTypeOpenCode:
		return b.buildAgentJob(task, workspace, agentConfig, b.OpenCodeImage, b.OpenCodeImagePullPolicy, prompt)
	case AgentTypeCursor:
		return b.buildAgentJob(task, workspace, agentConfig, b.CursorImage, b.CursorImagePullPolicy, prompt)
	default:
		return nil, fmt.Errorf("unsupported agent type: %s", agentType)
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
	case AgentTypeGemini:
		// GEMINI_API_KEY is the environment variable that the gemini CLI
		// reads for API key authentication.
		return "GEMINI_API_KEY"
	case AgentTypeOpenCode:
		// OPENCODE_API_KEY is the environment variable that the opencode
		// entrypoint reads for API key authentication.
		return "OPENCODE_API_KEY"
	case AgentTypeCursor:
		// CURSOR_API_KEY is the environment variable that the cursor
		// entrypoint reads for API key authentication.
		return "CURSOR_API_KEY"
	default:
		return "ANTHROPIC_API_KEY"
	}
}

// oauthEnvVar returns the environment variable name used for OAuth
// credentials for the given agent type.
func oauthEnvVar(agentType string) string {
	switch agentType {
	case AgentTypeCodex:
		return "CODEX_AUTH_JSON"
	case AgentTypeGemini:
		return "GEMINI_API_KEY"
	case AgentTypeOpenCode:
		return "OPENCODE_API_KEY"
	case AgentTypeCursor:
		// Cursor uses the same CURSOR_API_KEY for both API key and
		// OAuth credential flows.
		return "CURSOR_API_KEY"
	default:
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
}

// credentialEnvVars returns the environment variables to inject for the given
// credentials and agent type. This centralises all credential-type-specific
// logic so that new providers (e.g. Vertex) only need to add a case here.
func credentialEnvVars(creds kelos.Credentials, agentType string) []corev1.EnvVar {
	secretName := ""
	if creds.SecretRef != nil {
		secretName = creds.SecretRef.Name
	}

	secretEnvRef := func(key string, optional bool) corev1.EnvVar {
		sel := &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
		}
		if optional {
			sel.Optional = ptr.To(true)
		}
		return corev1.EnvVar{
			Name:      key,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: sel},
		}
	}

	switch creds.Type {
	case kelos.CredentialTypeAPIKey:
		keyName := apiKeyEnvVar(agentType)
		return []corev1.EnvVar{secretEnvRef(keyName, false)}

	case kelos.CredentialTypeOAuth:
		tokenName := oauthEnvVar(agentType)
		return []corev1.EnvVar{secretEnvRef(tokenName, false)}

	case kelos.CredentialTypeNone:
		// No built-in credential injection; users supply their own
		// credentials via PodOverrides.Env.
		return nil

	default:
		return nil
	}
}

func effectiveWorkspaceRemotes(workspace *kelos.WorkspaceSpec) []kelos.GitRemote {
	if workspace == nil {
		return nil
	}
	return append([]kelos.GitRemote(nil), workspace.Remotes...)
}

func upstreamRepoEnvValue(remotes []kelos.GitRemote) string {
	for _, remote := range remotes {
		if remote.Name != "upstream" {
			continue
		}
		_, upstreamOwner, upstreamRepo := parseGitHubRepo(remote.URL)
		if upstreamOwner != "" && upstreamRepo != "" {
			return fmt.Sprintf("%s/%s", upstreamOwner, upstreamRepo)
		}
	}
	return ""
}

// buildAgentJob creates a Job for the given agent type.
func (b *JobBuilder) buildAgentJob(task *kelos.Task, workspace *kelos.WorkspaceSpec, agentConfig *kelos.AgentConfigSpec, defaultImage string, pullPolicy corev1.PullPolicy, prompt string) (*batchv1.Job, error) {
	image := defaultImage
	if img := resolveTaskImage(task); img != "" {
		image = img
	}

	agentType := resolveTaskType(task)
	var envVars []corev1.EnvVar

	if model := resolveTaskModel(task); model != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_MODEL",
			Value: model,
		})
	}

	if effort := resolveTaskEffort(task); effort != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_EFFORT",
			Value: effort,
		})
	}

	if task.Spec.Branch != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_BRANCH",
			Value: task.Spec.Branch,
		})
	}

	envVars = append(envVars, corev1.EnvVar{
		Name:  "KELOS_AGENT_TYPE",
		Value: agentType,
	})

	if spawner := task.Labels["kelos.dev/taskspawner"]; spawner != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_TASKSPAWNER",
			Value: spawner,
		})
	}

	if creds := resolveTaskCredentials(task); creds != nil {
		credEnvVars := credentialEnvVars(*creds, agentType)
		envVars = append(envVars, credEnvVars...)
	}

	var workspaceEnvVars []corev1.EnvVar
	var isEnterprise bool
	effectiveRemotes := effectiveWorkspaceRemotes(workspace)
	if workspace != nil {
		host, _, _ := parseGitHubRepo(workspace.Repo)
		isEnterprise = host != "" && host != "github.com"

		if isEnterprise {
			// Set GH_HOST for GitHub Enterprise so that gh CLI targets the correct host.
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

		// Derive upstream repo: prefer the explicit task-level override
		// (set by the spawner from githubIssues.repo / githubPullRequests.repo),
		// then fall back to parsing workspace remotes named "upstream".
		upstreamRepo := task.Spec.UpstreamRepo
		if upstreamRepo == "" {
			upstreamRepo = upstreamRepoEnvValue(effectiveRemotes)
		}
		if upstreamRepo != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_UPSTREAM_REPO",
				Value: upstreamRepo,
			})
		}
	}

	if workspace != nil && workspace.SecretRef != nil {
		secretKeyRef := &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: workspace.SecretRef.Name,
			},
			Key: GitHubTokenSecretKey,
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

		// Point gh CLI at a clean config directory on the workspace volume
		// so it does not read stale auth from the container image.
		envVars = append(envVars, corev1.EnvVar{
			Name:  "GH_CONFIG_DIR",
			Value: GHConfigDir,
		})

		// Expose the mounted token file path so the git credential
		// helper and the gh wrapper script can re-read the token on
		// every invocation, picking up controller-side refreshes
		// without a pod restart.
		tokenFileEnv := corev1.EnvVar{
			Name:  "KELOS_GITHUB_TOKEN_FILE",
			Value: GitHubTokenMountPath + "/" + GitHubTokenSecretKey,
		}
		envVars = append(envVars, tokenFileEnv)
		workspaceEnvVars = append(workspaceEnvVars, tokenFileEnv)
	}

	backoffLimit := int32(1)
	agentUID := AgentUID

	mainContainer := corev1.Container{
		Name:            kelos.AgentContainerName,
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"/kelos_entrypoint.sh"},
		Args:            []string{prompt},
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

		targetPath := WorkspaceMountPath + "/repo"
		commitRef := isFullGitCommitSHA(workspace.Ref)

		// Workspace volume mounts shared by every container that needs
		// the cloned repo plus, when a token Secret is configured, the
		// auto-syncing token file.
		workspaceVolumeMounts := []corev1.VolumeMount{volumeMount}
		if workspace.SecretRef != nil {
			volumes = append(volumes, corev1.Volume{
				Name: GitHubTokenVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: workspace.SecretRef.Name,
						Items: []corev1.KeyToPath{
							{Key: GitHubTokenSecretKey, Path: GitHubTokenSecretKey},
						},
						Optional: ptr.To(true),
					},
				},
			})
			workspaceVolumeMounts = append(workspaceVolumeMounts, corev1.VolumeMount{
				Name:      GitHubTokenVolumeName,
				MountPath: GitHubTokenMountPath,
				ReadOnly:  true,
			})
		}

		cloneArgs := []string{"clone"}
		if workspace.Ref != "" && !commitRef {
			cloneArgs = append(cloneArgs, "--branch", workspace.Ref)
		}
		cloneArgs = append(cloneArgs, "--no-single-branch", "--depth", "1", "--", workspace.Repo, targetPath)

		initContainer := corev1.Container{
			Name:         "git-clone",
			Image:        GitCloneImage,
			Args:         cloneArgs,
			Env:          workspaceEnvVars,
			VolumeMounts: append([]corev1.VolumeMount(nil), workspaceVolumeMounts...),
			SecurityContext: &corev1.SecurityContext{
				RunAsUser: &agentUID,
			},
		}

		if commitRef {
			credentialHelper := ""
			if workspace.SecretRef != nil {
				credentialHelper = gitCredentialHelper()
			}
			initContainer.Command = []string{"sh", "-c", buildCommitRefCheckoutScript(credentialHelper)}
			initContainer.Args = []string{"--", workspace.Repo, targetPath, workspace.Ref}
		} else if workspace.SecretRef != nil {
			credentialHelper := gitCredentialHelper()
			// Clear inherited credential helpers with an empty -c credential.helper=
			// before setting the workspace helper, then persist the same
			// configuration into the repo so the agent container is
			// independent from global/system helpers.
			initContainer.Command = []string{"sh", "-c",
				fmt.Sprintf(
					`git -c credential.helper= -c credential.helper='%s' "$@" && { `+
						`git -C %s/repo config --unset-all credential.helper 2>/dev/null || true; `+
						`git -C %s/repo config --add credential.helper '%s'; }`,
					credentialHelper, WorkspaceMountPath, WorkspaceMountPath, credentialHelper,
				),
			}
			initContainer.Args = append([]string{"--"}, cloneArgs...)
		}

		initContainers = append(initContainers, initContainer)

		if len(effectiveRemotes) > 0 {
			var parts []string
			parts = append(parts, fmt.Sprintf("cd %s/repo", WorkspaceMountPath))
			for _, r := range effectiveRemotes {
				parts = append(parts,
					fmt.Sprintf(
						"if git remote get-url %s >/dev/null 2>&1; then git remote set-url %s %s; else git remote add %s %s; fi",
						shellQuote(r.Name),
						shellQuote(r.Name),
						shellQuote(r.URL),
						shellQuote(r.Name),
						shellQuote(r.URL),
					),
				)
			}
			remoteSetupContainer := corev1.Container{
				Name:         "remote-setup",
				Image:        GitCloneImage,
				Command:      []string{"sh", "-c", strings.Join(parts, " && ")},
				VolumeMounts: []corev1.VolumeMount{volumeMount},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser: &agentUID,
				},
			}
			initContainers = append(initContainers, remoteSetupContainer)
		}

		if task.Spec.Branch != "" {
			fetchCmd := `git fetch origin "$KELOS_BRANCH":"$KELOS_BRANCH" 2>/dev/null`
			if workspace.SecretRef != nil {
				credHelper := gitCredentialHelper()
				fetchCmd = fmt.Sprintf(`git -c credential.helper= -c credential.helper='%s' fetch origin "$KELOS_BRANCH":"$KELOS_BRANCH" 2>/dev/null`, credHelper)
			}
			branchSetupScript := fmt.Sprintf(
				`cd %s/repo && %s; `+
					`if git rev-parse --verify refs/heads/"$KELOS_BRANCH" >/dev/null 2>&1; then `+
					`git checkout "$KELOS_BRANCH"; `+
					`else git checkout -b "$KELOS_BRANCH"; fi`,
				WorkspaceMountPath, fetchCmd,
			)
			branchEnv := make([]corev1.EnvVar, len(workspaceEnvVars), len(workspaceEnvVars)+1)
			copy(branchEnv, workspaceEnvVars)
			branchEnv = append(branchEnv, corev1.EnvVar{
				Name:  "KELOS_BRANCH",
				Value: task.Spec.Branch,
			})
			branchSetupContainer := corev1.Container{
				Name:         "branch-setup",
				Image:        GitCloneImage,
				Command:      []string{"sh", "-c", branchSetupScript},
				Env:          branchEnv,
				VolumeMounts: append([]corev1.VolumeMount(nil), workspaceVolumeMounts...),
				SecurityContext: &corev1.SecurityContext{
					RunAsUser: &agentUID,
				},
			}
			initContainers = append(initContainers, branchSetupContainer)
		}

		if len(workspace.Files) > 0 {
			injectionScript, err := buildWorkspaceFileInjectionScript(workspace.Files)
			if err != nil {
				return nil, err
			}

			injectionContainer := corev1.Container{
				Name:         "workspace-files",
				Image:        GitCloneImage,
				Command:      []string{"sh", "-c", injectionScript},
				VolumeMounts: []corev1.VolumeMount{volumeMount},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser: &agentUID,
				},
			}
			initContainers = append(initContainers, injectionContainer)
		}

		if len(workspace.SetupCommand) > 0 {
			setupJSON, err := json.Marshal(workspace.SetupCommand)
			if err != nil {
				return nil, fmt.Errorf("marshalling setup command: %w", err)
			}
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_SETUP_COMMAND",
				Value: string(setupJSON),
			})
		}

		mainContainer.VolumeMounts = append([]corev1.VolumeMount(nil), workspaceVolumeMounts...)
		mainContainer.WorkingDir = WorkspaceMountPath + "/repo"
	}

	// Inject AgentConfig: agentsMD env var and plugin volume/init container.
	if agentConfig != nil {
		if agentConfig.AgentsMD != "" {
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_AGENTS_MD",
				Value: agentConfig.AgentsMD,
			})
		}

		needsPluginVolume := len(agentConfig.Plugins) > 0 || len(agentConfig.Skills) > 0
		if needsPluginVolume {
			volumes = append(volumes, corev1.Volume{
				Name:         PluginVolumeName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
			mainContainer.VolumeMounts = append(mainContainer.VolumeMounts,
				corev1.VolumeMount{Name: PluginVolumeName, MountPath: PluginMountPath})
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_PLUGIN_DIR",
				Value: PluginMountPath,
			})
		}

		if len(agentConfig.Plugins) > 0 {
			// Plugin content is delivered through a per-task ConfigMap
			// (created by the Task reconciler) and staged into the
			// plugin-setup init container as a read-only volume. Inlining
			// the content into the init script would hit Linux's 128KiB
			// MAX_ARG_STRLEN limit on a single execve argument for large
			// plugin sets.
			_, items, err := buildPluginConfigMapData(agentConfig.Plugins)
			if err != nil {
				return nil, fmt.Errorf("invalid plugin configuration: %w", err)
			}
			volumes = append(volumes, corev1.Volume{
				Name: PluginStagingVolumeName,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: PluginConfigMapName(task.Name),
						},
						Items: items,
					},
				},
			})
			initContainers = append(initContainers, corev1.Container{
				Name:    "plugin-setup",
				Image:   GitCloneImage,
				Command: []string{"sh", "-c", pluginSetupScript},
				VolumeMounts: []corev1.VolumeMount{
					{Name: PluginVolumeName, MountPath: PluginMountPath},
					{Name: PluginStagingVolumeName, MountPath: PluginStagingMountPath, ReadOnly: true},
				},
				SecurityContext: &corev1.SecurityContext{RunAsUser: &agentUID},
			})
		}

		if len(agentConfig.Skills) > 0 {
			for _, p := range agentConfig.Plugins {
				if p.Name == SkillsShPluginName {
					return nil, fmt.Errorf("invalid plugin configuration: plugin name %q is reserved for skills.sh packages when spec.skills is set", SkillsShPluginName)
				}
			}
			authEnvs := collectSkillsAuthEnvs(agentConfig.Skills)
			script, err := buildSkillsInstallScript(agentConfig.Skills, authEnvs)
			if err != nil {
				return nil, fmt.Errorf("invalid skills configuration: %w", err)
			}
			env := []corev1.EnvVar{{Name: "HOME", Value: PluginMountPath}}
			env = append(env, skillsAuthEnvVars(authEnvs)...)
			initContainers = append(initContainers, corev1.Container{
				Name:    "skills-install",
				Image:   NodeImage,
				Command: []string{"sh", "-c", script},
				Env:     env,
				VolumeMounts: []corev1.VolumeMount{
					{Name: PluginVolumeName, MountPath: PluginMountPath},
				},
			})
		}

		if len(agentConfig.MCPServers) > 0 {
			mcpJSON, err := buildMCPServersJSON(agentConfig.MCPServers)
			if err != nil {
				return nil, fmt.Errorf("invalid MCP server configuration: %w", err)
			}
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_MCP_SERVERS",
				Value: mcpJSON,
			})
		}
	}

	// Apply PodOverrides before constructing the Job so all overrides
	// are reflected in the final spec.
	var serviceAccountName string
	var activeDeadlineSeconds *int64
	var nodeSelector map[string]string
	var extraLabels map[string]string
	var tolerations []corev1.Toleration
	var affinity *corev1.Affinity
	var imagePullSecrets []corev1.LocalObjectReference

	if po := resolveTaskPodOverrides(task); po != nil {
		if po.Labels != nil {
			extraLabels = po.Labels
		}

		if po.Resources != nil {
			mainContainer.Resources = *po.Resources
		}

		if po.ActiveDeadlineSeconds != nil {
			activeDeadlineSeconds = po.ActiveDeadlineSeconds
		}

		if len(po.Env) > 0 {
			// Filter out user env vars that collide with built-in names
			// so that built-in vars always take precedence. Names in
			// reservedEnvNames are dropped unconditionally even when the
			// controller has not populated them on this Job, because they
			// drive entrypoint behavior (e.g. KELOS_SETUP_COMMAND triggers
			// arbitrary command execution before the agent starts).
			builtinNames := make(map[string]struct{}, len(mainContainer.Env))
			for _, e := range mainContainer.Env {
				builtinNames[e.Name] = struct{}{}
			}
			for _, e := range po.Env {
				if _, exists := builtinNames[e.Name]; exists {
					continue
				}
				if _, reserved := reservedEnvNames[e.Name]; reserved {
					continue
				}
				mainContainer.Env = append(mainContainer.Env, e)
			}
		}

		if po.NodeSelector != nil {
			nodeSelector = po.NodeSelector
		}

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
				return nil, err
			}
			volumes = append(volumes, po.Volumes...)
		}

		if len(po.VolumeMounts) > 0 {
			mainContainer.VolumeMounts = append(mainContainer.VolumeMounts, po.VolumeMounts...)
		}

		if po.PodSecurityContext != nil {
			merged := po.PodSecurityContext.DeepCopy()
			// Retain Kelos's default FSGroup so the agent user keeps
			// access to the workspace volume unless the user opts in
			// to a different value explicitly.
			if merged.FSGroup == nil && podSecurityContext != nil && podSecurityContext.FSGroup != nil {
				merged.FSGroup = podSecurityContext.FSGroup
			}
			podSecurityContext = merged
		}

		if po.ContainerSecurityContext != nil {
			mainContainer.SecurityContext = po.ContainerSecurityContext.DeepCopy()
		}
	}

	// Build the final containers list: agent container plus any extra containers.
	containers := []corev1.Container{mainContainer}

	if po := resolveTaskPodOverrides(task); po != nil {
		if len(po.ExtraContainers) > 0 {
			if err := validateExtraContainers(po.ExtraContainers); err != nil {
				return nil, err
			}
			containers = append(containers, po.ExtraContainers...)
		}

		// User-supplied init containers are appended after all Kelos built-in
		// init containers, ensuring the workspace and plugins are ready before
		// they start. Users can set restartPolicy: Always for sidecar semantics
		// (long-running services like databases) or leave it unset for
		// one-shot init tasks (migrations, schema setup, etc.).
		if len(po.ExtraInitContainers) > 0 {
			if err := validateExtraContainers(po.ExtraInitContainers); err != nil {
				return nil, err
			}
			initContainers = append(initContainers, po.ExtraInitContainers...)
		}

		// Check for name collisions across extraContainers and extraInitContainers.
		if len(po.ExtraContainers) > 0 && len(po.ExtraInitContainers) > 0 {
			if err := validateNoContainerNameCollision(po.ExtraContainers, po.ExtraInitContainers); err != nil {
				return nil, err
			}
		}
	}

	if err := validatePodFailurePolicy(task.Spec.PodFailurePolicy); err != nil {
		return nil, err
	}

	builtinLabels := map[string]string{
		"kelos.dev/name":       "kelos",
		"kelos.dev/component":  "task",
		"kelos.dev/managed-by": "kelos-controller",
		"kelos.dev/task":       task.Name,
	}

	// Merge user-specified labels with built-in labels.
	// Built-in labels take precedence over user-specified ones.
	jobLabels := make(map[string]string, len(builtinLabels)+len(extraLabels))
	for k, v := range extraLabels {
		jobLabels[k] = v
	}
	for k, v := range builtinLabels {
		jobLabels[k] = v
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      task.Name,
			Namespace: task.Namespace,
			Labels:    jobLabels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			PodFailurePolicy:      task.Spec.PodFailurePolicy,
			ActiveDeadlineSeconds: activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: jobLabels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					SecurityContext:    podSecurityContext,
					ServiceAccountName: serviceAccountName,
					InitContainers:     initContainers,
					Volumes:            volumes,
					Containers:         containers,
					NodeSelector:       nodeSelector,
					Tolerations:        tolerations,
					Affinity:           affinity,
					ImagePullSecrets:   imagePullSecrets,
				},
			},
		},
	}

	return job, nil
}

func buildWorkspaceFileInjectionScript(files []kelos.WorkspaceFile) (string, error) {
	lines := []string{"set -eu"}

	for _, file := range files {
		relativePath, err := sanitizeWorkspaceFilePath(file.Path)
		if err != nil {
			return "", fmt.Errorf("invalid workspace file path %q: %w", file.Path, err)
		}

		targetPath := WorkspaceMountPath + "/repo/" + relativePath
		contentBase64 := base64.StdEncoding.EncodeToString([]byte(file.Content))

		lines = append(lines,
			"target="+shellQuote(targetPath),
			`mkdir -p "$(dirname "$target")"`,
			fmt.Sprintf("printf '%%s' %s | base64 -d > \"$target\"", shellQuote(contentBase64)),
		)
	}

	return strings.Join(lines, "\n"), nil
}

func sanitizeWorkspaceFilePath(filePath string) (string, error) {
	if strings.TrimSpace(filePath) == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.Contains(filePath, `\`) {
		return "", fmt.Errorf("path must use forward slashes")
	}

	cleanPath := path.Clean(filePath)
	if cleanPath == "." {
		return "", fmt.Errorf("path resolves to current directory")
	}
	if strings.HasPrefix(cleanPath, "/") {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	if cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return "", fmt.Errorf("path escapes repository root")
	}

	return cleanPath, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func validatePodFailurePolicy(policy *batchv1.PodFailurePolicy) error {
	if policy == nil {
		return nil
	}
	for i, rule := range policy.Rules {
		if rule.Action == batchv1.PodFailurePolicyActionFailIndex {
			return fmt.Errorf("podFailurePolicy.rules[%d].action: FailIndex is not supported for Task Jobs", i)
		}
	}
	return nil
}

func isFullGitCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func buildCommitRefCheckoutScript(credentialHelper string) string {
	fetchCmd := `git -C "$target" fetch --depth 1 origin "$ref"`
	if credentialHelper != "" {
		fetchCmd = fmt.Sprintf(`git -C "$target" -c credential.helper= -c credential.helper='%s' fetch --depth 1 origin "$ref"`, credentialHelper)
	}

	lines := []string{
		"set -eu",
		"repo=$1",
		"target=$2",
		"ref=$3",
		`git init "$target"`,
		`git -C "$target" remote add origin "$repo"`,
		fetchCmd,
		`git -C "$target" checkout --detach FETCH_HEAD`,
	}

	if credentialHelper != "" {
		lines = append(lines,
			`git -C "$target" config --unset-all credential.helper 2>/dev/null || true`,
			fmt.Sprintf(`git -C "$target" config --add credential.helper '%s'`, credentialHelper),
		)
	}

	return strings.Join(lines, "\n")
}

// gitCredentialHelper returns the inline git credential helper that resolves
// the GitHub token by reading the mounted token file on each invocation,
// falling back to the inherited $GITHUB_TOKEN env var when the file is not
// present. Reading the file each time lets git pick up controller-side
// token refreshes (e.g. for GitHub App installation tokens that expire
// in ~1h) without restarting the pod.
func gitCredentialHelper() string {
	tokenFile := GitHubTokenMountPath + "/" + GitHubTokenSecretKey
	return fmt.Sprintf(
		`!f() { echo "username=x-access-token"; if [ -r %q ]; then echo "password=$(cat %q)"; else echo "password=$GITHUB_TOKEN"; fi; }; f`,
		tokenFile, tokenFile,
	)
}

// reservedVolumeNames is the set of non-prefixed volume names that Kelos
// manages internally. PodOverrides.Volumes entries must not use these names
// or the Kelos-reserved volume prefix.
var reservedVolumeNames = map[string]struct{}{
	WorkspaceVolumeName: {},
}

// validateUserVolumes ensures no user-supplied volume name collides with
// a Kelos-reserved name or duplicates another user-supplied name.
func validateUserVolumes(volumes []corev1.Volume) error {
	seen := make(map[string]struct{}, len(volumes))
	for _, v := range volumes {
		if strings.HasPrefix(v.Name, kelos.ReservedVolumeNamePrefix) {
			return fmt.Errorf("podOverrides.volumes: %q uses the reserved %q volume name prefix", v.Name, kelos.ReservedVolumeNamePrefix)
		}
		if _, reserved := reservedVolumeNames[v.Name]; reserved {
			return fmt.Errorf("podOverrides.volumes: %q is a Kelos-reserved volume name", v.Name)
		}
		if _, dup := seen[v.Name]; dup {
			return fmt.Errorf("podOverrides.volumes: duplicate volume name %q", v.Name)
		}
		seen[v.Name] = struct{}{}
	}
	return nil
}

// reservedContainerNames is the set of built-in init container names that
// Kelos uses internally. User-supplied extra/init containers must not use
// these names. The agent (main) container is reserved via the
// kelos.ReservedContainerNamePrefix prefix rather than enumerated
// here, so agent-type literals (claude-code, codex, gemini, opencode,
// cursor) are free for user-supplied containers.
var reservedContainerNames = map[string]struct{}{
	"git-clone":       {},
	"remote-setup":    {},
	"branch-setup":    {},
	"workspace-files": {},
	"plugin-setup":    {},
	"skills-install":  {},
}

// validateExtraContainers ensures no user-supplied container name carries the
// reserved kelos- prefix, collides with a Kelos-reserved name, or duplicates
// another container name in the list.
func validateExtraContainers(containers []corev1.Container) error {
	seen := make(map[string]struct{}, len(containers))
	for _, c := range containers {
		if strings.HasPrefix(c.Name, kelos.ReservedContainerNamePrefix) {
			return fmt.Errorf("podOverrides: %q uses the reserved %q container name prefix", c.Name, kelos.ReservedContainerNamePrefix)
		}
		if _, reserved := reservedContainerNames[c.Name]; reserved {
			return fmt.Errorf("podOverrides: %q is a Kelos-reserved container name", c.Name)
		}
		if _, dup := seen[c.Name]; dup {
			return fmt.Errorf("podOverrides: duplicate container name %q", c.Name)
		}
		seen[c.Name] = struct{}{}
	}
	return nil
}

// validateNoContainerNameCollision ensures no container name appears in both
// extraContainers and extraInitContainers. Kubernetes requires all container
// names within a pod to be unique.
func validateNoContainerNameCollision(extra, init []corev1.Container) error {
	names := make(map[string]struct{}, len(extra))
	for _, c := range extra {
		names[c.Name] = struct{}{}
	}
	for _, c := range init {
		if _, exists := names[c.Name]; exists {
			return fmt.Errorf("podOverrides: container name %q appears in both extraContainers and extraInitContainers", c.Name)
		}
	}
	return nil
}

// sanitizeComponentName validates that a plugin, skill, or agent name is safe
// for use as a path component. It rejects empty names, path separators, and
// traversal attempts.
func sanitizeComponentName(name, kind string) error {
	if name == "" {
		return fmt.Errorf("%s name is empty", kind)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("%s name %q contains path separators", kind, name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%s name %q is a path traversal", kind, name)
	}
	return nil
}

// pluginConfigMapMaxBytes caps the total plugin content placed in the
// per-task plugin ConfigMap. Kubernetes rejects objects whose total size
// exceeds 1MiB (the etcd request limit), so we stop a bit short of that to
// leave headroom for keys, metadata, and the API server's JSON envelope.
const pluginConfigMapMaxBytes = 900 * 1024

// pluginSetupScript copies the staged plugin tree from the read-only
// ConfigMap mount into the writable plugin emptyDir volume. The kubelet's
// atomic writer materializes ConfigMap items as symlinks into hidden
// "..data"/"..<timestamp>" directories, so the copy dereferences symlinks
// (-L) and skips the kubelet-internal "..*" entries. The script is a small
// constant: plugin content never appears in the sh -c argument, keeping it
// far below Linux's 128KiB MAX_ARG_STRLEN execve limit. ConfigMap-mounted
// files are world-readable, so a plain copy running as AgentUID produces
// files owned by (and readable as) the agent user.
const pluginSetupScript = "set -eu\n" +
	"find " + PluginStagingMountPath + " -mindepth 1 -maxdepth 1 ! -name '..*' " +
	"-exec cp -RL {} " + PluginMountPath + "/ ';'"

// PluginConfigMapName returns the name of the per-task ConfigMap that
// carries plugin skill and agent content for the given task.
func PluginConfigMapName(taskName string) string {
	return taskName + "-plugins"
}

// buildPluginConfigMapData validates plugin, skill, and agent names and
// produces the plugin ConfigMap payload: a flat map of safe keys
// ("p<i>-s<j>" for skills, "p<i>-a<j>" for agents; ConfigMap keys must
// match [-._a-zA-Z0-9]+) to content blobs, plus the volume projection
// items mapping each key to its nested path under the plugin directory
// ("<plugin>/skills/<skill>/SKILL.md", "<plugin>/agents/<agent>.md").
func buildPluginConfigMapData(plugins []kelos.PluginSpec) (map[string]string, []corev1.KeyToPath, error) {
	data := map[string]string{}
	var items []corev1.KeyToPath
	totalBytes := 0

	for i, plugin := range plugins {
		if err := sanitizeComponentName(plugin.Name, "plugin"); err != nil {
			return nil, nil, err
		}

		for j, skill := range plugin.Skills {
			if err := sanitizeComponentName(skill.Name, "skill"); err != nil {
				return nil, nil, err
			}
			key := fmt.Sprintf("p%d-s%d", i, j)
			data[key] = skill.Content
			totalBytes += len(skill.Content)
			items = append(items, corev1.KeyToPath{
				Key:  key,
				Path: path.Join(plugin.Name, "skills", skill.Name, "SKILL.md"),
			})
		}

		for j, agent := range plugin.Agents {
			if err := sanitizeComponentName(agent.Name, "agent"); err != nil {
				return nil, nil, err
			}
			key := fmt.Sprintf("p%d-a%d", i, j)
			data[key] = agent.Content
			totalBytes += len(agent.Content)
			items = append(items, corev1.KeyToPath{
				Key:  key,
				Path: path.Join(plugin.Name, "agents", agent.Name+".md"),
			})
		}
	}

	if totalBytes > pluginConfigMapMaxBytes {
		return nil, nil, fmt.Errorf(
			"plugin content totals %d bytes, exceeding the %d byte ConfigMap budget; reduce skill/agent content size",
			totalBytes, pluginConfigMapMaxBytes,
		)
	}

	return data, items, nil
}

// buildPluginConfigMap produces the per-task ConfigMap that delivers plugin
// skill and agent content to the plugin-setup init container. The Job built
// by JobBuilder.Build mounts this ConfigMap by name, so it must be created
// before the Job.
func buildPluginConfigMap(task *kelos.Task, plugins []kelos.PluginSpec) (*corev1.ConfigMap, error) {
	data, _, err := buildPluginConfigMapData(plugins)
	if err != nil {
		return nil, fmt.Errorf("invalid plugin configuration: %w", err)
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PluginConfigMapName(task.Name),
			Namespace: task.Namespace,
		},
		Data: data,
	}, nil
}

type skillsAuthEnv struct {
	SecretName string
	EnvName    string
}

func collectSkillsAuthEnvs(skills []kelos.SkillsShSpec) []skillsAuthEnv {
	seen := map[string]string{}
	var out []skillsAuthEnv
	for _, skill := range skills {
		if skill.SecretRef == nil {
			continue
		}
		if _, ok := seen[skill.SecretRef.Name]; ok {
			continue
		}
		envName := fmt.Sprintf("KELOS_SKILLS_GITHUB_TOKEN_%d", len(out))
		seen[skill.SecretRef.Name] = envName
		out = append(out, skillsAuthEnv{
			SecretName: skill.SecretRef.Name,
			EnvName:    envName,
		})
	}
	return out
}

func skillsAuthEnvVars(authEnvs []skillsAuthEnv) []corev1.EnvVar {
	if len(authEnvs) == 0 {
		return nil
	}
	out := make([]corev1.EnvVar, 0, len(authEnvs))
	for _, authEnv := range authEnvs {
		out = append(out, corev1.EnvVar{
			Name: authEnv.EnvName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: authEnv.SecretName},
					Key:                  GitHubTokenSecretKey,
				},
			},
		})
	}
	return out
}

func skillsAuthEnvMap(authEnvs []skillsAuthEnv) map[string]string {
	out := make(map[string]string, len(authEnvs))
	for _, authEnv := range authEnvs {
		out[authEnv.SecretName] = authEnv.EnvName
	}
	return out
}

// buildSkillsInstallScript generates a shell script that installs skills.sh
// packages into the plugin volume using "npx skills add".
// The script installs git (required by the skills CLI to clone repositories),
// then relocates the installed skills into the <plugin>/skills/<skill>
// layout that agent entrypoints discover, and ensures all output files are
// owned by AgentUID.
func buildSkillsInstallScript(skills []kelos.SkillsShSpec, authEnvs []skillsAuthEnv) (string, error) {
	lines := []string{
		"set -eu",
		"apk add --no-cache git >/dev/null 2>&1",
	}
	authEnvBySecret := skillsAuthEnvMap(authEnvs)
	if len(authEnvBySecret) > 0 {
		lines = append(lines,
			"cat > /tmp/kelos-skills-askpass <<'EOF'",
			"#!/bin/sh",
			`case "$1" in`,
			`  *Username*|*username*) printf '%s\n' "${KELOS_SKILLS_GIT_USERNAME:-x-access-token}" ;;`,
			`  *) printf '%s\n' "$KELOS_SKILLS_GIT_TOKEN" ;;`,
			"esac",
			"EOF",
			"chmod 700 /tmp/kelos-skills-askpass",
		)
	}

	for _, s := range skills {
		if s.Source == "" {
			return "", fmt.Errorf("skills.sh source is empty")
		}
		// The "universal" agent target installs the canonical skill format
		// into the fixed $HOME/.agents/skills directory regardless of the
		// task's agent type. Kelos agent type names are not valid skills.sh
		// agent names (e.g. "gemini" vs "gemini-cli"), and per-agent targets
		// scatter output across different hidden directories.
		args := fmt.Sprintf("npx -y skills add %s -a universal -y -g", shellQuote(s.Source))
		if s.Skill != "" {
			args += fmt.Sprintf(" -s %s", shellQuote(s.Skill))
		}
		if s.SecretRef != nil {
			tokenEnvName := authEnvBySecret[s.SecretRef.Name]
			if tokenEnvName == "" {
				return "", fmt.Errorf("skills.sh source %q references secret %q without a token env var", s.Source, s.SecretRef.Name)
			}
			args = authenticatedSkillsInstallCommand(s.Source, tokenEnvName, args)
		}
		lines = append(lines, args)
	}

	// "skills add -g" writes into hidden directories under $HOME (set to
	// PluginMountPath) that entrypoints never scan, so move the result into
	// the plugin layout and drop the installer's leftover state.
	installDir := path.Join(PluginMountPath, ".agents", "skills")
	pluginSkillsDir := path.Join(PluginMountPath, SkillsShPluginName, "skills")
	lines = append(lines,
		fmt.Sprintf("[ -d %s ] || { echo 'No skills.sh skills were installed' >&2; exit 1; }", shellQuote(installDir)),
		fmt.Sprintf("mkdir -p %s", shellQuote(pluginSkillsDir)),
		fmt.Sprintf("mv %s/* %s/", shellQuote(installDir), shellQuote(pluginSkillsDir)),
		fmt.Sprintf("rm -rf %s %s", shellQuote(path.Join(PluginMountPath, ".agents")), shellQuote(path.Join(PluginMountPath, ".npm"))),
		fmt.Sprintf("chown -R %d:%d %s", AgentUID, AgentUID, shellQuote(PluginMountPath)),
	)

	return strings.Join(lines, "\n"), nil
}

func authenticatedSkillsInstallCommand(source, tokenEnvName, command string) string {
	envParts := []string{
		envFromVar("GITHUB_TOKEN", tokenEnvName),
		envFromVar("KELOS_SKILLS_GIT_TOKEN", tokenEnvName),
		"KELOS_SKILLS_GIT_USERNAME=x-access-token",
		"GIT_ASKPASS=/tmp/kelos-skills-askpass",
		"GIT_TERMINAL_PROMPT=0",
	}
	host, _, _ := parseGitHubRepo(source)
	if host != "" && host != "github.com" {
		envParts = append(envParts,
			fmt.Sprintf("GH_HOST=%s", shellQuote(host)),
			envFromVar("GH_ENTERPRISE_TOKEN", tokenEnvName),
		)
	} else {
		envParts = append(envParts, envFromVar("GH_TOKEN", tokenEnvName))
	}
	return strings.Join(envParts, " ") + " " + command
}

func envFromVar(name, valueVar string) string {
	return fmt.Sprintf(`%s="${%s}"`, name, valueVar)
}

// mcpServerJSON represents a single MCP server entry in the .mcp.json
// format used by Claude Code and other agents. Fields are omitted when
// empty so that the resulting JSON stays minimal.
type mcpServerJSON struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// buildMCPServersJSON converts MCPServerSpec entries into a JSON string
// that matches the .mcp.json format: {"mcpServers":{"name":{...},...}}.
// Env entries must already be resolved to literal Name/Value pairs by
// resolveMCPServerSecrets — any remaining ValueFrom is treated as a bug.
func buildMCPServersJSON(servers []kelos.MCPServerSpec) (string, error) {
	mcpMap := make(map[string]mcpServerJSON, len(servers))
	for _, s := range servers {
		if s.Name == "" {
			return "", fmt.Errorf("MCP server name is empty")
		}
		if err := sanitizeComponentName(s.Name, "MCP server"); err != nil {
			return "", err
		}
		if _, exists := mcpMap[s.Name]; exists {
			return "", fmt.Errorf("duplicate MCP server name %q", s.Name)
		}
		envMap, err := envVarsToMap(s.Name, s.Env)
		if err != nil {
			return "", err
		}
		entry := mcpServerJSON{
			Type:    s.Type,
			Command: s.Command,
			Args:    s.Args,
			URL:     s.URL,
			Headers: s.Headers,
			Env:     envMap,
		}
		mcpMap[s.Name] = entry
	}
	wrapper := struct {
		MCPServers map[string]mcpServerJSON `json:"mcpServers"`
	}{MCPServers: mcpMap}
	data, err := json.Marshal(wrapper)
	if err != nil {
		return "", fmt.Errorf("marshalling MCP servers: %w", err)
	}
	return string(data), nil
}

// envVarsToMap flattens a resolved []corev1.EnvVar into the map shape used by
// the .mcp.json env field. ValueFrom must already have been resolved.
func envVarsToMap(serverName string, env []corev1.EnvVar) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for _, e := range env {
		if e.Name == "" {
			return nil, fmt.Errorf("MCP server %q has an env entry with an empty name", serverName)
		}
		if e.ValueFrom != nil {
			return nil, fmt.Errorf("MCP server %q env %q: valueFrom must be resolved before rendering", serverName, e.Name)
		}
		out[e.Name] = e.Value
	}
	return out, nil
}
