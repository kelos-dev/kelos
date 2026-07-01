package install

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/cli"
	"github.com/kelos-dev/kelos/internal/codexauth"
	"github.com/kelos-dev/kelos/internal/controller"
)

// controllerSettleTimeout bounds Eventually blocks that wait for the in-process
// controller to reconcile after install re-applies the CRDs and re-points their
// conversion config. The informers re-establish their watches, so reconciles
// need more headroom than a stable resource would.
const controllerSettleTimeout = 60 * time.Second

// kelosCRDNames are the kelos CRDs that serve two versions with a conversion
// webhook.
var kelosCRDNames = []string{
	"agentconfigs.kelos.dev",
	"tasks.kelos.dev",
	"taskspawners.kelos.dev",
	"workspaces.kelos.dev",
}

// pointConversionToEnvtest rewrites every kelos CRD conversion to the local
// webhook envtest serves, so conversion works regardless of any service-based
// config left by install.
func pointConversionToEnvtest() {
	crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
	url := fmt.Sprintf("https://%s/convert", net.JoinHostPort(webhookHost, fmt.Sprintf("%d", webhookPort)))
	conversion := map[string]interface{}{
		"strategy": "Webhook",
		"webhook": map[string]interface{}{
			"conversionReviewVersions": []interface{}{"v1"},
			"clientConfig": map[string]interface{}{
				"url":      url,
				"caBundle": base64.StdEncoding.EncodeToString(webhookCA),
			},
		},
	}
	for _, name := range kelosCRDNames {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(crdGVK)
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
			continue
		}
		Expect(unstructured.SetNestedMap(crd.Object, conversion, "spec", "conversion")).To(Succeed())
		_ = k8sClient.Update(ctx, crd)
	}
}

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
	secrets := &corev1.SecretList{}
	_ = k8sClient.List(ctx, secrets, client.MatchingLabels{codexauth.RefreshLabel: "true"})
	for i := range secrets.Items {
		_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &secrets.Items[i]))
	}

	selector := client.MatchingLabels{
		"app.kubernetes.io/component": "codex-auth-refresher",
		"kelos.dev/managed-by":        "kelos-controller",
	}
	cronJobs := &batchv1.CronJobList{}
	_ = k8sClient.List(ctx, cronJobs, selector)
	for i := range cronJobs.Items {
		_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &cronJobs.Items[i]))
	}
	roleBindings := &rbacv1.RoleBindingList{}
	_ = k8sClient.List(ctx, roleBindings, selector)
	for i := range roleBindings.Items {
		_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &roleBindings.Items[i]))
	}
	roles := &rbacv1.RoleList{}
	_ = k8sClient.List(ctx, roles, selector)
	for i := range roles.Items {
		_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &roles.Items[i]))
	}
	serviceAccounts := &corev1.ServiceAccountList{}
	_ = k8sClient.List(ctx, serviceAccounts, selector)
	for i := range serviceAccounts.Items {
		_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &serviceAccounts.Items[i]))
	}

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

func ensureCertManagerReadyFixture() {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager"}}
	err := k8sClient.Create(ctx, ns)
	Expect(client.IgnoreAlreadyExists(err)).To(Succeed())

	for _, name := range []string{"cert-manager", "cert-manager-webhook", "cert-manager-cainjector"} {
		labels := map[string]string{"app.kubernetes.io/name": name}
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "cert-manager",
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "placeholder", Image: "example.invalid/cert-manager"}},
					},
				},
			},
		}
		err := k8sClient.Create(ctx, deploy)
		Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "cert-manager"}, deploy)).To(Succeed())
		deploy.Status.Conditions = []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
		}
		Expect(k8sClient.Status().Update(ctx, deploy)).To(Succeed())
	}
}

func startKelosWebhookReadinessFixture() {
	ensureKelosSystemNamespaceFixture()
	ensureKelosWebhookFixtureState()

	fixtureCtx, stop := context.WithCancel(ctx)
	DeferCleanup(stop)

	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-fixtureCtx.Done():
				return
			case <-ticker.C:
				ensureKelosWebhookFixtureState()
			}
		}
	}()
}

func ensureKelosSystemNamespaceFixture() {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kelos-system"}}
	err := k8sClient.Create(ctx, ns)
	Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
}

func ensureKelosWebhookFixtureState() {
	ensureKelosWebhookCertificateSecret()
	ensureKelosControllerDeploymentAvailable()
	ensureKelosWebhookEndpointSlices()
	injectKelosCRDConversionCABundles()
}

func ensureKelosWebhookCertificateSecret() {
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-system"}, &corev1.Namespace{})
	if err != nil {
		return
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kelos-webhook-server-cert",
			Namespace: "kelos-system",
		},
		Type: corev1.SecretTypeTLS,
		Data: webhookFixtureTLSData(),
	}
	err = k8sClient.Create(ctx, secret)
	if client.IgnoreAlreadyExists(err) != nil {
		return
	}
}

func webhookFixtureTLSData() map[string][]byte {
	cert, certErr := os.ReadFile(filepath.Join(webhookCertDir, "tls.crt"))
	key, keyErr := os.ReadFile(filepath.Join(webhookCertDir, "tls.key"))
	if certErr == nil && keyErr == nil {
		return map[string][]byte{
			"tls.crt": cert,
			"tls.key": key,
			"ca.crt":  webhookFixtureCAData(),
		}
	}
	return map[string][]byte{
		"tls.crt": []byte("test-cert"),
		"tls.key": []byte("test-key"),
		"ca.crt":  webhookFixtureCAData(),
	}
}

func webhookFixtureCAData() []byte {
	if len(webhookCA) > 0 {
		return webhookCA
	}
	return []byte("test-ca")
}

func ensureKelosControllerDeploymentAvailable() {
	deploy := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-controller-manager", Namespace: "kelos-system"}, deploy); err != nil {
		return
	}
	deploy.Status.AvailableReplicas = 1
	deploy.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
	}
	_ = k8sClient.Status().Update(ctx, deploy)
}

func ensureKelosWebhookEndpointSlices() {
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-system"}, &corev1.Namespace{})
	if err != nil {
		return
	}
	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kelos-webhook-fixture",
			Namespace: "kelos-system",
			Labels: map[string]string{
				discoveryv1.LabelServiceName: "kelos-webhook",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1"},
				Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
			},
		},
		Ports: []discoveryv1.EndpointPort{
			{Name: ptr.To("webhook"), Port: ptr.To[int32](9443), Protocol: ptr.To(corev1.ProtocolTCP)},
		},
	}
	if err := k8sClient.Create(ctx, endpointSlice); err == nil || !apierrors.IsAlreadyExists(err) {
		return
	}
	current := &discoveryv1.EndpointSlice{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "kelos-webhook-fixture", Namespace: "kelos-system"}, current); err != nil {
		return
	}
	current.Labels = endpointSlice.Labels
	current.AddressType = endpointSlice.AddressType
	current.Endpoints = endpointSlice.Endpoints
	current.Ports = endpointSlice.Ports
	_ = k8sClient.Update(ctx, current)
}

func injectKelosCRDConversionCABundles() {
	crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
	caBundle := base64.StdEncoding.EncodeToString(webhookFixtureCAData())
	for _, name := range kelosCRDNames {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(crdGVK)
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
			continue
		}
		if err := unstructured.SetNestedField(crd.Object, caBundle, "spec", "conversion", "webhook", "clientConfig", "caBundle"); err != nil {
			continue
		}
		_ = k8sClient.Update(ctx, crd)
	}
}

// normalizeKelosConversion rewrites every live kelos CRD conversion to the
// service-based form that `kelos install` applies, clearing any url that
// envtest's WebhookInstallOptions injected. Without this, install's server-side
// apply (which sets clientConfig.service) leaves the envtest-injected
// clientConfig.url in place, and the API server rejects the CRD because exactly
// one of url or service is allowed. This is an envtest-only concern: a real
// cluster never has the local url. No-op for CRDs that do not exist yet.
func normalizeKelosConversion() {
	crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
	conversion := map[string]interface{}{
		"strategy": "Webhook",
		"webhook": map[string]interface{}{
			"conversionReviewVersions": []interface{}{"v1"},
			"clientConfig": map[string]interface{}{
				"service": map[string]interface{}{
					"name":      "kelos-webhook",
					"namespace": "kelos-system",
					"path":      "/convert",
					"port":      int64(443),
				},
			},
		},
	}
	for _, name := range kelosCRDNames {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(crdGVK)
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
			continue
		}
		if err := unstructured.SetNestedMap(crd.Object, conversion, "spec", "conversion"); err != nil {
			continue
		}
		_ = k8sClient.Update(ctx, crd)
	}
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
	for _, name := range kelosCRDNames {
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
	// Leave conversion pointing at the envtest webhook so the controller can
	// reconcile converted resources in subsequent specs.
	pointConversionToEnvtest()

	// Wait for all CRDs to be fully established before subsequent tests
	// can create custom resources. We verify by attempting to list each type.
	Eventually(func() error {
		return k8sClient.List(ctx, &kelos.TaskList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelos.TaskSpawnerList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())
	Eventually(func() error {
		return k8sClient.List(ctx, &kelos.WorkspaceList{})
	}, 30*time.Second, 100*time.Millisecond).Should(Succeed())

	deleteControllerResources()
}

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
		ensureCertManagerReadyFixture()
		startKelosWebhookReadinessFixture()
		// Reconcile each kelos CRD conversion with what install applies so
		// server-side apply does not collide with envtest's injected url.
		normalizeKelosConversion()
	})

	Context("kelos install", func() {
		AfterEach(func() {
			deleteControllerResources()
			pointConversionToEnvtest()
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
			}, controllerSettleTimeout, 100*time.Millisecond).Should(Succeed())

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
			}, controllerSettleTimeout, 100*time.Millisecond).Should(BeTrue())
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
			// Restore reachable (envtest) conversion so the controller can
			// process custom-resource finalizers during uninstall.
			pointConversionToEnvtest()

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
			// Restore reachable (envtest) conversion so the controller can
			// process custom-resource finalizers during uninstall.
			pointConversionToEnvtest()

			By("Creating a Task with required fields")
			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-uninstall-task",
					Namespace: "default",
				},
				Spec: kelos.TaskSpec{
					Type:   "claude-code",
					Prompt: "test prompt",
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeAPIKey,
						SecretRef: &kelos.SecretReference{Name: "fake-secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).To(Succeed())

			By("Waiting for the controller to add the finalizer")
			Eventually(func() bool {
				var t kelos.Task
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
			}, controllerSettleTimeout, 100*time.Millisecond).Should(BeTrue())

			By("Uninstalling")
			root2 := cli.NewRootCommand()
			root2.SetArgs([]string{"uninstall", "--kubeconfig", kubeconfigPath})
			Expect(root2.Execute()).To(Succeed())

			By("Verifying custom resources are gone")
			Eventually(func() bool {
				var taskList kelos.TaskList
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
