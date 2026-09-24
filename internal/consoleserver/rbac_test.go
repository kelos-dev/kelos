package consoleserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// consoleRoleListableResources returns the kelos.dev resources the rendered
// console ClusterRole may list.
func consoleRoleListableResources(t *testing.T) map[string]bool {
	t.Helper()

	path := filepath.Join("..", "manifests", "charts", "kelos", "templates", "rbac.yaml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	section := string(contents)
	start := strings.Index(section, "name: kelos-console-server-role")
	if start == -1 {
		t.Fatal("kelos-console-server-role is not defined in the chart")
	}
	section = section[start:]
	if end := strings.Index(section, "\n---\n"); end != -1 {
		section = section[:end]
	}

	listable := make(map[string]bool)
	var pending []string
	inResources, inVerbs, isKelosGroup := false, false, false
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "apiGroups:":
			inResources, inVerbs = false, false
			isKelosGroup = false
		case trimmed == "resources:":
			inResources, inVerbs = true, false
			pending = nil
		case trimmed == "verbs:":
			inResources, inVerbs = false, true
		case strings.HasPrefix(trimmed, "- "):
			value := strings.TrimPrefix(trimmed, "- ")
			switch {
			case value == "kelos.dev":
				isKelosGroup = true
			case inResources:
				pending = append(pending, value)
			case inVerbs && value == "list" && isKelosGroup:
				for _, resource := range pending {
					listable[resource] = true
				}
			}
		}
	}
	return listable
}

// TestConsoleRoleCoversEveryBrowsedResource pins the console's resource
// registry to the ClusterRole it runs under. listConsoleResources fails the
// whole request on the first List error, so a resource registered here without
// a matching RBAC rule takes down the entire resource browser, not just its own
// section — including for users who never create that resource.
func TestConsoleRoleCoversEveryBrowsedResource(t *testing.T) {
	listable := consoleRoleListableResources(t)
	if len(listable) == 0 {
		t.Fatal("parsed no listable resources from the console ClusterRole")
	}

	for _, definition := range consoleResourceDefinitions {
		if !listable[definition.Resource] {
			t.Errorf("console browses %q but kelos-console-server-role cannot list it", definition.Resource)
		}
	}
}
