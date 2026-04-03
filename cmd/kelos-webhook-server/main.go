package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/logging"
	"github.com/kelos-dev/kelos/internal/webhook"
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
		source               string
		metricsAddr          string
		probeAddr            string
		webhookAddr          string
		enableLeaderElection bool
	)

	flag.StringVar(&source, "source", "", "Webhook source type (github or linear)")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&webhookAddr, "webhook-bind-address", ":8443", "The address the webhook endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(opts)))

	// Validate source parameter
	source = strings.ToLower(strings.TrimSpace(source))
	var webhookSource webhook.WebhookSource
	switch source {
	case "github":
		webhookSource = webhook.GitHubSource
	case "linear":
		webhookSource = webhook.LinearSource
	default:
		setupLog.Error(fmt.Errorf("invalid source: %s", source),
			"Source must be 'github' or 'linear'")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       fmt.Sprintf("kelos-webhook-%s", source),
	})
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	// Set up signal handling context
	ctx := ctrl.SetupSignalHandler()

	// Create webhook handler
	handler, err := webhook.NewWebhookHandler(
		ctx,
		mgr.GetClient(),
		webhookSource,
		ctrl.Log.WithName("webhook").WithValues("source", source),
	)
	if err != nil {
		setupLog.Error(err, "Unable to create webhook handler")
		os.Exit(1)
	}

	// Set up HTTP server for webhooks
	mux := http.NewServeMux()
	mux.Handle("/", handler)

	webhookServer := &http.Server{
		Addr:              webhookAddr,
		Handler:           mux,
		ReadTimeout:       30 * time.Second,  // Maximum time to read request including body
		WriteTimeout:      30 * time.Second,  // Maximum time to write response
		ReadHeaderTimeout: 10 * time.Second,  // Maximum time to read request headers
		IdleTimeout:       120 * time.Second, // Maximum time for keep-alive connections
	}

	// Start webhook server in goroutine
	go func() {
		setupLog.Info("Starting webhook server", "addr", webhookAddr, "source", source)
		if err := webhookServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			setupLog.Error(err, "Webhook server failed")
			os.Exit(1)
		}
	}()

	// Add health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")

	// Shutdown webhook server gracefully when context is cancelled
	go func() {
		<-ctx.Done()
		setupLog.Info("Shutting down webhook server")
		if err := webhookServer.Shutdown(context.Background()); err != nil {
			setupLog.Error(err, "Error shutting down webhook server")
		}
	}()

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}
