package source

import (
	"strings"
	"testing"
)

func TestRenderPromptDefault(t *testing.T) {
	item := WorkItem{
		ID:     "42",
		Number: 42,
		Title:  "Fix the login bug",
		Body:   "Users cannot log in after the update.",
		URL:    "https://github.com/o/r/issues/42",
		Labels: []string{"bug"},
		Kind:   "Issue",
	}

	result, err := RenderPrompt("", item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Issue #42") {
		t.Errorf("expected 'Issue #42' in output: %s", result)
	}
	if !strings.Contains(result, "Fix the login bug") {
		t.Errorf("expected title in output: %s", result)
	}
	if !strings.Contains(result, "Users cannot log in") {
		t.Errorf("expected body in output: %s", result)
	}
}

func TestRenderPromptDefaultPR(t *testing.T) {
	item := WorkItem{
		ID:     "10",
		Number: 10,
		Title:  "Add feature",
		Body:   "This PR adds a feature.",
		URL:    "https://github.com/o/r/pull/10",
		Kind:   "PR",
	}

	result, err := RenderPrompt("", item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "PR #10") {
		t.Errorf("expected 'PR #10' in output: %s", result)
	}
}

func TestRenderPromptDefaultKindFallback(t *testing.T) {
	item := WorkItem{
		Number: 5,
		Title:  "Test",
		Body:   "Body",
	}

	result, err := RenderPrompt("", item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Issue #5") {
		t.Errorf("expected 'Issue #5' fallback in output: %s", result)
	}
}

func TestRenderPromptCustom(t *testing.T) {
	item := WorkItem{
		Number: 7,
		Title:  "Add dark mode",
	}

	result, err := RenderPrompt("Fix {{.Title}} (issue #{{.Number}})", item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Fix Add dark mode (issue #7)"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestRenderPromptWithComments(t *testing.T) {
	item := WorkItem{
		Number:   1,
		Title:    "Test",
		Body:     "Body",
		Comments: "A comment",
	}

	result, err := RenderPrompt("", item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Comments:") {
		t.Errorf("expected Comments section in output: %s", result)
	}
	if !strings.Contains(result, "A comment") {
		t.Errorf("expected comment text in output: %s", result)
	}

	// Without comments
	item.Comments = ""
	result, err = RenderPrompt("", item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result, "Comments:") {
		t.Errorf("expected no Comments section in output: %s", result)
	}
}

func TestRenderPromptAllVariables(t *testing.T) {
	item := WorkItem{
		ID:       "99",
		Number:   99,
		Title:    "T",
		Body:     "B",
		URL:      "U",
		Labels:   []string{"a", "b"},
		Comments: "C",
		Kind:     "PR",
	}

	tmpl := "{{.ID}} {{.Number}} {{.Title}} {{.Body}} {{.URL}} {{.Labels}} {{.Comments}} {{.Kind}}"
	result, err := RenderPrompt(tmpl, item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "99 99 T B U a, b C PR"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestRenderPromptInvalidTemplate(t *testing.T) {
	item := WorkItem{}

	_, err := RenderPrompt("{{.Invalid", item)
	if err == nil {
		t.Fatal("expected error for invalid template")
	}
}

func TestRenderTemplate(t *testing.T) {
	item := WorkItem{
		ID:     "42",
		Number: 42,
		Title:  "Fix the login bug",
		Kind:   "Issue",
	}

	result, err := RenderTemplate("kelos-task-{{.Number}}", item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "kelos-task-42" {
		t.Errorf("expected %q, got %q", "kelos-task-42", result)
	}
}

func TestRenderTemplateStaticString(t *testing.T) {
	item := WorkItem{Number: 1}

	result, err := RenderTemplate("my-branch", item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "my-branch" {
		t.Errorf("expected %q, got %q", "my-branch", result)
	}
}

func TestRenderTemplateInvalid(t *testing.T) {
	_, err := RenderTemplate("{{.Bad", WorkItem{})
	if err == nil {
		t.Fatal("expected error for invalid template")
	}
}
