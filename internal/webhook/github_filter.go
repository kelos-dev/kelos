package webhook

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/go-github/v66/github"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/source"
)

var filterLog = ctrl.Log.WithName("webhook-filter")

// GitHubEventData holds parsed GitHub event information for template rendering.
type GitHubEventData struct {
	// Event type (e.g., "issues", "pull_request", "push")
	Event string
	// Action (e.g., "opened", "created", "submitted")
	Action string
	// Sender username
	Sender string
	// Git ref for push events
	Ref string
	// Repository information
	Repository      string // Full repository name (owner/repo)
	RepositoryOwner string // Repository owner
	RepositoryName  string // Repository name only
	// Raw parsed event payload for template access
	RawEvent interface{}
	// Standard template variables for compatibility
	ID     string
	Title  string
	Number int
	Body   string
	URL    string
	Branch string
	// Comment-specific fields for issue_comment, pull_request_review,
	// and pull_request_review_comment events.
	CommentBody string
	CommentURL  string
	// ChangedFiles lists file paths modified by the event.
	// For push events, populated from the payload. For PR events, lazily
	// fetched from the GitHub API when a webhook filter uses FilePatterns.
	// NOTE: intentionally not exposed in ExtractGitHubWorkItem template vars
	// yet — the {{.ChangedFiles}} template variable is deferred to a follow-up
	// to resolve API design questions (slice vs pre-joined string, fetch gating).
	ChangedFiles []string
	// PullRequestAPIURL is the GitHub API URL for the pull request associated
	// with an issue_comment event. It is extracted from issue.pull_request.url
	// and used to lazily fetch the PR's head branch when needed.
	PullRequestAPIURL string
}

// ParseGitHubWebhook parses a GitHub webhook payload using the go-github SDK.
func ParseGitHubWebhook(eventType string, payload []byte) (*GitHubEventData, error) {
	event, err := github.ParseWebHook(eventType, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to parse GitHub webhook: %w", err)
	}

	data := &GitHubEventData{
		Event:    eventType,
		RawEvent: event,
	}

	// Extract repository information from any event that has it
	switch e := event.(type) {
	case *github.IssuesEvent:
		if repo := e.GetRepo(); repo != nil {
			data.Repository = repo.GetFullName()
			data.RepositoryOwner = repo.GetOwner().GetLogin()
			data.RepositoryName = repo.GetName()
		}
	case *github.PullRequestEvent:
		if repo := e.GetRepo(); repo != nil {
			data.Repository = repo.GetFullName()
			data.RepositoryOwner = repo.GetOwner().GetLogin()
			data.RepositoryName = repo.GetName()
		}
	case *github.IssueCommentEvent:
		if repo := e.GetRepo(); repo != nil {
			data.Repository = repo.GetFullName()
			data.RepositoryOwner = repo.GetOwner().GetLogin()
			data.RepositoryName = repo.GetName()
		}
	case *github.PullRequestReviewEvent:
		if repo := e.GetRepo(); repo != nil {
			data.Repository = repo.GetFullName()
			data.RepositoryOwner = repo.GetOwner().GetLogin()
			data.RepositoryName = repo.GetName()
		}
	case *github.PullRequestReviewCommentEvent:
		if repo := e.GetRepo(); repo != nil {
			data.Repository = repo.GetFullName()
			data.RepositoryOwner = repo.GetOwner().GetLogin()
			data.RepositoryName = repo.GetName()
		}
	case *github.PushEvent:
		if pushRepo := e.GetRepo(); pushRepo != nil {
			data.Repository = pushRepo.GetFullName()
			if owner := pushRepo.GetOwner(); owner != nil {
				data.RepositoryOwner = owner.GetLogin()
			}
			data.RepositoryName = pushRepo.GetName()
		}
	}

	// Extract common fields based on event type
	switch e := event.(type) {
	case *github.IssuesEvent:
		data.Action = e.GetAction()
		data.Sender = e.GetSender().GetLogin()
		if issue := e.GetIssue(); issue != nil {
			data.ID = fmt.Sprintf("%d", issue.GetNumber())
			data.Title = issue.GetTitle()
			data.Number = issue.GetNumber()
			data.Body = issue.GetBody()
			data.URL = issue.GetHTMLURL()
		}

	case *github.PullRequestEvent:
		data.Action = e.GetAction()
		data.Sender = e.GetSender().GetLogin()
		if pr := e.GetPullRequest(); pr != nil {
			data.ID = fmt.Sprintf("%d", pr.GetNumber())
			data.Title = pr.GetTitle()
			data.Number = pr.GetNumber()
			data.Body = pr.GetBody()
			data.URL = pr.GetHTMLURL()
			if head := pr.GetHead(); head != nil {
				data.Branch = head.GetRef()
			}
		}

	case *github.IssueCommentEvent:
		data.Action = e.GetAction()
		data.Sender = e.GetSender().GetLogin()
		if comment := e.GetComment(); comment != nil {
			data.CommentBody = comment.GetBody()
			data.CommentURL = comment.GetHTMLURL()
		}
		if issue := e.GetIssue(); issue != nil {
			data.ID = fmt.Sprintf("%d", issue.GetNumber())
			data.Title = issue.GetTitle()
			data.Number = issue.GetNumber()
			data.Body = issue.GetBody()
			data.URL = issue.GetHTMLURL()
			// When the comment is on a pull request, store the API URL so the
			// handler can lazily fetch the PR's head branch.
			if issue.IsPullRequest() {
				if links := issue.GetPullRequestLinks(); links != nil {
					data.PullRequestAPIURL = links.GetURL()
				}
			}
		}

	case *github.PullRequestReviewEvent:
		data.Action = e.GetAction()
		data.Sender = e.GetSender().GetLogin()
		if review := e.GetReview(); review != nil {
			data.CommentBody = review.GetBody()
			data.CommentURL = review.GetHTMLURL()
		}
		if pr := e.GetPullRequest(); pr != nil {
			data.ID = fmt.Sprintf("%d", pr.GetNumber())
			data.Title = pr.GetTitle()
			data.Number = pr.GetNumber()
			data.Body = pr.GetBody()
			data.URL = pr.GetHTMLURL()
			if head := pr.GetHead(); head != nil {
				data.Branch = head.GetRef()
			}
		}

	case *github.PullRequestReviewCommentEvent:
		data.Action = e.GetAction()
		data.Sender = e.GetSender().GetLogin()
		if comment := e.GetComment(); comment != nil {
			data.CommentBody = comment.GetBody()
			data.CommentURL = comment.GetHTMLURL()
		}
		if pr := e.GetPullRequest(); pr != nil {
			data.ID = fmt.Sprintf("%d", pr.GetNumber())
			data.Title = pr.GetTitle()
			data.Number = pr.GetNumber()
			data.Body = pr.GetBody()
			data.URL = pr.GetHTMLURL()
			if head := pr.GetHead(); head != nil {
				data.Branch = head.GetRef()
			}
		}

	case *github.PushEvent:
		data.Sender = e.GetSender().GetLogin()
		data.Ref = e.GetRef()
		// Extract branch name from refs/heads/branch-name
		if strings.HasPrefix(data.Ref, "refs/heads/") {
			data.Branch = strings.TrimPrefix(data.Ref, "refs/heads/")
		}
		if hc := e.GetHeadCommit(); hc != nil {
			data.ID = hc.GetID()
		}
		data.Title = fmt.Sprintf("Push to %s", data.Branch)
		data.ChangedFiles = extractPushEventFiles(e)

	default:
		// For other event types, try to extract sender from raw JSON
		var raw map[string]interface{}
		if err := json.Unmarshal(payload, &raw); err == nil {
			if sender, ok := raw["sender"].(map[string]interface{}); ok {
				if login, ok := sender["login"].(string); ok {
					data.Sender = login
				}
			}
			if action, ok := raw["action"].(string); ok {
				data.Action = action
			}
		}
	}

	return data, nil
}

// MatchesGitHubEvent evaluates whether a GitHub webhook event matches the spawner's filters.
// It accepts pre-parsed event data to avoid redundant parsing.
func MatchesGitHubEvent(spawner *v1alpha1.GitHubWebhook, eventType string, eventData *GitHubEventData) (bool, error) {
	// Check if event type is in the allowed list
	eventAllowed := false
	for _, allowedEvent := range spawner.Events {
		if allowedEvent == eventType {
			eventAllowed = true
			break
		}
	}
	if !eventAllowed {
		return false, nil
	}

	// Check top-level excluded authors before evaluating filters
	if len(spawner.ExcludeAuthors) > 0 && eventData.Sender != "" {
		for _, excluded := range spawner.ExcludeAuthors {
			if excluded == eventData.Sender {
				return false, nil
			}
		}
	}

	// If no filters, all events of the allowed types match
	if len(spawner.Filters) == 0 {
		return true, nil
	}

	// Apply filters with OR semantics for the same event type
	for _, filter := range spawner.Filters {
		if filter.Event != eventType {
			continue
		}

		if matchesFilter(filter, eventData) {
			return true, nil
		}
	}

	return false, nil
}

// matchesFilter checks if event data matches a specific filter.
func matchesFilter(filter v1alpha1.GitHubWebhookFilter, eventData *GitHubEventData) bool {
	// Action filter
	if filter.Action != "" && filter.Action != eventData.Action {
		return false
	}

	// Author filter
	if filter.Author != "" && filter.Author != eventData.Sender {
		return false
	}

	// ExcludeAuthors filter
	if len(filter.ExcludeAuthors) > 0 && eventData.Sender != "" {
		for _, excluded := range filter.ExcludeAuthors {
			if excluded == eventData.Sender {
				return false
			}
		}
	}

	// Branch filter (for push events)
	if filter.Branch != "" {
		if eventData.Branch == "" {
			return false
		}
		matched, _ := filepath.Match(filter.Branch, eventData.Branch)
		if !matched {
			return false
		}
	}

	// File patterns filter
	if filter.FilePatterns != nil {
		if !matchesWebhookFilePatterns(eventData.ChangedFiles, filter.FilePatterns) {
			return false
		}
	}

	// Event-specific filters
	switch e := eventData.RawEvent.(type) {
	case *github.IssuesEvent, *github.IssueCommentEvent:
		var issue *github.Issue
		if issueEvent, ok := e.(*github.IssuesEvent); ok {
			issue = issueEvent.GetIssue()
		} else if commentEvent, ok := e.(*github.IssueCommentEvent); ok {
			issue = commentEvent.GetIssue()
		}

		// CommentOn filter scopes issue_comment events to issues vs PRs.
		// GitHub fires issue_comment for both; the issue payload's
		// pull_request field is non-nil only when the comment is on a PR.
		if filter.CommentOn != "" {
			if _, ok := e.(*github.IssueCommentEvent); ok && issue != nil {
				isPR := issue.IsPullRequest()
				switch filter.CommentOn {
				case v1alpha1.CommentOnIssue:
					if isPR {
						return false
					}
				case v1alpha1.CommentOnPullRequest:
					if !isPR {
						return false
					}
				}
			}
		}

		if issue != nil {
			// State filter
			if filter.State != "" && filter.State != issue.GetState() {
				return false
			}

			// Labels filter (all required labels must be present)
			if len(filter.Labels) > 0 {
				issueLabels := make(map[string]bool)
				for _, label := range issue.Labels {
					issueLabels[label.GetName()] = true
				}
				for _, requiredLabel := range filter.Labels {
					if !issueLabels[requiredLabel] {
						return false
					}
				}
			}

			// ExcludeLabels filter (issue must NOT have any of these labels)
			if len(filter.ExcludeLabels) > 0 {
				issueLabels := make(map[string]bool)
				for _, label := range issue.Labels {
					issueLabels[label.GetName()] = true
				}
				for _, excludeLabel := range filter.ExcludeLabels {
					if issueLabels[excludeLabel] {
						return false
					}
				}
			}
		}

		// BodyContains filter
		if filter.BodyContains != "" {
			if issueEvent, ok := e.(*github.IssuesEvent); ok {
				if issue := issueEvent.GetIssue(); issue != nil {
					if !strings.Contains(issue.GetBody(), filter.BodyContains) {
						return false
					}
				}
			} else if commentEvent, ok := e.(*github.IssueCommentEvent); ok {
				if comment := commentEvent.GetComment(); comment != nil {
					if !strings.Contains(comment.GetBody(), filter.BodyContains) {
						return false
					}
				}
			}
		}

		// BodyPattern filter
		if filter.BodyPattern != "" {
			var body string
			if issueEvent, ok := e.(*github.IssuesEvent); ok {
				if issue := issueEvent.GetIssue(); issue != nil {
					body = issue.GetBody()
				}
			} else if commentEvent, ok := e.(*github.IssueCommentEvent); ok {
				if comment := commentEvent.GetComment(); comment != nil {
					body = comment.GetBody()
				}
			}
			matched, err := matchesPattern(body, filter.BodyPattern)
			if err != nil {
				filterLog.Error(err, "Invalid bodyPattern regex, rejecting event", "pattern", filter.BodyPattern)
				return false
			}
			if !matched {
				return false
			}
		}

		// ExcludeBodyPatterns filter (body must not match any pattern)
		if len(filter.ExcludeBodyPatterns) > 0 {
			var body string
			if issueEvent, ok := e.(*github.IssuesEvent); ok {
				if issue := issueEvent.GetIssue(); issue != nil {
					body = issue.GetBody()
				}
			} else if commentEvent, ok := e.(*github.IssueCommentEvent); ok {
				if comment := commentEvent.GetComment(); comment != nil {
					body = comment.GetBody()
				}
			}
			matched, err := matchesAnyPattern(body, filter.ExcludeBodyPatterns)
			if err != nil {
				filterLog.Error(err, "Invalid excludeBodyPatterns regex, rejecting event")
				return false
			}
			if matched {
				return false
			}
		}

	case *github.PullRequestEvent, *github.PullRequestReviewEvent, *github.PullRequestReviewCommentEvent:
		var pr *github.PullRequest
		switch event := e.(type) {
		case *github.PullRequestEvent:
			pr = event.GetPullRequest()
		case *github.PullRequestReviewEvent:
			pr = event.GetPullRequest()
		case *github.PullRequestReviewCommentEvent:
			pr = event.GetPullRequest()
		}

		if pr != nil {
			// State filter
			if filter.State != "" && filter.State != pr.GetState() {
				return false
			}

			// Draft filter
			if filter.Draft != nil && *filter.Draft != pr.GetDraft() {
				return false
			}

			// Labels filter (all required labels must be present)
			if len(filter.Labels) > 0 {
				prLabels := make(map[string]bool)
				for _, label := range pr.Labels {
					prLabels[label.GetName()] = true
				}
				for _, requiredLabel := range filter.Labels {
					if !prLabels[requiredLabel] {
						return false
					}
				}
			}

			// ExcludeLabels filter (PR must NOT have any of these labels)
			if len(filter.ExcludeLabels) > 0 {
				prLabels := make(map[string]bool)
				for _, label := range pr.Labels {
					prLabels[label.GetName()] = true
				}
				for _, excludeLabel := range filter.ExcludeLabels {
					if prLabels[excludeLabel] {
						return false
					}
				}
			}

			// BodyContains filter for PRs and reviews
			if filter.BodyContains != "" {
				if _, ok := e.(*github.PullRequestEvent); ok {
					if !strings.Contains(pr.GetBody(), filter.BodyContains) {
						return false
					}
				} else if reviewEvent, ok := e.(*github.PullRequestReviewEvent); ok {
					if review := reviewEvent.GetReview(); review != nil {
						if !strings.Contains(review.GetBody(), filter.BodyContains) {
							return false
						}
					}
				} else if commentEvent, ok := e.(*github.PullRequestReviewCommentEvent); ok {
					if comment := commentEvent.GetComment(); comment != nil {
						if !strings.Contains(comment.GetBody(), filter.BodyContains) {
							return false
						}
					}
				}
			}

			// BodyPattern filter for PRs and reviews
			if filter.BodyPattern != "" {
				var body string
				if _, ok := e.(*github.PullRequestEvent); ok {
					body = pr.GetBody()
				} else if reviewEvent, ok := e.(*github.PullRequestReviewEvent); ok {
					if review := reviewEvent.GetReview(); review != nil {
						body = review.GetBody()
					}
				} else if commentEvent, ok := e.(*github.PullRequestReviewCommentEvent); ok {
					if comment := commentEvent.GetComment(); comment != nil {
						body = comment.GetBody()
					}
				}
				matched, err := matchesPattern(body, filter.BodyPattern)
				if err != nil {
					filterLog.Error(err, "Invalid bodyPattern regex, rejecting event", "pattern", filter.BodyPattern)
					return false
				}
				if !matched {
					return false
				}
			}

			// ExcludeBodyPatterns filter for PRs and reviews
			if len(filter.ExcludeBodyPatterns) > 0 {
				var body string
				if _, ok := e.(*github.PullRequestEvent); ok {
					body = pr.GetBody()
				} else if reviewEvent, ok := e.(*github.PullRequestReviewEvent); ok {
					if review := reviewEvent.GetReview(); review != nil {
						body = review.GetBody()
					}
				} else if commentEvent, ok := e.(*github.PullRequestReviewCommentEvent); ok {
					if comment := commentEvent.GetComment(); comment != nil {
						body = comment.GetBody()
					}
				}
				matched, err := matchesAnyPattern(body, filter.ExcludeBodyPatterns)
				if err != nil {
					filterLog.Error(err, "Invalid excludeBodyPatterns regex, rejecting event")
					return false
				}
				if matched {
					return false
				}
			}
		}
	}

	return true
}

// matchesPattern returns true if body matches the given regular expression.
func matchesPattern(body string, pattern string) (bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("Invalid body pattern %q: %w", pattern, err)
	}
	return re.MatchString(body), nil
}

// matchesAnyPattern returns true if body matches at least one of the regular expressions.
func matchesAnyPattern(body string, patterns []string) (bool, error) {
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return false, fmt.Errorf("Invalid body pattern %q: %w", p, err)
		}
		if re.MatchString(body) {
			return true, nil
		}
	}
	return false, nil
}

// needsBranchEnrichment returns true if the event is an issue_comment on a pull
// request and the Branch field has not been populated yet.
func needsBranchEnrichment(eventData *GitHubEventData) bool {
	return eventData.Branch == "" && eventData.PullRequestAPIURL != ""
}

// ExtractGitHubWorkItem extracts template variables from GitHub webhook events for task creation.
func ExtractGitHubWorkItem(eventData *GitHubEventData) map[string]interface{} {
	vars := map[string]interface{}{
		"Event":           eventData.Event,
		"Action":          eventData.Action,
		"Sender":          eventData.Sender,
		"Ref":             eventData.Ref,
		"Repository":      eventData.Repository,
		"RepositoryOwner": eventData.RepositoryOwner,
		"RepositoryName":  eventData.RepositoryName,
		"Payload":         eventData.RawEvent,
		// Standard variables for compatibility
		"ID":    eventData.ID,
		"Title": eventData.Title,
		"Kind":  "webhook",
	}

	// Add number, body, URL if available
	if eventData.Number > 0 {
		vars["Number"] = eventData.Number
	}
	if eventData.Body != "" {
		vars["Body"] = eventData.Body
	}
	if eventData.URL != "" {
		vars["URL"] = eventData.URL
	}
	if eventData.Branch != "" {
		vars["Branch"] = eventData.Branch
	}
	if eventData.CommentBody != "" {
		vars["CommentBody"] = eventData.CommentBody
	}
	if eventData.CommentURL != "" {
		vars["CommentURL"] = eventData.CommentURL
	}

	return vars
}

// extractPushEventFiles collects all changed file paths from a push event's commits.
func extractPushEventFiles(e *github.PushEvent) []string {
	seen := make(map[string]struct{})
	var files []string
	for _, commit := range e.Commits {
		for _, batch := range [][]string{commit.Added, commit.Removed, commit.Modified} {
			for _, f := range batch {
				if _, ok := seen[f]; !ok {
					seen[f] = struct{}{}
					files = append(files, f)
				}
			}
		}
	}
	return files
}

// matchesWebhookFilePatterns checks whether the given changed files match the
// filter's FilePatterns using the shared source.MatchesFilePaths logic.
func matchesWebhookFilePatterns(files []string, patterns *v1alpha1.FilePatterns) bool {
	if patterns == nil {
		return true
	}
	return source.MatchesFilePaths(files, patterns.Include, patterns.Exclude)
}
