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
	markerStart     = "---AXON_OUTPUTS_START---"
	markerEnd       = "---AXON_OUTPUTS_END---"
	agentOutputFile = "/tmp/agent-output.jsonl"
	commandTimeout  = 30 * time.Second
)

// Run captures deterministic outputs (branch, commit, PRs, token usage) from
// the workspace and emits them between markers to stdout. Returns 0 on success.
func Run() int {
	outputs := captureOutputs(realRunner{}, agentOutputFile)
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

func captureOutputs(r runner, usageFile string) []string {
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

	if base := os.Getenv("AXON_BASE_BRANCH"); base != "" {
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

	agentType := os.Getenv("AXON_AGENT_TYPE")
	usage := ParseUsage(agentType, usageFile)
	for _, key := range []string{"cost-usd", "input-tokens", "output-tokens"} {
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
	output, err := r.run("gh", "pr", "list", "--head", branch, "--json", "url")
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
