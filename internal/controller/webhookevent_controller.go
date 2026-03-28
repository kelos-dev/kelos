package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// unprocessedTTLMultiplier is the multiplier applied to the configured TTL
// for events that were never processed. This ensures stuck events are
// eventually cleaned up even if no spawner ever processes them.
const unprocessedTTLMultiplier = 4

// +kubebuilder:rbac:groups=kelos.dev,resources=webhookevents,verbs=get;list;watch;delete

// WebhookEventReconciler reconciles WebhookEvent objects to handle TTL-based cleanup.
type WebhookEventReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile checks whether a WebhookEvent has exceeded its TTL and deletes it.
func (r *WebhookEventReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var event kelosv1alpha1.WebhookEvent
	if err := r.Get(ctx, req.NamespacedName, &event); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	expired, requeueAfter := webhookEventTTLExpired(&event)
	if !expired {
		if requeueAfter > 0 {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		return ctrl.Result{}, nil
	}

	logger.Info("Deleting WebhookEvent due to TTL expiration", "event", event.Name)
	if err := r.Delete(ctx, &event); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// webhookEventTTLExpired checks whether a WebhookEvent has exceeded its TTL.
// For processed events, the TTL is measured from ProcessedAt.
// For unprocessed events, a fallback of 4×TTL from ReceivedAt is used so
// that stuck events are eventually garbage-collected.
// Returns (true, 0) if the event should be deleted now, or (false, duration)
// if the event should be requeued after the given duration.
func webhookEventTTLExpired(event *kelosv1alpha1.WebhookEvent) (bool, time.Duration) {
	if event.Spec.TTLSecondsAfterProcessed == nil {
		return false, 0
	}

	ttl := time.Duration(*event.Spec.TTLSecondsAfterProcessed) * time.Second

	// If processed, expire based on ProcessedAt + TTL
	if event.Status.ProcessedAt != nil {
		expireAt := event.Status.ProcessedAt.Add(ttl)
		remaining := time.Until(expireAt)
		if remaining <= 0 {
			return true, 0
		}
		return false, remaining
	}

	// Fallback: expire unprocessed events after 4×TTL from ReceivedAt
	fallbackTTL := ttl * unprocessedTTLMultiplier
	expireAt := event.Spec.ReceivedAt.Add(fallbackTTL)
	remaining := time.Until(expireAt)
	if remaining <= 0 {
		return true, 0
	}
	return false, remaining
}

// webhookEventTTLPredicate filters out WebhookEvent objects that have no TTL
// configured, avoiding unnecessary reconcile churn.
func webhookEventTTLPredicate() predicate.Predicate {
	hasTTL := func(obj client.Object) bool {
		event, ok := obj.(*kelosv1alpha1.WebhookEvent)
		if !ok {
			return false
		}
		return event.Spec.TTLSecondsAfterProcessed != nil
	}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return hasTTL(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return hasTTL(e.ObjectNew)
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return false // no need to reconcile deletions
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return hasTTL(e.Object)
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WebhookEventReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelosv1alpha1.WebhookEvent{}, builder.WithPredicates(webhookEventTTLPredicate())).
		Complete(r)
}
