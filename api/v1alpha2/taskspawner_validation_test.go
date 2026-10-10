package v1alpha2

import "testing"

// TestIsOnDemandTreatsEmptyTriggerModeAsSource pins the half of the contract the
// conversion round-trip test cannot reach: a write through v1alpha1 stores a
// v1alpha2 object with no triggerMode at all, because defaulting runs against
// the request version's schema. An empty value must read as Source, not as a
// spawner that has silently stopped acting on its own source.
func TestIsOnDemandTreatsEmptyTriggerModeAsSource(t *testing.T) {
	tests := []struct {
		name string
		mode TriggerMode
		want bool
	}{
		{name: "unset", mode: "", want: false},
		{name: "explicit source", mode: TriggerModeSource, want: false},
		{name: "on demand", mode: TriggerModeOnDemand, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &TaskSpawnerSpec{TriggerMode: tt.mode}
			if got := spec.IsOnDemand(); got != tt.want {
				t.Errorf("IsOnDemand() with triggerMode %q = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}
