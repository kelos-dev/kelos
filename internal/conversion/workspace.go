package conversion

import (
	"context"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

// preservedWorkspaceProviderAnnotation carries spec.provider (a v1alpha2-only
// field) across a v1alpha1 round-trip so a client that reads and writes the
// Workspace through v1alpha1 does not silently turn a GitLab workspace back
// into a GitHub one.
const preservedWorkspaceProviderAnnotation = "kelos.dev/v1alpha2-workspace-provider"

func workspaceToHub(_ context.Context, src *v1alpha1.Workspace, dst *v1alpha2.Workspace) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	// Only the non-default provider is restored; anything else (github, or an
	// edited value) leaves the field empty so the v1alpha2 default applies.
	if src.Annotations[preservedWorkspaceProviderAnnotation] == v1alpha2.WorkspaceProviderGitLab {
		dst.Spec.Provider = v1alpha2.WorkspaceProviderGitLab
	}
	deleteAnnotation(dst.Annotations, preservedWorkspaceProviderAnnotation)
	return nil
}

func workspaceFromHub(_ context.Context, src *v1alpha2.Workspace, dst *v1alpha1.Workspace) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if src.Spec.Provider == "" || src.Spec.Provider == v1alpha2.WorkspaceProviderGitHub {
		deleteAnnotation(dst.Annotations, preservedWorkspaceProviderAnnotation)
		return nil
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedWorkspaceProviderAnnotation] = src.Spec.Provider
	return nil
}
