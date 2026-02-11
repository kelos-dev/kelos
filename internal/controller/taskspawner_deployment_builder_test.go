package controller

import (
	"testing"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			repoURL:   "https://github.com/axon-core/axon.git",
			wantOwner: "axon-core",
			wantRepo:  "axon",
		},
		{
			name:      "HTTPS URL without .git",
			repoURL:   "https://github.com/axon-core/axon",
			wantOwner: "axon-core",
			wantRepo:  "axon",
		},
		{
			name:      "HTTPS URL with trailing slash",
			repoURL:   "https://github.com/axon-core/axon/",
			wantOwner: "axon-core",
			wantRepo:  "axon",
		},
		{
			name:      "SSH URL",
			repoURL:   "git@github.com:axon-core/axon.git",
			wantOwner: "axon-core",
			wantRepo:  "axon",
		},
		{
			name:      "SSH URL without .git",
			repoURL:   "git@github.com:axon-core/axon",
			wantOwner: "axon-core",
			wantRepo:  "axon",
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
			repoURL:   "https://github.com/axon-core/axon.git",
			wantHost:  "github.com",
			wantOwner: "axon-core",
			wantRepo:  "axon",
		},
		{
			name:      "github.com SSH",
			repoURL:   "git@github.com:axon-core/axon.git",
			wantHost:  "github.com",
			wantOwner: "axon-core",
			wantRepo:  "axon",
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

	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				GitHubIssues: &axonv1alpha1.GitHubIssues{},
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
			repoURL:           "https://github.com/axon-core/axon.git",
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
			workspace := &axonv1alpha1.WorkspaceSpec{
				Repo: tt.repoURL,
			}
			dep := builder.Build(ts, workspace)
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

func TestParseBitbucketDCRepo(t *testing.T) {
	tests := []struct {
		name        string
		repoURL     string
		wantBaseURL string
		wantProject string
		wantRepo    string
	}{
		{
			name:        "SCM clone URL",
			repoURL:     "https://bitbucket.example.com/scm/PROJ/my-repo.git",
			wantBaseURL: "https://bitbucket.example.com",
			wantProject: "PROJ",
			wantRepo:    "my-repo",
		},
		{
			name:        "SCM clone URL without .git",
			repoURL:     "https://bitbucket.example.com/scm/PROJ/my-repo",
			wantBaseURL: "https://bitbucket.example.com",
			wantProject: "PROJ",
			wantRepo:    "my-repo",
		},
		{
			name:        "Browse-style URL",
			repoURL:     "https://bitbucket.example.com/projects/PROJ/repos/my-repo",
			wantBaseURL: "https://bitbucket.example.com",
			wantProject: "PROJ",
			wantRepo:    "my-repo",
		},
		{
			name:        "SSH URL",
			repoURL:     "git@bitbucket.example.com:PROJ/my-repo.git",
			wantBaseURL: "https://bitbucket.example.com",
			wantProject: "PROJ",
			wantRepo:    "my-repo",
		},
		{
			name:        "SSH URL with port",
			repoURL:     "ssh://git@bitbucket.example.com:7999/PROJ/my-repo.git",
			wantBaseURL: "https://bitbucket.example.com",
			wantProject: "PROJ",
			wantRepo:    "my-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, project, repo := parseBitbucketDCRepo(tt.repoURL)
			if baseURL != tt.wantBaseURL {
				t.Errorf("baseURL = %q, want %q", baseURL, tt.wantBaseURL)
			}
			if project != tt.wantProject {
				t.Errorf("project = %q, want %q", project, tt.wantProject)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

func TestBuildDeploymentWithBitbucketDC(t *testing.T) {
	builder := NewDeploymentBuilder()

	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner-bb",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				BitbucketDataCenterPRs: &axonv1alpha1.BitbucketDataCenterPRs{
					State: "OPEN",
				},
			},
		},
	}

	workspace := &axonv1alpha1.WorkspaceSpec{
		Repo: "https://bitbucket.example.com/scm/PROJ/my-repo.git",
		SecretRef: &axonv1alpha1.SecretReference{
			Name: "bb-token",
		},
	}

	dep := builder.Build(ts, workspace)
	args := dep.Spec.Template.Spec.Containers[0].Args
	env := dep.Spec.Template.Spec.Containers[0].Env

	// Check args
	expectedArgs := map[string]bool{
		"--taskspawner-name=test-spawner-bb":                    false,
		"--taskspawner-namespace=default":                       false,
		"--bitbucket-dc-base-url=https://bitbucket.example.com": false,
		"--bitbucket-dc-project=PROJ":                           false,
		"--bitbucket-dc-repo=my-repo":                           false,
	}
	for _, arg := range args {
		if _, ok := expectedArgs[arg]; ok {
			expectedArgs[arg] = true
		}
	}
	for arg, found := range expectedArgs {
		if !found {
			t.Errorf("Expected arg %q not found in %v", arg, args)
		}
	}

	// Should NOT have github-related args
	for _, arg := range args {
		if len(arg) > 15 && arg[:15] == "--github-owner=" {
			t.Errorf("Unexpected github-owner arg: %s", arg)
		}
	}

	// Check env
	if len(env) != 1 {
		t.Fatalf("Expected 1 env var, got %d", len(env))
	}
	if env[0].Name != "BITBUCKET_TOKEN" {
		t.Errorf("Expected env var BITBUCKET_TOKEN, got %s", env[0].Name)
	}
	if env[0].ValueFrom.SecretKeyRef.Name != "bb-token" {
		t.Errorf("Expected secret name bb-token, got %s", env[0].ValueFrom.SecretKeyRef.Name)
	}
	if env[0].ValueFrom.SecretKeyRef.Key != "BITBUCKET_TOKEN" {
		t.Errorf("Expected secret key BITBUCKET_TOKEN, got %s", env[0].ValueFrom.SecretKeyRef.Key)
	}
}
