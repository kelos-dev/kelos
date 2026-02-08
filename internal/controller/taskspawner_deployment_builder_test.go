package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
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

func TestDeploymentBuilderControllerImageAnnotation(t *testing.T) {
	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				Cron: &axonv1alpha1.Cron{Schedule: "0 9 * * 1"},
			},
		},
	}

	t.Run("annotation set when ControllerImage is provided", func(t *testing.T) {
		b := &DeploymentBuilder{
			SpawnerImage:    DefaultSpawnerImage,
			ControllerImage: "gjkim42/axon-controller:v1.0.0",
		}
		deploy := b.Build(ts, nil)
		ann := deploy.Spec.Template.Annotations
		if ann == nil {
			t.Fatal("expected pod template annotations to be set")
		}
		if got := ann[ControllerImageAnnotation]; got != "gjkim42/axon-controller:v1.0.0" {
			t.Errorf("annotation %q = %q, want %q", ControllerImageAnnotation, got, "gjkim42/axon-controller:v1.0.0")
		}
	})

	t.Run("no annotation when ControllerImage is empty", func(t *testing.T) {
		b := &DeploymentBuilder{
			SpawnerImage: DefaultSpawnerImage,
		}
		deploy := b.Build(ts, nil)
		ann := deploy.Spec.Template.Annotations
		if ann != nil {
			t.Errorf("expected nil pod template annotations, got %v", ann)
		}
	})
}

func TestDeploymentBuilderImagePullPolicy(t *testing.T) {
	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				Cron: &axonv1alpha1.Cron{Schedule: "0 9 * * 1"},
			},
		},
	}

	b := &DeploymentBuilder{
		SpawnerImage:           DefaultSpawnerImage,
		SpawnerImagePullPolicy: corev1.PullAlways,
	}
	deploy := b.Build(ts, nil)

	container := deploy.Spec.Template.Spec.Containers[0]
	if container.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("ImagePullPolicy = %q, want %q", container.ImagePullPolicy, corev1.PullAlways)
	}
}

func TestEqualAnnotations(t *testing.T) {
	tests := []struct {
		name string
		a    map[string]string
		b    map[string]string
		want bool
	}{
		{
			name: "both nil",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "both empty",
			a:    map[string]string{},
			b:    map[string]string{},
			want: true,
		},
		{
			name: "nil vs empty",
			a:    nil,
			b:    map[string]string{},
			want: true,
		},
		{
			name: "equal",
			a:    map[string]string{"key": "value"},
			b:    map[string]string{"key": "value"},
			want: true,
		},
		{
			name: "different values",
			a:    map[string]string{"key": "v1"},
			b:    map[string]string{"key": "v2"},
			want: false,
		},
		{
			name: "different keys",
			a:    map[string]string{"key1": "value"},
			b:    map[string]string{"key2": "value"},
			want: false,
		},
		{
			name: "extra key in b",
			a:    map[string]string{"key": "value"},
			b:    map[string]string{"key": "value", "extra": "val"},
			want: false,
		},
		{
			name: "empty string vs missing key",
			a:    map[string]string{"key": ""},
			b:    nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := equalAnnotations(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("equalAnnotations(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
