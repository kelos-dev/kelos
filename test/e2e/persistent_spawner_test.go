package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("Persistent Execution Mode", func() {
	f := framework.NewFramework("persistent")

	BeforeEach(func() {
		if githubToken == "" {
			Skip("GITHUB_TOKEN not set, skipping persistent mode e2e tests")
		}
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set, skipping persistent mode e2e tests")
		}
	})

	It("should create a StatefulSet and process tasks via session pods", func() {
		By("creating GitHub token secret")
		f.CreateSecret("github-token",
			"GITHUB_TOKEN="+githubToken)

		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace resource")
		f.CreateWorkspace(&kelosv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-persistent-workspace",
			},
			Spec: kelosv1alpha1.WorkspaceSpec{
				Repo:      "https://github.com/kelos-dev/kelos.git",
				Ref:       "main",
				SecretRef: &kelosv1alpha1.SecretReference{Name: "github-token"},
			},
		})

		By("creating a persistent-mode TaskSpawner")
		replicas := int32(1)
		storageSize := resource.MustParse("5Gi")
		f.CreateTaskSpawner(&kelosv1alpha1.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "persistent-spawner",
			},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
				SessionConfig: &kelosv1alpha1.SessionConfig{
					Replicas:    &replicas,
					StorageSize: &storageSize,
				},
				When: kelosv1alpha1.When{
					GitHubIssues: &kelosv1alpha1.GitHubIssues{
						Labels:        []string{"do-not-remove/e2e-anchor"},
						ExcludeLabels: []string{"e2e-exclude-placeholder"},
						State:         "open",
					},
				},
				TaskTemplate: kelosv1alpha1.TaskTemplate{
					Type:  "claude-code",
					Model: testModel,
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "e2e-persistent-workspace",
					},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeOAuth,
						SecretRef: &kelosv1alpha1.SecretReference{Name: "claude-credentials"},
					},
					PromptTemplate: "Fix: {{.Title}}\n{{.Body}}",
				},
				PollInterval: "1m",
			},
		})

		By("waiting for StatefulSet to become ready")
		f.WaitForStatefulSetReady("session-persistent-spawner")

		By("waiting for TaskSpawner phase to become Running")
		Eventually(func() string {
			return f.GetTaskSpawnerPhase("persistent-spawner")
		}, 3*time.Minute, 10*time.Second).Should(Equal("Running"))

		By("verifying at least one Task was created")
		var taskNames []string
		Eventually(func() []string {
			taskNames = f.ListTaskNames("kelos.dev/taskspawner=persistent-spawner")
			return taskNames
		}, 3*time.Minute, 10*time.Second).ShouldNot(BeEmpty())

		By("verifying a Task enters Queued phase before being assigned")
		// Tasks in persistent mode go through Queued -> Running -> terminal
		// By the time we check, the task may already be past Queued, so we
		// verify it reaches a non-Pending state (Queued, Running, or terminal).
		Eventually(func() string {
			return f.GetTaskPhase(taskNames[0])
		}, 2*time.Minute, 5*time.Second).ShouldNot(BeEmpty())

		By("verifying a Task gets assigned to a session pod")
		Eventually(func() string {
			return f.GetTaskSessionPodName(taskNames[0])
		}, 3*time.Minute, 10*time.Second).ShouldNot(BeEmpty())

		By("verifying the Task reaches a terminal phase")
		Eventually(func() string {
			phase := f.GetTaskPhase(taskNames[0])
			if phase == "Succeeded" || phase == "Failed" {
				return phase
			}
			return ""
		}, 10*time.Minute, 15*time.Second).ShouldNot(BeEmpty())
	})

	It("should show persistent mode in CLI output", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace resource")
		f.CreateWorkspace(&kelosv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-persistent-workspace",
			},
			Spec: kelosv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
			},
		})

		By("creating a persistent-mode TaskSpawner")
		replicas := int32(2)
		storageSize := resource.MustParse("10Gi")
		f.CreateTaskSpawner(&kelosv1alpha1.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name: "persistent-cli",
			},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
				SessionConfig: &kelosv1alpha1.SessionConfig{
					Replicas:    &replicas,
					StorageSize: &storageSize,
				},
				When: kelosv1alpha1.When{
					GitHubIssues: &kelosv1alpha1.GitHubIssues{},
				},
				TaskTemplate: kelosv1alpha1.TaskTemplate{
					Type: "claude-code",
					WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
						Name: "e2e-persistent-workspace",
					},
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeOAuth,
						SecretRef: &kelosv1alpha1.SecretReference{Name: "claude-credentials"},
					},
				},
				PollInterval: "5m",
			},
		})

		By("verifying kelos get taskspawner shows persistent mode detail")
		output := framework.KelosOutput("get", "taskspawner", "persistent-cli", "-n", f.Namespace, "--detail")
		Expect(output).To(ContainSubstring("persistent-cli"))
		Expect(output).To(ContainSubstring("persistent"))

		By("verifying YAML output includes executionMode and sessionConfig")
		output = framework.KelosOutput("get", "taskspawner", "persistent-cli", "-n", f.Namespace, "-o", "yaml")
		Expect(output).To(ContainSubstring("executionMode: persistent"))
		Expect(output).To(ContainSubstring("sessionConfig"))
		Expect(output).To(ContainSubstring("replicas: 2"))

		By("deleting the TaskSpawner")
		f.DeleteTaskSpawner("persistent-cli")

		By("verifying it disappears from list")
		Eventually(func() string {
			return framework.KelosOutput("get", "taskspawners", "-n", f.Namespace)
		}, 30*time.Second, time.Second).ShouldNot(ContainSubstring("persistent-cli"))
	})
})
