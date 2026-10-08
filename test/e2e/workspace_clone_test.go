package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("Workspace clone.branchPolicy", func() {
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

		By("creating a Workspace with branchPolicy Ref on a release tag that reports git state from setupCommand")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-clone-single-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo:  "https://github.com/kelos-dev/kelos.git",
				Ref:   "v0.59.0",
				Clone: &kelos.WorkspaceClone{BranchPolicy: kelos.WorkspaceCloneBranchPolicyRef},
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

	It("should clone the ref together with the remote's default branch and keep git fetch working on an unpushed Task branch", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Workspace with branchPolicy RefAndDefault on a release tag that reports git state from setupCommand")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e2e-clone-ref-and-default-workspace",
			},
			Spec: kelos.WorkspaceSpec{
				Repo:  "https://github.com/kelos-dev/kelos.git",
				Ref:   "v0.59.0",
				Clone: &kelos.WorkspaceClone{BranchPolicy: kelos.WorkspaceCloneBranchPolicyRefAndDefault},
				SetupCommand: []string{
					"sh", "-c",
					`echo "kelos-shallow=$(git rev-parse --is-shallow-repository)"
echo "kelos-remote-branches=$(git branch -r | wc -l | tr -d ' ')"
if git rev-parse --verify --quiet refs/remotes/origin/main >/dev/null; then echo kelos-has-origin-main; fi
if [ "$(git rev-parse HEAD)" = "$(git rev-parse 'v0.59.0^{commit}')" ]; then echo kelos-head-at-ref; fi
if git config --get-all remote.origin.fetch | grep -qxF '+refs/heads/main:refs/remotes/origin/main'; then echo kelos-origin-tracks-main; fi
echo "kelos-branch=$(git rev-parse --abbrev-ref HEAD)"
if git fetch origin; then echo kelos-plain-fetch-ok; fi
if ! git config --get-all remote.origin.fetch | grep -qF kelos-e2e-unpushed; then echo kelos-unpushed-branch-untracked; fi`,
				},
			},
		})

		By("creating a Task whose branch does not exist on origin")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "clone-ref-and-default-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'done'",
				Branch: "kelos-e2e-unpushed-ref-and-default",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
				WorkspaceRef: &kelos.WorkspaceReference{Name: "e2e-clone-ref-and-default-workspace"},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("clone-ref-and-default-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("clone-ref-and-default-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("clone-ref-and-default-task", "Succeeded")

		By("verifying the Task branch starts at the tag, the default branch was fetched shallowly, and git fetch works")
		logs := f.GetJobLogs("clone-ref-and-default-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		Expect(logs).To(ContainSubstring("---KELOS_SETUP_COMMAND_DONE---"))
		Expect(logs).To(ContainSubstring("kelos-shallow=true"))
		Expect(logs).To(ContainSubstring("kelos-remote-branches=1"))
		Expect(logs).To(ContainSubstring("kelos-has-origin-main"))
		Expect(logs).To(ContainSubstring("kelos-head-at-ref"))
		Expect(logs).To(ContainSubstring("kelos-origin-tracks-main"))
		Expect(logs).To(ContainSubstring("kelos-branch=kelos-e2e-unpushed-ref-and-default"))
		Expect(logs).To(ContainSubstring("kelos-plain-fetch-ok"))
		Expect(logs).To(ContainSubstring("kelos-unpushed-branch-untracked"))
	})
})
