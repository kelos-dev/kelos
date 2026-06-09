package source

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAikidoProxyURL = "http://cody-tools.kelos-system.svc.cluster.local:8080/aikido"

	aikidoOpenIssueGroupsPerPage   = 20
	aikidoMaxOpenIssueGroupPages   = 10
	aikidoIssueExportPerPage       = 20
	aikidoMaxIssueExportPages      = 50
	aikidoCodeRepositoriesPerPage  = 20
	aikidoMaxCodeRepositoryPages   = 50
	aikidoMaxRateLimitRetries      = 120
	aikidoDefaultRetryAfterMaxWait = 2 * time.Hour
	aikidoMaxBodyBytes             = 64 * 1024
)

const (
	aikidoHeaderClient        = "X-Cody-Aikido-Client"
	aikidoHeaderBudgetSeconds = "X-Cody-Aikido-Budget-Seconds"
	aikidoHeaderTaskSpawner   = "X-Cody-TaskSpawner"
	aikidoHeaderRunDate       = "X-Cody-Aikido-Run-Date"
	aikidoClientDiscovery     = "discovery"
	aikidoProxyRequestBudget  = 20 * time.Second
)

const (
	AikidoMetadataIssueGroupID     = "aikido.kelos.dev/issue-group-id"
	AikidoMetadataBranch           = "aikido.kelos.dev/branch"
	AikidoMetadataSeverity         = "aikido.kelos.dev/severity"
	AikidoMetadataStatus           = "aikido.kelos.dev/status"
	AikidoMetadataIssueType        = "aikido.kelos.dev/issue-type"
	AikidoMetadataRepositories     = "aikido.kelos.dev/repositories"
	AikidoMetadataCodeRepositories = "aikido.kelos.dev/code-repositories"
	AikidoMetadataAffectedPackages = "aikido.kelos.dev/affected-packages"
	AikidoMetadataCVEIDs           = "aikido.kelos.dev/cve-ids"
	AikidoMetadataURL              = "aikido.kelos.dev/url"
)

// AikidoSource discovers Aikido issue groups through the cody-tools Aikido proxy.
type AikidoSource struct {
	ProxyBaseURL      string
	Repositories      []string
	Statuses          []string
	Severities        []string
	Branch            string
	IssueTypes        []string
	UseIssueExport    bool
	TaskSpawnerName   string
	RetryAfterMaxWait time.Duration
	Client            *http.Client
}

func (s *AikidoSource) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *AikidoSource) proxyBaseURL() string {
	if strings.TrimSpace(s.ProxyBaseURL) != "" {
		return strings.TrimSpace(s.ProxyBaseURL)
	}
	return DefaultAikidoProxyURL
}

func (s *AikidoSource) retryAfterMaxWait() time.Duration {
	if s.RetryAfterMaxWait > 0 {
		return s.RetryAfterMaxWait
	}
	return aikidoDefaultRetryAfterMaxWait
}

// Discover fetches matching Aikido issue groups and returns them as WorkItems.
func (s *AikidoSource) Discover(ctx context.Context) ([]WorkItem, error) {
	baseURL, err := parseAikidoProxyBaseURL(s.proxyBaseURL())
	if err != nil {
		return nil, err
	}

	if s.useIssueExport() {
		return s.discoverIssueExport(ctx, baseURL)
	}

	return s.discoverOpenIssueGroups(ctx, baseURL)
}

// DiscoverEach fetches matching Aikido issue groups and emits each WorkItem as
// soon as that group's controller-owned snapshot is ready.
func (s *AikidoSource) DiscoverEach(ctx context.Context, emit func(WorkItem) error) (int, error) {
	baseURL, err := parseAikidoProxyBaseURL(s.proxyBaseURL())
	if err != nil {
		return 0, err
	}

	if s.useIssueExport() {
		return s.discoverIssueExportEach(ctx, baseURL, emit)
	}

	items, err := s.discoverOpenIssueGroups(ctx, baseURL)
	if err != nil {
		return 0, err
	}
	for i, item := range items {
		if err := emit(item); err != nil {
			return i, err
		}
	}
	return len(items), nil
}

func (s *AikidoSource) discoverOpenIssueGroups(ctx context.Context, baseURL *url.URL) ([]WorkItem, error) {
	if err := s.validateRepositories(ctx, baseURL); err != nil {
		return nil, err
	}

	statuses := s.resolvedStatuses()
	repositories := append([]string(nil), s.Repositories...)
	if len(repositories) == 0 {
		repositories = []string{""}
	}

	seen := map[string]struct{}{}
	var items []WorkItem
	for _, repo := range repositories {
		for _, status := range statuses {
			groups, err := s.fetchIssueGroups(ctx, baseURL, repo, status)
			if err != nil {
				return nil, err
			}
			for _, group := range groups {
				if !s.matchesSeverity(group) {
					continue
				}
				id := aikidoStringFromAny(firstAikidoValue(group, "id", "issue_group_id", "issueGroupId", "group_id"))
				if id == "" {
					return nil, fmt.Errorf("Aikido issue group response is missing issue group ID")
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}

				item, err := aikidoGroupToWorkItem(group, id)
				if err != nil {
					return nil, err
				}
				items = append(items, item)
			}
		}
	}

	return items, nil
}

func parseAikidoProxyBaseURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil {
		return nil, fmt.Errorf("parsing Aikido proxy URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("Aikido proxy URL must use http or https")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("Aikido proxy URL must include a host")
	}
	return u, nil
}

func (s *AikidoSource) resolvedStatuses() []string {
	if len(s.Statuses) == 0 {
		return []string{"open"}
	}
	return append([]string(nil), s.Statuses...)
}

func (s *AikidoSource) useIssueExport() bool {
	return s.UseIssueExport || strings.TrimSpace(s.Branch) != "" || len(s.IssueTypes) > 0
}

func (s *AikidoSource) resolvedBranch() string {
	branch := strings.TrimSpace(s.Branch)
	if branch == "" {
		return "main"
	}
	return branch
}

func (s *AikidoSource) resolvedIssueTypes() []string {
	return cleanUniqueStrings(s.IssueTypes)
}

func (s *AikidoSource) discoverIssueExport(ctx context.Context, baseURL *url.URL) ([]WorkItem, error) {
	var items []WorkItem
	_, err := s.discoverIssueExportEach(ctx, baseURL, func(item WorkItem) error {
		items = append(items, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *AikidoSource) discoverIssueExportEach(ctx context.Context, baseURL *url.URL, emit func(WorkItem) error) (int, error) {
	branch := s.resolvedBranch()
	repos, err := s.fetchBranchCodeRepositories(ctx, baseURL, branch)
	if err != nil {
		return 0, err
	}
	if len(repos) == 0 {
		return 0, nil
	}

	statuses := s.resolvedStatuses()
	issueTypes := s.resolvedIssueTypes()
	if len(issueTypes) == 0 {
		issueTypes = []string{""}
	}

	rowsByID := map[string]map[string]any{}
	groupRows := map[string][]map[string]any{}
	for _, repo := range repos {
		repoID := aikidoStringFromAny(firstAikidoValue(repo, "id", "code_repo_id", "codeRepoId"))
		if repoID == "" {
			return 0, fmt.Errorf("Aikido code repository response is missing repository ID")
		}
		for _, status := range statuses {
			for _, issueType := range issueTypes {
				rows, err := s.fetchIssueExportRows(ctx, baseURL, repoID, status, issueType)
				if err != nil {
					return 0, err
				}
				for _, row := range rows {
					if !s.matchesIssueExportRow(row, repoID, status, issueType) {
						continue
					}
					rowID := aikidoStringFromAny(firstAikidoValue(row, "id", "issue_id", "issueId"))
					if rowID == "" {
						return 0, fmt.Errorf("Aikido issue export row is missing issue ID")
					}
					if _, seen := rowsByID[rowID]; seen {
						continue
					}
					rowsByID[rowID] = row
					groupID := aikidoStringFromAny(firstAikidoValue(row, "group_id", "groupId", "issue_group_id", "issueGroupId"))
					if groupID == "" {
						return 0, fmt.Errorf("Aikido issue export row %s is missing issue group ID", rowID)
					}
					groupRows[groupID] = append(groupRows[groupID], row)
				}
			}
		}
	}

	groupIDs := make([]string, 0, len(groupRows))
	for groupID := range groupRows {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)

	emitted := 0
	for _, groupID := range groupIDs {
		group := map[string]any{}
		item, err := aikidoExportGroupToWorkItem(group, groupID, groupRows[groupID], branch)
		if err != nil {
			return emitted, err
		}
		if err := emit(item); err != nil {
			return emitted, err
		}
		emitted++
	}
	return emitted, nil
}

func (s *AikidoSource) fetchBranchCodeRepositories(ctx context.Context, baseURL *url.URL, branch string) ([]map[string]any, error) {
	names := cleanUniqueStrings(s.Repositories)
	seen := map[string]struct{}{}
	var all []map[string]any
	if len(names) > 0 {
		for _, name := range names {
			repos, err := s.fetchCodeRepositories(ctx, baseURL, branch, name)
			if err != nil {
				return nil, err
			}
			var matched bool
			for _, repo := range repos {
				if !aikidoCodeRepositoryMatches(repo, branch, name) {
					continue
				}
				matched = true
				id := aikidoStringFromAny(firstAikidoValue(repo, "id", "code_repo_id", "codeRepoId"))
				if id == "" {
					return nil, fmt.Errorf("Aikido code repository %q response is missing repository ID", name)
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				all = append(all, repo)
			}
			if !matched {
				return nil, fmt.Errorf("Aikido repository %q was not found as an exact active code repository match for branch %q", name, branch)
			}
		}
		return all, nil
	}

	repos, err := s.fetchCodeRepositories(ctx, baseURL, branch, "")
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		if !aikidoCodeRepositoryMatches(repo, branch, "") {
			continue
		}
		id := aikidoStringFromAny(firstAikidoValue(repo, "id", "code_repo_id", "codeRepoId"))
		if id == "" {
			return nil, fmt.Errorf("Aikido code repository response is missing repository ID")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		all = append(all, repo)
	}
	return all, nil
}

func (s *AikidoSource) fetchCodeRepositories(ctx context.Context, baseURL *url.URL, branch, name string) ([]map[string]any, error) {
	var all []map[string]any
	for page := 0; page < aikidoMaxCodeRepositoryPages; page++ {
		params := url.Values{}
		params.Set("per_page", strconv.Itoa(aikidoCodeRepositoriesPerPage))
		params.Set("page", strconv.Itoa(page))
		if strings.TrimSpace(branch) != "" {
			params.Set("filter_branch", branch)
		}
		if strings.TrimSpace(name) != "" {
			params.Set("filter_name", name)
		}
		repos, err := s.getAikidoObjects(ctx, baseURL, "/repositories/code", params)
		if err != nil {
			return nil, fmt.Errorf("fetching Aikido code repositories: %w", err)
		}
		all = append(all, repos...)
		if len(repos) < aikidoCodeRepositoriesPerPage {
			return all, nil
		}
	}
	return nil, fmt.Errorf("Aikido code repository page cap reached with a full page; narrow filters")
}

func aikidoCodeRepositoryMatches(repo map[string]any, branch, expectedName string) bool {
	name := aikidoStringFromAny(firstAikidoValue(repo, "name", "repository_name", "repositoryName", "full_name", "fullName"))
	if expectedName != "" && name != expectedName {
		return false
	}
	repoBranch := aikidoStringFromAny(firstAikidoValue(repo, "branch"))
	if branch != "" && repoBranch != "" && repoBranch != branch {
		return false
	}
	activeValue := firstAikidoValue(repo, "active", "is_active", "isActive")
	active, known := aikidoBoolFromAny(activeValue)
	return !known || active
}

func (s *AikidoSource) fetchIssueExportRows(ctx context.Context, baseURL *url.URL, repoID, status, issueType string) ([]map[string]any, error) {
	var all []map[string]any
	for page := 0; page < aikidoMaxIssueExportPages; page++ {
		params := url.Values{}
		params.Set("per_page", strconv.Itoa(aikidoIssueExportPerPage))
		params.Set("page", strconv.Itoa(page))
		params.Set("filter_code_repo_id", repoID)
		if strings.TrimSpace(status) != "" {
			params.Set("filter_status", status)
		}
		if strings.TrimSpace(issueType) != "" {
			params.Set("filter_issue_type", issueType)
		}
		rows, err := s.getAikidoObjects(ctx, baseURL, "/issues/export", params)
		if err != nil {
			return nil, fmt.Errorf("fetching Aikido issue export rows for code repo %s: %w", repoID, err)
		}
		all = append(all, rows...)
		if len(rows) < aikidoIssueExportPerPage {
			return all, nil
		}
	}
	return nil, fmt.Errorf("Aikido issue export page cap reached for code repo %s; narrow filters", repoID)
}

func (s *AikidoSource) matchesIssueExportRow(row map[string]any, repoID, status, issueType string) bool {
	if rowRepoID := aikidoStringFromAny(firstAikidoValue(row, "code_repo_id", "codeRepoId")); rowRepoID != "" && rowRepoID != repoID {
		return false
	}
	if status != "" {
		rowStatus := normalizeAikidoValue(aikidoStringFromAny(firstAikidoValue(row, "status", "state")))
		if rowStatus != "" && rowStatus != normalizeAikidoValue(status) {
			return false
		}
	}
	if issueType != "" {
		rowType := normalizeAikidoValue(aikidoIssueType(row))
		if rowType != "" && rowType != normalizeAikidoValue(issueType) {
			return false
		}
	}
	return s.matchesSeverity(row)
}

func (s *AikidoSource) fetchIssueGroup(ctx context.Context, baseURL *url.URL, groupID string) (map[string]any, error) {
	groups, err := s.getAikidoObjects(ctx, baseURL, "/issues/groups/"+url.PathEscape(groupID), nil)
	if err != nil {
		return nil, fmt.Errorf("fetching Aikido issue group %s: %w", groupID, err)
	}
	if len(groups) == 0 {
		return map[string]any{}, nil
	}
	return groups[0], nil
}

func (s *AikidoSource) validateRepositories(ctx context.Context, baseURL *url.URL) error {
	for _, repo := range s.Repositories {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return fmt.Errorf("Aikido repository filters must not be empty")
		}

		params := url.Values{}
		params.Set("filter_name", repo)
		params.Set("per_page", "20")
		groups, err := s.getAikidoObjects(ctx, baseURL, "/repositories/code", params)
		if err != nil {
			return fmt.Errorf("validating Aikido repository %q: %w", repo, err)
		}

		if !aikidoRepositoryExactActiveMatch(groups, repo) {
			return fmt.Errorf("Aikido repository %q was not found as an exact active code repository match", repo)
		}
	}
	return nil
}

func aikidoRepositoryExactActiveMatch(repos []map[string]any, expected string) bool {
	for _, repo := range repos {
		name := aikidoStringFromAny(firstAikidoValue(repo, "name", "repository_name", "repositoryName", "full_name", "fullName"))
		if name != expected {
			continue
		}
		activeValue := firstAikidoValue(repo, "active", "is_active", "isActive")
		active, known := aikidoBoolFromAny(activeValue)
		if !known || active {
			return true
		}
	}
	return false
}

func (s *AikidoSource) fetchIssueGroups(ctx context.Context, baseURL *url.URL, repository, status string) ([]map[string]any, error) {
	var all []map[string]any
	for page := 0; page < aikidoMaxOpenIssueGroupPages; page++ {
		params := url.Values{}
		params.Set("per_page", strconv.Itoa(aikidoOpenIssueGroupsPerPage))
		params.Set("page", strconv.Itoa(page))
		if strings.TrimSpace(repository) != "" {
			params.Set("filter_code_repo_name", repository)
		}
		if strings.TrimSpace(status) != "" {
			params.Set("filter_status", status)
		}

		groups, err := s.getAikidoObjects(ctx, baseURL, "/open-issue-groups", params)
		if err != nil {
			return nil, fmt.Errorf("fetching Aikido issue groups: %w", err)
		}
		all = append(all, groups...)
		if len(groups) < aikidoOpenIssueGroupsPerPage {
			return all, nil
		}
	}
	return nil, fmt.Errorf("Aikido issue group page cap reached with a full page; narrow filters")
}

func (s *AikidoSource) getAikidoObjects(ctx context.Context, baseURL *url.URL, path string, params url.Values) ([]map[string]any, error) {
	u := *baseURL
	u.Path = strings.TrimRight(baseURL.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawQuery = params.Encode()

	var waited time.Duration
	for attempt := 0; attempt <= aikidoMaxRateLimitRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("creating Aikido request: %w", err)
		}
		s.setAikidoRequestHeaders(req.Header)

		resp, err := s.httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("calling Aikido proxy: %w", err)
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			statusErr := newAikidoProxyStatusError(resp)
			resp.Body.Close()
			if statusErr.StatusCode == http.StatusTooManyRequests && statusErr.HasRetryAfter {
				if waited+statusErr.RetryAfter <= s.retryAfterMaxWait() {
					if err := waitAikidoRetryAfter(ctx, statusErr.RetryAfter); err != nil {
						return nil, fmt.Errorf("waiting for Aikido Retry-After: %w", err)
					}
					waited += statusErr.RetryAfter
					continue
				}
			}
			return nil, statusErr
		}

		var payload any
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding Aikido response: %w", err)
		}

		objects, err := aikidoObjectsFromPayload(payload)
		if err != nil {
			return nil, err
		}
		return objects, nil
	}
	return nil, fmt.Errorf("Aikido proxy returned repeated rate limits after %d retries", aikidoMaxRateLimitRetries)
}

func (s *AikidoSource) setAikidoRequestHeaders(headers http.Header) {
	headers.Set("Accept", "application/json")
	headers.Set(aikidoHeaderClient, aikidoClientDiscovery)
	headers.Set(aikidoHeaderBudgetSeconds, strconv.Itoa(int(aikidoProxyRequestBudget/time.Second)))
	if name := strings.TrimSpace(s.TaskSpawnerName); name != "" {
		headers.Set(aikidoHeaderTaskSpawner, name)
	}
	headers.Set(aikidoHeaderRunDate, time.Now().UTC().Format("2006-01-02"))
}

type aikidoProxyStatusError struct {
	StatusCode    int
	Body          string
	RetryAfter    time.Duration
	HasRetryAfter bool
}

func (e *aikidoProxyStatusError) Error() string {
	return fmt.Sprintf("Aikido proxy returned status %d: %s", e.StatusCode, e.Body)
}

func newAikidoProxyStatusError(resp *http.Response) *aikidoProxyStatusError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	retryAfter, hasRetryAfter := parseAikidoRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	return &aikidoProxyStatusError{
		StatusCode:    resp.StatusCode,
		Body:          string(body),
		RetryAfter:    retryAfter,
		HasRetryAfter: hasRetryAfter,
	}
}

func parseAikidoRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	if !when.After(now) {
		return 0, true
	}
	return when.Sub(now), true
}

func waitAikidoRetryAfter(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func aikidoObjectsFromPayload(payload any) ([]map[string]any, error) {
	switch v := payload.(type) {
	case []any:
		return aikidoObjectSlice(v), nil
	case map[string]any:
		for _, key := range []string{"data", "items", "results", "repositories", "issue_groups", "issueGroups"} {
			if raw, ok := v[key]; ok {
				if arr, ok := raw.([]any); ok {
					return aikidoObjectSlice(arr), nil
				}
			}
		}
		return []map[string]any{v}, nil
	default:
		return nil, fmt.Errorf("Aikido response did not contain an object list")
	}
}

func aikidoObjectSlice(items []any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func (s *AikidoSource) matchesSeverity(group map[string]any) bool {
	if len(s.Severities) == 0 {
		return true
	}
	severity := normalizeAikidoValue(aikidoSeverity(group))
	if severity == "" {
		return false
	}
	for _, allowed := range s.Severities {
		if severity == normalizeAikidoValue(allowed) {
			return true
		}
	}
	return false
}

func aikidoGroupToWorkItem(group map[string]any, id string) (WorkItem, error) {
	title := aikidoStringFromAny(firstAikidoValue(group, "title", "name", "summary"))
	if title == "" {
		title = fmt.Sprintf("Aikido issue group %s", id)
	}
	severity := valueOrUnknown(aikidoSeverity(group))
	status := valueOrUnknown(aikidoStringFromAny(firstAikidoValue(group, "status", "state")))
	issueType := valueOrUnknown(aikidoStringFromAny(firstAikidoValue(group, "issue_type", "issueType", "type", "category")))
	repos := aikidoRepositoryNames(group)
	reposValue := strings.Join(repos, ",")
	groupURL := aikidoStringFromAny(firstAikidoValue(group, "url", "app_url", "appUrl", "html_url", "htmlUrl", "link"))
	body := aikidoWorkItemBody(group, id, title, severity, status, issueType, repos, groupURL)

	number := 0
	if n, err := strconv.Atoi(id); err == nil {
		number = n
	}

	labels := []string{"aikido", "severity:" + severity, "status:" + status, "type:" + issueType}
	for _, repo := range repos {
		labels = append(labels, "repo:"+repo)
	}

	return WorkItem{
		ID:     aikidoWorkItemID(id),
		Number: number,
		Title:  title,
		URL:    groupURL,
		Labels: labels,
		Body:   body,
		Kind:   "AikidoIssueGroup",
		Metadata: map[string]string{
			AikidoMetadataIssueGroupID: id,
			AikidoMetadataSeverity:     severity,
			AikidoMetadataStatus:       status,
			AikidoMetadataIssueType:    issueType,
			AikidoMetadataRepositories: reposValue,
			AikidoMetadataURL:          groupURL,
		},
	}, nil
}

func aikidoExportGroupToWorkItem(group map[string]any, id string, rows []map[string]any, branch string) (WorkItem, error) {
	title := aikidoStringFromAny(firstAikidoValue(group, "title", "name", "summary"))
	if title == "" && len(rows) > 0 {
		title = firstAikidoIssueRowString(rows, "title", "name", "summary", "rule")
	}
	if title == "" {
		title = fmt.Sprintf("Aikido issue group %s", id)
	}
	severity := valueOrUnknown(aikidoSeverity(group))
	if len(rows) > 0 {
		severity = highestAikidoIssueRowSeverity(severity, rows)
	}
	status := valueOrUnknown(aikidoStringFromAny(firstAikidoValue(group, "group_status", "groupStatus", "status", "state")))
	if status == "unknown" && len(rows) > 0 {
		status = valueOrUnknown(aikidoStringFromAny(firstAikidoValue(rows[0], "status", "state")))
	}
	issueType := valueOrUnknown(aikidoIssueType(group))
	if issueType == "unknown" && len(rows) > 0 {
		issueType = valueOrUnknown(aikidoIssueType(rows[0]))
	}
	repos := aikidoIssueRowValues(rows, "code_repo_name", "codeRepoName")
	if len(repos) == 0 {
		repos = aikidoRepositoryNames(group)
	}
	packages := aikidoIssueRowValues(rows, "affected_package", "affectedPackage", "package", "package_name", "packageName")
	cves := aikidoIssueRowValues(rows, "cve_id", "cveId", "cve", "vulnerability_id", "vulnerabilityId")
	groupURL := aikidoStringFromAny(firstAikidoValue(group, "url", "app_url", "appUrl", "html_url", "htmlUrl", "link"))
	if groupURL == "" && len(rows) > 0 {
		groupURL = firstAikidoIssueRowString(rows, "url", "app_url", "appUrl", "html_url", "htmlUrl", "link")
	}
	body := aikidoIssueExportWorkItemBody(group, id, title, severity, status, issueType, branch, repos, packages, cves, groupURL, rows)

	number := 0
	if n, err := strconv.Atoi(id); err == nil {
		number = n
	}

	labels := []string{"aikido", "severity:" + severity, "status:" + status, "type:" + issueType, "branch:" + branch}
	for _, repo := range repos {
		labels = append(labels, "repo:"+repo)
	}

	reposValue := strings.Join(repos, ",")
	return WorkItem{
		ID:     aikidoWorkItemID(id),
		Number: number,
		Title:  title,
		URL:    groupURL,
		Labels: labels,
		Body:   body,
		Kind:   "AikidoIssueGroup",
		Branch: branch,
		Metadata: map[string]string{
			AikidoMetadataIssueGroupID:     id,
			AikidoMetadataBranch:           branch,
			AikidoMetadataSeverity:         severity,
			AikidoMetadataStatus:           status,
			AikidoMetadataIssueType:        issueType,
			AikidoMetadataRepositories:     reposValue,
			AikidoMetadataCodeRepositories: reposValue,
			AikidoMetadataAffectedPackages: strings.Join(packages, ","),
			AikidoMetadataCVEIDs:           strings.Join(cves, ","),
			AikidoMetadataURL:              groupURL,
		},
	}, nil
}

func firstAikidoIssueRowString(rows []map[string]any, keys ...string) string {
	for _, row := range rows {
		if value := aikidoStringFromAny(firstAikidoValue(row, keys...)); value != "" {
			return value
		}
	}
	return ""
}

func highestAikidoIssueRowSeverity(current string, rows []map[string]any) string {
	best := valueOrUnknown(current)
	for _, row := range rows {
		severity := valueOrUnknown(aikidoSeverity(row))
		if aikidoSeverityRank(severity) > aikidoSeverityRank(best) {
			best = severity
		}
	}
	return best
}

func aikidoSeverityRank(severity string) int {
	switch normalizeAikidoValue(severity) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func aikidoWorkItemID(id string) string {
	const prefix = "aikido-group-"
	safeID := aikidoInvalidIDPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(id)), "-")
	safeID = strings.Trim(safeID, "-")
	if safeID == "" {
		safeID = "unknown"
	}
	if len(safeID) <= 14 {
		return prefix + safeID
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(id))
	head := strings.Trim(safeID[:8], "-")
	if head == "" {
		head = "group"
	}
	return fmt.Sprintf("%s%s-%08x", prefix, head, hash.Sum32())
}

func aikidoWorkItemBody(group map[string]any, id, title, severity, status, issueType string, repos []string, groupURL string) string {
	leakedSecret := normalizeAikidoValue(issueType) == "leaked_secret" || strings.Contains(strings.ToLower(title), "secret")
	description := aikidoStringFromAny(firstAikidoValue(group, "description", "details", "message"))
	if leakedSecret {
		description = redactPotentialSecret(description)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Aikido issue group ID: %s\n", id)
	fmt.Fprintf(&b, "Title: %s\n", sanitizeAikidoText(title, leakedSecret))
	fmt.Fprintf(&b, "Severity: %s\n", severity)
	fmt.Fprintf(&b, "Status: %s\n", status)
	fmt.Fprintf(&b, "Issue type: %s\n", issueType)
	if len(repos) > 0 {
		fmt.Fprintf(&b, "Code repositories: %s\n", strings.Join(repos, ", "))
	}
	if groupURL != "" {
		fmt.Fprintf(&b, "Aikido URL: %s\n", groupURL)
	}
	if description != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", description)
	}

	hints := aikidoHints(group, leakedSecret)
	if len(hints) > 0 {
		b.WriteString("\nAvailable remediation/context hints:\n")
		for _, hint := range hints {
			fmt.Fprintf(&b, "- %s\n", hint)
		}
	}

	b.WriteString("\nUse only the controller-provided Aikido snapshot. Do not call Aikido from the agent session; the next controller run will verify whether the issue still exists.\n")

	body := b.String()
	if len(body) > aikidoMaxBodyBytes {
		body = body[:aikidoMaxBodyBytes] + "\n[truncated]\n"
	}
	return body
}

func aikidoIssueExportWorkItemBody(group map[string]any, id, title, severity, status, issueType, branch string, repos, packages, cves []string, groupURL string, rows []map[string]any) string {
	leakedSecret := normalizeAikidoValue(issueType) == "leaked_secret" || strings.Contains(strings.ToLower(title), "secret")
	description := aikidoStringFromAny(firstAikidoValue(group, "description", "details", "message"))
	howToFix := aikidoStringFromAny(firstAikidoValue(group, "how_to_fix", "howToFix", "fix", "recommendation"))
	if leakedSecret {
		description = redactPotentialSecret(description)
		howToFix = redactPotentialSecret(howToFix)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Aikido issue group\n\n")
	fmt.Fprintf(&b, "Aikido issue group ID: %s\n", id)
	fmt.Fprintf(&b, "Branch: %s\n", branch)
	fmt.Fprintf(&b, "Title: %s\n", sanitizeAikidoText(title, leakedSecret))
	fmt.Fprintf(&b, "Severity: %s\n", severity)
	fmt.Fprintf(&b, "Status: %s\n", status)
	fmt.Fprintf(&b, "Issue type: %s\n", issueType)
	if len(repos) > 0 {
		fmt.Fprintf(&b, "Code repositories: %s\n", strings.Join(repos, ", "))
	}
	if len(packages) > 0 {
		fmt.Fprintf(&b, "Affected packages: %s\n", strings.Join(packages, ", "))
	}
	if len(cves) > 0 {
		fmt.Fprintf(&b, "CVE IDs: %s\n", strings.Join(cves, ", "))
	}
	if groupURL != "" {
		fmt.Fprintf(&b, "Aikido URL: %s\n", groupURL)
	}
	if description != "" {
		fmt.Fprintf(&b, "\n## Group summary\n\n%s\n", sanitizeAikidoText(description, leakedSecret))
	}
	if howToFix != "" {
		fmt.Fprintf(&b, "\n## Aikido remediation guidance\n\n%s\n", sanitizeAikidoText(howToFix, leakedSecret))
	}

	b.WriteString("\n## Scoped issue rows\n\n")
	b.WriteString("These rows are scoped to active Aikido code repositories on the branch above.\n")
	for _, row := range rows {
		fmt.Fprintf(&b, "- %s\n", aikidoIssueRowSummary(row, leakedSecret))
	}

	b.WriteString("\n## Constraints\n\n")
	b.WriteString("- Work only against latest main unless explicitly instructed otherwise.\n")
	b.WriteString("- Search for an existing open remediation PR before creating a new one.\n")
	b.WriteString("- Do not merge PRs.\n")
	b.WriteString("- If remediation requires shared package or image rebuilds, continue across future turns.\n")
	b.WriteString("- Do not call Aikido from the agent session; the next controller run will verify whether the issue still exists.\n")

	body := b.String()
	if len(body) > aikidoMaxBodyBytes {
		body = body[:aikidoMaxBodyBytes] + "\n[truncated]\n"
	}
	return body
}

func aikidoIssueRowSummary(row map[string]any, leakedSecret bool) string {
	parts := []string{
		"issue_id=" + aikidoStringFromAny(firstAikidoValue(row, "id", "issue_id", "issueId")),
		"repo=" + aikidoStringFromAny(firstAikidoValue(row, "code_repo_name", "codeRepoName")),
		"type=" + aikidoIssueType(row),
		"severity=" + aikidoSeverity(row),
	}
	for _, field := range []struct {
		label string
		keys  []string
	}{
		{label: "package", keys: []string{"affected_package", "affectedPackage", "package", "package_name", "packageName"}},
		{label: "installed", keys: []string{"installed_version", "installedVersion", "version", "current_version", "currentVersion"}},
		{label: "patched", keys: []string{"patched_versions", "patchedVersions", "fixed_version", "fixedVersion"}},
		{label: "cve", keys: []string{"cve_id", "cveId", "cve", "vulnerability_id", "vulnerabilityId"}},
		{label: "file", keys: []string{"affected_file", "affectedFile", "file", "file_path", "filePath", "path"}},
		{label: "rule", keys: []string{"rule", "rule_id", "ruleId"}},
		{label: "language", keys: []string{"programming_language", "programmingLanguage", "language"}},
		{label: "exploitability", keys: []string{"exploitability"}},
		{label: "remediate_by", keys: []string{"sla_remediate_by", "slaRemediateBy"}},
	} {
		value := sanitizeAikidoText(aikidoStringFromAny(firstAikidoValue(row, field.keys...)), leakedSecret)
		if value != "" {
			parts = append(parts, field.label+"="+value)
		}
	}
	start := aikidoStringFromAny(firstAikidoValue(row, "start_line", "startLine"))
	end := aikidoStringFromAny(firstAikidoValue(row, "end_line", "endLine"))
	if start != "" || end != "" {
		parts = append(parts, "lines="+strings.Trim(start+"-"+end, "-"))
	}
	return strings.Join(nonEmptyStrings(parts), ", ")
}

func aikidoHints(group map[string]any, leakedSecret bool) []string {
	var hints []string
	fields := []struct {
		label string
		keys  []string
	}{
		{label: "Package", keys: []string{"package", "package_name", "packageName", "dependency"}},
		{label: "Version", keys: []string{"version", "current_version", "currentVersion"}},
		{label: "Fixed version", keys: []string{"fixed_version", "fixedVersion", "fix_version", "fixVersion"}},
		{label: "CVE", keys: []string{"cve", "cves", "vulnerability_id", "vulnerabilityId"}},
		{label: "File", keys: []string{"file", "file_path", "filePath", "path"}},
		{label: "Line", keys: []string{"line", "line_number", "lineNumber"}},
		{label: "Fix", keys: []string{"fix", "fix_suggestion", "fixSuggestion", "recommendation"}},
	}
	for _, field := range fields {
		value := aikidoStringFromAny(firstAikidoValue(group, field.keys...))
		value = sanitizeAikidoText(value, leakedSecret)
		if value != "" {
			hints = append(hints, field.label+": "+value)
		}
	}
	return hints
}

func aikidoSeverity(group map[string]any) string {
	raw := firstAikidoValue(group, "severity", "severity_level", "severityLevel", "severity_label", "severityLabel")
	if obj, ok := raw.(map[string]any); ok {
		raw = firstAikidoValue(obj, "name", "label", "level", "value")
	}
	return aikidoStringFromAny(raw)
}

func aikidoIssueType(obj map[string]any) string {
	return aikidoStringFromAny(firstAikidoValue(obj, "issue_type", "issueType", "type", "category"))
}

func aikidoRepositoryNames(group map[string]any) []string {
	names := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			names[value] = struct{}{}
		}
	}

	add(aikidoStringFromAny(firstAikidoValue(group, "code_repository_name", "codeRepositoryName", "repository_name", "repositoryName", "repo", "repo_name", "repoName")))

	for _, key := range []string{"repositories", "code_repositories", "codeRepositories"} {
		if raw, ok := group[key]; ok {
			for _, name := range aikidoNamesFromAny(raw) {
				add(name)
			}
		}
	}

	if raw, ok := group["locations"]; ok {
		if locations, ok := raw.([]any); ok {
			for _, location := range locations {
				obj, ok := location.(map[string]any)
				if !ok {
					continue
				}
				add(aikidoStringFromAny(firstAikidoValue(obj, "code_repository_name", "codeRepositoryName", "repository_name", "repositoryName", "repo", "repo_name", "repoName")))
				for _, key := range []string{"code_repository", "codeRepository", "repository"} {
					if nested, ok := obj[key].(map[string]any); ok {
						add(aikidoStringFromAny(firstAikidoValue(nested, "name", "repository_name", "repositoryName", "full_name", "fullName")))
					}
				}
			}
		}
	}

	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func aikidoIssueRowValues(rows []map[string]any, keys ...string) []string {
	values := map[string]struct{}{}
	for _, row := range rows {
		for _, value := range aikidoNamesFromAny(firstAikidoValue(row, keys...)) {
			value = strings.TrimSpace(value)
			if value != "" {
				values[value] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func aikidoNamesFromAny(raw any) []string {
	switch v := raw.(type) {
	case []any:
		var names []string
		for _, item := range v {
			if name := aikidoStringFromAny(item); name != "" {
				names = append(names, name)
				continue
			}
			if obj, ok := item.(map[string]any); ok {
				name := aikidoStringFromAny(firstAikidoValue(obj, "name", "repository_name", "repositoryName", "full_name", "fullName"))
				if name != "" {
					names = append(names, name)
				}
			}
		}
		return names
	default:
		if name := aikidoStringFromAny(raw); name != "" {
			return []string{name}
		}
		return nil
	}
}

func cleanUniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func firstAikidoValue(obj map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := obj[key]; ok {
			return value
		}
	}
	return nil
}

func aikidoStringFromAny(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		if math.Trunc(v) == v {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		f := float64(v)
		if math.Trunc(f) == f {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := aikidoStringFromAny(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func aikidoBoolFromAny(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return false, false
	}
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func normalizeAikidoValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sanitizeAikidoText(value string, leakedSecret bool) string {
	if !leakedSecret {
		return value
	}
	return redactPotentialSecret(value)
}

var (
	aikidoInvalidIDPattern = regexp.MustCompile(`[^a-z0-9-]+`)
	likelySecretPattern    = regexp.MustCompile(`(?i)(secret|token|key|password|credential)(\s*[:=]\s*)([^\s,;]+)|\b(AKIA[0-9A-Z]{12,}|gh[pousr]_[A-Za-z0-9_]{20,}|[A-Za-z0-9+/]{32,}={0,2})\b`)
)

func redactPotentialSecret(value string) string {
	return likelySecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		if strings.Contains(match, ":") || strings.Contains(match, "=") {
			idx := strings.IndexAny(match, ":=")
			return match[:idx+1] + " [redacted]"
		}
		return "[redacted]"
	})
}
