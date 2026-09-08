package source

import (
	"context"
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

// isAuthorized never fails: the allow-list is local, so no API call is made.
func (a *gitlabCommentAuthorizer) isAuthorized(_ context.Context, username string) (bool, error) {
	if !a.restricted {
		return true, nil
	}
	_, ok := a.allowedUsers[normalizeGitLabUsername(username)]
	return ok, nil
}

// evaluateGitLabCommentPolicy reports whether the item passes the policy and,
// when a trigger note matched, the note's creation time so re-triggers on a
// finished item can be detected.
func evaluateGitLabCommentPolicy(description, author string, notes []gitlabNote, policy gitlabCommentPolicy, authorizer *gitlabCommentAuthorizer) (bool, time.Time) {
	allowed, triggerTime, _ := evaluateCommentPolicy(context.Background(), policy.commands(), description, author, gitlabCommentEntries(notes), authorizer)
	return allowed, triggerTime
}

func gitlabCommentEntries(notes []gitlabNote) []commentEntry {
	entries := make([]commentEntry, len(notes))
	for i, n := range notes {
		entries[i] = commentEntry{Body: n.Body, Author: n.Author.Username, CreatedAt: n.CreatedAt, System: n.System}
	}
	return entries
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
