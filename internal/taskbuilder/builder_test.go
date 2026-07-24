package taskbuilder

import (
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestBuildTask_ForwardsEffort(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		Type: "codex",
		Credentials: &kelos.Credentials{
			Type: kelos.CredentialTypeAPIKey,
			SecretRef: &kelos.SecretReference{
				Name: "credentials",
			},
		},
		Effort:         "high",
		PromptTemplate: "Fix {{.Title}}",
	}

	task, err := tb.BuildTask("task-1", "default", template, map[string]interface{}{
		"Title": "the bug",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Spec.Effort != "high" {
		t.Fatalf("task.Spec.Effort = %q, want %q", task.Spec.Effort, "high")
	}
	if task.Spec.Prompt != "Fix the bug" {
		t.Fatalf("task.Spec.Prompt = %q, want %q", task.Spec.Prompt, "Fix the bug")
	}
}

func TestBuildTask_RendersFiles(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		Type: "codex",
		Credentials: &kelos.Credentials{
			Type:      kelos.CredentialTypeAPIKey,
			SecretRef: &kelos.SecretReference{Name: "credentials"},
		},
		PromptTemplate: "Review:{{range .Files}} {{.}}{{end}}",
	}

	task, err := tb.BuildTask("task-1", "default", template, map[string]interface{}{
		"Title": "the bug",
		"Files": []string{"main.go", "docs/guide.md"},
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}
	if want := "Review: main.go docs/guide.md"; task.Spec.Prompt != want {
		t.Fatalf("task.Spec.Prompt = %q, want %q", task.Spec.Prompt, want)
	}
}

func TestBuildTask_RendersEmptyFiles(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		Type: "codex",
		Credentials: &kelos.Credentials{
			Type:      kelos.CredentialTypeAPIKey,
			SecretRef: &kelos.SecretReference{Name: "credentials"},
		},
		// Referencing {{.Files}} must not trip missingkey=error when empty.
		PromptTemplate: "Files: {{.Files}}",
	}

	task, err := tb.BuildTask("task-1", "default", template, map[string]interface{}{
		"Title": "the bug",
		"Files": []string(nil),
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}
	if want := "Files: []"; task.Spec.Prompt != want {
		t.Fatalf("task.Spec.Prompt = %q, want %q", task.Spec.Prompt, want)
	}
}

func TestBuildTask_ForwardsPodFailurePolicy(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		Type: "codex",
		Credentials: &kelos.Credentials{
			Type: kelos.CredentialTypeAPIKey,
			SecretRef: &kelos.SecretReference{
				Name: "credentials",
			},
		},
		PromptTemplate: "{{.Title}}",
		PodFailurePolicy: &batchv1.PodFailurePolicy{
			Rules: []batchv1.PodFailurePolicyRule{
				{
					Action: batchv1.PodFailurePolicyActionIgnore,
					OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
						{
							Type:   corev1.DisruptionTarget,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
		},
	}

	task, err := tb.BuildTask("task-1", "default", template, map[string]interface{}{
		"Title": "the bug",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Spec.PodFailurePolicy == nil {
		t.Fatal("task.Spec.PodFailurePolicy is nil")
	}
	if got := task.Spec.PodFailurePolicy.Rules[0].Action; got != batchv1.PodFailurePolicyActionIgnore {
		t.Fatalf("task.Spec.PodFailurePolicy.Rules[0].Action = %q, want %q", got, batchv1.PodFailurePolicyActionIgnore)
	}
	conditions := task.Spec.PodFailurePolicy.Rules[0].OnPodConditions
	if len(conditions) != 1 {
		t.Fatalf("task.Spec.PodFailurePolicy.Rules[0].OnPodConditions length = %d, want 1", len(conditions))
	}
	wantCondition := batchv1.PodFailurePolicyOnPodConditionsPattern{
		Type:   corev1.DisruptionTarget,
		Status: corev1.ConditionTrue,
	}
	if conditions[0] != wantCondition {
		t.Fatalf("task.Spec.PodFailurePolicy.Rules[0].OnPodConditions[0] = %#v, want %#v", conditions[0], wantCondition)
	}
}

func TestBuildTask_NameTemplate(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		Type:           "codex",
		NameTemplate:   "responder-pr-{{.Number}}",
		PromptTemplate: "{{.Title}}",
	}

	task, err := tb.BuildTask("default-name", "default", template, map[string]interface{}{
		"Number": 42,
		"Title":  "the bug",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Name != "responder-pr-42" {
		t.Fatalf("task.Name = %q, want %q", task.Name, "responder-pr-42")
	}
}

func TestBuildTask_NameTemplateUnsetUsesDefault(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{PromptTemplate: "{{.Title}}"}

	task, err := tb.BuildTask("default-name", "default", template, map[string]interface{}{
		"Title": "the bug",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Name != "default-name" {
		t.Fatalf("task.Name = %q, want %q", task.Name, "default-name")
	}
}

func TestBuildTask_NameTemplateSanitized(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		NameTemplate:   "{{.Title}}",
		PromptTemplate: "{{.Title}}",
	}

	task, err := tb.BuildTask("default-name", "default", template, map[string]interface{}{
		"Title": "Fix THE Bug: crash on startup!",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Name != "fix-the-bug--crash-on-startup" {
		t.Fatalf("task.Name = %q, want %q", task.Name, "fix-the-bug--crash-on-startup")
	}
}

func TestBuildTask_NameTemplateEmptyAfterSanitizationErrors(t *testing.T) {
	tb := &TaskBuilder{}
	template := &kelos.TaskTemplate{
		NameTemplate:   "{{.Sep}}",
		PromptTemplate: "{{.Title}}",
	}

	_, err := tb.BuildTask("default-name", "default", template, map[string]interface{}{
		"Sep":   "---",
		"Title": "the bug",
	}, nil)
	if err == nil {
		t.Fatal("BuildTask() expected error for empty sanitized name, got nil")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"already-valid", "already-valid"},
		{"UPPER_case", "upper-case"},
		{"pr #42", "pr--42"},
		{"-leading-and-trailing.", "leading-and-trailing"},
		{"emoji🥚label", "emoji-label"},
		{"v1.2.3", "v1.2.3"},
		{"", ""},
		{"---", ""},
		// Dot-boundary cases that must not yield an invalid subdomain.
		{"a..b", "a.b"},
		{"a.-b", "a.b"},
		{"a-.b", "a.b"},
		{".foo.", "foo"},
		{"foo.-", "foo"},
		{"..", ""},
		{"org/repo:tag", "org-repo-tag"},
	}
	for _, tc := range cases {
		if got := sanitizeName(tc.in); got != tc.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeNameProducesValidSubdomain(t *testing.T) {
	inputs := []string{
		"a..b", "a.-b", "a-.b", ".foo.", "foo.-", "org/repo:tag",
		"Fix THE Bug: crash!!", "----.----", "v1.2.3", strings.Repeat("a.", 40),
	}
	for _, in := range inputs {
		got := sanitizeName(in)
		if got == "" {
			continue // empty is handled/rejected by callers, not a name to validate
		}
		if errs := validation.IsDNS1123Subdomain(got); len(errs) > 0 {
			t.Errorf("sanitizeName(%q) = %q is not a valid DNS-1123 subdomain: %v", in, got, errs)
		}
	}
}

func TestSanitizeNameTruncatesTo63(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := sanitizeName(long)
	if len(got) != 63 {
		t.Fatalf("sanitizeName truncated length = %d, want 63", len(got))
	}
}

func TestResolveTaskName(t *testing.T) {
	// Unset template falls back to the default name.
	name, err := ResolveTaskName("default-name", &kelos.TaskTemplate{}, nil)
	if err != nil {
		t.Fatalf("ResolveTaskName() returned error: %v", err)
	}
	if name != "default-name" {
		t.Fatalf("ResolveTaskName() = %q, want %q", name, "default-name")
	}

	// Set template renders and sanitizes.
	name, err = ResolveTaskName("default-name", &kelos.TaskTemplate{NameTemplate: "PR-{{.Number}}"}, map[string]interface{}{"Number": 7})
	if err != nil {
		t.Fatalf("ResolveTaskName() returned error: %v", err)
	}
	if name != "pr-7" {
		t.Fatalf("ResolveTaskName() = %q, want %q", name, "pr-7")
	}

	// Missing variable errors (missingkey=error).
	if _, err := ResolveTaskName("default-name", &kelos.TaskTemplate{NameTemplate: "PR-{{.Missing}}"}, map[string]interface{}{}); err == nil {
		t.Fatal("ResolveTaskName() expected error for missing key, got nil")
	}
}

func TestTaskBelongsToSpawner(t *testing.T) {
	controller := true
	ownedByUID := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Labels:          map[string]string{SpawnerLabel: "spawner"},
			OwnerReferences: []metav1.OwnerReference{{Name: "spawner", UID: "uid-1", Controller: &controller}},
		},
	}
	// Controller owner reference present but a different UID (spawner recreated
	// with the same name): must NOT be treated as owned despite the label match.
	staleOwner := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Labels:          map[string]string{SpawnerLabel: "spawner"},
			OwnerReferences: []metav1.OwnerReference{{Name: "spawner", UID: "old-uid", Controller: &controller}},
		},
	}
	// Ownerless legacy Task: fall back to the label.
	labelOnly := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{SpawnerLabel: "spawner"}}}
	unrelated := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{SpawnerLabel: "other"}}}

	cases := []struct {
		name string
		task *kelos.Task
		want bool
	}{
		{"owned by uid", ownedByUID, true},
		{"stale controller uid rejected despite label", staleOwner, false},
		{"ownerless label match", labelOnly, true},
		{"unrelated label", unrelated, false},
	}
	for _, tc := range cases {
		if got := TaskBelongsToSpawner(tc.task, "spawner", "uid-1"); got != tc.want {
			t.Errorf("%s: TaskBelongsToSpawner = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveTaskName_ContextExcluded(t *testing.T) {
	vars := map[string]interface{}{
		"Number":  7,
		"Context": map[string]interface{}{"foo": "bar"},
	}

	// A template that ignores context still resolves even when context is present.
	name, err := ResolveTaskName("default", &kelos.TaskTemplate{NameTemplate: "pr-{{.Number}}"}, vars)
	if err != nil {
		t.Fatalf("ResolveTaskName() returned error: %v", err)
	}
	if name != "pr-7" {
		t.Fatalf("ResolveTaskName() = %q, want %q", name, "pr-7")
	}

	// Referencing .Context fails even though the value is present, so names never
	// depend on mutable external data (missingkey=error after stripping Context).
	if _, err := ResolveTaskName("default", &kelos.TaskTemplate{NameTemplate: "{{.Context.foo}}"}, vars); err == nil {
		t.Fatal("ResolveTaskName() expected error for .Context reference, got nil")
	}

	// The index builtin bypasses missingkey=error and would render "<no value>";
	// it must be rejected too so it cannot collapse work items to one name.
	if _, err := ResolveTaskName("default", &kelos.TaskTemplate{NameTemplate: `{{index . "Context"}}`}, vars); err == nil {
		t.Fatal("ResolveTaskName() expected error for index-based .Context reference, got nil")
	}

	// A conditional that consumes the missing value and renders a constant must
	// also be rejected (parse-time Context detection, not just "<no value>").
	for _, tmpl := range []string{
		`{{if index . "Context"}}x{{else}}same{{end}}`,
		`{{with .Context}}{{.foo}}{{end}}-{{.Number}}`,
		`{{$c := index . "Context"}}job-{{.Number}}`,
	} {
		if _, err := ResolveTaskName("default", &kelos.TaskTemplate{NameTemplate: tmpl}, vars); err == nil {
			t.Errorf("ResolveTaskName(%q) expected error for Context reference, got nil", tmpl)
		}
	}
}
