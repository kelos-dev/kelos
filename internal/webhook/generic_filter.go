package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/PaesslerAG/jsonpath"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// regexpCache caches compiled regular expressions keyed by pattern string.
// Filter patterns come from TaskSpawner specs and are reused across webhook
// deliveries, so compiling once avoids per-request overhead.
var regexpCache sync.Map

// getOrCompileRegexp returns a cached compiled regexp or compiles and caches it.
func getOrCompileRegexp(pattern string) (*regexp.Regexp, error) {
	if cached, ok := regexpCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexpCache.Store(pattern, re)
	return re, nil
}

// GenericEventData represents parsed generic webhook data.
type GenericEventData struct {
	// Fields extracted via fieldMapping JSONPath expressions.
	Fields map[string]string
	// Raw payload for {{.Payload}} template access.
	Payload interface{}
}

// ParseGenericWebhook parses an arbitrary JSON webhook payload.
func ParseGenericWebhook(payload []byte) (*GenericEventData, error) {
	var rawPayload interface{}
	if err := json.Unmarshal(payload, &rawPayload); err != nil {
		return nil, fmt.Errorf("invalid JSON payload: %w", err)
	}

	return &GenericEventData{
		Fields:  make(map[string]string),
		Payload: rawPayload,
	}, nil
}

// ExtractFields evaluates JSONPath expressions from fieldMapping against
// the payload and populates the Fields map. Missing fields produce empty
// strings rather than errors so that optional mappings do not block task
// creation. Malformed JSONPath expressions (syntax errors) return an error
// immediately so that broken specs are surfaced rather than silently
// producing blank metadata.
func (e *GenericEventData) ExtractFields(fieldMapping map[string]string) error {
	e.Fields = make(map[string]string, len(fieldMapping))
	for key, expr := range fieldMapping {
		val, err := jsonpath.Get(expr, e.Payload)
		if err != nil {
			if strings.HasPrefix(err.Error(), "parsing error:") {
				return fmt.Errorf("invalid JSONPath expression for field %q: %w", key, err)
			}
			// Missing field — not an error, just empty.
			e.Fields[key] = ""
			continue
		}
		e.Fields[key] = fmt.Sprintf("%v", val)
	}
	return nil
}

// MatchesGenericFilters checks if the payload matches all filters (AND semantics).
func MatchesGenericFilters(filters []v1alpha1.GenericWebhookFilter, payload interface{}) (bool, error) {
	for _, filter := range filters {
		val, err := jsonpath.Get(filter.Field, payload)
		if err != nil {
			return false, nil // Field doesn't exist → filter fails
		}

		strVal := fmt.Sprintf("%v", val)

		if filter.Value != nil {
			if strVal != *filter.Value {
				return false, nil
			}
		} else if filter.Pattern != "" {
			re, err := getOrCompileRegexp(filter.Pattern)
			if err != nil {
				return false, fmt.Errorf("invalid regex pattern %q: %w", filter.Pattern, err)
			}
			if !re.MatchString(strVal) {
				return false, nil
			}
		}
	}
	return true, nil
}

// canonicalFieldNames maps documented lowercase fieldMapping keys to the
// uppercase template variable names used by GitHub and Linear sources.
// When a user writes fieldMapping: {id: "$.data.id"}, both {{.id}} and
// {{.ID}} will work in templates.
var canonicalFieldNames = map[string]string{
	"id":    "ID",
	"title": "Title",
	"body":  "Body",
	"url":   "URL",
}

// ExtractGenericWorkItem converts generic webhook data to template variables.
// All fieldMapping keys become top-level template variables. Lowercase keys
// that match standard field names (id, title, body, url) are also promoted
// to their uppercase equivalents (ID, Title, Body, URL) for compatibility
// with GitHub and Linear source templates. The full raw payload is always
// available as {{.Payload}}.
func ExtractGenericWorkItem(eventData *GenericEventData) map[string]interface{} {
	vars := map[string]interface{}{
		"Kind":    "GenericWebhook",
		"Payload": eventData.Payload,
	}

	for key, value := range eventData.Fields {
		vars[key] = value
		if upper, ok := canonicalFieldNames[key]; ok {
			vars[upper] = value
		}
	}

	// Ensure standard fields exist even if not mapped.
	for _, stdField := range []string{"ID", "Title", "Body", "URL"} {
		if _, ok := vars[stdField]; !ok {
			vars[stdField] = ""
		}
	}

	return vars
}

// extractGenericDeliveryID derives a delivery ID for deduplication from
// the mapped "id" field of the first spawner matching sourceName. This
// ensures retries of the same logical event deduplicate even when the raw
// JSON encoding differs between deliveries. Falls back to a SHA-256 hash
// of the body when no spawner maps an id for this source.
func extractGenericDeliveryID(sourceName string, body []byte, spawners []*v1alpha1.TaskSpawner) string {
	for _, sp := range spawners {
		wh := sp.Spec.When.GenericWebhook
		if wh == nil || wh.Source != sourceName {
			continue
		}
		idExpr, ok := wh.FieldMapping["id"]
		if !ok || idExpr == "" {
			continue
		}
		var payload interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			break // Invalid JSON — fall through to body hash
		}
		val, err := jsonpath.Get(idExpr, payload)
		if err != nil || val == nil {
			break // Missing or unparseable — fall through to body hash
		}
		idStr := fmt.Sprintf("%v", val)
		if idStr != "" {
			return "generic-" + sourceName + "-" + idStr
		}
		break
	}
	// Fallback: body hash
	sum := sha256.Sum256(body)
	return "generic-" + sourceName + "-" + hex.EncodeToString(sum[:])
}

// extractSourceFromPath extracts the source name from a URL path like
// /webhook/<source>. Returns an error describing what went wrong if the
// path doesn't match the expected format.
func extractSourceFromPath(path string) (string, error) {
	trimmed := strings.TrimSuffix(path, "/")
	parts := strings.Split(trimmed, "/")
	// Expect ["", "webhook", "<source>"]
	switch {
	case len(parts) < 3 || parts[2] == "":
		return "", fmt.Errorf("path %q missing source segment, expected /webhook/<source>", path)
	case len(parts) > 3:
		return "", fmt.Errorf("path %q has too many segments, expected /webhook/<source>", path)
	case parts[1] != "webhook":
		return "", fmt.Errorf("path %q has unexpected prefix, expected /webhook/<source>", path)
	}
	return parts[2], nil
}
