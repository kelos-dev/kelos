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

func debugTask(name string) {
	GinkgoWriter.Println("=== Debug: Task status ===")
	kubectl("get", "task", name, "-o", "yaml")

	GinkgoWriter.Println("=== Debug: Job status ===")
	kubectl("get", "job", name, "-o", "yaml")

	GinkgoWriter.Println("=== Debug: Pod status ===")
	kubectl("get", "pods", "-l", "axon.io/task="+name, "-o", "wide")
	kubectl("describe", "pods", "-l", "axon.io/task="+name)

	GinkgoWriter.Println("=== Debug: Pod logs ===")
	kubectl("logs", "job/"+name, "--tail=100")

	GinkgoWriter.Println("=== Debug: Controller logs ===")
	kubectl("logs", "-n", "axon-system", "deployment/axon-controller-manager", "--tail=50")
}

const taskName = "e2e-test-task"

var _ = Describe("Task", func() {
	BeforeEach(func() {
		By("cleaning up existing resources")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
		kubectl("delete", "task", taskName, "--ignore-not-found")
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("collecting debug info on failure")
			debugTask(taskName)
		}

		By("cleaning up test resources")
		kubectl("delete", "task", taskName, "--ignore-not-found")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
	})

	It("should run a Task to completion", func() {
		By("creating OAuth credentials secret")
		Expect(kubectlWithInput("", "create", "secret", "generic", "claude-credentials",
			"--from-literal=CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)).To(Succeed())

		By("creating a Task")
		taskYAML := `apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: ` + taskName + `
spec:
  type: claude-code
  model: ` + testModel + `
  prompt: "Print 'Hello from Axon e2e test' to stdout"
  credentials:
    type: oauth
    secretRef:
      name: claude-credentials
`
		Expect(kubectlWithInput(taskYAML, "apply", "-f", "-")).To(Succeed())

		By("waiting for Job to be created")
		Eventually(func() error {
			return kubectlWithInput("", "get", "job", taskName)
		}, 30*time.Second, time.Second).Should(Succeed())

		By("waiting for Job to complete")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=complete", "job/"+taskName, "--timeout=10s")
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("verifying Task status is Succeeded")
		output := kubectlOutput("get", "task", taskName, "-o", "jsonpath={.status.phase}")
		Expect(output).To(Equal("Succeeded"))

		By("getting Job logs")
		logs := kubectlOutput("logs", "job/"+taskName)
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})

const workspaceTaskName = "e2e-test-workspace-task"

var _ = Describe("Task with workspace", func() {
	BeforeEach(func() {
		By("cleaning up existing resources")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
		kubectl("delete", "task", workspaceTaskName, "--ignore-not-found")
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("collecting debug info on failure")
			debugTask(workspaceTaskName)
		}

		By("cleaning up test resources")
		kubectl("delete", "task", workspaceTaskName, "--ignore-not-found")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
	})

	It("should run a Task with workspace to completion", func() {
		By("creating OAuth credentials secret")
		Expect(kubectlWithInput("", "create", "secret", "generic", "claude-credentials",
			"--from-literal=CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)).To(Succeed())

		By("creating a Task with workspace")
		taskYAML := `apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: ` + workspaceTaskName + `
spec:
  type: claude-code
  model: ` + testModel + `
  prompt: "Run 'git log --oneline -1' and print the output"
  credentials:
    type: oauth
    secretRef:
      name: claude-credentials
  workspace:
    repo: https://github.com/gjkim42/axon.git
    ref: main
`
		Expect(kubectlWithInput(taskYAML, "apply", "-f", "-")).To(Succeed())

		By("waiting for Job to be created")
		Eventually(func() error {
			return kubectlWithInput("", "get", "job", workspaceTaskName)
		}, 30*time.Second, time.Second).Should(Succeed())

		By("waiting for Job to complete")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=complete", "job/"+workspaceTaskName, "--timeout=10s")
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("verifying Task status is Succeeded")
		output := kubectlOutput("get", "task", workspaceTaskName, "-o", "jsonpath={.status.phase}")
		Expect(output).To(Equal("Succeeded"))

		By("getting Job logs")
		logs := kubectlOutput("logs", "job/"+workspaceTaskName)
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})

func kubectl(args ...string) {
	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	_ = cmd.Run()
}

func kubectlWithInput(input string, args ...string) error {
	cmd := exec.Command("kubectl", args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	return cmd.Run()
}

func kubectlOutput(args ...string) string {
	cmd := exec.Command("kubectl", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out.String())
}
