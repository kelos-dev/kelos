package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

const privateSkillsMarker = "KELOS_E2E_PRIVATE_SKILL_MARKER_q4m8n2"

type githubContentResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

var _ = Describe("AgentConfig with skills.sh", func() {
	f := framework.NewFramework("skills-sh")

	It("should create an agentconfig with skills.sh packages via CLI", func() {
		By("creating an agentconfig with --skills-sh flags")
		framework.Kelos("create", "agentconfig", "skills-ac",
			"-n", f.Namespace,
			"--skills-sh", "anthropics/skills:skill-creator",
			"--skills-sh", "anthropics/skills:webapp-testing",
		)

		By("verifying agentconfig exists via typed client")
		ac, err := f.KelosClientset.ApiV1alpha2().AgentConfigs(f.Namespace).Get(
			context.TODO(), "skills-ac", metav1.GetOptions{},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(ac.Spec.Skills).To(HaveLen(2))
		Expect(ac.Spec.Skills[0].Source).To(Equal("anthropics/skills"))
		Expect(ac.Spec.Skills[0].Skill).To(Equal("skill-creator"))
		Expect(ac.Spec.Skills[1].Source).To(Equal("anthropics/skills"))
		Expect(ac.Spec.Skills[1].Skill).To(Equal("webapp-testing"))
	})

	It("should reject duplicate --skills-sh entries", func() {
		framework.KelosFail("create", "agentconfig", "dup-skills-ac",
			"-n", f.Namespace,
			"--skills-sh", "anthropics/skills:skill-creator",
			"--skills-sh", "anthropics/skills:skill-creator",
		)
	})

	It("should reject --skills-sh with empty source", func() {
		framework.KelosFail("create", "agentconfig", "empty-skills-ac",
			"-n", f.Namespace,
			"--skills-sh", ":myskill",
		)
	})
})

var _ = Describe("Task with skills.sh AgentConfig", func() {
	f := framework.NewFramework("skills-task")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should run a Task with skills.sh agentconfig to completion", func() {
		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating an AgentConfig with the e2e fixture skill package")
		// kelos-dev/e2e-skills is a fixture repository whose kelos-e2e skill
		// instructs the agent to print a marker string that exists only in
		// that repository, never in the prompt below.
		framework.Kelos("create", "agentconfig", "skills-ac",
			"-n", f.Namespace,
			"--skills-sh", "kelos-dev/e2e-skills:kelos-e2e",
		)

		By("creating a Task referencing the AgentConfig")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "skills-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Use the kelos-e2e skill and show its output.",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "skills-ac"}},
				PodOverrides: &kelos.PodOverrides{
					// Deterministic install check: this runs after the
					// built-in skills-install init container and fails the
					// Job when the skill is not in the plugin layout,
					// distinguishing installation bugs from agent-side
					// discovery bugs without involving the model.
					ExtraInitContainers: []corev1.Container{{
						Name:    "verify-skills-install",
						Image:   "busybox:1.37",
						Command: []string{"sh", "-c", "test -f /kelos/plugin/skills-sh/skills/kelos-e2e/SKILL.md"},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "kelos-plugin",
							MountPath: "/kelos/plugin",
						}},
					}},
				},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("skills-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("skills-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("skills-task", "Succeeded")

		By("verifying the agent used the installed skill")
		logs := f.GetJobLogs("skills-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		// The marker exists only in the kelos-dev/e2e-skills repository and
		// not in the task prompt, so it can appear in the logs only when the
		// installed skill's content actually reached the agent.
		Expect(logs).To(ContainSubstring("KELOS_E2E_SKILL_MARKER_x7k2p9"))
	})

	It("should run a Task with authenticated skills.sh agentconfig to completion", func() {
		if skillsGithubToken == "" {
			Skip("E2E_SKILLS_GITHUB_TOKEN not set")
		}

		By("verifying the skills.sh GitHub token can read the private fixture skill")
		expectPrivateSkillsFixtureAccess(skillsGithubToken)

		By("creating OAuth credentials secret")
		f.CreateSecret("claude-credentials",
			"CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating skills.sh GitHub token secret")
		f.CreateSecret("skills-github-token",
			"GITHUB_TOKEN="+skillsGithubToken)

		By("creating an AgentConfig with the private e2e fixture skill package")
		// kelos-dev/e2e-skills-private is a private fixture repository whose
		// kelos-e2e-private skill is only installable when token auth is wired
		// into the skills-install init container.
		_, err := f.KelosClientset.ApiV1alpha2().AgentConfigs(f.Namespace).Create(
			context.TODO(),
			&kelos.AgentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "private-skills-ac",
					Namespace: f.Namespace,
				},
				Spec: kelos.AgentConfigSpec{
					Skills: []kelos.SkillsShSpec{{
						Source:    "kelos-dev/e2e-skills-private",
						Skill:     "kelos-e2e-private",
						SecretRef: &kelos.SecretReference{Name: "skills-github-token"},
					}},
				},
			},
			metav1.CreateOptions{},
		)
		Expect(err).NotTo(HaveOccurred())

		By("creating a Task referencing the private AgentConfig")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "private-skills-task",
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Use the kelos-e2e-private skill and show its output.",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "private-skills-ac"}},
				PodOverrides: &kelos.PodOverrides{
					ExtraInitContainers: []corev1.Container{{
						Name:    "verify-private-skills-install",
						Image:   "busybox:1.37",
						Command: []string{"sh", "-c", "test -f /kelos/plugin/skills-sh/skills/kelos-e2e-private/SKILL.md"},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "kelos-plugin",
							MountPath: "/kelos/plugin",
						}},
					}},
				},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("private-skills-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("private-skills-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("private-skills-task", "Succeeded")

		By("verifying the agent used the installed private skill")
		logs := f.GetJobLogs("private-skills-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		Expect(logs).To(ContainSubstring(privateSkillsMarker))
	})
})

var _ = Describe("Private skills.sh fixture auth", func() {
	It("should allow the e2e skills token to read the private fixture skill", func() {
		if skillsGithubToken == "" {
			Skip("E2E_SKILLS_GITHUB_TOKEN not set")
		}

		expectPrivateSkillsFixtureAccess(skillsGithubToken)
	})
})

func expectPrivateSkillsFixtureAccess(token string) {
	req, err := http.NewRequest(
		http.MethodGet,
		"https://api.github.com/repos/kelos-dev/e2e-skills-private/contents/kelos-e2e-private/SKILL.md?ref=main",
		nil,
	)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "GitHub API response: %s", string(body))

	var content githubContentResponse
	Expect(json.Unmarshal(body, &content)).To(Succeed())
	Expect(content.Encoding).To(Equal("base64"))

	encoded := strings.ReplaceAll(content.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(decoded)).To(ContainSubstring(privateSkillsMarker))
}

func describePluginTaskTests(cfg agentTestConfig) {
	Describe(fmt.Sprintf("Task with plugin AgentConfig [%s]", cfg.AgentType), func() {
		f := framework.NewFramework(fmt.Sprintf("plugin-task-%s", cfg.AgentType))
		taskName := fmt.Sprintf("plugin-task-%s", cfg.AgentType)

		BeforeEach(func() {
			if cfg.credentialsMissing() {
				Skip(cfg.EnvVar + " not set")
			}
		})

		It("should run a Task with an AgentConfig that has a plugin", func() {
			By("creating credentials secret")
			f.CreateSecret(cfg.SecretName, cfg.SecretKey+"="+*cfg.SecretValue)

			By("creating an AgentConfig with a plugin (skill definition)")
			framework.Kelos("create", "agentconfig", "plugin-ac",
				"-n", f.Namespace,
				"--skill", "hello=When asked to greet, print 'Hello from plugin skill'",
			)

			By("creating a Task referencing the AgentConfig")
			f.CreateTask(&kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name: taskName,
				},
				Spec: kelos.TaskSpec{
					Type:   cfg.AgentType,
					Model:  cfg.Model,
					Prompt: "Print 'Hello from plugin e2e test' to stdout",
					Credentials: &kelos.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelos.SecretReference{Name: cfg.SecretName},
					},
					AgentConfigRefs: []kelos.AgentConfigReference{{Name: "plugin-ac"}},
				},
			})

			By("waiting for Job to be created")
			f.WaitForJobCreation(taskName)

			By("waiting for Job to complete")
			f.WaitForJobCompletion(taskName)

			By("verifying Task status is Succeeded")
			f.WaitForTaskPhase(taskName, "Succeeded")

			By("getting Job logs")
			logs := f.GetJobLogs(taskName)
			GinkgoWriter.Printf("Job logs:\n%s\n", logs)
		})
	})
}

var _ = func() bool {
	for _, cfg := range agentConfigs {
		describePluginTaskTests(cfg)
	}
	return true
}()
