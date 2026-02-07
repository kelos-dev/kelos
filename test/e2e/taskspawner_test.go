package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const taskSpawnerName = "e2e-test-spawner"

var _ = Describe("TaskSpawner", func() {
	BeforeEach(func() {
		if githubToken == "" {
			Skip("GITHUB_TOKEN not set, skipping TaskSpawner e2e tests")
		}

		By("cleaning up existing resources")
		kubectl("delete", "taskspawner", taskSpawnerName, "--ignore-not-found")
		kubectl("delete", "workspace", "e2e-spawner-workspace", "--ignore-not-found")
		kubectl("delete", "secret", "github-token", "--ignore-not-found")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("collecting debug info on failure")
			GinkgoWriter.Println("=== Debug: TaskSpawner status ===")
			kubectl("get", "taskspawner", taskSpawnerName, "-o", "yaml")
			GinkgoWriter.Println("=== Debug: Deployment status ===")
			kubectl("get", "deployment", taskSpawnerName, "-o", "yaml")
			GinkgoWriter.Println("=== Debug: Controller logs ===")
			kubectl("logs", "-n", "axon-system", "deployment/axon-controller-manager", "--tail=50")
		}

		By("cleaning up test resources")
		kubectl("delete", "taskspawner", taskSpawnerName, "--ignore-not-found")
		kubectl("delete", "workspace", "e2e-spawner-workspace", "--ignore-not-found")
		kubectl("delete", "secret", "github-token", "--ignore-not-found")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
	})

	It("should create a spawner Deployment and discover issues", func() {
		By("creating GitHub token secret")
		Expect(kubectlWithInput("", "create", "secret", "generic", "github-token",
			"--from-literal=GITHUB_TOKEN="+githubToken)).To(Succeed())

		By("creating OAuth credentials secret")
		Expect(kubectlWithInput("", "create", "secret", "generic", "claude-credentials",
			"--from-literal=CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)).To(Succeed())

		By("creating a Workspace resource with secretRef")
		wsYAML := `apiVersion: axon.io/v1alpha1
kind: Workspace
metadata:
  name: e2e-spawner-workspace
spec:
  repo: https://github.com/gjkim42/axon.git
  ref: main
  secretRef:
    name: github-token
`
		Expect(kubectlWithInput(wsYAML, "apply", "-f", "-")).To(Succeed())

		By("creating a TaskSpawner")
		tsYAML := `apiVersion: axon.io/v1alpha1
kind: TaskSpawner
metadata:
  name: ` + taskSpawnerName + `
spec:
  when:
    githubIssues:
      workspaceRef:
        name: e2e-spawner-workspace
      labels: [bug]
      excludeLabels: [e2e-exclude-placeholder]
      state: open
  taskTemplate:
    type: claude-code
    credentials:
      type: oauth
      secretRef:
        name: claude-credentials
    promptTemplate: "Fix: {{.Title}}\n{{.Body}}"
  pollInterval: 1m
`
		Expect(kubectlWithInput(tsYAML, "apply", "-f", "-")).To(Succeed())

		By("waiting for Deployment to become available")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=available", "deployment/"+taskSpawnerName, "--timeout=10s")
		}, 2*time.Minute, 10*time.Second).Should(Succeed())

		By("waiting for TaskSpawner phase to become Running")
		Eventually(func() string {
			return kubectlOutput("get", "taskspawner", taskSpawnerName, "-o", "jsonpath={.status.phase}")
		}, 3*time.Minute, 10*time.Second).Should(Equal("Running"))

		By("verifying at least one Task was created")
		Eventually(func() string {
			return kubectlOutput("get", "tasks", "-l", "axon.io/taskspawner="+taskSpawnerName, "-o", "name")
		}, 3*time.Minute, 10*time.Second).ShouldNot(BeEmpty())
	})

	It("should be accessible via CLI", func() {
		By("creating a Workspace resource")
		wsYAML := `apiVersion: axon.io/v1alpha1
kind: Workspace
metadata:
  name: e2e-spawner-workspace
spec:
  repo: https://github.com/gjkim42/axon.git
`
		Expect(kubectlWithInput(wsYAML, "apply", "-f", "-")).To(Succeed())

		By("creating a TaskSpawner")
		tsYAML := `apiVersion: axon.io/v1alpha1
kind: TaskSpawner
metadata:
  name: ` + taskSpawnerName + `
spec:
  when:
    githubIssues:
      workspaceRef:
        name: e2e-spawner-workspace
  taskTemplate:
    type: claude-code
    credentials:
      type: oauth
      secretRef:
        name: claude-credentials
  pollInterval: 5m
`
		Expect(kubectlWithInput(tsYAML, "apply", "-f", "-")).To(Succeed())

		By("verifying axon get taskspawners lists it")
		output := axonOutput("get", "taskspawners")
		Expect(output).To(ContainSubstring(taskSpawnerName))

		By("verifying axon get taskspawner shows detail")
		output = axonOutput("get", "taskspawner", taskSpawnerName)
		Expect(output).To(ContainSubstring(taskSpawnerName))
		Expect(output).To(ContainSubstring("GitHub Issues"))

		By("deleting via kubectl")
		kubectl("delete", "taskspawner", taskSpawnerName)

		By("verifying it disappears from list")
		Eventually(func() string {
			return axonOutput("get", "taskspawners")
		}, 30*time.Second, time.Second).ShouldNot(ContainSubstring(taskSpawnerName))
	})
})

var _ = Describe("get taskspawner", func() {
	It("should succeed with 'taskspawners' alias", func() {
		axonOutput("get", "taskspawners")
	})

	It("should succeed with 'ts' alias", func() {
		axonOutput("get", "ts")
	})

	It("should succeed with 'taskspawner' subcommand", func() {
		axonOutput("get", "taskspawner")
	})

	It("should fail for a nonexistent taskspawner", func() {
		axonFail("get", "taskspawner", "nonexistent-spawner")
	})
})
