package source

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// GitHubWebhookSource discovers work items from GitHub webhook events stored
// as WebhookEvent custom resources. This replaces polling the GitHub API with
// push-based webhook notifications.
type GitHubWebhookSource struct {
	Client    client.Client
	Namespace string

	// Labels filters issues/PRs by labels (applied client-side to webhook payloads)
	Labels []string
	// ExcludeLabels filters out items with these labels (applied client-side)
	ExcludeLabels []string
}

// GitHubWebhookPayload represents the relevant fields from a GitHub webhook payload.
// This handles both issue and pull_request events.
type GitHubWebhookPayload struct {
	Action string `json:"action"` // "opened", "reopened", "labeled", etc.
	Issue  *struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"` // "open" or "closed"
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue,omitempty"`
	PullRequest *struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"` // "open" or "closed"
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Head struct {
			Ref string `json:"ref"` // branch name
		} `json:"head"`
	} `json:"pull_request,omitempty"`
}

// Discover fetches unprocessed GitHub webhook events and converts them to WorkItems.
func (s *GitHubWebhookSource) Discover(ctx context.Context) ([]WorkItem, error) {
	var eventList kelosv1alpha1.WebhookEventList

	// List all webhook events in namespace
	// Field selectors are not supported by fake clients in tests, so filter client-side
	if err := s.Client.List(ctx, &eventList,
		client.InNamespace(s.Namespace),
	); err != nil {
		return nil, fmt.Errorf("listing webhook events: %w", err)
	}

	var items []WorkItem

	for i := range eventList.Items {
		event := eventList.Items[i].DeepCopy()

		// Filter by source and processed status client-side
		if event.Spec.Source != "github" || event.Status.Processed {
			continue
		}

		// Parse webhook payload
		var payload GitHubWebhookPayload
		if err := json.Unmarshal(event.Spec.Payload, &payload); err != nil {
			// Skip malformed payloads
			continue
		}

		// Convert to WorkItem
		item, ok := s.payloadToWorkItem(payload)
		if !ok {
			// Mark event as processed even if payload couldn't be converted
			event.Status.Processed = true
			now := metav1.Now()
			event.Status.ProcessedAt = &now
			_ = s.Client.Status().Update(ctx, event)
			continue
		}

		// Apply label filters
		if !s.matchesLabels(item.Labels) {
			// Mark event as processed even if it was filtered out
			event.Status.Processed = true
			now := metav1.Now()
			event.Status.ProcessedAt = &now
			_ = s.Client.Status().Update(ctx, event)
			continue
		}

		items = append(items, item)

		// Mark event as processed
		event.Status.Processed = true
		now := metav1.Now()
		event.Status.ProcessedAt = &now
		if err := s.Client.Status().Update(ctx, event); err != nil {
			// Log but continue with other events
			continue
		}
	}

	return items, nil
}

// payloadToWorkItem converts a GitHub webhook payload to a WorkItem.
// Returns false if the payload should be skipped.
func (s *GitHubWebhookSource) payloadToWorkItem(payload GitHubWebhookPayload) (WorkItem, bool) {
	// Handle issue webhooks
	if payload.Issue != nil {
		issue := payload.Issue

		// Only process open issues
		if issue.State != "open" {
			return WorkItem{}, false
		}

		labels := make([]string, len(issue.Labels))
		for i, l := range issue.Labels {
			labels[i] = l.Name
		}

		return WorkItem{
			ID:     fmt.Sprintf("issue-%d", issue.Number),
			Number: issue.Number,
			Title:  issue.Title,
			Body:   issue.Body,
			URL:    issue.HTMLURL,
			Labels: labels,
			Kind:   "Issue",
		}, true
	}

	// Handle pull request webhooks
	if payload.PullRequest != nil {
		pr := payload.PullRequest

		// Only process open PRs
		if pr.State != "open" {
			return WorkItem{}, false
		}

		labels := make([]string, len(pr.Labels))
		for i, l := range pr.Labels {
			labels[i] = l.Name
		}

		return WorkItem{
			ID:     fmt.Sprintf("pr-%d", pr.Number),
			Number: pr.Number,
			Title:  pr.Title,
			Body:   pr.Body,
			URL:    pr.HTMLURL,
			Labels: labels,
			Kind:   "PR",
			Branch: pr.Head.Ref,
		}, true
	}

	return WorkItem{}, false
}

// matchesLabels returns true if the item matches the configured label filters.
func (s *GitHubWebhookSource) matchesLabels(itemLabels []string) bool {
	// Check required labels (if configured)
	if len(s.Labels) > 0 {
		hasAllRequired := true
		for _, required := range s.Labels {
			found := false
			for _, label := range itemLabels {
				if label == required {
					found = true
					break
				}
			}
			if !found {
				hasAllRequired = false
				break
			}
		}
		if !hasAllRequired {
			return false
		}
	}

	// Check excluded labels
	for _, excluded := range s.ExcludeLabels {
		for _, label := range itemLabels {
			if label == excluded {
				return false
			}
		}
	}

	return true
}
