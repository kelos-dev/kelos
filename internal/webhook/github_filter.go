package webhook

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/go-github/v66/github"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

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

	// Event-specific filters
	switch e := eventData.RawEvent.(type) {
	case *github.IssuesEvent, *github.IssueCommentEvent:
		var issue *github.Issue
		if issueEvent, ok := e.(*github.IssuesEvent); ok {
			issue = issueEvent.GetIssue()
		} else if commentEvent, ok := e.(*github.IssueCommentEvent); ok {
			issue = commentEvent.GetIssue()
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
		}
	}

	return true
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

	return vars
}
