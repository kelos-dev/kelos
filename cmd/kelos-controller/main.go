package main

import (
	"context"
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/controller"
	"github.com/kelos-dev/kelos/internal/githubapp"
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
	var spawnerImage string
	var spawnerImagePullPolicy string
	var tokenRefresherImage string
	var tokenRefresherImagePullPolicy string
	var telemetryReport bool
	var telemetryEndpoint string

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
	flag.StringVar(&spawnerImage, "spawner-image", controller.DefaultSpawnerImage, "The image to use for spawner Deployments.")
	flag.StringVar(&spawnerImagePullPolicy, "spawner-image-pull-policy", "", "The image pull policy for spawner Deployments (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&tokenRefresherImage, "token-refresher-image", controller.DefaultTokenRefresherImage, "The image to use for the token refresher sidecar.")
	flag.StringVar(&tokenRefresherImagePullPolicy, "token-refresher-image-pull-policy", "", "The image pull policy for the token refresher sidecar (e.g., Always, Never, IfNotPresent).")
	flag.BoolVar(&telemetryReport, "telemetry-report", false, "Run a one-shot telemetry report and exit.")
	flag.StringVar(&telemetryEndpoint, "telemetry-endpoint", "https://telemetry.kelos.dev/v1/report", "The endpoint to send telemetry reports to.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

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

		ctx := context.Background()
		if err := telemetry.Run(ctx, log, c, clientset, telemetryEndpoint); err != nil {
			setupLog.Error(err, "Telemetry report failed")
			os.Exit(1)
		}
		os.Exit(0)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
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
	deploymentBuilder.TokenRefresherImage = tokenRefresherImage
	deploymentBuilder.TokenRefresherImagePullPolicy = corev1.PullPolicy(tokenRefresherImagePullPolicy)
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
