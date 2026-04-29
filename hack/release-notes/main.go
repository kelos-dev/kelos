/*
Copyright 2026 Gunju Kim

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// release-notes generates categorized release notes from merged PR
// descriptions between two release tags.
//
// Usage: release-notes <version-tag>
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// category maps a kind/* label to its display heading.
type category struct {
	Label   string
	Heading string
}

var categories = []category{
	{Label: "kind/api", Heading: "API Changes"},
	{Label: "kind/feature", Heading: "Features"},
	{Label: "kind/bug", Heading: "Bug Fixes"},
	{Label: "kind/docs", Heading: "Documentation"},
}

// prData mirrors the JSON returned by `gh pr view --json author,body,labels`.
type prData struct {
	Author prAuthor  `json:"author"`
	Body   string    `json:"body"`
	Labels []prLabel `json:"labels"`
}

type prAuthor struct {
	Login string `json:"login"`
}

type prLabel struct {
	Name string `json:"name"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: release-notes <version-tag>")
		os.Exit(1)
	}
	version := os.Args[1]

	previousTag, err := findPreviousTag(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Generating release notes for %s (since %s)\n", version, previousTag)

	prNumbers, err := collectPRs(previousTag, version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if len(prNumbers) == 0 {
		fmt.Fprintf(os.Stderr, "No merged PRs found between %s and %s\n", previousTag, version)
		fmt.Println("No notable changes.")
		return
	}

	categoryNotes := make(map[string][]string) // label -> formatted lines
	var otherNotes []string

	for _, pr := range prNumbers {
		data, err := fetchPR(pr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: Could not fetch PR #%s: %v\n", pr, err)
			continue
		}

		note := ExtractNote(data.Body)
		if note == "" || IsNone(note) {
			continue
		}

		labelSet := make(map[string]bool)
		for _, l := range data.Labels {
			labelSet[l.Name] = true
		}

		formatted := formatNote(note, pr, data.Author.Login)
		matched := false
		for _, cat := range categories {
			if labelSet[cat.Label] {
				categoryNotes[cat.Label] = append(categoryNotes[cat.Label], formatted...)
				matched = true
				break
			}
		}
		if !matched {
			otherNotes = append(otherNotes, formatted...)
		}
	}

	hasOutput := false
	for _, cat := range categories {
		lines := categoryNotes[cat.Label]
		if len(lines) == 0 {
			continue
		}
		fmt.Printf("## %s\n\n", cat.Heading)
		for _, l := range lines {
			fmt.Println(l)
		}
		fmt.Println()
		hasOutput = true
	}

	if len(otherNotes) > 0 {
		fmt.Println("## Other Changes")
		fmt.Println()
		for _, l := range otherNotes {
			fmt.Println(l)
		}
		fmt.Println()
		hasOutput = true
	}

	if !hasOutput {
		fmt.Println("No notable changes.")
	}
}

// findPreviousTag returns the release tag immediately before version.
func findPreviousTag(version string) (string, error) {
	out, err := gitOutput("tag", "--list", "v*", "--sort=-version:refname")
	if err != nil {
		return "", fmt.Errorf("listing tags: %w", err)
	}
	found := false
	for _, tag := range strings.Split(strings.TrimSpace(out), "\n") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if tag == version {
			found = true
			continue
		}
		if found {
			return tag, nil
		}
	}
	return "", fmt.Errorf("could not determine previous release tag")
}

// collectPRs returns PR numbers merged between previousTag and version by
// parsing merge commit messages from git history.
func collectPRs(previousTag, version string) ([]string, error) {
	out, err := gitOutput("log", previousTag+".."+version, "--merges", "--oneline")
	if err != nil {
		return nil, fmt.Errorf("listing merge commits: %w", err)
	}
	return parsePRNumbers(out), nil
}

var mergePRRe = regexp.MustCompile(`Merge pull request #(\d+)`)

// parsePRNumbers extracts PR numbers from git log --merges --oneline output.
func parsePRNumbers(gitLogOutput string) []string {
	var numbers []string
	for _, line := range strings.Split(gitLogOutput, "\n") {
		if m := mergePRRe.FindStringSubmatch(line); m != nil {
			numbers = append(numbers, m[1])
		}
	}
	return numbers
}

// fetchPR retrieves the author, body, and labels of a PR via the GitHub CLI.
func fetchPR(number string) (*prData, error) {
	out, err := runCommand("gh", "pr", "view", number, "--json", "author,body,labels")
	if err != nil {
		return nil, err
	}
	var data prData
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, fmt.Errorf("parsing PR JSON: %w", err)
	}
	return &data, nil
}

// ExtractNote extracts the content of the ```release-note``` fenced block.
// Blank lines are stripped. Returns empty string if no block is found.
func ExtractNote(body string) string {
	lines := strings.Split(body, "\n")
	var inBlock bool
	var result []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "```release-note" {
			inBlock = true
			continue
		}
		if inBlock && strings.TrimSpace(line) == "```" {
			break
		}
		if inBlock {
			if strings.TrimSpace(line) != "" {
				result = append(result, line)
			}
		}
	}
	return strings.Join(result, "\n")
}

// IsNone returns true if the note content is the word "NONE" (case-insensitive).
func IsNone(note string) bool {
	return strings.EqualFold(strings.TrimSpace(note), "none")
}

// formatNote formats a release note with a PR reference and author handle.
func formatNote(note, pr, author string) []string {
	suffix := fmt.Sprintf("(#%s", pr)
	if author != "" {
		suffix += ", @" + author
	}
	suffix += ")"
	var lines []string
	for _, line := range strings.Split(note, "\n") {
		if line != "" {
			lines = append(lines, fmt.Sprintf("- %s %s", line, suffix))
		}
	}
	return lines
}

func gitOutput(args ...string) (string, error) {
	return runCommand("git", args...)
}

func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%s %v: %w: %s", name, args, err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("%s %v: %w", name, args, err)
	}
	return string(out), nil
}
