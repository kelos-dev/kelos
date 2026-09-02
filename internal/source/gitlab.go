package source

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGitLabBaseURL = "https://gitlab.com"

	gitlabResourceIssues        = "issues"
	gitlabResourceMergeRequests = "merge_requests"

	pipelineStatusAny = "any"
)

// gitlabResourceByType maps the API-facing type names to GitLab REST resources.
var gitlabResourceByType = map[string]string{
	"issues":        gitlabResourceIssues,
	"mergeRequests": gitlabResourceMergeRequests,
}

// GitLabSource discovers issues and merge requests from a GitLab project.
type GitLabSource struct {
	// BaseURL is the GitLab instance URL (e.g. "https://gitlab.example.com").
	BaseURL string
	// Project is the full project path (e.g. "group/subgroup/project").
	Project       string
	Types         []string
	Labels        []string
	ExcludeLabels []string
	State         string
	// ReviewState gates merge requests by approval outcome
	// (approved, changes_requested, any).
	ReviewState string
	// PipelineStatus gates merge requests by head pipeline status.
	PipelineStatus  string
	Token           string
	Client          *http.Client
	TriggerComment  string
	ExcludeComments []string
	AllowedUsers    []string
}

// gitlabItem is the shared subset of the GitLab issue and merge request
// representations. SourceBranch, SHA, DetailedMergeStatus, and HeadPipeline
// are only populated for merge requests; HeadPipeline only on the single
// merge request endpoint.
type gitlabItem struct {
	IID          int             `json:"iid"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	WebURL       string          `json:"web_url"`
	Labels       []string        `json:"labels"`
	Author       gitlabUser      `json:"author"`
	SourceBranch string          `json:"source_branch"`
	SHA          string          `json:"sha"`
	HeadPipeline *gitlabPipeline `json:"head_pipeline"`
	// DetailedMergeStatus is "requested_changes" while any reviewer has
	// requested changes.
	DetailedMergeStatus string `json:"detailed_merge_status"`
}

type gitlabPipeline struct {
	Status     string `json:"status"`
	WebURL     string `json:"web_url"`
	FinishedAt string `json:"finished_at"`
	UpdatedAt  string `json:"updated_at"`
}

type gitlabUser struct {
	Username string `json:"username"`
}

type gitlabNote struct {
	Body      string     `json:"body"`
	CreatedAt string     `json:"created_at"`
	Author    gitlabUser `json:"author"`
	// System notes are GitLab-generated activity entries (label changes,
	// assignments, ...) rather than user comments.
	System bool `json:"system"`
	// Type is "DiffNote" for inline review comments on a merge request diff.
	Type     string              `json:"type"`
	Position *gitlabNotePosition `json:"position"`
}

type gitlabNotePosition struct {
	NewPath string `json:"new_path"`
	OldPath string `json:"old_path"`
	NewLine int    `json:"new_line"`
	OldLine int    `json:"old_line"`
}

type gitlabApprovals struct {
	Approved bool `json:"approved"`
}

func (s *GitLabSource) baseURL() string {
	if s.BaseURL != "" {
		return strings.TrimRight(s.BaseURL, "/")
	}
	return defaultGitLabBaseURL
}

// rest returns the REST plumbing for the GitLab API: PRIVATE-TOKEN auth and
// X-Next-Page pagination.
func (s *GitLabSource) rest() restClient {
	return restClient{
		name:   "GitLab",
		client: s.Client,
		authorize: func(req *http.Request) {
			if s.Token != "" {
				req.Header.Set("PRIVATE-TOKEN", s.Token)
			}
			req.Header.Set("Accept", "application/json")
		},
		nextPage: func(pageURL string, resp *http.Response) string {
			next := resp.Header.Get("X-Next-Page")
			if next == "" {
				return ""
			}
			u, err := url.Parse(pageURL)
			if err != nil {
				return ""
			}
			q := u.Query()
			q.Set("page", next)
			u.RawQuery = q.Encode()
			return u.String()
		},
	}
}

// Discover fetches issues and/or merge requests from GitLab and returns them
// as WorkItems. Merge requests get an "mr-" ID prefix because GitLab numbers
// issues and merge requests independently, so bare IIDs would collide when
// both types are discovered by one spawner.
func (s *GitLabSource) Discover(ctx context.Context) ([]WorkItem, error) {
	return discoverTracker(ctx, s)
}

func (s *GitLabSource) list(ctx context.Context) ([]trackerItem, error) {
	excluded := make(map[string]struct{}, len(s.ExcludeLabels))
	for _, l := range s.ExcludeLabels {
		excluded[l] = struct{}{}
	}

	var items []trackerItem
	for _, resource := range s.resolvedResources() {
		list, err := s.fetchAllItems(ctx, resource)
		if err != nil {
			return nil, err
		}
		for _, it := range list {
			if hasAnyLabel(it.Labels, excluded) {
				continue
			}
			item := trackerItem{
				WorkItem: WorkItem{
					ID:      strconv.Itoa(it.IID),
					Number:  it.IID,
					Title:   it.Title,
					Body:    it.Description,
					URL:     it.WebURL,
					Labels:  it.Labels,
					Kind:    "Issue",
					Branch:  it.SourceBranch,
					HeadSHA: it.SHA,
				},
				Author: it.Author.Username,
			}
			if resource == gitlabResourceMergeRequests {
				item.ID = "mr-" + item.ID
				item.Kind = "MR"
			}
			items = append(items, item)
		}
	}
	return items, nil
}

// enrich gates merge requests on pipeline status and review state before
// fetching notes. The policy thread is every note; conversation notes fill
// Comments and diff notes fill ReviewComments.
func (s *GitLabSource) enrich(ctx context.Context, item *trackerItem) (bool, []commentEntry, time.Time, error) {
	resource := gitlabResourceIssues
	var pipelineTriggerTime time.Time
	if item.Kind == "MR" {
		resource = gitlabResourceMergeRequests
		keep, triggerTime, err := s.enrichMergeRequest(ctx, item.Number, &item.WorkItem)
		if err != nil || !keep {
			return false, nil, time.Time{}, err
		}
		pipelineTriggerTime = triggerTime
	}

	notes, err := s.fetchNotes(ctx, resource, item.Number)
	if err != nil {
		return false, nil, time.Time{}, fmt.Errorf("fetching notes for %s !%d: %w", resource, item.Number, err)
	}
	item.Comments = concatBodies(gitlabNoteBodies(gitlabConversationNotes(notes)))
	if item.Kind == "MR" {
		item.ReviewComments = concatGitLabDiffNotes(notes)
	}
	return true, gitlabCommentEntries(notes), pipelineTriggerTime, nil
}

func (s *GitLabSource) commentPolicy(context.Context) (commentCommands, commentAuthorizer, error) {
	policy := gitlabCommentPolicy{
		TriggerComment:  s.TriggerComment,
		ExcludeComments: s.ExcludeComments,
		AllowedUsers:    s.AllowedUsers,
	}
	return policy.commands(), newGitLabCommentAuthorizer(policy), nil
}

// enrichMergeRequest loads the head pipeline and, when a review-state gate is
// configured, the approval state of a merge request. It reports whether the
// merge request passes the pipeline and review gates and, for a pipeline
// gate, the time the head pipeline finished so a newer run retriggers
// completed Tasks.
func (s *GitLabSource) enrichMergeRequest(ctx context.Context, iid int, item *WorkItem) (bool, time.Time, error) {
	// The list endpoint omits head_pipeline, so each merge request costs one
	// detail call.
	var detail gitlabItem
	if _, err := s.rest().getJSON(ctx, fmt.Sprintf("%s/%s/%d", s.projectURL(), gitlabResourceMergeRequests, iid), &detail); err != nil {
		return false, time.Time{}, fmt.Errorf("fetching merge request !%d: %w", iid, err)
	}

	var pipelineTriggerTime time.Time
	if detail.HeadPipeline != nil {
		item.PipelineStatus = detail.HeadPipeline.Status
		item.PipelineURL = detail.HeadPipeline.WebURL
		pipelineTriggerTime = detail.HeadPipeline.finishedTime()
	}
	if desired := s.resolvedPipelineStatus(); desired != pipelineStatusAny {
		if item.PipelineStatus != desired {
			return false, time.Time{}, nil
		}
	} else {
		pipelineTriggerTime = time.Time{}
	}

	if desired := s.resolvedReviewState(); desired != reviewStateAny {
		reviewState, err := s.fetchReviewState(ctx, iid, detail.DetailedMergeStatus)
		if err != nil {
			return false, time.Time{}, err
		}
		item.ReviewState = reviewState
		if reviewState != desired {
			return false, time.Time{}, nil
		}
	}

	return true, pipelineTriggerTime, nil
}

func (p *gitlabPipeline) finishedTime() time.Time {
	for _, raw := range []string{p.FinishedAt, p.UpdatedAt} {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// fetchReviewState maps GitLab's detailed merge status and approvals onto the
// GitHub-style review states: "changes_requested" when any reviewer requested
// changes, "approved" when the merge request has the required approvals, and
// "" otherwise.
func (s *GitLabSource) fetchReviewState(ctx context.Context, iid int, detailedMergeStatus string) (string, error) {
	if detailedMergeStatus == "requested_changes" {
		return reviewStateChangesRequested, nil
	}

	var approvals gitlabApprovals
	if _, err := s.rest().getJSON(ctx, fmt.Sprintf("%s/%s/%d/approvals", s.projectURL(), gitlabResourceMergeRequests, iid), &approvals); err != nil {
		return "", fmt.Errorf("fetching approvals for merge request !%d: %w", iid, err)
	}
	if approvals.Approved {
		return reviewStateApproved, nil
	}
	return "", nil
}

func (s *GitLabSource) resolvedReviewState() string {
	if s.ReviewState == "" {
		return reviewStateAny
	}
	return strings.ToLower(s.ReviewState)
}

func (s *GitLabSource) resolvedPipelineStatus() string {
	if s.PipelineStatus == "" {
		return pipelineStatusAny
	}
	return strings.ToLower(s.PipelineStatus)
}

// resolvedResources returns the GitLab REST resources to poll, in the order
// first listed in Types and without duplicates.
func (s *GitLabSource) resolvedResources() []string {
	if len(s.Types) == 0 {
		return []string{gitlabResourceIssues}
	}
	var resources []string
	seen := map[string]bool{}
	for _, t := range s.Types {
		resource := gitlabResourceByType[t]
		if resource == "" || seen[resource] {
			continue
		}
		seen[resource] = true
		resources = append(resources, resource)
	}
	return resources
}

func hasAnyLabel(labels []string, set map[string]struct{}) bool {
	for _, l := range labels {
		if _, ok := set[l]; ok {
			return true
		}
	}
	return false
}

// concatGitLabDiffNotes formats inline review comments as "path:line" headers
// followed by the note body, matching the GitHub review comment format.
func concatGitLabDiffNotes(notes []gitlabNote) string {
	var parts []string
	for _, n := range notes {
		if n.System || n.Type != "DiffNote" {
			continue
		}
		body := strings.TrimSpace(n.Body)
		if body == "" {
			continue
		}
		if n.Position != nil {
			location := n.Position.NewPath
			line := n.Position.NewLine
			// Notes on deleted lines carry new_path but no new_line; the
			// old side is the only location that exists.
			if location == "" || line <= 0 {
				location = n.Position.OldPath
				line = n.Position.OldLine
			}
			if line > 0 {
				location = fmt.Sprintf("%s:%d", location, line)
			}
			if location != "" {
				body = location + "\n" + body
			}
		}
		parts = append(parts, body)
	}
	return concatBodies(parts)
}

func (s *GitLabSource) projectURL() string {
	return s.baseURL() + "/api/v4/projects/" + url.PathEscape(s.Project)
}

func (s *GitLabSource) fetchAllItems(ctx context.Context, resource string) ([]gitlabItem, error) {
	params := url.Values{}
	params.Set("per_page", "100")
	params.Set("page", "1")
	state := s.State
	if state == "" {
		state = "opened"
	}
	params.Set("state", state)
	if len(s.Labels) > 0 {
		params.Set("labels", strings.Join(s.Labels, ","))
	}
	items, _, err := fetchAllPages[gitlabItem](ctx, s.rest(), s.projectURL()+"/"+resource+"?"+params.Encode())
	return items, err
}

// fetchNotes returns an item's notes oldest first. They are requested newest
// first so that the maxPages cap truncates the oldest notes, never a recent
// trigger command.
func (s *GitLabSource) fetchNotes(ctx context.Context, resource string, iid int) ([]gitlabNote, error) {
	params := url.Values{}
	params.Set("per_page", "100")
	params.Set("page", "1")
	params.Set("sort", "desc")
	params.Set("order_by", "created_at")

	notes, _, err := fetchAllPages[gitlabNote](ctx, s.rest(), fmt.Sprintf("%s/%s/%d/notes?%s", s.projectURL(), resource, iid, params.Encode()))
	slices.Reverse(notes)
	return notes, err
}
