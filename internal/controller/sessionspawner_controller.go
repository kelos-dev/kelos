package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
)

// SessionSpawnerReconciler observes Sessions owned by a SessionSpawner.
type SessionSpawnerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kelos.dev,resources=sessionspawners,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=sessionspawners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=sessions,verbs=get;list;watch

// Reconcile updates the observable Session count and readiness condition.
func (r *SessionSpawnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	ctx, span := startReconcileSpan(ctx, "SessionSpawner", req.NamespacedName)
	defer span.End()

	var spawner kelos.SessionSpawner
	if err := r.Get(ctx, req.NamespacedName, &spawner); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting SessionSpawner %q: %w", req.Name, err)
	}

	var sessions kelos.SessionList
	if err := r.List(ctx, &sessions,
		client.InNamespace(spawner.Namespace),
		client.MatchingLabels{sessionbuilder.LabelSessionSpawner: string(spawner.UID)},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing Sessions for SessionSpawner %q: %w", spawner.Name, err)
	}

	original := spawner.DeepCopy()
	spawner.Status.ObservedGeneration = spawner.Generation
	associatedSessions := make([]kelos.Session, 0, len(sessions.Items))
	for i := range sessions.Items {
		if metav1.IsControlledBy(&sessions.Items[i], &spawner) {
			associatedSessions = append(associatedSessions, sessions.Items[i])
		}
	}
	spawner.Status.TotalSessions = int32(len(associatedSessions))
	if err := r.Status().Patch(ctx, &spawner, client.MergeFrom(original)); err != nil {
		logger.Error(err, "Unable to update SessionSpawner status", "sessionSpawner", spawner.Name)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the SessionSpawner controller with the Manager.
func (r *SessionSpawnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.SessionSpawner{}).
		Owns(&kelos.Session{}).
		Complete(r)
}
