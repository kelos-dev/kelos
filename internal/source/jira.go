package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// maxJiraResults limits the number of results per page from the Jira API.
	maxJiraResults = 100

	// maxJiraPages limits the number of pages fetched from the Jira API.
	maxJiraPages = 10

	// maxJiraCommentBytes limits the total size of concatenated comments per issue.
	maxJiraCommentBytes = 64 * 1024
)

// JiraSource discovers issues from a Jira project.
type JiraSource struct {
	BaseURL string
	Project string
	JQL     string
	User    string
	Token   string
	Client  *http.Client
}

type jiraSearchResponse struct {
	Issues        []jiraIssue `json:"issues"`
	NextPageToken string      `json:"nextPageToken"`
	IsLast        bool        `json:"isLast"`
}

type jiraIssue struct {
	Key    string          `json:"key"`
	Fields jiraIssueFields `json:"fields"`
}

type jiraIssueFields struct {
	Summary   string         `json:"summary"`
	Status    *jiraStatus    `json:"status,omitempty"`
	Labels    []string       `json:"labels"`
	Comment   *jiraComments  `json:"comment,omitempty"`
	IssueType *jiraIssueType `json:"issuetype,omitempty"`
}

type jiraStatus struct {
	Name string `json:"name"`
}

type jiraIssueType struct {
	Name string `json:"name"`
}

type jiraComments struct {
	Comments []jiraComment `json:"comments"`
}

type jiraComment struct {
	Body interface{} `json:"body"`
}

func (s *JiraSource) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

// Discover fetches issues from Jira and returns them as WorkItems.
func (s *JiraSource) Discover(ctx context.Context) ([]WorkItem, error) {
	issues, err := s.fetchAllIssues(ctx)
	if err != nil {
		return nil, err
	}

	var items []WorkItem
	for _, issue := range issues {
		number := extractIssueNumber(issue.Key)

		comments := extractComments(issue.Fields.Comment)

		kind := "Issue"
		if issue.Fields.IssueType != nil {
			kind = issue.Fields.IssueType.Name
		}

		items = append(items, WorkItem{
			ID:       issue.Key,
			Number:   number,
			Title:    issue.Fields.Summary,
			URL:      fmt.Sprintf("%s/browse/%s", strings.TrimRight(s.BaseURL, "/"), issue.Key),
			Labels:   issue.Fields.Labels,
			Comments: comments,
			Kind:     kind,
		})
	}

	return items, nil
}

func (s *JiraSource) fetchAllIssues(ctx context.Context) ([]jiraIssue, error) {
	var allIssues []jiraIssue
	var nextPageToken string

	for page := 0; page < maxJiraPages; page++ {
		result, err := s.fetchIssuesPage(ctx, nextPageToken)
		if err != nil {
			return nil, err
		}
		allIssues = append(allIssues, result.Issues...)

		if result.IsLast || result.NextPageToken == "" {
			break
		}
		nextPageToken = result.NextPageToken
	}

	return allIssues, nil
}

func (s *JiraSource) buildJQL() string {
	if s.JQL != "" {
		filter, orderBy := splitJQLOrderBy(s.JQL)
		if orderBy != "" {
			return fmt.Sprintf("project = %s AND (%s) %s", s.Project, filter, orderBy)
		}
		return fmt.Sprintf("project = %s AND (%s)", s.Project, filter)
	}
	return fmt.Sprintf("project = %s", s.Project)
}

// splitJQLOrderBy splits a JQL string into filter conditions and an optional
// ORDER BY clause. JQL's ORDER BY must appear at the top level, not inside
// parentheses, so we extract it before wrapping filter conditions.
func splitJQLOrderBy(jql string) (filter, orderBy string) {
	upper := strings.ToUpper(jql)
	idx := strings.LastIndex(upper, "ORDER BY")
	if idx < 0 {
		return strings.TrimSpace(jql), ""
	}
	return strings.TrimSpace(jql[:idx]), strings.TrimSpace(jql[idx:])
}

func (s *JiraSource) fetchIssuesPage(ctx context.Context, nextPageToken string) (*jiraSearchResponse, error) {
	u, err := url.Parse(strings.TrimRight(s.BaseURL, "/") + "/rest/api/2/search/jql")
	if err != nil {
		return nil, fmt.Errorf("parsing base URL: %w", err)
	}

	params := url.Values{}
	params.Set("jql", s.buildJQL())
	params.Set("maxResults", strconv.Itoa(maxJiraResults))
	if nextPageToken != "" {
		params.Set("nextPageToken", nextPageToken)
	}
	params.Set("fields", "summary,status,labels,comment,issuetype")
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	if s.Token != "" {
		if s.User != "" {
			// Jira Cloud: Basic auth with email + API token
			req.SetBasicAuth(s.User, s.Token)
		} else {
			// Jira Data Center/Server: Bearer auth with PAT
			req.Header.Set("Authorization", "Bearer "+s.Token)
		}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching issues: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("Jira API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result jiraSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &result, nil
}

// extractIssueNumber extracts the numeric part from a Jira issue key (e.g., "PROJ-42" -> 42).
func extractIssueNumber(key string) int {
	parts := strings.SplitN(key, "-", 2)
	if len(parts) == 2 {
		if n, err := strconv.Atoi(parts[1]); err == nil {
			return n
		}
	}
	return 0
}

// extractComments concatenates comment bodies from a Jira issue, capped at maxJiraCommentBytes.
func extractComments(comments *jiraComments) string {
	if comments == nil {
		return ""
	}

	var parts []string
	totalBytes := 0
	for _, c := range comments.Comments {
		body := commentBodyToString(c.Body)
		if body == "" {
			continue
		}
		totalBytes += len(body)
		if totalBytes > maxJiraCommentBytes {
			break
		}
		parts = append(parts, body)
	}

	return strings.Join(parts, "\n---\n")
}

// commentBodyToString handles Jira comment bodies which can be a plain string
// (Jira Server/Data Center) or an Atlassian Document Format object (Jira Cloud).
func commentBodyToString(body interface{}) string {
	switch v := body.(type) {
	case string:
		return v
	case map[string]interface{}:
		// Atlassian Document Format: extract text from content nodes
		return extractADFText(v)
	default:
		return ""
	}
}

// extractADFText recursively extracts plain text from an Atlassian Document Format node.
func extractADFText(node map[string]interface{}) string {
	// If this node has a "text" field, return it
	if text, ok := node["text"].(string); ok {
		return text
	}

	// Otherwise recurse into "content" array
	content, ok := node["content"].([]interface{})
	if !ok {
		return ""
	}

	var parts []string
	for _, item := range content {
		if child, ok := item.(map[string]interface{}); ok {
			if text := extractADFText(child); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
