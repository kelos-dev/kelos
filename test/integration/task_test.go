package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func logJobSpec(job *batchv1.Job) {
	spec, _ := json.MarshalIndent(job.Spec, "", "  ")
	GinkgoWriter.Printf("\n=== Job Spec ===\n%s\n================\n", spec)
}

func findEvent(namespace, involvedObjectName, reason string) *corev1.Event {
	eventList := &corev1.EventList{}
	err := k8sClient.List(ctx, eventList, client.InNamespace(namespace))
	if err != nil {
		return nil
	}
	for i, event := range eventList.Items {
		if event.InvolvedObject.Name == involvedObjectName && event.Reason == reason {
			return &eventList.Items[i]
		}
	}
	return nil
}

var _ = Describe("Task Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When creating a Task with API key credentials", func() {
		It("Should create a Job and update status", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-apikey",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					Model: "claude-sonnet-4-20250514",
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}

			By("Verifying the Task has a finalizer")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return false
				}
				for _, f := range createdTask.Finalizers {
					if f == "kelos.dev/finalizer" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the Job spec")
			Expect(createdJob.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := createdJob.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("claude-code"))
			Expect(container.Command).To(Equal([]string{"/kelos_entrypoint.sh"}))
			Expect(container.Args).To(Equal([]string{"Create a hello world program"}))

			By("Verifying the Job has KELOS_MODEL, KELOS_AGENT_TYPE, and API key env vars")
			Expect(container.Env).To(HaveLen(3))
			Expect(container.Env[0].Name).To(Equal("KELOS_MODEL"))
			Expect(container.Env[0].Value).To(Equal("claude-sonnet-4-20250514"))
			Expect(container.Env[1].Name).To(Equal("KELOS_AGENT_TYPE"))
			Expect(container.Env[1].Value).To(Equal("claude-code"))
			Expect(container.Env[2].Name).To(Equal("ANTHROPIC_API_KEY"))
			Expect(container.Env[2].ValueFrom.SecretKeyRef.Name).To(Equal("anthropic-api-key"))

			By("Verifying the Job has owner reference")
			Expect(createdJob.OwnerReferences).To(HaveLen(1))
			Expect(createdJob.OwnerReferences[0].Name).To(Equal(task.Name))
			Expect(createdJob.OwnerReferences[0].Kind).To(Equal("Task"))

			By("Verifying Task status has JobName")
			Eventually(func() string {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.JobName
			}, timeout, interval).Should(Equal(task.Name))

			By("Simulating Job running")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
					return err
				}
				createdJob.Status.Active = 1
				return k8sClient.Status().Update(ctx, createdJob)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task status is Running")
			Eventually(func() kelosv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseRunning))

			By("Simulating Job completion")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
					return err
				}
				createdJob.Status.Active = 0
				createdJob.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, createdJob)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task status is Succeeded")
			Eventually(func() kelosv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseSucceeded))

			By("Verifying Task has completion time")
			Expect(createdTask.Status.CompletionTime).NotTo(BeNil())

			By("Verifying Outputs field exists (empty in envtest, no real Pod logs)")
			Expect(createdTask.Status.Outputs).To(BeEmpty())

			By("Deleting the Task")
			Expect(k8sClient.Delete(ctx, createdTask)).Should(Succeed())

			By("Verifying the Task is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				return err != nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a Task with OAuth credentials", func() {
		It("Should create a Job with OAuth token env var", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-oauth",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with OAuth token")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "claude-oauth",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"CLAUDE_CODE_OAUTH_TOKEN": "test-oauth-token",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task with OAuth")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-oauth-task",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeOAuth,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "claude-oauth",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the Job uses uniform interface")
			container := createdJob.Spec.Template.Spec.Containers[0]
			Expect(container.Command).To(Equal([]string{"/kelos_entrypoint.sh"}))
			Expect(container.Args).To(Equal([]string{"Create a hello world program"}))

			By("Verifying the Job has KELOS_AGENT_TYPE and OAuth token env vars")
			Expect(container.Env).To(HaveLen(2))
			Expect(container.Env[0].Name).To(Equal("KELOS_AGENT_TYPE"))
			Expect(container.Env[0].Value).To(Equal("claude-code"))
			Expect(container.Env[1].Name).To(Equal("CLAUDE_CODE_OAUTH_TOKEN"))
			Expect(container.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("claude-oauth"))
			Expect(container.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("CLAUDE_CODE_OAUTH_TOKEN"))
		})
	})

	Context("When creating a Task with workspace and ref", func() {
		It("Should create a Job with init container and workspace volume", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-workspace-ref",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace resource")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with workspace ref")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-ref",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Fix the bug",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-workspace",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the init container")
			Expect(createdJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := createdJob.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Name).To(Equal("git-clone"))
			Expect(initContainer.Image).To(Equal(controller.GitCloneImage))
			Expect(initContainer.Args).To(Equal([]string{
				"clone", "--branch", "main", "--no-single-branch", "--depth", "1",
				"--", "https://github.com/example/repo.git", "/workspace/repo",
			}))

			By("Verifying the init container runs as claude user")
			Expect(initContainer.SecurityContext).NotTo(BeNil())
			Expect(initContainer.SecurityContext.RunAsUser).NotTo(BeNil())
			Expect(*initContainer.SecurityContext.RunAsUser).To(Equal(controller.ClaudeCodeUID))

			By("Verifying the pod security context sets FSGroup")
			Expect(createdJob.Spec.Template.Spec.SecurityContext).NotTo(BeNil())
			Expect(createdJob.Spec.Template.Spec.SecurityContext.FSGroup).NotTo(BeNil())
			Expect(*createdJob.Spec.Template.Spec.SecurityContext.FSGroup).To(Equal(controller.ClaudeCodeUID))

			By("Verifying the workspace volume")
			Expect(createdJob.Spec.Template.Spec.Volumes).To(HaveLen(1))
			Expect(createdJob.Spec.Template.Spec.Volumes[0].Name).To(Equal(controller.WorkspaceVolumeName))
			Expect(createdJob.Spec.Template.Spec.Volumes[0].EmptyDir).NotTo(BeNil())

			By("Verifying the init container volume mount")
			Expect(initContainer.VolumeMounts).To(HaveLen(1))
			Expect(initContainer.VolumeMounts[0].Name).To(Equal(controller.WorkspaceVolumeName))
			Expect(initContainer.VolumeMounts[0].MountPath).To(Equal(controller.WorkspaceMountPath))

			By("Verifying the main container volume mount and workingDir")
			mainContainer := createdJob.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(1))
			Expect(mainContainer.VolumeMounts[0].Name).To(Equal(controller.WorkspaceVolumeName))
			Expect(mainContainer.VolumeMounts[0].MountPath).To(Equal(controller.WorkspaceMountPath))
			Expect(mainContainer.WorkingDir).To(Equal("/workspace/repo"))
		})
	})

	Context("When creating a Task with workspace and secretRef", func() {
		It("Should create a Job with GITHUB_TOKEN env var in both init and main containers", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-workspace-secret",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Secret with GITHUB_TOKEN")
			ghSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-token",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"GITHUB_TOKEN": "test-gh-token",
				},
			}
			Expect(k8sClient.Create(ctx, ghSecret)).Should(Succeed())

			By("Creating a Workspace resource with secretRef")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-secret",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
					SecretRef: &kelosv1alpha1.SecretReference{
						Name: "github-token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with workspace ref")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-secret",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a PR",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-workspace-secret",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the main container uses uniform interface")
			mainContainer := createdJob.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.Command).To(Equal([]string{"/kelos_entrypoint.sh"}))
			Expect(mainContainer.Args).To(Equal([]string{"Create a PR"}))

			By("Verifying the main container has KELOS_AGENT_TYPE, ANTHROPIC_API_KEY, KELOS_BASE_BRANCH, GITHUB_TOKEN, and GH_TOKEN env vars")
			Expect(mainContainer.Env).To(HaveLen(5))
			Expect(mainContainer.Env[0].Name).To(Equal("KELOS_AGENT_TYPE"))
			Expect(mainContainer.Env[0].Value).To(Equal("claude-code"))
			Expect(mainContainer.Env[1].Name).To(Equal("ANTHROPIC_API_KEY"))
			Expect(mainContainer.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("anthropic-api-key"))
			Expect(mainContainer.Env[2].Name).To(Equal("KELOS_BASE_BRANCH"))
			Expect(mainContainer.Env[2].Value).To(Equal("main"))
			Expect(mainContainer.Env[3].Name).To(Equal("GITHUB_TOKEN"))
			Expect(mainContainer.Env[3].ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
			Expect(mainContainer.Env[3].ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))
			Expect(mainContainer.Env[4].Name).To(Equal("GH_TOKEN"))
			Expect(mainContainer.Env[4].ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
			Expect(mainContainer.Env[4].ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))

			By("Verifying the init container has GITHUB_TOKEN, GH_TOKEN env vars and credential helper")
			Expect(createdJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := createdJob.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Env).To(HaveLen(2))
			Expect(initContainer.Env[0].Name).To(Equal("GITHUB_TOKEN"))
			Expect(initContainer.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
			Expect(initContainer.Env[0].ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))
			Expect(initContainer.Env[1].Name).To(Equal("GH_TOKEN"))
			Expect(initContainer.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
			Expect(initContainer.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))

			By("Verifying the init container uses credential helper for git auth")
			Expect(initContainer.Command).To(HaveLen(3))
			Expect(initContainer.Command[0]).To(Equal("sh"))
			Expect(initContainer.Command[2]).To(ContainSubstring("git -c credential.helper="))
			Expect(initContainer.Command[2]).To(ContainSubstring("git -C /workspace/repo config credential.helper"))
			Expect(initContainer.Args).To(Equal([]string{
				"--", "clone", "--branch", "main", "--no-single-branch", "--depth", "1",
				"--", "https://github.com/example/repo.git", "/workspace/repo",
			}))
		})
	})

	Context("When creating a Task with workspace without ref", func() {
		It("Should create a Job with git clone args omitting --branch", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-workspace-noref",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace resource without ref")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-noref",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with workspace ref but no git ref")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-noref",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Review the code",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-workspace-noref",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the init container args omit --branch")
			Expect(createdJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := createdJob.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Args).To(Equal([]string{
				"clone", "--no-single-branch", "--depth", "1",
				"--", "https://github.com/example/repo.git", "/workspace/repo",
			}))

			By("Verifying the init container runs as claude user")
			Expect(initContainer.SecurityContext).NotTo(BeNil())
			Expect(initContainer.SecurityContext.RunAsUser).NotTo(BeNil())
			Expect(*initContainer.SecurityContext.RunAsUser).To(Equal(controller.ClaudeCodeUID))

			By("Verifying the pod security context sets FSGroup")
			Expect(createdJob.Spec.Template.Spec.SecurityContext).NotTo(BeNil())
			Expect(createdJob.Spec.Template.Spec.SecurityContext.FSGroup).NotTo(BeNil())
			Expect(*createdJob.Spec.Template.Spec.SecurityContext.FSGroup).To(Equal(controller.ClaudeCodeUID))
		})
	})

	Context("When creating a Task with TTL", func() {
		It("Should delete the Task after TTL expires", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-ttl",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task with TTL")
			ttl := int32(3) // 3 second TTL
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-ttl",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					TTLSecondsAfterFinished: &ttl,
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Simulating Job completion")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
					return err
				}
				createdJob.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, createdJob)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task reaches Succeeded before TTL deletion")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					// Task already deleted by TTL, which implies it reached a terminal phase
					return true
				}
				return createdTask.Status.Phase == kelosv1alpha1.TaskPhaseSucceeded
			}, timeout, interval).Should(BeTrue())

			By("Verifying the Task is automatically deleted after TTL")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				return err != nil
			}, 2*timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a Task with TTL of zero", func() {
		It("Should delete the Task immediately after it finishes", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-ttl-zero",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task with TTL=0")
			ttl := int32(0)
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-ttl-zero",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					TTLSecondsAfterFinished: &ttl,
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Simulating Job completion")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
					return err
				}
				createdJob.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, createdJob)
			}, timeout, interval).Should(Succeed())

			By("Verifying the Task is deleted immediately after finishing")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				return err != nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a Task without TTL", func() {
		It("Should not delete the Task after it finishes", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-no-ttl",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task without TTL")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-no-ttl",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Simulating Job completion")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
					return err
				}
				createdJob.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, createdJob)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task reaches Succeeded")
			Eventually(func() kelosv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseSucceeded))

			By("Verifying the Task is NOT deleted after waiting")
			Consistently(func() error {
				return k8sClient.Get(ctx, taskLookupKey, createdTask)
			}, 3*time.Second, interval).Should(Succeed())
		})
	})

	Context("When creating a Task with a custom image and workspace", func() {
		It("Should create a Job using the custom image with uniform interface", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-custom-image",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace resource")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with custom image")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-custom-image",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Fix the bug",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					Model: "gpt-4",
					Image: "my-custom-agent:v1",
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-workspace",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the custom image is used with uniform interface")
			container := createdJob.Spec.Template.Spec.Containers[0]
			Expect(container.Image).To(Equal("my-custom-agent:v1"))
			Expect(container.Command).To(Equal([]string{"/kelos_entrypoint.sh"}))
			Expect(container.Args).To(Equal([]string{"Fix the bug"}))

			By("Verifying KELOS_MODEL and KELOS_AGENT_TYPE are set")
			Expect(container.Env).To(HaveLen(4))
			Expect(container.Env[0].Name).To(Equal("KELOS_MODEL"))
			Expect(container.Env[0].Value).To(Equal("gpt-4"))
			Expect(container.Env[1].Name).To(Equal("KELOS_AGENT_TYPE"))
			Expect(container.Env[1].Value).To(Equal("claude-code"))

			By("Verifying workspace volume mount and working dir")
			Expect(container.VolumeMounts).To(HaveLen(1))
			Expect(container.VolumeMounts[0].Name).To(Equal(controller.WorkspaceVolumeName))
			Expect(container.WorkingDir).To(Equal("/workspace/repo"))

			By("Verifying init container runs as shared UID")
			Expect(createdJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := createdJob.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.SecurityContext.RunAsUser).NotTo(BeNil())
			Expect(*initContainer.SecurityContext.RunAsUser).To(Equal(controller.ClaudeCodeUID))
		})
	})

	Context("When creating a Task with a GitHub Enterprise workspace", func() {
		It("Should create a Job with GH_HOST env var", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-ghe-workspace",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Secret with GITHUB_TOKEN")
			ghSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-token",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"GITHUB_TOKEN": "test-gh-token",
				},
			}
			Expect(k8sClient.Create(ctx, ghSecret)).Should(Succeed())

			By("Creating a Workspace resource with GitHub Enterprise URL")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ghe-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.example.com/my-org/my-repo.git",
					Ref:  "main",
					SecretRef: &kelosv1alpha1.SecretReference{
						Name: "github-token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task referencing the GHE workspace")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ghe-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Fix the bug",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-ghe-workspace",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the main container has GH_HOST and GH_ENTERPRISE_TOKEN env vars for enterprise host")
			mainContainer := createdJob.Spec.Template.Spec.Containers[0]
			var ghHostFound, ghEnterpriseTokenFound bool
			for _, env := range mainContainer.Env {
				if env.Name == "GH_HOST" {
					ghHostFound = true
					Expect(env.Value).To(Equal("github.example.com"))
				}
				if env.Name == "GH_ENTERPRISE_TOKEN" {
					ghEnterpriseTokenFound = true
					Expect(env.ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
					Expect(env.ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))
				}
				Expect(env.Name).NotTo(Equal("GH_TOKEN"), "GH_TOKEN should not be set for enterprise workspace")
			}
			Expect(ghHostFound).To(BeTrue(), "Expected GH_HOST env var in main container")
			Expect(ghEnterpriseTokenFound).To(BeTrue(), "Expected GH_ENTERPRISE_TOKEN env var in main container")

			By("Verifying the init container has GH_HOST and GH_ENTERPRISE_TOKEN env vars")
			Expect(createdJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := createdJob.Spec.Template.Spec.InitContainers[0]
			var initGHHostFound, initGHEnterpriseTokenFound bool
			for _, env := range initContainer.Env {
				if env.Name == "GH_HOST" {
					initGHHostFound = true
					Expect(env.Value).To(Equal("github.example.com"))
				}
				if env.Name == "GH_ENTERPRISE_TOKEN" {
					initGHEnterpriseTokenFound = true
					Expect(env.ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
					Expect(env.ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))
				}
				Expect(env.Name).NotTo(Equal("GH_TOKEN"), "GH_TOKEN should not be set in init container for enterprise workspace")
			}
			Expect(initGHHostFound).To(BeTrue(), "Expected GH_HOST env var in init container")
			Expect(initGHEnterpriseTokenFound).To(BeTrue(), "Expected GH_ENTERPRISE_TOKEN env var in init container")
		})
	})

	Context("When creating a Codex Task with API key credentials", func() {
		It("Should create a Job with CODEX_API_KEY env var", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-codex-apikey",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with Codex API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "codex-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"CODEX_API_KEY": "test-codex-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Codex Task")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-codex-task",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "codex",
					Prompt: "Fix the bug",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "codex-api-key",
						},
					},
					Model: "gpt-4.1",
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}

			By("Verifying the Task has a finalizer")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return false
				}
				for _, f := range createdTask.Finalizers {
					if f == "kelos.dev/finalizer" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the Job spec")
			Expect(createdJob.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := createdJob.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("codex"))
			Expect(container.Image).To(Equal(controller.CodexImage))
			Expect(container.Command).To(Equal([]string{"/kelos_entrypoint.sh"}))
			Expect(container.Args).To(Equal([]string{"Fix the bug"}))

			By("Verifying the Job has KELOS_MODEL, KELOS_AGENT_TYPE, and CODEX_API_KEY env vars")
			Expect(container.Env).To(HaveLen(3))
			Expect(container.Env[0].Name).To(Equal("KELOS_MODEL"))
			Expect(container.Env[0].Value).To(Equal("gpt-4.1"))
			Expect(container.Env[1].Name).To(Equal("KELOS_AGENT_TYPE"))
			Expect(container.Env[1].Value).To(Equal("codex"))
			Expect(container.Env[2].Name).To(Equal("CODEX_API_KEY"))
			Expect(container.Env[2].ValueFrom.SecretKeyRef.Name).To(Equal("codex-api-key"))
			Expect(container.Env[2].ValueFrom.SecretKeyRef.Key).To(Equal("CODEX_API_KEY"))

			By("Verifying the Job has owner reference")
			Expect(createdJob.OwnerReferences).To(HaveLen(1))
			Expect(createdJob.OwnerReferences[0].Name).To(Equal(task.Name))
			Expect(createdJob.OwnerReferences[0].Kind).To(Equal("Task"))
		})
	})

	Context("When creating a Codex Task with workspace", func() {
		It("Should create a Job with workspace volume and CODEX_API_KEY", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-codex-workspace",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with Codex API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "codex-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"CODEX_API_KEY": "test-codex-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace resource")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-codex-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Codex Task with workspace ref")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-codex-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "codex",
					Prompt: "Refactor the module",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "codex-api-key",
						},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-codex-workspace",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the main container uses codex image with uniform interface")
			mainContainer := createdJob.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.Name).To(Equal("codex"))
			Expect(mainContainer.Command).To(Equal([]string{"/kelos_entrypoint.sh"}))
			Expect(mainContainer.Args).To(Equal([]string{"Refactor the module"}))

			By("Verifying the main container has KELOS_AGENT_TYPE, CODEX_API_KEY, and KELOS_BASE_BRANCH env vars")
			Expect(mainContainer.Env).To(HaveLen(3))
			Expect(mainContainer.Env[0].Name).To(Equal("KELOS_AGENT_TYPE"))
			Expect(mainContainer.Env[0].Value).To(Equal("codex"))
			Expect(mainContainer.Env[1].Name).To(Equal("CODEX_API_KEY"))
			Expect(mainContainer.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("codex-api-key"))
			Expect(mainContainer.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("CODEX_API_KEY"))
			Expect(mainContainer.Env[2].Name).To(Equal("KELOS_BASE_BRANCH"))
			Expect(mainContainer.Env[2].Value).To(Equal("main"))

			By("Verifying the init container")
			Expect(createdJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := createdJob.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Name).To(Equal("git-clone"))

			By("Verifying the workspace volume mount and working dir")
			Expect(mainContainer.VolumeMounts).To(HaveLen(1))
			Expect(mainContainer.VolumeMounts[0].Name).To(Equal(controller.WorkspaceVolumeName))
			Expect(mainContainer.WorkingDir).To(Equal("/workspace/repo"))
		})
	})

	Context("When creating a Codex Task with OAuth credentials", func() {
		It("Should create a Job with CODEX_AUTH_JSON env var", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-codex-oauth",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with CODEX_AUTH_JSON")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "codex-oauth-secret",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"CODEX_AUTH_JSON": `{"token":"test-token"}`,
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Codex Task with OAuth credentials")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-codex-oauth",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "codex",
					Prompt: "Review the code",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeOAuth,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "codex-oauth-secret",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the Job has CODEX_AUTH_JSON env var")
			container := createdJob.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("codex"))
			Expect(container.Env).To(HaveLen(2))
			Expect(container.Env[0].Name).To(Equal("KELOS_AGENT_TYPE"))
			Expect(container.Env[0].Value).To(Equal("codex"))
			Expect(container.Env[1].Name).To(Equal("CODEX_AUTH_JSON"))
			Expect(container.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("codex-oauth-secret"))
			Expect(container.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("CODEX_AUTH_JSON"))
		})
	})

	Context("When creating an OpenCode Task with API key credentials", func() {
		It("Should create a Job with OPENCODE_API_KEY env var", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-opencode-apikey",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with OpenCode API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "opencode-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"OPENCODE_API_KEY": "test-opencode-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating an OpenCode Task")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-opencode-task",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "opencode",
					Prompt: "Fix the bug",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "opencode-api-key",
						},
					},
					Model: "anthropic/claude-sonnet-4-20250514",
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}

			By("Verifying the Task has a finalizer")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return false
				}
				for _, f := range createdTask.Finalizers {
					if f == "kelos.dev/finalizer" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the Job spec")
			Expect(createdJob.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := createdJob.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("opencode"))
			Expect(container.Image).To(Equal(controller.OpenCodeImage))
			Expect(container.Command).To(Equal([]string{"/kelos_entrypoint.sh"}))
			Expect(container.Args).To(Equal([]string{"Fix the bug"}))

			By("Verifying the Job has KELOS_MODEL, KELOS_AGENT_TYPE, and OPENCODE_API_KEY env vars")
			Expect(container.Env).To(HaveLen(3))
			Expect(container.Env[0].Name).To(Equal("KELOS_MODEL"))
			Expect(container.Env[0].Value).To(Equal("anthropic/claude-sonnet-4-20250514"))
			Expect(container.Env[1].Name).To(Equal("KELOS_AGENT_TYPE"))
			Expect(container.Env[1].Value).To(Equal("opencode"))
			Expect(container.Env[2].Name).To(Equal("OPENCODE_API_KEY"))
			Expect(container.Env[2].ValueFrom.SecretKeyRef.Name).To(Equal("opencode-api-key"))
			Expect(container.Env[2].ValueFrom.SecretKeyRef.Key).To(Equal("OPENCODE_API_KEY"))

			By("Verifying the Job has owner reference")
			Expect(createdJob.OwnerReferences).To(HaveLen(1))
			Expect(createdJob.OwnerReferences[0].Name).To(Equal(task.Name))
			Expect(createdJob.OwnerReferences[0].Kind).To(Equal("Task"))
		})
	})

	Context("When creating a Task with a nonexistent workspace", func() {
		It("Should not create a Job and keep retrying", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-workspace-missing",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task referencing a nonexistent Workspace")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-missing",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Fix the bug",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "nonexistent-workspace",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying no Job is created while workspace is missing")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Consistently(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err != nil
			}, 3*time.Second, interval).Should(BeTrue())

			By("Verifying the Task is not marked as Failed")
			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}

			Consistently(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return true
				}
				return createdTask.Status.Phase != kelosv1alpha1.TaskPhaseFailed
			}, 3*time.Second, interval).Should(BeTrue())

			By("Creating the Workspace and verifying the Job is eventually created")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nonexistent-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a Task with workspace using GitHub App secret", func() {
		It("Should create a generated token Secret and Job referencing it", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-github-app",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			credSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, credSecret)).Should(Succeed())

			By("Generating a test RSA key for GitHub App")
			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			Expect(err).NotTo(HaveOccurred())
			keyPEM := pem.EncodeToMemory(&pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
			})

			By("Creating a Secret with GitHub App credentials")
			ghAppSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-app-creds",
					Namespace: ns.Name,
				},
				Data: map[string][]byte{
					"appID":          []byte("12345"),
					"installationID": []byte("67890"),
					"privateKey":     keyPEM,
				},
			}
			Expect(k8sClient.Create(ctx, ghAppSecret)).Should(Succeed())

			By("Creating a Workspace with GitHub App secretRef")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-app",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
					SecretRef: &kelosv1alpha1.SecretReference{
						Name: "github-app-creds",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with workspace ref")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-github-app",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Fix the bug",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-workspace-app",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a generated token Secret is created")
			tokenSecretKey := types.NamespacedName{
				Name:      task.Name + "-github-token",
				Namespace: ns.Name,
			}
			tokenSecret := &corev1.Secret{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tokenSecretKey, tokenSecret)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(tokenSecret.Data).To(HaveKey("GITHUB_TOKEN"))
			Expect(string(tokenSecret.Data["GITHUB_TOKEN"])).To(Equal("ghs_mock_installation_token"))

			By("Verifying the token Secret has owner reference to the Task")
			Expect(tokenSecret.OwnerReferences).To(HaveLen(1))
			Expect(tokenSecret.OwnerReferences[0].Name).To(Equal(task.Name))
			Expect(tokenSecret.OwnerReferences[0].Kind).To(Equal("Task"))

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the Job references the generated token Secret, not the App Secret")
			mainContainer := createdJob.Spec.Template.Spec.Containers[0]
			var githubTokenEnv *corev1.EnvVar
			for i, env := range mainContainer.Env {
				if env.Name == "GITHUB_TOKEN" {
					githubTokenEnv = &mainContainer.Env[i]
					break
				}
			}
			Expect(githubTokenEnv).NotTo(BeNil())
			Expect(githubTokenEnv.ValueFrom.SecretKeyRef.Name).To(Equal(task.Name + "-github-token"))
			Expect(githubTokenEnv.ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))
		})
	})

	Context("When a Task goes through its lifecycle", func() {
		It("Should emit Kubernetes Events for key state transitions", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-events",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-events",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Test events",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			By("Waiting for Job to be created")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying TaskCreated event is emitted")
			Eventually(func() *corev1.Event {
				return findEvent(ns.Name, task.Name, "TaskCreated")
			}, timeout, interval).ShouldNot(BeNil())

			createdEvent := findEvent(ns.Name, task.Name, "TaskCreated")
			Expect(createdEvent.Type).To(Equal(corev1.EventTypeNormal))
			Expect(createdEvent.Message).To(ContainSubstring("Created Job"))

			By("Simulating Job running")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
					return err
				}
				createdJob.Status.Active = 1
				return k8sClient.Status().Update(ctx, createdJob)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task status is Running")
			Eventually(func() kelosv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseRunning))

			By("Verifying TaskRunning event is emitted")
			Eventually(func() *corev1.Event {
				return findEvent(ns.Name, task.Name, "TaskRunning")
			}, timeout, interval).ShouldNot(BeNil())

			runningEvent := findEvent(ns.Name, task.Name, "TaskRunning")
			Expect(runningEvent.Type).To(Equal(corev1.EventTypeNormal))

			By("Simulating Job completion")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
					return err
				}
				createdJob.Status.Active = 0
				createdJob.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, createdJob)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task status is Succeeded")
			Eventually(func() kelosv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseSucceeded))

			By("Verifying TaskSucceeded event is emitted")
			Eventually(func() *corev1.Event {
				return findEvent(ns.Name, task.Name, "TaskSucceeded")
			}, timeout, interval).ShouldNot(BeNil())

			succeededEvent := findEvent(ns.Name, task.Name, "TaskSucceeded")
			Expect(succeededEvent.Type).To(Equal(corev1.EventTypeNormal))
		})
	})

	Context("When a Task fails", func() {
		It("Should emit a TaskFailed warning event", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-events-failed",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-fail-event",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Test failure event",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			By("Waiting for Job to be created")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Simulating Job failure")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
					return err
				}
				createdJob.Status.Failed = 1
				return k8sClient.Status().Update(ctx, createdJob)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task status is Failed")
			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}
			Eventually(func() kelosv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseFailed))

			By("Verifying TaskFailed event is emitted")
			Eventually(func() *corev1.Event {
				return findEvent(ns.Name, task.Name, "TaskFailed")
			}, timeout, interval).ShouldNot(BeNil())

			failedEvent := findEvent(ns.Name, task.Name, "TaskFailed")
			Expect(failedEvent.Type).To(Equal(corev1.EventTypeWarning))
		})
	})

	Context("When creating a Task with dependsOn", func() {
		It("Should wait for dependency to succeed before creating Job", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-depends-on",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating dependency Task A")
			taskA := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "task-a",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Do something",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskA)).Should(Succeed())

			By("Creating Task B that depends on Task A")
			taskB := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "task-b",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:      "claude-code",
					Prompt:    "Continue work",
					DependsOn: []string{"task-a"},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskB)).Should(Succeed())

			By("Verifying Task B enters Waiting phase")
			taskBKey := types.NamespacedName{Name: "task-b", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskBKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseWaiting))

			By("Verifying no Job is created for Task B while dependency is not met")
			jobBKey := types.NamespacedName{Name: "task-b", Namespace: ns.Name}
			Consistently(func() bool {
				var job batchv1.Job
				return k8sClient.Get(ctx, jobBKey, &job) != nil
			}, 3*time.Second, interval).Should(BeTrue())

			By("Simulating Task A succeeding (update its Job status)")
			jobAKey := types.NamespacedName{Name: "task-a", Namespace: ns.Name}
			var jobA batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobAKey, &jobA) == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobAKey, &jobA); err != nil {
					return err
				}
				jobA.Status.Active = 1
				return k8sClient.Status().Update(ctx, &jobA)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobAKey, &jobA); err != nil {
					return err
				}
				jobA.Status.Active = 0
				jobA.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, &jobA)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task A reaches Succeeded")
			taskAKey := types.NamespacedName{Name: "task-a", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskAKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseSucceeded))

			By("Verifying Task B Job is eventually created")
			Eventually(func() bool {
				var job batchv1.Job
				return k8sClient.Get(ctx, jobBKey, &job) == nil
			}, 2*timeout, interval).Should(BeTrue())
		})
	})

	Context("When a dependency Task fails", func() {
		It("Should fail the dependent Task", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-dep-fail",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating dependency Task A")
			taskA := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dep-task-a",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Do something",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskA)).Should(Succeed())

			By("Creating Task B that depends on Task A")
			taskB := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dep-task-b",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:      "claude-code",
					Prompt:    "Continue work",
					DependsOn: []string{"dep-task-a"},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskB)).Should(Succeed())

			By("Simulating Task A failure")
			jobAKey := types.NamespacedName{Name: "dep-task-a", Namespace: ns.Name}
			var jobA batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobAKey, &jobA) == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobAKey, &jobA); err != nil {
					return err
				}
				jobA.Status.Failed = 1
				return k8sClient.Status().Update(ctx, &jobA)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task A reaches Failed")
			taskAKey := types.NamespacedName{Name: "dep-task-a", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskAKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseFailed))

			By("Verifying Task B transitions to Failed with dependency message")
			taskBKey := types.NamespacedName{Name: "dep-task-b", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskBKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, 2*timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseFailed))

			var taskBFinal kelosv1alpha1.Task
			Expect(k8sClient.Get(ctx, taskBKey, &taskBFinal)).Should(Succeed())
			Expect(taskBFinal.Status.Message).To(ContainSubstring("dep-task-a"))
		})
	})

	Context("When creating Tasks with same branch", func() {
		It("Should prevent concurrent execution on the same branch", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-branch-lock",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating Task A with branch feature-1")
			taskA := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "branch-task-a",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Task A",
					Branch: "feature-1",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskA)).Should(Succeed())

			By("Waiting for Task A Job to be created and simulating Running")
			jobAKey := types.NamespacedName{Name: "branch-task-a", Namespace: ns.Name}
			var jobA batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobAKey, &jobA) == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobAKey, &jobA); err != nil {
					return err
				}
				jobA.Status.Active = 1
				return k8sClient.Status().Update(ctx, &jobA)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task A is Running")
			taskAKey := types.NamespacedName{Name: "branch-task-a", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskAKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseRunning))

			By("Creating Task B with the same branch")
			taskB := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "branch-task-b",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Task B",
					Branch: "feature-1",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskB)).Should(Succeed())

			By("Verifying Task B enters Waiting phase")
			taskBKey := types.NamespacedName{Name: "branch-task-b", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskBKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseWaiting))

			By("Verifying Task B message mentions branch lock")
			var taskBWaiting kelosv1alpha1.Task
			Expect(k8sClient.Get(ctx, taskBKey, &taskBWaiting)).Should(Succeed())
			Expect(taskBWaiting.Status.Message).To(ContainSubstring("feature-1"))

			By("Simulating Task A completion")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobAKey, &jobA); err != nil {
					return err
				}
				jobA.Status.Active = 0
				jobA.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, &jobA)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task B Job is eventually created")
			jobBKey := types.NamespacedName{Name: "branch-task-b", Namespace: ns.Name}
			Eventually(func() bool {
				var job batchv1.Job
				return k8sClient.Get(ctx, jobBKey, &job) == nil
			}, 2*timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a Task with branch and workspace", func() {
		It("Should create a Job with branch-setup init container", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-branch-init",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace resource")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with branch and workspace")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "branch-init-task",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Work on branch",
					Branch: "feature-x",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-workspace",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobKey, createdJob) == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the Job has branch-setup init container")
			initContainers := createdJob.Spec.Template.Spec.InitContainers
			Expect(len(initContainers)).To(BeNumerically(">=", 2))

			var branchSetup *corev1.Container
			for i := range initContainers {
				if initContainers[i].Name == "branch-setup" {
					branchSetup = &initContainers[i]
					break
				}
			}
			Expect(branchSetup).NotTo(BeNil(), "Expected branch-setup init container")
			Expect(branchSetup.Command).To(Equal([]string{"sh", "-c", branchSetup.Command[2]}))
			Expect(branchSetup.Command[2]).To(ContainSubstring("$KELOS_BRANCH"))
			Expect(branchSetup.Command[2]).To(ContainSubstring("git checkout"))

			By("Verifying KELOS_BRANCH env var is set on branch-setup init container")
			var branchSetupEnvFound bool
			for _, env := range branchSetup.Env {
				if env.Name == "KELOS_BRANCH" {
					branchSetupEnvFound = true
					Expect(env.Value).To(Equal("feature-x"))
				}
			}
			Expect(branchSetupEnvFound).To(BeTrue(), "Expected KELOS_BRANCH env var on branch-setup")

			By("Verifying KELOS_BRANCH env var is set on main container")
			mainContainer := createdJob.Spec.Template.Spec.Containers[0]
			var kelosBranchFound bool
			for _, env := range mainContainer.Env {
				if env.Name == "KELOS_BRANCH" {
					kelosBranchFound = true
					Expect(env.Value).To(Equal("feature-x"))
				}
			}
			Expect(kelosBranchFound).To(BeTrue(), "Expected KELOS_BRANCH env var")
		})
	})

	Context("When creating Tasks with same branch but different workspaces", func() {
		It("Should not block each other", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-branch-diff-ws",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating two Workspace resources")
			wsA := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workspace-a",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/org/repo-a.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, wsA)).Should(Succeed())

			wsB := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workspace-b",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/org/repo-b.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, wsB)).Should(Succeed())

			By("Creating Task A on workspace-a with branch feature-1")
			taskA := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "diff-ws-task-a",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Task A",
					Branch: "feature-1",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "workspace-a"},
				},
			}
			Expect(k8sClient.Create(ctx, taskA)).Should(Succeed())

			By("Waiting for Task A Job and simulating Running")
			jobAKey := types.NamespacedName{Name: "diff-ws-task-a", Namespace: ns.Name}
			var jobA batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobAKey, &jobA) == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobAKey, &jobA); err != nil {
					return err
				}
				jobA.Status.Active = 1
				return k8sClient.Status().Update(ctx, &jobA)
			}, timeout, interval).Should(Succeed())

			By("Verifying Task A is Running")
			taskAKey := types.NamespacedName{Name: "diff-ws-task-a", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskAKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseRunning))

			By("Creating Task B on workspace-b with the same branch name")
			taskB := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "diff-ws-task-b",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Task B",
					Branch: "feature-1",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "workspace-b"},
				},
			}
			Expect(k8sClient.Create(ctx, taskB)).Should(Succeed())

			By("Verifying Task B Job is created (not blocked by Task A)")
			jobBKey := types.NamespacedName{Name: "diff-ws-task-b", Namespace: ns.Name}
			Eventually(func() bool {
				var job batchv1.Job
				return k8sClient.Get(ctx, jobBKey, &job) == nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating Tasks with circular dependencies", func() {
		It("Should fail with cycle detection message", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-cycle",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating Task A that depends on Task B")
			taskA := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cycle-task-a",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:      "claude-code",
					Prompt:    "Task A",
					DependsOn: []string{"cycle-task-b"},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskA)).Should(Succeed())

			By("Creating Task B that depends on Task A")
			taskB := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cycle-task-b",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:      "claude-code",
					Prompt:    "Task B",
					DependsOn: []string{"cycle-task-a"},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskB)).Should(Succeed())

			By("Verifying at least one task fails with circular dependency message")
			Eventually(func() bool {
				var tA kelosv1alpha1.Task
				var tB kelosv1alpha1.Task
				k8sClient.Get(ctx, types.NamespacedName{Name: "cycle-task-a", Namespace: ns.Name}, &tA)
				k8sClient.Get(ctx, types.NamespacedName{Name: "cycle-task-b", Namespace: ns.Name}, &tB)
				aFailed := tA.Status.Phase == kelosv1alpha1.TaskPhaseFailed && tA.Status.Message != "" && strings.Contains(tA.Status.Message, "Circular dependency")
				bFailed := tB.Status.Phase == kelosv1alpha1.TaskPhaseFailed && tB.Status.Message != "" && strings.Contains(tB.Status.Message, "Circular dependency")
				return aFailed || bFailed
			}, 2*timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a Task with prompt template referencing dependency outputs", func() {
		It("Should resolve template in Job prompt", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-prompt-tmpl",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating Task A and simulating it succeeding with outputs")
			taskA := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tmpl-task-a",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Generate outputs",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskA)).Should(Succeed())

			jobAKey := types.NamespacedName{Name: "tmpl-task-a", Namespace: ns.Name}
			var jobA batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobAKey, &jobA) == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobAKey, &jobA); err != nil {
					return err
				}
				jobA.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, &jobA)
			}, timeout, interval).Should(Succeed())

			taskAKey := types.NamespacedName{Name: "tmpl-task-a", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskAKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseSucceeded))

			By("Manually setting outputs on Task A")
			Eventually(func() error {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskAKey, &t); err != nil {
					return err
				}
				t.Status.Outputs = []string{"branch: feature-123", "pr: https://github.com/org/repo/pull/1"}
				return k8sClient.Status().Update(ctx, &t)
			}, timeout, interval).Should(Succeed())

			By("Creating Task B with prompt template")
			taskB := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tmpl-task-b",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:      "claude-code",
					Prompt:    `Review outputs: {{ index .Deps "tmpl-task-a" "Outputs" }}`,
					DependsOn: []string{"tmpl-task-a"},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskB)).Should(Succeed())

			By("Verifying Task B Job is created with resolved prompt")
			jobBKey := types.NamespacedName{Name: "tmpl-task-b", Namespace: ns.Name}
			var jobB batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobBKey, &jobB) == nil
			}, 2*timeout, interval).Should(BeTrue())

			mainContainer := jobB.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.Args[0]).To(ContainSubstring("branch: feature-123"))
		})
	})

	Context("When creating a Task with prompt template referencing dependency results", func() {
		It("Should resolve results template in Job prompt", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-results-tmpl",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating Task A and simulating it succeeding with results")
			taskA := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "results-task-a",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Generate results",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskA)).Should(Succeed())

			jobAKey := types.NamespacedName{Name: "results-task-a", Namespace: ns.Name}
			var jobA batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobAKey, &jobA) == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobAKey, &jobA); err != nil {
					return err
				}
				jobA.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, &jobA)
			}, timeout, interval).Should(Succeed())

			taskAKey := types.NamespacedName{Name: "results-task-a", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskAKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseSucceeded))

			By("Manually setting outputs on Task A (results are derived from key: value lines)")
			Eventually(func() error {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskAKey, &t); err != nil {
					return err
				}
				t.Status.Outputs = []string{
					"branch: feature-456",
					"pr: https://github.com/org/repo/pull/2",
				}
				t.Status.Results = controller.ResultsFromOutputs(t.Status.Outputs)
				return k8sClient.Status().Update(ctx, &t)
			}, timeout, interval).Should(Succeed())

			By("Creating Task B with prompt template referencing results")
			taskB := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "results-task-b",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:      "claude-code",
					Prompt:    `Review branch {{ index .Deps "results-task-a" "Results" "branch" }}`,
					DependsOn: []string{"results-task-a"},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, taskB)).Should(Succeed())

			By("Verifying Task B Job is created with resolved prompt")
			jobBKey := types.NamespacedName{Name: "results-task-b", Namespace: ns.Name}
			var jobB batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobBKey, &jobB) == nil
			}, 2*timeout, interval).Should(BeTrue())

			mainContainer := jobB.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.Args[0]).To(Equal("Review branch feature-456"))
		})
	})

	Context("When spawner creates Tasks with rendered branch templates", func() {
		It("Should set unique KELOS_BRANCH per task and not block each other", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-branch-template",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating Task for issue #42 with rendered branch kelos-task-42")
			task42 := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-42",
					Namespace: ns.Name,
					Labels: map[string]string{
						"kelos.dev/taskspawner": "my-spawner",
					},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Issue #42: Fix login bug",
					Branch: "kelos-task-42",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "test-workspace"},
				},
			}
			Expect(k8sClient.Create(ctx, task42)).Should(Succeed())

			By("Creating Task for issue #99 with rendered branch kelos-task-99")
			task99 := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-99",
					Namespace: ns.Name,
					Labels: map[string]string{
						"kelos.dev/taskspawner": "my-spawner",
					},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Issue #99: Add feature",
					Branch: "kelos-task-99",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "test-workspace"},
				},
			}
			Expect(k8sClient.Create(ctx, task99)).Should(Succeed())

			By("Verifying both Jobs are created (different branches should not block)")
			job42Key := types.NamespacedName{Name: "spawner-42", Namespace: ns.Name}
			job99Key := types.NamespacedName{Name: "spawner-99", Namespace: ns.Name}
			var job42, job99 batchv1.Job

			Eventually(func() bool {
				return k8sClient.Get(ctx, job42Key, &job42) == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() bool {
				return k8sClient.Get(ctx, job99Key, &job99) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying Job for issue #42 has KELOS_BRANCH=kelos-task-42")
			logJobSpec(&job42)
			mainContainer42 := job42.Spec.Template.Spec.Containers[0]
			var branch42 string
			for _, env := range mainContainer42.Env {
				if env.Name == "KELOS_BRANCH" {
					branch42 = env.Value
				}
			}
			Expect(branch42).To(Equal("kelos-task-42"))

			By("Verifying Job for issue #99 has KELOS_BRANCH=kelos-task-99")
			logJobSpec(&job99)
			mainContainer99 := job99.Spec.Template.Spec.Containers[0]
			var branch99 string
			for _, env := range mainContainer99.Env {
				if env.Name == "KELOS_BRANCH" {
					branch99 = env.Value
				}
			}
			Expect(branch99).To(Equal("kelos-task-99"))

			By("Verifying branch-setup init containers have correct branch values")
			var branchSetup42 *corev1.Container
			for i := range job42.Spec.Template.Spec.InitContainers {
				if job42.Spec.Template.Spec.InitContainers[i].Name == "branch-setup" {
					branchSetup42 = &job42.Spec.Template.Spec.InitContainers[i]
					break
				}
			}
			Expect(branchSetup42).NotTo(BeNil(), "Expected branch-setup init container for task 42")
			var branchSetupEnv42 string
			for _, env := range branchSetup42.Env {
				if env.Name == "KELOS_BRANCH" {
					branchSetupEnv42 = env.Value
				}
			}
			Expect(branchSetupEnv42).To(Equal("kelos-task-42"))

			var branchSetup99 *corev1.Container
			for i := range job99.Spec.Template.Spec.InitContainers {
				if job99.Spec.Template.Spec.InitContainers[i].Name == "branch-setup" {
					branchSetup99 = &job99.Spec.Template.Spec.InitContainers[i]
					break
				}
			}
			Expect(branchSetup99).NotTo(BeNil(), "Expected branch-setup init container for task 99")
			var branchSetupEnv99 string
			for _, env := range branchSetup99.Env {
				if env.Name == "KELOS_BRANCH" {
					branchSetupEnv99 = env.Value
				}
			}
			Expect(branchSetupEnv99).To(Equal("kelos-task-99"))
		})
	})

	Context("When spawner creates Tasks with same rendered branch", func() {
		It("Should enforce branch locking between spawned tasks", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-branch-tmpl-lock",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "lock-workspace",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating first Task with branch kelos-task-42")
			taskFirst := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-42-first",
					Namespace: ns.Name,
					Labels: map[string]string{
						"kelos.dev/taskspawner": "my-spawner",
					},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "First attempt at issue #42",
					Branch: "kelos-task-42",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "lock-workspace"},
				},
			}
			Expect(k8sClient.Create(ctx, taskFirst)).Should(Succeed())

			By("Waiting for first Task Job and simulating Running")
			jobFirstKey := types.NamespacedName{Name: "spawner-42-first", Namespace: ns.Name}
			var jobFirst batchv1.Job
			Eventually(func() bool {
				return k8sClient.Get(ctx, jobFirstKey, &jobFirst) == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobFirstKey, &jobFirst); err != nil {
					return err
				}
				jobFirst.Status.Active = 1
				return k8sClient.Status().Update(ctx, &jobFirst)
			}, timeout, interval).Should(Succeed())

			taskFirstKey := types.NamespacedName{Name: "spawner-42-first", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskFirstKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseRunning))

			By("Creating second Task with same branch kelos-task-42")
			taskSecond := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-42-second",
					Namespace: ns.Name,
					Labels: map[string]string{
						"kelos.dev/taskspawner": "my-spawner",
					},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Second attempt at issue #42",
					Branch: "kelos-task-42",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "anthropic-api-key"},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "lock-workspace"},
				},
			}
			Expect(k8sClient.Create(ctx, taskSecond)).Should(Succeed())

			By("Verifying second Task enters Waiting phase due to branch lock")
			taskSecondKey := types.NamespacedName{Name: "spawner-42-second", Namespace: ns.Name}
			Eventually(func() kelosv1alpha1.TaskPhase {
				var t kelosv1alpha1.Task
				if err := k8sClient.Get(ctx, taskSecondKey, &t); err != nil {
					return ""
				}
				return t.Status.Phase
			}, timeout, interval).Should(Equal(kelosv1alpha1.TaskPhaseWaiting))

			By("Verifying waiting message references the branch")
			var taskSecondWaiting kelosv1alpha1.Task
			Expect(k8sClient.Get(ctx, taskSecondKey, &taskSecondWaiting)).Should(Succeed())
			Expect(taskSecondWaiting.Status.Message).To(ContainSubstring("kelos-task-42"))

			By("Completing first Task")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, jobFirstKey, &jobFirst); err != nil {
					return err
				}
				jobFirst.Status.Active = 0
				jobFirst.Status.Succeeded = 1
				return k8sClient.Status().Update(ctx, &jobFirst)
			}, timeout, interval).Should(Succeed())

			By("Verifying second Task Job is eventually created after lock release")
			jobSecondKey := types.NamespacedName{Name: "spawner-42-second", Namespace: ns.Name}
			Eventually(func() bool {
				var job batchv1.Job
				return k8sClient.Get(ctx, jobSecondKey, &job) == nil
			}, 2*timeout, interval).Should(BeTrue())
		})
	})

	Context("When updating a Task spec after creation", func() {
		It("Should reject the update because Task spec is immutable", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-immutable",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Task")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-immutable",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Original prompt",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Waiting for the Task to be reconciled with a finalizer")
			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &kelosv1alpha1.Task{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return false
				}
				for _, f := range createdTask.Finalizers {
					if f == "kelos.dev/finalizer" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Attempting to update the Task prompt")
			createdTask.Spec.Prompt = "Updated prompt"
			err := k8sClient.Update(ctx, createdTask)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("immutable"))
		})
	})

	Context("When creating a Task with workspace remotes", func() {
		It("Should create a Job with a remote-setup init container", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-task-workspace-remotes",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with API key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-api-key",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"ANTHROPIC_API_KEY": "test-api-key",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace with remotes")
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-remotes",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/org/repo.git",
					Ref:  "main",
					Remotes: []kelosv1alpha1.GitRemote{
						{Name: "private", URL: "https://github.com/user/repo.git"},
						{Name: "downstream", URL: "https://github.com/vendor/repo.git"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task referencing the workspace")
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-remotes",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Work on feature",
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "test-workspace-remotes",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying a Job is created")
			jobLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdJob := &batchv1.Job{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, jobLookupKey, createdJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Logging the Job spec")
			logJobSpec(createdJob)

			By("Verifying the remote-setup init container exists")
			initContainers := createdJob.Spec.Template.Spec.InitContainers
			var remoteSetup *corev1.Container
			for i := range initContainers {
				if initContainers[i].Name == "remote-setup" {
					remoteSetup = &initContainers[i]
					break
				}
			}
			Expect(remoteSetup).NotTo(BeNil(), "Expected remote-setup init container")

			By("Verifying the remote-setup script adds both remotes")
			Expect(remoteSetup.Command).To(HaveLen(3))
			Expect(remoteSetup.Command[0]).To(Equal("sh"))
			Expect(remoteSetup.Command[2]).To(ContainSubstring("git remote add 'private' 'https://github.com/user/repo.git'"))
			Expect(remoteSetup.Command[2]).To(ContainSubstring("git remote add 'downstream' 'https://github.com/vendor/repo.git'"))

			By("Verifying init container ordering: git-clone before remote-setup")
			Expect(initContainers[0].Name).To(Equal("git-clone"))
			cloneFound := false
			for i, c := range initContainers {
				if c.Name == "remote-setup" {
					Expect(cloneFound).To(BeTrue(), "remote-setup should come after git-clone")
					_ = i
					break
				}
				if c.Name == "git-clone" {
					cloneFound = true
				}
			}
		})
	})
})
