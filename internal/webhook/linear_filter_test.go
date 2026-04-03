package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"

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
			eventData, err := ParseLinearWebhook(payload)
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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

func TestMatchesLinearEvent_LabelsCaseInsensitive(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Labels: []string{"Bug", "Priority:High"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "labels match with different casing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"priority:high"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "labels match with uppercase event labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"BUG"},
						{"name":"PRIORITY:HIGH"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "labels match with mixed casing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"Bug"},
						{"name":"Priority:High"},
						{"name":"frontend"}
					]
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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

func TestMatchesLinearEvent_ExcludeLabelsCaseInsensitive(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				ExcludeLabels: []string{"WontFix"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "excluded label matches with different casing",
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
			name: "excluded label matches with uppercase",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"WONTFIX"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "no excluded label present",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"}
					]
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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

func TestMatchesLinearEvent_TypedLabelsCaseInsensitive(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				Labels: []string{"Bug", "Priority:High"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "labels match with different casing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"priority:high"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "labels match with uppercase event labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"BUG"},
						{"name":"PRIORITY:HIGH"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "labels match with mixed casing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"Bug"},
						{"name":"Priority:High"},
						{"name":"frontend"}
					]
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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

func TestMatchesLinearEvent_TypedExcludeLabelsCaseInsensitive(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:          "Issue",
				ExcludeLabels: []string{"WontFix"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "excluded label matches with different casing",
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
			name: "excluded label matches with uppercase",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"WONTFIX"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "no excluded label present",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"}
					]
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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
				Action: "create",
			},
			{
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
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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

func TestMatchesLinearEvent_CommentLabelsFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Comment"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
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
			name: "comment with issue having all required labels",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"bug"},
							{"name":"priority:high"},
							{"name":"frontend"}
						]
					}
				}
			}`,
			want: true,
		},
		{
			name: "comment with issue missing required label",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"bug"},
							{"name":"frontend"}
						]
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue having no labels",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[]
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue.labels field missing",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue"
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue field missing entirely",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment"
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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

func TestMatchesLinearEvent_CommentExcludeLabelsFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Comment"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
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
			name: "comment with issue having no excluded labels",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"bug"},
							{"name":"frontend"}
						]
					}
				}
			}`,
			want: true,
		},
		{
			name: "comment with issue having excluded label",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"bug"},
							{"name":"wontfix"}
						]
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue having another excluded label",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"duplicate"},
							{"name":"frontend"}
						]
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue having empty labels array",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[]
					}
				}
			}`,
			want: true,
		},
		{
			name: "comment with issue.labels field missing",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue"
					}
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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

func TestMatchesLinearEvent_IssueLabelsRegression(t *testing.T) {
	// Regression test: ensure Issue events still use data.labels (not data.issue.labels)
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Labels: []string{"bug"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "issue with labels at data.labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"issue-123",
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
			name: "issue without required label",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"issue-123",
					"title":"Test issue",
					"labels":[
						{"name":"frontend"}
					]
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
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

func TestExtractLinearWorkItem_CommentIssueID(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantIssueID string
	}{
		{
			name: "comment event with string issue ID",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue"
					}
				}
			}`,
			wantIssueID: "issue-456",
		},
		{
			name: "comment event with numeric issue ID",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":789,
						"title":"Parent issue"
					}
				}
			}`,
			wantIssueID: "789",
		},
		{
			name: "comment event without issue defaults to empty",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment"
				}
			}`,
			wantIssueID: "",
		},
		{
			name: "issue event defaults to empty IssueID",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"issue-123",
					"title":"Test issue"
				}
			}`,
			wantIssueID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Errorf("ParseLinearWebhook() error = %v", err)
				return
			}

			vars := ExtractLinearWorkItem(eventData)

			issueID, ok := vars["IssueID"]
			if !ok {
				t.Fatal("ExtractLinearWorkItem() missing IssueID key in vars")
			}

			issueIDStr, ok := issueID.(string)
			if !ok {
				t.Fatalf("ExtractLinearWorkItem() IssueID is %T, want string", issueID)
			}
			if issueIDStr != tt.wantIssueID {
				t.Errorf("ExtractLinearWorkItem() IssueID = %q, want %q", issueIDStr, tt.wantIssueID)
			}
		})
	}
}

func TestParseLinearWebhook(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected *LinearEventData
		wantErr  bool
	}{
		{
			name: "issue created event",
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "issue-123",
					"title": "Test issue",
					"state": {"name": "Todo"},
					"labels": [
						{"name": "bug"},
						{"name": "urgent"}
					]
				}
			}`,
			expected: &LinearEventData{
				ID:     "issue-123",
				Title:  "Test issue",
				Type:   "Issue",
				Action: "create",
				State:  "Todo",
				Labels: []string{"bug", "urgent"},
			},
			wantErr: false,
		},
		{
			name: "comment created event",
			payload: `{
				"type": "Comment",
				"action": "create",
				"data": {
					"id": "comment-456",
					"body": "This is a comment"
				}
			}`,
			expected: &LinearEventData{
				ID:     "comment-456",
				Title:  "",
				Type:   "Comment",
				Action: "create",
				State:  "",
				Labels: nil,
			},
			wantErr: false,
		},
		{
			name:     "invalid JSON",
			payload:  `invalid json`,
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseLinearWebhook([]byte(tt.payload))

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.expected.ID, result.ID)
				assert.Equal(t, tt.expected.Title, result.Title)
				assert.Equal(t, tt.expected.Type, result.Type)
				assert.Equal(t, tt.expected.Action, result.Action)
				assert.Equal(t, tt.expected.State, result.State)
				assert.Equal(t, tt.expected.Labels, result.Labels)
			}
		})
	}
}

func TestMatchesLinearEvent(t *testing.T) {
	tests := []struct {
		name     string
		config   *v1alpha1.LinearWebhook
		payload  string
		expected bool
		wantErr  bool
	}{
		{
			name: "matches allowed type with no filters",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {"id": "123", "title": "Test"}
			}`,
			expected: true,
			wantErr:  false,
		},
		{
			name: "does not match disallowed type",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Comment"},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {"id": "123", "title": "Test"}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name: "matches with action filter",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Action: "create",
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {"id": "123", "title": "Test"}
			}`,
			expected: true,
			wantErr:  false,
		},
		{
			name: "does not match wrong action",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Action: "update",
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {"id": "123", "title": "Test"}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name: "matches with state filter",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						States: []string{"Todo", "In Progress"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"state": {"name": "Todo"}
				}
			}`,
			expected: true,
			wantErr:  false,
		},
		{
			name: "does not match wrong state",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						States: []string{"Done"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"state": {"name": "Todo"}
				}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name: "matches with required labels",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Labels: []string{"bug"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"labels": [{"name": "bug"}, {"name": "urgent"}]
				}
			}`,
			expected: true,
			wantErr:  false,
		},
		{
			name: "does not match missing required label",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Labels: []string{"feature"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"labels": [{"name": "bug"}, {"name": "urgent"}]
				}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name: "excludes based on exclude labels",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						ExcludeLabels: []string{"wontfix"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"labels": [{"name": "bug"}, {"name": "wontfix"}]
				}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name:     "invalid JSON payload",
			config:   &v1alpha1.LinearWebhook{Types: []string{"Issue"}},
			payload:  `invalid json`,
			expected: false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			if !assert.NoError(t, err) {
				return
			}

			result, err := MatchesLinearEvent(tt.config, eventData)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractLinearWorkItem(t *testing.T) {
	eventData := &LinearEventData{
		ID:      "issue-123",
		Title:   "Test Issue",
		Type:    "Issue",
		Action:  "create",
		State:   "Todo",
		Labels:  []string{"bug", "urgent"},
		Payload: map[string]interface{}{"key": "value"},
	}

	result := ExtractLinearWorkItem(eventData)

	expected := map[string]interface{}{
		"ID":      "issue-123",
		"Title":   "Test Issue",
		"Kind":    "LinearWebhook",
		"Type":    "Issue",
		"Action":  "create",
		"State":   "Todo",
		"Labels":  "bug, urgent",
		"IssueID": "",
		"Payload": map[string]interface{}{"key": "value"},
	}

	assert.Equal(t, expected, result)
}

func TestEnrichLinearCommentIssue(t *testing.T) {
	desc := "A detailed description of the issue"
	// Set up a mock Linear API server that returns labels and description
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req linearGraphQLRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issue": map[string]interface{}{
					"description": desc,
					"labels": map[string]interface{}{
						"nodes": []map[string]interface{}{
							{"name": "bug"},
							{"name": "priority:high"},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	origFetcher := linearDetailsFetcher
	linearDetailsFetcher = func(ctx context.Context, issueID string) (*linearIssueDetails, error) {
		return fetchLinearIssueDetailsFromURL(ctx, server.URL, "test-key", issueID)
	}
	defer func() { linearDetailsFetcher = origFetcher }()

	payload := `{
		"type":"Comment",
		"action":"create",
		"data":{
			"id":"comment-123",
			"body":"Test comment",
			"issue":{
				"id":"issue-456",
				"title":"Parent issue"
			}
		}
	}`

	eventData, err := ParseLinearWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseLinearWebhook() error = %v", err)
	}

	enrichLinearCommentIssue(context.Background(), logr.Discard(), eventData)

	// Check that labels were injected into the payload
	dataObj := eventData.Payload["data"].(map[string]interface{})
	issue := dataObj["issue"].(map[string]interface{})
	labels, ok := issue["labels"].([]interface{})
	if !ok {
		t.Fatal("Expected labels to be injected into issue")
	}
	if len(labels) != 2 {
		t.Fatalf("Expected 2 labels, got %d", len(labels))
	}

	label0 := labels[0].(map[string]interface{})
	if label0["name"] != "bug" {
		t.Errorf("Expected first label 'bug', got %v", label0["name"])
	}
	label1 := labels[1].(map[string]interface{})
	if label1["name"] != "priority:high" {
		t.Errorf("Expected second label 'priority:high', got %v", label1["name"])
	}

	// Check convenience field was also updated
	if len(eventData.Labels) != 2 || eventData.Labels[0] != "bug" || eventData.Labels[1] != "priority:high" {
		t.Errorf("Expected eventData.Labels = [bug priority:high], got %v", eventData.Labels)
	}

	// Check that description was injected
	gotDesc, ok := issue["description"].(string)
	if !ok {
		t.Fatal("Expected description to be injected into issue")
	}
	if gotDesc != desc {
		t.Errorf("Expected description %q, got %q", desc, gotDesc)
	}
}

func TestEnrichLinearCommentIssue_PreservesExistingDescription(t *testing.T) {
	apiDesc := "Description from API"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issue": map[string]interface{}{
					"description": apiDesc,
					"labels": map[string]interface{}{
						"nodes": []map[string]interface{}{
							{"name": "bug"},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	origFetcher := linearDetailsFetcher
	linearDetailsFetcher = func(ctx context.Context, issueID string) (*linearIssueDetails, error) {
		return fetchLinearIssueDetailsFromURL(ctx, server.URL, "test-key", issueID)
	}
	defer func() { linearDetailsFetcher = origFetcher }()

	// Payload already has a description — it should NOT be overwritten
	payload := `{
		"type":"Comment",
		"action":"create",
		"data":{
			"id":"comment-123",
			"body":"Test comment",
			"issue":{
				"id":"issue-456",
				"title":"Parent issue",
				"description":"Existing description"
			}
		}
	}`

	eventData, err := ParseLinearWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseLinearWebhook() error = %v", err)
	}

	enrichLinearCommentIssue(context.Background(), logr.Discard(), eventData)

	dataObj := eventData.Payload["data"].(map[string]interface{})
	issue := dataObj["issue"].(map[string]interface{})

	gotDesc, ok := issue["description"].(string)
	if !ok {
		t.Fatal("Expected description in issue")
	}
	if gotDesc != "Existing description" {
		t.Errorf("Expected existing description to be preserved, got %q", gotDesc)
	}
}

func TestEnrichLinearCommentIssue_SkipsNonCommentEvents(t *testing.T) {
	called := false
	origFetcher := linearDetailsFetcher
	linearDetailsFetcher = func(ctx context.Context, issueID string) (*linearIssueDetails, error) {
		called = true
		return &linearIssueDetails{Labels: []string{"bug"}}, nil
	}
	defer func() { linearDetailsFetcher = origFetcher }()

	payload := `{"type":"Issue","action":"create","data":{"id":"i1","title":"Test"}}`
	eventData, err := ParseLinearWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseLinearWebhook() error = %v", err)
	}

	enrichLinearCommentIssue(context.Background(), logr.Discard(), eventData)

	if called {
		t.Error("Expected enrichment to be skipped for non-Comment events")
	}
}

func TestEnrichLinearCommentIssue_MatchesFilterAfterEnrichment(t *testing.T) {
	desc := "Issue description"
	// End-to-end test: Comment payload without labels -> enrich -> filter matches
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issue": map[string]interface{}{
					"description": desc,
					"labels": map[string]interface{}{
						"nodes": []map[string]interface{}{
							{"name": "bug"},
							{"name": "priority:high"},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	origFetcher := linearDetailsFetcher
	linearDetailsFetcher = func(ctx context.Context, issueID string) (*linearIssueDetails, error) {
		return fetchLinearIssueDetailsFromURL(ctx, server.URL, "test-key", issueID)
	}
	defer func() { linearDetailsFetcher = origFetcher }()

	config := &v1alpha1.LinearWebhook{
		Types: []string{"Comment"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{Type: "Comment", Labels: []string{"bug"}},
		},
	}

	// Comment payload without labels — should NOT match before enrichment
	payload := `{
		"type":"Comment",
		"action":"create",
		"data":{
			"id":"comment-123",
			"body":"Test comment",
			"issue":{"id":"issue-456","title":"Parent issue"}
		}
	}`

	eventData, err := ParseLinearWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseLinearWebhook() error = %v", err)
	}

	// Before enrichment — filter should not match
	matched, err := MatchesLinearEvent(config, eventData)
	if err != nil {
		t.Fatalf("MatchesLinearEvent() error = %v", err)
	}
	if matched {
		t.Error("Expected no match before enrichment")
	}

	// Enrich
	enrichLinearCommentIssue(context.Background(), logr.Discard(), eventData)

	// After enrichment — filter should match
	matched, err = MatchesLinearEvent(config, eventData)
	if err != nil {
		t.Fatalf("MatchesLinearEvent() error = %v", err)
	}
	if !matched {
		t.Error("Expected match after enrichment")
	}

	// Verify description was also injected
	dataObj := eventData.Payload["data"].(map[string]interface{})
	issue := dataObj["issue"].(map[string]interface{})
	if issue["description"] != desc {
		t.Errorf("Expected description %q, got %v", desc, issue["description"])
	}
}
