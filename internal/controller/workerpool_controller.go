/*
Copyright 2026 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/utils/ptr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

const (
	WorkerRunnerServiceAccount = "kelos-worker-runner"
	WorkerRunnerClusterRole    = "kelos-worker-runner-role"
	WorkerComponentLabel       = "worker"
	DefaultWorkerRunnerImage   = "ghcr.io/kelos-dev/kelos-worker-runner:latest"

	labelWorkerPool    = "kelos.dev/workerpool"
	labelComponent     = "kelos.dev/component"
	labelManagedBy     = "kelos.dev/managed-by"
	labelName          = "kelos.dev/name"
	labelExecutionMode = "kelos.dev/execution-mode"
	annotationPoolName = "kelos.dev/workerpool-name"
	taskStartMarker    = "---KELOS_TASK_START---"
	taskEndMarker      = "---KELOS_TASK_END---"
)

// WorkerPoolReconciler reconciles WorkerPool objects and assigns Tasks to worker pods.
type WorkerPoolReconciler struct {
	client.Client
	Scheme                      *runtime.Scheme
	Recorder                    record.EventRecorder
	Clientset                   kubernetes.Interface
	WorkerRunnerImage           string
	WorkerRunnerImagePullPolicy corev1.PullPolicy
	ClaudeCodeImage             string
	ClaudeCodeImagePullPolicy   corev1.PullPolicy
	CodexImage                  string
	CodexImagePullPolicy        corev1.PullPolicy
	GeminiImage                 string
	GeminiImagePullPolicy       corev1.PullPolicy
	OpenCodeImage               string
	OpenCodeImagePullPolicy     corev1.PullPolicy
	CursorImage                 string
	CursorImagePullPolicy       corev1.PullPolicy

	// TokenClient mints GitHub App installation tokens for workspaces backed
	// by a GitHub App secret. Required for App-backed pools; PAT-style
	// workspaces do not use it.
	TokenClient *githubapp.TokenClient

	// NowFunc returns the current time. Defaults to time.Now.
	// Overridable in tests for deterministic behavior.
	NowFunc func() time.Time
}

// now returns the current time, using NowFunc if set for testability.
func (r *WorkerPoolReconciler) now() time.Time {
	if r.NowFunc != nil {
		return r.NowFunc()
	}
	return time.Now()
}

// budget returns a budgetEnforcer bound to this reconciler's client and clock,
// so worker-pool Tasks share the same budget admission and accounting logic as
// Job-backed Tasks.
func (r *WorkerPoolReconciler) budget() *budgetEnforcer {
	return &budgetEnforcer{Client: r.Client, now: r.now}
}

// +kubebuilder:rbac:groups=kelos.dev,resources=workerpools,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=workerpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=agentconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update

// Reconcile handles both WorkerPool infrastructure and Task assignment.
func (r *WorkerPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pool kelos.WorkerPool
	poolErr := r.Get(ctx, req.NamespacedName, &pool)
	if poolErr != nil && !apierrors.IsNotFound(poolErr) {
		logger.Error(poolErr, "Unable to fetch WorkerPool")
		return ctrl.Result{}, poolErr
	}

	var task kelos.Task
	taskErr := r.Get(ctx, req.NamespacedName, &task)
	if taskErr != nil && !apierrors.IsNotFound(taskErr) {
		logger.Error(taskErr, "Unable to fetch Task")
		return ctrl.Result{}, taskErr
	}

	if apierrors.IsNotFound(poolErr) && apierrors.IsNotFound(taskErr) {
		return ctrl.Result{}, nil
	}

	var result ctrl.Result
	if poolErr == nil {
		poolResult, err := r.reconcilePool(ctx, &pool)
		if err != nil {
			return poolResult, err
		}
		result = mergeReconcileResults(result, poolResult)
	}

	if taskErr == nil && task.Spec.WorkerPoolRef != nil {
		taskResult, err := r.reconcileTask(ctx, &task)
		if err != nil {
			return taskResult, err
		}
		result = mergeReconcileResults(result, taskResult)
	}

	return result, nil
}

func (r *WorkerPoolReconciler) reconcilePool(ctx context.Context, pool *kelos.WorkerPool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if err := r.ensureWorkerRBAC(ctx, pool.Namespace); err != nil {
		logger.Error(err, "Unable to ensure worker RBAC", "workerpool", pool.Name)
		return ctrl.Result{}, err
	}

	stsName := workerPoolStatefulSetName(pool.Name)
	svcName := stsName

	// Resolve Workspace
	if pool.Spec.Worker.WorkspaceRef == nil {
		return ctrl.Result{}, fmt.Errorf("workerpool %s has nil worker.workspaceRef", pool.Name)
	}
	var workspace *kelos.WorkspaceSpec
	var ws kelos.Workspace
	if err := r.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: pool.Spec.Worker.WorkspaceRef.Name}, &ws); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Workspace not found yet, requeuing", "workspace", pool.Spec.Worker.WorkspaceRef.Name)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		logger.Error(err, "Unable to fetch Workspace", "workspace", pool.Spec.Worker.WorkspaceRef.Name)
		return ctrl.Result{}, err
	}
	workspace = &ws.Spec

	// Resolve GitHub App-backed workspace secrets into a derived Secret
	// holding a short-lived installation token, minting it on first use and
	// re-minting it before expiry. PAT-style secrets are used as-is.
	var refreshAfter time.Duration
	if workspace.SecretRef != nil {
		resolvedWorkspace, next, err := r.ensureGitHubAppToken(ctx, pool, workspace)
		if err != nil {
			logger.Error(err, "Unable to ensure GitHub App installation token", "workerpool", pool.Name)
			r.recordEvent(pool, corev1.EventTypeWarning, "GitHubTokenFailed", "Failed to ensure GitHub App installation token: %v", err)
			return ctrl.Result{}, err
		}
		workspace = resolvedWorkspace
		refreshAfter = next
	}

	// Resolve AgentConfig
	var agentConfig *kelos.AgentConfigSpec
	refs := resolveWorkerPoolAgentConfigRefs(pool)
	if len(refs) > 0 {
		var specs []kelos.AgentConfigSpec
		for _, ref := range refs {
			var ac kelos.AgentConfig
			if err := r.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: ref.Name}, &ac); err != nil {
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
	}

	// Resolve MCP server secrets before building the pod spec
	if agentConfig != nil && len(agentConfig.MCPServers) > 0 {
		resolved, err := resolveMCPServerSecrets(ctx, r.Client, pool.Namespace, agentConfig.MCPServers)
		if err != nil {
			logger.Error(err, "Unable to resolve MCP server secrets", "workerpool", pool.Name)
			return ctrl.Result{}, err
		}
		agentConfig.MCPServers = resolved
	}

	// Reconcile plugin ConfigMap if plugins are configured
	if agentConfig != nil && len(agentConfig.Plugins) > 0 {
		if err := r.reconcilePluginConfigMap(ctx, pool, agentConfig.Plugins); err != nil {
			logger.Error(err, "Unable to reconcile plugin ConfigMap", "workerpool", pool.Name)
			return ctrl.Result{}, err
		}
	}

	// Reconcile headless Service
	if err := r.reconcileService(ctx, pool, svcName); err != nil {
		logger.Error(err, "Unable to reconcile Service", "workerpool", pool.Name)
		return ctrl.Result{}, err
	}

	// Reconcile StatefulSet
	result, err := r.reconcileStatefulSet(ctx, pool, stsName, svcName, workspace, agentConfig)
	if err != nil {
		logger.Error(err, "Unable to reconcile StatefulSet", "workerpool", pool.Name)
		return ctrl.Result{}, err
	}

	// Schedule the next GitHub App token refresh alongside any StatefulSet
	// requeue so the derived token Secret is re-minted before it expires.
	if refreshAfter > 0 {
		result = mergeReconcileResults(result, ctrl.Result{RequeueAfter: refreshAfter})
	}

	return result, nil
}

// ensureGitHubAppToken resolves a workspace secret that holds GitHub App
// credentials into a derived Secret containing a short-lived installation
// token, minting it on first use and re-minting it before expiry. It returns a
// workspace spec pointing at the derived token Secret (or the original
// workspace when the secret is a plain PAT or is not present yet), and the
// duration until the next token refresh should be scheduled (0 when no refresh
// is applicable).
func (r *WorkerPoolReconciler) ensureGitHubAppToken(ctx context.Context, pool *kelos.WorkerPool, workspace *kelos.WorkspaceSpec) (*kelos.WorkspaceSpec, time.Duration, error) {
	logger := log.FromContext(ctx)

	var appSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: workspace.SecretRef.Name}, &appSecret); err != nil {
		if apierrors.IsNotFound(err) {
			// Secret not present yet; leave the workspace untouched and let a
			// later reconcile (triggered by the secret watch) resolve it.
			return workspace, 0, nil
		}
		return nil, 0, fmt.Errorf("workerpool %s: fetching workspace secret %q: %w", pool.Name, workspace.SecretRef.Name, err)
	}

	if !githubapp.IsGitHubApp(appSecret.Data) {
		return workspace, 0, nil
	}

	if r.TokenClient == nil {
		return nil, 0, fmt.Errorf("workerpool %s: workspace %q uses a GitHub App secret but TokenClient is not configured", pool.Name, workspace.SecretRef.Name)
	}

	tokenSecretName := githubTokenSecretName(pool.Name)

	// Mint the derived token Secret if it does not exist yet. When it already
	// exists, the refresh below re-mints it only when close to expiry, so we
	// avoid a GitHub API call on every reconcile.
	var derived corev1.Secret
	switch err := r.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: tokenSecretName}, &derived); {
	case err != nil && !apierrors.IsNotFound(err):
		return nil, 0, fmt.Errorf("workerpool %s: fetching token secret %q: %w", pool.Name, tokenSecretName, err)
	case apierrors.IsNotFound(err):
		logger.Info("Detected GitHub App secret, generating installation token", "workerpool", pool.Name, "secret", workspace.SecretRef.Name)
		creds, err := githubapp.ParseCredentials(appSecret.Data)
		if err != nil {
			return nil, 0, fmt.Errorf("workerpool %s: parsing GitHub App credentials: %w", pool.Name, err)
		}
		if _, err := mintGitHubAppTokenSecret(ctx, r.Client, r.Scheme, r.TokenClient, pool, tokenSecretName, workspace.SecretRef.Name, workspace.Repo, creds); err != nil {
			return nil, 0, fmt.Errorf("workerpool %s: %w", pool.Name, err)
		}
	case !metav1.IsControlledBy(&derived, pool):
		// A Task and a WorkerPool can share a name in a namespace and both
		// derive <name>-github-token. Refuse to refresh or mount a token
		// Secret owned by another resource: doing so would mint into and point
		// the pool's pods at a Secret garbage-collected with a different owner.
		return nil, 0, fmt.Errorf("workerpool %s: token secret %q already exists and is not controlled by this WorkerPool", pool.Name, tokenSecretName)
	case derived.Annotations[githubAppSecretAnnotation] != workspace.SecretRef.Name:
		// The Workspace now points at a different App Secret than the one the
		// derived Secret was minted from. The refresh path keys off the derived
		// Secret's kelos.dev/github-app-secret annotation, so without re-minting
		// here it would keep reading the stale source Secret and never adopt the
		// new credential. Re-mint from the current workspace.SecretRef.Name,
		// which rewrites the annotation and the token in place.
		logger.Info("Workspace GitHub App secret changed, re-minting installation token",
			"workerpool", pool.Name,
			"previousSecret", derived.Annotations[githubAppSecretAnnotation],
			"secret", workspace.SecretRef.Name)
		creds, err := githubapp.ParseCredentials(appSecret.Data)
		if err != nil {
			return nil, 0, fmt.Errorf("workerpool %s: parsing GitHub App credentials: %w", pool.Name, err)
		}
		if _, err := mintGitHubAppTokenSecret(ctx, r.Client, r.Scheme, r.TokenClient, pool, tokenSecretName, workspace.SecretRef.Name, workspace.Repo, creds); err != nil {
			return nil, 0, fmt.Errorf("workerpool %s: %w", pool.Name, err)
		}
	}

	next, expiresAt, refreshed, refreshErr := refreshGitHubAppTokenSecret(ctx, r.Client, r.TokenClient, pool.Namespace, tokenSecretName, workspace.Repo)
	if refreshErr != nil {
		// Non-fatal: the token Secret exists and may still be valid. Log,
		// emit an event, and requeue to retry before expiry.
		logger.Error(refreshErr, "Unable to refresh GitHub App installation token", "workerpool", pool.Name)
		r.recordEvent(pool, corev1.EventTypeWarning, "GitHubTokenRefreshFailed", "Failed to refresh GitHub App installation token: %v", refreshErr)
		if next == 0 {
			next = tokenRefreshRetryInterval
		}
	} else if refreshed {
		logger.Info("Refreshed GitHub App installation token", "workerpool", pool.Name, "expiresAt", expiresAt)
		r.recordEvent(pool, corev1.EventTypeNormal, "GitHubTokenRefreshed", "Refreshed GitHub App installation token (expires at %s)", expiresAt.UTC().Format(time.RFC3339))
	}

	resolved := *workspace
	resolved.SecretRef = &kelos.SecretReference{Name: tokenSecretName}
	return &resolved, next, nil
}

func (r *WorkerPoolReconciler) reconcileService(ctx context.Context, pool *kelos.WorkerPool, svcName string) error {
	logger := log.FromContext(ctx)
	labels := workerPoolLabels(pool.Name)

	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: svcName, Namespace: pool.Namespace}, &svc); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		svc = corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svcName,
				Namespace: pool.Namespace,
				Labels:    labels,
			},
			Spec: corev1.ServiceSpec{
				ClusterIP: corev1.ClusterIPNone,
				Selector:  labels,
			},
		}

		if err := controllerutil.SetControllerReference(pool, &svc, r.Scheme); err != nil {
			return fmt.Errorf("workerpool %s: setting Service owner reference: %w", pool.Name, err)
		}

		if err := r.Create(ctx, &svc); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return fmt.Errorf("workerpool %s: creating Service: %w", pool.Name, err)
		}

		logger.Info("Created headless Service", "service", svcName)
		r.recordEvent(pool, corev1.EventTypeNormal, "ServiceCreated", "Created headless Service %s", svcName)
	}

	return nil
}

func (r *WorkerPoolReconciler) reconcilePluginConfigMap(ctx context.Context, pool *kelos.WorkerPool, plugins []kelos.PluginSpec) error {
	logger := log.FromContext(ctx)
	cmName := workerPoolPluginConfigMapName(pool.Name)

	data, _, err := buildPluginConfigMapData(plugins)
	if err != nil {
		return fmt.Errorf("workerpool %s: building plugin ConfigMap data: %w", pool.Name, err)
	}

	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: pool.Namespace}, &cm); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: pool.Namespace,
				Labels:    workerPoolLabels(pool.Name),
			},
			Data: data,
		}
		if err := controllerutil.SetControllerReference(pool, &cm, r.Scheme); err != nil {
			return fmt.Errorf("workerpool %s: setting ConfigMap owner reference: %w", pool.Name, err)
		}
		if err := r.Create(ctx, &cm); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return fmt.Errorf("workerpool %s: creating plugin ConfigMap: %w", pool.Name, err)
		}
		logger.Info("Created plugin ConfigMap", "configmap", cmName)
		return nil
	}

	if !apiequality.Semantic.DeepEqual(cm.Data, data) {
		cm.Data = data
		if err := r.Update(ctx, &cm); err != nil {
			return fmt.Errorf("workerpool %s: updating plugin ConfigMap: %w", pool.Name, err)
		}
		logger.Info("Updated plugin ConfigMap", "configmap", cmName)
	}

	return nil
}

func (r *WorkerPoolReconciler) reconcileStatefulSet(ctx context.Context, pool *kelos.WorkerPool, stsName, svcName string, workspace *kelos.WorkspaceSpec, agentConfig *kelos.AgentConfigSpec) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var sts appsv1.StatefulSet
	stsExists := true
	if err := r.Get(ctx, types.NamespacedName{Name: stsName, Namespace: pool.Namespace}, &sts); err != nil {
		if apierrors.IsNotFound(err) {
			stsExists = false
		} else {
			return ctrl.Result{}, err
		}
	}

	desired, err := r.buildStatefulSet(pool, stsName, svcName, workspace, agentConfig)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("workerpool %s: building StatefulSet: %w", pool.Name, err)
	}

	if !stsExists {
		if err := controllerutil.SetControllerReference(pool, desired, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("workerpool %s: setting StatefulSet owner reference: %w", pool.Name, err)
		}

		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("workerpool %s: creating StatefulSet: %w", pool.Name, err)
		}

		logger.Info("Created StatefulSet", "statefulset", stsName, "replicas", ptr.Deref(pool.Spec.Replicas, 1))
		r.recordEvent(pool, corev1.EventTypeNormal, "StatefulSetCreated", "Created StatefulSet %s with %d replicas", stsName, ptr.Deref(pool.Spec.Replicas, 1))

		if err := r.updatePoolStatus(ctx, pool, stsName, svcName, 0, 0); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Update StatefulSet if replicas or pod template changed
	needsUpdate := false
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != ptr.Deref(pool.Spec.Replicas, 1) {
		sts.Spec.Replicas = pool.Spec.Replicas
		needsUpdate = true
	}

	// Sync the pod template to pick up changes to worker image, model,
	// effort, credentials, workspace, agentConfig, or podOverrides.
	if !podTemplateSpecEqual(sts.Spec.Template.Spec, desired.Spec.Template.Spec) {
		sts.Spec.Template = desired.Spec.Template
		needsUpdate = true
	}

	if needsUpdate {
		if err := r.Update(ctx, &sts); err != nil {
			return ctrl.Result{}, fmt.Errorf("workerpool %s: updating StatefulSet: %w", pool.Name, err)
		}
		logger.Info("Updated StatefulSet", "statefulset", stsName, "replicas", ptr.Deref(pool.Spec.Replicas, 1))
		r.recordEvent(pool, corev1.EventTypeNormal, "StatefulSetUpdated", "Updated StatefulSet %s to %d replicas", stsName, ptr.Deref(pool.Spec.Replicas, 1))
	}

	// Update pool status from StatefulSet status
	if err := r.updatePoolStatus(ctx, pool, stsName, svcName, sts.Status.Replicas, sts.Status.ReadyReplicas); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *WorkerPoolReconciler) buildStatefulSet(pool *kelos.WorkerPool, stsName, svcName string, workspace *kelos.WorkspaceSpec, agentConfig *kelos.AgentConfigSpec) (*appsv1.StatefulSet, error) {
	labels := workerPoolLabels(pool.Name)
	agentUID := AgentUID

	workerRunnerImage := r.WorkerRunnerImage
	if workerRunnerImage == "" {
		workerRunnerImage = DefaultWorkerRunnerImage
	}

	agentImage := r.agentImage(pool.Spec.Worker.Type)
	agentPullPolicy := r.agentImagePullPolicy(pool.Spec.Worker.Type)
	if pool.Spec.Worker.Image != "" {
		agentImage = pool.Spec.Worker.Image
	}

	envVars := []corev1.EnvVar{
		{
			Name: "KELOS_POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
		{
			Name: "KELOS_POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
		{
			Name:  "KELOS_AGENT_TYPE",
			Value: pool.Spec.Worker.Type,
		},
	}

	if pool.Spec.Worker.Model != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_MODEL",
			Value: pool.Spec.Worker.Model,
		})
	}

	if pool.Spec.Worker.Effort != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_EFFORT",
			Value: pool.Spec.Worker.Effort,
		})
	}

	if pool.Spec.Worker.Credentials != nil {
		credEnvVars := credentialEnvVars(*pool.Spec.Worker.Credentials, pool.Spec.Worker.Type)
		envVars = append(envVars, credEnvVars...)
	}

	// Workspace env vars for init containers and main container
	var workspaceEnvVars []corev1.EnvVar
	var isEnterprise bool
	if workspace != nil {
		host, _, _ := parseGitHubRepo(workspace.Repo)
		isEnterprise = host != "" && host != "github.com"

		if isEnterprise {
			ghHostEnv := corev1.EnvVar{Name: "GH_HOST", Value: host}
			envVars = append(envVars, ghHostEnv)
			workspaceEnvVars = append(workspaceEnvVars, ghHostEnv)
		}

		if workspace.Ref != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_BASE_BRANCH",
				Value: workspace.Ref,
			})
		}

		// Derive upstream repo from workspace remotes
		effectiveRemotes := effectiveWorkspaceRemotes(workspace)
		upstreamRepo := upstreamRepoEnvValue(effectiveRemotes)
		if upstreamRepo != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KELOS_UPSTREAM_REPO",
				Value: upstreamRepo,
			})
		}
	}

	if workspace != nil && workspace.SecretRef != nil {
		secretKeyRef := &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: workspace.SecretRef.Name,
			},
			Key: "GITHUB_TOKEN",
		}
		githubTokenEnv := corev1.EnvVar{
			Name:      "GITHUB_TOKEN",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeyRef},
		}
		envVars = append(envVars, githubTokenEnv)
		workspaceEnvVars = append(workspaceEnvVars, githubTokenEnv)

		ghTokenName := "GH_TOKEN"
		if isEnterprise {
			ghTokenName = "GH_ENTERPRISE_TOKEN"
		}
		ghTokenEnv := corev1.EnvVar{
			Name:      ghTokenName,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeyRef},
		}
		envVars = append(envVars, ghTokenEnv)
		workspaceEnvVars = append(workspaceEnvVars, ghTokenEnv)

		// Point gh CLI at a clean config directory on the workspace volume
		envVars = append(envVars, corev1.EnvVar{
			Name:  "GH_CONFIG_DIR",
			Value: GHConfigDir,
		})

		// Expose the mounted token file path so the worker runner can re-read
		// the token on every task, picking up controller-side refreshes without
		// a pod restart. The secret-backed GITHUB_TOKEN / GH_TOKEN env vars
		// above are frozen at pod start, so the file is the source of truth for
		// long-lived pools. Only the main container runs the worker runner; the
		// init-container credential helper hardcodes the mount path via
		// gitCredentialHelper(), so it does not need this env var.
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KELOS_GITHUB_TOKEN_FILE",
			Value: GitHubTokenMountPath + "/" + GitHubTokenSecretKey,
		})
	}

	workerRunnerVolumeName := "worker-runner"
	workerRunnerMountPath := "/kelos/bin"

	mainContainer := corev1.Container{
		Name:            kelos.AgentContainerName,
		Image:           agentImage,
		ImagePullPolicy: agentPullPolicy,
		Command:         []string{workerRunnerMountPath + "/kelos-worker-runner"},
		Env:             envVars,
		VolumeMounts: []corev1.VolumeMount{
			{Name: WorkspaceVolumeName, MountPath: WorkspaceMountPath},
			{Name: workerRunnerVolumeName, MountPath: workerRunnerMountPath},
		},
		WorkingDir: WorkspaceMountPath + "/repo",
	}

	initContainers := []corev1.Container{
		{
			Name:            "inject-worker-runner",
			Image:           workerRunnerImage,
			ImagePullPolicy: r.WorkerRunnerImagePullPolicy,
			Command:         []string{"/kelos-worker-runner", "--self-copy", workerRunnerMountPath + "/kelos-worker-runner"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: workerRunnerVolumeName, MountPath: workerRunnerMountPath},
			},
			SecurityContext: &corev1.SecurityContext{RunAsUser: &agentUID},
		},
	}

	volumes := []corev1.Volume{
		{
			Name:         workerRunnerVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}

	// Mount the workspace token Secret as a file so the git credential helper
	// and worker runner read the current token on each use. kubelet syncs
	// secret-volume updates into the running pod, so a controller-side token
	// refresh propagates without a pod restart.
	if workspace != nil && workspace.SecretRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: GitHubTokenVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: workspace.SecretRef.Name,
					Items: []corev1.KeyToPath{
						{Key: GitHubTokenSecretKey, Path: GitHubTokenSecretKey},
					},
					Optional: ptr.To(true),
				},
			},
		})
		mainContainer.VolumeMounts = append(mainContainer.VolumeMounts, corev1.VolumeMount{
			Name:      GitHubTokenVolumeName,
			MountPath: GitHubTokenMountPath,
			ReadOnly:  true,
		})
	}

	// Build workspace init containers (git-clone, remote-setup, workspace-files)
	if workspace != nil {
		volumeMount := corev1.VolumeMount{
			Name:      WorkspaceVolumeName,
			MountPath: WorkspaceMountPath,
		}

		// Containers that need the cloned repo plus, when a token Secret is
		// configured, the auto-syncing token file used by the credential helper.
		workspaceVolumeMounts := []corev1.VolumeMount{volumeMount}
		if workspace.SecretRef != nil {
			workspaceVolumeMounts = append(workspaceVolumeMounts, corev1.VolumeMount{
				Name:      GitHubTokenVolumeName,
				MountPath: GitHubTokenMountPath,
				ReadOnly:  true,
			})
		}

		targetPath := WorkspaceMountPath + "/repo"
		commitRef := isFullGitCommitSHA(workspace.Ref)

		// Git clone init container - wraps with exists check for pod restarts on PVCs
		cloneArgs := []string{"clone"}
		if workspace.Ref != "" && !commitRef {
			cloneArgs = append(cloneArgs, "--branch", workspace.Ref)
		}
		cloneArgs = append(cloneArgs, "--no-single-branch", "--depth", "1", "--", workspace.Repo, targetPath)

		gitClone := corev1.Container{
			Name:         "git-clone",
			Image:        GitCloneImage,
			Args:         cloneArgs,
			Env:          workspaceEnvVars,
			VolumeMounts: append([]corev1.VolumeMount(nil), workspaceVolumeMounts...),
			SecurityContext: &corev1.SecurityContext{
				RunAsUser: &agentUID,
			},
		}

		if commitRef {
			credentialHelper := ""
			if workspace.SecretRef != nil {
				credentialHelper = gitCredentialHelper()
			}
			gitClone.Command = []string{"sh", "-c",
				fmt.Sprintf("if [ -d '%s/repo/.git' ]; then echo 'Workspace exists, skipping clone'; exit 0; fi; %s",
					WorkspaceMountPath, buildCommitRefCheckoutScript(credentialHelper)),
			}
			gitClone.Args = []string{"--", workspace.Repo, targetPath, workspace.Ref}
		} else if workspace.SecretRef != nil {
			credentialHelper := gitCredentialHelper()
			// Build the inner clone command with credential helper
			innerCmd := fmt.Sprintf(
				`git -c credential.helper= -c credential.helper='%s' "$@" && { `+
					`git -C %s/repo config --unset-all credential.helper 2>/dev/null || true; `+
					`git -C %s/repo config --add credential.helper '%s'; }`,
				credentialHelper, WorkspaceMountPath, WorkspaceMountPath, credentialHelper,
			)
			// Wrap with exists check so it skips if workspace already exists on PVC
			gitClone.Command = []string{"sh", "-c",
				fmt.Sprintf("if [ -d '%s/repo/.git' ]; then echo 'Workspace exists, skipping clone'; exit 0; fi; %s",
					WorkspaceMountPath, innerCmd),
			}
			gitClone.Args = append([]string{"--"}, cloneArgs...)
		} else {
			// Wrap with exists check for non-secret clones
			gitClone.Command = []string{"sh", "-c",
				fmt.Sprintf("if [ -d '%s/repo/.git' ]; then echo 'Workspace exists, skipping clone'; exit 0; fi; exec git \"$@\"",
					WorkspaceMountPath),
			}
			gitClone.Args = append([]string{"--"}, cloneArgs...)
		}

		initContainers = append(initContainers, gitClone)

		// Remote setup init container
		effectiveRemotes := effectiveWorkspaceRemotes(workspace)
		if len(effectiveRemotes) > 0 {
			var parts []string
			parts = append(parts, fmt.Sprintf("cd %s/repo", WorkspaceMountPath))
			for _, remote := range effectiveRemotes {
				parts = append(parts,
					fmt.Sprintf(
						"if git remote get-url %s >/dev/null 2>&1; then git remote set-url %s %s; else git remote add %s %s; fi",
						shellQuote(remote.Name),
						shellQuote(remote.Name),
						shellQuote(remote.URL),
						shellQuote(remote.Name),
						shellQuote(remote.URL),
					),
				)
			}
			remoteSetupContainer := corev1.Container{
				Name:         "remote-setup",
				Image:        GitCloneImage,
				Command:      []string{"sh", "-c", strings.Join(parts, " && ")},
				VolumeMounts: []corev1.VolumeMount{volumeMount},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser: &agentUID,
				},
			}
			initContainers = append(initContainers, remoteSetupContainer)
		}

		// Workspace files init container
		if len(workspace.Files) > 0 {
			injectionScript, err := buildWorkspaceFileInjectionScript(workspace.Files)
			if err != nil {
				return nil, err
			}
			injectionContainer := corev1.Container{
				Name:         "workspace-files",
				Image:        GitCloneImage,
				Command:      []string{"sh", "-c", injectionScript},
				VolumeMounts: []corev1.VolumeMount{volumeMount},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser: &agentUID,
				},
			}
			initContainers = append(initContainers, injectionContainer)
		}
	}

	// Inject setup command so workspace dependencies are available
	if workspace != nil && len(workspace.SetupCommand) > 0 {
		setupJSON, err := json.Marshal(workspace.SetupCommand)
		if err != nil {
			return nil, fmt.Errorf("marshalling setup command: %w", err)
		}
		mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
			Name:  "KELOS_SETUP_COMMAND",
			Value: string(setupJSON),
		})
	}

	// Inject AgentConfig: agentsMD, plugins, skills, MCP servers
	if agentConfig != nil {
		if agentConfig.AgentsMD != "" {
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_AGENTS_MD",
				Value: agentConfig.AgentsMD,
			})
		}

		needsPluginVolume := len(agentConfig.Plugins) > 0 || len(agentConfig.Skills) > 0
		if needsPluginVolume {
			volumes = append(volumes, corev1.Volume{
				Name:         PluginVolumeName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
			mainContainer.VolumeMounts = append(mainContainer.VolumeMounts,
				corev1.VolumeMount{Name: PluginVolumeName, MountPath: PluginMountPath})
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_PLUGIN_DIR",
				Value: PluginMountPath,
			})
		}

		if len(agentConfig.Plugins) > 0 {
			_, items, err := buildPluginConfigMapData(agentConfig.Plugins)
			if err != nil {
				return nil, fmt.Errorf("invalid plugin configuration: %w", err)
			}
			volumes = append(volumes, corev1.Volume{
				Name: PluginStagingVolumeName,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: workerPoolPluginConfigMapName(pool.Name),
						},
						Items: items,
					},
				},
			})
			initContainers = append(initContainers, corev1.Container{
				Name:    "plugin-setup",
				Image:   GitCloneImage,
				Command: []string{"sh", "-c", pluginSetupScript},
				VolumeMounts: []corev1.VolumeMount{
					{Name: PluginVolumeName, MountPath: PluginMountPath},
					{Name: PluginStagingVolumeName, MountPath: PluginStagingMountPath, ReadOnly: true},
				},
				SecurityContext: &corev1.SecurityContext{RunAsUser: &agentUID},
			})
		}

		if len(agentConfig.Skills) > 0 {
			authEnvs := collectSkillsAuthEnvs(agentConfig.Skills)
			script, err := buildSkillsInstallScript(agentConfig.Skills, authEnvs)
			if err != nil {
				return nil, fmt.Errorf("invalid skills configuration: %w", err)
			}
			env := []corev1.EnvVar{{Name: "HOME", Value: PluginMountPath}}
			env = append(env, skillsAuthEnvVars(authEnvs)...)
			initContainers = append(initContainers, corev1.Container{
				Name:    "skills-install",
				Image:   NodeImage,
				Command: []string{"sh", "-c", script},
				Env:     env,
				VolumeMounts: []corev1.VolumeMount{
					{Name: PluginVolumeName, MountPath: PluginMountPath},
				},
			})
		}

		if len(agentConfig.MCPServers) > 0 {
			mcpJSON, err := buildMCPServersJSON(agentConfig.MCPServers)
			if err != nil {
				return nil, fmt.Errorf("invalid MCP server configuration: %w", err)
			}
			mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{
				Name:  "KELOS_MCP_SERVERS",
				Value: mcpJSON,
			})
		}
	}

	podSecurityContext := &corev1.PodSecurityContext{
		FSGroup: &agentUID,
	}

	// Apply PodOverrides
	var nodeSelector map[string]string
	var tolerations []corev1.Toleration
	var affinity *corev1.Affinity
	var imagePullSecrets []corev1.LocalObjectReference

	if po := pool.Spec.Worker.PodOverrides; po != nil {
		if po.Resources != nil {
			mainContainer.Resources = *po.Resources
		}

		if len(po.Env) > 0 {
			builtinNames := make(map[string]struct{}, len(mainContainer.Env))
			for _, e := range mainContainer.Env {
				builtinNames[e.Name] = struct{}{}
			}
			for _, e := range po.Env {
				if _, exists := builtinNames[e.Name]; exists {
					continue
				}
				if _, reserved := reservedEnvNames[e.Name]; reserved {
					continue
				}
				mainContainer.Env = append(mainContainer.Env, e)
			}
		}

		if po.NodeSelector != nil {
			nodeSelector = po.NodeSelector
		}

		if len(po.Tolerations) > 0 {
			tolerations = po.Tolerations
		}

		if po.Affinity != nil {
			affinity = po.Affinity
		}

		if len(po.ImagePullSecrets) > 0 {
			imagePullSecrets = po.ImagePullSecrets
		}

		if len(po.Volumes) > 0 {
			if err := validateUserVolumes(po.Volumes); err != nil {
				return nil, err
			}
			volumes = append(volumes, po.Volumes...)
		}

		if len(po.VolumeMounts) > 0 {
			mainContainer.VolumeMounts = append(mainContainer.VolumeMounts, po.VolumeMounts...)
		}

		if po.PodSecurityContext != nil {
			merged := po.PodSecurityContext.DeepCopy()
			if merged.FSGroup == nil {
				merged.FSGroup = &agentUID
			}
			podSecurityContext = merged
		}

		if po.ContainerSecurityContext != nil {
			mainContainer.SecurityContext = po.ContainerSecurityContext.DeepCopy()
		}

		if len(po.ExtraInitContainers) > 0 {
			if err := validateExtraContainers(po.ExtraInitContainers); err != nil {
				return nil, err
			}
			initContainers = append(initContainers, po.ExtraInitContainers...)
		}

		if len(po.ExtraContainers) > 0 && len(po.ExtraInitContainers) > 0 {
			if err := validateNoContainerNameCollision(po.ExtraContainers, po.ExtraInitContainers); err != nil {
				return nil, err
			}
		}
	}

	containers := []corev1.Container{mainContainer}

	if po := pool.Spec.Worker.PodOverrides; po != nil && len(po.ExtraContainers) > 0 {
		if err := validateExtraContainers(po.ExtraContainers); err != nil {
			return nil, err
		}
		containers = append(containers, po.ExtraContainers...)
	}

	parallel := appsv1.ParallelPodManagement

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stsName,
			Namespace: pool.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            pool.Spec.Replicas,
			ServiceName:         svcName,
			PodManagementPolicy: parallel,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: map[string]string{annotationPoolName: pool.Name},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: WorkerRunnerServiceAccount,
					SecurityContext:    podSecurityContext,
					InitContainers:     initContainers,
					Containers:         containers,
					Volumes:            volumes,
					NodeSelector:       nodeSelector,
					Tolerations:        tolerations,
					Affinity:           affinity,
					ImagePullSecrets:   imagePullSecrets,
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: WorkspaceVolumeName,
					},
					Spec: pool.Spec.VolumeClaimTemplate,
				},
			},
		},
	}

	return sts, nil
}

func (r *WorkerPoolReconciler) updatePoolStatus(ctx context.Context, pool *kelos.WorkerPool, stsName, svcName string, replicas, readyReplicas int32) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, client.ObjectKeyFromObject(pool), pool); err != nil {
			return err
		}

		pool.Status.ObservedGeneration = pool.Generation
		pool.Status.StatefulSetName = stsName
		pool.Status.ServiceName = svcName
		pool.Status.Replicas = replicas
		pool.Status.ReadyReplicas = readyReplicas

		switch {
		case readyReplicas == ptr.Deref(pool.Spec.Replicas, 1) && readyReplicas > 0:
			pool.Status.Phase = kelos.WorkerPoolPhaseReady
			pool.Status.Message = ""
		case replicas == 0 && readyReplicas == 0:
			pool.Status.Phase = kelos.WorkerPoolPhasePending
			pool.Status.Message = "Waiting for pods to start"
		default:
			pool.Status.Phase = kelos.WorkerPoolPhaseScaling
			pool.Status.Message = fmt.Sprintf("%d/%d workers ready", readyReplicas, ptr.Deref(pool.Spec.Replicas, 1))
		}

		return r.Status().Update(ctx, pool)
	})
}

// reconcileTask handles Task assignment and completion monitoring for tasks
// that reference a WorkerPool.
func (r *WorkerPoolReconciler) reconcileTask(ctx context.Context, task *kelos.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	poolName := task.Spec.WorkerPoolRef.Name

	// Handle deletion: request cancellation while keeping the pod unavailable.
	// This is a safety net for event-based reconciles; TaskReconciler.handleDeletion
	// also waits for this handshake via the finalizer path.
	if !task.DeletionTimestamp.IsZero() {
		if task.Status.PodName != "" {
			var pod corev1.Pod
			if err := r.Get(ctx, types.NamespacedName{Name: task.Status.PodName, Namespace: task.Namespace}, &pod); err == nil {
				released, err := requestWorkerPodTaskCancellation(ctx, r.Client, &pod, task.Name)
				if err != nil {
					logger.Error(err, "Failed to request worker task cancellation", "pod", pod.Name, "task", task.Name)
					return ctrl.Result{}, err
				}
				if !released {
					return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
				}
			}
		}
		return ctrl.Result{}, nil
	}

	switch task.Status.Phase {
	case kelos.TaskPhaseSucceeded, kelos.TaskPhaseFailed:
		// completeTask writes the terminal phase before creating the TaskRecord, so
		// a transient createTaskRecord failure leaves the task terminal with no
		// record. Retry here so budget usage is not undercounted. Also clear any
		// leaked pod assignment from that failed completion attempt.
		var recordErr error
		if task.Status.Usage != nil {
			recordErr = r.budget().createTaskRecord(ctx, task)
		}
		if err := r.clearTaskPodAssignmentIfStillAssigned(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, recordErr
	case kelos.TaskPhaseRunning, kelos.TaskPhasePending:
		if task.Status.PodName != "" {
			return r.monitorTaskCompletion(ctx, task)
		}
		// Task has a phase but no pod assigned - try to assign
		return r.assignTask(ctx, task, poolName)
	default:
		// Queued or empty phase - try to assign
		return r.assignTask(ctx, task, poolName)
	}
}

func (r *WorkerPoolReconciler) assignTask(ctx context.Context, task *kelos.Task, poolName string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Enforce TaskBudgets before claiming a worker pod, so worker-pool Tasks are
	// gated by the same admission policy as Job-backed Tasks. A blocked Task is
	// left in Waiting phase and requeued for re-evaluation when the period rolls.
	admitted, result, err := r.budget().checkBudgetAdmission(ctx, task)
	if err != nil || !admitted {
		return result, err
	}

	// Find available worker pods
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(task.Namespace),
		client.MatchingLabels{
			labelWorkerPool: workerPoolLabelValue(poolName),
			labelComponent:  WorkerComponentLabel,
		},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("workerpool %s: listing worker pods: %w", poolName, err)
	}

	var availablePod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !isPodAvailable(pod) {
			continue
		}
		availablePod = pod
		break
	}

	if availablePod == nil {
		logger.Info("No available worker pods, requeuing", "workerpool", poolName, "task", task.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Annotate the pod first to atomically claim it via optimistic lock.
	// This ordering ensures that if the controller crashes between the two
	// writes, the pod is marked unavailable (safe) rather than having a task
	// pointing at an unaware pod (orphan).
	if err := r.Get(ctx, client.ObjectKeyFromObject(availablePod), availablePod); err != nil {
		return ctrl.Result{}, fmt.Errorf("workerpool %s: re-fetching pod %s before annotation: %w", poolName, availablePod.Name, err)
	}

	podPatch := client.MergeFromWithOptions(availablePod.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if availablePod.Annotations == nil {
		availablePod.Annotations = make(map[string]string)
	}
	availablePod.Annotations[kelos.AnnotationWorkerAssignedTask] = task.Name
	delete(availablePod.Annotations, kelos.AnnotationWorkerTaskStatus)
	delete(availablePod.Annotations, kelos.AnnotationWorkerTaskFailReason)
	delete(availablePod.Annotations, kelos.AnnotationWorkerCancelTask)

	if err := r.Patch(ctx, availablePod, podPatch); err != nil {
		logger.Error(err, "Failed to annotate pod for task assignment", "pod", availablePod.Name, "task", task.Name)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Update task status to record the assignment.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, client.ObjectKeyFromObject(task), task); err != nil {
			return err
		}
		task.Status.Phase = kelos.TaskPhasePending
		task.Status.PodName = availablePod.Name
		now := metav1.Now()
		task.Status.StartTime = &now
		return r.Status().Update(ctx, task)
	}); err != nil {
		// Roll back pod annotation so the worker is not permanently blocked.
		logger.Error(err, "Failed to update task status, rolling back pod annotation", "pod", availablePod.Name, "task", task.Name)
		_ = r.clearPodAssignment(ctx, availablePod)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	logger.Info("Assigned task to worker pod", "task", task.Name, "pod", availablePod.Name, "workerpool", poolName)
	r.recordEvent(task, corev1.EventTypeNormal, "TaskAssigned", "Assigned to worker pod %s", availablePod.Name)

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *WorkerPoolReconciler) monitorTaskCompletion(ctx context.Context, task *kelos.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Name: task.Status.PodName, Namespace: task.Namespace}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			// Pod is gone, mark task as failed
			if err := r.completeTask(ctx, task, kelos.TaskPhaseFailed, "Worker pod was deleted"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.createWorkerTaskRecord(ctx, task)
		}
		return ctrl.Result{}, fmt.Errorf("workerpool: fetching worker pod %s for task %s: %w", task.Status.PodName, task.Name, err)
	}

	taskStatus := pod.Annotations[kelos.AnnotationWorkerTaskStatus]
	switch taskStatus {
	case "succeeded":
		if err := r.completeTask(ctx, task, kelos.TaskPhaseSucceeded, ""); err != nil {
			return ctrl.Result{}, err
		}
		recordErr := r.createWorkerTaskRecord(ctx, task)
		if err := r.clearPodAssignment(ctx, &pod); err != nil {
			logger.Error(err, "Failed to clear pod assignment after task success", "pod", pod.Name)
		}
		return ctrl.Result{}, recordErr

	case "failed":
		reason := pod.Annotations[kelos.AnnotationWorkerTaskFailReason]
		if err := r.completeTask(ctx, task, kelos.TaskPhaseFailed, reason); err != nil {
			return ctrl.Result{}, err
		}
		recordErr := r.createWorkerTaskRecord(ctx, task)
		if err := r.clearPodAssignment(ctx, &pod); err != nil {
			logger.Error(err, "Failed to clear pod assignment after task failure", "pod", pod.Name)
		}
		return ctrl.Result{}, recordErr

	case "running", "":
		// Still running, update task phase if needed
		if task.Status.Phase != kelos.TaskPhaseRunning {
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := r.Get(ctx, client.ObjectKeyFromObject(task), task); err != nil {
					return err
				}
				task.Status.Phase = kelos.TaskPhaseRunning
				return r.Status().Update(ctx, task)
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	default:
		logger.V(1).Info("Unknown task status annotation", "pod", pod.Name, "status", taskStatus)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}

func (r *WorkerPoolReconciler) completeTask(ctx context.Context, task *kelos.Task, phase kelos.TaskPhase, message string) error {
	outputs, results := r.readPodOutputs(ctx, task.Namespace, task.Status.PodName, task.Name)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, client.ObjectKeyFromObject(task), task); err != nil {
			return err
		}
		task.Status.Phase = phase
		task.Status.Message = message
		now := metav1.Now()
		task.Status.CompletionTime = &now
		if outputs != nil {
			task.Status.Outputs = outputs
		}
		if results != nil {
			task.Status.Results = results
			task.Status.Usage = usageFromResults(results)
		}
		return r.Status().Update(ctx, task)
	}); err != nil {
		return err
	}

	if results != nil {
		RecordCostTokenMetrics(task, results)
	}
	return nil
}

func (r *WorkerPoolReconciler) createWorkerTaskRecord(ctx context.Context, task *kelos.Task) error {
	if task.Status.Usage != nil {
		if err := r.budget().createTaskRecord(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

func (r *WorkerPoolReconciler) clearTaskPodAssignmentIfStillAssigned(ctx context.Context, task *kelos.Task) error {
	if task.Status.PodName == "" {
		return nil
	}

	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Name: task.Status.PodName, Namespace: task.Namespace}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("workerpool: fetching worker pod %s for terminal task %s: %w", task.Status.PodName, task.Name, err)
	}
	if pod.Annotations[kelos.AnnotationWorkerAssignedTask] != task.Name {
		return nil
	}
	return r.clearPodAssignment(ctx, &pod)
}

func (r *WorkerPoolReconciler) readPodOutputs(ctx context.Context, namespace, podName, taskName string) ([]string, map[string]string) {
	if r.Clientset == nil || podName == "" {
		return nil, nil
	}
	logger := log.FromContext(ctx)

	var tailLines int64 = 500
	req := r.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: kelos.AgentContainerName,
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

	segment := workerTaskLogSegment(string(data), taskName)
	if segment == "" {
		logger.V(1).Info("Unable to find task log segment for outputs", "pod", podName, "task", taskName)
		return nil, nil
	}

	outputs := ParseOutputs(segment)
	return outputs, ResultsFromOutputs(outputs)
}

func (r *WorkerPoolReconciler) clearPodAssignment(ctx context.Context, pod *corev1.Pod) error {
	return clearWorkerPodTaskAnnotations(ctx, r.Client, pod)
}

func clearWorkerPodTaskAnnotations(ctx context.Context, cl client.Client, pod *corev1.Pod) error {
	podPatch := client.MergeFrom(pod.DeepCopy())
	// Increment tasks completed counter
	completed := 0
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	if v, ok := pod.Annotations[kelos.AnnotationWorkerTasksCompleted]; ok {
		completed, _ = strconv.Atoi(v)
	}
	completed++

	delete(pod.Annotations, kelos.AnnotationWorkerAssignedTask)
	delete(pod.Annotations, kelos.AnnotationWorkerTaskStatus)
	delete(pod.Annotations, kelos.AnnotationWorkerTaskFailReason)
	delete(pod.Annotations, kelos.AnnotationWorkerCancelTask)
	pod.Annotations[kelos.AnnotationWorkerTasksCompleted] = strconv.Itoa(completed)

	return cl.Patch(ctx, pod, podPatch)
}

func requestWorkerPodTaskCancellation(ctx context.Context, cl client.Client, pod *corev1.Pod, taskName string) (bool, error) {
	if pod.Annotations[kelos.AnnotationWorkerAssignedTask] != taskName {
		return true, nil
	}

	switch pod.Annotations[kelos.AnnotationWorkerTaskStatus] {
	case "succeeded", "failed":
		return true, clearWorkerPodTaskAnnotations(ctx, cl, pod)
	}

	podPatch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[kelos.AnnotationWorkerCancelTask] = taskName
	return false, cl.Patch(ctx, pod, podPatch)
}

func workerTaskLogSegment(logData, taskName string) string {
	startMarker := fmt.Sprintf("%s %s", taskStartMarker, taskName)
	startIdx := strings.LastIndex(logData, startMarker)
	if startIdx == -1 {
		return ""
	}

	segment := logData[startIdx+len(startMarker):]
	endMarker := fmt.Sprintf("%s %s", taskEndMarker, taskName)
	if endIdx := strings.Index(segment, endMarker); endIdx >= 0 {
		segment = segment[:endIdx]
	}
	return segment
}

func isPodAvailable(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	if pod.Annotations[kelos.AnnotationWorkerAssignedTask] != "" {
		return false
	}
	return true
}

func (r *WorkerPoolReconciler) ensureWorkerRBAC(ctx context.Context, namespace string) error {
	logger := log.FromContext(ctx)

	var sa corev1.ServiceAccount
	if err := r.Get(ctx, types.NamespacedName{Name: WorkerRunnerServiceAccount, Namespace: namespace}, &sa); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		sa = corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      WorkerRunnerServiceAccount,
				Namespace: namespace,
			},
		}
		if err := r.Create(ctx, &sa); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
		} else {
			logger.Info("Created ServiceAccount", "namespace", namespace, "name", WorkerRunnerServiceAccount)
		}
	}

	rbName := WorkerRunnerServiceAccount
	var rb rbacv1.RoleBinding
	if err := r.Get(ctx, types.NamespacedName{Name: rbName, Namespace: namespace}, &rb); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		rb = rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbName,
				Namespace: namespace,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     WorkerRunnerClusterRole,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      WorkerRunnerServiceAccount,
					Namespace: namespace,
				},
			},
		}
		if err := r.Create(ctx, &rb); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
		} else {
			logger.Info("Created RoleBinding", "namespace", namespace, "name", rbName)
		}
	}

	return nil
}

func (r *WorkerPoolReconciler) agentImage(agentType string) string {
	switch agentType {
	case AgentTypeClaudeCode:
		if r.ClaudeCodeImage != "" {
			return r.ClaudeCodeImage
		}
		return ClaudeCodeImage
	case AgentTypeCodex:
		if r.CodexImage != "" {
			return r.CodexImage
		}
		return CodexImage
	case AgentTypeGemini:
		if r.GeminiImage != "" {
			return r.GeminiImage
		}
		return GeminiImage
	case AgentTypeOpenCode:
		if r.OpenCodeImage != "" {
			return r.OpenCodeImage
		}
		return OpenCodeImage
	case AgentTypeCursor:
		if r.CursorImage != "" {
			return r.CursorImage
		}
		return CursorImage
	default:
		return ClaudeCodeImage
	}
}

func (r *WorkerPoolReconciler) agentImagePullPolicy(agentType string) corev1.PullPolicy {
	switch agentType {
	case AgentTypeClaudeCode:
		return r.ClaudeCodeImagePullPolicy
	case AgentTypeCodex:
		return r.CodexImagePullPolicy
	case AgentTypeGemini:
		return r.GeminiImagePullPolicy
	case AgentTypeOpenCode:
		return r.OpenCodeImagePullPolicy
	case AgentTypeCursor:
		return r.CursorImagePullPolicy
	default:
		return r.ClaudeCodeImagePullPolicy
	}
}

func (r *WorkerPoolReconciler) recordEvent(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkerPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.WorkerPool{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Watches(&kelos.Workspace{}, handler.EnqueueRequestsFromMapFunc(r.findPoolsForWorkspace)).
		Watches(&kelos.AgentConfig{}, handler.EnqueueRequestsFromMapFunc(r.findPoolsForAgentConfig)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findPoolsForSecret)).
		Watches(&kelos.Task{}, handler.EnqueueRequestsFromMapFunc(r.findTasksForWorkerPool),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				task, ok := obj.(*kelos.Task)
				if !ok {
					return false
				}
				return task.Spec.WorkerPoolRef != nil
			}))).
		Watches(&kelos.TaskBudget{}, handler.EnqueueRequestsFromMapFunc(r.findWorkerPoolTasksForBudget)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.findPoolForPod),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				labels := obj.GetLabels()
				return labels[labelComponent] == WorkerComponentLabel
			}))).
		Complete(r)
}

func (r *WorkerPoolReconciler) findTasksForWorkerPool(ctx context.Context, obj client.Object) []reconcile.Request {
	task, ok := obj.(*kelos.Task)
	if !ok {
		return nil
	}
	if task.Spec.WorkerPoolRef == nil {
		return nil
	}

	// Enqueue the task itself for assignment processing
	requests := []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: task.Name, Namespace: task.Namespace}},
	}

	// Also enqueue the WorkerPool so it can update status
	requests = append(requests, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      task.Spec.WorkerPoolRef.Name,
			Namespace: task.Namespace,
		},
	})

	return requests
}

// findWorkerPoolTasksForBudget enqueues worker-pool Tasks that are Waiting on a
// budget block in the TaskBudget's namespace, so they are re-evaluated promptly
// when a budget is created, updated, or deleted (e.g. the period rolls over).
func (r *WorkerPoolReconciler) findWorkerPoolTasksForBudget(ctx context.Context, obj client.Object) []reconcile.Request {
	budget, ok := obj.(*kelos.TaskBudget)
	if !ok {
		return nil
	}

	var taskList kelos.TaskList
	if err := r.List(ctx, &taskList, client.InNamespace(budget.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range taskList.Items {
		t := &taskList.Items[i]
		if t.Spec.WorkerPoolRef == nil || t.Status.Phase != kelos.TaskPhaseWaiting {
			continue
		}
		blocked := false
		for _, c := range t.Status.Conditions {
			if c.Type == "BudgetBlocked" && c.Status == metav1.ConditionTrue {
				blocked = true
				break
			}
		}
		if !blocked {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: t.Name, Namespace: t.Namespace},
		})
	}
	return requests
}

func (r *WorkerPoolReconciler) findPoolsForWorkspace(ctx context.Context, obj client.Object) []reconcile.Request {
	ws, ok := obj.(*kelos.Workspace)
	if !ok {
		return nil
	}

	var poolList kelos.WorkerPoolList
	if err := r.List(ctx, &poolList, client.InNamespace(ws.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, pool := range poolList.Items {
		if pool.Spec.Worker.WorkspaceRef != nil && pool.Spec.Worker.WorkspaceRef.Name == ws.Name {
			requests = appendWorkerPoolRequest(requests, pool.Namespace, pool.Name)
		}
	}
	return requests
}

func (r *WorkerPoolReconciler) findPoolsForAgentConfig(ctx context.Context, obj client.Object) []reconcile.Request {
	agentConfig, ok := obj.(*kelos.AgentConfig)
	if !ok {
		return nil
	}

	var poolList kelos.WorkerPoolList
	if err := r.List(ctx, &poolList, client.InNamespace(agentConfig.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, pool := range poolList.Items {
		for _, ref := range pool.Spec.Worker.AgentConfigRefs {
			if ref.Name == agentConfig.Name {
				requests = appendWorkerPoolRequest(requests, pool.Namespace, pool.Name)
				break
			}
		}
	}
	return requests
}

func (r *WorkerPoolReconciler) findPoolsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var poolList kelos.WorkerPoolList
	if err := r.List(ctx, &poolList, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	var workspaceList kelos.WorkspaceList
	if err := r.List(ctx, &workspaceList, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	workspaceNames := map[string]struct{}{}
	for _, workspace := range workspaceList.Items {
		if workspace.Spec.SecretRef != nil && workspace.Spec.SecretRef.Name == secret.Name {
			workspaceNames[workspace.Name] = struct{}{}
		}
	}

	var requests []reconcile.Request
	seen := map[types.NamespacedName]struct{}{}
	for _, pool := range poolList.Items {
		if poolWorkerCredentialsSecretName(&pool) == secret.Name {
			requests = appendWorkerPoolRequestOnce(requests, seen, pool.Namespace, pool.Name)
			continue
		}
		if pool.Spec.Worker.WorkspaceRef == nil {
			continue
		}
		if _, ok := workspaceNames[pool.Spec.Worker.WorkspaceRef.Name]; ok {
			requests = appendWorkerPoolRequestOnce(requests, seen, pool.Namespace, pool.Name)
		}
	}
	return requests
}

func (r *WorkerPoolReconciler) findPoolForPod(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	poolName := pod.Annotations[annotationPoolName]
	if poolName == "" {
		poolName = pod.Labels[labelWorkerPool]
	}
	if poolName == "" {
		return nil
	}

	requests := []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: poolName, Namespace: pod.Namespace}},
	}

	// If the pod has an assigned task, also enqueue it for monitoring
	if taskName, ok := pod.Annotations[kelos.AnnotationWorkerAssignedTask]; ok && taskName != "" {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: taskName, Namespace: pod.Namespace},
		})
	}

	return requests
}

func resolveWorkerPoolAgentConfigRefs(pool *kelos.WorkerPool) []kelos.AgentConfigReference {
	return pool.Spec.Worker.AgentConfigRefs
}

func workerPoolLabels(poolName string) map[string]string {
	return map[string]string{
		labelName:          "kelos",
		labelComponent:     WorkerComponentLabel,
		labelManagedBy:     "kelos-controller",
		labelWorkerPool:    workerPoolLabelValue(poolName),
		labelExecutionMode: "persistent",
	}
}

func workerPoolLabelValue(poolName string) string {
	if len(poolName) <= 63 {
		return poolName
	}
	return truncateResourceName(poolName)
}

func workerPoolStatefulSetName(poolName string) string {
	return truncateResourceName("wp-" + poolName)
}

func workerPoolPluginConfigMapName(poolName string) string {
	return truncateResourceName("wp-" + poolName + "-plugins")
}

func podTemplateSpecEqual(a, b corev1.PodSpec) bool {
	return apiequality.Semantic.DeepEqual(a.ServiceAccountName, b.ServiceAccountName) &&
		apiequality.Semantic.DeepEqual(a.SecurityContext, b.SecurityContext) &&
		apiequality.Semantic.DeepEqual(a.InitContainers, b.InitContainers) &&
		apiequality.Semantic.DeepEqual(a.Containers, b.Containers) &&
		apiequality.Semantic.DeepEqual(a.Volumes, b.Volumes) &&
		apiequality.Semantic.DeepEqual(a.NodeSelector, b.NodeSelector) &&
		apiequality.Semantic.DeepEqual(a.Tolerations, b.Tolerations) &&
		apiequality.Semantic.DeepEqual(a.Affinity, b.Affinity) &&
		apiequality.Semantic.DeepEqual(a.ImagePullSecrets, b.ImagePullSecrets)
}

func mergeReconcileResults(a, b ctrl.Result) ctrl.Result {
	a.Requeue = a.Requeue || b.Requeue
	if b.RequeueAfter != 0 && (a.RequeueAfter == 0 || b.RequeueAfter < a.RequeueAfter) {
		a.RequeueAfter = b.RequeueAfter
	}
	return a
}

func appendWorkerPoolRequest(requests []reconcile.Request, namespace, name string) []reconcile.Request {
	return append(requests, reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: name},
	})
}

func appendWorkerPoolRequestOnce(requests []reconcile.Request, seen map[types.NamespacedName]struct{}, namespace, name string) []reconcile.Request {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if _, ok := seen[key]; ok {
		return requests
	}
	seen[key] = struct{}{}
	return append(requests, reconcile.Request{NamespacedName: key})
}

func poolWorkerCredentialsSecretName(pool *kelos.WorkerPool) string {
	if pool.Spec.Worker.Credentials == nil || pool.Spec.Worker.Credentials.SecretRef == nil {
		return ""
	}
	return pool.Spec.Worker.Credentials.SecretRef.Name
}

func truncateResourceName(name string) string {
	if len(name) <= 63 {
		return name
	}
	sum := sha1.Sum([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:8]
	maxPrefixLen := 63 - len(suffix) - 1
	return name[:maxPrefixLen] + "-" + suffix
}
