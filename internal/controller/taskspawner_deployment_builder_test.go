package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func TestBuildControllerImageAnnotation(t *testing.T) {
	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{},
	}

	t.Run("no annotation when ControllerImage is empty", func(t *testing.T) {
		b := NewDeploymentBuilder()
		deploy := b.Build(ts, nil)

		ann := deploy.Spec.Template.Annotations
		if v, ok := ann[ControllerImageAnnotation]; ok {
			t.Errorf("expected no controller-image annotation, got %q", v)
		}
	})

	t.Run("annotation set when ControllerImage is provided", func(t *testing.T) {
		b := NewDeploymentBuilder()
		b.ControllerImage = "gjkim42/axon-controller:v1.0"
		deploy := b.Build(ts, nil)

		ann := deploy.Spec.Template.Annotations
		if v, ok := ann[ControllerImageAnnotation]; !ok {
			t.Errorf("expected controller-image annotation, got none")
		} else if v != "gjkim42/axon-controller:v1.0" {
			t.Errorf("annotation = %q, want %q", v, "gjkim42/axon-controller:v1.0")
		}
	})

	t.Run("annotation updates when ControllerImage changes", func(t *testing.T) {
		b := NewDeploymentBuilder()
		b.ControllerImage = "gjkim42/axon-controller:v1.0"
		deploy1 := b.Build(ts, nil)

		b.ControllerImage = "gjkim42/axon-controller:v2.0"
		deploy2 := b.Build(ts, nil)

		if deploy1.Spec.Template.Annotations[ControllerImageAnnotation] == deploy2.Spec.Template.Annotations[ControllerImageAnnotation] {
			t.Errorf("expected different annotations, both got %q", deploy1.Spec.Template.Annotations[ControllerImageAnnotation])
		}
	})
}

func TestBuildImagePullPolicy(t *testing.T) {
	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{},
	}

	t.Run("ImagePullPolicy propagated to container", func(t *testing.T) {
		b := NewDeploymentBuilder()
		b.SpawnerImagePullPolicy = corev1.PullAlways
		deploy := b.Build(ts, nil)

		container := deploy.Spec.Template.Spec.Containers[0]
		if container.ImagePullPolicy != corev1.PullAlways {
			t.Errorf("ImagePullPolicy = %q, want %q", container.ImagePullPolicy, corev1.PullAlways)
		}
	})
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
			name: "equal annotations",
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
			name: "extra key in a",
			a:    map[string]string{"key": "value", "extra": "stale"},
			b:    map[string]string{"key": "value"},
			want: false,
		},
		{
			name: "extra key in b",
			a:    map[string]string{"key": "value"},
			b:    map[string]string{"key": "value", "extra": "new"},
			want: false,
		},
		{
			name: "different keys with empty string values",
			a:    map[string]string{"key-a": ""},
			b:    map[string]string{"key-b": ""},
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

func TestParseGitHubOwnerRepo(t *testing.T) {
	tests := []struct {
		name      string
		repoURL   string
		wantOwner string
		wantRepo  string
	}{
		{
			name:      "HTTPS URL",
			repoURL:   "https://github.com/gjkim42/axon.git",
			wantOwner: "gjkim42",
			wantRepo:  "axon",
		},
		{
			name:      "HTTPS URL without .git",
			repoURL:   "https://github.com/gjkim42/axon",
			wantOwner: "gjkim42",
			wantRepo:  "axon",
		},
		{
			name:      "HTTPS URL with trailing slash",
			repoURL:   "https://github.com/gjkim42/axon/",
			wantOwner: "gjkim42",
			wantRepo:  "axon",
		},
		{
			name:      "SSH URL",
			repoURL:   "git@github.com:gjkim42/axon.git",
			wantOwner: "gjkim42",
			wantRepo:  "axon",
		},
		{
			name:      "SSH URL without .git",
			repoURL:   "git@github.com:gjkim42/axon",
			wantOwner: "gjkim42",
			wantRepo:  "axon",
		},
		{
			name:      "HTTPS URL with org",
			repoURL:   "https://github.com/my-org/my-repo.git",
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
