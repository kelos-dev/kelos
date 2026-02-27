package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("TaskSpawner", func() {
	f := framework.NewFramework("spawner")

	BeforeEach(func() {
		if githubToken == "" {
			Skip("GITHUB_TOKEN not set, skipping TaskSpawner e2e tests")
		}
	})

	// This test requires at least one open GitHub issue in kelos-dev/kelos
	// with the "do-not-remove/e2e-anchor" label. See issue #117.
	It("should create a spawner Deployment and discover issues", func() {
		By("creating GitHub token secret")
		f.CreateSecret("github-token",
			"GITHUB_TOKEN="+githubToken)

		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace resource with secretRef")
		f.CreateWorkspace(&kelosv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-spawner-workspace",
			},
			Spec: kelosv1alpha1.WorkspaceSpec{
				Repo:      "https://github.com/kelos-dev/kelos.git",
				Ref:       "main",
				SecretRef: &kelosv1alpha1.SecretReference{Name: "github-token"},
			},
		})

		By("creating a TaskSpawner")
		f.CreateTaskSpawner(&kelosv1alpha1.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "spawner",
			},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{
					GitHubIssues: &kelosv1alpha1.GitHubIssues{
						Labels:        []string{"do-not-remove/e2e-anchor"},
						ExcludeLabels: []string{"e2e-exclude-placeholder"},
						State:         "open",
					},
				},
				TaskTemplate: kelosv1alpha1.TaskTemplate{
					Type: "claude-code",
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "e2e-spawner-workspace",
					},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeOAuth,
						SecretRef: kelosv1alpha1.SecretReference{Name: "claude-credentials"},
					},
					PromptTemplate: "Fix: {{.Title}}\n{{.Body}}",
				},
				PollInterval: "1m",
			},
		})

		By("waiting for Deployment to become available")
		f.WaitForDeploymentAvailable("spawner")

		By("waiting for TaskSpawner phase to become Running")
		Eventually(func() string {
			return f.GetTaskSpawnerPhase("spawner")
		}, 3*time.Minute, 10*time.Second).Should(Equal("Running"))

		By("verifying at least one Task was created")
		Eventually(func() []string {
			return f.ListTaskNames("kelos.dev/taskspawner=spawner")
		}, 3*time.Minute, 10*time.Second).ShouldNot(BeEmpty())
	})

	It("should be accessible via CLI", func() {
		By("creating a Workspace resource")
		f.CreateWorkspace(&kelosv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-spawner-workspace",
			},
			Spec: kelosv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
			},
		})

		By("creating a TaskSpawner")
		f.CreateTaskSpawner(&kelosv1alpha1.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "spawner",
			},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{
					GitHubIssues: &kelosv1alpha1.GitHubIssues{},
				},
				TaskTemplate: kelosv1alpha1.TaskTemplate{
					Type: "claude-code",
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "e2e-spawner-workspace",
					},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeOAuth,
						SecretRef: kelosv1alpha1.SecretReference{Name: "claude-credentials"},
					},
				},
				PollInterval: "5m",
			},
		})

		By("verifying kelos get taskspawners lists it")
		output := framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		Expect(output).To(ContainSubstring("spawner"))

		By("verifying kelos get taskspawner shows detail")
		output = framework.KelosOutput("get", "taskspawner", "spawner", "-n", f.Namespace, "--detail")
		Expect(output).To(ContainSubstring("spawner"))
		Expect(output).To(ContainSubstring("GitHub Issues"))

		By("verifying YAML output for a single taskspawner")
		output = framework.KelosOutput("get", "taskspawner", "spawner", "-n", f.Namespace, "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: kelos.dev/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: TaskSpawner"))
		Expect(output).To(ContainSubstring("name: spawner"))

		By("verifying JSON output for a single taskspawner")
		output = framework.KelosOutput("get", "taskspawner", "spawner", "-n", f.Namespace, "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "kelos.dev/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "TaskSpawner"`))
		Expect(output).To(ContainSubstring(`"name": "spawner"`))

		By("deleting the TaskSpawner")
		f.DeleteTaskSpawner("spawner")

		By("verifying it disappears from list")
		Eventually(func() string {
			return framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		}, 30*time.Second, time.Second).ShouldNot(ContainSubstring("spawner"))
	})
})

var _ = Describe("Cron TaskSpawner", func() {
	f := framework.NewFramework("cron")

	It("should create a spawner Deployment and discover cron ticks", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a cron TaskSpawner with every-minute schedule")
		f.CreateTaskSpawner(&kelosv1alpha1.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cron-spawner",
			},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{
					Cron: &kelosv1alpha1.Cron{
						Schedule: "* * * * *",
					},
				},
				TaskTemplate: kelosv1alpha1.TaskTemplate{
					Type:  "claude-code",
					Model: testModel,
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeOAuth,
						SecretRef: kelosv1alpha1.SecretReference{Name: "claude-credentials"},
					},
					PromptTemplate: "Cron triggered at {{.Time}} (schedule: {{.Schedule}}). Print 'Hello from cron'",
				},
				PollInterval: "1m",
			},
		})

		By("waiting for Deployment to become available")
		f.WaitForDeploymentAvailable("cron-spawner")

		By("waiting for TaskSpawner phase to become Running")
		Eventually(func() string {
			return f.GetTaskSpawnerPhase("cron-spawner")
		}, 3*time.Minute, 10*time.Second).Should(Equal("Running"))

		By("verifying at least one Task was created")
		Eventually(func() []string {
			return f.ListTaskNames("kelos.dev/taskspawner=cron-spawner")
		}, 3*time.Minute, 10*time.Second).ShouldNot(BeEmpty())
	})

	It("should be accessible via CLI with cron source info", func() {
		By("creating a cron TaskSpawner")
		f.CreateTaskSpawner(&kelosv1alpha1.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cron-spawner",
			},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{
					Cron: &kelosv1alpha1.Cron{
						Schedule: "0 9 * * 1",
					},
				},
				TaskTemplate: kelosv1alpha1.TaskTemplate{
					Type: "claude-code",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeOAuth,
						SecretRef: kelosv1alpha1.SecretReference{Name: "claude-credentials"},
					},
				},
				PollInterval: "5m",
			},
		})

		By("verifying kelos get taskspawners lists it")
		output := framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		Expect(output).To(ContainSubstring("cron-spawner"))

		By("verifying kelos get taskspawner shows cron detail")
		output = framework.KelosOutput("get", "taskspawner", "cron-spawner", "-n", f.Namespace, "--detail")
		Expect(output).To(ContainSubstring("cron-spawner"))
		Expect(output).To(ContainSubstring("Cron"))
		Expect(output).To(ContainSubstring("0 9 * * 1"))

		By("deleting the TaskSpawner")
		f.DeleteTaskSpawner("cron-spawner")

		By("verifying it disappears from list")
		Eventually(func() string {
			return framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		}, 30*time.Second, time.Second).ShouldNot(ContainSubstring("cron-spawner"))
	})
})

var _ = Describe("get taskspawner", func() {
	It("should succeed with 'taskspawners' alias", func() {
		framework.KelosOutput("get", "taskspawners")
	})

	It("should succeed with 'ts' alias", func() {
		framework.KelosOutput("get", "ts")
	})

	It("should succeed with 'taskspawner' subcommand", func() {
		framework.KelosOutput("get", "taskspawner")
	})

	It("should fail for a nonexistent taskspawner", func() {
		framework.KelosFail("get", "taskspawner", "nonexistent-spawner")
	})

	It("should output taskspawner list in YAML format", func() {
		output := framework.KelosOutput("get", "taskspawners", "-o", "yaml")
		Expect(output).To(ContainSubstring("apiVersion: kelos.dev/v1alpha1"))
		Expect(output).To(ContainSubstring("kind: TaskSpawnerList"))
	})

	It("should output taskspawner list in JSON format", func() {
		output := framework.KelosOutput("get", "taskspawners", "-o", "json")
		Expect(output).To(ContainSubstring(`"apiVersion": "kelos.dev/v1alpha1"`))
		Expect(output).To(ContainSubstring(`"kind": "TaskSpawnerList"`))
	})

	It("should fail with unknown output format", func() {
		framework.KelosFail("get", "taskspawners", "-o", "invalid")
	})
})
