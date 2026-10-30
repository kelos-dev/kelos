package integration

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/controller"
)

var _ = Describe("Workspace restricted checkout", func() {
	const (
		timeout  = controllerSettleTimeout
		interval = time.Millisecond * 250
	)

	createNamespace := func(name string) {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}

	Context("API validation", func() {
		const ns = "test-workspace-checkout-validation"

		BeforeEach(func() {
			createNamespace(ns)
		})

		workspace := func(name string, clone *kelos.WorkspaceClone) *kelos.Workspace {
			return &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: kelos.WorkspaceSpec{
					Repo:  "https://github.com/example/repo.git",
					Clone: clone,
				},
			}
		}

		It("accepts clone paths, removeGitDir and agentCredentials None", func() {
			ws := workspace("valid", &kelos.WorkspaceClone{
				Paths:        []string{"docs", "www/app/", "cmd/main.go", "!literal", "#literal", ".github"},
				RemoveGitDir: true,
			})
			ws.Spec.SecretRef = &kelos.SecretReference{Name: "github-token"}
			ws.Spec.AgentCredentials = kelos.WorkspaceAgentCredentialsNone
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())
		})

		DescribeTable("rejects invalid clone paths",
			func(name, path, wantMessage string) {
				err := k8sClient.Create(ctx, workspace(name, &kelos.WorkspaceClone{Paths: []string{path}}))
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(wantMessage))
			},
			Entry("absolute", "absolute", "/docs", "must be relative to the repository root"),
			Entry("parent segment", "parent", "docs/../internal", "'..' segments"),
			Entry("leading parent", "leading-parent", "../docs", "'..' segments"),
			Entry("current directory", "current", ".", "'..' segments"),
			Entry("empty segment", "empty-segment", "docs//a", "'..' segments"),
			Entry("trailing space", "trailing-space", "docs ", "must not end with a space"),
			Entry("trailing space before a slash", "trailing-space-slash", "data /", "must not end with a space"),
			Entry("wildcard", "wildcard", "docs/*", "should match"),
			Entry("character class", "class", "doc[s]", "should match"),
			Entry("backslash", "backslash", `docs\a`, "should match"),
			Entry("newline", "newline", "docs\n/*", "should match"),
			Entry("empty", "empty", "", "at least 1 chars long"),
			Entry("longer than 256 characters", "too-long", strings.Repeat("a", 257), "may not be longer than 256"),
		)

		It("rejects more than 64 paths", func() {
			paths := make([]string, 65)
			for i := range paths {
				paths[i] = fmt.Sprintf("dir%d", i)
			}
			err := k8sClient.Create(ctx, workspace("too-many", &kelos.WorkspaceClone{Paths: paths}))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must have at most 64 items"))
		})

		It("rejects an empty paths list", func() {
			// Built unstructured because omitempty drops an empty list from
			// the typed object, while a user can write `paths: []` in YAML.
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha2", Kind: "Workspace"})
			obj.SetName("empty-list")
			obj.SetNamespace(ns)
			obj.Object["spec"] = map[string]interface{}{
				"repo":  "https://github.com/example/repo.git",
				"clone": map[string]interface{}{"paths": []interface{}{}},
			}
			err := k8sClient.Create(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("should have at least 1 items"))
		})

		It("rejects removeGitDir with remotes", func() {
			ws := workspace("remove-git-remotes", &kelos.WorkspaceClone{RemoveGitDir: true})
			ws.Spec.Remotes = []kelos.GitRemote{{Name: "upstream", URL: "https://github.com/upstream/repo.git"}}
			err := k8sClient.Create(ctx, ws)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("remotes cannot be set when clone.removeGitDir is true"))
		})

		It("accepts remotes when removeGitDir is false", func() {
			ws := workspace("keep-git-remotes", &kelos.WorkspaceClone{Paths: []string{"docs"}})
			ws.Spec.Remotes = []kelos.GitRemote{{Name: "upstream", URL: "https://github.com/upstream/repo.git"}}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())
		})

		It("rejects an unknown agentCredentials value", func() {
			ws := workspace("bad-agent-credentials", nil)
			ws.Spec.AgentCredentials = "Read"
			Expect(k8sClient.Create(ctx, ws)).ShouldNot(Succeed())
		})
	})

	Context("Task Jobs", func() {
		createTaskInputs := func(ns string, clone *kelos.WorkspaceClone) {
			createNamespace(ns)
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "anthropic-api-key", Namespace: ns},
				StringData: map[string]string{"ANTHROPIC_API_KEY": "test-api-key"},
			})).Should(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "github-token", Namespace: ns},
				StringData: map[string]string{"GITHUB_TOKEN": "test-github-token"},
			})).Should(Succeed())
			Expect(k8sClient.Create(ctx, &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "restricted", Namespace: ns},
				Spec: kelos.WorkspaceSpec{
					Repo:             "https://github.com/example/repo.git",
					Ref:              "main",
					SecretRef:        &kelos.SecretReference{Name: "github-token"},
					Clone:            clone,
					AgentCredentials: kelos.WorkspaceAgentCredentialsNone,
				},
			})).Should(Succeed())
		}

		newTask := func(ns, name, branch string) *kelos.Task {
			return &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: kelos.TaskSpec{
					Type:   "claude-code",
					Prompt: "Answer from the docs",
					Credentials: &kelos.Credentials{
						Type:      kelos.CredentialTypeAPIKey,
						SecretRef: &kelos.SecretReference{Name: "anthropic-api-key"},
					},
					WorkspaceRef: &kelos.WorkspaceReference{Name: "restricted"},
					Branch:       branch,
				},
			}
		}

		It("creates a Job whose agent gets the restricted checkout and no GitHub token", func() {
			const ns = "test-workspace-checkout-job"
			createTaskInputs(ns, &kelos.WorkspaceClone{Paths: []string{"docs"}, RemoveGitDir: true})
			Expect(k8sClient.Create(ctx, newTask(ns, "restricted-task", ""))).Should(Succeed())

			job := &batchv1.Job{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "restricted-task", Namespace: ns}, job)
			}, timeout, interval).Should(Succeed())
			logJobSpec(job)

			podSpec := job.Spec.Template.Spec
			var gitClone *corev1.Container
			for i := range podSpec.InitContainers {
				if podSpec.InitContainers[i].Name == "git-clone" {
					gitClone = &podSpec.InitContainers[i]
				}
			}
			Expect(gitClone).NotTo(BeNil())
			Expect(gitClone.Args).To(ContainElements("--filter=blob:none", "--no-checkout"))
			Expect(gitClone.Command[2]).To(ContainSubstring("sparse-checkout set --no-cone -- '/docs'"))
			Expect(strings.HasSuffix(gitClone.Command[2], "rm -rf /workspace/repo/.git")).To(BeTrue())
			Expect(gitClone.Env).To(ContainElement(HaveField("Name", "GITHUB_TOKEN")))

			agent := podSpec.Containers[0]
			for _, env := range agent.Env {
				Expect([]string{"GITHUB_TOKEN", "GH_TOKEN", "GH_ENTERPRISE_TOKEN", "GH_CONFIG_DIR", "KELOS_GITHUB_TOKEN_FILE"}).
					NotTo(ContainElement(env.Name))
			}
			for _, mount := range agent.VolumeMounts {
				Expect(mount.Name).NotTo(Equal(controller.GitHubTokenVolumeName))
			}
		})

		It("fails a Task that combines removeGitDir with a branch", func() {
			const ns = "test-workspace-checkout-branch"
			createTaskInputs(ns, &kelos.WorkspaceClone{RemoveGitDir: true})
			Expect(k8sClient.Create(ctx, newTask(ns, "branch-task", "feature-x"))).Should(Succeed())

			task := &kelos.Task{}
			Eventually(func() kelos.TaskPhase {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "branch-task", Namespace: ns}, task); err != nil {
					return ""
				}
				return task.Status.Phase
			}, timeout, interval).Should(Equal(kelos.TaskPhaseFailed))
			Expect(task.Status.Message).To(ContainSubstring(`clone.removeGitDir cannot be combined with branch "feature-x"`))

			Eventually(func() *corev1.Event {
				return findEvent(ns, "branch-task", "JobBuildFailed")
			}, timeout, interval).ShouldNot(BeNil())

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "branch-task", Namespace: ns}, job)).ShouldNot(Succeed())
		})
	})
})
