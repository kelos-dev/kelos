package webhook

import (
	"testing"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestMatchesLinearEvent_TypeFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue", "Comment"},
	}

	tests := []struct {
		name      string
		eventType string
		want      bool
		wantErr   bool
	}{
		{
			name:      "allowed event type",
			eventType: "Issue",
			want:      true,
		},
		{
			name:      "another allowed event type",
			eventType: "Comment",
			want:      true,
		},
		{
			name:      "disallowed event type",
			eventType: "Project",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"type":"` + tt.eventType + `","action":"create","data":{"id":"123"}}`)
			got, err := MatchesLinearEvent(spawner, payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("MatchesLinearEvent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_ActionFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				Action: "create",
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
			payload: `{"type":"Issue","action":"create","data":{"id":"123"}}`,
			want:    true,
		},
		{
			name:    "non-matching action",
			payload: `{"type":"Issue","action":"update","data":{"id":"123"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchesLinearEvent(spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_StateFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				States: []string{"Todo", "In Progress"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "matching state",
			payload: `{
				"type":"Issue",
				"action":"update",
				"data":{
					"id":"123",
					"title":"Test issue",
					"state":{"name":"Todo"}
				}
			}`,
			want: true,
		},
		{
			name: "another matching state",
			payload: `{
				"type":"Issue",
				"action":"update",
				"data":{
					"id":"123",
					"title":"Test issue",
					"state":{"name":"In Progress"}
				}
			}`,
			want: true,
		},
		{
			name: "non-matching state",
			payload: `{
				"type":"Issue",
				"action":"update",
				"data":{
					"id":"123",
					"title":"Test issue",
					"state":{"name":"Done"}
				}
			}`,
			want: false,
		},
		{
			name: "no state data",
			payload: `{
				"type":"Issue",
				"action":"update",
				"data":{
					"id":"123",
					"title":"Test issue"
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchesLinearEvent(spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_LabelsFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
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
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
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
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
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
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[]
				}
			}`,
			want: false,
		},
		{
			name: "labels field missing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue"
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchesLinearEvent(spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_ExcludeLabelsFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:          "Issue",
				ExcludeLabels: []string{"wontfix", "duplicate"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "no excluded labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"frontend"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "has excluded label",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"wontfix"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "has another excluded label",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"duplicate"},
						{"name":"frontend"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "empty labels array",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[]
				}
			}`,
			want: true,
		},
		{
			name: "no labels field",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue"
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchesLinearEvent(spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_ORSemantics(t *testing.T) {
	// Multiple filters for the same event type should use OR semantics
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				Action: "create",
			},
			{
				Type:   "Issue",
				Action: "update",
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
			payload: `{"type":"Issue","action":"create","data":{"id":"123"}}`,
			want:    true,
		},
		{
			name:    "matches second filter",
			payload: `{"type":"Issue","action":"update","data":{"id":"123"}}`,
			want:    true,
		},
		{
			name:    "matches neither filter",
			payload: `{"type":"Issue","action":"remove","data":{"id":"123"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchesLinearEvent(spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_NoFilters(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue", "Comment"},
		// No filters - should match all allowed types
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "allowed type with no filters",
			payload: `{"type":"Issue","action":"create","data":{"id":"123"}}`,
			want:    true,
		},
		{
			name:    "another allowed type with no filters",
			payload: `{"type":"Comment","action":"update","data":{"id":"456"}}`,
			want:    true,
		},
		{
			name:    "disallowed type",
			payload: `{"type":"Project","action":"create","data":{"id":"789"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchesLinearEvent(spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseLinearWebhook(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantType string
		wantID   string
		wantErr  bool
	}{
		{
			name: "issue event",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"issue-123",
					"title":"Test Issue",
					"state":{"name":"Todo"}
				}
			}`,
			wantType: "Issue",
			wantID:   "issue-123",
			wantErr:  false,
		},
		{
			name: "comment event",
			payload: `{
				"type":"Comment",
				"action":"update",
				"data":{
					"id":"comment-456",
					"body":"Updated comment"
				}
			}`,
			wantType: "Comment",
			wantID:   "comment-456",
			wantErr:  false,
		},
		{
			name: "numeric ID",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":789,
					"title":"Numeric ID Issue"
				}
			}`,
			wantType: "Issue",
			wantID:   "789",
			wantErr:  false,
		},
		{
			name:    "invalid JSON",
			payload: `{invalid json}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLinearWebhook([]byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseLinearWebhook() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Type != tt.wantType {
					t.Errorf("ParseLinearWebhook() Type = %v, want %v", got.Type, tt.wantType)
				}
				if got.ID != tt.wantID {
					t.Errorf("ParseLinearWebhook() ID = %v, want %v", got.ID, tt.wantID)
				}
			}
		})
	}
}
