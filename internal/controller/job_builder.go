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

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
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

	// AgentTypeClaudeCode is the agent type for Claude Code.
	AgentTypeClaudeCode = "claude-code"

	// AgentTypeCodex is the agent type for OpenAI Codex.
	AgentTypeCodex = "codex"

	// AgentTypeGemini is the agent type for Google Gemini CLI.
	AgentTypeGemini = "gemini"

	// AgentTypeOpenCode is the agent type for OpenCode.
	AgentTypeOpenCode = "opencode"

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
	GeminiImage               string
	GeminiImagePullPolicy     corev1.PullPolicy
	OpenCodeImage             string
	OpenCodeImagePullPolicy   corev1.PullPolicy
}

// NewJobBuilder creates a new JobBuilder.
func NewJobBuilder() *JobBuilder {
	return &JobBuilder{
		ClaudeCodeImage: ClaudeCodeImage,
		CodexImage:      CodexImage,
		GeminiImage:     GeminiImage,
		OpenCodeImage:   OpenCodeImage,
	}
}

// Build creates a Job for the given Task. The prompt parameter is the
// resolved prompt text (which may have been expanded from a template).
func (b *JobBuilder) Build(task *kelosv1alpha1.Task, workspace *kelosv1alpha1.WorkspaceSpec, agentConfig *kelosv1alpha1.AgentConfigSpec, prompt string) (*batchv1.Job, error) {
	switch task.Spec.Type {
	case AgentTypeClaudeCode:
		return b.buildAgentJob(task, workspace, agentConfig, b.ClaudeCodeImage, b.ClaudeCodeImagePullPolicy, prompt)
	case AgentTypeCodex:
		return b.buildAgentJob(task, workspace, agentConfig, b.CodexImage, b.CodexImagePullPolicy, prompt)
	case AgentTypeGemini:
		return b.buildAgentJob(task, workspace, agentConfig, b.GeminiImage, b.GeminiImagePullPolicy, prompt)
	case AgentTypeOpenCode:
		return b.buildAgentJob(task, workspace, agentConfig, b.OpenCodeImage, b.OpenCodeImagePullPolicy, prompt)
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
	case AgentTypeGemini:
		// GEMINI_API_KEY is the environment variable that the gemini CLI
		// reads for API key authentication.
		return "GEMINI_API_KEY"
	case AgentTypeOpenCode:
		// OPENCODE_API_KEY is the environment variable that the opencode
		// entrypoint reads for API key authentication.
		return "OPENCODE_API_KEY"
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
	default:
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
}

// buildAgentJob creates a Job for the given agent type.
func (b *JobBuilder) buildAgentJob(task *kelosv1alpha1.Task, workspace *kelosv1alpha1.WorkspaceSpec, agentConfig *kelosv1alpha1.AgentConfigSpec, defaultImage string, pullPolicy corev1.PullPolicy, prompt string) (*batchv1.Job, error) {
	image := defaultImage
	if task.Spec.Image != "" {
		image = task.Spec.Image
	}

	var envVars []corev1.EnvVar

	// Set KELOS_MODEL for all agent containers.
	if task.Spec.Model != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_MODEL",
			Value: task.Spec.Model,
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
		Value: task.Spec.Type,
	})

	if spawner := task.Labels["kelos.dev/taskspawner"]; spawner != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_TASKSPAWNER",
			Value: spawner,
		})
	}

	switch task.Spec.Credentials.Type {
	case kelosv1alpha1.CredentialTypeAPIKey:
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
	case kelosv1alpha1.CredentialTypeOAuth:
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

		if workspace.Ref != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_BASE_BRANCH",
				Value: workspace.Ref,
			})
		}

		// Inject KELOS_UPSTREAM_REPO if an "upstream" remote is configured
		for _, remote := range workspace.Remotes {
			if remote.Name == "upstream" {
				_, upstreamOwner, upstreamRepo := parseGitHubRepo(remote.URL)
				if upstreamOwner != "" && upstreamRepo != "" {
					envVars = append(envVars, corev1.EnvVar{
						Name:  "KELOS_UPSTREAM_REPO",
						Value: fmt.Sprintf("%s/%s", upstreamOwner, upstreamRepo),
					})
				}
				break
			}
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
			credentialHelper := `!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f`
			initContainer.Command = []string{"sh", "-c",
				fmt.Sprintf(
					`git -c credential.helper='%s' "$@" && git -C %s/repo config credential.helper '%s'`,
					credentialHelper, WorkspaceMountPath, credentialHelper,
				),
			}
			initContainer.Args = append([]string{"--"}, cloneArgs...)
		}

		initContainers = append(initContainers, initContainer)

		if len(workspace.Remotes) > 0 {
			var parts []string
			parts = append(parts, fmt.Sprintf("cd %s/repo", WorkspaceMountPath))
			for _, r := range workspace.Remotes {
				parts = append(parts, fmt.Sprintf("git remote add %s %s", shellQuote(r.Name), shellQuote(r.URL)))
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
				credHelper := `!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f`
				fetchCmd = fmt.Sprintf(`git -c credential.helper='%s' fetch origin "$KELOS_BRANCH":"$KELOS_BRANCH" 2>/dev/null`, credHelper)
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
				VolumeMounts: []corev1.VolumeMount{volumeMount},
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

		mainContainer.VolumeMounts = []corev1.VolumeMount{volumeMount}
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

		if len(agentConfig.Plugins) > 0 {
			volumes = append(volumes, corev1.Volume{
				Name:         PluginVolumeName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})

			wsHasToken := workspace != nil && workspace.SecretRef != nil
			script, pluginEnvVars, err := buildPluginSetupScript(agentConfig.Plugins, wsHasToken)
			if err != nil {
				return nil, fmt.Errorf("invalid plugin configuration: %w", err)
			}

			pluginSetupEnv := append([]corev1.EnvVar{}, pluginEnvVars...)
			// Only pass workspace token when a GitHub plugin needs it as fallback
			// (i.e., it has no secretRef of its own).
			if wsHasToken {
				needsWorkspaceToken := false
				for _, p := range agentConfig.Plugins {
					if p.GitHub != nil && p.GitHub.SecretRef == nil {
						needsWorkspaceToken = true
						break
					}
				}
				if needsWorkspaceToken {
					pluginSetupEnv = append(pluginSetupEnv, workspaceEnvVars...)
				}
			}

			initContainers = append(initContainers, corev1.Container{
				Name:    "plugin-setup",
				Image:   GitCloneImage,
				Command: []string{"sh", "-c", script},
				Env:     pluginSetupEnv,
				VolumeMounts: []corev1.VolumeMount{
					{Name: PluginVolumeName, MountPath: PluginMountPath},
				},
				SecurityContext: &corev1.SecurityContext{RunAsUser: &agentUID},
			})

			mainContainer.VolumeMounts = append(mainContainer.VolumeMounts,
				corev1.VolumeMount{Name: PluginVolumeName, MountPath: PluginMountPath})
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_PLUGIN_DIR",
				Value: PluginMountPath,
			})
		}

		if len(agentConfig.MarketplacePlugins) > 0 {
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_MARKETPLACE_PLUGINS",
				Value: strings.Join(agentConfig.MarketplacePlugins, ","),
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
	var activeDeadlineSeconds *int64
	var nodeSelector map[string]string

	if po := task.Spec.PodOverrides; po != nil {
		if po.Resources != nil {
			mainContainer.Resources = *po.Resources
		}

		if po.ActiveDeadlineSeconds != nil {
			activeDeadlineSeconds = po.ActiveDeadlineSeconds
		}

		if len(po.Env) > 0 {
			// Filter out user env vars that collide with built-in names
			// so that built-in vars always take precedence.
			builtinNames := make(map[string]struct{}, len(mainContainer.Env))
			for _, e := range mainContainer.Env {
				builtinNames[e.Name] = struct{}{}
			}
			for _, e := range po.Env {
				if _, exists := builtinNames[e.Name]; !exists {
					mainContainer.Env = append(mainContainer.Env, e)
				}
			}
		}

		if po.NodeSelector != nil {
			nodeSelector = po.NodeSelector
		}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      task.Name,
			Namespace: task.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "kelos",
				"app.kubernetes.io/component":  "task",
				"app.kubernetes.io/managed-by": "kelos-controller",
				"kelos.dev/task":               task.Name,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name":       "kelos",
						"app.kubernetes.io/component":  "task",
						"app.kubernetes.io/managed-by": "kelos-controller",
						"kelos.dev/task":               task.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:   corev1.RestartPolicyNever,
					SecurityContext: podSecurityContext,
					InitContainers:  initContainers,
					Volumes:         volumes,
					Containers:      []corev1.Container{mainContainer},
					NodeSelector:    nodeSelector,
				},
			},
		},
	}

	return job, nil
}

func buildWorkspaceFileInjectionScript(files []kelosv1alpha1.WorkspaceFile) (string, error) {
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

// validateGitHubRepo checks that a GitHub plugin repo string is in "owner/repo" format.
func validateGitHubRepo(repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("GitHub plugin repo %q must be in owner/repo format", repo)
	}
	return nil
}

// gitHubCloneURL returns the HTTPS clone URL for a GitHub repository.
func gitHubCloneURL(host, repo string) string {
	if host == "" {
		host = "github.com"
	}
	return fmt.Sprintf("https://%s/%s.git", host, repo)
}

func buildPluginSetupScript(plugins []kelosv1alpha1.PluginSpec, workspaceHasToken bool) (string, []corev1.EnvVar, error) {
	lines := []string{"set -eu"}
	var envVars []corev1.EnvVar

	seen := make(map[string]bool, len(plugins))
	tokenIdx := 0

	for _, plugin := range plugins {
		if err := sanitizeComponentName(plugin.Name, "plugin"); err != nil {
			return "", nil, err
		}
		if seen[plugin.Name] {
			return "", nil, fmt.Errorf("duplicate plugin name %q", plugin.Name)
		}
		seen[plugin.Name] = true

		if plugin.GitHub != nil && (len(plugin.Skills) > 0 || len(plugin.Agents) > 0) {
			return "", nil, fmt.Errorf("plugin %q: github and inline skills/agents are mutually exclusive", plugin.Name)
		}

		if plugin.GitHub != nil {
			gh := plugin.GitHub
			if err := validateGitHubRepo(gh.Repo); err != nil {
				return "", nil, err
			}

			cloneURL := gitHubCloneURL(gh.Host, gh.Repo)
			dest := path.Join(PluginMountPath, plugin.Name)

			// Build credential helper based on auth source.
			var credPrefix string
			if gh.SecretRef != nil {
				envName := fmt.Sprintf("KELOS_PLUGIN_TOKEN_%d", tokenIdx)
				tokenIdx++
				envVars = append(envVars, corev1.EnvVar{
					Name: envName,
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: gh.SecretRef.Name,
							},
							Key: "GITHUB_TOKEN",
						},
					},
				})
				credHelper := fmt.Sprintf(`!f() { echo "username=x-access-token"; echo "password=$%s"; }; f`, envName)
				credPrefix = fmt.Sprintf("git -c credential.helper=%s", shellQuote(credHelper))
			} else if workspaceHasToken {
				credHelper := `!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f`
				credPrefix = fmt.Sprintf("git -c credential.helper=%s", shellQuote(credHelper))
			}

			var cloneCmd string
			cloneArgs := "clone --depth 1"
			if gh.Ref != "" {
				cloneArgs += " --branch " + shellQuote(gh.Ref)
			}
			cloneArgs += " -- " + shellQuote(cloneURL) + " " + shellQuote(dest)
			if credPrefix != "" {
				cloneCmd = credPrefix + " " + cloneArgs
			} else {
				cloneCmd = "git " + cloneArgs
			}
			lines = append(lines, cloneCmd)
			continue
		}

		for _, skill := range plugin.Skills {
			if err := sanitizeComponentName(skill.Name, "skill"); err != nil {
				return "", nil, err
			}
			dir := path.Join(PluginMountPath, plugin.Name, "skills", skill.Name)
			target := path.Join(dir, "SKILL.md")
			contentBase64 := base64.StdEncoding.EncodeToString([]byte(skill.Content))
			lines = append(lines,
				fmt.Sprintf("mkdir -p %s", shellQuote(dir)),
				fmt.Sprintf("printf '%%s' %s | base64 -d > %s", shellQuote(contentBase64), shellQuote(target)),
			)
		}

		for _, agent := range plugin.Agents {
			if err := sanitizeComponentName(agent.Name, "agent"); err != nil {
				return "", nil, err
			}
			dir := path.Join(PluginMountPath, plugin.Name, "agents")
			target := path.Join(dir, agent.Name+".md")
			contentBase64 := base64.StdEncoding.EncodeToString([]byte(agent.Content))
			lines = append(lines,
				fmt.Sprintf("mkdir -p %s", shellQuote(dir)),
				fmt.Sprintf("printf '%%s' %s | base64 -d > %s", shellQuote(contentBase64), shellQuote(target)),
			)
		}
	}

	return strings.Join(lines, "\n"), envVars, nil
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
func buildMCPServersJSON(servers []kelosv1alpha1.MCPServerSpec) (string, error) {
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
		entry := mcpServerJSON{
			Type:    s.Type,
			Command: s.Command,
			Args:    s.Args,
			URL:     s.URL,
			Headers: s.Headers,
			Env:     s.Env,
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
