package source

import (
	"strings"
	"time"
)

// gitlabCommentPolicy gates discovery on slash commands found in the item
// description or its notes. Trigger and exclude commands are matched at the
// start of a line; when both are configured the most recent authorized command
// wins.
type gitlabCommentPolicy struct {
	TriggerComment  string
	ExcludeComments []string
	AllowedUsers    []string
}

func (p gitlabCommentPolicy) enabled() bool {
	return p.TriggerComment != "" || len(p.ExcludeComments) > 0
}

// gitlabCommentAuthorizer decides whether a GitLab user may issue commands.
// Authorization is a username allow-list; an empty list authorizes everyone.
// Access-level checks against /projects/:id/members/all would slot in here.
type gitlabCommentAuthorizer struct {
	allowedUsers map[string]struct{}
}

func newGitLabCommentAuthorizer(policy gitlabCommentPolicy) *gitlabCommentAuthorizer {
	a := &gitlabCommentAuthorizer{allowedUsers: make(map[string]struct{}, len(policy.AllowedUsers))}
	for _, user := range policy.AllowedUsers {
		if normalized := normalizeGitLabUsername(user); normalized != "" {
			a.allowedUsers[normalized] = struct{}{}
		}
	}
	return a
}

func (a *gitlabCommentAuthorizer) isAuthorized(username string) bool {
	if len(a.allowedUsers) == 0 {
		return true
	}
	_, ok := a.allowedUsers[normalizeGitLabUsername(username)]
	return ok
}

type gitlabCommentMatch struct {
	found   bool
	hasTime bool
	time    time.Time
	index   int
}

// evaluateGitLabCommentPolicy reports whether the item passes the policy and,
// when a trigger note matched, the note's creation time so re-triggers on a
// finished item can be detected. A trigger in the description counts but
// carries no time. System notes never match.
func evaluateGitLabCommentPolicy(description, author string, notes []gitlabNote, policy gitlabCommentPolicy, authorizer *gitlabCommentAuthorizer) (bool, time.Time) {
	if !policy.enabled() {
		return true, time.Time{}
	}

	authorAuthorized := authorizer.isAuthorized(author)
	bodyMatchesTrigger := authorAuthorized && policy.TriggerComment != "" && containsCommand(description, policy.TriggerComment)
	bodyMatchesExclude := authorAuthorized && len(policy.ExcludeComments) > 0 && containsAnyCommand(description, policy.ExcludeComments)

	var triggerMatch, excludeMatch gitlabCommentMatch
	if policy.TriggerComment != "" {
		triggerMatch = latestAuthorizedGitLabNote(notes, []string{policy.TriggerComment}, authorizer)
	}
	if len(policy.ExcludeComments) > 0 {
		excludeMatch = latestAuthorizedGitLabNote(notes, policy.ExcludeComments, authorizer)
	}

	switch {
	case len(policy.ExcludeComments) == 0:
		if triggerMatch.found {
			return true, triggerMatch.time
		}
		return bodyMatchesTrigger, time.Time{}
	case policy.TriggerComment == "":
		return !excludeMatch.found && !bodyMatchesExclude, time.Time{}
	}

	switch compareGitLabCommentMatches(triggerMatch, excludeMatch) {
	case 1:
		return true, triggerMatch.time
	case -1:
		return false, time.Time{}
	}
	if bodyMatchesExclude {
		return false, time.Time{}
	}
	return bodyMatchesTrigger, time.Time{}
}

func latestAuthorizedGitLabNote(notes []gitlabNote, commands []string, authorizer *gitlabCommentAuthorizer) gitlabCommentMatch {
	match := gitlabCommentMatch{index: -1}
	for i, note := range notes {
		if note.System || !containsAnyCommand(note.Body, commands) || !authorizer.isAuthorized(note.Author.Username) {
			continue
		}
		match.found = true
		createdAt, err := time.Parse(time.RFC3339, note.CreatedAt)
		if err != nil {
			if !match.hasTime {
				match.index = i
			}
			continue
		}
		if !match.hasTime || createdAt.After(match.time) || (createdAt.Equal(match.time) && i > match.index) {
			match.hasTime = true
			match.time = createdAt
			match.index = i
		}
	}
	return match
}

// compareGitLabCommentMatches orders two matches by recency: creation time
// when both carry one, otherwise position in the notes list.
func compareGitLabCommentMatches(left, right gitlabCommentMatch) int {
	switch {
	case left.found && right.found:
		if left.hasTime && right.hasTime {
			switch {
			case left.time.After(right.time):
				return 1
			case right.time.After(left.time):
				return -1
			}
		}
		switch {
		case left.index > right.index:
			return 1
		case right.index > left.index:
			return -1
		}
		return 0
	case left.found:
		return 1
	case right.found:
		return -1
	}
	return 0
}

func normalizeGitLabUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
}

// gitlabConversationNotes drops system notes and inline diff notes so
// {{.Comments}} carries only the discussion thread; diff notes are exposed
// separately through {{.ReviewComments}}.
func gitlabConversationNotes(notes []gitlabNote) []gitlabNote {
	conversation := make([]gitlabNote, 0, len(notes))
	for _, n := range notes {
		if n.System || n.Type == "DiffNote" {
			continue
		}
		conversation = append(conversation, n)
	}
	return conversation
}

func gitlabNoteBodies(notes []gitlabNote) []string {
	bodies := make([]string, 0, len(notes))
	for _, n := range notes {
		bodies = append(bodies, n.Body)
	}
	return bodies
}
