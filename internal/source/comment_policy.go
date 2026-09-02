package source

import "time"

// commentCommands is the provider-neutral shape of a comment policy: the
// trigger command that admits an item and the exclude commands that block it.
type commentCommands struct {
	Trigger  string
	Excludes []string
}

func (c commentCommands) enabled() bool {
	return c.Trigger != "" || len(c.Excludes) > 0
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
