package source

import (
	"context"
	"time"
)

// commentCommands is the provider-neutral shape of a comment policy: the
// trigger command that admits an item and the exclude commands that block it.
type commentCommands struct {
	Trigger  string
	Excludes []string
}

func (c commentCommands) enabled() bool {
	return c.Trigger != "" || len(c.Excludes) > 0
}

// commentEntry is the provider-neutral shape of one comment in an item's
// thread. Sources convert their own comment types to it once, at the
// boundary, so the policy logic never sees provider types.
type commentEntry struct {
	Body      string
	Author    string
	CreatedAt string
	// System marks tracker-generated activity (label changes, assignments)
	// that can never carry a command.
	System bool
}

// commentAuthorizer decides whether an author may issue commands. It may
// call the tracker's API, so it is only consulted for comments that carry a
// command.
type commentAuthorizer interface {
	isAuthorized(ctx context.Context, author string) (bool, error)
}

// evaluateCommentPolicy reports whether an item passes its comment policy
// and, when a trigger comment matched, that comment's creation time so a
// re-trigger on a finished item can be detected. A trigger in the body counts
// but carries no time.
func evaluateCommentPolicy(ctx context.Context, cmds commentCommands, body, author string, comments []commentEntry, authorizer commentAuthorizer) (bool, time.Time, error) {
	if !cmds.enabled() {
		return true, time.Time{}, nil
	}

	bodyHasTrigger := cmds.Trigger != "" && containsCommand(body, cmds.Trigger)
	bodyHasExclude := len(cmds.Excludes) > 0 && containsAnyCommand(body, cmds.Excludes)
	var bodyMatches bodyMatch
	if bodyHasTrigger || bodyHasExclude {
		authorized, err := authorizer.isAuthorized(ctx, author)
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
		triggerMatch, err = latestAuthorizedComment(ctx, comments, []string{cmds.Trigger}, authorizer)
		if err != nil {
			return false, time.Time{}, err
		}
	}
	if len(cmds.Excludes) > 0 {
		excludeMatch, err = latestAuthorizedComment(ctx, comments, cmds.Excludes, authorizer)
		if err != nil {
			return false, time.Time{}, err
		}
	}

	allowed, triggerTime := decideCommentPolicy(cmds, bodyMatches, triggerMatch, excludeMatch)
	return allowed, triggerTime, nil
}

// latestAuthorizedComment finds the most recent comment carrying one of the
// commands from an authorized author. System comments never match.
func latestAuthorizedComment(ctx context.Context, comments []commentEntry, commands []string, authorizer commentAuthorizer) (commentMatch, error) {
	match := newCommentMatch()
	for i, comment := range comments {
		if comment.System || !containsAnyCommand(comment.Body, commands) {
			continue
		}
		authorized, err := authorizer.isAuthorized(ctx, comment.Author)
		if err != nil {
			return commentMatch{}, err
		}
		if authorized {
			match.record(i, comment.CreatedAt)
		}
	}
	return match, nil
}

// bodyMatch records which commands the item body itself carries from an
// authorized author. A body trigger admits the item but carries no time.
type bodyMatch struct {
	trigger bool
	exclude bool
}

// commentMatch records the most recent authorized command found in a comment
// thread. Comments without a parseable creation time fall back to list
// position, and only win when no timed match exists.
type commentMatch struct {
	found   bool
	hasTime bool
	time    time.Time
	index   int
}

func newCommentMatch() commentMatch {
	return commentMatch{index: -1}
}

// record folds the comment at position i, created at the given RFC3339 time,
// into the match, keeping the most recent one.
func (m *commentMatch) record(i int, createdAt string) {
	m.found = true
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		if !m.hasTime {
			m.index = i
		}
		return
	}
	if !m.hasTime || t.After(m.time) || (t.Equal(m.time) && i > m.index) {
		m.hasTime = true
		m.time = t
		m.index = i
	}
}

// compare orders two matches by recency: creation time when both carry one,
// otherwise position in the comment list.
func (m commentMatch) compare(other commentMatch) int {
	switch {
	case m.found && other.found:
		if m.hasTime && other.hasTime {
			switch {
			case m.time.After(other.time):
				return 1
			case other.time.After(m.time):
				return -1
			}
		}
		switch {
		case m.index > other.index:
			return 1
		case other.index > m.index:
			return -1
		}
		return 0
	case m.found:
		return 1
	case other.found:
		return -1
	}
	return 0
}

// decideCommentPolicy applies the trigger/exclude precedence once body and
// comment matches are known: with only a trigger, any match admits; with only
// excludes, any match blocks; with both, the most recent comment command wins
// and the body breaks ties, exclude first. The returned time is the trigger
// comment's creation time, or zero when the trigger came from the body.
func decideCommentPolicy(cmds commentCommands, body bodyMatch, triggerMatch, excludeMatch commentMatch) (bool, time.Time) {
	switch {
	case len(cmds.Excludes) == 0:
		if triggerMatch.found {
			return true, triggerMatch.time
		}
		return body.trigger, time.Time{}
	case cmds.Trigger == "":
		return !excludeMatch.found && !body.exclude, time.Time{}
	}

	switch triggerMatch.compare(excludeMatch) {
	case 1:
		return true, triggerMatch.time
	case -1:
		return false, time.Time{}
	}
	if body.exclude {
		return false, time.Time{}
	}
	return body.trigger, time.Time{}
}
