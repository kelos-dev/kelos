package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/cli"
)

func runCLI(kubeconfigPath, namespace string, args ...string) error {
	root := cli.NewRootCommand()
	fullArgs := append([]string{"--kubeconfig", kubeconfigPath, "-n", namespace}, args...)
	root.SetArgs(fullArgs)
	return root.Execute()
}

var _ = Describe("CLI Workspace Commands", func() {
	Context("When creating a workspace via CLI", func() {
		It("Should create and get and delete a workspace", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-workspace",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Creating a workspace via CLI")
			err := runCLI(kubeconfigPath, ns.Name,
				"create", "workspace",
				"--name", "my-ws",
				"--repo", "https://github.com/org/repo.git",
				"--ref", "main",
			)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the workspace exists in the cluster")
			ws := &axonv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "my-ws", Namespace: ns.Name}, ws)).To(Succeed())
			Expect(ws.Spec.Repo).To(Equal("https://github.com/org/repo.git"))
			Expect(ws.Spec.Ref).To(Equal("main"))

			By("Getting the workspace via CLI succeeds")
			err = runCLI(kubeconfigPath, ns.Name, "get", "workspace", "my-ws")
			Expect(err).NotTo(HaveOccurred())

			By("Listing workspaces via CLI succeeds")
			err = runCLI(kubeconfigPath, ns.Name, "get", "workspaces")
			Expect(err).NotTo(HaveOccurred())

			By("Getting workspace in YAML format succeeds")
			err = runCLI(kubeconfigPath, ns.Name, "get", "workspace", "my-ws", "-o", "yaml")
			Expect(err).NotTo(HaveOccurred())

			By("Getting workspace in JSON format succeeds")
			err = runCLI(kubeconfigPath, ns.Name, "get", "workspace", "my-ws", "-o", "json")
			Expect(err).NotTo(HaveOccurred())

			By("Deleting the workspace via CLI")
			err = runCLI(kubeconfigPath, ns.Name, "delete", "workspace", "my-ws")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the workspace is deleted")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "my-ws", Namespace: ns.Name}, ws)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When creating a workspace with secret", func() {
		It("Should create a workspace with secretRef", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-ws-secret",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Creating a workspace with --secret flag")
			err := runCLI(kubeconfigPath, ns.Name,
				"create", "workspace",
				"--name", "secret-ws",
				"--repo", "https://github.com/org/repo.git",
				"--secret", "my-gh-secret",
			)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the workspace has secretRef")
			ws := &axonv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "secret-ws", Namespace: ns.Name}, ws)).To(Succeed())
			Expect(ws.Spec.SecretRef).NotTo(BeNil())
			Expect(ws.Spec.SecretRef.Name).To(Equal("my-gh-secret"))
		})
	})

	Context("When using workspace aliases", func() {
		It("Should support 'ws' alias for create, get, and delete", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-ws-alias",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Creating a workspace via CLI with 'ws' alias")
			err := runCLI(kubeconfigPath, ns.Name,
				"create", "ws",
				"--name", "alias-ws",
				"--repo", "https://github.com/org/repo.git",
			)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying workspace exists")
			ws := &axonv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "alias-ws", Namespace: ns.Name}, ws)).To(Succeed())

			By("Getting workspace using 'ws' alias succeeds")
			err = runCLI(kubeconfigPath, ns.Name, "get", "ws", "alias-ws")
			Expect(err).NotTo(HaveOccurred())

			By("Deleting workspace using 'ws' alias")
			err = runCLI(kubeconfigPath, ns.Name, "delete", "ws", "alias-ws")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying workspace is deleted")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "alias-ws", Namespace: ns.Name}, ws)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When completing workspace names", func() {
		It("Should return workspace names from the cluster", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-workspace",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating workspaces")
			for _, name := range []string{"ws-alpha", "ws-beta"} {
				ws := &axonv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: ns.Name,
					},
					Spec: axonv1alpha1.WorkspaceSpec{
						Repo: "https://github.com/org/repo.git",
					},
				}
				Expect(k8sClient.Create(ctx, ws)).Should(Succeed())
			}

			kubeconfigPath := writeEnvtestKubeconfig()
			output := runComplete(kubeconfigPath, ns.Name, "get", "workspace", "")
			Expect(output).To(ContainSubstring("ws-alpha"))
			Expect(output).To(ContainSubstring("ws-beta"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})
})

var _ = Describe("CLI Delete TaskSpawner Command", func() {
	Context("When deleting a task spawner via CLI", func() {
		It("Should delete the task spawner", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-delete-ts",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner directly")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-spawner",
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

			By("Deleting the task spawner via CLI")
			err := runCLI(kubeconfigPath, ns.Name, "delete", "taskspawner", "my-spawner")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the task spawner is deleted")
			Eventually(func() bool {
				ts2 := &axonv1alpha1.TaskSpawner{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "my-spawner", Namespace: ns.Name}, ts2)
				if err == nil {
					return ts2.DeletionTimestamp != nil
				}
				return apierrors.IsNotFound(err)
			}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
		})
	})

	Context("When using 'ts' alias", func() {
		It("Should support 'ts' alias for delete", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-delete-ts-alias",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner directly")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "alias-spawner",
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

			By("Deleting using 'ts' alias")
			err := runCLI(kubeconfigPath, ns.Name, "delete", "ts", "alias-spawner")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the task spawner is deleted")
			Eventually(func() bool {
				ts2 := &axonv1alpha1.TaskSpawner{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "alias-spawner", Namespace: ns.Name}, ts2)
				if err == nil {
					return ts2.DeletionTimestamp != nil
				}
				return apierrors.IsNotFound(err)
			}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
		})
	})

	Context("When completing delete taskspawner names", func() {
		It("Should return TaskSpawner names for delete command", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-del-ts",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-del",
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
			output := runComplete(kubeconfigPath, ns.Name, "delete", "taskspawner", "")
			Expect(output).To(ContainSubstring("spawner-del"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})

	Context("When completing delete workspace names", func() {
		It("Should return Workspace names for delete command", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-del-ws",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ws-del",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/org/repo.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()
			output := runComplete(kubeconfigPath, ns.Name, "delete", "workspace", "")
			Expect(output).To(ContainSubstring("ws-del"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})
})
