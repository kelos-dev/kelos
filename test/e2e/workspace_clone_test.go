package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("Workspace clone.branches", func() {
	f := framework.NewFramework("workspace-clone")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should clone only the ref and still check out the Task branch at the remote tip", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace that clones only a release tag and reports git state from setupCommand")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-clone-single-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo:  "https://github.com/kelos-dev/kelos.git",
				Ref:   "v0.59.0",
				Clone: &kelos.WorkspaceClone{Branches: kelos.WorkspaceCloneBranchesSingle},
				SetupCommand: []string{
					"sh", "-c",
					`echo "kelos-shallow=$(git rev-parse --is-shallow-repository)"
echo "kelos-branch=$(git rev-parse --abbrev-ref HEAD)"
echo "kelos-remote-branches=$(git branch -r | wc -l | tr -d ' ')"
if [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ]; then echo kelos-head-matches-origin-main; fi
if git config --get-all remote.origin.fetch | grep -qxF '+refs/heads/main:refs/remotes/origin/main'; then echo kelos-origin-tracks-main; fi`,
				},
			},
		})

		By("creating a Task whose branch is not in the clone")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "clone-single-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'done'",
				Branch: "main",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &kelos.WorkspaceReference{Name: "e2e-clone-single-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("clone-single-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("clone-single-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("clone-single-task", "Succeeded")

		By("verifying the repository is shallow, holds only the Task branch, and origin tracks it")
		logs := f.GetJobLogs("clone-single-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_DONE---"))
		Expect(logs).To(ContainSubstring("kelos-shallow=true"))
		Expect(logs).To(ContainSubstring("kelos-branch=main"))
		Expect(logs).To(ContainSubstring("kelos-remote-branches=1"))
		Expect(logs).To(ContainSubstring("kelos-head-matches-origin-main"))
		Expect(logs).To(ContainSubstring("kelos-origin-tracks-main"))
	})
})
