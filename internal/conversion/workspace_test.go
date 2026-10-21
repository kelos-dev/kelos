package conversion

import (
	"context"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestWorkspaceRoundTrip_PreservesCheckoutRestrictions(t *testing.T) {
	hub := &v1alpha2.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "ns", Annotations: map[string]string{"keep": "me"}},
		Spec: v1alpha2.WorkspaceSpec{
			Repo:      "https://github.com/o/r",
			Ref:       "main",
			SecretRef: &v1alpha2.SecretReference{Name: "gh"},
			Clone: &v1alpha2.WorkspaceClone{
				Paths:        []string{"docs", "www/app"},
				RemoveGitDir: true,
			},
			AgentCredentials: v1alpha2.WorkspaceAgentCredentialsNone,
		},
	}

	spoke := &v1alpha1.Workspace{}
	if err := workspaceFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("workspaceFromHub() error = %v", err)
	}
	if spoke.Annotations[preservedWorkspaceCheckoutAnnotation] == "" {
		t.Fatalf("Expected the %s annotation on the v1alpha1 object, got %v", preservedWorkspaceCheckoutAnnotation, spoke.Annotations)
	}
	if _, ok := hub.Annotations[preservedWorkspaceCheckoutAnnotation]; ok {
		t.Error("workspaceFromHub() must not write the annotation onto the source object")
	}

	back := &v1alpha2.Workspace{}
	if err := workspaceToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("workspaceToHub() error = %v", err)
	}
	if !reflect.DeepEqual(hub.Spec, back.Spec) {
		t.Errorf("Spec round-trip mismatch:\n orig=%#v\n back=%#v", hub.Spec, back.Spec)
	}
	if !reflect.DeepEqual(back.Annotations, map[string]string{"keep": "me"}) {
		t.Errorf("Expected only the user annotations after restore, got %v", back.Annotations)
	}
	if _, ok := spoke.Annotations[preservedWorkspaceCheckoutAnnotation]; !ok {
		t.Error("workspaceToHub() must not remove the annotation from the source object")
	}
}

func TestWorkspaceFromHub_NoCheckoutRestrictionsClearsAnnotation(t *testing.T) {
	hub := &v1alpha2.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "w",
			Namespace:   "ns",
			Annotations: map[string]string{preservedWorkspaceCheckoutAnnotation: `{"agentCredentials":"None"}`},
		},
		Spec: v1alpha2.WorkspaceSpec{Repo: "https://github.com/o/r"},
	}
	spoke := &v1alpha1.Workspace{}
	if err := workspaceFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("workspaceFromHub() error = %v", err)
	}
	if _, ok := spoke.Annotations[preservedWorkspaceCheckoutAnnotation]; ok {
		t.Errorf("Expected a stale %s annotation to be removed, got %v", preservedWorkspaceCheckoutAnnotation, spoke.Annotations)
	}
}

func TestWorkspaceToHub_RejectsMalformedCheckoutAnnotation(t *testing.T) {
	spoke := &v1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "w",
			Namespace:   "ns",
			Annotations: map[string]string{preservedWorkspaceCheckoutAnnotation: "{"},
		},
		Spec: v1alpha1.WorkspaceSpec{Repo: "https://github.com/o/r"},
	}
	if err := workspaceToHub(context.Background(), spoke, &v1alpha2.Workspace{}); err == nil {
		t.Fatal("Expected an error for a malformed annotation")
	}
}

// TestWorkspaceFromHub_CheckoutAnnotationStaysSmall checks that the largest
// clone.paths the API admits (64 entries of 256 characters) still fits well
// within the 256 KiB limit on an object's annotations, leaving room for
// others such as kubectl's last-applied-configuration. U+2028 is the
// costliest character to encode: JSON escapes it to six bytes.
func TestWorkspaceFromHub_CheckoutAnnotationStaysSmall(t *testing.T) {
	paths := make([]string, 64)
	for i := range paths {
		paths[i] = strings.Repeat(" ", 256)
	}
	hub := &v1alpha2.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "ns"},
		Spec: v1alpha2.WorkspaceSpec{
			Repo:             "https://github.com/o/r",
			Clone:            &v1alpha2.WorkspaceClone{Paths: paths, RemoveGitDir: true},
			AgentCredentials: v1alpha2.WorkspaceAgentCredentialsNone,
		},
	}
	spoke := &v1alpha1.Workspace{}
	if err := workspaceFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("workspaceFromHub() error = %v", err)
	}
	size := len(spoke.Annotations[preservedWorkspaceCheckoutAnnotation])
	if size == 0 {
		t.Fatalf("Expected the %s annotation on the v1alpha1 object", preservedWorkspaceCheckoutAnnotation)
	}
	if size > 128*1024 {
		t.Errorf("Preserved checkout annotation is %d bytes, want at most %d", size, 128*1024)
	}

	back := &v1alpha2.Workspace{}
	if err := workspaceToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("workspaceToHub() error = %v", err)
	}
	if !reflect.DeepEqual(hub.Spec, back.Spec) {
		t.Error("Spec did not survive the round trip at the size limit")
	}
}
