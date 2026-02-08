package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/controller"
)

var _ = Describe("TaskSpawner Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When creating a TaskSpawner with GitHub source", func() {
		It("Should create a Deployment and update status", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-github",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/axon-core/axon.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
							WorkspaceRef: &axonv1alpha1.WorkspaceReference{
								Name: "test-workspace",
							},
							State: "open",
						},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
					},
					PollInterval: "5m",
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &axonv1alpha1.TaskSpawner{}

			By("Verifying the TaskSpawner has a finalizer")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return false
				}
				for _, f := range createdTS.Finalizers {
					if f == "axon.io/taskspawner-finalizer" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the Deployment labels")
			Expect(createdDeploy.Labels["axon.io/taskspawner"]).To(Equal(ts.Name))

			By("Verifying the Deployment spec")
			Expect(createdDeploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := createdDeploy.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("spawner"))
			Expect(container.Image).To(Equal(controller.DefaultSpawnerImage))
			Expect(container.Args).To(ConsistOf(
				"--taskspawner-name="+ts.Name,
				"--taskspawner-namespace="+ns.Name,
				"--github-owner=axon-core",
				"--github-repo=axon",
			))

			By("Verifying the ServiceAccount")
			sa := &corev1.ServiceAccount{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: controller.SpawnerServiceAccount, Namespace: ns.Name}, sa)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdDeploy.Spec.Template.Spec.ServiceAccountName).To(Equal(controller.SpawnerServiceAccount))

			By("Verifying the Deployment has owner reference")
			Expect(createdDeploy.OwnerReferences).To(HaveLen(1))
			Expect(createdDeploy.OwnerReferences[0].Name).To(Equal(ts.Name))
			Expect(createdDeploy.OwnerReferences[0].Kind).To(Equal("TaskSpawner"))

			By("Verifying TaskSpawner status has deploymentName")
			Eventually(func() string {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.DeploymentName
			}, timeout, interval).Should(Equal(ts.Name))

			By("Verifying TaskSpawner phase is Pending")
			Expect(createdTS.Status.Phase).To(Equal(axonv1alpha1.TaskSpawnerPhasePending))
		})
	})

	Context("When creating a TaskSpawner with workspace secretRef", func() {
		It("Should create a Deployment with GITHUB_TOKEN env var", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-token",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Secret with GitHub token")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-token",
					Namespace: ns.Name,
				},
				StringData: map[string]string{
					"GITHUB_TOKEN": "test-github-token",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a Workspace with secretRef")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-token",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/axon-core/axon.git",
					Ref:  "main",
					SecretRef: &axonv1alpha1.SecretReference{
						Name: "github-token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with workspace secretRef")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-token",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
							WorkspaceRef: &axonv1alpha1.WorkspaceReference{
								Name: "test-workspace-token",
							},
						},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the Deployment has GITHUB_TOKEN env var")
			container := createdDeploy.Spec.Template.Spec.Containers[0]
			Expect(container.Env).To(HaveLen(1))
			Expect(container.Env[0].Name).To(Equal("GITHUB_TOKEN"))
			Expect(container.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("github-token"))
			Expect(container.Env[0].ValueFrom.SecretKeyRef.Key).To(Equal("GITHUB_TOKEN"))
		})
	})

	Context("When deleting a TaskSpawner", func() {
		It("Should clean up and remove the finalizer", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-delete",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-delete",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/axon-core/axon.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-delete",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
							WorkspaceRef: &axonv1alpha1.WorkspaceReference{
								Name: "test-workspace-delete",
							},
						},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &axonv1alpha1.TaskSpawner{}

			By("Waiting for the Deployment to be created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Deleting the TaskSpawner")
			Expect(k8sClient.Delete(ctx, ts)).Should(Succeed())

			By("Verifying the TaskSpawner is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err != nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("Idempotency", func() {
		It("Should not create duplicate Deployments on re-reconcile", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-idempotent",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-idempotent",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/axon-core/axon.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-idempotent",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
							WorkspaceRef: &axonv1alpha1.WorkspaceReference{
								Name: "test-workspace-idempotent",
							},
						},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Waiting for the Deployment to be created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying only 1 Deployment exists")
			deployList := &appsv1.DeploymentList{}
			Expect(k8sClient.List(ctx, deployList,
				client.InNamespace(ns.Name),
				client.MatchingLabels{"axon.io/taskspawner": ts.Name},
			)).Should(Succeed())
			Expect(deployList.Items).To(HaveLen(1))

			By("Triggering re-reconcile by updating TaskSpawner")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			updatedTS := &axonv1alpha1.TaskSpawner{}
			Expect(k8sClient.Get(ctx, tsLookupKey, updatedTS)).Should(Succeed())
			if updatedTS.Annotations == nil {
				updatedTS.Annotations = map[string]string{}
			}
			updatedTS.Annotations["test"] = "trigger-reconcile"
			Expect(k8sClient.Update(ctx, updatedTS)).Should(Succeed())

			By("Verifying still only 1 Deployment exists after re-reconcile")
			Consistently(func() int {
				dl := &appsv1.DeploymentList{}
				err := k8sClient.List(ctx, dl,
					client.InNamespace(ns.Name),
					client.MatchingLabels{"axon.io/taskspawner": ts.Name},
				)
				if err != nil {
					return -1
				}
				return len(dl.Items)
			}, time.Second*2, interval).Should(Equal(1))
		})
	})

	Context("When creating a TaskSpawner with types filter", func() {
		It("Should create a Deployment and preserve types in spec", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-types",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-types",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/axon-core/axon.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with types=[issues, pulls]")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-types",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
							WorkspaceRef: &axonv1alpha1.WorkspaceReference{
								Name: "test-workspace-types",
							},
							Types: []string{"issues", "pulls"},
							State: "open",
						},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
					},
					PollInterval: "5m",
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying the TaskSpawner spec preserves types")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &axonv1alpha1.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdTS.Spec.When.GitHubIssues.Types).To(ConsistOf("issues", "pulls"))

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a TaskSpawner with a nonexistent workspace", func() {
		It("Should fail with a meaningful error", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-no-workspace",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner referencing a nonexistent Workspace")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-no-workspace",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
							WorkspaceRef: &axonv1alpha1.WorkspaceReference{
								Name: "nonexistent-workspace",
							},
						},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying the TaskSpawner phase is Failed")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &axonv1alpha1.TaskSpawner{}

			Eventually(func() axonv1alpha1.TaskSpawnerPhase {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.Phase
			}, timeout, interval).Should(Equal(axonv1alpha1.TaskSpawnerPhaseFailed))

			Expect(createdTS.Status.Message).To(ContainSubstring("nonexistent-workspace"))
		})
	})

	Context("When creating a TaskSpawner with Cron source", func() {
		It("Should create a Deployment and update status", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-cron",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner with cron source")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-cron",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						Cron: &axonv1alpha1.Cron{
							Schedule: "0 9 * * 1",
						},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
					},
					PollInterval: "5m",
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &axonv1alpha1.TaskSpawner{}

			By("Verifying the TaskSpawner has a finalizer")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return false
				}
				for _, f := range createdTS.Finalizers {
					if f == "axon.io/taskspawner-finalizer" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the Deployment labels")
			Expect(createdDeploy.Labels["axon.io/taskspawner"]).To(Equal(ts.Name))

			By("Verifying the Deployment spec")
			Expect(createdDeploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := createdDeploy.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("spawner"))
			Expect(container.Image).To(Equal(controller.DefaultSpawnerImage))
			Expect(container.Args).To(ConsistOf(
				"--taskspawner-name="+ts.Name,
				"--taskspawner-namespace="+ns.Name,
			))

			By("Verifying the Deployment has no env vars (cron needs no secrets)")
			Expect(container.Env).To(BeEmpty())

			By("Verifying the Deployment has owner reference")
			Expect(createdDeploy.OwnerReferences).To(HaveLen(1))
			Expect(createdDeploy.OwnerReferences[0].Name).To(Equal(ts.Name))
			Expect(createdDeploy.OwnerReferences[0].Kind).To(Equal("TaskSpawner"))

			By("Verifying TaskSpawner status has deploymentName")
			Eventually(func() string {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.DeploymentName
			}, timeout, interval).Should(Equal(ts.Name))

			By("Verifying TaskSpawner phase is Pending")
			Expect(createdTS.Status.Phase).To(Equal(axonv1alpha1.TaskSpawnerPhasePending))
		})
	})
})
