package main

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
)

func TestReportingAnnotationPredicate_Create(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{name: "reporting enabled", annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"}, want: true},
		{name: "checks enabled", annotations: map[string]string{reporting.AnnotationGitHubChecks: "enabled"}, want: true},
		{name: "both enabled", annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled", reporting.AnnotationGitHubChecks: "enabled"}, want: true},
		{name: "reporting disabled value", annotations: map[string]string{reporting.AnnotationGitHubReporting: "disabled"}, want: false},
		{name: "missing annotation", annotations: nil, want: false},
		{name: "unrelated annotations only", annotations: map[string]string{"other": "value"}, want: false},
	}

	pred := reportingAnnotationPredicate{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			if got := pred.Create(event.CreateEvent{Object: task}); got != tt.want {
				t.Errorf("Create(%v) = %v, want %v", tt.annotations, got, tt.want)
			}
		})
	}
}

func TestReportingAnnotationPredicate_Update(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		oldPhase    kelos.TaskPhase
		newPhase    kelos.TaskPhase
		want        bool
	}{
		{
			name:        "enabled, phase changed",
			annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"},
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseRunning,
			want:        true,
		},
		{
			name:        "enabled, phase unchanged",
			annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"},
			oldPhase:    kelos.TaskPhaseRunning,
			newPhase:    kelos.TaskPhaseRunning,
			want:        false,
		},
		{
			name:        "checks only, phase changed",
			annotations: map[string]string{reporting.AnnotationGitHubChecks: "enabled"},
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseRunning,
			want:        true,
		},
		{
			name:        "missing annotation, phase changed",
			annotations: nil,
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseSucceeded,
			want:        false,
		},
	}

	pred := reportingAnnotationPredicate{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldTask := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Status:     kelos.TaskStatus{Phase: tt.oldPhase},
			}
			newTask := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Status:     kelos.TaskStatus{Phase: tt.newPhase},
			}
			if got := pred.Update(event.UpdateEvent{ObjectOld: oldTask, ObjectNew: newTask}); got != tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}
