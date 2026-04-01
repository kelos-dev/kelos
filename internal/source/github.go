package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.github.com"

	// maxPages limits the number of pages fetched from the GitHub API to prevent
	// unbounded API calls for repositories with many issues.
	maxPages = 10

	// maxCommentBytes limits the total size of concatenated comments per issue.
	maxCommentBytes = 64 * 1024
)

// GitHubSource discovers issues from a GitHub repository.
type GitHubSource struct {
	Owner             string
	Repo              string
	Types             []string
	Labels            []string
	ExcludeLabels     []string
	State             string
	Assignee          string
	Author            string
	ExcludeAuthors    []string
	Token             string
	BaseURL           string
	Client            *http.Client
	TriggerComment    string
	ExcludeComments   []string
	AllowedUsers      []string
	AllowedTeams      []string
	MinimumPermission string
	PriorityLabels    []string
}

type githubIssue struct {
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	HTMLURL     string        `json:"html_url"`
	Labels      []githubLabel `json:"labels"`
	User        githubUser    `json:"user"`
	PullRequest *struct{}     `json:"pull_request,omitempty"`
}

type githubLabel struct {
	Name string `json:"name"`
}

type githubComment struct {
	Body      string     `json:"body"`
	CreatedAt string     `json:"created_at"`
	User      githubUser `json:"user"`
}

func (s *GitHubSource) baseURL() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return defaultBaseURL
}

func (s *GitHubSource) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

// Discover fetches issues from GitHub and returns them as WorkItems.
func (s *GitHubSource) Discover(ctx context.Context) ([]WorkItem, error) {
	issues, err := s.fetchAllIssues(ctx)
	if err != nil {
		return nil, err
	}

	issues = s.filterItems(issues)

	policy := githubCommentPolicy{
		TriggerComment:    s.TriggerComment,
		ExcludeComments:   s.ExcludeComments,
		AllowedUsers:      s.AllowedUsers,
		AllowedTeams:      s.AllowedTeams,
		MinimumPermission: s.MinimumPermission,
	}
	needsCommentFilter := s.TriggerComment != "" || len(s.ExcludeComments) > 0
	var authorizer *githubCommentAuthorizer
	if needsCommentFilter {
		authorizer, err = newGitHubCommentAuthorizer(s.Owner, s.Repo, s.baseURL(), s.Token, s.httpClient(), policy)
		if err != nil {
			return nil, err
		}
	}

	var items []WorkItem
	for _, issue := range issues {
		var labels []string
		for _, l := range issue.Labels {
			labels = append(labels, l.Name)
		}

		rawComments, err := s.fetchComments(ctx, issue.Number)
		if err != nil {
			return nil, fmt.Errorf("fetching comments for issue #%d: %w", issue.Number, err)
		}

		comments := concatCommentBodies(rawComments)

		var triggerTime time.Time
		if needsCommentFilter {
			commentAllowed, resolvedTriggerTime, err := evaluateGitHubCommentPolicy(ctx, issue.Body, issue.User, rawComments, policy, authorizer)
			if err != nil {
				return nil, fmt.Errorf("evaluating comment policy for issue #%d: %w", issue.Number, err)
			}
			if !commentAllowed {
				continue
			}
			triggerTime = resolvedTriggerTime
		}

		kind := "Issue"
		if issue.PullRequest != nil {
			kind = "PR"
		}

		item := WorkItem{
			ID:       strconv.Itoa(issue.Number),
			Number:   issue.Number,
			Title:    issue.Title,
			Body:     issue.Body,
			URL:      issue.HTMLURL,
			Labels:   labels,
			Comments: comments,
			Kind:     kind,
		}

		// Record the timestamp of the most recent trigger comment so the
		// spawner can retrigger completed tasks when a new trigger arrives.
		if s.TriggerComment != "" {
			item.TriggerTime = triggerTime
		}

		items = append(items, item)
	}

	return items, nil
}

// containsAnyCommand reports whether body contains any of the given commands.
func containsAnyCommand(body string, cmds []string) bool {
	for _, cmd := range cmds {
		if containsCommand(body, cmd) {
			return true
		}
	}
	return false
}

// containsCommand reports whether body contains the given command string.
// The command must appear at the start of a line to avoid false matches
// inside prose.
func containsCommand(body, cmd string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == cmd {
			return true
		}
	}
	return false
}

func (s *GitHubSource) resolvedTypes() map[string]struct{} {
	types := s.Types
	if len(types) == 0 {
		types = []string{"issues"}
	}
	m := make(map[string]struct{}, len(types))
	for _, t := range types {
		m[t] = struct{}{}
	}
	return m
}

func (s *GitHubSource) filterItems(issues []githubIssue) []githubIssue {
	types := s.resolvedTypes()

	excluded := make(map[string]struct{}, len(s.ExcludeLabels))
	for _, l := range s.ExcludeLabels {
		excluded[l] = struct{}{}
	}

	excludedAuthors := make(map[string]struct{}, len(s.ExcludeAuthors))
	for _, a := range s.ExcludeAuthors {
		excludedAuthors[a] = struct{}{}
	}

	filtered := make([]githubIssue, 0, len(issues))
	for _, issue := range issues {
		// Type filtering
		if issue.PullRequest != nil {
			if _, ok := types["pulls"]; !ok {
				continue
			}
		} else {
			if _, ok := types["issues"]; !ok {
				continue
			}
		}

		// Exclude-author filtering
		if _, ok := excludedAuthors[issue.User.Login]; ok {
			continue
		}

		// Exclude-label filtering
		skip := false
		for _, l := range issue.Labels {
			if _, ok := excluded[l.Name]; ok {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

func (s *GitHubSource) fetchAllIssues(ctx context.Context) ([]githubIssue, error) {
	var allIssues []githubIssue

	pageURL := s.buildIssuesURL()

	for page := 0; pageURL != "" && page < maxPages; page++ {
		issues, nextURL, err := s.fetchIssuesPage(ctx, pageURL)
		if err != nil {
			return nil, err
		}
		allIssues = append(allIssues, issues...)
		pageURL = nextURL
	}

	return allIssues, nil
}

func (s *GitHubSource) buildIssuesURL() string {
	u := fmt.Sprintf("%s/repos/%s/%s/issues", s.baseURL(), s.Owner, s.Repo)

	params := url.Values{}
	params.Set("per_page", "100")

	state := s.State
	if state == "" {
		state = "open"
	}
	params.Set("state", state)

	if len(s.Labels) > 0 {
		params.Set("labels", strings.Join(s.Labels, ","))
	}

	if s.Assignee != "" {
		params.Set("assignee", s.Assignee)
	}

	if s.Author != "" {
		params.Set("creator", s.Author)
	}

	return u + "?" + params.Encode()
}

func (s *GitHubSource) fetchIssuesPage(ctx context.Context, pageURL string) ([]githubIssue, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("creating request: %w", err)
	}

	if s.Token != "" {
		req.Header.Set("Authorization", "token "+s.Token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetching issues: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var issues []githubIssue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, "", fmt.Errorf("decoding issues: %w", err)
	}

	nextURL := parseNextLink(resp.Header.Get("Link"))

	return issues, nextURL, nil
}

func (s *GitHubSource) fetchComments(ctx context.Context, issueNumber int) ([]githubComment, error) {
	var allComments []githubComment

	pageURL := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100",
		s.baseURL(), s.Owner, s.Repo, issueNumber)

	for page := 0; pageURL != "" && page < maxPages; page++ {
		comments, nextURL, err := s.fetchCommentsPage(ctx, pageURL)
		if err != nil {
			return nil, err
		}
		allComments = append(allComments, comments...)
		pageURL = nextURL
	}

	return allComments, nil
}

func (s *GitHubSource) fetchCommentsPage(ctx context.Context, pageURL string) ([]githubComment, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("creating request: %w", err)
	}

	if s.Token != "" {
		req.Header.Set("Authorization", "token "+s.Token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetching comments: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var comments []githubComment
	if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
		return nil, "", fmt.Errorf("decoding comments: %w", err)
	}

	nextURL := parseNextLink(resp.Header.Get("Link"))

	return comments, nextURL, nil
}

// concatCommentBodies joins comment bodies into a single string separated by
// "\n---\n". When the total size exceeds maxCommentBytes, older comments are
// dropped from the front so that the most recent (and most relevant) comments
// are preserved.
func concatCommentBodies(comments []githubComment) string {
	totalBytes := 0
	for _, c := range comments {
		totalBytes += len(c.Body)
	}

	// If within budget, return all comments.
	if totalBytes <= maxCommentBytes {
		parts := make([]string, len(comments))
		for i, c := range comments {
			parts[i] = c.Body
		}
		return strings.Join(parts, "\n---\n")
	}

	// Truncate from the front: keep the most recent comments.
	var parts []string
	remaining := maxCommentBytes
	for i := len(comments) - 1; i >= 0; i-- {
		if remaining-len(comments[i].Body) < 0 {
			break
		}
		remaining -= len(comments[i].Body)
		parts = append(parts, comments[i].Body)
	}

	// Reverse so comments are back in chronological order.
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "\n---\n")
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func parseNextLink(header string) string {
	matches := linkNextRe.FindStringSubmatch(header)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
