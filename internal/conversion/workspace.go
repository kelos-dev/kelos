package conversion

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

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
	AgentGitHubToken v1alpha2.WorkspaceAgentGitHubToken `json:"agentGitHubToken,omitempty"`
}

// Limits on Workspace.spec.clone.paths, matching the v1alpha2 schema markers.
const (
	maxPreservedClonePaths      = 64
	maxPreservedClonePathLength = 256
)

// validate rejects values the v1alpha2 schema would not admit. The annotation
// bypasses that schema: an unknown enum value would otherwise be read as the
// unrestricted default, and clone.paths could exceed the API limits. Path
// syntax is checked again when the Task or Session pod is built.
func (p preservedWorkspaceCheckout) validate() error {
	switch p.AgentGitHubToken {
	case "", v1alpha2.WorkspaceAgentGitHubTokenToken, v1alpha2.WorkspaceAgentGitHubTokenNone:
	default:
		return fmt.Errorf("unsupported agentGitHubToken %q", p.AgentGitHubToken)
	}
	if p.Clone != nil {
		switch p.Clone.GitDir {
		case "", v1alpha2.WorkspaceGitDirKeep, v1alpha2.WorkspaceGitDirRemove:
		default:
			return fmt.Errorf("unsupported clone.gitDir %q", p.Clone.GitDir)
		}
		switch p.Clone.BranchPolicy {
		case "", v1alpha2.WorkspaceCloneBranchPolicyAll, v1alpha2.WorkspaceCloneBranchPolicyRef, v1alpha2.WorkspaceCloneBranchPolicyRefAndDefault:
		default:
			return fmt.Errorf("unsupported clone.branchPolicy %q", p.Clone.BranchPolicy)
		}
		if p.Clone.Paths != nil && len(p.Clone.Paths) == 0 {
			return fmt.Errorf("clone.paths must have at least 1 entry when set")
		}
		if len(p.Clone.Paths) > maxPreservedClonePaths {
			return fmt.Errorf("clone.paths has %d entries, more than the limit of %d", len(p.Clone.Paths), maxPreservedClonePaths)
		}
		seen := make(map[string]struct{}, len(p.Clone.Paths))
		for _, path := range p.Clone.Paths {
			if n := utf8.RuneCountInString(path); n == 0 || n > maxPreservedClonePathLength {
				return fmt.Errorf("clone.paths entry %q must be 1 to %d characters", path, maxPreservedClonePathLength)
			}
			if _, dup := seen[path]; dup {
				return fmt.Errorf("clone.paths entry %q is duplicated", path)
			}
			seen[path] = struct{}{}
		}
	}
	return nil
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
		if err := preserved.validate(); err != nil {
			return fmt.Errorf("decoding %s annotation: %w", preservedWorkspaceCheckoutAnnotation, err)
		}
		dst.Spec.Clone = preserved.Clone
		dst.Spec.AgentGitHubToken = preserved.AgentGitHubToken
	}
	deleteAnnotation(dst.Annotations, preservedWorkspaceCheckoutAnnotation)
	return nil
}

func workspaceFromHub(_ context.Context, src *v1alpha2.Workspace, dst *v1alpha1.Workspace) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if src.Spec.Clone == nil && src.Spec.AgentGitHubToken == "" {
		deleteAnnotation(dst.Annotations, preservedWorkspaceCheckoutAnnotation)
		return nil
	}
	data, err := json.Marshal(preservedWorkspaceCheckout{
		Clone:            src.Spec.Clone,
		AgentGitHubToken: src.Spec.AgentGitHubToken,
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
