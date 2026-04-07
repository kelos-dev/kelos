package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/kelos-dev/kelos/internal/githubapp"
)

const (
	githubTokenEnvVar          = "GITHUB_TOKEN"
	githubAppIDEnvVar          = "GITHUB_APP_ID"
	githubInstallationIDEnvVar = "GITHUB_APP_INSTALLATION_ID"
	githubPrivateKeyEnvVar     = "GITHUB_APP_PRIVATE_KEY"
)

// githubHTTPClient is used for all GitHub API requests, with a reasonable
// timeout to avoid blocking webhook processing if the API is unresponsive.
var githubHTTPClient = &http.Client{Timeout: 10 * time.Second}

// githubPRBranchFetcher is the function used to fetch a PR's head branch from
// the GitHub API. It is a package-level variable so tests can swap in a stub.
var githubPRBranchFetcher = fetchGitHubPRBranch

// githubTokenProvider resolves a GitHub API token from the environment. It
// supports both a static GITHUB_TOKEN (PAT) and GitHub App credentials. When
// App credentials are configured, installation tokens are cached and
// automatically refreshed before expiry.
var githubTokenProvider = &tokenProvider{}

// githubPRResponse is the minimal structure needed to extract the head branch
// from a GitHub pull request API response.
type githubPRResponse struct {
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// tokenProvider resolves GitHub API tokens. It prefers a static GITHUB_TOKEN
// but falls back to GitHub App installation tokens when App credentials are
// present. Installation tokens are cached with a safety margin before expiry.
type tokenProvider struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// Token returns a valid GitHub API token, or "" if no credentials are configured.
func (tp *tokenProvider) Token(ctx context.Context) (string, error) {
	// Fast path: static PAT
	if pat := os.Getenv(githubTokenEnvVar); pat != "" {
		return pat, nil
	}

	// Check for GitHub App credentials
	appID := os.Getenv(githubAppIDEnvVar)
	installID := os.Getenv(githubInstallationIDEnvVar)
	privateKey := os.Getenv(githubPrivateKeyEnvVar)
	if appID == "" || installID == "" || privateKey == "" {
		return "", nil
	}

	tp.mu.Lock()
	defer tp.mu.Unlock()

	// Return cached token if still valid (with 5-minute safety margin)
	if tp.token != "" && time.Now().Add(5*time.Minute).Before(tp.expiresAt) {
		return tp.token, nil
	}

	// Generate a new installation token
	creds, err := githubapp.ParseCredentials(map[string][]byte{
		"appID":          []byte(appID),
		"installationID": []byte(installID),
		"privateKey":     []byte(privateKey),
	})
	if err != nil {
		return "", fmt.Errorf("parsing GitHub App credentials: %w", err)
	}

	tc := githubapp.NewTokenClient()
	resp, err := tc.GenerateInstallationToken(ctx, creds)
	if err != nil {
		return "", fmt.Errorf("generating GitHub App installation token: %w", err)
	}

	tp.token = resp.Token
	tp.expiresAt = resp.ExpiresAt
	return tp.token, nil
}

// fetchGitHubPRBranch fetches the head branch for a pull request using the
// GitHub REST API. It resolves the token via the token provider, which supports
// both GITHUB_TOKEN (PAT) and GitHub App credentials.
// Returns ("", nil) if no credentials are configured, allowing callers to fall
// back gracefully.
func fetchGitHubPRBranch(ctx context.Context, prAPIURL string) (string, error) {
	token, err := githubTokenProvider.Token(ctx)
	if err != nil {
		return "", err
	}
	return fetchGitHubPRBranchWithToken(ctx, prAPIURL, token)
}

// fetchGitHubPRBranchWithToken is the testable core of fetchGitHubPRBranch.
// It accepts the token explicitly.
func fetchGitHubPRBranchWithToken(ctx context.Context, prAPIURL, token string) (string, error) {
	if token == "" {
		return "", nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prAPIURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating GitHub API request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var pr githubPRResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", fmt.Errorf("decoding GitHub API response: %w", err)
	}

	return pr.Head.Ref, nil
}

// enrichGitHubIssueCommentBranch fetches the PR's head branch from the GitHub
// API and sets it on the event data. This is called lazily for issue_comment
// events on pull requests, since GitHub does not include the PR's head ref in
// the issue_comment webhook payload.
func enrichGitHubIssueCommentBranch(ctx context.Context, log logr.Logger, eventData *GitHubEventData) {
	if eventData.PullRequestAPIURL == "" {
		return
	}

	branch, err := githubPRBranchFetcher(ctx, eventData.PullRequestAPIURL)
	if err != nil {
		log.Error(err, "Failed to fetch PR branch for issue_comment event", "prAPIURL", eventData.PullRequestAPIURL)
		return
	}
	if branch == "" {
		log.Info("No GitHub credentials configured, cannot enrich issue_comment event with PR branch")
		return
	}

	log.Info("Enriched issue_comment event with PR branch", "branch", branch)
	eventData.Branch = branch
}
