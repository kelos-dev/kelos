package cli

import (
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestDetailFlagRegistered(t *testing.T) {
	root := NewRootCommand()

	tests := []struct {
		name string
		path []string
	}{
		{"get task", []string{"get", "task"}},
		{"get taskspawner", []string{"get", "taskspawner"}},
		{"get workspace", []string{"get", "workspace"}},
		{"get agentconfig", []string{"get", "agentconfig"}},
		{"get workerpool", []string{"get", "workerpool"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := findSubcommand(t, root, tt.path)
			f := cmd.Flags().Lookup("detail")
			if f == nil {
				t.Fatalf("expected --detail flag on %q", tt.name)
			}
			if f.Shorthand != "d" {
				t.Errorf("expected shorthand -d, got %q", f.Shorthand)
			}
			if f.DefValue != "false" {
				t.Errorf("expected default value false, got %q", f.DefValue)
			}
		})
	}
}

func TestPhaseFlagRegistered(t *testing.T) {
	root := NewRootCommand()
	cmd := findSubcommand(t, root, []string{"get", "task"})
	f := cmd.Flags().Lookup("phase")
	if f == nil {
		t.Fatal("expected --phase flag on get task")
	}
	if f.DefValue != "[]" {
		t.Errorf("expected default value [], got %q", f.DefValue)
	}
}

func TestValidatePhases(t *testing.T) {
	tests := []struct {
		name    string
		phases  []string
		wantErr bool
	}{
		{"valid single phase", []string{"Running"}, false},
		{"valid multiple phases", []string{"Pending", "Running", "Waiting"}, false},
		{"all valid phases", []string{"Pending", "Running", "Waiting", "Succeeded", "Failed"}, false},
		{"empty phases", nil, false},
		{"invalid phase", []string{"Unknown"}, true},
		{"mixed valid and invalid", []string{"Running", "Invalid"}, true},
		{"lowercase rejected", []string{"running"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePhases(tt.phases)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePhases(%v) error = %v, wantErr %v", tt.phases, err, tt.wantErr)
			}
		})
	}
}

func TestFilterTasksByPhase(t *testing.T) {
	tasks := []kelos.Task{
		{Status: kelos.TaskStatus{Phase: kelos.TaskPhasePending}},
		{Status: kelos.TaskStatus{Phase: kelos.TaskPhaseRunning}},
		{Status: kelos.TaskStatus{Phase: kelos.TaskPhaseSucceeded}},
		{Status: kelos.TaskStatus{Phase: kelos.TaskPhaseFailed}},
		{Status: kelos.TaskStatus{Phase: kelos.TaskPhaseWaiting}},
	}

	tests := []struct {
		name       string
		phases     []string
		wantCount  int
		wantPhases []kelos.TaskPhase
	}{
		{
			name:       "filter Running only",
			phases:     []string{"Running"},
			wantCount:  1,
			wantPhases: []kelos.TaskPhase{kelos.TaskPhaseRunning},
		},
		{
			name:      "filter non-completed",
			phases:    []string{"Pending", "Running", "Waiting"},
			wantCount: 3,
			wantPhases: []kelos.TaskPhase{
				kelos.TaskPhasePending,
				kelos.TaskPhaseRunning,
				kelos.TaskPhaseWaiting,
			},
		},
		{
			name:      "filter completed",
			phases:    []string{"Succeeded", "Failed"},
			wantCount: 2,
			wantPhases: []kelos.TaskPhase{
				kelos.TaskPhaseSucceeded,
				kelos.TaskPhaseFailed,
			},
		},
		{
			name:      "filter all phases",
			phases:    []string{"Pending", "Running", "Waiting", "Succeeded", "Failed"},
			wantCount: 5,
		},
		{
			name:      "no matching phase",
			phases:    []string{"Succeeded"},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterTasksByPhase(tasks, tt.phases)
			if len(result) != tt.wantCount {
				t.Errorf("filterTasksByPhase() returned %d tasks, want %d", len(result), tt.wantCount)
			}
			if tt.wantPhases != nil {
				for i, want := range tt.wantPhases {
					if i >= len(result) {
						break
					}
					if result[i].Status.Phase != want {
						t.Errorf("result[%d].Status.Phase = %q, want %q", i, result[i].Status.Phase, want)
					}
				}
			}
		})
	}
}
