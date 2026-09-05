package integration

import (
	"encoding/base64"
	"fmt"
	"net"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func init() {
	if err := kelosv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		panic(err)
	}
}

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
// config left by the install tests.
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

var _ = Describe("AgentConfig conversion webhook", Ordered, func() {
	BeforeAll(func() {
		pointConversionToEnvtest()
	})

	AfterAll(func() {
		// Remove the objects created here so specs that delete a kelos CRD are
		// not left with instances. envtest runs no namespace controller, so the
		// namespaces themselves cannot be cleaned up this way — the instances
		// have to go individually.
		for _, ns := range []string{"test-conv-up", "test-conv-down"} {
			_ = k8sClient.Delete(ctx, &kelos.AgentConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: ns},
			})
		}
		// TaskSpawners carry a controller finalizer, so leaving them behind
		// would keep instances of the taskspawners CRD alive indefinitely.
		for _, ts := range []types.NamespacedName{
			{Name: "ts", Namespace: "test-conv-ts"},
			{Name: "ts-slack", Namespace: "test-conv-ts-slack"},
		} {
			_ = k8sClient.Delete(ctx, &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{Name: ts.Name, Namespace: ts.Namespace},
			})
		}
	})

	It("converts a v1alpha1 map env up to a v1alpha2 list", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-conv-up"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		By("Creating the AgentConfig in v1alpha1 with a map env")
		v1 := &kelosv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: ns.Name},
			Spec: kelosv1alpha1.AgentConfigSpec{
				MCPServers: []kelosv1alpha1.MCPServerSpec{
					{
						Name: "local",
						Type: "stdio",
						Env:  map[string]string{"B": "2", "A": "1"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, v1)).To(Succeed())

		By("Reading it back as v1alpha2 and asserting the env is a sorted list")
		got := &kelos.AgentConfig{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cfg", Namespace: ns.Name}, got)).To(Succeed())
		Expect(got.Spec.MCPServers).To(HaveLen(1))
		Expect(got.Spec.MCPServers[0].Env).To(Equal([]corev1.EnvVar{
			{Name: "A", Value: "1"},
			{Name: "B", Value: "2"},
		}))
	})

	It("drops valueFrom when a v1alpha2 list is read down as a v1alpha1 map", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-conv-down"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		By("Creating the AgentConfig in v1alpha2 with a literal and a valueFrom env")
		v2 := &kelos.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: ns.Name},
			Spec: kelos.AgentConfigSpec{
				MCPServers: []kelos.MCPServerSpec{
					{
						Name:    "local",
						Type:    "stdio",
						Command: "run",
						Env: []corev1.EnvVar{
							{Name: "LITERAL", Value: "x"},
							{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
									Key:                  "k",
								},
							}},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, v2)).To(Succeed())

		By("Reading it back as v1alpha1 and asserting valueFrom was dropped")
		got := &kelosv1alpha1.AgentConfig{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cfg", Namespace: ns.Name}, got)).To(Succeed())
		Expect(got.Spec.MCPServers).To(HaveLen(1))
		Expect(got.Spec.MCPServers[0].Env).To(Equal(map[string]string{"LITERAL": "x"}))
	})

	It("folds TaskSpawner legacy comment + root pollInterval into v1alpha2", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-conv-ts"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		By("Creating a v1alpha1 TaskSpawner using legacy triggerComment + root pollInterval")
		v1 := &kelosv1alpha1.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: "ts", Namespace: ns.Name},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				PollInterval: "9m",
				When: kelosv1alpha1.When{
					GitHubIssues: &kelosv1alpha1.GitHubIssues{
						Repo:           "owner/repo",
						TriggerComment: "/kelos go",
					},
				},
				TaskTemplate: kelosv1alpha1.TaskTemplate{
					Type:         "claude-code",
					Credentials:  kelosv1alpha1.Credentials{Type: kelosv1alpha1.CredentialTypeNone},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, v1)).To(Succeed())

		By("Reading it back as v1alpha2 and asserting the foldings")
		got := &kelos.TaskSpawner{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ts", Namespace: ns.Name}, got)).To(Succeed())
		gi := got.Spec.When.GitHubIssues
		Expect(gi).NotTo(BeNil())
		Expect(gi.PollInterval).To(Equal("9m"))
		Expect(gi.CommentPolicy).NotTo(BeNil())
		Expect(gi.CommentPolicy.TriggerComment).To(Equal("/kelos go"))
	})

	It("preserves TaskSpawner slack excludeChannels across a v1alpha1 round-trip", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-conv-ts-slack"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		key := types.NamespacedName{Name: "ts-slack", Namespace: ns.Name}

		By("Creating a v1alpha2 TaskSpawner with slack channels and excludeChannels")
		v2 := &kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					Slack: &kelos.Slack{
						Channels:        []string{"C0123456789"},
						ExcludeChannels: []string{"C9876543210", "D0123456789"},
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Type:         "claude-code",
					Credentials:  &kelos.Credentials{Type: kelos.CredentialTypeNone},
					WorkspaceRef: &kelos.WorkspaceReference{Name: "ws"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, v2)).To(Succeed())

		By("Reading it back as v1alpha1 and asserting the field survives in the preservation annotation")
		down := &kelosv1alpha1.TaskSpawner{}
		Expect(k8sClient.Get(ctx, key, down)).To(Succeed())
		Expect(down.Spec.When.Slack).NotTo(BeNil())
		Expect(down.Spec.When.Slack.Channels).To(Equal([]string{"C0123456789"}))
		Expect(down.Annotations).To(HaveKeyWithValue(
			"kelos.dev/v1alpha2-slack-exclude-channels", `["C9876543210","D0123456789"]`))

		By("Writing the object back through v1alpha1")
		// The TaskSpawner controller reconciles this object concurrently, so the
		// read-modify-write has to tolerate a conflict on a stale resourceVersion.
		Eventually(func() error {
			current := &kelosv1alpha1.TaskSpawner{}
			if err := k8sClient.Get(ctx, key, current); err != nil {
				return err
			}
			if current.Spec.When.Slack == nil {
				return fmt.Errorf("slack config missing on the v1alpha1 read")
			}
			current.Spec.When.Slack.Channels = []string{"C0123456789", "C1122334455"}
			return k8sClient.Update(ctx, current)
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		By("Reading it as v1alpha2 and asserting excludeChannels was restored")
		got := &kelos.TaskSpawner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Spec.When.Slack).NotTo(BeNil())
		Expect(got.Spec.When.Slack.Channels).To(Equal([]string{"C0123456789", "C1122334455"}))
		Expect(got.Spec.When.Slack.ExcludeChannels).To(Equal([]string{"C9876543210", "D0123456789"}))
		Expect(got.Annotations).NotTo(HaveKey("kelos.dev/v1alpha2-slack-exclude-channels"))
	})
})
