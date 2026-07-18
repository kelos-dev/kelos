package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestPrintSessionDetailIncludesActivity(t *testing.T) {
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "default"},
		Spec:       kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
		Status: kelos.SessionStatus{
			Phase: kelos.SessionPhaseReady,
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionActive,
				Status: metav1.ConditionTrue,
				Reason: "TurnActive",
			}},
		},
	}

	var output bytes.Buffer
	printSessionDetail(&output, session)
	if !regexp.MustCompile(`(?m)^Active:\s+True$`).MatchString(output.String()) {
		t.Fatalf("Session detail is missing activity: \n%s", output.String())
	}
}

func TestPrintWorkspaceTable(t *testing.T) {
	workspaces := []kelos.Workspace{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-one",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/org/repo.git",
				Ref:  "main",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-two",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			},
			Spec: kelos.WorkspaceSpec{
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
	workspaces := []kelos.Workspace{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-one",
				Namespace:         "ns-a",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.WorkspaceSpec{
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
			Spec: kelos.WorkspaceSpec{
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
	tasks := []kelos.Task{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-one",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  "claude-sonnet-4-20250514",
				Branch: "feature/test",
				WorkspaceRef: &kelos.WorkspaceReference{
					Name: "my-ws",
				},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "my-config"}},
			},
			Status: kelos.TaskStatus{
				Phase:     kelos.TaskPhaseRunning,
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

func TestPrintTaskTableCanonicalWorkerFields(t *testing.T) {
	now := time.Now()
	tasks := []kelos.Task{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-one",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpec{
				Worker: &kelos.WorkerSpec{
					Type: "codex",
					WorkspaceRef: &kelos.WorkspaceReference{
						Name: "worker-ws",
					},
					AgentConfigRefs: []kelos.AgentConfigReference{{Name: "worker-config"}},
				},
			},
			Status: kelos.TaskStatus{Phase: kelos.TaskPhaseRunning},
		},
	}

	var buf bytes.Buffer
	printTaskTable(&buf, tasks, false)
	output := buf.String()

	for _, expected := range []string{"task-one", "codex", "worker-ws", "worker-config"} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in output, got %q", expected, output)
		}
	}
}

func TestPrintTaskTableAllNamespaces(t *testing.T) {
	now := time.Now()
	startTime := metav1.NewTime(now.Add(-90 * time.Minute))
	completionTime := metav1.NewTime(now.Add(-60 * time.Minute))
	tasks := []kelos.Task{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-one",
				Namespace:         "ns-a",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Branch: "feat/one",
			},
			Status: kelos.TaskStatus{
				Phase: kelos.TaskPhaseRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "task-two",
				Namespace:         "ns-b",
				CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			},
			Spec: kelos.TaskSpec{
				Type: "codex",
			},
			Status: kelos.TaskStatus{
				Phase:          kelos.TaskPhaseSucceeded,
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
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "spawner-one",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Cron: &kelos.Cron{
						Schedule: "*/5 * * * *",
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
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
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "spawner-one",
				Namespace:         "ns-a",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Cron: &kelos.Cron{
						Schedule: "*/5 * * * *",
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "spawner-two",
				Namespace:         "ns-b",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubIssues: &kelos.GitHubIssues{},
				},
				TaskTemplate: kelos.TaskTemplate{
					WorkspaceRef: &kelos.WorkspaceReference{
						Name: "my-ws",
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
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

func TestPrintTaskSpawnerTableGitHubPullRequests(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pr-spawner",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubPullRequests: &kelos.GitHubPullRequests{},
				},
				TaskTemplate: kelos.TaskTemplate{
					WorkspaceRef: &kelos.WorkspaceReference{
						Name: "my-ws",
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "GitHub Pull Requests") {
		t.Errorf("expected 'GitHub Pull Requests' as source in output, got %q", output)
	}
	if strings.Contains(output, "my-ws") {
		t.Errorf("expected workspace name not to appear in source column, got %q", output)
	}
}

func TestPrintTaskSpawnerTableGitHubIssuesWithWorkspace(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "issue-spawner",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubIssues: &kelos.GitHubIssues{},
				},
				TaskTemplate: kelos.TaskTemplate{
					WorkspaceRef: &kelos.WorkspaceReference{
						Name: "my-ws",
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "GitHub Issues") {
		t.Errorf("expected 'GitHub Issues' as source in output, got %q", output)
	}
	if strings.Contains(output, "my-ws") {
		t.Errorf("expected workspace name not to appear in source column, got %q", output)
	}
}

func TestPrintTaskSpawnerTableSlack(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "chat-trigger",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Slack: &kelos.Slack{
						Channels: []string{"C0123456789"},
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "Slack") {
		t.Errorf("expected 'Slack' as source in output, got %q", output)
	}
}

func TestPrintTaskSpawnerDetailSlack(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "slack-spawner",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				Slack: &kelos.Slack{
					Channels: []string{"C0123456789", "C9876543210"},
					Triggers: []kelos.SlackTrigger{
						{Pattern: "deploy"},
						{Pattern: "rollback"},
					},
					ExcludePatterns: []string{"^ignore"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase:             kelos.TaskSpawnerPhaseRunning,
			TotalDiscovered:   1,
			TotalTasksCreated: 1,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, spawner)
	output := buf.String()

	for _, expected := range []string{
		"Source:             Slack",
		"Channels:           [C0123456789 C9876543210]",
		"Triggers:           [deploy rollback]",
		"Exclude Patterns:   [^ignore]",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in detail output, got:\n%s", expected, output)
		}
	}
}

func TestPrintTaskSpawnerTableGitHubPullRequestsNoWorkspace(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pr-spawner",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubPullRequests: &kelos.GitHubPullRequests{},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "GitHub Pull Requests") {
		t.Errorf("expected 'GitHub Pull Requests' as source in output, got %q", output)
	}
}

func TestPrintTaskSpawnerTableJira(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "jira-spawner",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Jira: &kelos.Jira{
						BaseURL: "https://mycompany.atlassian.net",
						Project: "PROJ",
						SecretRef: kelos.SecretReference{
							Name: "jira-secret",
						},
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "PROJ") {
		t.Errorf("expected Jira project as source in output, got %q", output)
	}
}

func TestPrintTaskSpawnerTableGitHubWebhook(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "webhook-spawner",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubWebhook: &kelos.GitHubWebhook{
						Events:     []string{"issue_comment", "push"},
						Repository: "org/repo",
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "GitHub Webhook (org/repo)") {
		t.Errorf("expected 'GitHub Webhook (org/repo)' as source in output, got %q", output)
	}
}

func TestPrintTaskSpawnerTableGitHubWebhookNoRepo(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "webhook-spawner",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubWebhook: &kelos.GitHubWebhook{
						Events: []string{"push"},
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "GitHub Webhook") {
		t.Errorf("expected 'GitHub Webhook' as source in output, got %q", output)
	}
	if strings.Contains(output, "(") {
		t.Errorf("expected no repository in source when repository is empty, got %q", output)
	}
}

func TestPrintTaskSpawnerTableLinearWebhook(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "linear-spawner",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					LinearWebhook: &kelos.LinearWebhook{
						Types: []string{"Issue", "Comment"},
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "Linear Webhook") {
		t.Errorf("expected 'Linear Webhook' as source in output, got %q", output)
	}
}

func TestPrintTaskSpawnerDetailGitHubWebhook(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "webhook-spawner",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events:         []string{"issue_comment", "push"},
					Repository:     "org/repo",
					ExcludeAuthors: []string{"bot-user"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase:             kelos.TaskSpawnerPhaseRunning,
			TotalDiscovered:   3,
			TotalTasksCreated: 2,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, spawner)
	output := buf.String()

	for _, expected := range []string{
		"Source:", "GitHub Webhook",
		"Events:", "[issue_comment push]",
		"Repository:", "org/repo",
		"Exclude Authors:", "[bot-user]",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in detail output, got %q", expected, output)
		}
	}
}

func TestPrintTaskSpawnerDetailLinearWebhook(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "linear-spawner",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				LinearWebhook: &kelos.LinearWebhook{
					Types: []string{"Issue", "Comment"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase:             kelos.TaskSpawnerPhaseRunning,
			TotalDiscovered:   1,
			TotalTasksCreated: 1,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, spawner)
	output := buf.String()

	for _, expected := range []string{
		"Source:", "Linear Webhook",
		"Types:", "[Issue Comment]",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in detail output, got %q", expected, output)
		}
	}
}

func TestPrintTaskSpawnerTableGenericWebhook(t *testing.T) {
	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "sentry-fixer",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GenericWebhook: &kelos.GenericWebhook{
						Source:       "sentry",
						FieldMapping: map[string]string{"id": "$.id"},
					},
				},
			},
			Status: kelos.TaskSpawnerStatus{
				Phase: kelos.TaskSpawnerPhaseRunning,
			},
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, spawners, false)
	output := buf.String()

	if !strings.Contains(output, "Generic Webhook (sentry)") {
		t.Errorf("expected 'Generic Webhook (sentry)' as source in output, got %q", output)
	}
}

func TestPrintTaskSpawnerDetailGenericWebhook(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sentry-fixer",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source:       "sentry",
					FieldMapping: map[string]string{"id": "$.id"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase:             kelos.TaskSpawnerPhaseRunning,
			TotalDiscovered:   1,
			TotalTasksCreated: 1,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, spawner)
	output := buf.String()

	for _, expected := range []string{
		"Source:", "Generic Webhook",
		"Webhook Source:     sentry",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in detail output, got %q", expected, output)
		}
	}
}

func TestPrintTaskSpawnerDetailGitHubPullRequests(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-spawner",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubPullRequests: &kelos.GitHubPullRequests{
					State:       "open",
					Labels:      []string{"bug", "help-wanted"},
					ReviewState: "changes_requested",
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				WorkspaceRef: &kelos.WorkspaceReference{
					Name: "my-ws",
				},
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase:             kelos.TaskSpawnerPhaseRunning,
			TotalDiscovered:   3,
			TotalTasksCreated: 2,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, spawner)
	output := buf.String()

	for _, expected := range []string{
		"Source:", "GitHub Pull Requests",
		"State:", "open",
		"Labels:", "[bug help-wanted]",
		"Review State:", "changes_requested",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in detail output, got %q", expected, output)
		}
	}
}

func TestPrintTaskSpawnerDetailJira(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "jira-spawner",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				Jira: &kelos.Jira{
					BaseURL: "https://mycompany.atlassian.net",
					Project: "PROJ",
					JQL:     "status = Open",
					SecretRef: kelos.SecretReference{
						Name: "jira-secret",
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase:             kelos.TaskSpawnerPhaseRunning,
			TotalDiscovered:   5,
			TotalTasksCreated: 3,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, spawner)
	output := buf.String()

	for _, expected := range []string{
		"Source:", "Jira",
		"Project:", "PROJ",
		"JQL:", "status = Open",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in detail output, got %q", expected, output)
		}
	}
}

func TestPrintWorkspaceDetail(t *testing.T) {
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-workspace",
			Namespace: "default",
		},
		Spec: kelos.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
			Ref:  "main",
			SecretRef: &kelos.SecretReference{
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
	task := kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-task",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-30 * time.Minute)),
		},
		Spec: kelos.TaskSpec{
			Type: "claude-code",
		},
		Status: kelos.TaskStatus{
			Phase: kelos.TaskPhaseSucceeded,
		},
	}

	var buf bytes.Buffer
	printTaskTable(&buf, []kelos.Task{task}, false)
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
	if !strings.Contains(output, string(kelos.TaskPhaseSucceeded)) {
		t.Errorf("expected phase Succeeded in output, got %q", output)
	}
	if strings.Contains(output, "Prompt:") {
		t.Errorf("table view should not contain detail fields, got %q", output)
	}
}

func TestPrintTaskSpawnerTableSingleItem(t *testing.T) {
	spawner := kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-spawner",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				Cron: &kelos.Cron{
					Schedule: "0 * * * *",
				},
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase:             kelos.TaskSpawnerPhaseRunning,
			TotalDiscovered:   5,
			TotalTasksCreated: 3,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerTable(&buf, []kelos.TaskSpawner{spawner}, false)
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
	ws := kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-workspace",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
		},
		Spec: kelos.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
			Ref:  "main",
		},
	}

	var buf bytes.Buffer
	printWorkspaceTable(&buf, []kelos.Workspace{ws}, false)
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				Cron: &kelos.Cron{
					Schedule: "0 * * * *",
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type:  "claude-code",
				Model: "claude-sonnet-4-20250514",
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase:             kelos.TaskSpawnerPhaseRunning,
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
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-ws",
			Namespace: "default",
		},
		Spec: kelos.WorkspaceSpec{
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
		status kelos.TaskStatus
		want   string
	}{
		{
			name:   "no start time",
			status: kelos.TaskStatus{},
			want:   "-",
		},
		{
			name: "completed task",
			status: kelos.TaskStatus{
				StartTime:      &startTime,
				CompletionTime: &completionTime,
			},
			want: "20m",
		},
		{
			name: "running task",
			status: kelos.TaskStatus{
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

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "full-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Type:   "claude-code",
			Prompt: "Fix the bug",
			Credentials: &kelos.Credentials{
				Type:      kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{Name: "my-secret"},
			},
			Model:     "claude-sonnet-4-20250514",
			Image:     "custom-image:latest",
			Branch:    "feature/fix",
			DependsOn: []string{"task-a", "task-b"},
			WorkspaceRef: &kelos.WorkspaceReference{
				Name: "my-ws",
			},
			AgentConfigRefs:         []kelos.AgentConfigReference{{Name: "my-config"}},
			TTLSecondsAfterFinished: &ttl,
			PodOverrides: &kelos.PodOverrides{
				ActiveDeadlineSeconds: &timeout,
			},
		},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
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

func TestPrintTaskDetailCanonicalWorkerFields(t *testing.T) {
	timeout := int64(7200)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Prompt: "Fix the bug",
			Worker: &kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeAPIKey,
					SecretRef: &kelos.SecretReference{Name: "worker-secret"},
				},
				Model: "gpt-5",
				Image: "custom-codex:latest",
				WorkspaceRef: &kelos.WorkspaceReference{
					Name: "worker-ws",
				},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "worker-config"}},
				PodOverrides: &kelos.PodOverrides{
					ActiveDeadlineSeconds: &timeout,
				},
			},
		},
		Status: kelos.TaskStatus{Phase: kelos.TaskPhaseRunning},
	}

	var buf bytes.Buffer
	printTaskDetail(&buf, task)
	output := buf.String()

	for _, expected := range []string{
		"codex",
		"worker-secret",
		"gpt-5",
		"custom-codex:latest",
		"worker-ws",
		"worker-config",
		"7200s",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in output, got:\n%s", expected, output)
		}
	}
}

func TestPrintAgentConfigTable(t *testing.T) {
	configs := []kelos.AgentConfig{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "config-one",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.AgentConfigSpec{
				AgentsMD: "some instructions",
				Plugins: []kelos.PluginSpec{
					{Name: "kelos"},
				},
				MCPServers: []kelos.MCPServerSpec{
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
			Spec: kelos.AgentConfigSpec{},
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
	configs := []kelos.AgentConfig{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "config-one",
				Namespace:         "ns-a",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: kelos.AgentConfigSpec{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "config-two",
				Namespace:         "ns-b",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			},
			Spec: kelos.AgentConfigSpec{},
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
	ac := kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-config",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
		},
		Spec: kelos.AgentConfigSpec{
			Plugins: []kelos.PluginSpec{
				{Name: "kelos"},
			},
		},
	}

	var buf bytes.Buffer
	printAgentConfigTable(&buf, []kelos.AgentConfig{ac}, false)
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
	ac := &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-config",
			Namespace: "default",
		},
		Spec: kelos.AgentConfigSpec{
			AgentsMD: "Build and test instructions",
			Plugins: []kelos.PluginSpec{
				{
					Name: "kelos",
					Skills: []kelos.SkillDefinition{
						{Name: "review", Content: "review content"},
					},
					Agents: []kelos.AgentDefinition{
						{Name: "triage", Content: "triage content"},
					},
				},
			},
			MCPServers: []kelos.MCPServerSpec{
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
		"Plugins:", "kelos",
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
	ac := &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-config",
			Namespace: "default",
		},
		Spec: kelos.AgentConfigSpec{},
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
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Type:   "claude-code",
			Prompt: "Do something",
			Credentials: &kelos.Credentials{
				Type:      kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{Name: "secret"},
			},
		},
		Status: kelos.TaskStatus{
			Phase: kelos.TaskPhasePending,
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
		"Agent Configs:",
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

func TestEffectivePollInterval(t *testing.T) {
	tests := []struct {
		name string
		ts   *kelos.TaskSpawner
		want string
	}{
		{
			name: "github issues source override wins over default",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{PollInterval: "2m"},
					},
				},
			},
			want: "2m",
		},
		{
			name: "github issues falls back to default when source empty",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{},
					},
				},
			},
			want: "5m",
		},
		{
			name: "github pull requests source override wins over default",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubPullRequests: &kelos.GitHubPullRequests{PollInterval: "45s"},
					},
				},
			},
			want: "45s",
		},
		{
			name: "jira source override wins over default",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						Jira: &kelos.Jira{PollInterval: "1m"},
					},
				},
			},
			want: "1m",
		},
		{
			name: "cron source uses default since it has no per-source override",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						Cron: &kelos.Cron{Schedule: "*/5 * * * *"},
					},
				},
			},
			want: "5m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectivePollInterval(tt.ts); got != tt.want {
				t.Errorf("effectivePollInterval() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrintTaskSpawnerDetailShowsPerSourcePollInterval(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubIssues: &kelos.GitHubIssues{
					PollInterval: "2m",
					Labels:       []string{"bug"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
			},
		},
		Status: kelos.TaskSpawnerStatus{
			Phase: kelos.TaskSpawnerPhaseRunning,
		},
	}

	var buf bytes.Buffer
	printTaskSpawnerDetail(&buf, spawner)
	output := buf.String()

	var pollLine string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Poll Interval:") {
			pollLine = line
			break
		}
	}
	if pollLine == "" {
		t.Fatalf("expected Poll Interval line in output, got:\n%s", output)
	}
	if !strings.Contains(pollLine, "2m") {
		t.Errorf("expected per-source poll interval 2m in line %q", pollLine)
	}
	if strings.Contains(pollLine, "5m") {
		t.Errorf("expected deprecated top-level poll interval (5m) not to appear in line %q", pollLine)
	}
}
