package integration

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
	"github.com/gjkim42/axon/internal/cli"
)

func writeEnvtestKubeconfig() string {
	kubeconfig := clientcmdapi.NewConfig()
	kubeconfig.Clusters["envtest"] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
	}
	kubeconfig.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
	}
	kubeconfig.Contexts["envtest"] = &clientcmdapi.Context{
		Cluster:  "envtest",
		AuthInfo: "envtest",
	}
	kubeconfig.CurrentContext = "envtest"

	tmpFile := GinkgoT().TempDir() + "/kubeconfig"
	Expect(clientcmd.WriteToFile(*kubeconfig, tmpFile)).To(Succeed())
	return tmpFile
}

func runComplete(kubeconfigPath, namespace string, args ...string) string {
	root := cli.NewRootCommand()
	fullArgs := append([]string{"--kubeconfig", kubeconfigPath, "-n", namespace, "__complete"}, args...)
	root.SetArgs(fullArgs)
	out := &strings.Builder{}
	root.SetOut(out)
	Expect(root.Execute()).To(Succeed())
	return out.String()
}

var _ = Describe("Completion", func() {
	Context("When completing Task names", func() {
		It("Should return Task names from the cluster", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-task",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating Tasks")
			for _, name := range []string{"task-alpha", "task-beta"} {
				task := &axonv1alpha1.Task{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: ns.Name,
					},
					Spec: axonv1alpha1.TaskSpec{
						Type:   "claude-code",
						Prompt: "test",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeAPIKey,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, task)).Should(Succeed())
			}

			kubeconfigPath := writeEnvtestKubeconfig()
			output := runComplete(kubeconfigPath, ns.Name, "get", "task", "")
			Expect(output).To(ContainSubstring("task-alpha"))
			Expect(output).To(ContainSubstring("task-beta"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})

	Context("When completing TaskSpawner names", func() {
		It("Should return TaskSpawner names from the cluster", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-spawner",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-one",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeAPIKey,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()
			output := runComplete(kubeconfigPath, ns.Name, "get", "taskspawner", "")
			Expect(output).To(ContainSubstring("spawner-one"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})

	Context("When an argument is already provided", func() {
		It("Should not return completions", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-skip",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Task")
			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "task-gamma",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   "claude-code",
					Prompt: "test",
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialTypeAPIKey,
						SecretRef: axonv1alpha1.SecretReference{
							Name: "test-secret",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()
			output := runComplete(kubeconfigPath, ns.Name, "get", "task", "task-gamma", "")
			Expect(output).NotTo(ContainSubstring("task-gamma"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})

	Context("When the namespace has no resources", func() {
		It("Should return empty completions", func() {
			By("Creating an empty namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-empty",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()
			output := runComplete(kubeconfigPath, ns.Name, "get", "task", "")
			lines := strings.Split(strings.TrimSpace(output), "\n")
			Expect(lines).To(HaveLen(1))
			Expect(lines[0]).To(Equal(":4"))
		})
	})
})
