package scoring

import (
	"regexp"
	"strings"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// reactionNamePattern mirrors the Pattern marker on
// SlackReactionScore.Name. It exists because reaction config can reach the hub
// type without passing the v1alpha2 schema: CRD validation runs against the
// request version, so a scoring block restored from the v1alpha1 preservation
// annotation was never checked against these constraints.
//
// Keep it in step with the kubebuilder marker; ValidReactionScore is the only
// caller and its test asserts the two agree on the cases that matter.
var reactionNamePattern = regexp.MustCompile(`^[a-z0-9_+-]+$`)

// ValidReactionScore reports whether a reaction mapping satisfies the same
// constraints the CRD enforces: a name in Slack's alphabet, no longer than 64
// characters, and a known verdict.
func ValidReactionScore(entry kelos.SlackReactionScore) bool {
	if len(entry.Name) == 0 || len(entry.Name) > 64 {
		return false
	}
	if !reactionNamePattern.MatchString(entry.Name) {
		return false
	}
	switch entry.Verdict {
	case kelos.ScoreVerdictPositive, kelos.ScoreVerdictNegative:
		return true
	default:
		return false
	}
}

// NormalizeReaction strips the skin-tone modifier Slack appends to some emoji
// so "+1::skin-tone-4" is scored as the same signal as "+1". Configuration keys
// use the base name.
func NormalizeReaction(reaction string) string {
	if i := strings.Index(reaction, "::"); i >= 0 {
		return reaction[:i]
	}
	return reaction
}

// SlackReactionEnabled reports whether the configuration maps any reaction onto
// a verdict. It lets callers skip resolution work for spawners that do not score.
func SlackReactionEnabled(cfg *kelos.SlackScoring) bool {
	return cfg != nil && len(cfg.Reactions) > 0
}

// VerdictForSlackReaction maps a Slack reaction name onto a verdict. The second
// return is false when the reaction is not configured, in which case no score is
// recorded — unconfigured emoji in a busy channel must not dilute the aggregate.
//
// The event's reaction is normalized and compared against configured names
// exactly, rather than normalizing both sides. Configured names cannot carry a
// modifier (the API rejects it), so an exact comparison is unambiguous, whereas
// normalizing both sides would let two entries differing only by modifier match
// the same reaction and resolve to whichever the scan reached first.
func VerdictForSlackReaction(cfg *kelos.SlackScoring, reaction string) (kelos.ScoreVerdict, bool) {
	if !SlackReactionEnabled(cfg) {
		return "", false
	}
	name := NormalizeReaction(reaction)
	for _, configured := range cfg.Reactions {
		if configured.Name == name {
			return configured.Verdict, true
		}
	}
	return "", false
}
