package v1alpha2

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// slackFilterCELRule returns the at-least-one-criterion rule, which is declared
// on SlackFilter itself so a violation is reported at the offending entry
// rather than at the parent, and so a future positive filters list inherits it.
func slackFilterCELRule(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("taskspawner_types.go")
	if err != nil {
		t.Fatalf("read types: %v", err)
	}
	for _, line := range strings.Split(string(source), "\n") {
		if strings.Contains(line, "XValidation") && strings.Contains(line, "self.channels") {
			return line
		}
	}
	t.Fatal("could not find the SlackFilter CEL rule")
	return ""
}

// TestSlackFilterCriteriaCoverEveryField fails when a criterion is added to
// SlackFilter without being listed in SlackFilterMatchCriteria, which would
// leave it unusable as a rule's only criterion and unchecked by the conversion
// restore path.
func TestSlackFilterCriteriaCoverEveryField(t *testing.T) {
	classified := map[string]bool{}
	for _, criterion := range SlackFilterMatchCriteria() {
		if classified[criterion] {
			t.Errorf("%q is listed twice in SlackFilterMatchCriteria", criterion)
		}
		classified[criterion] = true
	}

	typ := reflect.TypeOf(SlackFilter{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if !classified[tag] {
			t.Errorf("criterion %q is missing from SlackFilterMatchCriteria; add it there and to the "+
				"excludeFilters CEL rule, and confirm a value the event does not carry never matches", tag)
		}
		delete(classified, tag)
	}
	for criterion := range classified {
		t.Errorf("%q is listed in SlackFilterMatchCriteria but is not a field of SlackFilter", criterion)
	}
}

// TestSlackFilterCELRuleCountsEveryCriterion pins that the CEL rule
// admits a rule built from any single criterion, and that it demands a non-empty
// value — an empty list would otherwise satisfy has() while the matcher skips
// it, making the rule match everything.
func TestSlackFilterCELRuleCountsEveryCriterion(t *testing.T) {
	rule := slackFilterCELRule(t)
	for _, criterion := range SlackFilterMatchCriteria() {
		if !strings.Contains(rule, "has(self."+criterion+")") {
			t.Errorf("criterion %q is not counted by the SlackFilter CEL rule", criterion)
		}
		if !strings.Contains(rule, "size(self."+criterion+") > 0") {
			t.Errorf("the SlackFilter CEL rule does not require a non-empty %q", criterion)
		}
	}
}

// TestSlackFilterChannelIDPatternMatchesMarker keeps the constant the conversion
// restore path compiles in sync with the kubebuilder marker the API server
// enforces.
func TestSlackFilterChannelIDPatternMatchesMarker(t *testing.T) {
	source, err := os.ReadFile("taskspawner_types.go")
	if err != nil {
		t.Fatalf("read types: %v", err)
	}
	needle := "// +kubebuilder:validation:items:Pattern=`" + SlackFilterChannelIDPattern + "`"
	if !strings.Contains(string(source), needle) {
		t.Errorf("no items:Pattern marker matches SlackFilterChannelIDPattern (%s); the constant and the "+
			"marker have drifted, so conversion would accept channel IDs the API server rejects",
			SlackFilterChannelIDPattern)
	}
}

// TestSlackMirroredBoundsMatchMarkers pins the numeric bounds the conversion
// restore path mirrors against the markers themselves, so the "keep in sync"
// intent is enforced rather than trusted.
func TestSlackMirroredBoundsMatchMarkers(t *testing.T) {
	source, err := os.ReadFile("taskspawner_types.go")
	if err != nil {
		t.Fatalf("read types: %v", err)
	}
	text := string(source)
	for _, tc := range []struct {
		field string
		want  int
	}{
		{"ExcludeFilters []SlackFilter", SlackExcludeFiltersMaxItems},
		{"Channels []string `json:\"channels,omitempty\"`", SlackFilterChannelsMaxItems},
	} {
		i := strings.LastIndex(text, tc.field)
		if i < 0 {
			t.Errorf("could not find the %s field", tc.field)
			continue
		}
		block := text[max(0, i-500):i]
		if !strings.Contains(block, fmt.Sprintf("+kubebuilder:validation:MaxItems=%d", tc.want)) {
			t.Errorf("%s has no MaxItems=%d marker; the mirrored constant has drifted", tc.field, tc.want)
		}
	}
}
