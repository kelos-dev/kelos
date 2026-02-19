package e2e

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/test/e2e/framework"
)

var _ = Describe("Config", func() {
	f := framework.NewFramework("config")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should run a Task using config file defaults", func() {
		By("writing a temp config file with oauthToken and inline workspace")
		dir := GinkgoT().TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		configContent := "oauthToken: " + oauthToken + "\nnamespace: " + f.Namespace + "\nworkspace:\n  repo: https://github.com/axon-core/axon.git\n  ref: main\n"
		Expect(os.WriteFile(configPath, []byte(configContent), 0o644)).To(Succeed())

		By("creating a Task via CLI using config defaults (no --secret or --credential-type)")
		framework.Axon("run",
			"-p", "Run 'git log --oneline -1' and print the output",
			"--config", configPath,
			"--name", "config-task",
		)

		By("waiting for Job to complete")
		f.WaitForJobCompletion("config-task")

		By("verifying task status via CLI get")
		output := framework.AxonOutput("get", "task", "config-task", "-n", f.Namespace, "--detail")
		Expect(output).To(ContainSubstring("Succeeded"))
		Expect(output).To(ContainSubstring("Workspace"))

		By("deleting task via CLI")
		framework.Axon("delete", "task", "config-task", "-n", f.Namespace)
	})

	It("should allow CLI flags to override config file", func() {
		By("creating a Workspace resource for override")
		f.CreateWorkspace(&axonv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-config-ws-override",
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/axon-core/axon.git",
				Ref:  "main",
			},
		})

		By("writing a temp config file with oauthToken and inline workspace (bad ref)")
		dir := GinkgoT().TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		configContent := "oauthToken: " + oauthToken + "\nnamespace: " + f.Namespace + "\nworkspace:\n  repo: https://github.com/axon-core/axon.git\n  ref: v0.0.0\n"
		Expect(os.WriteFile(configPath, []byte(configContent), 0o644)).To(Succeed())

		By("creating a Task with CLI flag overriding config workspace")
		framework.Axon("run",
			"-p", "Run 'git log --oneline -1' and print the output",
			"--config", configPath,
			"--workspace", "e2e-config-ws-override",
			"--name", "config-override-task",
		)

		By("waiting for Job to complete")
		f.WaitForJobCompletion("config-override-task")

		By("verifying the CLI flag value was used")
		output := framework.AxonOutput("get", "task", "config-override-task", "-n", f.Namespace)
		Expect(output).To(ContainSubstring("Succeeded"))

		By("deleting task via CLI")
		framework.Axon("delete", "task", "config-override-task", "-n", f.Namespace)
	})

	It("should initialize config file via init command", func() {
		dir := GinkgoT().TempDir()
		configPath := filepath.Join(dir, "test-config.yaml")

		By("running axon init")
		framework.Axon("init", "--config", configPath)

		By("verifying file was created with template content")
		data, err := os.ReadFile(configPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring("oauthToken:"))
		Expect(string(data)).To(ContainSubstring("apiKey:"))

		By("running axon init again without --force (should fail)")
		cmd := framework.AxonCommand("init", "--config", configPath)
		Expect(cmd.Run()).To(HaveOccurred())

		By("running axon init with --force (should succeed)")
		framework.Axon("init", "--config", configPath, "--force")
	})
})

var _ = Describe("Config with namespace", func() {
	f := framework.NewFramework("config-ns")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should use namespace from config file", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("writing a config with namespace set to test namespace")
		dir := GinkgoT().TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		configContent := "oauthToken: " + oauthToken + "\nnamespace: " + f.Namespace + "\n"
		Expect(os.WriteFile(configPath, []byte(configContent), 0o644)).To(Succeed())

		By("creating a Task via CLI using config namespace")
		framework.Axon("run",
			"--config", configPath,
			"-p", "Print 'hello' to stdout",
			"--secret", "claude-credentials",
			"--credential-type", "oauth",
			"--model", testModel,
			"--name", "ns-config-task",
		)

		By("verifying task exists in the framework namespace")
		Eventually(func() []string {
			return f.ListTaskNames("")
		}, 30*time.Second, time.Second).Should(ContainElement("ns-config-task"))
	})
})
