package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SourceKindMergeRequest is the AnnotationSourceKind value for GitLab merge
// requests. Issues and pull requests use "issue" and "pull-request".
const SourceKindMergeRequest = "merge-request"

// GitLabReporter posts and updates notes on GitLab issues and merge requests.
// TokenFunc, when set, is called on every API request; otherwise Token is used.
type GitLabReporter struct {
	// BaseURL is the GitLab instance URL (e.g. "https://gitlab.example.com").
	BaseURL string
	// Project is the full project path (e.g. "group/subgroup/project").
	Project   string
	Token     string
	TokenFunc func() string
	Client    *http.Client
}

type gitlabNote struct {
	ID     int64      `json:"id"`
	Body   string     `json:"body"`
	Author gitlabUser `json:"author"`
}

type gitlabUser struct {
	Username string `json:"username"`
}

func (r *GitLabReporter) httpClient() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

// notesURL returns the notes collection for the target, e.g.
// https://gitlab.example.com/api/v4/projects/group%2Frepo/issues/42/notes.
func (r *GitLabReporter) notesURL(target CommentTarget) string {
	resource := "issues"
	if target.Kind == SourceKindMergeRequest {
		resource = "merge_requests"
	}
	return fmt.Sprintf("%s/api/v4/projects/%s/%s/%d/notes", strings.TrimRight(r.BaseURL, "/"), url.PathEscape(r.Project), resource, target.Number)
}

// FindCommentByMarker returns the newest note authored by the token's user
// that contains marker, or zero when none exists.
func (r *GitLabReporter) FindCommentByMarker(ctx context.Context, target CommentTarget, marker string) (int64, error) {
	var me gitlabUser
	if _, err := r.do(ctx, http.MethodGet, strings.TrimRight(r.BaseURL, "/")+"/api/v4/user", nil, http.StatusOK, &me); err != nil {
		return 0, fmt.Errorf("getting authenticated GitLab user: %w", err)
	}

	var foundID int64
	for page := 1; ; page++ {
		var notes []gitlabNote
		resp, err := r.do(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&sort=asc&order_by=created_at&page=%d", r.notesURL(target), page), nil, http.StatusOK, &notes)
		if err != nil {
			return 0, fmt.Errorf("listing notes: %w", err)
		}
		for _, note := range notes {
			if strings.Contains(note.Body, marker) && strings.EqualFold(note.Author.Username, me.Username) {
				foundID = note.ID
			}
		}
		if resp.Header.Get("X-Next-Page") == "" || len(notes) == 0 {
			return foundID, nil
		}
	}
}

// CreateComment creates a note on the target and returns the note ID.
func (r *GitLabReporter) CreateComment(ctx context.Context, target CommentTarget, body string) (int64, error) {
	var note gitlabNote
	if _, err := r.do(ctx, http.MethodPost, r.notesURL(target), map[string]string{"body": body}, http.StatusCreated, &note); err != nil {
		return 0, fmt.Errorf("posting note: %w", err)
	}
	return note.ID, nil
}

// UpdateComment replaces the body of an existing note on the target.
func (r *GitLabReporter) UpdateComment(ctx context.Context, target CommentTarget, commentID int64, body string) error {
	if _, err := r.do(ctx, http.MethodPut, fmt.Sprintf("%s/%d", r.notesURL(target), commentID), map[string]string{"body": body}, http.StatusOK, nil); err != nil {
		return fmt.Errorf("updating note: %w", err)
	}
	return nil
}

func (r *GitLabReporter) resolveToken() string {
	if r.TokenFunc != nil {
		return r.TokenFunc()
	}
	return r.Token
}

// do sends a JSON request and decodes the response into out when the status
// matches wantStatus; any other status is returned as an error.
func (r *GitLabReporter) do(ctx context.Context, method, endpoint string, payload interface{}, wantStatus int, out interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshalling request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if token := r.resolveToken(); token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != wantStatus {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp, fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(errBody))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp, fmt.Errorf("decoding response: %w", err)
		}
	}
	return resp, nil
}
