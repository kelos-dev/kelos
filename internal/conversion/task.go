package conversion

import (
	"context"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func taskToHub(_ context.Context, src *v1alpha1.Task, dst *v1alpha2.Task) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	foldTaskAgentConfigRefForward(&src.Spec, &dst.Spec)
	if dst.Spec.BranchLock == "" {
		dst.Spec.BranchLock = preservedBranchLock(src.Annotations)
	}
	deleteAnnotation(dst.Annotations, preservedBranchLockAnnotation)
	return convertViaJSON(&src.Status, &dst.Status)
}

func taskFromHub(_ context.Context, src *v1alpha2.Task, dst *v1alpha1.Task) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if err := backfillTaskLegacyWorkerFields(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	setPreservedTaskBranchLockAnnotation(dst, src.Spec.BranchLock)
	return convertViaJSON(&src.Status, &dst.Status)
}

func setPreservedTaskBranchLockAnnotation(dst *v1alpha1.Task, branchLock v1alpha2.BranchLockPolicy) {
	if branchLock == "" {
		deleteAnnotation(dst.Annotations, preservedBranchLockAnnotation)
		return
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedBranchLockAnnotation] = string(branchLock)
}

func foldTaskAgentConfigRefForward(src *v1alpha1.TaskSpec, dst *v1alpha2.TaskSpec) {
	if len(dst.AgentConfigRefs) == 0 && src.AgentConfigRef != nil {
		dst.AgentConfigRefs = []v1alpha2.AgentConfigReference{{Name: src.AgentConfigRef.Name}}
	}
}

func backfillTaskLegacyWorkerFields(src *v1alpha2.TaskSpec, dst *v1alpha1.TaskSpec) error {
	if src.WorkerPoolRef != nil || src.Worker == nil {
		return nil
	}
	worker := src.Worker

	if dst.Type == "" {
		dst.Type = worker.Type
	}
	if dst.Credentials.Type == "" && worker.Credentials != nil {
		if err := convertViaJSON(worker.Credentials, &dst.Credentials); err != nil {
			return err
		}
	}
	if dst.Model == "" {
		dst.Model = worker.Model
	}
	if dst.Effort == "" {
		dst.Effort = worker.Effort
	}
	if dst.Image == "" {
		dst.Image = worker.Image
	}
	if dst.WorkspaceRef == nil && worker.WorkspaceRef != nil {
		if err := convertViaJSON(worker.WorkspaceRef, &dst.WorkspaceRef); err != nil {
			return err
		}
	}
	if len(dst.AgentConfigRefs) == 0 && len(worker.AgentConfigRefs) > 0 {
		if err := convertViaJSON(&worker.AgentConfigRefs, &dst.AgentConfigRefs); err != nil {
			return err
		}
	}
	if dst.PodOverrides == nil && worker.PodOverrides != nil {
		if err := convertViaJSON(worker.PodOverrides, &dst.PodOverrides); err != nil {
			return err
		}
	}
	return nil
}
