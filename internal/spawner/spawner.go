package spawner

import (
	"context"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/source"
)

// Options configures the spawner cycle.
type Options struct {
	GitHubOwner   string
	GitHubRepo    string
	GitHubBaseURL string
}

// RunCycle performs a single discovery-and-spawn cycle for the given TaskSpawner.
func RunCycle(ctx context.Context, cl client.Client, key types.NamespacedName, opts Options) error {
	var ts axonv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("fetching TaskSpawner: %w", err)
	}

	src, err := BuildSource(&ts, opts)
	if err != nil {
		return fmt.Errorf("building source: %w", err)
	}

	return RunCycleWithSource(ctx, cl, key, src)
}

// RunCycleWithSource performs a single discovery-and-spawn cycle using the
// provided source. This is useful for testing with fake sources.
func RunCycleWithSource(ctx context.Context, cl client.Client, key types.NamespacedName, src source.Source) error {
	log := ctrl.Log.WithName("spawner")

	var ts axonv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("fetching TaskSpawner: %w", err)
	}

	items, err := src.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discovering items: %w", err)
	}

	log.Info("Discovered items", "count", len(items))

	// Build set of already-created Tasks by listing them from the API.
	// This is resilient to spawner restarts (status may lag behind actual Tasks).
	var existingTaskList axonv1alpha1.TaskList
	if err := cl.List(ctx, &existingTaskList,
		client.InNamespace(ts.Namespace),
		client.MatchingLabels{"axon.io/taskspawner": ts.Name},
	); err != nil {
		return fmt.Errorf("listing existing Tasks: %w", err)
	}

	existingTasks := make(map[string]bool)
	activeTasks := 0
	for _, t := range existingTaskList.Items {
		existingTasks[t.Name] = true
		if t.Status.Phase != axonv1alpha1.TaskPhaseSucceeded && t.Status.Phase != axonv1alpha1.TaskPhaseFailed {
			activeTasks++
		}
	}

	var newItems []source.WorkItem
	for _, item := range items {
		taskName := fmt.Sprintf("%s-%s", ts.Name, item.ID)
		if !existingTasks[taskName] {
			newItems = append(newItems, item)
		}
	}

	maxConcurrency := int32(0)
	if ts.Spec.MaxConcurrency != nil {
		maxConcurrency = *ts.Spec.MaxConcurrency
	}

	newTasksCreated := 0
	for _, item := range newItems {
		// Enforce max concurrency limit
		if maxConcurrency > 0 && int32(activeTasks) >= maxConcurrency {
			log.Info("Max concurrency reached, skipping remaining items", "activeTasks", activeTasks, "maxConcurrency", maxConcurrency)
			break
		}

		taskName := fmt.Sprintf("%s-%s", ts.Name, item.ID)

		prompt, err := source.RenderPrompt(ts.Spec.TaskTemplate.PromptTemplate, item)
		if err != nil {
			log.Error(err, "Rendering prompt", "item", item.ID)
			continue
		}

		task := &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      taskName,
				Namespace: ts.Namespace,
				Labels: map[string]string{
					"axon.io/taskspawner": ts.Name,
				},
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:                    ts.Spec.TaskTemplate.Type,
				Prompt:                  prompt,
				Credentials:             ts.Spec.TaskTemplate.Credentials,
				Model:                   ts.Spec.TaskTemplate.Model,
				TTLSecondsAfterFinished: ts.Spec.TaskTemplate.TTLSecondsAfterFinished,
			},
		}

		if gh := ts.Spec.When.GitHubIssues; gh != nil && gh.WorkspaceRef != nil {
			task.Spec.WorkspaceRef = gh.WorkspaceRef
		}

		if err := cl.Create(ctx, task); err != nil {
			if apierrors.IsAlreadyExists(err) {
				log.Info("Task already exists, skipping", "task", taskName)
			} else {
				log.Error(err, "Creating Task", "task", taskName)
			}
			continue
		}

		log.Info("Created Task", "task", taskName, "item", item.ID)
		newTasksCreated++
		activeTasks++
	}

	// Re-list tasks to get an accurate total count. This avoids status
	// drift when a previous cycle created tasks but the status update
	// conflicted with the controller and was lost.
	var finalTaskList axonv1alpha1.TaskList
	if err := cl.List(ctx, &finalTaskList,
		client.InNamespace(ts.Namespace),
		client.MatchingLabels{"axon.io/taskspawner": ts.Name},
	); err != nil {
		return fmt.Errorf("re-listing Tasks for status: %w", err)
	}

	totalTasks := len(finalTaskList.Items)
	finalActive := 0
	for _, t := range finalTaskList.Items {
		if t.Status.Phase != axonv1alpha1.TaskPhaseSucceeded && t.Status.Phase != axonv1alpha1.TaskPhaseFailed {
			finalActive++
		}
	}

	// Update status in a single batch
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("re-fetching TaskSpawner for status update: %w", err)
	}

	now := metav1.Now()
	ts.Status.Phase = axonv1alpha1.TaskSpawnerPhaseRunning
	ts.Status.LastDiscoveryTime = &now
	ts.Status.TotalDiscovered = len(items)
	ts.Status.TotalTasksCreated = totalTasks
	ts.Status.ActiveTasks = finalActive
	ts.Status.Message = fmt.Sprintf("Discovered %d items, created %d tasks total", ts.Status.TotalDiscovered, ts.Status.TotalTasksCreated)

	if err := cl.Status().Update(ctx, &ts); err != nil {
		return fmt.Errorf("updating TaskSpawner status: %w", err)
	}

	return nil
}

// BuildSource creates the appropriate source.Source for the given TaskSpawner.
func BuildSource(ts *axonv1alpha1.TaskSpawner, opts Options) (source.Source, error) {
	if ts.Spec.When.GitHubIssues != nil {
		gh := ts.Spec.When.GitHubIssues
		return &source.GitHubSource{
			Owner:         opts.GitHubOwner,
			Repo:          opts.GitHubRepo,
			Types:         gh.Types,
			Labels:        gh.Labels,
			ExcludeLabels: gh.ExcludeLabels,
			State:         gh.State,
			Token:         os.Getenv("GITHUB_TOKEN"),
			BaseURL:       opts.GitHubBaseURL,
		}, nil
	}

	if ts.Spec.When.Cron != nil {
		var lastDiscovery time.Time
		if ts.Status.LastDiscoveryTime != nil {
			lastDiscovery = ts.Status.LastDiscoveryTime.Time
		} else {
			lastDiscovery = ts.CreationTimestamp.Time
		}
		return &source.CronSource{
			Schedule:          ts.Spec.When.Cron.Schedule,
			LastDiscoveryTime: lastDiscovery,
		}, nil
	}

	return nil, fmt.Errorf("no source configured in TaskSpawner %s/%s", ts.Namespace, ts.Name)
}
