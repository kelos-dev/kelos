package main

import (
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
	"github.com/gjkim42/axon/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(axonv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var claudeCodeImage string
	var claudeCodeImagePullPolicy string
	var spawnerImage string
	var spawnerImagePullPolicy string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&claudeCodeImage, "claude-code-image", controller.ClaudeCodeImage, "The image to use for Claude Code agent containers.")
	flag.StringVar(&claudeCodeImagePullPolicy, "claude-code-image-pull-policy", "", "The image pull policy for Claude Code agent containers (e.g., Always, Never, IfNotPresent).")
	flag.StringVar(&spawnerImage, "spawner-image", controller.DefaultSpawnerImage, "The image to use for spawner Deployments.")
	flag.StringVar(&spawnerImagePullPolicy, "spawner-image-pull-policy", "", "The image pull policy for spawner Deployments (e.g., Always, Never, IfNotPresent).")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "axon-controller-leader-election",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	jobBuilder := controller.NewJobBuilder()
	jobBuilder.ClaudeCodeImage = claudeCodeImage
	jobBuilder.ClaudeCodeImagePullPolicy = corev1.PullPolicy(claudeCodeImagePullPolicy)
	if err = (&controller.TaskReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		JobBuilder: jobBuilder,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Task")
		os.Exit(1)
	}

	deploymentBuilder := controller.NewDeploymentBuilder()
	deploymentBuilder.SpawnerImage = spawnerImage
	deploymentBuilder.SpawnerImagePullPolicy = corev1.PullPolicy(spawnerImagePullPolicy)
	if err = (&controller.TaskSpawnerReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		DeploymentBuilder: deploymentBuilder,
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
