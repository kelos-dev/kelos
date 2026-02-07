package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func TestTTLExpired(t *testing.T) {
	r := &TaskReconciler{}

	int32Ptr := func(v int32) *int32 { return &v }
	timePtr := func(t time.Time) *metav1.Time {
		mt := metav1.NewTime(t)
		return &mt
	}

	tests := []struct {
		name            string
		task            *axonv1alpha1.Task
		wantExpired     bool
		wantRequeueMin  time.Duration
		wantRequeueMax  time.Duration
		wantZeroRequeue bool
	}{
		{
			name: "No TTL set",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: nil,
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-10 * time.Second)),
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "Not in terminal phase",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase: axonv1alpha1.TaskPhaseRunning,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "CompletionTime not set",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: nil,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "TTL=0 and completed",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(0),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-1 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL expired for succeeded task",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(10),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-20 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL expired for failed task",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(5),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseFailed,
					CompletionTime: timePtr(time.Now().Add(-10 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL not yet expired",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now()),
				},
			},
			wantExpired:    false,
			wantRequeueMin: 50 * time.Second,
			wantRequeueMax: 61 * time.Second,
		},
		{
			name: "Pending phase with TTL",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(10),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase: axonv1alpha1.TaskPhasePending,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expired, requeueAfter := r.ttlExpired(tt.task)
			if expired != tt.wantExpired {
				t.Errorf("ttlExpired() expired = %v, want %v", expired, tt.wantExpired)
			}
			if tt.wantZeroRequeue {
				if requeueAfter != 0 {
					t.Errorf("ttlExpired() requeueAfter = %v, want 0", requeueAfter)
				}
			} else {
				if requeueAfter < tt.wantRequeueMin || requeueAfter > tt.wantRequeueMax {
					t.Errorf("ttlExpired() requeueAfter = %v, want between %v and %v",
						requeueAfter, tt.wantRequeueMin, tt.wantRequeueMax)
				}
			}
		})
	}
}
