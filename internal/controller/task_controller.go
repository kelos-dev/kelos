package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"text/template"
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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

const (
	taskFinalizer = "kelos.dev/finalizer"

	// outputRetryWindow is the maximum duration after CompletionTime
	// during which the controller retries reading Pod logs for outputs.
	outputRetryWindow = 30 * time.Second

	// outputRetryInterval is the delay between output capture retries.
	outputRetryInterval = 5 * time.Second
)

// TaskReconciler reconciles a Task object.
type TaskReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	JobBuilder   *JobBuilder
	Clientset    kubernetes.Interface
	TokenClient  *githubapp.TokenClient
	Recorder     record.EventRecorder
	BranchLocker *BranchLocker
}

// +kubebuilder:rbac:groups=kelos.dev,resources=tasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks/finalizers,verbs=update
// +kubebuilder:rbac:groups=kelos.dev,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=agentconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles Task reconciliation.
func (r *TaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var task kelos.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch Task")
		reconcileErrorsTotal.WithLabelValues("task").Inc()
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !task.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &task)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&task, taskFinalizer) {
		controllerutil.AddFinalizer(&task, taskFinalizer)
		if err := r.Update(ctx, &task); err != nil {
			logger.Error(err, "unable to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Check if Job already exists
	var job batchv1.Job
	jobExists := true
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		if apierrors.IsNotFound(err) {
			jobExists = false
		} else {
			logger.Error(err, "unable to fetch Job")
			return ctrl.Result{}, err
		}
	}

	// Create Job if it doesn't exist
	if !jobExists {
		if len(task.Spec.DependsOn) > 0 {
			ready, result, err := r.checkDependencies(ctx, &task)
			if err != nil || !ready {
				return result, err
			}
		}

		if task.Spec.Branch != "" {
			if task.Spec.WorkspaceRef == nil {
				logger.Info("Branch is set without workspaceRef, branch checkout will not happen", "task", task.Name, "branch", task.Spec.Branch)
				r.recordEvent(&task, corev1.EventTypeWarning, "BranchWithoutWorkspace", "Branch %q is set but workspaceRef is not configured, branch checkout will be skipped", task.Spec.Branch)
			}
			lockKey := branchLockKey(&task)
			acquired, holder := r.BranchLocker.TryAcquire(lockKey, task.Name)
			if !acquired {
				// In-memory lock is held by another task.
				logger.Info("Branch locked by another task", "branch", task.Spec.Branch, "lockedBy", holder)
				r.setWaitingPhase(ctx, &task, fmt.Sprintf("Waiting for branch %q (locked by %s)", task.Spec.Branch, holder))
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
			// Fallback: check status-based lock for restart recovery.
			// After a restart the in-memory map is empty, so TryAcquire
			// always succeeds. The status check catches Running/Pending
			// tasks whose lock was lost.
			locked, result, err := r.checkBranchLock(ctx, &task)
			if err != nil || locked {
				r.BranchLocker.Release(lockKey, task.Name)
				return result, err
			}
		}

		return r.createJob(ctx, &task)
	}

	// Update status based on Job status
	result, err := r.updateStatus(ctx, &task, &job)
	if err != nil {
		return result, err
	}

	// Check TTL expiration for finished Tasks
	if expired, requeueAfter := r.ttlExpired(&task); expired {
		logger.Info("Deleting Task due to TTL expiration", "task", task.Name)
		r.recordEvent(&task, corev1.EventTypeNormal, "TaskExpired", "Deleting Task due to TTL expiration")
		if err := r.Delete(ctx, &task); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			logger.Error(err, "Unable to delete expired Task")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	} else if requeueAfter > 0 {
		// Requeue to check TTL expiration later
		if result.RequeueAfter == 0 || requeueAfter < result.RequeueAfter {
			result.RequeueAfter = requeueAfter
		}
	}

	return result, nil
}

// handleDeletion handles Task deletion.
func (r *TaskReconciler) handleDeletion(ctx context.Context, task *kelos.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(task, taskFinalizer) {
		// Release branch lock if held.
		if task.Spec.Branch != "" {
			r.BranchLocker.Release(branchLockKey(task), task.Name)
		}

		// Delete the Job if it exists
		var job batchv1.Job
		if err := r.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Name}, &job); err == nil {
			propagationPolicy := metav1.DeletePropagationBackground
			if err := r.Delete(ctx, &job, &client.DeleteOptions{
				PropagationPolicy: &propagationPolicy,
			}); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "unable to delete Job")
				return ctrl.Result{}, err
			}
		}

		// Remove finalizer
		controllerutil.RemoveFinalizer(task, taskFinalizer)
		if err := r.Update(ctx, task); err != nil {
			logger.Error(err, "unable to remove finalizer")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// createJob creates a Job for the Task.
func (r *TaskReconciler) createJob(ctx context.Context, task *kelos.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var workspace *kelos.WorkspaceSpec
	if task.Spec.WorkspaceRef != nil {
		var ws kelos.Workspace
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: task.Namespace,
			Name:      task.Spec.WorkspaceRef.Name,
		}, &ws); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Workspace not found yet, requeuing", "workspace", task.Spec.WorkspaceRef.Name)
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			logger.Error(err, "Unable to fetch Workspace", "workspace", task.Spec.WorkspaceRef.Name)
			return ctrl.Result{}, err
		}
		workspace = &ws.Spec

		// Handle GitHub App authentication
		if workspace.SecretRef != nil {
			resolvedWorkspace, err := r.resolveGitHubAppToken(ctx, task, workspace)
			if err != nil {
				logger.Error(err, "Unable to resolve GitHub App token")
				r.recordEvent(task, corev1.EventTypeWarning, "GitHubTokenFailed", "Failed to resolve GitHub token: %v", err)
				updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
						return getErr
					}
					task.Status.Phase = kelos.TaskPhaseFailed
					task.Status.Message = fmt.Sprintf("Failed to resolve GitHub token: %v", err)
					return r.Status().Update(ctx, task)
				})
				if updateErr != nil {
					logger.Error(updateErr, "Unable to update Task status")
				}
				return ctrl.Result{}, nil
			}
			workspace = resolvedWorkspace
		}
	}

	var agentConfig *kelos.AgentConfigSpec
	if refs := ResolveAgentConfigRefs(&task.Spec); len(refs) > 0 {
		var specs []kelos.AgentConfigSpec
		var namedSpecs []namedAgentConfigSpec
		for _, ref := range refs {
			ac, err := r.getAgentConfig(ctx, client.ObjectKey{
				Namespace: task.Namespace,
				Name:      ref.Name,
			})
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.Info("AgentConfig not found yet, requeuing", "agentConfig", ref.Name)
					return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
				}
				logger.Error(err, "Unable to fetch AgentConfig", "agentConfig", ref.Name)
				return ctrl.Result{}, err
			}
			specs = append(specs, ac.Spec)
			namedSpecs = append(namedSpecs, namedAgentConfigSpec{Name: ref.Name, Spec: ac.Spec})
		}

		if err := validateAgentConfigSpecs(namedSpecs); err != nil {
			logger.Error(err, "Invalid AgentConfig combination")
			r.recordEvent(task, corev1.EventTypeWarning, "AgentConfigInvalid", "Invalid AgentConfig: %v", err)
			updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
					return getErr
				}
				task.Status.Phase = kelos.TaskPhaseFailed
				task.Status.Message = fmt.Sprintf("Invalid AgentConfig: %v", err)
				return r.Status().Update(ctx, task)
			})
			if updateErr != nil {
				logger.Error(updateErr, "Unable to update Task status")
			}
			return ctrl.Result{}, nil
		}

		agentConfig = MergeAgentConfigs(specs)

		if agentConfig.Kanon != nil && !supportsKanonAgentType(task.Spec.Type) {
			err := fmt.Errorf("Kanon does not support agent type %q", task.Spec.Type)
			logger.Error(err, "Kanon config ignored", "agentType", task.Spec.Type)
			r.recordEvent(task, corev1.EventTypeWarning, "KanonConfigIgnored", "Kanon config ignored for unsupported agent type %s", task.Spec.Type)
		}

		if len(agentConfig.MCPServers) > 0 {
			resolved, err := r.resolveMCPServerSecrets(ctx, task.Namespace, agentConfig.MCPServers)
			if err != nil {
				logger.Error(err, "Unable to resolve MCP server secrets")
				r.recordEvent(task, corev1.EventTypeWarning, "MCPSecretFailed", "Failed to resolve MCP server secret: %v", err)
				updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
						return getErr
					}
					task.Status.Phase = kelos.TaskPhaseFailed
					task.Status.Message = fmt.Sprintf("Failed to resolve MCP server secret: %v", err)
					return r.Status().Update(ctx, task)
				})
				if updateErr != nil {
					logger.Error(updateErr, "Unable to update Task status")
				}
				return ctrl.Result{}, nil
			}
			agentConfig.MCPServers = resolved
		}
	}

	resolvedPrompt := r.resolvePromptTemplate(ctx, task)

	job, err := r.JobBuilder.Build(task, workspace, agentConfig, resolvedPrompt)
	if err != nil {
		logger.Error(err, "unable to build Job")
		r.recordEvent(task, corev1.EventTypeWarning, "JobBuildFailed", "Failed to build Job: %v", err)
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
				return getErr
			}
			task.Status.Phase = kelos.TaskPhaseFailed
			task.Status.Message = fmt.Sprintf("Failed to build Job: %v", err)
			return r.Status().Update(ctx, task)
		})
		if updateErr != nil {
			logger.Error(updateErr, "Unable to update Task status")
		}
		return ctrl.Result{}, err
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(task, job, r.Scheme); err != nil {
		logger.Error(err, "unable to set owner reference")
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "unable to create Job")
		return ctrl.Result{}, err
	}

	logger.Info("created Job", "job", job.Name)
	r.recordEvent(task, corev1.EventTypeNormal, "TaskCreated", "Created Job %s for task", job.Name)
	taskCreatedTotal.WithLabelValues(task.Namespace, task.Spec.Type).Inc()

	// Update status
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		task.Status.Phase = kelos.TaskPhasePending
		task.Status.JobName = job.Name
		return r.Status().Update(ctx, task)
	}); err != nil {
		logger.Error(err, "Unable to update Task status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// resolveGitHubAppToken checks if the workspace secret is a GitHub App secret,
// and if so, generates an installation token and creates a new secret with
// the GITHUB_TOKEN key. Returns a modified workspace spec pointing to the
// generated secret.
func (r *TaskReconciler) resolveGitHubAppToken(ctx context.Context, task *kelos.Task, workspace *kelos.WorkspaceSpec) (*kelos.WorkspaceSpec, error) {
	logger := log.FromContext(ctx)

	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: task.Namespace,
		Name:      workspace.SecretRef.Name,
	}, &secret); err != nil {
		return nil, fmt.Errorf("fetching workspace secret %q: %w", workspace.SecretRef.Name, err)
	}

	if !githubapp.IsGitHubApp(secret.Data) {
		return workspace, nil
	}

	if r.TokenClient == nil {
		return nil, fmt.Errorf("GitHub App secret detected but TokenClient is not configured")
	}

	logger.Info("Detected GitHub App secret, generating installation token", "secret", workspace.SecretRef.Name)

	creds, err := githubapp.ParseCredentials(secret.Data)
	if err != nil {
		return nil, fmt.Errorf("parsing GitHub App credentials: %w", err)
	}

	// Use a per-call TokenClient so that concurrent reconciles with different
	// hosts do not race on the shared r.TokenClient.BaseURL.
	tc := &githubapp.TokenClient{
		BaseURL: r.TokenClient.BaseURL,
		Client:  r.TokenClient.Client,
	}
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

	// Create a new secret with the generated token, owned by the Task
	tokenSecretName := task.Name + "-github-token"
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tokenSecretName,
			Namespace: task.Namespace,
		},
		StringData: map[string]string{
			"GITHUB_TOKEN": tokenResp.Token,
		},
	}

	if err := controllerutil.SetControllerReference(task, tokenSecret, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting owner reference on token secret: %w", err)
	}

	if err := r.Create(ctx, tokenSecret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating token secret: %w", err)
		}
		// Update existing secret
		existing := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Name: tokenSecretName, Namespace: task.Namespace}, existing); err != nil {
			return nil, fmt.Errorf("fetching existing token secret: %w", err)
		}
		existing.StringData = tokenSecret.StringData
		if err := r.Update(ctx, existing); err != nil {
			return nil, fmt.Errorf("updating token secret: %w", err)
		}
	}

	// Return a modified workspace spec that points to the generated token secret
	resolved := *workspace
	resolved.SecretRef = &kelos.SecretReference{
		Name: tokenSecretName,
	}
	return &resolved, nil
}

func (r *TaskReconciler) resolveMCPServerSecrets(ctx context.Context, namespace string, servers []kelos.MCPServerSpec) ([]kelos.MCPServerSpec, error) {
	resolved := make([]kelos.MCPServerSpec, len(servers))
	for i, server := range servers {
		resolved[i] = server

		if server.HeadersFrom != nil {
			var secret corev1.Secret
			if err := r.Get(ctx, client.ObjectKey{
				Namespace: namespace,
				Name:      server.HeadersFrom.SecretRef.Name,
			}, &secret); err != nil {
				return nil, fmt.Errorf("fetching headersFrom secret %q for MCP server %q: %w", server.HeadersFrom.SecretRef.Name, server.Name, err)
			}
			merged := make(map[string]string, len(server.Headers)+len(secret.Data))
			for key, value := range server.Headers {
				merged[key] = value
			}
			for key, value := range secret.Data {
				merged[key] = string(value)
			}
			resolved[i].Headers = merged
			resolved[i].HeadersFrom = nil
		}

		resolvedEnv, err := r.resolveMCPServerEnv(ctx, namespace, server)
		if err != nil {
			return nil, err
		}
		resolved[i].Env = resolvedEnv
		resolved[i].EnvFrom = nil
	}

	return resolved, nil
}

// resolveMCPServerEnv resolves the Env entries of a single MCP server to a
// slice of literal Name/Value pairs. Entries with ValueFrom are resolved by
// fetching the referenced Secret key or ConfigMap key. EnvFrom (whole-secret)
// is then merged on top so its keys take precedence on collision, matching
// the existing behaviour.
func (r *TaskReconciler) resolveMCPServerEnv(ctx context.Context, namespace string, server kelos.MCPServerSpec) ([]corev1.EnvVar, error) {
	// Resolve inline Env entries into a map keyed by name so EnvFrom can
	// override and so the final list has no duplicate names.
	values := make(map[string]string, len(server.Env))
	order := make([]string, 0, len(server.Env))
	for _, entry := range server.Env {
		if entry.Name == "" {
			return nil, fmt.Errorf("MCP server %q has an env entry with an empty name", server.Name)
		}

		value, ok, err := r.resolveEnvVarValue(ctx, namespace, server.Name, entry)
		if err != nil {
			return nil, err
		}
		// An optional ValueFrom whose Secret/ConfigMap or key is missing is
		// omitted, matching kubelet semantics for pod env.
		if !ok {
			continue
		}
		if _, seen := values[entry.Name]; !seen {
			order = append(order, entry.Name)
		}
		values[entry.Name] = value
	}

	if server.EnvFrom != nil {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: namespace,
			Name:      server.EnvFrom.SecretRef.Name,
		}, &secret); err != nil {
			return nil, fmt.Errorf("fetching envFrom secret %q for MCP server %q: %w", server.EnvFrom.SecretRef.Name, server.Name, err)
		}
		// Iterate in sorted key order so the resolved Env slice is
		// deterministic regardless of Secret data map iteration order.
		keys := make([]string, 0, len(secret.Data))
		for key := range secret.Data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, seen := values[key]; !seen {
				order = append(order, key)
			}
			values[key] = string(secret.Data[key])
		}
	}

	if len(order) == 0 {
		return nil, nil
	}
	out := make([]corev1.EnvVar, 0, len(order))
	for _, name := range order {
		out = append(out, corev1.EnvVar{Name: name, Value: values[name]})
	}
	return out, nil
}

// resolveEnvVarValue returns the resolved string value for a single EnvVar
// entry on an MCP server. Literal Value entries pass through; ValueFrom is
// resolved against Secret or ConfigMap. Only SecretKeyRef and ConfigMapKeyRef
// are honored; FieldRef, ResourceFieldRef and FileKeyRef refer to pod-scoped
// (or file-gated) information that has no meaning for an MCP server process.
// Because MCP env is resolved here rather than by the kubelet, those variants
// have nothing to resolve against and are rejected. Setting both SecretKeyRef
// and ConfigMapKeyRef is ambiguous and is rejected rather than silently
// preferring one.
//
// The returned ok is false when an optional ValueFrom resolves to nothing
// (Secret/ConfigMap or key missing); the caller omits the variable entirely,
// matching kubelet semantics for pod env.
func (r *TaskReconciler) resolveEnvVarValue(ctx context.Context, namespace, serverName string, entry corev1.EnvVar) (string, bool, error) {
	if entry.ValueFrom == nil {
		return entry.Value, true, nil
	}
	if entry.Value != "" {
		return "", false, fmt.Errorf("MCP server %q env %q: value and valueFrom are mutually exclusive", serverName, entry.Name)
	}

	src := entry.ValueFrom
	if src.FieldRef != nil || src.ResourceFieldRef != nil || src.FileKeyRef != nil {
		return "", false, fmt.Errorf("MCP server %q env %q: valueFrom only supports secretKeyRef and configMapKeyRef", serverName, entry.Name)
	}
	if src.SecretKeyRef != nil && src.ConfigMapKeyRef != nil {
		return "", false, fmt.Errorf("MCP server %q env %q: valueFrom must set only one of secretKeyRef or configMapKeyRef", serverName, entry.Name)
	}

	switch {
	case src.SecretKeyRef != nil:
		ref := src.SecretKeyRef
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: namespace,
			Name:      ref.Name,
		}, &secret); err != nil {
			if apierrors.IsNotFound(err) && ref.Optional != nil && *ref.Optional {
				return "", false, nil
			}
			return "", false, fmt.Errorf("fetching secret %q for MCP server %q env %q: %w", ref.Name, serverName, entry.Name, err)
		}
		raw, ok := secret.Data[ref.Key]
		if !ok {
			if ref.Optional != nil && *ref.Optional {
				return "", false, nil
			}
			return "", false, fmt.Errorf("secret %q has no key %q for MCP server %q env %q", ref.Name, ref.Key, serverName, entry.Name)
		}
		return string(raw), true, nil
	case src.ConfigMapKeyRef != nil:
		ref := src.ConfigMapKeyRef
		var cm corev1.ConfigMap
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: namespace,
			Name:      ref.Name,
		}, &cm); err != nil {
			if apierrors.IsNotFound(err) && ref.Optional != nil && *ref.Optional {
				return "", false, nil
			}
			return "", false, fmt.Errorf("fetching configmap %q for MCP server %q env %q: %w", ref.Name, serverName, entry.Name, err)
		}
		if value, ok := cm.Data[ref.Key]; ok {
			return value, true, nil
		}
		if raw, ok := cm.BinaryData[ref.Key]; ok {
			return string(raw), true, nil
		}
		if ref.Optional != nil && *ref.Optional {
			return "", false, nil
		}
		return "", false, fmt.Errorf("configmap %q has no key %q for MCP server %q env %q", ref.Name, ref.Key, serverName, entry.Name)
	default:
		return "", false, fmt.Errorf("MCP server %q env %q: valueFrom must set secretKeyRef or configMapKeyRef", serverName, entry.Name)
	}
}

// updateStatus updates Task status based on Job status.
func (r *TaskReconciler) updateStatus(ctx context.Context, task *kelos.Task, job *batchv1.Job) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Discover pod name for the task
	var podName string
	podListSucceeded := false
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(task.Namespace), client.MatchingLabels{
		"kelos.dev/task": task.Name,
	}); err == nil {
		podListSucceeded = true
		podName = latestTaskPodName(pods.Items)
	}

	// Determine the new phase based on Job status
	var newPhase kelos.TaskPhase
	var newMessage string
	var setStartTime, setCompletionTime bool

	if job.Status.Active > 0 {
		if task.Status.Phase != kelos.TaskPhaseRunning {
			newPhase = kelos.TaskPhaseRunning
			setStartTime = true
			r.recordEvent(task, corev1.EventTypeNormal, "TaskRunning", "Task started running")
		}
	} else if job.Status.Succeeded > 0 {
		if task.Status.Phase != kelos.TaskPhaseSucceeded {
			newPhase = kelos.TaskPhaseSucceeded
			newMessage = "Task completed successfully"
			setCompletionTime = true
			r.recordEvent(task, corev1.EventTypeNormal, "TaskSucceeded", "Task completed successfully")
			taskCompletedTotal.WithLabelValues(task.Namespace, task.Spec.Type, string(kelos.TaskPhaseSucceeded)).Inc()
		}
	} else if isJobFailed(job) {
		if task.Status.Phase != kelos.TaskPhaseFailed {
			newPhase = kelos.TaskPhaseFailed
			newMessage = "Task failed"
			setCompletionTime = true
			r.recordEvent(task, corev1.EventTypeWarning, "TaskFailed", "Task failed")
			taskCompletedTotal.WithLabelValues(task.Namespace, task.Spec.Type, string(kelos.TaskPhaseFailed)).Inc()
		}
	}

	podNameChanged := podListSucceeded && task.Status.PodName != podName
	phaseChanged := newPhase != ""

	// Check if we should retry capturing outputs for an already-completed task
	retryOutputs := !phaseChanged &&
		len(task.Status.Outputs) == 0 && len(task.Status.Results) == 0 &&
		task.Status.CompletionTime != nil &&
		time.Since(task.Status.CompletionTime.Time) < outputRetryWindow

	if !phaseChanged && !podNameChanged && !retryOutputs {
		return ctrl.Result{}, nil
	}

	// Read outputs from Pod logs when transitioning to a terminal phase
	// or retrying capture for an already-completed task
	var outputs []string
	var results map[string]string
	if setCompletionTime || retryOutputs {
		effectivePodName := podName
		if effectivePodName == "" {
			effectivePodName = task.Status.PodName
		}
		containerName := kelos.AgentContainerName
		outputs, results = r.readOutputs(ctx, task.Namespace, effectivePodName, containerName)
	}

	// When retrying output capture, skip the status update if we still
	// have nothing — just requeue to try again later.
	if retryOutputs && outputs == nil && results == nil {
		return ctrl.Result{RequeueAfter: outputRetryInterval}, nil
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		if podNameChanged {
			task.Status.PodName = podName
		}
		if phaseChanged {
			task.Status.Phase = newPhase
			task.Status.Message = newMessage
			now := metav1.Now()
			if setStartTime {
				task.Status.StartTime = &now
			}
			if setCompletionTime {
				task.Status.CompletionTime = &now
				task.Status.Outputs = outputs
				task.Status.Results = results
			}
		}
		if retryOutputs && (outputs != nil || results != nil) {
			task.Status.Outputs = outputs
			task.Status.Results = results
		}
		return r.Status().Update(ctx, task)
	}); err != nil {
		logger.Error(err, "Unable to update Task status")
		reconcileErrorsTotal.WithLabelValues("task").Inc()
		return ctrl.Result{}, err
	}

	// Release branch lock when task reaches a terminal phase.
	if setCompletionTime && task.Spec.Branch != "" {
		r.BranchLocker.Release(branchLockKey(task), task.Name)
	}

	// Record task duration when completion time is set and we have a start time
	if setCompletionTime && task.Status.StartTime != nil {
		duration := task.Status.CompletionTime.Time.Sub(task.Status.StartTime.Time).Seconds()
		taskDurationSeconds.WithLabelValues(task.Namespace, task.Spec.Type, string(newPhase)).Observe(duration)
	}

	// Record cost and token metrics when results are available
	if (setCompletionTime || retryOutputs) && results != nil {
		RecordCostTokenMetrics(task, results)
	}

	if setCompletionTime && (outputs != nil || results != nil) {
		r.recordEvent(task, corev1.EventTypeNormal, "OutputsCaptured", "Captured %d outputs and %d results from agent", len(outputs), len(results))
	}

	// Requeue to retry output capture when the initial attempt got nothing
	if setCompletionTime && outputs == nil && results == nil {
		return ctrl.Result{RequeueAfter: outputRetryInterval}, nil
	}

	return ctrl.Result{}, nil
}

func latestTaskPodName(pods []corev1.Pod) string {
	if len(pods) == 0 {
		return ""
	}

	sortedPods := append([]corev1.Pod(nil), pods...)
	sort.Slice(sortedPods, func(i, j int) bool {
		left := sortedPods[i]
		right := sortedPods[j]
		if left.CreationTimestamp.Time.Equal(right.CreationTimestamp.Time) {
			return left.Name < right.Name
		}
		return left.CreationTimestamp.Time.Before(right.CreationTimestamp.Time)
	})

	return sortedPods[len(sortedPods)-1].Name
}

// ttlExpired checks whether a finished Task has exceeded its TTL.
// It returns (true, 0) if the Task should be deleted now, or (false, duration)
// if the Task should be requeued after the given duration.
func (r *TaskReconciler) ttlExpired(task *kelos.Task) (bool, time.Duration) {
	if task.Spec.TTLSecondsAfterFinished == nil {
		return false, 0
	}
	if task.Status.Phase != kelos.TaskPhaseSucceeded && task.Status.Phase != kelos.TaskPhaseFailed {
		return false, 0
	}
	if task.Status.CompletionTime == nil {
		return false, 0
	}

	ttl := time.Duration(*task.Spec.TTLSecondsAfterFinished) * time.Second
	expireAt := task.Status.CompletionTime.Add(ttl)
	remaining := time.Until(expireAt)
	if remaining <= 0 {
		return true, 0
	}
	return false, remaining
}

// readOutputs reads Pod logs and extracts output markers and structured results.
func (r *TaskReconciler) readOutputs(ctx context.Context, namespace, podName, container string) ([]string, map[string]string) {
	if r.Clientset == nil || podName == "" {
		return nil, nil
	}
	logger := log.FromContext(ctx)

	var tailLines int64 = 50
	req := r.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tailLines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		logger.V(1).Info("Unable to read Pod logs for outputs", "pod", podName, "error", err)
		return nil, nil
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		logger.V(1).Info("Unable to read Pod log stream", "pod", podName, "error", err)
		return nil, nil
	}

	outputs := ParseOutputs(string(data))
	return outputs, ResultsFromOutputs(outputs)
}

// recordEvent records a Kubernetes Event on the given object if a Recorder is configured.
func (r *TaskReconciler) recordEvent(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// checkDependencies verifies that all tasks listed in DependsOn have succeeded.
// Returns (ready, result, error). ready=true means all dependencies succeeded.
func (r *TaskReconciler) checkDependencies(ctx context.Context, task *kelos.Task) (bool, ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Cycle detection only on first check (skip if already Waiting)
	if task.Status.Phase != kelos.TaskPhaseWaiting {
		if err := r.detectCycle(ctx, task); err != nil {
			logger.Info("Circular dependency detected", "task", task.Name, "error", err)
			updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
					return getErr
				}
				task.Status.Phase = kelos.TaskPhaseFailed
				task.Status.Message = fmt.Sprintf("Circular dependency detected: %v", err)
				now := metav1.Now()
				task.Status.CompletionTime = &now
				return r.Status().Update(ctx, task)
			})
			if updateErr != nil {
				logger.Error(updateErr, "Unable to update Task status")
			}
			r.recordEvent(task, corev1.EventTypeWarning, "DependencyFailed", "Circular dependency detected")
			return false, ctrl.Result{}, nil
		}
	}

	for _, depName := range task.Spec.DependsOn {
		var depTask kelos.Task
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: task.Namespace, Name: depName,
		}, &depTask); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Dependency not found yet, waiting", "dependency", depName)
				r.setWaitingPhase(ctx, task, fmt.Sprintf("Waiting for dependency %q to be created", depName))
				return false, ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			return false, ctrl.Result{}, err
		}

		if depTask.Status.Phase == kelos.TaskPhaseFailed {
			logger.Info("Dependency failed", "dependency", depName)
			updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
					return getErr
				}
				task.Status.Phase = kelos.TaskPhaseFailed
				task.Status.Message = fmt.Sprintf("Dependency %q failed", depName)
				now := metav1.Now()
				task.Status.CompletionTime = &now
				return r.Status().Update(ctx, task)
			})
			if updateErr != nil {
				logger.Error(updateErr, "Unable to update Task status")
			}
			r.recordEvent(task, corev1.EventTypeWarning, "DependencyFailed", "Dependency %q failed", depName)
			taskCompletedTotal.WithLabelValues(task.Namespace, task.Spec.Type, string(kelos.TaskPhaseFailed)).Inc()
			return false, ctrl.Result{}, nil
		}

		if depTask.Status.Phase != kelos.TaskPhaseSucceeded {
			logger.Info("Dependency not ready", "dependency", depName, "phase", depTask.Status.Phase)
			r.setWaitingPhase(ctx, task, fmt.Sprintf("Waiting for dependency %q", depName))
			return false, ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	return true, ctrl.Result{}, nil
}

// detectCycle walks the dependency graph from the given task and returns an
// error if a cycle is detected.
func (r *TaskReconciler) detectCycle(ctx context.Context, task *kelos.Task) error {
	visited := make(map[string]bool)
	return r.walkDeps(ctx, task.Namespace, task.Name, visited)
}

func (r *TaskReconciler) walkDeps(ctx context.Context, namespace, name string, visited map[string]bool) error {
	if visited[name] {
		return fmt.Errorf("cycle involves %q", name)
	}
	visited[name] = true

	var t kelos.Task
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &t); err != nil {
		return nil // Cannot detect cycle if task doesn't exist yet
	}

	for _, dep := range t.Spec.DependsOn {
		if err := r.walkDeps(ctx, namespace, dep, visited); err != nil {
			return err
		}
	}

	visited[name] = false
	return nil
}

// branchLockKey returns the key used for branch locking. The lock is scoped
// to (workspace, branch) so that tasks on different workspaces with the same
// branch name do not block each other.
func branchLockKey(task *kelos.Task) string {
	ws := ""
	if task.Spec.WorkspaceRef != nil {
		ws = task.Spec.WorkspaceRef.Name
	}
	return ws + ":" + task.Spec.Branch
}

// checkBranchLock checks if another task with the same workspace and branch is
// active. Returns (locked, result, error). locked=true means another task holds
// the branch. A task is considered to hold the lock if it is Running, Pending,
// or is an earlier-created Waiting task (FIFO ordering for the branch queue).
func (r *TaskReconciler) checkBranchLock(ctx context.Context, task *kelos.Task) (bool, ctrl.Result, error) {
	logger := log.FromContext(ctx)
	key := branchLockKey(task)

	var taskList kelos.TaskList
	if err := r.List(ctx, &taskList, client.InNamespace(task.Namespace)); err != nil {
		return false, ctrl.Result{}, err
	}

	for _, t := range taskList.Items {
		if t.Name == task.Name {
			continue
		}
		if t.Spec.Branch == "" || branchLockKey(&t) != key {
			continue
		}
		switch t.Status.Phase {
		case kelos.TaskPhaseRunning, kelos.TaskPhasePending:
			logger.Info("Branch locked by another task", "branch", task.Spec.Branch, "lockedBy", t.Name)
			r.setWaitingPhase(ctx, task, fmt.Sprintf("Waiting for branch %q (locked by %s)", task.Spec.Branch, t.Name))
			return true, ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		case kelos.TaskPhaseWaiting:
			if t.CreationTimestamp.Before(&task.CreationTimestamp) {
				logger.Info("Branch queued behind earlier task", "branch", task.Spec.Branch, "queuedBehind", t.Name)
				r.setWaitingPhase(ctx, task, fmt.Sprintf("Waiting for branch %q (queued behind %s)", task.Spec.Branch, t.Name))
				return true, ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
	}

	return false, ctrl.Result{}, nil
}

// setWaitingPhase updates the task phase to Waiting with the given message.
func (r *TaskReconciler) setWaitingPhase(ctx context.Context, task *kelos.Task, message string) {
	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		if task.Status.Phase == kelos.TaskPhaseWaiting && task.Status.Message == message {
			return nil
		}
		task.Status.Phase = kelos.TaskPhaseWaiting
		task.Status.Message = message
		return r.Status().Update(ctx, task)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to update Task status to Waiting")
	}
}

// resolvePromptTemplate resolves Go template references in the prompt using
// dependency outputs. Falls back to the raw prompt on any error.
func (r *TaskReconciler) resolvePromptTemplate(ctx context.Context, task *kelos.Task) string {
	logger := log.FromContext(ctx)

	if len(task.Spec.DependsOn) == 0 {
		return task.Spec.Prompt
	}

	deps := make(map[string]interface{})
	for _, depName := range task.Spec.DependsOn {
		var depTask kelos.Task
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: task.Namespace, Name: depName,
		}, &depTask); err != nil {
			logger.Info("Failed to fetch dependency for prompt template, using raw prompt", "dependency", depName, "error", err)
			return task.Spec.Prompt
		}
		deps[depName] = map[string]interface{}{
			"Outputs": depTask.Status.Outputs,
			"Results": depTask.Status.Results,
			"Name":    depName,
		}
	}

	tmpl, err := template.New("prompt").Option("missingkey=error").Parse(task.Spec.Prompt)
	if err != nil {
		logger.Info("Failed to parse prompt template, using raw prompt", "error", err)
		return task.Spec.Prompt
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]interface{}{"Deps": deps}); err != nil {
		logger.Info("Failed to execute prompt template, using raw prompt", "error", err)
		return task.Spec.Prompt
	}
	return buf.String()
}

// RecordCostTokenMetrics emits Prometheus counters for cost and token usage
// extracted from Task results.
func RecordCostTokenMetrics(task *kelos.Task, results map[string]string) {
	spawner := task.Labels["kelos.dev/taskspawner"]
	model := task.Spec.Model
	labels := []string{task.Namespace, task.Spec.Type, spawner, model}

	if costStr, ok := results["cost-usd"]; ok {
		if cost, err := strconv.ParseFloat(costStr, 64); err == nil && cost > 0 {
			taskCostUSD.WithLabelValues(labels...).Add(cost)
		}
	}
	if inputStr, ok := results["input-tokens"]; ok {
		if tokens, err := strconv.ParseFloat(inputStr, 64); err == nil && tokens > 0 {
			taskInputTokens.WithLabelValues(labels...).Add(tokens)
		}
	}
	if outputStr, ok := results["output-tokens"]; ok {
		if tokens, err := strconv.ParseFloat(outputStr, 64); err == nil && tokens > 0 {
			taskOutputTokens.WithLabelValues(labels...).Add(tokens)
		}
	}
}

// isJobFailed checks whether the Job has permanently failed by looking for a
// JobFailed condition with status True. Unlike checking job.Status.Failed > 0,
// this correctly handles Jobs with backoffLimit > 0 where intermediate pod
// failures are retries rather than terminal failures.
func isJobFailed(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *TaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.Task{}).
		Owns(&batchv1.Job{}).
		Watches(&kelos.Task{}, handler.EnqueueRequestsFromMapFunc(r.enqueueDependentTasks)).
		Watches(&kelos.Workspace{}, handler.EnqueueRequestsFromMapFunc(r.enqueueTasksForWorkspace)).
		Complete(r)
}

// enqueueTasksForWorkspace returns reconcile requests for Tasks that reference
// the given Workspace. This ensures Tasks waiting for a Workspace are
// reconciled immediately when it appears.
func (r *TaskReconciler) enqueueTasksForWorkspace(ctx context.Context, obj client.Object) []reconcile.Request {
	ws, ok := obj.(*kelos.Workspace)
	if !ok {
		return nil
	}

	var taskList kelos.TaskList
	if err := r.List(ctx, &taskList, client.InNamespace(ws.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, t := range taskList.Items {
		if t.Spec.WorkspaceRef != nil && t.Spec.WorkspaceRef.Name == ws.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&t),
			})
		}
	}
	return requests
}

// enqueueDependentTasks returns reconcile requests for tasks that depend on the
// given task or are waiting for the same branch. This ensures dependent and
// branch-queued tasks are reconciled immediately when a task reaches a terminal
// phase, instead of waiting for a requeue timer.
func (r *TaskReconciler) enqueueDependentTasks(ctx context.Context, obj client.Object) []reconcile.Request {
	task, ok := obj.(*kelos.Task)
	if !ok {
		return nil
	}

	// Only trigger when a task reaches a terminal phase
	if task.Status.Phase != kelos.TaskPhaseSucceeded && task.Status.Phase != kelos.TaskPhaseFailed {
		return nil
	}

	var taskList kelos.TaskList
	if err := r.List(ctx, &taskList, client.InNamespace(task.Namespace)); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var requests []reconcile.Request
	for _, t := range taskList.Items {
		if t.Name == task.Name || seen[t.Name] {
			continue
		}
		// Re-enqueue tasks that depend on this task
		for _, dep := range t.Spec.DependsOn {
			if dep == task.Name {
				seen[t.Name] = true
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&t),
				})
				break
			}
		}
		// Re-enqueue tasks waiting for the same workspace+branch
		if !seen[t.Name] && task.Spec.Branch != "" && t.Spec.Branch != "" &&
			branchLockKey(&t) == branchLockKey(task) &&
			t.Status.Phase == kelos.TaskPhaseWaiting {
			seen[t.Name] = true
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&t),
			})
		}
	}
	return requests
}
