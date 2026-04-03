package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
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
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.DurationVar(&reportingInterval, "reporting-interval", 30*time.Second, "How often to run the Slack reporting cycle.")

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

	ctx := ctrl.SetupSignalHandler()

	// Create the Slack handler
	handler, err := kelosslack.NewSlackHandler(
		ctx,
		mgr.GetClient(),
		botToken,
		appToken,
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

	// Register reporting loop as a leader-elected runnable.
	if err := mgr.Add(&reportingRunnable{
		client:   mgr.GetClient(),
		botToken: botToken,
		interval: reportingInterval,
	}); err != nil {
		setupLog.Error(err, "Unable to register reporting loop with manager")
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
	botToken string
	interval time.Duration
}

func (r *reportingRunnable) Start(ctx context.Context) error {
	setupLog.Info("Starting Slack reporting loop", "interval", r.interval)
	runReportingLoop(ctx, r.client, r.botToken, r.interval)
	return nil
}

func (r *reportingRunnable) NeedLeaderElection() bool { return true }

// runReportingLoop periodically reports Slack task status for ALL Slack-annotated
// Tasks cluster-wide. This replaces the per-TaskSpawner reporting that previously
// ran in each spawner pod.
func runReportingLoop(ctx context.Context, cl client.Client, botToken string, interval time.Duration) {
	log := ctrl.Log.WithName("slack-reporter")
	slackReporter := &reporting.SlackTaskReporter{
		Client:   cl,
		Reporter: &reporting.SlackReporter{BotToken: botToken},
	}

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

	for i := range taskList.Items {
		task := &taskList.Items[i]
		if err := reporter.ReportTaskStatus(ctx, task); err != nil {
			log.Error(err, "Failed to report task status",
				"task", task.Name, "namespace", task.Namespace)
		}
	}

	return nil
}
