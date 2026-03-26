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

func TestGitHubWebhookSource_ParseIssuePayload(t *testing.T) {
	payload := []byte(`{
		"action": "opened",
		"issue": {
			"number": 123,
			"title": "Test Issue",
			"body": "Issue body",
			"html_url": "https://github.com/test/repo/issues/123",
			"state": "open",
			"labels": [
				{"name": "bug"},
				{"name": "kelos-task"}
			]
		}
	}`)

	var parsed GitHubWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &GitHubWebhookSource{}
	item, ok := source.payloadToWorkItem(parsed)

	if !ok {
		t.Fatal("Expected payload to be converted to WorkItem")
	}

	if item.Number != 123 {
		t.Errorf("Expected number 123, got %d", item.Number)
	}
	if item.Title != "Test Issue" {
		t.Errorf("Expected title 'Test Issue', got %s", item.Title)
	}
	if item.Kind != "Issue" {
		t.Errorf("Expected kind 'Issue', got %s", item.Kind)
	}
	if len(item.Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(item.Labels))
	}
}

func TestGitHubWebhookSource_ParsePullRequestPayload(t *testing.T) {
	payload := []byte(`{
		"action": "opened",
		"pull_request": {
			"number": 456,
			"title": "Test PR",
			"body": "PR body",
			"html_url": "https://github.com/test/repo/pull/456",
			"state": "open",
			"labels": [
				{"name": "enhancement"}
			],
			"head": {
				"ref": "feature-branch"
			}
		}
	}`)

	var parsed GitHubWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &GitHubWebhookSource{}
	item, ok := source.payloadToWorkItem(parsed)

	if !ok {
		t.Fatal("Expected payload to be converted to WorkItem")
	}

	if item.Number != 456 {
		t.Errorf("Expected number 456, got %d", item.Number)
	}
	if item.Kind != "PR" {
		t.Errorf("Expected kind 'PR', got %s", item.Kind)
	}
	if item.Branch != "feature-branch" {
		t.Errorf("Expected branch 'feature-branch', got %s", item.Branch)
	}
}

func TestGitHubWebhookSource_SkipClosedIssues(t *testing.T) {
	payload := []byte(`{
		"action": "closed",
		"issue": {
			"number": 123,
			"title": "Closed Issue",
			"state": "closed"
		}
	}`)

	var parsed GitHubWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &GitHubWebhookSource{}
	_, ok := source.payloadToWorkItem(parsed)

	if ok {
		t.Error("Expected closed issue to be skipped")
	}
}

func TestGitHubWebhookSource_LabelFiltering(t *testing.T) {
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
			itemLabels:     []string{"bug", "kelos-task"},
			requiredLabels: []string{"kelos-task"},
			excludeLabels:  nil,
			expectedMatch:  true,
		},
		{
			name:           "Required label missing",
			itemLabels:     []string{"bug"},
			requiredLabels: []string{"kelos-task"},
			excludeLabels:  nil,
			expectedMatch:  false,
		},
		{
			name:           "Excluded label present",
			itemLabels:     []string{"bug", "skip"},
			requiredLabels: nil,
			excludeLabels:  []string{"skip"},
			expectedMatch:  false,
		},
		{
			name:           "Multiple required labels",
			itemLabels:     []string{"bug", "kelos-task", "high-priority"},
			requiredLabels: []string{"kelos-task", "high-priority"},
			excludeLabels:  nil,
			expectedMatch:  true,
		},
		{
			name:           "Required present but also excluded",
			itemLabels:     []string{"kelos-task", "skip"},
			requiredLabels: []string{"kelos-task"},
			excludeLabels:  []string{"skip"},
			expectedMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &GitHubWebhookSource{
				Labels:        tt.requiredLabels,
				ExcludeLabels: tt.excludeLabels,
			}

			matches := source.matchesLabels(tt.itemLabels)
			if matches != tt.expectedMatch {
				t.Errorf("Expected match=%v, got %v", tt.expectedMatch, matches)
			}
		})
	}
}

func TestGitHubWebhookSource_Discover(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kelosv1alpha1.AddToScheme(scheme)

	// Create fake client with webhook events
	event1 := &kelosv1alpha1.WebhookEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "event-1",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WebhookEventSpec{
			Source: "github",
			Payload: []byte(`{
				"action": "opened",
				"issue": {
					"number": 100,
					"title": "First Issue",
					"body": "Body",
					"html_url": "https://github.com/test/repo/issues/100",
					"state": "open",
					"labels": [{"name": "bug"}]
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
			Source: "github",
			Payload: []byte(`{
				"action": "opened",
				"issue": {
					"number": 200,
					"title": "Second Issue",
					"state": "closed"
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

	source := &GitHubWebhookSource{
		Client:    fakeClient,
		Namespace: "default",
	}

	items, err := source.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// Should only get event1 (event2 has closed issue)
	if len(items) != 1 {
		t.Errorf("Expected 1 item, got %d", len(items))
	}

	if len(items) > 0 && items[0].Number != 100 {
		t.Errorf("Expected issue 100, got %d", items[0].Number)
	}

	// Verify events were marked as processed
	var updatedEvent kelosv1alpha1.WebhookEvent
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "event-1",
		Namespace: "default",
	}, &updatedEvent); err != nil {
		t.Fatalf("Failed to get updated event: %v", err)
	}

	if !updatedEvent.Status.Processed {
		t.Error("Expected event to be marked as processed")
	}
}
