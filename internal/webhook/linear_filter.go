package webhook

import (
	"encoding/json"
	"fmt"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// LinearEventData holds parsed Linear event information for template rendering.
type LinearEventData struct {
	// Type (e.g., "Issue", "Comment", "Project")
	Type string
	// Action (e.g., "create", "update", "remove")
	Action string
	// Raw parsed event payload for template access
	RawPayload map[string]interface{}
	// Standard template variables for compatibility
	ID    string
	Title string
}

// ParseLinearWebhook parses a Linear webhook payload using manual JSON parsing.
func ParseLinearWebhook(payload []byte) (*LinearEventData, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse Linear webhook JSON: %w", err)
	}

	data := &LinearEventData{
		RawPayload: raw,
	}

	// Extract type from payload
	if typ, ok := raw["type"].(string); ok {
		data.Type = typ
	}

	// Extract action from payload
	if action, ok := raw["action"].(string); ok {
		data.Action = action
	}

	// Extract data object for further processing
	var dataObj map[string]interface{}
	if d, ok := raw["data"].(map[string]interface{}); ok {
		dataObj = d
	}

	// Extract common fields based on type
	if dataObj != nil {
		// Extract ID (could be string or number)
		if id, ok := dataObj["id"].(string); ok {
			data.ID = id
		} else if id, ok := dataObj["id"].(float64); ok {
			data.ID = fmt.Sprintf("%.0f", id)
		}

		// Extract title
		if title, ok := dataObj["title"].(string); ok {
			data.Title = title
		}
	}

	return data, nil
}

// MatchesLinearEvent evaluates whether a Linear webhook event matches the spawner's filters.
func MatchesLinearEvent(spawner *v1alpha1.LinearWebhook, payload []byte) (bool, error) {
	// Parse the event for filtering
	eventData, err := ParseLinearWebhook(payload)
	if err != nil {
		return false, fmt.Errorf("failed to parse Linear webhook: %w", err)
	}

	// Check if event type is in the allowed list
	typeAllowed := false
	for _, allowedType := range spawner.Types {
		if allowedType == eventData.Type {
			typeAllowed = true
			break
		}
	}
	if !typeAllowed {
		return false, nil
	}

	// If no filters, all events of the allowed types match
	if len(spawner.Filters) == 0 {
		return true, nil
	}

	// Apply filters with OR semantics for the same event type
	for _, filter := range spawner.Filters {
		if filter.Type != eventData.Type {
			continue
		}

		if matchesLinearFilter(filter, eventData) {
			return true, nil
		}
	}

	return false, nil
}

// matchesLinearFilter checks if event data matches a specific Linear filter.
func matchesLinearFilter(filter v1alpha1.LinearWebhookFilter, eventData *LinearEventData) bool {
	// Action filter
	if filter.Action != "" && filter.Action != eventData.Action {
		return false
	}

	// Get data object for further filtering
	var dataObj map[string]interface{}
	if d, ok := eventData.RawPayload["data"].(map[string]interface{}); ok {
		dataObj = d
	}

	if dataObj == nil {
		// If no data object and we have state/label filters, this doesn't match
		if len(filter.States) > 0 || len(filter.Labels) > 0 || len(filter.ExcludeLabels) > 0 {
			return false
		}
		// Otherwise, it matches (only action filter matters)
		return true
	}

	// State filter
	if len(filter.States) > 0 {
		if state, ok := dataObj["state"].(map[string]interface{}); ok {
			if stateName, ok := state["name"].(string); ok {
				stateMatches := false
				for _, allowedState := range filter.States {
					if allowedState == stateName {
						stateMatches = true
						break
					}
				}
				if !stateMatches {
					return false
				}
			} else {
				// No state name found, but state filter required
				return false
			}
		} else {
			// No state object found, but state filter required
			return false
		}
	}

	// Labels filter (all required labels must be present)
	if len(filter.Labels) > 0 {
		labels, ok := dataObj["labels"].([]interface{})
		if !ok || labels == nil {
			// No labels found, but labels filter required
			return false
		}

		// Build set of present label names
		presentLabels := make(map[string]bool)
		for _, label := range labels {
			if labelObj, ok := label.(map[string]interface{}); ok {
				if labelName, ok := labelObj["name"].(string); ok {
					presentLabels[labelName] = true
				}
			}
		}

		// Check all required labels are present
		for _, requiredLabel := range filter.Labels {
			if !presentLabels[requiredLabel] {
				return false
			}
		}
	}

	// ExcludeLabels filter (issue must NOT have any of these labels)
	if len(filter.ExcludeLabels) > 0 {
		labels, ok := dataObj["labels"].([]interface{})
		if ok && labels != nil {
			// Build set of present label names
			presentLabels := make(map[string]bool)
			for _, label := range labels {
				if labelObj, ok := label.(map[string]interface{}); ok {
					if labelName, ok := labelObj["name"].(string); ok {
						presentLabels[labelName] = true
					}
				}
			}

			// Check that none of the excluded labels are present
			for _, excludeLabel := range filter.ExcludeLabels {
				if presentLabels[excludeLabel] {
					return false
				}
			}
		}
	}

	return true
}

// ExtractLinearWorkItem extracts template variables from Linear webhook events for task creation.
func ExtractLinearWorkItem(eventData *LinearEventData) map[string]interface{} {
	vars := map[string]interface{}{
		"Type":    eventData.Type,
		"Action":  eventData.Action,
		"Payload": eventData.RawPayload,
		// Standard variables for compatibility
		"ID":    eventData.ID,
		"Title": eventData.Title,
		"Kind":  "webhook",
	}

	return vars
}
