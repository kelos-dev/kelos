package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	markerStart    = "---KELOS_OUTPUTS_START---"
	markerEnd      = "---KELOS_OUTPUTS_END---"
	commandTimeout = 30 * time.Second
)

// ParseOutputs extracts output lines from log data between the
// ---KELOS_OUTPUTS_START--- and ---KELOS_OUTPUTS_END--- markers.
func ParseOutputs(logData string) []string {
	startIdx := strings.Index(logData, markerStart)
	if startIdx == -1 {
		return nil
	}
	searchAfter := startIdx + len(markerStart)
	relEndIdx := strings.Index(logData[searchAfter:], markerEnd)
	if relEndIdx == -1 {
		return nil
	}
	endIdx := searchAfter + relEndIdx

	between := logData[startIdx+len(markerStart) : endIdx]
	between = strings.TrimSpace(between)
	if between == "" {
		return nil
	}

	lines := strings.Split(between, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// ResultsFromOutputs builds a key-value map from output lines in "key: value" format.
// Lines that do not contain ": " are skipped. If duplicate keys exist, the last value wins.
func ResultsFromOutputs(outputs []string) map[string]string {
	if len(outputs) == 0 {
		return nil
	}
	var result map[string]string
	for _, line := range outputs {
		key, value, ok := strings.Cut(line, ": ")
		if !ok || key == "" {
			continue
		}
		if result == nil {
			result = make(map[string]string)
		}
		result[key] = value
	}
	return result
}

// Run streams the agent's JSON output from stdin to stdout, accumulating
// per-agent token usage in memory, then emits deterministic outputs
// (branch, commit, PRs, token usage) between markers on stdout. It is
// intended to be the right-hand side of a pipe from the agent process so
// that no on-disk copy of the stream is required.
func Run() int {
	agentType := os.Getenv("KELOS_AGENT_TYPE")
	usage, err := StreamUsage(agentType, os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kelos-capture: error reading agent output: %v\n", err)
	}
	outputs := captureOutputs(realRunner{}, usage)
	if len(outputs) == 0 {
		return 0
	}
	fmt.Println(markerStart)
	for _, line := range outputs {
		fmt.Println(line)
	}
	fmt.Println(markerEnd)
	return 0
}

// runner abstracts command execution for testing.
type runner interface {
	run(name string, args ...string) (string, error)
}

type realRunner struct{}

func (realRunner) run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), err
}

func captureOutputs(r runner, usage map[string]string) []string {
	var outputs []string

	inGitRepo := isGitRepo(r)

	if inGitRepo {
		branch, err := r.run("git", "branch", "--show-current")
		if err == nil && branch != "" {
			outputs = append(outputs, "branch: "+branch)
			outputs = append(outputs, capturePRs(r, branch)...)
		}

		commit, err := r.run("git", "rev-parse", "HEAD")
		if err == nil && commit != "" {
			outputs = append(outputs, "commit: "+commit)
		}
	}

	if base := os.Getenv("KELOS_BASE_BRANCH"); base != "" {
		outputs = append(outputs, "base-branch: "+base)
	} else if inGitRepo {
		ref, err := r.run("git", "symbolic-ref", "refs/remotes/origin/HEAD")
		if err == nil && ref != "" {
			branch := strings.TrimPrefix(ref, "refs/remotes/origin/")
			if branch != "" {
				outputs = append(outputs, "base-branch: "+branch)
			}
		}
	}

	for _, key := range []string{"cost-usd", "input-tokens", "output-tokens", "response"} {
		if v, ok := usage[key]; ok {
			outputs = append(outputs, key+": "+v)
		}
	}

	return outputs
}

func isGitRepo(r runner) bool {
	_, err := r.run("git", "rev-parse", "--is-inside-work-tree")
	return err == nil
}

func capturePRs(r runner, branch string) []string {
	// Check origin repo (current behavior)
	lines := queryPRs(r, branch, "")

	// Also check upstream repo if set (fork workflow)
	if upstreamRepo := os.Getenv("KELOS_UPSTREAM_REPO"); upstreamRepo != "" {
		lines = append(lines, queryPRs(r, branch, upstreamRepo)...)
	}

	return lines
}

func queryPRs(r runner, branch, repo string) []string {
	args := []string{"pr", "list", "--head", branch, "--json", "url"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	output, err := r.run("gh", args...)
	if err != nil || output == "" {
		return nil
	}
	var prs []struct {
		URL string `json:"url"`
	}
	if json.Unmarshal([]byte(output), &prs) != nil {
		return nil
	}
	var lines []string
	for _, pr := range prs {
		if pr.URL != "" {
			lines = append(lines, "pr: "+pr.URL)
		}
	}
	return lines
}
