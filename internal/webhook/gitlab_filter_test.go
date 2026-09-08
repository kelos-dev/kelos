package webhook

import (
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const gitlabMergeRequestPayload = `{
	"object_kind": "merge_request",
	"user": {"username": "alice"},
	"project": {"path_with_namespace": "group/sub/repo", "web_url": "https://gitlab.example.com/group/sub/repo"},
	"object_attributes": {
		"iid": 7,
		"title": "Add feature",
		"description": "MR body",
		"url": "https://gitlab.example.com/group/sub/repo/-/merge_requests/7",
		"state": "opened",
		"action": "open",
		"source_branch": "feature-x",
		"target_branch": "main",
		"draft": true,
		"last_commit": {"id": "abc123"}
	},
	"labels": [{"title": "kelos"}, {"title": "backend"}]
}`

const gitlabIssuePayload = `{
	"object_kind": "issue",
	"user": {"username": "bob"},
	"project": {"path_with_namespace": "group/sub/repo", "web_url": "https://gitlab.example.com/group/sub/repo"},
	"object_attributes": {
		"iid": 42,
		"title": "Crash on start",
		"description": "Stack trace attached",
		"url": "https://gitlab.example.com/group/sub/repo/-/issues/42",
		"state": "opened",
		"action": "open",
		"labels": [{"title": "bug"}]
	}
}`

const gitlabNotePayload = `{
	"object_kind": "note",
	"user": {"username": "carol"},
	"project": {"path_with_namespace": "group/sub/repo", "web_url": "https://gitlab.example.com/group/sub/repo"},
	"object_attributes": {
		"id": 900,
		"note": "/kelos fix please",
		"noteable_type": "MergeRequest",
		"url": "https://gitlab.example.com/group/sub/repo/-/merge_requests/7#note_900"
	},
	"merge_request": {
		"iid": 7,
		"title": "Add feature",
		"description": "MR body",
		"url": "https://gitlab.example.com/group/sub/repo/-/merge_requests/7",
		"state": "opened",
		"source_branch": "feature-x",
		"last_commit": {"id": "abc123"},
		"labels": [{"title": "kelos"}]
	}
}`

const gitlabPipelinePayload = `{
	"object_kind": "pipeline",
	"user": {"username": "alice"},
	"project": {"path_with_namespace": "group/sub/repo", "web_url": "https://gitlab.example.com/group/sub/repo"},
	"object_attributes": {
		"id": 555,
		"ref": "feature-x",
		"sha": "abc123",
		"status": "failed",
		"url": "https://gitlab.example.com/group/sub/repo/-/pipelines/555"
	},
	"merge_request": {
		"iid": 7,
		"title": "Add feature",
		"source_branch": "feature-x",
		"url": "https://gitlab.example.com/group/sub/repo/-/merge_requests/7"
	}
}`

const gitlabPushPayload = `{
	"object_kind": "push",
	"ref": "refs/heads/main",
	"checkout_sha": "deadbeef",
	"user_username": "dave",
	"project": {"path_with_namespace": "group/sub/repo", "web_url": "https://gitlab.example.com/group/sub/repo"}
}`

func TestParseGitLabWebhook_MergeRequest(t *testing.T) {
	data, err := ParseGitLabWebhook([]byte(gitlabMergeRequestPayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Event != "merge_request" || data.Action != "open" || data.Kind != "MR" {
		t.Errorf("unexpected event identity: %+v", data)
	}
	if data.ID != "mr-7" || data.Number != 7 || data.Title != "Add feature" || data.Body != "MR body" {
		t.Errorf("unexpected merge request fields: %+v", data)
	}
	if data.Sender != "alice" || data.Project != "group/sub/repo" || data.ProjectURL != "https://gitlab.example.com/group/sub/repo" {
		t.Errorf("unexpected sender/project: %+v", data)
	}
	if data.Branch != "feature-x" || data.HeadSHA != "abc123" || data.State != "opened" || !data.Draft {
		t.Errorf("unexpected branch/sha/state/draft: %+v", data)
	}
	if len(data.Labels) != 2 || data.Labels[0] != "kelos" {
		t.Errorf("expected top-level labels, got %v", data.Labels)
	}
}

func TestParseGitLabWebhook_Issue(t *testing.T) {
	data, err := ParseGitLabWebhook([]byte(gitlabIssuePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Event != "issue" || data.Kind != "Issue" || data.ID != "42" || data.Number != 42 {
		t.Errorf("unexpected issue identity: %+v", data)
	}
	if data.URL != "https://gitlab.example.com/group/sub/repo/-/issues/42" || data.Sender != "bob" {
		t.Errorf("unexpected url/sender: %+v", data)
	}
	if len(data.Labels) != 1 || data.Labels[0] != "bug" {
		t.Errorf("expected object_attributes labels, got %v", data.Labels)
	}
}

func TestParseGitLabWebhook_NoteOnMergeRequest(t *testing.T) {
	data, err := ParseGitLabWebhook([]byte(gitlabNotePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Event != "note" || data.NoteOn != "MergeRequest" || data.Kind != "MR" {
		t.Errorf("unexpected note identity: %+v", data)
	}
	if data.ID != "mr-7" || data.Number != 7 || data.Branch != "feature-x" || data.HeadSHA != "abc123" {
		t.Errorf("expected the commented merge request identity, got %+v", data)
	}
	if data.CommentBody != "/kelos fix please" || data.CommentURL == "" {
		t.Errorf("unexpected comment fields: %+v", data)
	}
	if len(data.Labels) != 1 || data.Labels[0] != "kelos" {
		t.Errorf("expected merge request labels, got %v", data.Labels)
	}
}

func TestParseGitLabWebhook_Pipeline(t *testing.T) {
	data, err := ParseGitLabWebhook([]byte(gitlabPipelinePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A pipeline attached to a merge request shares the merge request's ID so
	// pipeline and note deliveries for one MR dedupe onto the same Task.
	if data.Event != "pipeline" || data.PipelineStatus != "failed" || data.ID != "mr-7" {
		t.Errorf("unexpected pipeline identity: %+v", data)
	}
	if data.PipelineURL != "https://gitlab.example.com/group/sub/repo/-/pipelines/555" {
		t.Errorf("unexpected pipeline url %q", data.PipelineURL)
	}
	if data.Kind != "MR" || data.Number != 7 || data.Branch != "feature-x" || data.HeadSHA != "abc123" {
		t.Errorf("expected attached merge request identity, got %+v", data)
	}
}

func TestParseGitLabWebhook_Push(t *testing.T) {
	data, err := ParseGitLabWebhook([]byte(gitlabPushPayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Event != "push" || data.Branch != "main" || data.Ref != "refs/heads/main" || data.ID != "deadbeef" {
		t.Errorf("unexpected push identity: %+v", data)
	}
	if data.Sender != "dave" || data.Title != "Push to main" || data.Kind != "webhook" {
		t.Errorf("unexpected push fields: %+v", data)
	}
}

func TestParseGitLabWebhook_InvalidJSON(t *testing.T) {
	if _, err := ParseGitLabWebhook([]byte("{not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestMatchesGitLabEvent(t *testing.T) {
	mr, _ := ParseGitLabWebhook([]byte(gitlabMergeRequestPayload))
	note, _ := ParseGitLabWebhook([]byte(gitlabNotePayload))
	pipeline, _ := ParseGitLabWebhook([]byte(gitlabPipelinePayload))
	push, _ := ParseGitLabWebhook([]byte(gitlabPushPayload))
	draft := true
	notDraft := false

	tests := []struct {
		name   string
		config kelos.GitLabWebhook
		event  *GitLabEventData
		want   bool
	}{
		{"event listed, no filters", kelos.GitLabWebhook{Events: []string{"merge_request"}}, mr, true},
		{"event not listed", kelos.GitLabWebhook{Events: []string{"issue"}}, mr, false},
		{"project mismatch", kelos.GitLabWebhook{Events: []string{"merge_request"}, Project: "other/repo"}, mr, false},
		{"project match is case-insensitive", kelos.GitLabWebhook{Events: []string{"merge_request"}, Project: "Group/Sub/Repo"}, mr, true},
		{"excluded author", kelos.GitLabWebhook{Events: []string{"merge_request"}, ExcludeAuthors: []string{"alice"}}, mr, false},
		{"action filter matches", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Action: "open"}}}, mr, true},
		{"action filter rejects", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Action: "merge"}}}, mr, false},
		{"filter for other event leaves this event unfiltered", kelos.GitLabWebhook{Events: []string{"merge_request", "issue"}, Filters: []kelos.GitLabWebhookFilter{{Event: "issue", Action: "close"}}}, mr, true},
		{"labels all required", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Labels: []string{"kelos", "backend"}}}}, mr, true},
		{"missing required label", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Labels: []string{"frontend"}}}}, mr, false},
		{"excluded label", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", ExcludeLabels: []string{"Backend"}}}}, mr, false},
		{"draft filter", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Draft: &notDraft}}}, mr, false},
		{"draft filter matches", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Draft: &draft}}}, mr, true},
		{"branch glob", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Branch: "feature-*"}}}, mr, true},
		{"branch mismatch", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Branch: "release-*"}}}, mr, false},
		{"state filter", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", State: "merged"}}}, mr, false},
		{"or across filters", kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{{Event: "merge_request", Action: "merge"}, {Event: "merge_request", Action: "open"}}}, mr, true},
		{"note body pattern and subject", kelos.GitLabWebhook{Events: []string{"note"}, Filters: []kelos.GitLabWebhookFilter{{Event: "note", NoteOn: "MergeRequest", BodyPattern: `^/kelos fix`}}}, note, true},
		{"note subject mismatch", kelos.GitLabWebhook{Events: []string{"note"}, Filters: []kelos.GitLabWebhookFilter{{Event: "note", NoteOn: "Issue"}}}, note, false},
		{"note exclude body pattern", kelos.GitLabWebhook{Events: []string{"note"}, Filters: []kelos.GitLabWebhookFilter{{Event: "note", ExcludeBodyPatterns: []string{"please"}}}}, note, false},
		{"note labels come from merge request", kelos.GitLabWebhook{Events: []string{"note"}, Filters: []kelos.GitLabWebhookFilter{{Event: "note", Labels: []string{"kelos"}}}}, note, true},
		{"pipeline status", kelos.GitLabWebhook{Events: []string{"pipeline"}, Filters: []kelos.GitLabWebhookFilter{{Event: "pipeline", Status: "failed"}}}, pipeline, true},
		{"pipeline status mismatch", kelos.GitLabWebhook{Events: []string{"pipeline"}, Filters: []kelos.GitLabWebhookFilter{{Event: "pipeline", Status: "success"}}}, pipeline, false},
		{"push branch", kelos.GitLabWebhook{Events: []string{"push"}, Filters: []kelos.GitLabWebhookFilter{{Event: "push", Branch: "main"}}}, push, true},
		{"filter author", kelos.GitLabWebhook{Events: []string{"push"}, Filters: []kelos.GitLabWebhookFilter{{Event: "push", Author: "dave"}}}, push, true},
		{"filter exclude author", kelos.GitLabWebhook{Events: []string{"push"}, Filters: []kelos.GitLabWebhookFilter{{Event: "push", ExcludeAuthors: []string{"dave"}}}}, push, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchesGitLabEvent(&tt.config, tt.event)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("MatchesGitLabEvent = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitLabInstanceURL(t *testing.T) {
	tests := []struct{ projectURL, project, want string }{
		{"https://gitlab.example.com/group/sub/repo", "group/sub/repo", "https://gitlab.example.com"},
		{"http://gitlab-webservice.gitlab.svc:8181/grp/repo", "grp/repo", "http://gitlab-webservice.gitlab.svc:8181"},
		{"https://example.com/gitlab/group/repo", "group/repo", "https://example.com/gitlab"},
		{"https://example.com/gitlab/group/repo/", "group/repo", "https://example.com/gitlab"},
		{"", "group/repo", ""},
		{"not a url", "group/repo", ""},
	}
	for _, tt := range tests {
		if got := gitlabInstanceURL(tt.projectURL, tt.project); got != tt.want {
			t.Errorf("gitlabInstanceURL(%q, %q) = %q, want %q", tt.projectURL, tt.project, got, tt.want)
		}
	}
}

func TestMatchesGitLabEvent_InvalidPattern(t *testing.T) {
	note, _ := ParseGitLabWebhook([]byte(gitlabNotePayload))
	config := kelos.GitLabWebhook{Events: []string{"note"}, Filters: []kelos.GitLabWebhookFilter{{Event: "note", BodyPattern: "("}}}
	if _, err := MatchesGitLabEvent(&config, note); err == nil {
		t.Error("expected error for invalid regular expression")
	}
}

func TestExtractGitLabWorkItem(t *testing.T) {
	pipeline, _ := ParseGitLabWebhook([]byte(gitlabPipelinePayload))
	vars := ExtractGitLabWorkItem(pipeline)

	for _, key := range []string{"ID", "Title", "Kind", "Number", "Body", "URL", "Event", "Action", "Sender", "Ref", "Branch", "State", "Labels", "Repository", "RepositoryURL", "NoteOn", "CommentBody", "CommentURL", "PipelineStatus", "PipelineURL", "HeadSHA", "Payload"} {
		if _, ok := vars[key]; !ok {
			t.Errorf("expected template variable %q to be present", key)
		}
	}
	if vars["Event"] != "pipeline" || vars["PipelineStatus"] != "failed" || vars["Number"] != 7 || vars["Repository"] != "group/sub/repo" {
		t.Errorf("unexpected variables: %v", vars)
	}
	if _, ok := vars["Payload"].(map[string]interface{}); !ok {
		t.Errorf("expected Payload to be the parsed map, got %T", vars["Payload"])
	}
}
