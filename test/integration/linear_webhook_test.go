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

var _ = Describe("Linear Webhook Integration", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When receiving Linear webhook events", func() {
		It("Should discover and process WebhookEvent CRDs", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-linear-webhook-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent for a Linear issue")
			issuePayload := []byte(`{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "abc-123",
					"identifier": "ENG-42",
					"number": 42,
					"title": "Fix authentication bug",
					"description": "Users cannot log in after password reset",
					"url": "https://linear.app/myteam/issue/ENG-42",
					"state": {
						"name": "Todo",
						"type": "unstarted"
					},
					"labels": [
						{"name": "bug"},
						{"name": "high-priority"}
					],
					"team": {
						"key": "ENG",
						"name": "Engineering"
					}
				}
			}`)

			event := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "linear-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source:     "linear",
					Payload:    issuePayload,
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, event)).Should(Succeed())

			By("Creating a LinearWebhookSource")
			webhookSource := &source.LinearWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
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
			Expect(items[0].ID).To(Equal("ENG-42"))
			Expect(items[0].Number).To(Equal(42))
			Expect(items[0].Title).To(Equal("Fix authentication bug"))
			Expect(items[0].Kind).To(Equal("Todo"))
			Expect(items[0].Labels).To(ContainElement("bug"))
			Expect(items[0].Labels).To(ContainElement("high-priority"))

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
		})

		It("Should filter WebhookEvents by state", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-linear-state-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent for a completed issue")
			completedPayload := []byte(`{
				"type": "Issue",
				"action": "update",
				"data": {
					"identifier": "ENG-100",
					"number": 100,
					"title": "Completed Issue",
					"state": {
						"name": "Done",
						"type": "completed"
					},
					"labels": [],
					"team": {"key": "ENG"}
				}
			}`)

			event1 := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "linear-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source:     "linear",
					Payload:    completedPayload,
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, event1)).Should(Succeed())

			By("Creating a WebhookEvent for an open issue")
			openPayload := []byte(`{
				"type": "Issue",
				"action": "create",
				"data": {
					"identifier": "ENG-200",
					"number": 200,
					"title": "Open Issue",
					"state": {
						"name": "In Progress",
						"type": "started"
					},
					"labels": [],
					"team": {"key": "ENG"}
				}
			}`)

			event2 := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "linear-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source:     "linear",
					Payload:    openPayload,
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, event2)).Should(Succeed())

			By("Creating a LinearWebhookSource with no state filter")
			webhookSource := &source.LinearWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
			}

			By("Discovering work items")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only the non-terminal issue was discovered")
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
					Name:      event1.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())

			Eventually(func() bool {
				updatedEvent := &kelosv1alpha1.WebhookEvent{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      event2.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())
		})

		It("Should filter by configured states", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-linear-states-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating WebhookEvents with different states")
			todoEvent := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "linear-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "linear",
					Payload: []byte(`{
						"type": "Issue",
						"action": "create",
						"data": {
							"identifier": "ENG-300",
							"number": 300,
							"title": "Todo Issue",
							"state": {"name": "Todo", "type": "unstarted"},
							"labels": [],
							"team": {"key": "ENG"}
						}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, todoEvent)).Should(Succeed())

			inProgressEvent := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "linear-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "linear",
					Payload: []byte(`{
						"type": "Issue",
						"action": "create",
						"data": {
							"identifier": "ENG-400",
							"number": 400,
							"title": "In Progress Issue",
							"state": {"name": "In Progress", "type": "started"},
							"labels": [],
							"team": {"key": "ENG"}
						}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, inProgressEvent)).Should(Succeed())

			By("Creating a LinearWebhookSource with state filter")
			webhookSource := &source.LinearWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
				States:    []string{"Todo"},
			}

			By("Discovering work items")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only Todo state was discovered")
			Expect(items).To(HaveLen(1))
			Expect(items[0].Number).To(Equal(300))
			Expect(items[0].Kind).To(Equal("Todo"))
		})

		It("Should filter by labels", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-linear-labels-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a WebhookEvent with matching labels")
			matchingEvent := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "linear-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "linear",
					Payload: []byte(`{
						"type": "Issue",
						"action": "create",
						"data": {
							"identifier": "ENG-500",
							"number": 500,
							"title": "Bug Issue",
							"state": {"name": "Todo", "type": "unstarted"},
							"labels": [{"name": "bug"}, {"name": "backend"}],
							"team": {"key": "ENG"}
						}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, matchingEvent)).Should(Succeed())

			By("Creating a WebhookEvent without matching labels")
			nonMatchingEvent := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "linear-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "linear",
					Payload: []byte(`{
						"type": "Issue",
						"action": "create",
						"data": {
							"identifier": "ENG-600",
							"number": 600,
							"title": "Feature Issue",
							"state": {"name": "Todo", "type": "unstarted"},
							"labels": [{"name": "feature"}],
							"team": {"key": "ENG"}
						}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, nonMatchingEvent)).Should(Succeed())

			By("Creating a LinearWebhookSource with label filter")
			webhookSource := &source.LinearWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
				Labels:    []string{"bug"},
			}

			By("Discovering work items")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only the bug-labeled issue was discovered")
			Expect(items).To(HaveLen(1))
			Expect(items[0].Number).To(Equal(500))

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
					Name:      matchingEvent.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())

			Eventually(func() bool {
				updatedEvent := &kelosv1alpha1.WebhookEvent{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      nonMatchingEvent.Name,
					Namespace: ns.Name,
				}, updatedEvent)
				return err == nil && updatedEvent.Status.Processed
			}, timeout, interval).Should(BeTrue())
		})

		It("Should only process events with source=linear", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-linear-source-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a GitHub webhook event (should be ignored)")
			githubEvent := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "github-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "github",
					Payload: []byte(`{
						"action": "opened",
						"issue": {"number": 1}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, githubEvent)).Should(Succeed())

			By("Creating a Linear webhook event")
			linearEvent := &kelosv1alpha1.WebhookEvent{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "linear-webhook-",
					Namespace:    ns.Name,
				},
				Spec: kelosv1alpha1.WebhookEventSpec{
					Source: "linear",
					Payload: []byte(`{
						"type": "Issue",
						"action": "create",
						"data": {
							"identifier": "ENG-700",
							"number": 700,
							"title": "Linear Issue",
							"state": {"name": "Todo", "type": "unstarted"},
							"labels": [],
							"team": {"key": "ENG"}
						}
					}`),
					ReceivedAt: metav1.Now(),
				},
			}
			Expect(k8sClient.Create(ctx, linearEvent)).Should(Succeed())

			By("Creating a LinearWebhookSource")
			webhookSource := &source.LinearWebhookSource{
				Client:    k8sClient,
				Namespace: ns.Name,
			}

			By("Discovering work items")
			items, err := webhookSource.Discover(context.Background())
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only the Linear event was discovered")
			Expect(items).To(HaveLen(1))
			Expect(items[0].Number).To(Equal(700))

			By("Verifying GitHub event was not processed")
			githubUpdated := &kelosv1alpha1.WebhookEvent{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      githubEvent.Name,
				Namespace: ns.Name,
			}, githubUpdated)
			Expect(err).NotTo(HaveOccurred())
			Expect(githubUpdated.Status.Processed).To(BeFalse())
		})
	})
})
