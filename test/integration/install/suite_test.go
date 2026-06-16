package install

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
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
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
	managerDone      chan error
	mockGitHubServer *httptest.Server

	webhookHost    string
	webhookPort    int
	webhookCA      []byte
	webhookCertDir string
)

func TestInstallIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Install Integration Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
})

var _ = BeforeEach(func() {
	// Uninstall deletes CRDs, so these specs get a fresh envtest manager
	// instead of restoring CRDs under an already-running manager.
	ctx, cancel = context.WithCancel(context.Background())

	By("bootstrapping install test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "internal", "manifests"),
			filepath.Join("..", "testdata", "certmanager-crds.yaml"),
		},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			IgnoreSchemeConvertible: true,
		},
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = kelos.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = discoveryv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	mockGitHubServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_mock_installation_token",
			"expires_at": time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		})
	}))

	// Controller names remain registered in process-wide metrics after each
	// manager stops, while this suite starts a new manager for every spec.
	skipNameValidation := true
	webhookOpts := testEnv.WebhookInstallOptions
	webhookHost = webhookOpts.LocalServingHost
	webhookPort = webhookOpts.LocalServingPort
	webhookCA = webhookOpts.LocalServingCAData
	webhookCertDir = webhookOpts.LocalServingCertDir
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: &skipNameValidation},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookOpts.LocalServingHost,
			Port:    webhookOpts.LocalServingPort,
			CertDir: webhookOpts.LocalServingCertDir,
		}),
	})
	Expect(err).NotTo(HaveOccurred())

	for _, registration := range conversion.WebhookRegistrations() {
		Expect(ctrl.NewWebhookManagedBy(mgr, registration.Object).
			WithConverter(registration.Converter).
			Complete()).To(Succeed())
	}

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

	managerDone = make(chan error, 1)
	go func() {
		defer GinkgoRecover()
		managerDone <- mgr.Start(ctx)
	}()

	Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

	Eventually(func() error {
		addr := net.JoinHostPort(webhookOpts.LocalServingHost, fmt.Sprintf("%d", webhookOpts.LocalServingPort))
		conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if derr != nil {
			return derr
		}
		return conn.Close()
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())

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
		return k8sClient.List(ctx, &kelos.AgentConfigList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
})

var _ = AfterEach(func() {
	if cancel != nil {
		cancel()
		cancel = nil
	}
	if managerDone != nil {
		Eventually(managerDone, 10*time.Second).Should(Receive(Succeed()))
		managerDone = nil
	}
	if mockGitHubServer != nil {
		mockGitHubServer.Close()
		mockGitHubServer = nil
	}
	if testEnv != nil {
		By("tearing down install test environment")
		Expect(testEnv.Stop()).To(Succeed())
		testEnv = nil
	}
})

func writeEnvtestKubeconfig() string {
	kubeconfig := clientcmdapi.NewConfig()
	kubeconfig.Clusters["envtest"] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
	}
	kubeconfig.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
	}
	kubeconfig.Contexts["envtest"] = &clientcmdapi.Context{
		Cluster:  "envtest",
		AuthInfo: "envtest",
	}
	kubeconfig.CurrentContext = "envtest"

	tmpFile := filepath.Join(GinkgoT().TempDir(), "kubeconfig")
	Expect(clientcmd.WriteToFile(*kubeconfig, tmpFile)).To(Succeed())
	return tmpFile
}
