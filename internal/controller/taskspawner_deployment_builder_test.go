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

	if len(deploy.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(deploy.Spec.Template.Spec.InitContainers))
	}

	spawner := deploy.Spec.Template.Spec.Containers[0]
	refresher := deploy.Spec.Template.Spec.InitContainers[0]

	if spawner.Name != "spawner" {
		t.Errorf("container name = %q, want %q", spawner.Name, "spawner")
	}
	if refresher.Name != "token-refresher" {
		t.Errorf("init container name = %q, want %q", refresher.Name, "token-refresher")
	}

	if refresher.RestartPolicy == nil || *refresher.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Errorf("token-refresher RestartPolicy = %v, want %q", refresher.RestartPolicy, corev1.ContainerRestartPolicyAlways)
	}

	found := false
	for _, arg := range spawner.Args {
		if arg == "--github-token-file=/shared/token/GITHUB_TOKEN" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("spawner args missing --github-token-file flag: %v", spawner.Args)
	}

	for _, env := range spawner.Env {
		if env.Name == "GITHUB_TOKEN" {
			t.Error("spawner should not have GITHUB_TOKEN env var in GitHub App mode")
		}
	}

	if len(deploy.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(deploy.Spec.Template.Spec.Volumes))
	}

	if len(refresher.Env) != 2 {
		t.Fatalf("token-refresher expected 2 env vars, got %d", len(refresher.Env))
	}
	if refresher.Env[0].Name != "APP_ID" {
		t.Errorf("first env var = %q, want %q", refresher.Env[0].Name, "APP_ID")
	}
	if refresher.Env[1].Name != "INSTALLATION_ID" {
		t.Errorf("second env var = %q, want %q", refresher.Env[1].Name, "INSTALLATION_ID")
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
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.example.com/my-org/my-repo.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-app-creds",
		},
	}

	deploy := builder.Build(ts, workspace, true)

	refresher := deploy.Spec.Template.Spec.InitContainers[0]

	// Enterprise host should have 3 env vars: APP_ID, INSTALLATION_ID, GITHUB_API_BASE_URL
	if len(refresher.Env) != 3 {
		t.Fatalf("token-refresher expected 3 env vars for enterprise, got %d: %v", len(refresher.Env), refresher.Env)
	}

	apiBaseURLEnv := refresher.Env[2]
	if apiBaseURLEnv.Name != "GITHUB_API_BASE_URL" {
		t.Errorf("third env var = %q, want %q", apiBaseURLEnv.Name, "GITHUB_API_BASE_URL")
	}
	if apiBaseURLEnv.Value != "https://github.example.com/api/v3" {
		t.Errorf("GITHUB_API_BASE_URL = %q, want %q", apiBaseURLEnv.Value, "https://github.example.com/api/v3")
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
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-app-creds",
		},
	}

	deploy := builder.Build(ts, workspace, true)

	refresher := deploy.Spec.Template.Spec.InitContainers[0]

	// github.com host should have only 2 env vars: APP_ID, INSTALLATION_ID (no GITHUB_API_BASE_URL)
	if len(refresher.Env) != 2 {
		t.Fatalf("token-refresher expected 2 env vars for github.com, got %d: %v", len(refresher.Env), refresher.Env)
	}
	for _, env := range refresher.Env {
		if env.Name == "GITHUB_API_BASE_URL" {
			t.Error("token-refresher should not have GITHUB_API_BASE_URL for github.com")
		}
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

	// Verify initial state: PAT mode (no init containers, no volumes)
	if len(deploy.Spec.Template.Spec.InitContainers) != 0 {
		t.Fatalf("expected 0 init containers in PAT mode, got %d", len(deploy.Spec.Template.Spec.InitContainers))
	}
	if len(deploy.Spec.Template.Spec.Volumes) != 0 {
		t.Fatalf("expected 0 volumes in PAT mode, got %d", len(deploy.Spec.Template.Spec.Volumes))
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

	// Verify GitHub App mode: init container, volumes, volume mounts added
	if len(updated.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container after switch to GitHub App, got %d", len(updated.Spec.Template.Spec.InitContainers))
	}
	if updated.Spec.Template.Spec.InitContainers[0].Name != "token-refresher" {
		t.Errorf("init container name = %q, want %q", updated.Spec.Template.Spec.InitContainers[0].Name, "token-refresher")
	}
	if len(updated.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("expected 2 volumes after switch to GitHub App, got %d", len(updated.Spec.Template.Spec.Volumes))
	}
	if len(updated.Spec.Template.Spec.Containers[0].VolumeMounts) != 1 {
		t.Fatalf("expected 1 volume mount on spawner after switch to GitHub App, got %d", len(updated.Spec.Template.Spec.Containers[0].VolumeMounts))
	}

	// Verify no GITHUB_TOKEN env var (should use token file instead)
	for _, env := range updated.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "GITHUB_TOKEN" {
			t.Error("spawner should not have GITHUB_TOKEN env var in GitHub App mode")
		}
	}

	// Verify --github-token-file arg is present
	found := false
	for _, arg := range updated.Spec.Template.Spec.Containers[0].Args {
		if arg == "--github-token-file=/shared/token/GITHUB_TOKEN" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --github-token-file arg after switch to GitHub App, got args: %v", updated.Spec.Template.Spec.Containers[0].Args)
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

	// Verify initial state: GitHub App mode
	if len(deploy.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container in GitHub App mode, got %d", len(deploy.Spec.Template.Spec.InitContainers))
	}
	if len(deploy.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("expected 2 volumes in GitHub App mode, got %d", len(deploy.Spec.Template.Spec.Volumes))
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

	// Verify PAT mode: init containers, volumes, and volume mounts removed
	if len(updated.Spec.Template.Spec.InitContainers) != 0 {
		t.Errorf("expected 0 init containers after switch to PAT, got %d", len(updated.Spec.Template.Spec.InitContainers))
	}
	if len(updated.Spec.Template.Spec.Volumes) != 0 {
		t.Errorf("expected 0 volumes after switch to PAT, got %d", len(updated.Spec.Template.Spec.Volumes))
	}
	if len(updated.Spec.Template.Spec.Containers[0].VolumeMounts) != 0 {
		t.Errorf("expected 0 volume mounts on spawner after switch to PAT, got %d", len(updated.Spec.Template.Spec.Containers[0].VolumeMounts))
	}

	// Verify GITHUB_TOKEN env var is present
	foundToken := false
	for _, env := range updated.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "GITHUB_TOKEN" {
			foundToken = true
			break
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
					SecretRef: kelosv1alpha1.SecretReference{Name: "creds"},
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
					SecretRef: kelosv1alpha1.SecretReference{Name: "creds"},
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

	// Verify GITHUB_TOKEN env var is injected from PAT secret
	foundTokenEnv := false
	for _, env := range spawner.Env {
		if env.Name == "GITHUB_TOKEN" && env.ValueFrom != nil &&
			env.ValueFrom.SecretKeyRef != nil &&
			env.ValueFrom.SecretKeyRef.Name == "gh-pat-secret" {
			foundTokenEnv = true
		}
	}
	if !foundTokenEnv {
		t.Errorf("expected GITHUB_TOKEN env from secret gh-pat-secret, got: %v", spawner.Env)
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

	// Verify token-refresher init container is present
	if len(podSpec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container (token-refresher), got %d", len(podSpec.InitContainers))
	}
	if podSpec.InitContainers[0].Name != "token-refresher" {
		t.Errorf("expected init container name %q, got %q", "token-refresher", podSpec.InitContainers[0].Name)
	}

	// Verify volumes for GitHub App are present
	foundTokenVol := false
	foundSecretVol := false
	for _, vol := range podSpec.Volumes {
		if vol.Name == "github-token" {
			foundTokenVol = true
		}
		if vol.Name == "github-app-secret" {
			foundSecretVol = true
		}
	}
	if !foundTokenVol {
		t.Error("expected github-token volume")
	}
	if !foundSecretVol {
		t.Error("expected github-app-secret volume")
	}

	// Verify spawner container has token file arg and volume mount
	spawner := podSpec.Containers[0]
	foundTokenFileArg := false
	for _, arg := range spawner.Args {
		if arg == "--github-token-file=/shared/token/GITHUB_TOKEN" {
			foundTokenFileArg = true
		}
	}
	if !foundTokenFileArg {
		t.Errorf("expected --github-token-file arg, got: %v", spawner.Args)
	}

	foundTokenMount := false
	for _, vm := range spawner.VolumeMounts {
		if vm.Name == "github-token" && vm.MountPath == "/shared/token" {
			foundTokenMount = true
		}
	}
	if !foundTokenMount {
		t.Errorf("expected github-token volume mount, got: %v", spawner.VolumeMounts)
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
