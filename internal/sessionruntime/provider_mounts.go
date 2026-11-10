package sessionruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func readProviderMountPoints() ([]string, error) {
	// Session Pods run on Linux; other platforms exercise setup in local tests.
	if runtime.GOOS != "linux" {
		return nil, nil
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("reading Session mount points: %w", err)
	}
	return parseProviderMountPoints(string(data))
}

func parseProviderMountPoints(data string) ([]string, error) {
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	var points []string
	for i, line := range strings.Split(data, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 || !filepath.IsAbs(fields[4]) {
			return nil, fmt.Errorf("invalid Session mount information on line %d", i+1)
		}
		points = append(points, unescape.Replace(fields[4]))
	}
	return points, nil
}

func rejectProviderMounts(path string, mountPoints []string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolving Session provider path %q: %w", path, err)
	}
	for _, mount := range mountPoints {
		if mount == resolved || strings.HasPrefix(mount, resolved+string(filepath.Separator)) {
			return fmt.Errorf("volume mount %q is at or below Session provider path %q; mount configuration elsewhere and copy it during setup", mount, path)
		}
	}
	return nil
}
