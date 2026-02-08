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
  repo: https://github.com/axon-core/axon.git
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
      labels: [do-not-remove/e2e-anchor]
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
  repo: https://github.com/axon-core/axon.git
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

		By("verifying YAML output for a single taskspawner")
		output = axonOutput("get", "taskspawner", taskSpawnerName, "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: axon.io/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: TaskSpawner"))
		Expect(output).To(ContainSubstring("name: " + taskSpawnerName))

		By("verifying JSON output for a single taskspawner")
		output = axonOutput("get", "taskspawner", taskSpawnerName, "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "axon.io/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "TaskSpawner"`))
		Expect(output).To(ContainSubstring(`"name": "` + taskSpawnerName + `"`))

		By("deleting via kubectl")
		kubectl("delete", "taskspawner", taskSpawnerName)

		By("verifying it disappears from list")
		Eventually(func() string {
			return axonOutput("get", "taskspawners")
		}, 30*time.Second, time.Second).ShouldNot(ContainSubstring(taskSpawnerName))
	})
})

const cronTaskSpawnerName = "e2e-cron-spawner"

var _ = Describe("Cron TaskSpawner", func() {
	BeforeEach(func() {
		By("cleaning up existing resources")
		kubectl("delete", "taskspawner", cronTaskSpawnerName, "--ignore-not-found")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("collecting debug info on failure")
			GinkgoWriter.Println("=== Debug: Cron TaskSpawner status ===")
			kubectl("get", "taskspawner", cronTaskSpawnerName, "-o", "yaml")
			GinkgoWriter.Println("=== Debug: Deployment status ===")
			kubectl("get", "deployment", cronTaskSpawnerName, "-o", "yaml")
			GinkgoWriter.Println("=== Debug: Controller logs ===")
			kubectl("logs", "-n", "axon-system", "deployment/axon-controller-manager", "--tail=50")
		}

		By("cleaning up test resources")
		kubectl("delete", "taskspawner", cronTaskSpawnerName, "--ignore-not-found")
		kubectl("delete", "secret", "claude-credentials", "--ignore-not-found")
	})

	It("should create a spawner Deployment and discover cron ticks", func() {
		By("creating OAuth credentials secret")
		Expect(kubectlWithInput("", "create", "secret", "generic", "claude-credentials",
			"--from-literal=CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)).To(Succeed())

		By("creating a cron TaskSpawner with every-minute schedule")
		tsYAML := `apiVersion: axon.io/v1alpha1
kind: TaskSpawner
metadata:
  name: ` + cronTaskSpawnerName + `
spec:
  when:
    cron:
      schedule: "* * * * *"
  taskTemplate:
    type: claude-code
    model: ` + testModel + `
    credentials:
      type: oauth
      secretRef:
        name: claude-credentials
    promptTemplate: "Cron triggered at {{.Time}} (schedule: {{.Schedule}}). Print 'Hello from cron'"
  pollInterval: 1m
`
		Expect(kubectlWithInput(tsYAML, "apply", "-f", "-")).To(Succeed())

		By("waiting for Deployment to become available")
		Eventually(func() error {
			return kubectlWithInput("", "wait", "--for=condition=available", "deployment/"+cronTaskSpawnerName, "--timeout=10s")
		}, 2*time.Minute, 10*time.Second).Should(Succeed())

		By("waiting for TaskSpawner phase to become Running")
		Eventually(func() string {
			return kubectlOutput("get", "taskspawner", cronTaskSpawnerName, "-o", "jsonpath={.status.phase}")
		}, 3*time.Minute, 10*time.Second).Should(Equal("Running"))

		By("verifying at least one Task was created")
		Eventually(func() string {
			return kubectlOutput("get", "tasks", "-l", "axon.io/taskspawner="+cronTaskSpawnerName, "-o", "name")
		}, 3*time.Minute, 10*time.Second).ShouldNot(BeEmpty())
	})

	It("should be accessible via CLI with cron source info", func() {
		By("creating a cron TaskSpawner")
		tsYAML := `apiVersion: axon.io/v1alpha1
kind: TaskSpawner
metadata:
  name: ` + cronTaskSpawnerName + `
spec:
  when:
    cron:
      schedule: "0 9 * * 1"
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
		Expect(output).To(ContainSubstring(cronTaskSpawnerName))

		By("verifying axon get taskspawner shows cron detail")
		output = axonOutput("get", "taskspawner", cronTaskSpawnerName)
		Expect(output).To(ContainSubstring(cronTaskSpawnerName))
		Expect(output).To(ContainSubstring("Cron"))
		Expect(output).To(ContainSubstring("0 9 * * 1"))

		By("deleting via kubectl")
		kubectl("delete", "taskspawner", cronTaskSpawnerName)

		By("verifying it disappears from list")
		Eventually(func() string {
			return axonOutput("get", "taskspawners")
		}, 30*time.Second, time.Second).ShouldNot(ContainSubstring(cronTaskSpawnerName))
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

	It("should output taskspawner list in YAML format", func() {
		output := axonOutput("get", "taskspawners", "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: axon.io/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: TaskSpawnerList"))
	})

	It("should output taskspawner list in JSON format", func() {
		output := axonOutput("get", "taskspawners", "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "axon.io/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "TaskSpawnerList"`))
	})

	It("should fail with unknown output format", func() {
		axonFail("get", "taskspawners", "-o", "invalid")
	})
})
