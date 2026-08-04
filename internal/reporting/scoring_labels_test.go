package reporting

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/scoring"
)

func slackScoringTask(phase kelos.TaskPhase) *kelos.Task {
	return &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "task-uid-1",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",
			},
		},
		Spec: kelos.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: &kelos.Credentials{
				Type:      kelos.CredentialTypeOAuth,
				SecretRef: &kelos.SecretReference{Name: "creds"},
			},
		},
		Status: kelos.TaskStatus{Phase: phase},
	}
}

// Without these labels an inbound reaction cannot be resolved back to a Task, so
// the terminal report must record which message carries the result.
func TestSlackTaskReporter_LabelsTaskWithResultMessage(t *testing.T) {
	task := slackScoringTask(kelos.TaskPhaseSucceeded)
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	reporter := &fakeSlackReporter{
		postFn: func(_ context.Context, _, _ string, _ SlackMessage) (string, error) {
			return "1234567890.999999", nil
		},
	}
	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("ReportTaskStatus() error = %v", err)
	}

	var updated kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if got := updated.Labels[scoring.LabelSlackResultChannel]; got != "C123ABC" {
		t.Errorf("%s = %q, want C123ABC", scoring.LabelSlackResultChannel, got)
	}
	if got := updated.Labels[scoring.LabelSlackResultTS]; got != "1234567890.999999" {
		t.Errorf("%s = %q, want the posted reply timestamp", scoring.LabelSlackResultTS, got)
	}
}

// The result labels also have to reach the TaskRecord, which outlives the Task's
// TTL and is what late-arriving scores resolve against.
func TestSlackTaskReporter_LabelsTaskRecordWithResultMessage(t *testing.T) {
	task := slackScoringTask(kelos.TaskPhaseSucceeded)
	record := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "task-uid-1", Namespace: "default"},
		Spec: kelos.TaskRecordSpec{
			TaskRef: kelos.TaskReference{Name: "test-task", UID: "task-uid-1"},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task, record).Build()

	reporter := &fakeSlackReporter{
		postFn: func(_ context.Context, _, _ string, _ SlackMessage) (string, error) {
			return "1234567890.999999", nil
		},
	}
	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("ReportTaskStatus() error = %v", err)
	}

	var updated kelos.TaskRecord
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(record), &updated); err != nil {
		t.Fatalf("getting updated record: %v", err)
	}
	if got := updated.Labels[scoring.LabelSlackResultChannel]; got != "C123ABC" {
		t.Errorf("%s = %q, want C123ABC", scoring.LabelSlackResultChannel, got)
	}
	if got := updated.Labels[scoring.LabelSlackResultTS]; got != "1234567890.999999" {
		t.Errorf("%s = %q, want the posted reply timestamp", scoring.LabelSlackResultTS, got)
	}
}

// The record is written concurrently by the task controller, so a missing record
// must not fail the report — the controller copies the labels in that ordering.
func TestSlackTaskReporter_MissingTaskRecordDoesNotFailReport(t *testing.T) {
	task := slackScoringTask(kelos.TaskPhaseSucceeded)
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	tr := &SlackTaskReporter{Client: cl, Reporter: &fakeSlackReporter{}}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Errorf("ReportTaskStatus() error = %v, want nil when no TaskRecord exists yet", err)
	}
}

// A running Task's "accepted" ack is not the result, so reacting to it must not
// score the Task.
func TestSlackTaskReporter_DoesNotLabelNonTerminalPhase(t *testing.T) {
	task := slackScoringTask(kelos.TaskPhaseRunning)
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	reporter := &fakeSlackReporter{
		postFn: func(_ context.Context, _, _ string, _ SlackMessage) (string, error) {
			return "1234567890.999999", nil
		},
	}
	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("ReportTaskStatus() error = %v", err)
	}

	var updated kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if got, ok := updated.Labels[scoring.LabelSlackResultTS]; ok {
		t.Errorf("%s = %q, want the label to be absent for a non-terminal phase", scoring.LabelSlackResultTS, got)
	}
}

// When the progress message is edited in place to carry the result, that message
// is what a reader reacts to.
func TestSlackTaskReporter_LabelsEditedProgressMessage(t *testing.T) {
	task := slackScoringTask(kelos.TaskPhaseRunning)
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	reporter := &fakeSlackReporter{
		postFn: func(_ context.Context, _, _ string, _ SlackMessage) (string, error) {
			return "1234567890.777777", nil
		},
	}
	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	// First report posts the "accepted" reply and caches it as the progress
	// message for this Task.
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("ReportTaskStatus() accepted error = %v", err)
	}
	tr.setProgressTS(task.UID, "1234567890.777777")

	var current kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &current); err != nil {
		t.Fatalf("getting task: %v", err)
	}
	current.Status.Phase = kelos.TaskPhaseSucceeded
	if err := cl.Update(context.Background(), &current); err != nil {
		t.Fatalf("updating task phase: %v", err)
	}

	if err := tr.ReportTaskStatus(context.Background(), &current); err != nil {
		t.Fatalf("ReportTaskStatus() succeeded error = %v", err)
	}

	var updated kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if got := updated.Labels[scoring.LabelSlackResultTS]; got != "1234567890.777777" {
		t.Errorf("%s = %q, want the edited progress message timestamp", scoring.LabelSlackResultTS, got)
	}
}

// The channel comes from an unconstrained annotation. An invalid label value must
// not fail the persist: that would leave the reported phase unrecorded, so the
// next cycle would re-post the terminal message and fail again, forever.
func TestSlackTaskReporter_InvalidChannelSkipsLabelsButKeepsReporting(t *testing.T) {
	task := slackScoringTask(kelos.TaskPhaseSucceeded)
	task.Annotations[AnnotationSlackChannel] = "not a valid label value!"
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	posts := 0
	reporter := &fakeSlackReporter{
		postFn: func(_ context.Context, _, _ string, _ SlackMessage) (string, error) {
			posts++
			return "1234567890.999999", nil
		},
	}
	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("ReportTaskStatus() error = %v, want the invalid channel to be tolerated", err)
	}

	var updated kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if _, ok := updated.Labels[scoring.LabelSlackResultChannel]; ok {
		t.Error("result channel label was written with an invalid value")
	}
	if _, ok := updated.Labels[scoring.LabelSlackResultTS]; ok {
		t.Error("result timestamp label was written alongside an invalid channel")
	}

	// The reported phase must still be recorded, or the next cycle re-posts.
	if got := updated.Annotations[AnnotationSlackReportPhase]; got != "succeeded" {
		t.Fatalf("report phase = %q, want succeeded", got)
	}
	if err := tr.ReportTaskStatus(context.Background(), &updated); err != nil {
		t.Fatalf("second ReportTaskStatus() error = %v", err)
	}
	if posts != 1 {
		t.Errorf("posted %d times across two cycles, want 1", posts)
	}
}
