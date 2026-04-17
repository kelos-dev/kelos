package controller

import (
	"context"
	"strings"
	"testing"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestParseGitHubOwnerRepo(t *testing.T) {
	tests := []struct {
		name      string
		repoURL   string
		wantOwner string
		wantRepo  string
	}{
		{
			name:      "HTTPS URL",
			repoURL:   "https://github.com/kelos-dev/kelos.git",
			wantOwner: "kelos-dev",
			wantRepo:  "kelos",
		},
		{
			name:      "HTTPS URL without .git",
			repoURL:   "https://github.com/kelos-dev/kelos",
			wantOwner: "kelos-dev",
			wantRepo:  "kelos",
		},
		{
			name:      "HTTPS URL with trailing slash",
			repoURL:   "https://github.com/kelos-dev/kelos/",
			wantOwner: "kelos-dev",
			wantRepo:  "kelos",
		},
		{
			name:      "SSH URL",
			repoURL:   "git@github.com:kelos-dev/kelos.git",
			wantOwner: "kelos-dev",
			wantRepo:  "kelos",
		},
		{
			name:      "SSH URL without .git",
			repoURL:   "git@github.com:kelos-dev/kelos",
			wantOwner: "kelos-dev",
			wantRepo:  "kelos",
		},
		{
			name:      "HTTPS URL with org",
			repoURL:   "https://github.com/my-org/my-repo.git",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
		},
		{
			name:      "GitHub Enterprise HTTPS URL",
			repoURL:   "https://github.example.com/my-org/my-repo.git",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
		},
		{
			name:      "GitHub Enterprise SSH URL",
			repoURL:   "git@github.example.com:my-org/my-repo.git",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo := parseGitHubOwnerRepo(tt.repoURL)
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		name      string
		repoURL   string
		wantHost  string
		wantOwner string
		wantRepo  string
	}{
		{
			name:      "github.com HTTPS",
			repoURL:   "https://github.com/kelos-dev/kelos.git",
			wantHost:  "github.com",
			wantOwner: "kelos-dev",
			wantRepo:  "kelos",
		},
		{
			name:      "github.com SSH",
			repoURL:   "git@github.com:kelos-dev/kelos.git",
			wantHost:  "github.com",
			wantOwner: "kelos-dev",
			wantRepo:  "kelos",
		},
		{
			name:      "GitHub Enterprise HTTPS",
			repoURL:   "https://github.example.com/my-org/my-repo.git",
			wantHost:  "github.example.com",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
		},
		{
			name:      "GitHub Enterprise SSH",
			repoURL:   "git@github.example.com:my-org/my-repo.git",
			wantHost:  "github.example.com",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
		},
		{
			name:      "GitHub Enterprise HTTPS without .git",
			repoURL:   "https://github.example.com/my-org/my-repo",
			wantHost:  "github.example.com",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
		},
		{
			name:      "GitHub Enterprise with port",
			repoURL:   "https://github.example.com:8443/my-org/my-repo.git",
			wantHost:  "github.example.com:8443",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, owner, repo := parseGitHubRepo(tt.repoURL)
			if host != tt.wantHost {
				t.Errorf("host = %q, want %q", host, tt.wantHost)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

func TestGitHubAPIBaseURL(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{
			name: "empty host returns empty",
			host: "",
			want: "",
		},
		{
			name: "github.com returns empty",
			host: "github.com",
			want: "",
		},
		{
			name: "enterprise host",
			host: "github.example.com",
			want: "https://github.example.com/api/v3",
		},
		{
			name: "enterprise host with port",
			host: "github.example.com:8443",
			want: "https://github.example.com:8443/api/v3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gitHubAPIBaseURL(tt.host)
			if got != tt.want {
				t.Errorf("gitHubAPIBaseURL(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func TestValidateWorkspaceGHProxyRepoOverride(t *testing.T) {
	tests := []struct {
		name      string
		repo      string
		wantError bool
	}{
		{
			name:      "same host full URL allowed",
			repo:      "https://github.com/other/repo.git",
			wantError: false,
		},
		{
			name:      "owner repo shorthand allowed",
			repo:      "other/repo",
			wantError: false,
		},
		{
			name:      "cross host rejected",
			repo:      "https://github.example.com/other/repo.git",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubIssues: &kelosv1alpha1.GitHubIssues{Repo: tt.repo},
					},
				},
			}
			workspace := &kelosv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
			}

			err := validateWorkspaceGHProxyRepoOverride(ts, workspace)
			if tt.wantError && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func enableGitHubReporting(ts *kelosv1alpha1.TaskSpawner) {
	if ts.Spec.When.GitHubIssues != nil {
		ts.Spec.When.GitHubIssues.Reporting = &kelosv1alpha1.GitHubReporting{Enabled: true}
	}
	if ts.Spec.When.GitHubPullRequests != nil {
		ts.Spec.When.GitHubPullRequests.Reporting = &kelosv1alpha1.GitHubReporting{Enabled: true}
	}
}

func TestBuildDeploymentWithEnterpriseURL(t *testing.T) {
	builder := NewDeploymentBuilder()

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
		},
	}

	tests := []struct {
		name              string
		repoURL           string
		wantAPIBaseURLArg string
	}{
		{
			name:              "github.com repo does not include api-base-url arg",
			repoURL:           "https://github.com/kelos-dev/kelos.git",
			wantAPIBaseURLArg: "",
		},
		{
			name:              "enterprise repo includes api-base-url arg",
			repoURL:           "https://github.example.com/my-org/my-repo.git",
			wantAPIBaseURLArg: "--github-api-base-url=https://github.example.com/api/v3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := &kelosv1alpha1.WorkspaceSpec{
				Repo: tt.repoURL,
			}
			dep := builder.Build(ts, workspace, false)
			args := dep.Spec.Template.Spec.Containers[0].Args

			found := ""
			for _, arg := range args {
				if len(arg) > len("--github-api-base-url=") && arg[:len("--github-api-base-url=")] == "--github-api-base-url=" {
					found = arg
				}
			}

			if tt.wantAPIBaseURLArg == "" {
				if found != "" {
					t.Errorf("Expected no --github-api-base-url arg, got %q", found)
				}
			} else {
				if found != tt.wantAPIBaseURLArg {
					t.Errorf("Got arg %q, want %q", found, tt.wantAPIBaseURLArg)
				}
			}
		})
	}
}

func TestDeploymentBuilder_GitHubApp(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	enableGitHubReporting(ts)
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-app-creds",
		},
	}

	deploy := builder.Build(ts, workspace, true)

	if len(deploy.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(deploy.Spec.Template.Spec.Containers))
	}

	if len(deploy.Spec.Template.Spec.InitContainers) != 0 {
		t.Fatalf("expected 0 init containers, got %d", len(deploy.Spec.Template.Spec.InitContainers))
	}

	if len(deploy.Spec.Template.Spec.Volumes) != 0 {
		t.Fatalf("expected 0 volumes, got %d", len(deploy.Spec.Template.Spec.Volumes))
	}

	spawner := deploy.Spec.Template.Spec.Containers[0]

	if spawner.Name != "spawner" {
		t.Errorf("container name = %q, want %q", spawner.Name, "spawner")
	}

	for _, arg := range spawner.Args {
		if arg == "--github-token-file=/shared/token/GITHUB_TOKEN" {
			t.Error("spawner should not have --github-token-file arg in GitHub App mode")
		}
	}

	for _, env := range spawner.Env {
		if env.Name == "GITHUB_TOKEN" {
			t.Error("spawner should not have GITHUB_TOKEN env var in GitHub App mode")
		}
	}

	if len(spawner.VolumeMounts) != 0 {
		t.Fatalf("expected 0 volume mounts, got %d", len(spawner.VolumeMounts))
	}

	envMap := make(map[string]corev1.EnvVar)
	for _, env := range spawner.Env {
		envMap[env.Name] = env
	}

	appID, ok := envMap["GITHUB_APP_ID"]
	if !ok {
		t.Fatal("expected GITHUB_APP_ID env var")
	}
	if appID.ValueFrom == nil || appID.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected GITHUB_APP_ID to reference a secret")
	}
	if appID.ValueFrom.SecretKeyRef.Name != "github-app-creds" {
		t.Errorf("GITHUB_APP_ID secret name = %q, want %q", appID.ValueFrom.SecretKeyRef.Name, "github-app-creds")
	}
	if appID.ValueFrom.SecretKeyRef.Key != "appID" {
		t.Errorf("GITHUB_APP_ID secret key = %q, want %q", appID.ValueFrom.SecretKeyRef.Key, "appID")
	}

	installationID, ok := envMap["GITHUB_APP_INSTALLATION_ID"]
	if !ok {
		t.Fatal("expected GITHUB_APP_INSTALLATION_ID env var")
	}
	if installationID.ValueFrom == nil || installationID.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected GITHUB_APP_INSTALLATION_ID to reference a secret")
	}
	if installationID.ValueFrom.SecretKeyRef.Key != "installationID" {
		t.Errorf("GITHUB_APP_INSTALLATION_ID secret key = %q, want %q", installationID.ValueFrom.SecretKeyRef.Key, "installationID")
	}

	privateKey, ok := envMap["GITHUB_APP_PRIVATE_KEY"]
	if !ok {
		t.Fatal("expected GITHUB_APP_PRIVATE_KEY env var")
	}
	if privateKey.ValueFrom == nil || privateKey.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected GITHUB_APP_PRIVATE_KEY to reference a secret")
	}
	if privateKey.ValueFrom.SecretKeyRef.Key != "privateKey" {
		t.Errorf("GITHUB_APP_PRIVATE_KEY secret key = %q, want %q", privateKey.ValueFrom.SecretKeyRef.Key, "privateKey")
	}
}

func TestDeploymentBuilder_GitHubAppEnterprise(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	enableGitHubReporting(ts)
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.example.com/my-org/my-repo.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-app-creds",
		},
	}

	deploy := builder.Build(ts, workspace, true)

	spawner := deploy.Spec.Template.Spec.Containers[0]

	envMap := make(map[string]corev1.EnvVar)
	for _, env := range spawner.Env {
		envMap[env.Name] = env
	}

	if _, ok := envMap["GITHUB_APP_ID"]; !ok {
		t.Fatal("expected GITHUB_APP_ID env var")
	}
	if _, ok := envMap["GITHUB_APP_INSTALLATION_ID"]; !ok {
		t.Fatal("expected GITHUB_APP_INSTALLATION_ID env var")
	}
	if _, ok := envMap["GITHUB_APP_PRIVATE_KEY"]; !ok {
		t.Fatal("expected GITHUB_APP_PRIVATE_KEY env var")
	}

	if _, ok := envMap["GITHUB_API_BASE_URL"]; ok {
		t.Error("GITHUB_API_BASE_URL env var should not be injected (passed via --github-api-base-url arg)")
	}
}

func TestDeploymentBuilder_GitHubAppGitHubCom(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	enableGitHubReporting(ts)
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-app-creds",
		},
	}

	deploy := builder.Build(ts, workspace, true)

	spawner := deploy.Spec.Template.Spec.Containers[0]

	envMap := make(map[string]corev1.EnvVar)
	for _, env := range spawner.Env {
		envMap[env.Name] = env
	}

	if _, ok := envMap["GITHUB_APP_ID"]; !ok {
		t.Fatal("expected GITHUB_APP_ID env var")
	}
	if _, ok := envMap["GITHUB_APP_INSTALLATION_ID"]; !ok {
		t.Fatal("expected GITHUB_APP_INSTALLATION_ID env var")
	}
	if _, ok := envMap["GITHUB_APP_PRIVATE_KEY"]; !ok {
		t.Fatal("expected GITHUB_APP_PRIVATE_KEY env var")
	}
	if _, ok := envMap["GITHUB_API_BASE_URL"]; ok {
		t.Error("spawner should not have GITHUB_API_BASE_URL for github.com")
	}
}

func TestDeploymentBuilder_PAT(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	enableGitHubReporting(ts)
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	deploy := builder.Build(ts, workspace, false)

	if len(deploy.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(deploy.Spec.Template.Spec.Containers))
	}

	if len(deploy.Spec.Template.Spec.InitContainers) != 0 {
		t.Errorf("expected 0 init containers, got %d", len(deploy.Spec.Template.Spec.InitContainers))
	}

	spawner := deploy.Spec.Template.Spec.Containers[0]

	if len(spawner.Env) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(spawner.Env))
	}
	if spawner.Env[0].Name != "GITHUB_TOKEN" {
		t.Errorf("env var name = %q, want %q", spawner.Env[0].Name, "GITHUB_TOKEN")
	}

	if len(deploy.Spec.Template.Spec.Volumes) != 0 {
		t.Errorf("expected 0 volumes, got %d", len(deploy.Spec.Template.Spec.Volumes))
	}
}

func TestDeploymentBuilder_Jira(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Jira: &kelosv1alpha1.Jira{
					BaseURL:   "https://mycompany.atlassian.net",
					Project:   "PROJ",
					JQL:       "status = Open",
					SecretRef: kelosv1alpha1.SecretReference{Name: "jira-creds"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	deploy := builder.Build(ts, nil, false)

	if len(deploy.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(deploy.Spec.Template.Spec.Containers))
	}

	spawner := deploy.Spec.Template.Spec.Containers[0]

	// Check Jira args
	foundBaseURL := false
	foundProject := false
	foundJQL := false
	for _, arg := range spawner.Args {
		switch {
		case arg == "--jira-base-url=https://mycompany.atlassian.net":
			foundBaseURL = true
		case arg == "--jira-project=PROJ":
			foundProject = true
		case arg == "--jira-jql=status = Open":
			foundJQL = true
		}
	}
	if !foundBaseURL {
		t.Errorf("expected --jira-base-url arg, got args: %v", spawner.Args)
	}
	if !foundProject {
		t.Errorf("expected --jira-project arg, got args: %v", spawner.Args)
	}
	if !foundJQL {
		t.Errorf("expected --jira-jql arg, got args: %v", spawner.Args)
	}

	// Check env vars
	if len(spawner.Env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(spawner.Env))
	}

	envMap := make(map[string]corev1.EnvVar)
	for _, env := range spawner.Env {
		envMap[env.Name] = env
	}

	jiraUser, ok := envMap["JIRA_USER"]
	if !ok {
		t.Fatal("expected JIRA_USER env var")
	}
	if jiraUser.ValueFrom == nil || jiraUser.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected JIRA_USER to reference a secret")
	}
	if jiraUser.ValueFrom.SecretKeyRef.Name != "jira-creds" {
		t.Errorf("JIRA_USER secret name = %q, want %q", jiraUser.ValueFrom.SecretKeyRef.Name, "jira-creds")
	}
	if jiraUser.ValueFrom.SecretKeyRef.Optional == nil || !*jiraUser.ValueFrom.SecretKeyRef.Optional {
		t.Error("expected JIRA_USER secret key ref to be optional")
	}

	jiraToken, ok := envMap["JIRA_TOKEN"]
	if !ok {
		t.Fatal("expected JIRA_TOKEN env var")
	}
	if jiraToken.ValueFrom == nil || jiraToken.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected JIRA_TOKEN to reference a secret")
	}
	if jiraToken.ValueFrom.SecretKeyRef.Name != "jira-creds" {
		t.Errorf("JIRA_TOKEN secret name = %q, want %q", jiraToken.ValueFrom.SecretKeyRef.Name, "jira-creds")
	}
}

func TestBuildDeploymentWithGitHubIssuesRepoOverride(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{
					Repo: "https://github.com/upstream-org/upstream-repo.git",
				},
			},
		},
	}
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/my-fork/upstream-repo.git",
	}

	deploy := builder.Build(ts, workspace, false)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	foundOwner := false
	foundRepo := false
	for _, arg := range args {
		if arg == "--github-owner=upstream-org" {
			foundOwner = true
		}
		if arg == "--github-repo=upstream-repo" {
			foundRepo = true
		}
	}
	if !foundOwner {
		t.Errorf("expected --github-owner=upstream-org, got args: %v", args)
	}
	if !foundRepo {
		t.Errorf("expected --github-repo=upstream-repo, got args: %v", args)
	}

	// Verify it's NOT using the fork owner
	for _, arg := range args {
		if arg == "--github-owner=my-fork" {
			t.Errorf("should not use fork owner, got args: %v", args)
		}
	}
}

func TestBuildDeploymentWithGitHubIssuesRepoOverrideEnterprise(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{
					Repo: "https://github.example.com/upstream-org/upstream-repo.git",
				},
			},
		},
	}
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/my-fork/upstream-repo.git",
	}

	deploy := builder.Build(ts, workspace, false)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	foundAPIBaseURL := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--github-api-base-url=") {
			foundAPIBaseURL = arg
		}
	}
	want := "--github-api-base-url=https://github.example.com/api/v3"
	if foundAPIBaseURL != want {
		t.Errorf("got %q, want %q", foundAPIBaseURL, want)
	}
}

func TestBuildDeploymentWithGitHubPullRequestsRepoOverride(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubPullRequests: &kelosv1alpha1.GitHubPullRequests{
					Repo: "https://github.com/upstream-org/upstream-repo.git",
				},
			},
		},
	}
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/my-fork/upstream-repo.git",
	}

	deploy := builder.Build(ts, workspace, false)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	foundOwner := false
	foundRepo := false
	for _, arg := range args {
		if arg == "--github-owner=upstream-org" {
			foundOwner = true
		}
		if arg == "--github-repo=upstream-repo" {
			foundRepo = true
		}
	}
	if !foundOwner {
		t.Errorf("expected --github-owner=upstream-org, got args: %v", args)
	}
	if !foundRepo {
		t.Errorf("expected --github-repo=upstream-repo, got args: %v", args)
	}

	for _, arg := range args {
		if arg == "--github-owner=my-fork" {
			t.Errorf("should not use fork owner, got args: %v", args)
		}
	}
}

func TestBuildDeploymentWithGitHubIssuesShorthandRepoOverridePreservesGHESHost(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{
					Repo: "upstream-org/upstream-repo",
				},
			},
		},
	}
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.example.com/my-fork/upstream-repo.git",
	}

	deploy := builder.Build(ts, workspace, false)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	foundOwner := false
	foundRepo := false
	foundAPIBaseURL := ""
	for _, arg := range args {
		if arg == "--github-owner=upstream-org" {
			foundOwner = true
		}
		if arg == "--github-repo=upstream-repo" {
			foundRepo = true
		}
		if strings.HasPrefix(arg, "--github-api-base-url=") {
			foundAPIBaseURL = arg
		}
	}
	if !foundOwner {
		t.Errorf("expected --github-owner=upstream-org, got args: %v", args)
	}
	if !foundRepo {
		t.Errorf("expected --github-repo=upstream-repo, got args: %v", args)
	}
	want := "--github-api-base-url=https://github.example.com/api/v3"
	if foundAPIBaseURL != want {
		t.Errorf("GHES host should be preserved from workspace; got %q, want %q", foundAPIBaseURL, want)
	}
}

func TestBuildDeploymentWithGitHubPullRequestsShorthandRepoOverridePreservesGHESHost(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubPullRequests: &kelosv1alpha1.GitHubPullRequests{
					Repo: "upstream-org/upstream-repo",
				},
			},
		},
	}
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.example.com/my-fork/upstream-repo.git",
	}

	deploy := builder.Build(ts, workspace, false)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	foundOwner := false
	foundRepo := false
	foundAPIBaseURL := ""
	for _, arg := range args {
		if arg == "--github-owner=upstream-org" {
			foundOwner = true
		}
		if arg == "--github-repo=upstream-repo" {
			foundRepo = true
		}
		if strings.HasPrefix(arg, "--github-api-base-url=") {
			foundAPIBaseURL = arg
		}
	}
	if !foundOwner {
		t.Errorf("expected --github-owner=upstream-org, got args: %v", args)
	}
	if !foundRepo {
		t.Errorf("expected --github-repo=upstream-repo, got args: %v", args)
	}
	want := "--github-api-base-url=https://github.example.com/api/v3"
	if foundAPIBaseURL != want {
		t.Errorf("GHES host should be preserved from workspace; got %q, want %q", foundAPIBaseURL, want)
	}
}

func TestBuildDeploymentWithFullURLOverrideReplacesGHESHost(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{
					Repo: "https://other-ghes.example.com/upstream-org/upstream-repo.git",
				},
			},
		},
	}
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.example.com/my-fork/upstream-repo.git",
	}

	deploy := builder.Build(ts, workspace, false)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	foundAPIBaseURL := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--github-api-base-url=") {
			foundAPIBaseURL = arg
		}
	}
	want := "--github-api-base-url=https://other-ghes.example.com/api/v3"
	if foundAPIBaseURL != want {
		t.Errorf("full URL override should replace GHES host; got %q, want %q", foundAPIBaseURL, want)
	}
}

func TestDeploymentBuilder_JiraNoJQL(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Jira: &kelosv1alpha1.Jira{
					BaseURL:   "https://jira.example.com",
					Project:   "TEST",
					SecretRef: kelosv1alpha1.SecretReference{Name: "jira-creds"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	deploy := builder.Build(ts, nil, false)
	spawner := deploy.Spec.Template.Spec.Containers[0]

	for _, arg := range spawner.Args {
		if arg == "--jira-jql=" || (len(arg) > 10 && arg[:10] == "--jira-jql") {
			t.Errorf("should not include --jira-jql arg when JQL is empty, got %q", arg)
		}
	}
}

func boolPtr(v bool) *bool { return &v }

func TestUpdateDeployment_SuspendScalesDown(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
			Suspend: boolPtr(true),
		},
	}

	// Build a deployment with replicas=1 (running state)
	deploy := builder.Build(ts, nil, false)
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 1 {
		t.Fatalf("expected initial Replicas=1, got %v", deploy.Spec.Replicas)
	}

	// Create a reconciler with a fake client
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, deploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	// Call updateDeployment with desiredReplicas=0 (suspended)
	ctx := context.Background()
	if err := r.updateDeployment(ctx, ts, deploy, nil, false, 0); err != nil {
		t.Fatalf("updateDeployment error: %v", err)
	}

	// Verify the deployment was updated to 0 replicas
	var updated appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKeyFromObject(deploy), &updated); err != nil {
		t.Fatalf("getting deployment: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Errorf("expected Replicas=0 after suspend, got %v", updated.Spec.Replicas)
	}
}

func TestUpdateDeployment_ResumeScalesUp(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
			Suspend: boolPtr(false),
		},
	}

	// Build a deployment with replicas=0 (suspended state)
	deploy := builder.Build(ts, nil, false)
	zero := int32(0)
	deploy.Spec.Replicas = &zero

	// Create a reconciler with a fake client
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, deploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	// Call updateDeployment with desiredReplicas=1 (resumed)
	ctx := context.Background()
	if err := r.updateDeployment(ctx, ts, deploy, nil, false, 1); err != nil {
		t.Fatalf("updateDeployment error: %v", err)
	}

	// Verify the deployment was updated to 1 replica
	var updated appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKeyFromObject(deploy), &updated); err != nil {
		t.Fatalf("getting deployment: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 1 {
		t.Errorf("expected Replicas=1 after resume, got %v", updated.Spec.Replicas)
	}
}

func TestUpdateDeployment_NoUpdateWhenReplicasMatch(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}

	// Build a deployment with replicas=1
	deploy := builder.Build(ts, nil, false)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, deploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	// Call updateDeployment with desiredReplicas=1 (no change needed)
	ctx := context.Background()
	if err := r.updateDeployment(ctx, ts, deploy, nil, false, 1); err != nil {
		t.Fatalf("updateDeployment error: %v", err)
	}

	// Verify the deployment still has 1 replica (no unnecessary update)
	var updated appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKeyFromObject(deploy), &updated); err != nil {
		t.Fatalf("getting deployment: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 1 {
		t.Errorf("expected Replicas=1 (unchanged), got %v", updated.Spec.Replicas)
	}
}

func TestUpdateDeployment_PATToGitHubApp(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	enableGitHubReporting(ts)
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "my-secret",
		},
	}

	// Build the initial deployment in PAT mode
	deploy := builder.Build(ts, workspace, false)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, deploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	// Verify initial state: PAT mode
	foundToken := false
	for _, env := range deploy.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "GITHUB_TOKEN" {
			foundToken = true
		}
	}
	if !foundToken {
		t.Fatal("expected GITHUB_TOKEN env var in PAT mode")
	}

	// Switch to GitHub App mode
	ctx := context.Background()
	if err := r.updateDeployment(ctx, ts, deploy, workspace, true, 1); err != nil {
		t.Fatalf("updateDeployment error: %v", err)
	}

	var updated appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKeyFromObject(deploy), &updated); err != nil {
		t.Fatalf("getting deployment: %v", err)
	}

	// Verify no GITHUB_TOKEN env var
	for _, env := range updated.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "GITHUB_TOKEN" {
			t.Error("spawner should not have GITHUB_TOKEN env var in GitHub App mode")
		}
	}

	// Verify GitHub App env vars are present
	envMap := make(map[string]corev1.EnvVar)
	for _, env := range updated.Spec.Template.Spec.Containers[0].Env {
		envMap[env.Name] = env
	}
	if _, ok := envMap["GITHUB_APP_ID"]; !ok {
		t.Error("expected GITHUB_APP_ID env var after switch to GitHub App")
	}
	if _, ok := envMap["GITHUB_APP_INSTALLATION_ID"]; !ok {
		t.Error("expected GITHUB_APP_INSTALLATION_ID env var after switch to GitHub App")
	}
	if _, ok := envMap["GITHUB_APP_PRIVATE_KEY"]; !ok {
		t.Error("expected GITHUB_APP_PRIVATE_KEY env var after switch to GitHub App")
	}

	// Verify --github-token-file arg is NOT present
	for _, arg := range updated.Spec.Template.Spec.Containers[0].Args {
		if arg == "--github-token-file=/shared/token/GITHUB_TOKEN" {
			t.Error("should not have --github-token-file arg in GitHub App mode")
		}
	}
}

func TestUpdateDeployment_GitHubAppToPAT(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	enableGitHubReporting(ts)
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "my-secret",
		},
	}

	// Build the initial deployment in GitHub App mode
	deploy := builder.Build(ts, workspace, true)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, deploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	// Verify initial state: GitHub App mode env vars
	initialEnvMap := make(map[string]bool)
	for _, env := range deploy.Spec.Template.Spec.Containers[0].Env {
		initialEnvMap[env.Name] = true
	}
	if !initialEnvMap["GITHUB_APP_ID"] {
		t.Fatal("expected GITHUB_APP_ID env var in GitHub App mode")
	}

	// Switch to PAT mode
	ctx := context.Background()
	if err := r.updateDeployment(ctx, ts, deploy, workspace, false, 1); err != nil {
		t.Fatalf("updateDeployment error: %v", err)
	}

	var updated appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKeyFromObject(deploy), &updated); err != nil {
		t.Fatalf("getting deployment: %v", err)
	}

	// Verify GITHUB_TOKEN env var is present
	foundToken := false
	for _, env := range updated.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "GITHUB_TOKEN" {
			foundToken = true
		}
		if env.Name == "GITHUB_APP_ID" || env.Name == "GITHUB_APP_INSTALLATION_ID" || env.Name == "GITHUB_APP_PRIVATE_KEY" {
			t.Errorf("should not have %s env var after switch to PAT", env.Name)
		}
	}
	if !foundToken {
		t.Error("expected GITHUB_TOKEN env var after switch to PAT")
	}

	// Verify --github-token-file arg is not present
	for _, arg := range updated.Spec.Template.Spec.Containers[0].Args {
		if arg == "--github-token-file=/shared/token/GITHUB_TOKEN" {
			t.Error("should not have --github-token-file arg after switch to PAT")
		}
	}
}

func TestFindTaskSpawnersForSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret",
			Namespace: "default",
		},
	}
	// Workspace referencing the secret
	ws := &kelosv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/kelos-dev/kelos.git",
			SecretRef: &kelosv1alpha1.SecretReference{
				Name: "my-secret",
			},
		},
	}
	// Workspace not referencing the secret
	wsOther := &kelosv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws-other",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/kelos-dev/other.git",
			SecretRef: &kelosv1alpha1.SecretReference{
				Name: "other-secret",
			},
		},
	}
	// TaskSpawner referencing ws (should be returned)
	ts1 := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner-1",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	// TaskSpawner referencing ws-other (should not be returned)
	ts2 := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner-2",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws-other"},
			},
		},
	}
	// TaskSpawner with no workspaceRef (should not be returned)
	ts3 := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner-3",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Jira: &kelosv1alpha1.Jira{
					BaseURL:   "https://jira.example.com",
					Project:   "TEST",
					SecretRef: kelosv1alpha1.SecretReference{Name: "jira-creds"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret, ws, wsOther, ts1, ts2, ts3).
		Build()

	r := &TaskSpawnerReconciler{
		Client: cl,
		Scheme: scheme,
	}

	ctx := context.Background()
	requests := r.findTaskSpawnersForSecret(ctx, secret)

	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d: %v", len(requests), requests)
	}
	if requests[0].Name != "spawner-1" {
		t.Errorf("expected request for spawner-1, got %q", requests[0].Name)
	}
}

func TestFindTaskSpawnersForWorkspace(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	ws := &kelosv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/kelos-dev/kelos.git",
		},
	}

	// TaskSpawner referencing ws (should be returned)
	ts1 := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner-1",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	// Another TaskSpawner referencing ws (should also be returned)
	ts2 := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner-2",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}
	// TaskSpawner referencing different workspace (should not be returned)
	ts3 := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner-3",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:         "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "other-ws"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ws, ts1, ts2, ts3).
		Build()

	r := &TaskSpawnerReconciler{
		Client: cl,
		Scheme: scheme,
	}

	ctx := context.Background()
	requests := r.findTaskSpawnersForWorkspace(ctx, ws)

	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d: %v", len(requests), requests)
	}

	names := map[string]bool{}
	for _, req := range requests {
		names[req.Name] = true
	}
	if !names["spawner-1"] {
		t.Error("expected request for spawner-1")
	}
	if !names["spawner-2"] {
		t.Error("expected request for spawner-2")
	}
	if names["spawner-3"] {
		t.Error("should not have request for spawner-3")
	}
}

func TestBuildCronJob_BasicSchedule(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weekly-update",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeAPIKey,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
				},
			},
		},
	}

	cronJob := builder.BuildCronJob(ts, nil, false)

	// Verify CronJob metadata
	if cronJob.Name != "weekly-update" {
		t.Errorf("expected name %q, got %q", "weekly-update", cronJob.Name)
	}
	if cronJob.Namespace != "default" {
		t.Errorf("expected namespace %q, got %q", "default", cronJob.Namespace)
	}

	// Verify schedule
	if cronJob.Spec.Schedule != "0 9 * * 1" {
		t.Errorf("expected schedule %q, got %q", "0 9 * * 1", cronJob.Spec.Schedule)
	}

	// Verify concurrency policy
	if cronJob.Spec.ConcurrencyPolicy != "Forbid" {
		t.Errorf("expected concurrency policy %q, got %q", "Forbid", cronJob.Spec.ConcurrencyPolicy)
	}

	// Verify container args include --one-shot
	if len(cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers))
	}

	spawner := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	if spawner.Name != "spawner" {
		t.Errorf("container name = %q, want %q", spawner.Name, "spawner")
	}

	foundOneShot := false
	foundName := false
	foundNamespace := false
	for _, arg := range spawner.Args {
		if arg == "--one-shot" {
			foundOneShot = true
		}
		if arg == "--taskspawner-name=weekly-update" {
			foundName = true
		}
		if arg == "--taskspawner-namespace=default" {
			foundNamespace = true
		}
	}
	if !foundOneShot {
		t.Errorf("expected --one-shot flag in args, got: %v", spawner.Args)
	}
	if !foundName {
		t.Errorf("expected --taskspawner-name arg, got: %v", spawner.Args)
	}
	if !foundNamespace {
		t.Errorf("expected --taskspawner-namespace arg, got: %v", spawner.Args)
	}

	// Verify restart policy is Never (for Job pods)
	if cronJob.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("expected restart policy %q, got %q", corev1.RestartPolicyNever, cronJob.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy)
	}

	// Verify service account
	if cronJob.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName != SpawnerServiceAccount {
		t.Errorf("expected service account %q, got %q", SpawnerServiceAccount, cronJob.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName)
	}

	// Verify labels
	if cronJob.Labels["kelos.dev/taskspawner"] != "weekly-update" {
		t.Errorf("expected label kelos.dev/taskspawner=weekly-update, got %q", cronJob.Labels["kelos.dev/taskspawner"])
	}

	// Verify history limits
	if cronJob.Spec.SuccessfulJobsHistoryLimit == nil || *cronJob.Spec.SuccessfulJobsHistoryLimit != 3 {
		t.Errorf("expected SuccessfulJobsHistoryLimit=3, got %v", cronJob.Spec.SuccessfulJobsHistoryLimit)
	}
	if cronJob.Spec.FailedJobsHistoryLimit == nil || *cronJob.Spec.FailedJobsHistoryLimit != 1 {
		t.Errorf("expected FailedJobsHistoryLimit=1, got %v", cronJob.Spec.FailedJobsHistoryLimit)
	}
}

func TestBuildCronJob_BackoffLimit(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cron",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "*/5 * * * *",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	cronJob := builder.BuildCronJob(ts, nil, false)

	// Backoff limit should be 0 (no retries for one-shot)
	if cronJob.Spec.JobTemplate.Spec.BackoffLimit == nil || *cronJob.Spec.JobTemplate.Spec.BackoffLimit != 0 {
		t.Errorf("expected BackoffLimit=0, got %v", cronJob.Spec.JobTemplate.Spec.BackoffLimit)
	}
}

func TestIsCronBased(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeAPIKey,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
				},
			},
		},
	}

	// Verify isCronBased works correctly
	if !isCronBased(ts) {
		t.Error("Expected isCronBased to return true for cron TaskSpawner")
	}

	// Verify non-cron TaskSpawner returns false
	nonCronTS := &kelosv1alpha1.TaskSpawner{
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
		},
	}
	if isCronBased(nonCronTS) {
		t.Error("Expected isCronBased to return false for GitHub TaskSpawner")
	}

	_ = builder // Use the builder (tests the compilation)
}

func TestUpdateCronJob_ScheduleChange(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 10 * * 1", // Changed from 9 to 10
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	// Build the original CronJob with old schedule
	oldTS := ts.DeepCopy()
	oldTS.Spec.When.Cron.Schedule = "0 9 * * 1"
	cronJob := builder.BuildCronJob(oldTS, nil, false)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, cronJob).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	if err := r.updateCronJob(ctx, ts, cronJob, nil, false, false); err != nil {
		t.Fatalf("updateCronJob error: %v", err)
	}

	// Verify schedule was updated
	if cronJob.Spec.Schedule != "0 10 * * 1" {
		t.Errorf("expected schedule %q, got %q", "0 10 * * 1", cronJob.Spec.Schedule)
	}
}

func TestUpdateCronJob_SuspendToggle(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
			Suspend: boolPtr(true),
		},
	}

	cronJob := builder.BuildCronJob(ts, nil, false)
	notSuspended := false
	cronJob.Spec.Suspend = &notSuspended // Currently not suspended

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, cronJob).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	if err := r.updateCronJob(ctx, ts, cronJob, nil, false, true); err != nil {
		t.Fatalf("updateCronJob error: %v", err)
	}

	// Verify suspend was set to true
	if cronJob.Spec.Suspend == nil || !*cronJob.Spec.Suspend {
		t.Error("expected CronJob to be suspended")
	}
}

func TestUpdateCronJob_PodSpecChanges(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	cronJob := builder.BuildCronJob(ts, nil, false)

	scheme := newTestScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, cronJob).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	// Mutate the CronJob to simulate drift: wrong image, extra volume, extra init container
	podSpec := &cronJob.Spec.JobTemplate.Spec.Template.Spec
	podSpec.Containers[0].Image = "old-image:v1"
	podSpec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "stale", MountPath: "/stale"}}
	podSpec.InitContainers = []corev1.Container{{Name: "stale-init", Image: "stale:v1"}}
	podSpec.Volumes = []corev1.Volume{{Name: "stale", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}

	ctx := context.Background()
	if err := r.updateCronJob(ctx, ts, cronJob, nil, false, false); err != nil {
		t.Fatalf("updateCronJob error: %v", err)
	}

	// Verify image was corrected
	if podSpec.Containers[0].Image != DefaultSpawnerImage {
		t.Errorf("expected image %q, got %q", DefaultSpawnerImage, podSpec.Containers[0].Image)
	}

	// Verify stale volume mounts were removed (cron with no workspace has none)
	if len(podSpec.Containers[0].VolumeMounts) != 0 {
		t.Errorf("expected 0 volume mounts, got %d", len(podSpec.Containers[0].VolumeMounts))
	}

	// Verify stale init containers were removed
	if len(podSpec.InitContainers) != 0 {
		t.Errorf("expected 0 init containers, got %d", len(podSpec.InitContainers))
	}

	// Verify stale volumes were removed
	if len(podSpec.Volumes) != 0 {
		t.Errorf("expected 0 volumes, got %d", len(podSpec.Volumes))
	}
}

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
	return scheme
}

func TestReconcileCronJob_DeletesStaleDeployment(t *testing.T) {
	builder := NewDeploymentBuilder()
	scheme := newTestScheme()

	// A TaskSpawner that was previously polling-based but is now cron-based.
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-spawner",
			Namespace:  "default",
			Finalizers: []string{taskSpawnerFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	// The stale Deployment left over from the previous polling configuration.
	staleDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "kelos.dev/v1alpha1",
					Kind:       "TaskSpawner",
					Name:       "my-spawner",
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "spawner"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "spawner"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "spawner", Image: "test"}}},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, staleDeploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-spawner", Namespace: "default"}}
	_, err := r.reconcileCronJob(ctx, req, ts, false)
	if err != nil {
		t.Fatalf("reconcileCronJob error: %v", err)
	}

	// Verify the stale Deployment was deleted.
	var deploy appsv1.Deployment
	err = cl.Get(ctx, types.NamespacedName{Name: "my-spawner", Namespace: "default"}, &deploy)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected stale Deployment to be deleted, but Get returned: %v", err)
	}

	// Verify a CronJob was created.
	var cronJob batchv1.CronJob
	if err := cl.Get(ctx, types.NamespacedName{Name: "my-spawner", Namespace: "default"}, &cronJob); err != nil {
		t.Errorf("expected CronJob to be created, got error: %v", err)
	}
}

func TestReconcileDeployment_DeletesStaleCronJob(t *testing.T) {
	builder := NewDeploymentBuilder()
	scheme := newTestScheme()

	// A TaskSpawner that was previously cron-based but is now polling-based.
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-spawner",
			Namespace:  "default",
			Finalizers: []string{taskSpawnerFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	// The stale CronJob left over from the previous cron configuration.
	staleCronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "kelos.dev/v1alpha1",
					Kind:       "TaskSpawner",
					Name:       "my-spawner",
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 9 * * 1",
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers:    []corev1.Container{{Name: "spawner", Image: "test"}},
							RestartPolicy: corev1.RestartPolicyNever,
						},
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, staleCronJob).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-spawner", Namespace: "default"}}
	_, err := r.reconcileDeployment(ctx, req, ts, false)
	if err != nil {
		t.Fatalf("reconcileDeployment error: %v", err)
	}

	// Verify the stale CronJob was deleted.
	var cronJob batchv1.CronJob
	err = cl.Get(ctx, types.NamespacedName{Name: "my-spawner", Namespace: "default"}, &cronJob)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected stale CronJob to be deleted, but Get returned: %v", err)
	}

	// Verify a Deployment was created.
	var deploy appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Name: "my-spawner", Namespace: "default"}, &deploy); err != nil {
		t.Errorf("expected Deployment to be created, got error: %v", err)
	}
}

func TestBuildCronJob_WithWorkspacePAT(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-with-workspace",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "my-workspace",
				},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/myorg/myrepo",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "gh-pat-secret",
		},
	}

	cronJob := builder.BuildCronJob(ts, workspace, false)

	// Verify GitHub args are present
	spawner := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	foundOwner := false
	foundRepo := false
	foundOneShot := false
	for _, arg := range spawner.Args {
		if arg == "--github-owner=myorg" {
			foundOwner = true
		}
		if arg == "--github-repo=myrepo" {
			foundRepo = true
		}
		if arg == "--one-shot" {
			foundOneShot = true
		}
	}
	if !foundOwner {
		t.Errorf("expected --github-owner=myorg in args, got: %v", spawner.Args)
	}
	if !foundRepo {
		t.Errorf("expected --github-repo=myrepo in args, got: %v", spawner.Args)
	}
	if !foundOneShot {
		t.Errorf("expected --one-shot in args, got: %v", spawner.Args)
	}

	for _, env := range spawner.Env {
		if env.Name == "GITHUB_TOKEN" {
			t.Fatalf("expected no GITHUB_TOKEN env for cron workspace discovery-only spawner, got: %v", spawner.Env)
		}
	}
}

func TestBuildCronJob_WithWorkspaceGitHubApp(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-github-app",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "my-workspace",
				},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/myorg/myrepo",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "gh-app-secret",
		},
	}

	cronJob := builder.BuildCronJob(ts, workspace, true)
	podSpec := cronJob.Spec.JobTemplate.Spec.Template.Spec

	if len(podSpec.InitContainers) != 0 {
		t.Fatalf("expected no init containers for cron workspace discovery-only spawner, got %d", len(podSpec.InitContainers))
	}
	if len(podSpec.Volumes) != 0 {
		t.Fatalf("expected no volumes for cron workspace discovery-only spawner, got %d", len(podSpec.Volumes))
	}

	spawner := podSpec.Containers[0]
	for _, arg := range spawner.Args {
		if arg == "--github-token-file=/shared/token/GITHUB_TOKEN" {
			t.Fatalf("expected no github token file arg for cron workspace discovery-only spawner, got: %v", spawner.Args)
		}
	}
	for _, vm := range spawner.VolumeMounts {
		if vm.Name == "github-token" {
			t.Fatalf("expected no github-token volume mount for cron workspace discovery-only spawner, got: %v", spawner.VolumeMounts)
		}
	}
}

func TestReconcileCronJob_ClearsStaleDeploymentName(t *testing.T) {
	builder := NewDeploymentBuilder()
	scheme := newTestScheme()

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-spawner",
			Namespace:  "default",
			Finalizers: []string{taskSpawnerFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
		Status: kelosv1alpha1.TaskSpawnerStatus{
			DeploymentName: "my-spawner",
			Phase:          kelosv1alpha1.TaskSpawnerPhaseRunning,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-spawner", Namespace: "default"}}
	_, err := r.reconcileCronJob(ctx, req, ts, false)
	if err != nil {
		t.Fatalf("reconcileCronJob error: %v", err)
	}

	// Re-fetch to see status updates
	var updated kelosv1alpha1.TaskSpawner
	if err := cl.Get(ctx, req.NamespacedName, &updated); err != nil {
		t.Fatalf("failed to get updated TaskSpawner: %v", err)
	}

	if updated.Status.CronJobName != "my-spawner" {
		t.Errorf("expected CronJobName=%q, got %q", "my-spawner", updated.Status.CronJobName)
	}
	if updated.Status.DeploymentName != "" {
		t.Errorf("expected DeploymentName to be cleared, got %q", updated.Status.DeploymentName)
	}
}

func TestReconcileDeployment_ClearsStaleCronJobName(t *testing.T) {
	builder := NewDeploymentBuilder()
	scheme := newTestScheme()

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-spawner",
			Namespace:  "default",
			Finalizers: []string{taskSpawnerFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
		Status: kelosv1alpha1.TaskSpawnerStatus{
			CronJobName: "my-spawner",
			Phase:       kelosv1alpha1.TaskSpawnerPhaseRunning,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-spawner", Namespace: "default"}}
	_, err := r.reconcileDeployment(ctx, req, ts, false)
	if err != nil {
		t.Fatalf("reconcileDeployment error: %v", err)
	}

	// Re-fetch to see status updates
	var updated kelosv1alpha1.TaskSpawner
	if err := cl.Get(ctx, req.NamespacedName, &updated); err != nil {
		t.Fatalf("failed to get updated TaskSpawner: %v", err)
	}

	if updated.Status.DeploymentName != "my-spawner" {
		t.Errorf("expected DeploymentName=%q, got %q", "my-spawner", updated.Status.DeploymentName)
	}
	if updated.Status.CronJobName != "" {
		t.Errorf("expected CronJobName to be cleared, got %q", updated.Status.CronJobName)
	}
}

func TestParseResourceList(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		wantErr bool
		check   func(t *testing.T, rl corev1.ResourceList)
	}{
		{
			name:    "empty string returns nil",
			input:   "",
			wantNil: true,
		},
		{
			name:    "whitespace-only returns nil",
			input:   "  ",
			wantNil: true,
		},
		{
			name:  "valid cpu and memory",
			input: "cpu=250m,memory=512Mi",
			check: func(t *testing.T, rl corev1.ResourceList) {
				if cpu, ok := rl[corev1.ResourceCPU]; !ok || cpu.String() != "250m" {
					t.Errorf("expected cpu=250m, got %v", cpu)
				}
				if mem, ok := rl[corev1.ResourceMemory]; !ok || mem.String() != "512Mi" {
					t.Errorf("expected memory=512Mi, got %v", mem)
				}
			},
		},
		{
			name:  "single resource",
			input: "cpu=1",
			check: func(t *testing.T, rl corev1.ResourceList) {
				if len(rl) != 1 {
					t.Errorf("expected 1 resource, got %d", len(rl))
				}
			},
		},
		{
			name:  "ephemeral storage",
			input: "ephemeral-storage=2Gi",
			check: func(t *testing.T, rl corev1.ResourceList) {
				if _, ok := rl[corev1.ResourceEphemeralStorage]; !ok {
					t.Error("expected ephemeral-storage to be present")
				}
			},
		},
		{
			name:    "missing value",
			input:   "cpu=",
			wantErr: true,
		},
		{
			name:    "missing name",
			input:   "=250m",
			wantErr: true,
		},
		{
			name:    "no equals sign",
			input:   "cpu",
			wantErr: true,
		},
		{
			name:    "invalid quantity",
			input:   "cpu=notaquantity",
			wantErr: true,
		},
		{
			name:  "multiple resources with spaces",
			input: " cpu=100m , memory=256Mi ",
			check: func(t *testing.T, rl corev1.ResourceList) {
				if len(rl) != 2 {
					t.Errorf("expected 2 resources, got %d", len(rl))
				}
			},
		},
		{
			name:  "spaces around equals sign are trimmed",
			input: "cpu = 250m , memory = 512Mi",
			check: func(t *testing.T, rl corev1.ResourceList) {
				if cpu, ok := rl[corev1.ResourceCPU]; !ok || cpu.String() != "250m" {
					t.Errorf("expected cpu=250m, got %v", cpu)
				}
				if mem, ok := rl[corev1.ResourceMemory]; !ok || mem.String() != "512Mi" {
					t.Errorf("expected memory=512Mi, got %v", mem)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl, err := ParseResourceList(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if rl != nil {
					t.Errorf("expected nil, got %v", rl)
				}
				return
			}
			if tt.check != nil {
				tt.check(t, rl)
			}
		})
	}
}

func TestDeploymentBuilder_SpawnerResources(t *testing.T) {
	builder := NewDeploymentBuilder()
	builder.SpawnerResources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("250m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
		},
	}

	deploy := builder.Build(ts, nil, false)
	spawner := deploy.Spec.Template.Spec.Containers[0]
	if spawner.Resources.Requests.Cpu().String() != "250m" {
		t.Errorf("expected cpu request 250m, got %s", spawner.Resources.Requests.Cpu().String())
	}
	if spawner.Resources.Limits.Memory().String() != "1Gi" {
		t.Errorf("expected memory limit 1Gi, got %s", spawner.Resources.Limits.Memory().String())
	}
}

func TestDeploymentBuilder_SpawnerResources_NilPreservesDefault(t *testing.T) {
	builder := NewDeploymentBuilder()
	// SpawnerResources is nil by default

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
		},
	}

	deploy := builder.Build(ts, nil, false)
	spawner := deploy.Spec.Template.Spec.Containers[0]
	if len(spawner.Resources.Requests) != 0 || len(spawner.Resources.Limits) != 0 {
		t.Errorf("expected empty resources when SpawnerResources is nil, got requests=%v limits=%v", spawner.Resources.Requests, spawner.Resources.Limits)
	}
}

func TestDeploymentBuilder_CronJob_SpawnerResources(t *testing.T) {
	builder := NewDeploymentBuilder()
	builder.SpawnerResources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("100m"),
		},
	}

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "*/5 * * * *",
				},
			},
		},
	}

	cronJob := builder.BuildCronJob(ts, nil, false)
	spawner := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	if spawner.Resources.Requests.Cpu().String() != "100m" {
		t.Errorf("expected cpu request 100m on CronJob, got %s", spawner.Resources.Requests.Cpu().String())
	}
}

func TestUpdateDeployment_ResourcesDrift(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
		},
	}

	// Build a deployment with no resources
	deploy := builder.Build(ts, nil, false)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, deploy).
		WithStatusSubresource(ts).
		Build()

	// Now update the builder to have resources
	builder.SpawnerResources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("250m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	if err := r.updateDeployment(ctx, ts, deploy, nil, false, 1); err != nil {
		t.Fatalf("updateDeployment error: %v", err)
	}

	var updated appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKeyFromObject(deploy), &updated); err != nil {
		t.Fatalf("getting deployment: %v", err)
	}

	spawner := updated.Spec.Template.Spec.Containers[0]
	if spawner.Resources.Requests.Cpu().String() != "250m" {
		t.Errorf("expected cpu request 250m after drift update, got %s", spawner.Resources.Requests.Cpu().String())
	}
}

func TestUpdateCronJob_ResourcesDrift(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				Cron: &kelosv1alpha1.Cron{
					Schedule: "*/5 * * * *",
				},
			},
		},
	}

	// Build a CronJob with no resources
	cronJob := builder.BuildCronJob(ts, nil, false)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, cronJob).
		WithStatusSubresource(ts).
		Build()

	// Now update the builder to have resources
	builder.SpawnerResources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("100m"),
		},
	}

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	if err := r.updateCronJob(ctx, ts, cronJob, nil, false, false); err != nil {
		t.Fatalf("updateCronJob error: %v", err)
	}

	var updated batchv1.CronJob
	if err := cl.Get(ctx, client.ObjectKeyFromObject(cronJob), &updated); err != nil {
		t.Fatalf("getting CronJob: %v", err)
	}

	spawner := updated.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	if spawner.Resources.Requests.Cpu().String() != "100m" {
		t.Errorf("expected cpu request 100m after drift update, got %s", spawner.Resources.Requests.Cpu().String())
	}
}

func TestUpdateDeployment_PortsDrift(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
		},
	}

	// Build a deployment, then strip its ports to simulate a pre-existing deploy
	deploy := builder.Build(ts, nil, false)
	deploy.Spec.Template.Spec.Containers[0].Ports = nil

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, deploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	if err := r.updateDeployment(ctx, ts, deploy, nil, false, 1); err != nil {
		t.Fatalf("updateDeployment error: %v", err)
	}

	var updated appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKeyFromObject(deploy), &updated); err != nil {
		t.Fatalf("getting deployment: %v", err)
	}

	spawner := updated.Spec.Template.Spec.Containers[0]
	if len(spawner.Ports) != 1 {
		t.Fatalf("expected 1 port after drift update, got %d", len(spawner.Ports))
	}
	if spawner.Ports[0].Name != "metrics" || spawner.Ports[0].ContainerPort != 8080 {
		t.Errorf("expected metrics port 8080, got %s:%d", spawner.Ports[0].Name, spawner.Ports[0].ContainerPort)
	}
}

func TestDeploymentBuilder_MetricsPort(t *testing.T) {
	builder := NewDeploymentBuilder()
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
		},
	}

	deploy := builder.Build(ts, nil, false)
	spawner := deploy.Spec.Template.Spec.Containers[0]

	if len(spawner.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(spawner.Ports))
	}
	port := spawner.Ports[0]
	if port.Name != "metrics" {
		t.Errorf("expected port name 'metrics', got %q", port.Name)
	}
	if port.ContainerPort != 8080 {
		t.Errorf("expected container port 8080, got %d", port.ContainerPort)
	}
	if port.Protocol != corev1.ProtocolTCP {
		t.Errorf("expected protocol TCP, got %s", port.Protocol)
	}
}

func TestReconcileDeployment_DeletesDeploymentWithOldSelectorLabels(t *testing.T) {
	builder := NewDeploymentBuilder()
	scheme := newTestScheme()

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-spawner",
			Namespace:  "default",
			Finalizers: []string{taskSpawnerFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	// A Deployment with old app.kubernetes.io/* selector labels.
	oldLabels := map[string]string{
		"app.kubernetes.io/name":       "kelos",
		"app.kubernetes.io/component":  "spawner",
		"app.kubernetes.io/managed-by": "kelos-controller",
		"kelos.dev/taskspawner":        "my-spawner",
	}
	oldDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
			Labels:    oldLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: oldLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: oldLabels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "spawner", Image: "test"}}},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, oldDeploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-spawner", Namespace: "default"}}
	_, err := r.reconcileDeployment(ctx, req, ts, false)
	if err != nil {
		t.Fatalf("reconcileDeployment error: %v", err)
	}

	// Verify a new Deployment was created with kelos.dev/* labels.
	var deploy appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Name: "my-spawner", Namespace: "default"}, &deploy); err != nil {
		t.Fatalf("expected Deployment to be recreated, got error: %v", err)
	}
	if _, ok := deploy.Spec.Selector.MatchLabels["app.kubernetes.io/component"]; ok {
		t.Errorf("expected old app.kubernetes.io/component label to be gone from selector")
	}
	if _, ok := deploy.Spec.Selector.MatchLabels["kelos.dev/component"]; !ok {
		t.Errorf("expected new kelos.dev/component label in selector")
	}
}

func TestReconcileDeployment_KeepsDeploymentWithNewLabels(t *testing.T) {
	builder := NewDeploymentBuilder()
	scheme := newTestScheme()

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "my-spawner",
			Namespace:  "default",
			Finalizers: []string{taskSpawnerFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubIssues: &kelosv1alpha1.GitHubIssues{},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
			},
		},
	}

	// A Deployment that already has the new kelos.dev/* labels.
	deploy := builder.Build(ts, nil, false)

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ts, deploy).
		WithStatusSubresource(ts).
		Build()

	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: builder,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-spawner", Namespace: "default"}}
	_, err := r.reconcileDeployment(ctx, req, ts, false)
	if err != nil {
		t.Fatalf("reconcileDeployment error: %v", err)
	}

	// Verify the Deployment still exists and has the correct labels.
	var updated appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Name: "my-spawner", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("expected Deployment to exist, got error: %v", err)
	}
	if _, ok := updated.Spec.Selector.MatchLabels["kelos.dev/component"]; !ok {
		t.Errorf("expected kelos.dev/component label in selector")
	}
}
