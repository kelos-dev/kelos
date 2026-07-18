package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/controller"
	"github.com/kelos-dev/kelos/internal/sessionserver"
)

var _ = Describe("Session", func() {
	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("session-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	It("applies a persistent Session through the web YAML API", func() {
		clientset, err := kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		server, err := sessionserver.New(sessionserver.Config{
			Token:            "secret-token",
			Client:           k8sClient,
			Clientset:        clientset,
			RESTConfig:       cfg,
			DefaultNamespace: namespace,
		})
		Expect(err).NotTo(HaveOccurred())
		manifest := fmt.Sprintf(`apiVersion: kelos.dev/v1alpha2
kind: Session
metadata:
  name: yaml-chat
  namespace: %s
  labels:
    source: web
spec:
  volumeClaimTemplate:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 2Gi
  worker:
    type: codex
    credentials:
      type: none
`, namespace)

		request := httptest.NewRequest(http.MethodPost, "/api/sessions/apply", strings.NewReader(manifest))
		request.Header.Set("Authorization", "Bearer secret-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())

		var session kelos.Session
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "yaml-chat"}, &session)).To(Succeed())
		Expect(session.Spec.VolumeClaimTemplate).NotTo(BeNil())
		storage := session.Spec.VolumeClaimTemplate.Resources.Requests[corev1.ResourceStorage]
		Expect(storage.Cmp(resource.MustParse("2Gi"))).To(Equal(0))

		request = httptest.NewRequest(http.MethodPost, "/api/sessions/apply", strings.NewReader(strings.Replace(manifest, "source: web", "source: yaml", 1)))
		request.Header.Set("Authorization", "Bearer secret-token")
		response = httptest.NewRecorder()
		server.ServeHTTP(response, request)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "yaml-chat"}, &session)).To(Succeed())
		Expect(session.Labels).To(HaveKeyWithValue("source", "yaml"))
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
			g.Expect(metav1.IsControlledBy(&statefulSet.Spec.VolumeClaimTemplates[0], session)).To(BeTrue())
			g.Expect(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy).NotTo(BeNil())
			g.Expect(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
			g.Expect(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
			storage := statefulSet.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
			g.Expect(storage.Cmp(resource.MustParse("1Gi"))).To(Equal(0))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		var service corev1.Service
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: statefulSet.Spec.ServiceName}, &service)).To(Succeed())
		Expect(service.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))
		Expect(metav1.IsControlledBy(&service, session)).To(BeTrue())
		statefulSet.Status.UpdateRevision = "desired-revision"
		statefulSet.Status.ObservedGeneration = statefulSet.Generation
		Expect(k8sClient.Status().Update(ctx, &statefulSet)).To(Succeed())
		podLabels := make(map[string]string, len(statefulSet.Spec.Template.Labels)+1)
		for key, value := range statefulSet.Spec.Template.Labels {
			podLabels[key] = value
		}
		podLabels[appsv1.StatefulSetRevisionLabel] = statefulSet.Status.UpdateRevision

		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            statefulSet.Name + "-0",
				Namespace:       namespace,
				Labels:          podLabels,
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

	It("updates the runtime image of an existing StatefulSet", func() {
		session := validSession(namespace, "runtime-update", "codex")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		key := client.ObjectKey{Namespace: namespace, Name: "session-" + session.Name}
		var statefulSet appsv1.StatefulSet
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.Template.Spec.InitContainers).NotTo(BeEmpty())
			g.Expect(statefulSet.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		statefulSet.Spec.Template.Spec.InitContainers[0].Image = "runtime:old"
		statefulSet.Spec.Template.Spec.InitContainers[0].ImagePullPolicy = corev1.PullAlways
		Expect(k8sClient.Update(ctx, &statefulSet)).To(Succeed())

		Eventually(func(g Gomega) {
			var updated appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, key, &updated)).To(Succeed())
			g.Expect(updated.Spec.Template.Spec.InitContainers).NotTo(BeEmpty())
			g.Expect(updated.Spec.Template.Spec.InitContainers[0].Image).To(Equal(controller.DefaultSessionRuntimeImage))
			g.Expect(updated.Spec.Template.Spec.InitContainers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
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
