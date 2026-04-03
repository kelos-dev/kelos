package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// LinearEventData represents parsed Linear webhook data.
// Fields are populated manually by ParseLinearWebhook from the nested payload
// structure — they do not map 1:1 to the wire format so no JSON tags are used.
type LinearEventData struct {
	ID      string
	Title   string
	Type    string
	Action  string
	Payload map[string]interface{}
	Labels  []string
	State   string
}

// ParseLinearWebhook parses a Linear webhook payload.
func ParseLinearWebhook(payload []byte) (*LinearEventData, error) {
	var rawPayload map[string]interface{}
	if err := json.Unmarshal(payload, &rawPayload); err != nil {
		return nil, fmt.Errorf("invalid JSON payload: %w", err)
	}

	eventData := &LinearEventData{
		Payload: rawPayload,
	}

	// Extract type from the payload
	if typeVal, ok := rawPayload["type"].(string); ok {
		eventData.Type = typeVal
	}

	// Extract action from the payload
	if actionVal, ok := rawPayload["action"].(string); ok {
		eventData.Action = actionVal
	}

	// Extract data from nested structure
	if data, ok := rawPayload["data"].(map[string]interface{}); ok {
		// Extract ID
		if id, ok := data["id"].(string); ok {
			eventData.ID = id
		}

		// Extract title
		if title, ok := data["title"].(string); ok {
			eventData.Title = title
		}

		// Extract state
		if state, ok := data["state"].(map[string]interface{}); ok {
			if stateName, ok := state["name"].(string); ok {
				eventData.State = stateName
			}
		}

		// Extract labels
		if labels, ok := data["labels"].([]interface{}); ok {
			for _, label := range labels {
				if labelMap, ok := label.(map[string]interface{}); ok {
					if labelName, ok := labelMap["name"].(string); ok {
						eventData.Labels = append(eventData.Labels, labelName)
					}
				}
			}
		}
	}

	return eventData, nil
}

// MatchesLinearEvent checks if a Linear webhook event matches the given configuration.
func MatchesLinearEvent(config *v1alpha1.LinearWebhook, eventData *LinearEventData) (bool, error) {
	// Check if event type is in the allowed list
	typeMatched := false
	for _, allowedType := range config.Types {
		if strings.EqualFold(eventData.Type, allowedType) {
			typeMatched = true
			break
		}
	}
	if !typeMatched {
		return false, nil
	}

	// If no filters specified, match all events of allowed types
	if len(config.Filters) == 0 {
		return true, nil
	}

	// Check filters (OR semantics - any matching filter passes)
	for _, filter := range config.Filters {
		// Skip filters scoped to a different type
		if filter.Type != "" && !strings.EqualFold(filter.Type, eventData.Type) {
			continue
		}
		if matchesLinearFilter(&filter, eventData) {
			return true, nil
		}
	}

	return false, nil
}

// extractLabels extracts labels from a data object, checking both data.labels
// and data.issue.labels (for Comment webhooks).
// Returns nil if labels are missing or empty in both locations.
func extractLabels(dataObj map[string]interface{}) []interface{} {
	// Try data.labels first (Issue events) — only if non-empty
	if labels, ok := dataObj["labels"].([]interface{}); ok && labels != nil && len(labels) > 0 {
		return labels
	}

	// Fall back to data.issue.labels (Comment and IssueLabel events) — only if non-empty
	if issue, ok := dataObj["issue"].(map[string]interface{}); ok {
		if labels, ok := issue["labels"].([]interface{}); ok && labels != nil && len(labels) > 0 {
			return labels
		}
	}

	return nil
}

// matchesLinearFilter checks if event data matches a specific filter.
// Type filtering is already handled by MatchesLinearEvent before this is called.
func matchesLinearFilter(filter *v1alpha1.LinearWebhookFilter, eventData *LinearEventData) bool {
	// Check action filter
	if filter.Action != "" && !strings.EqualFold(filter.Action, eventData.Action) {
		return false
	}

	// Check state filter
	if len(filter.States) > 0 {
		stateMatched := false
		for _, allowedState := range filter.States {
			if strings.EqualFold(allowedState, eventData.State) {
				stateMatched = true
				break
			}
		}
		if !stateMatched {
			return false
		}
	}

	// Check label filters (required labels use AND semantics, excluded labels use OR semantics).
	// Uses extractLabels to check both data.labels and data.issue.labels (for Comments).
	if len(filter.Labels) > 0 || len(filter.ExcludeLabels) > 0 {
		dataObj, _ := eventData.Payload["data"].(map[string]interface{})
		if dataObj == nil {
			// No data object — required labels can't match, excluded labels vacuously pass
			return len(filter.Labels) == 0
		}
		labels := extractLabels(dataObj)
		if labels == nil {
			return len(filter.Labels) == 0
		}

		// Build set of present label names (lowercased for case-insensitive comparison)
		presentLabels := make(map[string]bool, len(labels))
		for _, label := range labels {
			if labelObj, ok := label.(map[string]interface{}); ok {
				if labelName, ok := labelObj["name"].(string); ok {
					presentLabels[strings.ToLower(labelName)] = true
				}
			}
		}

		// Required labels: must have all
		for _, requiredLabel := range filter.Labels {
			if !presentLabels[strings.ToLower(requiredLabel)] {
				return false
			}
		}

		// Excluded labels: any match fails
		for _, excludeLabel := range filter.ExcludeLabels {
			if presentLabels[strings.ToLower(excludeLabel)] {
				return false
			}
		}
	}

	return true
}

// linearDetailsFetcher is the function used to fetch issue details (labels and
// description) from the Linear API. It is a package-level variable so tests
// can swap in a stub.
var linearDetailsFetcher = fetchLinearIssueDetails

// enrichLinearCommentIssue fetches labels and description from the Linear API
// for Comment events and injects them into the parsed payload so that
// downstream label filtering and template rendering work correctly. This is
// called unconditionally for all Comment events — it is cheap (single GraphQL
// call) and prevents template crashes when prompts reference fields like
// {{.Payload.data.issue.description}}.
func enrichLinearCommentIssue(ctx context.Context, log logr.Logger, eventData *LinearEventData) {
	if eventData.Type != "Comment" {
		return
	}

	dataObj, _ := eventData.Payload["data"].(map[string]interface{})
	if dataObj == nil {
		return
	}

	// Extract the parent issue ID from data.issue.id
	issue, _ := dataObj["issue"].(map[string]interface{})
	if issue == nil {
		log.Info("Comment webhook has no issue object, cannot enrich")
		return
	}

	var issueID string
	switch id := issue["id"].(type) {
	case string:
		issueID = id
	case float64:
		issueID = fmt.Sprintf("%.0f", id)
	}
	if issueID == "" {
		log.Info("Comment webhook has no issue ID, cannot enrich")
		return
	}

	details, err := linearDetailsFetcher(ctx, issueID)
	if err != nil {
		log.Error(err, "Failed to fetch Linear issue details", "issueID", issueID)
		return
	}
	if details == nil {
		// LINEAR_API_KEY not set — enrichment cannot proceed.
		log.Info("LINEAR_API_KEY not set, cannot enrich Comment event with issue details from Linear API")
		return
	}

	log.Info("Enriched Comment event with issue details from Linear API", "issueID", issueID, "labels", details.Labels)

	// Inject labels into data.issue.labels as []interface{} matching the
	// format that extractLabels/matchesLinearFilter expect.
	labelObjs := make([]interface{}, len(details.Labels))
	for i, name := range details.Labels {
		labelObjs[i] = map[string]interface{}{"name": name}
	}
	issue["labels"] = labelObjs

	// Also update the convenience field on LinearEventData
	eventData.Labels = details.Labels

	// Inject description only if not already present in the payload
	if details.Description != nil {
		if _, exists := issue["description"]; !exists {
			issue["description"] = *details.Description
		}
	}
}

// ExtractLinearWorkItem converts Linear webhook data to template variables.
func ExtractLinearWorkItem(eventData *LinearEventData) map[string]interface{} {
	vars := map[string]interface{}{
		"ID":      eventData.ID,
		"Title":   eventData.Title,
		"Kind":    "LinearWebhook",
		"Type":    eventData.Type,
		"Action":  eventData.Action,
		"State":   eventData.State,
		"Labels":  strings.Join(eventData.Labels, ", "),
		"IssueID": "", // populated below for Comment events
		"Payload": eventData.Payload,
	}

	// For Comment events, extract the parent issue ID
	if eventData.Type == "Comment" {
		if dataObj, ok := eventData.Payload["data"].(map[string]interface{}); ok {
			if issue, ok := dataObj["issue"].(map[string]interface{}); ok {
				if issueID, ok := issue["id"].(string); ok {
					vars["IssueID"] = issueID
				} else if issueID, ok := issue["id"].(float64); ok {
					vars["IssueID"] = fmt.Sprintf("%.0f", issueID)
				}
			}
		}
	}

	return vars
}
