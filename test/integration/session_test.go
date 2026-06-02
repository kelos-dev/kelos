package integration

import (
	"crypto/rand"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/controller"
)

var _ = Describe("Persistent Execution Mode", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("Task lifecycle through session pod", func() {
		var (
			ns      *corev1.Namespace
			task    *kelosv1alpha1.Task
			pod     *corev1.Pod
			taskKey types.NamespacedName
			podKey  types.NamespacedName
		)

		BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-persistent-" + randomSuffix(),
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			task = &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "persistent-task",
					Namespace: ns.Name,
					Labels: map[string]string{
						controller.LabelExecutionMode: string(kelosv1alpha1.ExecutionModePersistent),
						"kelos.dev/taskspawner":       "my-spawner",
					},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Say hello",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeNone,
					},
				},
			}
			taskKey = types.NamespacedName{Name: task.Name, Namespace: ns.Name}

			pod = &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "session-my-spawner-0",
					Namespace: ns.Name,
					Labels: map[string]string{
						"kelos.dev/taskspawner": "my-spawner",
						"kelos.dev/component":   controller.SessionComponentLabel,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "agent", Image: "busybox"},
					},
				},
			}
			podKey = types.NamespacedName{Name: pod.Name, Namespace: ns.Name}
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should transition task through Queued → Pending → Running → Succeeded", func() {
			By("Creating a session pod")
			Expect(k8sClient.Create(ctx, pod)).Should(Succeed())

			By("Setting pod status to Running")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				pod.Status.Phase = corev1.PodRunning
				return k8sClient.Status().Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Creating a persistent-mode task")
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying the task transitions to Queued or Pending (no Job created)")
			Eventually(func() bool {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return false
				}
				return t.Status.Phase == kelosv1alpha1.TaskPhaseQueued || t.Status.Phase == kelosv1alpha1.TaskPhasePending
			}, timeout, interval).Should(BeTrue())

			By("Verifying no Job was created")
			Consistently(func() bool {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return false
				}
				return t.Status.JobName == ""
			}, 2*time.Second, interval).Should(BeTrue())

			By("Verifying the SessionReconciler assigns the task to the pod")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhasePending))

			By("Verifying pod has the assignment annotation")
			Eventually(func() string {
				var p corev1.Pod
				if err := k8sClient.Get(ctx, podKey, &p); err != nil {
					return ""
				}
				return p.Annotations[controller.AnnotationAssignedTask]
			}, timeout, interval).Should(Equal("persistent-task"))

			By("Verifying task has sessionPodName set")
			var updatedTask kelosv1alpha1.Task
			Expect(k8sClient.Get(ctx, taskKey, &updatedTask)).Should(Succeed())
			Expect(updatedTask.Status.SessionPodName).To(Equal(pod.Name))

			By("Simulating session runner setting status to running")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				if pod.Annotations == nil {
					pod.Annotations = make(map[string]string)
				}
				pod.Annotations[controller.AnnotationTaskStatus] = "running"
				return k8sClient.Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Verifying task transitions to Running")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseRunning))

			By("Simulating session runner setting status to succeeded")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				pod.Annotations[controller.AnnotationTaskStatus] = "succeeded"
				return k8sClient.Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Verifying task transitions to Succeeded")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseSucceeded))

			By("Verifying pod assignment was cleared")
			Eventually(func() bool {
				var p corev1.Pod
				if err := k8sClient.Get(ctx, podKey, &p); err != nil {
					return false
				}
				_, exists := p.Annotations[controller.AnnotationAssignedTask]
				return !exists
			}, timeout, interval).Should(BeTrue())
		})

		It("should mark task failed when session runner reports failure", func() {
			By("Creating a session pod in Running state")
			Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				pod.Status.Phase = corev1.PodRunning
				return k8sClient.Status().Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Creating a persistent-mode task")
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Waiting for task to be assigned")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhasePending))

			By("Simulating session runner reporting failure")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				if pod.Annotations == nil {
					pod.Annotations = make(map[string]string)
				}
				pod.Annotations[controller.AnnotationTaskStatus] = "failed"
				return k8sClient.Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Verifying task transitions to Failed")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseFailed))
		})

		It("should preserve task outputs written by session runner", func() {
			By("Creating a session pod in Running state")
			Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				pod.Status.Phase = corev1.PodRunning
				return k8sClient.Status().Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Creating a persistent-mode task")
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Waiting for task to be assigned")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhasePending))

			By("Simulating session runner writing outputs to task status")
			Eventually(func() error {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return err
				}
				t.Status.Outputs = []string{
					"branch: feat/hello",
					"commit: abc123",
					"response: SGVsbG8gd29ybGQ=",
				}
				t.Status.Results = map[string]string{
					"branch":   "feat/hello",
					"commit":   "abc123",
					"response": "SGVsbG8gd29ybGQ=",
				}
				return k8sClient.Status().Update(ctx, &t)
			}, timeout, interval).Should(Succeed())

			By("Simulating session runner setting status to succeeded")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				if pod.Annotations == nil {
					pod.Annotations = make(map[string]string)
				}
				pod.Annotations[controller.AnnotationTaskStatus] = "succeeded"
				return k8sClient.Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Verifying task reaches Succeeded with outputs preserved")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseSucceeded))

			By("Verifying outputs and results are still present on the task")
			var finalTask kelosv1alpha1.Task
			Expect(k8sClient.Get(ctx, taskKey, &finalTask)).Should(Succeed())
			Expect(finalTask.Status.Outputs).To(ContainElement("branch: feat/hello"))
			Expect(finalTask.Status.Outputs).To(ContainElement("response: SGVsbG8gd29ybGQ="))
			Expect(finalTask.Status.Results).To(HaveKeyWithValue("branch", "feat/hello"))
			Expect(finalTask.Status.Results).To(HaveKeyWithValue("response", "SGVsbG8gd29ybGQ="))
		})

		It("should requeue task when session runner reports retriable failure", func() {
			By("Creating a TaskSpawner for retry config")
			spawner := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeNone,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, spawner)).Should(Succeed())

			By("Creating a session pod in Running state")
			Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				pod.Status.Phase = corev1.PodRunning
				return k8sClient.Status().Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Creating a persistent-mode task")
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Waiting for task to be assigned")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhasePending))

			By("Simulating session runner reporting retriable failure (token-expired)")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				if pod.Annotations == nil {
					pod.Annotations = make(map[string]string)
				}
				pod.Annotations[controller.AnnotationTaskStatus] = "failed"
				pod.Annotations[controller.AnnotationTaskFailureReason] = "token-expired"
				return k8sClient.Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Verifying task is requeued (not Failed) and retry count incremented")
			Eventually(func() int32 {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return -1
				}
				return t.Status.SessionRetryCount
			}, timeout, interval).Should(BeNumerically(">=", int32(1)))

			By("Verifying task is not in terminal Failed state")
			var requeuedTask kelosv1alpha1.Task
			Expect(k8sClient.Get(ctx, taskKey, &requeuedTask)).Should(Succeed())
			Expect(requeuedTask.Status.Phase).NotTo(Equal(kelosv1alpha1.TaskPhaseFailed))

		})

		It("should mark task as terminal Failed when failure reason is not retriable", func() {
			By("Creating a session pod in Running state")
			Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				pod.Status.Phase = corev1.PodRunning
				return k8sClient.Status().Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Creating a persistent-mode task")
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Waiting for task to be assigned")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhasePending))

			By("Simulating session runner reporting non-retriable failure")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				if pod.Annotations == nil {
					pod.Annotations = make(map[string]string)
				}
				pod.Annotations[controller.AnnotationTaskStatus] = "failed"
				// No failure reason annotation = not retriable
				return k8sClient.Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			By("Verifying task transitions to Failed (terminal)")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseFailed))
		})

		It("should requeue when no session pod is available", func() {
			By("Creating a persistent-mode task without a session pod")
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying task reaches Queued phase")
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseQueued))

			By("Verifying task stays in Queued (not assigned)")
			Consistently(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, 3*time.Second, interval).Should(Equal(kelosv1alpha1.TaskPhaseQueued))
		})
	})
})

func randomSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
