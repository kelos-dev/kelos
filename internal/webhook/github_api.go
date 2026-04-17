package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"
)

// githubHTTPClient is used for all GitHub API requests, with a reasonable
// timeout to avoid blocking webhook processing if the API is unresponsive.
var githubHTTPClient = &http.Client{Timeout: 10 * time.Second}

// githubPRBranchFetcher is the function used to fetch a PR's head branch from
// the GitHub API. It is a package-level variable so tests can swap in a stub.
var githubPRBranchFetcher = fetchGitHubPRBranch

// githubTokenResolver resolves a GitHub API token. It must be set via
// SetGitHubTokenResolver before the webhook server starts processing events.
var githubTokenResolver func(context.Context) (string, error)

// SetGitHubTokenResolver sets the token resolver used for GitHub API calls
// (e.g. enriching issue_comment events with PR branch info).
func SetGitHubTokenResolver(resolver func(context.Context) (string, error)) {
	githubTokenResolver = resolver
}

// githubPRResponse is the minimal structure needed to extract the head branch
// from a GitHub pull request API response.
type githubPRResponse struct {
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// fetchGitHubPRBranch fetches the head branch for a pull request using the
// GitHub REST API. It resolves the token via the token provider, which supports
// both GITHUB_TOKEN (PAT) and GitHub App credentials.
// Returns ("", nil) if no credentials are configured, allowing callers to fall
// back gracefully.
func fetchGitHubPRBranch(ctx context.Context, prAPIURL string) (string, error) {
	if githubTokenResolver == nil {
		return "", nil
	}
	token, err := githubTokenResolver(ctx)
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
