package cli

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestBuildTaskFromCronTaskSpawner(t *testing.T) {
	spawner := testManualTaskSpawner("cron-spawner")
	spawner.Spec.When.Cron = &kelos.Cron{Schedule: "0 9 * * 1"}
	spawner.Spec.TaskTemplate.PromptTemplate = "Run for {{.Time}} on {{.Schedule}}: {{.Reason}} ({{.TriggerType}})"
	spawner.Spec.TaskTemplate.Metadata = &kelos.TaskTemplateMetadata{
		Labels: map[string]string{"kelos.dev/taskspawner": "automatic-owner"},
		Annotations: map[string]string{
			annotationCreatedFromTaskSpawner: "template-value",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(spawner).Build()

	task, err := buildTaskFromTaskSpawner(context.Background(), cl, "default", spawner.Name, runFromTaskSpawnerOptions{
		Name: "manual-task",
		Values: map[string]interface{}{
			"Reason":      "operator request",
			"Time":        "2026-07-06T00:00:00Z",
			"TriggerType": "replay",
		},
		Now: time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("buildTaskFromTaskSpawner: %v", err)
	}

	wantPrompt := "Run for 2026-07-10T01:02:03Z on 0 9 * * 1: operator request (replay)"
	if task.Spec.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", task.Spec.Prompt, wantPrompt)
	}
	if _, ok := task.Labels["kelos.dev/taskspawner"]; ok {
		t.Error("standalone manual Task has kelos.dev/taskspawner label")
	}
	if len(task.OwnerReferences) != 0 {
		t.Errorf("OwnerReferences = %#v, want none", task.OwnerReferences)
	}
	if got := task.Annotations[annotationCreatedFromTaskSpawner]; got != spawner.Name {
		t.Errorf("created-from annotation = %q, want %q", got, spawner.Name)
	}
	if got := task.Annotations[annotationManualTrigger]; got != "manual" {
		t.Errorf("trigger-type annotation = %q, want manual", got)
	}
	if got := task.Annotations[annotationManualTriggerTime]; got != "2026-07-10T01:02:03Z" {
		t.Errorf("trigger-time annotation = %q", got)
	}
}

func TestBuildTaskFromNonCronTaskSpawnerWithValues(t *testing.T) {
	spawner := testManualTaskSpawner("webhook-spawner")
	spawner.Spec.When.GenericWebhook = &kelos.GenericWebhook{Source: "alerts"}
	spawner.Spec.TaskTemplate.PromptTemplate = "Investigate {{.Title}} with severity {{.Severity}}"
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(spawner).Build()

	task, err := buildTaskFromTaskSpawner(context.Background(), cl, "default", spawner.Name, runFromTaskSpawnerOptions{
		Name: "manual-webhook-task",
		Values: map[string]interface{}{
			"Title":    "API latency",
			"Severity": "critical",
		},
		Now: time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("buildTaskFromTaskSpawner: %v", err)
	}
	if task.Spec.Prompt != "Investigate API latency with severity critical" {
		t.Errorf("Prompt = %q", task.Spec.Prompt)
	}
}

func TestCreateTaskFromTaskSpawnerCreatesTask(t *testing.T) {
	spawner := testManualTaskSpawner("cron-spawner")
	spawner.Spec.When.Cron = &kelos.Cron{Schedule: "0 9 * * 1"}
	spawner.Spec.TaskTemplate.PromptTemplate = "Run at {{.Time}}"
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(spawner).Build()

	task, err := createTaskFromTaskSpawner(context.Background(), cl, "default", spawner.Name, runFromTaskSpawnerOptions{
		Name: "manual-task",
		Now:  time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("createTaskFromTaskSpawner: %v", err)
	}

	var created kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKey{Name: task.Name, Namespace: "default"}, &created); err != nil {
		t.Fatalf("getting created Task: %v", err)
	}
	if created.Spec.Prompt != "Run at 2026-07-10T01:02:03Z" {
		t.Errorf("Prompt = %q", created.Spec.Prompt)
	}
}

func TestBuildTaskFromTaskSpawnerMissingTemplateValue(t *testing.T) {
	spawner := testManualTaskSpawner("issue-spawner")
	spawner.Spec.When.GitHubIssues = &kelos.GitHubIssues{Repo: "owner/repo"}
	spawner.Spec.TaskTemplate.PromptTemplate = "Issue {{.Number}}"
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(spawner).Build()

	_, err := buildTaskFromTaskSpawner(context.Background(), cl, "default", spawner.Name, runFromTaskSpawnerOptions{Name: "manual-task"})
	if err == nil {
		t.Fatal("expected missing template value error")
	}
	if !strings.Contains(err.Error(), "Number") {
		t.Fatalf("error = %v, want missing key context", err)
	}
}

func TestBuildTaskFromTaskSpawnerPreservesIntegerValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte("Number: 42\n"), 0o600); err != nil {
		t.Fatalf("writing values file: %v", err)
	}
	values, err := readTaskTemplateValues(path, true, nil)
	if err != nil {
		t.Fatalf("readTaskTemplateValues: %v", err)
	}

	spawner := testManualTaskSpawner("issue-spawner")
	spawner.Spec.When.GitHubIssues = &kelos.GitHubIssues{Repo: "owner/repo"}
	spawner.Spec.TaskTemplate.PromptTemplate = "{{if eq .Number 42}}matched{{else}}not matched{{end}}"
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(spawner).Build()
	task, err := buildTaskFromTaskSpawner(context.Background(), cl, "default", spawner.Name, runFromTaskSpawnerOptions{
		Name:   "manual-task",
		Values: values,
	})
	if err != nil {
		t.Fatalf("buildTaskFromTaskSpawner: %v", err)
	}
	if task.Spec.Prompt != "matched" {
		t.Errorf("Prompt = %q, want matched", task.Spec.Prompt)
	}
}

func TestBuildTaskFromTaskSpawnerDerivesUpstreamRepo(t *testing.T) {
	spawner := testManualTaskSpawner("issue-spawner")
	spawner.Spec.When.GitHubIssues = &kelos.GitHubIssues{Repo: "https://github.com/upstream/repository.git"}
	spawner.Spec.TaskTemplate.PromptTemplate = "Issue {{.Number}}"
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(spawner).Build()

	task, err := buildTaskFromTaskSpawner(context.Background(), cl, "default", spawner.Name, runFromTaskSpawnerOptions{
		Name:   "manual-task",
		Values: map[string]interface{}{"Number": int64(42)},
	})
	if err != nil {
		t.Fatalf("buildTaskFromTaskSpawner: %v", err)
	}
	if task.Spec.UpstreamRepo != "upstream/repository" {
		t.Errorf("UpstreamRepo = %q, want upstream/repository", task.Spec.UpstreamRepo)
	}
}

func TestBuildTaskFromTaskSpawnerWarnsWhenContextFailureIsIgnored(t *testing.T) {
	spawner := testManualTaskSpawner("context-spawner")
	spawner.Spec.When.Cron = &kelos.Cron{Schedule: "0 9 * * 1"}
	spawner.Spec.TaskTemplate.PromptTemplate = "Context: {{.Context.unavailable}}"
	spawner.Spec.TaskTemplate.ContextSources = []kelos.ContextSource{{
		Name: "unavailable",
		HTTP: &kelos.HTTPContextSource{
			URL: "http://example.com",
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyIgnore,
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(spawner).Build()
	var warnings bytes.Buffer

	task, err := buildTaskFromTaskSpawner(context.Background(), cl, "default", spawner.Name, runFromTaskSpawnerOptions{
		Name:        "manual-task",
		ErrorWriter: &warnings,
	})
	if err != nil {
		t.Fatalf("buildTaskFromTaskSpawner: %v", err)
	}
	if task.Spec.Prompt != "Context: " {
		t.Errorf("Prompt = %q, want empty ignored context", task.Spec.Prompt)
	}
	if got := warnings.String(); !strings.Contains(got, "Context source fetch failed") || !strings.Contains(got, `"source"="unavailable"`) {
		t.Errorf("warning = %q, want ignored context failure details", got)
	}
}

func TestTaskSpawnerHTTPClientUsesContextSourceTimeout(t *testing.T) {
	if got := taskSpawnerHTTPClient(nil); got != http.DefaultClient {
		t.Fatalf("taskSpawnerHTTPClient(nil) = %p, want http.DefaultClient", got)
	}
	if http.DefaultClient.Timeout != 0 {
		t.Fatalf("http.DefaultClient.Timeout = %s, want no client-level timeout", http.DefaultClient.Timeout)
	}
}

func TestParseTaskSpawnerReference(t *testing.T) {
	for _, ref := range []string{"taskspawner/example", "taskspawners/example", "ts/example"} {
		name, err := parseTaskSpawnerReference(ref)
		if err != nil {
			t.Fatalf("parseTaskSpawnerReference(%q): %v", ref, err)
		}
		if name != "example" {
			t.Errorf("parseTaskSpawnerReference(%q) = %q", ref, name)
		}
	}
	if _, err := parseTaskSpawnerReference("workspace/example"); err == nil {
		t.Fatal("expected invalid resource type error")
	}
}

func TestReadTaskTemplateValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte("Title: manual\nCount: 2\nNested:\n  Enabled: true\n"), 0o600); err != nil {
		t.Fatalf("writing values file: %v", err)
	}
	values, err := readTaskTemplateValues(path, true, nil)
	if err != nil {
		t.Fatalf("readTaskTemplateValues: %v", err)
	}
	if values["Title"] != "manual" || values["Count"] != int64(2) {
		t.Errorf("values = %#v", values)
	}

	stdinValues, err := readTaskTemplateValues("-", true, strings.NewReader(`{"Title":"stdin"}`))
	if err != nil {
		t.Fatalf("readTaskTemplateValues from stdin: %v", err)
	}
	if stdinValues["Title"] != "stdin" {
		t.Errorf("stdin values = %#v", stdinValues)
	}

	if _, err := readTaskTemplateValues("-", true, strings.NewReader("- one\n- two\n")); err == nil {
		t.Fatal("expected top-level array error")
	}
}

func TestRunFromTaskSpawnerRejectsTaskDefinitionFlags(t *testing.T) {
	cmd := newRunCommand(&ClientConfig{})
	cmd.SetArgs([]string{"--from", "taskspawner/example", "--prompt", "override"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--prompt cannot be used with --from") {
		t.Fatalf("error = %v, want incompatible flag error", err)
	}
}

func TestRunValuesRequiresFrom(t *testing.T) {
	cmd := newRunCommand(&ClientConfig{})
	cmd.SetArgs([]string{"-f", "values.yaml"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--values requires --from") {
		t.Fatalf("error = %v, want --values requirement", err)
	}
}

func TestManualTaskNameFitsDNSLabel(t *testing.T) {
	name := manualTaskName(strings.Repeat("a", 63), "abcde")
	if len(name) > 63 {
		t.Fatalf("name length = %d, want at most 63", len(name))
	}
	if !strings.HasSuffix(name, "-manual-abcde") {
		t.Errorf("name = %q, want manual suffix", name)
	}
}

func testManualTaskSpawner(name string) *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: kelos.CredentialTypeNone,
				},
			},
		},
	}
}
