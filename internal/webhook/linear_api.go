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

// linearIssueDetailsQuery is the GraphQL query to fetch labels and description for an issue.
const linearIssueDetailsQuery = `query IssueDetails($id: String!) {
  issue(id: $id) {
    description
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
			Description *string `json:"description"`
			Labels      struct {
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

// linearIssueDetails holds the enriched fields returned by the Linear API.
type linearIssueDetails struct {
	Labels      []string
	Description *string // nil when the API did not return a description
}

// fetchLinearIssueDetails fetches labels and description for a Linear issue by
// ID using the GraphQL API. The API key is read from the LINEAR_API_KEY
// environment variable. Returns nil (no error) if the env var is not set,
// allowing callers to fall back to whatever data is in the webhook payload.
func fetchLinearIssueDetails(ctx context.Context, issueID string) (*linearIssueDetails, error) {
	apiKey := os.Getenv(linearAPIKeyEnvVar)
	return fetchLinearIssueDetailsFromURL(ctx, defaultLinearAPIURL, apiKey, issueID)
}

// fetchLinearIssueDetailsFromURL is the testable core of fetchLinearIssueDetails.
// It accepts the API URL and key explicitly.
func fetchLinearIssueDetailsFromURL(ctx context.Context, apiURL, apiKey, issueID string) (*linearIssueDetails, error) {
	if apiKey == "" {
		return nil, nil
	}

	reqBody := linearGraphQLRequest{
		Query: linearIssueDetailsQuery,
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

	details := &linearIssueDetails{
		Description: gqlResp.Data.Issue.Description,
	}
	for _, node := range gqlResp.Data.Issue.Labels.Nodes {
		details.Labels = append(details.Labels, node.Name)
	}
	return details, nil
}
