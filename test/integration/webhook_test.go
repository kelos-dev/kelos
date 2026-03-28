package integration

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/source"
)

var _ = Describe("Webhook Integration", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When receiving GitHub webhook events", func() {
		It("Should discover and process WebhookEvent CRDs", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-webhook-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent for an open issue")
			issuePayload := []byte(`{
				"action": "opened",
				"issue": {
					"number": 42,
					"title": "Test Issue",
					"body": "This is a test issue",
					"html_url": "https://github.com/test/repo/issues/42",
					"state": "open",
					"labels": [
						{"name": "bug"},
						{"name": "kelos-task"}
					]
				}
			}`)

			event := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "github-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source:     "github",
					Payload:    issuePayload,
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, event)).Should(Succeed())

			By("Creating a GitHubWebhookSource")
			webhookSource := &source.GitHubWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
				Labels:    []string{"kelos-task"},
			}

			By("Discovering work items from the webhook")
			var items []source.WorkItem
			Eventually(func() int {
				var err error
				items, err = webhookSource.Discover(context.Background())
				Expect(err).NotTo(HaveOccurred())
				return len(items)
			}, timeout, interval).Should(Equal(1))

			By("Verifying the discovered work item")
			Expect(items[0].Number).To(Equal(42))
			Expect(items[0].Title).To(Equal("Test Issue"))
			Expect(items[0].Kind).To(Equal("Issue"))
			Expect(items[0].Labels).To(ContainElement("bug"))
			Expect(items[0].Labels).To(ContainElement("kelos-task"))

			By("Acknowledging discovered items")
			ids := make([]string, len(items))
			for i, item := range items {
				ids[i] = item.ID
			}
			webhookSource.AcknowledgeItems(context.Background(), ids)

			By("Verifying the WebhookEvent was marked as processed")
			updatedEvent := &kelosv1alpha1.WebhookEvent{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      event.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				if err != nil {
					return false
				}
				return updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())

			Expect(updatedEvent.Status.ProcessedAt).NotTo(BeNil())

			By("Verifying subsequent discoveries return no items (already processed)")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(items).To(BeEmpty())
		})

		It("Should filter WebhookEvents by labels", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-webhook-filter-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent for an issue without required label")
			eventWithoutLabel := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "github-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "github",
					Payload: []byte(`{
						"action": "opened",
						"issue": {
							"number": 100,
							"title": "Issue Without Label",
							"state": "open",
							"labels": [{"name": "bug"}]
						}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, eventWithoutLabel)).Should(Succeed())

			By("Creating a WebhookEvent for an issue with required label")
			eventWithLabel := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "github-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "github",
					Payload: []byte(`{
						"action": "opened",
						"issue": {
							"number": 200,
							"title": "Issue With Label",
							"state": "open",
							"labels": [
								{"name": "bug"},
								{"name": "kelos-task"}
							]
						}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, eventWithLabel)).Should(Succeed())

			By("Creating a GitHubWebhookSource with label filter")
			webhookSource := &source.GitHubWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
				Labels:    []string{"kelos-task"},
			}

			By("Discovering work items")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only the issue with the required label was discovered")
			Expect(items).To(HaveLen(1))
			Expect(items[0].Number).To(Equal(200))

			By("Acknowledging discovered items")
			ids := make([]string, len(items))
			for i, item := range items {
				ids[i] = item.ID
			}
			webhookSource.AcknowledgeItems(context.Background(), ids)

			By("Verifying both events were marked as processed")
			Eventually(func() bool {
				updatedEvent := &kelosv1alpha1.WebhookEvent{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      eventWithLabel.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())

			Eventually(func() bool {
				updatedEvent := &kelosv1alpha1.WebhookEvent{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      eventWithoutLabel.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())
		})

		It("Should handle pull request webhooks", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-webhook-pr-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent for an open pull request")
			prPayload := []byte(`{
				"action": "opened",
				"pull_request": {
					"number": 123,
					"title": "Test PR",
					"body": "This is a test PR",
					"html_url": "https://github.com/test/repo/pull/123",
					"state": "open",
					"labels": [
						{"name": "enhancement"}
					],
					"head": {
						"ref": "feature-branch"
					}
				}
			}`)

			event := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "github-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source:     "github",
					Payload:    prPayload,
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, event)).Should(Succeed())

			By("Creating a GitHubWebhookSource")
			webhookSource := &source.GitHubWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
			}

			By("Discovering the pull request")
			var items []source.WorkItem
			Eventually(func() int {
				var err error
				items, err = webhookSource.Discover(context.Background())
				Expect(err).NotTo(HaveOccurred())
				return len(items)
			}, timeout, interval).Should(Equal(1))

			By("Verifying the discovered work item")
			Expect(items[0].Number).To(Equal(123))
			Expect(items[0].Title).To(Equal("Test PR"))
			Expect(items[0].Kind).To(Equal("PR"))
			Expect(items[0].Branch).To(Equal("feature-branch"))
			Expect(items[0].Labels).To(ContainElement("enhancement"))
		})

		It("Should skip closed issues and pull requests", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-webhook-closed-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent for a closed issue")
			closedPayload := []byte(`{
				"action": "closed",
				"issue": {
					"number": 999,
					"title": "Closed Issue",
					"state": "closed",
					"labels": []
				}
			}`)

			event := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "github-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source:     "github",
					Payload:    closedPayload,
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, event)).Should(Succeed())

			By("Creating a GitHubWebhookSource")
			webhookSource := &source.GitHubWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
			}

			By("Discovering work items")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no items were discovered")
			Expect(items).To(BeEmpty())

			By("Verifying the event was still marked as processed")
			Eventually(func() bool {
				updatedEvent := &kelosv1alpha1.WebhookEvent{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      event.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())
		})

		It("Should handle excludeLabels filter", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-webhook-exclude-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent for an issue with excluded label")
			excludedPayload := []byte(`{
				"action": "opened",
				"issue": {
					"number": 300,
					"title": "Excluded Issue",
					"state": "open",
					"labels": [
						{"name": "bug"},
						{"name": "skip"}
					]
				}
			}`)

			event := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "github-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source:     "github",
					Payload:    excludedPayload,
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, event)).Should(Succeed())

			By("Creating a GitHubWebhookSource with excludeLabels")
			webhookSource := &source.GitHubWebhookSource{
				Client:        k8sClient,
				Namespace:     ns.Name,
				ExcludeLabels: []string{"skip"},
			}

			By("Discovering work items")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the issue was filtered out")
			Expect(items).To(BeEmpty())

			By("Verifying the event was still marked as processed")
			Eventually(func() bool {
				updatedEvent := &kelosv1alpha1.WebhookEvent{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      event.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())
		})

		It("Should only process events with source=github", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-webhook-source-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent with source=slack")
			slackEvent := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "slack-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source:     "slack",
					Payload:    []byte(`{"event": "message"}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, slackEvent)).Should(Succeed())

			By("Creating a WebhookEvent with source=github")
			githubEvent := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "github-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "github",
					Payload: []byte(`{
						"action": "opened",
						"issue": {
							"number": 500,
							"title": "GitHub Issue",
							"state": "open",
							"labels": []
						}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, githubEvent)).Should(Succeed())

			By("Creating a GitHubWebhookSource")
			webhookSource := &source.GitHubWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
			}

			By("Discovering work items")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only the GitHub event was discovered")
			Expect(items).To(HaveLen(1))
			Expect(items[0].Number).To(Equal(500))

			By("Acknowledging discovered items")
			ids := make([]string, len(items))
			for i, item := range items {
				ids[i] = item.ID
			}
			webhookSource.AcknowledgeItems(context.Background(), ids)

			By("Verifying only the GitHub event was marked as processed")
			Eventually(func() bool {
				updatedEvent := &kelosv1alpha1.WebhookEvent{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      githubEvent.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())

			slackUpdated := &kelosv1alpha1.WebhookEvent{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      slackEvent.Name,
				Namespace: ns.Name,
			}, slackUpdated)
			Expect(err).NotTo(HaveOccurred())
			Expect(slackUpdated.Status.Processed).To(BeFalse())
		})
	})
})
