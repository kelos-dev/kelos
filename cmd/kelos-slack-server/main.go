package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/logging"
	"github.com/kelos-dev/kelos/internal/reporting"
	kelosslack "github.com/kelos-dev/kelos/internal/slack"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		reportingInterval    time.Duration
		activityInterval     time.Duration
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.DurationVar(&reportingInterval, "reporting-interval", 30*time.Second, "How often to run the Slack reporting cycle.")
	flag.DurationVar(&activityInterval, "activity-interval", 5*time.Second, "How often to update Slack activity indicators.")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(opts)))

	botToken := os.Getenv("SLACK_BOT_TOKEN")
	appToken := os.Getenv("SLACK_APP_TOKEN")
	if botToken == "" || appToken == "" {
		setupLog.Error(fmt.Errorf("missing tokens"), "SLACK_BOT_TOKEN and SLACK_APP_TOKEN environment variables are required")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "kelos-slack-server",
	})
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "Unable to create Kubernetes clientset")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	joinMessageFile := os.Getenv("SLACK_JOIN_MESSAGE_FILE")

	// Create the Slack handler
	handler, err := kelosslack.NewSlackHandler(
		ctx,
		mgr.GetClient(),
		botToken,
		appToken,
		joinMessageFile,
		ctrl.Log.WithName("slack"),
	)
	if err != nil {
		setupLog.Error(err, "Unable to create Slack handler")
		os.Exit(1)
	}

	// Register Socket Mode listener as a leader-elected runnable so that only
	// one replica opens the single-connection Socket Mode WebSocket.
	if err := mgr.Add(&slackRunnable{handler: handler}); err != nil {
		setupLog.Error(err, "Unable to register Slack handler with manager")
		os.Exit(1)
	}

	// Build the shared SlackTaskReporter used by both the reporting and
	// activity loops. Sharing the instance ensures activity state is
	// correctly cleared when a progress snapshot is posted.
	slackReporter := &reporting.SlackTaskReporter{
		Client:         mgr.GetClient(),
		Reporter:       &reporting.SlackReporter{BotToken: botToken},
		ProgressReader: &reporting.DefaultProgressReader{Clientset: clientset},
		ActivityReader: &reporting.DefaultActivityReader{Clientset: clientset},
	}

	// Register reporting loop as a leader-elected runnable.
	if err := mgr.Add(&reportingRunnable{
		client:   mgr.GetClient(),
		reporter: slackReporter,
		interval: reportingInterval,
	}); err != nil {
		setupLog.Error(err, "Unable to register reporting loop with manager")
		os.Exit(1)
	}

	// Register activity indicator loop as a leader-elected runnable.
	if err := mgr.Add(&activityRunnable{
		client:   mgr.GetClient(),
		reporter: slackReporter,
		interval: activityInterval,
	}); err != nil {
		setupLog.Error(err, "Unable to register activity loop with manager")
		os.Exit(1)
	}

	// Health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}

// slackRunnable wraps the SlackHandler as a leader-elected manager.Runnable.
// This ensures only the leader replica opens the Socket Mode connection.
type slackRunnable struct {
	handler *kelosslack.SlackHandler
}

func (r *slackRunnable) Start(ctx context.Context) error {
	setupLog.Info("Starting Slack Socket Mode listener")
	err := r.handler.Start(ctx)
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (r *slackRunnable) NeedLeaderElection() bool { return true }

// reportingRunnable wraps the reporting loop as a leader-elected manager.Runnable.
type reportingRunnable struct {
	client   client.Client
	reporter *reporting.SlackTaskReporter
	interval time.Duration
}

func (r *reportingRunnable) Start(ctx context.Context) error {
	setupLog.Info("Starting Slack reporting loop", "interval", r.interval)
	runReportingLoop(ctx, r.client, r.reporter, r.interval)
	return nil
}

func (r *reportingRunnable) NeedLeaderElection() bool { return true }

// activityRunnable wraps the activity indicator loop as a leader-elected
// manager.Runnable. It shares the SlackTaskReporter with the reporting loop.
type activityRunnable struct {
	client   client.Client
	reporter *reporting.SlackTaskReporter
	interval time.Duration
}

func (r *activityRunnable) Start(ctx context.Context) error {
	setupLog.Info("Starting Slack activity indicator loop", "interval", r.interval)
	runActivityLoop(ctx, r.client, r.reporter, r.interval)
	return nil
}

func (r *activityRunnable) NeedLeaderElection() bool { return true }

// runReportingLoop periodically reports Slack task status for ALL Slack-annotated
// Tasks cluster-wide. This replaces the per-TaskSpawner reporting that previously
// ran in each spawner pod.
func runReportingLoop(ctx context.Context, cl client.Client, slackReporter *reporting.SlackTaskReporter, interval time.Duration) {
	log := ctrl.Log.WithName("slack-reporter")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := runSlackReportingCycle(ctx, cl, slackReporter, log); err != nil {
				log.Error(err, "Reporting cycle failed")
			}
		}
	}
}

// runSlackReportingCycle lists all Tasks with Slack reporting enabled and
// reports their status. Unlike the spawner version, this is not scoped to
// a single TaskSpawner.
func runSlackReportingCycle(ctx context.Context, cl client.Client, reporter *reporting.SlackTaskReporter, log logr.Logger) error {
	var taskList kelosv1alpha1.TaskList
	if err := cl.List(ctx, &taskList, &client.ListOptions{}); err != nil {
		return fmt.Errorf("Listing tasks for Slack reporting: %w", err)
	}

	activeUIDs := make(map[types.UID]bool, len(taskList.Items))
	for i := range taskList.Items {
		task := &taskList.Items[i]
		activeUIDs[task.UID] = true
		if err := reporter.ReportTaskStatus(ctx, task); err != nil {
			log.Error(err, "Failed to report task status",
				"task", task.Name, "namespace", task.Namespace)
		}
	}

	reporter.SweepProgressCache(activeUIDs)

	return nil
}

// runActivityLoop periodically updates activity indicators for running tasks.
// It runs on a faster cadence than the reporting loop.
func runActivityLoop(ctx context.Context, cl client.Client, reporter *reporting.SlackTaskReporter, interval time.Duration) {
	log := ctrl.Log.WithName("slack-activity")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var taskList kelosv1alpha1.TaskList
			if err := cl.List(ctx, &taskList, &client.ListOptions{}); err != nil {
				log.Error(err, "Listing tasks for activity update")
				continue
			}

			for i := range taskList.Items {
				reporter.UpdateActivityIndicator(ctx, &taskList.Items[i])
			}
		}
	}
}
