package githubapp

import (
	"context"
	"fmt"
	"strings"
)

// NewSecretTokenResolver builds a GitHub API token resolver from credentials
// Secret data: a personal access token under the GITHUB_TOKEN key, or GitHub
// App credentials (appID, installationID, privateKey). App installation tokens
// are minted against apiBaseURL, so a GitHub Enterprise deployment mints against
// its own API endpoint. Returns nil when no usable credentials are present.
func NewSecretTokenResolver(secretData map[string][]byte, apiBaseURL string) (func(context.Context) (string, error), error) {
	if token := strings.TrimSpace(string(secretData["GITHUB_TOKEN"])); token != "" {
		return func(context.Context) (string, error) { return token, nil }, nil
	}

	// Count how many GitHub App keys are present and non-empty. A partial set is
	// a misconfiguration, not "no credentials": surface it instead of silently
	// falling back to unauthenticated requests.
	appKeys := 0
	for _, k := range []string{"appID", "installationID", "privateKey"} {
		if len(strings.TrimSpace(string(secretData[k]))) > 0 {
			appKeys++
		}
	}
	switch appKeys {
	case 0:
		return nil, nil
	case 3:
		creds, err := ParseCredentials(secretData)
		if err != nil {
			return nil, fmt.Errorf("parsing GitHub App credentials: %w", err)
		}
		tc := NewTokenClient()
		if apiBaseURL != "" {
			tc.BaseURL = apiBaseURL
		}
		return NewTokenProvider(tc, creds).Token, nil
	default:
		return nil, fmt.Errorf("incomplete GitHub App credentials: provide all of appID, installationID, and privateKey")
	}
}
