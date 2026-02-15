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

	usageFile := writeTempFile(t, `{"type":"result","total_cost_usd":0.05,"usage":{"input_tokens":1000,"output_tokens":500}}`)
	t.Setenv("AXON_AGENT_TYPE", "claude-code")
	t.Setenv("AXON_BASE_BRANCH", "")

	outputs := captureOutputs(r, usageFile)

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

	t.Setenv("AXON_BASE_BRANCH", "develop")
	t.Setenv("AXON_AGENT_TYPE", "")

	outputs := captureOutputs(r, "/nonexistent")

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

	t.Setenv("AXON_BASE_BRANCH", "")
	t.Setenv("AXON_AGENT_TYPE", "")

	outputs := captureOutputs(r, "/nonexistent")

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

	t.Setenv("AXON_BASE_BRANCH", "")
	t.Setenv("AXON_AGENT_TYPE", "")

	outputs := captureOutputs(r, "/nonexistent")

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

	t.Setenv("AXON_BASE_BRANCH", "")
	t.Setenv("AXON_AGENT_TYPE", "")

	outputs := captureOutputs(r, "/nonexistent")

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

	t.Setenv("AXON_BASE_BRANCH", "")
	t.Setenv("AXON_AGENT_TYPE", "")

	outputs := captureOutputs(r, "/nonexistent")

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

	t.Setenv("AXON_BASE_BRANCH", "")
	t.Setenv("AXON_AGENT_TYPE", "")

	outputs := captureOutputs(r, "/nonexistent")

	if len(outputs) == 0 {
		t.Fatal("expected non-empty outputs")
	}
	// Markers should NOT be in the output slice; Run() adds them when printing.
	for _, line := range outputs {
		if strings.Contains(line, "AXON_OUTPUTS") {
			t.Errorf("markers should not be in output slice: %s", line)
		}
	}
}

func TestCaptureOutputsNoMarkersWhenEmpty(t *testing.T) {
	r := mockRunner{commands: map[string]mockResult{
		"git rev-parse --is-inside-work-tree": {err: fmt.Errorf("not a git repo")},
	}}

	t.Setenv("AXON_BASE_BRANCH", "")
	t.Setenv("AXON_AGENT_TYPE", "")

	outputs := captureOutputs(r, "/nonexistent")

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

func TestMain(m *testing.M) {
	// Ensure env vars don't leak between tests by clearing them.
	os.Unsetenv("AXON_BASE_BRANCH")
	os.Unsetenv("AXON_AGENT_TYPE")
	os.Exit(m.Run())
}
