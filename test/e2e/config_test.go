package e2e

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const configTaskName = "e2e-config-test-task"
const configOverrideTaskName = "e2e-config-override-task"

var _ = Describe("Config", func() {
	var configPath string

	BeforeEach(func() {
		By("cleaning up existing resources")
		kubectl("delete", "secret", "axon-credentials", "--ignore-not-found")
		kubectl("delete", "task", configTaskName, "--ignore-not-found")
		kubectl("delete", "task", configOverrideTaskName, "--ignore-not-found")
		kubectl("delete", "workspace", "axon-workspace", "--ignore-not-found")
		kubectl("delete", "workspace", "e2e-config-ws-override", "--ignore-not-found")
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("collecting debug info on failure")
			debugTask(configTaskName)
			debugTask(configOverrideTaskName)
		}

		By("cleaning up test resources")
		kubectl("delete", "task", configTaskName, "--ignore-not-found")
		kubectl("delete", "task", configOverrideTaskName, "--ignore-not-found")
		kubectl("delete", "workspace", "axon-workspace", "--ignore-not-found")
		kubectl("delete", "workspace", "e2e-config-ws-override", "--ignore-not-found")
		kubectl("delete", "secret", "axon-credentials", "--ignore-not-found")
		if configPath != "" {
			os.Remove(configPath)
		}
	})

	It("should run a Task using config file defaults", func() {
		By("writing a temp config file with oauthToken and inline workspace")
		dir := GinkgoT().TempDir()
		configPath = filepath.Join(dir, "config.yaml")
		configContent := "oauthToken: " + oauthToken + "\nworkspace:\n  repo: https://github.com/gjkim42/axon.git\n  ref: main\n"
		Expect(os.WriteFile(configPath, []byte(configContent), 0o644)).To(Succeed())

		By("creating a Task via CLI using config defaults (no --secret or --credential-type)")
		axon("run",
			"-p", "Run 'git log --oneline -1' and print the output",
			"--config", configPath,
			"--name", configTaskName,
		)

		By("waiting for Job to complete")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=complete", "job/"+configTaskName, "--timeout=10s")
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("verifying task status via CLI get")
		output := axonOutput("get", "task", configTaskName)
		Expect(output).To(ContainSubstring("Succeeded"))
		Expect(output).To(ContainSubstring("Workspace"))

		By("deleting task via CLI")
		axon("delete", "task", configTaskName)
		kubectl("delete", "workspace", "axon-workspace", "--ignore-not-found")
	})

	It("should allow CLI flags to override config file", func() {
		By("creating a Workspace resource for override")
		overrideWsYAML := `apiVersion: axon.io/v1alpha1
kind: Workspace
metadata:
  name: e2e-config-ws-override
spec:
  repo: https://github.com/gjkim42/axon.git
  ref: main
`
		Expect(kubectlWithInput(overrideWsYAML, "apply", "-f", "-")).To(Succeed())

		By("writing a temp config file with oauthToken and inline workspace (bad ref)")
		dir := GinkgoT().TempDir()
		configPath = filepath.Join(dir, "config.yaml")
		configContent := "oauthToken: " + oauthToken + "\nworkspace:\n  repo: https://github.com/gjkim42/axon.git\n  ref: v0.0.0\n"
		Expect(os.WriteFile(configPath, []byte(configContent), 0o644)).To(Succeed())

		By("creating a Task with CLI flag overriding config workspace")
		axon("run",
			"-p", "Run 'git log --oneline -1' and print the output",
			"--config", configPath,
			"--workspace", "e2e-config-ws-override",
			"--name", configOverrideTaskName,
		)

		By("waiting for Job to complete")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=complete", "job/"+configOverrideTaskName, "--timeout=10s")
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("verifying the CLI flag value was used")
		output := axonOutput("get", "task", configOverrideTaskName)
		Expect(output).To(ContainSubstring("Succeeded"))

		By("deleting task via CLI")
		axon("delete", "task", configOverrideTaskName)
		kubectl("delete", "workspace", "e2e-config-ws-override", "--ignore-not-found")
	})

	It("should initialize config file via init command", func() {
		dir := GinkgoT().TempDir()
		configPath = filepath.Join(dir, "test-config.yaml")

		By("running axon init")
		axon("init", "--config", configPath)

		By("verifying file was created with template content")
		data, err := os.ReadFile(configPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring("oauthToken:"))
		Expect(string(data)).To(ContainSubstring("apiKey:"))

		By("running axon init again without --force (should fail)")
		cmd := axonCommand("init", "--config", configPath)
		Expect(cmd.Run()).To(HaveOccurred())

		By("running axon init with --force (should succeed)")
		axon("init", "--config", configPath, "--force")
	})
})
