package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("Workspace setupCommand", func() {
	f := framework.NewFramework("setup-command")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should run setupCommand before the agent and surface its side effects", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace with a setupCommand that writes a sentinel file")
		f.CreateWorkspace(&kelosv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-setup-workspace",
			},
			Spec: kelosv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
				SetupCommand: []string{
					"sh", "-c",
					"echo setup-ran-from-workspace > /workspace/repo/.kelos-setup-sentinel",
				},
			},
		})

		By("creating a Task that asks the agent to read the sentinel file")
		f.CreateTask(&kelosv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "setup-task",
			},
			Spec: kelosv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Print the contents of .kelos-setup-sentinel verbatim, then print 'done'",
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeOAuth,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "e2e-setup-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("setup-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("setup-task")

		By("verifying Task status is Succeeded")
		Expect(f.GetTaskPhase("setup-task")).To(Equal("Succeeded"))

		By("verifying setup banners appear in Pod logs")
		logs := f.GetJobLogs("setup-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_START---"))
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_DONE---"))
		Expect(logs).NotTo(ContainSubstring("---KELOS_SETUP_COMMAND_FAILED---"))

		By("verifying the agent saw the file written by setupCommand")
		Expect(logs).To(ContainSubstring("setup-ran-from-workspace"))
	})

	It("should fail the Task when setupCommand exits non-zero", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace whose setupCommand always fails")
		f.CreateWorkspace(&kelosv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-setup-failing-workspace",
			},
			Spec: kelosv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
				SetupCommand: []string{
					"sh", "-c", "echo failing-setup >&2; exit 17",
				},
			},
		})

		By("creating a Task referencing the failing workspace")
		f.CreateTask(&kelosv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "setup-fail-task",
			},
			Spec: kelosv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  testModel,
				Prompt: "Print 'agent should never run'",
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeOAuth,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "e2e-setup-failing-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("setup-fail-task")

		By("verifying Task eventually transitions to Failed")
		Eventually(func() string {
			return f.GetTaskPhase("setup-fail-task")
		}, 5*time.Minute, 10*time.Second).Should(Equal("Failed"))

		By("verifying failure banner appears and agent never ran")
		logs := f.GetJobLogs("setup-fail-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_START---"))
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_FAILED---"))
		Expect(logs).NotTo(ContainSubstring("---KELOS_SETUP_COMMAND_DONE---"))
		Expect(logs).NotTo(ContainSubstring("agent should never run"))
	})
})
