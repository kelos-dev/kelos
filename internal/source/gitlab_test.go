package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// gitlabFixture holds the data served by newGitLabTestServer for project
// "group/sub/repo". Merge request details (including head_pipeline and
// detailed_merge_status) come from mrs; notes are keyed by
// "<resource>/<iid>" and approvals by merge request iid.
type gitlabFixture struct {
	issues    []gitlabItem
	mrs       []gitlabItem
	notes     map[string][]gitlabNote
	approvals map[int]gitlabApprovals
}

// requestLog records requests from the server goroutine for inspection after
// Discover returns.
type requestLog struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, r)
}

func (l *requestLog) all() []*http.Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*http.Request(nil), l.reqs...)
}

func newGitLabTestServer(t *testing.T, fx gitlabFixture, seen *requestLog) *httptest.Server {
	t.Helper()
	const prefix = "/api/v4/projects/group%2Fsub%2Frepo/"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.add(r)
		}
		path := r.URL.EscapedPath()
		if !strings.HasPrefix(path, prefix) {
			t.Errorf("unexpected path %s (project path must be URL-encoded)", path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rest := strings.TrimPrefix(path, prefix)
		parts := strings.Split(rest, "/")
		switch {
		case rest == "issues":
			json.NewEncoder(w).Encode(fx.issues)
		case rest == "merge_requests":
			json.NewEncoder(w).Encode(fx.mrs)
		case len(parts) == 2 && parts[0] == "merge_requests":
			iid, _ := strconv.Atoi(parts[1])
			for _, mr := range fx.mrs {
				if mr.IID == iid {
					json.NewEncoder(w).Encode(mr)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case len(parts) == 3 && parts[2] == "notes":
			// Fixtures list notes chronologically; honour sort=desc like GitLab.
			notes := slices.Clone(fx.notes[parts[0]+"/"+parts[1]])
			if r.URL.Query().Get("sort") == "desc" {
				slices.Reverse(notes)
			}
			json.NewEncoder(w).Encode(notes)
		case len(parts) == 3 && parts[2] == "approvals":
			iid, _ := strconv.Atoi(parts[1])
			json.NewEncoder(w).Encode(fx.approvals[iid])
		default:
			t.Errorf("unexpected path %s", path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestGitLabDiscoverIssues(t *testing.T) {
	fx := gitlabFixture{
		issues: []gitlabItem{
			{IID: 1, Title: "Bug 1", Description: "Body 1", WebURL: "https://gitlab.example.com/group/sub/repo/-/issues/1", Labels: []string{"bug"}},
			{IID: 2, Title: "Bug 2", Description: "Body 2", WebURL: "https://gitlab.example.com/group/sub/repo/-/issues/2", Labels: []string{"bug", "wontfix"}},
		},
		notes: map[string][]gitlabNote{
			"issues/1": {
				{Body: "changed label", System: true, Author: gitlabUser{Username: "alice"}},
				{Body: "first comment", Author: gitlabUser{Username: "alice"}},
				{Body: "second comment", Author: gitlabUser{Username: "bob"}},
			},
		},
	}
	var log requestLog
	server := newGitLabTestServer(t, fx, &log)
	defer server.Close()

	s := &GitLabSource{
		BaseURL:       server.URL,
		Project:       "group/sub/repo",
		Labels:        []string{"bug"},
		ExcludeLabels: []string{"wontfix"},
		Token:         "glpat-secret",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item after exclude-label filtering, got %d: %+v", len(items), items)
	}
	got := items[0]
	if got.ID != "1" || got.Number != 1 || got.Kind != "Issue" || got.Title != "Bug 1" || got.Body != "Body 1" {
		t.Errorf("unexpected item: %+v", got)
	}
	if got.URL != fx.issues[0].WebURL {
		t.Errorf("unexpected URL %q", got.URL)
	}
	if got.Comments != "first comment\n---\nsecond comment" {
		t.Errorf("expected system notes dropped from comments, got %q", got.Comments)
	}

	seen := log.all()
	list := seen[0]
	if list.Header.Get("PRIVATE-TOKEN") != "glpat-secret" {
		t.Errorf("expected PRIVATE-TOKEN header, got %q", list.Header.Get("PRIVATE-TOKEN"))
	}
	q := list.URL.Query()
	if q.Get("state") != "opened" || q.Get("labels") != "bug" || q.Get("per_page") != "100" {
		t.Errorf("unexpected list query: %v", q)
	}
	if len(seen) != 2 || !strings.HasSuffix(seen[1].URL.EscapedPath(), "/issues/1/notes") {
		t.Fatalf("expected exactly list + notes requests for issues, got %d requests", len(seen))
	}
	// Newest-first paging keeps recent trigger notes inside the page cap;
	// Comments above proves the result is still chronological.
	if q := seen[1].URL.Query(); q.Get("sort") != "desc" || q.Get("order_by") != "created_at" {
		t.Errorf("expected notes requested newest first, got %v", q)
	}
}

func TestGitLabDiscoverMergeRequests(t *testing.T) {
	fx := gitlabFixture{
		mrs: []gitlabItem{{
			IID: 7, Title: "Add feature", Description: "MR body",
			WebURL:       "https://gitlab.example.com/group/sub/repo/-/merge_requests/7",
			SourceBranch: "feature-x", SHA: "abc123",
			HeadPipeline: &gitlabPipeline{Status: "failed", WebURL: "https://gitlab.example.com/group/sub/repo/-/pipelines/99"},
		}},
		notes: map[string][]gitlabNote{
			"merge_requests/7": {
				{Body: "looks good overall", Author: gitlabUser{Username: "bob"}},
				{Body: "rename this", Type: "DiffNote", Author: gitlabUser{Username: "bob"}, Position: &gitlabNotePosition{NewPath: "main.go", NewLine: 12}},
				{Body: "deleted line comment", Type: "DiffNote", Author: gitlabUser{Username: "bob"}, Position: &gitlabNotePosition{OldPath: "old.go", OldLine: 3}},
				{Body: "removed in place", Type: "DiffNote", Author: gitlabUser{Username: "bob"}, Position: &gitlabNotePosition{NewPath: "main.go", OldPath: "main.go", OldLine: 8}},
			},
		},
	}
	var log requestLog
	server := newGitLabTestServer(t, fx, &log)
	defer server.Close()

	s := &GitLabSource{BaseURL: server.URL, Project: "group/sub/repo", Types: []string{"mergeRequests"}}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	got := items[0]
	if got.ID != "mr-7" || got.Number != 7 || got.Kind != "MR" {
		t.Errorf("unexpected merge request identity: %+v", got)
	}
	if got.Branch != "feature-x" || got.HeadSHA != "abc123" {
		t.Errorf("expected branch/sha from merge request, got %+v", got)
	}
	if got.PipelineStatus != "failed" || got.PipelineURL != fx.mrs[0].HeadPipeline.WebURL {
		t.Errorf("expected head pipeline from detail endpoint, got %+v", got)
	}
	if got.Comments != "looks good overall" {
		t.Errorf("expected diff notes excluded from comments, got %q", got.Comments)
	}
	if got.ReviewComments != "main.go:12\nrename this\n---\nold.go:3\ndeleted line comment\n---\nmain.go:8\nremoved in place" {
		t.Errorf("unexpected review comments %q", got.ReviewComments)
	}
	if got.ReviewState != "" || !got.TriggerTime.IsZero() {
		t.Errorf("expected no review state or trigger time without gates, got %+v", got)
	}
	for _, r := range log.all() {
		if strings.HasSuffix(r.URL.Path, "/approvals") {
			t.Errorf("review endpoints must not be fetched without a reviewState gate: %s", r.URL.Path)
		}
	}
}

func TestGitLabDiscoverDuplicateTypesPollOnce(t *testing.T) {
	fx := gitlabFixture{issues: []gitlabItem{{IID: 1, Title: "Issue 1"}}}
	var log requestLog
	server := newGitLabTestServer(t, fx, &log)
	defer server.Close()

	s := &GitLabSource{BaseURL: server.URL, Project: "group/sub/repo", Types: []string{"issues", "issues"}}
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected duplicate types to yield one item, got %+v", items)
	}
	lists := 0
	for _, r := range log.all() {
		if strings.HasSuffix(r.URL.EscapedPath(), "/issues") {
			lists++
		}
	}
	if lists != 1 {
		t.Errorf("expected one issues list request, got %d", lists)
	}
}

func TestGitLabDiscoverPipelineStatusGate(t *testing.T) {
	finished := "2026-03-01T10:00:00Z"
	fx := gitlabFixture{
		mrs: []gitlabItem{
			{IID: 1, Title: "green", HeadPipeline: &gitlabPipeline{Status: "success"}},
			{IID: 2, Title: "red", HeadPipeline: &gitlabPipeline{Status: "failed", FinishedAt: finished}},
			{IID: 3, Title: "no pipeline"},
		},
	}
	server := newGitLabTestServer(t, fx, nil)
	defer server.Close()

	s := &GitLabSource{BaseURL: server.URL, Project: "group/sub/repo", Types: []string{"mergeRequests"}, PipelineStatus: "failed"}
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "mr-2" {
		t.Fatalf("expected only the failed pipeline MR, got %+v", items)
	}
	want, _ := time.Parse(time.RFC3339, finished)
	if !items[0].TriggerTime.Equal(want) {
		t.Errorf("expected pipeline finish time as trigger time, got %v", items[0].TriggerTime)
	}
}

func TestGitLabDiscoverReviewStateGate(t *testing.T) {
	fx := gitlabFixture{
		mrs: []gitlabItem{
			{IID: 1, Title: "approved", DetailedMergeStatus: "mergeable"},
			{IID: 2, Title: "changes requested", DetailedMergeStatus: "requested_changes"},
			{IID: 3, Title: "unreviewed", DetailedMergeStatus: "not_approved"},
		},
		approvals: map[int]gitlabApprovals{
			1: {Approved: true},
			2: {Approved: true},
		},
	}
	server := newGitLabTestServer(t, fx, nil)
	defer server.Close()

	approved := &GitLabSource{BaseURL: server.URL, Project: "group/sub/repo", Types: []string{"mergeRequests"}, ReviewState: "approved"}
	items, err := approved.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "mr-1" || items[0].ReviewState != "approved" {
		t.Errorf("expected only approved MR (changes requested wins over approval), got %+v", items)
	}

	changes := &GitLabSource{BaseURL: server.URL, Project: "group/sub/repo", Types: []string{"mergeRequests"}, ReviewState: "changes_requested"}
	items, err = changes.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "mr-2" || items[0].ReviewState != "changes_requested" {
		t.Errorf("expected only changes-requested MR, got %+v", items)
	}
}

func TestGitLabDiscoverBothTypesKeepDistinctIDs(t *testing.T) {
	fx := gitlabFixture{
		issues: []gitlabItem{{IID: 3, Title: "Issue 3"}},
		mrs:    []gitlabItem{{IID: 3, Title: "MR 3", SourceBranch: "b"}},
	}
	server := newGitLabTestServer(t, fx, nil)
	defer server.Close()

	s := &GitLabSource{BaseURL: server.URL, Project: "group/sub/repo", Types: []string{"issues", "mergeRequests"}}
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 || items[0].ID != "3" || items[1].ID != "mr-3" {
		t.Errorf("expected ids [3 mr-3], got %+v", items)
	}
}

func TestGitLabDiscoverTriggerComment(t *testing.T) {
	fx := gitlabFixture{
		issues: []gitlabItem{
			{IID: 1, Title: "triggered by note", Author: gitlabUser{Username: "author"}},
			{IID: 2, Title: "no trigger", Author: gitlabUser{Username: "author"}},
			{IID: 3, Title: "triggered by unauthorized user", Author: gitlabUser{Username: "author"}},
			{IID: 4, Title: "triggered in description", Description: "/kelos fix", Author: gitlabUser{Username: "alice"}},
		},
		notes: map[string][]gitlabNote{
			"issues/1": {
				{Body: "please look", Author: gitlabUser{Username: "bob"}, CreatedAt: "2026-01-01T00:00:00Z"},
				{Body: "/kelos fix", Author: gitlabUser{Username: "alice"}, CreatedAt: "2026-01-02T00:00:00Z"},
			},
			"issues/3": {
				{Body: "/kelos fix", Author: gitlabUser{Username: "mallory"}, CreatedAt: "2026-01-02T00:00:00Z"},
			},
		},
	}
	server := newGitLabTestServer(t, fx, nil)
	defer server.Close()

	s := &GitLabSource{
		BaseURL:        server.URL,
		Project:        "group/sub/repo",
		TriggerComment: "/kelos fix",
		AllowedUsers:   []string{"alice"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 triggered items, got %d: %+v", len(items), items)
	}
	if items[0].ID != "1" || items[1].ID != "4" {
		t.Errorf("expected items 1 and 4, got %+v", items)
	}
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if !items[0].TriggerTime.Equal(want) {
		t.Errorf("expected trigger time %v, got %v", want, items[0].TriggerTime)
	}
	if !items[1].TriggerTime.IsZero() {
		t.Errorf("description-only trigger must not set TriggerTime, got %v", items[1].TriggerTime)
	}
}

func TestGitLabDiscoverPipelineAndCommentTriggerTimes(t *testing.T) {
	fx := gitlabFixture{
		mrs: []gitlabItem{{IID: 5, Title: "red", HeadPipeline: &gitlabPipeline{Status: "failed", FinishedAt: "2026-01-03T00:00:00Z"}}},
		notes: map[string][]gitlabNote{
			"merge_requests/5": {{Body: "/kelos fix", Author: gitlabUser{Username: "alice"}, CreatedAt: "2026-01-02T00:00:00Z"}},
		},
	}
	server := newGitLabTestServer(t, fx, nil)
	defer server.Close()

	s := &GitLabSource{BaseURL: server.URL, Project: "group/sub/repo", Types: []string{"mergeRequests"}, PipelineStatus: "failed", TriggerComment: "/kelos fix"}
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %+v", items)
	}
	want := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	if !items[0].TriggerTime.Equal(want) {
		t.Errorf("expected the later pipeline time to win, got %v", items[0].TriggerTime)
	}
}

func TestGitLabDiscoverPagination(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/notes") {
			json.NewEncoder(w).Encode([]gitlabNote{})
			return
		}
		page := r.URL.Query().Get("page")
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		switch page {
		case "1":
			w.Header().Set("X-Next-Page", "2")
			json.NewEncoder(w).Encode([]gitlabItem{{IID: 1}})
		case "2":
			w.Header().Set("X-Next-Page", "")
			json.NewEncoder(w).Encode([]gitlabItem{{IID: 2}})
		default:
			t.Errorf("unexpected page %q", page)
		}
	}))
	defer server.Close()

	s := &GitLabSource{BaseURL: server.URL, Project: "group/repo"}
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items across pages, got %d", len(items))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Errorf("expected pages [1 2], got %v", pages)
	}
}

func TestGitLabDiscoverAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"401 Unauthorized"}`))
	}))
	defer server.Close()

	s := &GitLabSource{BaseURL: server.URL, Project: "group/repo"}
	if _, err := s.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Errorf("expected 401 error, got %v", err)
	}
}
