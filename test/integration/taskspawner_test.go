package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
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
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "test-workspace",
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
						GitHubIssues: &axonv1alpha1.GitHubIssues{},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "test-workspace-token",
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
						GitHubIssues: &axonv1alpha1.GitHubIssues{},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "test-workspace-delete",
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
						GitHubIssues: &axonv1alpha1.GitHubIssues{},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "test-workspace-idempotent",
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
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "test-workspace-types",
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
		It("Should not create a Deployment and keep retrying", func() {
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
						GitHubIssues: &axonv1alpha1.GitHubIssues{},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialTypeOAuth,
							SecretRef: axonv1alpha1.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "nonexistent-workspace",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying no Deployment is created while workspace is missing")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}

			Consistently(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err != nil
			}, 3*time.Second, interval).Should(BeTrue())

			By("Verifying the TaskSpawner is not marked as Failed")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &axonv1alpha1.TaskSpawner{}

			Consistently(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return true
				}
				return createdTS.Status.Phase != axonv1alpha1.TaskSpawnerPhaseFailed
			}, 3*time.Second, interval).Should(BeTrue())

			By("Creating the Workspace and verifying the Deployment is eventually created")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nonexistent-workspace",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/axon-core/axon.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a TaskSpawner with maxConcurrency", func() {
		It("Should store maxConcurrency in spec and activeTasks in status", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-maxconc",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-maxconc",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/axon-core/axon.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with maxConcurrency=3")
			maxConc := int32(3)
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-maxconc",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
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
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "test-workspace-maxconc",
						},
					},
					PollInterval:   "5m",
					MaxConcurrency: &maxConc,
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying maxConcurrency is stored in spec")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &axonv1alpha1.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.MaxConcurrency).NotTo(BeNil())
			Expect(*createdTS.Spec.MaxConcurrency).To(Equal(int32(3)))

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Updating activeTasks in status")
			Eventually(func() error {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return err
				}
				createdTS.Status.ActiveTasks = 2
				return k8sClient.Status().Update(ctx, createdTS)
			}, timeout, interval).Should(Succeed())

			By("Verifying activeTasks is stored in status")
			updatedTS := &axonv1alpha1.TaskSpawner{}
			Eventually(func() int {
				err := k8sClient.Get(ctx, tsLookupKey, updatedTS)
				if err != nil {
					return -1
				}
				return updatedTS.Status.ActiveTasks
			}, timeout, interval).Should(Equal(2))
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

	Context("When creating a TaskSpawner with GitHub App workspace", func() {
		It("Should create a Deployment with token-refresher sidecar", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-github-app",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Generating a test RSA key for GitHub App")
			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			Expect(err).NotTo(HaveOccurred())
			keyPEM := pem.EncodeToMemory(&pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
			})

			By("Creating a Secret with GitHub App credentials")
			ghAppSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-app-creds",
					Namespace: ns.Name,
				},
				Data: map[string][]byte{
					"appID":          []byte("12345"),
					"installationID": []byte("67890"),
					"privateKey":     keyPEM,
				},
			}
			Expect(k8sClient.Create(ctx, ghAppSecret)).Should(Succeed())

			By("Creating a Workspace with GitHub App secretRef")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-app",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: "https://github.com/axon-core/axon.git",
					Ref:  "main",
					SecretRef: &axonv1alpha1.SecretReference{
						Name: "github-app-creds",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-app",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
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
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "test-workspace-app",
						},
					},
					PollInterval: "5m",
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

			By("Verifying the Deployment has 1 container (spawner) and 1 native sidecar init container (token-refresher)")
			Expect(createdDeploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(createdDeploy.Spec.Template.Spec.InitContainers).To(HaveLen(1))

			spawner := createdDeploy.Spec.Template.Spec.Containers[0]
			Expect(spawner.Name).To(Equal("spawner"))

			refresher := createdDeploy.Spec.Template.Spec.InitContainers[0]
			Expect(refresher.Name).To(Equal("token-refresher"))
			Expect(refresher.Image).To(Equal(controller.DefaultTokenRefresherImage))

			By("Verifying the token-refresher uses native sidecar (restartPolicy: Always)")
			Expect(refresher.RestartPolicy).NotTo(BeNil())
			Expect(*refresher.RestartPolicy).To(Equal(corev1.ContainerRestartPolicyAlways))

			By("Verifying the spawner has --github-token-file flag")
			Expect(spawner.Args).To(ContainElement("--github-token-file=/shared/token/GITHUB_TOKEN"))

			By("Verifying the spawner does NOT have GITHUB_TOKEN env var")
			for _, env := range spawner.Env {
				Expect(env.Name).NotTo(Equal("GITHUB_TOKEN"))
			}

			By("Verifying the token-refresher has APP_ID and INSTALLATION_ID env vars")
			Expect(refresher.Env).To(HaveLen(2))
			Expect(refresher.Env[0].Name).To(Equal("APP_ID"))
			Expect(refresher.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("github-app-creds"))
			Expect(refresher.Env[0].ValueFrom.SecretKeyRef.Key).To(Equal("appID"))
			Expect(refresher.Env[1].Name).To(Equal("INSTALLATION_ID"))
			Expect(refresher.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("github-app-creds"))
			Expect(refresher.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("installationID"))

			By("Verifying the Deployment has 2 volumes")
			Expect(createdDeploy.Spec.Template.Spec.Volumes).To(HaveLen(2))

			var tokenVol, secretVol *corev1.Volume
			for i, v := range createdDeploy.Spec.Template.Spec.Volumes {
				switch v.Name {
				case "github-token":
					tokenVol = &createdDeploy.Spec.Template.Spec.Volumes[i]
				case "github-app-secret":
					secretVol = &createdDeploy.Spec.Template.Spec.Volumes[i]
				}
			}
			Expect(tokenVol).NotTo(BeNil())
			Expect(tokenVol.EmptyDir).NotTo(BeNil())
			Expect(secretVol).NotTo(BeNil())
			Expect(secretVol.Secret).NotTo(BeNil())
			Expect(secretVol.Secret.SecretName).To(Equal("github-app-creds"))

			By("Verifying the spawner mounts the shared token volume (read-only)")
			Expect(spawner.VolumeMounts).To(HaveLen(1))
			Expect(spawner.VolumeMounts[0].Name).To(Equal("github-token"))
			Expect(spawner.VolumeMounts[0].MountPath).To(Equal("/shared/token"))
			Expect(spawner.VolumeMounts[0].ReadOnly).To(BeTrue())

			By("Verifying the token-refresher mounts both volumes")
			Expect(refresher.VolumeMounts).To(HaveLen(2))
		})
	})

	Context("When a TaskSpawner creates a Deployment", func() {
		It("Should emit a DeploymentCreated event", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-events",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-events",
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
					Name:      "test-spawner-events",
					Namespace: ns.Name,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
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
						WorkspaceRef: &axonv1alpha1.WorkspaceReference{
							Name: "test-workspace-events",
						},
					},
					PollInterval: "5m",
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

			By("Verifying DeploymentCreated event is emitted")
			Eventually(func() bool {
				eventList := &corev1.EventList{}
				err := k8sClient.List(ctx, eventList, client.InNamespace(ns.Name))
				if err != nil {
					return false
				}
				for _, event := range eventList.Items {
					if event.InvolvedObject.Name == ts.Name && event.Reason == "DeploymentCreated" {
						Expect(event.Type).To(Equal(corev1.EventTypeNormal))
						Expect(event.Message).To(ContainSubstring("Created spawner Deployment"))
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})
})
