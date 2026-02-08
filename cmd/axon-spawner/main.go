package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/spawner"
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

	flag.StringVar(&name, "taskspawner-name", "", "Name of the TaskSpawner to manage")
	flag.StringVar(&namespace, "taskspawner-namespace", "", "Namespace of the TaskSpawner")
	flag.StringVar(&githubOwner, "github-owner", "", "GitHub repository owner")
	flag.StringVar(&githubRepo, "github-repo", "", "GitHub repository name")

	zapOpts := zap.Options{Development: true}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	logger := zap.New(zap.UseFlagOptions(&zapOpts))
	ctrl.SetLogger(logger)
	log := ctrl.Log.WithName("spawner")

	if name == "" || namespace == "" {
		log.Error(fmt.Errorf("--taskspawner-name and --taskspawner-namespace are required"), "Invalid flags")
		os.Exit(1)
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "Unable to get kubeconfig")
		os.Exit(1)
	}

	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Unable to create client")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	key := types.NamespacedName{Name: name, Namespace: namespace}

	log.Info("Starting spawner", "taskspawner", key)

	opts := spawner.Options{
		GitHubOwner: githubOwner,
		GitHubRepo:  githubRepo,
	}

	for {
		if err := spawner.RunCycle(ctx, cl, key, opts); err != nil {
			log.Error(err, "Discovery cycle failed")
		}

		// Re-read the TaskSpawner to get the current poll interval
		var ts axonv1alpha1.TaskSpawner
		if err := cl.Get(ctx, key, &ts); err != nil {
			log.Error(err, "Unable to fetch TaskSpawner for poll interval")
			sleepOrDone(ctx, 5*time.Minute)
			continue
		}

		interval := parsePollInterval(ts.Spec.PollInterval)
		log.Info("Sleeping until next cycle", "interval", interval)
		if done := sleepOrDone(ctx, interval); done {
			return
		}
	}
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
