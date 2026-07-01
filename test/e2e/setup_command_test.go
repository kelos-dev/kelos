package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
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
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-setup-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
				SetupCommand: []string{
					"sh", "-c",
					"echo setup-ran-from-workspace > /workspace/repo/.kelos-setup-sentinel",
				},
			},
		})

		By("creating a Task that asks the agent to read the sentinel file")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "setup-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print the contents of .kelos-setup-sentinel verbatim, then print 'done'",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &kelos.WorkspaceReference{Name: "e2e-setup-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("setup-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("setup-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("setup-task", "Succeeded")

		By("verifying setup banners appear in Pod logs")
		logs := f.GetJobLogs("setup-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_START---"))
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_DONE---"))
		Expect(logs).NotTo(ContainSubstring("---KELOS_SETUP_COMMAND_FAILED---"))

		By("verifying the agent saw the file written by setupCommand")
		Expect(logs).To(ContainSubstring("setup-ran-from-workspace"))
	})

	It("should resolve binaries installed by setupCommand into $HOME/.local/bin from PATH", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace whose setupCommand drops an executable into $HOME/.local/bin")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-setup-path-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
				SetupCommand: []string{
					"sh", "-c",
					`set -e
mkdir -p "$HOME/.local/bin"
cat >"$HOME/.local/bin/kelos-setup-probe" <<'EOF'
#!/bin/sh
echo kelos-setup-probe-on-path
EOF
chmod +x "$HOME/.local/bin/kelos-setup-probe"`,
				},
			},
		})

		By("creating a Task that invokes the installed binary by name")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "setup-path-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Run the command 'kelos-setup-probe' and print its output verbatim.",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &kelos.WorkspaceReference{Name: "e2e-setup-path-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("setup-path-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("setup-path-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("setup-path-task", "Succeeded")

		By("verifying the agent resolved the installed binary via PATH")
		logs := f.GetJobLogs("setup-path-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_DONE---"))
		Expect(logs).To(ContainSubstring("kelos-setup-probe-on-path"))
	})

	It("should fail the Task when setupCommand exits non-zero", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace whose setupCommand always fails")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-setup-failing-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
				SetupCommand: []string{
					"sh", "-c", "echo failing-setup >&2; exit 17",
				},
			},
		})

		By("creating a Task referencing the failing workspace")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "setup-fail-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'agent should never run'",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &kelos.WorkspaceReference{Name: "e2e-setup-failing-workspace"},
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
