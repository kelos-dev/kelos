package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// GitHubWebhookSource discovers work items from GitHub webhook events stored
// as WebhookEvent custom resources. This replaces polling the GitHub API with
// push-based webhook notifications.
type GitHubWebhookSource struct {
	Client    client.Client
	Namespace string

	// SpawnerName is the name of the TaskSpawner using this source.
	// Used for per-spawner processed tracking so multiple spawners can
	// independently react to the same webhook event.
	SpawnerName string
	// Labels filters issues/PRs by labels (applied client-side to webhook payloads)
	Labels []string
	// ExcludeLabels filters out items with these labels (applied client-side)
	ExcludeLabels []string
	// Author filters issues/PRs by the username of the creator
	Author string
	// State filters issues/PRs by state (open, closed, all). Defaults to open.
	State string
	// Actions filters webhook events by action (e.g., "opened", "reopened", "labeled")
	Actions []string
	// Draft filters pull requests by draft state
	Draft *bool
	// PriorityLabels defines label-based priority ordering for discovered items
	PriorityLabels []string

	// pendingEvents tracks webhook events that passed filters during Discover
	// but have not yet been acknowledged by the caller. Keyed by work item ID.
	pendingEvents map[string][]*kelosv1alpha1.WebhookEvent

	// TriggerComment requires a matching command in an issue_comment event.
	// When set, only issue_comment events with a matching command are
	// discovered; issues/pull_request events are skipped unless the body
	// itself contains the command.
	TriggerComment string
	// ExcludeComments blocks issue_comment events whose comment body matches.
	ExcludeComments []string
	// AllowedUsers restricts comment-based triggering to these usernames.
	AllowedUsers []string
	// AllowedTeams restricts comment-based triggering to members of these
	// GitHub teams (org/team-slug format). Requires Token.
	AllowedTeams []string
	// MinimumPermission restricts comment-based triggering to users with at
	// least this repository permission level. Requires Token and Owner/Repo.
	MinimumPermission string

	// Owner and Repo are the GitHub repository coordinates, needed for
	// team membership and permission checks when AllowedTeams or
	// MinimumPermission are configured.
	Owner   string
	Repo    string
	Token   string
	BaseURL string
	// HTTPClient is an optional HTTP client for GitHub API calls.
	HTTPClient *http.Client
}

// GitHubWebhookPayload represents the relevant fields from a GitHub webhook payload.
// This handles issue, pull_request, and issue_comment events.
type GitHubWebhookPayload struct {
	Action string `json:"action"` // "opened", "reopened", "labeled", "created", etc.
	Issue  *struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"` // "open" or "closed"
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		PullRequest *struct{} `json:"pull_request,omitempty"` // non-nil when issue is a PR
	} `json:"issue,omitempty"`
	PullRequest *struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"` // "open" or "closed"
		Draft   bool   `json:"draft"`
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request,omitempty"`
	// Comment is populated for issue_comment events.
	Comment *struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment,omitempty"`
}

// Discover fetches unprocessed GitHub webhook events and converts them to WorkItems.
func (s *GitHubWebhookSource) Discover(ctx context.Context) ([]WorkItem, error) {
	var eventList kelosv1alpha1.WebhookEventList

	// List all webhook events in namespace
	// Field selectors are not supported by fake clients in tests, so filter client-side
	if err := s.Client.List(ctx, &eventList,
		client.InNamespace(s.Namespace),
	); err != nil {
		return nil, fmt.Errorf("listing webhook events: %w", err)
	}

	// Build authorizer lazily (only if comment policy needs API calls)
	var authorizer *githubCommentAuthorizer
	needsAuthorizer := len(s.AllowedTeams) > 0 || s.MinimumPermission != ""
	if needsAuthorizer && s.Owner != "" && s.Repo != "" {
		var err error
		policy := githubCommentPolicy{
			AllowedUsers:      s.AllowedUsers,
			AllowedTeams:      s.AllowedTeams,
			MinimumPermission: s.MinimumPermission,
		}
		authorizer, err = newGitHubCommentAuthorizer(s.Owner, s.Repo, s.baseURL(), s.Token, s.httpClient(), policy)
		if err != nil {
			return nil, fmt.Errorf("building comment authorizer: %w", err)
		}
	}

	var items []WorkItem
	s.pendingEvents = make(map[string][]*kelosv1alpha1.WebhookEvent)

	for i := range eventList.Items {
		event := eventList.Items[i].DeepCopy()

		// Filter by source; skip if already processed by this spawner
		if event.Spec.Source != "github" || s.alreadyProcessed(event) {
			continue
		}

		// Parse webhook payload
		var payload GitHubWebhookPayload
		if err := json.Unmarshal(event.Spec.Payload, &payload); err != nil {
			// Skip malformed payloads
			continue
		}

		// Convert to WorkItem (handles comment policy filtering)
		item, ok := s.payloadToWorkItem(ctx, payload, authorizer)
		if !ok {
			s.markProcessed(ctx, event)
			continue
		}

		// Apply label filters
		if !s.matchesLabels(item.Labels) {
			s.markProcessed(ctx, event)
			continue
		}

		items = append(items, item)

		// Defer marking as processed — the caller must acknowledge items
		// after task creation so that events skipped due to concurrency
		// or budget limits are rediscovered on the next cycle.
		s.pendingEvents[item.ID] = append(s.pendingEvents[item.ID], event)
	}

	SortByLabelPriority(items, s.PriorityLabels)

	return items, nil
}

// AcknowledgeItems marks the webhook events for the given work item IDs
// as processed by this spawner. This should be called after task creation
// or deduplication. Events for IDs not in the pending set are ignored.
func (s *GitHubWebhookSource) AcknowledgeItems(ctx context.Context, ids []string) {
	for _, id := range ids {
		events, ok := s.pendingEvents[id]
		if !ok {
			continue
		}
		for _, event := range events {
			s.markProcessed(ctx, event)
		}
		delete(s.pendingEvents, id)
	}
}

// alreadyProcessed returns true if this spawner has already processed the event.
func (s *GitHubWebhookSource) alreadyProcessed(event *kelosv1alpha1.WebhookEvent) bool {
	for _, name := range event.Status.ProcessedBy {
		if name == s.SpawnerName {
			return true
		}
	}
	return false
}

// markProcessed records that this spawner has processed the event.
func (s *GitHubWebhookSource) markProcessed(ctx context.Context, event *kelosv1alpha1.WebhookEvent) {
	event.Status.ProcessedBy = append(event.Status.ProcessedBy, s.SpawnerName)
	event.Status.Processed = true
	now := metav1.Now()
	event.Status.ProcessedAt = &now
	_ = s.Client.Status().Update(ctx, event)
}

// payloadToWorkItem converts a GitHub webhook payload to a WorkItem.
// Returns false if the payload should be skipped.
func (s *GitHubWebhookSource) payloadToWorkItem(ctx context.Context, payload GitHubWebhookPayload, authorizer *githubCommentAuthorizer) (WorkItem, bool) {
	// Filter by action if configured
	if len(s.Actions) > 0 && !containsString(s.Actions, payload.Action) {
		return WorkItem{}, false
	}

	// Handle issue_comment events (for trigger/exclude comment workflow)
	if payload.Comment != nil && payload.Issue != nil {
		return s.handleCommentEvent(ctx, payload, authorizer)
	}

	// When triggerComment is set, skip non-comment events unless the body
	// itself contains the trigger command.
	hasTrigger := s.TriggerComment != ""

	// Handle issue webhooks
	if payload.Issue != nil && payload.PullRequest == nil {
		issue := payload.Issue

		if !s.matchesState(issue.State) {
			return WorkItem{}, false
		}
		if s.Author != "" && issue.User.Login != s.Author {
			return WorkItem{}, false
		}

		// If trigger comment is configured, check the issue body
		if hasTrigger && !containsCommand(issue.Body, s.TriggerComment) {
			return WorkItem{}, false
		}

		labels := make([]string, len(issue.Labels))
		for i, l := range issue.Labels {
			labels[i] = l.Name
		}

		return WorkItem{
			ID:     fmt.Sprintf("issue-%d", issue.Number),
			Number: issue.Number,
			Title:  issue.Title,
			Body:   issue.Body,
			URL:    issue.HTMLURL,
			Labels: labels,
			Kind:   "Issue",
			Author: issue.User.Login,
			State:  issue.State,
			Action: payload.Action,
		}, true
	}

	// Handle pull request webhooks
	if payload.PullRequest != nil {
		pr := payload.PullRequest

		if !s.matchesState(pr.State) {
			return WorkItem{}, false
		}
		if s.Author != "" && pr.User.Login != s.Author {
			return WorkItem{}, false
		}
		if s.Draft != nil && pr.Draft != *s.Draft {
			return WorkItem{}, false
		}

		// If trigger comment is configured, check the PR body
		if hasTrigger && !containsCommand(pr.Body, s.TriggerComment) {
			return WorkItem{}, false
		}

		labels := make([]string, len(pr.Labels))
		for i, l := range pr.Labels {
			labels[i] = l.Name
		}

		return WorkItem{
			ID:     fmt.Sprintf("pr-%d", pr.Number),
			Number: pr.Number,
			Title:  pr.Title,
			Body:   pr.Body,
			URL:    pr.HTMLURL,
			Labels: labels,
			Kind:   "PR",
			Branch: pr.Head.Ref,
			Author: pr.User.Login,
			State:  pr.State,
			Action: payload.Action,
			Draft:  pr.Draft,
		}, true
	}

	return WorkItem{}, false
}

// handleCommentEvent processes an issue_comment webhook event.
// It extracts the associated issue/PR and applies comment policy filtering.
func (s *GitHubWebhookSource) handleCommentEvent(ctx context.Context, payload GitHubWebhookPayload, authorizer *githubCommentAuthorizer) (WorkItem, bool) {
	comment := payload.Comment
	issue := payload.Issue

	// Check exclude comments first
	if len(s.ExcludeComments) > 0 && containsAnyCommand(comment.Body, s.ExcludeComments) {
		return WorkItem{}, false
	}

	// Check trigger comment
	if s.TriggerComment != "" && !containsCommand(comment.Body, s.TriggerComment) {
		return WorkItem{}, false
	}

	// Check authorization (allowedUsers, allowedTeams, minimumPermission)
	if !s.isAuthorizedCommentUser(ctx, comment.User.Login, authorizer) {
		return WorkItem{}, false
	}

	if !s.matchesState(issue.State) {
		return WorkItem{}, false
	}
	if s.Author != "" && issue.User.Login != s.Author {
		return WorkItem{}, false
	}

	labels := make([]string, len(issue.Labels))
	for i, l := range issue.Labels {
		labels[i] = l.Name
	}

	// Determine if this is a PR or issue based on the pull_request field
	kind := "Issue"
	id := fmt.Sprintf("issue-%d", issue.Number)
	if issue.PullRequest != nil {
		kind = "PR"
		id = fmt.Sprintf("pr-%d", issue.Number)
	}

	return WorkItem{
		ID:     id,
		Number: issue.Number,
		Title:  issue.Title,
		Body:   issue.Body,
		URL:    issue.HTMLURL,
		Labels: labels,
		Kind:   kind,
		Author: issue.User.Login,
		State:  issue.State,
		Action: payload.Action,
	}, true
}

// isAuthorizedCommentUser checks if the comment author is authorized.
func (s *GitHubWebhookSource) isAuthorizedCommentUser(ctx context.Context, login string, authorizer *githubCommentAuthorizer) bool {
	// If no authorization is configured, allow all
	if len(s.AllowedUsers) == 0 && len(s.AllowedTeams) == 0 && s.MinimumPermission == "" {
		return true
	}

	// Check allowed users
	for _, allowed := range s.AllowedUsers {
		if allowed == login {
			return true
		}
	}

	// Check teams and permissions via authorizer (requires API calls)
	if authorizer != nil {
		authorized, _ := authorizer.isAuthorizedLogin(ctx, login)
		return authorized
	}

	// If only allowedUsers was set and didn't match, deny
	return false
}

// matchesState returns true if the item state matches the configured state filter.
// When state is empty or "all", all states match. Otherwise only the specified state matches.
func (s *GitHubWebhookSource) matchesState(itemState string) bool {
	state := s.State
	if state == "" {
		state = "open"
	}
	if state == "all" {
		return true
	}
	return itemState == state
}

// containsString returns true if the slice contains the given string.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// matchesLabels returns true if the item matches the configured label filters.
func (s *GitHubWebhookSource) matchesLabels(itemLabels []string) bool {
	// Check required labels (if configured)
	if len(s.Labels) > 0 {
		hasAllRequired := true
		for _, required := range s.Labels {
			found := false
			for _, label := range itemLabels {
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
		for _, label := range itemLabels {
			if label == excluded {
				return false
			}
		}
	}

	return true
}

func (s *GitHubWebhookSource) baseURL() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return defaultBaseURL
}

func (s *GitHubWebhookSource) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return http.DefaultClient
}
