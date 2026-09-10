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

// SlackFilterMatchCriteria names every SlackFilter criterion by JSON tag. It is
// the single source of truth shared by the excludeFilters CEL rule on Slack, the
// conversion restore path (which re-validates a user-writable preservation
// annotation the API server never checks), and the tests that pin both. Adding
// a criterion to SlackFilter without listing it here fails the API package's
// validation tests.
//
// Every criterion must hold the property the ExcludeFilters contract documents:
// a value the event does not carry never matches, so an exclusion rule using
// the criterion cannot silently reject events it was not meant to. A criterion
// that can only be evaluated for some event shapes does not belong here without
// a corresponding scoping rule, the way the GitHub webhook source handles its
// event-scoped criteria.
//
// It is unexported and reached through SlackFilterMatchCriteria, which returns
// a copy: a caller that reordered or appended to a shared slice would silently
// change how exclusion rules are validated.
var slackFilterMatchCriteria = []string{"channels"}

// SlackFilterMatchCriteria returns every SlackFilter criterion by JSON tag.
func SlackFilterMatchCriteria() []string {
	return slices.Clone(slackFilterMatchCriteria)
}

// Bounds the conversion restore path mirrors from the kubebuilder markers, kept
// here so there is one home for everything that path re-checks. The API
// package's validation tests assert each against its marker.
const (
	// SlackExcludeFiltersMaxItems bounds spec.when.slack.excludeFilters.
	SlackExcludeFiltersMaxItems = 20
	// SlackFilterChannelsMaxItems bounds a rule's channels list.
	SlackFilterChannelsMaxItems = 64
)

// SlackFilterChannelIDPattern is the item pattern applied to a SlackFilter's
// channel IDs. It admits direct-message IDs, which Slack.Channels does not, so
// an exclusion rule can keep a catch-all spawner out of DMs. Kept in sync with
// the kubebuilder marker by the API package's validation tests.
const SlackFilterChannelIDPattern = `^[CGD][A-Z0-9]{8,}$`
