package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func int32Ptr(i int32) *int32 { return &i }

func TestWebhookEventTTLExpired(t *testing.T) {
	now := metav1.Now()
	pastTime := metav1.NewTime(now.Add(-2 * time.Hour))
	futureTime := metav1.NewTime(now.Add(1 * time.Hour))
	longAgo := metav1.NewTime(now.Add(-24 * time.Hour))
	recentlyReceived := metav1.NewTime(now.Add(-30 * time.Minute))

	tests := []struct {
		name           string
		event          *kelosv1alpha1.WebhookEvent
		wantExpired    bool
		wantRequeueGt0 bool
	}{
		{
			name: "No TTL set",
			event: &kelosv1alpha1.WebhookEvent{
				Status: kelosv1alpha1.WebhookEventStatus{
					ProcessedAt: &now,
				},
			},
			wantExpired:    false,
			wantRequeueGt0: false,
		},
		{
			name: "Processed TTL expired",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(3600),
				},
				Status: kelosv1alpha1.WebhookEventStatus{
					ProcessedAt: &pastTime,
				},
			},
			wantExpired:    true,
			wantRequeueGt0: false,
		},
		{
			name: "Processed TTL not yet expired",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(3600),
				},
				Status: kelosv1alpha1.WebhookEventStatus{
					ProcessedAt: &futureTime,
				},
			},
			wantExpired:    false,
			wantRequeueGt0: true,
		},
		{
			name: "Zero TTL expires immediately",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(0),
				},
				Status: kelosv1alpha1.WebhookEventStatus{
					ProcessedAt: &now,
				},
			},
			wantExpired:    true,
			wantRequeueGt0: false,
		},
		{
			name: "Unprocessed event exceeds fallback TTL",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(3600),
					ReceivedAt:               longAgo,
				},
			},
			wantExpired:    true,
			wantRequeueGt0: false,
		},
		{
			name: "Unprocessed event within fallback TTL",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(3600),
					ReceivedAt:               recentlyReceived,
				},
			},
			wantExpired:    false,
			wantRequeueGt0: true,
		},
		{
			name: "Unprocessed with zero TTL expires on 4x fallback",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(0),
					ReceivedAt:               now,
				},
			},
			wantExpired:    true,
			wantRequeueGt0: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expired, requeueAfter := webhookEventTTLExpired(tt.event)
			if expired != tt.wantExpired {
				t.Errorf("expired = %v, want %v", expired, tt.wantExpired)
			}
			if tt.wantRequeueGt0 && requeueAfter <= 0 {
				t.Errorf("expected positive requeue duration, got %v", requeueAfter)
			}
			if !tt.wantRequeueGt0 && requeueAfter > 0 {
				t.Errorf("expected zero requeue duration, got %v", requeueAfter)
			}
		})
	}
}

func TestWebhookEventTTLPredicate(t *testing.T) {
	pred := webhookEventTTLPredicate()

	withTTL := &kelosv1alpha1.WebhookEvent{
		Spec: kelosv1alpha1.WebhookEventSpec{
			TTLSecondsAfterProcessed: int32Ptr(7200),
		},
	}
	withoutTTL := &kelosv1alpha1.WebhookEvent{}

	if !pred.Create(event.CreateEvent{Object: withTTL}) {
		t.Error("predicate should accept Create with TTL set")
	}
	if pred.Create(event.CreateEvent{Object: withoutTTL}) {
		t.Error("predicate should reject Create without TTL")
	}
	if !pred.Update(event.UpdateEvent{ObjectNew: withTTL}) {
		t.Error("predicate should accept Update with TTL set")
	}
	if pred.Update(event.UpdateEvent{ObjectNew: withoutTTL}) {
		t.Error("predicate should reject Update without TTL")
	}
	if pred.Delete(event.DeleteEvent{Object: withTTL}) {
		t.Error("predicate should reject Delete events")
	}
}
