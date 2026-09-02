package source

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	reviewStateAny              = "any"
	reviewStateApproved         = "approved"
	reviewStateChangesRequested = "changes_requested"
)

// MatchesFilePaths returns true when the given file list passes the
// include/exclude filter with remove-then-match semantics.
// Files matching any exclude pattern are removed first; the item passes when
// at least one remaining file matches any include pattern (or include is empty
// and at least one file remains after exclusion).
// Both nil/empty include and exclude means everything passes.
func MatchesFilePaths(files, include, exclude []string) bool {
	if len(include) == 0 && len(exclude) == 0 {
		return true
	}
	if len(files) == 0 {
		return false
	}

	// Phase 1: remove files matching any exclude pattern.
	var remaining []string
	for _, file := range files {
		excluded := false
		for _, pattern := range exclude {
			if match, _ := doublestar.Match(pattern, file); match {
				excluded = true
				break
			}
		}
		if !excluded {
			remaining = append(remaining, file)
		}
	}

	// All files excluded → reject.
	if len(remaining) == 0 {
		return false
	}

	// Phase 2: if include is empty, any surviving file is sufficient.
	if len(include) == 0 {
		return true
	}

	// Phase 2: at least one remaining file must match an include pattern.
	for _, file := range remaining {
		for _, pattern := range include {
			if match, _ := doublestar.Match(pattern, file); match {
				return true
			}
		}
	}
	return false
}

// GitHubPullRequestSource discovers pull requests from a GitHub repository.
type GitHubPullRequestSource struct {
	Owner             string
	Repo              string
	Labels            []string
	ExcludeLabels     []string
	State             string
	Author            string
	ExcludeAuthors    []string
	Token             string
	BaseURL           string
	Client            *http.Client
	ReviewState       string
	TriggerComment    string
	ExcludeComments   []string
	AllowedUsers      []string
	AllowedTeams      []string
	MinimumPermission string
	Draft             *bool
	PriorityLabels    []string
	FileInclude       []string
	FileExclude       []string
}

type githubUser struct {
	Login string `json:"login"`
}

type githubPullRequestHead struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type githubPullRequest struct {
	Number  int                   `json:"number"`
	Title   string                `json:"title"`
	Body    string                `json:"body"`
	HTMLURL string                `json:"html_url"`
	Labels  []githubLabel         `json:"labels"`
	User    githubUser            `json:"user"`
	Draft   bool                  `json:"draft"`
	Head    githubPullRequestHead `json:"head"`
}

type githubPullRequestReview struct {
	Body        string     `json:"body"`
	State       string     `json:"state"`
	SubmittedAt string     `json:"submitted_at"`
	CommitID    string     `json:"commit_id"`
	User        githubUser `json:"user"`
}

type githubPullRequestComment struct {
	Body      string     `json:"body"`
	Path      string     `json:"path"`
	Line      int        `json:"line"`
	CreatedAt string     `json:"created_at"`
	CommitID  string     `json:"commit_id"`
	User      githubUser `json:"user"`
}

func (s *GitHubPullRequestSource) Discover(ctx context.Context) ([]WorkItem, error) {
	return discoverTracker(ctx, s)
}

func (s *GitHubPullRequestSource) rest() restClient {
	return githubREST(s.Token, s.Client)
}

func (s *GitHubPullRequestSource) list(ctx context.Context) ([]trackerItem, error) {
	pullRequests, err := s.fetchAllPullRequests(ctx)
	if err != nil {
		return nil, err
	}
	pullRequests = s.filterPullRequests(pullRequests)

	// File-pattern filtering runs after cheap label/author/draft filters
	// but before expensive per-PR review and comment fetches.
	if len(s.FileInclude) > 0 || len(s.FileExclude) > 0 {
		var fileFiltered []githubPullRequest
		for _, pr := range pullRequests {
			files, err := s.fetchPRFiles(ctx, pr.Number)
			if err != nil {
				return nil, fmt.Errorf("fetching files for PR #%d: %w", pr.Number, err)
			}
			if MatchesFilePaths(files, s.FileInclude, s.FileExclude) {
				fileFiltered = append(fileFiltered, pr)
			}
		}
		pullRequests = fileFiltered
	}

	items := make([]trackerItem, 0, len(pullRequests))
	for _, pr := range pullRequests {
		items = append(items, trackerItem{
			WorkItem: WorkItem{
				ID:      strconv.Itoa(pr.Number),
				Number:  pr.Number,
				Title:   pr.Title,
				Body:    pr.Body,
				URL:     pr.HTMLURL,
				Labels:  githubLabelNames(pr.Labels),
				Kind:    "PR",
				Branch:  pr.Head.Ref,
				HeadSHA: pr.Head.SHA,
			},
			Author: pr.User.Login,
		})
	}
	return items, nil
}

// enrich gates on the aggregated review state before fetching comments, so a
// pull request that fails the gate costs one call. The policy thread covers
// conversation comments, inline review comments, and review bodies; the
// review comments exposed to templates are limited to the head commit.
func (s *GitHubPullRequestSource) enrich(ctx context.Context, item *trackerItem) (bool, []commentEntry, time.Time, error) {
	reviews, err := s.fetchPullRequestReviews(ctx, item.Number)
	if err != nil {
		return false, nil, time.Time{}, fmt.Errorf("fetching reviews for pull request #%d: %w", item.Number, err)
	}
	reviewState, reviewTime := aggregatePullRequestReviewState(reviews, item.HeadSHA)
	if !matchesDesiredReviewState(s.resolvedReviewState(), reviewState) {
		return false, nil, time.Time{}, nil
	}
	item.ReviewState = reviewState

	conversation, err := fetchGitHubIssueComments(ctx, s.rest(), s.baseURL(), s.Owner, s.Repo, item.Number)
	if err != nil {
		return false, nil, time.Time{}, fmt.Errorf("fetching comments for pull request #%d: %w", item.Number, err)
	}
	reviewComments, err := s.fetchPullRequestComments(ctx, item.Number)
	if err != nil {
		return false, nil, time.Time{}, fmt.Errorf("fetching review comments for pull request #%d: %w", item.Number, err)
	}
	item.Comments = concatCommentBodies(conversation)
	item.ReviewComments = concatPullRequestReviewComments(filterPullRequestCommentsForCommit(reviewComments, item.HeadSHA))

	thread := githubCommentEntries(appendReviewBodies(mergeComments(conversation, reviewComments), reviews))
	if s.resolvedReviewState() == reviewStateAny {
		reviewTime = time.Time{}
	}
	return true, thread, reviewTime, nil
}

func (s *GitHubPullRequestSource) commentPolicy(context.Context) (commentCommands, commentAuthorizer, error) {
	policy := githubCommentPolicy{
		TriggerComment:    s.TriggerComment,
		ExcludeComments:   s.ExcludeComments,
		AllowedUsers:      s.AllowedUsers,
		AllowedTeams:      s.AllowedTeams,
		MinimumPermission: s.MinimumPermission,
	}
	return githubCommentPolicyAuthorizer(s.Owner, s.Repo, s.baseURL(), s.Token, s.httpClient(), policy)
}

func (s *GitHubPullRequestSource) resolvedReviewState() string {
	if s.ReviewState == "" {
		return reviewStateAny
	}
	return strings.ToLower(s.ReviewState)
}

func matchesDesiredReviewState(desired, actual string) bool {
	if desired == reviewStateAny {
		return true
	}
	return actual == desired
}

func (s *GitHubPullRequestSource) filterPullRequests(pullRequests []githubPullRequest) []githubPullRequest {
	requiredLabels := make(map[string]struct{}, len(s.Labels))
	for _, label := range s.Labels {
		requiredLabels[label] = struct{}{}
	}

	excludedLabels := make(map[string]struct{}, len(s.ExcludeLabels))
	for _, label := range s.ExcludeLabels {
		excludedLabels[label] = struct{}{}
	}

	excludedAuthors := make(map[string]struct{}, len(s.ExcludeAuthors))
	for _, a := range s.ExcludeAuthors {
		excludedAuthors[a] = struct{}{}
	}

	filtered := make([]githubPullRequest, 0, len(pullRequests))
	for _, pr := range pullRequests {
		if s.Author != "" && pr.User.Login != s.Author {
			continue
		}
		if _, ok := excludedAuthors[pr.User.Login]; ok {
			continue
		}
		if s.Draft != nil && pr.Draft != *s.Draft {
			continue
		}

		labelSet := make(map[string]struct{}, len(pr.Labels))
		skip := false
		for _, label := range pr.Labels {
			labelSet[label.Name] = struct{}{}
			if _, ok := excludedLabels[label.Name]; ok {
				skip = true
			}
		}
		if skip {
			continue
		}

		missingLabel := false
		for label := range requiredLabels {
			if _, ok := labelSet[label]; !ok {
				missingLabel = true
				break
			}
		}
		if missingLabel {
			continue
		}

		filtered = append(filtered, pr)
	}

	return filtered
}

func (s *GitHubPullRequestSource) fetchAllPullRequests(ctx context.Context) ([]githubPullRequest, error) {
	pullRequests, _, err := fetchAllPages[githubPullRequest](ctx, s.rest(), s.buildPullRequestsURL())
	if err != nil {
		return nil, fmt.Errorf("fetching pull requests: %w", err)
	}
	return pullRequests, nil
}

func (s *GitHubPullRequestSource) buildPullRequestsURL() string {
	u := fmt.Sprintf("%s/repos/%s/%s/pulls", s.baseURL(), s.Owner, s.Repo)

	params := url.Values{}
	params.Set("per_page", "100")

	state := s.State
	if state == "" {
		state = "open"
	}
	params.Set("state", state)
	params.Set("sort", "updated")
	params.Set("direction", "desc")

	return u + "?" + params.Encode()
}

func (s *GitHubPullRequestSource) pullRequestURL(number int, resource string) string {
	return fmt.Sprintf("%s/repos/%s/%s/pulls/%d/%s?per_page=100", s.baseURL(), s.Owner, s.Repo, number, resource)
}

func (s *GitHubPullRequestSource) fetchPullRequestReviews(ctx context.Context, number int) ([]githubPullRequestReview, error) {
	reviews, _, err := fetchAllPages[githubPullRequestReview](ctx, s.rest(), s.pullRequestURL(number, "reviews"))
	if err != nil {
		return nil, fmt.Errorf("fetching reviews: %w", err)
	}
	return reviews, nil
}

func (s *GitHubPullRequestSource) fetchPullRequestComments(ctx context.Context, number int) ([]githubPullRequestComment, error) {
	comments, _, err := fetchAllPages[githubPullRequestComment](ctx, s.rest(), s.pullRequestURL(number, "comments"))
	if err != nil {
		return nil, fmt.Errorf("fetching review comments: %w", err)
	}
	return comments, nil
}

type githubPullRequestFile struct {
	Filename string `json:"filename"`
}

func (s *GitHubPullRequestSource) fetchPRFiles(ctx context.Context, number int) ([]string, error) {
	files, complete, err := fetchAllPages[githubPullRequestFile](ctx, s.rest(), s.pullRequestURL(number, "files"))
	if err != nil {
		return nil, fmt.Errorf("fetching PR files: %w", err)
	}
	// A partial file list is not safe for include/exclude decisions.
	if !complete {
		return nil, fmt.Errorf("PR #%d has more than %d pages of changed files; file list truncated, refusing to evaluate filters on incomplete data", number, maxPages)
	}

	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Filename
	}
	return paths, nil
}

func (s *GitHubPullRequestSource) baseURL() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return defaultBaseURL
}

func (s *GitHubPullRequestSource) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func aggregatePullRequestReviewState(reviews []githubPullRequestReview, headSHA string) (string, time.Time) {
	type reviewerState struct {
		State       string
		SubmittedAt time.Time
	}

	latestByReviewer := make(map[string]reviewerState)
	for _, review := range reviews {
		state := normalizePullRequestReviewState(review.State)
		if state == "" || review.CommitID != headSHA {
			continue
		}

		reviewer := strings.ToLower(strings.TrimSpace(review.User.Login))
		if reviewer == "" {
			continue
		}

		submittedAt, err := time.Parse(time.RFC3339, review.SubmittedAt)
		if err != nil {
			continue
		}

		current, ok := latestByReviewer[reviewer]
		if !ok || submittedAt.After(current.SubmittedAt) {
			latestByReviewer[reviewer] = reviewerState{
				State:       state,
				SubmittedAt: submittedAt,
			}
		}
	}

	var latestApproved time.Time
	var latestChangesRequested time.Time
	for _, state := range latestByReviewer {
		switch state.State {
		case reviewStateChangesRequested:
			if state.SubmittedAt.After(latestChangesRequested) {
				latestChangesRequested = state.SubmittedAt
			}
		case reviewStateApproved:
			if state.SubmittedAt.After(latestApproved) {
				latestApproved = state.SubmittedAt
			}
		}
	}

	if !latestChangesRequested.IsZero() {
		return reviewStateChangesRequested, latestChangesRequested
	}
	if !latestApproved.IsZero() {
		return reviewStateApproved, latestApproved
	}
	return "", time.Time{}
}

func normalizePullRequestReviewState(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "APPROVED":
		return reviewStateApproved
	case "CHANGES_REQUESTED":
		return reviewStateChangesRequested
	default:
		return ""
	}
}

// appendReviewBodies appends review body text from pull request reviews to the
// comment list so that commands in review bodies are evaluated by the comment
// filter.
func appendReviewBodies(comments []githubComment, reviews []githubPullRequestReview) []githubComment {
	for _, r := range reviews {
		body := strings.TrimSpace(r.Body)
		if body == "" {
			continue
		}
		comments = append(comments, githubComment{
			Body:      body,
			CreatedAt: r.SubmittedAt,
			User:      r.User,
		})
	}
	return comments
}

// mergeComments combines conversation comments and review comments into a
// single slice so that both sources are evaluated by the comment filter.
func mergeComments(conversation []githubComment, review []githubPullRequestComment) []githubComment {
	merged := make([]githubComment, 0, len(conversation)+len(review))
	merged = append(merged, conversation...)
	for _, rc := range review {
		merged = append(merged, githubComment{
			Body:      rc.Body,
			CreatedAt: rc.CreatedAt,
			User:      rc.User,
		})
	}
	return merged
}

func filterPullRequestCommentsForCommit(comments []githubPullRequestComment, commitID string) []githubPullRequestComment {
	filtered := make([]githubPullRequestComment, 0, len(comments))
	for _, comment := range comments {
		if comment.CommitID != commitID {
			continue
		}
		filtered = append(filtered, comment)
	}
	return filtered
}

func concatPullRequestReviewComments(comments []githubPullRequestComment) string {
	parts := make([]string, 0, len(comments))
	for _, comment := range comments {
		body := strings.TrimSpace(comment.Body)
		if body == "" {
			continue
		}

		location := strings.TrimSpace(comment.Path)
		if comment.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, comment.Line)
		}

		if location != "" {
			body = location + "\n" + body
		}
		parts = append(parts, body)
	}

	return concatBodies(parts)
}

func concatBodies(parts []string) string {
	totalBytes := 0
	for _, part := range parts {
		totalBytes += len(part)
	}

	if totalBytes <= maxCommentBytes {
		return strings.Join(parts, "\n---\n")
	}

	var kept []string
	remaining := maxCommentBytes
	for i := len(parts) - 1; i >= 0; i-- {
		if remaining-len(parts[i]) < 0 {
			break
		}
		remaining -= len(parts[i])
		kept = append(kept, parts[i])
	}

	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	return strings.Join(kept, "\n---\n")
}
