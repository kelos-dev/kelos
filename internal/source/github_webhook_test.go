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

func webhookPayloadToWorkItem(s *GitHubWebhookSource, payload GitHubWebhookPayload) (WorkItem, bool) {
	return s.payloadToWorkItem(context.Background(), payload, nil)
}

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
			],
			"user": {"login": "octocat"}
		}
	}`)

	var parsed GitHubWebhookPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Failed to parse payload: %v", err)
	}

	source := &GitHubWebhookSource{}
	item, ok := webhookPayloadToWorkItem(source, parsed)

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
	if item.Author != "octocat" {
		t.Errorf("Expected author 'octocat', got %s", item.Author)
	}
	if item.State != "open" {
		t.Errorf("Expected state 'open', got %s", item.State)
	}
	if item.Action != "opened" {
		t.Errorf("Expected action 'opened', got %s", item.Action)
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
			"draft": true,
			"labels": [
				{"name": "enhancement"}
			],
			"user": {"login": "contributor"},
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
	item, ok := webhookPayloadToWorkItem(source, parsed)

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
	if item.Author != "contributor" {
		t.Errorf("Expected author 'contributor', got %s", item.Author)
	}
	if item.State != "open" {
		t.Errorf("Expected state 'open', got %s", item.State)
	}
	if item.Action != "opened" {
		t.Errorf("Expected action 'opened', got %s", item.Action)
	}
	if !item.Draft {
		t.Error("Expected draft to be true")
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
	_, ok := webhookPayloadToWorkItem(source, parsed)

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

func TestGitHubWebhookSource_AuthorFiltering(t *testing.T) {
	makePayload := func() GitHubWebhookPayload {
		raw := []byte(`{"action":"opened","issue":{"number":1,"title":"Test","state":"open","html_url":"https://github.com/t/r/issues/1","user":{"login":"octocat"}}}`)
		var p GitHubWebhookPayload
		_ = json.Unmarshal(raw, &p)
		return p
	}

	// Should match when author filter matches
	s := &GitHubWebhookSource{Author: "octocat"}
	_, ok := webhookPayloadToWorkItem(s, makePayload())
	if !ok {
		t.Error("Expected item to match author filter")
	}

	// Should not match when author filter does not match
	s = &GitHubWebhookSource{Author: "other-user"}
	_, ok = webhookPayloadToWorkItem(s, makePayload())
	if ok {
		t.Error("Expected item to be filtered out by author")
	}

	// Should match when no author filter is set
	s = &GitHubWebhookSource{}
	_, ok = webhookPayloadToWorkItem(s, makePayload())
	if !ok {
		t.Error("Expected item to match with no author filter")
	}
}

func TestGitHubWebhookSource_StateFiltering(t *testing.T) {
	makePayload := func(state string) GitHubWebhookPayload {
		raw := []byte(`{"action":"opened","issue":{"number":1,"title":"T","state":"` + state + `","html_url":"https://github.com/t/r/issues/1"}}`)
		var p GitHubWebhookPayload
		_ = json.Unmarshal(raw, &p)
		return p
	}

	// Default (empty) state should only match "open"
	s := &GitHubWebhookSource{}
	_, ok := webhookPayloadToWorkItem(s, makePayload("open"))
	if !ok {
		t.Error("Expected open issue to match default state filter")
	}
	_, ok = webhookPayloadToWorkItem(s, makePayload("closed"))
	if ok {
		t.Error("Expected closed issue to be filtered by default state filter")
	}

	// Explicit "closed" state
	s = &GitHubWebhookSource{State: "closed"}
	_, ok = webhookPayloadToWorkItem(s, makePayload("closed"))
	if !ok {
		t.Error("Expected closed issue to match 'closed' state filter")
	}
	_, ok = webhookPayloadToWorkItem(s, makePayload("open"))
	if ok {
		t.Error("Expected open issue to be filtered by 'closed' state filter")
	}

	// "all" state should match everything
	s = &GitHubWebhookSource{State: "all"}
	_, ok = webhookPayloadToWorkItem(s, makePayload("open"))
	if !ok {
		t.Error("Expected open issue to match 'all' state filter")
	}
	_, ok = webhookPayloadToWorkItem(s, makePayload("closed"))
	if !ok {
		t.Error("Expected closed issue to match 'all' state filter")
	}
}

func TestGitHubWebhookSource_ActionsFiltering(t *testing.T) {
	makePayload := func(action string) GitHubWebhookPayload {
		raw := []byte(`{"action":"` + action + `","issue":{"number":1,"title":"T","state":"open","html_url":"https://github.com/t/r/issues/1"}}`)
		var p GitHubWebhookPayload
		_ = json.Unmarshal(raw, &p)
		return p
	}

	// No actions filter - all actions match
	s := &GitHubWebhookSource{}
	_, ok := webhookPayloadToWorkItem(s, makePayload("labeled"))
	if !ok {
		t.Error("Expected item to match with no actions filter")
	}

	// With actions filter
	s = &GitHubWebhookSource{Actions: []string{"opened", "reopened"}}
	_, ok = webhookPayloadToWorkItem(s, makePayload("opened"))
	if !ok {
		t.Error("Expected 'opened' action to match filter")
	}
	_, ok = webhookPayloadToWorkItem(s, makePayload("labeled"))
	if ok {
		t.Error("Expected 'labeled' action to be filtered out")
	}
}

func TestGitHubWebhookSource_DraftFiltering(t *testing.T) {
	makePayload := func(draft bool) GitHubWebhookPayload {
		draftStr := "false"
		if draft {
			draftStr = "true"
		}
		raw := []byte(`{"action":"opened","pull_request":{"number":1,"title":"T","state":"open","draft":` + draftStr + `,"html_url":"https://github.com/t/r/pull/1","head":{"ref":"b"}}}`)
		var p GitHubWebhookPayload
		_ = json.Unmarshal(raw, &p)
		return p
	}

	boolPtr := func(b bool) *bool { return &b }

	// No draft filter - both match
	s := &GitHubWebhookSource{}
	_, ok := webhookPayloadToWorkItem(s, makePayload(true))
	if !ok {
		t.Error("Expected draft PR to match with no draft filter")
	}
	_, ok = webhookPayloadToWorkItem(s, makePayload(false))
	if !ok {
		t.Error("Expected non-draft PR to match with no draft filter")
	}

	// Draft=true filter
	s = &GitHubWebhookSource{Draft: boolPtr(true)}
	_, ok = webhookPayloadToWorkItem(s, makePayload(true))
	if !ok {
		t.Error("Expected draft PR to match draft=true filter")
	}
	_, ok = webhookPayloadToWorkItem(s, makePayload(false))
	if ok {
		t.Error("Expected non-draft PR to be filtered by draft=true filter")
	}

	// Draft=false filter
	s = &GitHubWebhookSource{Draft: boolPtr(false)}
	_, ok = webhookPayloadToWorkItem(s, makePayload(false))
	if !ok {
		t.Error("Expected non-draft PR to match draft=false filter")
	}
	_, ok = webhookPayloadToWorkItem(s, makePayload(true))
	if ok {
		t.Error("Expected draft PR to be filtered by draft=false filter")
	}
}

func TestGitHubWebhookSource_TriggerComment(t *testing.T) {
	// issue_comment event with matching trigger
	commentPayload := []byte(`{
		"action": "created",
		"comment": {"body": "/kelos-run", "user": {"login": "admin"}},
		"issue": {
			"number": 42,
			"title": "Test Issue",
			"body": "Some body",
			"html_url": "https://github.com/test/repo/issues/42",
			"state": "open",
			"labels": [{"name": "bug"}],
			"user": {"login": "author"}
		}
	}`)

	var parsed GitHubWebhookPayload
	if err := json.Unmarshal(commentPayload, &parsed); err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	// With trigger comment set, matching comment should be accepted
	s := &GitHubWebhookSource{TriggerComment: "/kelos-run"}
	item, ok := webhookPayloadToWorkItem(s, parsed)
	if !ok {
		t.Fatal("Expected comment with trigger to produce a work item")
	}
	if item.Number != 42 {
		t.Errorf("Expected number 42, got %d", item.Number)
	}
	if item.Kind != "Issue" {
		t.Errorf("Expected kind 'Issue', got %s", item.Kind)
	}

	// Non-matching comment should be rejected
	nonMatchPayload := []byte(`{
		"action": "created",
		"comment": {"body": "just a normal comment", "user": {"login": "admin"}},
		"issue": {
			"number": 42,
			"title": "Test Issue",
			"state": "open",
			"html_url": "https://github.com/test/repo/issues/42",
			"user": {"login": "author"}
		}
	}`)
	var nonMatch GitHubWebhookPayload
	_ = json.Unmarshal(nonMatchPayload, &nonMatch)
	_, ok = webhookPayloadToWorkItem(s, nonMatch)
	if ok {
		t.Error("Expected non-matching comment to be rejected")
	}

	// Issue event without trigger in body should be rejected
	issuePayload := []byte(`{
		"action": "opened",
		"issue": {
			"number": 42,
			"title": "Test Issue",
			"body": "No trigger here",
			"state": "open",
			"html_url": "https://github.com/test/repo/issues/42",
			"user": {"login": "author"}
		}
	}`)
	var issueParsed GitHubWebhookPayload
	_ = json.Unmarshal(issuePayload, &issueParsed)
	_, ok = webhookPayloadToWorkItem(s, issueParsed)
	if ok {
		t.Error("Expected issue without trigger in body to be rejected")
	}

	// Issue event WITH trigger in body should be accepted
	issueWithTrigger := []byte(`{
		"action": "opened",
		"issue": {
			"number": 42,
			"title": "Test Issue",
			"body": "/kelos-run",
			"state": "open",
			"html_url": "https://github.com/test/repo/issues/42",
			"user": {"login": "author"}
		}
	}`)
	var issueTrigger GitHubWebhookPayload
	_ = json.Unmarshal(issueWithTrigger, &issueTrigger)
	_, ok = webhookPayloadToWorkItem(s, issueTrigger)
	if !ok {
		t.Error("Expected issue with trigger in body to be accepted")
	}
}

func TestGitHubWebhookSource_ExcludeComments(t *testing.T) {
	commentPayload := []byte(`{
		"action": "created",
		"comment": {"body": "/kelos-stop", "user": {"login": "admin"}},
		"issue": {
			"number": 42,
			"title": "Test Issue",
			"state": "open",
			"html_url": "https://github.com/test/repo/issues/42",
			"user": {"login": "author"}
		}
	}`)

	var parsed GitHubWebhookPayload
	_ = json.Unmarshal(commentPayload, &parsed)

	s := &GitHubWebhookSource{ExcludeComments: []string{"/kelos-stop"}}
	_, ok := webhookPayloadToWorkItem(s, parsed)
	if ok {
		t.Error("Expected exclude comment to reject the event")
	}
}

func TestGitHubWebhookSource_AllowedUsers(t *testing.T) {
	commentPayload := []byte(`{
		"action": "created",
		"comment": {"body": "/kelos-run", "user": {"login": "trusted"}},
		"issue": {
			"number": 42,
			"title": "Test Issue",
			"state": "open",
			"html_url": "https://github.com/test/repo/issues/42",
			"user": {"login": "author"}
		}
	}`)

	var parsed GitHubWebhookPayload
	_ = json.Unmarshal(commentPayload, &parsed)

	// Allowed user should be accepted
	s := &GitHubWebhookSource{
		TriggerComment: "/kelos-run",
		AllowedUsers:   []string{"trusted"},
	}
	_, ok := webhookPayloadToWorkItem(s, parsed)
	if !ok {
		t.Error("Expected allowed user to be accepted")
	}

	// Non-allowed user should be rejected
	s = &GitHubWebhookSource{
		TriggerComment: "/kelos-run",
		AllowedUsers:   []string{"other-user"},
	}
	_, ok = webhookPayloadToWorkItem(s, parsed)
	if ok {
		t.Error("Expected non-allowed user to be rejected")
	}
}

func TestGitHubWebhookSource_CommentOnPR(t *testing.T) {
	// issue_comment event on a pull request (has pull_request field on issue)
	commentPayload := []byte(`{
		"action": "created",
		"comment": {"body": "/kelos-run", "user": {"login": "admin"}},
		"issue": {
			"number": 99,
			"title": "Test PR",
			"body": "PR body",
			"html_url": "https://github.com/test/repo/pull/99",
			"state": "open",
			"labels": [{"name": "enhancement"}],
			"user": {"login": "pr-author"},
			"pull_request": {}
		}
	}`)

	var parsed GitHubWebhookPayload
	_ = json.Unmarshal(commentPayload, &parsed)

	s := &GitHubWebhookSource{TriggerComment: "/kelos-run"}
	item, ok := webhookPayloadToWorkItem(s, parsed)
	if !ok {
		t.Fatal("Expected comment on PR to produce a work item")
	}
	if item.Kind != "PR" {
		t.Errorf("Expected kind 'PR', got %s", item.Kind)
	}
	if item.ID != "pr-99" {
		t.Errorf("Expected ID 'pr-99', got %s", item.ID)
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

	src := &GitHubWebhookSource{
		Client:      fakeClient,
		Namespace:   "default",
		SpawnerName: "spawner-a",
	}

	items, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// Should only get event1 (event2 has closed issue)
	if len(items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(items))
	}

	if len(items) > 0 && items[0].Number != 100 {
		t.Errorf("Expected issue 100, got %d", items[0].Number)
	}

	// Matching event should NOT be marked as processed yet (deferred acknowledgment)
	var updatedEvent kelosv1alpha1.WebhookEvent
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "event-1",
		Namespace: "default",
	}, &updatedEvent); err != nil {
		t.Fatalf("Failed to get updated event: %v", err)
	}

	if len(updatedEvent.Status.ProcessedBy) != 0 {
		t.Errorf("Expected event-1 to NOT be processed yet, got ProcessedBy: %v", updatedEvent.Status.ProcessedBy)
	}

	// Filtered event (event-2, closed) should be marked as processed immediately
	var filteredEvent kelosv1alpha1.WebhookEvent
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "event-2",
		Namespace: "default",
	}, &filteredEvent); err != nil {
		t.Fatalf("Failed to get filtered event: %v", err)
	}

	if !filteredEvent.Status.Processed {
		t.Error("Expected filtered event-2 to be marked as processed")
	}

	// Acknowledge the matching item
	src.AcknowledgeItems(context.Background(), []string{items[0].ID})

	// Now event-1 should be marked as processed
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "event-1",
		Namespace: "default",
	}, &updatedEvent); err != nil {
		t.Fatalf("Failed to get updated event after acknowledge: %v", err)
	}

	if !updatedEvent.Status.Processed {
		t.Error("Expected event-1 to be marked as processed after acknowledge")
	}
	if len(updatedEvent.Status.ProcessedBy) != 1 || updatedEvent.Status.ProcessedBy[0] != "spawner-a" {
		t.Errorf("Expected ProcessedBy to contain 'spawner-a', got %v", updatedEvent.Status.ProcessedBy)
	}

	// A second spawner should still see event1 (it was only acknowledged by spawner-a)
	src2 := &GitHubWebhookSource{
		Client:      fakeClient,
		Namespace:   "default",
		SpawnerName: "spawner-b",
	}

	items2, err := src2.Discover(context.Background())
	if err != nil {
		t.Fatalf("Second Discover failed: %v", err)
	}

	if len(items2) != 1 {
		t.Errorf("Expected spawner-b to discover 1 item, got %d", len(items2))
	}

	// Acknowledge spawner-b's items
	src2.AcknowledgeItems(context.Background(), []string{items2[0].ID})

	// Same spawner should not see it again
	items3, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Third Discover failed: %v", err)
	}

	if len(items3) != 0 {
		t.Errorf("Expected spawner-a to discover 0 items on re-run, got %d", len(items3))
	}
}

func TestGitHubWebhookSource_DeferredAcknowledgment(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kelosv1alpha1.AddToScheme(scheme)

	// Create two matching events for different PRs
	event1 := &kelosv1alpha1.WebhookEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "event-pr-1",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WebhookEventSpec{
			Source: "github",
			Payload: []byte(`{
				"action": "opened",
				"pull_request": {
					"number": 100,
					"title": "PR 100",
					"body": "Body",
					"html_url": "https://github.com/test/repo/pull/100",
					"state": "open",
					"draft": false,
					"labels": [],
					"user": {"login": "renovate[bot]"},
					"head": {"ref": "renovate/dep-1"}
				}
			}`),
			ReceivedAt: metav1.Now(),
		},
	}
	event2 := &kelosv1alpha1.WebhookEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "event-pr-2",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WebhookEventSpec{
			Source: "github",
			Payload: []byte(`{
				"action": "opened",
				"pull_request": {
					"number": 200,
					"title": "PR 200",
					"body": "Body",
					"html_url": "https://github.com/test/repo/pull/200",
					"state": "open",
					"draft": false,
					"labels": [],
					"user": {"login": "renovate[bot]"},
					"head": {"ref": "renovate/dep-2"}
				}
			}`),
			ReceivedAt: metav1.Now(),
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(event1, event2).
		WithStatusSubresource(&kelosv1alpha1.WebhookEvent{}).
		Build()

	src := &GitHubWebhookSource{
		Client:      fakeClient,
		Namespace:   "default",
		SpawnerName: "dep-review",
		Author:      "renovate[bot]",
	}

	items, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(items))
	}

	// Simulate maxConcurrency=1: only acknowledge the first item
	src.AcknowledgeItems(context.Background(), []string{items[0].ID})

	// Verify: first event is processed, second is NOT
	var ev1 kelosv1alpha1.WebhookEvent
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name: "event-pr-1", Namespace: "default",
	}, &ev1); err != nil {
		t.Fatalf("Failed to get event-pr-1: %v", err)
	}
	if !ev1.Status.Processed {
		t.Error("Expected event-pr-1 to be processed after acknowledge")
	}

	var ev2 kelosv1alpha1.WebhookEvent
	if err := fakeClient.Get(context.Background(), client.ObjectKey{
		Name: "event-pr-2", Namespace: "default",
	}, &ev2); err != nil {
		t.Fatalf("Failed to get event-pr-2: %v", err)
	}
	if ev2.Status.Processed {
		t.Error("Expected event-pr-2 to NOT be processed (skipped by maxConcurrency)")
	}

	// On the next cycle, the unacknowledged event should be rediscovered
	items2, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Second Discover failed: %v", err)
	}

	if len(items2) != 1 {
		t.Fatalf("Expected 1 item on second discover, got %d", len(items2))
	}
	if items2[0].Number != 200 {
		t.Errorf("Expected rediscovered item to be PR 200, got %d", items2[0].Number)
	}
}
