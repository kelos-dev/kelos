package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
	"github.com/gjkim42/axon/internal/cli"
)

const (
	cliTimeout  = time.Second * 10
	cliInterval = time.Millisecond * 250
)

var _ = Describe("CLI Workspace Commands", func() {
	Context("When completing Workspace names", func() {
		It("Should return Workspace names from the cluster", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-workspace",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating Workspaces")
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

	Context("When completing Workspace names for delete", func() {
		It("Should return Workspace names from the cluster", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-ws-delete",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ws-to-delete",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/org/repo.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()
			output := runComplete(kubeconfigPath, ns.Name, "delete", "workspace", "")
			Expect(output).To(ContainSubstring("ws-to-delete"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})

	Context("When completing TaskSpawner names for delete", func() {
		It("Should return TaskSpawner names from the cluster", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-ts-delete",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ts-to-delete",
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
			Expect(output).To(ContainSubstring("ts-to-delete"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})

	Context("When using create workspace via CLI", func() {
		It("Should create a Workspace resource in the cluster", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-create-ws",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Running create workspace command")
			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"--kubeconfig", kubeconfigPath,
				"-n", ns.Name,
				"create", "workspace", "cli-test-ws",
				"--repo", "https://github.com/org/repo.git",
				"--ref", "develop",
			})
			Expect(root.Execute()).To(Succeed())

			By("Verifying the Workspace exists in the cluster")
			ws := &axonv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "cli-test-ws",
				Namespace: ns.Name,
			}, ws)).To(Succeed())
			Expect(ws.Spec.Repo).To(Equal("https://github.com/org/repo.git"))
			Expect(ws.Spec.Ref).To(Equal("develop"))
		})
	})

	Context("When using get workspace via CLI", func() {
		It("Should execute successfully for list and detail", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-get-ws",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "get-test-ws",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/org/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Listing workspaces")
			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"--kubeconfig", kubeconfigPath,
				"-n", ns.Name,
				"get", "workspace",
			})
			Expect(root.Execute()).To(Succeed())

			By("Getting workspace detail")
			root = cli.NewRootCommand()
			root.SetArgs([]string{
				"--kubeconfig", kubeconfigPath,
				"-n", ns.Name,
				"get", "workspace", "get-test-ws",
			})
			Expect(root.Execute()).To(Succeed())

			By("Getting workspace in YAML format")
			root = cli.NewRootCommand()
			root.SetArgs([]string{
				"--kubeconfig", kubeconfigPath,
				"-n", ns.Name,
				"get", "workspace", "get-test-ws", "-o", "yaml",
			})
			Expect(root.Execute()).To(Succeed())

			By("Getting workspace in JSON format")
			root = cli.NewRootCommand()
			root.SetArgs([]string{
				"--kubeconfig", kubeconfigPath,
				"-n", ns.Name,
				"get", "workspace", "get-test-ws", "-o", "json",
			})
			Expect(root.Execute()).To(Succeed())
		})
	})

	Context("When using delete workspace via CLI", func() {
		It("Should delete the Workspace resource", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-delete-ws",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "delete-test-ws",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/org/repo.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Deleting workspace via CLI")
			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"--kubeconfig", kubeconfigPath,
				"-n", ns.Name,
				"delete", "workspace", "delete-test-ws",
			})
			Expect(root.Execute()).To(Succeed())

			By("Verifying the Workspace is deleted")
			deleted := &axonv1alpha1.Workspace{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      "delete-test-ws",
				Namespace: ns.Name,
			}, deleted)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When using delete taskspawner via CLI", func() {
		It("Should delete the TaskSpawner resource", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-delete-ts",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "delete-test-ts",
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

			By("Deleting taskspawner via CLI")
			root := cli.NewRootCommand()
			root.SetArgs([]string{
				"--kubeconfig", kubeconfigPath,
				"-n", ns.Name,
				"delete", "taskspawner", "delete-test-ts",
			})
			Expect(root.Execute()).To(Succeed())

			By("Verifying the TaskSpawner is eventually deleted")
			Eventually(func() bool {
				deleted := &axonv1alpha1.TaskSpawner{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "delete-test-ts",
					Namespace: ns.Name,
				}, deleted)
				return err != nil
			}, cliTimeout, cliInterval).Should(BeTrue())
		})
	})
})
