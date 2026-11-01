package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	authorizationclient "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/consoleserver"
)

func main() {
	var address string
	var tokenFile string
	var defaultNamespace string
	var secureCookie bool
	var authMode string
	var proxyAuth consoleserver.ProxyAuthConfig
	var pushConfig consoleserver.WebPushConfig
	var pushPublicKeyFile, pushPrivateKeyFile string
	flag.StringVar(&address, "bind-address", ":8080", "HTTP listen address")
	flag.StringVar(&tokenFile, "token-file", "", "Path to the static authentication token")
	flag.StringVar(&authMode, "auth-mode", consoleserver.AuthModeStaticToken, "Authentication mode: staticToken, oidc or github")
	flag.StringVar(&proxyAuth.ExternalURL, "external-url", "", "HTTPS origin of the Console")
	flag.StringVar(&proxyAuth.UsernamePrefix, "username-prefix", "", "Prefix for authenticated user identities")
	flag.StringVar(&proxyAuth.GroupsPrefix, "groups-prefix", "", "Prefix for authenticated group identities")
	flag.StringVar(&defaultNamespace, "default-namespace", "default", "Initial namespace in the Console")
	flag.BoolVar(&secureCookie, "secure-cookie", false, "Mark the authentication cookie as HTTPS-only")
	flag.StringVar(&pushConfig.Namespace, "web-push-namespace", "", "Namespace for Web Push subscriptions and delivery Lease; enables Web Push")
	flag.StringVar(&pushConfig.Subject, "web-push-subject", "", "Web Push operator contact URL (mailto: or https:)")
	flag.StringVar(&pushPublicKeyFile, "web-push-public-key-file", "", "Path to the VAPID public key")
	flag.StringVar(&pushPrivateKeyFile, "web-push-private-key-file", "", "Path to the VAPID private key")
	flag.Parse()

	if err := validateAuthFlags(authMode, address, tokenFile, secureCookie, proxyAuth); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}
	var token string
	if authMode == consoleserver.AuthModeStaticToken {
		var err error
		token, err = readToken(tokenFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid authentication token: %v\n", err)
			os.Exit(1)
		}
	}

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load in-cluster configuration: %v\n", err)
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	controllerClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Kubernetes client: %v\n", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Kubernetes clientset: %v\n", err)
		os.Exit(1)
	}
	config := consoleserver.Config{
		AuthMode:         authMode,
		Token:            token,
		Client:           controllerClient,
		Clientset:        clientset,
		RESTConfig:       restConfig,
		HTTPClient:       http.DefaultClient,
		DefaultNamespace: defaultNamespace,
		SecureCookie:     secureCookie,
	}
	if pushConfig.Namespace != "" || pushConfig.Subject != "" || pushPublicKeyFile != "" || pushPrivateKeyFile != "" {
		publicKey, publicErr := readToken(pushPublicKeyFile)
		privateKey, privateErr := readToken(pushPrivateKeyFile)
		if publicErr != nil || privateErr != nil {
			fmt.Fprintf(os.Stderr, "Invalid Web Push configuration: public key: %v; private key: %v\n", publicErr, privateErr)
			os.Exit(1)
		}
		pushConfig.PublicKey, pushConfig.PrivateKey = publicKey, privateKey
		pushConfig.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
		config.WebPush = &pushConfig
	}
	if authMode == consoleserver.AuthModeOIDC || authMode == consoleserver.AuthModeGitHub {
		authorizationClient, err := newAuthorizationClient(restConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create authorization client: %v\n", err)
			os.Exit(1)
		}
		proxyAuth.Reviewer = authorizationClient.SubjectAccessReviews()
		proxyAuth.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
		config.ProxyAuth = &proxyAuth
	}
	handler, err := consoleserver.New(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid server configuration: %v\n", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	workerDone := make(chan error, 1)
	if config.WebPush != nil {
		go func() {
			err := handler.RunWebPush(ctx)
			workerDone <- err
			if err != nil {
				pushConfig.Logger.Error("Web Push worker stopped", "error", err)
				cancel()
			}
		}()
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Printf("Kelos Console server listening address=%s\n", address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Console server failed: %v\n", err)
		os.Exit(1)
	}
	<-shutdownDone
	if config.WebPush != nil {
		select {
		case err := <-workerDone:
			if err != nil {
				os.Exit(1)
			}
		case <-time.After(10 * time.Second):
			fmt.Fprintln(os.Stderr, "Web Push worker did not stop before the shutdown deadline")
			os.Exit(1)
		}
	}
}

func newAuthorizationClient(config *rest.Config) (*authorizationclient.AuthorizationV1Client, error) {
	config = rest.CopyConfig(config)
	// Namespace discovery can require ten authorization requests per namespace.
	config.QPS = 50
	config.Burst = 100
	return authorizationclient.NewForConfig(config)
}

func validateAuthFlags(mode, address, tokenFile string, secureCookie bool, proxyAuth consoleserver.ProxyAuthConfig) error {
	switch mode {
	case consoleserver.AuthModeStaticToken:
		if tokenFile == "" {
			return fmt.Errorf("--token-file is required")
		}
		if proxyAuth.ExternalURL != "" || proxyAuth.UsernamePrefix != "" || proxyAuth.GroupsPrefix != "" {
			return fmt.Errorf("proxy flags require --auth-mode=oidc or github")
		}
	case consoleserver.AuthModeOIDC, consoleserver.AuthModeGitHub:
		if tokenFile != "" || secureCookie {
			return fmt.Errorf("static token flags cannot be used with proxy authentication")
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("proxy authentication requires a loopback IP in --bind-address")
		}
	default:
		return fmt.Errorf("unsupported authentication mode %q", mode)
	}
	return nil
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return value, nil
}
