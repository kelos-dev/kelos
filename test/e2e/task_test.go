package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/test/e2e/framework"
)

var _ = Describe("Task", func() {
	f := framework.NewFramework("task")

	It("should run a Task to completion", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Task")
		f.CreateTask(&axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "basic-task",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Print 'Hello from Axon e2e test' to stdout",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("basic-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("basic-task")

		By("verifying Task status is Succeeded")
		Expect(f.GetTaskPhase("basic-task")).To(Equal("Succeeded"))

		By("getting Job logs")
		logs := f.GetJobLogs("basic-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})

var _ = Describe("Task with make available", func() {
	f := framework.NewFramework("make")

	It("should have make command available in claude-code container", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Task that uses make")
		f.CreateTask(&axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "make-task",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Run 'make --version' and print the output",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("make-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("make-task")

		By("verifying Task status is Succeeded")
		Expect(f.GetTaskPhase("make-task")).To(Equal("Succeeded"))

		By("getting Job logs")
		logs := f.GetJobLogs("make-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})

var _ = Describe("Task with workspace", func() {
	f := framework.NewFramework("ws")

	It("should run a Task with workspace to completion", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace resource")
		f.CreateWorkspace(&axonv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-workspace",
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/axon-core/axon.git",
				Ref:  "main",
			},
		})

		By("creating a Task with workspace ref")
		f.CreateTask(&axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ws-task",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Create a file called 'test.txt' with the content 'hello' in the current directory and print 'done'",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &axonv1alpha1.WorkspaceReference{Name: "e2e-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("ws-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("ws-task")

		By("verifying Task status is Succeeded")
		Expect(f.GetTaskPhase("ws-task")).To(Equal("Succeeded"))

		By("getting Job logs")
		logs := f.GetJobLogs("ws-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)

		By("verifying no permission errors in logs")
		Expect(logs).NotTo(ContainSubstring("permission denied"))
		Expect(logs).NotTo(ContainSubstring("Permission denied"))
		Expect(logs).NotTo(ContainSubstring("EACCES"))
	})
})

var _ = Describe("Task output capture", func() {
	f := framework.NewFramework("output")

	It("should populate Outputs with branch name after task completes", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace resource")
		f.CreateWorkspace(&axonv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-outputs-workspace",
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/axon-core/axon.git",
				Ref:  "main",
			},
		})

		By("creating a Task with workspace ref")
		f.CreateTask(&axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "outputs-task",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Run 'git branch --show-current' and print the output, then say done",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &axonv1alpha1.WorkspaceReference{Name: "e2e-outputs-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("outputs-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("outputs-task")

		By("verifying Task status is Succeeded")
		Expect(f.GetTaskPhase("outputs-task")).To(Equal("Succeeded"))

		By("verifying output markers appear in Pod logs")
		logs := f.GetJobLogs("outputs-task")
		Expect(logs).To(ContainSubstring("---AXON_OUTPUTS_START---"))
		Expect(logs).To(ContainSubstring("---AXON_OUTPUTS_END---"))
		Expect(logs).To(ContainSubstring("branch: main"))

		By("verifying Outputs field is populated in Task status")
		outputs := f.GetTaskOutputs("outputs-task")
		Expect(outputs).To(ContainSubstring("branch: main"))
	})
})

var _ = Describe("Task with workspace and secretRef", func() {
	f := framework.NewFramework("github")

	BeforeEach(func() {
		if githubToken == "" {
			Skip("GITHUB_TOKEN not set, skipping GitHub e2e tests")
		}
	})

	It("should run a Task with gh CLI available and GITHUB_TOKEN injected", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating workspace credentials secret")
		f.CreateSecret("workspace-credentials",
			"GITHUB_TOKEN="+githubToken)

		By("creating a Workspace resource with secretRef")
		f.CreateWorkspace(&axonv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-github-workspace",
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo:      "https://github.com/axon-core/axon.git",
				Ref:       "main",
				SecretRef: &axonv1alpha1.SecretReference{Name: "workspace-credentials"},
			},
		})

		By("creating a Task with workspace ref")
		f.CreateTask(&axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "github-task",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Run 'gh auth status' and print the output",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &axonv1alpha1.WorkspaceReference{Name: "e2e-github-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("github-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("github-task")

		By("verifying Task status is Succeeded")
		Expect(f.GetTaskPhase("github-task")).To(Equal("Succeeded"))

		By("getting Job logs")
		logs := f.GetJobLogs("github-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})

var _ = Describe("Task dependency chain", func() {
	f := framework.NewFramework("deps")

	It("should start dependent task only after dependency succeeds", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating Task A")
		f.CreateTask(&axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "dep-chain-a",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Print 'Task A done' to stdout",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("creating Task B that depends on Task A")
		f.CreateTask(&axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "dep-chain-b",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:      "claude-code",
				Model:     testModel,
				Prompt:    "Print 'Task B done' to stdout",
				DependsOn: []string{"dep-chain-a"},
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("verifying Task B enters Waiting phase while Task A runs")
		Eventually(func() string {
			return f.GetTaskPhase("dep-chain-b")
		}, 30*time.Second, time.Second).Should(Equal("Waiting"))

		By("waiting for Task A to complete")
		f.WaitForJobCreation("dep-chain-a")
		f.WaitForJobCompletion("dep-chain-a")
		Expect(f.GetTaskPhase("dep-chain-a")).To(Equal("Succeeded"))

		By("waiting for Task B to start and complete after Task A succeeds")
		f.WaitForJobCreation("dep-chain-b")
		f.WaitForJobCompletion("dep-chain-b")
		Expect(f.GetTaskPhase("dep-chain-b")).To(Equal("Succeeded"))
	})
})

var _ = Describe("Task cleanup on failure", func() {
	f := framework.NewFramework("cleanup")

	It("should clean up namespace resources automatically", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Task")
		f.CreateTask(&axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cleanup-task",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Print 'Hello' to stdout",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("verifying resources exist in the namespace")
		Eventually(func() []string {
			return f.ListTaskNames("")
		}, 30*time.Second, time.Second).Should(ContainElement("cleanup-task"))
	})
})
