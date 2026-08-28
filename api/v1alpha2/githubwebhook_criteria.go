/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha2

import "slices"

// The classification of GitHubWebhookFilter criteria that the excludeFilters
// CEL rules on GitHubWebhook encode, named by JSON tag. These lists are the
// single source of truth: the CEL markers, the conversion restore path (which
// re-validates a user-writable preservation annotation the API server never
// checks), and the tests that pin both are all derived from or checked against
// them. Adding a criterion to GitHubWebhookFilter without classifying it here
// fails the API package's validation tests.
//
// They are unexported and reached through the accessors below, which return
// copies: a caller that reordered or appended to a shared slice would silently
// change how exclusion rules are validated.
var (
	// safe unscoped: a value the event does not carry never matches, so the
	// criterion is usable in a rule that omits Event.
	gitHubWebhookExcludeFilterSafeUnscopedCriteria = []string{
		"action", "author", "pullRequestAuthor", "branch", "tag",
	}

	// event scoped: evaluated only inside their arm of the matcher's event
	// switch and skipped — and so treated as satisfied — for any other event
	// type. A rule using one must set Event or it would reject every event the
	// criterion does not apply to.
	//
	// TODO: requiring *an* Event is not the whole guarantee. Scoping to an
	// event type whose matcher arm still does not read the criterion leaves it
	// satisfied, so {event: issue_comment, draft: true} rejects every comment.
	// Closing that needs a rule pairing each criterion with the event types
	// whose arm actually reads it; it is documented in the godoc, the reference
	// table, and the integration guide until then.
	gitHubWebhookExcludeFilterEventScopedCriteria = []string{
		"labels", "state", "draft", "commentOn", "bodyPattern", "conclusion", "checkName",
	}

	// rejected: not accepted on an exclusion rule at all. FilePatterns would
	// require a changed-files fetch the exclusion path does not perform,
	// BodyContains is deprecated, and a negative criterion inverts inside an
	// exclusion rule so a rule built from one rejects every event that does not
	// match it.
	gitHubWebhookExcludeFilterRejectedCriteria = []string{
		"filePatterns", "bodyContains", "excludeAuthors", "excludeLabels", "excludeBodyPatterns",
	}

	// Allowed values for the criteria that declare an Enum marker, keyed by JSON
	// tag. The restore path checks these because an out-of-enum value is not
	// merely invalid: the matcher switches on the value and treats an unknown
	// one as satisfied, so a rule carrying it rejects every event of that type.
	gitHubWebhookFilterCriterionEnums = map[string][]string{
		"commentOn":  {CommentOnIssue, CommentOnPullRequest, ""},
		"conclusion": {"success", "failure", "cancelled", "timed_out", "action_required", "neutral", "skipped", "stale"},
	}
)

// Bounds the conversion restore path mirrors from the kubebuilder markers, kept
// here so there is one home for everything that path re-checks. The API
// package's validation tests assert each against its marker.
const (
	// GitHubWebhookFiltersMaxItems bounds spec.when.githubWebhook.filters.
	GitHubWebhookFiltersMaxItems = 50
	// GitHubWebhookExcludeFiltersMaxItems bounds
	// spec.when.githubWebhook.excludeFilters.
	GitHubWebhookExcludeFiltersMaxItems = 20
	// GitHubWebhookFilterPullRequestAuthorMaxLength bounds a filter's
	// pullRequestAuthor, in runes.
	GitHubWebhookFilterPullRequestAuthorMaxLength = 64
)

// GitHubWebhookExcludeFilterSafeUnscopedCriteria returns the criteria usable in
// an exclusion rule that omits Event.
func GitHubWebhookExcludeFilterSafeUnscopedCriteria() []string {
	return slices.Clone(gitHubWebhookExcludeFilterSafeUnscopedCriteria)
}

// GitHubWebhookExcludeFilterEventScopedCriteria returns the criteria that an
// exclusion rule may only use together with an Event scope.
func GitHubWebhookExcludeFilterEventScopedCriteria() []string {
	return slices.Clone(gitHubWebhookExcludeFilterEventScopedCriteria)
}

// GitHubWebhookExcludeFilterRejectedCriteria returns the criteria an exclusion
// rule may not use at all.
func GitHubWebhookExcludeFilterRejectedCriteria() []string {
	return slices.Clone(gitHubWebhookExcludeFilterRejectedCriteria)
}

// GitHubWebhookFilterCriterionEnum returns the values a criterion accepts, and
// whether it constrains its values at all.
func GitHubWebhookFilterCriterionEnum(criterion string) ([]string, bool) {
	values, ok := gitHubWebhookFilterCriterionEnums[criterion]
	return slices.Clone(values), ok
}

// GitHubWebhookFilterEnumCriteria returns the criteria that constrain their
// values to an enum.
func GitHubWebhookFilterEnumCriteria() []string {
	criteria := make([]string, 0, len(gitHubWebhookFilterCriterionEnums))
	for criterion := range gitHubWebhookFilterCriterionEnums {
		criteria = append(criteria, criterion)
	}
	slices.Sort(criteria)
	return criteria
}
