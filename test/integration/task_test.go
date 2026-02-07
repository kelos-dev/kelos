package integration

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
	"github.com/gjkim42/axon/internal/controller"
)

func logJobSpec(job *batchv1.Job) {
	spec, _ := json.MarshalIndent(job.Spec, "", "  ")
	GinkgoWriter.Printf("\n=== Job Spec ===\n%s\n================\n", spec)
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
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					Model: "claude-sonnet-4-20250514",
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &axonv1alpha1.Task{}

			By("Verifying the Task has a finalizer")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return false
				}
				for _, f := range createdTask.Finalizers {
					if f == "axon.io/finalizer" {
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
			Expect(container.Args).To(ContainElements(
				"--dangerously-skip-permissions",
				"--output-format", "stream-json",
				"--verbose",
				"-p", "Create a hello world program",
				"--model", "claude-sonnet-4-20250514",
			))

			By("Verifying the Job has API key env var")
			Expect(container.Env).To(HaveLen(1))
			Expect(container.Env[0].Name).To(Equal("ANTHROPIC_API_KEY"))
			Expect(container.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("anthropic-api-key"))

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
			Eventually(func() axonv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(axonv1alpha1.TaskPhaseRunning))

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
			Eventually(func() axonv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(axonv1alpha1.TaskPhaseSucceeded))

			By("Verifying Task has completion time")
			Expect(createdTask.Status.CompletionTime).NotTo(BeNil())

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
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-oauth-task",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeOAuth,
						SecretRef: axonv1alpha1.SecretReference{
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

			By("Verifying the Job has OAuth token env var")
			container := createdJob.Spec.Template.Spec.Containers[0]
			Expect(container.Env).To(HaveLen(1))
			Expect(container.Env[0].Name).To(Equal("CLAUDE_CODE_OAUTH_TOKEN"))
			Expect(container.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("claude-oauth"))
			Expect(container.Env[0].ValueFrom.SecretKeyRef.Key).To(Equal("CLAUDE_CODE_OAUTH_TOKEN"))
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
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with workspace ref")
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-ref",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Fix the bug",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &axonv1alpha1.WorkspaceReference{
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
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-secret",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
					SecretRef: &axonv1alpha1.SecretReference{
						Name: "github-token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with workspace ref")
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-secret",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a PR",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &axonv1alpha1.WorkspaceReference{
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

			By("Verifying the main container has ANTHROPIC_API_KEY, GITHUB_TOKEN, and GH_TOKEN env vars")
			mainContainer := createdJob.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.Env).To(HaveLen(3))
			Expect(mainContainer.Env[0].Name).To(Equal("ANTHROPIC_API_KEY"))
			Expect(mainContainer.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("anthropic-api-key"))
			Expect(mainContainer.Env[1].Name).To(Equal("GITHUB_TOKEN"))
			Expect(mainContainer.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
			Expect(mainContainer.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))
			Expect(mainContainer.Env[2].Name).To(Equal("GH_TOKEN"))
			Expect(mainContainer.Env[2].ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
			Expect(mainContainer.Env[2].ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))

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
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-noref",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a Task with workspace ref but no git ref")
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-noref",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Review the code",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &axonv1alpha1.WorkspaceReference{
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
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-ttl",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					TTLSecondsAfterFinished: &ttl,
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &axonv1alpha1.Task{}

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
				return createdTask.Status.Phase == axonv1alpha1.TaskPhaseSucceeded
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
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-ttl-zero",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					TTLSecondsAfterFinished: &ttl,
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &axonv1alpha1.Task{}

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
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-no-ttl",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Create a hello world program",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &axonv1alpha1.Task{}

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
			Eventually(func() axonv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(axonv1alpha1.TaskPhaseSucceeded))

			By("Verifying the Task is NOT deleted after waiting")
			Consistently(func() error {
				return k8sClient.Get(ctx, taskLookupKey, createdTask)
			}, 3*time.Second, interval).Should(Succeed())
		})
	})

	Context("When creating a Task with a nonexistent workspace", func() {
		It("Should fail with a meaningful error", func() {
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
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-missing",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "Fix the bug",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "anthropic-api-key",
						},
					},
					WorkspaceRef: &axonv1alpha1.WorkspaceReference{
						Name: "nonexistent-workspace",
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Verifying the Task status is Failed")
			taskLookupKey := types.NamespacedName{Name: task.Name, Namespace: ns.Name}
			createdTask := &axonv1alpha1.Task{}

			Eventually(func() axonv1alpha1.TaskPhase {
				err := k8sClient.Get(ctx, taskLookupKey, createdTask)
				if err != nil {
					return ""
				}
				return createdTask.Status.Phase
			}, timeout, interval).Should(Equal(axonv1alpha1.TaskPhaseFailed))

			Expect(createdTask.Status.Message).To(ContainSubstring("nonexistent-workspace"))
		})
	})
})
