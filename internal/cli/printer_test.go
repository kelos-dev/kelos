package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func TestPrintWorkspaceTable(t *testing.T) {
	workspaces := []axonv1alpha1.Workspace{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-one",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/org/repo.git",
				Ref:  "main",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-two",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/org/other.git",
			},
		},
	}

	var buf bytes.Buffer
	printWorkspaceTable(&buf, workspaces, false)
	output := buf.String()

	if !strings.Contains(output, "NAME") {
		t.Errorf("expected header NAME in output, got %q", output)
	}
	if !strings.Contains(output, "REPO") {
		t.Errorf("expected header REPO in output, got %q", output)
	}
	if strings.Contains(output, "NAMESPACE") {
		t.Errorf("expected no NAMESPACE header when allNamespaces is false, got %q", output)
	}
	if !strings.Contains(output, "ws-one") {
		t.Errorf("expected ws-one in output, got %q", output)
	}
	if !strings.Contains(output, "ws-two") {
		t.Errorf("expected ws-two in output, got %q", output)
	}
	if !strings.Contains(output, "https://github.com/org/repo.git") {
		t.Errorf("expected repo URL in output, got %q", output)
	}
}

func TestPrintWorkspaceTableAllNamespaces(t *testing.T) {
	workspaces := []axonv1alpha1.Workspace{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-one",
				Namespace:         "ns-a",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/org/repo.git",
				Ref:  "main",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-two",
				Namespace:         "ns-b",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/org/other.git",
			},
		},
	}

	var buf bytes.Buffer
	printWorkspaceTable(&buf, workspaces, true)
	output := buf.String()

	if !strings.Contains(output, "NAMESPACE") {
		t.Errorf("expected NAMESPACE header when allNamespaces is true, got %q", output)
	}
	if !strings.Contains(output, "ns-a") {
		t.Errorf("expected namespace ns-a in output, got %q", output)
	}
	if !strings.Contains(output, "ns-b") {
		t.Errorf("expected namespace ns-b in output, got %q", output)
	}
}

func TestPrintTaskTable(t *testing.T) {
	tasks := []axonv1alpha1.Task{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-one",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.TaskSpec{
				Type: "claude-code",
			},
			Status: axonv1alpha1.TaskStatus{
				Phase: axonv1alpha1.TaskPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskTable(&buf, tasks, false)
	output := buf.String()

	if !strings.Contains(output, "NAME") {
		t.Errorf("expected header NAME in output, got %q", output)
	}
	if strings.Contains(output, "NAMESPACE") {
		t.Errorf("expected no NAMESPACE header when allNamespaces is false, got %q", output)
	}
	if !strings.Contains(output, "task-one") {
		t.Errorf("expected task-one in output, got %q", output)
	}
}

func TestPrintTaskTableAllNamespaces(t *testing.T) {
	tasks := []axonv1alpha1.Task{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-one",
				Namespace:         "ns-a",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.TaskSpec{
				Type: "claude-code",
			},
			Status: axonv1alpha1.TaskStatus{
				Phase: axonv1alpha1.TaskPhaseRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-two",
				Namespace:         "ns-b",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			},
			Spec: axonv1alpha1.TaskSpec{
				Type: "codex",
			},
			Status: axonv1alpha1.TaskStatus{
				Phase: axonv1alpha1.TaskPhaseSucceeded,
			},
		},
	}

	var buf bytes.Buffer
	printTaskTable(&buf, tasks, true)
	output := buf.String()

	if !strings.Contains(output, "NAMESPACE") {
		t.Errorf("expected NAMESPACE header when allNamespaces is true, got %q", output)
	}
	if !strings.Contains(output, "ns-a") {
		t.Errorf("expected namespace ns-a in output, got %q", output)
	}
	if !strings.Contains(output, "ns-b") {
		t.Errorf("expected namespace ns-b in output, got %q", output)
	}
}

func TestPrintTaskSpawnerTable(t *testing.T) {
	spawners := []axonv1alpha1.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "spawner-one",
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
				Phase: axonv1alpha1.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "NAME") {
		t.Errorf("expected header NAME in output, got %q", output)
	}
	if strings.Contains(output, "NAMESPACE") {
		t.Errorf("expected no NAMESPACE header when allNamespaces is false, got %q", output)
	}
	if !strings.Contains(output, "spawner-one") {
		t.Errorf("expected spawner-one in output, got %q", output)
	}
}

func TestPrintTaskSpawnerTableAllNamespaces(t *testing.T) {
	spawners := []axonv1alpha1.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "spawner-one",
				Namespace:         "ns-a",
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
				Phase: axonv1alpha1.TaskSpawnerPhaseRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "spawner-two",
				Namespace:         "ns-b",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
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
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, true)
	output := buf.String()

	if !strings.Contains(output, "NAMESPACE") {
		t.Errorf("expected NAMESPACE header when allNamespaces is true, got %q", output)
	}
	if !strings.Contains(output, "ns-a") {
		t.Errorf("expected namespace ns-a in output, got %q", output)
	}
	if !strings.Contains(output, "ns-b") {
		t.Errorf("expected namespace ns-b in output, got %q", output)
	}
}

func TestPrintWorkspaceDetail(t *testing.T) {
	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-workspace",
			Namespace: "default",
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
			Ref:  "main",
			SecretRef: &axonv1alpha1.SecretReference{
				Name: "gh-token",
			},
		},
	}

	var buf bytes.Buffer
	printWorkspaceDetail(&buf, ws)
	output := buf.String()

	if !strings.Contains(output, "my-workspace") {
		t.Errorf("expected workspace name in output, got %q", output)
	}
	if !strings.Contains(output, "https://github.com/org/repo.git") {
		t.Errorf("expected repo URL in output, got %q", output)
	}
	if !strings.Contains(output, "main") {
		t.Errorf("expected ref in output, got %q", output)
	}
	if !strings.Contains(output, "gh-token") {
		t.Errorf("expected secret name in output, got %q", output)
	}
}

func TestPrintTaskDetail(t *testing.T) {
	now := metav1.Now()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-task",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "Fix the bug",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
			Model: "claude-sonnet-4-20250514",
			Image: "my-custom-agent:v1",
			WorkspaceRef: &axonv1alpha1.WorkspaceReference{
				Name: "my-workspace",
			},
		},
		Status: axonv1alpha1.TaskStatus{
			Phase:          axonv1alpha1.TaskPhaseSucceeded,
			JobName:        "my-task",
			PodName:        "my-task-abc123",
			StartTime:      &now,
			CompletionTime: &now,
			Message:        "Task completed",
			Outputs:        []string{"branch: fix-branch", "https://github.com/org/repo/pull/1"},
		},
	}

	var buf bytes.Buffer
	printTaskDetail(&buf, task)
	output := buf.String()

	for _, expected := range []string{
		"my-task",
		"claude-code",
		"Succeeded",
		"Fix the bug",
		"my-secret",
		"api-key",
		"claude-sonnet-4-20250514",
		"my-custom-agent:v1",
		"my-workspace",
		"my-task-abc123",
		"Task completed",
		"branch: fix-branch",
		"https://github.com/org/repo/pull/1",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in output, got %q", expected, output)
		}
	}
}

func TestPrintTaskDetailWithoutOptionalFields(t *testing.T) {
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-task",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "Hello",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	var buf bytes.Buffer
	printTaskDetail(&buf, task)
	output := buf.String()

	if !strings.Contains(output, "minimal-task") {
		t.Errorf("expected task name in output, got %q", output)
	}
	if strings.Contains(output, "Model:") {
		t.Errorf("expected no Model field when model is empty, got %q", output)
	}
	if strings.Contains(output, "Image:") {
		t.Errorf("expected no Image field when image is empty, got %q", output)
	}
	if strings.Contains(output, "Workspace:") {
		t.Errorf("expected no Workspace field when workspaceRef is nil, got %q", output)
	}
}

func TestPrintTaskSpawnerDetail(t *testing.T) {
	now := metav1.Now()
	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				GitHubIssues: &axonv1alpha1.GitHubIssues{
					Types:  []string{"issues", "pulls"},
					State:  "open",
					Labels: []string{"bug", "help-wanted"},
				},
			},
			TaskTemplate: axonv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "creds"},
				},
				Model: "claude-sonnet-4-20250514",
				Image: "my-custom-agent:v2",
				WorkspaceRef: &axonv1alpha1.WorkspaceReference{
					Name: "my-workspace",
				},
			},
			PollInterval: "10m",
		},
		Status: axonv1alpha1.TaskSpawnerStatus{
			Phase:             axonv1alpha1.TaskSpawnerPhaseRunning,
			DeploymentName:    "my-spawner",
			TotalDiscovered:   10,
			TotalTasksCreated: 5,
			LastDiscoveryTime: &now,
			Message:           "Discovered 10 items",
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, ts)
	output := buf.String()

	for _, expected := range []string{
		"my-spawner",
		"Running",
		"my-workspace",
		"GitHub Issues",
		"claude-code",
		"claude-sonnet-4-20250514",
		"my-custom-agent:v2",
		"10m",
		"10",
		"5",
		"Discovered 10 items",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in output, got %q", expected, output)
		}
	}
}

func TestPrintTaskSpawnerDetailWithoutOptionalFields(t *testing.T) {
	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				Cron: &axonv1alpha1.Cron{
					Schedule: "0 9 * * 1",
				},
			},
			TaskTemplate: axonv1alpha1.TaskTemplate{
				Type: "codex",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeAPIKey,
					SecretRef: axonv1alpha1.SecretReference{Name: "creds"},
				},
			},
			PollInterval: "5m",
		},
		Status: axonv1alpha1.TaskSpawnerStatus{
			Phase: axonv1alpha1.TaskSpawnerPhasePending,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, ts)
	output := buf.String()

	if !strings.Contains(output, "minimal-spawner") {
		t.Errorf("expected spawner name in output, got %q", output)
	}
	if !strings.Contains(output, "Cron") {
		t.Errorf("expected Cron source in output, got %q", output)
	}
	if !strings.Contains(output, "0 9 * * 1") {
		t.Errorf("expected schedule in output, got %q", output)
	}
	if strings.Contains(output, "Model:") {
		t.Errorf("expected no Model field when model is empty, got %q", output)
	}
	if strings.Contains(output, "Image:") {
		t.Errorf("expected no Image field when image is empty, got %q", output)
	}
}

func TestPrintWorkspaceDetailWithoutOptionalFields(t *testing.T) {
	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-ws",
			Namespace: "default",
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
		},
	}

	var buf bytes.Buffer
	printWorkspaceDetail(&buf, ws)
	output := buf.String()

	if !strings.Contains(output, "minimal-ws") {
		t.Errorf("expected workspace name in output, got %q", output)
	}
	if strings.Contains(output, "Ref:") {
		t.Errorf("expected no Ref field when ref is empty, got %q", output)
	}
	if strings.Contains(output, "Secret:") {
		t.Errorf("expected no Secret field when secretRef is nil, got %q", output)
	}
}
