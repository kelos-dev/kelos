package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func TestGetTaskCommand_WatchWithOutput(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"get", "task", "--watch", "--output", "yaml"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when --watch is used with --output")
	}
	if !strings.Contains(err.Error(), "--watch is not supported with --output") {
		t.Errorf("Expected '--watch is not supported with --output' error, got: %v", err)
	}
}

func TestGetTaskCommand_WatchWithName(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"get", "task", "my-task", "--watch"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when --watch is used with a resource name")
	}
	if !strings.Contains(err.Error(), "--watch is only supported when listing resources") {
		t.Errorf("Expected '--watch is only supported when listing resources' error, got: %v", err)
	}
}

func TestGetTaskSpawnerCommand_WatchWithOutput(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"get", "taskspawner", "--watch", "--output", "json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when --watch is used with --output")
	}
	if !strings.Contains(err.Error(), "--watch is not supported with --output") {
		t.Errorf("Expected '--watch is not supported with --output' error, got: %v", err)
	}
}

func TestGetTaskSpawnerCommand_WatchWithName(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"get", "taskspawner", "my-spawner", "--watch"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when --watch is used with a resource name")
	}
	if !strings.Contains(err.Error(), "--watch is only supported when listing resources") {
		t.Errorf("Expected '--watch is only supported when listing resources' error, got: %v", err)
	}
}

func TestGetWorkspaceCommand_WatchWithOutput(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"get", "workspace", "--watch", "--output", "yaml"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when --watch is used with --output")
	}
	if !strings.Contains(err.Error(), "--watch is not supported with --output") {
		t.Errorf("Expected '--watch is not supported with --output' error, got: %v", err)
	}
}

func TestGetWorkspaceCommand_WatchWithName(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"get", "workspace", "my-ws", "--watch"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when --watch is used with a resource name")
	}
	if !strings.Contains(err.Error(), "--watch is only supported when listing resources") {
		t.Errorf("Expected '--watch is only supported when listing resources' error, got: %v", err)
	}
}

func TestPrintTaskRow(t *testing.T) {
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-task",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: axonv1alpha1.TaskSpec{
			Type: "claude-code",
		},
		Status: axonv1alpha1.TaskStatus{
			Phase: axonv1alpha1.TaskPhaseRunning,
		},
	}

	var buf bytes.Buffer
	printTaskRow(&buf, task, false)
	output := buf.String()

	if !strings.Contains(output, "test-task") {
		t.Errorf("expected task name in output, got %q", output)
	}
	if !strings.Contains(output, "claude-code") {
		t.Errorf("expected task type in output, got %q", output)
	}
	if !strings.Contains(output, "Running") {
		t.Errorf("expected phase in output, got %q", output)
	}
	if strings.Contains(output, "default") {
		t.Errorf("expected no namespace when allNamespaces is false, got %q", output)
	}
}

func TestPrintTaskRowAllNamespaces(t *testing.T) {
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-task",
			Namespace:         "ns-a",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: axonv1alpha1.TaskSpec{
			Type: "claude-code",
		},
		Status: axonv1alpha1.TaskStatus{
			Phase: axonv1alpha1.TaskPhaseRunning,
		},
	}

	var buf bytes.Buffer
	printTaskRow(&buf, task, true)
	output := buf.String()

	if !strings.Contains(output, "ns-a") {
		t.Errorf("expected namespace in output, got %q", output)
	}
	if !strings.Contains(output, "test-task") {
		t.Errorf("expected task name in output, got %q", output)
	}
}

func TestPrintTaskSpawnerRow(t *testing.T) {
	spawner := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-spawner",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				Cron: &axonv1alpha1.Cron{
					Schedule: "*/5 * * * *",
				},
			},
		},
		Status: axonv1alpha1.TaskSpawnerStatus{
			Phase:             axonv1alpha1.TaskSpawnerPhaseRunning,
			TotalDiscovered:   10,
			TotalTasksCreated: 5,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerRow(&buf, spawner, false)
	output := buf.String()

	if !strings.Contains(output, "test-spawner") {
		t.Errorf("expected spawner name in output, got %q", output)
	}
	if !strings.Contains(output, "Running") {
		t.Errorf("expected phase in output, got %q", output)
	}
	if !strings.Contains(output, "10") {
		t.Errorf("expected discovered count in output, got %q", output)
	}
	if !strings.Contains(output, "5") {
		t.Errorf("expected tasks created count in output, got %q", output)
	}
}

func TestPrintTaskSpawnerRowAllNamespaces(t *testing.T) {
	spawner := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-spawner",
			Namespace:         "ns-b",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				GitHubIssues: &axonv1alpha1.GitHubIssues{},
			},
			TaskTemplate: axonv1alpha1.TaskTemplate{
				WorkspaceRef: &axonv1alpha1.WorkspaceReference{
					Name: "my-ws",
				},
			},
		},
		Status: axonv1alpha1.TaskSpawnerStatus{
			Phase: axonv1alpha1.TaskSpawnerPhaseRunning,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerRow(&buf, spawner, true)
	output := buf.String()

	if !strings.Contains(output, "ns-b") {
		t.Errorf("expected namespace in output, got %q", output)
	}
	if !strings.Contains(output, "test-spawner") {
		t.Errorf("expected spawner name in output, got %q", output)
	}
}

func TestPrintWorkspaceRow(t *testing.T) {
	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-ws",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
			Ref:  "main",
		},
	}

	var buf bytes.Buffer
	printWorkspaceRow(&buf, ws, false)
	output := buf.String()

	if !strings.Contains(output, "test-ws") {
		t.Errorf("expected workspace name in output, got %q", output)
	}
	if !strings.Contains(output, "https://github.com/org/repo.git") {
		t.Errorf("expected repo URL in output, got %q", output)
	}
	if !strings.Contains(output, "main") {
		t.Errorf("expected ref in output, got %q", output)
	}
}

func TestPrintWorkspaceRowAllNamespaces(t *testing.T) {
	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-ws",
			Namespace:         "ns-c",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
			Ref:  "main",
		},
	}

	var buf bytes.Buffer
	printWorkspaceRow(&buf, ws, true)
	output := buf.String()

	if !strings.Contains(output, "ns-c") {
		t.Errorf("expected namespace in output, got %q", output)
	}
	if !strings.Contains(output, "test-ws") {
		t.Errorf("expected workspace name in output, got %q", output)
	}
}
