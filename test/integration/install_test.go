package integration

import (
	"os"
	"path/filepath"
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

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/cli"
)

// clearNamespaceFinalizers removes finalizers from the kelos-system namespace
// so it can be deleted in envtest (which has no namespace controller).
func clearNamespaceFinalizers() {
	ns := &corev1.Namespace{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-system"}, ns)
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
// without touching the CRDs, keeping the envtest environment intact. Includes
// optional cluster-scoped webhook RBAC so tests that enable webhook sources
// start from a clean slate and cannot satisfy assertions against stale state
// left by a previous test.
func deleteControllerResources() {
	for _, obj := range []client.Object{
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "kelos-controller-rolebinding"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "kelos-controller-role"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "kelos-spawner-role"}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "kelos-webhook-rolebinding"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "kelos-webhook-role"}},
	} {
		_ = client.IgnoreNotFound(k8sClient.Delete(ctx, obj))
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kelos-system"}}
	_ = client.IgnoreNotFound(k8sClient.Delete(ctx, ns))

	// Clear finalizers so the namespace can be deleted in envtest.
	clearNamespaceFinalizers()

	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-system"}, &corev1.Namespace{})
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
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-system"}, &corev1.Namespace{})
		return apierrors.IsNotFound(err)
	}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())

	// Wait for all CRDs to be fully deleted before reinstalling. If install's
	// server-side apply patches a CRD that still has a deletionTimestamp, the
	// patch succeeds but the CRD is still deleted, leaving the API unavailable.
	crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
	for _, name := range []string{"tasks.kelos.dev", "taskspawners.kelos.dev", "workspaces.kelos.dev"} {
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
		return k8sClient.List(ctx, &kelosv1alpha1.TaskList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelosv1alpha1.TaskSpawnerList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelosv1alpha1.WorkspaceList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())

	deleteControllerResources()
}

var _ = Describe("Install/Uninstall", Ordered, func() {
	var kubeconfigPath string

	BeforeEach(func() {
		kubeconfigPath = writeEnvtestKubeconfig()
	})

	Context("kelos install", func() {
		AfterEach(func() {
			deleteControllerResources()
		})

		It("Should create kelos-system namespace and controller resources", func() {
			root := cli.NewRootCommand()
			root.SetArgs([]string{"install", "--kubeconfig", kubeconfigPath})
			Expect(root.Execute()).To(Succeed())

			By("Verifying the kelos-system namespace exists")
			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-system"}, ns)).To(Succeed())

			By("Verifying the controller ServiceAccount exists")
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "kelos-controller",
				Namespace: "kelos-system",
			}, sa)).To(Succeed())

			By("Verifying the ClusterRole exists")
			cr := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "kelos-controller-role",
			}, cr)).To(Succeed())

			By("Verifying the ClusterRoleBinding exists")
			crb := &rbacv1.ClusterRoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "kelos-controller-rolebinding",
			}, crb)).To(Succeed())

			By("Verifying the Deployment exists")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "kelos-controller-manager",
				Namespace: "kelos-system",
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

		It("Should apply Helm values file overrides", func() {
			valuesPath := filepath.Join(GinkgoT().TempDir(), "values.yaml")
			values := `webhookServer:
  sources:
    github:
      enabled: true
      secretName: github-webhook-secret
`
			Expect(os.WriteFile(valuesPath, []byte(values), 0o644)).To(Succeed())

			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"install",
				"--kubeconfig", kubeconfigPath,
				"--values", valuesPath,
			})
			Expect(root.Execute()).To(Succeed())

			webhookDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "kelos-webhook-github",
				Namespace: "kelos-system",
			}, webhookDep)).To(Succeed())

			env := webhookDep.Spec.Template.Spec.Containers[0].Env
			Expect(env).NotTo(BeEmpty())
			Expect(env[0].ValueFrom).NotTo(BeNil())
			Expect(env[0].ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(env[0].ValueFrom.SecretKeyRef.Name).To(Equal("github-webhook-secret"))
		})

		It("Should support Linear-only webhook installs", func() {
			valuesPath := filepath.Join(GinkgoT().TempDir(), "values.yaml")
			values := `webhookServer:
  sources:
    linear:
      enabled: true
      secretName: linear-webhook-secret
`
			Expect(os.WriteFile(valuesPath, []byte(values), 0o644)).To(Succeed())

			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"install",
				"--kubeconfig", kubeconfigPath,
				"--values", valuesPath,
			})
			Expect(root.Execute()).To(Succeed())

			webhookDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "kelos-webhook-linear",
				Namespace: "kelos-system",
			}, webhookDep)).To(Succeed())
			Expect(webhookDep.Spec.Template.Spec.ServiceAccountName).To(Equal("kelos-webhook"))

			webhookSA := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "kelos-webhook",
				Namespace: "kelos-system",
			}, webhookSA)).To(Succeed())

			webhookCRB := &rbacv1.ClusterRoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "kelos-webhook-rolebinding",
			}, webhookCRB)).To(Succeed())
		})
	})

	Context("kelos uninstall", func() {
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
				Name:      "kelos-controller-manager",
				Namespace: "kelos-system",
			}, dep)
			Expect(client.IgnoreNotFound(err)).To(Succeed())
			if err == nil {
				Fail("expected Deployment to be deleted")
			}

			By("Verifying the ClusterRole is gone")
			cr := &rbacv1.ClusterRole{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: "kelos-controller-role",
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

		It("Should clean up custom resources with finalizers before removing controller", func() {
			By("Installing first")
			root := cli.NewRootCommand()
			root.SetArgs([]string{"install", "--kubeconfig", kubeconfigPath})
			Expect(root.Execute()).To(Succeed())

			By("Creating a Task with required fields")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-uninstall-task",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "test prompt",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: &kelosv1alpha1.SecretReference{Name: "fake-secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).To(Succeed())

			By("Waiting for the controller to add the finalizer")
			Eventually(func() bool {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: "test-uninstall-task", Namespace: "default",
				}, &t); err != nil {
					return false
				}
				for _, f := range t.Finalizers {
					if f == "kelos.dev/finalizer" {
						return true
					}
				}
				return false
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())

			By("Uninstalling")
			root2 := cli.NewRootCommand()
			root2.SetArgs([]string{"uninstall", "--kubeconfig", kubeconfigPath})
			Expect(root2.Execute()).To(Succeed())

			By("Verifying custom resources are gone")
			Eventually(func() bool {
				var taskList kelosv1alpha1.TaskList
				err := k8sClient.List(ctx, &taskList)
				// After CRDs are deleted, listing will fail
				return err != nil || len(taskList.Items) == 0
			}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("Should remove optional webhook RBAC", func() {
			valuesPath := filepath.Join(GinkgoT().TempDir(), "values.yaml")
			values := `webhookServer:
  sources:
    github:
      enabled: true
      secretName: github-webhook-secret
`
			Expect(os.WriteFile(valuesPath, []byte(values), 0o644)).To(Succeed())

			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"install",
				"--kubeconfig", kubeconfigPath,
				"--values", valuesPath,
			})
			Expect(root.Execute()).To(Succeed())

			webhookCR := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "kelos-webhook-role",
			}, webhookCR)).To(Succeed())

			root2 := cli.NewRootCommand()
			root2.SetArgs([]string{"uninstall", "--kubeconfig", kubeconfigPath})
			Expect(root2.Execute()).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-webhook-role"}, &rbacv1.ClusterRole{})
				return apierrors.IsNotFound(err)
			}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-webhook-rolebinding"}, &rbacv1.ClusterRoleBinding{})
				return apierrors.IsNotFound(err)
			}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("Should remove optional webhook RBAC for Linear-only installs", func() {
			valuesPath := filepath.Join(GinkgoT().TempDir(), "values.yaml")
			values := `webhookServer:
  sources:
    linear:
      enabled: true
      secretName: linear-webhook-secret
`
			Expect(os.WriteFile(valuesPath, []byte(values), 0o644)).To(Succeed())

			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"install",
				"--kubeconfig", kubeconfigPath,
				"--values", valuesPath,
			})
			Expect(root.Execute()).To(Succeed())

			webhookCR := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "kelos-webhook-role",
			}, webhookCR)).To(Succeed())

			root2 := cli.NewRootCommand()
			root2.SetArgs([]string{"uninstall", "--kubeconfig", kubeconfigPath})
			Expect(root2.Execute()).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-webhook-role"}, &rbacv1.ClusterRole{})
				return apierrors.IsNotFound(err)
			}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-webhook-rolebinding"}, &rbacv1.ClusterRoleBinding{})
				return apierrors.IsNotFound(err)
			}, 30*time.Second, 100*time.Millisecond).Should(BeTrue())
		})
	})
})
