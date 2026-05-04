package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestWatchTask_SucceededReturnsNil(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-ok",
			Namespace: "default",
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseSucceeded,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task).WithStatusSubresource(task).Build()
	var out, errOut bytes.Buffer

	if err := watchTask(context.Background(), cl, "task-ok", "default", &out, &errOut); err != nil {
		t.Fatalf("watchTask returned error on Succeeded: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "task/task-ok Succeeded") {
		t.Errorf("stdout = %q, want it to contain phase line", got)
	}
	if got := errOut.String(); got != "" {
		t.Errorf("stderr = %q, want empty on Succeeded", got)
	}
}

func TestWatchTask_FailedReturnsError(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-bad",
			Namespace: "default",
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhaseFailed,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task).WithStatusSubresource(task).Build()
	var out, errOut bytes.Buffer

	err := watchTask(context.Background(), cl, "task-bad", "default", &out, &errOut)
	if err == nil {
		t.Fatal("watchTask returned nil on Failed, want non-nil error")
	}
	if !strings.Contains(err.Error(), "task task-bad failed") {
		t.Errorf("error = %v, want it to contain 'task task-bad failed'", err)
	}

	if got := out.String(); !strings.Contains(got, "task/task-bad Failed") {
		t.Errorf("stdout = %q, want it to contain phase line", got)
	}
	hint := errOut.String()
	if !strings.Contains(hint, "kelos logs task-bad") {
		t.Errorf("stderr = %q, want it to suggest 'kelos logs task-bad'", hint)
	}
	if !strings.Contains(hint, "kelos get tasks task-bad -d") {
		t.Errorf("stderr = %q, want it to suggest 'kelos get tasks task-bad -d'", hint)
	}
}
