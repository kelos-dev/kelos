package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/source"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
}

func main() {
	var name string
	var namespace string
	var githubOwner string
	var githubRepo string
	var githubAPIBaseURL string
	var githubTokenFile string
	var jiraBaseURL string
	var jiraProject string
	var jiraJQL string
	var oneShot bool

	flag.StringVar(&name, "taskspawner-name", "", "Name of the TaskSpawner to manage")
	flag.StringVar(&namespace, "taskspawner-namespace", "", "Namespace of the TaskSpawner")
	flag.StringVar(&githubOwner, "github-owner", "", "GitHub repository owner")
	flag.StringVar(&githubRepo, "github-repo", "", "GitHub repository name")
	flag.StringVar(&githubAPIBaseURL, "github-api-base-url", "", "GitHub API base URL for enterprise servers (e.g. https://github.example.com/api/v3)")
	flag.StringVar(&githubTokenFile, "github-token-file", "", "Path to file containing GitHub token (refreshed by sidecar)")
	flag.StringVar(&jiraBaseURL, "jira-base-url", "", "Jira instance base URL (e.g. https://mycompany.atlassian.net)")
	flag.StringVar(&jiraProject, "jira-project", "", "Jira project key")
	flag.StringVar(&jiraJQL, "jira-jql", "", "Optional JQL filter for Jira issues")
	flag.BoolVar(&oneShot, "one-shot", false, "Run a single discovery cycle and exit (used by CronJob)")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	logger := zap.New(zap.UseFlagOptions(&opts))
	ctrl.SetLogger(logger)
	log := ctrl.Log.WithName("spawner")

	if name == "" || namespace == "" {
		log.Error(fmt.Errorf("--taskspawner-name and --taskspawner-namespace are required"), "invalid flags")
		os.Exit(1)
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "unable to get kubeconfig")
		os.Exit(1)
	}

	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create client")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	key := types.NamespacedName{Name: name, Namespace: namespace}

	log.Info("Starting spawner", "taskspawner", key, "oneShot", oneShot)

	if oneShot {
		if err := runCycle(ctx, cl, key, githubOwner, githubRepo, githubAPIBaseURL, githubTokenFile, jiraBaseURL, jiraProject, jiraJQL); err != nil {
			log.Error(err, "Discovery cycle failed")
			os.Exit(1)
		}
		return
	}

	for {
		if err := runCycle(ctx, cl, key, githubOwner, githubRepo, githubAPIBaseURL, githubTokenFile, jiraBaseURL, jiraProject, jiraJQL); err != nil {
			log.Error(err, "Discovery cycle failed")
		}

		// Re-read the TaskSpawner to get the current poll interval
		var ts kelosv1alpha1.TaskSpawner
		if err := cl.Get(ctx, key, &ts); err != nil {
			log.Error(err, "Unable to fetch TaskSpawner for poll interval")
			sleepOrDone(ctx, 5*time.Minute)
			continue
		}

		interval := parsePollInterval(ts.Spec.PollInterval)
		log.Info("Sleeping until next cycle", "interval", interval)
		if done := sleepOrDone(ctx, interval); done {
			return
		}
	}
}

func runCycle(ctx context.Context, cl client.Client, key types.NamespacedName, githubOwner, githubRepo, githubAPIBaseURL, githubTokenFile, jiraBaseURL, jiraProject, jiraJQL string) error {
	var ts kelosv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("fetching TaskSpawner: %w", err)
	}

	src, err := buildSource(&ts, githubOwner, githubRepo, githubAPIBaseURL, githubTokenFile, jiraBaseURL, jiraProject, jiraJQL)
	if err != nil {
		return fmt.Errorf("building source: %w", err)
	}

	return runCycleWithSource(ctx, cl, key, src)
}

func runCycleWithSource(ctx context.Context, cl client.Client, key types.NamespacedName, src source.Source) error {
	log := ctrl.Log.WithName("spawner")

	var ts kelosv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("fetching TaskSpawner: %w", err)
	}

	// Check if suspended
	if ts.Spec.Suspend != nil && *ts.Spec.Suspend {
		log.Info("TaskSpawner is suspended, skipping cycle")
		if ts.Status.Phase != kelosv1alpha1.TaskSpawnerPhaseSuspended {
			// Re-fetch to get the latest resource version before status update
			if err := cl.Get(ctx, key, &ts); err != nil {
				return fmt.Errorf("re-fetching TaskSpawner for suspend status: %w", err)
			}
			// Re-validate after re-fetch: user may have un-suspended between checks
			if ts.Spec.Suspend == nil || !*ts.Spec.Suspend {
				return nil
			}
			if ts.Status.Phase == kelosv1alpha1.TaskSpawnerPhaseSuspended {
				return nil
			}
			ts.Status.Phase = kelosv1alpha1.TaskSpawnerPhaseSuspended
			ts.Status.Message = "Suspended by user"
			meta.SetStatusCondition(&ts.Status.Conditions, metav1.Condition{
				Type:               "Suspended",
				Status:             metav1.ConditionTrue,
				Reason:             "UserSuspended",
				Message:            "TaskSpawner is suspended by user",
				ObservedGeneration: ts.Generation,
			})
			if err := cl.Status().Update(ctx, &ts); err != nil {
				return fmt.Errorf("updating status for suspend: %w", err)
			}
		}
		return nil
	}

	items, err := src.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discovering items: %w", err)
	}

	log.Info("discovered items", "count", len(items))

	// Build set of already-created Tasks by listing them from the API.
	// This is resilient to spawner restarts (status may lag behind actual Tasks).
	var existingTaskList kelosv1alpha1.TaskList
	if err := cl.List(ctx, &existingTaskList,
		client.InNamespace(ts.Namespace),
		client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
	); err != nil {
		return fmt.Errorf("listing existing Tasks: %w", err)
	}

	existingTaskMap := make(map[string]*kelosv1alpha1.Task)
	activeTasks := 0
	for i := range existingTaskList.Items {
		t := &existingTaskList.Items[i]
		existingTaskMap[t.Name] = t
		if t.Status.Phase != kelosv1alpha1.TaskPhaseSucceeded && t.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
			activeTasks++
		}
	}

	// Determine whether the source supports retrigger via TriggerComment.
	hasTriggerComment := ts.Spec.When.GitHubIssues != nil && ts.Spec.When.GitHubIssues.TriggerComment != ""

	var newItems []source.WorkItem
	for _, item := range items {
		taskName := fmt.Sprintf("%s-%s", ts.Name, item.ID)
		existing, found := existingTaskMap[taskName]
		if !found {
			newItems = append(newItems, item)
			continue
		}

		// Retrigger: when TriggerComment is configured and the existing task
		// is completed, check whether a trigger comment was posted after the
		// task finished. If so, delete the completed task so a new one can be
		// created. Note: if creation is later blocked by maxConcurrency or
		// maxTotalTasks, the item will be picked up as new on the next cycle
		// since the old task no longer exists.
		if hasTriggerComment && !item.TriggerTime.IsZero() &&
			(existing.Status.Phase == kelosv1alpha1.TaskPhaseSucceeded || existing.Status.Phase == kelosv1alpha1.TaskPhaseFailed) &&
			existing.Status.CompletionTime != nil &&
			item.TriggerTime.After(existing.Status.CompletionTime.Time) {

			if err := cl.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Deleting completed task for retrigger", "task", taskName)
				continue
			}
			log.Info("Deleted completed task for retrigger", "task", taskName)
			newItems = append(newItems, item)
		}
	}

	// Sort new items by priority labels when configured
	if ts.Spec.When.GitHubIssues != nil && len(ts.Spec.When.GitHubIssues.PriorityLabels) > 0 {
		source.SortByLabelPriority(newItems, ts.Spec.When.GitHubIssues.PriorityLabels)
	}

	maxConcurrency := int32(0)
	if ts.Spec.MaxConcurrency != nil {
		maxConcurrency = *ts.Spec.MaxConcurrency
	}

	maxTotalTasks := 0
	if ts.Spec.MaxTotalTasks != nil {
		maxTotalTasks = int(*ts.Spec.MaxTotalTasks)
	}

	newTasksCreated := 0
	for _, item := range newItems {
		// Enforce max concurrency limit
		if maxConcurrency > 0 && int32(activeTasks) >= maxConcurrency {
			log.Info("Max concurrency reached, skipping remaining items", "activeTasks", activeTasks, "maxConcurrency", maxConcurrency)
			break
		}

		// Enforce max total tasks limit
		if maxTotalTasks > 0 && ts.Status.TotalTasksCreated+newTasksCreated >= maxTotalTasks {
			log.Info("Task budget exhausted, skipping remaining items", "totalCreated", ts.Status.TotalTasksCreated+newTasksCreated, "maxTotalTasks", maxTotalTasks)
			break
		}

		taskName := fmt.Sprintf("%s-%s", ts.Name, item.ID)

		prompt, err := source.RenderPrompt(ts.Spec.TaskTemplate.PromptTemplate, item)
		if err != nil {
			log.Error(err, "rendering prompt", "item", item.ID)
			continue
		}

		task := &kelosv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      taskName,
				Namespace: ts.Namespace,
				Labels: map[string]string{
					"kelos.dev/taskspawner": ts.Name,
				},
			},
			Spec: kelosv1alpha1.TaskSpec{
				Type:                    ts.Spec.TaskTemplate.Type,
				Prompt:                  prompt,
				Credentials:             ts.Spec.TaskTemplate.Credentials,
				Model:                   ts.Spec.TaskTemplate.Model,
				Image:                   ts.Spec.TaskTemplate.Image,
				TTLSecondsAfterFinished: ts.Spec.TaskTemplate.TTLSecondsAfterFinished,
				PodOverrides:            ts.Spec.TaskTemplate.PodOverrides,
			},
		}

		if ts.Spec.TaskTemplate.WorkspaceRef != nil {
			task.Spec.WorkspaceRef = ts.Spec.TaskTemplate.WorkspaceRef
		}

		if ts.Spec.TaskTemplate.AgentConfigRef != nil {
			task.Spec.AgentConfigRef = ts.Spec.TaskTemplate.AgentConfigRef
		}

		if len(ts.Spec.TaskTemplate.DependsOn) > 0 {
			task.Spec.DependsOn = ts.Spec.TaskTemplate.DependsOn
		}
		if ts.Spec.TaskTemplate.Branch != "" {
			branch, err := source.RenderTemplate(ts.Spec.TaskTemplate.Branch, item)
			if err != nil {
				log.Error(err, "rendering branch template", "item", item.ID)
				continue
			}
			task.Spec.Branch = branch
		}

		if err := cl.Create(ctx, task); err != nil {
			if apierrors.IsAlreadyExists(err) {
				log.Info("Task already exists, skipping", "task", taskName)
			} else {
				log.Error(err, "creating Task", "task", taskName)
			}
			continue
		}

		log.Info("Created Task", "task", taskName, "item", item.ID)
		newTasksCreated++
		activeTasks++
	}

	// Update status in a single batch
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("re-fetching TaskSpawner for status update: %w", err)
	}

	now := metav1.Now()
	ts.Status.Phase = kelosv1alpha1.TaskSpawnerPhaseRunning
	ts.Status.LastDiscoveryTime = &now
	ts.Status.TotalDiscovered = len(items)
	ts.Status.TotalTasksCreated += newTasksCreated
	ts.Status.ActiveTasks = activeTasks
	ts.Status.Message = fmt.Sprintf("Discovered %d items, created %d tasks total", ts.Status.TotalDiscovered, ts.Status.TotalTasksCreated)

	// Clear Suspended condition since we are running
	meta.SetStatusCondition(&ts.Status.Conditions, metav1.Condition{
		Type:               "Suspended",
		Status:             metav1.ConditionFalse,
		Reason:             "Running",
		Message:            "TaskSpawner is running",
		ObservedGeneration: ts.Generation,
	})

	// Set TaskBudgetExhausted condition
	if maxTotalTasks > 0 && ts.Status.TotalTasksCreated >= maxTotalTasks {
		meta.SetStatusCondition(&ts.Status.Conditions, metav1.Condition{
			Type:               "TaskBudgetExhausted",
			Status:             metav1.ConditionTrue,
			Reason:             "BudgetReached",
			Message:            fmt.Sprintf("Total tasks created (%d) has reached maxTotalTasks (%d)", ts.Status.TotalTasksCreated, maxTotalTasks),
			ObservedGeneration: ts.Generation,
		})
	} else {
		meta.SetStatusCondition(&ts.Status.Conditions, metav1.Condition{
			Type:               "TaskBudgetExhausted",
			Status:             metav1.ConditionFalse,
			Reason:             "BudgetAvailable",
			Message:            "Task budget has not been exhausted",
			ObservedGeneration: ts.Generation,
		})
	}

	if err := cl.Status().Update(ctx, &ts); err != nil {
		return fmt.Errorf("updating TaskSpawner status: %w", err)
	}

	return nil
}

func buildSource(ts *kelosv1alpha1.TaskSpawner, owner, repo, apiBaseURL, tokenFile, jiraBaseURL, jiraProject, jiraJQL string) (source.Source, error) {
	if ts.Spec.When.GitHubIssues != nil {
		gh := ts.Spec.When.GitHubIssues

		token := os.Getenv("GITHUB_TOKEN")
		if tokenFile != "" {
			data, err := os.ReadFile(tokenFile)
			if err != nil {
				if os.IsNotExist(err) {
					ctrl.Log.WithName("spawner").Info("Token file not yet available, proceeding without token", "path", tokenFile)
				} else {
					return nil, fmt.Errorf("reading token file %s: %w", tokenFile, err)
				}
			} else {
				token = strings.TrimSpace(string(data))
			}
		}

		return &source.GitHubSource{
			Owner:           owner,
			Repo:            repo,
			Types:           gh.Types,
			Labels:          gh.Labels,
			ExcludeLabels:   gh.ExcludeLabels,
			State:           gh.State,
			Assignee:        gh.Assignee,
			Author:          gh.Author,
			Token:           token,
			BaseURL:         apiBaseURL,
			TriggerComment:  gh.TriggerComment,
			ExcludeComments: gh.ExcludeComments,
			PriorityLabels:  gh.PriorityLabels,
		}, nil
	}

	if ts.Spec.When.Jira != nil {
		user := os.Getenv("JIRA_USER")
		token := os.Getenv("JIRA_TOKEN")

		return &source.JiraSource{
			BaseURL: jiraBaseURL,
			Project: jiraProject,
			JQL:     jiraJQL,
			User:    user,
			Token:   token,
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

func parsePollInterval(s string) time.Duration {
	if s == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		// Try parsing as plain number (seconds)
		if n, err := strconv.Atoi(s); err == nil {
			return time.Duration(n) * time.Second
		}
		return 5 * time.Minute
	}
	return d
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}
