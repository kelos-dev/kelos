package reporting

import (
	"strings"
	"testing"
)

func TestExtractClaudeActivity_ToolUse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "read file",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/home/user/project/src/main.go"}}]}}`,
			want:  "Reading `src/main.go`...",
		},
		{
			name:  "edit file",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/project/internal/handler.go"}}]}}`,
			want:  "Editing `internal/handler.go`...",
		},
		{
			name:  "write file",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/project/new_file.go"}}]}}`,
			want:  "Writing `project/new_file.go`...",
		},
		{
			name:  "bash command",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"make test"}}]}}`,
			want:  "Running `make test`...",
		},
		{
			name:  "grep pattern",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"func main"}}]}}`,
			want:  "Searching for `func main`...",
		},
		{
			name:  "glob pattern",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.go"}}]}}`,
			want:  "Searching for `**/*.go`...",
		},
		{
			name:  "web fetch",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebFetch","input":{"url":"https://example.com"}}]}}`,
			want:  "Fetching web page...",
		},
		{
			name:  "web search",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebSearch","input":{"query":"golang rate limiter"}}]}}`,
			want:  "Searching `golang rate limiter`...",
		},
		{
			name:  "unknown tool",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"CustomTool","input":{}}]}}`,
			want:  "Using CustomTool...",
		},
		{
			name:  "text block returns thinking",
			input: `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me analyze this code."}]}}`,
			want:  "Thinking...",
		},
		{
			name: "last tool wins",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a/b.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go build"}}]}}`,
			want: "Running `go build`...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractActivity(strings.NewReader(tt.input), "claude-code")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractClaudeActivity_Empty(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty input", ""},
		{"non-json", "this is not json"},
		{"system event", `{"type":"system","subtype":"init"}`},
		{"result event", `{"type":"result","is_error":false}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractActivity(strings.NewReader(tt.input), "claude-code")
			if got != "" {
				t.Errorf("got %q, want empty string", got)
			}
		})
	}
}

func TestExtractClaudeActivity_CursorAgentType(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/project/main.go"}}]}}`
	got := ExtractActivity(strings.NewReader(input), "cursor")
	if got != "Reading `project/main.go`..." {
		t.Errorf("got %q, want %q", got, "Reading `project/main.go`...")
	}
}

func TestExtractClaudeActivity_DefaultAgentType(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`
	got := ExtractActivity(strings.NewReader(input), "")
	if got != "Running `ls`..." {
		t.Errorf("got %q, want %q", got, "Running `ls`...")
	}
}

func TestExtractCodexActivity(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "command execution",
			input: `{"type":"item.started","item":{"type":"command_execution","command":"npm test"}}`,
			want:  "Running `npm test`...",
		},
		{
			name:  "agent message",
			input: `{"type":"item.started","item":{"type":"agent_message"}}`,
			want:  "Thinking...",
		},
		{
			name:  "empty command",
			input: `{"type":"item.started","item":{"type":"command_execution"}}`,
			want:  "Running command...",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractActivity(strings.NewReader(tt.input), "codex")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractGeminiActivity(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "shell command",
			input: `{"type":"tool_use","tool_name":"Shell","parameters":{"command":"make build"}}`,
			want:  "Running `make build`...",
		},
		{
			name:  "read file",
			input: `{"type":"tool_use","tool_name":"ReadFile","parameters":{"file_path":"/src/app.ts"}}`,
			want:  "Reading `src/app.ts`...",
		},
		{
			name:  "write file",
			input: `{"type":"tool_use","tool_name":"WriteFile","parameters":{"file_path":"/src/config.yaml"}}`,
			want:  "Writing `src/config.yaml`...",
		},
		{
			name:  "edit file",
			input: `{"type":"tool_use","tool_name":"EditFile","parameters":{"file_path":"/src/handler.go"}}`,
			want:  "Editing `src/handler.go`...",
		},
		{
			name:  "assistant message",
			input: `{"type":"message","role":"assistant","content":"Let me check."}`,
			want:  "Thinking...",
		},
		{
			name:  "unknown tool",
			input: `{"type":"tool_use","tool_name":"SpecialTool","parameters":{}}`,
			want:  "Using SpecialTool...",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractActivity(strings.NewReader(tt.input), "gemini")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractOpenCodeActivity(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "step start",
			input: `{"type":"step_start"}`,
			want:  "Working...",
		},
		{
			name:  "text event",
			input: `{"type":"text","text":"analyzing..."}`,
			want:  "Thinking...",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractActivity(strings.NewReader(tt.input), "opencode")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractActivity_UnknownAgentType(t *testing.T) {
	got := ExtractActivity(strings.NewReader(`{"type":"assistant"}`), "unknown-agent")
	if got != "" {
		t.Errorf("got %q, want empty for unknown agent type", got)
	}
}

func TestShortenPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/home/user/project/src/main.go", "src/main.go"},
		{"/single.go", "single.go"},
		{"relative.go", "relative.go"},
		{"/a/b/c/d/e.go", "d/e.go"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shortenPath(tt.input)
			if got != tt.want {
				t.Errorf("shortenPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateActivity(t *testing.T) {
	short := "hello"
	got := truncateActivity(short, 10)
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}

	long := strings.Repeat("a", 60)
	got = truncateActivity(long, 50)
	if len([]rune(got)) != 53 { // 50 + "..."
		t.Errorf("truncated length = %d, want 53", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("got %q, want ... suffix", got)
	}

	// newlines collapsed
	got = truncateActivity("line1\nline2", 50)
	if got != "line1 line2" {
		t.Errorf("got %q, want %q", got, "line1 line2")
	}
}

func TestClaudeToolActivity_NoInput(t *testing.T) {
	got := claudeToolActivity("Read", nil)
	if got != "Reading file..." {
		t.Errorf("got %q, want %q", got, "Reading file...")
	}
}

func TestClaudeToolActivity_EmptyToolName(t *testing.T) {
	got := claudeToolActivity("", nil)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
