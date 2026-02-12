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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/source"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(axonv1alpha1.AddToScheme(scheme))
}

func main() {
	var name string
	var namespace string
	var githubOwner string
	var githubRepo string
	var githubAPIBaseURL string
	var githubTokenFile string

	flag.StringVar(&name, "taskspawner-name", "", "Name of the TaskSpawner to manage")
	flag.StringVar(&namespace, "taskspawner-namespace", "", "Namespace of the TaskSpawner")
	flag.StringVar(&githubOwner, "github-owner", "", "GitHub repository owner")
	flag.StringVar(&githubRepo, "github-repo", "", "GitHub repository name")
	flag.StringVar(&githubAPIBaseURL, "github-api-base-url", "", "GitHub API base URL for enterprise servers (e.g. https://github.example.com/api/v3)")
	flag.StringVar(&githubTokenFile, "github-token-file", "", "Path to file containing GitHub token (refreshed by sidecar)")

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

	log.Info("starting spawner", "taskspawner", key)

	for {
		if err := runCycle(ctx, cl, key, githubOwner, githubRepo, githubAPIBaseURL, githubTokenFile); err != nil {
			log.Error(err, "discovery cycle failed")
		}

		// Re-read the TaskSpawner to get the current poll interval
		var ts axonv1alpha1.TaskSpawner
		if err := cl.Get(ctx, key, &ts); err != nil {
			log.Error(err, "unable to fetch TaskSpawner for poll interval")
			sleepOrDone(ctx, 5*time.Minute)
			continue
		}

		interval := parsePollInterval(ts.Spec.PollInterval)
		log.Info("sleeping until next cycle", "interval", interval)
		if done := sleepOrDone(ctx, interval); done {
			return
		}
	}
}

func runCycle(ctx context.Context, cl client.Client, key types.NamespacedName, githubOwner, githubRepo, githubAPIBaseURL, githubTokenFile string) error {
	var ts axonv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("fetching TaskSpawner: %w", err)
	}

	src, err := buildSource(&ts, githubOwner, githubRepo, githubAPIBaseURL, githubTokenFile)
	if err != nil {
		return fmt.Errorf("building source: %w", err)
	}

	return runCycleWithSource(ctx, cl, key, src)
}

func runCycleWithSource(ctx context.Context, cl client.Client, key types.NamespacedName, src source.Source) error {
	log := ctrl.Log.WithName("spawner")

	var ts axonv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("fetching TaskSpawner: %w", err)
	}

	items, err := src.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discovering items: %w", err)
	}

	log.Info("discovered items", "count", len(items))

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
			log.Error(err, "rendering prompt", "item", item.ID)
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
	ts.Status.Phase = axonv1alpha1.TaskSpawnerPhaseRunning
	ts.Status.LastDiscoveryTime = &now
	ts.Status.TotalDiscovered = len(items)
	ts.Status.TotalTasksCreated += newTasksCreated
	ts.Status.ActiveTasks = activeTasks
	ts.Status.Message = fmt.Sprintf("Discovered %d items, created %d tasks total", ts.Status.TotalDiscovered, ts.Status.TotalTasksCreated)

	if err := cl.Status().Update(ctx, &ts); err != nil {
		return fmt.Errorf("updating TaskSpawner status: %w", err)
	}

	return nil
}

func buildSource(ts *axonv1alpha1.TaskSpawner, owner, repo, apiBaseURL, tokenFile string) (source.Source, error) {
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
			Owner:         owner,
			Repo:          repo,
			Types:         gh.Types,
			Labels:        gh.Labels,
			ExcludeLabels: gh.ExcludeLabels,
			State:         gh.State,
			Token:         token,
			BaseURL:       apiBaseURL,
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
