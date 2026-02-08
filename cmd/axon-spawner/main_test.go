package main

import (
	"testing"
	"time"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func TestParsePollInterval(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{name: "empty defaults to 5m", input: "", expected: 5 * time.Minute},
		{name: "valid duration 5m", input: "5m", expected: 5 * time.Minute},
		{name: "valid duration 30s", input: "30s", expected: 30 * time.Second},
		{name: "valid duration 1h", input: "1h", expected: 1 * time.Hour},
		{name: "plain number treated as seconds", input: "60", expected: 60 * time.Second},
		{name: "invalid string defaults to 5m", input: "invalid", expected: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePollInterval(tt.input)
			if got != tt.expected {
				t.Errorf("parsePollInterval(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestPollIntervalFor(t *testing.T) {
	tests := []struct {
		name     string
		ts       *axonv1alpha1.TaskSpawner
		expected time.Duration
	}{
		{
			name: "GitHub source with pollInterval",
			ts: &axonv1alpha1.TaskSpawner{
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
							PollInterval: "10m",
						},
					},
				},
			},
			expected: 10 * time.Minute,
		},
		{
			name: "GitHub source with empty pollInterval defaults to 5m",
			ts: &axonv1alpha1.TaskSpawner{
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{},
					},
				},
			},
			expected: 5 * time.Minute,
		},
		{
			name: "Cron source always returns 1m",
			ts: &axonv1alpha1.TaskSpawner{
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						Cron: &axonv1alpha1.Cron{
							Schedule: "0 9 * * 1",
						},
					},
				},
			},
			expected: 1 * time.Minute,
		},
		{
			name: "No source configured returns 1m (cron fallback)",
			ts: &axonv1alpha1.TaskSpawner{
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{},
				},
			},
			expected: 1 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pollIntervalFor(tt.ts)
			if got != tt.expected {
				t.Errorf("pollIntervalFor() = %v, want %v", got, tt.expected)
			}
		})
	}
}
