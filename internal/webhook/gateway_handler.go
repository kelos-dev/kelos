package webhook

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/githubapp"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// gatewayWebhookSecretKey is the Secret data key holding the HMAC secret used to
// verify inbound deliveries for a github/linear WebhookGateway.
const gatewayWebhookSecretKey = "webhook-secret"

// gatewayMaxPayloadSize bounds the request body the gateway reads, matching the
// legacy per-source handler. GitHub caps webhook payloads at 25 MB.
const gatewayMaxPayloadSize = 10 * 1024 * 1024

// GatewayHandler serves webhook deliveries addressed to a per-gateway path
// (/webhook/<namespace>/<name>). It resolves the WebhookGateway named by the
// path, verifies the delivery against that gateway's secret (github/linear),
// then fans out only to TaskSpawners in the gateway's namespace that reference
// it via gatewayRef. The task builder and delivery cache are shared across
// requests; a per-request WebhookHandler carries the resolved source, secret,
// token resolver, and API base URL.
type GatewayHandler struct {
	client        client.Client
	log           logr.Logger
	taskBuilder   *taskbuilder.TaskBuilder
	deliveryCache *DeliveryCache
}

// NewGatewayHandler creates a GatewayHandler with a shared task builder and
// delivery cache.
func NewGatewayHandler(ctx context.Context, cl client.Client, log logr.Logger) (*GatewayHandler, error) {
	taskBuilder, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		return nil, fmt.Errorf("failed to create task builder: %w", err)
	}
	return &GatewayHandler{
		client:        cl,
		log:           log,
		taskBuilder:   taskBuilder,
		deliveryCache: NewDeliveryCache(ctx),
	}, nil
}

// parseGatewayPath extracts the namespace and name from a gateway webhook path
// of the form /webhook/<namespace>/<name>.
func parseGatewayPath(path string) (namespace, name string, err error) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) != 3 || segments[0] != "webhook" || segments[1] == "" || segments[2] == "" {
		return "", "", fmt.Errorf("invalid gateway webhook path %q: expected /webhook/<namespace>/<name>", path)
	}
	return segments[1], segments[2], nil
}

// ServeHTTP handles a webhook delivery for a per-gateway path.
func (g *GatewayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := g.log.WithValues("method", r.Method, "path", r.URL.Path, "remoteAddr", r.RemoteAddr)

	if r.Method != http.MethodPost {
		log.Info("Rejected non-POST request", "method", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	namespace, name, err := parseGatewayPath(r.URL.Path)
	if err != nil {
		log.Info("Invalid gateway webhook path", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log = log.WithValues("gatewayNamespace", namespace, "gatewayName", name)

	var gateway v1alpha1.WebhookGateway
	if err := g.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("WebhookGateway not found")
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// A transient API or RBAC failure must not be reported as a missing
		// gateway; return 500 so the provider retries.
		log.Error(err, "Failed to get WebhookGateway")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, gatewayMaxPayloadSize+1))
	if err != nil {
		log.Error(err, "Failed to read request body")
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if len(body) > gatewayMaxPayloadSize {
		log.Info("Rejected oversized webhook payload", "size", len(body))
		http.Error(w, "Payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	source, err := gatewaySource(&gateway.Spec)
	if err != nil {
		log.Error(err, "Invalid gateway configuration")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Resolve the inbound HMAC secret for github/linear. Generic gateways are
	// accepted without verification in this version.
	var secret []byte
	if source != GenericSource {
		secret, err = g.resolveGatewaySecret(ctx, &gateway)
		if err != nil {
			log.Error(err, "Failed to resolve gateway secret")
			http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	// Extract per-source headers, verify the signature, and derive a delivery ID.
	var eventType, deliveryID string
	var scopedSpawners []*v1alpha1.TaskSpawner
	spawnersListed := false

	switch source {
	case GitHubSource:
		eventType = r.Header.Get(GitHubEventHeader)
		deliveryID = r.Header.Get(GitHubDeliveryHeader)
		if err := ValidateGitHubSignature(body, r.Header.Get(GitHubSignatureHeader), secret); err != nil {
			log.Error(err, "GitHub signature validation failed", "eventType", eventType, "deliveryID", deliveryID)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	case LinearSource:
		eventType = "linear"
		deliveryID = r.Header.Get(LinearDeliveryHeader)
		if deliveryID == "" {
			deliveryID = linearDeliveryID(body)
		}
		if err := ValidateLinearSignature(body, r.Header.Get(LinearSignatureHeader), secret); err != nil {
			log.Error(err, "Linear signature validation failed", "deliveryID", deliveryID)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	case GenericSource:
		// No verification scheme is configured for generic gateways yet. Accept
		// the delivery but log loudly so the lack of authentication is visible
		// in server logs (the gateway's status also surfaces Unauthenticated).
		eventType = name
		scopedSpawners, err = g.listGatewayScopedSpawners(ctx, namespace, name, source)
		if err != nil {
			log.Error(err, "Failed to list TaskSpawners", "namespace", namespace)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		spawnersListed = true
		// The spawners are already scoped by gatewayRef, so derive the dedup id
		// from their fieldMapping regardless of the spawner's source name. The
		// prefix is namespace-qualified so same-named gateways in different
		// namespaces do not collide in the process-wide delivery cache.
		deliveryID = extractGatewayGenericDeliveryID(namespace+"/"+name, body, scopedSpawners)
		log.Info("WARNING: accepting generic webhook without signature verification", "deliveryID", deliveryID)
	}

	if deliveryID != "" && g.deliveryCache.CheckAndMark(deliveryID) {
		log.Info("Duplicate webhook delivery, returning cached response", "eventType", eventType, "deliveryID", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	if !spawnersListed {
		scopedSpawners, err = g.listGatewayScopedSpawners(ctx, namespace, name, source)
		if err != nil {
			log.Error(err, "Failed to list TaskSpawners", "namespace", namespace)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}
	if len(scopedSpawners) == 0 {
		log.Info("No TaskSpawners reference this gateway", "eventType", eventType, "deliveryID", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Build a per-request token resolver from the gateway's credentialsRef so
	// outbound GitHub API calls (enrichment, reporting) use per-instance creds.
	tokenResolver, err := g.resolveGatewayTokenResolver(ctx, &gateway)
	if err != nil {
		log.Error(err, "Failed to resolve gateway credentials")
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	wh := g.handlerForGateway(&gateway, source, secret, tokenResolver)
	if _, err := wh.processWebhook(ctx, eventType, body, deliveryID, scopedSpawners); err != nil {
		log.Error(err, "Failed to process webhook", "eventType", eventType, "deliveryID", deliveryID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	log.Info("Webhook processed successfully", "eventType", eventType, "deliveryID", deliveryID)
	w.WriteHeader(http.StatusOK)
}

// handlerForGateway builds a per-request WebhookHandler that shares the gateway
// handler's task builder and delivery cache.
func (g *GatewayHandler) handlerForGateway(gw *v1alpha1.WebhookGateway, source WebhookSource, secret []byte, tokenResolver func(context.Context) (string, error)) *WebhookHandler {
	var apiBaseURL string
	if gw.Spec.GitHub != nil {
		apiBaseURL = gw.Spec.GitHub.APIBaseURL
	}
	return &WebhookHandler{
		client:           g.client,
		source:           source,
		log:              g.log.WithValues("gateway", gw.Name, "namespace", gw.Namespace),
		taskBuilder:      g.taskBuilder,
		secret:           secret,
		deliveryCache:    g.deliveryCache,
		githubAPIBaseURL: apiBaseURL,
		tokenResolver:    tokenResolver,
		gatewayName:      gw.Name,
	}
}

// listGatewayScopedSpawners returns TaskSpawners in the gateway's namespace
// whose matching webhook block references this gateway by name. A List error is
// returned to the caller (rather than swallowed as an empty set) so a transient
// API failure surfaces as a retryable 5xx instead of silently dropping the
// delivery.
func (g *GatewayHandler) listGatewayScopedSpawners(ctx context.Context, namespace, name string, source WebhookSource) ([]*v1alpha1.TaskSpawner, error) {
	var spawnerList v1alpha1.TaskSpawnerList
	if err := g.client.List(ctx, &spawnerList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing TaskSpawners in namespace %s: %w", namespace, err)
	}

	var spawners []*v1alpha1.TaskSpawner
	for i := range spawnerList.Items {
		when := &spawnerList.Items[i].Spec.When
		var ref *v1alpha1.GatewayReference
		switch source {
		case GitHubSource:
			if when.GitHubWebhook != nil {
				ref = when.GitHubWebhook.GatewayRef
			}
		case LinearSource:
			if when.LinearWebhook != nil {
				ref = when.LinearWebhook.GatewayRef
			}
		case GenericSource:
			if when.GenericWebhook != nil {
				ref = when.GenericWebhook.GatewayRef
			}
		}
		if ref != nil && ref.Name == name {
			spawners = append(spawners, &spawnerList.Items[i])
		}
	}
	return spawners, nil
}

// resolveGatewaySecret reads the inbound HMAC secret for a github/linear gateway.
func (g *GatewayHandler) resolveGatewaySecret(ctx context.Context, gw *v1alpha1.WebhookGateway) ([]byte, error) {
	ref := gatewaySecretRef(&gw.Spec)
	if ref == nil {
		return nil, fmt.Errorf("gateway %s/%s has no secretRef", gw.Namespace, gw.Name)
	}
	var secret corev1.Secret
	if err := g.client.Get(ctx, types.NamespacedName{Namespace: gw.Namespace, Name: ref.Name}, &secret); err != nil {
		return nil, fmt.Errorf("fetching gateway secret %s: %w", ref.Name, err)
	}
	value := secret.Data[gatewayWebhookSecretKey]
	if len(value) == 0 {
		return nil, fmt.Errorf("gateway secret %s is missing key %q", ref.Name, gatewayWebhookSecretKey)
	}
	return value, nil
}

// resolveGatewayTokenResolver builds a GitHub token resolver from a github
// gateway's credentialsRef. Returns nil when not a github gateway or no
// credentialsRef is configured.
func (g *GatewayHandler) resolveGatewayTokenResolver(ctx context.Context, gw *v1alpha1.WebhookGateway) (func(context.Context) (string, error), error) {
	if gw.Spec.GitHub == nil || gw.Spec.GitHub.CredentialsRef == nil {
		return nil, nil
	}
	var secret corev1.Secret
	if err := g.client.Get(ctx, types.NamespacedName{Namespace: gw.Namespace, Name: gw.Spec.GitHub.CredentialsRef.Name}, &secret); err != nil {
		return nil, fmt.Errorf("fetching gateway credentials %s: %w", gw.Spec.GitHub.CredentialsRef.Name, err)
	}
	return githubapp.NewSecretTokenResolver(secret.Data, gw.Spec.GitHub.APIBaseURL)
}

// gatewaySource maps a WebhookGateway spec to the internal WebhookSource based on
// which provider sub-struct is set.
func gatewaySource(spec *v1alpha1.WebhookGatewaySpec) (WebhookSource, error) {
	switch {
	case spec.GitHub != nil:
		return GitHubSource, nil
	case spec.Linear != nil:
		return LinearSource, nil
	case spec.Generic != nil:
		return GenericSource, nil
	default:
		return "", fmt.Errorf("no source configured: exactly one of github, linear, or generic is required")
	}
}

// gatewaySecretRef returns the inbound HMAC secretRef for the gateway's source,
// or nil for generic gateways.
func gatewaySecretRef(spec *v1alpha1.WebhookGatewaySpec) *v1alpha1.SecretReference {
	switch {
	case spec.GitHub != nil:
		return &spec.GitHub.SecretRef
	case spec.Linear != nil:
		return &spec.Linear.SecretRef
	default:
		return nil
	}
}
