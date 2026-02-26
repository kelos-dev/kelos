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
	Owner           string
	Repo            string
	Types           []string
	Labels          []string
	ExcludeLabels   []string
	State           string
	Assignee        string
	Author          string
	Token           string
	BaseURL         string
	Client          *http.Client
	TriggerComment  string
	ExcludeComments []string
	PriorityLabels  []string
}

type githubIssue struct {
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	HTMLURL     string        `json:"html_url"`
	Labels      []githubLabel `json:"labels"`
	PullRequest *struct{}     `json:"pull_request,omitempty"`
}

type githubLabel struct {
	Name string `json:"name"`
}

type githubComment struct {
	Body string `json:"body"`
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

	needsCommentFilter := s.TriggerComment != "" || len(s.ExcludeComments) > 0

	var items []WorkItem
	for _, issue := range issues {
		var labels []string
		for _, l := range issue.Labels {
			labels = append(labels, l.Name)
		}

		comments, err := s.fetchComments(ctx, issue.Number)
		if err != nil {
			return nil, fmt.Errorf("fetching comments for issue #%d: %w", issue.Number, err)
		}

		if needsCommentFilter && !s.passesCommentFilter(comments) {
			continue
		}

		kind := "Issue"
		if issue.PullRequest != nil {
			kind = "PR"
		}

		items = append(items, WorkItem{
			ID:       strconv.Itoa(issue.Number),
			Number:   issue.Number,
			Title:    issue.Title,
			Body:     issue.Body,
			URL:      issue.HTMLURL,
			Labels:   labels,
			Comments: comments,
			Kind:     kind,
		})
	}

	return items, nil
}

// passesCommentFilter checks whether an issue's comments satisfy the
// comment-based trigger and exclude rules. Comments are expected in the
// concatenated format produced by fetchComments (separated by "\n---\n").
//
// When both TriggerComment and ExcludeComments are set, the most recent
// matching command wins (scanned in reverse chronological order).
// TriggerComment doubles as a resume command — posting it after an
// ExcludeComment re-enables the issue.
func (s *GitHubSource) passesCommentFilter(comments string) bool {
	// Split into individual comment bodies.
	var parts []string
	if comments != "" {
		parts = strings.Split(comments, "\n---\n")
	}

	// When only TriggerComment is set, require at least one matching comment.
	if s.TriggerComment != "" && len(s.ExcludeComments) == 0 {
		for _, p := range parts {
			if containsCommand(p, s.TriggerComment) {
				return true
			}
		}
		return false
	}

	// When only ExcludeComments is set, exclude if any comment matches.
	if len(s.ExcludeComments) > 0 && s.TriggerComment == "" {
		for i := len(parts) - 1; i >= 0; i-- {
			if containsAnyCommand(parts[i], s.ExcludeComments) {
				return false
			}
		}
		return true
	}

	// When both are set, scan in reverse; the most recent matching command wins.
	// TriggerComment acts as both initial trigger and resume.
	if s.TriggerComment != "" && len(s.ExcludeComments) > 0 {
		for i := len(parts) - 1; i >= 0; i-- {
			if containsAnyCommand(parts[i], s.ExcludeComments) {
				return false
			}
			if containsCommand(parts[i], s.TriggerComment) {
				return true
			}
		}
		// Neither command found — trigger is required but absent.
		return false
	}

	return true
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

func (s *GitHubSource) fetchComments(ctx context.Context, issueNumber int) (string, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100", s.baseURL(), s.Owner, s.Repo, issueNumber)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	if s.Token != "" {
		req.Header.Set("Authorization", "token "+s.Token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching comments: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var comments []githubComment
	if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
		return "", fmt.Errorf("decoding comments: %w", err)
	}

	var parts []string
	totalBytes := 0
	for _, c := range comments {
		totalBytes += len(c.Body)
		if totalBytes > maxCommentBytes {
			break
		}
		parts = append(parts, c.Body)
	}

	return strings.Join(parts, "\n---\n"), nil
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func parseNextLink(header string) string {
	matches := linkNextRe.FindStringSubmatch(header)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
