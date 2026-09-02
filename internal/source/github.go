package source

import (
	"context"
	"fmt"
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

func (s *GitHubSource) rest() restClient {
	return githubREST(s.Token, s.Client)
}

// githubREST returns the REST plumbing for the GitHub API: token auth and
// Link-header pagination.
func githubREST(token string, client *http.Client) restClient {
	return restClient{
		name:   "GitHub",
		client: client,
		authorize: func(req *http.Request) {
			if token != "" {
				req.Header.Set("Authorization", "token "+token)
			}
			req.Header.Set("Accept", "application/vnd.github.v3+json")
		},
		nextPage: func(_ string, resp *http.Response) string {
			return parseNextLink(resp.Header.Get("Link"))
		},
	}
}

// Discover fetches issues from GitHub and returns them as WorkItems.
func (s *GitHubSource) Discover(ctx context.Context) ([]WorkItem, error) {
	return discoverTracker(ctx, s)
}

func (s *GitHubSource) list(ctx context.Context) ([]trackerItem, error) {
	issues, err := s.fetchAllIssues(ctx)
	if err != nil {
		return nil, err
	}
	var items []trackerItem
	for _, issue := range s.filterItems(issues) {
		kind := "Issue"
		if issue.PullRequest != nil {
			kind = "PR"
		}
		items = append(items, trackerItem{
			WorkItem: WorkItem{
				ID:     strconv.Itoa(issue.Number),
				Number: issue.Number,
				Title:  issue.Title,
				Body:   issue.Body,
				URL:    issue.HTMLURL,
				Labels: githubLabelNames(issue.Labels),
				Kind:   kind,
			},
			Author: issue.User.Login,
		})
	}
	return items, nil
}

func (s *GitHubSource) enrich(ctx context.Context, item *trackerItem) (bool, []commentEntry, time.Time, error) {
	comments, err := fetchGitHubIssueComments(ctx, s.rest(), s.baseURL(), s.Owner, s.Repo, item.Number)
	if err != nil {
		return false, nil, time.Time{}, fmt.Errorf("fetching comments for issue #%d: %w", item.Number, err)
	}
	item.Comments = concatCommentBodies(comments)
	return true, githubCommentEntries(comments), time.Time{}, nil
}

func (s *GitHubSource) commentPolicy(context.Context) (commentCommands, commentAuthorizer, error) {
	policy := githubCommentPolicy{
		TriggerComment:    s.TriggerComment,
		ExcludeComments:   s.ExcludeComments,
		AllowedUsers:      s.AllowedUsers,
		AllowedTeams:      s.AllowedTeams,
		MinimumPermission: s.MinimumPermission,
	}
	return githubCommentPolicyAuthorizer(s.Owner, s.Repo, s.baseURL(), s.Token, s.httpClient(), policy)
}

// githubCommentPolicyAuthorizer builds the authorizer only when a command is
// configured, because it may call the GitHub API.
func githubCommentPolicyAuthorizer(owner, repo, baseURL, token string, client *http.Client, policy githubCommentPolicy) (commentCommands, commentAuthorizer, error) {
	cmds := policy.commands()
	if !cmds.enabled() {
		return cmds, nil, nil
	}
	authorizer, err := newGitHubCommentAuthorizer(owner, repo, baseURL, token, client, policy)
	if err != nil {
		return cmds, nil, err
	}
	return cmds, authorizer, nil
}

func githubLabelNames(labels []githubLabel) []string {
	var names []string
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names
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
	issues, _, err := fetchAllPages[githubIssue](ctx, s.rest(), s.buildIssuesURL())
	if err != nil {
		return nil, fmt.Errorf("fetching issues: %w", err)
	}
	return issues, nil
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

// fetchGitHubIssueComments returns the conversation comments of an issue or
// pull request (GitHub serves both from the issues endpoint).
func fetchGitHubIssueComments(ctx context.Context, rest restClient, baseURL, owner, repo string, number int) ([]githubComment, error) {
	comments, _, err := fetchAllPages[githubComment](ctx, rest, fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100", baseURL, owner, repo, number))
	return comments, err
}

// concatCommentBodies joins comment bodies into a single string separated by
// "\n---\n". When the total size exceeds maxCommentBytes, older comments are
// dropped from the front so that the most recent (and most relevant) comments
// are preserved.
func concatCommentBodies(comments []githubComment) string {
	parts := make([]string, len(comments))
	for i, c := range comments {
		parts[i] = c.Body
	}
	return concatBodies(parts)
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func parseNextLink(header string) string {
	matches := linkNextRe.FindStringSubmatch(header)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
