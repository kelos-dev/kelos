package controller

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestTaskPipelineReconcileCreatesMatrixTasks(t *testing.T) {
	pipeline := testTaskPipeline("security-scan", []kelos.PipelineStage{{
		Name: "scan",
		Matrix: &kelos.PipelineMatrix{Parameters: []kelos.PipelineMatrixParameter{
			{Name: "env", Values: []string{"staging", "production"}},
			{Name: "service", Values: []string{"auth", "billing"}},
		}},
		TaskTemplate: testPipelineTaskTemplate("Scan {{index .Matrix \"service\"}} in {{.Matrix.env}}"),
	}})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)

	reconcileTaskPipeline(t, reconciler, pipeline)

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 4 {
		t.Fatalf("created Tasks = %d, want 4", len(tasks.Items))
	}
	sort.Slice(tasks.Items, func(i, j int) bool { return tasks.Items[i].Name < tasks.Items[j].Name })
	wantPrompts := []string{
		"Scan auth in production",
		"Scan auth in staging",
		"Scan billing in production",
		"Scan billing in staging",
	}
	gotPrompts := make([]string, 0, len(tasks.Items))
	for i := range tasks.Items {
		task := &tasks.Items[i]
		gotPrompts = append(gotPrompts, task.Spec.Prompt)
		if !metav1.IsControlledBy(task, pipeline) {
			t.Errorf("Task %q is not controlled by TaskPipeline", task.Name)
		}
		if task.Labels[taskPipelineLabel] != pipeline.Name || task.Labels[pipelineStageLabel] != "scan" {
			t.Errorf("Task %q labels = %#v", task.Name, task.Labels)
		}
	}
	sort.Strings(gotPrompts)
	for i := range wantPrompts {
		if gotPrompts[i] != wantPrompts[i] {
			t.Errorf("rendered prompt %d = %q, want %q", i, gotPrompts[i], wantPrompts[i])
		}
	}

	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.Phase != kelos.TaskPipelinePhaseRunning {
		t.Fatalf("pipeline phase = %q, want %q", updated.Status.Phase, kelos.TaskPipelinePhaseRunning)
	}
	if len(updated.Status.StageStatuses) != 1 {
		t.Fatalf("stage statuses = %d, want 1", len(updated.Status.StageStatuses))
	}
	status := updated.Status.StageStatuses[0]
	if status.Total != 4 || status.Active != 4 {
		t.Fatalf("stage status = %#v", status)
	}
}

func TestTaskPipelineReconcilePassesStageResults(t *testing.T) {
	pipeline := testTaskPipeline("aggregate", []kelos.PipelineStage{
		{
			Name: "scan",
			Matrix: &kelos.PipelineMatrix{Parameters: []kelos.PipelineMatrixParameter{
				{Name: "service", Values: []string{"auth", "billing"}},
			}},
			TaskTemplate: testPipelineTaskTemplate("Scan {{index .Matrix \"service\"}}"),
		},
		{
			Name: "report",
			TaskTemplate: testPipelineTaskTemplate(
				`{{range index .Stages "scan"}}{{index .Matrix "service"}}={{index .Results "severity"}};{{end}}`,
			),
		},
	})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 2 {
		t.Fatalf("created Tasks before scan stage completion = %d, want 2", len(tasks.Items))
	}
	for i := range tasks.Items {
		tasks.Items[i].Status.Phase = kelos.TaskPhaseSucceeded
		service := "auth"
		if strings.Contains(tasks.Items[i].Spec.Prompt, "billing") {
			service = "billing"
		}
		tasks.Items[i].Status.Results = map[string]string{"severity": map[string]string{
			"auth": "high", "billing": "low",
		}[service]}
		if err := k8sClient.Status().Update(context.Background(), &tasks.Items[i]); err != nil {
			t.Fatal(err)
		}
	}

	reconcileTaskPipeline(t, reconciler, pipeline)

	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 3 {
		t.Fatalf("created Tasks = %d, want 3", len(tasks.Items))
	}
	var report *kelos.Task
	for i := range tasks.Items {
		if tasks.Items[i].Labels[pipelineStageLabel] == "report" {
			report = &tasks.Items[i]
		}
	}
	if report == nil {
		t.Fatal("report Task was not created")
	}
	if report.Spec.Prompt != "auth=high;billing=low;" {
		t.Fatalf("report prompt = %q, want %q", report.Spec.Prompt, "auth=high;billing=low;")
	}

	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	scanStatus := updated.Status.StageStatuses[0]
	if scanStatus.Phase != kelos.TaskPhaseSucceeded || scanStatus.Succeeded != 2 {
		t.Fatalf("scan stage status = %#v", scanStatus)
	}
	report.Status.Phase = kelos.TaskPhaseSucceeded
	report.Status.Results = map[string]string{"summary": "complete"}
	if err := k8sClient.Status().Update(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	reconcileTaskPipeline(t, reconciler, pipeline)
	updated = getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.Phase != kelos.TaskPipelinePhaseSucceeded {
		t.Fatalf("completed pipeline status = %#v", updated.Status)
	}
	ready := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.TaskPipelineConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v, want True", ready)
	}
}

func TestTaskPipelineReconcileFailsOnMissingResult(t *testing.T) {
	pipeline := testTaskPipeline("missing-result", []kelos.PipelineStage{
		{Name: "plan", TaskTemplate: testPipelineTaskTemplate("Plan the work")},
		{
			Name:         "implement",
			TaskTemplate: testPipelineTaskTemplate(`Implement {{index .Stages "plan" 0 "Results" "branch"}}`),
		},
	})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("created Tasks = %d, want 1", len(tasks.Items))
	}
	tasks.Items[0].Status.Phase = kelos.TaskPhaseSucceeded
	completionTime := metav1.NewTime(time.Now().Add(-outputRetryWindow - time.Second))
	tasks.Items[0].Status.CompletionTime = &completionTime
	if err := k8sClient.Status().Update(context.Background(), &tasks.Items[0]); err != nil {
		t.Fatal(err)
	}

	reconcileTaskPipeline(t, reconciler, pipeline)

	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("created Tasks after template failure = %d, want 1", len(tasks.Items))
	}
	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.Phase != kelos.TaskPipelinePhaseFailed {
		t.Fatalf("pipeline phase = %q, want %q", updated.Status.Phase, kelos.TaskPipelinePhaseFailed)
	}
	ready := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.TaskPipelineConditionReady)
	if ready == nil || !strings.Contains(ready.Message, `key "branch" is not present`) {
		t.Fatalf("Ready condition = %#v", ready)
	}
}

func TestTaskPipelineReconcileWaitsForStageOutputCapture(t *testing.T) {
	pipeline := testTaskPipeline("capture-retry", []kelos.PipelineStage{
		{Name: "plan", TaskTemplate: testPipelineTaskTemplate("Plan the work")},
		{
			Name:         "implement",
			TaskTemplate: testPipelineTaskTemplate(`Implement {{index .Stages "plan" 0 "Results" "branch"}}`),
		},
	})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	tasks.Items[0].Status.Phase = kelos.TaskPhaseSucceeded
	completionTime := metav1.Now()
	tasks.Items[0].Status.CompletionTime = &completionTime
	if err := k8sClient.Status().Update(context.Background(), &tasks.Items[0]); err != nil {
		t.Fatal(err)
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pipeline)})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("requeueAfter = %v, want a positive output capture wait", result.RequeueAfter)
	}
	updated := getTaskPipeline(t, k8sClient, pipeline.Name)
	if updated.Status.Phase == kelos.TaskPipelinePhaseFailed {
		t.Fatalf("pipeline failed while dependency outputs may still arrive: %#v", updated.Status)
	}

	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&tasks.Items[0]), &tasks.Items[0]); err != nil {
		t.Fatal(err)
	}
	tasks.Items[0].Status.Results = map[string]string{"branch": "feature/auth"}
	if err := k8sClient.Status().Update(context.Background(), &tasks.Items[0]); err != nil {
		t.Fatal(err)
	}
	reconcileTaskPipeline(t, reconciler, pipeline)

	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 2 {
		t.Fatalf("created Tasks = %d, want 2", len(tasks.Items))
	}
}

func TestTaskPipelineReconcileDoesNotRerunTerminalPipeline(t *testing.T) {
	pipeline := testTaskPipeline("terminal", []kelos.PipelineStage{{
		Name:         "work",
		TaskTemplate: testPipelineTaskTemplate("Do the work"),
	}})
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline)
	reconcileTaskPipeline(t, reconciler, pipeline)

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	tasks.Items[0].Status.Phase = kelos.TaskPhaseSucceeded
	if err := k8sClient.Status().Update(context.Background(), &tasks.Items[0]); err != nil {
		t.Fatal(err)
	}
	reconcileTaskPipeline(t, reconciler, pipeline)
	if got := getTaskPipeline(t, k8sClient, pipeline.Name).Status.Phase; got != kelos.TaskPipelinePhaseSucceeded {
		t.Fatalf("pipeline phase = %q, want %q", got, kelos.TaskPipelinePhaseSucceeded)
	}

	if err := k8sClient.Delete(context.Background(), &tasks.Items[0]); err != nil {
		t.Fatal(err)
	}
	reconcileTaskPipeline(t, reconciler, pipeline)
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 0 {
		t.Fatalf("Tasks after terminal child deletion = %d, want 0", len(tasks.Items))
	}
	if got := getTaskPipeline(t, k8sClient, pipeline.Name).Status.Phase; got != kelos.TaskPipelinePhaseSucceeded {
		t.Fatalf("pipeline phase after child deletion = %q, want %q", got, kelos.TaskPipelinePhaseSucceeded)
	}
}

func TestTaskPipelineReconcileWaitsForEarlierPipelineTasks(t *testing.T) {
	pipeline := testTaskPipeline("recreated", []kelos.PipelineStage{{
		Name:         "work",
		TaskTemplate: testPipelineTaskTemplate("Do the work"),
	}})
	controller := true
	oldTask := &kelos.Task{ObjectMeta: metav1.ObjectMeta{
		Name:      "recreated-work",
		Namespace: "default",
		Labels: map[string]string{
			taskPipelineLabel:      pipeline.Name,
			pipelineStageLabel:     "work",
			pipelineTaskIndexLabel: "0",
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: kelos.GroupVersion.String(),
			Kind:       "TaskPipeline",
			Name:       pipeline.Name,
			UID:        "earlier-pipeline-uid",
			Controller: &controller,
		}},
	}}
	reconciler, k8sClient := testTaskPipelineReconciler(t, pipeline, oldTask)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pipeline)})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != pipelineCollisionWait {
		t.Fatalf("requeueAfter = %v, want %v", result.RequeueAfter, pipelineCollisionWait)
	}
	if got := getTaskPipeline(t, k8sClient, pipeline.Name).Status.Phase; got == kelos.TaskPipelinePhaseFailed {
		t.Fatalf("pipeline phase = %q while earlier owned Task is being deleted", got)
	}

	if err := k8sClient.Delete(context.Background(), oldTask); err != nil {
		t.Fatal(err)
	}
	reconcileTaskPipeline(t, reconciler, pipeline)
	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 || !metav1.IsControlledBy(&tasks.Items[0], pipeline) {
		t.Fatalf("Tasks after old Task deletion = %#v", tasks.Items)
	}
}

func TestValidateTaskPipelineRejectsDuplicateStageName(t *testing.T) {
	pipeline := testTaskPipeline("duplicate-stage", []kelos.PipelineStage{
		{Name: "work", TaskTemplate: testPipelineTaskTemplate("A")},
		{Name: "work", TaskTemplate: testPipelineTaskTemplate("B")},
	})
	if err := validateTaskPipeline(pipeline); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("validateTaskPipeline() error = %v, want duplicate stage error", err)
	}
}

func TestPipelineChildTaskNameIsStableAndBounded(t *testing.T) {
	got := pipelineChildTaskName(strings.Repeat("pipeline", 20), strings.Repeat("stage", 20), 123, 200)
	if len(got) > 63 {
		t.Fatalf("child Task name length = %d, want at most 63", len(got))
	}
	if got != pipelineChildTaskName(strings.Repeat("pipeline", 20), strings.Repeat("stage", 20), 123, 200) {
		t.Fatal("child Task name is not stable")
	}
	label := pipelineLabelValue(strings.Repeat("pipeline", 20))
	if len(label) > 63 {
		t.Fatalf("pipeline label value length = %d, want at most 63", len(label))
	}
}

func testTaskPipeline(name string, stages []kelos.PipelineStage) *kelos.TaskPipeline {
	return &kelos.TaskPipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			UID:        types.UID(name + "-uid"),
			Generation: 1,
		},
		Spec: kelos.TaskPipelineSpec{Stages: stages},
	}
}

func testPipelineTaskTemplate(prompt string) kelos.PipelineTaskTemplate {
	return kelos.PipelineTaskTemplate{
		Worker: &kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		},
		Prompt: prompt,
	}
}

func testTaskPipelineReconciler(t *testing.T, objects ...client.Object) (*TaskPipelineReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskPipeline{}, &kelos.Task{}).
		WithObjects(objects...).
		Build()
	return &TaskPipelineReconciler{Client: k8sClient, Scheme: scheme}, k8sClient
}

func reconcileTaskPipeline(t *testing.T, reconciler *TaskPipelineReconciler, pipeline *kelos.TaskPipeline) {
	t.Helper()
	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pipeline)})
	if err != nil {
		t.Fatal(err)
	}
}

func getTaskPipeline(t *testing.T, k8sClient client.Client, name string) *kelos.TaskPipeline {
	t.Helper()
	var pipeline kelos.TaskPipeline
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &pipeline); err != nil {
		t.Fatal(err)
	}
	return &pipeline
}
