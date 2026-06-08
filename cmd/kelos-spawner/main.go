package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/contextfetch"
	"github.com/kelos-dev/kelos/internal/githubapp"
	"github.com/kelos-dev/kelos/internal/logging"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/source"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

var scheme = runtime.NewScheme()

const (
	labelAgentSession     = "kelos.dev/agent-session"
	labelTaskSpawner      = "kelos.dev/taskspawner"
	labelSource           = "kelos.dev/source"
	labelSessionScopeHash = "kelos.dev/session-scope-hash"

	sourceTypeCron             = "Cron"
	sourceTypeCronTick         = "CronTick"
	sourceTypeAikido           = "Aikido"
	sourceTypeAikidoIssueGroup = "AikidoIssueGroup"

	defaultCronSessionScopeTemplate = "{{.TaskSpawner}}/{{.Date}}"
	defaultCronSessionMaxAge        = 24 * time.Hour
	defaultCronSessionIdleTimeout   = time.Hour
	defaultCronSessionMaxQueued     = int32(5)

	defaultAikidoSessionScopeTemplate = `aikido/{{.Branch}}/{{ index .Metadata "aikido.kelos.dev/issue-group-id" }}`
	defaultAikidoSessionMaxAge        = 14 * 24 * time.Hour
	defaultAikidoSessionIdleTimeout   = 25 * time.Hour
	defaultAikidoSessionMaxQueued     = int32(1)
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
}

func main() {
	var name string
	var namespace string
	var githubOwner string
	var githubRepo string
	var ghProxyURL string
	var githubAPIBaseURL string
	var githubToken string
	var githubAppID string
	var githubAppInstallationID string
	var githubAppPrivateKey string
	var jiraBaseURL string
	var jiraProject string
	var jiraJQL string
	var aikidoProxyURL string
	var oneShot bool

	flag.StringVar(&name, "taskspawner-name", "", "Name of the TaskSpawner to manage")
	flag.StringVar(&namespace, "taskspawner-namespace", "", "Namespace of the TaskSpawner")
	flag.StringVar(&githubOwner, "github-owner", "", "GitHub repository owner")
	flag.StringVar(&githubRepo, "github-repo", "", "GitHub repository name")
	flag.StringVar(&ghProxyURL, "gh-proxy-url", "", "Workspace ghproxy base URL for GitHub read requests")
	flag.StringVar(&githubAPIBaseURL, "github-api-base-url", "", "GitHub API base URL for enterprise servers (e.g. https://github.example.com/api/v3)")
	flag.StringVar(&githubToken, "github-token", "", "GitHub personal access token (env: GITHUB_TOKEN)")
	flag.StringVar(&githubAppID, "github-app-id", "", "GitHub App ID for installation token generation (env: GITHUB_APP_ID)")
	flag.StringVar(&githubAppInstallationID, "github-app-installation-id", "", "GitHub App installation ID (env: GITHUB_APP_INSTALLATION_ID)")
	flag.StringVar(&githubAppPrivateKey, "github-app-private-key", "", "GitHub App private key in PEM format (env: GITHUB_APP_PRIVATE_KEY)")
	flag.StringVar(&jiraBaseURL, "jira-base-url", "", "Jira instance base URL (e.g. https://mycompany.atlassian.net)")
	flag.StringVar(&jiraProject, "jira-project", "", "Jira project key")
	flag.StringVar(&jiraJQL, "jira-jql", "", "Optional JQL filter for Jira issues")
	flag.StringVar(&aikidoProxyURL, "aikido-proxy-url", "", "cody-tools Aikido proxy base URL (env: KELOS_AIKIDO_PROXY_URL)")
	flag.BoolVar(&oneShot, "one-shot", false, "Run a single discovery cycle and exit (used by CronJob)")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	logger := zap.New(zap.UseFlagOptions(opts))
	ctrl.SetLogger(logger)
	log := ctrl.Log.WithName("spawner")

	// Fall back to environment variables for credentials not passed via flags.
	if githubToken == "" {
		githubToken = os.Getenv("GITHUB_TOKEN")
	}
	if githubAppID == "" {
		githubAppID = os.Getenv("GITHUB_APP_ID")
	}
	if githubAppInstallationID == "" {
		githubAppInstallationID = os.Getenv("GITHUB_APP_INSTALLATION_ID")
	}
	if githubAppPrivateKey == "" {
		githubAppPrivateKey = os.Getenv("GITHUB_APP_PRIVATE_KEY")
	}
	if aikidoProxyURL == "" {
		aikidoProxyURL = os.Getenv("KELOS_AIKIDO_PROXY_URL")
	}
	if aikidoProxyURL == "" {
		aikidoProxyURL = source.DefaultAikidoProxyURL
	}

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

	httpClient := &http.Client{Transport: source.NewMetricsTransport(http.DefaultTransport)}

	tokenResolver := newGitHubTokenResolver(githubToken, githubAppID, githubAppInstallationID, githubAppPrivateKey, githubAPIBaseURL)

	cfgArgs := spawnerRuntimeConfig{
		GitHubOwner:      githubOwner,
		GitHubRepo:       githubRepo,
		GitHubAPIBaseURL: githubAPIBaseURL,
		GHProxyURL:       ghProxyURL,
		TokenResolver:    tokenResolver,
		JiraBaseURL:      jiraBaseURL,
		JiraProject:      jiraProject,
		JiraJQL:          jiraJQL,
		AikidoProxyURL:   aikidoProxyURL,
		HTTPClient:       httpClient,
	}

	if oneShot {
		if _, err := runOnce(ctx, cl, key, cfgArgs); err != nil {
			log.Error(err, "Cycle failed")
			os.Exit(1)
		}
		return
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: ":8080"},
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				namespace: {},
			},
		},
	})
	if err != nil {
		log.Error(err, "Unable to create manager")
		os.Exit(1)
	}

	if err := (&spawnerReconciler{
		Client: cl,
		Key:    key,
		Config: cfgArgs,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "Unable to create controller")
		os.Exit(1)
	}

	if err := mgr.Start(ctx); err != nil {
		log.Error(err, "Manager exited with error")
		os.Exit(1)
	}
}

// runReportingCycle lists all Tasks owned by the given TaskSpawner and runs
// reporting for each one that has GitHub reporting enabled. Running this
// in the same goroutine as the discovery loop avoids races between Task
// creation/deletion and annotation patching.
func runReportingCycle(ctx context.Context, cl client.Client, key types.NamespacedName, reporter *reporting.TaskReporter) error {
	var taskList kelosv1alpha1.TaskList
	if err := cl.List(ctx, &taskList,
		client.InNamespace(key.Namespace),
		client.MatchingLabels{"kelos.dev/taskspawner": key.Name},
	); err != nil {
		return fmt.Errorf("listing tasks for reporting: %w", err)
	}

	for i := range taskList.Items {
		if err := reporter.ReportTaskStatus(ctx, &taskList.Items[i]); err != nil {
			ctrl.Log.WithName("spawner").Error(err, "Reporting task status", "task", taskList.Items[i].Name)
			// Continue with remaining tasks rather than aborting the cycle
		}
	}
	return nil
}

func runCycle(ctx context.Context, cl client.Client, key types.NamespacedName, githubOwner, githubRepo, githubAPIBaseURL string, tokenResolver func(context.Context) (string, error), jiraBaseURL, jiraProject, jiraJQL string, httpClient *http.Client) error {
	return runCycleWithProxy(ctx, cl, key, githubOwner, githubRepo, "", githubAPIBaseURL, tokenResolver, jiraBaseURL, jiraProject, jiraJQL, httpClient)
}

func runCycleWithProxy(ctx context.Context, cl client.Client, key types.NamespacedName, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL string, tokenResolver func(context.Context) (string, error), jiraBaseURL, jiraProject, jiraJQL string, httpClient *http.Client) error {
	return runCycleWithProxyAndAikido(ctx, cl, key, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL, tokenResolver, jiraBaseURL, jiraProject, jiraJQL, source.DefaultAikidoProxyURL, httpClient)
}

func runCycleWithProxyAndAikido(ctx context.Context, cl client.Client, key types.NamespacedName, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL string, tokenResolver func(context.Context) (string, error), jiraBaseURL, jiraProject, jiraJQL, aikidoProxyURL string, httpClient *http.Client) error {
	start := time.Now()
	err := runCycleCore(ctx, cl, key, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL, tokenResolver, jiraBaseURL, jiraProject, jiraJQL, aikidoProxyURL, httpClient)
	discoveryDurationSeconds.Observe(time.Since(start).Seconds())
	if err != nil {
		discoveryErrorsTotal.Inc()
	}
	return err
}

func runCycleCore(ctx context.Context, cl client.Client, key types.NamespacedName, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL string, tokenResolver func(context.Context) (string, error), jiraBaseURL, jiraProject, jiraJQL, aikidoProxyURL string, httpClient *http.Client) error {
	var ts kelosv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("fetching TaskSpawner: %w", err)
	}

	src, err := buildSourceWithProxyAndAikido(ctx, &ts, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL, tokenResolver, jiraBaseURL, jiraProject, jiraJQL, aikidoProxyURL, httpClient)
	if err != nil {
		return fmt.Errorf("building source: %w", err)
	}

	return runCycleWithSourceCore(ctx, cl, key, src)
}

func runCycleWithSource(ctx context.Context, cl client.Client, key types.NamespacedName, src source.Source) error {
	start := time.Now()
	err := runCycleWithSourceCore(ctx, cl, key, src)
	discoveryDurationSeconds.Observe(time.Since(start).Seconds())
	if err != nil {
		discoveryErrorsTotal.Inc()
	}
	return err
}

func runCycleWithSourceCore(ctx context.Context, cl client.Client, key types.NamespacedName, src source.Source) error {
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

	itemsDiscoveredTotal.Add(float64(len(items)))
	log.Info("discovered items", "count", len(items))

	sessionMode := cronSessionEnabled(&ts) || aikidoSessionEnabled(&ts)
	activeTasks := 0

	var existingTaskMap map[string]*kelosv1alpha1.Task
	if !sessionMode {
		// Build set of already-created Tasks by listing them from the API.
		// This is resilient to spawner restarts (status may lag behind actual Tasks).
		var existingTaskList kelosv1alpha1.TaskList
		if err := cl.List(ctx, &existingTaskList,
			client.InNamespace(ts.Namespace),
			client.MatchingLabels{labelTaskSpawner: ts.Name},
		); err != nil {
			return fmt.Errorf("listing existing Tasks: %w", err)
		}

		existingTaskMap = make(map[string]*kelosv1alpha1.Task)
		for i := range existingTaskList.Items {
			t := &existingTaskList.Items[i]
			existingTaskMap[t.Name] = t
			if t.Status.Phase != kelosv1alpha1.TaskPhaseSucceeded && t.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
				activeTasks++
			}
		}
	}

	var newItems []source.WorkItem
	if sessionMode {
		newItems = append(newItems, items...)
	} else {
		for _, item := range items {
			taskName := fmt.Sprintf("%s-%s", ts.Name, item.ID)
			existing, found := existingTaskMap[taskName]
			if !found {
				newItems = append(newItems, item)
				continue
			}

			// Retrigger: when the source provides a trigger time and the existing
			// task is completed, check whether a new trigger arrived after the task
			// finished. If so, delete the completed task so a new one can be created.
			// Note: if creation is later blocked by maxConcurrency or maxTotalTasks,
			// the item will be picked up as new on the next cycle since the old task
			// no longer exists.
			if !item.TriggerTime.IsZero() &&
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
	}

	// Sort new items by priority labels when configured
	if priorityLabels := priorityLabelsForTaskSpawner(&ts); len(priorityLabels) > 0 {
		source.SortByLabelPriority(newItems, priorityLabels)
	}

	maxConcurrency := int32(0)
	if ts.Spec.MaxConcurrency != nil {
		maxConcurrency = *ts.Spec.MaxConcurrency
	}

	maxTotalTasks := 0
	if ts.Spec.MaxTotalTasks != nil {
		maxTotalTasks = int(*ts.Spec.MaxTotalTasks)
	}

	var contextFetcher *contextfetch.Fetcher
	if len(ts.Spec.TaskTemplate.ContextSources) > 0 {
		contextFetcher = &contextfetch.Fetcher{
			Client:     cl,
			HTTPClient: http.DefaultClient,
			Namespace:  ts.Namespace,
			Logger:     log,
		}
	}

	newTasksCreated := 0
	cycleTime := time.Now().UTC().Truncate(time.Minute)
	for _, item := range newItems {
		// Enforce max concurrency limit
		if !sessionMode && maxConcurrency > 0 && int32(activeTasks) >= maxConcurrency {
			log.Info("Max concurrency reached, skipping remaining items", "activeTasks", activeTasks, "maxConcurrency", maxConcurrency)
			break
		}

		// Enforce max total tasks limit
		if maxTotalTasks > 0 && ts.Status.TotalTasksCreated+newTasksCreated >= maxTotalTasks {
			log.Info("Task budget exhausted, skipping remaining items", "totalCreated", ts.Status.TotalTasksCreated+newTasksCreated, "maxTotalTasks", maxTotalTasks)
			break
		}

		taskName := fmt.Sprintf("%s-%s", ts.Name, item.ID)

		templateVars := source.WorkItemToTemplateVars(item)
		enrichCronTemplateVars(templateVars, &ts, item)
		enrichAikidoTemplateVars(templateVars, &ts, item, cycleTime)

		// Enrich with external context sources
		if contextFetcher != nil {
			contextData, err := contextFetcher.FetchAll(ctx, ts.Spec.TaskTemplate.ContextSources, templateVars)
			if err != nil {
				log.Error(err, "Fetching context sources", "item", item.ID)
				continue
			}
			templateVars["Context"] = contextData
		}

		tb, err := taskbuilder.NewTaskBuilder(cl)
		if err != nil {
			log.Error(err, "creating task builder", "item", item.ID)
			continue
		}

		task, err := tb.BuildTask(
			taskName,
			ts.Namespace,
			&ts.Spec.TaskTemplate,
			templateVars,
			&taskbuilder.SpawnerRef{
				Name:       ts.Name,
				UID:        string(ts.UID),
				APIVersion: kelosv1alpha1.GroupVersion.String(),
				Kind:       "TaskSpawner",
			},
		)
		if err != nil {
			log.Error(err, "building task", "item", item.ID)
			continue
		}

		if sessionMode {
			if srcAnnotations := sourceAnnotations(&ts, item); len(srcAnnotations) > 0 {
				if task.Annotations == nil {
					task.Annotations = make(map[string]string)
				}
				for k, v := range srcAnnotations {
					task.Annotations[k] = v
				}
			}
			var created bool
			if cronSessionEnabled(&ts) {
				created, err = createCronSessionTurn(ctx, cl, &ts, item, templateVars, task)
			} else {
				created, err = createAikidoSessionTurn(ctx, cl, &ts, item, templateVars, task, cycleTime)
			}
			if err != nil {
				log.Error(err, "creating session AgentTurn", "item", item.ID)
				continue
			}
			if created {
				log.Info("Created AgentTurn for session source", "item", item.ID)
				newTasksCreated++
			}
			continue
		}

		// Apply source-specific annotations (GitHub reporting metadata)
		srcAnnotations := sourceAnnotations(&ts, item)
		if len(srcAnnotations) > 0 {
			if task.Annotations == nil {
				task.Annotations = make(map[string]string)
			}
			for k, v := range srcAnnotations {
				task.Annotations[k] = v
			}
		}

		// Propagate upstream repo for fork workflows. Explicit template
		// value takes precedence; otherwise derive from the source repo
		// override (githubIssues.repo or githubPullRequests.repo).
		if task.Spec.UpstreamRepo == "" {
			if upstreamRepo := deriveUpstreamRepo(&ts); upstreamRepo != "" {
				task.Spec.UpstreamRepo = upstreamRepo
			}
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

	tasksCreatedTotal.Add(float64(newTasksCreated))

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

	// Count the cycle as successful only after the status write commits.
	discoveryTotal.Inc()

	return nil
}

func cronSessionEnabled(ts *kelosv1alpha1.TaskSpawner) bool {
	return ts != nil &&
		ts.Spec.When.Cron != nil &&
		ts.Spec.When.Cron.Session != nil &&
		ts.Spec.When.Cron.Session.Enabled
}

func aikidoSessionEnabled(ts *kelosv1alpha1.TaskSpawner) bool {
	return ts != nil &&
		ts.Spec.When.Aikido != nil &&
		ts.Spec.When.Aikido.Session != nil &&
		ts.Spec.When.Aikido.Session.Enabled
}

type cronSessionConfig struct {
	scopeTemplate string
	maxAge        time.Duration
	idleTimeout   time.Duration
	maxQueued     int32
}

func effectiveCronSessionConfig(ts *kelosv1alpha1.TaskSpawner) cronSessionConfig {
	cfg := cronSessionConfig{
		scopeTemplate: defaultCronSessionScopeTemplate,
		maxAge:        defaultCronSessionMaxAge,
		idleTimeout:   defaultCronSessionIdleTimeout,
		maxQueued:     defaultCronSessionMaxQueued,
	}
	if ts == nil || ts.Spec.When.Cron == nil || ts.Spec.When.Cron.Session == nil {
		return cfg
	}
	session := ts.Spec.When.Cron.Session
	if strings.TrimSpace(session.ScopeTemplate) != "" {
		cfg.scopeTemplate = session.ScopeTemplate
	}
	if session.MaxAge != nil && session.MaxAge.Duration > 0 {
		cfg.maxAge = session.MaxAge.Duration
	}
	if session.IdleTimeout != nil && session.IdleTimeout.Duration > 0 {
		cfg.idleTimeout = session.IdleTimeout.Duration
	}
	if session.MaxQueuedTurns != nil && *session.MaxQueuedTurns > 0 {
		cfg.maxQueued = *session.MaxQueuedTurns
	}
	return cfg
}

type aikidoSessionConfig struct {
	scopeTemplate string
	maxAge        time.Duration
	idleTimeout   time.Duration
	maxQueued     int32
}

func effectiveAikidoSessionConfig(ts *kelosv1alpha1.TaskSpawner) aikidoSessionConfig {
	cfg := aikidoSessionConfig{
		scopeTemplate: defaultAikidoSessionScopeTemplate,
		maxAge:        defaultAikidoSessionMaxAge,
		idleTimeout:   defaultAikidoSessionIdleTimeout,
		maxQueued:     defaultAikidoSessionMaxQueued,
	}
	if ts == nil || ts.Spec.When.Aikido == nil || ts.Spec.When.Aikido.Session == nil {
		return cfg
	}
	session := ts.Spec.When.Aikido.Session
	if strings.TrimSpace(session.ScopeTemplate) != "" {
		cfg.scopeTemplate = session.ScopeTemplate
	}
	if session.MaxAge != nil && session.MaxAge.Duration > 0 {
		cfg.maxAge = session.MaxAge.Duration
	}
	if session.IdleTimeout != nil && session.IdleTimeout.Duration > 0 {
		cfg.idleTimeout = session.IdleTimeout.Duration
	}
	if session.MaxQueuedTurns != nil && *session.MaxQueuedTurns > 0 {
		cfg.maxQueued = *session.MaxQueuedTurns
	}
	return cfg
}

func enrichCronTemplateVars(vars map[string]interface{}, ts *kelosv1alpha1.TaskSpawner, item source.WorkItem) {
	if vars == nil || ts == nil || ts.Spec.When.Cron == nil {
		return
	}
	tickTime := cronTickTime(item)
	vars["TaskSpawner"] = ts.Name
	vars["Namespace"] = ts.Namespace
	vars["Schedule"] = ts.Spec.When.Cron.Schedule
	if item.Schedule != "" {
		vars["Schedule"] = item.Schedule
	}
	vars["Time"] = tickTime.Format(time.RFC3339)
	vars["ID"] = item.ID
	vars["Date"] = tickTime.Format("2006-01-02")
	vars["Hour"] = tickTime.Format("20060102-15")
}

func enrichAikidoTemplateVars(vars map[string]interface{}, ts *kelosv1alpha1.TaskSpawner, item source.WorkItem, runTime time.Time) {
	if vars == nil || ts == nil || ts.Spec.When.Aikido == nil {
		return
	}
	schedule := ts.Spec.When.Aikido.Schedule
	if item.Schedule != "" {
		schedule = item.Schedule
	}
	branch := strings.TrimSpace(item.Branch)
	if branch == "" {
		branch = strings.TrimSpace(ts.Spec.When.Aikido.Branch)
	}
	if branch == "" {
		branch = "main"
	}
	vars["TaskSpawner"] = ts.Name
	vars["Namespace"] = ts.Namespace
	vars["Schedule"] = schedule
	vars["Time"] = runTime.Format(time.RFC3339)
	vars["ID"] = item.ID
	vars["Branch"] = branch
	vars["Date"] = runTime.Format("2006-01-02")
	vars["Hour"] = runTime.Format("20060102-15")
}

func cronTickTime(item source.WorkItem) time.Time {
	if item.Time != "" {
		if parsed, err := time.Parse(time.RFC3339, item.Time); err == nil {
			return parsed.UTC()
		}
	}
	if item.ID != "" {
		if parsed, err := time.Parse("20060102-1504", item.ID); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func renderTemplateString(name, templateStr string, vars map[string]interface{}) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("parsing %s template: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("executing %s template: %w", name, err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func createCronSessionTurn(ctx context.Context, cl client.Client, ts *kelosv1alpha1.TaskSpawner, item source.WorkItem, templateVars map[string]interface{}, renderedTask *kelosv1alpha1.Task) (bool, error) {
	cfg := effectiveCronSessionConfig(ts)
	scope, err := renderTemplateString("cronSessionScope", cfg.scopeTemplate, templateVars)
	if err != nil {
		return false, err
	}
	if scope == "" {
		return false, fmt.Errorf("cron session scope rendered empty")
	}
	session, err := findOrCreateCronSession(ctx, cl, ts, scope, cfg)
	if err != nil {
		return false, err
	}
	if exists, err := cronTurnExists(ctx, cl, session, item.ID); err != nil {
		return false, err
	} else if exists {
		return false, nil
	}
	if session.Spec.MaxQueuedTurns > 0 {
		count, err := countQueuedOrRunningCronTurns(ctx, cl, session)
		if err != nil {
			return false, err
		}
		if count >= session.Spec.MaxQueuedTurns {
			return false, fmt.Errorf("session %s already has %d queued or running turns", session.Name, count)
		}
	}
	sequence, err := nextCronTurnSequence(ctx, cl, session)
	if err != nil {
		return false, err
	}

	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cronTurnName(session.Name, sequence),
			Namespace:   session.Namespace,
			Labels:      copyStringMap(renderedTask.Labels),
			Annotations: copyStringMap(renderedTask.Annotations),
		},
		Spec: kelosv1alpha1.AgentTurnSpec{
			SessionRef: kelosv1alpha1.AgentSessionReference{Name: session.Name},
			Sequence:   sequence,
			Source: kelosv1alpha1.AgentTurnSource{
				Type:        sourceTypeCronTick,
				ID:          item.ID,
				DisplayName: fmt.Sprintf("cron:%s", ts.Name),
				Time:        cronTickTime(item).Format(time.RFC3339),
				Schedule:    ts.Spec.When.Cron.Schedule,
			},
			Input: kelosv1alpha1.AgentTurnInput{
				Text: renderedTask.Spec.Prompt,
				Body: renderedTask.Spec.Prompt,
			},
			Context: kelosv1alpha1.AgentTurnContext{
				Mode:          kelosv1alpha1.SlackSessionContextWindowSinceLastAgentMessage,
				ToTSInclusive: item.ID,
			},
		},
	}
	if turn.Labels == nil {
		turn.Labels = map[string]string{}
	}
	turn.Labels[labelSource] = "cron"
	turn.Labels[labelAgentSession] = session.Name
	turn.Labels[labelTaskSpawner] = ts.Name
	if turn.Annotations == nil {
		turn.Annotations = map[string]string{}
	}
	for k, v := range sourceAnnotations(ts, item) {
		turn.Annotations[k] = v
	}

	if err := controllerutil.SetControllerReference(session, turn, cl.Scheme()); err != nil {
		return false, fmt.Errorf("setting AgentTurn owner reference: %w", err)
	}
	if err := cl.Create(ctx, turn); err != nil {
		if apierrors.IsAlreadyExists(err) {
			var existing kelosv1alpha1.AgentTurn
			key := client.ObjectKey{Namespace: turn.Namespace, Name: turn.Name}
			if getErr := cl.Get(ctx, key, &existing); getErr != nil {
				return false, fmt.Errorf("fetching existing AgentTurn after name collision: %w", getErr)
			}
			if existing.Spec.SessionRef.Name == session.Name &&
				existing.Spec.Source.Type == sourceTypeCronTick &&
				existing.Spec.Source.ID == item.ID {
				return false, nil
			}
			return false, fmt.Errorf("AgentTurn name collision for %s: existing session=%s tick=%s, desired session=%s tick=%s",
				turn.Name,
				existing.Spec.SessionRef.Name,
				existing.Spec.Source.ID,
				session.Name,
				item.ID,
			)
		}
		return false, fmt.Errorf("creating AgentTurn: %w", err)
	}
	return true, nil
}

func createAikidoSessionTurn(ctx context.Context, cl client.Client, ts *kelosv1alpha1.TaskSpawner, item source.WorkItem, templateVars map[string]interface{}, renderedTask *kelosv1alpha1.Task, runTime time.Time) (bool, error) {
	cfg := effectiveAikidoSessionConfig(ts)
	scope, err := renderTemplateString("aikidoSessionScope", cfg.scopeTemplate, templateVars)
	if err != nil {
		return false, err
	}
	if scope == "" {
		return false, fmt.Errorf("Aikido session scope rendered empty")
	}
	session, err := findOrCreateAikidoSession(ctx, cl, ts, item, scope, cfg)
	if err != nil {
		return false, err
	}
	sourceID := aikidoTurnSourceID(item, runTime)
	if exists, err := aikidoTurnExists(ctx, cl, session, sourceID); err != nil {
		return false, err
	} else if exists {
		return false, nil
	}
	if session.Spec.MaxQueuedTurns > 0 {
		count, err := countQueuedOrRunningCronTurns(ctx, cl, session)
		if err != nil {
			return false, err
		}
		if count >= session.Spec.MaxQueuedTurns {
			return false, fmt.Errorf("session %s already has %d queued or running turns", session.Name, count)
		}
	}
	sequence, err := nextCronTurnSequence(ctx, cl, session)
	if err != nil {
		return false, err
	}

	schedule := ""
	if ts.Spec.When.Aikido != nil {
		schedule = ts.Spec.When.Aikido.Schedule
	}
	if item.Schedule != "" {
		schedule = item.Schedule
	}
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cronTurnName(session.Name, sequence),
			Namespace:   session.Namespace,
			Labels:      copyStringMap(renderedTask.Labels),
			Annotations: copyStringMap(renderedTask.Annotations),
		},
		Spec: kelosv1alpha1.AgentTurnSpec{
			SessionRef: kelosv1alpha1.AgentSessionReference{Name: session.Name},
			Sequence:   sequence,
			Source: kelosv1alpha1.AgentTurnSource{
				Type:        sourceTypeAikidoIssueGroup,
				ID:          sourceID,
				DisplayName: item.Title,
				Time:        runTime.Format(time.RFC3339),
				Schedule:    schedule,
			},
			Input: kelosv1alpha1.AgentTurnInput{
				Text: renderedTask.Spec.Prompt,
				Body: renderedTask.Spec.Prompt,
			},
			Context: kelosv1alpha1.AgentTurnContext{
				Mode:          kelosv1alpha1.SlackSessionContextWindowSinceLastAgentMessage,
				ToTSInclusive: sourceID,
			},
		},
	}
	if turn.Labels == nil {
		turn.Labels = map[string]string{}
	}
	turn.Labels[labelSource] = "aikido"
	turn.Labels[labelAgentSession] = session.Name
	turn.Labels[labelTaskSpawner] = ts.Name
	if turn.Annotations == nil {
		turn.Annotations = map[string]string{}
	}
	for k, v := range sourceAnnotations(ts, item) {
		turn.Annotations[k] = v
	}

	if err := controllerutil.SetControllerReference(session, turn, cl.Scheme()); err != nil {
		return false, fmt.Errorf("setting AgentTurn owner reference: %w", err)
	}
	if err := cl.Create(ctx, turn); err != nil {
		if apierrors.IsAlreadyExists(err) {
			var existing kelosv1alpha1.AgentTurn
			key := client.ObjectKey{Namespace: turn.Namespace, Name: turn.Name}
			if getErr := cl.Get(ctx, key, &existing); getErr != nil {
				return false, fmt.Errorf("fetching existing AgentTurn after name collision: %w", getErr)
			}
			if existing.Spec.SessionRef.Name == session.Name &&
				existing.Spec.Source.Type == sourceTypeAikidoIssueGroup &&
				existing.Spec.Source.ID == sourceID {
				return false, nil
			}
			return false, fmt.Errorf("AgentTurn name collision for %s: existing session=%s source=%s, desired session=%s source=%s",
				turn.Name,
				existing.Spec.SessionRef.Name,
				existing.Spec.Source.ID,
				session.Name,
				sourceID,
			)
		}
		return false, fmt.Errorf("creating AgentTurn: %w", err)
	}
	return true, nil
}

func findOrCreateAikidoSession(ctx context.Context, cl client.Client, ts *kelosv1alpha1.TaskSpawner, item source.WorkItem, scope string, cfg aikidoSessionConfig) (*kelosv1alpha1.AgentSession, error) {
	scopeHash := cronSessionScopeHash(ts.Namespace, ts.Name, scope)
	var list kelosv1alpha1.AgentSessionList
	if err := cl.List(ctx, &list,
		client.InNamespace(ts.Namespace),
		client.MatchingLabels{
			labelSource:           "aikido",
			labelTaskSpawner:      ts.Name,
			labelSessionScopeHash: scopeHash,
		},
	); err != nil {
		return nil, fmt.Errorf("listing AgentSessions: %w", err)
	}

	if session := newestActiveCronSession(list.Items); session != nil {
		return session, nil
	}

	generation := int32(len(list.Items) + 1)
	name := aikidoSessionName(ts.Name, scopeHash, generation)
	maxAge := metav1.Duration{Duration: cfg.maxAge}
	annotations := sourceAnnotations(ts, item)
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ts.Namespace,
			Annotations: annotations,
			Labels: map[string]string{
				labelSource:           "aikido",
				labelTaskSpawner:      ts.Name,
				labelSessionScopeHash: scopeHash,
			},
		},
		Spec: kelosv1alpha1.AgentSessionSpec{
			Source: kelosv1alpha1.AgentSessionSource{
				Type:        sourceTypeAikido,
				Key:         scope,
				DisplayName: fmt.Sprintf("aikido:%s", item.Title),
				Schedule:    ts.Spec.When.Aikido.Schedule,
			},
			TaskSpawnerRef:       kelosv1alpha1.TaskSpawnerReference{Name: ts.Name},
			TaskTemplateSnapshot: ts.Spec.TaskTemplate,
			IdleTimeout:          metav1.Duration{Duration: cfg.idleTimeout},
			MaxAge:               &maxAge,
			MaxQueuedTurns:       cfg.maxQueued,
		},
	}
	if err := controllerutil.SetControllerReference(ts, session, cl.Scheme()); err != nil {
		return nil, fmt.Errorf("setting AgentSession owner reference: %w", err)
	}
	if err := cl.Create(ctx, session); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating AgentSession: %w", err)
		}
		var existing kelosv1alpha1.AgentSession
		if getErr := cl.Get(ctx, client.ObjectKey{Namespace: ts.Namespace, Name: name}, &existing); getErr != nil {
			return nil, fmt.Errorf("fetching existing AgentSession: %w", getErr)
		}
		if isTerminalAgentSession(&existing) {
			return nil, fmt.Errorf("AgentSession %s already exists in terminal phase %s", name, existing.Status.Phase)
		}
		return &existing, nil
	}
	return session, nil
}

func findOrCreateCronSession(ctx context.Context, cl client.Client, ts *kelosv1alpha1.TaskSpawner, scope string, cfg cronSessionConfig) (*kelosv1alpha1.AgentSession, error) {
	scopeHash := cronSessionScopeHash(ts.Namespace, ts.Name, scope)
	var list kelosv1alpha1.AgentSessionList
	if err := cl.List(ctx, &list,
		client.InNamespace(ts.Namespace),
		client.MatchingLabels{
			labelSource:           "cron",
			labelTaskSpawner:      ts.Name,
			labelSessionScopeHash: scopeHash,
		},
	); err != nil {
		return nil, fmt.Errorf("listing AgentSessions: %w", err)
	}

	if session := newestActiveCronSession(list.Items); session != nil {
		return session, nil
	}

	generation := int32(len(list.Items) + 1)
	name := cronSessionName(ts.Name, scopeHash, generation)
	maxAge := metav1.Duration{Duration: cfg.maxAge}
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ts.Namespace,
			Labels: map[string]string{
				labelSource:           "cron",
				labelTaskSpawner:      ts.Name,
				labelSessionScopeHash: scopeHash,
			},
		},
		Spec: kelosv1alpha1.AgentSessionSpec{
			Source: kelosv1alpha1.AgentSessionSource{
				Type:        sourceTypeCron,
				Key:         scope,
				DisplayName: fmt.Sprintf("cron:%s", ts.Name),
				Schedule:    ts.Spec.When.Cron.Schedule,
			},
			TaskSpawnerRef:       kelosv1alpha1.TaskSpawnerReference{Name: ts.Name},
			TaskTemplateSnapshot: ts.Spec.TaskTemplate,
			IdleTimeout:          metav1.Duration{Duration: cfg.idleTimeout},
			MaxAge:               &maxAge,
			MaxQueuedTurns:       cfg.maxQueued,
		},
	}
	if err := controllerutil.SetControllerReference(ts, session, cl.Scheme()); err != nil {
		return nil, fmt.Errorf("setting AgentSession owner reference: %w", err)
	}
	if err := cl.Create(ctx, session); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating AgentSession: %w", err)
		}
		var existing kelosv1alpha1.AgentSession
		if getErr := cl.Get(ctx, client.ObjectKey{Namespace: ts.Namespace, Name: name}, &existing); getErr != nil {
			return nil, fmt.Errorf("fetching existing AgentSession: %w", getErr)
		}
		if isTerminalAgentSession(&existing) {
			return nil, fmt.Errorf("AgentSession %s already exists in terminal phase %s", name, existing.Status.Phase)
		}
		return &existing, nil
	}
	return session, nil
}

func newestActiveCronSession(items []kelosv1alpha1.AgentSession) *kelosv1alpha1.AgentSession {
	active := make([]kelosv1alpha1.AgentSession, 0, len(items))
	for _, session := range items {
		if isTerminalAgentSession(&session) {
			continue
		}
		active = append(active, session)
	}
	if len(active) == 0 {
		return nil
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].CreationTimestamp.After(active[j].CreationTimestamp.Time)
	})
	return &active[0]
}

func isTerminalAgentSession(session *kelosv1alpha1.AgentSession) bool {
	switch session.Status.Phase {
	case kelosv1alpha1.AgentSessionPhaseClosed, kelosv1alpha1.AgentSessionPhaseError:
		return true
	default:
		return false
	}
}

func cronTurnExists(ctx context.Context, cl client.Client, session *kelosv1alpha1.AgentSession, itemID string) (bool, error) {
	if itemID == "" {
		return false, nil
	}
	var list kelosv1alpha1.AgentTurnList
	if err := cl.List(ctx, &list, client.InNamespace(session.Namespace), client.MatchingLabels{labelAgentSession: session.Name}); err != nil {
		return false, err
	}
	for _, turn := range list.Items {
		if turn.Spec.Source.Type == sourceTypeCronTick && turn.Spec.Source.ID == itemID {
			return true, nil
		}
	}
	return false, nil
}

func aikidoTurnExists(ctx context.Context, cl client.Client, session *kelosv1alpha1.AgentSession, sourceID string) (bool, error) {
	if sourceID == "" {
		return false, nil
	}
	var list kelosv1alpha1.AgentTurnList
	if err := cl.List(ctx, &list, client.InNamespace(session.Namespace), client.MatchingLabels{labelAgentSession: session.Name}); err != nil {
		return false, err
	}
	for _, turn := range list.Items {
		if turn.Spec.Source.Type == sourceTypeAikidoIssueGroup && turn.Spec.Source.ID == sourceID {
			return true, nil
		}
	}
	return false, nil
}

func countQueuedOrRunningCronTurns(ctx context.Context, cl client.Client, session *kelosv1alpha1.AgentSession) (int32, error) {
	var list kelosv1alpha1.AgentTurnList
	if err := cl.List(ctx, &list, client.InNamespace(session.Namespace), client.MatchingLabels{labelAgentSession: session.Name}); err != nil {
		return 0, err
	}
	var count int32
	for _, turn := range list.Items {
		switch turn.Status.Phase {
		case "", kelosv1alpha1.AgentTurnPhaseQueued, kelosv1alpha1.AgentTurnPhaseRunning:
			count++
		}
	}
	return count, nil
}

func nextCronTurnSequence(ctx context.Context, cl client.Client, session *kelosv1alpha1.AgentSession) (int32, error) {
	var list kelosv1alpha1.AgentTurnList
	if err := cl.List(ctx, &list, client.InNamespace(session.Namespace), client.MatchingLabels{labelAgentSession: session.Name}); err != nil {
		return 0, err
	}
	var maxSeq int32
	for _, turn := range list.Items {
		if turn.Spec.Sequence > maxSeq {
			maxSeq = turn.Spec.Sequence
		}
	}
	return maxSeq + 1, nil
}

func cronSessionScopeHash(namespace, spawner, scope string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{namespace, spawner, scope}, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

func cronSessionName(spawnerName, scopeHash string, generation int32) string {
	suffix := "-cron-sess-" + scopeHash
	if generation > 1 {
		suffix = fmt.Sprintf("%s-g%d", suffix, generation)
	}
	prefix := spawnerName
	if len(prefix) > 63-len(suffix) {
		prefix = strings.TrimRight(prefix[:63-len(suffix)], "-.")
	}
	if prefix == "" {
		prefix = "cron"
	}
	return prefix + suffix
}

func aikidoSessionName(spawnerName, scopeHash string, generation int32) string {
	suffix := "-aikido-sess-" + scopeHash
	if generation > 1 {
		suffix = fmt.Sprintf("%s-g%d", suffix, generation)
	}
	prefix := spawnerName
	if len(prefix) > 63-len(suffix) {
		prefix = strings.TrimRight(prefix[:63-len(suffix)], "-.")
	}
	if prefix == "" {
		prefix = "aikido"
	}
	return prefix + suffix
}

func aikidoTurnSourceID(item source.WorkItem, runTime time.Time) string {
	sourceID := strings.TrimSpace(item.ID)
	if sourceID == "" {
		sourceID = "aikido-group"
	}
	return sourceID + "-" + runTime.UTC().Format("20060102-1504")
}

func cronTurnName(sessionName string, sequence int32) string {
	suffix := fmt.Sprintf("-t-%04d", sequence)
	prefix := sessionName
	if len(prefix)+len(suffix) > 63 {
		hash := shortHexHash(sessionName, 8)
		suffix = fmt.Sprintf("-%s-t-%04d", hash, sequence)
		prefix = strings.TrimRight(prefix[:63-len(suffix)], "-.")
	}
	if prefix == "" {
		prefix = "turn"
	}
	return prefix + suffix
}

func shortHexHash(value string, length int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	if length > len(encoded) {
		return encoded
	}
	return encoded[:length]
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// sourceAnnotations returns source-owned annotations for a spawned Task.
// WorkItem metadata is copied first so every source can expose stable
// machine-readable identifiers. Source-specific annotations are applied after
// template metadata and therefore win on conflicts.
func sourceAnnotations(ts *kelosv1alpha1.TaskSpawner, item source.WorkItem) map[string]string {
	annotations := make(map[string]string, len(item.Metadata)+4)
	for k, v := range item.Metadata {
		annotations[k] = v
	}

	if ts.Spec.When.GitHubIssues == nil && ts.Spec.When.GitHubPullRequests == nil {
		if len(annotations) == 0 {
			return nil
		}
		return annotations
	}

	kind := "issue"
	if item.Kind == "PR" {
		kind = "pull-request"
	}

	annotations[reporting.AnnotationSourceKind] = kind
	annotations[reporting.AnnotationSourceNumber] = strconv.Itoa(item.Number)

	if reportingEnabled(ts) {
		annotations[reporting.AnnotationGitHubReporting] = "enabled"
	}

	if checksReportingEnabled(ts) {
		annotations[reporting.AnnotationGitHubChecks] = "enabled"
		if item.HeadSHA != "" {
			annotations[reporting.AnnotationSourceSHA] = item.HeadSHA
		}
		if name := resolvedCheckName(ts); name != "" {
			annotations[reporting.AnnotationGitHubCheckName] = name
		}
	}

	return annotations
}

// reportingEnabled returns true when GitHub comment reporting is configured
// and enabled on the TaskSpawner. This only covers polling-based sources
// (Issues, PRs); webhook-based reporting is handled by the webhook server
// and its handler.
func reportingEnabled(ts *kelosv1alpha1.TaskSpawner) bool {
	if ts.Spec.When.GitHubIssues != nil && ts.Spec.When.GitHubIssues.Reporting != nil {
		return ts.Spec.When.GitHubIssues.Reporting.Enabled
	}
	if ts.Spec.When.GitHubPullRequests != nil && ts.Spec.When.GitHubPullRequests.Reporting != nil {
		return ts.Spec.When.GitHubPullRequests.Reporting.Enabled
	}
	return false
}

// checksReportingEnabled returns true when GitHub Checks API reporting is
// configured and enabled on the TaskSpawner.
func checksReportingEnabled(ts *kelosv1alpha1.TaskSpawner) bool {
	if ts.Spec.When.GitHubPullRequests != nil && ts.Spec.When.GitHubPullRequests.Reporting != nil && ts.Spec.When.GitHubPullRequests.Reporting.Checks != nil {
		return true
	}
	return false
}

// resolvedCheckName returns the configured check name, or empty string for
// the default.
func resolvedCheckName(ts *kelosv1alpha1.TaskSpawner) string {
	if ts.Spec.When.GitHubPullRequests != nil && ts.Spec.When.GitHubPullRequests.Reporting != nil && ts.Spec.When.GitHubPullRequests.Reporting.Checks != nil {
		return ts.Spec.When.GitHubPullRequests.Reporting.Checks.Name
	}
	return ""
}

type resolvedGitHubCommentPolicy struct {
	TriggerComment    string
	ExcludeComments   []string
	AllowedUsers      []string
	AllowedTeams      []string
	MinimumPermission string
}

func githubTeamRefsToStrings(teams []kelosv1alpha1.GitHubTeamRef) []string {
	if len(teams) == 0 {
		return nil
	}

	out := make([]string, len(teams))
	for i, team := range teams {
		out[i] = string(team)
	}
	return out
}

func resolveGitHubCommentPolicy(policy *kelosv1alpha1.GitHubCommentPolicy, legacyTrigger string, legacyExclude []string) (resolvedGitHubCommentPolicy, error) {
	legacyConfigured := strings.TrimSpace(legacyTrigger) != "" || len(legacyExclude) > 0
	if policy != nil {
		if legacyConfigured {
			return resolvedGitHubCommentPolicy{}, fmt.Errorf("commentPolicy cannot be used with triggerComment or excludeComments")
		}

		return resolvedGitHubCommentPolicy{
			TriggerComment:    policy.TriggerComment,
			ExcludeComments:   append([]string(nil), policy.ExcludeComments...),
			AllowedUsers:      append([]string(nil), policy.AllowedUsers...),
			AllowedTeams:      githubTeamRefsToStrings(policy.AllowedTeams),
			MinimumPermission: policy.MinimumPermission,
		}, nil
	}

	return resolvedGitHubCommentPolicy{
		TriggerComment:  legacyTrigger,
		ExcludeComments: append([]string(nil), legacyExclude...),
	}, nil
}

func buildSource(ctx context.Context, ts *kelosv1alpha1.TaskSpawner, owner, repo, apiBaseURL string, tokenResolver func(context.Context) (string, error), jiraBaseURL, jiraProject, jiraJQL string, httpClient *http.Client) (source.Source, error) {
	return buildSourceWithProxy(ctx, ts, owner, repo, "", apiBaseURL, tokenResolver, jiraBaseURL, jiraProject, jiraJQL, httpClient)
}

func buildSourceWithProxy(ctx context.Context, ts *kelosv1alpha1.TaskSpawner, owner, repo, ghProxyURL, apiBaseURL string, tokenResolver func(context.Context) (string, error), jiraBaseURL, jiraProject, jiraJQL string, httpClient *http.Client) (source.Source, error) {
	return buildSourceWithProxyAndAikido(ctx, ts, owner, repo, ghProxyURL, apiBaseURL, tokenResolver, jiraBaseURL, jiraProject, jiraJQL, source.DefaultAikidoProxyURL, httpClient)
}

func buildSourceWithProxyAndAikido(ctx context.Context, ts *kelosv1alpha1.TaskSpawner, owner, repo, ghProxyURL, apiBaseURL string, tokenResolver func(context.Context) (string, error), jiraBaseURL, jiraProject, jiraJQL, aikidoProxyURL string, httpClient *http.Client) (source.Source, error) {
	if ts.Spec.When.GitHubIssues != nil {
		gh := ts.Spec.When.GitHubIssues
		commentPolicy, err := resolveGitHubCommentPolicy(gh.CommentPolicy, gh.TriggerComment, gh.ExcludeComments)
		if err != nil {
			return nil, err
		}
		baseURL := apiBaseURL
		token := ""
		if ghProxyURL != "" {
			baseURL = ghProxyURL
		} else if tokenResolver != nil {
			token, err = tokenResolver(ctx)
			if err != nil {
				return nil, err
			}
		}
		return &source.GitHubSource{
			Owner:             owner,
			Repo:              repo,
			Types:             gh.Types,
			Labels:            gh.Labels,
			ExcludeLabels:     gh.ExcludeLabels,
			State:             gh.State,
			Assignee:          gh.Assignee,
			Author:            gh.Author,
			ExcludeAuthors:    gh.ExcludeAuthors,
			Token:             token,
			BaseURL:           baseURL,
			Client:            httpClient,
			TriggerComment:    commentPolicy.TriggerComment,
			ExcludeComments:   commentPolicy.ExcludeComments,
			AllowedUsers:      commentPolicy.AllowedUsers,
			AllowedTeams:      commentPolicy.AllowedTeams,
			MinimumPermission: commentPolicy.MinimumPermission,
			PriorityLabels:    gh.PriorityLabels,
		}, nil
	}

	if ts.Spec.When.GitHubPullRequests != nil {
		gh := ts.Spec.When.GitHubPullRequests
		commentPolicy, err := resolveGitHubCommentPolicy(gh.CommentPolicy, gh.TriggerComment, gh.ExcludeComments)
		if err != nil {
			return nil, err
		}
		baseURL := apiBaseURL
		token := ""
		if ghProxyURL != "" {
			baseURL = ghProxyURL
		} else if tokenResolver != nil {
			token, err = tokenResolver(ctx)
			if err != nil {
				return nil, err
			}
		}

		src := &source.GitHubPullRequestSource{
			Owner:             owner,
			Repo:              repo,
			Labels:            gh.Labels,
			ExcludeLabels:     gh.ExcludeLabels,
			State:             gh.State,
			Author:            gh.Author,
			ExcludeAuthors:    gh.ExcludeAuthors,
			Token:             token,
			BaseURL:           baseURL,
			Client:            httpClient,
			ReviewState:       gh.ReviewState,
			TriggerComment:    commentPolicy.TriggerComment,
			ExcludeComments:   commentPolicy.ExcludeComments,
			AllowedUsers:      commentPolicy.AllowedUsers,
			AllowedTeams:      commentPolicy.AllowedTeams,
			MinimumPermission: commentPolicy.MinimumPermission,
			Draft:             gh.Draft,
			PriorityLabels:    gh.PriorityLabels,
		}
		if gh.FilePatterns != nil {
			src.FileInclude = gh.FilePatterns.Include
			src.FileExclude = gh.FilePatterns.Exclude
		}
		return src, nil
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

	if ts.Spec.When.Aikido != nil {
		aikido := ts.Spec.When.Aikido
		return &source.AikidoSource{
			ProxyBaseURL:   aikidoProxyURL,
			Repositories:   aikido.Repositories,
			Statuses:       aikido.Statuses,
			Severities:     aikido.Severities,
			Branch:         aikido.Branch,
			IssueTypes:     aikido.IssueTypes,
			UseIssueExport: aikidoSessionEnabled(ts),
			Client:         httpClient,
		}, nil
	}

	return nil, fmt.Errorf("no source configured in TaskSpawner %s/%s", ts.Namespace, ts.Name)
}

// newGitHubTokenResolver returns a function that resolves a GitHub API token.
// It prefers a static PAT, then falls back to GitHub App credentials.
func newGitHubTokenResolver(token, appID, installID, privateKey, apiBaseURL string) func(context.Context) (string, error) {
	if token != "" {
		return func(context.Context) (string, error) { return token, nil }
	}
	if appID == "" || installID == "" || privateKey == "" {
		return nil
	}

	creds, err := githubapp.ParseCredentials(map[string][]byte{
		"appID":          []byte(appID),
		"installationID": []byte(installID),
		"privateKey":     []byte(privateKey),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse GitHub App credentials: %v\n", err)
		os.Exit(1)
	}

	tc := githubapp.NewTokenClient()
	if apiBaseURL != "" {
		tc.BaseURL = apiBaseURL
	}
	return githubapp.NewTokenProvider(tc, creds).Token
}

func priorityLabelsForTaskSpawner(ts *kelosv1alpha1.TaskSpawner) []string {
	if ts.Spec.When.GitHubIssues != nil {
		return ts.Spec.When.GitHubIssues.PriorityLabels
	}
	if ts.Spec.When.GitHubPullRequests != nil {
		return ts.Spec.When.GitHubPullRequests.PriorityLabels
	}
	return nil
}

// deriveUpstreamRepo extracts the owner/repo from the githubIssues.repo or
// githubPullRequests.repo override, returning it in "owner/repo" format.
// Returns an empty string when no override is configured.
func deriveUpstreamRepo(ts *kelosv1alpha1.TaskSpawner) string {
	var repoOverride string
	if ts.Spec.When.GitHubIssues != nil && ts.Spec.When.GitHubIssues.Repo != "" {
		repoOverride = ts.Spec.When.GitHubIssues.Repo
	} else if ts.Spec.When.GitHubPullRequests != nil && ts.Spec.When.GitHubPullRequests.Repo != "" {
		repoOverride = ts.Spec.When.GitHubPullRequests.Repo
	}
	if repoOverride == "" {
		return ""
	}
	// Detect shorthand "owner/repo" format by checking that the first
	// segment has no ":" (rules out SSH "git@host:owner/repo") and no "."
	// (rules out "https://host/..."). Anything else is treated as a URL.
	parts := strings.SplitN(repoOverride, "/", 2)
	if len(parts) == 2 && !strings.Contains(parts[0], ":") && !strings.Contains(parts[0], ".") {
		return repoOverride
	}
	// Parse full URL to extract owner/repo.
	owner, repo := parseOwnerRepo(repoOverride)
	if owner != "" && repo != "" {
		return owner + "/" + repo
	}
	return ""
}

// parseOwnerRepo extracts owner and repo from a GitHub repository URL.
// Supports HTTPS (https://host/owner/repo) and SSH (git@host:owner/repo).
func parseOwnerRepo(repoURL string) (string, string) {
	repoURL = strings.TrimSuffix(repoURL, ".git")
	repoURL = strings.TrimSuffix(repoURL, "/")

	// Handle SSH format: git@host:owner/repo
	// SSH URLs have no "//" after the colon, unlike "https://".
	if idx := strings.Index(repoURL, ":"); idx > 0 && !strings.HasPrefix(repoURL, "http") {
		path := repoURL[idx+1:]
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
	}

	// Handle HTTPS format: https://host/owner/repo
	parts := strings.Split(repoURL, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2], parts[len(parts)-1]
	}
	return "", ""
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
