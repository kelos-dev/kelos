package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/reporting"
)

type spawnerRuntimeConfig struct {
	GitHubOwner      string
	GitHubRepo       string
	GitHubAPIBaseURL string
	GHProxyURL       string
	TokenResolver    func(context.Context) (string, error)
	JiraBaseURL      string
	JiraProject      string
	JiraJQL          string
	HTTPClient       *http.Client
}

type spawnerReconciler struct {
	client.Client
	Key    types.NamespacedName
	Config spawnerRuntimeConfig
}

func (r *spawnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.NamespacedName != r.Key {
		return ctrl.Result{}, nil
	}

	interval, err := runOnce(ctx, r.Client, r.Key, r.Config)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: interval}, nil
}

func (r *spawnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("taskspawner-loop").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		For(&kelosv1alpha1.TaskSpawner{}, builder.WithPredicates(r.taskSpawnerPredicate())).
		Watches(
			&kelosv1alpha1.Task{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForTask),
			builder.WithPredicates(r.taskPredicate()),
		).
		Complete(r)
}

func runOnce(ctx context.Context, cl client.Client, key types.NamespacedName, cfg spawnerRuntimeConfig) (time.Duration, error) {
	if err := runCycleWithProxy(ctx, cl, key, cfg.GitHubOwner, cfg.GitHubRepo, cfg.GHProxyURL, cfg.GitHubAPIBaseURL, cfg.TokenResolver, cfg.JiraBaseURL, cfg.JiraProject, cfg.JiraJQL, cfg.HTTPClient); err != nil {
		return 0, err
	}

	var ts kelosv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return 0, fmt.Errorf("fetching TaskSpawner after cycle: %w", err)
	}

	if reportingEnabled(&ts) {
		if cfg.TokenResolver == nil {
			return 0, fmt.Errorf("GitHub reporting is enabled but no token resolver is configured")
		}
		resolve := cfg.TokenResolver
		// Reporting always uses the direct API base URL (writes bypass the proxy).
		reporter := &reporting.TaskReporter{
			Client: cl,
			Reporter: &reporting.GitHubReporter{
				Owner: cfg.GitHubOwner,
				Repo:  cfg.GitHubRepo,
				TokenFunc: func() string {
					token, err := resolve(ctx)
					if err != nil {
						ctrl.Log.WithName("spawner").Error(err, "Resolving GitHub token for reporting")
						return ""
					}
					return token
				},
				BaseURL: cfg.GitHubAPIBaseURL,
				Client:  cfg.HTTPClient,
			},
		}
		if err := runReportingCycle(ctx, cl, key, reporter); err != nil {
			return 0, err
		}
	}

	return resolvedPollInterval(&ts), nil
}

// resolvedPollInterval returns the effective poll interval for the TaskSpawner.
// It checks the active source's PollInterval first, falling back to
// spec.pollInterval.
func resolvedPollInterval(ts *kelosv1alpha1.TaskSpawner) time.Duration {
	var sourceInterval string
	switch {
	case ts.Spec.When.GitHubIssues != nil:
		sourceInterval = ts.Spec.When.GitHubIssues.PollInterval
	case ts.Spec.When.GitHubPullRequests != nil:
		sourceInterval = ts.Spec.When.GitHubPullRequests.PollInterval
	case ts.Spec.When.Jira != nil:
		sourceInterval = ts.Spec.When.Jira.PollInterval
	}
	if sourceInterval != "" {
		return parsePollInterval(sourceInterval)
	}
	return parsePollInterval(ts.Spec.PollInterval)
}

func (r *spawnerReconciler) requestsForTask(_ context.Context, obj client.Object) []reconcile.Request {
	if !matchesSpawnerTask(obj, r.Key) {
		return nil
	}
	return []reconcile.Request{{NamespacedName: r.Key}}
}

func (r *spawnerReconciler) taskSpawnerPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return matchesNamespacedName(e.Object, r.Key)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !matchesNamespacedName(e.ObjectNew, r.Key) {
				return false
			}
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return matchesNamespacedName(e.Object, r.Key)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return matchesNamespacedName(e.Object, r.Key)
		},
	}
}

func (r *spawnerReconciler) taskPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldTask, okOld := e.ObjectOld.(*kelosv1alpha1.Task)
			newTask, okNew := e.ObjectNew.(*kelosv1alpha1.Task)
			if !okOld || !okNew || !matchesSpawnerTask(newTask, r.Key) {
				return false
			}

			if oldTask.Status.Phase != newTask.Status.Phase {
				return true
			}

			oldDeleting := oldTask.DeletionTimestamp != nil
			newDeleting := newTask.DeletionTimestamp != nil
			return oldDeleting != newDeleting
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return matchesSpawnerTask(e.Object, r.Key)
		},
		GenericFunc: func(event.GenericEvent) bool {
			return false
		},
	}
}

func matchesNamespacedName(obj client.Object, key types.NamespacedName) bool {
	if obj == nil {
		return false
	}
	return obj.GetNamespace() == key.Namespace && obj.GetName() == key.Name
}

func matchesSpawnerTask(obj client.Object, key types.NamespacedName) bool {
	if obj == nil {
		return false
	}
	return obj.GetNamespace() == key.Namespace && obj.GetLabels()["kelos.dev/taskspawner"] == key.Name
}
