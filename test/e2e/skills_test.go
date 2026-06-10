package e2e

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

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
		ac, err := f.KelosClientset.ApiV1alpha1().AgentConfigs(f.Namespace).Get(
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

		By("creating an AgentConfig with skills.sh packages")
		framework.Kelos("create", "agentconfig", "skills-ac",
			"-n", f.Namespace,
			"--skills-sh", "anthropics/skills:skill-creator",
		)

		By("creating a Task referencing the AgentConfig")
		f.CreateTask(&kelosv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "skills-task",
			},
			Spec: kelosv1alpha1.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'Hello from skills.sh e2e test' to stdout",
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeOAuth,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "claude-credentials"},
				},
				AgentConfigRef: &kelosv1alpha1.AgentConfigReference{
					Name: "skills-ac",
				},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("skills-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("skills-task")

		By("verifying Task status is Succeeded")
		f.WaitForTaskPhase("skills-task", "Succeeded")

		By("getting Job logs")
		logs := f.GetJobLogs("skills-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})

func describePluginTaskTests(cfg agentTestConfig) {
	Describe(fmt.Sprintf("Task with plugin AgentConfig [%s]", cfg.AgentType), func() {
		f := framework.NewFramework(fmt.Sprintf("plugin-task-%s", cfg.AgentType))
		taskName := fmt.Sprintf("plugin-task-%s", cfg.AgentType)

		BeforeEach(func() {
			if *cfg.SecretValue == "" {
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
			f.CreateTask(&kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name: taskName,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   cfg.AgentType,
					Model:  cfg.Model,
					Prompt: "Print 'Hello from plugin e2e test' to stdout",
					Credentials: kelosv1alpha1.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelosv1alpha1.SecretReference{Name: cfg.SecretName},
					},
					AgentConfigRef: &kelosv1alpha1.AgentConfigReference{
						Name: "plugin-ac",
					},
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
