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

package main

import (
	"testing"
)

func TestParsePRNumbers(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want []string
	}{
		{
			name: "standard merge commits",
			log:  "abc1234 Merge pull request #523 from org/feature-branch\ndef5678 Merge pull request #525 from org/bugfix-branch",
			want: []string{"523", "525"},
		},
		{
			name: "mixed merge and non-merge commits",
			log:  "abc1234 Merge pull request #100 from org/branch\ndef5678 Some regular commit\nghi9012 Merge pull request #200 from org/other",
			want: []string{"100", "200"},
		},
		{
			name: "no merge commits",
			log:  "abc1234 Some commit\ndef5678 Another commit",
			want: nil,
		},
		{
			name: "empty output",
			log:  "",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePRNumbers(tt.log)
			if len(got) != len(tt.want) {
				t.Fatalf("parsePRNumbers() returned %d items, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parsePRNumbers()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractNote(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "standard release note",
			body: "## Description\nSome description here.\n\n## Release Note\n\n```release-note\nAdded support for configuring agent timeout\n```",
			want: "Added support for configuring agent timeout",
		},
		{
			name: "NONE release note",
			body: "## Release Note\n\n```release-note\nNONE\n```",
			want: "NONE",
		},
		{
			name: "multi-line release note",
			body: "```release-note\nFixed a bug where session cleanup would skip errored pods\nImproved error logging for failed sessions\n```",
			want: "Fixed a bug where session cleanup would skip errored pods\nImproved error logging for failed sessions",
		},
		{
			name: "missing release note block",
			body: "## Description\nJust a description, no release note block.",
			want: "",
		},
		{
			name: "empty release note block",
			body: "```release-note\n\n```",
			want: "",
		},
		{
			name: "release note with surrounding whitespace lines",
			body: "```release-note\n\nAdded a new feature\n\n```",
			want: "Added a new feature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractNote(tt.body)
			if got != tt.want {
				t.Errorf("ExtractNote() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsNone(t *testing.T) {
	tests := []struct {
		name string
		note string
		want bool
	}{
		{name: "uppercase NONE", note: "NONE", want: true},
		{name: "lowercase none", note: "none", want: true},
		{name: "mixed case None", note: "None", want: true},
		{name: "with whitespace", note: "  NONE  ", want: true},
		{name: "actual content", note: "Added support for configuring agent timeout", want: false},
		{name: "empty string", note: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNone(tt.note)
			if got != tt.want {
				t.Errorf("IsNone(%q) = %v, want %v", tt.note, got, tt.want)
			}
		})
	}
}

func TestFormatNote(t *testing.T) {
	tests := []struct {
		name   string
		note   string
		pr     string
		author string
		want   []string
	}{
		{
			name:   "single line with author",
			note:   "Added a feature",
			pr:     "42",
			author: "octocat",
			want:   []string{"- Added a feature (#42, @octocat)"},
		},
		{
			name:   "multi-line with author",
			note:   "Fixed bug A\nFixed bug B",
			pr:     "99",
			author: "octocat",
			want:   []string{"- Fixed bug A (#99, @octocat)", "- Fixed bug B (#99, @octocat)"},
		},
		{
			name:   "missing author falls back to PR only",
			note:   "Added a feature",
			pr:     "42",
			author: "",
			want:   []string{"- Added a feature (#42)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatNote(tt.note, tt.pr, tt.author)
			if len(got) != len(tt.want) {
				t.Fatalf("formatNote() returned %d lines, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("formatNote()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
