package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
	"github.com/kelos-dev/kelos/internal/sessionupdate"
)

const (
	SessionRuntimeImageRepository = "ghcr.io/kelos-dev/kelos-session-runtime"
	DefaultSessionRuntimeImage    = SessionRuntimeImageRepository + ":latest"

	sessionRuntimeContainerName = "kelos-session-runtime"
	sessionRuntimeVolumeName    = "kelos-session-runtime"
	sessionRuntimeMountPath     = "/kelos/bin"
	sessionRuntimeBinary        = sessionRuntimeMountPath + "/kelos-session-runtime"
	sessionClaudeConfigDir      = "/workspace/.kelos/session/claude-config"
	sessionCodexHome            = "/workspace/.kelos/session/codex-home"
	sessionOpenCodeConfigDir    = "/workspace/.kelos/session/opencode-config"
	sessionOpenCodeDataDir      = "/workspace/.kelos/session/opencode-data"
	sessionInitializedPath      = "/workspace/.kelos/session/initialized"
	sessionNameAnnotation       = "kelos.dev/session-name"
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

// +kubebuilder:rbac:groups=kelos.dev,resources=sessions,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=sessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update

// Reconcile creates and observes the StatefulSet that owns a Session conversation.
func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
	if apierrors.IsNotFound(err) {
		return r.createSessionStatefulSet(ctx, &session)
	}
	if err != nil {
		logger.Error(err, "Unable to fetch Session StatefulSet", "session", session.Name)
		return ctrl.Result{}, err
	}

	if !metav1.IsControlledBy(&statefulSet, &session) {
		message := fmt.Sprintf("StatefulSet %q already exists and is not controlled by this Session", statefulSet.Name)
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "StatefulSetConflict")
	}
	if statefulSet.DeletionTimestamp != nil {
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhasePending, "Session StatefulSet is terminating and will be recreated", "StatefulSetTerminating")
	}
	serviceAccountName := statefulSet.Spec.Template.Spec.ServiceAccountName
	if serviceAccountName == "" {
		serviceAccountName = sessionRuntimeAccessName(&session)
	}
	if err := r.ensureSessionRuntimeAccess(ctx, &session, serviceAccountName); err != nil {
		message := fmt.Sprintf("Failed to prepare Session runtime access: %v", err)
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "RuntimeAccessFailed")
	}
	if err := r.ensureSessionWorkspaceClaimRetention(ctx, &session, &statefulSet); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureSessionWorkspaceClaimOwnership(ctx, &session, &statefulSet); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureSessionService(ctx, &session); err != nil {
		message := fmt.Sprintf("Failed to prepare Session governing Service: %v", err)
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, message, "ServiceFailed")
	}
	if err := r.ensureSessionRuntime(ctx, &session, &statefulSet, serviceAccountName); err != nil {
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
	result := ctrl.Result{}
	if next, err := r.refreshSessionGitHubAppTokenIfNeeded(ctx, &session, &pod); err != nil {
		logger.Error(err, "Unable to refresh Session GitHub App token", "session", session.Name)
		result.RequeueAfter = tokenRefreshRetryInterval
	} else if next > 0 {
		result.RequeueAfter = next
	}
	updateResult, waitingForUpdate, err := r.reconcileSessionRuntimeUpdate(ctx, &session, &statefulSet, &pod, phase)
	if err != nil {
		return ctrl.Result{}, err
	}
	if waitingForUpdate && (result.RequeueAfter == 0 || updateResult.RequeueAfter < result.RequeueAfter) {
		result = updateResult
	}
	return result, nil
}

func (r *SessionReconciler) createSessionStatefulSet(ctx context.Context, session *kelos.Session) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	workspace, agentConfig, waitingMessage, err := r.resolveSessionInputs(ctx, session)
	if err != nil {
		message := fmt.Sprintf("Failed to resolve Session configuration: %v", err)
		_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "ConfigurationInvalid")
		return ctrl.Result{}, err
	}
	if waitingMessage != "" {
		if err := r.updateSessionStatus(ctx, session, nil, kelos.SessionPhasePending, waitingMessage, "WaitingForDependency"); err != nil {
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
	if err := r.ensureSessionRuntimeAccess(ctx, session, statefulSet.Spec.Template.Spec.ServiceAccountName); err != nil {
		message := fmt.Sprintf("Failed to prepare Session runtime access: %v", err)
		_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "RuntimeAccessFailed")
		return ctrl.Result{}, err
	}
	if configMap != nil {
		if err := controllerutil.SetControllerReference(session, configMap, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting Session owner on plugin ConfigMap: %w", err)
		}
		if err := r.Create(ctx, configMap); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("creating Session plugin ConfigMap: %w", err)
			}
			var existing corev1.ConfigMap
			if err := r.Get(ctx, client.ObjectKeyFromObject(configMap), &existing); err != nil {
				return ctrl.Result{}, fmt.Errorf("getting existing Session plugin ConfigMap: %w", err)
			}
			if !metav1.IsControlledBy(&existing, session) {
				return ctrl.Result{}, fmt.Errorf("plugin ConfigMap %q already exists and is not controlled by this Session", configMap.Name)
			}
		}
	}

	if err := r.ensureSessionService(ctx, session); err != nil {
		message := fmt.Sprintf("Failed to prepare Session governing Service: %v", err)
		_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "ServiceFailed")
		return ctrl.Result{}, err
	}
	if err := controllerutil.SetControllerReference(session, statefulSet, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting Session owner on StatefulSet: %w", err)
	}
	if err := r.Create(ctx, statefulSet); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Unable to create Session StatefulSet", "session", session.Name)
		return ctrl.Result{}, err
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(session, corev1.EventTypeNormal, "StatefulSetCreated", "Created StatefulSet %s for Session", statefulSet.Name)
	}
	if err := r.updateSessionStatus(ctx, session, nil, kelos.SessionPhasePending, "Session Pod is starting", "PodStarting"); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *SessionReconciler) ensureSessionRuntime(ctx context.Context, session *kelos.Session, statefulSet *appsv1.StatefulSet, serviceAccountName string) error {
	original := statefulSet.DeepCopy()
	runtimeFound := false
	for i := range statefulSet.Spec.Template.Spec.InitContainers {
		container := &statefulSet.Spec.Template.Spec.InitContainers[i]
		if container.Name != sessionRuntimeContainerName {
			continue
		}
		runtimeFound = true
		pullPolicyMatches := r.SessionRuntimeImagePullPolicy == "" || container.ImagePullPolicy == r.SessionRuntimeImagePullPolicy
		if container.Image != r.SessionRuntimeImage || !pullPolicyMatches {
			container.Image = r.SessionRuntimeImage
			if r.SessionRuntimeImagePullPolicy != "" {
				container.ImagePullPolicy = r.SessionRuntimeImagePullPolicy
			}
		}
		break
	}
	if !runtimeFound {
		return fmt.Errorf("Session StatefulSet %q has no session runtime init container", statefulSet.Name)
	}
	if len(statefulSet.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("Session StatefulSet %q has no agent container", statefulSet.Name)
	}
	mainContainer := &statefulSet.Spec.Template.Spec.Containers[0]
	setSessionContainerEnv(mainContainer, "KELOS_SESSION_NAME", session.Name)
	setSessionContainerEnv(mainContainer, "KELOS_SESSION_NAMESPACE", session.Namespace)
	setSessionContainerEnvVar(mainContainer, corev1.EnvVar{
		Name: "KELOS_SESSION_POD_UID",
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
			APIVersion: "v1",
			FieldPath:  "metadata.uid",
		}},
	})
	statefulSet.Spec.Template.Spec.ServiceAccountName = serviceAccountName
	statefulSet.Spec.Template.Spec.AutomountServiceAccountToken = ptr.To(true)
	statefulSet.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	if reflect.DeepEqual(original.Spec.Template.Spec, statefulSet.Spec.Template.Spec) &&
		reflect.DeepEqual(original.Spec.UpdateStrategy, statefulSet.Spec.UpdateStrategy) {
		return nil
	}
	if err := r.Patch(ctx, statefulSet, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patching Session StatefulSet %q runtime configuration: %w", statefulSet.Name, err)
	}
	return nil
}

func (r *SessionReconciler) ensureSessionWorkspaceClaimRetention(ctx context.Context, session *kelos.Session, statefulSet *appsv1.StatefulSet) error {
	if session.Spec.VolumeClaimTemplate == nil {
		return nil
	}
	desired := &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
		WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
		WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
	}
	if reflect.DeepEqual(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy, desired) {
		return nil
	}
	original := statefulSet.DeepCopy()
	statefulSet.Spec.PersistentVolumeClaimRetentionPolicy = desired
	if err := r.Patch(ctx, statefulSet, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patching Session StatefulSet %q workspace claim retention: %w", statefulSet.Name, err)
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
	key := client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session)}
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

func (r *SessionReconciler) resolveSessionInputs(ctx context.Context, session *kelos.Session) (*kelos.WorkspaceSpec, *kelos.AgentConfigSpec, string, error) {
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
			resolved, err := r.resolveSessionGitHubAppToken(ctx, session, workspace)
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
	if len(agentConfig.Skills) > 0 {
		taskReconciler := TaskReconciler{Client: r.Client}
		if err := taskReconciler.validateSkillsAuthSecrets(ctx, session.Namespace, agentConfig.Skills); err != nil {
			return nil, nil, "", err
		}
	}
	if len(agentConfig.MCPServers) > 0 {
		resolved, err := resolveMCPServerSecrets(ctx, r.Client, session.Namespace, agentConfig.MCPServers)
		if err != nil {
			return nil, nil, "", err
		}
		agentConfig.MCPServers = resolved
	}

	return workspace, agentConfig, "", nil
}

func (r *SessionReconciler) resolveSessionGitHubAppToken(ctx context.Context, session *kelos.Session, workspace *kelos.WorkspaceSpec) (*kelos.WorkspaceSpec, error) {
	var source corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: workspace.SecretRef.Name}, &source); err != nil {
		return nil, fmt.Errorf("fetching Workspace Secret %q: %w", workspace.SecretRef.Name, err)
	}
	if !githubapp.IsGitHubApp(source.Data) {
		return workspace, nil
	}
	if r.TokenClient == nil {
		return nil, errors.New("GitHub App Secret detected but TokenClient is not configured")
	}
	credentials, err := githubapp.ParseCredentials(source.Data)
	if err != nil {
		return nil, fmt.Errorf("parsing GitHub App credentials: %w", err)
	}
	tokenClient := sessionGitHubTokenClient(r.TokenClient, workspace.Repo)
	response, err := tokenClient.GenerateInstallationToken(ctx, credentials)
	if err != nil {
		return nil, fmt.Errorf("generating GitHub App installation token: %w", err)
	}

	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tokenSecretName,
			Namespace: session.Namespace,
			Annotations: map[string]string{
				githubAppSecretAnnotation: workspace.SecretRef.Name,
				tokenExpiresAtAnnotation:  response.ExpiresAt.UTC().Format(time.RFC3339),
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
			return nil, fmt.Errorf("GitHub token Secret %q already exists and is not controlled by this Session", tokenSecretName)
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

func (r *SessionReconciler) refreshSessionGitHubAppTokenIfNeeded(ctx context.Context, session *kelos.Session, pod *corev1.Pod) (time.Duration, error) {
	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	var tokenSecret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: tokenSecretName}, &tokenSecret); err != nil {
		if apierrors.IsNotFound(err) {
			if !sessionPodUsesSecret(pod, tokenSecretName) {
				return 0, nil
			}
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

func sessionPodUsesSecret(pod *corev1.Pod, name string) bool {
	for _, volume := range pod.Spec.Volumes {
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
	resolved, err := r.resolveSessionGitHubAppToken(ctx, session, workspace.Spec.DeepCopy())
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
	mainContainer.Command = []string{sessionRuntimeBinary}
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
	if err := prepareSessionWorkspaceInit(podSpec.InitContainers); err != nil {
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
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionWorkloadName(session),
			Namespace: session.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:                             ptr.To(int32(1)),
			ServiceName:                          sessionWorkloadName(session),
			Selector:                             &metav1.LabelSelector{MatchLabels: selector},
			UpdateStrategy:                       appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
			PersistentVolumeClaimRetentionPolicy: retentionPolicy,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: map[string]string{sessionNameAnnotation: session.Name},
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: volumeClaimTemplates,
		},
	}
	return statefulSet, configMap, nil
}

func buildSessionService(session *kelos.Session) *corev1.Service {
	labels := sessionSelectorLabels(session)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionWorkloadName(session),
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
	return truncateResourceName("session-" + session.Name)
}

func sessionSelectorLabels(session *kelos.Session) map[string]string {
	return map[string]string{
		"kelos.dev/component": "session",
		"kelos.dev/session":   sessionLabelValue(session),
	}
}

func prepareSessionWorkspaceInit(containers []corev1.Container) error {
	for i := range containers {
		container := &containers[i]
		switch container.Name {
		case "git-clone":
			prefix := `if [ -f ` + sessionInitializedPath + ` ]; then exit 0; fi
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
	if pod == nil || phase != kelos.SessionPhaseReady || session.Status.PodUID != pod.UID {
		session.Status.Branch = ""
		session.Status.PullRequest = nil
		apiMeta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{
			Type:               kelos.SessionConditionActive,
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: session.Generation,
			Reason:             "RuntimeStatusUnknown",
			Message:            "Session runtime activity has not been reported",
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
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.findSessionForPod)).
		Complete(r)
}

func (r *SessionReconciler) findSessionForPod(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetAnnotations()[sessionNameAnnotation]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: name}}}
}
