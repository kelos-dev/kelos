package source

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// LinearWebhookSource discovers work items from Linear webhook events stored
// as WebhookEvent custom resources.
type LinearWebhookSource struct {
	Client    client.Client
	Namespace string

	// States filters issues by workflow state names (e.g., ["Todo", "In Progress"])
	// When empty, all non-terminal states are processed (excludes "Done", "Canceled")
	States []string
	// Labels filters issues by labels (applied client-side to webhook payloads)
	Labels []string
	// ExcludeLabels filters out items with these labels (applied client-side)
	ExcludeLabels []string
}

// LinearWebhookPayload represents the relevant fields from a Linear webhook payload.
type LinearWebhookPayload struct {
	Type   string `json:"type"`   // "Issue" or "Comment"
	Action string `json:"action"` // "create", "update", "remove"
	Data   struct {
		ID          string `json:"id"`
		Identifier  string `json:"identifier"` // "TEAM-123" format
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Description string `json:"description"`
		URL         string `json:"url"`
		State       struct {
			Name string `json:"name"` // "Todo", "In Progress", "Done", etc.
			Type string `json:"type"` // "triage", "backlog", "unstarted", "started", "completed", "canceled"
		} `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Team struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"team"`
	} `json:"data"`
}

// Discover fetches unprocessed Linear webhook events and converts them to WorkItems.
func (s *LinearWebhookSource) Discover(ctx context.Context) ([]WorkItem, error) {
	var eventList kelosv1alpha1.WebhookEventList

	// List all webhook events in namespace
	if err := s.Client.List(ctx, &eventList,
		client.InNamespace(s.Namespace),
	); err != nil {
		return nil, fmt.Errorf("listing webhook events: %w", err)
	}

	var items []WorkItem

	for i := range eventList.Items {
		event := eventList.Items[i].DeepCopy()

		// Filter by source and processed status client-side
		if event.Spec.Source != "linear" || event.Status.Processed {
			continue
		}

		// Parse webhook payload
		var payload LinearWebhookPayload
		if err := json.Unmarshal(event.Spec.Payload, &payload); err != nil {
			// Mark event as processed even if payload is malformed
			s.markProcessed(ctx, event)
			continue
		}

		// Convert to WorkItem
		item, ok := s.payloadToWorkItem(payload)
		if !ok {
			// Mark event as processed even if it can't be converted
			s.markProcessed(ctx, event)
			continue
		}

		// Apply filters
		if !s.matchesFilters(item, payload) {
			// Mark event as processed even if filtered out
			s.markProcessed(ctx, event)
			continue
		}

		items = append(items, item)

		// Mark event as processed
		s.markProcessed(ctx, event)
	}

	return items, nil
}

// payloadToWorkItem converts a Linear webhook payload to a WorkItem.
// Returns false if the payload should be skipped.
func (s *LinearWebhookSource) payloadToWorkItem(payload LinearWebhookPayload) (WorkItem, bool) {
	// Only process Issue events
	if payload.Type != "Issue" {
		return WorkItem{}, false
	}

	// Only process create and update actions
	if payload.Action != "create" && payload.Action != "update" {
		return WorkItem{}, false
	}

	// Skip if no data
	if payload.Data.Identifier == "" {
		return WorkItem{}, false
	}

	// Extract labels
	labels := make([]string, len(payload.Data.Labels))
	for i, l := range payload.Data.Labels {
		labels[i] = l.Name
	}

	return WorkItem{
		ID:     payload.Data.Identifier, // e.g., "ENG-42"
		Number: payload.Data.Number,
		Title:  payload.Data.Title,
		Body:   payload.Data.Description,
		URL:    payload.Data.URL,
		Labels: labels,
		Kind:   payload.Data.State.Name, // e.g., "Todo", "In Progress"
	}, true
}

// matchesFilters returns true if the item matches the configured filters.
func (s *LinearWebhookSource) matchesFilters(item WorkItem, payload LinearWebhookPayload) bool {
	// Check state filter
	if !s.matchesState(payload.Data.State) {
		return false
	}

	// Check required labels
	if len(s.Labels) > 0 {
		hasAllRequired := true
		for _, required := range s.Labels {
			found := false
			for _, label := range item.Labels {
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
		for _, label := range item.Labels {
			if label == excluded {
				return false
			}
		}
	}

	return true
}

// matchesState returns true if the issue state matches the configured state filter.
func (s *LinearWebhookSource) matchesState(state struct {
	Name string `json:"name"`
	Type string `json:"type"`
}) bool {
	// If no states configured, exclude terminal states by default
	if len(s.States) == 0 {
		// Terminal states: completed, canceled
		return state.Type != "completed" && state.Type != "canceled"
	}

	// Check if state name is in the configured list
	for _, allowed := range s.States {
		if state.Name == allowed {
			return true
		}
	}

	return false
}

// markProcessed marks an event as processed.
func (s *LinearWebhookSource) markProcessed(ctx context.Context, event *kelosv1alpha1.WebhookEvent) {
	event.Status.Processed = true
	now := metav1.Now()
	event.Status.ProcessedAt = &now
	_ = s.Client.Status().Update(ctx, event)
}
