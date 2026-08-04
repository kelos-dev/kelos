package v1alpha2

import (
	"encoding/json"
	"strings"
	"testing"
)

// A scored Task with zero of one verdict must still serialize that zero, or the
// print column renders <none>, which reads as unknown rather than zero.
func TestTaskScoreSummarySerializesZeroCounts(t *testing.T) {
	data, err := json.Marshal(&TaskScoreSummary{Negative: 3, Total: 3})
	if err != nil {
		t.Fatalf("marshalling summary: %v", err)
	}
	for _, want := range []string{`"positive":0`, `"negative":3`, `"total":3`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("serialized summary %s is missing %s", data, want)
		}
	}
}
