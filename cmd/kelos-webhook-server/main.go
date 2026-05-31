package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/githubapp"
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
		source                  string
		gatewayMode             bool
		metricsAddr             string
		probeAddr               string
		webhookAddr             string
		enableLeaderElection    bool
		githubToken             string
		githubAppID             string
		githubAppInstallationID string
		githubAppPrivateKey     string
		githubAPIBaseURL        string
		githubTokenFile         string
	)

	flag.StringVar(&source, "source", "", "Webhook source type (github, linear, or generic). Ignored when --gateway-mode is set.")
	flag.BoolVar(&gatewayMode, "gateway-mode", false, "Serve per-gateway paths (/webhook/<namespace>/<name>) driven by WebhookGateway resources instead of a single --source.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&webhookAddr, "webhook-bind-address", ":8443", "The address the webhook endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.StringVar(&githubToken, "github-token", "", "GitHub personal access token for API calls (env: GITHUB_TOKEN)")
	flag.StringVar(&githubAppID, "github-app-id", "", "GitHub App ID for installation token generation (env: GITHUB_APP_ID)")
	flag.StringVar(&githubAppInstallationID, "github-app-installation-id", "", "GitHub App installation ID (env: GITHUB_APP_INSTALLATION_ID)")
	flag.StringVar(&githubAppPrivateKey, "github-app-private-key", "", "GitHub App private key in PEM format (env: GITHUB_APP_PRIVATE_KEY)")
	flag.StringVar(&githubAPIBaseURL, "github-api-base-url", "", "GitHub API base URL for enterprise servers (env: GITHUB_API_BASE_URL)")
	flag.StringVar(&githubTokenFile, "github-token-file", "", "Path to file containing GitHub token for reporting.")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(opts)))

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
	if githubAPIBaseURL == "" {
		githubAPIBaseURL = os.Getenv("GITHUB_API_BASE_URL")
	}

	// Validate source parameter (legacy mode only; gateway mode routes by path).
	source = strings.ToLower(strings.TrimSpace(source))
	var webhookSource webhook.WebhookSource
	if !gatewayMode {
		switch source {
		case "github":
			webhookSource = webhook.GitHubSource
		case "linear":
			webhookSource = webhook.LinearSource
		case "generic":
			webhookSource = webhook.GenericSource
		default:
			setupLog.Error(fmt.Errorf("invalid source: %s", source),
				"Source must be 'github', 'linear', or 'generic'")
			os.Exit(1)
		}
	}

	leaderID := source
	mgrOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
	}
	if gatewayMode {
		leaderID = "gateway"
		// Resolve Secrets with direct Gets instead of a cluster-wide cache so the
		// webhook server needs only get (not list/watch) on Secrets and does not
		// cache every Secret in the cluster.
		mgrOptions.Client = client.Options{
			Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}}},
		}
	}
	mgrOptions.LeaderElectionID = fmt.Sprintf("kelos-webhook-%s", leaderID)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOptions)
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	// Set up signal handling context
	ctx := ctrl.SetupSignalHandler()

	// Build a unified GitHub token resolver from any configured credential
	// source. Used for PR branch enrichment in the webhook handler and for
	// status reporting on Tasks. Precedence: --github-token, GitHub App,
	// --github-token-file. The static --github-token already absorbed the
	// GITHUB_TOKEN env fallback above.
	var tokenResolver func(context.Context) (string, error)
	switch {
	case githubToken != "":
		tokenResolver = func(context.Context) (string, error) { return githubToken, nil }
	case githubAppID != "" && githubAppInstallationID != "" && githubAppPrivateKey != "":
		creds, err := githubapp.ParseCredentials(map[string][]byte{
			"appID":          []byte(githubAppID),
			"installationID": []byte(githubAppInstallationID),
			"privateKey":     []byte(githubAppPrivateKey),
		})
		if err != nil {
			setupLog.Error(err, "Failed to parse GitHub App credentials")
			os.Exit(1)
		}
		tc := githubapp.NewTokenClient()
		if githubAPIBaseURL != "" {
			tc.BaseURL = githubAPIBaseURL
		}
		tokenResolver = githubapp.NewTokenProvider(tc, creds).Token
	case githubTokenFile != "":
		tokenResolver = func(context.Context) (string, error) {
			data, err := os.ReadFile(githubTokenFile)
			if err != nil {
				return "", fmt.Errorf("reading token file %q: %w", githubTokenFile, err)
			}
			return strings.TrimSpace(string(data)), nil
		}
	}

	if tokenResolver != nil {
		webhook.SetGitHubTokenResolver(tokenResolver)
	}

	// Set up the HTTP mux. In gateway mode a single handler routes by path
	// (/webhook/<namespace>/<name>) using WebhookGateway resources. In legacy
	// mode a source-specific handler is mounted (generic uses /webhook/<source>,
	// github/linear use root).
	mux := http.NewServeMux()
	if gatewayMode {
		gatewayHandler, err := webhook.NewGatewayHandler(ctx, mgr.GetClient(), ctrl.Log.WithName("gateway"))
		if err != nil {
			setupLog.Error(err, "Unable to create gateway handler")
			os.Exit(1)
		}
		mux.Handle("/webhook/", gatewayHandler)
	} else {
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
		if webhookSource == webhook.GenericSource {
			mux.Handle("/webhook/", handler)
		} else {
			mux.Handle("/", handler)
		}
	}

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
		setupLog.Info("Starting webhook server", "addr", webhookAddr, "source", source, "gatewayMode", gatewayMode)
		if err := webhookServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			setupLog.Error(err, "Webhook server failed")
			os.Exit(1)
		}
	}()

	// Start the reporting reconciler in gateway mode (where each Task resolves
	// its GitHub credentials and API base URL from the stamped WebhookGateway),
	// or in legacy GitHub mode when a token resolver is available. Owner and repo
	// come from per-Task annotations stamped by the webhook handler from the
	// originating event payload, so one server can report against many
	// repositories. Legacy linear/generic sources never produce GitHub-reporting
	// tasks, so the reconciler stays disabled there.
	if gatewayMode || (webhookSource == webhook.GitHubSource && tokenResolver != nil) {
		reportingReconciler := &reportingReconciler{
			Client: mgr.GetClient(),
			config: reportingConfig{
				TokenResolver:    tokenResolver,
				GitHubAPIBaseURL: githubAPIBaseURL,
			},
		}
		if err := reportingReconciler.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create reporting controller")
			os.Exit(1)
		}
		setupLog.Info("Reporting controller enabled")
	} else if webhookSource == webhook.GitHubSource {
		setupLog.Info("Reporting controller disabled: no GitHub credentials configured. " +
			"Set --github-token, --github-app-* flags, or --github-token-file to enable status reporting on Tasks")
	}

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
