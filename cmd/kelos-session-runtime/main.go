package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
	clientset "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned"
	clientv1alpha2 "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/typed/api/v1alpha2"
)

func selfCopy(destination string) error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--self-copy" {
		if err := selfCopy(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Self-copy failed: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: kelos-session-runtime <serve|health|client>")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		runServe()
	case "health":
		runHealth()
	case "client":
		runClient()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func runServe() {
	agentType := os.Getenv("KELOS_AGENT_TYPE")
	if agentType == "" {
		fmt.Fprintln(os.Stderr, "Invalid configuration: KELOS_AGENT_TYPE must be set")
		os.Exit(1)
	}
	sessionClient, sessionName, podUID, err := sessionClientFromEnvironment()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}
	publisher, err := sessionruntime.NewSessionStatusPublisher(sessionClient, sessionName, podUID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}
	config := sessionruntime.Config{
		SocketPath:           envOrDefault("KELOS_SESSION_SOCKET", sessionruntime.DefaultSocketPath),
		StateDir:             envOrDefault("KELOS_SESSION_STATE_DIR", sessionruntime.DefaultStateDir),
		WorkingDir:           envOrDefault("KELOS_SESSION_WORKING_DIR", sessionruntime.DefaultWorkingDir),
		AgentType:            agentType,
		Model:                os.Getenv("KELOS_MODEL"),
		Effort:               os.Getenv("KELOS_EFFORT"),
		PluginDir:            os.Getenv("KELOS_PLUGIN_DIR"),
		Environment:          os.Environ(),
		PublishSessionStatus: publisher,
		SessionName:          sessionName,
		PodUID:               podUID,
		SessionClient:        sessionClient,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := sessionruntime.Run(ctx, config); err != nil {
		fmt.Fprintf(os.Stderr, "Session runtime failed: %v\n", err)
		os.Exit(1)
	}
}

func sessionClientFromEnvironment() (clientv1alpha2.SessionInterface, string, types.UID, error) {
	sessionName := os.Getenv("KELOS_SESSION_NAME")
	namespace := os.Getenv("KELOS_SESSION_NAMESPACE")
	podUID := types.UID(os.Getenv("KELOS_SESSION_POD_UID"))
	if sessionName == "" || namespace == "" || podUID == "" {
		return nil, "", "", fmt.Errorf("KELOS_SESSION_NAME, KELOS_SESSION_NAMESPACE, and KELOS_SESSION_POD_UID must be set")
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, "", "", fmt.Errorf("loading in-cluster Kubernetes configuration: %w", err)
	}
	client, err := clientset.NewForConfig(config)
	if err != nil {
		return nil, "", "", fmt.Errorf("creating Kubernetes client: %w", err)
	}
	return client.ApiV1alpha2().Sessions(namespace), sessionName, podUID, nil
}

func runHealth() {
	flags := flag.NewFlagSet("health", flag.ExitOnError)
	socket := flags.String("socket", envOrDefault("KELOS_SESSION_SOCKET", sessionruntime.DefaultSocketPath), "Session runtime socket")
	_ = flags.Parse(os.Args[2:])
	if err := sessionruntime.Health(*socket); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runClient() {
	flags := flag.NewFlagSet("client", flag.ExitOnError)
	socket := flags.String("socket", envOrDefault("KELOS_SESSION_SOCKET", sessionruntime.DefaultSocketPath), "Session runtime socket")
	_ = flags.Parse(os.Args[2:])
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	err := sessionruntime.RunJSONClient(ctx, *socket)
	if err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "Session client failed: %v\n", err)
		os.Exit(1)
	}
}
