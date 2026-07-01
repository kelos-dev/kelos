package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("WorkerPool", func() {
	f := framework.NewFramework("workerpool")

	It("should create a StatefulSet and reach Ready phase", func() {
		By("creating credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "wp-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
			},
		})

		By("creating a WorkerPool")
		f.CreateWorkerPool(&kelos.WorkerPool{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-pool",
			},
			Spec: kelos.WorkerPoolSpec{
				Worker: kelos.WorkerSpec{
					Type: "claude-code",
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
					WorkspaceRef: &kelos.WorkspaceReference{Name: "wp-workspace"},
				},
				Replicas: ptr.To(int32(1)),
				VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		})

		By("waiting for WorkerPool to reach Ready phase")
		f.WaitForWorkerPoolReady("e2e-pool")

		By("verifying StatefulSet was created")
		f.WaitForStatefulSetReady("wp-e2e-pool", 1)
	})

	It("should assign a Task to a worker pod and complete it", func() {
		By("creating credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "wp-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
			},
		})

		By("creating a WorkerPool")
		f.CreateWorkerPool(&kelos.WorkerPool{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-pool",
			},
			Spec: kelos.WorkerPoolSpec{
				Worker: kelos.WorkerSpec{
					Type: "claude-code",
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
					WorkspaceRef: &kelos.WorkspaceReference{Name: "wp-workspace"},
				},
				Replicas: ptr.To(int32(1)),
				VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		})

		By("waiting for WorkerPool to reach Ready phase")
		f.WaitForWorkerPoolReady("e2e-pool")

		By("creating a Task that references the pool")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pool-task",
			},
			Spec: kelos.TaskSpec{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "e2e-pool"},
				Prompt:        "Print 'Hello from WorkerPool' to stdout and exit",
			},
		})

		By("waiting for Task to be assigned to a worker pod")
		podName := f.WaitForTaskWorkerPodName("pool-task")
		GinkgoWriter.Printf("Task assigned to pod: %s\n", podName)

		By("waiting for Task to reach Succeeded phase")
		f.WaitForTaskPhase("pool-task", "Succeeded")
	})

	It("should handle multiple tasks sequentially on a single worker", func() {
		By("creating credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "wp-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
			},
		})

		By("creating a WorkerPool with 1 replica")
		f.CreateWorkerPool(&kelos.WorkerPool{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-pool",
			},
			Spec: kelos.WorkerPoolSpec{
				Worker: kelos.WorkerSpec{
					Type: "claude-code",
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
					WorkspaceRef: &kelos.WorkspaceReference{Name: "wp-workspace"},
				},
				Replicas: ptr.To(int32(1)),
				VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		})

		By("waiting for WorkerPool to reach Ready phase")
		f.WaitForWorkerPoolReady("e2e-pool")

		By("creating first Task")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pool-task-1",
			},
			Spec: kelos.TaskSpec{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "e2e-pool"},
				Prompt:        "Print 'task-1-done' to stdout and exit",
			},
		})

		By("waiting for first Task to succeed")
		f.WaitForTaskPhase("pool-task-1", "Succeeded")

		By("creating second Task")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pool-task-2",
			},
			Spec: kelos.TaskSpec{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "e2e-pool"},
				Prompt:        "Print 'task-2-done' to stdout and exit",
			},
		})

		By("waiting for second Task to succeed")
		f.WaitForTaskPhase("pool-task-2", "Succeeded")
	})
})

var _ = Describe("WorkerPool with TaskSpawner", func() {
	f := framework.NewFramework("wp-spawner")

	It("should spawn tasks that execute on the worker pool", func() {
		By("creating GitHub token secret")
		f.CreateSecret("github-token",
			"GITHUB_TOKEN="+githubToken)

		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace with secretRef")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "wp-spawner-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo:      "https://github.com/kelos-dev/kelos.git",
				Ref:       "main",
				SecretRef: &kelos.SecretReference{Name: "github-token"},
			},
		})

		By("creating a WorkerPool")
		f.CreateWorkerPool(&kelos.WorkerPool{
			ObjectMeta: metav1.ObjectMeta{
				Name: "spawner-pool",
			},
			Spec: kelos.WorkerPoolSpec{
				Worker: kelos.WorkerSpec{
					Type: "claude-code",
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeOAuth,
						SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
					},
					WorkspaceRef: &kelos.WorkspaceReference{Name: "wp-spawner-workspace"},
				},
				Replicas: ptr.To(int32(1)),
				VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		})

		By("waiting for WorkerPool to reach Ready phase")
		f.WaitForWorkerPoolReady("spawner-pool")

		By("creating a TaskSpawner that uses the pool")
		f.CreateTaskSpawner(&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pool-spawner",
			},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{
					GitHubIssues: &kelos.GitHubIssues{
						Labels: []string{"do-not-remove/e2e-anchor"},
						State:  "open",
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					WorkerPoolRef:  &kelos.WorkerPoolReference{Name: "spawner-pool"},
					WorkspaceRef:   &kelos.WorkspaceReference{Name: "wp-spawner-workspace"},
					PromptTemplate: "Summarize: {{.Title}}",
				},
			},
		})

		By("waiting for TaskSpawner Deployment to become available")
		f.WaitForDeploymentAvailable("pool-spawner")

		By("waiting for TaskSpawner phase to become Running")
		Eventually(func() string {
			return f.GetTaskSpawnerPhase("pool-spawner")
		}, 3*time.Minute, 10*time.Second).Should(Equal("Running"))

		By("waiting for at least one Task to be created with workerPoolRef")
		Eventually(func() bool {
			tasks, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).List(
				context.TODO(), metav1.ListOptions{
					LabelSelector: "kelos.dev/taskspawner=pool-spawner",
				})
			if err != nil || len(tasks.Items) == 0 {
				return false
			}
			for _, t := range tasks.Items {
				if t.Spec.WorkerPoolRef != nil && t.Spec.WorkerPoolRef.Name == "spawner-pool" {
					return true
				}
			}
			return false
		}, 5*time.Minute, 10*time.Second).Should(BeTrue(), "No task with workerPoolRef was spawned")

		By("waiting for at least one spawned Task to reach a terminal phase")
		Eventually(func() bool {
			tasks, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).List(
				context.TODO(), metav1.ListOptions{
					LabelSelector: "kelos.dev/taskspawner=pool-spawner",
				})
			if err != nil {
				return false
			}
			for _, t := range tasks.Items {
				if t.Status.Phase == kelos.TaskPhaseSucceeded || t.Status.Phase == kelos.TaskPhaseFailed {
					GinkgoWriter.Printf("Spawned task %s reached phase %s\n", t.Name, t.Status.Phase)
					return true
				}
			}
			return false
		}, 5*time.Minute, 10*time.Second).Should(BeTrue(), "No spawned task reached a terminal phase")
	})
})
