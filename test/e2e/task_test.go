package e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

func describeAgentTests(cfg agentTestConfig) {
	Describe(fmt.Sprintf("Task [%s]", cfg.AgentType), func() {
		f := framework.NewFramework(fmt.Sprintf("task-%s", cfg.AgentType))

		BeforeEach(func() {
			if *cfg.SecretValue == "" {
				Skip(cfg.EnvVar + " not set")
			}
		})

		It("should run a Task to completion", func() {
			By("creating credentials secret")
			f.CreateSecret(cfg.SecretName, cfg.SecretKey+"="+*cfg.SecretValue)

			By("creating a Task")
			f.CreateTask(&kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name: "basic-task",
				},
				Spec: kelos.TaskSpec{
					Type:   cfg.AgentType,
					Model:  cfg.Model,
					Prompt: "Print 'Hello from Kelos e2e test' to stdout",
					Credentials: &kelos.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelos.SecretReference{Name: cfg.SecretName},
					},
				},
			})

			By("waiting for Job to be created")
			f.WaitForJobCreation("basic-task")

			By("waiting for Job to complete")
			f.WaitForJobCompletion("basic-task")

			By("verifying Task status is Succeeded")
			f.WaitForTaskPhase("basic-task", "Succeeded")

			By("getting Job logs")
			logs := f.GetJobLogs("basic-task")
			GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		})
	})

	Describe(fmt.Sprintf("Task with workspace [%s]", cfg.AgentType), func() {
		f := framework.NewFramework(fmt.Sprintf("ws-%s", cfg.AgentType))

		BeforeEach(func() {
			if *cfg.SecretValue == "" {
				Skip(cfg.EnvVar + " not set")
			}
		})

		It("should run a Task with workspace to completion", func() {
			By("creating credentials secret")
			f.CreateSecret(cfg.SecretName, cfg.SecretKey+"="+*cfg.SecretValue)

			By("creating a Workspace resource")
			f.CreateWorkspace(&kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "e2e-workspace",
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			})

			By("creating a Task with workspace ref")
			f.CreateTask(&kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ws-task",
				},
				Spec: kelos.TaskSpec{
					Type:   cfg.AgentType,
					Model:  cfg.Model,
					Prompt: "Create a file called 'test.txt' with the content 'hello' in the current directory and print 'done'",
					Credentials: &kelos.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelos.SecretReference{Name: cfg.SecretName},
					},
					WorkspaceRef: &kelos.WorkspaceReference{Name: "e2e-workspace"},
				},
			})

			By("waiting for Job to be created")
			f.WaitForJobCreation("ws-task")

			By("waiting for Job to complete")
			f.WaitForJobCompletion("ws-task")

			By("verifying Task status is Succeeded")
			f.WaitForTaskPhase("ws-task", "Succeeded")

			By("getting Job logs")
			logs := f.GetJobLogs("ws-task")
			GinkgoWriter.Printf("Job logs:\n%s\n", logs)

			By("verifying no permission errors in logs")
			Expect(logs).NotTo(ContainSubstring("permission denied"))
			Expect(logs).NotTo(ContainSubstring("Permission denied"))
			Expect(logs).NotTo(ContainSubstring("EACCES"))
		})
	})

	Describe(fmt.Sprintf("Task output capture [%s]", cfg.AgentType), func() {
		f := framework.NewFramework(fmt.Sprintf("output-%s", cfg.AgentType))

		BeforeEach(func() {
			if *cfg.SecretValue == "" {
				Skip(cfg.EnvVar + " not set")
			}
		})

		It("should populate Outputs and Results after task completes", func() {
			By("creating credentials secret")
			f.CreateSecret(cfg.SecretName, cfg.SecretKey+"="+*cfg.SecretValue)

			By("creating a Workspace resource")
			f.CreateWorkspace(&kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "e2e-outputs-workspace",
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			})

			By("creating a Task with workspace ref")
			f.CreateTask(&kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name: "outputs-task",
				},
				Spec: kelos.TaskSpec{
					Type:   cfg.AgentType,
					Model:  cfg.Model,
					Prompt: "Print 'hello' to stdout",
					Credentials: &kelos.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelos.SecretReference{Name: cfg.SecretName},
					},
					WorkspaceRef: &kelos.WorkspaceReference{Name: "e2e-outputs-workspace"},
				},
			})

			By("waiting for Job to be created")
			f.WaitForJobCreation("outputs-task")

			By("waiting for Job to complete")
			f.WaitForJobCompletion("outputs-task")

			By("verifying Task status is Succeeded")
			f.WaitForTaskPhase("outputs-task", "Succeeded")

			By("verifying output markers appear in Pod logs")
			logs := f.GetJobLogs("outputs-task")
			GinkgoWriter.Printf("Job logs:\n%s\n", logs)
			Expect(logs).To(ContainSubstring("---KELOS_OUTPUTS_START---"))
			Expect(logs).To(ContainSubstring("---KELOS_OUTPUTS_END---"))

			By("verifying Outputs field contains branch, commit, base-branch, and usage")
			outputs := f.GetTaskOutputs("outputs-task")
			Expect(outputs).To(ContainSubstring("branch: main"))
			Expect(outputs).To(MatchRegexp(`commit: [0-9a-f]{40}`))
			Expect(outputs).To(ContainSubstring("base-branch: main"))
			Expect(outputs).To(MatchRegexp(`input-tokens: \d+`))
			Expect(outputs).To(MatchRegexp(`output-tokens: \d+`))

			if cfg.SupportsCost {
				Expect(outputs).To(MatchRegexp(`cost-usd: [\d.]+`))
			}

			if cfg.SupportsResponse {
				By("verifying response is captured in Outputs")
				Expect(outputs).To(MatchRegexp(`response: [A-Za-z0-9+/]+=*`))
			}

			By("verifying Results map has structured entries")
			results := f.GetTaskResults("outputs-task")
			Expect(results).To(HaveKeyWithValue("branch", "main"))
			Expect(results).To(HaveKey("commit"))
			Expect(results["commit"]).To(MatchRegexp(`^[0-9a-f]{40}$`))
			Expect(results).To(HaveKeyWithValue("base-branch", "main"))
			Expect(results).To(HaveKey("input-tokens"))
			Expect(results).To(HaveKey("output-tokens"))

			if cfg.SupportsCost {
				Expect(results).To(HaveKey("cost-usd"))
			}

			if cfg.SupportsResponse {
				By("verifying response is captured in Results as base64")
				Expect(results).To(HaveKey("response"))
				Expect(results["response"]).To(MatchRegexp(`^[A-Za-z0-9+/]+=*$`))
			}
		})
	})

	Describe(fmt.Sprintf("Task dependency chain [%s]", cfg.AgentType), func() {
		f := framework.NewFramework(fmt.Sprintf("deps-%s", cfg.AgentType))

		BeforeEach(func() {
			if *cfg.SecretValue == "" {
				Skip(cfg.EnvVar + " not set")
			}
		})

		It("should start dependent task only after dependency succeeds", func() {
			By("creating credentials secret")
			f.CreateSecret(cfg.SecretName, cfg.SecretKey+"="+*cfg.SecretValue)

			By("creating Task A")
			f.CreateTask(&kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name: "dep-chain-a",
				},
				Spec: kelos.TaskSpec{
					Type:   cfg.AgentType,
					Model:  cfg.Model,
					Prompt: "Print 'Task A done' to stdout",
					Credentials: &kelos.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelos.SecretReference{Name: cfg.SecretName},
					},
				},
			})

			By("creating Task B that depends on Task A")
			f.CreateTask(&kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name: "dep-chain-b",
				},
				Spec: kelos.TaskSpec{
					Type:      cfg.AgentType,
					Model:     cfg.Model,
					Prompt:    "Print 'Task B done' to stdout",
					DependsOn: []string{"dep-chain-a"},
					Credentials: &kelos.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelos.SecretReference{Name: cfg.SecretName},
					},
				},
			})

			By("verifying Task B enters Waiting phase while Task A runs")
			f.WaitForTaskPhase("dep-chain-b", "Waiting")

			By("waiting for Task A to complete")
			f.WaitForJobCreation("dep-chain-a")
			f.WaitForJobCompletion("dep-chain-a")
			f.WaitForTaskPhase("dep-chain-a", "Succeeded")

			By("waiting for Task B to start and complete after Task A succeeds")
			f.WaitForJobCreation("dep-chain-b")
			f.WaitForJobCompletion("dep-chain-b")
			f.WaitForTaskPhase("dep-chain-b", "Succeeded")
		})
	})

	Describe(fmt.Sprintf("Task cleanup on failure [%s]", cfg.AgentType), func() {
		f := framework.NewFramework(fmt.Sprintf("cleanup-%s", cfg.AgentType))

		BeforeEach(func() {
			if *cfg.SecretValue == "" {
				Skip(cfg.EnvVar + " not set")
			}
		})

		It("should clean up namespace resources automatically", func() {
			By("creating credentials secret")
			f.CreateSecret(cfg.SecretName, cfg.SecretKey+"="+*cfg.SecretValue)

			By("creating a Task")
			f.CreateTask(&kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cleanup-task",
				},
				Spec: kelos.TaskSpec{
					Type:   cfg.AgentType,
					Model:  cfg.Model,
					Prompt: "Print 'Hello' to stdout",
					Credentials: &kelos.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelos.SecretReference{Name: cfg.SecretName},
					},
				},
			})

			By("verifying resources exist in the namespace")
			Eventually(func() []string {
				return f.ListTaskNames("")
			}, 30*time.Second, time.Second).Should(ContainElement("cleanup-task"))
		})
	})
}

// Register shared tests for each agent type.
var _ = func() bool {
	for _, cfg := range agentConfigs {
		describeAgentTests(cfg)
	}
	return true
}()

// Claude-code-only tests below.

var _ = Describe("Task with make available", func() {
	f := framework.NewFramework("make")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should have make command available in claude-code container", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Task that uses make")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "make-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Run 'make --version' and print the output",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("make-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("make-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("make-task", "Succeeded")

		By("getting Job logs")
		logs := f.GetJobLogs("make-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})

var _ = Describe("Task with workspace and secretRef", func() {
	f := framework.NewFramework("github")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
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
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-github-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo:      "https://github.com/kelos-dev/kelos.git",
				Ref:       "main",
				SecretRef: &kelos.SecretReference{Name: "workspace-credentials"},
			},
		})

		By("creating a Task with workspace ref")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "github-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Run 'gh auth status' and print the output",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &kelos.WorkspaceReference{Name: "e2e-github-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("github-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("github-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("github-task", "Succeeded")

		By("getting Job logs")
		logs := f.GetJobLogs("github-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})
