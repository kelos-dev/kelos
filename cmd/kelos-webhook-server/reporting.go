package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/reporting"
)

// reportingConfig holds the configuration for the reporting reconciler.
type reportingConfig struct {
	GitHubOwner      string
	GitHubRepo       string
	GitHubTokenFile  string
	GitHubAPIBaseURL string
}

// reportingReconciler watches Tasks with GitHub reporting annotations
// and reports their status back to GitHub.
type reportingReconciler struct {
	client.Client
	config reportingConfig
}

func (r *reportingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.Log.WithName("reporting")

	var task kelosv1alpha1.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only process tasks with GitHub reporting enabled
	if task.Annotations == nil || task.Annotations[reporting.AnnotationGitHubReporting] != "enabled" {
		return ctrl.Result{}, nil
	}

	token, err := readGitHubToken(r.config.GitHubTokenFile)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading GitHub token for reporting: %w", err)
	}

	reporter := &reporting.TaskReporter{
		Client: r.Client,
		Reporter: &reporting.GitHubReporter{
			Owner:     r.config.GitHubOwner,
			Repo:      r.config.GitHubRepo,
			Token:     token,
			TokenFile: r.config.GitHubTokenFile,
			BaseURL:   r.config.GitHubAPIBaseURL,
		},
	}

	if err := reporter.ReportTaskStatus(ctx, &task); err != nil {
		log.Error(err, "Reporting task status", "task", task.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *reportingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("webhook-reporting").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		For(&kelosv1alpha1.Task{}, builder.WithPredicates(
			reportingAnnotationPredicate{},
		)).
		Complete(r)
}

// reportingAnnotationPredicate triggers reconciliation when a Task's status
// phase changes. Status sub-resource updates do not bump metadata.generation,
// so GenerationChangedPredicate alone would miss them.
type reportingAnnotationPredicate struct{}

func (reportingAnnotationPredicate) Create(_ event.CreateEvent) bool   { return true }
func (reportingAnnotationPredicate) Delete(_ event.DeleteEvent) bool   { return false }
func (reportingAnnotationPredicate) Generic(_ event.GenericEvent) bool { return false }

func (reportingAnnotationPredicate) Update(e event.UpdateEvent) bool {
	oldTask, ok1 := e.ObjectOld.(*kelosv1alpha1.Task)
	newTask, ok2 := e.ObjectNew.(*kelosv1alpha1.Task)
	if !ok1 || !ok2 {
		return true
	}
	// Reconcile when the Task phase changes
	return oldTask.Status.Phase != newTask.Status.Phase
}

// readGitHubToken reads the GitHub token from a file or environment variable.
func readGitHubToken(tokenFile string) (string, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if tokenFile == "" {
		return token, nil
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		if os.IsNotExist(err) {
			ctrl.Log.WithName("reporting").Info("Token file not yet available, proceeding without token", "path", tokenFile)
			return token, nil
		}
		return "", fmt.Errorf("reading token file: %w", err)
	}

	if t := strings.TrimSpace(string(data)); t != "" {
		return t, nil
	}
	return token, nil
}
