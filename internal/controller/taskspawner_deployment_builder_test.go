package controller

import (
	"context"
	"strings"
	"testing"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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
