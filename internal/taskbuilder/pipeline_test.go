package taskbuilder

import (
	"context"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func testSpawnerPipelineTemplate() *kelos.TaskSpawnerPipelineTemplate {
	return &kelos.TaskSpawnerPipelineTemplate{Stages: []kelos.PipelineStage{
		{
			Name: "route",
			TaskTemplate: kelos.PipelineTaskTemplate{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "router"},
				Prompt:        "Route {{.Params.Body}}",
			},
		},
		{
			Name: "docs",
			Conditions: []kelos.PipelineStageCondition{{
				Input:    `{{index .Stages "route" 0 "Results" "response" | b64dec}}`,
				Operator: kelos.PipelineConditionOperatorIn,
				Values:   []string{"docs"},
			}},
			TaskTemplate: kelos.PipelineTaskTemplate{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "docs"},
				Prompt:        "{{.Params.Body}}",
			},
		},
	}}
}

func TestBuildTaskPipelineCopiesStagesAndStringParams(t *testing.T) {
	tb, err := NewTaskBuilder(nil)
	if err != nil {
		t.Fatal(err)
	}
	template := testSpawnerPipelineTemplate()
	pipeline := tb.BuildTaskPipeline("chat-slack-abc", "default", template, map[string]interface{}{
		"ID":      "1111.2222",
		"Body":    "How do I rotate credentials?",
		"Number":  42,
		"Context": map[string]interface{}{"docs": "ignored"},
	}, &SpawnerRef{Name: "chat", UID: "spawner-uid", APIVersion: "kelos.dev/v1alpha2", Kind: "TaskSpawner"})

	if pipeline.Name != "chat-slack-abc" || pipeline.Namespace != "default" {
		t.Errorf("pipeline key = %s/%s, want default/chat-slack-abc", pipeline.Namespace, pipeline.Name)
	}
	wantParams := map[string]string{"ID": "1111.2222", "Body": "How do I rotate credentials?"}
	if len(pipeline.Spec.Params) != len(wantParams) {
		t.Fatalf("params = %#v, want %#v", pipeline.Spec.Params, wantParams)
	}
	for key, value := range wantParams {
		if pipeline.Spec.Params[key] != value {
			t.Errorf("params[%q] = %q, want %q", key, pipeline.Spec.Params[key], value)
		}
	}
	if len(pipeline.Spec.Stages) != 2 || pipeline.Spec.Stages[1].Conditions[0].Values[0] != "docs" {
		t.Fatalf("stages = %#v, want the template stages", pipeline.Spec.Stages)
	}
	pipeline.Spec.Stages[1].Conditions[0].Values[0] = "mutated"
	if template.Stages[1].Conditions[0].Values[0] != "docs" {
		t.Error("BuildTaskPipeline shares stage storage with the spawner template")
	}
	if pipeline.Labels[SpawnerLabel] != "chat" {
		t.Errorf("spawner label = %q, want chat", pipeline.Labels[SpawnerLabel])
	}
	if !ObjectBelongsToSpawner(pipeline, "chat", "spawner-uid") {
		t.Error("built TaskPipeline does not belong to its spawner")
	}
}

func TestObjectBelongsToSpawnerRejectsOtherController(t *testing.T) {
	isController := true
	pipeline := &kelos.TaskPipeline{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{SpawnerLabel: "chat"},
		OwnerReferences: []metav1.OwnerReference{{
			Kind: "TaskSpawner", Name: "chat", UID: types.UID("old-uid"), Controller: &isController,
		}},
	}}
	if ObjectBelongsToSpawner(pipeline, "chat", "new-uid") {
		t.Error("TaskPipeline owned by a deleted spawner with the same name was treated as owned")
	}
}

func TestBuildManualTaskRejectsPipelineTemplate(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			When:             kelos.When{Slack: &kelos.Slack{}},
			PipelineTemplate: testSpawnerPipelineTemplate(),
		},
	}
	_, err := BuildManualTask(context.Background(), nil, spawner, ManualTaskOptions{HTTPClient: http.DefaultClient})
	if err == nil || !strings.Contains(err.Error(), "TaskSpawner chat uses pipelineTemplate") {
		t.Fatalf("BuildManualTask() error = %v, want a pipelineTemplate error naming the spawner", err)
	}
}
