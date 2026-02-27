package integration

import (
	"context"
	"encoding/json"
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

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/controller"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

var (
	cfg              *rest.Config
	k8sClient        client.Client
	testEnv          *envtest.Environment
	ctx              context.Context
	cancel           context.CancelFunc
	mockGitHubServer *httptest.Server
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.Background())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = kelosv1alpha1.AddToScheme(scheme.Scheme)
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

	// Start controller manager
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).NotTo(HaveOccurred())

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

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// Wait for the manager cache to sync before running any tests
	Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

	// Verify all CRDs are fully established by attempting to list each custom resource type
	Eventually(func() error {
		return k8sClient.List(ctx, &kelosv1alpha1.TaskList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelosv1alpha1.TaskSpawnerList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelosv1alpha1.WorkspaceList{})
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
