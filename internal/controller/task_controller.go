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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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

	// tokenRefreshMargin is the safety margin before token expiry. The
	// controller re-mints and updates the per-task token Secret when less
	// than this duration remains until expiration. Mirrors the semantics
	// of githubapp.tokenExpiryMargin used by long-lived processes.
	tokenRefreshMargin = 5 * time.Minute

	// tokenRefreshRetryInterval is the maximum delay before retrying a
	// failed token refresh. It must stay shorter than tokenRefreshMargin so
	// a transient failure still leaves time for another refresh attempt
	// before the current installation token expires.
	tokenRefreshRetryInterval = 30 * time.Second

	// tokenExpiresAtAnnotation stores the expiration time of the
	// installation token currently held in a per-task token Secret,
	// formatted as RFC3339. Used by the controller to decide when to
	// re-mint without re-deriving expiry from secret data.
	tokenExpiresAtAnnotation = "kelos.dev/token-expires-at"

	// githubAppSecretAnnotation stores the name of the source GitHub
	// App credential Secret on the per-task token Secret. Its presence
	// marks the Secret as App-derived (i.e. refreshable).
	githubAppSecretAnnotation = "kelos.dev/github-app-secret"
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

	// NowFunc returns the current time. Defaults to time.Now.
	// Overridable in tests for deterministic behavior.
	NowFunc func() time.Time
}

// +kubebuilder:rbac:groups=kelos.dev,resources=tasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks/finalizers,verbs=update
// +kubebuilder:rbac:groups=kelos.dev,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=agentconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskrecords,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=kelos.dev,resources=taskbudgets,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskbudgets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
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

	// Tasks with workerPoolRef are handled by the WorkerPoolReconciler
	// after the finalizer is in place.
	if task.Spec.WorkerPoolRef != nil {
		return ctrl.Result{}, nil
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

	if !jobExists && isTerminalTaskPhase(task.Status.Phase) {
		// The Job may have been deleted before its TaskRecord was created (e.g. a
		// transient create failure). Retry here so budget usage is not undercounted.
		// TaskRecord TTL GC is handled by the dedicated TaskRecordReconciler.
		if task.Status.Usage != nil {
			if err := r.createTaskRecord(ctx, &task); err != nil {
				return ctrl.Result{}, err
			}
		}
		return r.applyTaskTTL(ctx, &task, ctrl.Result{})
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
			if resolveTaskWorkspaceRef(&task) == nil {
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

		admitted, result, err := r.checkBudgetAdmission(ctx, &task)
		if err != nil || !admitted {
			if task.Spec.Branch != "" {
				r.BranchLocker.Release(branchLockKey(&task), task.Name)
			}
			return result, err
		}

		return r.createJob(ctx, &task)
	}

	// Update status based on Job status
	result, err := r.updateStatus(ctx, &task, &job)
	if err != nil {
		return result, err
	}

	// Refresh the per-task GitHub App installation token when the job
	// is still running, so long-running agent pods keep working past
	// the 1h installation-token TTL. Errors are logged but do not
	// fail the reconcile — the cached token may still be valid and
	// the next reconcile retries.
	if job.Status.Active > 0 {
		if next, refreshErr := r.refreshGitHubAppTokenIfNeeded(ctx, &task); refreshErr != nil {
			logger.Error(refreshErr, "Unable to refresh GitHub App installation token")
			r.recordEvent(&task, corev1.EventTypeWarning, "GitHubTokenRefreshFailed", "Failed to refresh GitHub App installation token: %v", refreshErr)
			if next == 0 {
				next = tokenRefreshRetryInterval
			}
			if result.RequeueAfter == 0 || next < result.RequeueAfter {
				result.RequeueAfter = next
			}
		} else if next > 0 {
			if result.RequeueAfter == 0 || next < result.RequeueAfter {
				result.RequeueAfter = next
			}
		}
	}

	return r.applyTaskTTL(ctx, &task, result)
}

func (r *TaskReconciler) applyTaskTTL(ctx context.Context, task *kelos.Task, result ctrl.Result) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if expired, requeueAfter := r.ttlExpired(task); expired {
		logger.Info("Deleting Task due to TTL expiration", "task", task.Name)
		r.recordEvent(task, corev1.EventTypeNormal, "TaskExpired", "Deleting Task due to TTL expiration")
		if err := r.Delete(ctx, task); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			logger.Error(err, "Unable to delete expired Task")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	} else if requeueAfter > 0 {
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

		if task.Spec.WorkerPoolRef != nil {
			if task.Status.PodName != "" {
				var pod corev1.Pod
				if err := r.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Status.PodName}, &pod); err == nil {
					released, err := requestWorkerPodTaskCancellation(ctx, r.Client, &pod, task.Name)
					if err != nil {
						logger.Error(err, "Unable to request worker task cancellation", "pod", pod.Name, "task", task.Name)
						return ctrl.Result{}, err
					}
					if !released {
						logger.Info("Waiting for worker pod to release deleted task", "pod", pod.Name, "task", task.Name)
						return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
					}
				}
			}
		} else {
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

// clearWorkerPodAssignment removes task assignment annotations from a worker pod.
func (r *TaskReconciler) clearWorkerPodAssignment(ctx context.Context, pod *corev1.Pod) {
	logger := log.FromContext(ctx)

	if err := clearWorkerPodTaskAnnotations(ctx, r.Client, pod); err != nil {
		logger.Error(err, "Failed to clear worker pod assignment on task deletion", "pod", pod.Name)
	}
}

// createJob creates a Job for the Task.
func (r *TaskReconciler) createJob(ctx context.Context, task *kelos.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var workspace *kelos.WorkspaceSpec
	if resolveTaskWorkspaceRef(task) != nil {
		var ws kelos.Workspace
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: task.Namespace,
			Name:      resolveTaskWorkspaceRef(task).Name,
		}, &ws); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Workspace not found yet, requeuing", "workspace", resolveTaskWorkspaceRef(task).Name)
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			logger.Error(err, "Unable to fetch Workspace", "workspace", resolveTaskWorkspaceRef(task).Name)
			return ctrl.Result{}, err
		}
		workspace = &ws.Spec

		// Handle GitHub App authentication
		if workspace.SecretRef != nil {
			resolvedWorkspace, err := r.resolveGitHubAppToken(ctx, task, workspace)
			if err != nil {
				logger.Error(err, "Unable to resolve GitHub App token")
				message := fmt.Sprintf("Failed to resolve GitHub token: %v", err)
				r.recordEvent(task, corev1.EventTypeWarning, "GitHubTokenFailed", "%s", message)
				if updateErr := r.failTaskBeforeJob(ctx, task, message); updateErr != nil {
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
		}

		agentConfig = MergeAgentConfigs(specs)

		if len(agentConfig.Skills) > 0 {
			if err := r.validateSkillsAuthSecrets(ctx, task.Namespace, agentConfig.Skills); err != nil {
				logger.Error(err, "Unable to validate skills auth secrets")
				message := fmt.Sprintf("Failed to validate skills auth secret: %v", err)
				r.recordEvent(task, corev1.EventTypeWarning, "SkillsAuthSecretFailed", "%s", message)
				if updateErr := r.failTaskBeforeJob(ctx, task, message); updateErr != nil {
					logger.Error(updateErr, "Unable to update Task status")
				}
				return ctrl.Result{}, nil
			}
		}

		if len(agentConfig.MCPServers) > 0 {
			resolved, err := r.resolveMCPServerSecrets(ctx, task.Namespace, agentConfig.MCPServers)
			if err != nil {
				logger.Error(err, "Unable to resolve MCP server secrets")
				message := fmt.Sprintf("Failed to resolve MCP server secret: %v", err)
				r.recordEvent(task, corev1.EventTypeWarning, "MCPSecretFailed", "%s", message)
				if updateErr := r.failTaskBeforeJob(ctx, task, message); updateErr != nil {
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
		message := fmt.Sprintf("Failed to build Job: %v", err)
		r.recordEvent(task, corev1.EventTypeWarning, "JobBuildFailed", "%s", message)
		if updateErr := r.failTaskBeforeJob(ctx, task, message); updateErr != nil {
			logger.Error(updateErr, "Unable to update Task status")
		}
		return ctrl.Result{}, err
	}

	// Plugin content is delivered via a per-task ConfigMap that the Job's
	// plugin-setup init container mounts, so it must exist before the Job.
	if agentConfig != nil && len(agentConfig.Plugins) > 0 {
		configMap, err := buildPluginConfigMap(task, agentConfig.Plugins)
		if err != nil {
			// Deterministic (invalid component names, payload over budget) —
			// retrying can't succeed, so fail the Task like JobBuildFailed.
			logger.Error(err, "unable to build plugin ConfigMap")
			r.recordEvent(task, corev1.EventTypeWarning, "PluginConfigMapFailed", "Failed to build plugin ConfigMap: %v", err)
			updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
					return getErr
				}
				task.Status.Phase = kelos.TaskPhaseFailed
				task.Status.Message = fmt.Sprintf("Failed to build plugin ConfigMap: %v", err)
				return r.Status().Update(ctx, task)
			})
			if updateErr != nil {
				logger.Error(updateErr, "Unable to update Task status")
			}
			return ctrl.Result{}, err
		}
		if err := r.ensurePluginConfigMap(ctx, task, configMap); err != nil {
			// API-level failures are transient — requeue without failing the Task.
			logger.Error(err, "unable to ensure plugin ConfigMap")
			r.recordEvent(task, corev1.EventTypeWarning, "PluginConfigMapFailed", "Failed to ensure plugin ConfigMap: %v", err)
			return ctrl.Result{}, err
		}
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
	taskCreatedTotal.WithLabelValues(task.Namespace, resolveTaskType(task)).Inc()

	// Update status
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		task.Status.Phase = kelos.TaskPhasePending
		task.Status.JobName = job.Name
		// Clear any stale message from a prior Waiting state (e.g. a budget-blocked
		// or branch-lock wait) now that the Job has been created.
		task.Status.Message = ""
		return r.Status().Update(ctx, task)
	}); err != nil {
		logger.Error(err, "Unable to update Task status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *TaskReconciler) failTaskBeforeJob(ctx context.Context, task *kelos.Task, message string) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		task.Status.Phase = kelos.TaskPhaseFailed
		task.Status.Message = message
		if task.Status.CompletionTime == nil {
			now := metav1.Now()
			task.Status.CompletionTime = &now
		}
		return r.Status().Update(ctx, task)
	}); err != nil {
		return err
	}
	if task.Spec.Branch != "" && r.BranchLocker != nil {
		r.BranchLocker.Release(branchLockKey(task), task.Name)
	}
	return nil
}

func (r *TaskReconciler) validateSkillsAuthSecrets(ctx context.Context, namespace string, skills []kelos.SkillsShSpec) error {
	checked := map[string]struct{}{}
	for _, skill := range skills {
		if skill.SecretRef == nil {
			continue
		}
		if skill.SecretRef.Name == "" {
			return fmt.Errorf("skills.sh source %q has secretRef with empty name", skill.Source)
		}
		if _, ok := checked[skill.SecretRef.Name]; ok {
			continue
		}
		checked[skill.SecretRef.Name] = struct{}{}

		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: namespace,
			Name:      skill.SecretRef.Name,
		}, &secret); err != nil {
			return fmt.Errorf("fetching secret %q for skills.sh source %q: %w", skill.SecretRef.Name, skill.Source, err)
		}
		token, ok := secret.Data[GitHubTokenSecretKey]
		if !ok {
			return fmt.Errorf("secret %q has no key %q for skills.sh source %q", skill.SecretRef.Name, GitHubTokenSecretKey, skill.Source)
		}
		if len(token) == 0 {
			return fmt.Errorf("secret %q has empty key %q for skills.sh source %q", skill.SecretRef.Name, GitHubTokenSecretKey, skill.Source)
		}
	}
	return nil
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

	tokenSecretName := githubTokenSecretName(task.Name)
	if _, err := mintGitHubAppTokenSecret(ctx, r.Client, r.Scheme, r.TokenClient, task, tokenSecretName, workspace.SecretRef.Name, workspace.Repo, creds); err != nil {
		return nil, err
	}

	// Return a modified workspace spec that points to the generated token secret
	resolved := *workspace
	resolved.SecretRef = &kelos.SecretReference{
		Name: tokenSecretName,
	}
	return &resolved, nil
}

// ensurePluginConfigMap creates or updates the per-task ConfigMap that
// carries plugin skill/agent content. The Job mounts this ConfigMap into
// the plugin-setup init container instead of inlining base64 content into
// the init script, which would exceed Linux's 128KiB MAX_ARG_STRLEN limit
// for a single execve argument once plugin content grows large. The
// ConfigMap is owned by the Task, so garbage collection removes it
// together with the Task. Building the ConfigMap (validation, size budget)
// happens in the caller so deterministic failures can fail the Task;
// errors returned here are API-level and treated as transient.
func (r *TaskReconciler) ensurePluginConfigMap(ctx context.Context, task *kelos.Task, configMap *corev1.ConfigMap) error {
	if err := controllerutil.SetControllerReference(task, configMap, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on plugin ConfigMap: %w", err)
	}

	if err := r.Create(ctx, configMap); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating plugin ConfigMap: %w", err)
		}
		// Update existing ConfigMap so reconciles pick up content changes.
		// Only adopt a ConfigMap this Task already controls: a pre-existing
		// ConfigMap that happens to share the generated name but is unowned
		// or owned by something else must not be overwritten and later
		// garbage-collected with the Task. Re-assert the controller
		// reference too, since an existing ConfigMap with a stripped
		// ownerRef would otherwise outlive its Task. Concurrent reconciles
		// can race here, so retry on conflict like the other update paths in
		// this controller.
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			existing := &corev1.ConfigMap{}
			if err := r.Get(ctx, client.ObjectKey{Name: configMap.Name, Namespace: configMap.Namespace}, existing); err != nil {
				return err
			}
			if !metav1.IsControlledBy(existing, task) {
				return fmt.Errorf("plugin ConfigMap %q already exists and is not controlled by this Task", configMap.Name)
			}
			existing.Data = configMap.Data
			if err := controllerutil.SetControllerReference(task, existing, r.Scheme); err != nil {
				return err
			}
			return r.Update(ctx, existing)
		})
		if updateErr != nil {
			return fmt.Errorf("updating plugin ConfigMap: %w", updateErr)
		}
	}

	return nil
}

// refreshGitHubAppTokenIfNeeded re-mints the per-task GitHub App installation
// token when it is close to expiry, so long-running agent pods keep working
// past the 1h installation-token TTL. The function is a no-op for tasks whose
// workspace does not use GitHub App credentials. It returns the duration
// until the next refresh should run, or 0 when no refresh is applicable.
func (r *TaskReconciler) refreshGitHubAppTokenIfNeeded(ctx context.Context, task *kelos.Task) (time.Duration, error) {
	logger := log.FromContext(ctx)

	tokenSecretName := githubTokenSecretName(task.Name)

	// Resolve the workspace repo so refreshed tokens are minted against the
	// correct GitHub API host for GitHub Enterprise workspaces.
	repoURL := ""
	if ref := resolveTaskWorkspaceRef(task); ref != nil {
		var ws kelos.Workspace
		if err := r.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: ref.Name}, &ws); err != nil {
			// Fall back to the controller-wide BaseURL. Log at V(1) so
			// operators can correlate a later 401 against the wrong
			// API host with the lookup that failed here.
			logger.V(1).Info("Unable to fetch workspace for token refresh, using default API base URL", "workspace", ref.Name, "error", err)
		} else {
			repoURL = ws.Spec.Repo
		}
	}

	next, expiresAt, refreshed, err := refreshGitHubAppTokenSecret(ctx, r.Client, r.TokenClient, task.Namespace, tokenSecretName, repoURL)
	if refreshed {
		logger.Info("Refreshed GitHub App installation token", "task", task.Name, "expiresAt", expiresAt)
		r.recordEvent(task, corev1.EventTypeNormal, "GitHubTokenRefreshed", "Refreshed GitHub App installation token (expires at %s)", expiresAt.UTC().Format(time.RFC3339))
	}
	return next, err
}

func tokenRefreshFailureRetryAfter(expiresAt time.Time) time.Duration {
	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		return tokenRefreshRetryInterval
	}
	retryAfter := remaining / 2
	if retryAfter > tokenRefreshRetryInterval {
		return tokenRefreshRetryInterval
	}
	if retryAfter < time.Second {
		return time.Second
	}
	return retryAfter
}

func (r *TaskReconciler) resolveMCPServerSecrets(ctx context.Context, namespace string, servers []kelos.MCPServerSpec) ([]kelos.MCPServerSpec, error) {
	return resolveMCPServerSecrets(ctx, r.Client, namespace, servers)
}

// resolveMCPServerEnv resolves the Env entries of a single MCP server to a
// slice of literal Name/Value pairs. Entries with ValueFrom are resolved by
// fetching the referenced Secret key or ConfigMap key. EnvFrom (whole-secret)
// is then merged on top so its keys take precedence on collision, matching
// the existing behaviour.
func resolveMCPServerEnv(ctx context.Context, reader client.Reader, namespace string, server kelos.MCPServerSpec) ([]corev1.EnvVar, error) {
	// Resolve inline Env entries into a map keyed by name so EnvFrom can
	// override and so the final list has no duplicate names.
	values := make(map[string]string, len(server.Env))
	order := make([]string, 0, len(server.Env))
	for _, entry := range server.Env {
		if entry.Name == "" {
			return nil, fmt.Errorf("MCP server %q has an env entry with an empty name", server.Name)
		}

		value, ok, err := resolveEnvVarValue(ctx, reader, namespace, server.Name, entry)
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
		if err := reader.Get(ctx, client.ObjectKey{
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
func resolveEnvVarValue(ctx context.Context, reader client.Reader, namespace, serverName string, entry corev1.EnvVar) (string, bool, error) {
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
		if err := reader.Get(ctx, client.ObjectKey{
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
		if err := reader.Get(ctx, client.ObjectKey{
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

// resolveMCPServerSecrets resolves all secret-backed references (headersFrom,
// envFrom, env[].valueFrom) in a slice of MCP server specs into literal values.
func resolveMCPServerSecrets(ctx context.Context, reader client.Reader, namespace string, servers []kelos.MCPServerSpec) ([]kelos.MCPServerSpec, error) {
	resolved := make([]kelos.MCPServerSpec, len(servers))
	for i, server := range servers {
		resolved[i] = server

		if server.HeadersFrom != nil {
			var secret corev1.Secret
			if err := reader.Get(ctx, client.ObjectKey{
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

		resolvedEnv, err := resolveMCPServerEnv(ctx, reader, namespace, server)
		if err != nil {
			return nil, err
		}
		resolved[i].Env = resolvedEnv
		resolved[i].EnvFrom = nil
	}

	return resolved, nil
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
			taskCompletedTotal.WithLabelValues(task.Namespace, resolveTaskType(task), string(kelos.TaskPhaseSucceeded)).Inc()
		}
	} else if isJobFailed(job) {
		if task.Status.Phase != kelos.TaskPhaseFailed {
			newPhase = kelos.TaskPhaseFailed
			newMessage = "Task failed"
			setCompletionTime = true
			r.recordEvent(task, corev1.EventTypeWarning, "TaskFailed", "Task failed")
			taskCompletedTotal.WithLabelValues(task.Namespace, resolveTaskType(task), string(kelos.TaskPhaseFailed)).Inc()
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
		if isTerminalTaskPhase(task.Status.Phase) && task.Status.Usage != nil {
			if err := r.createTaskRecord(ctx, task); err != nil {
				return ctrl.Result{}, err
			}
		}
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
				task.Status.Usage = usageFromResults(results)
			}
		}
		if retryOutputs && (outputs != nil || results != nil) {
			task.Status.Outputs = outputs
			task.Status.Results = results
			task.Status.Usage = usageFromResults(results)
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
		taskDurationSeconds.WithLabelValues(task.Namespace, resolveTaskType(task), string(newPhase)).Observe(duration)
	}

	// Record cost and token metrics when results are available
	if (setCompletionTime || retryOutputs) && results != nil {
		RecordCostTokenMetrics(task, results)
	}

	// Create a TaskRecord for budget accounting whenever a terminal Task has
	// usage data. Keying on the terminal state (rather than only the transition
	// or retry-output path) ensures a transient create failure is retried on a
	// later reconcile instead of permanently undercounting budget usage.
	// createTaskRecord is idempotent, so repeated calls are safe.
	// TaskRecord TTL GC is handled by the dedicated TaskRecordReconciler.
	if isTerminalTaskPhase(task.Status.Phase) && task.Status.Usage != nil {
		if err := r.createTaskRecord(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
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
	if !isTerminalTaskPhase(task.Status.Phase) {
		return false, 0
	}
	if task.Status.CompletionTime == nil {
		// TTL is configured but CompletionTime hasn't been set yet; requeue
		// to avoid stopping all reconciles for a dangling terminal task.
		return false, 5 * time.Second
	}

	ttl := time.Duration(*task.Spec.TTLSecondsAfterFinished) * time.Second
	expireAt := task.Status.CompletionTime.Add(ttl)
	remaining := time.Until(expireAt)
	if remaining <= 0 {
		return true, 0
	}
	return false, remaining
}

func isTerminalTaskPhase(phase kelos.TaskPhase) bool {
	return phase == kelos.TaskPhaseSucceeded || phase == kelos.TaskPhaseFailed
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
			taskCompletedTotal.WithLabelValues(task.Namespace, resolveTaskType(task), string(kelos.TaskPhaseFailed)).Inc()
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
	if resolveTaskWorkspaceRef(task) != nil {
		ws = resolveTaskWorkspaceRef(task).Name
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
	model := resolveTaskModel(task)
	labels := []string{task.Namespace, resolveTaskType(task), spawner, model}

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

// usageFromResults parses cost-usd, input-tokens, output-tokens from the
// string results map and returns a TaskUsage struct. Returns nil if no
// usage data is present.
func usageFromResults(results map[string]string) *kelos.TaskUsage {
	if len(results) == 0 {
		return nil
	}

	var usage kelos.TaskUsage
	hasData := false

	if costStr, ok := results["cost-usd"]; ok && costStr != "" {
		q, err := resource.ParseQuantity(costStr)
		if err == nil && !q.IsZero() && q.Cmp(resource.Quantity{}) >= 0 {
			usage.CostUSD = &q
			hasData = true
		}
	}
	if inputStr, ok := results["input-tokens"]; ok && inputStr != "" {
		tokens, err := strconv.ParseInt(inputStr, 10, 64)
		if err == nil && tokens > 0 {
			usage.InputTokens = &tokens
			hasData = true
		}
	}
	if outputStr, ok := results["output-tokens"]; ok && outputStr != "" {
		tokens, err := strconv.ParseInt(outputStr, 10, 64)
		if err == nil && tokens > 0 {
			usage.OutputTokens = &tokens
			hasData = true
		}
	}

	if !hasData {
		return nil
	}
	return &usage
}

const (
	// defaultTaskRecordTTL is the default retention period for TaskRecords
	// (30 days). Records older than this are garbage-collected.
	defaultTaskRecordTTL int32 = 30 * 24 * 60 * 60
)

// now returns the current time, using NowFunc if set for testability.
func (r *TaskReconciler) now() time.Time {
	if r.NowFunc != nil {
		return r.NowFunc()
	}
	return time.Now()
}

// budget returns a budgetEnforcer bound to this reconciler's client and clock.
func (r *TaskReconciler) budget() *budgetEnforcer {
	return &budgetEnforcer{Client: r.Client, now: r.now}
}

// checkBudgetAdmission checks all matching TaskBudgets before job creation.
func (r *TaskReconciler) checkBudgetAdmission(ctx context.Context, task *kelos.Task) (bool, ctrl.Result, error) {
	return r.budget().checkBudgetAdmission(ctx, task)
}

// createTaskRecord creates an immutable TaskRecord for a completed Task.
func (r *TaskReconciler) createTaskRecord(ctx context.Context, task *kelos.Task) error {
	return r.budget().createTaskRecord(ctx, task)
}

// sumPeriodUsage sums usage from TaskRecords matching the selector within the period.
func (r *TaskReconciler) sumPeriodUsage(ctx context.Context, namespace string, selector labels.Selector, periodStart, periodEnd time.Time) (*kelos.TaskUsage, error) {
	return r.budget().sumPeriodUsage(ctx, namespace, selector, periodStart, periodEnd)
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
		Watches(&kelos.TaskBudget{}, handler.EnqueueRequestsFromMapFunc(r.enqueueTasksBlockedByBudget)).
		Complete(r)
}

// enqueueTasksBlockedByBudget returns reconcile requests for Tasks in Waiting
// phase that have a BudgetBlocked condition. This allows budget-blocked tasks
// to be re-evaluated when a TaskBudget changes (e.g. period rolls over).
func (r *TaskReconciler) enqueueTasksBlockedByBudget(ctx context.Context, obj client.Object) []reconcile.Request {
	budget, ok := obj.(*kelos.TaskBudget)
	if !ok {
		return nil
	}

	var taskList kelos.TaskList
	if err := r.List(ctx, &taskList, client.InNamespace(budget.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, t := range taskList.Items {
		if t.Status.Phase != kelos.TaskPhaseWaiting {
			continue
		}
		for _, c := range t.Status.Conditions {
			if c.Type == "BudgetBlocked" && c.Status == metav1.ConditionTrue {
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&t),
				})
				break
			}
		}
	}
	return requests
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
