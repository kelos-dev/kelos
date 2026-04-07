package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// parseAndMatch is a test helper that parses a payload and calls MatchesGitHubEvent.
func parseAndMatch(t *testing.T, spawner *v1alpha1.GitHubWebhook, eventType string, payload []byte) (bool, error) {
	t.Helper()
	eventData, err := ParseGitHubWebhook(eventType, payload)
	if err != nil {
		return false, err
	}
	return MatchesGitHubEvent(spawner, eventType, eventData)
}

func TestMatchesGitHubEvent_EventTypeFilter(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"issues", "pull_request"},
	}

	tests := []struct {
		name      string
		eventType string
		want      bool
		wantErr   bool
	}{
		{
			name:      "allowed event type",
			eventType: "issues",
			want:      true,
		},
		{
			name:      "another allowed event type",
			eventType: "pull_request",
			want:      true,
		},
		{
			name:      "disallowed event type",
			eventType: "push",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"action":"opened","sender":{"login":"user"}}`)
			got, err := parseAndMatch(t, spawner, tt.eventType, payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("MatchesGitHubEvent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_ActionFilter(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"issues"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event:  "issues",
				Action: "opened",
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "matching action",
			payload: `{"action":"opened","sender":{"login":"user"}}`,
			want:    true,
		},
		{
			name:    "non-matching action",
			payload: `{"action":"closed","sender":{"login":"user"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "issues", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_AuthorFilter(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"issues"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event:  "issues",
				Author: "specific-user",
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "matching author",
			payload: `{"action":"opened","sender":{"login":"specific-user"}}`,
			want:    true,
		},
		{
			name:    "non-matching author",
			payload: `{"action":"opened","sender":{"login":"other-user"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "issues", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_ExcludeAuthorsTopLevel(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events:         []string{"issues"},
		ExcludeAuthors: []string{"bot-user", "dependabot[bot]"},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "excluded author is rejected",
			payload: `{"action":"opened","sender":{"login":"bot-user"}}`,
			want:    false,
		},
		{
			name:    "another excluded author is rejected",
			payload: `{"action":"opened","sender":{"login":"dependabot[bot]"}}`,
			want:    false,
		},
		{
			name:    "non-excluded author is accepted",
			payload: `{"action":"opened","sender":{"login":"human-user"}}`,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "issues", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_ExcludeAuthorsPerFilter(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"issues"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event:          "issues",
				Action:         "opened",
				ExcludeAuthors: []string{"bot-user"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "excluded author in filter is rejected",
			payload: `{"action":"opened","sender":{"login":"bot-user"}}`,
			want:    false,
		},
		{
			name:    "non-excluded author in filter is accepted",
			payload: `{"action":"opened","sender":{"login":"human-user"}}`,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "issues", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_ExcludeAuthorsTopLevelOverridesFilter(t *testing.T) {
	// Top-level ExcludeAuthors should reject even if a filter's Author field matches
	spawner := &v1alpha1.GitHubWebhook{
		Events:         []string{"issues"},
		ExcludeAuthors: []string{"bot-user"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event:  "issues",
				Author: "bot-user",
			},
		},
	}

	got, err := parseAndMatch(t, spawner, "issues", []byte(`{"action":"opened","sender":{"login":"bot-user"}}`))
	if err != nil {
		t.Fatalf("MatchesGitHubEvent() error = %v", err)
	}
	if got {
		t.Error("Expected top-level ExcludeAuthors to take precedence over filter Author match")
	}
}

func TestMatchesGitHubEvent_LabelsFilter(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"issues"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event:  "issues",
				Labels: []string{"bug", "priority:high"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "has all required labels",
			payload: `{
				"action":"opened",
				"sender":{"login":"user"},
				"issue":{
					"number":1,
					"title":"Test issue",
					"body":"Test body",
					"html_url":"https://github.com/owner/repo/issues/1",
					"state":"open",
					"labels":[
						{"name":"bug"},
						{"name":"priority:high"},
						{"name":"frontend"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "missing required label",
			payload: `{
				"action":"opened",
				"sender":{"login":"user"},
				"issue":{
					"number":1,
					"title":"Test issue",
					"body":"Test body",
					"html_url":"https://github.com/owner/repo/issues/1",
					"state":"open",
					"labels":[
						{"name":"bug"},
						{"name":"frontend"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "no labels",
			payload: `{
				"action":"opened",
				"sender":{"login":"user"},
				"issue":{
					"number":1,
					"title":"Test issue",
					"body":"Test body",
					"html_url":"https://github.com/owner/repo/issues/1",
					"state":"open",
					"labels":[]
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "issues", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_ExcludeLabelsFilter(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		spawner   *v1alpha1.GitHubWebhook
		payload   string
		want      bool
	}{
		{
			name:      "issue - no excluded labels",
			eventType: "issues",
			spawner: &v1alpha1.GitHubWebhook{
				Events: []string{"issues"},
				Filters: []v1alpha1.GitHubWebhookFilter{
					{
						Event:         "issues",
						ExcludeLabels: []string{"wontfix", "duplicate"},
					},
				},
			},
			payload: `{
				"action": "opened",
				"issue": {
					"number": 1,
					"title": "Test issue",
					"labels": [
						{"name": "bug"},
						{"name": "frontend"}
					]
				}
			}`,
			want: true,
		},
		{
			name:      "issue - has excluded label",
			eventType: "issues",
			spawner: &v1alpha1.GitHubWebhook{
				Events: []string{"issues"},
				Filters: []v1alpha1.GitHubWebhookFilter{
					{
						Event:         "issues",
						ExcludeLabels: []string{"wontfix", "duplicate"},
					},
				},
			},
			payload: `{
				"action": "opened",
				"issue": {
					"number": 1,
					"title": "Test issue",
					"labels": [
						{"name": "bug"},
						{"name": "wontfix"}
					]
				}
			}`,
			want: false,
		},
		{
			name:      "PR - no excluded labels",
			eventType: "pull_request",
			spawner: &v1alpha1.GitHubWebhook{
				Events: []string{"pull_request"},
				Filters: []v1alpha1.GitHubWebhookFilter{
					{
						Event:         "pull_request",
						ExcludeLabels: []string{"do-not-merge", "draft"},
					},
				},
			},
			payload: `{
				"action": "opened",
				"pull_request": {
					"number": 1,
					"title": "Test PR",
					"labels": [
						{"name": "feature"},
						{"name": "ready-for-review"}
					]
				}
			}`,
			want: true,
		},
		{
			name:      "PR - has excluded label",
			eventType: "pull_request",
			spawner: &v1alpha1.GitHubWebhook{
				Events: []string{"pull_request"},
				Filters: []v1alpha1.GitHubWebhookFilter{
					{
						Event:         "pull_request",
						ExcludeLabels: []string{"do-not-merge", "draft"},
					},
				},
			},
			payload: `{
				"action": "opened",
				"pull_request": {
					"number": 1,
					"title": "Test PR",
					"labels": [
						{"name": "feature"},
						{"name": "do-not-merge"}
					]
				}
			}`,
			want: false,
		},
		{
			name:      "empty labels - should match",
			eventType: "issues",
			spawner: &v1alpha1.GitHubWebhook{
				Events: []string{"issues"},
				Filters: []v1alpha1.GitHubWebhookFilter{
					{
						Event:         "issues",
						ExcludeLabels: []string{"wontfix"},
					},
				},
			},
			payload: `{
				"action": "opened",
				"issue": {
					"number": 1,
					"title": "Test issue",
					"labels": []
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, tt.spawner, tt.eventType, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_PullRequestDraftFilter(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"pull_request"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event: "pull_request",
				Draft: func() *bool { b := false; return &b }(), // Only ready PRs
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "ready PR",
			payload: `{
				"action":"opened",
				"sender":{"login":"user"},
				"pull_request":{
					"number":1,
					"title":"Test PR",
					"body":"Test body",
					"html_url":"https://github.com/owner/repo/pull/1",
					"state":"open",
					"draft":false,
					"head":{"ref":"feature-branch"}
				}
			}`,
			want: true,
		},
		{
			name: "draft PR",
			payload: `{
				"action":"opened",
				"sender":{"login":"user"},
				"pull_request":{
					"number":1,
					"title":"Test PR",
					"body":"Test body",
					"html_url":"https://github.com/owner/repo/pull/1",
					"state":"open",
					"draft":true,
					"head":{"ref":"feature-branch"}
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "pull_request", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_BodyContainsPullRequest(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"pull_request"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event:        "pull_request",
				BodyContains: "/deploy",
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "PR body contains keyword",
			payload: `{
				"action":"opened",
				"sender":{"login":"user"},
				"pull_request":{
					"number":1,
					"title":"Test PR",
					"body":"Please /deploy this to staging",
					"html_url":"https://github.com/owner/repo/pull/1",
					"state":"open",
					"head":{"ref":"feature-branch"}
				}
			}`,
			want: true,
		},
		{
			name: "PR body does not contain keyword",
			payload: `{
				"action":"opened",
				"sender":{"login":"user"},
				"pull_request":{
					"number":1,
					"title":"Test PR",
					"body":"Just a regular PR",
					"html_url":"https://github.com/owner/repo/pull/1",
					"state":"open",
					"head":{"ref":"feature-branch"}
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "pull_request", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_BranchFilter(t *testing.T) {
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"push"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event:  "push",
				Branch: "main",
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "matching branch",
			payload: `{
				"ref":"refs/heads/main",
				"sender":{"login":"user"},
				"head_commit":{"id":"abc123"}
			}`,
			want: true,
		},
		{
			name: "non-matching branch",
			payload: `{
				"ref":"refs/heads/feature",
				"sender":{"login":"user"},
				"head_commit":{"id":"abc123"}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "push", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitHubEvent_ORSemantics(t *testing.T) {
	// Multiple filters for the same event type should use OR semantics
	spawner := &v1alpha1.GitHubWebhook{
		Events: []string{"issues"},
		Filters: []v1alpha1.GitHubWebhookFilter{
			{
				Event:  "issues",
				Action: "opened",
			},
			{
				Event:  "issues",
				Action: "closed",
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "matches first filter",
			payload: `{"action":"opened","sender":{"login":"user"}}`,
			want:    true,
		},
		{
			name:    "matches second filter",
			payload: `{"action":"closed","sender":{"login":"user"}}`,
			want:    true,
		},
		{
			name:    "matches neither filter",
			payload: `{"action":"edited","sender":{"login":"user"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatch(t, spawner, "issues", []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesGitHubEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseGitHubWebhook(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		payload   string
		wantEvent string
		wantTitle string
		wantErr   bool
	}{
		{
			name:      "issues event",
			eventType: "issues",
			payload: `{
				"action":"opened",
				"sender":{"login":"testuser"},
				"issue":{
					"number":42,
					"title":"Test Issue",
					"body":"This is a test issue",
					"html_url":"https://github.com/owner/repo/issues/42",
					"state":"open"
				}
			}`,
			wantEvent: "issues",
			wantTitle: "Test Issue",
			wantErr:   false,
		},
		{
			name:      "pull request event",
			eventType: "pull_request",
			payload: `{
				"action":"opened",
				"sender":{"login":"testuser"},
				"pull_request":{
					"number":123,
					"title":"Test PR",
					"body":"This is a test PR",
					"html_url":"https://github.com/owner/repo/pull/123",
					"state":"open",
					"head":{"ref":"feature-branch"}
				}
			}`,
			wantEvent: "pull_request",
			wantTitle: "Test PR",
			wantErr:   false,
		},
		{
			name:      "invalid JSON",
			eventType: "issues",
			payload:   `{invalid json}`,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGitHubWebhook(tt.eventType, []byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseGitHubWebhook() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Event != tt.wantEvent {
					t.Errorf("ParseGitHubWebhook() Event = %v, want %v", got.Event, tt.wantEvent)
				}
				if got.Title != tt.wantTitle {
					t.Errorf("ParseGitHubWebhook() Title = %v, want %v", got.Title, tt.wantTitle)
				}
			}
		})
	}
}

// buildTaskName mirrors the task name generation logic from handler.go.
func buildTaskName(spawnerName, eventType, deliveryID string) string {
	sanitizedEventType := strings.ReplaceAll(eventType, "_", "-")
	sum := sha256.Sum256([]byte(deliveryID))
	shortHash := hex.EncodeToString(sum[:])[:12]
	taskName := fmt.Sprintf("%s-%s-%s", spawnerName, sanitizedEventType, shortHash)
	if len(taskName) > 63 {
		taskName = strings.TrimRight(taskName[:63], "-.")
	}
	return taskName
}

func TestTaskNameSanitization(t *testing.T) {
	tests := []struct {
		name        string
		spawnerName string
		eventType   string
		deliveryID  string
	}{
		{
			name:        "pull_request event with delivery ID",
			spawnerName: "dep-review",
			eventType:   "pull_request",
			deliveryID:  "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
		{
			name:        "issue_comment event with delivery ID",
			spawnerName: "comment-handler",
			eventType:   "issue_comment",
			deliveryID:  "deadbeef-1234-5678-9abc-def012345678",
		},
		{
			name:        "push event with short delivery ID",
			spawnerName: "push-handler",
			eventType:   "push",
			deliveryID:  "abc123",
		},
		{
			name:        "long task name truncated correctly",
			spawnerName: "very-long-spawner-name-that-exceeds-kubernetes-limits",
			eventType:   "pull_request_review_comment",
			deliveryID:  "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskName := buildTaskName(tt.spawnerName, tt.eventType, tt.deliveryID)

			// Verify the task name is valid for Kubernetes
			if strings.Contains(taskName, "_") {
				t.Errorf("Task name contains underscores which are invalid for Kubernetes: %v", taskName)
			}
			if len(taskName) > 63 {
				t.Errorf("Task name exceeds 63 character limit: %v (length: %d)", taskName, len(taskName))
			}
			if strings.HasSuffix(taskName, "-") || strings.HasSuffix(taskName, ".") {
				t.Errorf("Task name ends with invalid character: %v", taskName)
			}
		})
	}
}

func TestTaskNameUniqueness(t *testing.T) {
	// Different delivery IDs must produce different task names
	nameA := buildTaskName("spawner", "issues", "delivery-a")
	nameB := buildTaskName("spawner", "issues", "delivery-b")
	if nameA == nameB {
		t.Errorf("Different delivery IDs produced identical task names: %s", nameA)
	}

	// Same delivery ID must produce the same task name (deterministic)
	name1 := buildTaskName("spawner", "issues", "same-delivery")
	name2 := buildTaskName("spawner", "issues", "same-delivery")
	if name1 != name2 {
		t.Errorf("Same delivery ID produced different task names: %s vs %s", name1, name2)
	}
}

func TestParseGitHubWebhook_RepositoryExtraction(t *testing.T) {
	tests := []struct {
		name          string
		eventType     string
		payload       string
		wantRepo      string
		wantRepoOwner string
		wantRepoName  string
	}{
		{
			name:      "issues event with repository",
			eventType: "issues",
			payload: `{
				"action":"opened",
				"sender":{"login":"testuser"},
				"repository":{"full_name":"myorg/myrepo","name":"myrepo","owner":{"login":"myorg"}},
				"issue":{"number":42,"title":"Test","state":"open"}
			}`,
			wantRepo:      "myorg/myrepo",
			wantRepoOwner: "myorg",
			wantRepoName:  "myrepo",
		},
		{
			name:      "pull_request event with repository",
			eventType: "pull_request",
			payload: `{
				"action":"opened",
				"sender":{"login":"testuser"},
				"repository":{"full_name":"owner/repo","name":"repo","owner":{"login":"owner"}},
				"pull_request":{"number":10,"title":"PR","state":"open","head":{"ref":"main"}}
			}`,
			wantRepo:      "owner/repo",
			wantRepoOwner: "owner",
			wantRepoName:  "repo",
		},
		{
			name:      "issue_comment event with repository",
			eventType: "issue_comment",
			payload: `{
				"action":"created",
				"sender":{"login":"testuser"},
				"repository":{"full_name":"org/project","name":"project","owner":{"login":"org"}},
				"issue":{"number":5,"title":"Issue","state":"open"},
				"comment":{"body":"hello"}
			}`,
			wantRepo:      "org/project",
			wantRepoOwner: "org",
			wantRepoName:  "project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGitHubWebhook(tt.eventType, []byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseGitHubWebhook() error = %v", err)
			}
			if got.Repository != tt.wantRepo {
				t.Errorf("Repository = %v, want %v", got.Repository, tt.wantRepo)
			}
			if got.RepositoryOwner != tt.wantRepoOwner {
				t.Errorf("RepositoryOwner = %v, want %v", got.RepositoryOwner, tt.wantRepoOwner)
			}
			if got.RepositoryName != tt.wantRepoName {
				t.Errorf("RepositoryName = %v, want %v", got.RepositoryName, tt.wantRepoName)
			}
		})
	}
}

func TestMatchesGitHubEvent_RepositoryFiltering(t *testing.T) {
	payloadRepoA := `{
		"action":"opened",
		"sender":{"login":"testuser"},
		"repository":{"full_name":"org/repo-a","name":"repo-a","owner":{"login":"org"}},
		"issue":{"number":1,"title":"Issue in A","state":"open"}
	}`

	payloadRepoB := `{
		"action":"opened",
		"sender":{"login":"testuser"},
		"repository":{"full_name":"org/repo-b","name":"repo-b","owner":{"login":"org"}},
		"issue":{"number":1,"title":"Issue in B","state":"open"}
	}`

	spawnerRepoA := &v1alpha1.GitHubWebhook{
		Events:     []string{"issues"},
		Repository: "org/repo-a",
	}

	spawnerNoRepo := &v1alpha1.GitHubWebhook{
		Events: []string{"issues"},
	}

	tests := []struct {
		name    string
		spawner *v1alpha1.GitHubWebhook
		payload string
		want    bool
	}{
		{
			name:    "spawner with repo filter matches correct repo",
			spawner: spawnerRepoA,
			payload: payloadRepoA,
			want:    true,
		},
		{
			name:    "spawner with repo filter rejects wrong repo",
			spawner: spawnerRepoA,
			payload: payloadRepoB,
			want:    false,
		},
		{
			name:    "spawner without repo filter accepts any repo",
			spawner: spawnerNoRepo,
			payload: payloadRepoA,
			want:    true,
		},
		{
			name:    "spawner without repo filter accepts other repo",
			spawner: spawnerNoRepo,
			payload: payloadRepoB,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseGitHubWebhook("issues", []byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseGitHubWebhook() error = %v", err)
			}

			// First check repository filter (simulating matchesSpawner logic)
			got := true
			if tt.spawner.Repository != "" {
				if eventData.Repository != tt.spawner.Repository {
					got = false
				}
			}

			// Then check event/action filters
			if got {
				matched, err := MatchesGitHubEvent(tt.spawner, "issues", eventData)
				if err != nil {
					t.Fatalf("MatchesGitHubEvent() error = %v", err)
				}
				got = matched
			}

			if got != tt.want {
				t.Errorf("Repository filtering = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseGitHubWebhook_IssueCommentOnPR_ExtractsPullRequestAPIURL(t *testing.T) {
	payload := `{
		"action": "created",
		"sender": {"login": "testuser"},
		"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
		"issue": {
			"number": 42,
			"title": "Test PR",
			"body": "PR body",
			"html_url": "https://github.com/org/repo/pull/42",
			"state": "open",
			"pull_request": {
				"url": "https://api.github.com/repos/org/repo/pulls/42",
				"html_url": "https://github.com/org/repo/pull/42"
			}
		},
		"comment": {"body": "/review"}
	}`

	got, err := ParseGitHubWebhook("issue_comment", []byte(payload))
	if err != nil {
		t.Fatalf("ParseGitHubWebhook() error = %v", err)
	}

	if got.PullRequestAPIURL != "https://api.github.com/repos/org/repo/pulls/42" {
		t.Errorf("PullRequestAPIURL = %q, want %q", got.PullRequestAPIURL, "https://api.github.com/repos/org/repo/pulls/42")
	}
	if got.Number != 42 {
		t.Errorf("Number = %d, want 42", got.Number)
	}
	// Branch should be empty at parse time (enriched lazily)
	if got.Branch != "" {
		t.Errorf("Branch should be empty at parse time, got %q", got.Branch)
	}
}

func TestParseGitHubWebhook_IssueCommentOnIssue_NoPullRequestAPIURL(t *testing.T) {
	payload := `{
		"action": "created",
		"sender": {"login": "testuser"},
		"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
		"issue": {
			"number": 10,
			"title": "Plain Issue",
			"body": "Issue body",
			"html_url": "https://github.com/org/repo/issues/10",
			"state": "open"
		},
		"comment": {"body": "hello"}
	}`

	got, err := ParseGitHubWebhook("issue_comment", []byte(payload))
	if err != nil {
		t.Fatalf("ParseGitHubWebhook() error = %v", err)
	}

	if got.PullRequestAPIURL != "" {
		t.Errorf("PullRequestAPIURL should be empty for plain issues, got %q", got.PullRequestAPIURL)
	}
}

func TestNeedsBranchEnrichment(t *testing.T) {
	tests := []struct {
		name      string
		eventData *GitHubEventData
		want      bool
	}{
		{
			name: "needs enrichment - PR comment with no branch",
			eventData: &GitHubEventData{
				PullRequestAPIURL: "https://api.github.com/repos/org/repo/pulls/42",
			},
			want: true,
		},
		{
			name: "no enrichment needed - branch already set",
			eventData: &GitHubEventData{
				Branch:            "feature-branch",
				PullRequestAPIURL: "https://api.github.com/repos/org/repo/pulls/42",
			},
			want: false,
		},
		{
			name: "no enrichment needed - plain issue comment",
			eventData: &GitHubEventData{
				PullRequestAPIURL: "",
			},
			want: false,
		},
		{
			name:      "no enrichment needed - empty event data",
			eventData: &GitHubEventData{},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsBranchEnrichment(tt.eventData)
			if got != tt.want {
				t.Errorf("needsBranchEnrichment() = %v, want %v", got, tt.want)
			}
		})
	}
}
