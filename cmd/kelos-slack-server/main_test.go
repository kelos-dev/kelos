package main

import (
	"os"
	"testing"
)

func TestLoadSlackRoutes(t *testing.T) {
	path := writeSlackRouteConfig(t, `
routes:
  asd:
    channel: "#asd"
  prod:
    channel: C999
`)

	got, err := loadSlackRoutes(path)
	if err != nil {
		t.Fatalf("loadSlackRoutes returned error: %v", err)
	}

	want := map[string]string{
		"asd":  "#asd",
		"prod": "C999",
	}
	for alias, channel := range want {
		if got[alias].Channel != channel {
			t.Errorf("route %q channel = %q, want %q", alias, got[alias].Channel, channel)
		}
	}
}

func TestLoadSlackRoutesRejectsInvalidConfigs(t *testing.T) {
	tests := map[string]string{
		"empty":                 "",
		"missing routes":        "notRoutes: {}\n",
		"empty channel":         "routes:\n  incident:\n    channel: \"\"\n",
		"whitespace route name": "routes:\n  \"incident detection\":\n    channel: C123ABC\n",
		"unknown field":         "routes:\n  incident:\n    channel: C123ABC\n    extra: nope\n",
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadSlackRoutes(writeSlackRouteConfig(t, raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func writeSlackRouteConfig(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/routes.yaml"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing route config: %v", err)
	}
	return path
}
