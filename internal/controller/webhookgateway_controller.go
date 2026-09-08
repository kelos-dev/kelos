package controller

import (
	"bytes"
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// gatewayWebhookSecretKey is the Secret data key the webhook server reads the
// inbound verification secret from.
const gatewayWebhookSecretKey = "webhook-secret"

// WebhookGatewayReconciler reconciles WebhookGateway status. It derives the
// inbound URL and reflects the authentication state based on the gateway type
// and the presence of its referenced Secrets. It manages no workloads.
type WebhookGatewayReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kelos.dev,resources=webhookgateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=webhookgateways/status,verbs=get;update;patch

// Reconcile derives status.path and the authentication phase for a WebhookGateway.
func (r *WebhookGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var gw kelos.WebhookGateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch WebhookGateway")
		return ctrl.Result{}, err
	}

	path := "/webhook/" + gw.Namespace + "/" + gw.Name

	phase, message, requeue, err := r.evaluate(ctx, &gw)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Capture the generation the status was computed from. The retry below
	// re-fetches the object to get a fresh resourceVersion, so it must not stamp
	// a newer generation onto status derived from an older spec.
	observedGen := gw.Generation

	if gw.Status.Path != path || gw.Status.Phase != phase || gw.Status.Message != message ||
		gw.Status.ObservedGeneration != observedGen {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if getErr := r.Get(ctx, req.NamespacedName, &gw); getErr != nil {
				return getErr
			}
			if gw.Generation != observedGen {
				// The spec changed since this status was computed; a fresh
				// reconcile is already queued for the new generation.
				return nil
			}
			gw.Status.Path = path
			gw.Status.Phase = phase
			gw.Status.Message = message
			gw.Status.ObservedGeneration = observedGen
			return r.Status().Update(ctx, &gw)
		}); err != nil {
			logger.Error(err, "Unable to update WebhookGateway status")
			return ctrl.Result{}, err
		}
	}

	if requeue {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// evaluate determines the authentication phase and message for a gateway. It
// requeues (requeue=true) when a referenced Secret is not yet present so the
// gateway becomes Authenticated once the Secret is created.
func (r *WebhookGatewayReconciler) evaluate(ctx context.Context, gw *kelos.WebhookGateway) (kelos.WebhookGatewayPhase, string, bool, error) {
	switch {
	case gw.Spec.Generic != nil:
		return kelos.WebhookGatewayPhaseUnauthenticated,
			"Generic gateways have no verification scheme; inbound deliveries are accepted without authentication. Restrict access at the network layer.",
			false, nil

	case gw.Spec.GitHub != nil:
		// Inbound HMAC secret, then optionally the outbound API credentials.
		if phase, msg, requeue, err := r.checkSecret(ctx, gw.Namespace, gw.Spec.GitHub.SecretRef.Name, "HMAC secret", ""); err != nil || phase != "" {
			return phase, msg, requeue, err
		}
		if gw.Spec.GitHub.CredentialsRef != nil {
			if phase, msg, requeue, err := r.checkSecret(ctx, gw.Namespace, gw.Spec.GitHub.CredentialsRef.Name, "credentials secret", ""); err != nil || phase != "" {
				return phase, msg, requeue, err
			}
		}
		return kelos.WebhookGatewayPhaseAuthenticated, "", false, nil

	case gw.Spec.Linear != nil:
		if phase, msg, requeue, err := r.checkSecret(ctx, gw.Namespace, gw.Spec.Linear.SecretRef.Name, "HMAC secret", ""); err != nil || phase != "" {
			return phase, msg, requeue, err
		}
		return kelos.WebhookGatewayPhaseAuthenticated, "", false, nil

	case gw.Spec.GitLab != nil:
		if phase, msg, requeue, err := r.checkSecret(ctx, gw.Namespace, gw.Spec.GitLab.SecretRef.Name, "webhook token secret", gatewayWebhookSecretKey); err != nil || phase != "" {
			return phase, msg, requeue, err
		}
		if gw.Spec.GitLab.CredentialsRef != nil {
			if phase, msg, requeue, err := r.checkSecret(ctx, gw.Namespace, gw.Spec.GitLab.CredentialsRef.Name, "credentials secret", GitLabTokenSecretKey); err != nil || phase != "" {
				return phase, msg, requeue, err
			}
		}
		return kelos.WebhookGatewayPhaseAuthenticated, "", false, nil

	default:
		// The CEL "exactly one of" rule should prevent reaching here.
		return kelos.WebhookGatewayPhaseSecretMissing,
			"no source configured: exactly one of github, linear, gitlab, or generic is required", false, nil
	}
}

// checkSecret returns a SecretMissing phase (with a requeue) when the named
// Secret is absent or, when key is set, lacks a non-blank value under that
// key. An empty phase means the Secret is usable.
func (r *WebhookGatewayReconciler) checkSecret(ctx context.Context, namespace, name, kind, key string) (kelos.WebhookGatewayPhase, string, bool, error) {
	var secret corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret)
	if apierrors.IsNotFound(err) {
		return kelos.WebhookGatewayPhaseSecretMissing,
			fmt.Sprintf("%s %q not found", kind, name), true, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if key != "" && len(bytes.TrimSpace(secret.Data[key])) == 0 {
		return kelos.WebhookGatewayPhaseSecretMissing,
			fmt.Sprintf("%s %q has no %s key", kind, name, key), true, nil
	}
	return "", "", false, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WebhookGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.WebhookGateway{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findGatewaysForSecret)).
		Complete(r)
}

func (r *WebhookGatewayReconciler) findGatewaysForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var list kelos.WebhookGatewayList
	if err := r.List(ctx, &list, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range list.Items {
		gw := &list.Items[i]
		if gatewayReferencesSecret(gw, secret.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name},
			})
		}
	}
	return requests
}

// gatewayReferencesSecret reports whether the gateway references the named
// Secret — the inbound HMAC secret or token, or the outbound credentials for
// github and gitlab.
func gatewayReferencesSecret(gw *kelos.WebhookGateway, name string) bool {
	switch {
	case gw.Spec.GitHub != nil:
		return gw.Spec.GitHub.SecretRef.Name == name ||
			(gw.Spec.GitHub.CredentialsRef != nil && gw.Spec.GitHub.CredentialsRef.Name == name)
	case gw.Spec.Linear != nil:
		return gw.Spec.Linear.SecretRef.Name == name
	case gw.Spec.GitLab != nil:
		return gw.Spec.GitLab.SecretRef.Name == name ||
			(gw.Spec.GitLab.CredentialsRef != nil && gw.Spec.GitLab.CredentialsRef.Name == name)
	default:
		return false
	}
}
