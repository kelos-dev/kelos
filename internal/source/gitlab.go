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
)

const (
	defaultGitLabBaseURL = "https://gitlab.com"

	gitlabResourceIssues        = "issues"
	gitlabResourceMergeRequests = "merge_requests"

	pipelineStatusAny = "any"
)

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
// representations. SourceBranch, SHA, and HeadPipeline are only populated for
// merge requests; HeadPipeline only on the single merge request endpoint.
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

type gitlabReviewer struct {
	State string `json:"state"`
}

func (s *GitLabSource) baseURL() string {
	if s.BaseURL != "" {
		return strings.TrimRight(s.BaseURL, "/")
	}
	return defaultGitLabBaseURL
}

func (s *GitLabSource) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

// Discover fetches issues and/or merge requests from GitLab and returns them
// as WorkItems. Merge requests get an "mr-" ID prefix because GitLab numbers
// issues and merge requests independently, so bare IIDs would collide when
// both types are discovered by one spawner.
func (s *GitLabSource) Discover(ctx context.Context) ([]WorkItem, error) {
	policy := gitlabCommentPolicy{
		TriggerComment:  s.TriggerComment,
		ExcludeComments: s.ExcludeComments,
		AllowedUsers:    s.AllowedUsers,
	}
	authorizer := newGitLabCommentAuthorizer(policy)

	excluded := make(map[string]struct{}, len(s.ExcludeLabels))
	for _, l := range s.ExcludeLabels {
		excluded[l] = struct{}{}
	}

	var items []WorkItem
	for _, resource := range s.resolvedResources() {
		list, err := s.fetchAllItems(ctx, resource)
		if err != nil {
			return nil, err
		}

		for _, it := range list {
			if hasAnyLabel(it.Labels, excluded) {
				continue
			}

			item := WorkItem{
				ID:      strconv.Itoa(it.IID),
				Number:  it.IID,
				Title:   it.Title,
				Body:    it.Description,
				URL:     it.WebURL,
				Labels:  it.Labels,
				Kind:    "Issue",
				Branch:  it.SourceBranch,
				HeadSHA: it.SHA,
			}

			var pipelineTriggerTime time.Time
			if resource == gitlabResourceMergeRequests {
				item.ID = "mr-" + item.ID
				item.Kind = "MR"

				keep, triggerTime, err := s.enrichMergeRequest(ctx, it.IID, &item)
				if err != nil {
					return nil, err
				}
				if !keep {
					continue
				}
				pipelineTriggerTime = triggerTime
			}

			notes, err := s.fetchNotes(ctx, resource, it.IID)
			if err != nil {
				return nil, fmt.Errorf("fetching notes for %s !%d: %w", resource, it.IID, err)
			}
			item.Comments = concatBodies(gitlabNoteBodies(gitlabConversationNotes(notes)))
			if resource == gitlabResourceMergeRequests {
				item.ReviewComments = concatGitLabDiffNotes(notes)
			}

			if policy.enabled() {
				allowed, triggerTime := evaluateGitLabCommentPolicy(it.Description, it.Author.Username, notes, policy, authorizer)
				if !allowed {
					continue
				}
				if s.TriggerComment != "" {
					item.TriggerTime = triggerTime
				}
			}
			if pipelineTriggerTime.After(item.TriggerTime) {
				item.TriggerTime = pipelineTriggerTime
			}

			items = append(items, item)
		}
	}

	return items, nil
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
	if err := s.getJSON(ctx, fmt.Sprintf("%s/%s/%d", s.projectURL(), gitlabResourceMergeRequests, iid), nil, &detail); err != nil {
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
		reviewState, err := s.fetchReviewState(ctx, iid)
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

// fetchReviewState aggregates GitLab approvals and reviewer states into the
// GitHub-style review states: "changes_requested" when any reviewer requested
// changes, "approved" when the merge request has the required approvals, and
// "" otherwise.
func (s *GitLabSource) fetchReviewState(ctx context.Context, iid int) (string, error) {
	mrURL := fmt.Sprintf("%s/%s/%d", s.projectURL(), gitlabResourceMergeRequests, iid)

	var reviewers []gitlabReviewer
	if err := s.getJSON(ctx, mrURL+"/reviewers", nil, &reviewers); err != nil {
		return "", fmt.Errorf("fetching reviewers for merge request !%d: %w", iid, err)
	}
	for _, r := range reviewers {
		if r.State == "requested_changes" {
			return reviewStateChangesRequested, nil
		}
	}

	var approvals gitlabApprovals
	if err := s.getJSON(ctx, mrURL+"/approvals", nil, &approvals); err != nil {
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

// resolvedResources maps the API-facing type names to GitLab REST resources.
func (s *GitLabSource) resolvedResources() []string {
	if len(s.Types) == 0 {
		return []string{gitlabResourceIssues}
	}
	var resources []string
	for _, t := range s.Types {
		switch t {
		case "issues":
			resources = append(resources, gitlabResourceIssues)
		case "mergeRequests":
			resources = append(resources, gitlabResourceMergeRequests)
		}
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
			if location == "" {
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
	state := s.State
	if state == "" {
		state = "opened"
	}
	params.Set("state", state)
	if len(s.Labels) > 0 {
		params.Set("labels", strings.Join(s.Labels, ","))
	}

	var all []gitlabItem
	err := s.fetchPages(ctx, s.projectURL()+"/"+resource, params, func(body io.Reader) (int, error) {
		var page []gitlabItem
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return 0, err
		}
		all = append(all, page...)
		return len(page), nil
	})
	return all, err
}

func (s *GitLabSource) fetchNotes(ctx context.Context, resource string, iid int) ([]gitlabNote, error) {
	params := url.Values{}
	params.Set("per_page", "100")
	params.Set("sort", "asc")
	params.Set("order_by", "created_at")

	var all []gitlabNote
	err := s.fetchPages(ctx, fmt.Sprintf("%s/%s/%d/notes", s.projectURL(), resource, iid), params, func(body io.Reader) (int, error) {
		var page []gitlabNote
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return 0, err
		}
		all = append(all, page...)
		return len(page), nil
	})
	return all, err
}

// fetchPages walks GitLab's page-number pagination, following the X-Next-Page
// response header until it is empty or maxPages is reached.
func (s *GitLabSource) fetchPages(ctx context.Context, endpoint string, params url.Values, decode func(io.Reader) (int, error)) error {
	page := "1"
	for i := 0; page != "" && i < maxPages; i++ {
		params.Set("page", page)
		resp, err := s.get(ctx, endpoint, params)
		if err != nil {
			return err
		}
		n, err := decode(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
		if n == 0 {
			break
		}
		page = resp.Header.Get("X-Next-Page")
	}
	return nil
}

// getJSON fetches a single JSON document.
func (s *GitLabSource) getJSON(ctx context.Context, endpoint string, params url.Values, out interface{}) error {
	resp, err := s.get(ctx, endpoint, params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// get performs an authenticated GET and returns the response for a 200
// status; any other status is returned as an error with the response body.
func (s *GitLabSource) get(ctx context.Context, endpoint string, params url.Values) (*http.Response, error) {
	target := endpoint
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if s.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", s.Token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(body))
	}
	return resp, nil
}
