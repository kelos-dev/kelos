package v1alpha2

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestTaskConcurrencyGroupMaxLengthMatchesMarker pins the exported bound that
// conversion and Task rendering mirror against the marker on Group, so the two
// cannot drift apart.
func TestTaskConcurrencyGroupMaxLengthMatchesMarker(t *testing.T) {
	source, err := os.ReadFile("task_types.go")
	if err != nil {
		t.Fatalf("read types: %v", err)
	}
	text := string(source)

	const field = "Group string `json:\"group\"`"
	i := strings.Index(text, field)
	if i < 0 {
		t.Fatalf("could not find the %s field", field)
	}
	// The markers sit in the doc block immediately above the field.
	block := text[max(0, i-400):i]
	want := fmt.Sprintf("+kubebuilder:validation:MaxLength=%d", TaskConcurrencyGroupMaxLength)
	if !strings.Contains(block, want) {
		t.Errorf("TaskConcurrency.Group has no %s marker; the mirrored constant has drifted", want)
	}
}
