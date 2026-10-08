package conversion

import (
	"context"
	"encoding/json"
	"fmt"
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
				GitDir:       v1alpha2.WorkspaceGitDirRemove,
				BranchPolicy: v1alpha2.WorkspaceCloneBranchPolicyRefAndDefault,
			},
			AgentGitHubToken: v1alpha2.WorkspaceAgentGitHubTokenNone,
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
			Annotations: map[string]string{preservedWorkspaceCheckoutAnnotation: `{"agentGitHubToken":"None"}`},
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

func TestWorkspaceToHub_RejectsUnknownCheckoutEnumValues(t *testing.T) {
	tests := []struct {
		name       string
		annotation string
		wantErr    string
	}{
		{name: "lowercase agentGitHubToken", annotation: `{"agentGitHubToken":"none"}`, wantErr: `unsupported agentGitHubToken "none"`},
		{name: "lowercase gitDir", annotation: `{"clone":{"gitDir":"remove"}}`, wantErr: `unsupported clone.gitDir "remove"`},
		{name: "lowercase branchPolicy", annotation: `{"clone":{"branchPolicy":"ref"}}`, wantErr: `unsupported clone.branchPolicy "ref"`},
		{name: "empty clone paths", annotation: `{"clone":{"paths":[]}}`, wantErr: "clone.paths must have at least 1 entry"},
		{name: "too many clone paths", annotation: clonePathsAnnotation(65, "p"), wantErr: "clone.paths has 65 entries, more than the limit of 64"},
		{name: "clone path too long", annotation: `{"clone":{"paths":["` + strings.Repeat("é", 257) + `"]}}`, wantErr: "must be 1 to 256 characters"},
		{name: "empty clone path", annotation: `{"clone":{"paths":[""]}}`, wantErr: "must be 1 to 256 characters"},
		{name: "duplicate clone paths", annotation: `{"clone":{"paths":["docs","docs"]}}`, wantErr: `clone.paths entry "docs" is duplicated`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spoke := &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "w",
					Namespace:   "ns",
					Annotations: map[string]string{preservedWorkspaceCheckoutAnnotation: tt.annotation},
				},
				Spec: v1alpha1.WorkspaceSpec{Repo: "https://github.com/o/r"},
			}
			err := workspaceToHub(context.Background(), spoke, &v1alpha2.Workspace{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("workspaceToHub() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// clonePathsAnnotation returns a checkout annotation with n distinct paths.
func clonePathsAnnotation(n int, prefix string) string {
	paths := make([]string, n)
	for i := range paths {
		paths[i] = fmt.Sprintf("%q", fmt.Sprintf("%s%d", prefix, i))
	}
	return `{"clone":{"paths":[` + strings.Join(paths, ",") + `]}}`
}

func TestWorkspaceToHub_AcceptsCheckoutAtTheLimits(t *testing.T) {
	paths := make([]string, 64)
	for i := range paths {
		// 256 characters, each two bytes in UTF-8, so a byte-based
		// limit would wrongly reject them.
		paths[i] = fmt.Sprintf("%03d", i) + strings.Repeat("é", 253)
	}
	data, err := json.Marshal(preservedWorkspaceCheckout{Clone: &v1alpha2.WorkspaceClone{Paths: paths}})
	if err != nil {
		t.Fatal(err)
	}
	spoke := &v1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "w",
			Namespace:   "ns",
			Annotations: map[string]string{preservedWorkspaceCheckoutAnnotation: string(data)},
		},
		Spec: v1alpha1.WorkspaceSpec{Repo: "https://github.com/o/r"},
	}
	hub := &v1alpha2.Workspace{}
	if err := workspaceToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("workspaceToHub() error = %v", err)
	}
	if hub.Spec.Clone == nil || len(hub.Spec.Clone.Paths) != 64 {
		t.Fatalf("clone = %+v, want 64 paths", hub.Spec.Clone)
	}
}

func TestWorkspaceToHub_AcceptsKnownCheckoutEnumValues(t *testing.T) {
	spoke := &v1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "w",
			Namespace:   "ns",
			Annotations: map[string]string{preservedWorkspaceCheckoutAnnotation: `{"clone":{"gitDir":"Keep"},"agentGitHubToken":"Token"}`},
		},
		Spec: v1alpha1.WorkspaceSpec{Repo: "https://github.com/o/r"},
	}
	hub := &v1alpha2.Workspace{}
	if err := workspaceToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("workspaceToHub() error = %v", err)
	}
	if hub.Spec.Clone == nil || hub.Spec.Clone.GitDir != v1alpha2.WorkspaceGitDirKeep {
		t.Errorf("clone = %+v, want gitDir Keep", hub.Spec.Clone)
	}
	if hub.Spec.AgentGitHubToken != v1alpha2.WorkspaceAgentGitHubTokenToken {
		t.Errorf("agentGitHubToken = %q, want Token", hub.Spec.AgentGitHubToken)
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
		// clone.paths is a set, so the entries differ in their last
		// character; the rest are the costliest character to encode.
		paths[i] = strings.Repeat(" ", 255) + string(rune(0x4e00+i))
	}
	hub := &v1alpha2.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "ns"},
		Spec: v1alpha2.WorkspaceSpec{
			Repo:             "https://github.com/o/r",
			Clone:            &v1alpha2.WorkspaceClone{Paths: paths, GitDir: v1alpha2.WorkspaceGitDirRemove},
			AgentGitHubToken: v1alpha2.WorkspaceAgentGitHubTokenNone,
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
