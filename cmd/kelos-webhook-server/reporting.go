package main

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
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

	var task kelosv1alpha1.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if task.Annotations == nil ||
		(task.Annotations[reporting.AnnotationGitHubReporting] != "enabled" &&
			task.Annotations[reporting.AnnotationGitHubChecks] != "enabled") {
		return ctrl.Result{}, nil
	}

	owner := task.Annotations[reporting.AnnotationSourceOwner]
	repo := task.Annotations[reporting.AnnotationSourceRepo]
	if owner == "" || repo == "" {
		log.Info("Skipping reporting: missing source owner/repo annotation", "task", task.Name)
		return ctrl.Result{}, nil
	}

	// In gateway mode the reconciler watches all reporting-enabled Tasks
	// cluster-wide, but only Tasks created via a WebhookGateway carry the gateway
	// annotation. A Task without that annotation and without a globally
	// configured resolver belongs to another reporter (e.g. a legacy per-source
	// server); skip it rather than failing every reconcile.
	if task.Annotations[reporting.AnnotationWebhookGateway] == "" && r.config.TokenResolver == nil {
		log.V(1).Info("Skipping reporting: no gateway annotation and no global token resolver", "task", task.Name)
		return ctrl.Result{}, nil
	}

	resolver, baseURL, err := r.resolveReportingCreds(ctx, &task)
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
			Owner:     owner,
			Repo:      repo,
			TokenFunc: tokenFunc,
			BaseURL:   baseURL,
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

// resolveReportingCreds returns the GitHub token resolver and API base URL to
// use for reporting on the given Task. When the Task was created via a
// WebhookGateway (gateway annotation present), credentials and base URL are
// resolved from that gateway so reporting targets the correct GitHub instance
// (github.com or a GitHub Enterprise server). Otherwise the server-configured
// resolver and base URL are used (legacy --source mode).
func (r *reportingReconciler) resolveReportingCreds(ctx context.Context, task *kelosv1alpha1.Task) (func(context.Context) (string, error), string, error) {
	gwName := task.Annotations[reporting.AnnotationWebhookGateway]
	if gwName == "" {
		if r.config.TokenResolver == nil {
			return nil, "", fmt.Errorf("no GitHub token resolver configured for reporting")
		}
		return r.config.TokenResolver, r.config.GitHubAPIBaseURL, nil
	}

	var gw kelosv1alpha1.WebhookGateway
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: gwName}, &gw); err != nil {
		return nil, "", fmt.Errorf("fetching webhook gateway %s: %w", gwName, err)
	}
	if gw.Spec.GitHub == nil || gw.Spec.GitHub.CredentialsRef == nil {
		return nil, "", fmt.Errorf("webhook gateway %s has no github.credentialsRef for reporting", gwName)
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: gw.Spec.GitHub.CredentialsRef.Name}, &secret); err != nil {
		return nil, "", fmt.Errorf("fetching webhook gateway credentials %s: %w", gw.Spec.GitHub.CredentialsRef.Name, err)
	}
	resolver, err := githubapp.NewSecretTokenResolver(secret.Data, gw.Spec.GitHub.APIBaseURL)
	if err != nil {
		return nil, "", fmt.Errorf("building token resolver for gateway %s: %w", gwName, err)
	}
	if resolver == nil {
		return nil, "", fmt.Errorf("webhook gateway %s credentials contain no usable token", gwName)
	}
	return resolver, gw.Spec.GitHub.APIBaseURL, nil
}

func (r *reportingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.cache == nil {
		r.cache = reporting.NewReportStateCache()
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("webhook-reporting").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		For(&kelosv1alpha1.Task{}, builder.WithPredicates(
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
	oldTask, ok1 := e.ObjectOld.(*kelosv1alpha1.Task)
	newTask, ok2 := e.ObjectNew.(*kelosv1alpha1.Task)
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
