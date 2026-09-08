package conversion

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestWorkspaceConvert_ProviderRoundTrips(t *testing.T) {
	hub := &v1alpha2.Workspace{Spec: v1alpha2.WorkspaceSpec{
		Repo:      "https://gitlab.example.com/group/repo.git",
		Provider:  v1alpha2.WorkspaceProviderGitLab,
		SecretRef: &v1alpha2.SecretReference{Name: "gitlab-token"},
	}}
	spoke := &v1alpha1.Workspace{}
	if err := workspaceFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("workspaceFromHub() error = %v", err)
	}
	if got := spoke.Annotations[preservedWorkspaceProviderAnnotation]; got != v1alpha2.WorkspaceProviderGitLab {
		t.Fatalf("preservation annotation = %q, want %q", got, v1alpha2.WorkspaceProviderGitLab)
	}
	if hub.Annotations != nil {
		t.Fatal("workspaceFromHub() must not mutate the hub annotations")
	}

	back := &v1alpha2.Workspace{}
	if err := workspaceToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("workspaceToHub() error = %v", err)
	}
	if back.Spec.Provider != v1alpha2.WorkspaceProviderGitLab {
		t.Fatalf("round-tripped provider = %q, want gitlab", back.Spec.Provider)
	}
	if back.Spec.Repo != hub.Spec.Repo || back.Spec.SecretRef == nil || back.Spec.SecretRef.Name != "gitlab-token" {
		t.Fatalf("round-tripped spec = %+v, want repo and secretRef preserved", back.Spec)
	}
	if _, ok := back.Annotations[preservedWorkspaceProviderAnnotation]; ok {
		t.Fatal("preservation annotation remained on hub")
	}
}

func TestWorkspaceFromHub_GitHubProviderOmitsAnnotation(t *testing.T) {
	for _, provider := range []string{"", v1alpha2.WorkspaceProviderGitHub} {
		// A stale annotation on the hub (left by an earlier gitlab round-trip)
		// is copied onto the spoke before the provider is inspected.
		hub := &v1alpha2.Workspace{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				preservedWorkspaceProviderAnnotation: "gitlab",
			}},
			Spec: v1alpha2.WorkspaceSpec{
				Repo:     "https://github.com/org/repo.git",
				Provider: provider,
			},
		}
		spoke := &v1alpha1.Workspace{}
		if err := workspaceFromHub(context.Background(), hub, spoke); err != nil {
			t.Fatalf("workspaceFromHub() error = %v", err)
		}
		if _, ok := spoke.Annotations[preservedWorkspaceProviderAnnotation]; ok {
			t.Fatalf("provider %q must not leave a stale preservation annotation", provider)
		}
	}
}

func TestWorkspaceToHub_WithoutAnnotationLeavesProviderEmpty(t *testing.T) {
	spoke := &v1alpha1.Workspace{Spec: v1alpha1.WorkspaceSpec{Repo: "https://github.com/org/repo.git"}}
	hub := &v1alpha2.Workspace{}
	if err := workspaceToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("workspaceToHub() error = %v", err)
	}
	if hub.Spec.Provider != "" {
		t.Fatalf("provider = %q, want empty so the CRD default applies", hub.Spec.Provider)
	}
}

func TestWorkspaceToHub_IgnoresUnsupportedAnnotationValues(t *testing.T) {
	for _, value := range []string{v1alpha2.WorkspaceProviderGitHub, "bitbucket", " "} {
		spoke := &v1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{preservedWorkspaceProviderAnnotation: value}},
			Spec:       v1alpha1.WorkspaceSpec{Repo: "https://github.com/org/repo.git"},
		}
		hub := &v1alpha2.Workspace{}
		if err := workspaceToHub(context.Background(), spoke, hub); err != nil {
			t.Fatalf("workspaceToHub() error = %v", err)
		}
		if hub.Spec.Provider != "" {
			t.Fatalf("annotation %q set provider = %q, want empty so the CRD default applies", value, hub.Spec.Provider)
		}
		if _, ok := hub.Annotations[preservedWorkspaceProviderAnnotation]; ok {
			t.Fatalf("annotation %q remained on hub", value)
		}
	}
}
