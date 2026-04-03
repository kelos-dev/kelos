package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLinearIssueLabels(t *testing.T) {
	tests := []struct {
		name       string
		response   linearGraphQLResponse
		statusCode int
		wantLabels []string
		wantErr    bool
	}{
		{
			name: "successful response with labels",
			response: linearGraphQLResponse{
				Data: struct {
					Issue struct {
						Labels struct {
							Nodes []struct {
								Name string `json:"name"`
							} `json:"nodes"`
						} `json:"labels"`
					} `json:"issue"`
				}{
					Issue: struct {
						Labels struct {
							Nodes []struct {
								Name string `json:"name"`
							} `json:"nodes"`
						} `json:"labels"`
					}{
						Labels: struct {
							Nodes []struct {
								Name string `json:"name"`
							} `json:"nodes"`
						}{
							Nodes: []struct {
								Name string `json:"name"`
							}{
								{Name: "bug"},
								{Name: "priority:high"},
							},
						},
					},
				},
			},
			statusCode: http.StatusOK,
			wantLabels: []string{"bug", "priority:high"},
		},
		{
			name: "empty labels",
			response: linearGraphQLResponse{
				Data: struct {
					Issue struct {
						Labels struct {
							Nodes []struct {
								Name string `json:"name"`
							} `json:"nodes"`
						} `json:"labels"`
					} `json:"issue"`
				}{},
			},
			statusCode: http.StatusOK,
			wantLabels: nil,
		},
		{
			name:       "API error status",
			statusCode: http.StatusUnauthorized,
			wantErr:    true,
		},
		{
			name: "GraphQL error",
			response: linearGraphQLResponse{
				Errors: []struct {
					Message string `json:"message"`
				}{
					{Message: "Issue not found"},
				},
			},
			statusCode: http.StatusOK,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request structure
				if r.Method != http.MethodPost {
					t.Errorf("Expected POST, got %s", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
				}
				if r.Header.Get("Authorization") == "" {
					t.Error("Expected Authorization header")
				}

				// Verify request body contains the issue ID
				var req linearGraphQLRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request body: %v", err)
				}
				if req.Variables["id"] != "issue-123" {
					t.Errorf("Expected issue ID 'issue-123', got %v", req.Variables["id"])
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					json.NewEncoder(w).Encode(tt.response)
				} else {
					w.Write([]byte("error"))
				}
			}))
			defer server.Close()

			labels, err := fetchLinearIssueLabelsFromURL(context.Background(), server.URL, "test-api-key", "issue-123")

			if (err != nil) != tt.wantErr {
				t.Errorf("fetchLinearIssueLabels() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(labels) != len(tt.wantLabels) {
					t.Errorf("fetchLinearIssueLabels() = %v, want %v", labels, tt.wantLabels)
					return
				}
				for i, label := range labels {
					if label != tt.wantLabels[i] {
						t.Errorf("fetchLinearIssueLabels()[%d] = %v, want %v", i, label, tt.wantLabels[i])
					}
				}
			}
		})
	}
}

func TestFetchLinearIssueLabels_NoAPIKey(t *testing.T) {
	// When no API key is provided, should return nil, nil
	labels, err := fetchLinearIssueLabelsFromURL(context.Background(), "http://unused", "", "issue-123")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if labels != nil {
		t.Errorf("Expected nil labels, got %v", labels)
	}
}
