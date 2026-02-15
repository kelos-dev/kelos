package controller

import (
	"reflect"
	"testing"
)

func TestParseOutputs(t *testing.T) {
	tests := []struct {
		name     string
		logData  string
		expected []string
	}{
		{
			name:     "no markers",
			logData:  "some random log output\nmore logs\n",
			expected: nil,
		},
		{
			name:     "empty between markers",
			logData:  "---AXON_OUTPUTS_START---\n---AXON_OUTPUTS_END---\n",
			expected: nil,
		},
		{
			name:     "whitespace only between markers",
			logData:  "---AXON_OUTPUTS_START---\n  \n  \n---AXON_OUTPUTS_END---\n",
			expected: nil,
		},
		{
			name:     "branch only",
			logData:  "---AXON_OUTPUTS_START---\nbranch: axon-task-123\n---AXON_OUTPUTS_END---\n",
			expected: []string{"branch: axon-task-123"},
		},
		{
			name: "branch and PR URL",
			logData: "---AXON_OUTPUTS_START---\nbranch: axon-task-123\n" +
				"pr: https://github.com/org/repo/pull/456\n---AXON_OUTPUTS_END---\n",
			expected: []string{
				"branch: axon-task-123",
				"pr: https://github.com/org/repo/pull/456",
			},
		},
		{
			name: "branch and multiple PR URLs",
			logData: "---AXON_OUTPUTS_START---\nbranch: feature-branch\n" +
				"pr: https://github.com/org/repo/pull/1\n" +
				"pr: https://github.com/org/repo/pull/2\n---AXON_OUTPUTS_END---\n",
			expected: []string{
				"branch: feature-branch",
				"pr: https://github.com/org/repo/pull/1",
				"pr: https://github.com/org/repo/pull/2",
			},
		},
		{
			name: "markers in noisy log data",
			logData: "Starting agent...\nProcessing task...\nDone.\n" +
				"---AXON_OUTPUTS_START---\nbranch: my-branch\n" +
				"pr: https://github.com/org/repo/pull/99\n---AXON_OUTPUTS_END---\n" +
				"Exiting with code 0\n",
			expected: []string{
				"branch: my-branch",
				"pr: https://github.com/org/repo/pull/99",
			},
		},
		{
			name: "all output keys including commit, base-branch, and usage",
			logData: "---AXON_OUTPUTS_START---\nbranch: axon-task-42\n" +
				"pr: https://github.com/org/repo/pull/42\n" +
				"commit: abc123def456\n" +
				"base-branch: main\n" +
				"input-tokens: 1500\n" +
				"output-tokens: 800\n" +
				"cost-usd: 0.025\n---AXON_OUTPUTS_END---\n",
			expected: []string{
				"branch: axon-task-42",
				"pr: https://github.com/org/repo/pull/42",
				"commit: abc123def456",
				"base-branch: main",
				"input-tokens: 1500",
				"output-tokens: 800",
				"cost-usd: 0.025",
			},
		},
		{
			name:     "start marker without end marker",
			logData:  "---AXON_OUTPUTS_START---\nbranch: broken\n",
			expected: nil,
		},
		{
			name:     "end marker before start marker",
			logData:  "---AXON_OUTPUTS_END---\n---AXON_OUTPUTS_START---\nbranch: wrong-order\n",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseOutputs(tt.logData)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d items, got %d: %v", len(tt.expected), len(result), result)
				return
			}
			for i, v := range tt.expected {
				if result[i] != v {
					t.Errorf("item %d: expected %q, got %q", i, v, result[i])
				}
			}
		})
	}
}

func TestResultsFromOutputs(t *testing.T) {
	tests := []struct {
		name     string
		outputs  []string
		expected map[string]string
	}{
		{
			name:     "nil outputs",
			outputs:  nil,
			expected: nil,
		},
		{
			name:     "empty outputs",
			outputs:  []string{},
			expected: nil,
		},
		{
			name:    "branch only",
			outputs: []string{"branch: feature-123"},
			expected: map[string]string{
				"branch": "feature-123",
			},
		},
		{
			name:    "branch and pr",
			outputs: []string{"branch: feature-123", "pr: https://github.com/org/repo/pull/1"},
			expected: map[string]string{
				"branch": "feature-123",
				"pr":     "https://github.com/org/repo/pull/1",
			},
		},
		{
			name:    "duplicate keys use last value",
			outputs: []string{"pr: https://github.com/org/repo/pull/1", "pr: https://github.com/org/repo/pull/2"},
			expected: map[string]string{
				"pr": "https://github.com/org/repo/pull/2",
			},
		},
		{
			name:     "lines without colon-space are skipped",
			outputs:  []string{"no-separator-here"},
			expected: nil,
		},
		{
			name:    "mixed valid and invalid lines",
			outputs: []string{"branch: main", "bare-line", "pr: https://example.com/pull/1"},
			expected: map[string]string{
				"branch": "main",
				"pr":     "https://example.com/pull/1",
			},
		},
		{
			name:    "value contains colon-space",
			outputs: []string{"message: hello: world"},
			expected: map[string]string{
				"message": "hello: world",
			},
		},
		{
			name: "new output keys in results map",
			outputs: []string{
				"branch: axon-task-42",
				"commit: abc123def456",
				"base-branch: main",
				"input-tokens: 1500",
				"output-tokens: 800",
				"cost-usd: 0.025",
			},
			expected: map[string]string{
				"branch":        "axon-task-42",
				"commit":        "abc123def456",
				"base-branch":   "main",
				"input-tokens":  "1500",
				"output-tokens": "800",
				"cost-usd":      "0.025",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResultsFromOutputs(tt.outputs)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}
