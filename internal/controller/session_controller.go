package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
	"github.com/kelos-dev/kelos/internal/sessionreset"
	"github.com/kelos-dev/kelos/internal/sessionsuspend"
	"github.com/kelos-dev/kelos/internal/sessionupdate"
)

const (
	SessionRuntimeImageRepository = "ghcr.io/kelos-dev/kelos-session-runtime"
	DefaultSessionRuntimeImage    = SessionRuntimeImageRepository + ":latest"

	sessionRuntimeContainerName       = "kelos-session-runtime"
	sessionRuntimeVolumeName          = "kelos-session-runtime"
	sessionRuntimeMountPath           = "/kelos/bin"
	sessionRuntimeBinary              = sessionRuntimeMountPath + "/kelos-session-runtime"
	sessionClaudeConfigDir            = "/workspace/.kelos/session/claude-config"
	sessionCodexHome                  = "/workspace/.kelos/session/codex-home"
	sessionOpenCodeConfigDir          = "/workspace/.kelos/session/opencode-config"
	sessionOpenCodeDataDir            = "/workspace/.kelos/session/opencode-data"
	sessionInitializedPath            = "/workspace/.kelos/session/initialized"
	sessionNameAnnotation             = "kelos.dev/session-name"
	sessionPluginChecksumAnnotation   = "kelos.dev/plugin-content-checksum"
	sessionTokenFingerprintAnnotation = "kelos.dev/github-token-mint-fingerprint"
	// ControllerRevision names append a hyphen and up to 10 hash characters and are used as Pod label values.
	sessionWorkloadNameMaxLength   = 52
	idleResumeAcknowledgementGrace = 5 * time.Second
	idleResumeRequestTimeout       = 10 * time.Minute
)

// SessionReconciler reconciles a Session object.
type SessionReconciler struct {
	client.Client
	Scheme                        *runtime.Scheme
	JobBuilder                    *JobBuilder
	SessionRuntimeImage           string
	SessionRuntimeImagePullPolicy corev1.PullPolicy
	Recorder                      record.EventRecorder
	TokenClient                   *githubapp.TokenClient
}

type sessionConfigurationError struct {
	err error
}

func (e *sessionConfigurationError) Error() string {
	return e.err.Error()
}

func (e *sessionConfigurationError) Unwrap() error {
	return e.err
}

func invalidSessionConfiguration(err error) error {
	return &sessionConfigurationError{err: err}
}

func isInvalidSessionConfiguration(err error) bool {
	var configurationError *sessionConfigurationError
	return errors.As(err, &configurationError)
}

type sessionInputClient struct {
	client.Client
}

type sessionInputUnavailableError struct {
	err error
}

func (e *sessionInputUnavailableError) Error() string {
	return e.err.Error()
}

func (e *sessionInputUnavailableError) Unwrap() error {
	return e.err
}

func (c sessionInputClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return &sessionInputUnavailableError{err: err}
	}
	return nil
}

func isSessionInputUnavailable(err error) bool {
	var unavailableError *sessionInputUnavailableError
	return errors.As(err, &unavailableError)
}

// +kubebuilder:rbac:groups=kelos.dev,resources=sessions,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups=kelos.dev,resources=sessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=workspaces;agentconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;delete;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update

// Reconcile creates and observes the StatefulSet that owns a Session conversation.
func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, reconcileErr error) {
	logger := log.FromContext(ctx)

	var session kelos.Session
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch Session")
		return ctrl.Result{}, err
	}

	workloadName := sessionWorkloadName(&session)
	var statefulSet appsv1.StatefulSet
	err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: workloadName}, &statefulSet)
	statefulSetMissing := apierrors.IsNotFound(err)
	if err != nil && !statefulSetMissing {
		logger.Error(err, "Unable to fetch Session StatefulSet", "session", session.Name)
		return ctrl.Result{}, err
	}
	if !statefulSetMissing && !metav1.IsControlledBy(&statefulSet, &session) {
		message := fmt.Sprintf("StatefulSet %q already exists and is not controlled by this Session", statefulSet.Name)
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "StatefulSetConflict")
	}
	if session.Annotations[sessionreset.RequestAnnotation] != "" {
		if statefulSetMissing {
			return r.reconcileSessionReset(ctx, &session, nil)
		}
		return r.reconcileSessionReset(ctx, &session, &statefulSet)
	}
	if sessionSuspendedByUser(&session) && sessionsuspend.ResumeRequested(&session) {
		if err := r.clearSessionIdleResumeRequest(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if sessionsuspend.ResumeRequested(&session) {
		protectionEndsAt := time.Time{}
		if sessionsuspend.ResumeAcknowledged(&session) {
			acknowledgedAt, observed := sessionsuspend.ResumeAcknowledgementTime(&session)
			if !observed {
				if err := r.setSessionIdleResumeTime(ctx, &session, sessionsuspend.ResumeAcknowledgementTimeAnnotation); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{Requeue: true}, nil
			}
			idleBaseline := acknowledgedAt.Truncate(time.Second)
			if session.Status.LastActivityTime == nil || idleBaseline.After(session.Status.LastActivityTime.Time) {
				lastActivityTime := metav1.NewTime(idleBaseline)
				session.Status.LastActivityTime = &lastActivityTime
				if err := r.Status().Update(ctx, &session); err != nil {
					return ctrl.Result{}, fmt.Errorf("starting idle period for resumed Session %q: %w", session.Name, err)
				}
				return ctrl.Result{Requeue: true}, nil
			}
			protectionEndsAt = acknowledgedAt.Add(idleResumeAcknowledgementGrace)
			if !time.Now().Before(protectionEndsAt) {
				if err := r.clearSessionIdleResumeRequest(ctx, &session); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{Requeue: true}, nil
			}
		} else {
			requestedAt, observed := sessionsuspend.ResumeRequestTime(&session)
			if !observed {
				if err := r.setSessionIdleResumeTime(ctx, &session, sessionsuspend.ResumeRequestTimeAnnotation); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{Requeue: true}, nil
			}
			protectionEndsAt = requestedAt.Add(idleResumeRequestTimeout)
			if !time.Now().Before(protectionEndsAt) {
				if statefulSetMissing {
					return r.expireSessionIdleResumeRequest(ctx, &session, nil)
				}
				return r.expireSessionIdleResumeRequest(ctx, &session, &statefulSet)
			}
		}
		defer func() {
			if reconcileErr != nil || result.Requeue {
				return
			}
			remaining := time.Until(protectionEndsAt)
			if remaining <= 0 {
				result = ctrl.Result{Requeue: true}
				return
			}
			if result.RequeueAfter == 0 || remaining < result.RequeueAfter {
				result.RequeueAfter = remaining
			}
		}()
	}
	idleSuspended := sessionsuspend.IsIdlePolicySuspended(&session)
	if idleSuspended && sessionsuspend.ResumeRequested(&session) {
		if session.Annotations[sessionupdate.IdleDrainRequestAnnotation] != "" ||
			session.Annotations[sessionupdate.IdleDrainReportAnnotation] != "" {
			if err := r.clearSessionIdleDrainRequest(ctx, &session); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		if err := r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhasePending, "Session Pod is resuming after idle suspension", "IdleResumeRequested"); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Started resuming idle-suspended Session", "session", session.Name)
		if r.Recorder != nil {
			r.Recorder.Event(&session, corev1.EventTypeNormal, "SessionIdleResumeStarted", "Started resuming Session after a client requested a connection")
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if statefulSetMissing {
		// Reap an already-idle Session whose workload is gone rather than
		// recreating it just to delete it later. A missing StatefulSet does not
		// prove its ordinal Pod is gone, so reapMissingWorkloadIdleSession drains a
		// surviving Pod before deleting; the unknown-activity guard lives in
		// sessionIdleExpired and the resource-version precondition in
		// reapIdleSession handles any status change since the fetch above.
		if session.DeletionTimestamp == nil {
			if expired, _ := sessionIdleExpired(&session); expired {
				if idleSuspended {
					return r.reapIdleSession(ctx, &session)
				}
				return r.reapMissingWorkloadIdleSession(ctx, &session, workloadName)
			}
		}
		// Drop any idle-drain request before re-creating a runtime that must accept
		// turns while the controller re-evaluates its activity.
		if err := r.clearSessionIdleDrainRequest(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		return r.createSessionStatefulSet(ctx, &session)
	}
	if statefulSet.DeletionTimestamp != nil {
		if idleSuspended {
			if session.DeletionTimestamp == nil {
				if expired, _ := sessionIdleExpired(&session); expired {
					return r.reapIdleSession(ctx, &session)
				}
			}
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhasePending, "Session StatefulSet is terminating and will be recreated", "StatefulSetTerminating")
	}
	suspended := sessionSuspended(&session)
	if suspended {
		// Suspension must remain fail-safe even when referenced configuration
		// is missing or invalid. Continue below so available desired state is
		// still reconciled while the runtime remains stopped.
		if err := r.setSessionReplicas(ctx, &statefulSet, 0); err != nil {
			return ctrl.Result{}, err
		}
	}
	if idleSuspended && session.DeletionTimestamp == nil {
		if expired, _ := sessionIdleExpired(&session); expired {
			return r.reapIdleSession(ctx, &session)
		}
	}
	runtimeStopped := statefulSet.Spec.Replicas != nil && *statefulSet.Spec.Replicas == 0
	workspace, agentConfig, waitingMessage, err := r.resolveSessionInputs(
		ctx,
		&session,
		sessionGitHubTokenMinimumValidity(&session, &statefulSet),
	)
	if err != nil {
		if isInvalidSessionConfiguration(err) {
			message := fmt.Sprintf("Failed to resolve Session configuration: %v", err)
			_ = r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "ConfigurationInvalid")
		}
		return ctrl.Result{}, err
	}
	if waitingMessage != "" {
		phase := kelos.SessionPhasePending
		message := waitingMessage
		reason := "WaitingForDependency"
		if suspended {
			phase = kelos.SessionPhaseSuspended
			if idleSuspended {
				message = "Session runtime is suspended after exceeding its idle policy"
				reason = sessionsuspend.IdlePolicyReason
			} else {
				message = fmt.Sprintf("Session runtime is suspended: %s", waitingMessage)
			}
		}
		if err := r.updateSessionStatus(ctx, &session, nil, phase, message, reason); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	desiredStatefulSet, configMap, err := r.buildSessionStatefulSet(&session, workspace, agentConfig)
	if err != nil {
		message := fmt.Sprintf("Failed to build Session StatefulSet: %v", err)
		_ = r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "StatefulSetBuildFailed")
		return ctrl.Result{}, err
	}
	if err := r.ensureSessionPluginConfigMap(ctx, &session, configMap); err != nil {
		return ctrl.Result{}, err
	}
	serviceAccountName := desiredStatefulSet.Spec.Template.Spec.ServiceAccountName
	if err := r.ensureSessionRuntimeAccess(ctx, &session, serviceAccountName); err != nil {
		message := fmt.Sprintf("Failed to prepare Session runtime access: %v", err)
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "RuntimeAccessFailed")
	}
	if err := r.ensureSessionService(ctx, &session); err != nil {
		message := fmt.Sprintf("Failed to prepare Session governing Service: %v", err)
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "ServiceFailed")
	}
	if _, err := r.reconcileSessionStatefulSet(ctx, &session, &statefulSet, desiredStatefulSet); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureSessionWorkspaceClaimOwnership(ctx, &session, &statefulSet); err != nil {
		return ctrl.Result{}, err
	}
	if suspended {
		if err := r.setSessionReplicas(ctx, &statefulSet, 0); err != nil {
			return ctrl.Result{}, err
		}
		if idleSuspended {
			deleteExpired, deleteRemaining := sessionIdleExpired(&session)
			if deleteExpired && session.DeletionTimestamp == nil {
				return r.reapIdleSession(ctx, &session)
			}
			err := r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseSuspended, "Session runtime is suspended after exceeding its idle policy", sessionsuspend.IdlePolicyReason)
			if err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: deleteRemaining}, nil
		}
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseSuspended, "Session runtime is suspended", "RuntimeSuspended")
	}
	result = ctrl.Result{}
	if next, err := r.refreshSessionGitHubAppTokenIfNeeded(ctx, &session, &statefulSet.Spec.Template.Spec); err != nil {
		logger.Error(err, "Unable to refresh Session GitHub App token", "session", session.Name)
		if runtimeStopped {
			return ctrl.Result{RequeueAfter: tokenRefreshRetryInterval}, nil
		}
		result.RequeueAfter = tokenRefreshRetryInterval
	} else if next > 0 {
		result.RequeueAfter = next
	}
	if err := r.setSessionReplicas(ctx, &statefulSet, 1); err != nil {
		return ctrl.Result{}, err
	}

	podName := statefulSet.Name + "-0"
	var pod corev1.Pod
	err = r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: podName}, &pod)
	if apierrors.IsNotFound(err) {
		if hasSessionRuntimeUpdateAnnotations(&session) {
			if err := r.clearSessionRuntimeUpdateRequest(ctx, &session); err != nil {
				return ctrl.Result{}, err
			}
		}
		if err := r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhasePending, "Session Pod is starting", "PodStarting"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err != nil {
		logger.Error(err, "Unable to fetch Session Pod", "session", session.Name)
		return ctrl.Result{}, err
	}
	if !metav1.IsControlledBy(&pod, &statefulSet) {
		message := fmt.Sprintf("Pod %q already exists and is not controlled by StatefulSet %q", pod.Name, statefulSet.Name)
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "PodConflict")
	}
	if pod.DeletionTimestamp != nil {
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, &pod, kelos.SessionPhasePending, "Session Pod is terminating and will be recreated", "PodTerminating")
	}
	phase, message, reason := sessionPhaseForPod(&pod)
	if err := r.updateSessionStatus(ctx, &session, &pod, phase, message, reason); err != nil {
		return ctrl.Result{}, err
	}
	updateResult, waitingForUpdate, err := r.reconcileSessionRuntimeUpdate(ctx, &session, &statefulSet, &pod, phase)
	if err != nil {
		return ctrl.Result{}, err
	}
	if waitingForUpdate && (result.RequeueAfter == 0 || updateResult.RequeueAfter < result.RequeueAfter) {
		result = updateResult
	}
	// Evaluate idle actions only after the Active condition has been validated
	// against the current Pod above, so a stale condition from a previous Pod
	// cannot trigger suspension or deletion.
	if session.DeletionTimestamp == nil && !sessionsuspend.ResumeRequested(&session) {
		deleteExpired, deleteRemaining := sessionIdleExpired(&session)
		if deleteExpired {
			return r.reconcileIdleReap(ctx, &session, &pod)
		}
		suspendExpired, suspendRemaining := sessionIdleSuspendExpired(&session)
		if suspendExpired {
			return r.reconcileIdleSuspend(ctx, &session, &statefulSet, &pod)
		}
		// No idle action is due. Cancel any pending drain so the runtime resumes
		// accepting turns after activity or a resume request.
		if err := r.clearSessionIdleDrainRequest(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		for _, remaining := range []time.Duration{deleteRemaining, suspendRemaining} {
			if remaining > 0 && (result.RequeueAfter == 0 || remaining < result.RequeueAfter) {
				result.RequeueAfter = remaining
			}
		}
	}
	return result, nil
}

func (r *SessionReconciler) expireSessionIdleResumeRequest(
	ctx context.Context,
	session *kelos.Session,
	statefulSet *appsv1.StatefulSet,
) (ctrl.Result, error) {
	var pod corev1.Pod
	err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session) + "-0"}, &pod)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("getting Session Pod before expiring resume request for Session %q: %w", session.Name, err)
	}
	podIsOurs := err == nil && (statefulSet != nil || pod.Annotations[sessionNameAnnotation] == session.Name)
	if podIsOurs {
		if pod.DeletionTimestamp != nil {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodUnknown {
			drained, err := r.ensureSessionIdleDrained(
				ctx,
				session,
				&pod,
				"Waiting for Session Pod %s to drain after its idle resume request expired",
			)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !drained {
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
		}
	}

	if err := r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseSuspended, "Session runtime is suspended after its idle resume request expired", sessionsuspend.IdlePolicyReason); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.clearSessionIdleResumeRequest(ctx, session); err != nil {
		return ctrl.Result{}, err
	}
	if statefulSet != nil && statefulSet.DeletionTimestamp == nil {
		if err := r.setSessionReplicas(ctx, statefulSet, 0); err != nil {
			return ctrl.Result{}, err
		}
	}
	log.FromContext(ctx).Info("Expired idle Session resume request", "session", session.Name)
	return ctrl.Result{Requeue: true}, nil
}

// reapMissingWorkloadIdleSession reaps an idle Session whose StatefulSet has
// been deleted. A missing StatefulSet does not prove its ordinal Pod is gone:
// background garbage collection can leave the Pod running temporarily, and
// orphan propagation leaves it running indefinitely. If the Pod still exists and
// is running it is drained through the normal handshake before deletion so a turn
// it has accepted but not yet published to status is not lost.
//
// When no live runtime remains (the Pod is absent, terminal, or belongs to a
// different Session), a Session with a persistent workspace is not deleted from
// its possibly stale Active=False status: its journal may hold activity that a
// Pod accepted or completed while its ordered status patches were still retrying,
// which the resource-version precondition cannot detect. Such a Session has its
// runtime recreated so it recovers the journal and republishes activity — leaving
// the idle-period reset intact — before the normal reconcile decides whether it
// is still idle. Only a Session without persistent workspace state, which has no
// journal to recover, is reaped directly.
func (r *SessionReconciler) reapMissingWorkloadIdleSession(ctx context.Context, session *kelos.Session, workloadName string) (ctrl.Result, error) {
	podName := workloadName + "-0"
	var pod corev1.Pod
	err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: podName}, &pod)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("getting Session Pod %q before idle reap: %w", podName, err)
	}
	podIsOurs := err == nil && pod.Annotations[sessionNameAnnotation] == session.Name
	if podIsOurs {
		if pod.DeletionTimestamp != nil {
			// The Pod is terminating; wait for its absence to be confirmed rather than
			// reaping while it may still be finishing an accepted turn.
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			// A running Pod can still acknowledge the drain handshake.
			return r.reconcileIdleReap(ctx, session, &pod)
		}
	}
	// No live runtime remains. Preserve any unpublished activity in a persistent
	// workspace journal by recovering the runtime rather than deleting the Session
	// and its workspace against a possibly stale Active=False status.
	if session.Spec.VolumeClaimTemplate != nil {
		if podIsOurs {
			// Clear the leftover terminal Pod so the recreated StatefulSet can start a
			// fresh ordinal Pod that mounts the retained workspace and recovers.
			if err := r.Delete(ctx, &pod, client.Preconditions{UID: &pod.UID}); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("deleting terminal Session Pod %q before recovery: %w", pod.Name, err)
			}
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return r.createSessionStatefulSet(ctx, session)
	}
	return r.reapIdleSession(ctx, session)
}

// reconcileIdleReap drains the Session Pod before deleting an idle Session, so a
// turn the runtime has locally accepted but not yet published to status is not
// lost. It sets an idle-drain request and waits for the runtime to acknowledge
// that no turn is in flight and it is no longer accepting turns, then deletes.
func (r *SessionReconciler) reconcileIdleReap(ctx context.Context, session *kelos.Session, pod *corev1.Pod) (ctrl.Result, error) {
	drained, err := r.ensureSessionIdleDrained(
		ctx,
		session,
		pod,
		"Waiting for Session Pod %s to drain before reclaiming the idle Session",
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !drained {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return r.reapIdleSession(ctx, session)
}

func (r *SessionReconciler) reconcileIdleSuspend(
	ctx context.Context,
	session *kelos.Session,
	statefulSet *appsv1.StatefulSet,
	pod *corev1.Pod,
) (ctrl.Result, error) {
	drained, err := r.ensureSessionIdleDrained(
		ctx,
		session,
		pod,
		"Waiting for Session Pod %s to drain before suspending the idle Session",
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !drained {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.updateSessionStatus(
		ctx,
		session,
		nil,
		kelos.SessionPhaseSuspended,
		"Session runtime is suspended after exceeding its idle policy",
		sessionsuspend.IdlePolicyReason,
	); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setSessionReplicas(ctx, statefulSet, 0); err != nil {
		return ctrl.Result{}, fmt.Errorf("suspending idle Session %q: %w", session.Name, err)
	}
	log.FromContext(ctx).Info("Suspended idle Session", "session", session.Name)
	if r.Recorder != nil {
		r.Recorder.Event(session, corev1.EventTypeNormal, "SessionIdleSuspended", "Suspended Session after exceeding its idle policy")
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *SessionReconciler) ensureSessionIdleDrained(
	ctx context.Context,
	session *kelos.Session,
	pod *corev1.Pod,
	eventMessage string,
) (bool, error) {
	if pod.UID == "" {
		return false, nil
	}
	request, installed := installedSessionIdleDrainRequest(session, pod)
	if !installed {
		request = newSessionIdleDrainRequest(pod)
		encoded, err := sessionupdate.Encode(request)
		if err != nil {
			return false, err
		}
		if err := r.setSessionIdleDrainRequest(ctx, session, encoded); err != nil {
			return false, err
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(session, corev1.EventTypeNormal, "SessionIdleDraining", eventMessage, pod.Name)
		}
		return false, nil
	}
	drained, err := sessionIdleDrainComplete(session, request, pod)
	if err != nil {
		return false, err
	}
	return drained, nil
}

func (r *SessionReconciler) setSessionIdleDrainRequest(ctx context.Context, session *kelos.Session, value string) error {
	original := session.DeepCopy()
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[sessionupdate.IdleDrainRequestAnnotation] = value
	// Drop any report from a previous idle period so a stale Drained
	// acknowledgement cannot satisfy this newly installed request before the
	// runtime has observed it and stopped accepting turns.
	delete(session.Annotations, sessionupdate.IdleDrainReportAnnotation)
	if err := r.Patch(ctx, session, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("requesting idle drain for Session %q: %w", session.Name, err)
	}
	return nil
}

// clearSessionIdleDrainRequest removes a pending idle-drain request so the
// runtime stops rejecting new turns. It is a no-op when no request is present.
// The runtime clears its own report once it observes the request is gone.
func (r *SessionReconciler) clearSessionIdleDrainRequest(ctx context.Context, session *kelos.Session) error {
	if session.Annotations[sessionupdate.IdleDrainRequestAnnotation] == "" &&
		session.Annotations[sessionupdate.IdleDrainReportAnnotation] == "" {
		return nil
	}
	original := session.DeepCopy()
	delete(session.Annotations, sessionupdate.IdleDrainRequestAnnotation)
	delete(session.Annotations, sessionupdate.IdleDrainReportAnnotation)
	if err := r.Patch(ctx, session, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("clearing idle drain request for Session %q: %w", session.Name, err)
	}
	return nil
}

// sessionIdleDrainComplete reports whether the runtime has acknowledged the
// idle-drain request for the current Pod with a Drained phase.
func sessionIdleDrainComplete(session *kelos.Session, request sessionupdate.Request, pod *corev1.Pod) (bool, error) {
	value := session.Annotations[sessionupdate.IdleDrainReportAnnotation]
	if value == "" {
		return false, nil
	}
	report, err := sessionupdate.DecodeReport(value)
	if err != nil {
		return false, fmt.Errorf("reading idle drain report for Session %q: %w", session.Name, err)
	}
	return report.RequestID == request.ID && report.PodUID == pod.UID && report.Phase == sessionupdate.PhaseDrained, nil
}

// reapIdleSession deletes a Session that has exceeded its idle delete policy.
// The delete is guarded by a UID and resource-version precondition so that a
// turn starting between the status read and the delete (for example, the
// runtime publishing Active=True) fails the precondition and forces a requeue
// rather than deleting a Session whose activity is in flight.
func (r *SessionReconciler) reapIdleSession(ctx context.Context, session *kelos.Session) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	preconditions := client.Preconditions{UID: &session.UID, ResourceVersion: &session.ResourceVersion}
	if err := r.Delete(ctx, session, preconditions); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		if apierrors.IsConflict(err) {
			logger.Info("Session changed before idle deletion; requeuing to re-evaluate", "session", session.Name)
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Unable to delete idle Session", "session", session.Name)
		return ctrl.Result{}, err
	}
	logger.Info("Deleted Session due to idle delete policy", "session", session.Name)
	if r.Recorder != nil {
		r.Recorder.Event(session, corev1.EventTypeNormal, "SessionIdleReaped", "Deleted Session after exceeding its idle delete policy")
	}
	return ctrl.Result{}, nil
}

func (r *SessionReconciler) createSessionStatefulSet(ctx context.Context, session *kelos.Session) (ctrl.Result, error) {
	workspace, agentConfig, waitingMessage, err := r.resolveSessionInputs(
		ctx,
		session,
		sessionGitHubTokenMinimumValidity(session, nil),
	)
	if err != nil {
		if isInvalidSessionConfiguration(err) {
			message := fmt.Sprintf("Failed to resolve Session configuration: %v", err)
			_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "ConfigurationInvalid")
		}
		return ctrl.Result{}, err
	}
	if waitingMessage != "" {
		phase := kelos.SessionPhasePending
		message := waitingMessage
		reason := "WaitingForDependency"
		if sessionsuspend.IsIdlePolicySuspended(session) {
			phase = kelos.SessionPhaseSuspended
			message = "Session runtime is suspended after exceeding its idle policy"
			reason = sessionsuspend.IdlePolicyReason
		}
		if err := r.updateSessionStatus(ctx, session, nil, phase, message, reason); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	statefulSet, configMap, err := r.buildSessionStatefulSet(session, workspace, agentConfig)
	if err != nil {
		message := fmt.Sprintf("Failed to build Session StatefulSet: %v", err)
		_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "StatefulSetBuildFailed")
		return ctrl.Result{}, err
	}
	if err := r.ensureSessionPluginConfigMap(ctx, session, configMap); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureSessionRuntimeAccess(ctx, session, statefulSet.Spec.Template.Spec.ServiceAccountName); err != nil {
		message := fmt.Sprintf("Failed to prepare Session runtime access: %v", err)
		_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "RuntimeAccessFailed")
		return ctrl.Result{}, err
	}
	if err := r.ensureSessionService(ctx, session); err != nil {
		message := fmt.Sprintf("Failed to prepare Session governing Service: %v", err)
		_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "ServiceFailed")
		return ctrl.Result{}, err
	}
	created, err := r.reconcileSessionStatefulSet(ctx, session, nil, statefulSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !created {
		return ctrl.Result{Requeue: true}, nil
	}
	phase := kelos.SessionPhasePending
	message := "Session Pod is starting"
	reason := "PodStarting"
	if sessionsuspend.IsIdlePolicySuspended(session) {
		phase = kelos.SessionPhaseSuspended
		message = "Session runtime is suspended after exceeding its idle policy"
		reason = sessionsuspend.IdlePolicyReason
	} else if sessionSuspended(session) {
		phase = kelos.SessionPhaseSuspended
		message = "Session runtime is suspended"
		reason = "RuntimeSuspended"
	}
	if err := r.updateSessionStatus(ctx, session, nil, phase, message, reason); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// reconcileSessionStatefulSet applies the complete controller-managed
// StatefulSet state. Fields preserved by sessionStatefulSetUpdateCandidate are
// reconciled through their dedicated lifecycle paths or fixed at creation.
func (r *SessionReconciler) reconcileSessionStatefulSet(
	ctx context.Context,
	session *kelos.Session,
	current *appsv1.StatefulSet,
	desired *appsv1.StatefulSet,
) (bool, error) {
	if current == nil {
		if err := controllerutil.SetControllerReference(session, desired, r.Scheme); err != nil {
			return false, fmt.Errorf("setting Session owner on StatefulSet: %w", err)
		}
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return false, nil
			}
			return false, fmt.Errorf("creating Session StatefulSet %q: %w", desired.Name, err)
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(session, corev1.EventTypeNormal, "StatefulSetCreated", "Created StatefulSet %s for Session", desired.Name)
		}
		return true, nil
	}

	candidate := sessionStatefulSetUpdateCandidate(current, desired)

	// Let the API server apply StatefulSet and Pod defaults before comparing.
	// This keeps the complete desired spec authoritative without repeatedly
	// updating fields defaulted by Kubernetes or admission webhooks.
	if err := r.Update(ctx, candidate, client.DryRunAll); err != nil {
		return false, fmt.Errorf("dry-run updating Session StatefulSet %q: %w", current.Name, err)
	}
	if apiequality.Semantic.DeepEqual(current.Labels, candidate.Labels) &&
		apiequality.Semantic.DeepEqual(current.Spec, candidate.Spec) {
		return false, nil
	}
	if err := r.Update(ctx, candidate); err != nil {
		return false, fmt.Errorf("updating Session StatefulSet %q: %w", current.Name, err)
	}
	*current = *candidate
	if r.Recorder != nil {
		r.Recorder.Eventf(session, corev1.EventTypeNormal, "StatefulSetUpdated", "Updated StatefulSet %s for Session", current.Name)
	}
	return false, nil
}

func sessionStatefulSetUpdateCandidate(current, desired *appsv1.StatefulSet) *appsv1.StatefulSet {
	candidate := current.DeepCopy()
	desiredCopy := desired.DeepCopy()
	candidate.Labels = desiredCopy.Labels
	candidate.Spec = desiredCopy.Spec

	// Replica changes are gated separately by suspension and credential
	// refresh. Retain fields that cannot be updated across every supported
	// Kubernetes version.
	liveSpec := current.Spec.DeepCopy()
	candidate.Spec.Replicas = liveSpec.Replicas
	candidate.Spec.ServiceName = liveSpec.ServiceName
	candidate.Spec.PodManagementPolicy = liveSpec.PodManagementPolicy
	candidate.Spec.Selector = liveSpec.Selector
	candidate.Spec.VolumeClaimTemplates = liveSpec.VolumeClaimTemplates
	candidate.Spec.RevisionHistoryLimit = liveSpec.RevisionHistoryLimit
	return candidate
}

func (r *SessionReconciler) ensureSessionPluginConfigMap(ctx context.Context, session *kelos.Session, desired *corev1.ConfigMap) error {
	if desired == nil {
		return nil
	}
	if err := controllerutil.SetControllerReference(session, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting Session owner on plugin ConfigMap: %w", err)
	}
	var existing corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), &existing); apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("creating Session plugin ConfigMap: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("getting Session plugin ConfigMap: %w", err)
	}
	if !metav1.IsControlledBy(&existing, session) {
		return fmt.Errorf("plugin ConfigMap %q already exists and is not controlled by this Session", desired.Name)
	}
	original := existing.DeepCopy()
	existing.Data = desired.Data
	existing.BinaryData = desired.BinaryData
	if apiequality.Semantic.DeepEqual(original.Data, existing.Data) &&
		apiequality.Semantic.DeepEqual(original.BinaryData, existing.BinaryData) {
		return nil
	}
	if err := r.Patch(ctx, &existing, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patching Session plugin ConfigMap %q: %w", existing.Name, err)
	}
	return nil
}

func (r *SessionReconciler) reconcileSessionReset(
	ctx context.Context,
	session *kelos.Session,
	statefulSet *appsv1.StatefulSet,
) (ctrl.Result, error) {
	requestID := session.Annotations[sessionreset.RequestAnnotation]
	state := sessionreset.State{RequestID: requestID, Phase: sessionreset.PhaseStopping}
	if value := session.Annotations[sessionreset.StateAnnotation]; value != "" {
		current, err := sessionreset.DecodeState(value)
		if err != nil {
			message := fmt.Sprintf("Session reset state is invalid: %v", err)
			_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "ResetStateInvalid")
			return ctrl.Result{}, err
		}
		if current.RequestID == requestID {
			state = current
		}
	}
	if value, err := sessionreset.EncodeState(state); err != nil {
		return ctrl.Result{}, err
	} else if session.Annotations[sessionreset.StateAnnotation] != value {
		if err := r.setSessionResetState(ctx, session, value); err != nil {
			return ctrl.Result{}, err
		}
		if r.Recorder != nil {
			r.Recorder.Event(session, corev1.EventTypeNormal, "ResetStarted", "Started resetting Session workspace")
		}
	}

	switch state.Phase {
	case sessionreset.PhaseStopping:
		if err := r.updateSessionStatus(ctx, session, nil, kelos.SessionPhasePending, "Session Pod is stopping for workspace reset", "ResetStopping"); err != nil {
			return ctrl.Result{}, err
		}
		stopped, err := r.stopSessionForReset(ctx, session, statefulSet)
		if err != nil || !stopped {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, err
		}
		state.Phase = sessionreset.PhaseDeletingStorage
		value, err := sessionreset.EncodeState(state)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.setSessionResetState(ctx, session, value); err != nil {
			return ctrl.Result{}, err
		}
		fallthrough

	case sessionreset.PhaseDeletingStorage:
		if err := r.updateSessionStatus(ctx, session, nil, kelos.SessionPhasePending, "Session workspace storage is being deleted", "ResetDeletingStorage"); err != nil {
			return ctrl.Result{}, err
		}
		stopped, err := r.stopSessionForReset(ctx, session, statefulSet)
		if err != nil || !stopped {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, err
		}
		deleted, err := r.deleteSessionWorkspaceForReset(ctx, session, statefulSet)
		if err != nil || !deleted {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, err
		}
		state.Phase = sessionreset.PhaseStarting
		value, err := sessionreset.EncodeState(state)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.setSessionResetState(ctx, session, value); err != nil {
			return ctrl.Result{}, err
		}
		fallthrough

	case sessionreset.PhaseStarting:
		return r.startSessionAfterReset(ctx, session, statefulSet)
	default:
		return ctrl.Result{}, fmt.Errorf("resetting Session %q: unsupported phase %q", session.Name, state.Phase)
	}
}

func (r *SessionReconciler) stopSessionForReset(ctx context.Context, session *kelos.Session, statefulSet *appsv1.StatefulSet) (bool, error) {
	if statefulSet != nil && statefulSet.DeletionTimestamp == nil {
		if err := r.setSessionReplicas(ctx, statefulSet, 0); err != nil {
			return false, err
		}
	}

	var pod corev1.Pod
	key := client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session) + "-0"}
	if err := r.Get(ctx, key, &pod); apierrors.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("getting Session Pod %q for reset: %w", key.Name, err)
	}
	if statefulSet != nil {
		if !metav1.IsControlledBy(&pod, statefulSet) {
			return false, fmt.Errorf("Session Pod %q is not controlled by StatefulSet %q", pod.Name, statefulSet.Name)
		}
	} else if pod.Annotations[sessionNameAnnotation] != session.Name {
		return false, fmt.Errorf("Pod %q is not associated with Session %q", pod.Name, session.Name)
	}
	if pod.DeletionTimestamp != nil {
		return false, nil
	}
	if err := r.Delete(ctx, &pod, client.Preconditions{UID: &pod.UID}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("deleting Session Pod %q for reset: %w", pod.Name, err)
	}
	return false, nil
}

func (r *SessionReconciler) deleteSessionWorkspaceForReset(ctx context.Context, session *kelos.Session, statefulSet *appsv1.StatefulSet) (bool, error) {
	if session.Spec.VolumeClaimTemplate == nil {
		return true, nil
	}
	claimName := fmt.Sprintf("%s-%s-0", WorkspaceVolumeName, sessionWorkloadName(session))
	var claim corev1.PersistentVolumeClaim
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: claimName}, &claim); apierrors.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("getting Session workspace PersistentVolumeClaim %q for reset: %w", claimName, err)
	}
	owner := metav1.GetControllerOf(&claim)
	ownedByStatefulSet := owner != nil && owner.APIVersion == appsv1.SchemeGroupVersion.String() &&
		owner.Kind == "StatefulSet" && owner.Name == sessionWorkloadName(session) &&
		(statefulSet == nil || owner.UID == statefulSet.UID)
	if !metav1.IsControlledBy(&claim, session) && !ownedByStatefulSet {
		return false, fmt.Errorf("Session workspace PersistentVolumeClaim %q is not controlled by Session %q", claim.Name, session.Name)
	}
	if claim.DeletionTimestamp != nil {
		return false, nil
	}
	if err := r.Delete(ctx, &claim, client.Preconditions{UID: &claim.UID}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("deleting Session workspace PersistentVolumeClaim %q for reset: %w", claim.Name, err)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(session, corev1.EventTypeNormal, "ResetStorageDeleted", "Deleted workspace PersistentVolumeClaim %s", claim.Name)
	}
	return false, nil
}

func (r *SessionReconciler) startSessionAfterReset(ctx context.Context, session *kelos.Session, statefulSet *appsv1.StatefulSet) (ctrl.Result, error) {
	if statefulSet == nil {
		return r.createSessionStatefulSet(ctx, session)
	}
	if statefulSet.DeletionTimestamp != nil {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if sessionSuspended(session) {
		if err := r.setSessionReplicas(ctx, statefulSet, 0); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseSuspended, "Session runtime is suspended", "RuntimeSuspended"); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.clearSessionReset(ctx, session); err != nil {
			return ctrl.Result{}, err
		}
		if r.Recorder != nil {
			r.Recorder.Event(session, corev1.EventTypeNormal, "ResetCompleted", "Completed Session workspace reset")
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if err := r.updateSessionStatus(ctx, session, nil, kelos.SessionPhasePending, "Session Pod is starting with a fresh workspace", "ResetStarting"); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureSessionResetStartPrerequisites(ctx, session, statefulSet); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setSessionReplicas(ctx, statefulSet, 1); err != nil {
		return ctrl.Result{}, err
	}

	var pod corev1.Pod
	key := client.ObjectKey{Namespace: session.Namespace, Name: statefulSet.Name + "-0"}
	if err := r.Get(ctx, key, &pod); apierrors.IsNotFound(err) {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting replacement Session Pod %q after reset: %w", key.Name, err)
	}
	if !metav1.IsControlledBy(&pod, statefulSet) {
		return ctrl.Result{}, fmt.Errorf("replacement Session Pod %q is not controlled by StatefulSet %q", pod.Name, statefulSet.Name)
	}
	if pod.DeletionTimestamp != nil {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	phase, message, reason := sessionPhaseForPod(&pod)
	if err := r.updateSessionStatus(ctx, session, &pod, phase, message, reason); err != nil {
		return ctrl.Result{}, err
	}
	if phase != kelos.SessionPhaseReady {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.clearSessionReset(ctx, session); err != nil {
		return ctrl.Result{}, err
	}
	if r.Recorder != nil {
		r.Recorder.Event(session, corev1.EventTypeNormal, "ResetCompleted", "Completed Session workspace reset")
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *SessionReconciler) ensureSessionResetStartPrerequisites(ctx context.Context, session *kelos.Session, statefulSet *appsv1.StatefulSet) error {
	serviceAccountName := statefulSet.Spec.Template.Spec.ServiceAccountName
	if serviceAccountName == "" {
		serviceAccountName = sessionRuntimeAccessName(session)
	}
	if err := r.ensureSessionRuntimeAccess(ctx, session, serviceAccountName); err != nil {
		return fmt.Errorf("preparing Session %q runtime access after reset: %w", session.Name, err)
	}
	if err := r.ensureSessionService(ctx, session); err != nil {
		return fmt.Errorf("preparing Session %q governing Service after reset: %w", session.Name, err)
	}
	if _, err := r.refreshSessionGitHubAppTokenIfNeeded(ctx, session, &statefulSet.Spec.Template.Spec); err != nil {
		return fmt.Errorf("preparing Session %q GitHub App token after reset: %w", session.Name, err)
	}
	return nil
}

func (r *SessionReconciler) setSessionReplicas(ctx context.Context, statefulSet *appsv1.StatefulSet, replicas int32) error {
	if statefulSet.Spec.Replicas != nil && *statefulSet.Spec.Replicas == replicas {
		return nil
	}
	original := statefulSet.DeepCopy()
	statefulSet.Spec.Replicas = ptr.To(replicas)
	if err := r.Patch(ctx, statefulSet, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("scaling Session StatefulSet %q to %d replicas: %w", statefulSet.Name, replicas, err)
	}
	return nil
}

func (r *SessionReconciler) clearSessionIdleResumeRequest(ctx context.Context, session *kelos.Session) error {
	if !sessionsuspend.ResumeRequested(session) {
		return nil
	}
	original := session.DeepCopy()
	delete(session.Annotations, sessionsuspend.ResumeRequestAnnotation)
	delete(session.Annotations, sessionsuspend.ResumeRequestTimeAnnotation)
	delete(session.Annotations, sessionsuspend.ResumeAcknowledgementAnnotation)
	delete(session.Annotations, sessionsuspend.ResumeAcknowledgementTimeAnnotation)
	if err := r.Patch(ctx, session, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("clearing idle resume request for Session %q: %w", session.Name, err)
	}
	return nil
}

func (r *SessionReconciler) setSessionIdleResumeTime(ctx context.Context, session *kelos.Session, annotation string) error {
	original := session.DeepCopy()
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[annotation] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := r.Patch(ctx, session, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("recording idle resume progress for Session %q: %w", session.Name, err)
	}
	return nil
}

func sessionSuspendedByUser(session *kelos.Session) bool {
	return ptr.Deref(session.Spec.Suspend, false)
}

func sessionSuspended(session *kelos.Session) bool {
	return sessionSuspendedByUser(session) ||
		(sessionsuspend.IsIdlePolicySuspended(session) && !sessionsuspend.ResumeRequested(session))
}

func sessionRuntimeReplicas(session *kelos.Session) int32 {
	if sessionSuspended(session) {
		return 0
	}
	return 1
}

func (r *SessionReconciler) setSessionResetState(ctx context.Context, session *kelos.Session, value string) error {
	original := session.DeepCopy()
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[sessionreset.StateAnnotation] = value
	if err := r.Patch(ctx, session, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("updating Session %q reset state: %w", session.Name, err)
	}
	return nil
}

func (r *SessionReconciler) clearSessionReset(ctx context.Context, session *kelos.Session) error {
	original := session.DeepCopy()
	delete(session.Annotations, sessionreset.RequestAnnotation)
	delete(session.Annotations, sessionreset.StateAnnotation)
	if err := r.Patch(ctx, session, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("completing Session %q reset: %w", session.Name, err)
	}
	return nil
}

func (r *SessionReconciler) reconcileSessionRuntimeUpdate(
	ctx context.Context,
	session *kelos.Session,
	statefulSet *appsv1.StatefulSet,
	pod *corev1.Pod,
	phase kelos.SessionPhase,
) (ctrl.Result, bool, error) {
	if statefulSet.Status.UpdateRevision == "" || statefulSet.Status.ObservedGeneration < statefulSet.Generation {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, true, nil
	}
	// The controller revision remains stable when Pod admission rewrites fields such as container images.
	podCurrent := pod.Labels[appsv1.StatefulSetRevisionLabel] == statefulSet.Status.UpdateRevision
	if podCurrent {
		if !hasSessionRuntimeUpdateAnnotations(session) {
			return ctrl.Result{}, false, nil
		}
		if err := r.clearSessionRuntimeUpdateRequest(ctx, session); err != nil {
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{Requeue: true}, true, nil
	}
	if pod.UID == "" {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, true, nil
	}
	if phase == kelos.SessionPhaseFailed {
		return r.replaceSessionPod(ctx, session, pod)
	}

	request := sessionupdate.NewRequest(pod.UID, statefulSet.Status.UpdateRevision)
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		return ctrl.Result{}, true, err
	}
	if session.Annotations[sessionupdate.RequestAnnotation] != encoded {
		if err := r.setSessionRuntimeUpdateRequest(ctx, session, encoded); err != nil {
			return ctrl.Result{}, true, err
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(session, corev1.EventTypeNormal, "RuntimeUpdateDraining", "Waiting for Session Pod %s to drain before replacing it with its updated runtime", pod.Name)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, true, nil
	}

	force := session.Annotations[sessionupdate.ForceUpdateAnnotation]
	var report sessionupdate.Report
	if value := session.Annotations[sessionupdate.ReportAnnotation]; value != "" {
		report, err = sessionupdate.DecodeReport(value)
		if err != nil {
			return ctrl.Result{}, true, fmt.Errorf("reading runtime update report for Session %q: %w", session.Name, err)
		}
	}
	drained := report.RequestID == request.ID && report.PodUID == pod.UID && report.Phase == sessionupdate.PhaseDrained
	if !drained && force != "true" && force != request.ID {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, true, nil
	}
	return r.replaceSessionPod(ctx, session, pod)
}

func (r *SessionReconciler) replaceSessionPod(ctx context.Context, session *kelos.Session, pod *corev1.Pod) (ctrl.Result, bool, error) {
	if err := r.Delete(ctx, pod, client.Preconditions{UID: &pod.UID}); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, true, fmt.Errorf("deleting Session Pod %q for runtime update: %w", pod.Name, err)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(session, corev1.EventTypeNormal, "RuntimeUpdateReplacing", "Replacing Session Pod %s with its updated runtime", pod.Name)
	}
	return ctrl.Result{Requeue: true}, true, nil
}

func (r *SessionReconciler) setSessionRuntimeUpdateRequest(ctx context.Context, session *kelos.Session, value string) error {
	original := session.DeepCopy()
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[sessionupdate.RequestAnnotation] = value
	delete(session.Annotations, sessionupdate.ReportAnnotation)
	if err := r.Patch(ctx, session, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("requesting runtime drain for Session %q: %w", session.Name, err)
	}
	return nil
}

func (r *SessionReconciler) clearSessionRuntimeUpdateRequest(ctx context.Context, session *kelos.Session) error {
	original := session.DeepCopy()
	delete(session.Annotations, sessionupdate.RequestAnnotation)
	delete(session.Annotations, sessionupdate.ReportAnnotation)
	delete(session.Annotations, sessionupdate.ForceUpdateAnnotation)
	if err := r.Patch(ctx, session, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("clearing runtime update request for Session %q: %w", session.Name, err)
	}
	return nil
}

func hasSessionRuntimeUpdateAnnotations(session *kelos.Session) bool {
	return session.Annotations[sessionupdate.RequestAnnotation] != "" ||
		session.Annotations[sessionupdate.ReportAnnotation] != "" ||
		session.Annotations[sessionupdate.ForceUpdateAnnotation] != ""
}

func (r *SessionReconciler) ensureSessionWorkspaceClaimOwnership(ctx context.Context, session *kelos.Session, statefulSet *appsv1.StatefulSet) error {
	if session.Spec.VolumeClaimTemplate == nil {
		return nil
	}
	for i := range statefulSet.Spec.VolumeClaimTemplates {
		template := &statefulSet.Spec.VolumeClaimTemplates[i]
		key := client.ObjectKey{
			Namespace: statefulSet.Namespace,
			Name:      fmt.Sprintf("%s-%s-0", template.Name, statefulSet.Name),
		}
		var claim corev1.PersistentVolumeClaim
		if err := r.Get(ctx, key, &claim); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("getting Session workspace PersistentVolumeClaim %q: %w", key.Name, err)
		}
		if metav1.IsControlledBy(&claim, session) {
			continue
		}
		if owner := metav1.GetControllerOf(&claim); owner != nil && !metav1.IsControlledBy(&claim, statefulSet) {
			return fmt.Errorf("Session workspace PersistentVolumeClaim %q is controlled by %s %q", claim.Name, owner.Kind, owner.Name)
		}

		original := claim.DeepCopy()
		ownerReferences := make([]metav1.OwnerReference, 0, len(claim.OwnerReferences))
		for _, ownerReference := range claim.OwnerReferences {
			if ownerReference.UID != statefulSet.UID {
				ownerReferences = append(ownerReferences, ownerReference)
			}
		}
		claim.OwnerReferences = ownerReferences
		if err := controllerutil.SetControllerReference(session, &claim, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
			return fmt.Errorf("setting Session owner on workspace PersistentVolumeClaim %q: %w", claim.Name, err)
		}
		if err := r.Patch(ctx, &claim, client.MergeFrom(original)); err != nil {
			return fmt.Errorf("patching Session workspace PersistentVolumeClaim %q ownership: %w", claim.Name, err)
		}
	}
	return nil
}

func (r *SessionReconciler) ensureSessionService(ctx context.Context, session *kelos.Session) error {
	var existing corev1.Service
	key := client.ObjectKey{Namespace: session.Namespace, Name: sessionServiceName(session)}
	if err := r.Get(ctx, key, &existing); err == nil {
		if !metav1.IsControlledBy(&existing, session) {
			return fmt.Errorf("Service %q already exists and is not controlled by this Session", existing.Name)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting Session Service %q: %w", key.Name, err)
	}

	service := buildSessionService(session)
	if err := controllerutil.SetControllerReference(session, service, r.Scheme); err != nil {
		return fmt.Errorf("setting Session owner on Service: %w", err)
	}
	if err := r.Create(ctx, service); err != nil {
		return fmt.Errorf("creating Session Service %q: %w", service.Name, err)
	}
	return nil
}

func sessionGitHubTokenMinimumValidity(session *kelos.Session, statefulSet *appsv1.StatefulSet) time.Duration {
	if statefulSet != nil &&
		(statefulSet.Spec.Replicas == nil || *statefulSet.Spec.Replicas > 0) &&
		session.Status.Phase == kelos.SessionPhaseReady &&
		session.Status.PodName != "" &&
		sessionPodSpecUsesSecret(&statefulSet.Spec.Template.Spec, sessionGitHubTokenSecretName(session.Name)) {
		// A ready runtime can keep using its unexpired token while proactive
		// refresh retries transient failures without taking it offline.
		return 0
	}
	// Creation and any stopped-to-running transition require enough token
	// lifetime to initialize the runtime safely.
	return tokenRefreshMargin
}

func (r *SessionReconciler) resolveSessionInputs(
	ctx context.Context,
	session *kelos.Session,
	minimumGitHubTokenValidity time.Duration,
) (*kelos.WorkspaceSpec, *kelos.AgentConfigSpec, string, error) {
	var workspace *kelos.WorkspaceSpec
	if ref := session.Spec.Worker.WorkspaceRef; ref != nil {
		var value kelos.Workspace
		if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: ref.Name}, &value); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil, fmt.Sprintf("Waiting for Workspace %q", ref.Name), nil
			}
			return nil, nil, "", fmt.Errorf("fetching Workspace %q: %w", ref.Name, err)
		}
		workspace = value.Spec.DeepCopy()
		if workspace.SecretRef != nil {
			resolved, err := r.resolveSessionGitHubAppToken(ctx, session, workspace, minimumGitHubTokenValidity)
			if err != nil {
				return nil, nil, "", err
			}
			workspace = resolved
		}
	}

	refs := session.Spec.Worker.AgentConfigRefs
	if len(refs) == 0 {
		return workspace, nil, "", nil
	}

	specs := make([]kelos.AgentConfigSpec, 0, len(refs))
	for _, ref := range refs {
		var value kelos.AgentConfig
		if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: ref.Name}, &value); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil, fmt.Sprintf("Waiting for AgentConfig %q", ref.Name), nil
			}
			return nil, nil, "", fmt.Errorf("fetching AgentConfig %q: %w", ref.Name, err)
		}
		specs = append(specs, value.Spec)
	}

	agentConfig := MergeAgentConfigs(specs)
	inputClient := sessionInputClient{Client: r.Client}
	if len(agentConfig.Skills) > 0 {
		taskReconciler := TaskReconciler{Client: inputClient}
		if err := taskReconciler.validateSkillsAuthSecrets(ctx, session.Namespace, agentConfig.Skills); err != nil {
			if isSessionInputUnavailable(err) {
				return nil, nil, "", err
			}
			return nil, nil, "", invalidSessionConfiguration(err)
		}
	}
	if len(agentConfig.MCPServers) > 0 {
		resolved, err := resolveMCPServerSecrets(ctx, inputClient, session.Namespace, agentConfig.MCPServers)
		if err != nil {
			if isSessionInputUnavailable(err) {
				return nil, nil, "", err
			}
			return nil, nil, "", invalidSessionConfiguration(err)
		}
		agentConfig.MCPServers = resolved
	}

	return workspace, agentConfig, "", nil
}

func (r *SessionReconciler) resolveSessionGitHubAppToken(
	ctx context.Context,
	session *kelos.Session,
	workspace *kelos.WorkspaceSpec,
	minimumValidity time.Duration,
) (*kelos.WorkspaceSpec, error) {
	var source corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: workspace.SecretRef.Name}, &source); err != nil {
		return nil, fmt.Errorf("fetching Workspace Secret %q: %w", workspace.SecretRef.Name, err)
	}
	if !githubapp.IsGitHubApp(source.Data) {
		return workspace, nil
	}
	if r.TokenClient == nil {
		return nil, invalidSessionConfiguration(errors.New("GitHub App Secret detected but TokenClient is not configured"))
	}
	tokenClient := sessionGitHubTokenClient(r.TokenClient, workspace.Repo)
	fingerprint := sessionGitHubTokenMintFingerprint(&source, tokenClient.BaseURL)

	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	var existing corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: tokenSecretName}, &existing); err == nil {
		if !metav1.IsControlledBy(&existing, session) {
			return nil, invalidSessionConfiguration(fmt.Errorf("GitHub token Secret %q already exists and is not controlled by this Session", tokenSecretName))
		}
		if sessionGitHubTokenSecretReusable(
			&existing,
			workspace.SecretRef.Name,
			fingerprint,
			minimumValidity,
			time.Now(),
		) {
			resolved := workspace.DeepCopy()
			resolved.SecretRef = &kelos.SecretReference{Name: tokenSecretName}
			return resolved, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting existing Session GitHub token Secret: %w", err)
	}
	credentials, err := githubapp.ParseCredentials(source.Data)
	if err != nil {
		return nil, invalidSessionConfiguration(fmt.Errorf("parsing GitHub App credentials: %w", err))
	}
	response, err := tokenClient.GenerateInstallationToken(ctx, credentials)
	if err != nil {
		return nil, fmt.Errorf("generating GitHub App installation token: %w", err)
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tokenSecretName,
			Namespace: session.Namespace,
			Annotations: map[string]string{
				githubAppSecretAnnotation:         workspace.SecretRef.Name,
				sessionTokenFingerprintAnnotation: fingerprint,
				tokenExpiresAtAnnotation:          response.ExpiresAt.UTC().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{GitHubTokenSecretKey: []byte(response.Token)},
	}
	if err := controllerutil.SetControllerReference(session, tokenSecret, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting Session owner on GitHub token Secret: %w", err)
	}
	if err := r.Create(ctx, tokenSecret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating Session GitHub token Secret: %w", err)
		}
		var existing corev1.Secret
		if err := r.Get(ctx, client.ObjectKeyFromObject(tokenSecret), &existing); err != nil {
			return nil, fmt.Errorf("getting existing Session GitHub token Secret: %w", err)
		}
		if !metav1.IsControlledBy(&existing, session) {
			return nil, invalidSessionConfiguration(fmt.Errorf("GitHub token Secret %q already exists and is not controlled by this Session", tokenSecretName))
		}
		existing.Data = tokenSecret.Data
		existing.Annotations = tokenSecret.Annotations
		if err := r.Update(ctx, &existing); err != nil {
			return nil, fmt.Errorf("updating Session GitHub token Secret: %w", err)
		}
	}
	resolved := workspace.DeepCopy()
	resolved.SecretRef = &kelos.SecretReference{Name: tokenSecretName}
	return resolved, nil
}

func sessionGitHubTokenSecretReusable(
	secret *corev1.Secret,
	sourceName string,
	fingerprint string,
	minimumValidity time.Duration,
	now time.Time,
) bool {
	if secret.Annotations[githubAppSecretAnnotation] != sourceName ||
		secret.Annotations[sessionTokenFingerprintAnnotation] != fingerprint ||
		len(secret.Data[GitHubTokenSecretKey]) == 0 {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, secret.Annotations[tokenExpiresAtAnnotation])
	return err == nil && now.Before(expiresAt.Add(-minimumValidity))
}

func (r *SessionReconciler) refreshSessionGitHubAppTokenIfNeeded(ctx context.Context, session *kelos.Session, podSpec *corev1.PodSpec) (time.Duration, error) {
	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	if !sessionPodSpecUsesSecret(podSpec, tokenSecretName) {
		return 0, nil
	}
	var tokenSecret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: tokenSecretName}, &tokenSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return r.recreateSessionGitHubAppToken(ctx, session)
		}
		return 0, err
	}
	sourceName := tokenSecret.Annotations[githubAppSecretAnnotation]
	expiresAtText := tokenSecret.Annotations[tokenExpiresAtAnnotation]
	if sourceName == "" || expiresAtText == "" {
		return 0, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtText)
	if err != nil {
		return 0, fmt.Errorf("parsing Session GitHub token expiration: %w", err)
	}
	next := time.Until(expiresAt.Add(-tokenRefreshMargin))
	if next > 0 {
		return next, nil
	}
	if r.TokenClient == nil {
		return 0, errors.New("TokenClient is not configured")
	}

	var source corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: sourceName}, &source); err != nil {
		return 0, fmt.Errorf("fetching source GitHub App Secret %q: %w", sourceName, err)
	}
	credentials, err := githubapp.ParseCredentials(source.Data)
	if err != nil {
		return 0, fmt.Errorf("parsing GitHub App credentials: %w", err)
	}
	repo := ""
	if ref := session.Spec.Worker.WorkspaceRef; ref != nil {
		var workspace kelos.Workspace
		if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: ref.Name}, &workspace); err != nil {
			return 0, fmt.Errorf("fetching Workspace %q for token refresh: %w", ref.Name, err)
		}
		repo = workspace.Spec.Repo
	}
	response, err := sessionGitHubTokenClient(r.TokenClient, repo).GenerateInstallationToken(ctx, credentials)
	if err != nil {
		return 0, fmt.Errorf("refreshing GitHub App installation token: %w", err)
	}
	if tokenSecret.Data == nil {
		tokenSecret.Data = map[string][]byte{}
	}
	tokenSecret.Data[GitHubTokenSecretKey] = []byte(response.Token)
	tokenSecret.Annotations[tokenExpiresAtAnnotation] = response.ExpiresAt.UTC().Format(time.RFC3339)
	if err := r.Update(ctx, &tokenSecret); err != nil {
		return 0, fmt.Errorf("updating Session GitHub token Secret: %w", err)
	}
	return time.Until(response.ExpiresAt.Add(-tokenRefreshMargin)), nil
}

func sessionPodSpecUsesSecret(podSpec *corev1.PodSpec, name string) bool {
	for _, volume := range podSpec.Volumes {
		if volume.Name == GitHubTokenVolumeName && volume.Secret != nil && volume.Secret.SecretName == name {
			return true
		}
	}
	return false
}

func (r *SessionReconciler) recreateSessionGitHubAppToken(ctx context.Context, session *kelos.Session) (time.Duration, error) {
	if session.Spec.Worker.WorkspaceRef == nil {
		return 0, nil
	}
	var workspace kelos.Workspace
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: session.Spec.Worker.WorkspaceRef.Name}, &workspace); err != nil {
		return 0, fmt.Errorf("fetching Workspace %q for Session GitHub token recovery: %w", session.Spec.Worker.WorkspaceRef.Name, err)
	}
	if workspace.Spec.SecretRef == nil {
		return 0, nil
	}
	resolved, err := r.resolveSessionGitHubAppToken(ctx, session, workspace.Spec.DeepCopy(), tokenRefreshMargin)
	if err != nil {
		return 0, err
	}
	if resolved.SecretRef.Name != sessionGitHubTokenSecretName(session.Name) {
		return 0, nil
	}
	return tokenRefreshRetryInterval, nil
}

func sessionGitHubTokenClient(base *githubapp.TokenClient, repo string) *githubapp.TokenClient {
	client := &githubapp.TokenClient{BaseURL: base.BaseURL, Client: base.Client}
	if host, _, _ := parseGitHubRepo(repo); host != "" {
		if apiURL := gitHubAPIBaseURL(host); apiURL != "" {
			client.BaseURL = apiURL
		}
	}
	return client
}

func sessionGitHubTokenMintFingerprint(source *corev1.Secret, apiBaseURL string) string {
	value := source.Namespace + "\x00" +
		source.Name + "\x00" +
		string(source.UID) + "\x00" +
		source.ResourceVersion + "\x00" +
		apiBaseURL
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func sessionGitHubTokenSecretName(sessionName string) string {
	return truncateResourceName(sessionName + "-session-github-token")
}

func (r *SessionReconciler) buildSessionStatefulSet(session *kelos.Session, workspace *kelos.WorkspaceSpec, agentConfig *kelos.AgentConfigSpec) (*appsv1.StatefulSet, *corev1.ConfigMap, error) {
	worker := session.Spec.Worker.DeepCopy()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace},
		Spec: kelos.TaskSpec{
			Worker: worker,
			Prompt: "session",
			Branch: session.Spec.InitialBranch,
		},
	}

	job, err := r.JobBuilder.Build(task, workspace, agentConfig, "session")
	if err != nil {
		return nil, nil, err
	}

	var configMap *corev1.ConfigMap
	if agentConfig != nil && len(agentConfig.Plugins) > 0 {
		configMap, err = buildPluginConfigMap(task, agentConfig.Plugins)
		if err != nil {
			return nil, nil, err
		}
		configMap.Name = sessionPluginConfigMapName(session)
	}

	podSpec := *job.Spec.Template.Spec.DeepCopy()
	if configMap != nil {
		found := false
		for i := range podSpec.Volumes {
			volume := &podSpec.Volumes[i]
			if volume.Name != PluginStagingVolumeName || volume.ConfigMap == nil {
				continue
			}
			volume.ConfigMap.Name = configMap.Name
			found = true
			break
		}
		if !found {
			return nil, nil, errors.New("agent Pod has no plugin ConfigMap volume")
		}
	}
	podSpec.RestartPolicy = corev1.RestartPolicyAlways
	podSpec.ActiveDeadlineSeconds = job.Spec.ActiveDeadlineSeconds
	if podSpec.SecurityContext == nil {
		podSpec.SecurityContext = &corev1.PodSecurityContext{}
	}
	if podSpec.SecurityContext.FSGroup == nil {
		agentUID := AgentUID
		podSpec.SecurityContext.FSGroup = &agentUID
	}

	if len(podSpec.Containers) == 0 {
		return nil, nil, fmt.Errorf("agent Pod has no containers")
	}
	mainContainer := &podSpec.Containers[0]
	useTini := worker.Image == "" && isBundledAgentImage(mainContainer.Image)
	mainContainer.Command = agentProcessCommand(sessionRuntimeBinary, useTini)
	mainContainer.Args = []string{"serve"}
	setSessionContainerEnv(mainContainer, "KELOS_SESSION_NAME", session.Name)
	setSessionContainerEnv(mainContainer, "KELOS_SESSION_NAMESPACE", session.Namespace)
	setSessionContainerEnvVar(mainContainer, corev1.EnvVar{
		Name: "KELOS_SESSION_POD_UID",
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
			APIVersion: "v1",
			FieldPath:  "metadata.uid",
		}},
	})
	switch worker.Type {
	case "claude-code":
		setSessionContainerEnv(mainContainer, "CLAUDE_CONFIG_DIR", sessionClaudeConfigDir)
	case "codex":
		setSessionContainerEnv(mainContainer, "CODEX_HOME", sessionCodexHome)
	case "opencode":
		setSessionContainerEnv(mainContainer, "OPENCODE_CONFIG_DIR", sessionOpenCodeConfigDir)
		setSessionContainerEnv(mainContainer, "XDG_DATA_HOME", sessionOpenCodeDataDir)
	}
	mainContainer.ReadinessProbe = &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{sessionRuntimeBinary, "health"}}},
		InitialDelaySeconds: 1,
		PeriodSeconds:       2,
		TimeoutSeconds:      1,
		FailureThreshold:    15,
	}
	mainContainer.VolumeMounts = append(mainContainer.VolumeMounts, corev1.VolumeMount{
		Name:      sessionRuntimeVolumeName,
		MountPath: sessionRuntimeMountPath,
		ReadOnly:  true,
	})

	workspaceVolume := -1
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == WorkspaceVolumeName {
			workspaceVolume = i
			break
		}
	}
	if workspaceVolume == -1 {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name:         WorkspaceVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		workspaceVolume = len(podSpec.Volumes) - 1
		mainContainer.VolumeMounts = append(mainContainer.VolumeMounts, corev1.VolumeMount{
			Name:      WorkspaceVolumeName,
			MountPath: WorkspaceMountPath,
		})
		mainContainer.WorkingDir = WorkspaceMountPath
	}
	var volumeClaimTemplates []corev1.PersistentVolumeClaim
	var retentionPolicy *appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy
	if session.Spec.VolumeClaimTemplate == nil {
		podSpec.Volumes[workspaceVolume].VolumeSource = corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	} else {
		podSpec.Volumes = append(podSpec.Volumes[:workspaceVolume], podSpec.Volumes[workspaceVolume+1:]...)
		claimOwnerReference := *metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))
		claimOwnerReference.BlockOwnerDeletion = ptr.To(false)
		volumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name:            WorkspaceVolumeName,
				OwnerReferences: []metav1.OwnerReference{claimOwnerReference},
			},
			Spec: *session.Spec.VolumeClaimTemplate.DeepCopy(),
		}}
		retentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
			WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
		}
	}
	credentialHelper := ""
	provider := workspaceProviderFor(workspace)
	if workspace != nil && workspace.SecretRef != nil {
		credentialHelper = gitCredentialHelper(provider)
	}
	if err := prepareSessionWorkspaceInit(podSpec.InitContainers, credentialHelper, provider.gitUsername); err != nil {
		return nil, nil, err
	}

	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name:         sessionRuntimeVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	podSpec.InitContainers = append([]corev1.Container{{
		Name:            sessionRuntimeContainerName,
		Image:           r.SessionRuntimeImage,
		ImagePullPolicy: r.SessionRuntimeImagePullPolicy,
		Args:            []string{"--self-copy", sessionRuntimeBinary},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
			RunAsNonRoot: ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      sessionRuntimeVolumeName,
			MountPath: sessionRuntimeMountPath,
		}},
	}}, podSpec.InitContainers...)
	if podSpec.ServiceAccountName == "" {
		podSpec.ServiceAccountName = sessionRuntimeAccessName(session)
	}
	podSpec.AutomountServiceAccountToken = ptr.To(true)

	labels := make(map[string]string, len(job.Labels)+1)
	for key, value := range job.Labels {
		if key != "kelos.dev/task" {
			labels[key] = value
		}
	}
	labels["kelos.dev/component"] = "session"
	labels["kelos.dev/session"] = sessionLabelValue(session)

	selector := sessionSelectorLabels(session)
	templateAnnotations := map[string]string{sessionNameAnnotation: session.Name}
	if configMap != nil {
		checksum, err := sessionPluginConfigMapChecksum(configMap)
		if err != nil {
			return nil, nil, err
		}
		templateAnnotations[sessionPluginChecksumAnnotation] = checksum
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionWorkloadName(session),
			Namespace: session.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:                             ptr.To(sessionRuntimeReplicas(session)),
			ServiceName:                          sessionServiceName(session),
			PodManagementPolicy:                  appsv1.OrderedReadyPodManagement,
			Selector:                             &metav1.LabelSelector{MatchLabels: selector},
			UpdateStrategy:                       appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
			RevisionHistoryLimit:                 ptr.To(int32(10)),
			PersistentVolumeClaimRetentionPolicy: retentionPolicy,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: templateAnnotations,
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: volumeClaimTemplates,
		},
	}
	return statefulSet, configMap, nil
}

func sessionPluginConfigMapChecksum(configMap *corev1.ConfigMap) (string, error) {
	content, err := json.Marshal(struct {
		Data       map[string]string `json:"data,omitempty"`
		BinaryData map[string][]byte `json:"binaryData,omitempty"`
	}{
		Data:       configMap.Data,
		BinaryData: configMap.BinaryData,
	})
	if err != nil {
		return "", fmt.Errorf("marshalling Session plugin ConfigMap content: %w", err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func buildSessionService(session *kelos.Session) *corev1.Service {
	labels := sessionSelectorLabels(session)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionServiceName(session),
			Namespace: session.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  labels,
		},
	}
}

func sessionWorkloadName(session *kelos.Session) string {
	return truncateResourceNameTo(session.Name, sessionWorkloadNameMaxLength)
}

func sessionServiceName(session *kelos.Session) string {
	return truncateResourceName("s-" + session.Name)
}

func sessionSelectorLabels(session *kelos.Session) map[string]string {
	return map[string]string{
		"kelos.dev/component": "session",
		"kelos.dev/session":   sessionLabelValue(session),
	}
}

func prepareSessionWorkspaceInit(containers []corev1.Container, credentialHelper, credentialUsername string) error {
	for i := range containers {
		container := &containers[i]
		switch container.Name {
		case "git-clone":
			initializedAction := "exit 0"
			if credentialHelper != "" {
				initializedAction = fmt.Sprintf(
					`{ %s; } || exit $?; exit 0`,
					workspaceGitCredentialConfigScript(credentialHelper, credentialUsername),
				)
			}
			prefix := `if [ -f ` + sessionInitializedPath + ` ]; then ` + initializedAction + `; fi
rm -rf -- /workspace/repo
`
			if len(container.Command) == 0 {
				originalArgs := append([]string(nil), container.Args...)
				container.Command = []string{"sh", "-c"}
				container.Args = append([]string{prefix + `exec git "$@"`, "git"}, originalArgs...)
			} else if len(container.Command) == 3 && container.Command[0] == "sh" && container.Command[1] == "-c" {
				container.Command[2] = prefix + container.Command[2]
			} else {
				return fmt.Errorf("Session workspace init container %q has an unsupported command", container.Name)
			}
		case "remote-setup", "branch-setup", "workspace-files":
			if len(container.Command) != 3 || container.Command[0] != "sh" || container.Command[1] != "-c" {
				return fmt.Errorf("Session workspace init container %q has an unsupported command", container.Name)
			}
			container.Command[2] = `if [ -f ` + sessionInitializedPath + ` ]; then exit 0; fi
` + container.Command[2]
		}
	}
	return nil
}

func setSessionContainerEnv(container *corev1.Container, name, value string) {
	setSessionContainerEnvVar(container, corev1.EnvVar{Name: name, Value: value})
}

func setSessionContainerEnvVar(container *corev1.Container, value corev1.EnvVar) {
	for i := range container.Env {
		if container.Env[i].Name == value.Name {
			container.Env[i] = value
			return
		}
	}
	container.Env = append(container.Env, value)
}

func sessionPluginConfigMapName(session *kelos.Session) string {
	identity := string(session.UID)
	if identity == "" {
		identity = session.Namespace + "/" + session.Name
	}
	sum := sha256.Sum256([]byte(identity))
	return "session-" + hex.EncodeToString(sum[:16]) + "-plugins"
}

func sessionLabelValue(session *kelos.Session) string {
	if len(session.Name) <= 63 {
		return session.Name
	}
	if session.UID != "" && len(session.UID) <= 63 {
		return string(session.UID)
	}
	sum := sha256.Sum256([]byte(session.Name))
	return hex.EncodeToString(sum[:16])
}

// sessionIdleExpired reports whether an idle Session has exceeded its idle
// delete policy. It returns (true, 0) if the Session should be deleted now, or
// (false, duration) if it should be requeued after the given duration. A Session
// is only considered idle when its Active condition is explicitly False; an
// active or unknown turn never counts as idle.
func sessionIdleExpired(session *kelos.Session) (bool, time.Duration) {
	if session.Spec.IdlePolicy == nil {
		return false, 0
	}
	return sessionIdlePolicyExpired(session, session.Spec.IdlePolicy.DeleteAfterSeconds)
}

// sessionIdleSuspendExpired reports whether an idle Session has exceeded its
// idle suspend policy.
func sessionIdleSuspendExpired(session *kelos.Session) (bool, time.Duration) {
	if session.Spec.IdlePolicy == nil || sessionSuspendedByUser(session) {
		return false, 0
	}
	return sessionIdlePolicyExpired(session, session.Spec.IdlePolicy.SuspendAfterSeconds)
}

func sessionIdlePolicyExpired(session *kelos.Session, afterSeconds *int32) (bool, time.Duration) {
	if afterSeconds == nil || sessionsuspend.ResumeRequested(session) {
		return false, 0
	}
	active := apiMeta.FindStatusCondition(session.Status.Conditions, kelos.SessionConditionActive)
	if (active == nil || active.Status != metav1.ConditionFalse) && !sessionsuspend.IsIdlePolicySuspended(session) {
		return false, 0
	}
	ttl := time.Duration(*afterSeconds) * time.Second
	expireAt := sessionIdleSince(session).Add(ttl)
	remaining := time.Until(expireAt)
	if remaining <= 0 {
		return true, 0
	}
	return false, remaining
}

// sessionIdleSince returns the time from which Session idleness is measured: the
// later of the Session creation time and the last reported activity time. The
// Active condition transition is deliberately not consulted: status.lastActivityTime
// is preserved across Pod replacement, so measuring from it keeps the idle clock
// running when a replacement Pod re-reports Active=False without any user activity.
func sessionIdleSince(session *kelos.Session) time.Time {
	since := session.CreationTimestamp.Time
	if last := session.Status.LastActivityTime; last != nil && last.After(since) {
		since = last.Time
	}
	return since
}

// installedSessionIdleDrainRequest returns the idle-drain request already
// installed on the Session for the given Pod, if any. The request is reused
// across reconciles so its ID stays stable while the drain is in progress; it is
// only treated as installed when it decodes and targets the current Pod.
func installedSessionIdleDrainRequest(session *kelos.Session, pod *corev1.Pod) (sessionupdate.Request, bool) {
	value := session.Annotations[sessionupdate.IdleDrainRequestAnnotation]
	if value == "" {
		return sessionupdate.Request{}, false
	}
	request, err := sessionupdate.Decode(value)
	if err != nil || request.PodUID != pod.UID {
		return sessionupdate.Request{}, false
	}
	return request, true
}

// newSessionIdleDrainRequest mints a drain request whose ID is unique to this
// drain episode. A random ID (rather than one derived from the idle-start time)
// guarantees that a Drained report left over from a previous idle period cannot
// be mistaken for an acknowledgement of the current request: metav1.Time is
// persisted with whole-second precision, so an ID salted with the idle-start
// time collides for distinct idle periods that begin within the same second
// (most acutely with deleteAfterSeconds: 0). The ID is persisted on the Session
// and reused via installedSessionIdleDrainRequest, so it remains stable across
// reconciles of the same episode.
func newSessionIdleDrainRequest(pod *corev1.Pod) sessionupdate.Request {
	return sessionupdate.Request{ID: uuid.NewString(), PodUID: pod.UID}
}

func sessionPhaseForPod(pod *corev1.Pod) (kelos.SessionPhase, string, string) {
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		message := pod.Status.Message
		if message == "" {
			message = fmt.Sprintf("Session Pod entered phase %s", pod.Status.Phase)
		}
		return kelos.SessionPhaseFailed, message, "PodTerminated"
	}
	if message, reason, failed := sessionContainerFailure(pod); failed {
		return kelos.SessionPhaseFailed, message, reason
	}
	if condition := findPodCondition(pod.Status.Conditions, corev1.PodReady); condition != nil && condition.Status == corev1.ConditionTrue {
		return kelos.SessionPhaseReady, "Session runtime is ready", "RuntimeReady"
	}
	return kelos.SessionPhasePending, "Session Pod is starting", "PodStarting"
}

func sessionContainerFailure(pod *corev1.Pod) (string, string, bool) {
	failureReasons := map[string]struct{}{
		"CrashLoopBackOff":           {},
		"CreateContainerConfigError": {},
		"CreateContainerError":       {},
		"ErrImagePull":               {},
		"ImagePullBackOff":           {},
		"InvalidImageName":           {},
		"RunContainerError":          {},
	}
	statusGroups := [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses}
	for _, statuses := range statusGroups {
		for _, status := range statuses {
			waiting := status.State.Waiting
			if waiting == nil {
				continue
			}
			if _, failed := failureReasons[waiting.Reason]; !failed {
				continue
			}
			message := fmt.Sprintf("Session container %q is waiting: %s", status.Name, waiting.Reason)
			if waiting.Message != "" {
				message += ": " + waiting.Message
			}
			return message, waiting.Reason, true
		}
	}
	return "", "", false
}

func findPodCondition(conditions []corev1.PodCondition, conditionType corev1.PodConditionType) *corev1.PodCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func (r *SessionReconciler) updateSessionStatus(ctx context.Context, session *kelos.Session, pod *corev1.Pod, phase kelos.SessionPhase, message, reason string) error {
	original := session.Status.DeepCopy()
	session.Status.ObservedGeneration = session.Generation
	session.Status.Phase = phase
	session.Status.Message = message
	if phase == kelos.SessionPhaseSuspended {
		session.Status.PodName = ""
		session.Status.PodUID = ""
	}
	if pod == nil || phase != kelos.SessionPhaseReady || session.Status.PodUID != pod.UID {
		session.Status.Model = ""
		session.Status.Branch = ""
		session.Status.PullRequest = nil
		if session.Status.LastActivityTime == nil {
			active := apiMeta.FindStatusCondition(session.Status.Conditions, kelos.SessionConditionActive)
			if active != nil && active.Status != metav1.ConditionUnknown && !active.LastTransitionTime.IsZero() {
				lastActivityTime := active.LastTransitionTime
				session.Status.LastActivityTime = &lastActivityTime
			}
		}
		activeReason := "RuntimeStatusUnknown"
		activeMessage := "Session runtime activity has not been reported"
		if phase == kelos.SessionPhaseSuspended {
			activeReason = "RuntimeSuspended"
			activeMessage = "Session runtime is suspended"
		}
		apiMeta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{
			Type:               kelos.SessionConditionActive,
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: session.Generation,
			Reason:             activeReason,
			Message:            activeMessage,
		})
	}
	if pod != nil {
		session.Status.PodName = pod.Name
		session.Status.PodUID = pod.UID
	}
	conditionStatus := metav1.ConditionFalse
	if phase == kelos.SessionPhaseReady {
		conditionStatus = metav1.ConditionTrue
	}
	apiMeta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{
		Type:               kelos.SessionConditionReady,
		Status:             conditionStatus,
		ObservedGeneration: session.Generation,
		Reason:             reason,
		Message:            message,
	})

	if reflect.DeepEqual(*original, session.Status) {
		return nil
	}
	if err := r.Status().Update(ctx, session); err != nil {
		return fmt.Errorf("updating Session %q status: %w", session.Name, err)
	}
	return nil
}

// SetupWithManager sets up the Session controller with the Manager.
func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.Session{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&kelos.Workspace{}, handler.EnqueueRequestsFromMapFunc(r.findSessionsForWorkspace)).
		Watches(&kelos.AgentConfig{}, handler.EnqueueRequestsFromMapFunc(r.findSessionsForAgentConfig)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findSessionsForSecret)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.findSessionForPod)).
		Complete(r)
}

func (r *SessionReconciler) findSessionsForWorkspace(ctx context.Context, obj client.Object) []reconcile.Request {
	workspace, ok := obj.(*kelos.Workspace)
	if !ok {
		return nil
	}
	var sessions kelos.SessionList
	if err := r.List(ctx, &sessions, client.InNamespace(workspace.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range sessions.Items {
		session := &sessions.Items[i]
		if session.Spec.Worker.WorkspaceRef != nil && session.Spec.Worker.WorkspaceRef.Name == workspace.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(session)})
		}
	}
	return requests
}

func (r *SessionReconciler) findSessionsForAgentConfig(ctx context.Context, obj client.Object) []reconcile.Request {
	agentConfig, ok := obj.(*kelos.AgentConfig)
	if !ok {
		return nil
	}
	var sessions kelos.SessionList
	if err := r.List(ctx, &sessions, client.InNamespace(agentConfig.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range sessions.Items {
		session := &sessions.Items[i]
		for _, ref := range session.Spec.Worker.AgentConfigRefs {
			if ref.Name == agentConfig.Name {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(session)})
				break
			}
		}
	}
	return requests
}

func (r *SessionReconciler) findSessionsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	var workspaces kelos.WorkspaceList
	if err := r.List(ctx, &workspaces, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}
	workspaceNames := make(map[string]struct{})
	for i := range workspaces.Items {
		workspace := &workspaces.Items[i]
		if workspace.Spec.SecretRef != nil && workspace.Spec.SecretRef.Name == secret.Name {
			workspaceNames[workspace.Name] = struct{}{}
		}
	}
	if len(workspaceNames) == 0 {
		return nil
	}

	var sessions kelos.SessionList
	if err := r.List(ctx, &sessions, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range sessions.Items {
		session := &sessions.Items[i]
		if session.Spec.Worker.WorkspaceRef == nil {
			continue
		}
		if _, ok := workspaceNames[session.Spec.Worker.WorkspaceRef.Name]; ok {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(session)})
		}
	}
	return requests
}

func (r *SessionReconciler) findSessionForPod(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetAnnotations()[sessionNameAnnotation]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: name}}}
}
