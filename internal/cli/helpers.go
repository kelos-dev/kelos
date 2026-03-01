package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// parseGitHubPluginFlag parses a --github-plugin flag value in the format
// "name=owner/repo[@ref][,host=HOST][,secret=SECRET]" into a PluginSpec
// with a GitHubPluginSource.
func parseGitHubPluginFlag(s string) (kelosv1alpha1.PluginSpec, error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return kelosv1alpha1.PluginSpec{}, fmt.Errorf("invalid --github-plugin value %q: must be name=owner/repo[@ref][,host=HOST][,secret=SECRET]", s)
	}

	name := parts[0]
	remainder := parts[1]

	// Split remainder by comma to extract optional key=value pairs.
	segments := strings.Split(remainder, ",")
	if segments[0] == "" {
		return kelosv1alpha1.PluginSpec{}, fmt.Errorf("invalid --github-plugin value %q: repo is required", s)
	}

	repoAndRef := segments[0]
	var host, secret string

	for _, seg := range segments[1:] {
		kv := strings.SplitN(seg, "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			return kelosv1alpha1.PluginSpec{}, fmt.Errorf("invalid --github-plugin option %q in %q", seg, s)
		}
		switch kv[0] {
		case "host":
			host = kv[1]
		case "secret":
			secret = kv[1]
		default:
			return kelosv1alpha1.PluginSpec{}, fmt.Errorf("unknown --github-plugin option %q in %q", kv[0], s)
		}
	}

	// Split repo@ref.
	var repo, ref string
	if idx := strings.LastIndex(repoAndRef, "@"); idx > 0 {
		repo = repoAndRef[:idx]
		ref = repoAndRef[idx+1:]
		if ref == "" {
			return kelosv1alpha1.PluginSpec{}, fmt.Errorf("invalid --github-plugin repo %q: ref must not be empty when '@' is present", repoAndRef)
		}
	} else {
		repo = repoAndRef
	}

	// Validate owner/repo format.
	repoParts := strings.Split(repo, "/")
	if len(repoParts) != 2 || repoParts[0] == "" || repoParts[1] == "" {
		return kelosv1alpha1.PluginSpec{}, fmt.Errorf("invalid --github-plugin repo %q: must be in owner/repo format", repo)
	}

	ghSource := &kelosv1alpha1.GitHubPluginSource{
		Repo: repo,
	}
	if ref != "" {
		ghSource.Ref = &ref
	}
	if host != "" {
		ghSource.Host = &host
	}
	if secret != "" {
		ghSource.SecretRef = &kelosv1alpha1.SecretReference{Name: secret}
	}

	return kelosv1alpha1.PluginSpec{
		Name:   name,
		GitHub: ghSource,
	}, nil
}

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
		return strings.TrimRight(string(data), "\n"), nil
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
