package version

import "testing"

func TestVersionDefault(t *testing.T) {
	// When built with go test (no ldflags), the init function runs but
	// debug.ReadBuildInfo returns "(devel)" for the main module version,
	// so Version should remain "latest".
	if Version != "latest" {
		t.Errorf("expected default version to be 'latest', got %q", Version)
	}
}
