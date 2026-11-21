package controller

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const routeConditionInput = `{{index .Stages "route" 0 "Results" "response" | b64dec}}`

// routedPipeline returns a pipeline whose route stage selects one of two
// branches, with a fallback stage for any other answer.
func routedPipeline(name string) *kelos.TaskPipeline {
	return testTaskPipeline(name, []kelos.PipelineStage{
		{Name: "route", TaskTemplate: testPipelineTaskTemplate("Answer docs, incident, or none")},
		{
			Name: "docs",
			Conditions: []kelos.PipelineStageCondition{{
				Input:    routeConditionInput,
				Operator: kelos.PipelineConditionOperatorIn,
				Values:   []string{"docs"},
			}},
			TaskTemplate: testPipelineTaskTemplate(`Handle the docs request routed as {{index .Stages "route" 0 "Results" "response" | b64dec}}`),
		},
		{
			Name: "incident",
			Conditions: []kelos.PipelineStageCondition{{
				Input:    routeConditionInput,
				Operator: kelos.PipelineConditionOperatorIn,
				Values:   []string{"incident"},
			}},
			TaskTemplate: testPipelineTaskTemplate("Handle the incident"),
		},
		{
			Name: "fallback",
			Conditions: []kelos.PipelineStageCondition{{
				Input:    routeConditionInput,
				Operator: kelos.PipelineConditionOperatorNotIn,
				Values:   []string{"docs", "incident"},
			}},
			TaskTemplate: testPipelineTaskTemplate("Explain that no team handles this"),
		},
	})
}

func TestTaskPipelineReconcileRunsOnlyMatchingConditionalStage(t *testing.T) {
	pipeline := routedPipeline("routed")
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	completePipelineStageTask(t, k8sClient, pipeline, "route", map[string]string{
		"response": base64.StdEncoding.EncodeToString([]byte("docs\n")),
	})
	reconcileTaskPipeline(t, reconciler, pipeline)

	docs := pipelineStageTask(t, k8sClient, pipeline, "docs")
	if docs == nil {
		t.Fatal("docs Task was not created")
	}
	if docs.Spec.Prompt != "Handle the docs request routed as docs\n" {
		t.Fatalf("docs prompt = %q", docs.Spec.Prompt)
	}
	if pipelineStageTask(t, k8sClient, pipeline, "incident") != nil {
		t.Fatal("incident Task was created before the docs stage finished")
	}

	completePipelineStageTask(t, k8sClient, pipeline, "docs", map[string]string{"summary": "done"})
	reconcileTaskPipeline(t, reconciler, pipeline)

	for _, stage := range []string{"incident", "fallback"} {
		if pipelineStageTask(t, k8sClient, pipeline, stage) != nil {
			t.Errorf("%s Task was created, want the stage skipped", stage)
		}
	}
	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.Phase != kelos.TaskPipelinePhaseSucceeded {
		t.Fatalf("pipeline phase = %q, want %q", updated.Status.Phase, kelos.TaskPipelinePhaseSucceeded)
	}
	wantPhases := map[string]kelos.TaskPhase{
		"route":    kelos.TaskPhaseSucceeded,
		"docs":     kelos.TaskPhaseSucceeded,
		"incident": kelos.PipelineStagePhaseSkipped,
		"fallback": kelos.PipelineStagePhaseSkipped,
	}
	for _, status := range updated.Status.StageStatuses {
		if status.Phase != wantPhases[status.Name] {
			t.Errorf("stage %q phase = %q, want %q", status.Name, status.Phase, wantPhases[status.Name])
		}
		if status.Phase == kelos.PipelineStagePhaseSkipped && status.Total != 0 {
			t.Errorf("skipped stage %q total = %d, want 0", status.Name, status.Total)
		}
	}
	succeeded := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.TaskPipelineConditionSucceeded)
	if succeeded == nil || succeeded.Status != metav1.ConditionTrue {
		t.Fatalf("Succeeded condition = %#v, want True", succeeded)
	}
}

func TestTaskPipelineReconcileRunsNotInFallbackForUnknownAnswer(t *testing.T) {
	pipeline := routedPipeline("fallback")
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	completePipelineStageTask(t, k8sClient, pipeline, "route", map[string]string{
		"response": base64.StdEncoding.EncodeToString([]byte("billing")),
	})
	reconcileTaskPipeline(t, reconciler, pipeline)

	for _, stage := range []string{"docs", "incident"} {
		if pipelineStageTask(t, k8sClient, pipeline, stage) != nil {
			t.Errorf("%s Task was created for an unknown answer", stage)
		}
	}
	if pipelineStageTask(t, k8sClient, pipeline, "fallback") == nil {
		t.Fatal("fallback Task was not created")
	}
	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.Phase != kelos.TaskPipelinePhaseRunning {
		t.Fatalf("pipeline phase = %q, want %q", updated.Status.Phase, kelos.TaskPipelinePhaseRunning)
	}
}

func TestTaskPipelineReconcileRequiresEveryCondition(t *testing.T) {
	pipeline := testTaskPipeline("all-conditions", []kelos.PipelineStage{
		{Name: "check", TaskTemplate: testPipelineTaskTemplate("Check the change")},
		{
			Name: "deploy",
			Conditions: []kelos.PipelineStageCondition{
				{
					Input:    `{{index .Stages "check" 0 "Results" "tests"}}`,
					Operator: kelos.PipelineConditionOperatorIn,
					Values:   []string{"passed"},
				},
				{
					Input:    `{{index .Stages "check" 0 "Results" "risk"}}`,
					Operator: kelos.PipelineConditionOperatorNotIn,
					Values:   []string{"high"},
				},
			},
			TaskTemplate: testPipelineTaskTemplate("Deploy the change"),
		},
	})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	completePipelineStageTask(t, k8sClient, pipeline, "check", map[string]string{"tests": "passed", "risk": "high"})
	reconcileTaskPipeline(t, reconciler, pipeline)

	if pipelineStageTask(t, k8sClient, pipeline, "deploy") != nil {
		t.Fatal("deploy Task was created although one condition was not met")
	}
	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.Phase != kelos.TaskPipelinePhaseSucceeded {
		t.Fatalf("pipeline phase = %q, want %q", updated.Status.Phase, kelos.TaskPipelinePhaseSucceeded)
	}
}

func TestTaskPipelineReconcileKeepsSkipDecision(t *testing.T) {
	pipeline := testTaskPipeline("sticky-skip", []kelos.PipelineStage{
		{Name: "route", TaskTemplate: testPipelineTaskTemplate("Pick a route")},
		{
			Name: "optional",
			Conditions: []kelos.PipelineStageCondition{{
				Input:    `{{index .Stages "route" 0 "Results" "route"}}`,
				Operator: kelos.PipelineConditionOperatorIn,
				Values:   []string{"optional"},
			}},
			TaskTemplate: testPipelineTaskTemplate("Run the optional work"),
		},
		{Name: "finish", TaskTemplate: testPipelineTaskTemplate("Finish")},
	})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	route := completePipelineStageTask(t, k8sClient, pipeline, "route", map[string]string{"route": "other"})
	reconcileTaskPipeline(t, reconciler, pipeline)
	if pipelineStageTask(t, k8sClient, pipeline, "finish") == nil {
		t.Fatal("finish Task was not created after the optional stage was skipped")
	}

	route.Status.Results = map[string]string{"route": "optional"}
	if err := k8sClient.Status().Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	reconcileTaskPipeline(t, reconciler, pipeline)

	if pipelineStageTask(t, k8sClient, pipeline, "optional") != nil {
		t.Fatal("optional Task was created after its skip was recorded")
	}
	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.StageStatuses[1].Phase != kelos.PipelineStagePhaseSkipped {
		t.Fatalf("optional stage phase = %q, want %q", updated.Status.StageStatuses[1].Phase, kelos.PipelineStagePhaseSkipped)
	}
}

func TestTaskPipelineReconcileFailsOnConditionRenderError(t *testing.T) {
	pipeline := testTaskPipeline("condition-error", []kelos.PipelineStage{
		{Name: "route", TaskTemplate: testPipelineTaskTemplate("Pick a route")},
		{
			Name: "docs",
			Conditions: []kelos.PipelineStageCondition{{
				Input:    `{{index .Stages "route" 0 "Results" "route"}}`,
				Operator: kelos.PipelineConditionOperatorIn,
				Values:   []string{"docs"},
			}},
			TaskTemplate: testPipelineTaskTemplate("Handle docs"),
		},
	})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	route := completePipelineStageTask(t, k8sClient, pipeline, "route", map[string]string{"summary": "no route key"})
	completionTime := metav1.NewTime(time.Now().Add(-outputRetryWindow - time.Second))
	route.Status.CompletionTime = &completionTime
	if err := k8sClient.Status().Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	reconcileTaskPipeline(t, reconciler, pipeline)

	if pipelineStageTask(t, k8sClient, pipeline, "docs") != nil {
		t.Fatal("docs Task was created although its condition could not be rendered")
	}
	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.Phase != kelos.TaskPipelinePhaseFailed {
		t.Fatalf("pipeline phase = %q, want %q", updated.Status.Phase, kelos.TaskPipelinePhaseFailed)
	}
	if updated.Status.StageStatuses[1].Phase != kelos.TaskPhaseFailed {
		t.Fatalf("docs stage phase = %q, want %q", updated.Status.StageStatuses[1].Phase, kelos.TaskPhaseFailed)
	}
	succeeded := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.TaskPipelineConditionSucceeded)
	if succeeded == nil || !strings.Contains(succeeded.Message, `key "route" is not present`) {
		t.Fatalf("Succeeded condition = %#v", succeeded)
	}
}

func TestTaskPipelineReconcileSkipsLeadingStageWithConstantCondition(t *testing.T) {
	pipeline := testTaskPipeline("disabled-first-stage", []kelos.PipelineStage{
		{
			Name: "disabled",
			Conditions: []kelos.PipelineStageCondition{{
				Input:    "off",
				Operator: kelos.PipelineConditionOperatorIn,
				Values:   []string{"on"},
			}},
			TaskTemplate: testPipelineTaskTemplate("Never runs"),
		},
		{Name: "work", TaskTemplate: testPipelineTaskTemplate("Do the work")},
	})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	if pipelineStageTask(t, k8sClient, pipeline, "disabled") != nil {
		t.Fatal("disabled Task was created")
	}
	if pipelineStageTask(t, k8sClient, pipeline, "work") == nil {
		t.Fatal("work Task was not created after the leading stage was skipped")
	}
}

func TestValidateTaskPipelineRejectsInvalidConditionTemplate(t *testing.T) {
	pipeline := testTaskPipeline("bad-condition", []kelos.PipelineStage{{
		Name: "work",
		Conditions: []kelos.PipelineStageCondition{{
			Input:    "{{.Stages",
			Operator: kelos.PipelineConditionOperatorIn,
			Values:   []string{"x"},
		}},
		TaskTemplate: testPipelineTaskTemplate("Do the work"),
	}})
	err := validateTaskPipeline(pipeline)
	if err == nil || !strings.Contains(err.Error(), `stage "work" condition 0 input`) {
		t.Fatalf("validateTaskPipeline() error = %v, want a condition input error", err)
	}
}

func TestBase64DecodeTemplateValueRejectsInvalidInput(t *testing.T) {
	if _, err := base64DecodeTemplateValue("not base64!"); err == nil {
		t.Fatal("base64DecodeTemplateValue() error = nil, want an error")
	}
}

// completePipelineStageTask marks the single Task of a stage as succeeded with
// the given results and returns the updated Task.
func completePipelineStageTask(
	t *testing.T,
	k8sClient client.Client,
	pipeline *kelos.TaskPipeline,
	stage string,
	results map[string]string,
) *kelos.Task {
	t.Helper()
	task := pipelineStageTask(t, k8sClient, pipeline, stage)
	if task == nil {
		t.Fatalf("stage %q has no Task", stage)
	}
	now := metav1.Now()
	task.Status.Phase = kelos.TaskPhaseSucceeded
	task.Status.CompletionTime = &now
	task.Status.Results = results
	if err := k8sClient.Status().Update(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

func pipelineStageTask(t *testing.T, k8sClient client.Client, pipeline *kelos.TaskPipeline, stage string) *kelos.Task {
	t.Helper()
	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks,
		client.InNamespace(pipeline.Namespace),
		client.MatchingLabels{taskPipelineStageLabel: stage},
	); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) == 0 {
		return nil
	}
	return &tasks.Items[0]
}
