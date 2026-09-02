package main

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
	"github.com/kelos-dev/kelos/internal/reporting"
)

// reportingConfig holds the configuration for the reporting reconciler.
// Owner and repo are not configured here — they come from per-Task annotations
// stamped by the webhook handler from the originating webhook payload, so a
// single webhook server can report against many repositories. The token
// resolver covers all supported credential paths (PAT, GitHub App, token
// file, env), shared with the webhook handler for consistency.
type reportingConfig struct {
	TokenResolver    func(context.Context) (string, error)
	GitHubAPIBaseURL string
	GitHubAppID      string
	// GitLabToken authenticates status notes in --source=gitlab mode. Gateway
	// mode resolves the token from the gateway's credentialsRef instead.
	GitLabToken string
	GatewayMode bool
}

// reportingReconciler watches Tasks with GitHub reporting annotations
// and reports their status back to GitHub.
type reportingReconciler struct {
	client.Client
	config reportingConfig
	// cache survives across reconciles to backstop the AnnotationGitHubCommentID
	// annotation on fast Pending→Succeeded transitions where the annotation
	// Update has not yet propagated to the controller-runtime cache.
	cache *reporting.ReportStateCache
}

func (r *reportingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.Log.WithName("reporting")

	var task kelos.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if task.Annotations == nil ||
		(task.Annotations[reporting.AnnotationGitHubReporting] != "enabled" &&
			task.Annotations[reporting.AnnotationGitHubChecks] != "enabled") {
		return ctrl.Result{}, nil
	}

	gatewayName := task.Annotations[reporting.AnnotationWebhookGateway]
	if r.config.GatewayMode && gatewayName == "" {
		log.V(1).Info("Skipping reporting: task is not owned by a webhook gateway", "task", task.Name)
		return ctrl.Result{}, nil
	}
	if !r.config.GatewayMode && gatewayName != "" {
		log.V(1).Info("Skipping reporting: task is owned by a webhook gateway", "task", task.Name, "gateway", gatewayName)
		return ctrl.Result{}, nil
	}

	if task.Annotations[reporting.AnnotationSourceProvider] == reporting.SourceProviderGitLab {
		return r.reportGitLab(ctx, &task)
	}

	owner := task.Annotations[reporting.AnnotationSourceOwner]
	repo := task.Annotations[reporting.AnnotationSourceRepo]
	if owner == "" || repo == "" {
		log.Info("Skipping reporting: missing source owner/repo annotation", "task", task.Name)
		return ctrl.Result{}, nil
	}

	resolver, baseURL, githubAppID, err := r.resolveReportingCreds(ctx, &task)
	if err != nil {
		log.Error(err, "Resolving GitHub credentials for reporting", "task", task.Name)
		return ctrl.Result{}, fmt.Errorf("resolving reporting credentials: %w", err)
	}
	tokenFunc := func() string {
		token, err := resolver(ctx)
		if err != nil {
			log.Error(err, "Resolving GitHub token for reporting")
			return ""
		}
		return token
	}

	reporter := &reporting.TaskReporter{
		Client: r.Client,
		Reporter: &reporting.GitHubReporter{
			Owner:       owner,
			Repo:        repo,
			TokenFunc:   tokenFunc,
			GitHubAppID: githubAppID,
			BaseURL:     baseURL,
		},
		Cache: r.cache,
	}

	if task.Annotations[reporting.AnnotationGitHubChecks] == "enabled" {
		reporter.ChecksReporter = &reporting.ChecksReporter{
			Owner:     owner,
			Repo:      repo,
			TokenFunc: tokenFunc,
			BaseURL:   baseURL,
		}
	}

	if err := reporter.ReportTaskStatus(ctx, &task); err != nil {
		log.Error(err, "Reporting task status", "task", task.Name)
		return ctrl.Result{}, fmt.Errorf("reporting task status: %w", err)
	}

	return ctrl.Result{}, nil
}

// reportGitLab posts status notes for a Task created from a GitLab webhook.
// The project path and instance URL come from Task annotations; the token
// comes from the bound gateway's credentialsRef or the server's GitLab token.
func (r *reportingReconciler) reportGitLab(ctx context.Context, task *kelos.Task) (ctrl.Result, error) {
	log := ctrl.Log.WithName("reporting")

	project := task.Annotations[reporting.AnnotationSourceRepo]
	baseURL := task.Annotations[reporting.AnnotationSourceBaseURL]
	if project == "" || baseURL == "" {
		log.Info("Skipping reporting: missing source project/base-url annotation", "task", task.Name)
		return ctrl.Result{}, nil
	}

	token, apiBaseURL, err := r.resolveGitLabReportingCreds(ctx, task)
	if err != nil {
		log.Error(err, "Resolving GitLab credentials for reporting", "task", task.Name)
		return ctrl.Result{}, fmt.Errorf("resolving reporting credentials: %w", err)
	}
	if apiBaseURL != "" {
		baseURL = apiBaseURL
	}

	reporter := &reporting.TaskReporter{
		Client: r.Client,
		Reporter: &reporting.GitLabReporter{
			BaseURL: baseURL,
			Project: project,
			Token:   token,
		},
		Cache: r.cache,
	}
	if err := reporter.ReportTaskStatus(ctx, task); err != nil {
		log.Error(err, "Reporting task status", "task", task.Name)
		return ctrl.Result{}, fmt.Errorf("reporting task status: %w", err)
	}
	return ctrl.Result{}, nil
}

// resolveGitLabReportingCreds returns the GitLab token and, for gateway-owned
// Tasks, the gateway's API base URL override (empty when not configured).
func (r *reportingReconciler) resolveGitLabReportingCreds(ctx context.Context, task *kelos.Task) (string, string, error) {
	gwName := task.Annotations[reporting.AnnotationWebhookGateway]
	if gwName == "" {
		if r.config.GitLabToken == "" {
			return "", "", fmt.Errorf("no GitLab token configured for reporting")
		}
		return r.config.GitLabToken, "", nil
	}

	var gw kelos.WebhookGateway
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: gwName}, &gw); err != nil {
		return "", "", fmt.Errorf("fetching webhook gateway %s: %w", gwName, err)
	}
	if gw.Spec.GitLab == nil || gw.Spec.GitLab.CredentialsRef == nil {
		return "", "", fmt.Errorf("webhook gateway %s has no gitlab.credentialsRef for reporting", gwName)
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: gw.Spec.GitLab.CredentialsRef.Name}, &secret); err != nil {
		return "", "", fmt.Errorf("fetching webhook gateway credentials %s: %w", gw.Spec.GitLab.CredentialsRef.Name, err)
	}
	token := strings.TrimSpace(string(secret.Data["GITLAB_TOKEN"]))
	if token == "" {
		return "", "", fmt.Errorf("webhook gateway %s credentials contain no GITLAB_TOKEN", gwName)
	}
	return token, gw.Spec.GitLab.APIBaseURL, nil
}

// resolveReportingCreds returns the GitHub token resolver, API base URL, and
// GitHub App ID to use for reporting on the given Task. When the Task was
// created via a WebhookGateway, these values are resolved from that gateway so
// reporting targets the correct GitHub instance and identity. Otherwise the
// server-configured values are used.
func (r *reportingReconciler) resolveReportingCreds(ctx context.Context, task *kelos.Task) (func(context.Context) (string, error), string, string, error) {
	gwName := task.Annotations[reporting.AnnotationWebhookGateway]
	if gwName == "" {
		if r.config.TokenResolver == nil {
			return nil, "", "", fmt.Errorf("no GitHub token resolver configured for reporting")
		}
		return r.config.TokenResolver, r.config.GitHubAPIBaseURL, r.config.GitHubAppID, nil
	}

	var gw kelos.WebhookGateway
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: gwName}, &gw); err != nil {
		return nil, "", "", fmt.Errorf("fetching webhook gateway %s: %w", gwName, err)
	}
	if gw.Spec.GitHub == nil || gw.Spec.GitHub.CredentialsRef == nil {
		return nil, "", "", fmt.Errorf("webhook gateway %s has no github.credentialsRef for reporting", gwName)
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: gw.Spec.GitHub.CredentialsRef.Name}, &secret); err != nil {
		return nil, "", "", fmt.Errorf("fetching webhook gateway credentials %s: %w", gw.Spec.GitHub.CredentialsRef.Name, err)
	}
	resolver, err := githubapp.NewSecretTokenResolver(secret.Data, gw.Spec.GitHub.APIBaseURL)
	if err != nil {
		return nil, "", "", fmt.Errorf("building token resolver for gateway %s: %w", gwName, err)
	}
	if resolver == nil {
		return nil, "", "", fmt.Errorf("webhook gateway %s credentials contain no usable token", gwName)
	}

	githubAppID := ""
	if strings.TrimSpace(string(secret.Data["GITHUB_TOKEN"])) == "" {
		githubAppID = strings.TrimSpace(string(secret.Data["appID"]))
	}
	return resolver, gw.Spec.GitHub.APIBaseURL, githubAppID, nil
}

func (r *reportingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.cache == nil {
		r.cache = reporting.NewReportStateCache()
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("webhook-reporting").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		For(&kelos.Task{}, builder.WithPredicates(
			reportingAnnotationPredicate{},
		)).
		Complete(r)
}

// reportingAnnotationPredicate filters Task events down to ones the reporter
// actually cares about: only Tasks carrying the github-reporting annotation,
// and only on phase transitions. Status sub-resource updates do not bump
// metadata.generation, so GenerationChangedPredicate alone would miss them.
type reportingAnnotationPredicate struct{}

func (reportingAnnotationPredicate) Create(e event.CreateEvent) bool {
	return reportingEnabled(e.Object)
}
func (reportingAnnotationPredicate) Delete(_ event.DeleteEvent) bool   { return false }
func (reportingAnnotationPredicate) Generic(_ event.GenericEvent) bool { return false }

func (reportingAnnotationPredicate) Update(e event.UpdateEvent) bool {
	if !reportingEnabled(e.ObjectNew) {
		return false
	}
	oldTask, ok1 := e.ObjectOld.(*kelos.Task)
	newTask, ok2 := e.ObjectNew.(*kelos.Task)
	if !ok1 || !ok2 {
		return true
	}
	return oldTask.Status.Phase != newTask.Status.Phase
}

func reportingEnabled(obj client.Object) bool {
	if obj == nil {
		return false
	}
	a := obj.GetAnnotations()
	return a[reporting.AnnotationGitHubReporting] == "enabled" || a[reporting.AnnotationGitHubChecks] == "enabled"
}
