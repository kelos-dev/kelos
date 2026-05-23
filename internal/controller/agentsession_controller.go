package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

const agentSessionFinalizer = "kelos.dev/agentsession-finalizer"

// AgentSessionReconciler reconciles AgentSession objects.
type AgentSessionReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	JobBuilder  *JobBuilder
	Clientset   kubernetes.Interface
	TokenClient *githubapp.TokenClient
	Recorder    record.EventRecorder
}

// +kubebuilder:rbac:groups=kelos.dev,resources=agentsessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kelos.dev,resources=agentsessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=agentsessions/finalizers,verbs=update
// +kubebuilder:rbac:groups=kelos.dev,resources=agentturns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kelos.dev,resources=agentturns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=agentconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *AgentSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var session kelosv1alpha1.AgentSession
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !session.DeletionTimestamp.IsZero() {
		return r.handleAgentSessionDeletion(ctx, &session)
	}

	if !controllerutil.ContainsFinalizer(&session, agentSessionFinalizer) {
		controllerutil.AddFinalizer(&session, agentSessionFinalizer)
		if err := r.Update(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if session.Spec.TaskTemplateSnapshot.Type != AgentTypeCodex {
		return r.setAgentSessionPhase(ctx, &session, kelosv1alpha1.AgentSessionPhaseError, "AgentSession only supports codex task templates")
	}

	if session.Status.Phase == kelosv1alpha1.AgentSessionPhaseClosed ||
		session.Status.Phase == kelosv1alpha1.AgentSessionPhaseError {
		return ctrl.Result{}, nil
	}

	var job batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: session.Name}, &job)
	if apierrors.IsNotFound(err) {
		return r.createAgentSessionJob(ctx, &session)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if podName := r.latestRunnerPodName(ctx, &session); podName != "" && session.Status.RunnerPodName != podName {
		if err := r.patchAgentSessionStatus(ctx, &session, func(s *kelosv1alpha1.AgentSession) {
			s.Status.RunnerPodName = podName
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	if isJobFailed(&job) {
		return r.setAgentSessionPhase(ctx, &session, kelosv1alpha1.AgentSessionPhaseError, "Session runner job failed")
	}
	if job.Status.Active > 0 {
		return r.syncAgentSessionRunningState(ctx, &session)
	}
	if job.Status.Succeeded > 0 {
		return r.setAgentSessionPhase(ctx, &session, kelosv1alpha1.AgentSessionPhaseClosed, "Session runner completed")
	}

	logger.V(1).Info("AgentSession runner job not active yet", "job", job.Name)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *AgentSessionReconciler) createAgentSessionJob(ctx context.Context, session *kelosv1alpha1.AgentSession) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	workspace, err := r.resolveSessionWorkspace(ctx, session)
	if err != nil {
		logger.Error(err, "Unable to resolve session workspace")
		return r.setAgentSessionPhase(ctx, session, kelosv1alpha1.AgentSessionPhaseError, err.Error())
	}
	agentConfig, err := r.resolveSessionAgentConfig(ctx, session)
	if err != nil {
		logger.Error(err, "Unable to resolve session AgentConfig")
		return r.setAgentSessionPhase(ctx, session, kelosv1alpha1.AgentSessionPhaseError, err.Error())
	}
	job, err := r.JobBuilder.BuildSessionRunner(session, workspace, agentConfig)
	if err != nil {
		logger.Error(err, "Unable to build session runner job")
		return r.setAgentSessionPhase(ctx, session, kelosv1alpha1.AgentSessionPhaseError, err.Error())
	}
	if err := controllerutil.SetControllerReference(session, job, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.recordEvent(session, corev1.EventTypeNormal, "SessionRunnerCreated", "Created session runner job %s", job.Name)
	if err := r.patchAgentSessionStatus(ctx, session, func(s *kelosv1alpha1.AgentSession) {
		now := metav1.Now()
		s.Status.Phase = kelosv1alpha1.AgentSessionPhaseStarting
		s.Status.RunnerJobName = job.Name
		s.Status.LastActivityAt = &now
		s.Status.Message = "Session runner job created"
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *AgentSessionReconciler) resolveSessionWorkspace(ctx context.Context, session *kelosv1alpha1.AgentSession) (*kelosv1alpha1.WorkspaceSpec, error) {
	if session.Spec.TaskTemplateSnapshot.WorkspaceRef == nil {
		return nil, nil
	}
	var ws kelosv1alpha1.Workspace
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: session.Spec.TaskTemplateSnapshot.WorkspaceRef.Name}, &ws); err != nil {
		return nil, fmt.Errorf("fetching Workspace %q: %w", session.Spec.TaskTemplateSnapshot.WorkspaceRef.Name, err)
	}
	workspace := ws.Spec.DeepCopy()
	if workspace.SecretRef != nil {
		resolved, err := r.resolveSessionGitHubAppToken(ctx, session, workspace)
		if err != nil {
			return nil, err
		}
		workspace = resolved
	}
	return workspace, nil
}

func (r *AgentSessionReconciler) resolveSessionAgentConfig(ctx context.Context, session *kelosv1alpha1.AgentSession) (*kelosv1alpha1.AgentConfigSpec, error) {
	spec := taskSpecFromTemplate(session.Spec.TaskTemplateSnapshot)
	refs := ResolveAgentConfigRefs(&spec)
	if len(refs) == 0 {
		return nil, nil
	}
	var specs []kelosv1alpha1.AgentConfigSpec
	for _, ref := range refs {
		var ac kelosv1alpha1.AgentConfig
		if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: ref.Name}, &ac); err != nil {
			return nil, fmt.Errorf("fetching AgentConfig %q: %w", ref.Name, err)
		}
		specs = append(specs, ac.Spec)
	}
	merged := MergeAgentConfigs(specs)
	if len(merged.MCPServers) > 0 {
		resolver := &TaskReconciler{Client: r.Client}
		resolved, err := resolver.resolveMCPServerSecrets(ctx, session.Namespace, merged.MCPServers)
		if err != nil {
			return nil, err
		}
		merged.MCPServers = resolved
	}
	return merged, nil
}

func (r *AgentSessionReconciler) resolveSessionGitHubAppToken(ctx context.Context, session *kelosv1alpha1.AgentSession, workspace *kelosv1alpha1.WorkspaceSpec) (*kelosv1alpha1.WorkspaceSpec, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: workspace.SecretRef.Name}, &secret); err != nil {
		return nil, fmt.Errorf("fetching workspace secret %q: %w", workspace.SecretRef.Name, err)
	}
	if !githubapp.IsGitHubApp(secret.Data) {
		return workspace, nil
	}
	if r.TokenClient == nil {
		return nil, fmt.Errorf("GitHub App secret detected but TokenClient is not configured")
	}
	creds, err := githubapp.ParseCredentials(secret.Data)
	if err != nil {
		return nil, fmt.Errorf("parsing GitHub App credentials: %w", err)
	}
	tc := &githubapp.TokenClient{BaseURL: r.TokenClient.BaseURL, Client: r.TokenClient.Client}
	if workspace.Repo != "" {
		host, _, _ := parseGitHubRepo(workspace.Repo)
		if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
			tc.BaseURL = apiBaseURL
		}
	}
	tokenResp, err := tc.GenerateInstallationToken(ctx, creds)
	if err != nil {
		return nil, fmt.Errorf("generating installation token: %w", err)
	}
	tokenSecretName := session.Name + "-github-token"
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: tokenSecretName, Namespace: session.Namespace},
		StringData: map[string]string{
			"GITHUB_TOKEN": tokenResp.Token,
		},
	}
	if err := controllerutil.SetControllerReference(session, tokenSecret, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting owner reference on token secret: %w", err)
	}
	if err := r.Create(ctx, tokenSecret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating token secret: %w", err)
		}
		var existing corev1.Secret
		if getErr := r.Get(ctx, client.ObjectKey{Name: tokenSecretName, Namespace: session.Namespace}, &existing); getErr != nil {
			return nil, fmt.Errorf("fetching existing token secret: %w", getErr)
		}
		existing.StringData = tokenSecret.StringData
		if updateErr := r.Update(ctx, &existing); updateErr != nil {
			return nil, fmt.Errorf("updating token secret: %w", updateErr)
		}
	}
	resolved := workspace.DeepCopy()
	resolved.SecretRef = &kelosv1alpha1.SecretReference{Name: tokenSecretName}
	return resolved, nil
}

func (r *AgentSessionReconciler) syncAgentSessionRunningState(ctx context.Context, session *kelosv1alpha1.AgentSession) (ctrl.Result, error) {
	turns, err := r.listSessionTurns(ctx, session)
	if err != nil {
		return ctrl.Result{}, err
	}
	var queued int32
	var current string
	for _, turn := range turns {
		switch turn.Status.Phase {
		case "", kelosv1alpha1.AgentTurnPhaseQueued:
			queued++
		case kelosv1alpha1.AgentTurnPhaseRunning:
			current = turn.Name
		}
	}
	phase := kelosv1alpha1.AgentSessionPhaseIdle
	if current != "" {
		phase = kelosv1alpha1.AgentSessionPhaseRunning
	}
	if queued > 0 && current == "" {
		phase = kelosv1alpha1.AgentSessionPhaseRunning
	}
	if err := r.patchAgentSessionStatus(ctx, session, func(s *kelosv1alpha1.AgentSession) {
		now := metav1.Now()
		s.Status.Phase = phase
		s.Status.CurrentTurn = current
		s.Status.QueuedTurns = queued
		if phase == kelosv1alpha1.AgentSessionPhaseRunning || s.Status.LastActivityAt == nil {
			s.Status.LastActivityAt = &now
		}
		s.Status.Message = "Session runner active"
	}); err != nil {
		return ctrl.Result{}, err
	}
	if phase == kelosv1alpha1.AgentSessionPhaseIdle && session.Spec.IdleTimeout.Duration > 0 && session.Status.LastActivityAt != nil {
		idleFor := time.Since(session.Status.LastActivityAt.Time)
		if idleFor >= session.Spec.IdleTimeout.Duration {
			return r.setAgentSessionPhase(ctx, session, kelosv1alpha1.AgentSessionPhaseClosed, "Session idle timeout reached")
		}
		return ctrl.Result{RequeueAfter: session.Spec.IdleTimeout.Duration - idleFor}, nil
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *AgentSessionReconciler) listSessionTurns(ctx context.Context, session *kelosv1alpha1.AgentSession) ([]kelosv1alpha1.AgentTurn, error) {
	var list kelosv1alpha1.AgentTurnList
	if err := r.List(ctx, &list, client.InNamespace(session.Namespace), client.MatchingLabels{"kelos.dev/agent-session": session.Name}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *AgentSessionReconciler) latestRunnerPodName(ctx context.Context, session *kelosv1alpha1.AgentSession) string {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(session.Namespace), client.MatchingLabels{"kelos.dev/agent-session": session.Name}); err != nil {
		return ""
	}
	return latestTaskPodName(pods.Items)
}

func (r *AgentSessionReconciler) handleAgentSessionDeletion(ctx context.Context, session *kelosv1alpha1.AgentSession) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(session, agentSessionFinalizer) {
		var job batchv1.Job
		if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: session.Name}, &job); err == nil {
			propagationPolicy := metav1.DeletePropagationBackground
			if err := r.Delete(ctx, &job, &client.DeleteOptions{PropagationPolicy: &propagationPolicy}); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
		controllerutil.RemoveFinalizer(session, agentSessionFinalizer)
		if err := r.Update(ctx, session); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *AgentSessionReconciler) setAgentSessionPhase(ctx context.Context, session *kelosv1alpha1.AgentSession, phase kelosv1alpha1.AgentSessionPhase, message string) (ctrl.Result, error) {
	err := r.patchAgentSessionStatus(ctx, session, func(s *kelosv1alpha1.AgentSession) {
		now := metav1.Now()
		s.Status.Phase = phase
		s.Status.Message = message
		s.Status.LastActivityAt = &now
	})
	return ctrl.Result{}, err
}

func (r *AgentSessionReconciler) patchAgentSessionStatus(ctx context.Context, session *kelosv1alpha1.AgentSession, mutate func(*kelosv1alpha1.AgentSession)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.AgentSession
		if err := r.Get(ctx, client.ObjectKeyFromObject(session), &current); err != nil {
			return err
		}
		mutate(&current)
		if err := r.Status().Update(ctx, &current); err != nil {
			return err
		}
		session.Status = current.Status
		return nil
	})
}

func (r *AgentSessionReconciler) recordEvent(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentSessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelosv1alpha1.AgentSession{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
