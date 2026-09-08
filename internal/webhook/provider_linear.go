package webhook

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-logr/logr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

type linearProvider struct{}

// authenticate reports the source name as event type; Linear sends the
// resource type in the payload only, which parse applies.
func (linearProvider) authenticate(r *http.Request, body, secret []byte, _ func() []*kelos.TaskSpawner) (string, string, error) {
	deliveryID := r.Header.Get(LinearDeliveryHeader)
	if deliveryID == "" {
		deliveryID = linearDeliveryID(body)
	}
	return string(LinearSource), deliveryID, ValidateLinearSignature(body, r.Header.Get(LinearSignatureHeader), secret)
}

func (linearProvider) gatewayRef(spawner *kelos.TaskSpawner) (*kelos.GatewayReference, bool) {
	if spawner.Spec.When.LinearWebhook == nil {
		return nil, false
	}
	return spawner.Spec.When.LinearWebhook.GatewayRef, true
}

func (linearProvider) parse(log logr.Logger, eventType string, body []byte) (*ParsedWebhook, string, error) {
	eventData, err := ParseLinearWebhook(body)
	if err != nil {
		return nil, eventType, err
	}
	// The resource type (e.g. "Issue", "Comment") makes Task names
	// distinguishable.
	if eventData.Type != "" {
		eventType = strings.ToLower(eventData.Type)
	} else {
		log.Info("Linear webhook payload has no 'type' field, will not match any Types filter")
	}
	return &ParsedWebhook{Linear: eventData, ID: eventData.ID, Title: eventData.Title}, eventType, nil
}

// prepare fetches issue labels for Comment events when any spawner filters
// them by label, because Linear omits labels from Comment payloads.
func (linearProvider) prepare(ctx context.Context, _ *WebhookHandler, log logr.Logger, parsed *ParsedWebhook, spawners []*kelos.TaskSpawner) {
	for _, spawner := range spawners {
		if spawnerNeedsLinearLabels(spawner, parsed.Linear) {
			enrichLinearCommentLabels(ctx, log, parsed.Linear)
			return
		}
	}
}

func (linearProvider) match(_ context.Context, _ *WebhookHandler, spawner *kelos.TaskSpawner, _ string, parsed *ParsedWebhook) (bool, error) {
	if spawner.Spec.When.LinearWebhook == nil {
		return false, nil
	}
	return MatchesLinearEvent(spawner.Spec.When.LinearWebhook, parsed.Linear)
}

func (linearProvider) templateVars(_ *kelos.TaskSpawner, _ string, parsed *ParsedWebhook) map[string]interface{} {
	return ExtractLinearWorkItem(parsed.Linear)
}

func (linearProvider) annotate(*kelos.Task, *kelos.TaskSpawner, string, *ParsedWebhook, string) {}
