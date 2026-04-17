package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/controller"
	"github.com/kelos-dev/kelos/internal/githubapp"
	"github.com/kelos-dev/kelos/internal/logging"
	"github.com/kelos-dev/kelos/internal/telemetry"
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
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var claudeCodeImage string
	var claudeCodeImagePullPolicy string
	var codexImage string
	var codexImagePullPolicy string
	var geminiImage string
	var geminiImagePullPolicy string
	var openCodeImage string
	var openCodeImagePullPolicy string
	var cursorImage string
	var cursorImagePullPolicy string
	var spawnerImage string
	var spawnerImagePullPolicy string
	var spawnerResourceRequests string
	var spawnerResourceLimits string
	var ghProxyImage string
	var ghProxyImagePullPolicy string
	var ghProxyResourceRequests string
	var ghProxyResourceLimits string
	var ghProxyCacheTTL time.Duration
	var telemetryReport bool
	var telemetryEndpoint string
	var telemetryEnvironment string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&claudeCodeImage, "claude-code-image", controller.ClaudeCodeImage, "The image to use for Claude Code agent containers.")
	flag.StringVar(&claudeCodeImagePullPolicy, "claude-code-image-pull-policy", "", "The image pull policy for Claude Code agent containers (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&codexImage, "codex-image", controller.CodexImage, "The image to use for Codex agent containers.")
	flag.StringVar(&codexImagePullPolicy, "codex-image-pull-policy", "", "The image pull policy for Codex agent containers (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&geminiImage, "gemini-image", controller.GeminiImage, "The image to use for Gemini CLI agent containers.")
	flag.StringVar(&geminiImagePullPolicy, "gemini-image-pull-policy", "", "The image pull policy for Gemini CLI agent containers (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&openCodeImage, "opencode-image", controller.OpenCodeImage, "The image to use for OpenCode agent containers.")
	flag.StringVar(&openCodeImagePullPolicy, "opencode-image-pull-policy", "", "The image pull policy for OpenCode agent containers (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&cursorImage, "cursor-image", controller.CursorImage, "The image to use for Cursor CLI agent containers.")
	flag.StringVar(&cursorImagePullPolicy, "cursor-image-pull-policy", "", "The image pull policy for Cursor CLI agent containers (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&spawnerImage, "spawner-image", controller.DefaultSpawnerImage, "The image to use for spawner Deployments.")
	flag.StringVar(&spawnerImagePullPolicy, "spawner-image-pull-policy", "", "The image pull policy for spawner Deployments (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&spawnerResourceRequests, "spawner-resource-requests", "", "Resource requests for spawner containers as comma-separated name=value pairs (e.g., cpu=250m,memory=512Mi).")
	flag.StringVar(&spawnerResourceLimits, "spawner-resource-limits", "", "Resource limits for spawner containers as comma-separated name=value pairs (e.g., cpu=1,memory=1Gi).")
	flag.StringVar(&ghProxyImage, "ghproxy-image", controller.DefaultGHProxyImage, "The image to use for workspace ghproxy Deployments.")
	flag.StringVar(&ghProxyImagePullPolicy, "ghproxy-image-pull-policy", "", "The image pull policy for workspace ghproxy Deployments (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&ghProxyResourceRequests, "ghproxy-resource-requests", "", "Resource requests for workspace ghproxy containers as comma-separated name=value pairs (e.g., cpu=50m,memory=64Mi).")
	flag.StringVar(&ghProxyResourceLimits, "ghproxy-resource-limits", "", "Resource limits for workspace ghproxy containers as comma-separated name=value pairs (e.g., cpu=200m,memory=128Mi).")
	flag.DurationVar(&ghProxyCacheTTL, "ghproxy-cache-ttl", 0, "Cache TTL for workspace ghproxy instances (e.g., 30s, 1m). When set, passed as --cache-ttl to ghproxy containers. Zero means use the ghproxy default (15s).")
	flag.BoolVar(&telemetryReport, "telemetry-report", false, "Run a one-shot telemetry report and exit.")
	flag.StringVar(&telemetryEndpoint, "telemetry-endpoint", telemetry.DefaultPostHogEndpoint, "The PostHog endpoint for sending telemetry reports.")
	flag.StringVar(&telemetryEnvironment, "telemetry-environment", "production", "The environment label for telemetry reports (e.g., production, development).")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(opts)))

	// Parse resource flags.
	var spawnerResources *corev1.ResourceRequirements
	requests, err := controller.ParseResourceList(spawnerResourceRequests)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing --spawner-resource-requests: %v\n", err)
		os.Exit(1)
	}
	limits, err := controller.ParseResourceList(spawnerResourceLimits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing --spawner-resource-limits: %v\n", err)
		os.Exit(1)
	}
	if requests != nil || limits != nil {
		spawnerResources = &corev1.ResourceRequirements{
			Requests: requests,
			Limits:   limits,
		}
	}
	var ghProxyResources *corev1.ResourceRequirements
	ghProxyRequests, err := controller.ParseResourceList(ghProxyResourceRequests)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing --ghproxy-resource-requests: %v\n", err)
		os.Exit(1)
	}
	ghProxyLimits, err := controller.ParseResourceList(ghProxyResourceLimits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing --ghproxy-resource-limits: %v\n", err)
		os.Exit(1)
	}
	if ghProxyRequests != nil || ghProxyLimits != nil {
		ghProxyResources = &corev1.ResourceRequirements{
			Requests: ghProxyRequests,
			Limits:   ghProxyLimits,
		}
	}
	if telemetryReport {
		log := ctrl.Log.WithName("telemetry")
		cfg := ctrl.GetConfigOrDie()

		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			setupLog.Error(err, "Unable to create client for telemetry")
			os.Exit(1)
		}

		clientset, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			setupLog.Error(err, "Unable to create clientset for telemetry")
			os.Exit(1)
		}

		phClient, err := telemetry.NewPostHogClient(telemetryEndpoint)
		if err != nil {
			setupLog.Error(err, "Unable to create PostHog client")
			os.Exit(1)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := telemetry.Run(ctx, log, c, clientset, phClient, telemetryEnvironment); err != nil {
			setupLog.Error(err, "Telemetry report failed")
			os.Exit(1)
		}
		os.Exit(0)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "kelos-controller-leader-election",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to create Kubernetes clientset")
		os.Exit(1)
	}

	jobBuilder := controller.NewJobBuilder()
	jobBuilder.ClaudeCodeImage = claudeCodeImage
	jobBuilder.ClaudeCodeImagePullPolicy = corev1.PullPolicy(claudeCodeImagePullPolicy)
	jobBuilder.CodexImage = codexImage
	jobBuilder.CodexImagePullPolicy = corev1.PullPolicy(codexImagePullPolicy)
	jobBuilder.GeminiImage = geminiImage
	jobBuilder.GeminiImagePullPolicy = corev1.PullPolicy(geminiImagePullPolicy)
	jobBuilder.OpenCodeImage = openCodeImage
	jobBuilder.OpenCodeImagePullPolicy = corev1.PullPolicy(openCodeImagePullPolicy)
	jobBuilder.CursorImage = cursorImage
	jobBuilder.CursorImagePullPolicy = corev1.PullPolicy(cursorImagePullPolicy)
	if err = (&controller.TaskReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		JobBuilder:   jobBuilder,
		Clientset:    clientset,
		TokenClient:  githubapp.NewTokenClient(),
		Recorder:     mgr.GetEventRecorderFor("kelos-controller"),
		BranchLocker: controller.NewBranchLocker(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Task")
		os.Exit(1)
	}

	deploymentBuilder := controller.NewDeploymentBuilder()
	deploymentBuilder.SpawnerImage = spawnerImage
	deploymentBuilder.SpawnerImagePullPolicy = corev1.PullPolicy(spawnerImagePullPolicy)
	deploymentBuilder.SpawnerResources = spawnerResources
	workspaceProxyBuilder := controller.NewWorkspaceGHProxyBuilder()
	workspaceProxyBuilder.GHProxyImage = ghProxyImage
	workspaceProxyBuilder.GHProxyImagePullPolicy = corev1.PullPolicy(ghProxyImagePullPolicy)
	workspaceProxyBuilder.GHProxyResources = ghProxyResources
	workspaceProxyBuilder.GHProxyCacheTTL = ghProxyCacheTTL
	if err = (&controller.WorkspaceReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		ProxyBuilder: workspaceProxyBuilder,
		Recorder:     mgr.GetEventRecorderFor("kelos-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Workspace")
		os.Exit(1)
	}
	if err = (&controller.TaskSpawnerReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		DeploymentBuilder: deploymentBuilder,
		Recorder:          mgr.GetEventRecorderFor("kelos-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TaskSpawner")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
