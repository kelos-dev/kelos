package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/test/e2e/framework"
)

var _ = Describe("CLI", func() {
	f := framework.NewFramework("cli")

	It("should run a Task to completion", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Task via CLI")
		framework.Axon("run",
			"-n", f.Namespace,
			"-p", "Print 'Hello from Axon CLI e2e test' to stdout",
			"--secret", "claude-credentials",
			"--credential-type", "oauth",
			"--model", testModel,
			"--name", "cli-task",
		)

		By("waiting for Job to complete")
		f.WaitForJobCompletion("cli-task")

		By("verifying task status via CLI get (detail)")
		output := framework.AxonOutput("get", "task", "cli-task", "-n", f.Namespace)
		Expect(output).To(ContainSubstring("Succeeded"))

		By("verifying YAML output for a single task")
		output = framework.AxonOutput("get", "task", "cli-task", "-n", f.Namespace, "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: axon.io/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: Task"))
		Expect(output).To(ContainSubstring("name: cli-task"))

		By("verifying JSON output for a single task")
		output = framework.AxonOutput("get", "task", "cli-task", "-n", f.Namespace, "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "axon.io/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "Task"`))
		Expect(output).To(ContainSubstring(`"name": "cli-task"`))

		By("verifying task logs via CLI")
		logs := framework.AxonOutput("logs", "cli-task", "-n", f.Namespace)
		Expect(logs).NotTo(BeEmpty())

		By("deleting task via CLI")
		framework.Axon("delete", "task", "cli-task", "-n", f.Namespace)

		By("verifying task is no longer listed")
		output = framework.AxonOutput("get", "tasks", "-n", f.Namespace)
		Expect(output).NotTo(ContainSubstring("cli-task"))
	})

	It("should follow logs from task creation with -f", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Task and immediately following logs")
		framework.Axon("run",
			"-n", f.Namespace,
			"-p", "Print 'Hello from follow test' to stdout",
			"--secret", "claude-credentials",
			"--credential-type", "oauth",
			"--name", "cli-follow-task",
		)

		stdout, stderr := framework.AxonOutputWithStderr("logs", "cli-follow-task", "-n", f.Namespace, "-f")
		By("verifying stderr contains streaming status")
		Expect(stderr).To(ContainSubstring("Streaming container (claude-code) logs..."))
		By("verifying stderr contains result summary")
		Expect(stderr).To(ContainSubstring("[result]"))
		By("verifying stdout contains log output")
		Expect(stdout).NotTo(BeEmpty())
	})

	It("should run a Task with workspace to completion", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace resource")
		f.CreateWorkspace(&axonv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-cli-workspace",
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/axon-core/axon.git",
				Ref:  "main",
			},
		})

		By("creating a Task with workspace via CLI")
		framework.Axon("run",
			"-n", f.Namespace,
			"-p", "Run 'git log --oneline -1' and print the output",
			"--secret", "claude-credentials",
			"--credential-type", "oauth",
			"--model", testModel,
			"--workspace", "e2e-cli-workspace",
			"--name", "cli-ws-task",
		)

		By("waiting for Job to complete")
		f.WaitForJobCompletion("cli-ws-task")

		By("verifying task status via CLI get (detail)")
		output := framework.AxonOutput("get", "task", "cli-ws-task", "-n", f.Namespace, "--detail")
		Expect(output).To(ContainSubstring("Succeeded"))
		Expect(output).To(ContainSubstring("Workspace"))

		By("verifying task logs via CLI")
		logs := framework.AxonOutput("logs", "cli-ws-task", "-n", f.Namespace)
		Expect(logs).NotTo(BeEmpty())

		By("deleting task via CLI")
		framework.Axon("delete", "task", "cli-ws-task", "-n", f.Namespace)

		By("verifying task is no longer listed")
		output = framework.AxonOutput("get", "tasks", "-n", f.Namespace)
		Expect(output).NotTo(ContainSubstring("cli-ws-task"))
	})
})

var _ = Describe("create", func() {
	It("should fail without a resource type", func() {
		framework.AxonFail("create")
	})
})

var _ = Describe("delete", func() {
	It("should fail without a resource type", func() {
		framework.AxonFail("delete")
	})

	It("should fail for a nonexistent task", func() {
		framework.AxonFail("delete", "task", "nonexistent-task-name")
	})

	It("should fail for a nonexistent workspace", func() {
		framework.AxonFail("delete", "workspace", "nonexistent-workspace-name")
	})

	It("should fail for a nonexistent taskspawner", func() {
		framework.AxonFail("delete", "taskspawner", "nonexistent-spawner-name")
	})
})

var _ = Describe("get", func() {
	It("should fail without a resource type", func() {
		framework.AxonFail("get")
	})

	It("should succeed with 'tasks' alias", func() {
		framework.AxonOutput("get", "tasks")
	})

	It("should succeed with 'task' subcommand", func() {
		framework.AxonOutput("get", "task")
	})

	It("should succeed with 'workspaces' alias", func() {
		framework.AxonOutput("get", "workspaces")
	})

	It("should succeed with 'workspace' subcommand", func() {
		framework.AxonOutput("get", "workspace")
	})

	It("should fail for a nonexistent task", func() {
		framework.AxonFail("get", "task", "nonexistent-task-name")
	})

	It("should fail for a nonexistent workspace", func() {
		framework.AxonFail("get", "workspace", "nonexistent-workspace-name")
	})

	It("should output task list in YAML format", func() {
		output := framework.AxonOutput("get", "tasks", "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: axon.io/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: TaskList"))
	})

	It("should output task list in JSON format", func() {
		output := framework.AxonOutput("get", "tasks", "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "axon.io/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "TaskList"`))
	})

	It("should output workspace list in YAML format", func() {
		output := framework.AxonOutput("get", "workspaces", "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: axon.io/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: WorkspaceList"))
	})

	It("should output workspace list in JSON format", func() {
		output := framework.AxonOutput("get", "workspaces", "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "axon.io/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "WorkspaceList"`))
	})

	It("should fail with unknown output format", func() {
		framework.AxonFail("get", "tasks", "-o", "invalid")
	})
})

var _ = Describe("workspace CRUD", func() {
	f := framework.NewFramework("ws-crud")

	It("should create, get, and delete a workspace", func() {
		By("creating a workspace via CLI")
		framework.Axon("create", "workspace", "test-ws",
			"-n", f.Namespace,
			"--repo", "https://github.com/axon-core/axon.git",
			"--ref", "main",
		)

		By("verifying workspace exists via get")
		output := framework.AxonOutput("get", "workspace", "test-ws", "-n", f.Namespace)
		Expect(output).To(ContainSubstring("test-ws"))
		Expect(output).To(ContainSubstring("https://github.com/axon-core/axon.git"))

		By("verifying workspace in list")
		output = framework.AxonOutput("get", "workspaces", "-n", f.Namespace)
		Expect(output).To(ContainSubstring("test-ws"))

		By("verifying YAML output")
		output = framework.AxonOutput("get", "workspace", "test-ws", "-n", f.Namespace, "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: axon.io/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: Workspace"))
		Expect(output).To(ContainSubstring("name: test-ws"))

		By("verifying JSON output")
		output = framework.AxonOutput("get", "workspace", "test-ws", "-n", f.Namespace, "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "axon.io/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "Workspace"`))

		By("deleting workspace via CLI")
		framework.Axon("delete", "workspace", "test-ws", "-n", f.Namespace)

		By("verifying workspace is deleted")
		framework.AxonFail("get", "workspace", "test-ws", "-n", f.Namespace)
	})
})

var _ = Describe("agentconfig CRUD", func() {
	f := framework.NewFramework("ac-crud")

	It("should create and verify an agentconfig", func() {
		By("creating an agentconfig via CLI")
		framework.Axon("create", "agentconfig", "test-ac",
			"-n", f.Namespace,
			"--agents-md", "Follow TDD",
		)

		By("verifying agentconfig exists via typed client")
		ac, err := f.AxonClientset.ApiV1alpha1().AgentConfigs(f.Namespace).Get(
			context.TODO(), "test-ac", metav1.GetOptions{},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(ac.Spec.AgentsMD).To(Equal("Follow TDD"))
	})
})

var _ = Describe("CLI with namespace flag", func() {
	f := framework.NewFramework("cli-ns")

	It("should scope operations to the specified namespace", func() {
		By("creating OAuth credentials secret in the test namespace")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Task via CLI with namespace flag")
		framework.Axon("run",
			"-n", f.Namespace,
			"-p", "Print 'hello' to stdout",
			"--secret", "claude-credentials",
			"--credential-type", "oauth",
			"--model", testModel,
			"--name", "ns-task",
		)

		By("verifying task exists only in the test namespace")
		Eventually(func() string {
			return framework.AxonOutput("get", "tasks", "-n", f.Namespace)
		}, 30*time.Second, time.Second).Should(ContainSubstring("ns-task"))
	})
})
