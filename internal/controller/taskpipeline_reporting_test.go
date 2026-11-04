package controller

import (
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
)

func TestTaskPipelineStageTasksInheritSlackReporting(t *testing.T) {
	pipeline := testTaskPipeline("slack-routed", []kelos.PipelineStage{
		{Name: "route", TaskTemplate: testPipelineTaskTemplate("Pick a route")},
		{
			Name: "docs",
			Conditions: []kelos.PipelineStageCondition{{
				Input:    `{{index .Stages "route" 0 "Results" "route"}}`,
				Operator: kelos.PipelineConditionOperatorIn,
				Values:   []string{"docs"},
			}},
			TaskTemplate: testPipelineTaskTemplate("Answer the docs question"),
		},
	})
	pipeline.Labels = map[string]string{
		reporting.LabelSlackReporting: "enabled",
		"kelos.dev/taskspawner":       "front-door",
	}
	pipeline.Annotations = map[string]string{
		reporting.AnnotationSlackReporting:   "enabled",
		reporting.AnnotationSlackChannel:     "C456",
		reporting.AnnotationSlackThreadTS:    "1111111100.000000",
		reporting.AnnotationSlackUserID:      "U123",
		reporting.AnnotationSlackReportPhase: "accepted",
		"example.com/unrelated":              "value",
	}
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)
	completePipelineStageTask(t, k8sClient, pipeline, "route", map[string]string{"route": "docs"})
	reconcileTaskPipeline(t, reconciler, pipeline)

	wantAnnotations := map[string]string{
		reporting.AnnotationSlackReporting: "enabled",
		reporting.AnnotationSlackChannel:   "C456",
		reporting.AnnotationSlackThreadTS:  "1111111100.000000",
		reporting.AnnotationSlackUserID:    "U123",
	}
	for _, stage := range []string{"route", "docs"} {
		task := pipelineStageTask(t, k8sClient, pipeline, stage)
		if task == nil {
			t.Fatalf("%s Task was not created", stage)
		}
		if len(task.Annotations) != len(wantAnnotations) {
			t.Errorf("%s Task annotations = %#v, want only the Slack reporting annotations", stage, task.Annotations)
		}
		for key, want := range wantAnnotations {
			if got := task.Annotations[key]; got != want {
				t.Errorf("%s Task annotation %s = %q, want %q", stage, key, got, want)
			}
		}
		if got := task.Labels[reporting.LabelSlackReporting]; got != "enabled" {
			t.Errorf("%s Task label %s = %q, want enabled", stage, reporting.LabelSlackReporting, got)
		}
		if _, ok := task.Labels["kelos.dev/taskspawner"]; ok {
			t.Errorf("%s Task inherited the TaskSpawner label, which would double count it", stage)
		}
	}
}

func TestTaskPipelineStageTasksWithoutReportingHaveNoAnnotations(t *testing.T) {
	pipeline := testTaskPipeline("unreported", []kelos.PipelineStage{
		{Name: "work", TaskTemplate: testPipelineTaskTemplate("Do the work")},
	})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	task := pipelineStageTask(t, k8sClient, pipeline, "work")
	if task == nil {
		t.Fatal("work Task was not created")
	}
	if len(task.Annotations) != 0 {
		t.Errorf("Task annotations = %#v, want none", task.Annotations)
	}
	if _, ok := task.Labels[reporting.LabelSlackReporting]; ok {
		t.Errorf("Task has the %s label without Slack reporting on the pipeline", reporting.LabelSlackReporting)
	}
}

func TestPipelineSlackReportingKeysMatchReporter(t *testing.T) {
	for got, want := range map[string]string{
		pipelineSlackReportingKey: reporting.AnnotationSlackReporting,
		pipelineSlackChannelKey:   reporting.AnnotationSlackChannel,
		pipelineSlackThreadTSKey:  reporting.AnnotationSlackThreadTS,
		pipelineSlackUserIDKey:    reporting.AnnotationSlackUserID,
	} {
		if got != want {
			t.Errorf("pipeline reporting key = %q, want reporter key %q", got, want)
		}
	}
	if pipelineSlackReportingKey != reporting.LabelSlackReporting {
		t.Errorf("pipeline reporting label = %q, want %q", pipelineSlackReportingKey, reporting.LabelSlackReporting)
	}
}
