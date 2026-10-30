package conversion

import (
	"context"
	"encoding/json"
	"fmt"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

// preservedWorkspaceCheckoutAnnotation carries the v1alpha2-only checkout
// restrictions through a v1alpha1 round trip. Dropping them would turn a
// restricted Workspace back into one that hands the agent the full checkout
// and the GitHub token.
const preservedWorkspaceCheckoutAnnotation = "kelos.dev/v1alpha2-workspace-checkout"

type preservedWorkspaceCheckout struct {
	Clone            *v1alpha2.WorkspaceClone           `json:"clone,omitempty"`
	AgentCredentials v1alpha2.WorkspaceAgentCredentials `json:"agentCredentials,omitempty"`
}

func workspaceToHub(_ context.Context, src *v1alpha1.Workspace, dst *v1alpha2.Workspace) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if raw, ok := src.Annotations[preservedWorkspaceCheckoutAnnotation]; ok {
		var preserved preservedWorkspaceCheckout
		if err := json.Unmarshal([]byte(raw), &preserved); err != nil {
			return fmt.Errorf("decoding %s annotation: %w", preservedWorkspaceCheckoutAnnotation, err)
		}
		dst.Spec.Clone = preserved.Clone
		dst.Spec.AgentCredentials = preserved.AgentCredentials
	}
	deleteAnnotation(dst.Annotations, preservedWorkspaceCheckoutAnnotation)
	return nil
}

func workspaceFromHub(_ context.Context, src *v1alpha2.Workspace, dst *v1alpha1.Workspace) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if src.Spec.Clone == nil && src.Spec.AgentCredentials == "" {
		deleteAnnotation(dst.Annotations, preservedWorkspaceCheckoutAnnotation)
		return nil
	}
	data, err := json.Marshal(preservedWorkspaceCheckout{
		Clone:            src.Spec.Clone,
		AgentCredentials: src.Spec.AgentCredentials,
	})
	if err != nil {
		return fmt.Errorf("encoding %s annotation: %w", preservedWorkspaceCheckoutAnnotation, err)
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedWorkspaceCheckoutAnnotation] = string(data)
	return nil
}
