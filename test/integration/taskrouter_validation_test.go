package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("TaskRouter API validation", func() {
	const ns = "test-taskrouter-validation"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	validRouter := func(name string) *kelos.TaskRouter {
		return &kelos.TaskRouter{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: kelos.TaskRouterSpec{
				When: kelos.RouterWhen{Slack: &kelos.Slack{Channels: []string{"C0123456789"}}},
				Decision: kelos.RouterDecision{
					Worker: &kelos.WorkerSpec{
						Type:        "claude-code",
						Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
					},
				},
				Prompt: "Route this request to exactly one team",
				Routes: []kelos.RouterRoute{
					{
						Name:        "docs",
						Description: "Documentation questions",
						TargetRef: kelos.RouterTargetRef{
							Kind: kelos.RouterTargetKindTaskSpawner,
							Name: "docs-agent",
						},
					},
					{
						Name:        "code",
						Description: "Bug reports and code changes",
						TargetRef: kelos.RouterTargetRef{
							Kind: kelos.RouterTargetKindTaskSpawner,
							Name: "code-agent",
						},
					},
				},
			},
		}
	}

	It("accepts a slack-sourced router", func() {
		Expect(k8sClient.Create(ctx, validRouter("valid"), client.DryRunAll)).To(Succeed())
	})

	It("accepts a decision dispatched to a worker pool", func() {
		router := validRouter("valid-pool")
		router.Spec.Decision = kelos.RouterDecision{
			WorkerPoolRef: &kelos.WorkerPoolReference{Name: "router-pool"},
		}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).To(Succeed())
	})

	It("defaults suspend and the decision retention, and leaves the timeout unset", func() {
		router := validRouter("defaults")
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).To(Succeed())
		Expect(router.Spec.Suspend).NotTo(BeNil())
		Expect(*router.Spec.Suspend).To(BeFalse())
		Expect(router.Spec.Decision.TimeoutSeconds).To(BeNil())
		Expect(router.Spec.Decision.TTLSecondsAfterHandled).NotTo(BeNil())
		Expect(*router.Spec.Decision.TTLSecondsAfterHandled).To(Equal(int32(86400)))
	})

	It("rejects a zero decision retention", func() {
		router := validRouter("zero-retention")
		router.Spec.Decision.TTLSecondsAfterHandled = ptr.To(int32(0))
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a negative decision retention", func() {
		router := validRouter("negative-retention")
		router.Spec.Decision.TTLSecondsAfterHandled = ptr.To(int32(-1))
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("accepts a decision retention alongside a pooled decision", func() {
		router := validRouter("pooled-retention")
		router.Spec.Decision = kelos.RouterDecision{
			WorkerPoolRef:          &kelos.WorkerPoolReference{Name: "router-pool"},
			TTLSecondsAfterHandled: ptr.To(int32(600)),
		}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).To(Succeed())
	})

	It("accepts a decision timeout on an inline worker", func() {
		router := validRouter("inline-timeout")
		router.Spec.Decision.TimeoutSeconds = ptr.To(int32(120))
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).To(Succeed())
	})

	// A pooled decision runs on a pod the WorkerPool owns, so the router cannot
	// bound it. Rejecting the combination keeps a knob that cannot be honored
	// from being accepted and silently ignored.
	It("rejects a decision timeout on a pooled decision", func() {
		router := validRouter("pooled-timeout")
		router.Spec.Decision = kelos.RouterDecision{
			WorkerPoolRef:  &kelos.WorkerPoolReference{Name: "router-pool"},
			TimeoutSeconds: ptr.To(int32(120)),
		}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a router with no routes", func() {
		router := validRouter("no-routes")
		router.Spec.Routes = nil
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects duplicate route names", func() {
		router := validRouter("duplicate-routes")
		router.Spec.Routes[1].Name = router.Spec.Routes[0].Name
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an invalid route name", func() {
		router := validRouter("invalid-route-name")
		router.Spec.Routes[0].Name = "Not DNS"
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an empty route description", func() {
		router := validRouter("empty-description")
		router.Spec.Routes[0].Description = ""
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an unsupported target kind", func() {
		router := validRouter("unsupported-target-kind")
		router.Spec.Routes[0].TargetRef.Kind = "TaskPipeline"
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a missing source", func() {
		router := validRouter("no-source")
		router.Spec.When = kelos.RouterWhen{}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an unsupported target kind at the schema", func() {
		// The schema carries one target kind today; the controller refuses any
		// other, so adding an enum value cannot silently resolve a TaskSpawner.
		router := validRouter("unsupported-kind")
		router.Spec.Routes[0].TargetRef.Kind = "Task"
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an empty prompt", func() {
		router := validRouter("empty-prompt")
		router.Spec.Prompt = ""
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a decision with neither worker nor workerPoolRef", func() {
		router := validRouter("no-decision-worker")
		router.Spec.Decision = kelos.RouterDecision{}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a decision with both worker and workerPoolRef", func() {
		router := validRouter("both-decision-workers")
		router.Spec.Decision.WorkerPoolRef = &kelos.WorkerPoolReference{Name: "router-pool"}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an inline decision worker without credentials", func() {
		router := validRouter("decision-worker-no-credentials")
		router.Spec.Decision.Worker.Credentials = nil
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a zero decision timeout", func() {
		router := validRouter("zero-timeout")
		router.Spec.Decision.TimeoutSeconds = ptr.To(int32(0))
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("accepts a Route fallback naming a listed route", func() {
		router := validRouter("valid-fallback")
		router.Spec.Fallback = &kelos.RouterFallback{
			Action: kelos.RouterFallbackActionRoute,
			Route:  "docs",
		}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).To(Succeed())
	})

	It("accepts a Reply fallback", func() {
		router := validRouter("reply-fallback")
		router.Spec.Fallback = &kelos.RouterFallback{Action: kelos.RouterFallbackActionReply}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).To(Succeed())
	})

	It("rejects a Route fallback with no route", func() {
		router := validRouter("fallback-without-route")
		router.Spec.Fallback = &kelos.RouterFallback{Action: kelos.RouterFallbackActionRoute}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a Fail fallback that names a route", func() {
		router := validRouter("fail-fallback-with-route")
		router.Spec.Fallback = &kelos.RouterFallback{
			Action: kelos.RouterFallbackActionFail,
			Route:  "docs",
		}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a fallback route that is not listed", func() {
		router := validRouter("fallback-unlisted-route")
		router.Spec.Fallback = &kelos.RouterFallback{
			Action: kelos.RouterFallbackActionRoute,
			Route:  "nonexistent",
		}
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a negative maxConcurrency", func() {
		router := validRouter("negative-concurrency")
		concurrency := int32(-1)
		router.Spec.MaxConcurrency = &concurrency
		Expect(k8sClient.Create(ctx, router, client.DryRunAll)).NotTo(Succeed())
	})
})
