package integration

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/admission"
	"github.com/kelos-dev/kelos/internal/controller"
	"github.com/kelos-dev/kelos/internal/conversion"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

var (
	cfg              *rest.Config
	k8sClient        client.Client
	testEnv          *envtest.Environment
	ctx              context.Context
	cancel           context.CancelFunc
	mockGitHubServer *httptest.Server

	// Conversion webhook serving coordinates (populated by envtest) so tests
	// can point the agentconfigs CRD conversion back at the local webhook.
	webhookHost    string
	webhookPort    int
	webhookCA      []byte
	webhookCertDir string
)

// controllerSettleTimeout bounds Eventually blocks that wait for the
// controller to reconcile after CRD conversion has been enabled.
const controllerSettleTimeout = 60 * time.Second

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.Background())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "internal", "manifests"),
			// Stub cert-manager CRDs so `kelos install` can apply the
			// conversion-webhook Issuer/Certificate (a real cluster has
			// cert-manager installed; envtest does not).
			filepath.Join("testdata", "certmanager-crds.yaml"),
		},
		ErrorIfCRDPathMissing: true,
		// Rewrite admission webhooks to the locally served endpoint.
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			IgnoreSchemeConvertible: true,
			Paths: []string{
				filepath.Join("..", "..", "internal", "manifests", "charts", "kelos", "templates", "validating-webhook.yaml"),
			},
		},
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = kelos.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Set up mock GitHub token endpoint for integration tests
	mockGitHubServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_mock_installation_token",
			"expires_at": time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		})
	}))

	// Start controller manager with a webhook server backed by the certs
	// envtest generated for the conversion webhook.
	webhookOpts := testEnv.WebhookInstallOptions
	webhookHost = webhookOpts.LocalServingHost
	webhookPort = webhookOpts.LocalServingPort
	webhookCA = webhookOpts.LocalServingCAData
	webhookCertDir = webhookOpts.LocalServingCertDir
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookOpts.LocalServingHost,
			Port:    webhookOpts.LocalServingPort,
			CertDir: webhookOpts.LocalServingCertDir,
		}),
	})
	Expect(err).NotTo(HaveOccurred())

	// Register the conversion webhooks for every Kelos kind.
	for _, registration := range conversion.WebhookRegistrations() {
		Expect(ctrl.NewWebhookManagedBy(mgr, registration.Object).
			WithConverter(registration.Converter).
			Complete()).To(Succeed())
	}
	Expect(ctrl.NewWebhookManagedBy(mgr, &kelos.TaskBudget{}).
		WithValidator(&admission.TaskBudgetValidator{}).
		Complete()).To(Succeed())

	tokenClient := githubapp.NewTokenClient()
	tokenClient.BaseURL = mockGitHubServer.URL
	tokenClient.Client = mockGitHubServer.Client()

	err = (&controller.TaskReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		JobBuilder:   controller.NewJobBuilder(),
		TokenClient:  tokenClient,
		Recorder:     mgr.GetEventRecorderFor("kelos-controller"),
		BranchLocker: controller.NewBranchLocker(),
	}).SetupWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())

	err = (&controller.TaskSpawnerReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		DeploymentBuilder: controller.NewDeploymentBuilder(),
		Recorder:          mgr.GetEventRecorderFor("kelos-controller"),
	}).SetupWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())

	err = (&controller.CodexAuthRefresherReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Builder: controller.NewCodexAuthRefresherBuilder(),
	}).SetupWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())

	err = (&controller.WorkerPoolReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("kelos-controller"),
	}).SetupWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// Wait for the manager cache to sync before running any tests
	Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

	// Wait for the conversion webhook server to accept TLS connections so the
	// API server can reach /convert for AgentConfig operations.
	Eventually(func() error {
		addr := net.JoinHostPort(webhookOpts.LocalServingHost, fmt.Sprintf("%d", webhookOpts.LocalServingPort))
		conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if derr != nil {
			return derr
		}
		return conn.Close()
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())

	// Verify all CRDs are fully established by attempting to list each custom resource type
	Eventually(func() error {
		return k8sClient.List(ctx, &kelos.TaskList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelos.TaskSpawnerList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelos.WorkspaceList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelos.WorkerPoolList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	// AgentConfig requires the conversion webhook to be reachable.
	Eventually(func() error {
		return k8sClient.List(ctx, &kelos.AgentConfigList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
})

var _ = AfterSuite(func() {
	cancel()
	if mockGitHubServer != nil {
		mockGitHubServer.Close()
	}
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
