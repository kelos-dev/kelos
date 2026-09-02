package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func envNames(vars []corev1.EnvVar) []string {
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, v.Name)
	}
	return names
}

func TestWorkspaceProviderFor(t *testing.T) {
	tests := []struct {
		name      string
		workspace *kelos.WorkspaceSpec
		want      string
	}{
		{name: "nil workspace defaults to github", workspace: nil, want: kelos.WorkspaceProviderGitHub},
		{name: "empty provider defaults to github", workspace: &kelos.WorkspaceSpec{}, want: kelos.WorkspaceProviderGitHub},
		{name: "gitlab", workspace: &kelos.WorkspaceSpec{Provider: kelos.WorkspaceProviderGitLab}, want: kelos.WorkspaceProviderGitLab},
		{name: "unknown falls back to github", workspace: &kelos.WorkspaceSpec{Provider: "bitbucket"}, want: kelos.WorkspaceProviderGitHub},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workspaceProviderFor(tt.workspace).name; got != tt.want {
				t.Errorf("workspaceProviderFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkspaceProviderGitHubContract(t *testing.T) {
	p := workspaceProviderFor(&kelos.WorkspaceSpec{Provider: kelos.WorkspaceProviderGitHub})

	if p.secretKey != "GITHUB_TOKEN" || p.tokenEnv != "GITHUB_TOKEN" || p.tokenFileEnv != "KELOS_GITHUB_TOKEN_FILE" || p.gitUsername != "x-access-token" {
		t.Fatalf("unexpected github contract: %+v", p)
	}
	if p.tokenFile() != GitHubTokenMountPath+"/GITHUB_TOKEN" {
		t.Errorf("tokenFile() = %q", p.tokenFile())
	}

	if got := p.hostEnv("https://github.com/org/repo.git"); len(got) != 0 {
		t.Errorf("github.com must not set GH_HOST, got %v", got)
	}
	if got := p.hostEnv("https://ghe.example.com/org/repo.git"); len(got) != 1 || got[0].Name != "GH_HOST" || got[0].Value != "ghe.example.com" {
		t.Errorf("enterprise host env = %v, want GH_HOST=ghe.example.com", got)
	}

	shared, agentOnly := p.tokenEnvVars("https://github.com/org/repo.git", "github-token")
	if got := strings.Join(envNames(shared), ","); got != "GITHUB_TOKEN,GH_TOKEN,KELOS_GITHUB_TOKEN_FILE" {
		t.Errorf("shared env = %s", got)
	}
	if got := strings.Join(envNames(agentOnly), ","); got != "GH_CONFIG_DIR" {
		t.Errorf("agent-only env = %s", got)
	}
	shared, _ = p.tokenEnvVars("https://ghe.example.com/org/repo.git", "github-token")
	if got := strings.Join(envNames(shared), ","); got != "GITHUB_TOKEN,GH_ENTERPRISE_TOKEN,KELOS_GITHUB_TOKEN_FILE" {
		t.Errorf("enterprise shared env = %s", got)
	}
	for _, v := range shared[:2] {
		if v.ValueFrom == nil || v.ValueFrom.SecretKeyRef == nil || v.ValueFrom.SecretKeyRef.Name != "github-token" || v.ValueFrom.SecretKeyRef.Key != "GITHUB_TOKEN" {
			t.Errorf("%s must come from github-token/GITHUB_TOKEN, got %+v", v.Name, v.ValueFrom)
		}
	}
}

func TestWorkspaceProviderGitLabContract(t *testing.T) {
	p := workspaceProviderFor(&kelos.WorkspaceSpec{Provider: kelos.WorkspaceProviderGitLab})

	if p.secretKey != "GITLAB_TOKEN" || p.tokenEnv != "GITLAB_TOKEN" || p.tokenFileEnv != "KELOS_GITLAB_TOKEN_FILE" || p.gitUsername != "oauth2" {
		t.Fatalf("unexpected gitlab contract: %+v", p)
	}
	if p.tokenFile() != GitLabTokenMountPath+"/GITLAB_TOKEN" {
		t.Errorf("tokenFile() = %q", p.tokenFile())
	}

	got := p.hostEnv("http://gitlab-webservice-default.gitlab.svc:8181/group/repo.git")
	if len(got) != 1 || got[0].Name != "GITLAB_HOST" || got[0].Value != "http://gitlab-webservice-default.gitlab.svc:8181" {
		t.Errorf("host env = %v, want GITLAB_HOST with scheme, host and port", got)
	}

	shared, agentOnly := p.tokenEnvVars("https://gitlab.example.com/group/repo.git", "gitlab-token")
	if got := strings.Join(envNames(shared), ","); got != "GITLAB_TOKEN,KELOS_GITLAB_TOKEN_FILE" {
		t.Errorf("shared env = %s", got)
	}
	if shared[0].ValueFrom.SecretKeyRef.Name != "gitlab-token" || shared[0].ValueFrom.SecretKeyRef.Key != "GITLAB_TOKEN" {
		t.Errorf("GITLAB_TOKEN must come from gitlab-token/GITLAB_TOKEN, got %+v", shared[0].ValueFrom)
	}
	if shared[1].Value != GitLabTokenMountPath+"/GITLAB_TOKEN" {
		t.Errorf("KELOS_GITLAB_TOKEN_FILE = %q", shared[1].Value)
	}
	if got := strings.Join(envNames(agentOnly), ","); got != "GLAB_CONFIG_DIR,GLAB_NO_PROMPT,GLAB_CHECK_UPDATE,GLAB_SEND_TELEMETRY" {
		t.Errorf("agent-only env = %s", got)
	}
	for _, v := range append(shared, agentOnly...) {
		if strings.HasPrefix(v.Name, "GH_") || v.Name == "GITHUB_TOKEN" {
			t.Errorf("gitlab provider must not export GitHub variables, got %s", v.Name)
		}
	}

	volume := p.tokenVolume("gitlab-token")
	if volume.Name != GitLabTokenVolumeName || volume.Secret.SecretName != "gitlab-token" || len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Key != "GITLAB_TOKEN" {
		t.Errorf("unexpected token volume: %+v", volume)
	}
	mount := p.tokenVolumeMount()
	if mount.Name != GitLabTokenVolumeName || mount.MountPath != GitLabTokenMountPath || !mount.ReadOnly {
		t.Errorf("unexpected token mount: %+v", mount)
	}
}

func TestGitCredentialHelperUsesProviderTokenEnv(t *testing.T) {
	helper := gitCredentialHelper(workspaceProviderFor(&kelos.WorkspaceSpec{Provider: kelos.WorkspaceProviderGitLab}))
	if !strings.Contains(helper, GitLabTokenMountPath+"/GITLAB_TOKEN") || !strings.Contains(helper, `password=$GITLAB_TOKEN`) {
		t.Errorf("gitlab credential helper must read the GitLab token file and env, got %q", helper)
	}
	if strings.Contains(helper, "GITHUB_TOKEN") {
		t.Errorf("gitlab credential helper must not fall back to GITHUB_TOKEN, got %q", helper)
	}
}

func TestWorkspaceSecretTokenError(t *testing.T) {
	github := workspaceProviderFor(&kelos.WorkspaceSpec{})
	gitlab := workspaceProviderFor(&kelos.WorkspaceSpec{Provider: kelos.WorkspaceProviderGitLab})

	if err := workspaceSecretTokenError(github, "app", map[string][]byte{"appID": []byte("1")}); err != nil {
		t.Errorf("github secrets are exempt from the token check, got %v", err)
	}
	if err := workspaceSecretTokenError(gitlab, "gl", map[string][]byte{"GITLAB_TOKEN": []byte("glpat")}); err != nil {
		t.Errorf("unexpected error for a valid gitlab secret: %v", err)
	}
	err := workspaceSecretTokenError(gitlab, "gl", map[string][]byte{"GITHUB_TOKEN": []byte("glpat")})
	if err == nil || !strings.Contains(err.Error(), `secret "gl" has no GITLAB_TOKEN key`) {
		t.Errorf("GITHUB_TOKEN must not stand in for GITLAB_TOKEN, got %v", err)
	}
	if err := workspaceSecretTokenError(gitlab, "gl", map[string][]byte{"GITLAB_TOKEN": []byte(" \n")}); err == nil {
		t.Error("blank GITLAB_TOKEN must be rejected")
	}
}

func TestValidateTaskSpawnerWorkspace(t *testing.T) {
	gitlabWS := &kelos.WorkspaceSpec{Provider: kelos.WorkspaceProviderGitLab, SecretRef: &kelos.SecretReference{Name: "gl"}}
	githubWS := &kelos.WorkspaceSpec{SecretRef: &kelos.SecretReference{Name: "gh"}}
	gitlabData := map[string][]byte{"GITLAB_TOKEN": []byte("glpat")}

	tests := []struct {
		name      string
		when      kelos.When
		workspace *kelos.WorkspaceSpec
		data      map[string][]byte
		wantErr   string
	}{
		{name: "gitlab source with gitlab workspace", when: kelos.When{GitLab: &kelos.GitLab{}}, workspace: gitlabWS, data: gitlabData},
		{name: "gitlab webhook with gitlab workspace", when: kelos.When{GitLabWebhook: &kelos.GitLabWebhook{}}, workspace: gitlabWS, data: gitlabData},
		{name: "github issues with github workspace", when: kelos.When{GitHubIssues: &kelos.GitHubIssues{}}, workspace: githubWS, data: map[string][]byte{"GITHUB_TOKEN": []byte("ghp")}},
		{name: "github app secret without token key", when: kelos.When{GitHubIssues: &kelos.GitHubIssues{}}, workspace: githubWS, data: map[string][]byte{"appID": []byte("1")}},
		{name: "cron works with any provider", when: kelos.When{Cron: &kelos.Cron{Schedule: "@hourly"}}, workspace: gitlabWS, data: gitlabData},
		{name: "jira with gitlab workspace missing token", when: kelos.When{Jira: &kelos.Jira{}}, workspace: gitlabWS, data: map[string][]byte{}, wantErr: "has no GITLAB_TOKEN key"},
		{name: "gitlab source with github workspace", when: kelos.When{GitLab: &kelos.GitLab{}}, workspace: githubWS, data: map[string][]byte{"GITHUB_TOKEN": []byte("glpat")}, wantErr: "requires a Workspace with provider gitlab, but the Workspace provider is github"},
		{name: "github source with gitlab workspace", when: kelos.When{GitHubPullRequests: &kelos.GitHubPullRequests{}}, workspace: gitlabWS, data: gitlabData, wantErr: "requires a Workspace with provider github, but the Workspace provider is gitlab"},
		{name: "gitlab secret under GITHUB_TOKEN key", when: kelos.When{GitLab: &kelos.GitLab{}}, workspace: gitlabWS, data: map[string][]byte{"GITHUB_TOKEN": []byte("glpat")}, wantErr: `secret "gl" has no GITLAB_TOKEN key`},
		{name: "gitlab source without workspace secret", when: kelos.When{GitLab: &kelos.GitLab{}}, workspace: &kelos.WorkspaceSpec{Provider: kelos.WorkspaceProviderGitLab}, wantErr: "reference a Secret with a GITLAB_TOKEN key"},
		{name: "nil workspace", when: kelos.When{GitLab: &kelos.GitLab{}}, workspace: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := &kelos.TaskSpawner{Spec: kelos.TaskSpawnerSpec{When: tt.when}}
			err := validateTaskSpawnerWorkspace(ts, tt.workspace, tt.data)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if _, ok := err.(*workspaceValidationError); !ok {
				t.Fatalf("error must be a *workspaceValidationError so the spawner is marked Failed, got %T", err)
			}
		})
	}
}
