package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

const defaultBaseURL = "https://api.github.com"

// GitHubReporter posts and updates issue/PR comments on GitHub.
// TokenFunc, when set, is called on every API request to resolve the current
// token. This supports dynamic credentials such as GitHub App installation
// tokens that are refreshed in-process. When TokenFunc is nil the static
// Token field is used instead.
type GitHubReporter struct {
	Owner     string
	Repo      string
	Token     string        // static token (used when TokenFunc is nil)
	TokenFunc func() string // dynamic token resolver; takes precedence over Token
	BaseURL   string
	Client    *http.Client
}

func (r *GitHubReporter) baseURL() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	return defaultBaseURL
}

func (r *GitHubReporter) httpClient() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

type createCommentRequest struct {
	Body string `json:"body"`
}

type commentResponse struct {
	ID int64 `json:"id"`
}

// CreateComment creates a comment on a GitHub issue or pull request and returns
// the comment ID.
func (r *GitHubReporter) CreateComment(ctx context.Context, number int, body string) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", r.baseURL(), r.Owner, r.Repo, number)

	payload, err := json.Marshal(createCommentRequest{Body: body})
	if err != nil {
		return 0, fmt.Errorf("marshalling comment body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("posting comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	var result commentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decoding comment response: %w", err)
	}

	return result.ID, nil
}

// UpdateComment updates an existing GitHub comment by its ID.
func (r *GitHubReporter) UpdateComment(ctx context.Context, commentID int64, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/comments/%s", r.baseURL(), r.Owner, r.Repo, strconv.FormatInt(commentID, 10))

	payload, err := json.Marshal(createCommentRequest{Body: body})
	if err != nil {
		return fmt.Errorf("marshalling comment body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("updating comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	return nil
}

// resolveToken returns the current GitHub token. When TokenFunc is set it
// is called to resolve the token dynamically. Falls back to the static
// Token field.
func (r *GitHubReporter) resolveToken() string {
	if r.TokenFunc != nil {
		return r.TokenFunc()
	}
	return r.Token
}

func (r *GitHubReporter) setHeaders(req *http.Request) {
	if token := r.resolveToken(); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")
}

// FormatAcceptedComment returns the comment body for an accepted task.
func FormatAcceptedComment(taskName string) string {
	return fmt.Sprintf("🤖 **Kelos Task Status**\n\nTask `%s` has been **accepted** and is being processed.", taskName)
}

// FormatSucceededComment returns the comment body for a succeeded task.
func FormatSucceededComment(taskName string) string {
	return fmt.Sprintf("🤖 **Kelos Task Status**\n\nTask `%s` has **succeeded**. ✅", taskName)
}

// FormatFailedComment returns the comment body for a failed task.
func FormatFailedComment(taskName string) string {
	return fmt.Sprintf("🤖 **Kelos Task Status**\n\nTask `%s` has **failed**. ❌", taskName)
}
