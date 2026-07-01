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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/controller"
)

var _ = Describe("TaskSpawner Controller", func() {
	const (
		// Right after the install/uninstall specs churn the CRDs, the
		// in-process informer re-establishes its watch, so reconciles need more
		// headroom than a stable resource would.
		timeout  = time.Second * 60
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
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}

			By("Verifying the TaskSpawner has a finalizer")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return false
				}
				for _, f := range createdTS.Finalizers {
					if f == "kelos.dev/taskspawner-finalizer" {
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
			Expect(createdDeploy.Labels["kelos.dev/taskspawner"]).To(Equal(ts.Name))

			By("Verifying the Deployment spec")
			Expect(createdDeploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := createdDeploy.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("spawner"))
			Expect(container.Image).To(Equal(controller.DefaultSpawnerImage))
			Expect(container.Args).To(ConsistOf(
				"--taskspawner-name="+ts.Name,
				"--taskspawner-namespace="+ns.Name,
				"--github-owner=kelos-dev",
				"--github-repo=kelos",
				"--gh-proxy-url="+controller.WorkspaceGHProxyServiceURL(ns.Name, "test-workspace"),
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
			Expect(createdTS.Status.Phase).To(Equal(kelos.TaskSpawnerPhasePending))
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
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-token",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
					SecretRef: &kelos.SecretReference{
						Name: "github-token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with workspace secretRef")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-token",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							Reporting: &kelos.GitHubReporting{Enabled: true},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
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
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-delete",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-delete",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-delete",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}

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
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-idempotent",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-idempotent",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
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
				client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
			)).Should(Succeed())
			Expect(deployList.Items).To(HaveLen(1))

			By("Triggering re-reconcile by updating TaskSpawner")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			updatedTS := &kelos.TaskSpawner{}
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
					client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
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
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-types",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with types=[issues, pulls]")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-types",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							Types: []string{"issues", "pulls"},
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-types",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying the TaskSpawner spec preserves types")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
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
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-no-workspace",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
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
			createdTS := &kelos.TaskSpawner{}

			Consistently(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return true
				}
				return createdTS.Status.Phase != kelos.TaskSpawnerPhaseFailed
			}, 3*time.Second, interval).Should(BeTrue())

			By("Creating the Workspace and verifying the Deployment is eventually created")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nonexistent-workspace",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
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
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-maxconc",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with maxConcurrency=3")
			maxConc := int32(3)
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-maxconc",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-maxconc",
						},
					},
					MaxConcurrency: &maxConc,
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying maxConcurrency is stored in spec")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
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
			updatedTS := &kelos.TaskSpawner{}
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
		It("Should create a CronJob and update status", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-cron",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner with cron source")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-cron",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						Cron: &kelos.Cron{
							Schedule: "0 9 * * 1",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}

			By("Verifying the TaskSpawner has a finalizer")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return false
				}
				for _, f := range createdTS.Finalizers {
					if f == "kelos.dev/taskspawner-finalizer" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying a CronJob is created")
			cronJobLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdCronJob := &batchv1.CronJob{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, cronJobLookupKey, createdCronJob)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the CronJob labels")
			Expect(createdCronJob.Labels["kelos.dev/taskspawner"]).To(Equal(ts.Name))

			By("Verifying the CronJob schedule")
			Expect(createdCronJob.Spec.Schedule).To(Equal("0 9 * * 1"))

			By("Verifying the CronJob concurrency policy")
			Expect(createdCronJob.Spec.ConcurrencyPolicy).To(Equal(batchv1.ForbidConcurrent))

			By("Verifying the CronJob pod spec")
			podSpec := createdCronJob.Spec.JobTemplate.Spec.Template.Spec
			Expect(podSpec.Containers).To(HaveLen(1))
			container := podSpec.Containers[0]
			Expect(container.Name).To(Equal("spawner"))
			Expect(container.Image).To(Equal(controller.DefaultSpawnerImage))
			Expect(container.Args).To(ConsistOf(
				"--taskspawner-name="+ts.Name,
				"--taskspawner-namespace="+ns.Name,
				"--one-shot",
			))

			By("Verifying the CronJob has no env vars (cron needs no secrets)")
			Expect(container.Env).To(BeEmpty())

			By("Verifying the CronJob has owner reference")
			Expect(createdCronJob.OwnerReferences).To(HaveLen(1))
			Expect(createdCronJob.OwnerReferences[0].Name).To(Equal(ts.Name))
			Expect(createdCronJob.OwnerReferences[0].Kind).To(Equal("TaskSpawner"))

			By("Verifying TaskSpawner status has cronJobName")
			Eventually(func() string {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.CronJobName
			}, timeout, interval).Should(Equal(ts.Name))

			By("Verifying TaskSpawner phase is Running")
			Expect(createdTS.Status.Phase).To(Equal(kelos.TaskSpawnerPhaseRunning))
		})
	})

	Context("When creating a TaskSpawner with GitHub App workspace", func() {
		It("Should create a Deployment with GitHub App env vars", func() {
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
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-app",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
					SecretRef: &kelos.SecretReference{
						Name: "github-app-creds",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-app",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State:     "open",
							Reporting: &kelos.GitHubReporting{Enabled: true},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-app",
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

			By("Verifying the Deployment has 1 container (spawner) and no init containers")
			Expect(createdDeploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(createdDeploy.Spec.Template.Spec.InitContainers).To(BeEmpty())

			spawner := createdDeploy.Spec.Template.Spec.Containers[0]
			Expect(spawner.Name).To(Equal("spawner"))

			By("Verifying the spawner does NOT have --github-token-file flag")
			Expect(spawner.Args).NotTo(ContainElement("--github-token-file=/shared/token/GITHUB_TOKEN"))

			By("Verifying the spawner does NOT have GITHUB_TOKEN env var")
			for _, env := range spawner.Env {
				Expect(env.Name).NotTo(Equal("GITHUB_TOKEN"))
			}

			By("Verifying the spawner has GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, GITHUB_APP_PRIVATE_KEY env vars")
			Expect(spawner.Env).To(HaveLen(3))
			Expect(spawner.Env[0].Name).To(Equal("GITHUB_APP_ID"))
			Expect(spawner.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("github-app-creds"))
			Expect(spawner.Env[0].ValueFrom.SecretKeyRef.Key).To(Equal("appID"))
			Expect(spawner.Env[1].Name).To(Equal("GITHUB_APP_INSTALLATION_ID"))
			Expect(spawner.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("github-app-creds"))
			Expect(spawner.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("installationID"))
			Expect(spawner.Env[2].Name).To(Equal("GITHUB_APP_PRIVATE_KEY"))
			Expect(spawner.Env[2].ValueFrom.SecretKeyRef.Name).To(Equal("github-app-creds"))
			Expect(spawner.Env[2].ValueFrom.SecretKeyRef.Key).To(Equal("privateKey"))

			By("Verifying the Deployment has no volumes")
			Expect(createdDeploy.Spec.Template.Spec.Volumes).To(BeEmpty())

			By("Verifying the spawner has no volume mounts")
			Expect(spawner.VolumeMounts).To(BeEmpty())
		})
	})

	Context("When creating a TaskSpawner with suspend=true", func() {
		It("Should create a Deployment with 0 replicas and set phase to Suspended", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-suspend",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-suspend",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with suspend=true")
			suspend := true
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-suspend",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-suspend",
						},
					},
					Suspend: &suspend,
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying a Deployment is created with 0 replicas")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdDeploy.Spec.Replicas).NotTo(BeNil())
			Expect(*createdDeploy.Spec.Replicas).To(Equal(int32(0)))

			By("Verifying TaskSpawner phase is Suspended")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() kelos.TaskSpawnerPhase {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.Phase
			}, timeout, interval).Should(Equal(kelos.TaskSpawnerPhaseSuspended))

			Expect(createdTS.Status.Message).To(Equal("Suspended by user"))
		})
	})

	Context("When resuming a suspended TaskSpawner", func() {
		It("Should scale the Deployment to 1 replica and set phase to Running", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-resume",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-resume",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with suspend=true")
			suspend := true
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-resume",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-resume",
						},
					},
					Suspend: &suspend,
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}

			By("Waiting for Deployment to be created with 0 replicas")
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil {
					return false
				}
				return createdDeploy.Spec.Replicas != nil && *createdDeploy.Spec.Replicas == 0
			}, timeout, interval).Should(BeTrue())

			By("Waiting for phase to be Suspended")
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() kelos.TaskSpawnerPhase {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.Phase
			}, timeout, interval).Should(Equal(kelos.TaskSpawnerPhaseSuspended))

			By("Resuming the TaskSpawner by setting suspend=false")
			Eventually(func() error {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return err
				}
				resume := false
				createdTS.Spec.Suspend = &resume
				return k8sClient.Update(ctx, createdTS)
			}, timeout, interval).Should(Succeed())

			By("Verifying the Deployment is scaled to 1 replica")
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil || createdDeploy.Spec.Replicas == nil {
					return -1
				}
				return *createdDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(1)))

			By("Verifying TaskSpawner phase is Running")
			Eventually(func() kelos.TaskSpawnerPhase {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.Phase
			}, timeout, interval).Should(Equal(kelos.TaskSpawnerPhaseRunning))

			Expect(createdTS.Status.Message).To(Equal("Resumed"))
		})
	})

	Context("When creating a TaskSpawner with maxTotalTasks", func() {
		It("Should store maxTotalTasks in spec", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-maxtotal",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-maxtotal",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with maxTotalTasks=10")
			maxTotal := int32(10)
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-maxtotal",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-maxtotal",
						},
					},
					MaxTotalTasks: &maxTotal,
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying maxTotalTasks is stored in spec")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.MaxTotalTasks).NotTo(BeNil())
			Expect(*createdTS.Spec.MaxTotalTasks).To(Equal(int32(10)))

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When suspending a running TaskSpawner", func() {
		It("Should scale the Deployment to 0 replicas and emit DeploymentUpdated event", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-suspend-running",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-suspend-running",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner (not suspended)")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-suspend-running",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-suspend-running",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}

			By("Waiting for Deployment to be created with 1 replica")
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil {
					return false
				}
				return createdDeploy.Spec.Replicas != nil && *createdDeploy.Spec.Replicas == 1
			}, timeout, interval).Should(BeTrue())

			By("Waiting for phase to be Pending")
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() kelos.TaskSpawnerPhase {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.Phase
			}, timeout, interval).Should(Equal(kelos.TaskSpawnerPhasePending))

			By("Suspending the TaskSpawner")
			Eventually(func() error {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return err
				}
				suspend := true
				createdTS.Spec.Suspend = &suspend
				return k8sClient.Update(ctx, createdTS)
			}, timeout, interval).Should(Succeed())

			By("Verifying the Deployment is scaled to 0 replicas")
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil || createdDeploy.Spec.Replicas == nil {
					return -1
				}
				return *createdDeploy.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			By("Verifying TaskSpawner phase is Suspended")
			Eventually(func() kelos.TaskSpawnerPhase {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				if err != nil {
					return ""
				}
				return createdTS.Status.Phase
			}, timeout, interval).Should(Equal(kelos.TaskSpawnerPhaseSuspended))

			By("Verifying DeploymentUpdated event is emitted")
			Eventually(func() bool {
				eventList := &corev1.EventList{}
				err := k8sClient.List(ctx, eventList, client.InNamespace(ns.Name))
				if err != nil {
					return false
				}
				for _, event := range eventList.Items {
					if event.InvolvedObject.Name == ts.Name && event.Reason == "DeploymentUpdated" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
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
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-events",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-events",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-events",
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

	Context("When creating a TaskSpawner with branch template", func() {
		It("Should store the branch template and create a Deployment", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-branch-tmpl",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with branch template")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner-branch-tmpl",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace",
						},
						Branch: "kelos-task-{{.Number}}",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying the branch template is stored in the spec")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.TaskTemplate.Branch).To(Equal("kelos-task-{{.Number}}"))

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the Deployment has owner reference")
			Expect(createdDeploy.OwnerReferences).To(HaveLen(1))
			Expect(createdDeploy.OwnerReferences[0].Name).To(Equal(ts.Name))
		})
	})

	Context("When creating a TaskSpawner with githubIssues.repo override (fork workflow)", func() {
		It("Should create a Deployment with overridden owner/repo args", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-fork",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace pointing to the fork")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-fork",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/my-fork/my-repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with githubIssues.repo pointing to upstream")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-fork",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							Repo:  "https://github.com/upstream-org/my-repo.git",
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-fork",
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

			By("Verifying the Deployment args use the upstream owner/repo")
			container := createdDeploy.Spec.Template.Spec.Containers[0]
			Expect(container.Args).To(ContainElement("--github-owner=upstream-org"))
			Expect(container.Args).To(ContainElement("--github-repo=my-repo"))
			Expect(container.Args).NotTo(ContainElement("--github-owner=my-fork"))
		})
	})

	Context("When switching workspace secret from PAT to GitHub App", func() {
		It("Should update the Deployment to add GitHub App env vars and remove GITHUB_TOKEN", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-pat-to-app",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a PAT secret")
			patSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "switch-secret",
					Namespace: ns.Name,
				},
				Data: map[string][]byte{
					"GITHUB_TOKEN": []byte("ghp_test_token"),
				},
			}
			Expect(k8sClient.Create(ctx, patSecret)).Should(Succeed())

			By("Creating a Workspace with PAT secretRef")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-switch",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
					SecretRef: &kelos.SecretReference{
						Name: "switch-secret",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner referencing the workspace")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-switch",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							Reporting: &kelos.GitHubReporting{Enabled: true},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-switch",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying a Deployment is created in PAT mode")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying initial state: PAT mode (GITHUB_TOKEN env, no init containers)")
			Expect(createdDeploy.Spec.Template.Spec.InitContainers).To(BeEmpty())
			Expect(createdDeploy.Spec.Template.Spec.Volumes).To(BeEmpty())
			Expect(createdDeploy.Spec.Template.Spec.Containers[0].Env).To(HaveLen(1))
			Expect(createdDeploy.Spec.Template.Spec.Containers[0].Env[0].Name).To(Equal("GITHUB_TOKEN"))

			By("Switching the secret to GitHub App credentials")
			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			Expect(err).NotTo(HaveOccurred())
			keyPEM := pem.EncodeToMemory(&pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
			})

			Eventually(func() error {
				secret := &corev1.Secret{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "switch-secret", Namespace: ns.Name}, secret); err != nil {
					return err
				}
				secret.Data = map[string][]byte{
					"appID":          []byte("12345"),
					"installationID": []byte("67890"),
					"privateKey":     keyPEM,
				}
				return k8sClient.Update(ctx, secret)
			}, timeout, interval).Should(Succeed())

			By("Verifying the Deployment is updated to GitHub App mode with env vars")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil {
					return false
				}
				for _, env := range createdDeploy.Spec.Template.Spec.Containers[0].Env {
					if env.Name == "GITHUB_APP_ID" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying no init containers or volumes")
			Expect(createdDeploy.Spec.Template.Spec.InitContainers).To(BeEmpty())
			Expect(createdDeploy.Spec.Template.Spec.Volumes).To(BeEmpty())

			spawner := createdDeploy.Spec.Template.Spec.Containers[0]

			By("Verifying spawner has no volume mounts and no --github-token-file arg")
			Expect(spawner.VolumeMounts).To(BeEmpty())
			Expect(spawner.Args).NotTo(ContainElement("--github-token-file=/shared/token/GITHUB_TOKEN"))

			By("Verifying GITHUB_TOKEN env var is removed")
			for _, env := range spawner.Env {
				Expect(env.Name).NotTo(Equal("GITHUB_TOKEN"))
			}

			By("Verifying GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, GITHUB_APP_PRIVATE_KEY env vars are present")
			Expect(spawner.Env).To(HaveLen(3))
			Expect(spawner.Env[0].Name).To(Equal("GITHUB_APP_ID"))
			Expect(spawner.Env[1].Name).To(Equal("GITHUB_APP_INSTALLATION_ID"))
			Expect(spawner.Env[2].Name).To(Equal("GITHUB_APP_PRIVATE_KEY"))
		})
	})

	Context("When switching workspace secret from GitHub App to PAT", func() {
		It("Should update the Deployment to remove GitHub App env vars and add GITHUB_TOKEN", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-app-to-pat",
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

			By("Creating a GitHub App secret")
			appSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "switch-secret-2",
					Namespace: ns.Name,
				},
				Data: map[string][]byte{
					"appID":          []byte("12345"),
					"installationID": []byte("67890"),
					"privateKey":     keyPEM,
				},
			}
			Expect(k8sClient.Create(ctx, appSecret)).Should(Succeed())

			By("Creating a Workspace with GitHub App secretRef")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-switch-2",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
					SecretRef: &kelos.SecretReference{
						Name: "switch-secret-2",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner referencing the workspace")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-switch-2",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							Reporting: &kelos.GitHubReporting{Enabled: true},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-switch-2",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying a Deployment is created in GitHub App mode")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil {
					return false
				}
				for _, env := range createdDeploy.Spec.Template.Spec.Containers[0].Env {
					if env.Name == "GITHUB_APP_ID" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying initial state: GitHub App mode (env vars, no init containers, no volumes)")
			Expect(createdDeploy.Spec.Template.Spec.InitContainers).To(BeEmpty())
			Expect(createdDeploy.Spec.Template.Spec.Volumes).To(BeEmpty())
			Expect(createdDeploy.Spec.Template.Spec.Containers[0].Env).To(HaveLen(3))

			By("Switching the secret to PAT credentials")
			Eventually(func() error {
				secret := &corev1.Secret{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "switch-secret-2", Namespace: ns.Name}, secret); err != nil {
					return err
				}
				secret.Data = map[string][]byte{
					"GITHUB_TOKEN": []byte("ghp_new_pat_token"),
				}
				return k8sClient.Update(ctx, secret)
			}, timeout, interval).Should(Succeed())

			By("Verifying the Deployment is updated to PAT mode (GITHUB_TOKEN env var present)")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil {
					return false
				}
				for _, env := range createdDeploy.Spec.Template.Spec.Containers[0].Env {
					if env.Name == "GITHUB_TOKEN" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			spawner := createdDeploy.Spec.Template.Spec.Containers[0]

			By("Verifying GitHub App env vars are removed")
			for _, env := range spawner.Env {
				Expect(env.Name).NotTo(Equal("GITHUB_APP_ID"))
				Expect(env.Name).NotTo(Equal("GITHUB_APP_INSTALLATION_ID"))
				Expect(env.Name).NotTo(Equal("GITHUB_APP_PRIVATE_KEY"))
			}

			By("Verifying no init containers, volumes, or volume mounts")
			Expect(createdDeploy.Spec.Template.Spec.InitContainers).To(BeEmpty())
			Expect(createdDeploy.Spec.Template.Spec.Volumes).To(BeEmpty())
			Expect(spawner.VolumeMounts).To(BeEmpty())

			By("Verifying --github-token-file arg is not present")
			Expect(spawner.Args).NotTo(ContainElement("--github-token-file=/shared/token/GITHUB_TOKEN"))
		})
	})

	Context("When workspace secretRef changes to a different secret", func() {
		It("Should update the Deployment when workspace spec changes", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-ws-change",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a PAT secret")
			patSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pat-secret-ws",
					Namespace: ns.Name,
				},
				Data: map[string][]byte{
					"GITHUB_TOKEN": []byte("ghp_test_token"),
				},
			}
			Expect(k8sClient.Create(ctx, patSecret)).Should(Succeed())

			By("Generating a test RSA key for GitHub App")
			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			Expect(err).NotTo(HaveOccurred())
			keyPEM := pem.EncodeToMemory(&pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
			})

			By("Creating a GitHub App secret")
			appSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-secret-ws",
					Namespace: ns.Name,
				},
				Data: map[string][]byte{
					"appID":          []byte("12345"),
					"installationID": []byte("67890"),
					"privateKey":     keyPEM,
				},
			}
			Expect(k8sClient.Create(ctx, appSecret)).Should(Succeed())

			By("Creating a Workspace with PAT secretRef")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-ws-change",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
					SecretRef: &kelos.SecretReference{
						Name: "pat-secret-ws",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner referencing the workspace")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-ws-change",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							Reporting: &kelos.GitHubReporting{Enabled: true},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-ws-change",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying a Deployment is created in PAT mode")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdDeploy.Spec.Template.Spec.InitContainers).To(BeEmpty())

			By("Updating workspace to point to GitHub App secret")
			Eventually(func() error {
				wsObj := &kelos.Workspace{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-workspace-ws-change", Namespace: ns.Name}, wsObj); err != nil {
					return err
				}
				wsObj.Spec.SecretRef = &kelos.SecretReference{
					Name: "app-secret-ws",
				}
				return k8sClient.Update(ctx, wsObj)
			}, timeout, interval).Should(Succeed())

			By("Verifying the Deployment is updated to GitHub App mode")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil {
					return false
				}
				for _, env := range createdDeploy.Spec.Template.Spec.Containers[0].Env {
					if env.Name == "GITHUB_APP_ID" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			Expect(createdDeploy.Spec.Template.Spec.InitContainers).To(BeEmpty())
			Expect(createdDeploy.Spec.Template.Spec.Volumes).To(BeEmpty())
		})
	})

	Context("When creating a TaskSpawner with comment-based workflow control", func() {
		It("Should store triggerComment and excludeComments in commentPolicy and create a Deployment", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-comments",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-comments",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with triggerComment and excludeComments")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-comments",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
							CommentPolicy: &kelos.GitHubCommentPolicy{
								TriggerComment:  "/kelos pick-up",
								ExcludeComments: []string{"/kelos needs-input", "/kelos pause"},
							},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-comments",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying the comment fields are stored in spec")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy).ToNot(BeNil())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.TriggerComment).To(Equal("/kelos pick-up"))
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.ExcludeComments).To(ConsistOf("/kelos needs-input", "/kelos pause"))

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the Deployment has owner reference")
			Expect(createdDeploy.OwnerReferences).To(HaveLen(1))
			Expect(createdDeploy.OwnerReferences[0].Name).To(Equal(ts.Name))
		})

		It("Should store only triggerComment when excludeComments is not set", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-trigger-only",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-trigger-only",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with only triggerComment")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-trigger-only",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
							CommentPolicy: &kelos.GitHubCommentPolicy{
								TriggerComment: "/kelos pick-up",
							},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-trigger-only",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying triggerComment is stored and excludeComments is empty")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy).ToNot(BeNil())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.TriggerComment).To(Equal("/kelos pick-up"))
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.ExcludeComments).To(BeEmpty())

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})

		It("Should store only excludeComments when triggerComment is not set", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-exclude-only",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-exclude-only",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with only excludeComments")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-exclude-only",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
							CommentPolicy: &kelos.GitHubCommentPolicy{
								ExcludeComments: []string{"/kelos needs-input"},
							},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-exclude-only",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying excludeComments is stored and triggerComment is empty")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy).ToNot(BeNil())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.TriggerComment).To(BeEmpty())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.ExcludeComments).To(ConsistOf("/kelos needs-input"))

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})

		It("Should store commentPolicy in spec and create a Deployment", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-comment-policy",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-comment-policy",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with commentPolicy")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-comment-policy",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
							CommentPolicy: &kelos.GitHubCommentPolicy{
								TriggerComment:    "/kelos pick-up",
								ExcludeComments:   []string{"/kelos needs-input"},
								AllowedUsers:      []string{"alice"},
								AllowedTeams:      []kelos.GitHubTeamRef{"my-org/platform"},
								MinimumPermission: "write",
							},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-comment-policy",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying commentPolicy is stored in spec")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy).ToNot(BeNil())
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.TriggerComment).To(Equal("/kelos pick-up"))
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.ExcludeComments).To(ConsistOf("/kelos needs-input"))
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.AllowedUsers).To(ConsistOf("alice"))
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.AllowedTeams).To(ConsistOf(kelos.GitHubTeamRef("my-org/platform")))
			Expect(createdTS.Spec.When.GitHubIssues.CommentPolicy.MinimumPermission).To(Equal("write"))

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})

		It("Should reject commentPolicy with invalid allowedTeams format", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-comment-policy-invalid-team",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-comment-policy-invalid-team",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating an invalid TaskSpawner")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-comment-policy-invalid-team",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
							CommentPolicy: &kelos.GitHubCommentPolicy{
								AllowedTeams: []kelos.GitHubTeamRef{"platform"},
							},
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-comment-policy-invalid-team",
						},
					},
				},
			}
			err := k8sClient.Create(ctx, ts)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})
	})

	Context("When creating a TaskSpawner with githubPullRequests", func() {
		It("Should store githubPullRequests fields in spec and create a Deployment", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-github-prs",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-github-prs",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with githubPullRequests")
			draft := false
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-github-prs",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubPullRequests: &kelos.GitHubPullRequests{
							State:       "open",
							ReviewState: "changes_requested",
							CommentPolicy: &kelos.GitHubCommentPolicy{
								TriggerComment:  "/kelos pick-up",
								ExcludeComments: []string{"/kelos needs-input"},
							},
							Labels: []string{"generated-by-kelos"},
							Draft:  &draft,
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-github-prs",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying the githubPullRequests fields are stored in spec")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.When.GitHubPullRequests.ReviewState).To(Equal("changes_requested"))
			Expect(createdTS.Spec.When.GitHubPullRequests.CommentPolicy).ToNot(BeNil())
			Expect(createdTS.Spec.When.GitHubPullRequests.CommentPolicy.TriggerComment).To(Equal("/kelos pick-up"))
			Expect(createdTS.Spec.When.GitHubPullRequests.CommentPolicy.ExcludeComments).To(ConsistOf("/kelos needs-input"))
			Expect(createdTS.Spec.When.GitHubPullRequests.Labels).To(ConsistOf("generated-by-kelos"))
			Expect(createdTS.Spec.When.GitHubPullRequests.Draft).ToNot(BeNil())
			Expect(*createdTS.Spec.When.GitHubPullRequests.Draft).To(BeFalse())

			By("Verifying a Deployment is created")
			deployLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating a TaskSpawner with per-source pollInterval", func() {
		It("Should store the per-source pollInterval in the spec", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-source-poll",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workspace-source-poll",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/kelos-dev/kelos.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a TaskSpawner with per-source pollInterval")
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-source-poll",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							Labels:       []string{"bug"},
							PollInterval: "30s",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeOAuth,
							SecretRef: &kelos.SecretReference{
								Name: "claude-credentials",
							},
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "test-workspace-source-poll",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

			By("Verifying the per-source pollInterval is stored in spec")
			tsLookupKey := types.NamespacedName{Name: ts.Name, Namespace: ns.Name}
			createdTS := &kelos.TaskSpawner{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, tsLookupKey, createdTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdTS.Spec.When.GitHubIssues.PollInterval).To(Equal("30s"))
		})
	})

	Context("When creating a TaskSpawner with workerPoolRef and ttlSecondsAfterFinished", func() {
		It("Should reject the TaskSpawner", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-taskspawner-pool-ttl-exclusive",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a TaskSpawner with both workerPoolRef and ttlSecondsAfterFinished")
			ttl := int32(300)
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spawner-pool-ttl-exclusive",
					Namespace: ns.Name,
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{
							State: "open",
						},
					},
					TaskTemplate: kelos.TaskTemplate{
						WorkerPoolRef: &kelos.WorkerPoolReference{
							Name: "my-pool",
						},
						TTLSecondsAfterFinished: &ttl,
					},
				},
			}
			err := k8sClient.Create(ctx, ts)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})
	})
})
