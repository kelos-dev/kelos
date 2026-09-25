package integration

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("SessionSpawner", func() {
	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("sessionspawner-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	It("accepts a configured GitHub webhook", func() {
		spawner := validSessionSpawner(namespace, "workers")
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())

		Eventually(func(g Gomega) {
			var current kelos.SessionSpawner
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(spawner), &current)).To(Succeed())
			g.Expect(current.Status.ObservedGeneration).To(Equal(current.Generation))
			g.Expect(current.Status.TotalSessions).To(Equal(int32(0)))
			g.Expect(current.Status.Conditions).To(BeEmpty())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("rejects a SessionSpawner without spec", func() {
		spawner := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "kelos.dev/v1alpha2",
			"kind":       "SessionSpawner",
			"metadata": map[string]interface{}{
				"name":      "missing-spec",
				"namespace": namespace,
			},
		}}
		spawner.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha2", Kind: "SessionSpawner"})
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("accepts GitHub event types other than issue_comment", func() {
		spawner := validSessionSpawner(namespace, "issues-events")
		spawner.Spec.When.GitHubWebhook.Events = []string{"issues"}
		spawner.Spec.When.GitHubWebhook.Filters[0].Event = "issues"
		spawner.Spec.When.GitHubWebhook.Filters[0].Action = "opened"
		spawner.Spec.When.GitHubWebhook.Filters[0].BodyPattern = ""
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())
	})

	It("rejects GitHub reporting until Session reporting is supported", func() {
		spawner := validSessionSpawner(namespace, "reporting")
		spawner.Spec.When.GitHubWebhook.Reporting = &kelos.GitHubReporting{Enabled: true}
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("requires a templated initialPrompt", func() {
		spawner := validSessionSpawner(namespace, "missing-prompt")
		spawner.Spec.SessionTemplate.InitialPrompt = ""
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("requires a named workspace reference", func() {
		spawner := validSessionSpawner(namespace, "missing-workspace")
		spawner.Spec.SessionTemplate.Worker.WorkspaceRef.Name = ""
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("accepts credentials without session template credentials", func() {
		spawner := validSessionSpawner(namespace, "multi-account")
		spawner.Spec.SessionTemplate.Worker.Credentials = nil
		spawner.Spec.Credentials = []kelos.SpawnerCredential{
			{Name: "account-a", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-a"}},
			{Name: "account-b", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-b"}},
		}
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())
	})

	It("rejects credentials combined with session template credentials", func() {
		spawner := validSessionSpawner(namespace, "credential-conflict")
		spawner.Spec.Credentials = []kelos.SpawnerCredential{
			{Name: "account-a", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-a"}},
		}
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("requires session template or spawner credentials", func() {
		spawner := validSessionSpawner(namespace, "missing-credentials")
		spawner.Spec.SessionTemplate.Worker.Credentials = nil
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("accepts a Slack source without an initialPrompt", func() {
		spawner := validSlackSessionSpawner(namespace, "slack-thread")
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())

		Eventually(func(g Gomega) {
			var current kelos.SessionSpawner
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(spawner), &current)).To(Succeed())
			g.Expect(current.Status.ObservedGeneration).To(Equal(current.Generation))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("accepts a Slack source with triggers and exclusions", func() {
		spawner := validSlackSessionSpawner(namespace, "slack-filtered")
		mentionOptional := true
		spawner.Spec.When.Slack.Triggers = []kelos.SlackTrigger{
			{Pattern: "^gravity ", MentionOptional: &mentionOptional},
		}
		spawner.Spec.When.Slack.ExcludeFilters = []kelos.SlackFilter{
			{Channels: []string{"D0123456789"}},
		}
		spawner.Spec.When.Slack.BotMessagePolicy = kelos.BotMessagePolicyOthersOnly
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())
	})

	It("rejects a spawner with neither a GitHub webhook nor a Slack source", func() {
		spawner := validSlackSessionSpawner(namespace, "no-source")
		spawner.Spec.When.Slack = nil
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("rejects a spawner with both a GitHub webhook and a Slack source", func() {
		spawner := validSessionSpawner(namespace, "two-sources")
		spawner.Spec.When.Slack = &kelos.Slack{}
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})

	It("rejects an initialPrompt for a Slack source", func() {
		spawner := validSlackSessionSpawner(namespace, "slack-initial-prompt")
		spawner.Spec.SessionTemplate.InitialPrompt = "Prime the Session"
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
		Expect(err.Error()).To(ContainSubstring("initialPrompt is not supported for when.slack"))
	})

	It("accepts an empty initialPrompt for a Slack source", func() {
		spawner := validSlackSessionSpawner(namespace, "slack-empty-prompt")
		spawner.Spec.SessionTemplate.InitialPrompt = ""
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())
	})

	It("rejects a suspended template for a Slack source", func() {
		spawner := validSlackSessionSpawner(namespace, "slack-suspended")
		suspend := true
		spawner.Spec.SessionTemplate.Suspend = &suspend
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
		Expect(err.Error()).To(ContainSubstring("suspend is not supported for when.slack"))
	})

	It("accepts an unsuspended template for a Slack source", func() {
		spawner := validSlackSessionSpawner(namespace, "slack-unsuspended")
		suspend := false
		spawner.Spec.SessionTemplate.Suspend = &suspend
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())
	})

	It("still allows an initialPrompt for a GitHub webhook source", func() {
		spawner := validSessionSpawner(namespace, "webhook-prompt")
		Expect(k8sClient.Create(ctx, spawner)).To(Succeed())
	})

	It("still requires a workspace reference for a Slack source", func() {
		spawner := validSlackSessionSpawner(namespace, "slack-missing-workspace")
		spawner.Spec.SessionTemplate.Worker.WorkspaceRef.Name = ""
		err := k8sClient.Create(ctx, spawner)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
	})
})

// validSlackSessionSpawner returns a SessionSpawner driven by Slack threads,
// which needs no initialPrompt because every thread message is its own turn.
func validSlackSessionSpawner(namespace, name string) *kelos.SessionSpawner {
	spawner := validSessionSpawner(namespace, name)
	spawner.Spec.When = kelos.SessionSpawnerWhen{Slack: &kelos.Slack{
		Channels: []string{"C0123456789"},
	}}
	spawner.Spec.SessionTemplate.InitialBranch = ""
	spawner.Spec.SessionTemplate.InitialPrompt = ""
	return spawner
}

func validSessionSpawner(namespace, name string) *kelos.SessionSpawner {
	return &kelos.SessionSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kelos.SessionSpawnerSpec{
			When: kelos.SessionSpawnerWhen{GitHubWebhook: &kelos.GitHubWebhook{
				Events: []string{"issue_comment"},
				Filters: []kelos.GitHubWebhookFilter{{
					Event:       "issue_comment",
					Action:      "created",
					BodyPattern: `(?m)^/kelos pick-up[ \t]*\r?$`,
				}},
			}},
			SessionTemplate: kelos.SessionTemplate{
				SessionSpec: kelos.SessionSpec{
					Worker: kelos.WorkerSpec{
						Type:         "codex",
						Credentials:  &kelos.Credentials{Type: kelos.CredentialTypeNone},
						WorkspaceRef: &kelos.WorkspaceReference{Name: "workspace"},
					},
					InitialBranch: "kelos-task-{{.Number}}",
					InitialPrompt: "Handle issue #{{.Number}}",
				},
			},
		},
	}
}
