package integration

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("Session", func() {
	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("session-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	It("creates a one-replica StatefulSet and becomes ready with its Pod", func() {
		session := validSession(namespace, "chat", "claude-code")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		var statefulSet appsv1.StatefulSet
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "session-" + session.Name}, &statefulSet)).To(Succeed())
			g.Expect(metav1.IsControlledBy(&statefulSet, session)).To(BeTrue())
			g.Expect(statefulSet.Spec.Replicas).NotTo(BeNil())
			g.Expect(*statefulSet.Spec.Replicas).To(Equal(int32(1)))
			g.Expect(statefulSet.Spec.Template.Spec.Containers[0].Command).To(Equal([]string{"/kelos/bin/kelos-session-runtime"}))
			g.Expect(statefulSet.Spec.VolumeClaimTemplates).To(HaveLen(1))
			g.Expect(statefulSet.Spec.VolumeClaimTemplates[0].Name).To(Equal("workspace"))
			storage := statefulSet.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
			g.Expect(storage.Cmp(resource.MustParse("1Gi"))).To(Equal(0))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		var service corev1.Service
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: statefulSet.Spec.ServiceName}, &service)).To(Succeed())
		Expect(service.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))
		Expect(metav1.IsControlledBy(&service, session)).To(BeTrue())

		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            statefulSet.Name + "-0",
				Namespace:       namespace,
				Labels:          statefulSet.Spec.Template.Labels,
				Annotations:     statefulSet.Spec.Template.Annotations,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
			},
			Spec: statefulSet.Spec.Template.Spec,
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: "workspace-" + statefulSet.Name + "-0",
			}},
		})
		Expect(k8sClient.Create(ctx, &pod)).To(Succeed())
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		Expect(k8sClient.Status().Update(ctx, &pod)).To(Succeed())

		Eventually(func(g Gomega) {
			var current kelos.Session
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current)).To(Succeed())
			g.Expect(current.Status.Phase).To(Equal(kelos.SessionPhaseReady))
			g.Expect(current.Status.PodName).To(Equal(pod.Name))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("uses emptyDir when persistent storage is omitted", func() {
		session := validSession(namespace, "ephemeral", "codex")
		session.Spec.VolumeClaimTemplate = nil
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "session-" + session.Name}, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.VolumeClaimTemplates).To(BeEmpty())
			var workspace *corev1.Volume
			for i := range statefulSet.Spec.Template.Spec.Volumes {
				if statefulSet.Spec.Template.Spec.Volumes[i].Name == "workspace" {
					workspace = &statefulSet.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			g.Expect(workspace).NotTo(BeNil())
			g.Expect(workspace.EmptyDir).NotTo(BeNil())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("accepts only the initial providers", func() {
		opencode := validSession(namespace, "opencode", "opencode")
		Expect(k8sClient.Create(ctx, opencode)).To(Succeed())

		unsupported := validSession(namespace, "unsupported", "gemini")
		Expect(k8sClient.Create(ctx, unsupported)).NotTo(Succeed())

		missingCredentials := validSession(namespace, "missing-credentials", "codex")
		missingCredentials.Spec.Worker.Credentials = nil
		Expect(k8sClient.Create(ctx, missingCredentials)).NotTo(Succeed())
	})

	It("requires a spec", func() {
		session := &unstructured.Unstructured{}
		session.SetAPIVersion("kelos.dev/v1alpha2")
		session.SetKind("Session")
		session.SetNamespace(namespace)
		session.SetName("missing-spec")
		Expect(k8sClient.Create(ctx, session)).NotTo(Succeed())
	})

	It("keeps the spec immutable", func() {
		session := validSession(namespace, "immutable", "codex")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())
		session.Spec.Worker.Model = "another-model"
		Expect(k8sClient.Update(ctx, session)).NotTo(Succeed())
	})
})

func validSession(namespace, name, provider string) *kelos.Session {
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kelos.SessionSpec{
			Worker: kelos.WorkerSpec{
				Type: provider,
				Credentials: &kelos.Credentials{
					Type: kelos.CredentialTypeNone,
				},
			},
			VolumeClaimTemplate: &corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				}},
			},
		},
	}
}
