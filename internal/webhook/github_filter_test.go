package webhook

import (
	"testing"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

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
			got, err := MatchesGitHubEvent(spawner, tt.eventType, payload)
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
			got, err := MatchesGitHubEvent(spawner, "issues", []byte(tt.payload))
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
			got, err := MatchesGitHubEvent(spawner, "issues", []byte(tt.payload))
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
			got, err := MatchesGitHubEvent(spawner, "issues", []byte(tt.payload))
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
			got, err := MatchesGitHubEvent(spawner, "pull_request", []byte(tt.payload))
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
			got, err := MatchesGitHubEvent(spawner, "push", []byte(tt.payload))
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
			got, err := MatchesGitHubEvent(spawner, "issues", []byte(tt.payload))
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
