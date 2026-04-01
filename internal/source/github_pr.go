package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// FilePatternFilter filters items by changed file paths using glob patterns.
// This is a source-level mirror of the API type to avoid importing API types
// in the source package.
type FilePatternFilter struct {
	Include     []string
	Exclude     []string
	ExcludeOnly bool
}

// MatchesFilePatterns returns true when the given file list passes the filter.
// A nil filter matches everything. When ExcludeOnly is false (default), any
// single exclude match rejects the item. When ExcludeOnly is true, the item
// is only rejected when ALL changed files match exclude patterns.
func MatchesFilePatterns(files []string, patterns *FilePatternFilter) bool {
	if patterns == nil {
		return true
	}
	if len(files) == 0 {
		return len(patterns.Include) == 0
	}

	hasIncludeMatch := len(patterns.Include) == 0
	allMatchExclude := len(patterns.Exclude) > 0

	for _, file := range files {
		if !patterns.ExcludeOnly {
			for _, pattern := range patterns.Exclude {
				if match, _ := doublestar.Match(pattern, file); match {
					return false
				}
			}
		} else {
			matchedAnyExclude := false
			for _, pattern := range patterns.Exclude {
				if match, _ := doublestar.Match(pattern, file); match {
					matchedAnyExclude = true
					break
				}
			}
			if !matchedAnyExclude {
				allMatchExclude = false
			}
		}

		if !hasIncludeMatch {
			for _, pattern := range patterns.Include {
				if match, _ := doublestar.Match(pattern, file); match {
					hasIncludeMatch = true
					break
				}
			}
		}
	}

	if patterns.ExcludeOnly && allMatchExclude {
		return false
	}
	return hasIncludeMatch
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
	FilePatterns      *FilePatternFilter
	// NeedsChangedFiles causes file lists to be fetched for every PR even
	// when FilePatterns is nil. This mirrors the webhook handler behavior
	// where templates that reference {{.ChangedFiles}} automatically
	// trigger file fetching.
	NeedsChangedFiles bool
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
	pullRequests, err := s.fetchAllPullRequests(ctx)
	if err != nil {
		return nil, err
	}

	pullRequests = s.filterPullRequests(pullRequests)

	// File-pattern filtering runs after cheap label/author/draft filters
	// but before expensive per-PR review and comment fetches.
	// When NeedsChangedFiles is true (template references {{.ChangedFiles}}),
	// files are fetched even without FilePatterns so the template variable
	// is populated — mirroring the webhook handler behavior.
	var prFiles map[int][]string
	if s.FilePatterns != nil || s.NeedsChangedFiles {
		prFiles = make(map[int][]string)
		var fileFiltered []githubPullRequest
		for _, pr := range pullRequests {
			files, err := s.fetchPRFiles(ctx, pr.Number)
			if err != nil {
				return nil, fmt.Errorf("fetching files for PR #%d: %w", pr.Number, err)
			}
			if MatchesFilePatterns(files, s.FilePatterns) {
				fileFiltered = append(fileFiltered, pr)
				prFiles[pr.Number] = files
			}
		}
		pullRequests = fileFiltered
	}

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

	issueSource := &GitHubSource{
		Owner:   s.Owner,
		Repo:    s.Repo,
		Token:   s.Token,
		BaseURL: s.BaseURL,
		Client:  s.Client,
	}

	var items []WorkItem
	for _, pr := range pullRequests {
		reviews, err := s.fetchPullRequestReviews(ctx, pr.Number)
		if err != nil {
			return nil, fmt.Errorf("fetching reviews for pull request #%d: %w", pr.Number, err)
		}

		reviewState, triggerTime := aggregatePullRequestReviewState(reviews, pr.Head.SHA)
		if !matchesDesiredReviewState(s.resolvedReviewState(), reviewState) {
			continue
		}

		conversationComments, err := issueSource.fetchComments(ctx, pr.Number)
		if err != nil {
			return nil, fmt.Errorf("fetching comments for pull request #%d: %w", pr.Number, err)
		}

		reviewComments, err := s.fetchPullRequestComments(ctx, pr.Number)
		if err != nil {
			return nil, fmt.Errorf("fetching review comments for pull request #%d: %w", pr.Number, err)
		}

		allComments := mergeComments(conversationComments, reviewComments)
		allComments = appendReviewBodies(allComments, reviews)
		commentTriggerTime := time.Time{}
		if needsCommentFilter {
			commentAllowed, resolvedTriggerTime, err := evaluateGitHubCommentPolicy(ctx, pr.Body, pr.User, allComments, policy, authorizer)
			if err != nil {
				return nil, fmt.Errorf("evaluating comment policy for pull request #%d: %w", pr.Number, err)
			}
			if !commentAllowed {
				continue
			}
			commentTriggerTime = resolvedTriggerTime
		}

		reviewComments = filterPullRequestCommentsForCommit(reviewComments, pr.Head.SHA)

		labels := make([]string, 0, len(pr.Labels))
		for _, l := range pr.Labels {
			labels = append(labels, l.Name)
		}

		item := WorkItem{
			ID:             strconv.Itoa(pr.Number),
			Number:         pr.Number,
			Title:          pr.Title,
			Body:           pr.Body,
			URL:            pr.HTMLURL,
			Labels:         labels,
			Comments:       concatCommentBodies(conversationComments),
			Kind:           "PR",
			Branch:         pr.Head.Ref,
			ReviewState:    reviewState,
			ReviewComments: concatPullRequestReviewComments(reviewComments),
			ChangedFiles:   prFiles[pr.Number],
		}

		item.TriggerTime = s.resolveTriggerTime(triggerTime, commentTriggerTime)

		items = append(items, item)
	}

	return items, nil
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
	var allPullRequests []githubPullRequest

	pageURL := s.buildPullRequestsURL()

	for page := 0; pageURL != "" && page < maxPages; page++ {
		pullRequests, nextURL, err := s.fetchPullRequestsPage(ctx, pageURL)
		if err != nil {
			return nil, err
		}
		allPullRequests = append(allPullRequests, pullRequests...)
		pageURL = nextURL
	}

	return allPullRequests, nil
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

func (s *GitHubPullRequestSource) fetchPullRequestsPage(ctx context.Context, pageURL string) ([]githubPullRequest, string, error) {
	var pullRequests []githubPullRequest
	nextURL, err := s.fetchGitHubPage(ctx, pageURL, &pullRequests)
	if err != nil {
		return nil, "", fmt.Errorf("fetching pull requests: %w", err)
	}
	return pullRequests, nextURL, nil
}

func (s *GitHubPullRequestSource) fetchPullRequestReviews(ctx context.Context, number int) ([]githubPullRequestReview, error) {
	var allReviews []githubPullRequestReview

	pageURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews?per_page=100",
		s.baseURL(), s.Owner, s.Repo, number)

	for page := 0; pageURL != "" && page < maxPages; page++ {
		var reviews []githubPullRequestReview
		nextURL, err := s.fetchGitHubPage(ctx, pageURL, &reviews)
		if err != nil {
			return nil, fmt.Errorf("fetching reviews: %w", err)
		}
		allReviews = append(allReviews, reviews...)
		pageURL = nextURL
	}

	return allReviews, nil
}

func (s *GitHubPullRequestSource) fetchPullRequestComments(ctx context.Context, number int) ([]githubPullRequestComment, error) {
	var allComments []githubPullRequestComment

	pageURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments?per_page=100",
		s.baseURL(), s.Owner, s.Repo, number)

	for page := 0; pageURL != "" && page < maxPages; page++ {
		var comments []githubPullRequestComment
		nextURL, err := s.fetchGitHubPage(ctx, pageURL, &comments)
		if err != nil {
			return nil, fmt.Errorf("fetching review comments: %w", err)
		}
		allComments = append(allComments, comments...)
		pageURL = nextURL
	}

	return allComments, nil
}

type githubPullRequestFile struct {
	Filename string `json:"filename"`
}

func (s *GitHubPullRequestSource) fetchPRFiles(ctx context.Context, number int) ([]string, error) {
	var allFiles []githubPullRequestFile

	pageURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100",
		s.baseURL(), s.Owner, s.Repo, number)

	for page := 0; pageURL != "" && page < maxPages; page++ {
		var files []githubPullRequestFile
		nextURL, err := s.fetchGitHubPage(ctx, pageURL, &files)
		if err != nil {
			return nil, fmt.Errorf("fetching PR files: %w", err)
		}
		allFiles = append(allFiles, files...)
		pageURL = nextURL
	}

	paths := make([]string, len(allFiles))
	for i, f := range allFiles {
		paths[i] = f.Filename
	}
	return paths, nil
}

func (s *GitHubPullRequestSource) fetchGitHubPage(ctx context.Context, pageURL string, out interface{}) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	if s.Token != "" {
		req.Header.Set("Authorization", "token "+s.Token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	return parseNextLink(resp.Header.Get("Link")), nil
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

func (s *GitHubPullRequestSource) resolveTriggerTime(reviewTriggerTime, commentTriggerTime time.Time) time.Time {
	triggerTime := commentTriggerTime
	if s.resolvedReviewState() != reviewStateAny && reviewTriggerTime.After(triggerTime) {
		triggerTime = reviewTriggerTime
	}
	return triggerTime
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
