package controller

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/scoring"
)

// The record is the only durable anchor for a score once the Task's TTL removes
// it, so the result-message labels must be carried onto it.
func TestTaskRecordLabelsCarryResultCorrelationLabels(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
			Labels: map[string]string{
				"team":                          "platform",
				scoring.LabelSlackResultChannel: "C123",
				scoring.LabelSlackResultTS:      "1712345678.123456",
			},
		},
	}

	labels, err := taskRecordLabels(task)
	if err != nil {
		t.Fatalf("taskRecordLabels() error = %v", err)
	}
	if labels[scoring.LabelSlackResultChannel] != "C123" {
		t.Errorf("%s = %q, want C123", scoring.LabelSlackResultChannel, labels[scoring.LabelSlackResultChannel])
	}
	if labels[scoring.LabelSlackResultTS] != "1712345678.123456" {
		t.Errorf("%s = %q, want 1712345678.123456", scoring.LabelSlackResultTS, labels[scoring.LabelSlackResultTS])
	}
	if labels["team"] != "platform" {
		t.Errorf("team = %q, want platform (budget labels must be preserved)", labels["team"])
	}
}

// The budget label snapshot replaces the Task's live labels and is taken at
// admission, long before a result exists — so the correlation labels have to be
// merged in on top of it rather than inherited.
func TestTaskRecordLabelsCarryResultLabelsAlongsideBudgetSnapshot(t *testing.T) {
	snapshot, err := json.Marshal(map[string]string{"team": "platform"})
	if err != nil {
		t.Fatalf("encoding snapshot: %v", err)
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "task-1",
			Namespace:   "default",
			Annotations: map[string]string{taskBudgetLabelSnapshotAnnotation: string(snapshot)},
			Labels: map[string]string{
				"team":                          "platform",
				"added-after-admission":         "yes",
				scoring.LabelSlackResultChannel: "C123",
				scoring.LabelSlackResultTS:      "1712345678.123456",
			},
		},
	}

	labels, err := taskRecordLabels(task)
	if err != nil {
		t.Fatalf("taskRecordLabels() error = %v", err)
	}
	if labels[scoring.LabelSlackResultChannel] != "C123" {
		t.Errorf("%s = %q, want C123", scoring.LabelSlackResultChannel, labels[scoring.LabelSlackResultChannel])
	}
	if labels[scoring.LabelSlackResultTS] != "1712345678.123456" {
		t.Errorf("%s = %q, want 1712345678.123456", scoring.LabelSlackResultTS, labels[scoring.LabelSlackResultTS])
	}
	if labels["team"] != "platform" {
		t.Errorf("team = %q, want platform from the snapshot", labels["team"])
	}
	// The snapshot is authoritative for budget matching: labels added after
	// admission must not retroactively change which budgets a record counts against.
	if _, ok := labels["added-after-admission"]; ok {
		t.Error("label added after admission leaked into the record's budget labels")
	}
}

func TestTaskRecordLabelsWithoutResultLabels(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
	}

	labels, err := taskRecordLabels(task)
	if err != nil {
		t.Fatalf("taskRecordLabels() error = %v", err)
	}
	if labels != nil {
		t.Errorf("taskRecordLabels() = %#v, want nil for an unlabelled Task", labels)
	}
}

// The interleaving that used to lose the labels entirely: the reporter labels the
// Task and tries to patch the record before it exists (a single attempt — the
// reporter does not revisit the Task once it has recorded the reported phase),
// then this controller creates the record from a Task revision that predates the
// label write. Without a backfill on the AlreadyExists path the labels reach
// neither object and late-arriving scores silently never resolve.
func TestCreateTaskRecordBackfillsResultLabelsOnExistingRecord(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	completionTime := metav1.Now()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
			UID:       "task-uid-1",
			Labels: map[string]string{
				scoring.LabelSlackResultChannel: "C123",
				scoring.LabelSlackResultTS:      "1712345678.123456",
			},
		},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: &completionTime,
			Usage:          &kelos.TaskUsage{},
		},
	}
	// The record was already created from a Task revision without the labels.
	existing := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "task-uid-1", Namespace: "default"},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "task-1", UID: "task-uid-1"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: &completionTime,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task, existing).Build()
	enforcer := &budgetEnforcer{Client: cl}

	if err := enforcer.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("createTaskRecord() error = %v", err)
	}

	var updated kelos.TaskRecord
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(existing), &updated); err != nil {
		t.Fatalf("getting record: %v", err)
	}
	if got := updated.Labels[scoring.LabelSlackResultChannel]; got != "C123" {
		t.Errorf("%s = %q, want C123", scoring.LabelSlackResultChannel, got)
	}
	if got := updated.Labels[scoring.LabelSlackResultTS]; got != "1712345678.123456" {
		t.Errorf("%s = %q, want 1712345678.123456", scoring.LabelSlackResultTS, got)
	}
}

// A Task with no result labels must not provoke a write to the existing record.
func TestCreateTaskRecordSkipsBackfillWithoutResultLabels(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	completionTime := metav1.Now()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default", UID: "task-uid-1"},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: &completionTime,
			Usage:          &kelos.TaskUsage{},
		},
	}
	existing := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "task-uid-1", Namespace: "default", ResourceVersion: "999"},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "task-1", UID: "task-uid-1"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: &completionTime,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task, existing).Build()
	enforcer := &budgetEnforcer{Client: cl}

	if err := enforcer.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("createTaskRecord() error = %v", err)
	}

	var updated kelos.TaskRecord
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(existing), &updated); err != nil {
		t.Fatalf("getting record: %v", err)
	}
	if updated.ResourceVersion != existing.ResourceVersion {
		t.Errorf("record was written (resourceVersion %s -> %s), want no write",
			existing.ResourceVersion, updated.ResourceVersion)
	}
}
