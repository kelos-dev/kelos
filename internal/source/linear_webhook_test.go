package source

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestLinearWebhookSource_ParseIssuePayload(t *testing.T) {
	payload := []byte(`{
		"type": "Issue",
		"action": "create",
		"data": {
			"id": "abc-123",
			"identifier": "ENG-42",
			"number": 42,
			"title": "Fix login bug",
			"description": "Users cannot log in after password reset",
			"url": "https://linear.app/myteam/issue/ENG-42",
			"state": {
				"name": "Todo",
				"type": "unstarted"
			},
			"labels": [
				{"name": "bug"},
				{"name": "high-priority"}
			],
			"team": {
				"key": "ENG",
				"name": "Engineering"
			}
		}
	}`)

	var parsed LinearWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &LinearWebhookSource{}
	item, ok := source.payloadToWorkItem(parsed)

	if !ok {
		t.Fatal("Expected payload to be converted to WorkItem")
	}

	if item.ID != "ENG-42" {
		t.Errorf("Expected ID 'ENG-42', got %s", item.ID)
	}
	if item.Number != 42 {
		t.Errorf("Expected number 42, got %d", item.Number)
	}
	if item.Title != "Fix login bug" {
		t.Errorf("Expected title 'Fix login bug', got %s", item.Title)
	}
	if item.Kind != "Todo" {
		t.Errorf("Expected kind 'Todo', got %s", item.Kind)
	}
	if len(item.Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(item.Labels))
	}
}

func TestLinearWebhookSource_SkipNonIssueEvents(t *testing.T) {
	payload := []byte(`{
		"type": "Comment",
		"action": "create",
		"data": {
			"identifier": "ENG-42-comment",
			"body": "This is a comment"
		}
	}`)

	var parsed LinearWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &LinearWebhookSource{}
	_, ok := source.payloadToWorkItem(parsed)

	if ok {
		t.Error("Expected Comment event to be skipped by default")
	}
}

func TestLinearWebhookSource_ProcessCommentWhenAllowed(t *testing.T) {
	payload := []byte(`{
		"type": "Comment",
		"action": "create",
		"data": {
			"identifier": "comment-123",
			"number": 123,
			"title": "Comment title",
			"description": "This is a comment",
			"url": "https://linear.app/myteam/issue/ENG-42#comment-123"
		}
	}`)

	var parsed LinearWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &LinearWebhookSource{
		Types: []string{"Comment"},
	}
	item, ok := source.payloadToWorkItem(parsed)

	if !ok {
		t.Fatal("Expected Comment event to be processed when Types includes Comment")
	}

	if item.Kind != "Comment" {
		t.Errorf("Expected kind 'Comment', got %s", item.Kind)
	}
}

func TestLinearWebhookSource_SkipRemoveAction(t *testing.T) {
	payload := []byte(`{
		"type": "Issue",
		"action": "remove",
		"data": {
			"identifier": "ENG-42",
			"number": 42,
			"title": "Deleted Issue"
		}
	}`)

	var parsed LinearWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &LinearWebhookSource{}
	_, ok := source.payloadToWorkItem(parsed)

	if ok {
		t.Error("Expected remove action to be skipped by default")
	}
}

func TestLinearWebhookSource_ProcessRemoveWhenAllowed(t *testing.T) {
	payload := []byte(`{
		"type": "Issue",
		"action": "remove",
		"data": {
			"identifier": "ENG-42",
			"number": 42,
			"title": "Deleted Issue"
		}
	}`)

	var parsed LinearWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &LinearWebhookSource{
		Actions: []string{"remove"},
	}
	item, ok := source.payloadToWorkItem(parsed)

	if !ok {
		t.Fatal("Expected remove action to be processed when Actions includes remove")
	}

	if item.ID != "ENG-42" {
		t.Errorf("Expected ID 'ENG-42', got %s", item.ID)
	}
}

func TestLinearWebhookSource_StateFiltering(t *testing.T) {
	tests := []struct {
		name          string
		states        []string
		stateName     string
		stateType     string
		expectedMatch bool
	}{
		{
			name:          "No filter - accepts non-terminal state",
			states:        nil,
			stateName:     "In Progress",
			stateType:     "started",
			expectedMatch: true,
		},
		{
			name:          "No filter - excludes completed",
			states:        nil,
			stateName:     "Done",
			stateType:     "completed",
			expectedMatch: false,
		},
		{
			name:          "No filter - excludes canceled",
			states:        nil,
			stateName:     "Canceled",
			stateType:     "canceled",
			expectedMatch: false,
		},
		{
			name:          "Filter matches state name",
			states:        []string{"Todo", "In Progress"},
			stateName:     "Todo",
			stateType:     "unstarted",
			expectedMatch: true,
		},
		{
			name:          "Filter does not match state name",
			states:        []string{"Todo"},
			stateName:     "In Progress",
			stateType:     "started",
			expectedMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &LinearWebhookSource{
				States: tt.states,
			}

			state := struct {
				Name string `json:"name"`
				Type string `json:"type"`
			}{
				Name: tt.stateName,
				Type: tt.stateType,
			}

			matches := source.matchesState(state)
			if matches != tt.expectedMatch {
				t.Errorf("Expected match=%v, got %v", tt.expectedMatch, matches)
			}
		})
	}
}

func TestLinearWebhookSource_LabelFiltering(t *testing.T) {
	tests := []struct {
		name           string
		itemLabels     []string
		requiredLabels []string
		excludeLabels  []string
		expectedMatch  bool
	}{
		{
			name:           "No filters - matches",
			itemLabels:     []string{"bug"},
			requiredLabels: nil,
			excludeLabels:  nil,
			expectedMatch:  true,
		},
		{
			name:           "Required label present",
			itemLabels:     []string{"bug", "high-priority"},
			requiredLabels: []string{"high-priority"},
			excludeLabels:  nil,
			expectedMatch:  true,
		},
		{
			name:           "Required label missing",
			itemLabels:     []string{"bug"},
			requiredLabels: []string{"high-priority"},
			excludeLabels:  nil,
			expectedMatch:  false,
		},
		{
			name:           "Excluded label present",
			itemLabels:     []string{"bug", "wont-fix"},
			requiredLabels: nil,
			excludeLabels:  []string{"wont-fix"},
			expectedMatch:  false,
		},
		{
			name:           "Multiple required labels",
			itemLabels:     []string{"bug", "high-priority", "backend"},
			requiredLabels: []string{"high-priority", "backend"},
			excludeLabels:  nil,
			expectedMatch:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &LinearWebhookSource{
				Labels:        tt.requiredLabels,
				ExcludeLabels: tt.excludeLabels,
			}

			item := WorkItem{Labels: tt.itemLabels}
			payload := LinearWebhookPayload{
				Type: "Issue", // Label filtering only applies to Issue events
			}
			payload.Data.State.Type = "unstarted" // Non-terminal state

			matches := source.matchesFilters(item, payload)
			if matches != tt.expectedMatch {
				t.Errorf("Expected match=%v, got %v", tt.expectedMatch, matches)
			}
		})
	}
}

func TestLinearWebhookSource_Discover(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kelosv1alpha1.AddToScheme(scheme)

	// Create fake client with webhook events
	event1 := &kelosv1alpha1.WebhookEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "event-1",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WebhookEventSpec{
			Source: "linear",
			Payload: []byte(`{
				"type": "Issue",
				"action": "create",
				"data": {
					"identifier": "ENG-100",
					"number": 100,
					"title": "First Issue",
					"description": "Test description",
					"url": "https://linear.app/myteam/issue/ENG-100",
					"state": {"name": "Todo", "type": "unstarted"},
					"labels": [{"name": "bug"}],
					"team": {"key": "ENG"}
				}
			}`),
			ReceivedAt: metav1.Now(),
		},
		Status: kelosv1alpha1.WebhookEventStatus{
			Processed: false,
		},
	}

	event2 := &kelosv1alpha1.WebhookEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "event-2",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WebhookEventSpec{
			Source: "linear",
			Payload: []byte(`{
				"type": "Issue",
				"action": "update",
				"data": {
					"identifier": "ENG-200",
					"number": 200,
					"title": "Second Issue",
					"state": {"name": "Done", "type": "completed"},
					"labels": [],
					"team": {"key": "ENG"}
				}
			}`),
			ReceivedAt: metav1.Now(),
		},
		Status: kelosv1alpha1.WebhookEventStatus{
			Processed: false,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(event1, event2).
		WithStatusSubresource(&kelosv1alpha1.WebhookEvent{}).
		Build()

	src := &LinearWebhookSource{
		Client:      fakeClient,
		Namespace:   "default",
		SpawnerName: "spawner-a",
	}

	items, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// Should only get event1 (event2 has completed state)
	if len(items) != 1 {
		t.Errorf("Expected 1 item, got %d", len(items))
	}

	if len(items) > 0 && items[0].Number != 100 {
		t.Errorf("Expected issue 100, got %d", items[0].Number)
	}

	// Before acknowledgment, matching event should NOT be marked processed
	var beforeAck kelosv1alpha1.WebhookEvent
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "event-1",
		Namespace: "default",
	}, &beforeAck); err != nil {
		t.Fatalf("Failed to get event: %v", err)
	}
	if beforeAck.Status.Processed {
		t.Error("Expected matching event to NOT be processed before acknowledgment")
	}

	// Acknowledge the discovered items
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	src.AcknowledgeItems(context.Background(), ids)

	// After acknowledgment, event should be marked as processed by this spawner
	var updatedEvent kelosv1alpha1.WebhookEvent
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "event-1",
		Namespace: "default",
	}, &updatedEvent); err != nil {
		t.Fatalf("Failed to get updated event: %v", err)
	}

	if !updatedEvent.Status.Processed {
		t.Error("Expected event to be marked as processed after acknowledgment")
	}
	if len(updatedEvent.Status.ProcessedBy) != 1 || updatedEvent.Status.ProcessedBy[0] != "spawner-a" {
		t.Errorf("Expected ProcessedBy to contain 'spawner-a', got %v", updatedEvent.Status.ProcessedBy)
	}
}

func TestLinearWebhookSource_OnlyProcessLinearSource(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kelosv1alpha1.AddToScheme(scheme)

	// Create a GitHub webhook event (should be ignored)
	githubEvent := &kelosv1alpha1.WebhookEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-event",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WebhookEventSpec{
			Source: "github",
			Payload: []byte(`{
				"action": "opened",
				"issue": {"number": 1}
			}`),
			ReceivedAt: metav1.Now(),
		},
		Status: kelosv1alpha1.WebhookEventStatus{
			Processed: false,
		},
	}

	// Create a Linear webhook event
	linearEvent := &kelosv1alpha1.WebhookEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "linear-event",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WebhookEventSpec{
			Source: "linear",
			Payload: []byte(`{
				"type": "Issue",
				"action": "create",
				"data": {
					"identifier": "ENG-300",
					"number": 300,
					"title": "Linear Issue",
					"state": {"name": "Todo", "type": "unstarted"},
					"labels": [],
					"team": {"key": "ENG"}
				}
			}`),
			ReceivedAt: metav1.Now(),
		},
		Status: kelosv1alpha1.WebhookEventStatus{
			Processed: false,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(githubEvent, linearEvent).
		WithStatusSubresource(&kelosv1alpha1.WebhookEvent{}).
		Build()

	src := &LinearWebhookSource{
		Client:      fakeClient,
		Namespace:   "default",
		SpawnerName: "linear-spawner",
	}

	items, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// Should only get the Linear event
	if len(items) != 1 {
		t.Errorf("Expected 1 item, got %d", len(items))
	}

	if len(items) > 0 && items[0].Number != 300 {
		t.Errorf("Expected issue 300, got %d", items[0].Number)
	}

	// Verify GitHub event was not processed
	var githubUpdated kelosv1alpha1.WebhookEvent
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "github-event",
		Namespace: "default",
	}, &githubUpdated); err != nil {
		t.Fatalf("Failed to get GitHub event: %v", err)
	}

	if githubUpdated.Status.Processed {
		t.Error("Expected GitHub event to not be processed by Linear source")
	}
}
