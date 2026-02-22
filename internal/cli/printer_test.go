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
	now := time.Now()
	startTime := metav1.NewTime(now.Add(-30 * time.Minute))
	tasks := []axonv1alpha1.Task{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-one",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  "claude-sonnet-4-20250514",
				Branch: "feature/test",
				WorkspaceRef: &axonv1alpha1.WorkspaceReference{
					Name: "my-ws",
				},
				AgentConfigRef: &axonv1alpha1.AgentConfigReference{
					Name: "my-config",
				},
			},
			Status: axonv1alpha1.TaskStatus{
				Phase:     axonv1alpha1.TaskPhaseRunning,
				StartTime: &startTime,
			},
		},
	}

	var buf bytes.Buffer
	printTaskTable(&buf, tasks, false)
	output := buf.String()

	for _, header := range []string{"NAME", "TYPE", "PHASE", "BRANCH", "WORKSPACE", "AGENT CONFIG", "DURATION", "AGE"} {
		if !strings.Contains(output, header) {
			t.Errorf("expected header %s in output, got %q", header, output)
		}
	}
	if strings.Contains(output, "NAMESPACE") {
		t.Errorf("expected no NAMESPACE header when allNamespaces is false, got %q", output)
	}
	for _, val := range []string{"task-one", "feature/test", "my-ws", "my-config"} {
		if !strings.Contains(output, val) {
			t.Errorf("expected %s in output, got %q", val, output)
		}
	}
}

func TestPrintTaskTableAllNamespaces(t *testing.T) {
	now := time.Now()
	startTime := metav1.NewTime(now.Add(-90 * time.Minute))
	completionTime := metav1.NewTime(now.Add(-60 * time.Minute))
	tasks := []axonv1alpha1.Task{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-one",
				Namespace:         "ns-a",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Branch: "feat/one",
			},
			Status: axonv1alpha1.TaskStatus{
				Phase: axonv1alpha1.TaskPhaseRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-two",
				Namespace:         "ns-b",
				CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			},
			Spec: axonv1alpha1.TaskSpec{
				Type: "codex",
			},
			Status: axonv1alpha1.TaskStatus{
				Phase:          axonv1alpha1.TaskPhaseSucceeded,
				StartTime:      &startTime,
				CompletionTime: &completionTime,
			},
		},
	}

	var buf bytes.Buffer
	printTaskTable(&buf, tasks, true)
	output := buf.String()

	for _, header := range []string{"NAMESPACE", "NAME", "TYPE", "PHASE", "BRANCH", "WORKSPACE", "AGENT CONFIG", "DURATION", "AGE"} {
		if !strings.Contains(output, header) {
			t.Errorf("expected header %s in output, got %q", header, output)
		}
	}
	for _, val := range []string{"ns-a", "ns-b", "feat/one"} {
		if !strings.Contains(output, val) {
			t.Errorf("expected %s in output, got %q", val, output)
		}
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

func TestPrintTaskTableSingleItem(t *testing.T) {
	task := axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-task",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-30 * time.Minute)),
		},
		Spec: axonv1alpha1.TaskSpec{
			Type: "claude-code",
		},
		Status: axonv1alpha1.TaskStatus{
			Phase: axonv1alpha1.TaskPhaseSucceeded,
		},
	}

	var buf bytes.Buffer
	printTaskTable(&buf, []axonv1alpha1.Task{task}, false)
	output := buf.String()

	if !strings.Contains(output, "NAME") {
		t.Errorf("expected header NAME in output, got %q", output)
	}
	if !strings.Contains(output, "my-task") {
		t.Errorf("expected my-task in output, got %q", output)
	}
	if !strings.Contains(output, "claude-code") {
		t.Errorf("expected type claude-code in output, got %q", output)
	}
	if !strings.Contains(output, string(axonv1alpha1.TaskPhaseSucceeded)) {
		t.Errorf("expected phase Succeeded in output, got %q", output)
	}
	if strings.Contains(output, "Prompt:") {
		t.Errorf("table view should not contain detail fields, got %q", output)
	}
}

func TestPrintTaskSpawnerTableSingleItem(t *testing.T) {
	spawner := axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-spawner",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				Cron: &axonv1alpha1.Cron{
					Schedule: "0 * * * *",
				},
			},
		},
		Status: axonv1alpha1.TaskSpawnerStatus{
			Phase:             axonv1alpha1.TaskSpawnerPhaseRunning,
			TotalDiscovered:   5,
			TotalTasksCreated: 3,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, []axonv1alpha1.TaskSpawner{spawner}, false)
	output := buf.String()

	if !strings.Contains(output, "NAME") {
		t.Errorf("expected header NAME in output, got %q", output)
	}
	if !strings.Contains(output, "my-spawner") {
		t.Errorf("expected my-spawner in output, got %q", output)
	}
	if !strings.Contains(output, "cron: 0 * * * *") {
		t.Errorf("expected cron source in output, got %q", output)
	}
	if strings.Contains(output, "Poll Interval:") {
		t.Errorf("table view should not contain detail fields, got %q", output)
	}
}

func TestPrintWorkspaceTableSingleItem(t *testing.T) {
	ws := axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-workspace",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
			Ref:  "main",
		},
	}

	var buf bytes.Buffer
	printWorkspaceTable(&buf, []axonv1alpha1.Workspace{ws}, false)
	output := buf.String()

	if !strings.Contains(output, "NAME") {
		t.Errorf("expected header NAME in output, got %q", output)
	}
	if !strings.Contains(output, "my-workspace") {
		t.Errorf("expected my-workspace in output, got %q", output)
	}
	if !strings.Contains(output, "https://github.com/org/repo.git") {
		t.Errorf("expected repo URL in output, got %q", output)
	}
	if strings.Contains(output, "Secret:") {
		t.Errorf("table view should not contain detail fields, got %q", output)
	}
}

func TestPrintTaskSpawnerDetail(t *testing.T) {
	lastDiscovery := metav1.NewTime(time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC))
	spawner := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				Cron: &axonv1alpha1.Cron{
					Schedule: "0 * * * *",
				},
			},
			TaskTemplate: axonv1alpha1.TaskTemplate{
				Type:  "claude-code",
				Model: "claude-sonnet-4-20250514",
			},
			PollInterval: "5m",
		},
		Status: axonv1alpha1.TaskSpawnerStatus{
			Phase:             axonv1alpha1.TaskSpawnerPhaseRunning,
			DeploymentName:    "my-spawner-deploy",
			TotalDiscovered:   10,
			TotalTasksCreated: 7,
			LastDiscoveryTime: &lastDiscovery,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, spawner)
	output := buf.String()

	for _, expected := range []string{
		"Name:", "my-spawner",
		"Namespace:", "default",
		"Source:", "Cron",
		"Schedule:", "0 * * * *",
		"Task Type:", "claude-code",
		"Model:", "claude-sonnet-4-20250514",
		"Poll Interval:", "5m",
		"Deployment:", "my-spawner-deploy",
		"Discovered:", "10",
		"Tasks Created:", "7",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in detail output, got %q", expected, output)
		}
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

func TestTaskDuration(t *testing.T) {
	now := time.Now()
	startTime := metav1.NewTime(now.Add(-30 * time.Minute))
	completionTime := metav1.NewTime(now.Add(-10 * time.Minute))

	tests := []struct {
		name   string
		status axonv1alpha1.TaskStatus
		want   string
	}{
		{
			name:   "no start time",
			status: axonv1alpha1.TaskStatus{},
			want:   "-",
		},
		{
			name: "completed task",
			status: axonv1alpha1.TaskStatus{
				StartTime:      &startTime,
				CompletionTime: &completionTime,
			},
			want: "20m",
		},
		{
			name: "running task",
			status: axonv1alpha1.TaskStatus{
				StartTime: &startTime,
			},
			want: "30m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskDuration(&tt.status)
			if got != tt.want {
				t.Errorf("taskDuration() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrintTaskDetail(t *testing.T) {
	now := time.Now()
	startTime := metav1.NewTime(now.Add(-30 * time.Minute))
	completionTime := metav1.NewTime(now.Add(-10 * time.Minute))
	ttl := int32(3600)
	timeout := int64(7200)

	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "full-task",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "Fix the bug",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
			Model:     "claude-sonnet-4-20250514",
			Image:     "custom-image:latest",
			Branch:    "feature/fix",
			DependsOn: []string{"task-a", "task-b"},
			WorkspaceRef: &axonv1alpha1.WorkspaceReference{
				Name: "my-ws",
			},
			AgentConfigRef: &axonv1alpha1.AgentConfigReference{
				Name: "my-config",
			},
			TTLSecondsAfterFinished: &ttl,
			PodOverrides: &axonv1alpha1.PodOverrides{
				ActiveDeadlineSeconds: &timeout,
			},
		},
		Status: axonv1alpha1.TaskStatus{
			Phase:          axonv1alpha1.TaskPhaseSucceeded,
			JobName:        "full-task-job",
			PodName:        "full-task-pod",
			StartTime:      &startTime,
			CompletionTime: &completionTime,
			Message:        "Task completed successfully",
			Outputs:        []string{"https://github.com/org/repo/pull/1"},
			Results:        map[string]string{"pr": "1"},
		},
	}

	var buf bytes.Buffer
	printTaskDetail(&buf, task)
	output := buf.String()

	for _, expected := range []string{
		"full-task",
		"claude-code",
		"Succeeded",
		"Fix the bug",
		"my-secret",
		"claude-sonnet-4-20250514",
		"custom-image:latest",
		"feature/fix",
		"task-a, task-b",
		"my-ws",
		"my-config",
		"3600s",
		"7200s",
		"full-task-job",
		"full-task-pod",
		"Duration:",
		"Task completed successfully",
		"https://github.com/org/repo/pull/1",
		"pr=1",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in output, got:\n%s", expected, output)
		}
	}
}

func TestPrintAgentConfigTable(t *testing.T) {
	configs := []axonv1alpha1.AgentConfig{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "config-one",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.AgentConfigSpec{
				AgentsMD: "some instructions",
				Plugins: []axonv1alpha1.PluginSpec{
					{Name: "axon"},
				},
				MCPServers: []axonv1alpha1.MCPServerSpec{
					{Name: "github", Type: "http"},
					{Name: "local", Type: "stdio"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "config-two",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			},
			Spec: axonv1alpha1.AgentConfigSpec{},
		},
	}

	var buf bytes.Buffer
	printAgentConfigTable(&buf, configs, false)
	output := buf.String()

	for _, header := range []string{"NAME", "PLUGINS", "MCP SERVERS", "AGE"} {
		if !strings.Contains(output, header) {
			t.Errorf("expected header %s in output, got %q", header, output)
		}
	}
	if strings.Contains(output, "NAMESPACE") {
		t.Errorf("expected no NAMESPACE header when allNamespaces is false, got %q", output)
	}
	if !strings.Contains(output, "config-one") {
		t.Errorf("expected config-one in output, got %q", output)
	}
	if !strings.Contains(output, "config-two") {
		t.Errorf("expected config-two in output, got %q", output)
	}
}

func TestPrintAgentConfigTableAllNamespaces(t *testing.T) {
	configs := []axonv1alpha1.AgentConfig{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "config-one",
				Namespace:         "ns-a",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.AgentConfigSpec{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "config-two",
				Namespace:         "ns-b",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			},
			Spec: axonv1alpha1.AgentConfigSpec{},
		},
	}

	var buf bytes.Buffer
	printAgentConfigTable(&buf, configs, true)
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

func TestPrintAgentConfigTableSingleItem(t *testing.T) {
	ac := axonv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-config",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
		},
		Spec: axonv1alpha1.AgentConfigSpec{
			Plugins: []axonv1alpha1.PluginSpec{
				{Name: "axon"},
			},
		},
	}

	var buf bytes.Buffer
	printAgentConfigTable(&buf, []axonv1alpha1.AgentConfig{ac}, false)
	output := buf.String()

	if !strings.Contains(output, "NAME") {
		t.Errorf("expected header NAME in output, got %q", output)
	}
	if !strings.Contains(output, "my-config") {
		t.Errorf("expected my-config in output, got %q", output)
	}
	if strings.Contains(output, "Agents MD:") {
		t.Errorf("table view should not contain detail fields, got %q", output)
	}
}

func TestPrintAgentConfigDetail(t *testing.T) {
	ac := &axonv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-config",
			Namespace: "default",
		},
		Spec: axonv1alpha1.AgentConfigSpec{
			AgentsMD: "Build and test instructions",
			Plugins: []axonv1alpha1.PluginSpec{
				{
					Name: "axon",
					Skills: []axonv1alpha1.SkillDefinition{
						{Name: "review", Content: "review content"},
					},
					Agents: []axonv1alpha1.AgentDefinition{
						{Name: "triage", Content: "triage content"},
					},
				},
			},
			MCPServers: []axonv1alpha1.MCPServerSpec{
				{Name: "github", Type: "http"},
				{Name: "local-tool", Type: "stdio"},
			},
		},
	}

	var buf bytes.Buffer
	printAgentConfigDetail(&buf, ac)
	output := buf.String()

	for _, expected := range []string{
		"Name:", "my-config",
		"Namespace:", "default",
		"Agents MD:", "Build and test instructions",
		"Plugins:", "axon",
		"skills=[review]",
		"agents=[triage]",
		"MCP Servers:", "github (http)",
		"local-tool (stdio)",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in detail output, got %q", expected, output)
		}
	}
}

func TestPrintAgentConfigDetailMinimal(t *testing.T) {
	ac := &axonv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-config",
			Namespace: "default",
		},
		Spec: axonv1alpha1.AgentConfigSpec{},
	}

	var buf bytes.Buffer
	printAgentConfigDetail(&buf, ac)
	output := buf.String()

	if !strings.Contains(output, "minimal-config") {
		t.Errorf("expected config name in output, got %q", output)
	}
	for _, absent := range []string{
		"Agents MD:",
		"Plugins:",
		"MCP Servers:",
	} {
		if strings.Contains(output, absent) {
			t.Errorf("expected no %s field for minimal config, got %q", absent, output)
		}
	}
}

func TestPrintTaskDetailMinimal(t *testing.T) {
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-task",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "Do something",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "secret"},
			},
		},
		Status: axonv1alpha1.TaskStatus{
			Phase: axonv1alpha1.TaskPhasePending,
		},
	}

	var buf bytes.Buffer
	printTaskDetail(&buf, task)
	output := buf.String()

	if !strings.Contains(output, "minimal-task") {
		t.Errorf("expected task name in output, got:\n%s", output)
	}

	for _, absent := range []string{
		"Model:",
		"Image:",
		"Branch:",
		"Depends On:",
		"Workspace:",
		"Agent Config:",
		"TTL:",
		"Timeout:",
		"Job:",
		"Pod:",
		"Start Time:",
		"Completion Time:",
		"Duration:",
		"Message:",
		"Outputs:",
		"Results:",
	} {
		if strings.Contains(output, absent) {
			t.Errorf("expected no %s field for minimal task, got:\n%s", absent, output)
		}
	}
}
