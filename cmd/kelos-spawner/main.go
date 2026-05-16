package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
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
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	start := time.Now()
	err := runCycleCore(ctx, cl, key, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL, tokenResolver, jiraBaseURL, jiraProject, jiraJQL, httpClient)
	discoveryDurationSeconds.Observe(time.Since(start).Seconds())
	if err != nil {
		discoveryErrorsTotal.Inc()
	}
	return err
}

func runCycleCore(ctx context.Context, cl client.Client, key types.NamespacedName, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL string, tokenResolver func(context.Context) (string, error), jiraBaseURL, jiraProject, jiraJQL string, httpClient *http.Client) error {
	var ts kelosv1alpha1.TaskSpawner
	if err := cl.Get(ctx, key, &ts); err != nil {
		return fmt.Errorf("fetching TaskSpawner: %w", err)
	}

	src, err := buildSourceWithProxy(ctx, &ts, githubOwner, githubRepo, ghProxyURL, githubAPIBaseURL, tokenResolver, jiraBaseURL, jiraProject, jiraJQL, httpClient)
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

	var newItems []source.WorkItem
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

		templateVars := source.WorkItemToTemplateVars(item)

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

// sourceAnnotations returns annotations that stamp GitHub source metadata
// onto a spawned Task. These annotations enable downstream consumers (such
// as the reporting watcher) to identify the originating issue or PR.
func sourceAnnotations(ts *kelosv1alpha1.TaskSpawner, item source.WorkItem) map[string]string {
	if ts.Spec.When.GitHubIssues == nil && ts.Spec.When.GitHubPullRequests == nil {
		return nil
	}

	kind := "issue"
	if item.Kind == "PR" {
		kind = "pull-request"
	}

	annotations := map[string]string{
		reporting.AnnotationSourceKind:   kind,
		reporting.AnnotationSourceNumber: strconv.Itoa(item.Number),
	}

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
