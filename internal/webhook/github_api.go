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

// githubPRHeadInfo contains head branch and SHA returned from the GitHub API.
type githubPRHeadInfo struct {
	Branch string
	SHA    string
}

// githubPRBranchFetcher is the function used to fetch a PR's head info from
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
// and SHA from a GitHub pull request API response.
type githubPRResponse struct {
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

// fetchGitHubPRBranch fetches the head branch and SHA for a pull request using
// the GitHub REST API. It resolves the token via the token provider, which
// supports both GITHUB_TOKEN (PAT) and GitHub App credentials.
// Returns a zero-value githubPRHeadInfo when no credentials are configured,
// allowing callers to fall back gracefully.
func fetchGitHubPRBranch(ctx context.Context, prAPIURL string) (githubPRHeadInfo, error) {
	if githubTokenResolver == nil {
		return githubPRHeadInfo{}, nil
	}
	token, err := githubTokenResolver(ctx)
	if err != nil {
		return githubPRHeadInfo{}, err
	}
	return fetchGitHubPRBranchWithToken(ctx, prAPIURL, token)
}

// fetchGitHubPRBranchWithToken is the testable core of fetchGitHubPRBranch.
// It accepts the token explicitly.
func fetchGitHubPRBranchWithToken(ctx context.Context, prAPIURL, token string) (githubPRHeadInfo, error) {
	if token == "" {
		return githubPRHeadInfo{}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prAPIURL, nil)
	if err != nil {
		return githubPRHeadInfo{}, fmt.Errorf("creating GitHub API request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return githubPRHeadInfo{}, fmt.Errorf("calling GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return githubPRHeadInfo{}, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var pr githubPRResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return githubPRHeadInfo{}, fmt.Errorf("decoding GitHub API response: %w", err)
	}

	return githubPRHeadInfo{Branch: pr.Head.Ref, SHA: pr.Head.SHA}, nil
}

// enrichGitHubIssueCommentBranch fetches the PR's head branch and SHA from the
// GitHub API and sets them on the event data. This is called lazily for
// issue_comment events on pull requests, since GitHub does not include the PR's
// head ref or SHA in the issue_comment webhook payload.
func enrichGitHubIssueCommentBranch(ctx context.Context, log logr.Logger, eventData *GitHubEventData) {
	if eventData.PullRequestAPIURL == "" {
		return
	}

	head, err := githubPRBranchFetcher(ctx, eventData.PullRequestAPIURL)
	if err != nil {
		log.Error(err, "Failed to fetch PR head for issue_comment event", "prAPIURL", eventData.PullRequestAPIURL)
		return
	}
	if head.Branch == "" {
		log.Info("No GitHub credentials configured, cannot enrich issue_comment event with PR head")
		return
	}

	log.Info("Enriched issue_comment event with PR head", "branch", head.Branch, "sha", head.SHA)
	eventData.Branch = head.Branch
	eventData.HeadSHA = head.SHA
}
