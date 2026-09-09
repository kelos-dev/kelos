package v1alpha2

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The buckets under test live in githubwebhook_criteria.go so the CEL markers,
// the conversion restore path, and these tests all read the same lists.
var (
	safeUnscoped = GitHubWebhookExcludeFilterSafeUnscopedCriteria()
	eventScoped  = GitHubWebhookExcludeFilterEventScopedCriteria()
	rejected     = GitHubWebhookExcludeFilterRejectedCriteria()
)

// celRules returns the three excludeFilters rules keyed by a distinctive
// fragment of their message.
func celRules(t *testing.T) map[string]string {
	t.Helper()
	source, err := os.ReadFile("taskspawner_types.go")
	if err != nil {
		t.Fatalf("read types: %v", err)
	}
	want := map[string]string{
		"positive": "at least one non-empty matching criterion",
		"rejected": "cannot use filePatterns",
		"scope":    "must set event when using a criterion",
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(source), "\n") {
		if !strings.Contains(line, "XValidation") || !strings.Contains(line, "self.excludeFilters") {
			continue
		}
		for key, fragment := range want {
			if strings.Contains(line, fragment) {
				got[key] = line
			}
		}
	}
	for key := range want {
		if got[key] == "" {
			t.Fatalf("could not find the %q excludeFilters CEL rule; its message may have changed", key)
		}
	}
	return got
}

// positiveRefs counts non-negated has(f.<field>) references, so a criterion that
// only survives as !has(f.<field>) in another clause is not miscounted.
func positiveRefs(rule, field string) int {
	return strings.Count(rule, "has(f."+field+")") - strings.Count(rule, "!has(f."+field+")")
}

func TestExcludeFilterCELRulesMatchCriterionBuckets(t *testing.T) {
	rules := celRules(t)

	for _, field := range safeUnscoped {
		if positiveRefs(rules["positive"], field) == 0 {
			t.Errorf("%q is safe unscoped but is not counted as a criterion; a rule setting only it would be rejected", field)
		}
		if strings.Contains(rules["scope"], "has(f."+field+")") {
			t.Errorf("%q is safe unscoped but the scoping rule demands an event for it", field)
		}
	}

	for _, field := range eventScoped {
		if positiveRefs(rules["positive"], field) == 0 {
			t.Errorf("%q is event-scoped but is not counted as a criterion; a rule setting only it would be rejected", field)
		}
		if !strings.Contains(rules["scope"], "!has(f."+field+")") {
			t.Errorf("%q is only evaluated inside its event arm, so the scoping rule must demand an event for it", field)
		}
	}

	for _, field := range rejected {
		if !strings.Contains(rules["rejected"], "!has(f."+field+")") {
			t.Errorf("%q is no longer rejected on excludeFilters entries", field)
		}
		if positiveRefs(rules["positive"], field) != 0 {
			t.Errorf("%q is rejected but still counts as a criterion", field)
		}
	}
}

// TestExcludeFilterBucketsCoverEveryCriterion fails when a criterion is added to
// GitHubWebhookFilter without being classified, which would otherwise leave it
// silently unusable on its own in an exclusion rule or let it past the
// event-scoping requirement.
func TestExcludeFilterBucketsCoverEveryCriterion(t *testing.T) {
	classified := map[string]string{}
	for _, bucket := range []struct {
		name   string
		fields []string
	}{{"safeUnscoped", safeUnscoped}, {"eventScoped", eventScoped}, {"rejected", rejected}} {
		for _, field := range bucket.fields {
			if prior, dup := classified[field]; dup {
				t.Errorf("%q is in both %s and %s", field, prior, bucket.name)
			}
			classified[field] = bucket.name
		}
	}

	typ := reflect.TypeOf(GitHubWebhookFilter{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" || tag == "event" {
			continue // event is the scope, not a criterion
		}
		if _, ok := classified[tag]; !ok {
			t.Errorf("criterion %q is unclassified: add it to safeUnscoped, eventScoped, or rejected "+
				"and update the excludeFilters CEL rules to match", tag)
		}
		delete(classified, tag)
	}
	for field, bucket := range classified {
		t.Errorf("%q is classified in %s but no longer exists on GitHubWebhookFilter", field, bucket)
	}
}

// filterStructMarkers returns the kubebuilder markers declared on each
// GitHubWebhookFilter field, keyed by JSON tag, so tests can compare what the
// API package mirrors against what the API server actually enforces.
func filterStructMarkers(t *testing.T) map[string][]string {
	t.Helper()
	source, err := os.ReadFile("taskspawner_types.go")
	if err != nil {
		t.Fatalf("read types: %v", err)
	}
	lines := strings.Split(string(source), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "type GitHubWebhookFilter struct {") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatal("could not find the GitHubWebhookFilter struct")
	}

	markers := map[string][]string{}
	var pending []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "}" {
			break
		}
		if strings.HasPrefix(trimmed, "// +kubebuilder:") {
			pending = append(pending, strings.TrimPrefix(trimmed, "// "))
			continue
		}
		if strings.HasPrefix(trimmed, "//") || trimmed == "" {
			continue
		}
		if tag := structTagJSONName(trimmed); tag != "" {
			markers[tag] = pending
		}
		pending = nil
	}
	return markers
}

func structTagJSONName(line string) string {
	i := strings.Index(line, "`json:\"")
	if i < 0 {
		return ""
	}
	rest := line[i+len("`json:\""):]
	j := strings.Index(rest, "\"")
	if j < 0 {
		return ""
	}
	return strings.Split(rest[:j], ",")[0]
}

// TestFilterCriterionEnumsMatchMarkers keeps the enum values the restore path
// checks in step with the markers the API server enforces, in both directions:
// a criterion that gains an Enum marker must be listed, and a listed criterion
// must match its marker exactly. An unlisted enum criterion is not a cosmetic
// gap — the matcher switches on the value and treats an unknown one as
// satisfied, so a restored rule carrying it rejects every event of that type.
func TestFilterCriterionEnumsMatchMarkers(t *testing.T) {
	markers := filterStructMarkers(t)

	for tag, fieldMarkers := range markers {
		var marker string
		for _, m := range fieldMarkers {
			if strings.HasPrefix(m, "+kubebuilder:validation:Enum=") {
				marker = strings.TrimPrefix(m, "+kubebuilder:validation:Enum=")
			}
		}
		values, listed := GitHubWebhookFilterCriterionEnum(tag)
		if marker == "" {
			if listed {
				t.Errorf("%q is listed with enum values but declares no Enum marker", tag)
			}
			continue
		}
		if !listed {
			t.Errorf("criterion %q declares Enum=%s but is not listed in the API package, so the "+
				"conversion restore path would accept a value the API server rejects", tag, marker)
			continue
		}
		want := []string{}
		for _, v := range strings.Split(marker, ";") {
			want = append(want, strings.Trim(v, `"`))
		}
		if !slices.Equal(values, want) {
			t.Errorf("criterion %q enum = %v, marker declares %v", tag, values, want)
		}
	}
}

// TestMirroredBoundsMatchMarkers pins the numeric bounds the restore path
// mirrors against the markers themselves, so the "keep these in sync" comment
// is enforced rather than trusted.
func TestMirroredBoundsMatchMarkers(t *testing.T) {
	source, err := os.ReadFile("taskspawner_types.go")
	if err != nil {
		t.Fatalf("read types: %v", err)
	}
	text := string(source)

	markers := filterStructMarkers(t)
	if !slices.Contains(markers["pullRequestAuthor"],
		fmt.Sprintf("+kubebuilder:validation:MaxLength=%d", GitHubWebhookFilterPullRequestAuthorMaxLength)) {
		t.Errorf("pullRequestAuthor has no MaxLength=%d marker", GitHubWebhookFilterPullRequestAuthorMaxLength)
	}

	for _, tc := range []struct {
		field string
		want  int
	}{
		{"Filters []GitHubWebhookFilter", GitHubWebhookFiltersMaxItems},
		{"ExcludeFilters []GitHubWebhookFilter", GitHubWebhookExcludeFiltersMaxItems},
	} {
		i := strings.Index(text, tc.field)
		if i < 0 {
			t.Errorf("could not find the %s field", tc.field)
			continue
		}
		// The markers sit in the doc block immediately above the field.
		block := text[max(0, i-400):i]
		if !strings.Contains(block, fmt.Sprintf("+kubebuilder:validation:MaxItems=%d", tc.want)) {
			t.Errorf("%s has no MaxItems=%d marker; the mirrored constant has drifted", tc.field, tc.want)
		}
	}
}
