package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/logging"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
}

func main() {
	var namespace string
	var port int

	flag.StringVar(&namespace, "namespace", "default", "Namespace to create WebhookEvent resources in")
	flag.IntVar(&port, "port", 8080, "HTTP server port")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	logger := zap.New(zap.UseFlagOptions(opts))
	ctrl.SetLogger(logger)
	log := ctrl.Log.WithName("webhook-receiver")

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "unable to get kubeconfig")
		os.Exit(1)
	}

	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create client")
		os.Exit(1)
	}

	handler := &webhookHandler{
		client:    cl,
		namespace: namespace,
		log:       log,
	}

	http.HandleFunc("/webhook/", handler.handle)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	addr := fmt.Sprintf(":%d", port)
	log.Info("Starting webhook receiver", "address", addr, "namespace", namespace)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Error(err, "HTTP server failed")
		os.Exit(1)
	}
}

type webhookHandler struct {
	client    client.Client
	namespace string
	log       logr.Logger
}

func (h *webhookHandler) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract source from path: /webhook/{source}
	path := strings.TrimPrefix(r.URL.Path, "/webhook/")
	source := strings.Trim(path, "/")

	if source == "" {
		http.Error(w, "Source not specified in path", http.StatusBadRequest)
		return
	}

	// Read payload
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.Error(err, "Failed to read request body")
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Validate signature for GitHub webhooks
	if source == "github" {
		if err := validateGitHubSignature(r.Header, body); err != nil {
			h.log.Error(err, "GitHub signature validation failed")
			http.Error(w, "Signature validation failed", http.StatusUnauthorized)
			return
		}
	}

	// Create WebhookEvent CRD
	defaultTTL := int32(7200) // 2 hours
	event := &kelosv1alpha1.WebhookEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-webhook-", source),
			Namespace:    h.namespace,
		},
		Spec: kelosv1alpha1.WebhookEventSpec{
			Source:                   source,
			Payload:                  body,
			ReceivedAt:               metav1.Now(),
			TTLSecondsAfterProcessed: &defaultTTL,
		},
	}

	ctx := context.Background()
	if err := h.client.Create(ctx, event); err != nil {
		h.log.Error(err, "Failed to create WebhookEvent", "source", source)
		http.Error(w, "Failed to store event", http.StatusInternalServerError)
		return
	}

	h.log.Info("Webhook received and stored", "source", source, "event", event.Name)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Webhook received"))
}

// validateGitHubSignature validates the X-Hub-Signature-256 header against the payload.
// The secret is read from the GITHUB_WEBHOOK_SECRET environment variable.
func validateGitHubSignature(headers http.Header, payload []byte) error {
	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	if secret == "" {
		// If no secret is configured, skip validation (development mode)
		return nil
	}

	signature := headers.Get("X-Hub-Signature-256")
	if signature == "" {
		return fmt.Errorf("missing X-Hub-Signature-256 header")
	}

	// Compute expected signature
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedMAC := mac.Sum(nil)
	expectedSignature := "sha256=" + hex.EncodeToString(expectedMAC)

	if !hmac.Equal([]byte(signature), []byte(expectedSignature)) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}
