package webhook

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-logr/logr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
)

// webhookProvider is the per-source half of webhook handling. WebhookHandler
// owns the delivery pipeline (deduplication, spawner iteration, Task
// creation); a provider owns everything that depends on one source's payload
// format: request verification, delivery identity, parsing, spawner matching,
// template variables, and reporting annotations.
type webhookProvider interface {
	// authenticate verifies the request against secret. It returns the event
	// type known from headers (refined later by parse) and the delivery ID used
	// for deduplication, both also on failure so the rejection can be logged.
	// spawners lists this source's spawners and is only consulted by providers
	// whose delivery identity depends on spawner configuration.
	authenticate(r *http.Request, body, secret []byte, spawners func() []*kelos.TaskSpawner) (eventType, deliveryID string, err error)
	// gatewayRef returns the spawner's gateway binding and whether the spawner
	// uses this provider at all.
	gatewayRef(spawner *kelos.TaskSpawner) (*kelos.GatewayReference, bool)
	// parse decodes the payload and returns the event type to use from here on.
	parse(log logr.Logger, eventType string, body []byte) (*ParsedWebhook, string, error)
	// prepare runs once per delivery before spawners are matched, for payload
	// enrichment that needs outbound API calls.
	prepare(ctx context.Context, h *WebhookHandler, log logr.Logger, parsed *ParsedWebhook, spawners []*kelos.TaskSpawner)
	// match reports whether the spawner's filters accept the event.
	match(ctx context.Context, h *WebhookHandler, spawner *kelos.TaskSpawner, eventType string, parsed *ParsedWebhook) (bool, error)
	// templateVars returns the variables exposed to the spawner's templates.
	templateVars(spawner *kelos.TaskSpawner, eventType string, parsed *ParsedWebhook) map[string]interface{}
	// annotate stamps reporting annotations on the Task when the spawner
	// configures reporting and the event maps to a reportable item.
	annotate(task *kelos.Task, spawner *kelos.TaskSpawner, eventType string, parsed *ParsedWebhook, gatewayName string)
}

// webhookProviders is the registry the handlers dispatch through.
var webhookProviders = map[WebhookSource]webhookProvider{
	GitHubSource:  githubProvider{},
	LinearSource:  linearProvider{},
	GitLabSource:  gitlabProvider{},
	GenericSource: genericProvider{},
}

func providerFor(source WebhookSource) (webhookProvider, error) {
	p, ok := webhookProviders[source]
	if !ok {
		return nil, fmt.Errorf("unsupported source: %s", source)
	}
	return p, nil
}

// httpError is an authentication failure that carries its own HTTP response,
// used when a request is malformed rather than unauthorized.
type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return e.message }

// reportingAnnotations returns the annotations every provider stamps when
// comment reporting is configured: the reporting target and the comment mode.
// Providers add their own addressing (owner/repo, project) on top.
func reportingAnnotations(tracker kelos.TrackerSource, kind string, number int, gatewayName string) map[string]string {
	annotations := map[string]string{
		reporting.AnnotationSourceKind:   kind,
		reporting.AnnotationSourceNumber: strconv.Itoa(number),
	}
	// In gateway mode, record the serving gateway so the reporting reconciler
	// can resolve its per-instance credentials and API base URL.
	if gatewayName != "" {
		annotations[reporting.AnnotationWebhookGateway] = gatewayName
	}
	if tracker.Comments != nil {
		annotations[reporting.AnnotationCommentReporting] = "enabled"
		annotations[reporting.AnnotationCommentMode] = string(tracker.CommentMode())
	}
	return annotations
}

func addAnnotations(task *kelos.Task, annotations map[string]string) {
	if task.Annotations == nil {
		task.Annotations = make(map[string]string, len(annotations))
	}
	for k, v := range annotations {
		task.Annotations[k] = v
	}
}
