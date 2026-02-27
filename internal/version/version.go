package version

import (
	"runtime/debug"

	"golang.org/x/mod/semver"
)

// Version is the current version of the kelos binary. It is set at build time
// via ldflags. When unset (e.g. during development or go install) it falls
// back to the module version from Go build info, and finally to "latest".
var Version = "latest"

func init() {
	if Version != "latest" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	v := info.Main.Version
	// Only accept valid semver versions without pre-release suffixes.
	// This filters out "(devel)" and pseudo-versions like
	// "v0.0.0-20260101000000-abcdef123456" that Go generates for
	// local builds, while accepting clean tags like "v0.1.0".
	if !semver.IsValid(v) || semver.Prerelease(v) != "" {
		return
	}
	Version = v
}
