package main

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
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
			task := &kelosv1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
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
		oldPhase    kelosv1alpha1.TaskPhase
		newPhase    kelosv1alpha1.TaskPhase
		want        bool
	}{
		{
			name:        "enabled, phase changed",
			annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"},
			oldPhase:    kelosv1alpha1.TaskPhasePending,
			newPhase:    kelosv1alpha1.TaskPhaseRunning,
			want:        true,
		},
		{
			name:        "enabled, phase unchanged",
			annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"},
			oldPhase:    kelosv1alpha1.TaskPhaseRunning,
			newPhase:    kelosv1alpha1.TaskPhaseRunning,
			want:        false,
		},
		{
			name:        "checks only, phase changed",
			annotations: map[string]string{reporting.AnnotationGitHubChecks: "enabled"},
			oldPhase:    kelosv1alpha1.TaskPhasePending,
			newPhase:    kelosv1alpha1.TaskPhaseRunning,
			want:        true,
		},
		{
			name:        "missing annotation, phase changed",
			annotations: nil,
			oldPhase:    kelosv1alpha1.TaskPhasePending,
			newPhase:    kelosv1alpha1.TaskPhaseSucceeded,
			want:        false,
		},
	}

	pred := reportingAnnotationPredicate{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldTask := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Status:     kelosv1alpha1.TaskStatus{Phase: tt.oldPhase},
			}
			newTask := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Status:     kelosv1alpha1.TaskStatus{Phase: tt.newPhase},
			}
			if got := pred.Update(event.UpdateEvent{ObjectOld: oldTask, ObjectNew: newTask}); got != tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}
