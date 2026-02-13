package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const openCodeTaskName = "e2e-opencode-task"

// openCodeTestModel uses a free OpenCode model so e2e tests require no authentication.
const openCodeTestModel = "opencode/big-pickle"

var _ = Describe("OpenCode Task", func() {
	BeforeEach(func() {
		By("cleaning up existing resources")
		kubectl("delete", "secret", "opencode-credentials", "--ignore-not-found")
		kubectl("delete", "task", openCodeTaskName, "--ignore-not-found")
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("collecting debug info on failure")
			debugTask(openCodeTaskName)
		}

		By("cleaning up test resources")
		kubectl("delete", "task", openCodeTaskName, "--ignore-not-found")
		kubectl("delete", "secret", "opencode-credentials", "--ignore-not-found")
	})

	It("should run an OpenCode Task to completion", func() {
		By("creating credentials secret (empty key for free OpenCode model)")
		Expect(kubectlWithInput("", "create", "secret", "generic", "opencode-credentials",
			"--from-literal=OPENCODE_API_KEY=")).To(Succeed())

		By("creating an OpenCode Task")
		taskYAML := `apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: ` + openCodeTaskName + `
spec:
  type: opencode
  model: ` + openCodeTestModel + `
  prompt: "Print 'Hello from OpenCode e2e test' to stdout"
  credentials:
    type: api-key
    secretRef:
      name: opencode-credentials
`
		Expect(kubectlWithInput(taskYAML, "apply", "-f", "-")).To(Succeed())

		By("waiting for Job to be created")
		Eventually(func() error {
			return kubectlWithInput("", "get", "job", openCodeTaskName)
		}, 30*time.Second, time.Second).Should(Succeed())

		By("waiting for Job to complete")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=complete", "job/"+openCodeTaskName, "--timeout=10s")
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("verifying Task status is Succeeded")
		output := kubectlOutput("get", "task", openCodeTaskName, "-o", "jsonpath={.status.phase}")
		Expect(output).To(Equal("Succeeded"))

		By("getting Job logs")
		logs := kubectlOutput("logs", "job/"+openCodeTaskName)
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})
