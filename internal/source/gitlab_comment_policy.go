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

func (p gitlabCommentPolicy) commands() commentCommands {
	return commentCommands{Trigger: p.TriggerComment, Excludes: p.ExcludeComments}
}

func (p gitlabCommentPolicy) enabled() bool {
	return p.commands().enabled()
}

// gitlabCommentAuthorizer decides whether a GitLab user may issue commands.
// Authorization is a username allow-list; an empty list authorizes everyone.
// A configured list whose entries are all blank fails closed rather than
// silently authorizing everyone. Access-level checks against
// /projects/:id/members/all would slot in here.
type gitlabCommentAuthorizer struct {
	restricted   bool
	allowedUsers map[string]struct{}
}

func newGitLabCommentAuthorizer(policy gitlabCommentPolicy) *gitlabCommentAuthorizer {
	a := &gitlabCommentAuthorizer{
		restricted:   len(policy.AllowedUsers) > 0,
		allowedUsers: make(map[string]struct{}, len(policy.AllowedUsers)),
	}
	for _, user := range policy.AllowedUsers {
		if normalized := normalizeGitLabUsername(user); normalized != "" {
			a.allowedUsers[normalized] = struct{}{}
		}
	}
	return a
}

func (a *gitlabCommentAuthorizer) isAuthorized(username string) bool {
	if !a.restricted {
		return true
	}
	_, ok := a.allowedUsers[normalizeGitLabUsername(username)]
	return ok
}

// evaluateGitLabCommentPolicy reports whether the item passes the policy and,
// when a trigger note matched, the note's creation time so re-triggers on a
// finished item can be detected. A trigger in the description counts but
// carries no time. System notes never match.
func evaluateGitLabCommentPolicy(description, author string, notes []gitlabNote, policy gitlabCommentPolicy, authorizer *gitlabCommentAuthorizer) (bool, time.Time) {
	if !policy.enabled() {
		return true, time.Time{}
	}

	cmds := policy.commands()
	var body bodyMatch
	if authorizer.isAuthorized(author) {
		body.trigger = cmds.Trigger != "" && containsCommand(description, cmds.Trigger)
		body.exclude = len(cmds.Excludes) > 0 && containsAnyCommand(description, cmds.Excludes)
	}

	triggerMatch, excludeMatch := newCommentMatch(), newCommentMatch()
	if cmds.Trigger != "" {
		triggerMatch = latestAuthorizedGitLabNote(notes, []string{cmds.Trigger}, authorizer)
	}
	if len(cmds.Excludes) > 0 {
		excludeMatch = latestAuthorizedGitLabNote(notes, cmds.Excludes, authorizer)
	}

	return decideCommentPolicy(cmds, body, triggerMatch, excludeMatch)
}

func latestAuthorizedGitLabNote(notes []gitlabNote, commands []string, authorizer *gitlabCommentAuthorizer) commentMatch {
	match := newCommentMatch()
	for i, note := range notes {
		if note.System || !containsAnyCommand(note.Body, commands) || !authorizer.isAuthorized(note.Author.Username) {
			continue
		}
		match.record(i, note.CreatedAt)
	}
	return match
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
