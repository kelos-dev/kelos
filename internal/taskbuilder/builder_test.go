package taskbuilder

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestBuildTask_ForwardsEffort(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		Type: "codex",
		Credentials: &kelos.Credentials{
			Type: kelos.CredentialTypeAPIKey,
			SecretRef: &kelos.SecretReference{
				Name: "credentials",
			},
		},
		Effort:         "high",
		PromptTemplate: "Fix {{.Title}}",
	}

	task, err := tb.BuildTask("task-1", "default", template, map[string]interface{}{
		"Title": "the bug",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Spec.Effort != "high" {
		t.Fatalf("task.Spec.Effort = %q, want %q", task.Spec.Effort, "high")
	}
	if task.Spec.Prompt != "Fix the bug" {
		t.Fatalf("task.Spec.Prompt = %q, want %q", task.Spec.Prompt, "Fix the bug")
	}
}

func TestBuildTask_ForwardsPodFailurePolicy(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		Type: "codex",
		Credentials: &kelos.Credentials{
			Type: kelos.CredentialTypeAPIKey,
			SecretRef: &kelos.SecretReference{
				Name: "credentials",
			},
		},
		PromptTemplate: "{{.Title}}",
		PodFailurePolicy: &batchv1.PodFailurePolicy{
			Rules: []batchv1.PodFailurePolicyRule{
				{
					Action: batchv1.PodFailurePolicyActionIgnore,
					OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
						{
							Type:   corev1.DisruptionTarget,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
		},
	}

	task, err := tb.BuildTask("task-1", "default", template, map[string]interface{}{
		"Title": "the bug",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Spec.PodFailurePolicy == nil {
		t.Fatal("task.Spec.PodFailurePolicy is nil")
	}
	if got := task.Spec.PodFailurePolicy.Rules[0].Action; got != batchv1.PodFailurePolicyActionIgnore {
		t.Fatalf("task.Spec.PodFailurePolicy.Rules[0].Action = %q, want %q", got, batchv1.PodFailurePolicyActionIgnore)
	}
	conditions := task.Spec.PodFailurePolicy.Rules[0].OnPodConditions
	if len(conditions) != 1 {
		t.Fatalf("task.Spec.PodFailurePolicy.Rules[0].OnPodConditions length = %d, want 1", len(conditions))
	}
	wantCondition := batchv1.PodFailurePolicyOnPodConditionsPattern{
		Type:   corev1.DisruptionTarget,
		Status: corev1.ConditionTrue,
	}
	if conditions[0] != wantCondition {
		t.Fatalf("task.Spec.PodFailurePolicy.Rules[0].OnPodConditions[0] = %#v, want %#v", conditions[0], wantCondition)
	}
}
