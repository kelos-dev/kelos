package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/consoleserver"
	"github.com/kelos-dev/kelos/internal/controller"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
	clientv1alpha2 "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/typed/api/v1alpha2"
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
		server, err := consoleserver.New(consoleserver.Config{
			Token:            "secret-token",
			Client:           k8sClient,
			Clientset:        clientset,
			RESTConfig:       cfg,
			HTTPClient:       http.DefaultClient,
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
  initialBranch: issue-42
  initialPrompt: "Investigate issue #42 interactively"
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
    workspaceRef:
      name: workspace
`, namespace)

		request := httptest.NewRequest(http.MethodPost, "/api/sessions/apply", strings.NewReader(manifest))
		request.Header.Set("Authorization", "Bearer secret-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())

		var session kelos.Session
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "yaml-chat"}, &session)).To(Succeed())
		Expect(session.Spec.InitialBranch).To(Equal("issue-42"))
		Expect(session.Spec.InitialPrompt).To(Equal("Investigate issue #42 interactively"))
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

	It("creates a ready Session and publishes runtime notifications to the console", func() {
		session := validSession(namespace, "chat", "claude-code")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())
		Expect(session.Spec.Suspend).NotTo(BeNil())
		Expect(*session.Spec.Suspend).To(BeFalse())

		var statefulSet appsv1.StatefulSet
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: session.Name}, &statefulSet)).To(Succeed())
			g.Expect(metav1.IsControlledBy(&statefulSet, session)).To(BeTrue())
			g.Expect(statefulSet.Spec.Replicas).NotTo(BeNil())
			g.Expect(*statefulSet.Spec.Replicas).To(Equal(int32(1)))
			g.Expect(statefulSet.Spec.Template.Spec.Containers[0].Command).To(Equal(expectedAgentProcessCommand("/kelos/bin/kelos-session-runtime", true)))
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

		runtimeClient, err := clientv1alpha2.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		publisher, err := sessionruntime.NewSessionStatusPublisher(runtimeClient.Sessions(namespace), session.Name, pod.UID)
		Expect(err).NotTo(HaveOccurred())
		clientset, err := kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		console, err := consoleserver.New(consoleserver.Config{
			Token: "secret-token", Client: k8sClient, Clientset: clientset, RESTConfig: cfg, HTTPClient: http.DefaultClient, DefaultNamespace: namespace,
		})
		Expect(err).NotTo(HaveOccurred())
		journal := sessionruntime.NewJournal()
		defer journal.Close()
		for _, event := range []sessionruntime.Event{
			{Type: sessionruntime.EventInputRequested, InputID: "first"},
			{Type: sessionruntime.EventInputResolved, InputID: "first"},
			{Type: sessionruntime.EventTurnCompleted, Status: "completed"},
			{Type: sessionruntime.EventTurnCompleted, Status: "failed"},
			{Type: sessionruntime.EventTurnCompleted, Status: "interrupted"},
		} {
			timestamp := time.Now().UTC().Truncate(time.Second)
			event.Timestamp = &timestamp
			Expect(journal.Append(event)).To(Succeed())
			notification := journal.LatestNotification()
			Eventually(func() error {
				return publisher(ctx, sessionruntime.ObservedSessionStatus{LastNotification: notification})
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
			request := httptest.NewRequest(http.MethodGet, "/api/sessions?namespace="+namespace, nil)
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			console.ServeHTTP(response, request)
			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
			var summaries []struct {
				LastNotification *kelos.SessionNotification `json:"lastNotification"`
			}
			Expect(json.Unmarshal(response.Body.Bytes(), &summaries)).To(Succeed())
			Expect(summaries).To(HaveLen(1))
			got := summaries[0].LastNotification
			if notification == nil {
				Expect(got).To(BeNil())
			} else {
				Expect(got).NotTo(BeNil())
				Expect(got.ID).To(Equal(notification.ID))
				Expect(got.Type).To(Equal(notification.Type))
				Expect(got.EventTime.Equal(&notification.EventTime)).To(BeTrue())
			}
		}
	})

	It("rejects an empty notification identity", func() {
		session := validSession(namespace, "empty-notification-id", "codex")
		session.Spec.Suspend = ptr.To(true)
		Expect(k8sClient.Create(ctx, session)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), session)).To(Succeed())
			session.Status.LastNotification = &kelos.SessionNotification{
				Type: kelos.SessionNotificationTypeInputRequested, EventTime: metav1.Now(),
			}
			err := k8sClient.Status().Update(ctx, session)
			g.Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected empty notification ID to fail validation, got %v", err)
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("creates a governing Service for a digit-leading Session name", func() {
		session := validSession(namespace, "123-chat", "codex")
		session.Spec.Suspend = ptr.To(true)
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.ServiceName).To(Equal("s-" + session.Name))

			var service corev1.Service
			serviceKey := client.ObjectKey{Namespace: namespace, Name: statefulSet.Spec.ServiceName}
			g.Expect(k8sClient.Get(ctx, serviceKey, &service)).To(Succeed())
			g.Expect(metav1.IsControlledBy(&service, session)).To(BeTrue())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("reconciles controller-managed fields on an existing persistent StatefulSet", func() {
		session := validSession(namespace, "existing-statefulset", "codex")
		session.Spec.Suspend = ptr.To(true)
		session.Spec.Worker.AgentConfigRefs = []kelos.AgentConfigReference{{Name: "late-config"}}
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		Eventually(func(g Gomega) {
			var current kelos.Session
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current)).To(Succeed())
			g.Expect(current.Status.Message).To(Equal(`Waiting for AgentConfig "late-config"`))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		workloadName := session.Name
		selector := map[string]string{
			"kelos.dev/component": "session",
			"kelos.dev/session":   session.Name,
		}
		statefulSet := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:            workloadName,
				Namespace:       namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))},
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas:            ptr.To(int32(0)),
				ServiceName:         "s-" + workloadName,
				PodManagementPolicy: appsv1.ParallelPodManagement,
				Selector:            &metav1.LabelSelector{MatchLabels: selector},
				UpdateStrategy:      appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: selector},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:  kelos.AgentContainerName,
						Image: "agent:stale",
					}}},
				},
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
					ObjectMeta: metav1.ObjectMeta{Name: "workspace"},
					Spec:       *session.Spec.VolumeClaimTemplate.DeepCopy(),
				}},
			},
		}
		Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())

		claim := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "workspace-" + workloadName + "-0",
				Namespace:       namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
			},
			Spec: *session.Spec.VolumeClaimTemplate.DeepCopy(),
		}
		Expect(k8sClient.Create(ctx, claim)).To(Succeed())
		Expect(k8sClient.Create(ctx, &kelos.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "late-config", Namespace: namespace},
		})).To(Succeed())

		Eventually(func(g Gomega) {
			var updated appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), &updated)).To(Succeed())
			g.Expect(updated.Spec.Replicas).NotTo(BeNil())
			g.Expect(*updated.Spec.Replicas).To(BeZero())
			g.Expect(updated.Spec.PodManagementPolicy).To(Equal(appsv1.ParallelPodManagement))
			g.Expect(updated.Spec.VolumeClaimTemplates).To(HaveLen(1))
			g.Expect(updated.Spec.VolumeClaimTemplates[0].OwnerReferences).To(BeEmpty())
			g.Expect(updated.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
			g.Expect(updated.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			g.Expect(updated.Spec.Template.Spec.Containers[0].Image).To(Equal(controller.CodexImage))
			g.Expect(updated.Spec.Template.Spec.Containers[0].Command).To(Equal(expectedAgentProcessCommand("/kelos/bin/kelos-session-runtime", true)))
			g.Expect(updated.Spec.PersistentVolumeClaimRetentionPolicy).NotTo(BeNil())
			g.Expect(updated.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
			g.Expect(updated.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))

			var updatedClaim corev1.PersistentVolumeClaim
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(claim), &updatedClaim)).To(Succeed())
			g.Expect(metav1.IsControlledBy(&updatedClaim, session)).To(BeTrue())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("suspends and resumes a Session while retaining its workspace claim", func() {
		session := validSession(namespace, "suspend", "codex")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		statefulSetKey := client.ObjectKey{Namespace: namespace, Name: session.Name}
		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, statefulSetKey, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.Replicas).NotTo(BeNil())
			g.Expect(*statefulSet.Spec.Replicas).To(Equal(int32(1)))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		claim := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "workspace-" + session.Name + "-0",
				Namespace:       namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))},
			},
			Spec: *session.Spec.VolumeClaimTemplate.DeepCopy(),
		}
		Expect(k8sClient.Create(ctx, claim)).To(Succeed())

		Eventually(func() error {
			var current kelos.Session
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current); err != nil {
				return err
			}
			current.Spec.Suspend = ptr.To(true)
			return k8sClient.Update(ctx, &current)
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, statefulSetKey, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.Replicas).NotTo(BeNil())
			g.Expect(*statefulSet.Spec.Replicas).To(Equal(int32(0)))
			var current kelos.Session
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current)).To(Succeed())
			g.Expect(current.Status.Phase).To(Equal(kelos.SessionPhaseSuspended))
			g.Expect(current.Status.PodName).To(BeEmpty())
			g.Expect(current.Status.PodUID).To(BeEmpty())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(claim), &corev1.PersistentVolumeClaim{})).To(Succeed())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func() error {
			var current kelos.Session
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current); err != nil {
				return err
			}
			current.Spec.Suspend = ptr.To(false)
			return k8sClient.Update(ctx, &current)
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, statefulSetKey, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.Replicas).NotTo(BeNil())
			g.Expect(*statefulSet.Spec.Replicas).To(Equal(int32(1)))
			var current kelos.Session
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current)).To(Succeed())
			g.Expect(current.Status.Phase).To(Equal(kelos.SessionPhasePending))
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(claim), &corev1.PersistentVolumeClaim{})).To(Succeed())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("reconciles controller-managed fields of an existing StatefulSet", func() {
		session := validSession(namespace, "runtime-update", "codex")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		key := client.ObjectKey{Namespace: namespace, Name: session.Name}
		var statefulSet appsv1.StatefulSet
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.Template.Spec.InitContainers).NotTo(BeEmpty())
			g.Expect(statefulSet.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		statefulSet.Labels = map[string]string{"stale": "label"}
		statefulSet.Spec.Template.Labels["stale"] = "label"
		statefulSet.Spec.Template.Spec.Containers[0].Command = []string{"/kelos/bin/kelos-session-runtime"}
		statefulSet.Spec.Template.Spec.Containers[0].Image = "agent:stale"
		statefulSet.Spec.Template.Spec.InitContainers[0].Image = "runtime:stale"
		statefulSet.Spec.Template.Spec.InitContainers[0].ImagePullPolicy = corev1.PullAlways
		statefulSet.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}
		statefulSet.Spec.MinReadySeconds = 10
		statefulSet.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
			WhenScaled:  appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
		}
		Expect(k8sClient.Update(ctx, &statefulSet)).To(Succeed())

		Eventually(func(g Gomega) {
			var updated appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, key, &updated)).To(Succeed())
			g.Expect(updated.Labels).NotTo(HaveKey("stale"))
			g.Expect(updated.Spec.Template.Labels).NotTo(HaveKey("stale"))
			g.Expect(updated.Spec.Template.Spec.Containers[0].Command).To(Equal(expectedAgentProcessCommand("/kelos/bin/kelos-session-runtime", true)))
			g.Expect(updated.Spec.Template.Spec.Containers[0].Image).To(Equal(controller.CodexImage))
			g.Expect(updated.Spec.Template.Spec.InitContainers).NotTo(BeEmpty())
			g.Expect(updated.Spec.Template.Spec.InitContainers[0].Image).To(Equal(controller.DefaultSessionRuntimeImage))
			g.Expect(updated.Spec.Template.Spec.InitContainers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
			g.Expect(updated.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
			g.Expect(updated.Spec.RevisionHistoryLimit).NotTo(BeNil())
			g.Expect(*updated.Spec.RevisionHistoryLimit).To(Equal(int32(10)))
			g.Expect(updated.Spec.MinReadySeconds).To(BeZero())
			g.Expect(updated.Spec.PersistentVolumeClaimRetentionPolicy).NotTo(BeNil())
			g.Expect(updated.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
			g.Expect(updated.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("preserves revisionHistoryLimit on Kubernetes versions where it is immutable", func() {
		session := validSession(namespace, "preserved-revision-history", "codex")
		session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		Consistently(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: session.Name}, &appsv1.StatefulSet{})
		}, time.Second, 100*time.Millisecond).ShouldNot(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), session)).To(Succeed())

		selector := map[string]string{
			"kelos.dev/component": "session",
			"kelos.dev/session":   session.Name,
		}
		statefulSet := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:            session.Name,
				Namespace:       namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))},
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas:             ptr.To(int32(0)),
				ServiceName:          "s-" + session.Name,
				RevisionHistoryLimit: ptr.To(int32(1)),
				Selector:             &metav1.LabelSelector{MatchLabels: selector},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: selector},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: kelos.AgentContainerName, Image: "agent:stale"}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())
		Expect(k8sClient.Create(ctx, &kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: namespace},
			Spec:       kelos.WorkspaceSpec{Repo: "https://github.com/kelos-dev/kelos.git"},
		})).To(Succeed())

		Eventually(func(g Gomega) {
			var updated appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), &updated)).To(Succeed())
			g.Expect(updated.Spec.RevisionHistoryLimit).NotTo(BeNil())
			g.Expect(*updated.Spec.RevisionHistoryLimit).To(Equal(int32(1)))
			g.Expect(updated.Spec.Template.Spec.Containers[0].Image).To(Equal(controller.CodexImage))
			g.Expect(updated.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("changes the Pod template when plugin content changes", func() {
		agentConfig := &kelos.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "session-plugins", Namespace: namespace},
			Spec: kelos.AgentConfigSpec{Plugins: []kelos.PluginSpec{{
				Name:   "tools",
				Skills: []kelos.SkillDefinition{{Name: "review", Content: "Review changes"}},
			}}},
		}
		Expect(k8sClient.Create(ctx, agentConfig)).To(Succeed())

		session := validSession(namespace, "plugin-update", "codex")
		session.Spec.Suspend = ptr.To(true)
		session.Spec.Worker.AgentConfigRefs = []kelos.AgentConfigReference{{Name: agentConfig.Name}}
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		key := client.ObjectKey{Namespace: namespace, Name: session.Name}
		var originalChecksum string
		var pluginConfigMapName string
		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, key, &statefulSet)).To(Succeed())
			originalChecksum = statefulSet.Spec.Template.Annotations["kelos.dev/plugin-content-checksum"]
			g.Expect(originalChecksum).NotTo(BeEmpty())
			for i := range statefulSet.Spec.Template.Spec.Volumes {
				volume := &statefulSet.Spec.Template.Spec.Volumes[i]
				if volume.Name == controller.PluginStagingVolumeName && volume.ConfigMap != nil {
					pluginConfigMapName = volume.ConfigMap.Name
					break
				}
			}
			g.Expect(pluginConfigMapName).NotTo(BeEmpty())
			var configMap corev1.ConfigMap
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: pluginConfigMapName}, &configMap)).To(Succeed())
			g.Expect(configMap.Data).To(HaveKeyWithValue("p0-s0", "Review changes"))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func() error {
			var current kelos.AgentConfig
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(agentConfig), &current); err != nil {
				return err
			}
			current.Spec.Plugins[0].Skills[0].Content = "Review changes carefully"
			return k8sClient.Update(ctx, &current)
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, key, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.Template.Annotations["kelos.dev/plugin-content-checksum"]).NotTo(BeEmpty())
			g.Expect(statefulSet.Spec.Template.Annotations["kelos.dev/plugin-content-checksum"]).NotTo(Equal(originalChecksum))
			g.Expect(statefulSet.Spec.Replicas).NotTo(BeNil())
			g.Expect(*statefulSet.Spec.Replicas).To(BeZero())
			var configMap corev1.ConfigMap
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: pluginConfigMapName}, &configMap)).To(Succeed())
			g.Expect(configMap.Data).To(HaveKeyWithValue("p0-s0", "Review changes carefully"))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		var drifted corev1.ConfigMap
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: pluginConfigMapName}, &drifted)).To(Succeed())
		drifted.Data["p0-s0"] = "drifted content"
		Expect(k8sClient.Update(ctx, &drifted)).To(Succeed())
		Eventually(func(g Gomega) {
			var repaired corev1.ConfigMap
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&drifted), &repaired)).To(Succeed())
			g.Expect(repaired.Data).To(HaveKeyWithValue("p0-s0", "Review changes carefully"))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("uses emptyDir when persistent storage is omitted", func() {
		session := validSession(namespace, "ephemeral", "codex")
		session.Spec.VolumeClaimTemplate = nil
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: session.Name}, &statefulSet)).To(Succeed())
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

		branchWithoutWorkspace := validSession(namespace, "branch-without-workspace", "codex")
		branchWithoutWorkspace.Spec.InitialBranch = "issue-42"
		Expect(k8sClient.Create(ctx, branchWithoutWorkspace)).NotTo(Succeed())
	})

	It("requires a spec", func() {
		session := &unstructured.Unstructured{}
		session.SetAPIVersion("kelos.dev/v1alpha2")
		session.SetKind("Session")
		session.SetNamespace(namespace)
		session.SetName("missing-spec")
		Expect(k8sClient.Create(ctx, session)).NotTo(Succeed())
	})

	It("allows suspend to change", func() {
		session := validSession(namespace, "mutable-suspend", "codex")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())
		session.Spec.Suspend = ptr.To(true)
		Expect(k8sClient.Update(ctx, session)).To(Succeed())
	})

	It("reconciles credential, model, prompt suggestion, and Pod override changes", func() {
		session := validSession(namespace, "mutable-worker-fields", "codex")
		session.Spec.Suspend = ptr.To(true)
		session.Spec.Worker.Model = "initial-model"
		Expect(k8sClient.Create(ctx, session)).To(Succeed())
		Expect(session.Spec.PromptSuggestions).To(Equal(ptr.To(false)))

		statefulSetKey := client.ObjectKey{Namespace: namespace, Name: session.Name}
		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, statefulSetKey, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			g.Expect(statefulSet.Spec.Template.Spec.Containers[0].Args).To(Equal([]string{"serve"}))
			model := ""
			for _, env := range statefulSet.Spec.Template.Spec.Containers[0].Env {
				if env.Name == "KELOS_MODEL" {
					model = env.Value
					break
				}
			}
			g.Expect(model).To(Equal("initial-model"))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func() error {
			var current kelos.Session
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current); err != nil {
				return err
			}
			current.Spec.Worker.Credentials = &kelos.Credentials{
				Type:      kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{Name: "updated-credentials"},
			}
			current.Spec.Worker.Model = "updated-model"
			current.Spec.PromptSuggestions = ptr.To(true)
			current.Spec.Worker.PodOverrides = &kelos.PodOverrides{
				ServiceAccountName: "workload-identity",
				Labels: map[string]string{
					"app.kubernetes.io/version": "updated-version",
				},
			}
			return k8sClient.Update(ctx, &current)
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, statefulSetKey, &statefulSet)).To(Succeed())
			g.Expect(statefulSet.Labels).To(HaveKeyWithValue("app.kubernetes.io/version", "updated-version"))
			g.Expect(statefulSet.Spec.Template.Labels).To(HaveKeyWithValue("app.kubernetes.io/version", "updated-version"))
			g.Expect(statefulSet.Spec.Template.Spec.ServiceAccountName).To(Equal("workload-identity"))
			g.Expect(statefulSet.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			model := ""
			g.Expect(statefulSet.Spec.Template.Spec.Containers[0].Args).To(Equal([]string{"serve"}))
			var credentialEnv *corev1.EnvVar
			for i := range statefulSet.Spec.Template.Spec.Containers[0].Env {
				env := &statefulSet.Spec.Template.Spec.Containers[0].Env[i]
				switch env.Name {
				case "KELOS_MODEL":
					model = env.Value
				case "CODEX_API_KEY":
					credentialEnv = env
				}
			}
			g.Expect(model).To(Equal("updated-model"))
			g.Expect(credentialEnv).NotTo(BeNil())
			g.Expect(credentialEnv.ValueFrom).NotTo(BeNil())
			g.Expect(credentialEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
			g.Expect(credentialEnv.ValueFrom.SecretKeyRef.Name).To(Equal("updated-credentials"))
			g.Expect(credentialEnv.ValueFrom.SecretKeyRef.Key).To(Equal("CODEX_API_KEY"))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func() error {
			var current kelos.Session
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current); err != nil {
				return err
			}
			current.Spec.Worker.PodOverrides = nil
			return k8sClient.Update(ctx, &current)
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		Eventually(func(g Gomega) {
			var statefulSet appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, statefulSetKey, &statefulSet)).To(Succeed())
			accountName := statefulSet.Spec.Template.Spec.ServiceAccountName
			g.Expect(accountName).NotTo(BeEmpty())
			g.Expect(accountName).NotTo(Equal("workload-identity"))
			var account corev1.ServiceAccount
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: accountName}, &account)).To(Succeed())
			g.Expect(metav1.IsControlledBy(&account, session)).To(BeTrue())
			g.Expect(*statefulSet.Spec.Replicas).To(BeZero())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("allows a typed client to suspend a Session with stored empty optional strings", func() {
		session := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "kelos.dev/v1alpha2",
			"kind":       "Session",
			"metadata": map[string]interface{}{
				"name":      "mutable-suspend-stored-empty",
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"worker": map[string]interface{}{
					"type":   "codex",
					"model":  "",
					"effort": "",
					"image":  "",
					"credentials": map[string]interface{}{
						"type": "none",
					},
				},
				"initialBranch": "",
				"initialPrompt": "",
			},
		}}
		Expect(k8sClient.Create(ctx, session)).To(Succeed())
		for _, path := range [][]string{
			{"spec", "worker", "model"},
			{"spec", "worker", "effort"},
			{"spec", "worker", "image"},
			{"spec", "initialBranch"},
			{"spec", "initialPrompt"},
		} {
			value, found, err := unstructured.NestedString(session.Object, path...)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "stored field %s is absent", strings.Join(path, "."))
			Expect(value).To(BeEmpty())
		}

		var current kelos.Session
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current)).To(Succeed())
		current.Spec.Suspend = ptr.To(true)
		Expect(k8sClient.Update(ctx, &current)).To(Succeed())
	})

	It("rejects a negative idle suspension duration", func() {
		session := validSession(namespace, "negative-idle-suspend", "codex")
		session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{SuspendAfterSeconds: ptr.To(int32(-1))}
		Expect(k8sClient.Create(ctx, session)).NotTo(Succeed())
	})

	It("requires idle suspension before idle deletion", func() {
		for _, test := range []struct {
			name                string
			suspendAfterSeconds int32
			deleteAfterSeconds  int32
		}{
			{name: "equal", suspendAfterSeconds: 60, deleteAfterSeconds: 60},
			{name: "reversed", suspendAfterSeconds: 120, deleteAfterSeconds: 60},
		} {
			session := validSession(namespace, "invalid-idle-policy-"+test.name, "codex")
			session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{
				SuspendAfterSeconds: ptr.To(test.suspendAfterSeconds),
				DeleteAfterSeconds:  ptr.To(test.deleteAfterSeconds),
			}
			Expect(k8sClient.Create(ctx, session)).NotTo(Succeed())
		}
	})

	It("allows adding and removing the Session idle policy", func() {
		session := validSession(namespace, "mutable-idle-policy", "codex")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{SuspendAfterSeconds: ptr.To(int32(3600))}
		Expect(k8sClient.Update(ctx, session)).To(Succeed())

		session.Spec.IdlePolicy = nil
		Expect(k8sClient.Update(ctx, session)).To(Succeed())

		var current kelos.Session
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current)).To(Succeed())
		Expect(current.Spec.IdlePolicy).To(BeNil())
	})

	It("rejects invalid Session idle policy updates", func() {
		session := validSession(namespace, "invalid-idle-policy-update", "codex")
		session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{
			SuspendAfterSeconds: ptr.To(int32(3600)),
			DeleteAfterSeconds:  ptr.To(int32(7200)),
		}
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		session.Spec.IdlePolicy.SuspendAfterSeconds = ptr.To(int32(7200))
		Expect(k8sClient.Update(ctx, session)).NotTo(Succeed())
	})

	It("keeps Session configuration immutable", func() {
		mutations := []struct {
			name   string
			mutate func(*kelos.Session)
		}{
			{name: "worker-type", mutate: func(session *kelos.Session) {
				session.Spec.Worker.Type = "opencode"
			}},
			{name: "worker-effort", mutate: func(session *kelos.Session) {
				session.Spec.Worker.Effort = "high"
			}},
			{name: "worker-image", mutate: func(session *kelos.Session) {
				session.Spec.Worker.Image = "example.com/agent:latest"
			}},
			{name: "worker-workspace-ref", mutate: func(session *kelos.Session) {
				session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
			}},
			{name: "worker-agent-config-refs", mutate: func(session *kelos.Session) {
				session.Spec.Worker.AgentConfigRefs = []kelos.AgentConfigReference{{Name: "agent-config"}}
			}},
			{name: "initial-branch", mutate: func(session *kelos.Session) {
				session.Spec.InitialBranch = "another-branch"
			}},
			{name: "initial-prompt", mutate: func(session *kelos.Session) {
				session.Spec.InitialPrompt = "another prompt"
			}},
			{name: "volume-claim-template", mutate: func(session *kelos.Session) {
				session.Spec.VolumeClaimTemplate = nil
			}},
		}
		for _, mutation := range mutations {
			session := validSession(namespace, "immutable-"+mutation.name, "codex")
			Expect(k8sClient.Create(ctx, session)).To(Succeed())
			mutation.mutate(session)
			Expect(k8sClient.Update(ctx, session)).NotTo(Succeed())
		}
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
