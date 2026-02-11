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
	// maxBitbucketPages limits the number of pages fetched from the Bitbucket API.
	maxBitbucketPages = 10

	// defaultBitbucketLimit is the number of results per page.
	defaultBitbucketLimit = 100
)

// BitbucketDataCenterSource discovers pull requests from a Bitbucket Data Center repository.
type BitbucketDataCenterSource struct {
	BaseURL string // e.g. "https://bitbucket.example.com"
	Project string // Bitbucket project key
	Repo    string // Repository slug
	State   string // OPEN, MERGED, DECLINED, ALL
	Token   string // HTTP access token for authentication
	Client  *http.Client
}

type bitbucketPR struct {
	ID          int              `json:"id"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	State       string           `json:"state"`
	Links       bitbucketPRLinks `json:"links"`
}

type bitbucketPRLinks struct {
	Self []bitbucketLink `json:"self"`
}

type bitbucketLink struct {
	Href string `json:"href"`
}

type bitbucketPage struct {
	Values        json.RawMessage `json:"values"`
	Size          int             `json:"size"`
	IsLastPage    bool            `json:"isLastPage"`
	NextPageStart int             `json:"nextPageStart"`
}

type bitbucketActivity struct {
	Action  string           `json:"action"`
	Comment *bitbucketPRNote `json:"comment,omitempty"`
}

type bitbucketPRNote struct {
	Text string `json:"text"`
}

func (s *BitbucketDataCenterSource) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

// Discover fetches pull requests from Bitbucket Data Center and returns them as WorkItems.
func (s *BitbucketDataCenterSource) Discover(ctx context.Context) ([]WorkItem, error) {
	prs, err := s.fetchAllPRs(ctx)
	if err != nil {
		return nil, err
	}

	var items []WorkItem
	for _, pr := range prs {
		comments, err := s.fetchPRComments(ctx, pr.ID)
		if err != nil {
			return nil, fmt.Errorf("fetching comments for PR #%d: %w", pr.ID, err)
		}

		prURL := ""
		if len(pr.Links.Self) > 0 {
			prURL = pr.Links.Self[0].Href
		}

		items = append(items, WorkItem{
			ID:       strconv.Itoa(pr.ID),
			Number:   pr.ID,
			Title:    pr.Title,
			Body:     pr.Description,
			URL:      prURL,
			Comments: comments,
			Kind:     "PR",
		})
	}

	return items, nil
}

func (s *BitbucketDataCenterSource) fetchAllPRs(ctx context.Context) ([]bitbucketPR, error) {
	var allPRs []bitbucketPR
	start := 0

	for page := 0; page < maxBitbucketPages; page++ {
		prs, nextStart, isLast, err := s.fetchPRsPage(ctx, start)
		if err != nil {
			return nil, err
		}
		allPRs = append(allPRs, prs...)
		if isLast {
			break
		}
		start = nextStart
	}

	return allPRs, nil
}

func (s *BitbucketDataCenterSource) fetchPRsPage(ctx context.Context, start int) ([]bitbucketPR, int, bool, error) {
	u := fmt.Sprintf("%s/rest/api/1.0/projects/%s/repos/%s/pull-requests", s.BaseURL, s.Project, s.Repo)

	params := url.Values{}
	params.Set("limit", strconv.Itoa(defaultBitbucketLimit))
	params.Set("start", strconv.Itoa(start))

	state := s.State
	if state == "" {
		state = "OPEN"
	}
	if state != "ALL" {
		params.Set("state", state)
	}

	reqURL := u + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, false, fmt.Errorf("creating request: %w", err)
	}

	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, 0, false, fmt.Errorf("fetching pull requests: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, 0, false, fmt.Errorf("Bitbucket API returned status %d: %s", resp.StatusCode, string(body))
	}

	var page bitbucketPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, 0, false, fmt.Errorf("decoding response: %w", err)
	}

	var prs []bitbucketPR
	if err := json.Unmarshal(page.Values, &prs); err != nil {
		return nil, 0, false, fmt.Errorf("decoding pull requests: %w", err)
	}

	return prs, page.NextPageStart, page.IsLastPage, nil
}

func (s *BitbucketDataCenterSource) fetchPRComments(ctx context.Context, prID int) (string, error) {
	u := fmt.Sprintf("%s/rest/api/1.0/projects/%s/repos/%s/pull-requests/%d/activities",
		s.BaseURL, s.Project, s.Repo, prID)

	params := url.Values{}
	params.Set("limit", strconv.Itoa(defaultBitbucketLimit))
	reqURL := u + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching activities: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("Bitbucket API returned status %d: %s", resp.StatusCode, string(body))
	}

	var page bitbucketPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return "", fmt.Errorf("decoding activities response: %w", err)
	}

	var activities []bitbucketActivity
	if err := json.Unmarshal(page.Values, &activities); err != nil {
		return "", fmt.Errorf("decoding activities: %w", err)
	}

	var parts []string
	totalBytes := 0
	for _, a := range activities {
		if a.Action != "COMMENTED" || a.Comment == nil {
			continue
		}
		totalBytes += len(a.Comment.Text)
		if totalBytes > maxCommentBytes {
			break
		}
		parts = append(parts, a.Comment.Text)
	}

	return strings.Join(parts, "\n---\n"), nil
}
