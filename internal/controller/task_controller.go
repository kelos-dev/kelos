package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/githubapp"
)

const (
	taskFinalizer = "axon.io/finalizer"

	// outputRetryWindow is the maximum duration after CompletionTime
	// during which the controller retries reading Pod logs for outputs.
	outputRetryWindow = 30 * time.Second

	// outputRetryInterval is the delay between output capture retries.
	outputRetryInterval = 5 * time.Second
)

// TaskReconciler reconciles a Task object.
type TaskReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	JobBuilder  *JobBuilder
	Clientset   kubernetes.Interface
	TokenClient *githubapp.TokenClient
	Recorder    record.EventRecorder
}

// +kubebuilder:rbac:groups=axon.io,resources=tasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=axon.io,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=axon.io,resources=tasks/finalizers,verbs=update
// +kubebuilder:rbac:groups=axon.io,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=axon.io,resources=agentconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles Task reconciliation.
func (r *TaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var task axonv1alpha1.Task
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
func (r *TaskReconciler) handleDeletion(ctx context.Context, task *axonv1alpha1.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(task, taskFinalizer) {
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
func (r *TaskReconciler) createJob(ctx context.Context, task *axonv1alpha1.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var workspace *axonv1alpha1.WorkspaceSpec
	if task.Spec.WorkspaceRef != nil {
		var ws axonv1alpha1.Workspace
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
					task.Status.Phase = axonv1alpha1.TaskPhaseFailed
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

	var agentConfig *axonv1alpha1.AgentConfigSpec
	if task.Spec.AgentConfigRef != nil {
		var ac axonv1alpha1.AgentConfig
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: task.Namespace,
			Name:      task.Spec.AgentConfigRef.Name,
		}, &ac); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("AgentConfig not found yet, requeuing", "agentConfig", task.Spec.AgentConfigRef.Name)
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			logger.Error(err, "Unable to fetch AgentConfig", "agentConfig", task.Spec.AgentConfigRef.Name)
			return ctrl.Result{}, err
		}
		agentConfig = &ac.Spec
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
			task.Status.Phase = axonv1alpha1.TaskPhaseFailed
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
		task.Status.Phase = axonv1alpha1.TaskPhasePending
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
func (r *TaskReconciler) resolveGitHubAppToken(ctx context.Context, task *axonv1alpha1.Task, workspace *axonv1alpha1.WorkspaceSpec) (*axonv1alpha1.WorkspaceSpec, error) {
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

	tokenResp, err := r.TokenClient.GenerateInstallationToken(ctx, creds)
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
	resolved.SecretRef = &axonv1alpha1.SecretReference{
		Name: tokenSecretName,
	}
	return &resolved, nil
}

// updateStatus updates Task status based on Job status.
func (r *TaskReconciler) updateStatus(ctx context.Context, task *axonv1alpha1.Task, job *batchv1.Job) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Discover pod name for the task
	var podName string
	if task.Status.PodName == "" {
		var pods corev1.PodList
		if err := r.List(ctx, &pods, client.InNamespace(task.Namespace), client.MatchingLabels{
			"axon.io/task": task.Name,
		}); err == nil && len(pods.Items) > 0 {
			podName = pods.Items[0].Name
		}
	}

	// Determine the new phase based on Job status
	var newPhase axonv1alpha1.TaskPhase
	var newMessage string
	var setStartTime, setCompletionTime bool

	if job.Status.Active > 0 {
		if task.Status.Phase != axonv1alpha1.TaskPhaseRunning {
			newPhase = axonv1alpha1.TaskPhaseRunning
			setStartTime = true
			r.recordEvent(task, corev1.EventTypeNormal, "TaskRunning", "Task started running")
		}
	} else if job.Status.Succeeded > 0 {
		if task.Status.Phase != axonv1alpha1.TaskPhaseSucceeded {
			newPhase = axonv1alpha1.TaskPhaseSucceeded
			newMessage = "Task completed successfully"
			setCompletionTime = true
			r.recordEvent(task, corev1.EventTypeNormal, "TaskSucceeded", "Task completed successfully")
			taskCompletedTotal.WithLabelValues(task.Namespace, task.Spec.Type, string(axonv1alpha1.TaskPhaseSucceeded)).Inc()
		}
	} else if job.Status.Failed > 0 {
		if task.Status.Phase != axonv1alpha1.TaskPhaseFailed {
			newPhase = axonv1alpha1.TaskPhaseFailed
			newMessage = "Task failed"
			setCompletionTime = true
			r.recordEvent(task, corev1.EventTypeWarning, "TaskFailed", "Task failed")
			taskCompletedTotal.WithLabelValues(task.Namespace, task.Spec.Type, string(axonv1alpha1.TaskPhaseFailed)).Inc()
		}
	}

	podNameChanged := podName != "" && task.Status.PodName != podName
	phaseChanged := newPhase != ""

	// Check if we should retry capturing outputs for an already-completed task
	retryOutputs := !phaseChanged &&
		len(task.Status.Outputs) == 0 &&
		task.Status.CompletionTime != nil &&
		time.Since(task.Status.CompletionTime.Time) < outputRetryWindow

	if !phaseChanged && !podNameChanged && !retryOutputs {
		return ctrl.Result{}, nil
	}

	// Read outputs from Pod logs when transitioning to a terminal phase
	// or retrying capture for an already-completed task
	var outputs []string
	if setCompletionTime || retryOutputs {
		effectivePodName := podName
		if effectivePodName == "" {
			effectivePodName = task.Status.PodName
		}
		containerName := task.Spec.Type
		outputs = r.readOutputs(ctx, task.Namespace, effectivePodName, containerName)
	}

	// When retrying output capture, skip the status update if we still
	// have nothing — just requeue to try again later.
	if retryOutputs && outputs == nil {
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
			}
		}
		if retryOutputs && outputs != nil {
			task.Status.Outputs = outputs
		}
		return r.Status().Update(ctx, task)
	}); err != nil {
		logger.Error(err, "Unable to update Task status")
		reconcileErrorsTotal.WithLabelValues("task").Inc()
		return ctrl.Result{}, err
	}

	// Record task duration when completion time is set and we have a start time
	if setCompletionTime && task.Status.StartTime != nil {
		duration := task.Status.CompletionTime.Time.Sub(task.Status.StartTime.Time).Seconds()
		taskDurationSeconds.WithLabelValues(task.Namespace, task.Spec.Type, string(newPhase)).Observe(duration)
	}

	if setCompletionTime && outputs != nil {
		r.recordEvent(task, corev1.EventTypeNormal, "OutputsCaptured", "Captured %d outputs from agent", len(outputs))
	}

	// Requeue to retry output capture when the initial attempt got nothing
	if setCompletionTime && outputs == nil {
		return ctrl.Result{RequeueAfter: outputRetryInterval}, nil
	}

	return ctrl.Result{}, nil
}

// ttlExpired checks whether a finished Task has exceeded its TTL.
// It returns (true, 0) if the Task should be deleted now, or (false, duration)
// if the Task should be requeued after the given duration.
func (r *TaskReconciler) ttlExpired(task *axonv1alpha1.Task) (bool, time.Duration) {
	if task.Spec.TTLSecondsAfterFinished == nil {
		return false, 0
	}
	if task.Status.Phase != axonv1alpha1.TaskPhaseSucceeded && task.Status.Phase != axonv1alpha1.TaskPhaseFailed {
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

// readOutputs reads Pod logs and extracts output markers.
func (r *TaskReconciler) readOutputs(ctx context.Context, namespace, podName, container string) []string {
	if r.Clientset == nil || podName == "" {
		return nil
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
		return nil
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		logger.V(1).Info("Unable to read Pod log stream", "pod", podName, "error", err)
		return nil
	}

	return ParseOutputs(string(data))
}

// recordEvent records a Kubernetes Event on the given object if a Recorder is configured.
func (r *TaskReconciler) recordEvent(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// checkDependencies verifies that all tasks listed in DependsOn have succeeded.
// Returns (ready, result, error). ready=true means all dependencies succeeded.
func (r *TaskReconciler) checkDependencies(ctx context.Context, task *axonv1alpha1.Task) (bool, ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Cycle detection only on first check (skip if already Waiting)
	if task.Status.Phase != axonv1alpha1.TaskPhaseWaiting {
		if err := r.detectCycle(ctx, task); err != nil {
			logger.Info("Circular dependency detected", "task", task.Name, "error", err)
			updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
					return getErr
				}
				task.Status.Phase = axonv1alpha1.TaskPhaseFailed
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
		var depTask axonv1alpha1.Task
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

		if depTask.Status.Phase == axonv1alpha1.TaskPhaseFailed {
			logger.Info("Dependency failed", "dependency", depName)
			updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
					return getErr
				}
				task.Status.Phase = axonv1alpha1.TaskPhaseFailed
				task.Status.Message = fmt.Sprintf("Dependency %q failed", depName)
				now := metav1.Now()
				task.Status.CompletionTime = &now
				return r.Status().Update(ctx, task)
			})
			if updateErr != nil {
				logger.Error(updateErr, "Unable to update Task status")
			}
			r.recordEvent(task, corev1.EventTypeWarning, "DependencyFailed", "Dependency %q failed", depName)
			taskCompletedTotal.WithLabelValues(task.Namespace, task.Spec.Type, string(axonv1alpha1.TaskPhaseFailed)).Inc()
			return false, ctrl.Result{}, nil
		}

		if depTask.Status.Phase != axonv1alpha1.TaskPhaseSucceeded {
			logger.Info("Dependency not ready", "dependency", depName, "phase", depTask.Status.Phase)
			r.setWaitingPhase(ctx, task, fmt.Sprintf("Waiting for dependency %q", depName))
			return false, ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	return true, ctrl.Result{}, nil
}

// detectCycle walks the dependency graph from the given task and returns an
// error if a cycle is detected.
func (r *TaskReconciler) detectCycle(ctx context.Context, task *axonv1alpha1.Task) error {
	visited := make(map[string]bool)
	return r.walkDeps(ctx, task.Namespace, task.Name, visited)
}

func (r *TaskReconciler) walkDeps(ctx context.Context, namespace, name string, visited map[string]bool) error {
	if visited[name] {
		return fmt.Errorf("cycle involves %q", name)
	}
	visited[name] = true

	var t axonv1alpha1.Task
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

// setWaitingPhase updates the task phase to Waiting with the given message.
func (r *TaskReconciler) setWaitingPhase(ctx context.Context, task *axonv1alpha1.Task, message string) {
	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		if task.Status.Phase == axonv1alpha1.TaskPhaseWaiting && task.Status.Message == message {
			return nil
		}
		task.Status.Phase = axonv1alpha1.TaskPhaseWaiting
		task.Status.Message = message
		return r.Status().Update(ctx, task)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to update Task status to Waiting")
	}
}

// resolvePromptTemplate resolves Go template references in the prompt using
// dependency outputs. Falls back to the raw prompt on any error.
func (r *TaskReconciler) resolvePromptTemplate(ctx context.Context, task *axonv1alpha1.Task) string {
	logger := log.FromContext(ctx)

	if len(task.Spec.DependsOn) == 0 {
		return task.Spec.Prompt
	}

	deps := make(map[string]interface{})
	for _, depName := range task.Spec.DependsOn {
		var depTask axonv1alpha1.Task
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: task.Namespace, Name: depName,
		}, &depTask); err != nil {
			logger.Info("Failed to fetch dependency for prompt template, using raw prompt", "dependency", depName, "error", err)
			return task.Spec.Prompt
		}
		deps[depName] = map[string]interface{}{
			"Outputs": depTask.Status.Outputs,
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

// SetupWithManager sets up the controller with the Manager.
func (r *TaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&axonv1alpha1.Task{}).
		Owns(&batchv1.Job{}).
		Watches(&axonv1alpha1.Task{}, handler.EnqueueRequestsFromMapFunc(r.enqueueDependentTasks)).
		Complete(r)
}

// enqueueDependentTasks returns reconcile requests for tasks that depend on the
// given task. This ensures dependent tasks are reconciled immediately when a
// dependency reaches a terminal phase, instead of waiting for a requeue timer.
func (r *TaskReconciler) enqueueDependentTasks(ctx context.Context, obj client.Object) []reconcile.Request {
	task, ok := obj.(*axonv1alpha1.Task)
	if !ok {
		return nil
	}

	// Only trigger when a task reaches a terminal phase
	if task.Status.Phase != axonv1alpha1.TaskPhaseSucceeded && task.Status.Phase != axonv1alpha1.TaskPhaseFailed {
		return nil
	}

	var taskList axonv1alpha1.TaskList
	if err := r.List(ctx, &taskList, client.InNamespace(task.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, t := range taskList.Items {
		for _, dep := range t.Spec.DependsOn {
			if dep == task.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&t),
				})
				break
			}
		}
	}
	return requests
}
