package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentEntrypointsHandleKelosEffort(t *testing.T) {
	tests := []struct {
		path  string
		parts []string
	}{
		{
			path: "../..//codex/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"model_reasoning_effort",
				"KELOS_KANON_HOME",
				"kanon apply --yes --home \"$KELOS_KANON_HOME\" --agent codex",
			},
		},
		{
			path: "../..//claude-code/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"--effort",
				"KELOS_KANON_HOME",
				"kanon apply --yes --home \"$KELOS_KANON_HOME\" --agent claude",
			},
		},
		{
			path: "../..//gemini/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"thinkingConfig",
				"thinkingBudget",
				"thinkingLevel",
			},
		},
		{
			path: "../..//opencode/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"reasoningEffort",
				"variant",
			},
		},
		{
			path: "../..//cursor/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"Kelos Effort",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.path, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Clean(tt.path))
			if err != nil {
				t.Fatalf("reading entrypoint: %v", err)
			}
			content := string(data)
			for _, part := range tt.parts {
				if !strings.Contains(content, part) {
					t.Fatalf("expected %s to contain %q", tt.path, part)
				}
			}
		})
	}
}
