package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GitHubFeedback posts status comments on GitHub issues and pull requests.
type GitHubFeedback struct {
	Owner   string
	Repo    string
	Token   string
	BaseURL string
	Client  *http.Client
}

func (g *GitHubFeedback) baseURL() string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	return defaultBaseURL
}

func (g *GitHubFeedback) httpClient() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return http.DefaultClient
}

// PostComment posts a comment on the given issue or pull request number.
func (g *GitHubFeedback) PostComment(ctx context.Context, number int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments",
		g.baseURL(), g.Owner, g.Repo, number)

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("marshaling comment body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	if g.Token != "" {
		req.Header.Set("Authorization", "token "+g.Token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("posting comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// TaskStartedComment returns the comment body for a task that has started.
func TaskStartedComment(taskName string) string {
	return fmt.Sprintf("<!-- kelos:task:%s -->\n**Kelos Task Started**\n\nTask `%s` is now working on this issue.",
		taskName, taskName)
}

// TaskCompletedComment returns the comment body for a task that has completed.
func TaskCompletedComment(taskName string, succeeded bool, duration string, results map[string]string) string {
	marker := fmt.Sprintf("<!-- kelos:task:%s:result -->", taskName)

	status := "Succeeded"
	if !succeeded {
		status = "Failed"
	}

	body := fmt.Sprintf("%s\n**Kelos Task %s**\n\nTask `%s`", marker, status, taskName)
	if succeeded {
		body += " completed"
	} else {
		body += " failed"
	}
	if duration != "" {
		body += fmt.Sprintf(" after %s", duration)
	}
	body += "."

	if len(results) > 0 {
		body += "\n"
		if pr, ok := results["pr"]; ok {
			body += fmt.Sprintf("\n- **PR:** %s", pr)
		}
		if branch, ok := results["branch"]; ok {
			body += fmt.Sprintf("\n- **Branch:** `%s`", branch)
		}
		if commit, ok := results["commit"]; ok {
			body += fmt.Sprintf("\n- **Commit:** `%s`", commit)
		}
		if cost, ok := results["cost-usd"]; ok {
			body += fmt.Sprintf("\n- **Cost:** $%s", cost)
		}
	}

	return body
}
