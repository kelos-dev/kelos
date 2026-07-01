package e2e

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("API conversion", func() {
	f := framework.NewFramework("conversion")

	It("should convert AgentConfig MCP env between v1alpha1 and v1alpha2", func() {
		ctx := context.TODO()

		By("creating a v1alpha1 AgentConfig with MCP env as a map")
		_, err := f.KelosClientset.ApiV1alpha1().AgentConfigs(f.Namespace).Create(ctx, &kelosv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-config",
				Namespace: f.Namespace,
			},
			Spec: kelosv1alpha1.AgentConfigSpec{
				MCPServers: []kelosv1alpha1.MCPServerSpec{{
					Name:    "local",
					Type:    "stdio",
					Command: "run",
					Env: map[string]string{
						"LOG_LEVEL": "debug",
						"TOKEN":     "literal-token",
					},
					EnvFrom: &kelosv1alpha1.SecretValuesSource{
						SecretRef: kelosv1alpha1.SecretReference{Name: "mcp-env"},
					},
				}},
			},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("reading it as v1alpha2")
		gotV2, err := f.KelosClientset.ApiV1alpha2().AgentConfigs(f.Namespace).Get(ctx, "legacy-config", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(gotV2.Spec.MCPServers).To(HaveLen(1))
		Expect(gotV2.Spec.MCPServers[0].Env).To(ConsistOf(
			corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"},
			corev1.EnvVar{Name: "TOKEN", Value: "literal-token"},
		))
		Expect(gotV2.Spec.MCPServers[0].EnvFrom).NotTo(BeNil())
		Expect(gotV2.Spec.MCPServers[0].EnvFrom.SecretRef.Name).To(Equal("mcp-env"))

		By("creating a v1alpha2 AgentConfig with literal and valueFrom env")
		_, err = f.KelosClientset.ApiV1alpha2().AgentConfigs(f.Namespace).Create(ctx, &kelos.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "current-config",
				Namespace: f.Namespace,
			},
			Spec: kelos.AgentConfigSpec{
				MCPServers: []kelos.MCPServerSpec{{
					Name:    "local",
					Type:    "stdio",
					Command: "run",
					Env: []corev1.EnvVar{
						{Name: "LOG_LEVEL", Value: "debug"},
						{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-secret"},
								Key:                  "token",
							},
						}},
					},
				}},
			},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("reading it as v1alpha1")
		gotV1, err := f.KelosClientset.ApiV1alpha1().AgentConfigs(f.Namespace).Get(ctx, "current-config", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(gotV1.Spec.MCPServers).To(HaveLen(1))
		Expect(gotV1.Spec.MCPServers[0].Env).To(Equal(map[string]string{"LOG_LEVEL": "debug"}))

		By("confirming the v1alpha2 valueFrom entry remains available")
		gotV2, err = f.KelosClientset.ApiV1alpha2().AgentConfigs(f.Namespace).Get(ctx, "current-config", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(gotV2.Spec.MCPServers).To(HaveLen(1))
		env, ok := envVarByName(gotV2.Spec.MCPServers[0].Env, "TOKEN")
		Expect(ok).To(BeTrue())
		Expect(env.ValueFrom).NotTo(BeNil())
		Expect(env.ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(env.ValueFrom.SecretKeyRef.Name).To(Equal("mcp-secret"))
		Expect(env.ValueFrom.SecretKeyRef.Key).To(Equal("token"))
	})

	It("should convert Task agentConfigRef to v1alpha2 agentConfigRefs", func() {
		ctx := context.TODO()

		By("creating a v1alpha1 Task with a singular agentConfigRef")
		_, err := f.KelosClientset.ApiV1alpha1().Tasks(f.Namespace).Create(ctx, &kelosv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-task",
				Namespace: f.Namespace,
			},
			Spec: kelosv1alpha1.TaskSpec{
				Type:        "claude-code",
				Prompt:      "Wait for conversion verification",
				Credentials: kelosv1alpha1.Credentials{Type: kelosv1alpha1.CredentialTypeNone},
				AgentConfigRef: &kelosv1alpha1.AgentConfigReference{
					Name: "legacy-config",
				},
				DependsOn: []string{"missing-dependency"},
			},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("reading it as v1alpha2")
		got, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(ctx, "legacy-task", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Spec.AgentConfigRefs).To(Equal([]kelos.AgentConfigReference{{Name: "legacy-config"}}))
	})

	It("should convert TaskSpawner legacy poll, comments, and agent config fields", func() {
		ctx := context.TODO()
		suspend := true

		By("creating a v1alpha1 TaskSpawner with legacy fields")
		_, err := f.KelosClientset.ApiV1alpha1().TaskSpawners(f.Namespace).Create(ctx, &kelosv1alpha1.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-spawner",
				Namespace: f.Namespace,
			},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				PollInterval: "7m",
				Suspend:      &suspend,
				When: kelosv1alpha1.When{
					GitHubIssues: &kelosv1alpha1.GitHubIssues{
						Repo:            "kelos-dev/kelos",
						Labels:          []string{"ready"},
						TriggerComment:  "/kelos pick-up",
						ExcludeComments: []string{"/kelos pause"},
					},
				},
				TaskTemplate: kelosv1alpha1.TaskTemplate{
					Type:           "claude-code",
					Credentials:    kelosv1alpha1.Credentials{Type: kelosv1alpha1.CredentialTypeNone},
					WorkspaceRef:   &kelosv1alpha1.WorkspaceReference{Name: "missing-workspace"},
					PromptTemplate: "Handle {{.Title}}",
					AgentConfigRef: &kelosv1alpha1.AgentConfigReference{Name: "legacy-config"},
				},
			},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("reading it as v1alpha2")
		gotV2, err := f.KelosClientset.ApiV1alpha2().TaskSpawners(f.Namespace).Get(ctx, "legacy-spawner", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(gotV2.Spec.When.GitHubIssues).NotTo(BeNil())
		Expect(gotV2.Spec.When.GitHubIssues.PollInterval).To(Equal("7m"))
		Expect(gotV2.Spec.When.GitHubIssues.CommentPolicy).To(Equal(&kelos.GitHubCommentPolicy{
			TriggerComment:  "/kelos pick-up",
			ExcludeComments: []string{"/kelos pause"},
		}))
		Expect(gotV2.Spec.TaskTemplate.AgentConfigRefs).To(Equal([]kelos.AgentConfigReference{{Name: "legacy-config"}}))

		By("creating a v1alpha2 TaskSpawner with current fields")
		_, err = f.KelosClientset.ApiV1alpha2().TaskSpawners(f.Namespace).Create(ctx, &kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "current-spawner",
				Namespace: f.Namespace,
			},
			Spec: kelos.TaskSpawnerSpec{
				Suspend: &suspend,
				When: kelos.When{
					GitHubPullRequests: &kelos.GitHubPullRequests{
						Repo:         "kelos-dev/kelos",
						PollInterval: "9m",
						CommentPolicy: &kelos.GitHubCommentPolicy{
							TriggerComment:  "/kelos review",
							ExcludeComments: []string{"/kelos skip"},
						},
					},
				},
				TaskTemplate: kelos.TaskTemplate{
					Type:            "claude-code",
					Credentials:     &kelos.Credentials{Type: kelos.CredentialTypeNone},
					WorkspaceRef:    &kelos.WorkspaceReference{Name: "missing-workspace"},
					PromptTemplate:  "Review {{.Title}}",
					AgentConfigRefs: []kelos.AgentConfigReference{{Name: "current-config"}},
				},
			},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("reading it as v1alpha1")
		gotV1, err := f.KelosClientset.ApiV1alpha1().TaskSpawners(f.Namespace).Get(ctx, "current-spawner", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(gotV1.Spec.PollInterval).To(Equal("9m"))
		Expect(gotV1.Spec.When.GitHubPullRequests).NotTo(BeNil())
		Expect(gotV1.Spec.When.GitHubPullRequests.TriggerComment).To(Equal("/kelos review"))
		Expect(gotV1.Spec.When.GitHubPullRequests.ExcludeComments).To(Equal([]string{"/kelos skip"}))
		Expect(gotV1.Spec.TaskTemplate.AgentConfigRefs).To(Equal([]kelosv1alpha1.AgentConfigReference{{Name: "current-config"}}))
	})
})

func envVarByName(env []corev1.EnvVar, name string) (corev1.EnvVar, bool) {
	for _, item := range env {
		if item.Name == name {
			return item, true
		}
	}
	return corev1.EnvVar{}, false
}
