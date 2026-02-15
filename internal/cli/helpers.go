package cli

import (
	"fmt"
	"os"
	"strings"
)

// resolveContent returns the content string directly, or if it starts with "@",
// reads the content from the referenced file path.
func resolveContent(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "@") {
		data, err := os.ReadFile(s[1:])
		if err != nil {
			return "", fmt.Errorf("reading file %s: %w", s[1:], err)
		}
		return string(data), nil
	}
	return s, nil
}

// parseNameContent splits a "name=content" or "name=@file" string into name
// and resolved content. The flagName parameter is used in error messages.
func parseNameContent(s, flagName string) (string, string, error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", fmt.Errorf("invalid --%s value %q: must be name=content or name=@file", flagName, s)
	}
	content, err := resolveContent(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("resolving --%s %q: %w", flagName, parts[0], err)
	}
	return parts[0], content, nil
}
