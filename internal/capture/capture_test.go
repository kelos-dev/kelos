package capture

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// mockRunner returns predefined outputs for specific commands.
type mockRunner struct {
	commands map[string]mockResult
}

type mockResult struct {
	output string
	err    error
}

func (m mockRunner) run(name string, args ...string) (string, error) {
	key := name + " " + strings.Join(args, " ")
	if r, ok := m.commands[key]; ok {
		return r.output, r.err
	}
	return "", fmt.Errorf("command not mocked: %s", key)
}

func TestCaptureOutputsFullFlow(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree": {output: "true"},
		"git branch --show-current":           {output: "feature/test-branch"},
		"gh pr list --head feature/test-branch --json url": {
			output: `[{"url":"https://github.com/org/repo/pull/42"}]`,
		},
		"git rev-parse HEAD":                        {output: "abc1234def5678"},
		"git symbolic-ref refs/remotes/origin/HEAD": {output: "refs/remotes/origin/main"},
	}}

	usage := map[string]string{
		"cost-usd":      "0.05",
		"input-tokens":  "1000",
		"output-tokens": "500",
	}
	t.Setenv("KELOS_BASE_BRANCH", "")

	outputs := captureOutputs(r, usage)

	expected := []string{
		"branch: feature/test-branch",
		"pr: https://github.com/org/repo/pull/42",
		"commit: abc1234def5678",
		"base-branch: main",
		"cost-usd: 0.05",
		"input-tokens: 1000",
		"output-tokens: 500",
	}
	assertOutputLines(t, expected, outputs)
}

func TestCaptureOutputsBaseBranchFromEnv(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree": {output: "true"},
		"git branch --show-current":           {output: "my-branch"},
		"gh pr list --head my-branch --json url": {
			output: `[]`,
		},
		"git rev-parse HEAD": {output: "deadbeef"},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "develop")

	outputs := captureOutputs(r, nil)

	expected := []string{
		"branch: my-branch",
		"commit: deadbeef",
		"base-branch: develop",
	}
	assertOutputLines(t, expected, outputs)
}

func TestCaptureOutputsNotGitRepo(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree": {err: fmt.Errorf("not a git repo")},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "")

	outputs := captureOutputs(r, nil)

	if len(outputs) != 0 {
		t.Errorf("expected no outputs outside git repo, got %v", outputs)
	}
}

func TestCaptureOutputsMultiplePRs(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree": {output: "true"},
		"git branch --show-current":           {output: "feature"},
		"gh pr list --head feature --json url": {
			output: `[{"url":"https://github.com/org/repo/pull/1"},{"url":"https://github.com/org/repo/pull/2"}]`,
		},
		"git rev-parse HEAD":                        {output: "aaa"},
		"git symbolic-ref refs/remotes/origin/HEAD": {output: "refs/remotes/origin/main"},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "")

	outputs := captureOutputs(r, nil)

	expected := []string{
		"branch: feature",
		"pr: https://github.com/org/repo/pull/1",
		"pr: https://github.com/org/repo/pull/2",
		"commit: aaa",
		"base-branch: main",
	}
	assertOutputLines(t, expected, outputs)
}

func TestCaptureOutputsDetachedHead(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree":       {output: "true"},
		"git branch --show-current":                 {output: ""},
		"git rev-parse HEAD":                        {output: "abc123"},
		"git symbolic-ref refs/remotes/origin/HEAD": {output: "refs/remotes/origin/main"},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "")

	outputs := captureOutputs(r, nil)

	expected := []string{
		"commit: abc123",
		"base-branch: main",
	}
	assertOutputLines(t, expected, outputs)
}

func TestCaptureOutputsGhFails(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree":       {output: "true"},
		"git branch --show-current":                 {output: "branch"},
		"gh pr list --head branch --json url":       {err: fmt.Errorf("gh not found")},
		"git rev-parse HEAD":                        {output: "abc"},
		"git symbolic-ref refs/remotes/origin/HEAD": {output: "refs/remotes/origin/main"},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "")

	outputs := captureOutputs(r, nil)

	expected := []string{
		"branch: branch",
		"commit: abc",
		"base-branch: main",
	}
	assertOutputLines(t, expected, outputs)
}

func TestCaptureOutputsMarkers(t *testing.T) {
	// Verify that Run() would emit markers by checking captureOutputs returns non-empty.
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree":       {output: "true"},
		"git branch --show-current":                 {output: "test"},
		"gh pr list --head test --json url":         {output: "[]"},
		"git rev-parse HEAD":                        {output: "abc"},
		"git symbolic-ref refs/remotes/origin/HEAD": {err: fmt.Errorf("no remote")},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "")

	outputs := captureOutputs(r, nil)

	if len(outputs) == 0 {
		t.Fatal("expected non-empty outputs")
	}
	// Markers should NOT be in the output slice; Run() adds them when printing.
	for _, line := range outputs {
		if strings.Contains(line, "KELOS_OUTPUTS") {
			t.Errorf("markers should not be in output slice: %s", line)
		}
	}
}

func TestCaptureOutputsNoMarkersWhenEmpty(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree": {err: fmt.Errorf("not a git repo")},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "")

	outputs := captureOutputs(r, nil)

	if len(outputs) != 0 {
		t.Errorf("expected empty outputs, got %v", outputs)
	}
}

func TestCapturePRsInvalidJSON(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"gh pr list --head branch --json url": {output: "not json"},
	}}

	prs := capturePRs(r, "branch")
	if prs != nil {
		t.Errorf("expected nil for invalid JSON, got %v", prs)
	}
}

func assertOutputLines(t *testing.T, expected, got []string) {
	t.Helper()
	if len(expected) != len(got) {
		t.Errorf("output line count mismatch: want %d, got %d\n  want: %v\n  got:  %v",
			len(expected), len(got), expected, got)
		return
	}
	for i, want := range expected {
		if got[i] != want {
			t.Errorf("line %d: want %q, got %q", i, want, got[i])
		}
	}
}

func TestCaptureOutputsWithUpstreamRepo(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree": {output: "true"},
		"git branch --show-current":           {output: "fix-branch"},
		"gh pr list --head fix-branch --json url": {
			output: `[{"url":"https://github.com/my-fork/repo/pull/1"}]`,
		},
		"gh pr list --head fix-branch --json url --repo upstream-org/repo": {
			output: `[{"url":"https://github.com/upstream-org/repo/pull/99"}]`,
		},
		"git rev-parse HEAD":                        {output: "abc123"},
		"git symbolic-ref refs/remotes/origin/HEAD": {output: "refs/remotes/origin/main"},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "")
	t.Setenv("KELOS_UPSTREAM_REPO", "upstream-org/repo")

	outputs := captureOutputs(r, nil)

	expected := []string{
		"branch: fix-branch",
		"pr: https://github.com/my-fork/repo/pull/1",
		"pr: https://github.com/upstream-org/repo/pull/99",
		"commit: abc123",
		"base-branch: main",
	}
	assertOutputLines(t, expected, outputs)
}

func TestCaptureOutputsUpstreamRepoNoPRs(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree": {output: "true"},
		"git branch --show-current":           {output: "fix-branch"},
		"gh pr list --head fix-branch --json url": {
			output: `[]`,
		},
		"gh pr list --head fix-branch --json url --repo upstream-org/repo": {
			output: `[]`,
		},
		"git rev-parse HEAD":                        {output: "abc123"},
		"git symbolic-ref refs/remotes/origin/HEAD": {output: "refs/remotes/origin/main"},
	}}

	t.Setenv("KELOS_BASE_BRANCH", "")
	t.Setenv("KELOS_UPSTREAM_REPO", "upstream-org/repo")

	outputs := captureOutputs(r, nil)

	expected := []string{
		"branch: fix-branch",
		"commit: abc123",
		"base-branch: main",
	}
	assertOutputLines(t, expected, outputs)
}

func TestParseOutputs(t *testing.T) {
	tests := []struct {
		name     string
		logData  string
		expected []string
	}{
		{
			name:     "no markers",
			logData:  "some random log data",
			expected: nil,
		},
		{
			name:     "valid block",
			logData:  "prefix\n---KELOS_OUTPUTS_START---\nbranch: main\ncommit: abc\n---KELOS_OUTPUTS_END---\nsuffix",
			expected: []string{"branch: main", "commit: abc"},
		},
		{
			name:     "prior end marker before start marker",
			logData:  "---KELOS_OUTPUTS_END---\ngarbage\n---KELOS_OUTPUTS_START---\nbranch: feature\n---KELOS_OUTPUTS_END---\n",
			expected: []string{"branch: feature"},
		},
		{
			name:     "start marker without end marker",
			logData:  "---KELOS_OUTPUTS_START---\nbranch: main\n",
			expected: nil,
		},
		{
			name:     "empty block",
			logData:  "---KELOS_OUTPUTS_START---\n---KELOS_OUTPUTS_END---",
			expected: nil,
		},
		{
			name:     "whitespace only block",
			logData:  "---KELOS_OUTPUTS_START---\n   \n  \n---KELOS_OUTPUTS_END---",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseOutputs(tt.logData)
			if tt.expected == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.expected) {
				t.Fatalf("length mismatch: want %d, got %d\n  want: %v\n  got:  %v", len(tt.expected), len(got), tt.expected, got)
			}
			for i := range tt.expected {
				if got[i] != tt.expected[i] {
					t.Errorf("line %d: want %q, got %q", i, tt.expected[i], got[i])
				}
			}
		})
	}
}

func TestMain(m *testing.M) {
	// Ensure env vars don't leak between tests by clearing them.
	os.Unsetenv("KELOS_BASE_BRANCH")
	os.Unsetenv("KELOS_AGENT_TYPE")
	os.Unsetenv("KELOS_UPSTREAM_REPO")
	os.Exit(m.Run())
}
