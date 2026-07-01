package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("WorkerPool Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When creating a WorkerPool", func() {
		It("Should create a StatefulSet, Service, and update status", func() {
			By("Creating a namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-workerpool",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			By("Creating a Workspace resource")
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pool-workspace",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkspaceSpec{
					Repo: "https://github.com/example/repo.git",
					Ref:  "main",
				},
			}
			Expect(k8sClient.Create(ctx, ws)).Should(Succeed())

			By("Creating a WorkerPool")
			pool := &kelos.WorkerPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: ns.Name,
				},
				Spec: kelos.WorkerPoolSpec{
					Worker: kelos.WorkerSpec{
						Type: "claude-code",
						Credentials: &kelos.Credentials{
							Type: kelos.CredentialTypeNone,
						},
						WorkspaceRef: &kelos.WorkspaceReference{
							Name: "pool-workspace",
						},
					},
					Replicas: ptr.To(int32(2)),
					VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("5Gi"),
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pool)).Should(Succeed())

			By("Verifying a StatefulSet is created")
			stsKey := types.NamespacedName{Name: "wp-test-pool", Namespace: ns.Name}
			createdSTS := &appsv1.StatefulSet{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, stsKey, createdSTS)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdSTS.Spec.Replicas).NotTo(BeNil())
			Expect(*createdSTS.Spec.Replicas).To(Equal(int32(2)))
			Expect(createdSTS.Spec.VolumeClaimTemplates).To(HaveLen(1))
			expectedSize := resource.MustParse("5Gi")
			Expect(createdSTS.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]).To(
				Equal(expectedSize))

			By("Verifying a headless Service is created")
			svcKey := types.NamespacedName{Name: "wp-test-pool", Namespace: ns.Name}
			createdSvc := &corev1.Service{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, svcKey, createdSvc)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdSvc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))

			By("Verifying WorkerPool status is updated")
			poolKey := types.NamespacedName{Name: "test-pool", Namespace: ns.Name}
			updatedPool := &kelos.WorkerPool{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, poolKey, updatedPool); err != nil {
					return ""
				}
				return updatedPool.Status.StatefulSetName
			}, timeout, interval).Should(Equal("wp-test-pool"))

			Expect(updatedPool.Status.ServiceName).To(Equal("wp-test-pool"))
			Expect(updatedPool.Status.Phase).To(Equal(kelos.WorkerPoolPhasePending))
		})
	})
})
