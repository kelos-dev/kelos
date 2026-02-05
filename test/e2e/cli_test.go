package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const cliTaskName = "e2e-cli-test-task"
const cliWorkspaceTaskName = "e2e-cli-workspace-task"

var _ = Describe("CLI", func() {
	BeforeEach(func() {
		By("cleaning up existing resources")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
		kubectl("delete", "task", cliTaskName, "--ignore-not-found")
		kubectl("delete", "task", cliWorkspaceTaskName, "--ignore-not-found")
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("collecting debug info on failure")
			debugTask(cliTaskName)
			debugTask(cliWorkspaceTaskName)
		}

		By("cleaning up test resources")
		kubectl("delete", "task", cliTaskName, "--ignore-not-found")
		kubectl("delete", "task", cliWorkspaceTaskName, "--ignore-not-found")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
	})

	It("should run a Task to completion", func() {
		By("creating OAuth credentials secret")
		Expect(kubectlWithInput("", "create", "secret", "generic", "claude-credentials",
			"--from-literal=CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)).To(Succeed())

		By("creating a Task via CLI")
		axon("run",
			"-p", "Print 'Hello from Axon CLI e2e test' to stdout",
			"--secret", "claude-credentials",
			"--credential-type", "oauth",
			"--model", testModel,
			"--name", cliTaskName,
		)

		By("waiting for Job to complete")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=complete", "job/"+cliTaskName, "--timeout=10s")
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("verifying task status via CLI get (detail)")
		output := axonOutput("get", "task", cliTaskName)
		Expect(output).To(ContainSubstring("Succeeded"))

		By("verifying YAML output for a single task")
		output = axonOutput("get", "task", cliTaskName, "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: axon.io/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: Task"))
		Expect(output).To(ContainSubstring("name: " + cliTaskName))

		By("verifying JSON output for a single task")
		output = axonOutput("get", "task", cliTaskName, "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "axon.io/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "Task"`))
		Expect(output).To(ContainSubstring(`"name": "` + cliTaskName + `"`))

		By("verifying task logs via CLI")
		logs := axonOutput("logs", cliTaskName)
		Expect(logs).NotTo(BeEmpty())

		By("deleting task via CLI")
		axon("delete", cliTaskName)

		By("verifying task is no longer listed")
		output = axonOutput("get", "tasks")
		Expect(output).NotTo(ContainSubstring(cliTaskName))
	})

	It("should run a Task with workspace to completion", func() {
		By("creating OAuth credentials secret")
		Expect(kubectlWithInput("", "create", "secret", "generic", "claude-credentials",
			"--from-literal=CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)).To(Succeed())

		By("creating a Task with workspace via CLI")
		axon("run",
			"-p", "Run 'git log --oneline -1' and print the output",
			"--secret", "claude-credentials",
			"--credential-type", "oauth",
			"--model", testModel,
			"--workspace-repo", "https://github.com/gjkim42/axon.git",
			"--workspace-ref", "main",
			"--name", cliWorkspaceTaskName,
		)

		By("waiting for Job to complete")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=complete", "job/"+cliWorkspaceTaskName, "--timeout=10s")
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("verifying task status via CLI get (detail)")
		output := axonOutput("get", "task", cliWorkspaceTaskName)
		Expect(output).To(ContainSubstring("Succeeded"))
		Expect(output).To(ContainSubstring("Workspace Repo"))

		By("verifying task logs via CLI")
		logs := axonOutput("logs", cliWorkspaceTaskName)
		Expect(logs).NotTo(BeEmpty())

		By("deleting task via CLI")
		axon("delete", cliWorkspaceTaskName)

		By("verifying task is no longer listed")
		output = axonOutput("get", "tasks")
		Expect(output).NotTo(ContainSubstring(cliWorkspaceTaskName))
	})
})

var _ = Describe("get", func() {
	It("should fail without a resource type", func() {
		axonFail("get")
	})

	It("should succeed with 'tasks' alias", func() {
		axonOutput("get", "tasks")
	})

	It("should succeed with 'task' subcommand", func() {
		axonOutput("get", "task")
	})

	It("should fail for a nonexistent task", func() {
		axonFail("get", "task", "nonexistent-task-name")
	})

	It("should output task list in YAML format", func() {
		output := axonOutput("get", "tasks", "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: axon.io/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: TaskList"))
	})

	It("should output task list in JSON format", func() {
		output := axonOutput("get", "tasks", "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "axon.io/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "TaskList"`))
	})

	It("should fail with unknown output format", func() {
		axonFail("get", "tasks", "-o", "invalid")
	})
})

func axonBin() string {
	if bin := os.Getenv("AXON_BIN"); bin != "" {
		return bin
	}
	return "axon"
}

func axon(args ...string) {
	cmd := exec.Command(axonBin(), args...)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	err := cmd.Run()
	Expect(err).NotTo(HaveOccurred())
}

func axonOutput(args ...string) string {
	cmd := exec.Command(axonBin(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out.String())
}

func axonFail(args ...string) {
	cmd := exec.Command(axonBin(), args...)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	err := cmd.Run()
	Expect(err).To(HaveOccurred())
}
