package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/contextfetch"
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// WebhookSource represents the type of webhook source.
type WebhookSource string

const (
	GitHubSource  WebhookSource = "github"
	LinearSource  WebhookSource = "linear"
	GitLabSource  WebhookSource = "gitlab"
	GenericSource WebhookSource = "generic"

	// GitLab webhook headers. Idempotency-Key is stable across retries of one
	// delivery; X-Gitlab-Event-UUID identifies the event that triggered it.
	GitLabEventHeader       = "X-Gitlab-Event"
	GitLabTokenHeader       = "X-Gitlab-Token"
	GitLabIdempotencyHeader = "Idempotency-Key"
	GitLabDeliveryHeader    = "X-Gitlab-Event-UUID"

	// GitHub webhook headers
	GitHubEventHeader     = "X-GitHub-Event"
	GitHubSignatureHeader = "X-Hub-Signature-256"
	GitHubDeliveryHeader  = "X-GitHub-Delivery"

	// Linear webhook headers
	LinearSignatureHeader = "Linear-Signature"
	LinearDeliveryHeader  = "Linear-Delivery"
)

// ParsedWebhook holds parsed webhook data for GitHub, Linear, GitLab, or generic sources.
type ParsedWebhook struct {
	GitHub  *GitHubEventData
	Linear  *LinearEventData
	GitLab  *GitLabEventData
	Generic *GenericEventData
	// Common fields for logging and task naming
	ID    string
	Title string
}

// WebhookHandler handles webhook requests for a specific source type.
type WebhookHandler struct {
	client           client.Client
	source           WebhookSource
	log              logr.Logger
	taskBuilder      *taskbuilder.TaskBuilder
	secret           []byte
	deliveryCache    *DeliveryCache
	githubAPIBaseURL string

	githubTokenResolver func(context.Context) (string, error)

	// gatewayName is the WebhookGateway this handler serves (gateway mode only).
	// When set, created Tasks are stamped with it so the reporting reconciler can
	// resolve per-gateway GitHub credentials and API base URL. Empty in --source
	// mode.
	gatewayName string
}

// WebhookHandlerOptions contains source-specific webhook server configuration.
type WebhookHandlerOptions struct {
	Secret              []byte
	GitHubAPIBaseURL    string
	GitHubTokenResolver func(context.Context) (string, error)
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

// Forget allows a failed webhook delivery to be retried.
func (d *DeliveryCache) Forget(deliveryID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.cache, deliveryID)
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

// NewWebhookHandler creates a webhook handler for one source.
func NewWebhookHandler(ctx context.Context, client client.Client, source WebhookSource, options WebhookHandlerOptions, log logr.Logger) (*WebhookHandler, error) {
	if source != GenericSource && len(options.Secret) == 0 {
		return nil, fmt.Errorf("webhook secret is required for %s", source)
	}

	taskBuilder, err := taskbuilder.NewTaskBuilder(client)
	if err != nil {
		return nil, fmt.Errorf("failed to create task builder: %w", err)
	}

	return &WebhookHandler{
		client:              client,
		source:              source,
		log:                 log,
		taskBuilder:         taskBuilder,
		secret:              options.Secret,
		deliveryCache:       NewDeliveryCache(ctx),
		githubAPIBaseURL:    options.GitHubAPIBaseURL,
		githubTokenResolver: options.GitHubTokenResolver,
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

	provider, err := providerFor(h.source)
	if err != nil {
		log.Error(err, "Unsupported webhook source")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// A provider that needs the spawner list to identify the delivery gets it
	// once here, and processWebhook reuses it instead of listing again.
	var listedSpawners []*kelos.TaskSpawner
	listSpawners := func() []*kelos.TaskSpawner {
		if listedSpawners == nil {
			listedSpawners, _ = h.getMatchingSpawners(ctx)
		}
		return listedSpawners
	}

	eventType, deliveryID, err := provider.authenticate(r, body, h.secret, listSpawners)
	if err != nil {
		var rejection *httpError
		if errors.As(err, &rejection) {
			log.Info("Rejected webhook request", "error", err)
			http.Error(w, rejection.message, rejection.status)
			return
		}
		log.Error(err, "Webhook authentication failed", "eventType", eventType, "deliveryID", deliveryID)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	log.Info("Processing webhook", "eventType", eventType, "deliveryID", deliveryID, "payloadSize", len(body))

	// Check for duplicate delivery
	if deliveryID != "" && h.deliveryCache.CheckAndMark(deliveryID) {
		log.Info("Duplicate webhook delivery, returning cached response", "eventType", eventType, "deliveryID", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	_, err = h.processWebhook(ctx, eventType, body, deliveryID, listedSpawners, nil)
	if err != nil {
		if deliveryID != "" {
			h.deliveryCache.Forget(deliveryID)
		}
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

func githubDeliveryID(body []byte) string {
	sum := sha256.Sum256(body)
	return "github-" + hex.EncodeToString(sum[:])
}

// gitlabRequestDeliveryID picks the delivery identifier for a GitLab request:
// the retry-stable Idempotency-Key, then the event UUID, then a body hash for
// GitLab versions that send neither.
func gitlabRequestDeliveryID(r *http.Request, body []byte) string {
	if id := r.Header.Get(GitLabIdempotencyHeader); id != "" {
		return "gitlab-" + id
	}
	if id := r.Header.Get(GitLabDeliveryHeader); id != "" {
		return "gitlab-" + id
	}
	return gitlabDeliveryID(body)
}

// gitlabDeliveryID derives a delivery identifier from the body for GitLab
// versions that send no delivery headers.
func gitlabDeliveryID(body []byte) string {
	sum := sha256.Sum256(body)
	return "gitlab-" + hex.EncodeToString(sum[:])
}

// processWebhook processes a validated payload with optional
// pre-scoped TaskSpawners and SessionSpawners. A non-nil slice, including an
// empty one, prevents a cluster-wide list for that resource type.
func (h *WebhookHandler) processWebhook(ctx context.Context, eventType string, payload []byte, deliveryID string, prefetchedSpawners []*kelos.TaskSpawner, prefetchedSessionSpawners []*kelos.SessionSpawner) (bool, error) {
	log := h.log.WithValues("deliveryID", deliveryID)

	provider, err := providerFor(h.source)
	if err != nil {
		return false, err
	}

	// Parse the webhook payload once up front and reuse across matching and task creation.
	parsed, eventType, err := provider.parse(log, eventType, payload)
	if err != nil {
		return false, fmt.Errorf("failed to parse %s webhook: %w", h.source, err)
	}
	log = log.WithValues("eventType", eventType)
	log.Info("Processing webhook event", "resourceID", parsed.ID, "title", parsed.Title)

	// Use pre-fetched spawners when available, otherwise list.
	spawners := prefetchedSpawners
	if spawners == nil {
		spawners, err = h.getMatchingSpawners(ctx)
		if err != nil {
			return false, fmt.Errorf("failed to get matching spawners: %w", err)
		}
	}
	// SessionSpawners are driven by GitHub webhooks only.
	var sessionSpawners []*kelos.SessionSpawner
	if parsed.GitHub != nil {
		if prefetchedSessionSpawners != nil {
			sessionSpawners = prefetchedSessionSpawners
		} else {
			var err error
			sessionSpawners, err = h.getMatchingSessionSpawners(ctx)
			if err != nil {
				return false, fmt.Errorf("failed to get matching SessionSpawners: %w", err)
			}
		}
	}

	if len(spawners) == 0 && len(sessionSpawners) == 0 {
		log.Info("No matching spawners found for webhook")
		return true, nil // Not an error, just nothing to do
	}

	log.Info("Found matching spawners", "taskSpawners", len(spawners), "sessionSpawners", len(sessionSpawners))

	provider.prepare(ctx, h, log, parsed, spawners)

	tasksCreated := 0
	var taskSpawnerErrors []error

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

		// Check if this webhook matches the spawner's filters
		matches, err := provider.match(ctx, h, spawner, eventType, parsed)
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
		created, err := h.createTask(ctx, provider, spawner, eventType, parsed, deliveryID)
		if err != nil {
			spawnerLog.Error(err, "Failed to create task")
			taskSpawnerErrors = append(taskSpawnerErrors, fmt.Errorf("spawner %s: %w", spawner.Name, err))
			continue
		}

		if created {
			tasksCreated++
			spawnerLog.Info("Successfully created task from webhook")
		} else {
			spawnerLog.Info("Webhook deduplicated against existing task, no new task created")
		}
	}

	sessionsProcessed := 0
	var sessionErrors []error
	for _, spawner := range sessionSpawners {
		processed, err := h.processSessionSpawner(ctx, spawner, eventType, parsed.GitHub, deliveryID)
		if err != nil {
			h.log.Error(err, "Failed to process SessionSpawner", "sessionSpawner", spawner.Name, "namespace", spawner.Namespace)
			sessionErrors = append(sessionErrors, err)
			continue
		}
		if processed {
			sessionsProcessed++
		}
	}

	log.Info("Webhook processing completed", "taskSpawners", len(spawners), "sessionSpawners", len(sessionSpawners), "tasksCreated", tasksCreated, "sessionsProcessed", sessionsProcessed)
	return tasksCreated > 0 || sessionsProcessed > 0, errors.Join(append(taskSpawnerErrors, sessionErrors...)...)
}

// getMatchingSpawners returns TaskSpawners that use the webhook source.
// Spawners bound to a WebhookGateway are skipped: those are served (and
// authenticated) by the gateway path, so the per-source server must not also
// match them, or the Task would be created twice.
func (h *WebhookHandler) getMatchingSpawners(ctx context.Context) ([]*kelos.TaskSpawner, error) {
	provider, err := providerFor(h.source)
	if err != nil {
		return nil, err
	}
	var spawnerList kelos.TaskSpawnerList
	if err := h.client.List(ctx, &spawnerList, &client.ListOptions{}); err != nil {
		return nil, err
	}

	matching := make([]*kelos.TaskSpawner, 0)
	for i := range spawnerList.Items {
		spawner := &spawnerList.Items[i]
		if ref, ok := provider.gatewayRef(spawner); ok && ref == nil {
			matching = append(matching, spawner)
		}
	}
	return matching, nil
}

// getMatchingSessionSpawners returns SessionSpawners configured for GitHub webhooks.
func (h *WebhookHandler) getMatchingSessionSpawners(ctx context.Context) ([]*kelos.SessionSpawner, error) {
	var spawnerList kelos.SessionSpawnerList
	if err := h.client.List(ctx, &spawnerList); err != nil {
		// Kelos installs missing CRDs after rolling out controller resources so
		// existing conversion webhooks remain available during upgrades. Until
		// the SessionSpawner CRD is installed, keep processing TaskSpawners.
		if apiMeta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	matching := make([]*kelos.SessionSpawner, 0, len(spawnerList.Items))
	for i := range spawnerList.Items {
		githubWebhook := spawnerList.Items[i].Spec.When.GitHubWebhook
		if githubWebhook != nil && githubWebhook.GatewayRef == nil {
			matching = append(matching, &spawnerList.Items[i])
		}
	}
	return matching, nil
}

// createTask creates a Task from the webhook event. It returns true when a
// new Task was created, and false when the delivery was deduplicated against a
// Task this spawner already owns.
func (h *WebhookHandler) createTask(ctx context.Context, provider webhookProvider, spawner *kelos.TaskSpawner, eventType string, parsed *ParsedWebhook, deliveryID string) (bool, error) {
	log := h.log.WithValues("spawner", spawner.Name, "namespace", spawner.Namespace, "eventType", eventType, "deliveryID", deliveryID)

	templateVars := provider.templateVars(spawner, eventType, parsed)
	log.Info("Extracted template variables", "ID", templateVars["ID"], "Title", templateVars["Title"], "Action", templateVars["Action"])

	// Pre-Create deduplication: when a deterministic nameTemplate is configured,
	// the name is context-independent, so resolve it and skip if this spawner
	// already owns a Task with it — before fetching context sources or building.
	// This avoids failing a duplicate delivery (and making external calls) just
	// because a context source is temporarily unavailable. A post-Create check
	// below still handles races where the Task is created concurrently.
	if spawner.Spec.TaskTemplate.NameTemplate != "" {
		resolvedName, err := taskbuilder.ResolveTaskName("", &spawner.Spec.TaskTemplate, templateVars)
		if err != nil {
			return false, fmt.Errorf("resolving task name: %w", err)
		}
		existing := &kelos.Task{}
		switch getErr := h.client.Get(ctx, client.ObjectKey{Namespace: spawner.Namespace, Name: resolvedName}, existing); {
		case getErr == nil:
			if taskbuilder.TaskBelongsToSpawner(existing, spawner.Name, spawner.UID) {
				log.Info("Task already exists for spawner, skipping duplicate", "task", resolvedName)
				return false, nil
			}
			return false, fmt.Errorf("task %s name collides with an existing Task not owned by spawner %s", resolvedName, spawner.Name)
		case !apierrors.IsNotFound(getErr):
			return false, fmt.Errorf("reading existing Task %s: %w", resolvedName, getErr)
		}
	}

	// Enrich with external context sources
	if len(spawner.Spec.TaskTemplate.ContextSources) > 0 {
		fetcher := &contextfetch.Fetcher{
			Client:     h.client,
			HTTPClient: http.DefaultClient,
			Namespace:  spawner.Namespace,
			Logger:     log,
		}
		contextData, err := fetcher.FetchAll(ctx, spawner.Spec.TaskTemplate.ContextSources, templateVars)
		if err != nil {
			return false, fmt.Errorf("fetching context sources: %w", err)
		}
		templateVars["Context"] = contextData
	}

	// Default task name uses a hash of the delivery ID, so every delivery
	// produces a distinct Task. Configure taskTemplate.nameTemplate with a
	// deterministic value (e.g. "{{.Number}}") to deduplicate Tasks across
	// multiple deliveries for the same pull request. When nameTemplate is set,
	// BuildTask renders the name from it and this default is ignored.
	taskName := webhookSpawnName(spawner.Name, eventType, deliveryID)

	// Resolve GVK for the spawner owner reference
	gvks, _, err := h.client.Scheme().ObjectKinds(spawner)
	if err != nil || len(gvks) == 0 {
		return false, fmt.Errorf("failed to get GVK for TaskSpawner: %w", err)
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
		return false, fmt.Errorf("failed to build task: %w", err)
	}
	if err := h.taskBuilder.AssignSpawnerCredential(spawner, task); err != nil {
		return false, fmt.Errorf("assigning TaskSpawner credential: %w", err)
	}

	provider.annotate(task, spawner, eventType, parsed, h.gatewayName)

	if err := h.client.Create(ctx, task); err != nil {
		// A configured nameTemplate makes Task names deterministic, so a second
		// delivery for the same work item collides with the Task already created
		// for it. Treat that as a successful no-op — the intended deduplication
		// path — but only when the existing Task belongs to this spawner. A name
		// collision with an unrelated Task must surface as an error rather than
		// silently dropping this event.
		if apierrors.IsAlreadyExists(err) {
			existing := &kelos.Task{}
			if getErr := h.client.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Name}, existing); getErr != nil {
				return false, fmt.Errorf("failed to read existing Task %s after create conflict: %w", task.Name, getErr)
			}
			if taskbuilder.TaskBelongsToSpawner(existing, spawner.Name, spawner.UID) {
				log.Info("Task already exists for spawner, skipping duplicate", "task", task.Name)
				return false, nil
			}
			return false, fmt.Errorf("task %s name collides with an existing Task not owned by spawner %s", task.Name, spawner.Name)
		}
		return false, fmt.Errorf("failed to create task: %w", err)
	}

	return true, nil
}

// webhookSpawnName preserves the deterministic naming used for webhook-created
// Tasks and applies the same behavior to Sessions.
func webhookSpawnName(spawnerName, eventType, deliveryID string) string {
	sanitizedEventType := strings.ReplaceAll(eventType, "_", "-")
	sum := sha256.Sum256([]byte(deliveryID))
	shortHash := hex.EncodeToString(sum[:])[:12]
	name := fmt.Sprintf("%s-%s-%s", spawnerName, sanitizedEventType, shortHash)
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-.")
	}
	return name
}

func (h *WebhookHandler) processSessionSpawner(ctx context.Context, spawner *kelos.SessionSpawner, eventType string, eventData *GitHubEventData, deliveryID string) (bool, error) {
	githubWebhook := spawner.Spec.When.GitHubWebhook
	matches, err := h.matchesGitHubWebhook(ctx, githubWebhook, eventType, eventData, func(ctx context.Context, eventData *GitHubEventData) ([]string, error) {
		return h.enrichSessionSpawnerPRChangedFiles(ctx, spawner, eventData)
	})
	if err != nil {
		reason := "FilterEvaluationFailed"
		var changedFilesErr *githubChangedFilesFetchError
		if errors.As(err, &changedFilesErr) {
			reason = "ChangedFilesFetchFailed"
		}
		return false, h.recordSessionSpawnerFailure(ctx, spawner, reason, err)
	}
	if !matches {
		return false, nil
	}

	changedFiles := changedFilesForSpawner(githubWebhook, eventType, eventData)
	templateVars := ExtractGitHubWorkItem(eventData, changedFiles)
	sessionName := webhookSpawnName(spawner.Name, eventType, deliveryID)
	gvks, _, gvkErr := h.client.Scheme().ObjectKinds(spawner)
	if gvkErr != nil {
		err := fmt.Errorf("getting SessionSpawner GVK: %w", gvkErr)
		return false, h.recordSessionSpawnerFailure(ctx, spawner, "SessionBuildFailed", err)
	}
	if len(gvks) == 0 {
		err := errors.New("getting SessionSpawner GVK: no registered kind")
		return false, h.recordSessionSpawnerFailure(ctx, spawner, "SessionBuildFailed", err)
	}
	session, buildErr := sessionbuilder.Build(
		sessionName,
		spawner.Namespace,
		&spawner.Spec.SessionTemplate,
		templateVars,
		sessionbuilder.SpawnerRef{
			Name:       spawner.Name,
			UID:        spawner.UID,
			APIVersion: gvks[0].GroupVersion().String(),
			Kind:       gvks[0].Kind,
		},
	)
	if buildErr != nil {
		return false, h.recordSessionSpawnerFailure(ctx, spawner, "SessionBuildFailed", buildErr)
	}
	if credentialErr := sessionbuilder.AssignSpawnerCredential(spawner, session); credentialErr != nil {
		return false, h.recordSessionSpawnerFailure(ctx, spawner, "SessionBuildFailed", credentialErr)
	}
	if createErr := h.client.Create(ctx, session); createErr != nil {
		if apierrors.IsAlreadyExists(createErr) {
			if statusErr := h.recordSessionSpawnerSuccess(ctx, spawner, sessionName, "DeliveryAlreadyProcessed", "Session already exists for webhook delivery"); statusErr != nil {
				return false, statusErr
			}
			return true, nil
		}
		return false, h.recordSessionSpawnerFailure(ctx, spawner, "SessionCreateFailed", createErr)
	}
	if statusErr := h.recordSessionSpawnerSuccess(ctx, spawner, sessionName, "SessionCreated", "Created Session for matching webhook delivery"); statusErr != nil {
		return false, statusErr
	}
	return true, nil
}

func (h *WebhookHandler) matchesGitHubWebhook(
	ctx context.Context,
	githubWebhook *kelos.GitHubWebhook,
	eventType string,
	eventData *GitHubEventData,
	fetchChangedFiles func(context.Context, *GitHubEventData) ([]string, error),
) (bool, error) {
	if githubWebhook == nil || eventData == nil {
		return false, nil
	}
	if githubWebhook.Repository != "" && githubWebhook.Repository != eventData.Repository {
		return false, nil
	}

	matches, err := MatchesGitHubEvent(githubWebhook, eventType, eventData)
	if err != nil || matches || len(eventData.ChangedFiles) > 0 || !githubWebhookNeedsChangedFiles(githubWebhook, eventType, eventData) {
		return matches, err
	}

	files, err := fetchChangedFiles(ctx, eventData)
	if err != nil {
		return false, &githubChangedFilesFetchError{err: err}
	}
	eventData.ChangedFiles = files
	return MatchesGitHubEvent(githubWebhook, eventType, eventData)
}

type githubChangedFilesFetchError struct {
	err error
}

func (e *githubChangedFilesFetchError) Error() string {
	return fmt.Sprintf("fetching pull request changed files: %v", e.err)
}

func (e *githubChangedFilesFetchError) Unwrap() error {
	return e.err
}

func (h *WebhookHandler) recordSessionSpawnerSuccess(ctx context.Context, spawner *kelos.SessionSpawner, sessionName, reason, message string) error {
	return h.updateSessionSpawnerDeliveryStatus(ctx, spawner, sessionName, metav1.ConditionTrue, reason, message)
}

func (h *WebhookHandler) recordSessionSpawnerFailure(ctx context.Context, spawner *kelos.SessionSpawner, reason string, deliveryErr error) error {
	statusErr := h.updateSessionSpawnerDeliveryStatus(ctx, spawner, "", metav1.ConditionFalse, reason, deliveryErr.Error())
	if statusErr != nil {
		return errors.Join(deliveryErr, fmt.Errorf("updating SessionSpawner status: %w", statusErr))
	}
	return deliveryErr
}

func (h *WebhookHandler) updateSessionSpawnerDeliveryStatus(ctx context.Context, spawner *kelos.SessionSpawner, sessionName string, status metav1.ConditionStatus, reason, message string) error {
	key := client.ObjectKeyFromObject(spawner)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelos.SessionSpawner
		if err := h.client.Get(ctx, key, &current); err != nil {
			return err
		}
		original := current.DeepCopy()
		if sessionName != "" {
			current.Status.LastSessionName = sessionName
		}
		now := metav1.Now()
		current.Status.LastDeliveryTime = &now
		apiMeta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
			Type:               kelos.SessionSpawnerConditionLastDeliverySucceeded,
			Status:             status,
			ObservedGeneration: spawner.Generation,
			Reason:             reason,
			Message:            message,
		})
		return h.client.Status().Patch(ctx, &current, client.MergeFrom(original))
	})
}

// enrichPRChangedFiles fetches changed files for PR-related webhook events
// from the GitHub API. Returns nil for non-PR events.
func (h *WebhookHandler) enrichPRChangedFiles(ctx context.Context, spawner *kelos.TaskSpawner, eventData *GitHubEventData) ([]string, error) {
	if eventData.Number == 0 || eventData.Repository == "" {
		return nil, nil
	}
	return fetchPRChangedFiles(ctx, h.client, spawner, h.githubTokenResolver, h.githubAPIBaseURL, eventData.RepositoryOwner, eventData.RepositoryName, eventData.Number)
}

func (h *WebhookHandler) enrichSessionSpawnerPRChangedFiles(ctx context.Context, spawner *kelos.SessionSpawner, eventData *GitHubEventData) ([]string, error) {
	if eventData.Number == 0 || eventData.Repository == "" {
		return nil, nil
	}
	return fetchSessionSpawnerPRChangedFiles(ctx, h.client, spawner, h.githubTokenResolver, h.githubAPIBaseURL, eventData.RepositoryOwner, eventData.RepositoryName, eventData.Number)
}

// webhookSourceKind determines the reporting source kind from a GitHub webhook event.
func webhookSourceKind(eventType string, eventData *GitHubEventData) string {
	switch eventType {
	case "pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_target":
		return "pull-request"
	case "issue_comment":
		if eventData.PullRequestAPIURL != "" {
			return "pull-request"
		}
		return "issue"
	case "check_run":
		// A check_run is associated with a PR when the webhook payload links one.
		if eventData.Number > 0 {
			return "pull-request"
		}
		return "issue"
	default:
		return "issue"
	}
}
