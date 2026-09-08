package webhook

import (
	"context"
	"net/http"

	"github.com/go-logr/logr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// genericProvider accepts arbitrary JSON without verification; the URL path
// selects the source name. Restrict access at the network layer.
type genericProvider struct{}

// authenticate derives the delivery ID from the "id" fieldMapping of the
// source's spawners, so retries of one logical event deduplicate even when
// the raw JSON encoding differs.
func (genericProvider) authenticate(r *http.Request, body, _ []byte, spawners func() []*kelos.TaskSpawner) (string, string, error) {
	sourceName, err := extractSourceFromPath(r.URL.Path)
	if err != nil {
		return "", "", &httpError{status: http.StatusBadRequest, message: err.Error()}
	}
	return sourceName, extractGenericDeliveryID(sourceName, body, spawners()), nil
}

func (genericProvider) gatewayRef(spawner *kelos.TaskSpawner) (*kelos.GatewayReference, bool) {
	if spawner.Spec.When.GenericWebhook == nil {
		return nil, false
	}
	return spawner.Spec.When.GenericWebhook.GatewayRef, true
}

func (genericProvider) parse(_ logr.Logger, eventType string, body []byte) (*ParsedWebhook, string, error) {
	eventData, err := ParseGenericWebhook(body)
	if err != nil {
		return nil, eventType, err
	}
	// ID and Title come from each spawner's fieldMapping during match.
	return &ParsedWebhook{Generic: eventData}, eventType, nil
}

func (genericProvider) prepare(context.Context, *WebhookHandler, logr.Logger, *ParsedWebhook, []*kelos.TaskSpawner) {
}

func (genericProvider) match(_ context.Context, h *WebhookHandler, spawner *kelos.TaskSpawner, eventType string, parsed *ParsedWebhook) (bool, error) {
	generic := spawner.Spec.When.GenericWebhook
	if generic == nil {
		return false, nil
	}
	// In per-source mode the URL path segment selects the source, so it must
	// match the spawner's declared source. In gateway mode the gatewayRef
	// already scoped this spawner, so the source-name check is skipped.
	if h.gatewayName == "" && generic.Source != eventType {
		return false, nil
	}
	if err := parsed.Generic.ExtractFields(generic.FieldMapping); err != nil {
		return false, err
	}
	parsed.ID = parsed.Generic.Fields["id"]
	parsed.Title = parsed.Generic.Fields["title"]
	matched, err := MatchesGenericFilters(generic.Filters, parsed.Generic.Payload)
	if err != nil || !matched {
		return false, err
	}
	excluded, err := MatchesGenericExcludeFilters(generic.ExcludeFilters, parsed.Generic.Payload)
	if err != nil {
		return false, err
	}
	return !excluded, nil
}

func (genericProvider) templateVars(_ *kelos.TaskSpawner, _ string, parsed *ParsedWebhook) map[string]interface{} {
	return ExtractGenericWorkItem(parsed.Generic)
}

func (genericProvider) annotate(*kelos.Task, *kelos.TaskSpawner, string, *ParsedWebhook, string) {}
