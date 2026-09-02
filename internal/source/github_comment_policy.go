package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var githubPermissionRanks = map[string]int{
	"read":     1,
	"triage":   2,
	"write":    3,
	"maintain": 4,
	"admin":    5,
}

type githubCommentPolicy struct {
	TriggerComment    string
	ExcludeComments   []string
	AllowedUsers      []string
	AllowedTeams      []string
	MinimumPermission string
}

func (p githubCommentPolicy) commands() commentCommands {
	return commentCommands{Trigger: p.TriggerComment, Excludes: p.ExcludeComments}
}

type githubTeamRef struct {
	Org  string
	Slug string
}

type githubAuthorizationDecision struct {
	authorized bool
}

type githubCommentAuthorizer struct {
	owner             string
	repo              string
	baseURL           string
	token             string
	client            *http.Client
	allowedUsers      map[string]struct{}
	allowedTeams      []githubTeamRef
	minimumPermission string
	cache             map[string]githubAuthorizationDecision
}

type githubMembershipResponse struct {
	State string `json:"state"`
}

type githubPermissionResponse struct {
	Permission string `json:"permission"`
	RoleName   string `json:"role_name"`
}

func newGitHubCommentAuthorizer(owner, repo, baseURL, token string, client *http.Client, policy githubCommentPolicy) (*githubCommentAuthorizer, error) {
	allowedUsers := make(map[string]struct{}, len(policy.AllowedUsers))
	for _, login := range policy.AllowedUsers {
		login = normalizeGitHubLogin(login)
		if login == "" {
			continue
		}
		allowedUsers[login] = struct{}{}
	}

	allowedTeams := make([]githubTeamRef, 0, len(policy.AllowedTeams))
	for _, team := range policy.AllowedTeams {
		ref, err := parseGitHubTeamRef(team)
		if err != nil {
			return nil, err
		}
		allowedTeams = append(allowedTeams, ref)
	}

	minimumPermission := normalizeGitHubPermission(policy.MinimumPermission)
	if policy.MinimumPermission != "" && minimumPermission == "" {
		return nil, fmt.Errorf("invalid minimum permission %q", policy.MinimumPermission)
	}

	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	if client == nil {
		client = http.DefaultClient
	}

	return &githubCommentAuthorizer{
		owner:             owner,
		repo:              repo,
		baseURL:           strings.TrimRight(baseURL, "/"),
		token:             token,
		client:            client,
		allowedUsers:      allowedUsers,
		allowedTeams:      allowedTeams,
		minimumPermission: minimumPermission,
		cache:             make(map[string]githubAuthorizationDecision),
	}, nil
}

func (a *githubCommentAuthorizer) authorizationConfigured() bool {
	return len(a.allowedUsers) > 0 || len(a.allowedTeams) > 0 || a.minimumPermission != ""
}

func (a *githubCommentAuthorizer) isAuthorized(ctx context.Context, actor githubUser) (bool, error) {
	return a.isAuthorizedLogin(ctx, actor.Login)
}

func (a *githubCommentAuthorizer) isAuthorizedLogin(ctx context.Context, login string) (bool, error) {
	if a == nil || !a.authorizationConfigured() {
		return true, nil
	}

	login = normalizeGitHubLogin(login)
	if login == "" {
		return false, nil
	}

	if _, ok := a.allowedUsers[login]; ok {
		return true, nil
	}

	if decision, ok := a.cache[login]; ok {
		return decision.authorized, nil
	}

	authorized, err := a.authorizeLogin(ctx, login)
	if err == nil {
		a.cache[login] = githubAuthorizationDecision{authorized: authorized}
	}
	return authorized, err
}

func (a *githubCommentAuthorizer) authorizeLogin(ctx context.Context, login string) (bool, error) {
	var firstErr error

	if a.minimumPermission != "" {
		ok, err := a.hasMinimumPermission(ctx, login)
		if err != nil {
			firstErr = err
		} else if ok {
			return true, nil
		}
	}

	for _, team := range a.allowedTeams {
		ok, err := a.isTeamMember(ctx, team, login)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			return true, nil
		}
	}

	if firstErr != nil {
		return false, firstErr
	}

	return false, nil
}

func (a *githubCommentAuthorizer) hasMinimumPermission(ctx context.Context, login string) (bool, error) {
	var permission githubPermissionResponse
	statusCode, err := a.getJSON(
		ctx,
		fmt.Sprintf(
			"/repos/%s/%s/collaborators/%s/permission",
			url.PathEscape(a.owner),
			url.PathEscape(a.repo),
			url.PathEscape(login),
		),
		&permission,
	)
	if err != nil {
		return false, fmt.Errorf("checking repository permission for %q: %w", login, err)
	}
	if statusCode == http.StatusNotFound {
		return false, nil
	}

	actualPermission := normalizeGitHubPermission(permission.RoleName)
	if actualPermission == "" {
		actualPermission = normalizeGitHubPermission(permission.Permission)
	}

	return githubPermissionRanks[actualPermission] >= githubPermissionRanks[a.minimumPermission], nil
}

func (a *githubCommentAuthorizer) isTeamMember(ctx context.Context, team githubTeamRef, login string) (bool, error) {
	var membership githubMembershipResponse
	statusCode, err := a.getJSON(
		ctx,
		fmt.Sprintf(
			"/orgs/%s/teams/%s/memberships/%s",
			url.PathEscape(team.Org),
			url.PathEscape(team.Slug),
			url.PathEscape(login),
		),
		&membership,
	)
	if err != nil {
		return false, fmt.Errorf("checking team membership for %q in %s/%s: %w", login, team.Org, team.Slug, err)
	}
	if statusCode == http.StatusNotFound {
		return false, nil
	}

	return strings.EqualFold(strings.TrimSpace(membership.State), "active"), nil
}

func (a *githubCommentAuthorizer) getJSON(ctx context.Context, path string, out interface{}) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}

	if a.token != "" {
		req.Header.Set("Authorization", "token "+a.token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}
	if out == nil {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decoding response: %w", err)
	}

	return resp.StatusCode, nil
}

func evaluateGitHubCommentPolicy(ctx context.Context, body string, bodyActor githubUser, comments []githubComment, policy githubCommentPolicy, authorizer *githubCommentAuthorizer) (bool, time.Time, error) {
	cmds := policy.commands()
	if !cmds.enabled() {
		return true, time.Time{}, nil
	}

	bodyHasTrigger := cmds.Trigger != "" && containsCommand(body, cmds.Trigger)
	bodyHasExclude := len(cmds.Excludes) > 0 && containsAnyCommand(body, cmds.Excludes)
	var bodyMatches bodyMatch
	if bodyHasTrigger || bodyHasExclude {
		authorized, err := authorizer.isAuthorized(ctx, bodyActor)
		if err != nil {
			return false, time.Time{}, err
		}
		if authorized {
			bodyMatches = bodyMatch{trigger: bodyHasTrigger, exclude: bodyHasExclude}
		}
	}

	triggerMatch, excludeMatch := newCommentMatch(), newCommentMatch()
	var err error
	if cmds.Trigger != "" {
		triggerMatch, err = latestAuthorizedCommentMatch(ctx, comments, []string{cmds.Trigger}, authorizer)
		if err != nil {
			return false, time.Time{}, err
		}
	}
	if len(cmds.Excludes) > 0 {
		excludeMatch, err = latestAuthorizedCommentMatch(ctx, comments, cmds.Excludes, authorizer)
		if err != nil {
			return false, time.Time{}, err
		}
	}

	allowed, triggerTime := decideCommentPolicy(cmds, bodyMatches, triggerMatch, excludeMatch)
	return allowed, triggerTime, nil
}

func latestAuthorizedCommentMatch(ctx context.Context, comments []githubComment, commands []string, authorizer *githubCommentAuthorizer) (commentMatch, error) {
	match := newCommentMatch()

	for i, comment := range comments {
		if !containsAnyCommand(comment.Body, commands) {
			continue
		}

		authorized, err := authorizer.isAuthorized(ctx, comment.User)
		if err != nil {
			return commentMatch{}, err
		}
		if !authorized {
			continue
		}

		match.record(i, comment.CreatedAt)
	}

	return match, nil
}

func normalizeGitHubLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

func normalizeGitHubPermission(permission string) string {
	permission = strings.ToLower(strings.TrimSpace(permission))
	if _, ok := githubPermissionRanks[permission]; !ok {
		return ""
	}
	return permission
}

func parseGitHubTeamRef(team string) (githubTeamRef, error) {
	org, slug, found := strings.Cut(strings.TrimSpace(team), "/")
	if !found || strings.TrimSpace(org) == "" || strings.TrimSpace(slug) == "" {
		return githubTeamRef{}, fmt.Errorf("invalid team %q: expected org/team-slug", team)
	}
	return githubTeamRef{Org: strings.TrimSpace(org), Slug: strings.TrimSpace(slug)}, nil
}
