package slack

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

func pipelineSpawner() *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "front-door", Namespace: "default", UID: "spawner-uid"},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{Slack: &kelos.Slack{}},
			PipelineTemplate: &kelos.TaskSpawnerPipelineTemplate{Stages: []kelos.PipelineStage{{
				Name: "route",
				TaskTemplate: kelos.PipelineTaskTemplate{
					WorkerPoolRef: &kelos.WorkerPoolReference{Name: "router"},
					Prompt:        "Route this request: {{.Params.Body}}",
				},
			}}},
		},
	}
}

func newPipelineTestHandler(t *testing.T, objects ...client.Object) (*SlackHandler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	tb, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}
	return &SlackHandler{client: cl, log: logr.Discard(), taskBuilder: tb}, cl
}

func TestCreateTaskCreatesTaskPipelineForPipelineTemplate(t *testing.T) {
	spawner := pipelineSpawner()
	h, cl := newPipelineTestHandler(t)
	msg := &SlackMessageData{
		UserID:    "U123",
		ChannelID: "C456",
		Text:      "How do I rotate credentials?",
		Body:      "How do I rotate credentials?",
		Timestamp: "1111111111.111111",
		ThreadTS:  "1111111100.000000",
		Permalink: "https://example.slack.com/archives/C456/p1111111111111111",
	}

	if err := h.createTask(context.Background(), spawner, msg); err != nil {
		t.Fatalf("createTask() error = %v", err)
	}

	var tasks kelos.TaskList
	if err := cl.List(context.Background(), &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 0 {
		t.Fatalf("created %d Tasks, want none for a pipelineTemplate spawner", len(tasks.Items))
	}
	var pipelines kelos.TaskPipelineList
	if err := cl.List(context.Background(), &pipelines); err != nil {
		t.Fatal(err)
	}
	if len(pipelines.Items) != 1 {
		t.Fatalf("created %d TaskPipelines, want 1", len(pipelines.Items))
	}
	pipeline := &pipelines.Items[0]
	if !strings.HasPrefix(pipeline.Name, "front-door-slack-") {
		t.Errorf("TaskPipeline name = %q, want the Slack spawn name", pipeline.Name)
	}
	if got := pipeline.Spec.Params["Body"]; got != msg.Body {
		t.Errorf("params.Body = %q, want %q", got, msg.Body)
	}
	if got := pipeline.Spec.Params["URL"]; got != msg.Permalink {
		t.Errorf("params.URL = %q, want %q", got, msg.Permalink)
	}
	if len(pipeline.Spec.Stages) != 1 || pipeline.Spec.Stages[0].Name != "route" {
		t.Errorf("stages = %#v, want the spawner's stages", pipeline.Spec.Stages)
	}
	if !taskbuilder.ObjectBelongsToSpawner(pipeline, spawner.Name, spawner.UID) {
		t.Error("TaskPipeline is not owned by the spawner")
	}
	wantAnnotations := map[string]string{
		reporting.AnnotationSlackReporting: "enabled",
		reporting.AnnotationSlackChannel:   "C456",
		reporting.AnnotationSlackUserID:    "U123",
		reporting.AnnotationSlackThreadTS:  "1111111100.000000",
	}
	for key, want := range wantAnnotations {
		if got := pipeline.Annotations[key]; got != want {
			t.Errorf("annotation %s = %q, want %q", key, got, want)
		}
	}
	if pipeline.Labels[reporting.LabelSlackReporting] != "enabled" {
		t.Errorf("label %s = %q, want enabled", reporting.LabelSlackReporting, pipeline.Labels[reporting.LabelSlackReporting])
	}
}

func TestCreateTaskDeduplicatesTaskPipeline(t *testing.T) {
	spawner := pipelineSpawner()
	h, cl := newPipelineTestHandler(t)
	msg := &SlackMessageData{UserID: "U123", ChannelID: "C456", Body: "hello", Timestamp: "1111111111.111111"}

	for i := 0; i < 2; i++ {
		if err := h.createTask(context.Background(), spawner, msg); err != nil {
			t.Fatalf("createTask() attempt %d error = %v", i+1, err)
		}
	}
	var pipelines kelos.TaskPipelineList
	if err := cl.List(context.Background(), &pipelines); err != nil {
		t.Fatal(err)
	}
	if len(pipelines.Items) != 1 {
		t.Fatalf("TaskPipelines = %d, want 1 after a duplicate delivery", len(pipelines.Items))
	}
}

func TestCreateTaskRejectsTaskPipelineNameCollision(t *testing.T) {
	spawner := pipelineSpawner()
	msg := &SlackMessageData{UserID: "U123", ChannelID: "C456", Body: "hello", Timestamp: "1111111111.111111"}

	probe, probeClient := newPipelineTestHandler(t)
	if err := probe.createTask(context.Background(), spawner, msg); err != nil {
		t.Fatalf("createTask() error = %v", err)
	}
	var created kelos.TaskPipelineList
	if err := probeClient.List(context.Background(), &created); err != nil {
		t.Fatal(err)
	}
	unrelated := &kelos.TaskPipeline{
		ObjectMeta: metav1.ObjectMeta{Name: created.Items[0].Name, Namespace: "default"},
		Spec:       created.Items[0].Spec,
	}

	h, _ := newPipelineTestHandler(t, unrelated)
	err := h.createTask(context.Background(), spawner, msg)
	if err == nil || !strings.Contains(err.Error(), "not owned by spawner front-door") {
		t.Fatalf("createTask() error = %v, want a collision error naming the spawner", err)
	}
}
