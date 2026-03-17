package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/cli"
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
				"create", "workspace", "my-ws",
				"--repo", "https://github.com/org/repo.git",
				"--ref", "main",
			)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the workspace exists in the cluster")
			ws := &kelosv1alpha1.Workspace{}
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
				"create", "workspace", "secret-ws",
				"--repo", "https://github.com/org/repo.git",
				"--secret", "my-gh-secret",
			)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the workspace has secretRef")
			ws := &kelosv1alpha1.Workspace{}
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
				"create", "ws", "alias-ws",
				"--repo", "https://github.com/org/repo.git",
			)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying workspace exists")
			ws := &kelosv1alpha1.Workspace{}
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
				ws := &kelosv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: ns.Name,
					},
					Spec: kelosv1alpha1.WorkspaceSpec{
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

var _ = Describe("CLI Delete All Commands", func() {
	Context("When deleting all tasks via CLI", func() {
		It("Should delete all tasks in the namespace", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-delete-all-tasks",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Creating multiple tasks directly")
			for _, name := range []string{"task-a", "task-b", "task-c"} {
				task := &kelosv1alpha1.Task{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: ns.Name,
					},
					Spec: kelosv1alpha1.TaskSpec{
						Type:   "claude-code",
						Prompt: "test",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, task)).Should(Succeed())
			}

			By("Deleting all tasks via CLI")
			err := runCLI(kubeconfigPath, ns.Name, "delete", "task", "--all")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying all tasks are deleted")
			Eventually(func() int {
				taskList := &kelosv1alpha1.TaskList{}
				Expect(k8sClient.List(ctx, taskList, client.InNamespace(ns.Name))).To(Succeed())
				count := 0
				for _, t := range taskList.Items {
					if t.DeletionTimestamp == nil {
						count++
					}
				}
				return count
			}, 10*time.Second, 250*time.Millisecond).Should(Equal(0))
		})
	})

	Context("When deleting all workspaces via CLI", func() {
		It("Should delete all workspaces in the namespace", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-delete-all-ws",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Creating multiple workspaces directly")
			for _, name := range []string{"ws-a", "ws-b"} {
				ws := &kelosv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: ns.Name,
					},
					Spec: kelosv1alpha1.WorkspaceSpec{
						Repo: "https://github.com/org/repo.git",
					},
				}
				Expect(k8sClient.Create(ctx, ws)).Should(Succeed())
			}

			By("Deleting all workspaces via CLI")
			err := runCLI(kubeconfigPath, ns.Name, "delete", "workspace", "--all")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying all workspaces are deleted")
			Eventually(func() int {
				wsList := &kelosv1alpha1.WorkspaceList{}
				Expect(k8sClient.List(ctx, wsList, client.InNamespace(ns.Name))).To(Succeed())
				return len(wsList.Items)
			}, 10*time.Second, 250*time.Millisecond).Should(Equal(0))
		})
	})

	Context("When deleting all task spawners via CLI", func() {
		It("Should delete all task spawners in the namespace", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-delete-all-ts",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Creating multiple task spawners directly")
			for _, name := range []string{"ts-a", "ts-b"} {
				ts := &kelosv1alpha1.TaskSpawner{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: ns.Name,
					},
					Spec: kelosv1alpha1.TaskSpawnerSpec{
						TaskTemplate: kelosv1alpha1.TaskTemplate{
							Type: "claude-code",
							Credentials: kelosv1alpha1.Credentials{
								Type: kelosv1alpha1.CredentialTypeAPIKey,
								SecretRef: kelosv1alpha1.SecretReference{
									Name: "test-secret",
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, ts)).Should(Succeed())
			}

			By("Deleting all task spawners via CLI")
			err := runCLI(kubeconfigPath, ns.Name, "delete", "taskspawner", "--all")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying all task spawners are deleted")
			Eventually(func() bool {
				tsList := &kelosv1alpha1.TaskSpawnerList{}
				Expect(k8sClient.List(ctx, tsList, client.InNamespace(ns.Name))).To(Succeed())
				for _, ts := range tsList.Items {
					if ts.DeletionTimestamp == nil {
						return false
					}
				}
				return true
			}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
		})
	})

	Context("When using --all with a name argument", func() {
		It("Should return an error for task", func() {
			kubeconfigPath := writeEnvtestKubeconfig()
			err := runCLI(kubeconfigPath, "default", "delete", "task", "some-task", "--all")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot specify task name with --all"))
		})

		It("Should return an error for workspace", func() {
			kubeconfigPath := writeEnvtestKubeconfig()
			err := runCLI(kubeconfigPath, "default", "delete", "workspace", "some-ws", "--all")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot specify workspace name with --all"))
		})

		It("Should return an error for taskspawner", func() {
			kubeconfigPath := writeEnvtestKubeconfig()
			err := runCLI(kubeconfigPath, "default", "delete", "taskspawner", "some-ts", "--all")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot specify task spawner name with --all"))
		})
	})

	Context("When using --all on empty namespace", func() {
		It("Should succeed with no tasks", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-delete-all-empty",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()
			err := runCLI(kubeconfigPath, ns.Name, "delete", "task", "--all")
			Expect(err).NotTo(HaveOccurred())
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
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
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
				ts2 := &kelosv1alpha1.TaskSpawner{}
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
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "alias-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
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
				ts2 := &kelosv1alpha1.TaskSpawner{}
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
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-del",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
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
			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ws-del",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.WorkspaceSpec{
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

var _ = Describe("CLI Suspend/Resume Commands", func() {
	Context("When suspending a TaskSpawner via CLI", func() {
		It("Should set suspend=true on the TaskSpawner", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-suspend-ts",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cli-suspend-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Suspending the TaskSpawner via CLI")
			err := runCLI(kubeconfigPath, ns.Name, "suspend", "taskspawner", "cli-suspend-spawner")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the TaskSpawner has suspend=true")
			updatedTS := &kelosv1alpha1.TaskSpawner{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cli-suspend-spawner", Namespace: ns.Name}, updatedTS)).To(Succeed())
			Expect(updatedTS.Spec.Suspend).NotTo(BeNil())
			Expect(*updatedTS.Spec.Suspend).To(BeTrue())
		})
	})

	Context("When suspending an already suspended TaskSpawner via CLI", func() {
		It("Should be idempotent and not error", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-suspend-idem",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner already suspended")
			suspend := true
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cli-suspend-idem",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					Suspend: &suspend,
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Suspending again via CLI (should be idempotent)")
			err := runCLI(kubeconfigPath, ns.Name, "suspend", "taskspawner", "cli-suspend-idem")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the TaskSpawner is still suspended")
			updatedTS := &kelosv1alpha1.TaskSpawner{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cli-suspend-idem", Namespace: ns.Name}, updatedTS)).To(Succeed())
			Expect(updatedTS.Spec.Suspend).NotTo(BeNil())
			Expect(*updatedTS.Spec.Suspend).To(BeTrue())
		})
	})

	Context("When resuming a suspended TaskSpawner via CLI", func() {
		It("Should set suspend=false on the TaskSpawner", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-resume-ts",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a suspended TaskSpawner")
			suspend := true
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cli-resume-spawner",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					Suspend: &suspend,
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Resuming the TaskSpawner via CLI")
			err := runCLI(kubeconfigPath, ns.Name, "resume", "taskspawner", "cli-resume-spawner")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the TaskSpawner has suspend=false")
			updatedTS := &kelosv1alpha1.TaskSpawner{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cli-resume-spawner", Namespace: ns.Name}, updatedTS)).To(Succeed())
			Expect(updatedTS.Spec.Suspend).NotTo(BeNil())
			Expect(*updatedTS.Spec.Suspend).To(BeFalse())
		})
	})

	Context("When resuming a non-suspended TaskSpawner via CLI", func() {
		It("Should be idempotent and not error", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-resume-idem",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner (not suspended)")
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cli-resume-idem",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Resuming via CLI (should be idempotent)")
			err := runCLI(kubeconfigPath, ns.Name, "resume", "taskspawner", "cli-resume-idem")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When using 'ts' alias for suspend/resume", func() {
		It("Should support 'ts' alias for suspend and resume", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cli-suspend-alias",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "alias-suspend",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Suspending using 'ts' alias")
			err := runCLI(kubeconfigPath, ns.Name, "suspend", "ts", "alias-suspend")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the TaskSpawner is suspended")
			updatedTS := &kelosv1alpha1.TaskSpawner{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "alias-suspend", Namespace: ns.Name}, updatedTS)).To(Succeed())
			Expect(updatedTS.Spec.Suspend).NotTo(BeNil())
			Expect(*updatedTS.Spec.Suspend).To(BeTrue())

			By("Resuming using 'ts' alias")
			err = runCLI(kubeconfigPath, ns.Name, "resume", "ts", "alias-suspend")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the TaskSpawner is resumed")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "alias-suspend", Namespace: ns.Name}, updatedTS)).To(Succeed())
			Expect(updatedTS.Spec.Suspend).NotTo(BeNil())
			Expect(*updatedTS.Spec.Suspend).To(BeFalse())
		})
	})

	Context("When completing suspend/resume taskspawner names", func() {
		It("Should return TaskSpawner names for suspend command", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-complete-suspend-ts",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-suspend-comp",
					Namespace: ns.Name,
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: kelosv1alpha1.Credentials{
							Type: kelosv1alpha1.CredentialTypeAPIKey,
							SecretRef: kelosv1alpha1.SecretReference{
								Name: "test-secret",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			kubeconfigPath := writeEnvtestKubeconfig()

			By("Verifying shell completion for suspend command")
			output := runComplete(kubeconfigPath, ns.Name, "suspend", "taskspawner", "")
			Expect(output).To(ContainSubstring("spawner-suspend-comp"))
			Expect(output).To(ContainSubstring(":4"))

			By("Verifying shell completion for resume command")
			output = runComplete(kubeconfigPath, ns.Name, "resume", "taskspawner", "")
			Expect(output).To(ContainSubstring("spawner-suspend-comp"))
			Expect(output).To(ContainSubstring(":4"))
		})
	})
})
