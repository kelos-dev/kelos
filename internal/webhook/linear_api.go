package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	defaultLinearAPIURL = "https://api.linear.app/graphql"
	linearAPIKeyEnvVar  = "LINEAR_API_KEY"
)

// linearHTTPClient is used for all Linear API requests, with a reasonable
// timeout to avoid blocking webhook processing if the API is unresponsive.
var linearHTTPClient = &http.Client{Timeout: 10 * time.Second}

// linearIssueLabelsQuery is the GraphQL query to fetch labels for an issue.
const linearIssueLabelsQuery = `query IssueLabels($id: String!) {
  issue(id: $id) {
    labels {
      nodes {
        name
      }
    }
  }
}`

type linearGraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

type linearGraphQLResponse struct {
	Data struct {
		Issue struct {
			Labels struct {
				Nodes []struct {
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"issue"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

// fetchLinearIssueLabels fetches labels for a Linear issue by ID using the
// GraphQL API. The API key is read from the LINEAR_API_KEY environment variable.
// Returns nil (no error) if the env var is not set, allowing callers to fall
// back to whatever labels are in the webhook payload.
func fetchLinearIssueLabels(ctx context.Context, issueID string) ([]string, error) {
	apiKey := os.Getenv(linearAPIKeyEnvVar)
	return fetchLinearIssueLabelsFromURL(ctx, defaultLinearAPIURL, apiKey, issueID)
}

// fetchLinearIssueLabelsFromURL is the testable core of fetchLinearIssueLabels.
// It accepts the API URL and key explicitly.
func fetchLinearIssueLabelsFromURL(ctx context.Context, apiURL, apiKey, issueID string) ([]string, error) {
	if apiKey == "" {
		return nil, nil
	}

	reqBody := linearGraphQLRequest{
		Query: linearIssueLabelsQuery,
		Variables: map[string]interface{}{
			"id": issueID,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling Linear GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("creating Linear API request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	resp, err := linearHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Linear API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("Linear API returned status %d: %s", resp.StatusCode, string(body))
	}

	var gqlResp linearGraphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, fmt.Errorf("decoding Linear API response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("Linear API error: %s", gqlResp.Errors[0].Message)
	}

	var labels []string
	for _, node := range gqlResp.Data.Issue.Labels.Nodes {
		labels = append(labels, node.Name)
	}
	return labels, nil
}
