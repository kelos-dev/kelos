package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/cli"
)

// clearNamespaceFinalizers removes finalizers from the axon-system namespace
// so it can be deleted in envtest (which has no namespace controller).
func clearNamespaceFinalizers() {
	ns := &corev1.Namespace{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "axon-system"}, ns)
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())
	if len(ns.Spec.Finalizers) > 0 {
		ns.Spec.Finalizers = nil
		Expect(k8sClient.SubResource("finalize").Update(ctx, ns)).To(Succeed())
	}
}

// deleteControllerResources removes the non-CRD resources created by install
// without touching the CRDs, keeping the envtest environment intact.
func deleteControllerResources() {
	for _, obj := range []client.Object{
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "axon-controller-rolebinding"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "axon-controller-role"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "axon-spawner-role"}},
	} {
		_ = client.IgnoreNotFound(k8sClient.Delete(ctx, obj))
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "axon-system"}}
	_ = client.IgnoreNotFound(k8sClient.Delete(ctx, ns))

	// Clear finalizers so the namespace can be deleted in envtest.
	clearNamespaceFinalizers()

	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "axon-system"}, &corev1.Namespace{})
		return apierrors.IsNotFound(err)
	}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())
}

// restoreCRDs re-applies CRDs by running install followed by cleanup of
// non-CRD resources. This restores the envtest environment after uninstall
// removes CRDs that were originally loaded by the BeforeSuite.
func restoreCRDs(kubeconfigPath string) {
	// Wait for namespace termination to complete before re-installing.
	clearNamespaceFinalizers()
	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "axon-system"}, &corev1.Namespace{})
		return apierrors.IsNotFound(err)
	}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())

	// Wait for all CRDs to be fully deleted before reinstalling. If install's
	// server-side apply patches a CRD that still has a deletionTimestamp, the
	// patch succeeds but the CRD is still deleted, leaving the API unavailable.
	crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
	for _, name := range []string{"tasks.axon.io", "taskspawners.axon.io", "workspaces.axon.io"} {
		Eventually(func() bool {
			crd := &unstructured.Unstructured{}
			crd.SetGroupVersionKind(crdGVK)
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, crd)
			return apierrors.IsNotFound(err)
		}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())
	}

	reinstall := cli.NewRootCommand()
	reinstall.SetArgs([]string{"install", "--kubeconfig", kubeconfigPath})
	Expect(reinstall.Execute()).To(Succeed())

	// Wait for all CRDs to be fully established before subsequent tests
	// can create custom resources. We verify by attempting to list each type.
	Eventually(func() error {
		return k8sClient.List(ctx, &axonv1alpha1.TaskList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &axonv1alpha1.TaskSpawnerList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &axonv1alpha1.WorkspaceList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())

	deleteControllerResources()
}

var _ = Describe("Install/Uninstall", Ordered, func() {
	var kubeconfigPath string

	BeforeEach(func() {
		kubeconfigPath = writeEnvtestKubeconfig()
	})

	Context("axon install", func() {
		AfterEach(func() {
			deleteControllerResources()
		})

		It("Should create axon-system namespace and controller resources", func() {
			root := cli.NewRootCommand()
			root.SetArgs([]string{"install", "--kubeconfig", kubeconfigPath})
			Expect(root.Execute()).To(Succeed())

			By("Verifying the axon-system namespace exists")
			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "axon-system"}, ns)).To(Succeed())

			By("Verifying the controller ServiceAccount exists")
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "axon-controller",
				Namespace: "axon-system",
			}, sa)).To(Succeed())

			By("Verifying the ClusterRole exists")
			cr := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "axon-controller-role",
			}, cr)).To(Succeed())

			By("Verifying the ClusterRoleBinding exists")
			crb := &rbacv1.ClusterRoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "axon-controller-rolebinding",
			}, crb)).To(Succeed())

			By("Verifying the Deployment exists")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "axon-controller-manager",
				Namespace: "axon-system",
			}, dep)).To(Succeed())
		})

		It("Should be idempotent", func() {
			root := cli.NewRootCommand()
			root.SetArgs([]string{"install", "--kubeconfig", kubeconfigPath})
			Expect(root.Execute()).To(Succeed())

			root2 := cli.NewRootCommand()
			root2.SetArgs([]string{"install", "--kubeconfig", kubeconfigPath})
			Expect(root2.Execute()).To(Succeed())
		})

		It("Should skip ClusterRole and ClusterRoleBinding with --skip-rbac", func() {
			root := cli.NewRootCommand()
			root.SetArgs([]string{"install", "--kubeconfig", kubeconfigPath, "--skip-rbac"})
			Expect(root.Execute()).To(Succeed())

			By("Verifying the axon-system namespace exists")
			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "axon-system"}, ns)).To(Succeed())

			By("Verifying the controller ServiceAccount exists")
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "axon-controller",
				Namespace: "axon-system",
			}, sa)).To(Succeed())

			By("Verifying the ClusterRole axon-controller-role was NOT created")
			cr := &rbacv1.ClusterRole{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: "axon-controller-role",
			}, cr)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected ClusterRole axon-controller-role to not exist")

			By("Verifying the ClusterRole axon-spawner-role was NOT created")
			spawnerRole := &rbacv1.ClusterRole{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: "axon-spawner-role",
			}, spawnerRole)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected ClusterRole axon-spawner-role to not exist")

			By("Verifying the ClusterRoleBinding was NOT created")
			crb := &rbacv1.ClusterRoleBinding{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: "axon-controller-rolebinding",
			}, crb)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected ClusterRoleBinding to not exist")

			By("Verifying the Deployment exists")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "axon-controller-manager",
				Namespace: "axon-system",
			}, dep)).To(Succeed())
		})
	})

	Context("axon uninstall", func() {
		AfterEach(func() {
			restoreCRDs(kubeconfigPath)
		})

		It("Should remove controller resources", func() {
			By("Installing first")
			root := cli.NewRootCommand()
			root.SetArgs([]string{"install", "--kubeconfig", kubeconfigPath})
			Expect(root.Execute()).To(Succeed())

			By("Uninstalling")
			root2 := cli.NewRootCommand()
			root2.SetArgs([]string{"uninstall", "--kubeconfig", kubeconfigPath})
			Expect(root2.Execute()).To(Succeed())

			By("Verifying the Deployment is gone")
			dep := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      "axon-controller-manager",
				Namespace: "axon-system",
			}, dep)
			Expect(client.IgnoreNotFound(err)).To(Succeed())
			if err == nil {
				Fail("expected Deployment to be deleted")
			}

			By("Verifying the ClusterRole is gone")
			cr := &rbacv1.ClusterRole{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: "axon-controller-role",
			}, cr)
			Expect(client.IgnoreNotFound(err)).To(Succeed())
			if err == nil {
				Fail("expected ClusterRole to be deleted")
			}
		})

		It("Should be idempotent", func() {
			root := cli.NewRootCommand()
			root.SetArgs([]string{"uninstall", "--kubeconfig", kubeconfigPath})
			Expect(root.Execute()).To(Succeed())
		})
	})
})
