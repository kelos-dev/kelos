package main

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
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

	tokenFunc := func() string {
		token, err := r.config.TokenResolver(ctx)
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
			BaseURL:   r.config.GitHubAPIBaseURL,
		},
		Cache: r.cache,
	}

	if task.Annotations[reporting.AnnotationGitHubChecks] == "enabled" {
		reporter.ChecksReporter = &reporting.ChecksReporter{
			Owner:     owner,
			Repo:      repo,
			TokenFunc: tokenFunc,
			BaseURL:   r.config.GitHubAPIBaseURL,
		}
	}

	if err := reporter.ReportTaskStatus(ctx, &task); err != nil {
		log.Error(err, "Reporting task status", "task", task.Name)
		return ctrl.Result{}, fmt.Errorf("reporting task status: %w", err)
	}

	return ctrl.Result{}, nil
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
