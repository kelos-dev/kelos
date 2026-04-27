package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// resolveContent returns the content string directly, or if it starts with "@",
// reads the content from the referenced file path. A leading "~" or "~/" in the
// file path is expanded to the current user's home directory, since os.ReadFile
// does not perform shell-style tilde expansion.
func resolveContent(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "@") {
		path, err := expandHome(s[1:])
		if err != nil {
			return "", fmt.Errorf("reading file %s: %w", s[1:], err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading file %s: %w", s[1:], err)
		}
		return strings.TrimRight(string(data), "\n"), nil
	}
	return s, nil
}

// expandHome replaces a leading "~" or "~/" in path with the current user's
// home directory. Other paths (including "~user" forms) are returned unchanged.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expanding ~: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
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

// parseMCPFlag parses a --mcp flag value in the format "name=JSON" or
// "name=@file" into an MCPServerSpec. The JSON (or file content) must
// contain at least a "type" field.
func parseMCPFlag(s string) (kelosv1alpha1.MCPServerSpec, error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return kelosv1alpha1.MCPServerSpec{}, fmt.Errorf("invalid --mcp value %q: must be name=JSON or name=@file", s)
	}
	name := parts[0]
	content, err := resolveContent(parts[1])
	if err != nil {
		return kelosv1alpha1.MCPServerSpec{}, fmt.Errorf("resolving --mcp %q: %w", name, err)
	}

	var raw struct {
		Type    string            `json:"type"`
		Command string            `json:"command,omitempty"`
		Args    []string          `json:"args,omitempty"`
		URL     string            `json:"url,omitempty"`
		Headers map[string]string `json:"headers,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return kelosv1alpha1.MCPServerSpec{}, fmt.Errorf("invalid --mcp %q JSON: %w", name, err)
	}
	if raw.Type == "" {
		return kelosv1alpha1.MCPServerSpec{}, fmt.Errorf("--mcp %q: \"type\" field is required", name)
	}
	switch raw.Type {
	case "stdio", "http", "sse":
	default:
		return kelosv1alpha1.MCPServerSpec{}, fmt.Errorf("--mcp %q: unsupported type %q (must be stdio, http, or sse)", name, raw.Type)
	}

	return kelosv1alpha1.MCPServerSpec{
		Name:    name,
		Type:    raw.Type,
		Command: raw.Command,
		Args:    raw.Args,
		URL:     raw.URL,
		Headers: raw.Headers,
		Env:     raw.Env,
	}, nil
}

// parseSkillsShFlag parses a --skills-sh flag value in the format
// "source" or "source:skill" into a SkillsShSpec.
func parseSkillsShFlag(s string) (kelosv1alpha1.SkillsShSpec, error) {
	if s == "" {
		return kelosv1alpha1.SkillsShSpec{}, fmt.Errorf("invalid --skills-sh value: must not be empty")
	}
	parts := strings.SplitN(s, ":", 2)
	if parts[0] == "" {
		return kelosv1alpha1.SkillsShSpec{}, fmt.Errorf("invalid --skills-sh value %q: source must not be empty", s)
	}
	spec := kelosv1alpha1.SkillsShSpec{Source: parts[0]}
	if len(parts) == 2 {
		if parts[1] == "" {
			return kelosv1alpha1.SkillsShSpec{}, fmt.Errorf("invalid --skills-sh value %q: skill name after colon must not be empty", s)
		}
		spec.Skill = parts[1]
	}
	return spec, nil
}
