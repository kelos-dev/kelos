package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// WebhookSource represents the type of webhook source.
type WebhookSource string

const (
	GitHubSource WebhookSource = "github"
	LinearSource WebhookSource = "linear"

	// GitHub webhook headers
	GitHubEventHeader     = "X-GitHub-Event"
	GitHubSignatureHeader = "X-Hub-Signature-256"
	GitHubDeliveryHeader  = "X-GitHub-Delivery"

	// Linear webhook headers
	LinearSignatureHeader = "Linear-Signature"
	LinearDeliveryHeader  = "Linear-Delivery"
)

// ParsedWebhook holds parsed webhook data for either GitHub or Linear sources.
type ParsedWebhook struct {
	GitHub *GitHubEventData
	Linear *LinearEventData
	// Common fields for logging and task naming
	ID    string
	Title string
}

// WebhookHandler handles webhook requests for a specific source type.
type WebhookHandler struct {
	client        client.Client
	source        WebhookSource
	log           logr.Logger
	taskBuilder   *taskbuilder.TaskBuilder
	secret        []byte
	deliveryCache *DeliveryCache
}

// DeliveryCache tracks processed webhook deliveries for idempotency.
type DeliveryCache struct {
	mu    sync.RWMutex
	cache map[string]time.Time
}

// NewDeliveryCache creates a new delivery cache with cleanup.
func NewDeliveryCache(ctx context.Context) *DeliveryCache {
	cache := &DeliveryCache{
		cache: make(map[string]time.Time),
	}

	// Clean up expired entries every hour
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cache.cleanup()
			}
		}
	}()

	return cache
}

// CheckAndMark atomically checks if a delivery ID was already processed and marks it if not.
// Returns true if already processed, false if this is the first time.
func (d *DeliveryCache) CheckAndMark(deliveryID string) (alreadyProcessed bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.cache[deliveryID]; exists {
		return true
	}
	d.cache[deliveryID] = time.Now()
	return false
}

// cleanup removes entries older than 24 hours.
func (d *DeliveryCache) cleanup() {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().Add(-24 * time.Hour)
	for id, timestamp := range d.cache {
		if timestamp.Before(cutoff) {
			delete(d.cache, id)
		}
	}
}

// NewWebhookHandler creates a new webhook handler for the specified source.
func NewWebhookHandler(ctx context.Context, client client.Client, source WebhookSource, log logr.Logger) (*WebhookHandler, error) {
	secret := []byte(os.Getenv("WEBHOOK_SECRET"))
	if len(secret) == 0 {
		return nil, fmt.Errorf("WEBHOOK_SECRET environment variable is required")
	}

	taskBuilder, err := taskbuilder.NewTaskBuilder(client)
	if err != nil {
		return nil, fmt.Errorf("failed to create task builder: %w", err)
	}

	return &WebhookHandler{
		client:        client,
		source:        source,
		log:           log,
		taskBuilder:   taskBuilder,
		secret:        secret,
		deliveryCache: NewDeliveryCache(ctx),
	}, nil
}

// ServeHTTP handles webhook HTTP requests.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := h.log.WithValues("method", r.Method, "path", r.URL.Path, "source", h.source, "remoteAddr", r.RemoteAddr)

	// Log incoming webhook request
	log.Info("Received webhook request")

	// Only accept POST requests
	if r.Method != http.MethodPost {
		log.Info("Rejected non-POST request", "method", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the payload with a size limit to prevent resource exhaustion.
	// GitHub caps webhook payloads at 25 MB; we use a 10 MB limit.
	const maxPayloadSize = 10 * 1024 * 1024 // 10 MB
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPayloadSize+1))
	if err != nil {
		log.Error(err, "Failed to read request body")
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if len(body) > maxPayloadSize {
		log.Info("Rejected oversized webhook payload", "size", len(body))
		http.Error(w, "Payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Extract headers and validate signature
	var eventType, signature, deliveryID string

	switch h.source {
	case GitHubSource:
		eventType = r.Header.Get(GitHubEventHeader)
		signature = r.Header.Get(GitHubSignatureHeader)
		deliveryID = r.Header.Get(GitHubDeliveryHeader)

		log.Info("Processing GitHub webhook", "eventType", eventType, "deliveryID", deliveryID, "payloadSize", len(body))

		if err := ValidateGitHubSignature(body, signature, h.secret); err != nil {
			log.Error(err, "GitHub signature validation failed", "eventType", eventType, "deliveryID", deliveryID)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	case LinearSource:
		signature = r.Header.Get(LinearSignatureHeader)
		deliveryID = r.Header.Get(LinearDeliveryHeader)
		eventType = "linear" // Linear doesn't send event type in header

		// If no delivery header was sent, derive delivery ID from a SHA-256
		// hash of the body so that identical retries are still deduplicated.
		if deliveryID == "" {
			deliveryID = linearDeliveryID(body)
		}

		log.Info("Processing Linear webhook", "eventType", eventType, "deliveryID", deliveryID, "payloadSize", len(body))

		if err := ValidateLinearSignature(body, signature, h.secret); err != nil {
			log.Error(err, "Linear signature validation failed", "eventType", eventType, "deliveryID", deliveryID)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	default:
		log.Error(fmt.Errorf("unsupported source: %s", h.source), "Unsupported webhook source")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Check for duplicate delivery
	if deliveryID != "" && h.deliveryCache.CheckAndMark(deliveryID) {
		log.Info("Duplicate webhook delivery, returning cached response", "eventType", eventType, "deliveryID", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Process the webhook
	_, err = h.processWebhook(ctx, eventType, body, deliveryID)
	if err != nil {
		log.Error(err, "Failed to process webhook", "eventType", eventType, "deliveryID", deliveryID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	log.Info("Webhook processed successfully", "eventType", eventType, "deliveryID", deliveryID)
	w.WriteHeader(http.StatusOK)
}

// linearDeliveryID computes a stable delivery identifier for a Linear webhook.
// Linear does not send a per-delivery ID header (webhookId in the payload
// identifies the webhook configuration, not an individual delivery). We use a
// SHA-256 hash of the body so that byte-identical retries are deduplicated
// while distinct events always get processed.
func linearDeliveryID(body []byte) string {
	sum := sha256.Sum256(body)
	return "linear-" + hex.EncodeToString(sum[:])
}

// processWebhook processes a validated webhook payload.
func (h *WebhookHandler) processWebhook(ctx context.Context, eventType string, payload []byte, deliveryID string) (bool, error) {
	log := h.log.WithValues("eventType", eventType, "deliveryID", deliveryID)

	// Parse the webhook payload once up front and reuse across matching and task creation.
	parsed := &ParsedWebhook{}
	switch h.source {
	case GitHubSource:
		eventData, err := ParseGitHubWebhook(eventType, payload)
		if err != nil {
			return false, fmt.Errorf("failed to parse %s webhook: %w", h.source, err)
		}
		parsed.GitHub = eventData
		parsed.ID = eventData.ID
		parsed.Title = eventData.Title
		if parsed.ID != "" {
			log = log.WithValues("githubID", parsed.ID)
			if parsed.Title != "" {
				log = log.WithValues("githubTitle", parsed.Title)
			}
		}

	case LinearSource:
		eventData, err := ParseLinearWebhook(payload)
		if err != nil {
			return false, fmt.Errorf("failed to parse %s webhook: %w", h.source, err)
		}
		parsed.Linear = eventData
		parsed.ID = eventData.ID
		parsed.Title = eventData.Title
		// Override the generic "linear" eventType with the actual resource type
		// (e.g., "Issue", "Comment") so task names are distinguishable.
		if eventData.Type != "" {
			eventType = strings.ToLower(eventData.Type)
		} else {
			log.Info("Linear webhook payload has no 'type' field, will not match any Types filter")
		}
		if parsed.ID != "" {
			log = log.WithValues("linearID", parsed.ID)
			if parsed.Title != "" {
				log = log.WithValues("linearTitle", parsed.Title)
			}
		}
	}

	log.Info("Processing webhook event", "resourceID", parsed.ID, "title", parsed.Title)

	// Get all TaskSpawners that match this source type
	spawners, err := h.getMatchingSpawners(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get matching spawners: %w", err)
	}

	if len(spawners) == 0 {
		log.Info("No matching TaskSpawners found for webhook")
		return true, nil // Not an error, just nothing to do
	}

	log.Info("Found matching TaskSpawners", "count", len(spawners))

	// Lazily enrich the Branch field for issue_comment events on pull
	// requests. The GitHub issue_comment payload does not include the PR's
	// head ref, so we fetch it from the API once per delivery.
	if parsed.GitHub != nil && needsBranchEnrichment(parsed.GitHub) {
		enrichGitHubIssueCommentBranch(ctx, log, parsed.GitHub)
	}

	tasksCreated := 0
	linearLabelsEnriched := false

	for _, spawner := range spawners {
		spawnerLog := log.WithValues("spawner", spawner.Name, "namespace", spawner.Namespace)

		// Check if spawner is suspended
		if spawner.Spec.Suspend != nil && *spawner.Spec.Suspend {
			spawnerLog.V(1).Info("Skipping suspended spawner")
			continue
		}

		// Check max concurrency
		// Note: For webhook TaskSpawners, activeTasks is updated by the kelos-controller
		// when Tasks change status. This provides eventually consistent rate limiting.
		if spawner.Spec.MaxConcurrency != nil && *spawner.Spec.MaxConcurrency > 0 {
			activeTasks := spawner.Status.ActiveTasks
			if int32(activeTasks) >= *spawner.Spec.MaxConcurrency {
				spawnerLog.Info("Max concurrency reached, dropping webhook event",
					"activeTasks", activeTasks,
					"maxConcurrency", *spawner.Spec.MaxConcurrency,
					"reason", "Webhook accepted but task creation skipped due to concurrency limits")
				continue // Skip this spawner, continue with others
			}
		}

		// Lazily enrich labels for Linear Comment events. Linear does not
		// include issue labels in Comment webhook payloads, so when a
		// spawner filters Comments by labels we fetch them from the API.
		// Lazily enrich labels once per delivery. We set the flag after the
		// call so that a transient API failure does not silently skip label
		// filtering for all remaining spawners in this loop.
		if parsed.Linear != nil && !linearLabelsEnriched && spawnerNeedsLinearLabels(spawner, parsed.Linear) {
			enrichLinearCommentLabels(ctx, spawnerLog, parsed.Linear)
			linearLabelsEnriched = true
		}

		// Check if this webhook matches the spawner's filters
		matches, err := h.matchesSpawner(spawner, eventType, parsed)
		if err != nil {
			spawnerLog.Error(err, "Failed to check spawner match")
			continue
		}

		if !matches {
			spawnerLog.Info("Webhook does not match spawner filters")
			continue
		}

		spawnerLog.Info("Webhook matches spawner filters - creating task")

		// Create task for this spawner
		err = h.createTask(ctx, spawner, eventType, parsed, deliveryID)
		if err != nil {
			spawnerLog.Error(err, "Failed to create task")
			continue
		}

		tasksCreated++
		spawnerLog.Info("Successfully created task from webhook")
	}

	log.Info("Webhook processing completed", "totalSpawners", len(spawners), "tasksCreated", tasksCreated)
	return tasksCreated > 0, nil
}

// getMatchingSpawners returns TaskSpawners that match the webhook source.
func (h *WebhookHandler) getMatchingSpawners(ctx context.Context) ([]*v1alpha1.TaskSpawner, error) {
	var spawnerList v1alpha1.TaskSpawnerList
	if err := h.client.List(ctx, &spawnerList, &client.ListOptions{}); err != nil {
		return nil, err
	}

	var matching []*v1alpha1.TaskSpawner
	for i := range spawnerList.Items {
		spawner := &spawnerList.Items[i]

		switch h.source {
		case GitHubSource:
			if spawner.Spec.When.GitHubWebhook != nil {
				matching = append(matching, spawner)
			}
		case LinearSource:
			if spawner.Spec.When.LinearWebhook != nil {
				matching = append(matching, spawner)
			}
		}
	}

	return matching, nil
}

// matchesSpawner checks if the webhook matches the spawner's configuration.
func (h *WebhookHandler) matchesSpawner(spawner *v1alpha1.TaskSpawner, eventType string, parsed *ParsedWebhook) (bool, error) {
	switch h.source {
	case GitHubSource:
		if spawner.Spec.When.GitHubWebhook == nil {
			return false, nil
		}

		// Check repository filter first
		if spawner.Spec.When.GitHubWebhook.Repository != "" {
			if parsed.GitHub.Repository != spawner.Spec.When.GitHubWebhook.Repository {
				return false, nil
			}
		}

		return MatchesGitHubEvent(spawner.Spec.When.GitHubWebhook, eventType, parsed.GitHub)

	case LinearSource:
		if spawner.Spec.When.LinearWebhook == nil {
			return false, nil
		}
		return MatchesLinearEvent(spawner.Spec.When.LinearWebhook, parsed.Linear)

	default:
		return false, fmt.Errorf("unsupported source: %s", h.source)
	}
}

// createTask creates a new Task from the webhook event.
func (h *WebhookHandler) createTask(ctx context.Context, spawner *v1alpha1.TaskSpawner, eventType string, parsed *ParsedWebhook, deliveryID string) error {
	log := h.log.WithValues("spawner", spawner.Name, "namespace", spawner.Namespace, "eventType", eventType, "deliveryID", deliveryID)

	// Extract template variables based on source
	var templateVars map[string]interface{}

	switch h.source {
	case GitHubSource:
		templateVars = ExtractGitHubWorkItem(parsed.GitHub)

	case LinearSource:
		templateVars = ExtractLinearWorkItem(parsed.Linear)

	default:
		return fmt.Errorf("unsupported source: %s", h.source)
	}

	log.Info("Extracted template variables", "ID", templateVars["ID"], "Title", templateVars["Title"], "Action", templateVars["Action"])

	// Build unique task name using a hash of the delivery ID to avoid collisions.
	// Hashing gives uniform 12-hex-char suffix regardless of input length,
	// avoiding the collision risk of simple prefix truncation.
	sanitizedEventType := strings.ReplaceAll(eventType, "_", "-")
	sum := sha256.Sum256([]byte(deliveryID))
	shortHash := hex.EncodeToString(sum[:])[:12]
	taskName := fmt.Sprintf("%s-%s-%s", spawner.Name, sanitizedEventType, shortHash)
	if len(taskName) > 63 {
		taskName = strings.TrimRight(taskName[:63], "-.")
	}

	// Resolve GVK for the spawner owner reference
	gvks, _, err := h.client.Scheme().ObjectKinds(spawner)
	if err != nil || len(gvks) == 0 {
		return fmt.Errorf("failed to get GVK for TaskSpawner: %w", err)
	}
	gvk := gvks[0]

	// Create the task — BuildTask sets kelos.dev/taskspawner label and owner reference
	task, err := h.taskBuilder.BuildTask(
		taskName,
		spawner.Namespace,
		&spawner.Spec.TaskTemplate,
		templateVars,
		&taskbuilder.SpawnerRef{
			Name:       spawner.Name,
			UID:        string(spawner.UID),
			APIVersion: gvk.GroupVersion().String(),
			Kind:       gvk.Kind,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to build task: %w", err)
	}

	if err := h.client.Create(ctx, task); err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}

	return nil
}
