package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ChecksReporter creates and updates GitHub Check Runs via the Checks API.
// It follows the same authentication pattern as GitHubReporter: TokenFunc is
// called on every request when set, falling back to the static Token field.
type ChecksReporter struct {
	Owner     string
	Repo      string
	Token     string        // static token (used when TokenFunc is nil)
	TokenFunc func() string // dynamic token resolver; takes precedence over Token
	BaseURL   string
	Client    *http.Client
}

type createCheckRunRequest struct {
	Name        string          `json:"name"`
	HeadSHA     string          `json:"head_sha"`
	Status      string          `json:"status"`
	Conclusion  string          `json:"conclusion,omitempty"`
	Output      *checkRunOutput `json:"output,omitempty"`
	StartedAt   *string         `json:"started_at,omitempty"`
	CompletedAt *string         `json:"completed_at,omitempty"`
}

type updateCheckRunRequest struct {
	Name        string          `json:"name,omitempty"`
	Status      string          `json:"status"`
	Conclusion  string          `json:"conclusion,omitempty"`
	Output      *checkRunOutput `json:"output,omitempty"`
	CompletedAt *string         `json:"completed_at,omitempty"`
}

type checkRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type checkRunResponse struct {
	ID int64 `json:"id"`
}

// CreateCheckRun creates a new GitHub Check Run and returns the check run ID.
// When status is "completed", conclusion must be set (e.g. "success", "failure").
func (r *ChecksReporter) CreateCheckRun(ctx context.Context, name, headSHA, status, conclusion string, output *checkRunOutput) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/check-runs", r.baseURL(), r.Owner, r.Repo)

	now := time.Now().UTC().Format(time.RFC3339)
	reqBody := createCheckRunRequest{
		Name:       name,
		HeadSHA:    headSHA,
		Status:     status,
		Conclusion: conclusion,
		Output:     output,
		StartedAt:  &now,
	}
	if status == "completed" {
		reqBody.CompletedAt = &now
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("marshalling check run request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("creating check run: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	var result checkRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decoding check run response: %w", err)
	}

	return result.ID, nil
}

// UpdateCheckRun updates an existing GitHub Check Run.
func (r *ChecksReporter) UpdateCheckRun(ctx context.Context, checkRunID int64, status, conclusion string, output *checkRunOutput) error {
	url := fmt.Sprintf("%s/repos/%s/%s/check-runs/%s", r.baseURL(), r.Owner, r.Repo, strconv.FormatInt(checkRunID, 10))

	reqBody := updateCheckRunRequest{
		Status:     status,
		Conclusion: conclusion,
		Output:     output,
	}
	if status == "completed" {
		now := time.Now().UTC().Format(time.RFC3339)
		reqBody.CompletedAt = &now
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshalling check run update: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("updating check run: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	return nil
}

func (r *ChecksReporter) baseURL() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	return defaultBaseURL
}

func (r *ChecksReporter) httpClient() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

func (r *ChecksReporter) resolveToken() string {
	if r.TokenFunc != nil {
		return r.TokenFunc()
	}
	return r.Token
}

func (r *ChecksReporter) setHeaders(req *http.Request) {
	if token := r.resolveToken(); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
}
