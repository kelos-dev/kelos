package controller

import (
	"bytes"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	// GitLabTokenSecretKey is the Secret key under which a GitLab access token
	// is stored for provider gitlab. Mounted as a file at
	// GitLabTokenMountPath + "/" + GitLabTokenSecretKey.
	GitLabTokenSecretKey = "GITLAB_TOKEN"

	// GitLabTokenVolumeName is the volume that mounts the workspace token
	// Secret into agent and init containers for provider gitlab.
	GitLabTokenVolumeName = "kelos-gitlab-token"

	// GitLabTokenMountPath is the directory where the GitLab token Secret is
	// mounted.
	GitLabTokenMountPath = "/kelos/gitlab-token"

	// GlabConfigDir is the directory used for glab CLI configuration. It is
	// placed on the shared workspace volume so glab does not read stale auth
	// from the container image's home directory.
	GlabConfigDir = WorkspaceMountPath + "/.glab-config"

	gitLabCredentialUsername = "oauth2"
)

// workspaceProvider is the contract between a Workspace secret and the pods
// that consume it: which Secret key holds the token, where the auto-syncing
// token file is mounted, which git username pairs with the token, and which
// CLI is preconfigured for the agent.
type workspaceProvider struct {
	name            string
	secretKey       string
	tokenEnv        string
	tokenFileEnv    string
	tokenVolumeName string
	tokenMountPath  string
	gitUsername     string
	// hostEnv returns the CLI host variables for a repo URL. They do not
	// depend on a Secret and reach agent and init containers alike.
	hostEnv func(repoURL string) []corev1.EnvVar
	// cliTokenEnvNames returns the CLI variables that mirror the token.
	cliTokenEnvNames func(repoURL string) []string
	// cliConfigEnv is static CLI configuration for the agent container only.
	cliConfigEnv []corev1.EnvVar
}

var workspaceProviders = map[string]workspaceProvider{
	kelos.WorkspaceProviderGitHub: {
		name:            kelos.WorkspaceProviderGitHub,
		secretKey:       GitHubTokenSecretKey,
		tokenEnv:        "GITHUB_TOKEN",
		tokenFileEnv:    "KELOS_GITHUB_TOKEN_FILE",
		tokenVolumeName: GitHubTokenVolumeName,
		tokenMountPath:  GitHubTokenMountPath,
		gitUsername:     gitCredentialDefaultUsername,
		hostEnv: func(repoURL string) []corev1.EnvVar {
			if host, enterprise := gitHubEnterpriseHost(repoURL); enterprise {
				// GH_HOST points the gh CLI at the GitHub Enterprise host.
				return []corev1.EnvVar{{Name: "GH_HOST", Value: host}}
			}
			return nil
		},
		cliTokenEnvNames: func(repoURL string) []string {
			// gh reads GH_TOKEN for github.com and GH_ENTERPRISE_TOKEN for
			// GitHub Enterprise Server hosts.
			if _, enterprise := gitHubEnterpriseHost(repoURL); enterprise {
				return []string{"GH_ENTERPRISE_TOKEN"}
			}
			return []string{"GH_TOKEN"}
		},
		cliConfigEnv: []corev1.EnvVar{{Name: "GH_CONFIG_DIR", Value: GHConfigDir}},
	},
	kelos.WorkspaceProviderGitLab: {
		name:            kelos.WorkspaceProviderGitLab,
		secretKey:       GitLabTokenSecretKey,
		tokenEnv:        "GITLAB_TOKEN",
		tokenFileEnv:    "KELOS_GITLAB_TOKEN_FILE",
		tokenVolumeName: GitLabTokenVolumeName,
		tokenMountPath:  GitLabTokenMountPath,
		gitUsername:     gitLabCredentialUsername,
		hostEnv: func(repoURL string) []corev1.EnvVar {
			// glab defaults to gitlab.com; GITLAB_HOST carries the instance URL
			// including scheme and port for self-hosted and in-cluster GitLab.
			baseURL, _ := parseGitLabRepo(repoURL)
			return []corev1.EnvVar{{Name: "GITLAB_HOST", Value: baseURL}}
		},
		// glab reads GITLAB_TOKEN directly, so no mirror variable is needed.
		cliTokenEnvNames: func(string) []string { return nil },
		cliConfigEnv: []corev1.EnvVar{
			{Name: "GLAB_CONFIG_DIR", Value: GlabConfigDir},
			{Name: "GLAB_NO_PROMPT", Value: "true"},
			{Name: "GLAB_CHECK_UPDATE", Value: "false"},
			{Name: "GLAB_SEND_TELEMETRY", Value: "false"},
		},
	},
}

// workspaceProviderFor returns the provider contract for a workspace. An
// unset provider means github, matching the CRD default.
func workspaceProviderFor(workspace *kelos.WorkspaceSpec) workspaceProvider {
	if workspace != nil {
		if p, ok := workspaceProviders[workspace.Provider]; ok {
			return p
		}
	}
	return workspaceProviders[kelos.WorkspaceProviderGitHub]
}

func gitHubEnterpriseHost(repoURL string) (string, bool) {
	host, _, _ := parseGitHubRepo(repoURL)
	return host, host != "" && host != "github.com"
}

func (p workspaceProvider) tokenFile() string {
	return p.tokenMountPath + "/" + p.secretKey
}

// tokenEnvVars returns the Secret-backed token variables plus the token file
// path, which every container that touches the repo needs (shared), and the
// CLI configuration only the agent container needs (agentOnly).
func (p workspaceProvider) tokenEnvVars(repoURL, secretName string) (shared, agentOnly []corev1.EnvVar) {
	secretKeyRef := &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
		Key:                  p.secretKey,
	}
	shared = append(shared, corev1.EnvVar{Name: p.tokenEnv, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeyRef}})
	for _, name := range p.cliTokenEnvNames(repoURL) {
		shared = append(shared, corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeyRef}})
	}
	// The mounted token file lets git and CLI wrappers re-read the token on
	// every invocation, picking up controller-side refreshes without a pod
	// restart.
	shared = append(shared, corev1.EnvVar{Name: p.tokenFileEnv, Value: p.tokenFile()})
	return shared, append([]corev1.EnvVar(nil), p.cliConfigEnv...)
}

// tokenVolume mounts the workspace token Secret as a file. The kubelet
// auto-syncs Secret volume contents, so the controller can refresh the token
// in place.
func (p workspaceProvider) tokenVolume(secretName string) corev1.Volume {
	return corev1.Volume{
		Name: p.tokenVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: secretName,
				Items:      []corev1.KeyToPath{{Key: p.secretKey, Path: p.secretKey}},
				Optional:   ptrTo(true),
			},
		},
	}
}

func (p workspaceProvider) tokenVolumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      p.tokenVolumeName,
		MountPath: p.tokenMountPath,
		ReadOnly:  true,
	}
}

// workspaceSecretTokenError reports a workspace Secret that lacks the token
// key its provider requires. GitHub is exempt because GitHub App secrets
// legitimately carry appID/installationID/privateKey instead of a token.
func workspaceSecretTokenError(p workspaceProvider, secretName string, data map[string][]byte) error {
	if p.name == kelos.WorkspaceProviderGitHub {
		return nil
	}
	if len(bytes.TrimSpace(data[p.secretKey])) == 0 {
		return fmt.Errorf("workspace secret %q has no %s key required by provider %s", secretName, p.secretKey, p.name)
	}
	return nil
}

// taskSpawnerSourceProvider returns the workspace provider a TaskSpawner
// source is bound to, or "" for sources that work with any provider.
func taskSpawnerSourceProvider(ts *kelos.TaskSpawner) string {
	when := ts.Spec.When
	switch {
	case when.GitLab != nil || when.GitLabWebhook != nil:
		return kelos.WorkspaceProviderGitLab
	case when.GitHubIssues != nil || when.GitHubPullRequests != nil || when.GitHubWebhook != nil:
		return kelos.WorkspaceProviderGitHub
	}
	return ""
}

// workspaceValidationError marks a TaskSpawner whose Workspace cannot serve
// its source. The spawner is marked Failed instead of being requeued.
type workspaceValidationError struct{ msg string }

func (e *workspaceValidationError) Error() string { return e.msg }

// validateTaskSpawnerWorkspace checks that the Workspace provider matches the
// TaskSpawner source and that the workspace Secret carries the provider's
// token key. secretData is the referenced Secret's data, or nil when the
// Workspace has no SecretRef.
func validateTaskSpawnerWorkspace(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, secretData map[string][]byte) error {
	if workspace == nil {
		return nil
	}
	p := workspaceProviderFor(workspace)
	if sourceProvider := taskSpawnerSourceProvider(ts); sourceProvider != "" && sourceProvider != p.name {
		return &workspaceValidationError{msg: fmt.Sprintf("TaskSpawner source requires a Workspace with provider %s, but the Workspace provider is %s", sourceProvider, p.name)}
	}
	if workspace.SecretRef == nil {
		if p.name != kelos.WorkspaceProviderGitHub && taskSpawnerSourceProvider(ts) == p.name {
			return &workspaceValidationError{msg: fmt.Sprintf("TaskSpawner source requires the Workspace to reference a Secret with a %s key", p.secretKey)}
		}
		return nil
	}
	if err := workspaceSecretTokenError(p, workspace.SecretRef.Name, secretData); err != nil {
		return &workspaceValidationError{msg: err.Error()}
	}
	return nil
}
