package install

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/cli"
	"github.com/kelos-dev/kelos/internal/codexauth"
	"github.com/kelos-dev/kelos/internal/controller"
)

func clusterRoleHasVerbs(clusterRole *rbacv1.ClusterRole, apiGroup, resource string, verbs ...string) bool {
	for _, rule := range clusterRole.Rules {
		if !containsString(rule.APIGroups, apiGroup) || !containsString(rule.Resources, resource) {
			continue
		}
		matches := true
		for _, verb := range verbs {
			if !containsString(rule.Verbs, verb) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

var _ = Describe("Install/Uninstall", Ordered, func() {
	var kubeconfigPath string

	BeforeEach(func() {
		kubeconfigPath = writeEnvtestKubeconfig()
	})

	Context("kelos install", func() {
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
			Expect(clusterRoleHasVerbs(cr, "", "secrets", "get", "list", "watch", "update")).To(BeTrue())
			Expect(clusterRoleHasVerbs(cr, "batch", "cronjobs", "get", "list", "watch", "create", "update", "patch", "delete")).To(BeTrue())

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

			By("Verifying no static Codex auth refresher CronJob is rendered")
			codexRefreshCronJob := &batchv1.CronJob{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      "kelos-codex-auth-refresher",
				Namespace: "kelos-system",
			}, codexRefreshCronJob)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("Verifying the controller receives the default refresher schedule")
			args := dep.Spec.Template.Spec.Containers[0].Args
			Expect(args).To(ContainElement("--codex-auth-refresher-schedule=0 */6 * * *"))
		})

		It("Should wire Codex auth refresher controller schedule override", func() {
			valuesPath := filepath.Join(GinkgoT().TempDir(), "values.yaml")
			values := `codexAuthRefresher:
  schedule: "*/15 * * * *"
`
			Expect(os.WriteFile(valuesPath, []byte(values), 0o644)).To(Succeed())

			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"install",
				"--kubeconfig", kubeconfigPath,
				"--values", valuesPath,
			})
			Expect(root.Execute()).To(Succeed())

			By("Verifying the static Codex auth refresher CronJob is not rendered")
			cronJob := &batchv1.CronJob{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      "kelos-codex-auth-refresher",
				Namespace: "kelos-system",
			}, cronJob)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("Verifying the controller receives the refresher flags")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "kelos-controller-manager",
				Namespace: "kelos-system",
			}, dep)).To(Succeed())
			args := dep.Spec.Template.Spec.Containers[0].Args
			Expect(args).To(ContainElement("--codex-auth-refresher-schedule=*/15 * * * *"))
		})

		It("Should create Codex auth refresher CronJob for a labeled Secret", func() {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kelos-system"}}
			err := k8sClient.Create(ctx, namespace)
			Expect(client.IgnoreAlreadyExists(err)).To(Succeed())

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "codex-oauth",
					Labels: map[string]string{
						codexauth.RefreshLabel: "true",
					},
				},
				Data: map[string][]byte{
					"CODEX_AUTH_JSON": []byte(`{"tokens":{"refresh_token":"refresh"}}`),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("Verifying the per-Secret Codex auth refresher CronJob exists")
			cronJob := &batchv1.CronJob{}
			key := types.NamespacedName{
				Name:      controller.CodexAuthRefresherCronJobName("default", "codex-oauth"),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, key, cronJob)
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			Expect(cronJob.Spec.Schedule).To(Equal(controller.DefaultCodexAuthRefreshSchedule))
			Expect(cronJob.Spec.ConcurrencyPolicy).To(Equal(batchv1.ForbidConcurrent))
			Expect(cronJob.Spec.JobTemplate.Spec.ActiveDeadlineSeconds).NotTo(BeNil())
			Expect(*cronJob.Spec.JobTemplate.Spec.ActiveDeadlineSeconds).To(Equal(int64(600)))

			podSpec := cronJob.Spec.JobTemplate.Spec.Template.Spec
			Expect(podSpec.ServiceAccountName).To(Equal(key.Name))
			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      podSpec.ServiceAccountName,
				Namespace: cronJob.Namespace,
			}, serviceAccount)).To(Succeed())
			role := &rbacv1.Role{}
			Expect(k8sClient.Get(ctx, key, role)).To(Succeed())
			Expect(role.Rules).To(HaveLen(1))
			Expect(role.Rules[0].Resources).To(Equal([]string{"secrets"}))
			Expect(role.Rules[0].ResourceNames).To(Equal([]string{"codex-oauth"}))
			Expect(role.Rules[0].Verbs).To(Equal([]string{"get", "update"}))
			roleBinding := &rbacv1.RoleBinding{}
			Expect(k8sClient.Get(ctx, key, roleBinding)).To(Succeed())
			Expect(roleBinding.RoleRef.Kind).To(Equal("Role"))
			Expect(roleBinding.RoleRef.Name).To(Equal(role.Name))
			Expect(roleBinding.Subjects).To(HaveLen(1))
			Expect(roleBinding.Subjects[0].Name).To(Equal(podSpec.ServiceAccountName))
			Expect(roleBinding.Subjects[0].Namespace).To(Equal(cronJob.Namespace))
			Expect(podSpec.RestartPolicy).To(Equal(corev1.RestartPolicyOnFailure))
			Expect(podSpec.SecurityContext).NotTo(BeNil())
			Expect(podSpec.SecurityContext.RunAsNonRoot).NotTo(BeNil())
			Expect(*podSpec.SecurityContext.RunAsNonRoot).To(BeTrue())
			Expect(podSpec.Containers).To(HaveLen(1))

			container := podSpec.Containers[0]
			Expect(container.Name).To(Equal("codex-auth-refresher"))
			Expect(container.Image).To(Equal("ghcr.io/kelos-dev/codex:latest"))
			Expect(container.Command).To(Equal([]string{"/kelos/kelos-codex-auth-refresh"}))
			Expect(container.Args).To(Equal([]string{"--namespace=default", "--secret=codex-oauth"}))
			Expect(container.SecurityContext).NotTo(BeNil())
			Expect(container.SecurityContext.AllowPrivilegeEscalation).NotTo(BeNil())
			Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
		})

		It("Should clean up stale Codex auth refresher CronJob when source Secret is missing", func() {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kelos-system"}}
			err := k8sClient.Create(ctx, namespace)
			Expect(client.IgnoreAlreadyExists(err)).To(Succeed())

			sourceSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "deleted-codex-oauth",
				},
			}
			cronJob := controller.NewCodexAuthRefresherBuilder().Build(sourceSecret)
			Expect(k8sClient.Create(ctx, cronJob)).To(Succeed())

			By("Verifying the controller deletes the stale managed CronJob")
			key := types.NamespacedName{
				Name:      cronJob.Name,
				Namespace: cronJob.Namespace,
			}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, key, &batchv1.CronJob{})
				return apierrors.IsNotFound(err)
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
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
