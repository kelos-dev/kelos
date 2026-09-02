package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
	"github.com/kelos-dev/kelos/internal/sessionreset"
	"github.com/kelos-dev/kelos/internal/sessionsuspend"
	"github.com/kelos-dev/kelos/internal/sessionupdate"
)

func TestSessionReconcilerReconcilesStatefulSetRuntimeConfiguration(t *testing.T) {
	tests := []struct {
		name           string
		configuredPull corev1.PullPolicy
		wantPull       corev1.PullPolicy
	}{
		{name: "configured pull policy", configuredPull: corev1.PullIfNotPresent, wantPull: corev1.PullIfNotPresent},
		{name: "default pull policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			if err := appsv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := rbacv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := kelos.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}

			session := testSession("chat", "claude-code")
			statefulSet := testSessionStatefulSet(session)
			statefulSet.Spec.Template.Spec.InitContainers[0].Image = "runtime:stale"
			statefulSet.Spec.Template.Spec.InitContainers[0].ImagePullPolicy = corev1.PullAlways
			statefulSet.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{},
			}
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
				WithObjects(session, statefulSet).
				Build()
			reconciler := testSessionReconciler(cl, scheme)
			reconciler.SessionRuntimeImagePullPolicy = tt.configuredPull
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
			if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}

			var updated appsv1.StatefulSet
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updated); err != nil {
				t.Fatalf("getting updated Session StatefulSet: %v", err)
			}
			runtimeContainer := updated.Spec.Template.Spec.InitContainers[0]
			if runtimeContainer.Image != "runtime:test" {
				t.Fatalf("runtime init container image = %q, want %q", runtimeContainer.Image, "runtime:test")
			}
			if runtimeContainer.ImagePullPolicy != tt.wantPull {
				t.Fatalf("runtime init container imagePullPolicy = %q, want %q", runtimeContainer.ImagePullPolicy, tt.wantPull)
			}
			if updated.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType || updated.Spec.UpdateStrategy.RollingUpdate != nil {
				t.Fatalf("StatefulSet updateStrategy = %#v, want OnDelete", updated.Spec.UpdateStrategy)
			}
			podSpec := updated.Spec.Template.Spec
			if podSpec.ServiceAccountName != sessionRuntimeAccessName(session) {
				t.Fatalf("Session serviceAccountName = %q, want %q", podSpec.ServiceAccountName, sessionRuntimeAccessName(session))
			}
			if podSpec.AutomountServiceAccountToken == nil || !*podSpec.AutomountServiceAccountToken {
				t.Fatalf("automountServiceAccountToken = %v, want true", podSpec.AutomountServiceAccountToken)
			}
			wantEnvironment := map[string]string{
				"KELOS_SESSION_NAME":      session.Name,
				"KELOS_SESSION_NAMESPACE": session.Namespace,
			}
			sawPodUID := false
			for _, environment := range podSpec.Containers[0].Env {
				if value, ok := wantEnvironment[environment.Name]; ok && environment.Value == value {
					delete(wantEnvironment, environment.Name)
				}
				if environment.Name == "KELOS_SESSION_POD_UID" && environment.ValueFrom != nil && environment.ValueFrom.FieldRef != nil && environment.ValueFrom.FieldRef.FieldPath == "metadata.uid" {
					sawPodUID = true
				}
			}
			if len(wantEnvironment) != 0 || !sawPodUID {
				t.Fatalf("Session runtime environment is incomplete: missing=%v podUID=%t", wantEnvironment, sawPodUID)
			}
		})
	}
}

func TestSessionReconcilerReconcilesSuspendedStatefulSetSpec(t *testing.T) {
	tests := []struct {
		name          string
		image         string
		imageOverride string
		useTini       bool
	}{
		{
			name:    "bundled image",
			image:   CodexImage,
			useTini: true,
		},
		{
			name:          "explicit image override",
			image:         CodexImage,
			imageOverride: CodexImage,
			useTini:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}

			session := testSession("suspended-runtime-command", "codex")
			session.Spec.Suspend = ptr.To(true)
			session.Spec.Worker.Image = tt.imageOverride
			statefulSet := testSessionStatefulSet(session)
			statefulSet.Spec.Replicas = ptr.To(int32(0))
			statefulSet.Spec.ServiceName = sessionServiceName(session)
			statefulSet.Spec.PodManagementPolicy = appsv1.ParallelPodManagement
			statefulSet.Spec.Selector = &metav1.LabelSelector{MatchLabels: sessionSelectorLabels(session)}
			statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: WorkspaceVolumeName},
				Spec:       *session.Spec.VolumeClaimTemplate.DeepCopy(),
			}}
			statefulSet.Labels = map[string]string{"stale": "label"}
			statefulSet.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{},
			}
			statefulSet.Spec.RevisionHistoryLimit = ptr.To(int32(1))
			statefulSet.Spec.MinReadySeconds = 10
			statefulSet.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
				WhenScaled:  appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
			}
			statefulSet.Spec.Ordinals = &appsv1.StatefulSetOrdinals{Start: 3}
			mainContainer := &statefulSet.Spec.Template.Spec.Containers[0]
			mainContainer.Image = tt.image
			mainContainer.Command = []string{sessionRuntimeBinary}
			mainContainer.Args = []string{"serve"}
			liveSpec := statefulSet.Spec.DeepCopy()

			reconciler := testSessionReconciler(nil, scheme)
			if tt.imageOverride == "" {
				reconciler.JobBuilder.CodexImage = CodexImage
			}
			desired, _, err := reconciler.buildSessionStatefulSet(session, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
				WithObjects(session, statefulSet).
				Build()
			reconciler.Client = cl
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
			if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}

			var updated appsv1.StatefulSet
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updated); err != nil {
				t.Fatalf("getting updated Session StatefulSet: %v", err)
			}
			if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
				t.Fatalf("StatefulSet replicas = %v, want 0", updated.Spec.Replicas)
			}
			desired.Spec.Replicas = ptr.To(int32(0))
			desired.Spec.ServiceName = liveSpec.ServiceName
			desired.Spec.PodManagementPolicy = liveSpec.PodManagementPolicy
			desired.Spec.Selector = liveSpec.Selector
			desired.Spec.VolumeClaimTemplates = liveSpec.VolumeClaimTemplates
			desired.Spec.RevisionHistoryLimit = liveSpec.RevisionHistoryLimit
			if !apiequality.Semantic.DeepEqual(updated.Labels, desired.Labels) {
				t.Fatalf("StatefulSet labels = %#v, want %#v", updated.Labels, desired.Labels)
			}
			if !apiequality.Semantic.DeepEqual(updated.Spec, desired.Spec) {
				t.Fatal("StatefulSet controller-managed spec fields were not fully reconciled")
			}
			if updated.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
				t.Fatalf("StatefulSet podManagementPolicy = %q, want preserved %q", updated.Spec.PodManagementPolicy, appsv1.ParallelPodManagement)
			}
			if len(updated.Spec.VolumeClaimTemplates) != 1 || len(updated.Spec.VolumeClaimTemplates[0].OwnerReferences) != 0 {
				t.Fatalf("StatefulSet volumeClaimTemplates = %#v, want preserved existing template", updated.Spec.VolumeClaimTemplates)
			}
			assertAgentProcessCommand(t, updated.Spec.Template.Spec.Containers[0].Command, sessionRuntimeBinary, tt.useTini)
			if got := updated.Spec.Template.Spec.Containers[0].Args; !reflect.DeepEqual(got, []string{"serve"}) {
				t.Fatalf("Session runtime args = %v, want [serve]", got)
			}
		})
	}
}

func TestSessionReconcilerKeepsRuntimeStoppedWhenCredentialRefreshFails(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	session := testSession("resume", "codex")
	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace},
		Spec: kelos.WorkspaceSpec{
			SecretRef: &kelos.SecretReference{Name: tokenSecretName},
		},
	}
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.Replicas = ptr.To(int32(0))
	statefulSet.Spec.Template.Spec.InitContainers[0].Image = "runtime:old"
	statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: GitHubTokenVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: tokenSecretName,
		}},
	})
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tokenSecretName,
			Namespace: session.Namespace,
			Annotations: map[string]string{
				githubAppSecretAnnotation: "github-app",
				tokenExpiresAtAnnotation:  time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{GitHubTokenSecretKey: []byte("expired")},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, workspace, statefulSet, tokenSecret).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != tokenRefreshRetryInterval {
		t.Fatalf("Reconcile() requeueAfter = %s, want %s", result.RequeueAfter, tokenRefreshRetryInterval)
	}

	var updated appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updated); err != nil {
		t.Fatalf("getting updated Session StatefulSet: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0", updated.Spec.Replicas)
	}
	if got := updated.Spec.Template.Spec.InitContainers[0].Image; got != "runtime:test" {
		t.Fatalf("runtime init container image = %q, want %q", got, "runtime:test")
	}
}

func TestSessionGitHubTokenMinimumValidity(t *testing.T) {
	tests := []struct {
		name               string
		currentStatefulSet bool
		replicas           *int32
		phase              kelos.SessionPhase
		podName            string
		usesToken          bool
		want               time.Duration
	}{
		{
			name:  "runtime creation",
			phase: kelos.SessionPhaseReady,
			want:  tokenRefreshMargin,
		},
		{
			name:               "stopped runtime",
			currentStatefulSet: true,
			replicas:           ptr.To(int32(0)),
			phase:              kelos.SessionPhaseReady,
			podName:            "token-policy-0",
			usesToken:          true,
			want:               tokenRefreshMargin,
		},
		{
			name:               "runtime not ready",
			currentStatefulSet: true,
			replicas:           ptr.To(int32(1)),
			phase:              kelos.SessionPhasePending,
			podName:            "token-policy-0",
			usesToken:          true,
			want:               tokenRefreshMargin,
		},
		{
			name:               "missing Pod identity",
			currentStatefulSet: true,
			replicas:           ptr.To(int32(1)),
			phase:              kelos.SessionPhaseReady,
			usesToken:          true,
			want:               tokenRefreshMargin,
		},
		{
			name:               "token not deployed",
			currentStatefulSet: true,
			replicas:           ptr.To(int32(1)),
			phase:              kelos.SessionPhaseReady,
			podName:            "token-policy-0",
			want:               tokenRefreshMargin,
		},
		{
			name:               "ready runtime using token",
			currentStatefulSet: true,
			phase:              kelos.SessionPhaseReady,
			podName:            "token-policy-0",
			usesToken:          true,
			want:               0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := testSession("token-policy", "codex")
			session.Status.Phase = tt.phase
			session.Status.PodName = tt.podName
			var statefulSet *appsv1.StatefulSet
			if tt.currentStatefulSet {
				statefulSet = testSessionStatefulSet(session)
				statefulSet.Spec.Replicas = tt.replicas
				if tt.usesToken {
					statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
						Name: GitHubTokenVolumeName,
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName: sessionGitHubTokenSecretName(session.Name),
						}},
					})
				}
			}
			if got := sessionGitHubTokenMinimumValidity(session, statefulSet); got != tt.want {
				t.Fatalf("minimum token validity = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSessionReconcilerPreservesReadyStatusWhenInputReadFails(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("ready-input-retry", "codex")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	session.Status.Phase = kelos.SessionPhaseReady
	session.Status.PodName = "ready-input-retry-0"
	session.Status.PodUID = types.UID("ready-pod-uid")
	session.Status.Conditions = []metav1.Condition{{
		Type:               kelos.SessionConditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: session.Generation,
		Reason:             "RuntimeReady",
		Message:            "Session runtime is ready",
	}}
	originalStatus := session.Status.DeepCopy()
	statefulSet := testSessionStatefulSet(session)
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace},
		Spec:       kelos.WorkspaceSpec{Repo: "https://github.com/kelos-dev/kelos.git"},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, workspace).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kelos.Workspace); ok {
					return apierrors.NewServiceUnavailable("temporary Workspace read failure")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	reconciler := testSessionReconciler(cl, scheme)

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("Reconcile() error = %v, want ServiceUnavailable", err)
	}

	var updated kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(&updated.Status, originalStatus) {
		t.Fatalf("Session status = %#v, want preserved %#v", updated.Status, *originalStatus)
	}
}

func TestSessionReconcilerKeepsReadyRuntimeAvailableWhenTokenRefreshFails(t *testing.T) {
	t.Parallel()
	tokenRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenRequests++
		http.Error(writer, "temporary failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("active-token-refresh", "codex")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace},
		Spec: kelos.WorkspaceSpec{
			SecretRef: &kelos.SecretReference{Name: "github-app"},
		},
	}
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "github-app",
			Namespace:       session.Namespace,
			UID:             types.UID("github-app-uid"),
			ResourceVersion: "source-version",
		},
		Data: testGitHubAppSecretData(t),
	}
	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            tokenSecretName,
			Namespace:       session.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))},
			Annotations: map[string]string{
				githubAppSecretAnnotation:         source.Name,
				sessionTokenFingerprintAnnotation: sessionGitHubTokenMintFingerprint(source, server.URL),
				tokenExpiresAtAnnotation:          time.Now().Add(tokenRefreshMargin / 2).UTC().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{GitHubTokenSecretKey: []byte("still-valid")},
	}

	reconciler := testSessionReconciler(nil, scheme)
	reconciler.TokenClient = &githubapp.TokenClient{BaseURL: server.URL, Client: server.Client()}
	resolvedWorkspace := workspace.Spec.DeepCopy()
	resolvedWorkspace.SecretRef = &kelos.SecretReference{Name: tokenSecretName}
	statefulSet, _, err := reconciler.buildSessionStatefulSet(session, resolvedWorkspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	statefulSet.UID = types.UID("active-token-refresh-statefulset-uid")
	statefulSet.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))}
	const revision = "current-revision"
	statefulSet.Status.CurrentRevision = revision
	statefulSet.Status.UpdateRevision = revision
	statefulSet.Status.ObservedGeneration = statefulSet.Generation

	podLabels := make(map[string]string, len(statefulSet.Spec.Template.Labels)+1)
	for key, value := range statefulSet.Spec.Template.Labels {
		podLabels[key] = value
	}
	podLabels[appsv1.StatefulSetRevisionLabel] = revision
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            statefulSet.Name + "-0",
			Namespace:       session.Namespace,
			UID:             types.UID("active-token-refresh-pod-uid"),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
			Labels:          podLabels,
		},
		Spec: *statefulSet.Spec.Template.Spec.DeepCopy(),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	session.Status.Phase = kelos.SessionPhaseReady
	session.Status.PodName = pod.Name
	session.Status.PodUID = pod.UID

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, workspace, source, tokenSecret, statefulSet, pod).
		Build()
	reconciler.Client = cl

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != tokenRefreshRetryInterval {
		t.Fatalf("Reconcile() requeueAfter = %s, want %s", result.RequeueAfter, tokenRefreshRetryInterval)
	}
	if tokenRequests != 1 {
		t.Fatalf("GitHub App token requests = %d, want 1 proactive refresh attempt", tokenRequests)
	}

	var updatedSession kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if updatedSession.Status.Phase != kelos.SessionPhaseReady {
		t.Fatalf("Session phase = %q, want %q", updatedSession.Status.Phase, kelos.SessionPhaseReady)
	}
	if updatedSession.Status.PodName != pod.Name || updatedSession.Status.PodUID != pod.UID {
		t.Fatalf("Session Pod identity = %q/%q, want %q/%q", updatedSession.Status.PodName, updatedSession.Status.PodUID, pod.Name, pod.UID)
	}
}

func TestSessionReconcilerUsesCurrentWorkspaceCredentialsWhenResuming(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("resume-with-pat", "codex")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace},
		Spec: kelos.WorkspaceSpec{
			Repo:      "https://github.com/kelos-dev/kelos.git",
			SecretRef: &kelos.SecretReference{Name: "pat"},
		},
	}
	pat := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pat", Namespace: session.Namespace},
		Data:       map[string][]byte{GitHubTokenSecretKey: []byte("github_pat_current")},
	}
	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.Replicas = ptr.To(int32(0))
	statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: GitHubTokenVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: tokenSecretName,
		}},
	})
	staleToken := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            tokenSecretName,
			Namespace:       session.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))},
			Annotations: map[string]string{
				githubAppSecretAnnotation: "removed-github-app",
				tokenExpiresAtAnnotation:  time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{GitHubTokenSecretKey: []byte("ghs_stale")},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, workspace, pat, statefulSet, staleToken).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 1 {
		t.Fatalf("StatefulSet replicas = %v, want 1", updated.Spec.Replicas)
	}
	if sessionPodSpecUsesSecret(&updated.Spec.Template.Spec, tokenSecretName) {
		t.Fatalf("StatefulSet still uses stale derived token Secret %q", tokenSecretName)
	}
	if !sessionPodSpecUsesSecret(&updated.Spec.Template.Spec, pat.Name) {
		t.Fatalf("StatefulSet does not use PAT Secret %q", pat.Name)
	}
}

func TestSessionReconcilerCompletesSuspendedResetWithoutRefreshingCredentials(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	resetState, err := sessionreset.EncodeState(sessionreset.State{RequestID: "reset-1", Phase: sessionreset.PhaseStarting})
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("suspended-reset", "codex")
	session.Spec.Suspend = ptr.To(true)
	session.Annotations = map[string]string{
		sessionreset.RequestAnnotation: "reset-1",
		sessionreset.StateAnnotation:   resetState,
	}
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.Replicas = ptr.To(int32(0))
	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: GitHubTokenVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: tokenSecretName,
		}},
	})
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tokenSecretName,
			Namespace: session.Namespace,
			Annotations: map[string]string{
				githubAppSecretAnnotation: "github-app",
				tokenExpiresAtAnnotation:  time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{GitHubTokenSecretKey: []byte("expired")},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, tokenSecret).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !result.Requeue {
		t.Fatal("Reconcile() did not requeue after completing the suspended reset")
	}

	var updated kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatalf("getting updated Session: %v", err)
	}
	if updated.Status.Phase != kelos.SessionPhaseSuspended {
		t.Fatalf("Session phase = %q, want %q", updated.Status.Phase, kelos.SessionPhaseSuspended)
	}
	if updated.Annotations[sessionreset.RequestAnnotation] != "" || updated.Annotations[sessionreset.StateAnnotation] != "" {
		t.Fatalf("Session reset annotations were not cleared: %#v", updated.Annotations)
	}
	var updatedStatefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updatedStatefulSet); err != nil {
		t.Fatalf("getting updated Session StatefulSet: %v", err)
	}
	if updatedStatefulSet.Spec.Replicas == nil || *updatedStatefulSet.Spec.Replicas != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0", updatedStatefulSet.Spec.Replicas)
	}
}

func TestSessionReconcilerMigratesWorkspaceClaimOwnership(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	session := testSession("chat", "claude-code")
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: WorkspaceVolumeName},
		Spec:       *session.Spec.VolumeClaimTemplate.DeepCopy(),
	}}
	statefulSet.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
		WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
		WhenScaled:  appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkspaceVolumeName + "-" + statefulSet.Name + "-0",
			Namespace: session.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet")),
			},
		},
		Spec: *session.Spec.VolumeClaimTemplate.DeepCopy(),
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, claim).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updatedStatefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updatedStatefulSet); err != nil {
		t.Fatal(err)
	}
	retention := updatedStatefulSet.Spec.PersistentVolumeClaimRetentionPolicy
	if retention == nil || retention.WhenDeleted != appsv1.RetainPersistentVolumeClaimRetentionPolicyType || retention.WhenScaled != appsv1.RetainPersistentVolumeClaimRetentionPolicyType {
		t.Fatalf("persistentVolumeClaimRetentionPolicy = %#v, want Retain/Retain", retention)
	}
	var updatedClaim corev1.PersistentVolumeClaim
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(claim), &updatedClaim); err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(&updatedClaim, session) {
		t.Fatalf("workspace PersistentVolumeClaim ownerReferences = %#v, want Session owner", updatedClaim.OwnerReferences)
	}
}

func TestSessionReconcilerResetsPersistentWorkspace(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	session := testSession("chat", "codex")
	session.Annotations = map[string]string{sessionreset.RequestAnnotation: "reset-1"}
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.Replicas = ptr.To(int32(1))
	statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: WorkspaceVolumeName},
		Spec:       *session.Spec.VolumeClaimTemplate.DeepCopy(),
	}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:            statefulSet.Name + "-0",
		Namespace:       session.Namespace,
		UID:             types.UID("old-pod-uid"),
		Annotations:     map[string]string{sessionNameAnnotation: session.Name},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
	}}
	claimName := WorkspaceVolumeName + "-" + statefulSet.Name + "-0"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name:            claimName,
		Namespace:       session.Namespace,
		UID:             types.UID("old-claim-uid"),
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))},
	}}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, pod, claim).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() stopping error = %v", err)
	}
	var updatedStatefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updatedStatefulSet); err != nil {
		t.Fatal(err)
	}
	if updatedStatefulSet.Spec.Replicas == nil || *updatedStatefulSet.Spec.Replicas != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0", updatedStatefulSet.Spec.Replicas)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old Session Pod still exists: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(claim), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("workspace claim was deleted before the Pod stopped: %v", err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() deleting storage error = %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(claim), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old workspace claim still exists: %v", err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() starting error = %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updatedStatefulSet); err != nil {
		t.Fatal(err)
	}
	if updatedStatefulSet.Spec.Replicas == nil || *updatedStatefulSet.Spec.Replicas != 1 {
		t.Fatalf("StatefulSet replicas = %v, want 1", updatedStatefulSet.Spec.Replicas)
	}

	replacementClaim := claim.DeepCopy()
	replacementClaim.ResourceVersion = ""
	replacementClaim.UID = types.UID("new-claim-uid")
	if err := cl.Create(context.Background(), replacementClaim); err != nil {
		t.Fatal(err)
	}
	replacementPod := pod.DeepCopy()
	replacementPod.ResourceVersion = ""
	replacementPod.UID = types.UID("new-pod-uid")
	if err := cl.Create(context.Background(), replacementPod); err != nil {
		t.Fatal(err)
	}
	replacementPod.Status.Phase = corev1.PodRunning
	replacementPod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := cl.Status().Update(context.Background(), replacementPod); err != nil {
		t.Fatal(err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() completion error = %v", err)
	}
	var updatedSession kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updatedSession); err != nil {
		t.Fatal(err)
	}
	if updatedSession.Annotations[sessionreset.RequestAnnotation] != "" || updatedSession.Annotations[sessionreset.StateAnnotation] != "" {
		t.Fatalf("reset annotations were not cleared: %#v", updatedSession.Annotations)
	}
	if updatedSession.Status.Phase != kelos.SessionPhaseReady || updatedSession.Status.PodUID != replacementPod.UID {
		t.Fatalf("Session status after reset = %#v", updatedSession.Status)
	}
}

func TestSessionReconcilerDoesNotStealWorkspaceClaim(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	session := testSession("chat", "claude-code")
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: WorkspaceVolumeName}}}
	otherOwner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      "other-owner",
		Namespace: session.Namespace,
		UID:       types.UID("other-owner-uid"),
	}}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name:      WorkspaceVolumeName + "-" + statefulSet.Name + "-0",
		Namespace: session.Namespace,
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(otherOwner, corev1.SchemeGroupVersion.WithKind("ConfigMap")),
		},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session, statefulSet, otherOwner, claim).Build()
	reconciler := testSessionReconciler(cl, scheme)
	err := reconciler.ensureSessionWorkspaceClaimOwnership(context.Background(), session, statefulSet)
	if err == nil || !strings.Contains(err.Error(), `PersistentVolumeClaim "workspace-chat-0" is controlled by ConfigMap "other-owner"`) {
		t.Fatalf("ensureSessionWorkspaceClaimOwnership() error = %v", err)
	}
}

func TestSessionReconcilerTreatsAdmissionRewrittenRuntimeImageAsCurrent(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	session := testSession("chat", "claude-code")
	reconciler := testSessionReconciler(nil, nil)
	statefulSet, _, err := reconciler.buildSessionStatefulSet(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	statefulSet.UID = types.UID(statefulSet.Name + "-uid")
	statefulSet.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))}
	statefulSet.Status.UpdateRevision = "desired-revision"
	statefulSet.Status.ObservedGeneration = statefulSet.Generation
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSet.Name + "-0",
			Namespace: session.Namespace,
			UID:       types.UID("pod-uid"),
			Labels: map[string]string{
				appsv1.StatefulSetRevisionLabel: statefulSet.Status.UpdateRevision,
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		},
		Spec: *statefulSet.Spec.Template.Spec.DeepCopy(),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	pod.Spec.InitContainers[0].Image = "mirror.example/kelos-session-runtime@sha256:admitted"
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, pod).
		Build()
	reconciler = testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() result = %#v, want no runtime update requeue", result)
	}
	var currentPod corev1.Pod
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &currentPod); err != nil {
		t.Fatalf("admission-rewritten Session Pod was replaced: %v", err)
	}
	var updatedSession kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updatedSession); err != nil {
		t.Fatal(err)
	}
	if hasSessionRuntimeUpdateAnnotations(&updatedSession) {
		t.Fatalf("admission-rewritten Session Pod entered drain protocol: %#v", updatedSession.Annotations)
	}
}

func TestSessionReconcilerWaitsForMatchingRuntimeDrain(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	session := testSession("chat", "claude-code")
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Status.UpdateRevision = "desired-revision"
	statefulSet.Status.ObservedGeneration = statefulSet.Generation
	statefulSet.Spec.Template.Spec.InitContainers[0].Image = "runtime:old"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            statefulSet.Name + "-0",
			Namespace:       session.Namespace,
			UID:             types.UID("pod-uid"),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
			Annotations:     map[string]string{sessionNameAnnotation: session.Name},
			Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: "stale-revision"},
		},
		Spec: *statefulSet.Spec.Template.Spec.DeepCopy(),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() requesting drain error = %v", err)
	}

	var updatedSession kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updatedSession); err != nil {
		t.Fatal(err)
	}
	drainRequest, err := sessionupdate.Decode(updatedSession.Annotations[sessionupdate.RequestAnnotation])
	if err != nil {
		t.Fatal(err)
	}
	if want := sessionupdate.NewRequest(pod.UID, statefulSet.Status.UpdateRevision); !reflect.DeepEqual(drainRequest, want) {
		t.Fatalf("runtime update request = %#v, want %#v", drainRequest, want)
	}
	var updatedStatefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updatedStatefulSet); err != nil {
		t.Fatal(err)
	}
	if got := updatedStatefulSet.Spec.Template.Spec.InitContainers[0].Image; got != "runtime:test" {
		t.Fatalf("desired runtime image while draining = %q, want runtime:test", got)
	}
	if updatedStatefulSet.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType || updatedStatefulSet.Spec.UpdateStrategy.RollingUpdate != nil {
		t.Fatalf("StatefulSet updateStrategy = %#v, want OnDelete", updatedStatefulSet.Spec.UpdateStrategy)
	}
	var currentPod corev1.Pod
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &currentPod); err != nil {
		t.Fatalf("Session Pod was replaced before it drained: %v", err)
	}

	staleReport, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    types.UID("stale-pod"),
		Phase:     sessionupdate.PhaseDrained,
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedSession.Annotations[sessionupdate.ReportAnnotation] = staleReport
	if err := cl.Update(context.Background(), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() with stale drain acknowledgement error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &updatedSession); err != nil {
		t.Fatal(err)
	}
	report, err := sessionupdate.DecodeReport(updatedSession.Annotations[sessionupdate.ReportAnnotation])
	if err != nil {
		t.Fatal(err)
	}
	if report.PodUID != types.UID("stale-pod") {
		t.Fatalf("runtime update report after controller reconcile = %#v", report)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &currentPod); err != nil {
		t.Fatalf("Session Pod was replaced after a stale drain acknowledgement: %v", err)
	}

	drainingReport, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    pod.UID,
		Phase:     sessionupdate.PhaseDraining,
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedSession.Annotations[sessionupdate.ReportAnnotation] = drainingReport
	if err := cl.Update(context.Background(), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() while draining error = %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &currentPod); err != nil {
		t.Fatalf("Session Pod was replaced while still draining: %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &updatedSession); err != nil {
		t.Fatal(err)
	}
	drainedReport, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    pod.UID,
		Phase:     sessionupdate.PhaseDrained,
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedSession.Annotations[sessionupdate.ReportAnnotation] = drainedReport
	if err := cl.Update(context.Background(), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() after drain error = %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &currentPod); !apierrors.IsNotFound(err) {
		t.Fatalf("getting replaced Session Pod error = %v, want NotFound", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() after Pod replacement error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &updatedSession); err != nil {
		t.Fatal(err)
	}
	if hasSessionRuntimeUpdateAnnotations(&updatedSession) {
		t.Fatalf("runtime update annotations were not cleared: %#v", updatedSession.Annotations)
	}
}

func TestSessionReconcilerReplacesFailedPodWithoutDrain(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	session := testSession("failed-runtime", "claude-code")
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Status.UpdateRevision = "desired-revision"
	statefulSet.Status.ObservedGeneration = statefulSet.Generation
	statefulSet.Spec.Template.Spec.InitContainers[0].Image = "runtime:old"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            statefulSet.Name + "-0",
			Namespace:       session.Namespace,
			UID:             types.UID("failed-pod-uid"),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
			Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: "stale-revision"},
		},
		Spec: *statefulSet.Spec.Template.Spec.DeepCopy(),
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  kelos.AgentContainerName,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updatedStatefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updatedStatefulSet); err != nil {
		t.Fatal(err)
	}
	if got := updatedStatefulSet.Spec.Template.Spec.InitContainers[0].Image; got != "runtime:test" {
		t.Fatalf("desired runtime image = %q, want runtime:test", got)
	}
	if updatedStatefulSet.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType || updatedStatefulSet.Spec.UpdateStrategy.RollingUpdate != nil {
		t.Fatalf("StatefulSet updateStrategy = %#v, want OnDelete", updatedStatefulSet.Spec.UpdateStrategy)
	}
	var currentPod corev1.Pod
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &currentPod); !apierrors.IsNotFound(err) {
		t.Fatalf("getting failed Session Pod error = %v, want NotFound", err)
	}
	var updatedSession kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updatedSession); err != nil {
		t.Fatal(err)
	}
	if hasSessionRuntimeUpdateAnnotations(&updatedSession) {
		t.Fatalf("failed Pod unexpectedly entered drain protocol: %#v", updatedSession.Annotations)
	}
}

func TestSessionReconcilerKeepsDrainRequestWhilePodTerminates(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	session := testSession("terminating-runtime", "claude-code")
	session.Annotations = map[string]string{
		sessionupdate.RequestAnnotation:     "request",
		sessionupdate.ReportAnnotation:      "report",
		sessionupdate.ForceUpdateAnnotation: "true",
	}
	statefulSet := testSessionStatefulSet(session)
	now := metav1.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              statefulSet.Name + "-0",
			Namespace:         session.Namespace,
			UID:               types.UID("terminating-pod-uid"),
			DeletionTimestamp: &now,
			Finalizers:        []string{"test.kelos.dev/terminating"},
			OwnerReferences:   []metav1.OwnerReference{*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		},
		Spec: *statefulSet.Spec.Template.Spec.DeepCopy(),
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updatedSession kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updatedSession); err != nil {
		t.Fatal(err)
	}
	for annotation, want := range session.Annotations {
		if got := updatedSession.Annotations[annotation]; got != want {
			t.Fatalf("Session annotation %q = %q, want %q", annotation, got, want)
		}
	}
}

func TestSessionReconcilerCreatesStatefulSetAndObservesPod(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	session := testSession("chat", "claude-code")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{Env: []corev1.EnvVar{
		{Name: "KELOS_SESSION_SETUP_ONLY", Value: "1"},
	}}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	workloadKey := client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session)}
	var statefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), workloadKey, &statefulSet); err != nil {
		t.Fatalf("getting Session StatefulSet: %v", err)
	}
	if !metav1.IsControlledBy(&statefulSet, session) {
		t.Fatal("Session StatefulSet does not have the Session as controller owner")
	}
	if statefulSet.Spec.Replicas == nil || *statefulSet.Spec.Replicas != 1 {
		t.Fatalf("StatefulSet replicas = %v, want 1", statefulSet.Spec.Replicas)
	}
	serviceKey := client.ObjectKey{Namespace: session.Namespace, Name: sessionServiceName(session)}
	if statefulSet.Spec.ServiceName != serviceKey.Name {
		t.Fatalf("StatefulSet serviceName = %q, want %q", statefulSet.Spec.ServiceName, serviceKey.Name)
	}
	if statefulSet.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType || statefulSet.Spec.UpdateStrategy.RollingUpdate != nil {
		t.Fatalf("StatefulSet updateStrategy = %#v, want OnDelete", statefulSet.Spec.UpdateStrategy)
	}
	accessName := sessionRuntimeAccessName(session)
	if statefulSet.Spec.Template.Spec.ServiceAccountName != accessName {
		t.Fatalf("Session serviceAccountName = %q, want %q", statefulSet.Spec.Template.Spec.ServiceAccountName, accessName)
	}
	var service corev1.Service
	if err := cl.Get(context.Background(), serviceKey, &service); err != nil {
		t.Fatalf("getting Session Service: %v", err)
	}
	if !metav1.IsControlledBy(&service, session) || service.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("Session Service = %#v", service)
	}
	if !reflect.DeepEqual(service.Spec.Selector, statefulSet.Spec.Selector.MatchLabels) {
		t.Fatalf("Session Service selector = %#v, want %#v", service.Spec.Selector, statefulSet.Spec.Selector.MatchLabels)
	}
	podSpec := statefulSet.Spec.Template.Spec
	if workspaceVolume := findVolume(podSpec.Volumes, WorkspaceVolumeName); workspaceVolume != nil {
		t.Fatalf("workspace volume = %#v, want StatefulSet volume claim template", workspaceVolume)
	}
	if len(statefulSet.Spec.VolumeClaimTemplates) != 1 || statefulSet.Spec.VolumeClaimTemplates[0].Name != WorkspaceVolumeName {
		t.Fatalf("volumeClaimTemplates = %#v", statefulSet.Spec.VolumeClaimTemplates)
	}
	storage := statefulSet.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Fatalf("workspace storage = %s, want 1Gi", storage.String())
	}
	retention := statefulSet.Spec.PersistentVolumeClaimRetentionPolicy
	if retention == nil || retention.WhenDeleted != appsv1.RetainPersistentVolumeClaimRetentionPolicyType || retention.WhenScaled != appsv1.RetainPersistentVolumeClaimRetentionPolicyType {
		t.Fatalf("persistentVolumeClaimRetentionPolicy = %#v", retention)
	}
	if !metav1.IsControlledBy(&statefulSet.Spec.VolumeClaimTemplates[0], session) {
		t.Fatalf("workspace claim template ownerReferences = %#v, want Session owner", statefulSet.Spec.VolumeClaimTemplates[0].OwnerReferences)
	}
	if podSpec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Fatalf("restartPolicy = %q, want %q", podSpec.RestartPolicy, corev1.RestartPolicyAlways)
	}
	if got := statefulSet.Spec.Template.Labels["kelos.dev/session"]; got != session.Name {
		t.Fatalf("kelos.dev/session label = %q, want %q", got, session.Name)
	}
	if _, exists := statefulSet.Spec.Template.Labels["kelos.dev/task"]; exists {
		t.Fatal("Session Pod retained the Task label")
	}
	if statefulSet.Spec.Template.Annotations[sessionNameAnnotation] != session.Name {
		t.Fatalf("Session Pod template annotations = %#v", statefulSet.Spec.Template.Annotations)
	}
	if len(podSpec.Containers) == 0 {
		t.Fatal("Session Pod has no containers")
	}
	assertAgentProcessCommand(t, podSpec.Containers[0].Command, sessionRuntimeBinary, false)
	if podSpec.SecurityContext == nil || podSpec.SecurityContext.FSGroup == nil || *podSpec.SecurityContext.FSGroup != AgentUID {
		t.Fatalf("pod FSGroup = %#v, want %d", podSpec.SecurityContext, AgentUID)
	}
	if len(podSpec.InitContainers) == 0 || podSpec.InitContainers[0].Image != "runtime:test" {
		t.Fatalf("runtime init container = %#v", podSpec.InitContainers)
	}
	runtimeSecurity := podSpec.InitContainers[0].SecurityContext
	if runtimeSecurity == nil || runtimeSecurity.AllowPrivilegeEscalation == nil || *runtimeSecurity.AllowPrivilegeEscalation ||
		runtimeSecurity.RunAsNonRoot == nil || !*runtimeSecurity.RunAsNonRoot ||
		runtimeSecurity.SeccompProfile == nil || runtimeSecurity.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault ||
		runtimeSecurity.Capabilities == nil || len(runtimeSecurity.Capabilities.Drop) != 1 || runtimeSecurity.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("runtime init container securityContext = %#v", runtimeSecurity)
	}
	if podSpec.AutomountServiceAccountToken == nil || !*podSpec.AutomountServiceAccountToken {
		t.Fatalf("automountServiceAccountToken = %v, want true", podSpec.AutomountServiceAccountToken)
	}
	wantRuntimeEnv := map[string]string{
		"KELOS_SESSION_NAME":      session.Name,
		"KELOS_SESSION_NAMESPACE": session.Namespace,
	}
	sawPodUID := false
	for _, env := range podSpec.Containers[0].Env {
		if env.Name == "KELOS_SESSION_SETUP_ONLY" {
			t.Fatalf("reserved KELOS_SESSION_SETUP_ONLY reached the Session container with value %q", env.Value)
		}
		if value, exists := wantRuntimeEnv[env.Name]; exists {
			if env.Value != value || env.ValueFrom != nil {
				t.Fatalf("%s = %#v, want %q", env.Name, env, value)
			}
			delete(wantRuntimeEnv, env.Name)
		}
		if env.Name == "KELOS_SESSION_POD_UID" {
			if env.ValueFrom == nil || env.ValueFrom.FieldRef == nil || env.ValueFrom.FieldRef.APIVersion != "v1" || env.ValueFrom.FieldRef.FieldPath != "metadata.uid" {
				t.Fatalf("KELOS_SESSION_POD_UID = %#v", env)
			}
			sawPodUID = true
		}
	}
	if len(wantRuntimeEnv) != 0 || !sawPodUID {
		t.Fatalf("Session runtime environment is missing %v", wantRuntimeEnv)
	}
	statefulSet.Status.UpdateRevision = "desired-revision"
	statefulSet.Status.ObservedGeneration = statefulSet.Generation
	if err := cl.Status().Update(context.Background(), &statefulSet); err != nil {
		t.Fatalf("updating StatefulSet status: %v", err)
	}
	podLabels := make(map[string]string, len(statefulSet.Spec.Template.Labels)+1)
	for key, value := range statefulSet.Spec.Template.Labels {
		podLabels[key] = value
	}
	podLabels[appsv1.StatefulSetRevisionLabel] = statefulSet.Status.UpdateRevision

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            statefulSet.Name + "-0",
			Namespace:       session.Namespace,
			Labels:          podLabels,
			Annotations:     statefulSet.Spec.Template.Annotations,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		},
		Spec: statefulSet.Spec.Template.Spec,
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: WorkspaceVolumeName,
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: WorkspaceVolumeName + "-" + statefulSet.Name + "-0",
		}},
	})
	if err := cl.Create(context.Background(), &pod); err != nil {
		t.Fatalf("creating Session Pod: %v", err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := cl.Status().Update(context.Background(), &pod); err != nil {
		t.Fatalf("updating Pod status: %v", err)
	}
	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("Reconcile() ready error = %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want no requeue", result.RequeueAfter)
	}

	var updated kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.SessionPhaseReady {
		t.Fatalf("Session phase = %q, want %q", updated.Status.Phase, kelos.SessionPhaseReady)
	}
	if updated.Status.PodName != pod.Name {
		t.Fatalf("Session podName = %q, want %q", updated.Status.PodName, pod.Name)
	}
	ready := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.SessionConditionReady)
	active := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.SessionConditionActive)
	if ready == nil || ready.Status != metav1.ConditionTrue || active == nil || active.Status != metav1.ConditionUnknown {
		t.Fatalf("Session conditions = %#v", updated.Status.Conditions)
	}
}

func TestUpdateSessionStatusInvalidatesStaleRuntimeStatus(t *testing.T) {
	lastActivityTime := metav1.NewTime(time.Now().UTC().Truncate(time.Second).Add(-time.Hour))
	tests := []struct {
		name      string
		phase     kelos.SessionPhase
		podUID    types.UID
		wantClear bool
	}{
		{name: "not ready", phase: kelos.SessionPhasePending, podUID: types.UID("pod-uid"), wantClear: true},
		{name: "ready", phase: kelos.SessionPhaseReady, podUID: types.UID("pod-uid"), wantClear: false},
		{name: "replacement Pod", phase: kelos.SessionPhaseReady, podUID: types.UID("replacement-pod"), wantClear: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := kelos.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			session := testSession("status-test", "codex")
			session.Status.PodUID = types.UID("pod-uid")
			session.Status.Model = "gpt-5.6-sol"
			session.Status.Branch = "feature/status"
			session.Status.PullRequest = &kelos.SessionPullRequest{
				URL:   "https://github.com/kelos-dev/kelos/pull/42",
				State: kelos.SessionPullRequestStateOpen,
			}
			session.Status.Conditions = []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionTrue,
				Reason:             "TurnActive",
				LastTransitionTime: lastActivityTime,
			}}
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kelos.Session{}).
				WithObjects(session).
				Build()
			reconciler := &SessionReconciler{Client: cl}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name:      "status-test-0",
				Namespace: session.Namespace,
				UID:       tt.podUID,
			}}

			if err := reconciler.updateSessionStatus(context.Background(), session, pod, tt.phase, "status", "Status"); err != nil {
				t.Fatal(err)
			}
			var updated kelos.Session
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updated); err != nil {
				t.Fatal(err)
			}
			activity := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.SessionConditionActive)
			cleared := activity != nil && activity.Status == metav1.ConditionUnknown && updated.Status.Model == "" && updated.Status.Branch == "" && updated.Status.PullRequest == nil
			if cleared != tt.wantClear {
				t.Fatalf("runtime-owned status invalidated = %t, want %t: %#v", cleared, tt.wantClear, updated.Status)
			}
			if !tt.wantClear && (activity == nil || activity.Status != metav1.ConditionTrue) {
				t.Fatalf("Active condition = %#v, want True", activity)
			}
			if !tt.wantClear && updated.Status.Model != "gpt-5.6-sol" {
				t.Fatalf("model = %q, want gpt-5.6-sol", updated.Status.Model)
			}
			if tt.wantClear && (updated.Status.LastActivityTime == nil || !updated.Status.LastActivityTime.Time.Equal(lastActivityTime.Time)) {
				t.Fatalf("lastActivityTime = %v, want preserved %v", updated.Status.LastActivityTime, lastActivityTime)
			}
		})
	}
}

func TestSessionCodexPodUsesPersistentCodexHome(t *testing.T) {
	t.Parallel()
	session := testSession("codex-chat", "codex")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{Env: []corev1.EnvVar{{Name: "CODEX_HOME", Value: "/tmp/ignored"}}}
	reconciler := testSessionReconciler(nil, nil)
	statefulSet, _, err := reconciler.buildSessionStatefulSet(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range statefulSet.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "CODEX_HOME" {
			if env.Value != sessionCodexHome || env.ValueFrom != nil {
				t.Fatalf("CODEX_HOME = %#v, want %q", env, sessionCodexHome)
			}
			return
		}
	}
	t.Fatal("CODEX_HOME was not injected")
}

func TestSessionPodUsesInitialBranch(t *testing.T) {
	t.Parallel()
	session := testSession("issue-42", "codex")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	session.Spec.InitialBranch = "issue-42"
	session.Spec.InitialPrompt = "Investigate $(ANTHROPIC_API_KEY) and $$HOME"
	workspace := &kelos.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		Ref:  "main",
	}

	statefulSet, _, err := testSessionReconciler(nil, nil).buildSessionStatefulSet(session, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	podSpec := statefulSet.Spec.Template.Spec
	mainEnv := map[string]string{}
	for _, env := range podSpec.Containers[0].Env {
		mainEnv[env.Name] = env.Value
	}
	if mainEnv["KELOS_BRANCH"] != "issue-42" {
		t.Fatalf("KELOS_BRANCH = %q, want %q", mainEnv["KELOS_BRANCH"], "issue-42")
	}
	if _, found := mainEnv["KELOS_SESSION_INITIAL_PROMPT"]; found {
		t.Fatal("KELOS_SESSION_INITIAL_PROMPT must not be set")
	}
	if args := podSpec.Containers[0].Args; len(args) != 1 || args[0] != "serve" {
		t.Fatalf("Session runtime args = %v, want [serve]", args)
	}

	var branchSetup *corev1.Container
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == "branch-setup" {
			branchSetup = &podSpec.InitContainers[i]
			break
		}
	}
	if branchSetup == nil {
		t.Fatal("Session Pod has no branch-setup init container")
	}
	if len(branchSetup.Command) != 3 || !strings.Contains(branchSetup.Command[2], sessionInitializedPath) {
		t.Fatalf("branch-setup command = %v", branchSetup.Command)
	}
	branchFound := false
	for _, env := range branchSetup.Env {
		if env.Name == "KELOS_BRANCH" && env.Value == "issue-42" {
			branchFound = true
			break
		}
	}
	if !branchFound {
		t.Fatalf("branch-setup env = %#v, want KELOS_BRANCH=issue-42", branchSetup.Env)
	}
}

func TestSessionClaudePodUsesPersistentConfig(t *testing.T) {
	t.Parallel()
	session := testSession("claude-chat", "claude-code")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{Env: []corev1.EnvVar{{Name: "CLAUDE_CONFIG_DIR", Value: "/tmp/ignored"}}}
	reconciler := testSessionReconciler(nil, nil)
	statefulSet, _, err := reconciler.buildSessionStatefulSet(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range statefulSet.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "CLAUDE_CONFIG_DIR" {
			if env.Value != sessionClaudeConfigDir || env.ValueFrom != nil {
				t.Fatalf("CLAUDE_CONFIG_DIR = %#v, want %q", env, sessionClaudeConfigDir)
			}
			return
		}
	}
	t.Fatal("CLAUDE_CONFIG_DIR was not injected")
}

func TestSessionOpenCodePodUsesPersistentDirectories(t *testing.T) {
	t.Parallel()
	session := testSession("opencode-chat", "opencode")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{Env: []corev1.EnvVar{
		{Name: "OPENCODE_CONFIG_DIR", Value: "/tmp/ignored"},
		{Name: "XDG_DATA_HOME", Value: "/tmp/ignored"},
	}}
	reconciler := testSessionReconciler(nil, nil)
	statefulSet, _, err := reconciler.buildSessionStatefulSet(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OPENCODE_CONFIG_DIR": sessionOpenCodeConfigDir,
		"XDG_DATA_HOME":       sessionOpenCodeDataDir,
	}
	for _, env := range statefulSet.Spec.Template.Spec.Containers[0].Env {
		if value, exists := want[env.Name]; exists {
			if env.Value != value || env.ValueFrom != nil {
				t.Fatalf("%s = %#v, want %q", env.Name, env, value)
			}
			delete(want, env.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("OpenCode Session environment is missing %v", want)
	}
}

func TestSessionRuntimeUsesConfiguredServiceAccount(t *testing.T) {
	t.Parallel()
	session := testSession("custom-service-account", "codex")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{ServiceAccountName: "workload-identity"}
	reconciler := testSessionReconciler(nil, nil)
	statefulSet, _, err := reconciler.buildSessionStatefulSet(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if statefulSet.Spec.Template.Spec.ServiceAccountName != "workload-identity" {
		t.Fatalf("Session serviceAccountName = %q, want workload-identity", statefulSet.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestPrepareSessionWorkspaceInitPreservesCloneCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		container corev1.Container
		wantArgs  []string
	}{
		{
			name:      "image entrypoint",
			container: corev1.Container{Name: "git-clone", Args: []string{"clone", "repo", "/workspace/repo"}},
			wantArgs:  []string{"clone", "repo", "/workspace/repo"},
		},
		{
			name: "shell command",
			container: corev1.Container{
				Name:    "git-clone",
				Command: []string{"sh", "-c", `git -c credential.helper= "$@"`},
				Args:    []string{"--", "clone", "repo", "/workspace/repo"},
			},
			wantArgs: []string{"--", "clone", "repo", "/workspace/repo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containers := []corev1.Container{tt.container}
			if err := prepareSessionWorkspaceInit(containers, "", gitCredentialDefaultUsername); err != nil {
				t.Fatal(err)
			}
			script := ""
			if len(containers[0].Command) == 3 {
				script = containers[0].Command[2]
			} else if len(containers[0].Command) == 2 && len(containers[0].Args) > 0 {
				script = containers[0].Args[0]
			}
			if !strings.Contains(script, sessionInitializedPath) || !strings.Contains(script, "rm -rf -- /workspace/repo") {
				t.Fatalf("prepared command = %#v", containers[0].Command)
			}
			if tt.name == "shell command" && !strings.Contains(script, `credential.helper`) {
				t.Fatalf("prepared command lost original shell command: %q", script)
			}
			if got := containers[0].Args[len(containers[0].Args)-len(tt.wantArgs):]; !reflect.DeepEqual(got, tt.wantArgs) {
				t.Fatalf("prepared args = %#v, want suffix %#v", containers[0].Args, tt.wantArgs)
			}
		})
	}
}

func TestPrepareSessionWorkspaceInitRefreshesCredentials(t *testing.T) {
	credentialHelper := gitCredentialHelperForTokenFile("/test/GITHUB_TOKEN", "GITHUB_TOKEN")
	containers := []corev1.Container{{
		Name:    "git-clone",
		Command: []string{"sh", "-c", `git -c credential.helper= "$@"`},
		Args:    []string{"--", "clone", "repo", "/workspace/repo"},
	}}

	if err := prepareSessionWorkspaceInit(containers, credentialHelper, gitCredentialDefaultUsername); err != nil {
		t.Fatal(err)
	}
	script := containers[0].Command[2]
	marker := strings.Index(script, sessionInitializedPath)
	config := strings.Index(script, "config credential.username "+gitCredentialDefaultUsername)
	exit := strings.Index(script, "exit 0")
	if marker == -1 || config <= marker || exit <= config {
		t.Fatalf("prepared command does not refresh credentials before exiting: %q", script)
	}
	if !strings.Contains(script, credentialHelper) {
		t.Fatal("prepared command does not persist the credential helper")
	}
}

func TestSessionPodUsesGitLabWorkspaceCredentials(t *testing.T) {
	t.Parallel()
	session := testSession("gitlab-session", "codex")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	workspace := &kelos.WorkspaceSpec{
		Repo:      "https://gitlab.example.com/group/repo.git",
		Provider:  kelos.WorkspaceProviderGitLab,
		SecretRef: &kelos.SecretReference{Name: "gitlab-token"},
	}

	statefulSet, _, err := testSessionReconciler(nil, nil).buildSessionStatefulSet(session, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	podSpec := statefulSet.Spec.Template.Spec

	tokenFile := GitLabTokenMountPath + "/" + GitLabTokenSecretKey
	var gitClone *corev1.Container
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == "git-clone" {
			gitClone = &podSpec.InitContainers[i]
		}
	}
	if gitClone == nil {
		t.Fatalf("Session Pod has no git-clone init container: %v", podSpec.InitContainers)
	}
	script := strings.Join(gitClone.Command, " ")
	if !strings.Contains(script, "config credential.username "+gitLabCredentialUsername) {
		t.Errorf("git-clone must pin credential.username to %q for GitLab tokens: %q", gitLabCredentialUsername, script)
	}
	if !strings.Contains(script, tokenFile) {
		t.Errorf("git-clone credential helper must read the GitLab token file %q: %q", tokenFile, script)
	}
	if strings.Contains(script, GitHubTokenMountPath) {
		t.Errorf("git-clone must not reference the GitHub token file: %q", script)
	}

	mainEnv := map[string]string{}
	for _, env := range podSpec.Containers[0].Env {
		mainEnv[env.Name] = env.Value
	}
	if mainEnv["KELOS_GITLAB_TOKEN_FILE"] != tokenFile {
		t.Errorf("KELOS_GITLAB_TOKEN_FILE = %q, want %q", mainEnv["KELOS_GITLAB_TOKEN_FILE"], tokenFile)
	}
	if _, found := mainEnv["KELOS_GITHUB_TOKEN_FILE"]; found {
		t.Error("KELOS_GITHUB_TOKEN_FILE must not be set for a GitLab workspace")
	}
}

func TestSessionPluginConfigMapUsesSessionIdentity(t *testing.T) {
	t.Parallel()
	session := testSession("shared-name", "claude-code")
	agentConfig := &kelos.AgentConfigSpec{Plugins: []kelos.PluginSpec{{
		Name:   "tools",
		Skills: []kelos.SkillDefinition{{Name: "review", Content: "Review changes"}},
	}}}
	reconciler := testSessionReconciler(nil, nil)
	statefulSet, configMap, err := reconciler.buildSessionStatefulSet(session, nil, agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	if configMap == nil {
		t.Fatal("Session plugin ConfigMap was not built")
	}
	if configMap.Name == PluginConfigMapName(session.Name) {
		t.Fatalf("Session plugin ConfigMap reused Task name %q", configMap.Name)
	}
	checksum := statefulSet.Spec.Template.Annotations[sessionPluginChecksumAnnotation]
	if checksum == "" {
		t.Fatal("Session Pod template has no plugin content checksum")
	}
	wantChecksum, err := sessionPluginConfigMapChecksum(configMap)
	if err != nil {
		t.Fatal(err)
	}
	if checksum != wantChecksum {
		t.Fatalf("plugin content checksum = %q, want %q", checksum, wantChecksum)
	}
	for _, volume := range statefulSet.Spec.Template.Spec.Volumes {
		if volume.Name == PluginStagingVolumeName && volume.ConfigMap != nil {
			if volume.ConfigMap.Name != configMap.Name {
				t.Fatalf("plugin volume ConfigMap = %q, want %q", volume.ConfigMap.Name, configMap.Name)
			}
			return
		}
	}
	t.Fatal("Session Pod has no plugin ConfigMap volume")
}

func TestSessionPluginContentChangesPodTemplateChecksum(t *testing.T) {
	t.Parallel()
	session := testSession("plugin-update", "claude-code")
	reconciler := testSessionReconciler(nil, nil)
	build := func(content string) (*appsv1.StatefulSet, *corev1.ConfigMap) {
		t.Helper()
		agentConfig := &kelos.AgentConfigSpec{Plugins: []kelos.PluginSpec{{
			Name:   "tools",
			Skills: []kelos.SkillDefinition{{Name: "review", Content: content}},
		}}}
		statefulSet, configMap, err := reconciler.buildSessionStatefulSet(session, nil, agentConfig)
		if err != nil {
			t.Fatal(err)
		}
		return statefulSet, configMap
	}

	before, beforeConfigMap := build("Review changes")
	after, afterConfigMap := build("Review changes carefully")
	if beforeConfigMap.Name != afterConfigMap.Name {
		t.Fatalf("plugin ConfigMap name changed from %q to %q", beforeConfigMap.Name, afterConfigMap.Name)
	}
	beforeChecksum := before.Spec.Template.Annotations[sessionPluginChecksumAnnotation]
	afterChecksum := after.Spec.Template.Annotations[sessionPluginChecksumAnnotation]
	if beforeChecksum == "" || afterChecksum == "" || beforeChecksum == afterChecksum {
		t.Fatalf("plugin content checksums = %q and %q, want different non-empty values", beforeChecksum, afterChecksum)
	}

	beforeTemplate := before.Spec.Template.DeepCopy()
	afterTemplate := after.Spec.Template.DeepCopy()
	delete(beforeTemplate.Annotations, sessionPluginChecksumAnnotation)
	delete(afterTemplate.Annotations, sessionPluginChecksumAnnotation)
	if !apiequality.Semantic.DeepEqual(beforeTemplate, afterTemplate) {
		t.Fatal("plugin content changed Pod template fields other than its checksum")
	}
}

func TestSessionPluginConfigMapPreservesUnownedMetadata(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := testSession("plugin-metadata", "codex")
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            sessionPluginConfigMapName(session),
			Namespace:       session.Namespace,
			Labels:          map[string]string{"backup.example/enabled": "true"},
			Annotations:     map[string]string{"policy.example/owner": "platform"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))},
		},
		Data:       map[string]string{"p0-s0": "current content"},
		BinaryData: map[string][]byte{"current": []byte("current binary content")},
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: existing.Name, Namespace: existing.Namespace},
		Data:       map[string]string{"p0-s0": "desired content"},
		BinaryData: map[string][]byte{"desired": []byte("desired binary content")},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session, existing).Build()
	reconciler := testSessionReconciler(cl, scheme)
	if err := reconciler.ensureSessionPluginConfigMap(context.Background(), session, desired); err != nil {
		t.Fatal(err)
	}

	var updated corev1.ConfigMap
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(existing), &updated); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.Labels, existing.Labels) {
		t.Fatalf("ConfigMap labels = %#v, want preserved %#v", updated.Labels, existing.Labels)
	}
	if !reflect.DeepEqual(updated.Annotations, existing.Annotations) {
		t.Fatalf("ConfigMap annotations = %#v, want preserved %#v", updated.Annotations, existing.Annotations)
	}
	if !reflect.DeepEqual(updated.Data, desired.Data) || !reflect.DeepEqual(updated.BinaryData, desired.BinaryData) {
		t.Fatalf("ConfigMap content = (%#v, %#v), want (%#v, %#v)", updated.Data, updated.BinaryData, desired.Data, desired.BinaryData)
	}
}

func TestSessionLabelValueBoundsLongNames(t *testing.T) {
	t.Parallel()
	session := testSession(strings.Repeat("a", 80), "codex")
	session.UID = types.UID("6d693cca-eace-4a0c-bf53-f2ea763c9b1f")
	if got := sessionLabelValue(session); got != string(session.UID) {
		t.Fatalf("sessionLabelValue() = %q, want UID %q", got, session.UID)
	}
	session.UID = ""
	if got := sessionLabelValue(session); len(got) > 63 {
		t.Fatalf("fallback label length = %d, want at most 63", len(got))
	}
}

func TestSessionGitHubTokenSecretNameBoundsLongNames(t *testing.T) {
	t.Parallel()
	name := strings.Repeat("a", 253)
	got := sessionGitHubTokenSecretName(name)
	if len(got) > 63 {
		t.Fatalf("token Secret name length = %d, want at most 63", len(got))
	}
	if got != sessionGitHubTokenSecretName(name) {
		t.Fatal("token Secret name is not deterministic")
	}
	if got == sessionGitHubTokenSecretName(strings.Repeat("b", 253)) {
		t.Fatal("different Session names produced the same token Secret name")
	}
}

func TestSessionWorkloadNameBoundsStatefulSetRevisionLabel(t *testing.T) {
	t.Parallel()
	if got := sessionWorkloadName(testSession("chat", "codex")); got != "chat" {
		t.Fatalf("Session workload name = %q, want %q", got, "chat")
	}

	for _, sessionName := range []string{
		"open-actions-workers-issue-comment-bbc08c8a77c3",
		strings.Repeat("a", 253),
	} {
		session := testSession(sessionName, "codex")
		name := sessionWorkloadName(session)
		if len(name) > sessionWorkloadNameMaxLength {
			t.Fatalf("Session workload name length = %d, want at most %d", len(name), sessionWorkloadNameMaxLength)
		}
		if name != sessionWorkloadName(session) {
			t.Fatal("Session workload name is not deterministic")
		}
	}

	if sessionWorkloadName(testSession(strings.Repeat("a", 253), "codex")) ==
		sessionWorkloadName(testSession(strings.Repeat("b", 253), "codex")) {
		t.Fatal("different Session names produced the same workload name")
	}
}

func TestSessionServiceName(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		sessionName string
		want        string
	}{
		{sessionName: "chat", want: "s-chat"},
		{sessionName: "123-chat", want: "s-123-chat"},
		{sessionName: "s-123-chat", want: "s-s-123-chat"},
	} {
		if got := sessionServiceName(testSession(tt.sessionName, "codex")); got != tt.want {
			t.Errorf("Session Service name for %q = %q, want %q", tt.sessionName, got, tt.want)
		}
	}

	longSession := testSession(strings.Repeat("a", 253), "codex")
	if got := sessionServiceName(longSession); len(got) > 63 {
		t.Fatalf("Session Service name length = %d, want at most 63", len(got))
	}
}

func TestSessionReconcilerMapsStatefulSetPod(t *testing.T) {
	t.Parallel()
	reconciler := &SessionReconciler{}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:        "chat-0",
		Namespace:   "default",
		Annotations: map[string]string{sessionNameAnnotation: "chat"},
	}}
	requests := reconciler.findSessionForPod(context.Background(), pod)
	if len(requests) != 1 || requests[0].Name != "chat" || requests[0].Namespace != "default" {
		t.Fatalf("findSessionForPod() = %#v", requests)
	}
	if requests := reconciler.findSessionForPod(context.Background(), &corev1.Pod{}); len(requests) != 0 {
		t.Fatalf("findSessionForPod() returned requests for an unannotated Pod: %#v", requests)
	}
}

func TestSessionReconcilerMapsReferencedConfiguration(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := testSession("matching", "codex")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	session.Spec.Worker.AgentConfigRefs = []kelos.AgentConfigReference{{Name: "agent-config"}}
	other := testSession("other", "codex")
	other.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "other-workspace"}
	other.Spec.Worker.AgentConfigRefs = []kelos.AgentConfigReference{{Name: "other-agent-config"}}
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace},
		Spec: kelos.WorkspaceSpec{
			Repo:      "https://github.com/kelos-dev/kelos.git",
			SecretRef: &kelos.SecretReference{Name: "workspace-secret"},
		},
	}
	otherWorkspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "other-workspace", Namespace: session.Namespace},
		Spec: kelos.WorkspaceSpec{
			Repo:      "https://github.com/kelos-dev/kelos.git",
			SecretRef: &kelos.SecretReference{Name: "other-secret"},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session, other, workspace, otherWorkspace).Build()
	reconciler := &SessionReconciler{Client: cl}

	tests := []struct {
		name string
		obj  client.Object
		mapf func(context.Context, client.Object) []reconcile.Request
	}{
		{
			name: "workspace",
			obj:  &kelos.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace}},
			mapf: reconciler.findSessionsForWorkspace,
		},
		{
			name: "agent config",
			obj:  &kelos.AgentConfig{ObjectMeta: metav1.ObjectMeta{Name: "agent-config", Namespace: session.Namespace}},
			mapf: reconciler.findSessionsForAgentConfig,
		},
		{
			name: "workspace secret",
			obj:  &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "workspace-secret", Namespace: session.Namespace}},
			mapf: reconciler.findSessionsForSecret,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := tt.mapf(context.Background(), tt.obj)
			if len(requests) != 1 || requests[0].NamespacedName != client.ObjectKeyFromObject(session) {
				t.Fatalf("mapped requests = %#v, want Session %s", requests, session.Name)
			}
		})
	}
}

func TestSessionGitHubTokenReuseTracksMintInputs(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	tokenServer := func(token string, requests *int) *httptest.Server {
		t.Helper()
		return httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			*requests++
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(map[string]string{
				"token":      token,
				"expires_at": expiresAt.Format(time.RFC3339),
			})
		}))
	}
	firstRequests := 0
	firstServer := tokenServer("ghs_first", &firstRequests)
	defer firstServer.Close()
	secondRequests := 0
	secondServer := tokenServer("ghs_second", &secondRequests)
	defer secondServer.Close()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := testSession("token-inputs", "codex")
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-app",
			Namespace: session.Namespace,
			UID:       types.UID("github-app-uid"),
		},
		Data: testGitHubAppSecretData(t),
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build()
	reconciler := testSessionReconciler(cl, scheme)
	reconciler.TokenClient = &githubapp.TokenClient{BaseURL: firstServer.URL, Client: firstServer.Client()}
	workspace := &kelos.WorkspaceSpec{SecretRef: &kelos.SecretReference{Name: source.Name}}

	if _, err := reconciler.resolveSessionGitHubAppToken(context.Background(), session, workspace, tokenRefreshMargin); err != nil {
		t.Fatal(err)
	}
	var tokenSecret corev1.Secret
	tokenKey := client.ObjectKey{Namespace: session.Namespace, Name: sessionGitHubTokenSecretName(session.Name)}
	if err := cl.Get(context.Background(), tokenKey, &tokenSecret); err != nil {
		t.Fatal(err)
	}
	firstFingerprint := tokenSecret.Annotations[sessionTokenFingerprintAnnotation]
	if firstFingerprint == "" || string(tokenSecret.Data[GitHubTokenSecretKey]) != "ghs_first" {
		t.Fatalf("first token Secret = %#v", tokenSecret)
	}
	if _, err := reconciler.resolveSessionGitHubAppToken(context.Background(), session, workspace, tokenRefreshMargin); err != nil {
		t.Fatal(err)
	}
	if firstRequests != 1 {
		t.Fatalf("first token endpoint requests = %d, want 1 after reuse", firstRequests)
	}

	reconciler.TokenClient = &githubapp.TokenClient{BaseURL: secondServer.URL, Client: secondServer.Client()}
	if _, err := reconciler.resolveSessionGitHubAppToken(context.Background(), session, workspace, tokenRefreshMargin); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), tokenKey, &tokenSecret); err != nil {
		t.Fatal(err)
	}
	secondFingerprint := tokenSecret.Annotations[sessionTokenFingerprintAnnotation]
	if secondFingerprint == "" || secondFingerprint == firstFingerprint {
		t.Fatalf("token fingerprint = %q, want different from %q after API endpoint change", secondFingerprint, firstFingerprint)
	}
	if string(tokenSecret.Data[GitHubTokenSecretKey]) != "ghs_second" || secondRequests != 1 {
		t.Fatalf("second token = %q, endpoint requests = %d", tokenSecret.Data[GitHubTokenSecretKey], secondRequests)
	}

	var updatedSource corev1.Secret
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(source), &updatedSource); err != nil {
		t.Fatal(err)
	}
	updatedSource.Data["installationID"] = []byte("67891")
	if err := cl.Update(context.Background(), &updatedSource); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.resolveSessionGitHubAppToken(context.Background(), session, workspace, tokenRefreshMargin); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), tokenKey, &tokenSecret); err != nil {
		t.Fatal(err)
	}
	if tokenSecret.Annotations[sessionTokenFingerprintAnnotation] == secondFingerprint {
		t.Fatal("token fingerprint did not change after source Secret update")
	}
	if secondRequests != 2 {
		t.Fatalf("second token endpoint requests = %d, want 2 after source Secret update", secondRequests)
	}

	expirationCases := []struct {
		name  string
		value string
	}{
		{name: "malformed", value: "not-a-timestamp"},
		{name: "expired", value: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		{name: "near expiry", value: time.Now().Add(tokenRefreshMargin / 2).UTC().Format(time.RFC3339)},
	}
	for _, tt := range expirationCases {
		t.Run(tt.name, func(t *testing.T) {
			if err := cl.Get(context.Background(), tokenKey, &tokenSecret); err != nil {
				t.Fatal(err)
			}
			tokenSecret.Annotations[tokenExpiresAtAnnotation] = tt.value
			tokenSecret.Data[GitHubTokenSecretKey] = []byte("ghs_cached")
			if err := cl.Update(context.Background(), &tokenSecret); err != nil {
				t.Fatal(err)
			}
			requestsBefore := secondRequests
			if _, err := reconciler.resolveSessionGitHubAppToken(context.Background(), session, workspace, tokenRefreshMargin); err != nil {
				t.Fatal(err)
			}
			if secondRequests != requestsBefore+1 {
				t.Fatalf("token endpoint requests = %d, want %d", secondRequests, requestsBefore+1)
			}
			if err := cl.Get(context.Background(), tokenKey, &tokenSecret); err != nil {
				t.Fatal(err)
			}
			if got := string(tokenSecret.Data[GitHubTokenSecretKey]); got != "ghs_second" {
				t.Fatalf("token = %q, want re-minted token", got)
			}
			remintedExpiry, err := time.Parse(time.RFC3339, tokenSecret.Annotations[tokenExpiresAtAnnotation])
			if err != nil || !time.Now().Before(remintedExpiry.Add(-tokenRefreshMargin)) {
				t.Fatalf("re-minted token expiration = %q, err = %v", tokenSecret.Annotations[tokenExpiresAtAnnotation], err)
			}
		})
	}
}

func TestSessionReconcilerRecreatesMissingGitHubTokenSecret(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	tokenRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenRequests++
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"token":      "ghs_recreated",
			"expires_at": expiresAt.Format(time.RFC3339),
		})
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	_ = kelos.AddToScheme(scheme)
	session := testSession("recover-token", "codex")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace},
		Spec: kelos.WorkspaceSpec{
			Repo:      "https://github.com/kelos-dev/kelos.git",
			SecretRef: &kelos.SecretReference{Name: "github-app"},
		},
	}
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-app", Namespace: session.Namespace},
		Data:       testGitHubAppSecretData(t),
	}
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Status.UpdateRevision = "desired-revision"
	statefulSet.Status.ObservedGeneration = statefulSet.Generation
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSet.Name + "-0",
			Namespace: session.Namespace,
			UID:       types.UID("pod-uid"),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet")),
			},
			Annotations: map[string]string{sessionNameAnnotation: session.Name},
			Labels:      map[string]string{appsv1.StatefulSetRevisionLabel: statefulSet.Status.UpdateRevision},
		},
		Spec: *statefulSet.Spec.Template.Spec.DeepCopy(),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	tokenVolume := corev1.Volume{
		Name: GitHubTokenVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: sessionGitHubTokenSecretName(session.Name),
		}},
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, tokenVolume)
	statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, tokenVolume)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, workspace, source, statefulSet, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	reconciler.TokenClient = &githubapp.TokenClient{BaseURL: server.URL, Client: server.Client()}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want positive refresh interval", result.RequeueAfter)
	}
	var recreated corev1.Secret
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: sessionGitHubTokenSecretName(session.Name)}, &recreated); err != nil {
		t.Fatalf("getting recreated token Secret: %v", err)
	}
	if got := string(recreated.Data[GitHubTokenSecretKey]); got != "ghs_recreated" {
		t.Fatalf("recreated token = %q, want ghs_recreated", got)
	}
	if !metav1.IsControlledBy(&recreated, session) {
		t.Fatal("recreated token Secret is not controlled by the Session")
	}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}); err != nil {
		t.Fatalf("Reconcile() with current token error = %v", err)
	}
	if tokenRequests != 1 {
		t.Fatalf("GitHub App token requests = %d, want 1", tokenRequests)
	}

	if err := cl.Delete(context.Background(), &recreated); err != nil {
		t.Fatal(err)
	}
	serviceAccount := sessionRuntimeServiceAccount(session)
	if err := cl.Delete(context.Background(), serviceAccount); err != nil {
		t.Fatal(err)
	}
	service := buildSessionService(session)
	if err := cl.Delete(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	var resetStatefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &resetStatefulSet); err != nil {
		t.Fatal(err)
	}
	resetStatefulSet.Spec.Replicas = ptr.To(int32(0))
	if err := cl.Update(context.Background(), &resetStatefulSet); err != nil {
		t.Fatal(err)
	}
	state, err := sessionreset.EncodeState(sessionreset.State{RequestID: "reset-1", Phase: sessionreset.PhaseStarting})
	if err != nil {
		t.Fatal(err)
	}
	var resetting kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &resetting); err != nil {
		t.Fatal(err)
	}
	resetting.Annotations = map[string]string{
		sessionreset.RequestAnnotation: "reset-1",
		sessionreset.StateAnnotation:   state,
	}
	if err := cl.Update(context.Background(), &resetting); err != nil {
		t.Fatal(err)
	}

	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() reset start error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("Reconcile() reset start requeueAfter = %s, want positive duration", result.RequeueAfter)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(serviceAccount), &corev1.ServiceAccount{}); err != nil {
		t.Fatalf("getting recreated runtime ServiceAccount: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(service), &corev1.Service{}); err != nil {
		t.Fatalf("getting recreated governing Service: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: sessionGitHubTokenSecretName(session.Name)}, &recreated); err != nil {
		t.Fatalf("getting token Secret recreated during reset: %v", err)
	}
	if got := string(recreated.Data[GitHubTokenSecretKey]); got != "ghs_recreated" {
		t.Fatalf("reset token = %q, want ghs_recreated", got)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &resetStatefulSet); err != nil {
		t.Fatal(err)
	}
	if resetStatefulSet.Spec.Replicas == nil || *resetStatefulSet.Spec.Replicas != 1 {
		t.Fatalf("StatefulSet replicas after prerequisite repair = %v, want 1", resetStatefulSet.Spec.Replicas)
	}
}

func TestSessionPhaseForPodReportsContainerFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status corev1.PodStatus
		reason string
	}{
		{
			name: "runtime crash loop",
			status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "agent",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off restarting failed container"}},
			}}},
			reason: "CrashLoopBackOff",
		},
		{
			name: "runtime image pull failure",
			status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
				Name:  "kelos-session-runtime",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
			}}},
			reason: "ImagePullBackOff",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phase, message, reason := sessionPhaseForPod(&corev1.Pod{Status: tt.status})
			if phase != kelos.SessionPhaseFailed || reason != tt.reason {
				t.Fatalf("sessionPhaseForPod() = (%q, %q, %q), want Failed with reason %q", phase, message, reason, tt.reason)
			}
			if !strings.Contains(message, tt.reason) {
				t.Fatalf("failure message %q does not contain %q", message, tt.reason)
			}
		})
	}
}

func TestSessionWithoutVolumeClaimUsesEmptyDir(t *testing.T) {
	t.Parallel()
	session := testSession("ephemeral", "codex")
	session.Spec.VolumeClaimTemplate = nil
	statefulSet, _, err := testSessionReconciler(nil, nil).buildSessionStatefulSet(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(statefulSet.Spec.VolumeClaimTemplates) != 0 {
		t.Fatalf("volumeClaimTemplates = %#v, want none", statefulSet.Spec.VolumeClaimTemplates)
	}
	if statefulSet.Spec.PersistentVolumeClaimRetentionPolicy != nil {
		t.Fatalf("persistentVolumeClaimRetentionPolicy = %#v, want nil", statefulSet.Spec.PersistentVolumeClaimRetentionPolicy)
	}
	workspaceVolume := findVolume(statefulSet.Spec.Template.Spec.Volumes, WorkspaceVolumeName)
	if workspaceVolume == nil || workspaceVolume.EmptyDir == nil {
		t.Fatalf("workspace volume = %#v, want emptyDir", workspaceVolume)
	}
}

func TestSessionReconcilerWaitsForWorkspace(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = kelos.AddToScheme(scheme)
	session := testSession("waiting", "claude-code")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "missing"}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kelos.Session{}).WithObjects(session).Build()
	reconciler := testSessionReconciler(cl, scheme)
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("Reconcile() did not requeue for missing Workspace")
	}
	var updated kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.SessionPhasePending || updated.Status.Message != "Waiting for Workspace \"missing\"" {
		t.Fatalf("Session status = %#v", updated.Status)
	}
}

func TestSessionReconcilerSuspendsBeforeResolvingWorkspace(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := testSession("suspend-with-missing-workspace", "codex")
	session.Spec.Suspend = ptr.To(true)
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "missing"}
	session.Status.Phase = kelos.SessionPhaseReady
	session.Status.PodName = "suspend-with-missing-workspace-0"
	session.Status.PodUID = types.UID("stale-pod-uid")
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.Replicas = ptr.To(int32(1))
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("Reconcile() did not requeue for missing Workspace")
	}
	var updated appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0", updated.Spec.Replicas)
	}
	var updatedSession kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if updatedSession.Status.Phase != kelos.SessionPhaseSuspended {
		t.Fatalf("Session phase = %q, want %q", updatedSession.Status.Phase, kelos.SessionPhaseSuspended)
	}
	if !strings.Contains(updatedSession.Status.Message, `Waiting for Workspace "missing"`) {
		t.Fatalf("Session message = %q, want missing Workspace detail", updatedSession.Status.Message)
	}
	if updatedSession.Status.PodName != "" || updatedSession.Status.PodUID != "" {
		t.Fatalf("Session retained stale Pod identity: name=%q uid=%q", updatedSession.Status.PodName, updatedSession.Status.PodUID)
	}
}

func testSession(name, provider string) *kelos.Session {
	return &kelos.Session{
		TypeMeta: metav1.TypeMeta{APIVersion: kelos.GroupVersion.String(), Kind: "Session"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name + "-uid"),
		},
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

func testSessionStatefulSet(session *kelos.Session) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            sessionWorkloadName(session),
			Namespace:       session.Namespace,
			UID:             types.UID(sessionWorkloadName(session) + "-uid"),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))},
		},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: kelos.AgentContainerName}},
					InitContainers: []corev1.Container{{
						Name:  sessionRuntimeContainerName,
						Image: "runtime:test",
					}},
				},
			},
		},
	}
}

func testSessionReconciler(cl client.Client, scheme *runtime.Scheme) *SessionReconciler {
	builder := NewJobBuilder()
	builder.ClaudeCodeImage = "claude:test"
	builder.CodexImage = "codex:test"
	builder.OpenCodeImage = "opencode:test"
	return &SessionReconciler{
		Client:              cl,
		Scheme:              scheme,
		JobBuilder:          builder,
		SessionRuntimeImage: "runtime:test",
		Recorder:            record.NewFakeRecorder(10),
	}
}

func testGitHubAppSecretData(t *testing.T) map[string][]byte {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"appID":          []byte("12345"),
		"installationID": []byte("67890"),
		"privateKey":     pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}),
	}
}

func TestSessionIdleExpired(t *testing.T) {
	t.Parallel()
	now := time.Now()
	activeCondition := func(status metav1.ConditionStatus, transition time.Time) metav1.Condition {
		return metav1.Condition{
			Type:               kelos.SessionConditionActive,
			Status:             status,
			Reason:             "Test",
			LastTransitionTime: metav1.NewTime(transition),
		}
	}
	newSession := func(ttl *int32, mutate func(*kelos.Session)) *kelos.Session {
		s := testSession("idle", "codex")
		s.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
		if ttl != nil {
			s.Spec.IdlePolicy = &kelos.SessionIdlePolicy{DeleteAfterSeconds: ttl}
		}
		if mutate != nil {
			mutate(s)
		}
		return s
	}
	idleFor := func(d time.Duration) func(*kelos.Session) {
		return func(s *kelos.Session) {
			at := metav1.NewTime(now.Add(-d))
			s.Status.LastActivityTime = &at
			s.Status.Conditions = []metav1.Condition{activeCondition(metav1.ConditionFalse, now.Add(-d))}
		}
	}

	tests := []struct {
		name        string
		session     *kelos.Session
		wantExpired bool
		wantRequeue bool
	}{
		{
			name:    "no TTL is never reaped",
			session: newSession(nil, idleFor(time.Hour)),
		},
		{
			name: "active turn is not idle",
			session: newSession(ptr.To(int32(60)), func(s *kelos.Session) {
				s.Status.Conditions = []metav1.Condition{activeCondition(metav1.ConditionTrue, now.Add(-time.Hour))}
			}),
		},
		{
			name: "unknown activity is not idle",
			session: newSession(ptr.To(int32(60)), func(s *kelos.Session) {
				s.Status.Conditions = []metav1.Condition{activeCondition(metav1.ConditionUnknown, now.Add(-time.Hour))}
			}),
		},
		{
			name:    "missing active condition is not idle",
			session: newSession(ptr.To(int32(60)), nil),
		},
		{
			name:        "idle beyond TTL is reaped",
			session:     newSession(ptr.To(int32(60)), idleFor(2*time.Minute)),
			wantExpired: true,
		},
		{
			// A replacement Pod re-reporting Active=False stamps a fresh Active
			// transition, but idleness is measured from lastActivityTime (which
			// Pod replacement preserves), so the idle clock is not reset.
			name: "recent active transition does not reset idle clock",
			session: newSession(ptr.To(int32(60)), func(s *kelos.Session) {
				old := metav1.NewTime(now.Add(-10 * time.Minute))
				s.Status.LastActivityTime = &old
				s.Status.Conditions = []metav1.Condition{activeCondition(metav1.ConditionFalse, now)}
			}),
			wantExpired: true,
		},
		{
			name:        "idle within TTL requeues",
			session:     newSession(ptr.To(int32(600)), idleFor(time.Minute)),
			wantRequeue: true,
		},
		{
			name:        "zero TTL reaps as soon as idle",
			session:     newSession(ptr.To(int32(0)), idleFor(time.Second)),
			wantExpired: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expired, remaining := sessionIdleExpired(tt.session)
			if expired != tt.wantExpired {
				t.Fatalf("expired = %t, want %t", expired, tt.wantExpired)
			}
			if tt.wantRequeue && remaining <= 0 {
				t.Fatalf("remaining = %s, want > 0", remaining)
			}
			if !tt.wantRequeue && !tt.wantExpired && remaining != 0 {
				t.Fatalf("remaining = %s, want 0", remaining)
			}
		})
	}
}

// newReadyIdleSessionFixture builds a Ready Session whose runtime has reported
// Active=False since idleSince, backed by a matching StatefulSet and ready Pod,
// so that Reconcile validates the idle condition against the current Pod before
// evaluating its idle policy.
func newReadyIdleSessionFixture(t *testing.T, policy *kelos.SessionIdlePolicy, idleSince time.Time) (client.Client, *SessionReconciler, ctrl.Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("idle-chat", "codex")
	session.CreationTimestamp = metav1.NewTime(idleSince.Add(-time.Hour))
	session.Spec.IdlePolicy = policy
	activity := metav1.NewTime(idleSince)
	session.Status.PodUID = types.UID("pod-uid")
	session.Status.LastActivityTime = &activity
	session.Status.Conditions = []metav1.Condition{{
		Type:               kelos.SessionConditionActive,
		Status:             metav1.ConditionFalse,
		Reason:             "Idle",
		LastTransitionTime: activity,
	}}

	statefulSet, _, err := testSessionReconciler(nil, nil).buildSessionStatefulSet(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	statefulSet.UID = types.UID(statefulSet.Name + "-uid")
	statefulSet.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session"))}
	statefulSet.Status.UpdateRevision = "desired-revision"
	statefulSet.Status.ObservedGeneration = statefulSet.Generation

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            statefulSet.Name + "-0",
			Namespace:       session.Namespace,
			UID:             types.UID("pod-uid"),
			Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: statefulSet.Status.UpdateRevision},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		},
		Spec: *statefulSet.Spec.Template.Spec.DeepCopy(),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	return cl, reconciler, request
}

func newIdleSuspendedSession(name string, policy *kelos.SessionIdlePolicy, idleSince time.Time) *kelos.Session {
	session := testSession(name, "codex")
	session.CreationTimestamp = metav1.NewTime(idleSince.Add(-time.Hour))
	session.Spec.IdlePolicy = policy
	activity := metav1.NewTime(idleSince)
	session.Status.Phase = kelos.SessionPhaseSuspended
	session.Status.LastActivityTime = &activity
	apiMeta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{
		Type:   kelos.SessionConditionReady,
		Status: metav1.ConditionFalse,
		Reason: sessionsuspend.IdlePolicyReason,
	})
	return session
}

func TestSessionReconcileDrainsThenReapsIdleSession(t *testing.T) {
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(60))},
		time.Now().Add(-10*time.Minute))
	recorder := record.NewFakeRecorder(10)
	reconciler.Recorder = recorder

	// The first reconcile requests a drain and waits; it must not delete the
	// Session before the runtime confirms no turn is in flight.
	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("Reconcile() drain-request error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want a positive drain requeue", result.RequeueAfter)
	}
	var draining kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &draining); err != nil {
		t.Fatalf("Session was deleted before the runtime drained: %v", err)
	}
	encodedRequest := draining.Annotations[sessionupdate.IdleDrainRequestAnnotation]
	if encodedRequest == "" {
		t.Fatal("expected an idle drain request annotation")
	}
	drainRequest, err := sessionupdate.Decode(encodedRequest)
	if err != nil {
		t.Fatalf("decoding installed idle drain request: %v", err)
	}
	if drainRequest.PodUID != types.UID("pod-uid") {
		t.Fatalf("idle drain request pod UID = %q, want %q", drainRequest.PodUID, "pod-uid")
	}

	// The runtime acknowledges it has drained and stopped accepting turns.
	report, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    types.UID("pod-uid"),
		Phase:     sessionupdate.PhaseDrained,
	})
	if err != nil {
		t.Fatal(err)
	}
	patched := draining.DeepCopy()
	patched.Annotations[sessionupdate.IdleDrainReportAnnotation] = report
	if err := cl.Patch(context.Background(), patched, client.MergeFrom(&draining)); err != nil {
		t.Fatalf("patching idle drain report: %v", err)
	}

	// The second reconcile deletes now that the runtime is confirmed idle.
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() reap error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Session get after reap = %v, want NotFound", err)
	}

	reaped := false
	for {
		select {
		case event := <-recorder.Events:
			if strings.Contains(event, "SessionIdleReaped") {
				reaped = true
			}
			continue
		default:
		}
		break
	}
	if !reaped {
		t.Fatal("expected a SessionIdleReaped event to be recorded")
	}
}

func TestSessionReconcileDrainsSuspendsAndResumesIdleSession(t *testing.T) {
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{SuspendAfterSeconds: ptr.To(int32(60))},
		time.Now().Add(-10*time.Minute))

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() drain-request error = %v", err)
	}
	var session kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	drainRequest, err := sessionupdate.Decode(session.Annotations[sessionupdate.IdleDrainRequestAnnotation])
	if err != nil {
		t.Fatal(err)
	}
	report, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    drainRequest.PodUID,
		Phase:     sessionupdate.PhaseDrained,
	})
	if err != nil {
		t.Fatal(err)
	}
	patched := session.DeepCopy()
	patched.Annotations[sessionupdate.IdleDrainReportAnnotation] = report
	if err := cl.Patch(context.Background(), patched, client.MergeFrom(&session)); err != nil {
		t.Fatal(err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() suspend error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.IsIdlePolicySuspended(&session) {
		t.Fatalf("Session status = %#v, want idle suspension", session.Status)
	}
	if ptr.Deref(session.Spec.Suspend, false) {
		t.Fatal("idle suspension changed Session.spec.suspend")
	}
	var statefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(&session)}, &statefulSet); err != nil {
		t.Fatal(err)
	}
	if ptr.Deref(statefulSet.Spec.Replicas, 1) != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0", statefulSet.Spec.Replicas)
	}

	if _, requested, err := sessionsuspend.RequestResume(context.Background(), cl, request.NamespacedName); err != nil {
		t.Fatal(err)
	} else if !requested {
		t.Fatal("RequestResume() requested = false, want true")
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() observe-request error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() clear-drain error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() begin-resume error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() start-runtime error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	resumedAt := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	session.Status.LastActivityTime = &resumedAt
	apiMeta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{
		Type:               kelos.SessionConditionActive,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: session.Generation,
		Reason:             "Idle",
		Message:            "Session runtime is idle",
		LastTransitionTime: resumedAt,
	})
	if err := cl.Status().Update(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() observed-runtime error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.ResumeRequested(&session) {
		t.Fatal("resume request was cleared before a client connected")
	}
	if session.Status.LastActivityTime == nil || !session.Status.LastActivityTime.Equal(&resumedAt) {
		t.Fatalf("last activity time = %v, want %v", session.Status.LastActivityTime, resumedAt)
	}
	if acknowledged, err := sessionsuspend.AcknowledgeResume(context.Background(), cl, request.NamespacedName, session.Annotations[sessionsuspend.ResumeRequestAnnotation]); err != nil {
		t.Fatal(err)
	} else if !acknowledged {
		t.Fatal("AcknowledgeResume() acknowledged = false, want true")
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() observe-acknowledgement error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	acknowledgedAt, acknowledged := sessionsuspend.ResumeAcknowledgementTime(&session)
	if !acknowledged {
		t.Fatalf("resume acknowledgement was not recorded: %#v", session.Annotations)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() after-resume error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.ResumeRequested(&session) {
		t.Fatalf("resume request was cleared before the connection grace elapsed: %#v", session.Annotations)
	}
	if session.Status.LastActivityTime == nil || session.Status.LastActivityTime.Before(&metav1.Time{Time: acknowledgedAt.Truncate(time.Second)}) {
		t.Fatalf("last activity time = %v, want at or after acknowledgement %v", session.Status.LastActivityTime, acknowledgedAt)
	}
	if session.Status.Phase != kelos.SessionPhaseReady {
		t.Fatalf("Session phase = %q, want %q", session.Status.Phase, kelos.SessionPhaseReady)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(&statefulSet), &statefulSet); err != nil {
		t.Fatal(err)
	}
	if ptr.Deref(statefulSet.Spec.Replicas, 0) != 1 {
		t.Fatalf("StatefulSet replicas = %v, want 1", statefulSet.Spec.Replicas)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() post-resume error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if session.Annotations[sessionupdate.IdleDrainRequestAnnotation] != "" {
		t.Fatalf("idle drain restarted immediately after resume: %#v", session.Annotations)
	}
}

func TestSessionReconcileExpiresUnacknowledgedIdleResumeRequest(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := newIdleSuspendedSession(
		"expired-idle-resume",
		&kelos.SessionIdlePolicy{
			SuspendAfterSeconds: ptr.To(int32(30)),
			DeleteAfterSeconds:  ptr.To(int32(60)),
		},
		time.Now().Add(-time.Hour),
	)
	session.Status.Phase = kelos.SessionPhasePending
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[sessionsuspend.ResumeRequestAnnotation] = "expired-request"
	session.Annotations[sessionsuspend.ResumeRequestTimeAnnotation] = time.Now().Add(-idleResumeRequestTimeout - time.Minute).UTC().Format(time.RFC3339Nano)
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.Replicas = ptr.To(int32(1))
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() expire request error = %v", err)
	}
	var current kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &current); err != nil {
		t.Fatal(err)
	}
	if sessionsuspend.ResumeRequested(&current) {
		t.Fatalf("expired resume request was not cleared: %#v", current.Annotations)
	}
	if !sessionsuspend.IsIdlePolicySuspended(&current) {
		t.Fatalf("Session status = %#v, want idle suspension", current.Status)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() reap error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Session get after expired resume request = %v, want NotFound", err)
	}
}

func TestSessionReconcileDrainsBeforeExpiringIdleResumeRequest(t *testing.T) {
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{
			SuspendAfterSeconds: ptr.To(int32(30)),
			DeleteAfterSeconds:  ptr.To(int32(60)),
		},
		time.Now().Add(-time.Hour))

	var session kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[sessionsuspend.ResumeRequestAnnotation] = "expired-request"
	session.Annotations[sessionsuspend.ResumeRequestTimeAnnotation] = time.Now().Add(-idleResumeRequestTimeout - time.Minute).UTC().Format(time.RFC3339Nano)
	if err := cl.Update(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() drain-request error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.ResumeRequested(&session) {
		t.Fatal("resume request was cleared before the runtime drained")
	}
	drainRequest, err := sessionupdate.Decode(session.Annotations[sessionupdate.IdleDrainRequestAnnotation])
	if err != nil {
		t.Fatal(err)
	}
	var statefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(&session)}, &statefulSet); err != nil {
		t.Fatal(err)
	}
	if ptr.Deref(statefulSet.Spec.Replicas, 0) != 1 {
		t.Fatalf("StatefulSet replicas = %v, want 1 while draining", statefulSet.Spec.Replicas)
	}

	report, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    drainRequest.PodUID,
		Phase:     sessionupdate.PhaseDrained,
	})
	if err != nil {
		t.Fatal(err)
	}
	patched := session.DeepCopy()
	patched.Annotations[sessionupdate.IdleDrainReportAnnotation] = report
	if err := cl.Patch(context.Background(), patched, client.MergeFrom(&session)); err != nil {
		t.Fatal(err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() expire request error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if sessionsuspend.ResumeRequested(&session) {
		t.Fatalf("drained resume request was not cleared: %#v", session.Annotations)
	}
	if !sessionsuspend.IsIdlePolicySuspended(&session) {
		t.Fatalf("Session status = %#v, want idle suspension", session.Status)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(&statefulSet), &statefulSet); err != nil {
		t.Fatal(err)
	}
	if ptr.Deref(statefulSet.Spec.Replicas, 1) != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0 after drain", statefulSet.Spec.Replicas)
	}
}

func TestSessionReconcileDrainsSurvivingPodBeforeExpiringIdleResumeRequest(t *testing.T) {
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{SuspendAfterSeconds: ptr.To(int32(30))},
		time.Now().Add(-time.Hour))

	var session kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[sessionsuspend.ResumeRequestAnnotation] = "expired-request"
	session.Annotations[sessionsuspend.ResumeRequestTimeAnnotation] = time.Now().Add(-idleResumeRequestTimeout - time.Minute).UTC().Format(time.RFC3339Nano)
	if err := cl.Update(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	workloadKey := client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(&session)}
	var statefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), workloadKey, &statefulSet); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: workloadKey.Name + "-0"}, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[sessionNameAnnotation] = session.Name
	if err := cl.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete(context.Background(), &statefulSet); err != nil {
		t.Fatal(err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() drain-request error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.ResumeRequested(&session) {
		t.Fatal("resume request was cleared before the surviving Pod drained")
	}
	drainRequest, err := sessionupdate.Decode(session.Annotations[sessionupdate.IdleDrainRequestAnnotation])
	if err != nil {
		t.Fatal(err)
	}

	report, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    drainRequest.PodUID,
		Phase:     sessionupdate.PhaseDrained,
	})
	if err != nil {
		t.Fatal(err)
	}
	patched := session.DeepCopy()
	patched.Annotations[sessionupdate.IdleDrainReportAnnotation] = report
	if err := cl.Patch(context.Background(), patched, client.MergeFrom(&session)); err != nil {
		t.Fatal(err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() expire request error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if sessionsuspend.ResumeRequested(&session) {
		t.Fatalf("drained resume request was not cleared: %#v", session.Annotations)
	}
	if !sessionsuspend.IsIdlePolicySuspended(&session) {
		t.Fatalf("Session status = %#v, want idle suspension", session.Status)
	}
}

func TestSessionReconcileDoesNotResuspendDuringResumeProtection(t *testing.T) {
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{SuspendAfterSeconds: ptr.To(int32(0))},
		time.Now().Add(-time.Hour))

	var session kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[sessionsuspend.ResumeRequestAnnotation] = "resume-request"
	session.Annotations[sessionsuspend.ResumeRequestTimeAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := cl.Update(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > idleResumeRequestTimeout {
		t.Fatalf("Reconcile() requeueAfter = %s, want resume request expiry", result.RequeueAfter)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if session.Annotations[sessionupdate.IdleDrainRequestAnnotation] != "" {
		t.Fatalf("idle drain started before resume acknowledgement: %#v", session.Annotations)
	}
	requestValue := session.Annotations[sessionsuspend.ResumeRequestAnnotation]
	if acknowledged, err := sessionsuspend.AcknowledgeResume(context.Background(), cl, request.NamespacedName, requestValue); err != nil {
		t.Fatal(err)
	} else if !acknowledged {
		t.Fatal("AcknowledgeResume() acknowledged = false, want true")
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() observe-acknowledgement error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() acknowledgement error = %v", err)
	}
	result, err = reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > idleResumeAcknowledgementGrace {
		t.Fatalf("Reconcile() requeueAfter = %s, want acknowledgement grace", result.RequeueAfter)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.ResumeRequested(&session) {
		t.Fatalf("resume request was cleared before the connection grace elapsed: %#v", session.Annotations)
	}
	if session.Annotations[sessionupdate.IdleDrainRequestAnnotation] != "" {
		t.Fatalf("idle drain started during the connection grace: %#v", session.Annotations)
	}
}

func TestSessionReconcileDeletesIdleSuspendedSessionAtDeleteDeadline(t *testing.T) {
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{
			SuspendAfterSeconds: ptr.To(int32(60)),
			DeleteAfterSeconds:  ptr.To(int32(3600)),
		},
		time.Now().Add(-10*time.Minute))

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var session kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	drainRequest, err := sessionupdate.Decode(session.Annotations[sessionupdate.IdleDrainRequestAnnotation])
	if err != nil {
		t.Fatal(err)
	}
	report, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    drainRequest.PodUID,
		Phase:     sessionupdate.PhaseDrained,
	})
	if err != nil {
		t.Fatal(err)
	}
	patched := session.DeepCopy()
	patched.Annotations[sessionupdate.IdleDrainReportAnnotation] = report
	if err := cl.Patch(context.Background(), patched, client.MergeFrom(&session)); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &session); err != nil {
		t.Fatal(err)
	}
	oldActivity := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	session.Status.LastActivityTime = &oldActivity
	if err := cl.Status().Update(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Session get after delete deadline = %v, want NotFound", err)
	}
}

func TestSessionReconcileDeletesIdleSuspendedSessionWithMissingDependency(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := newIdleSuspendedSession(
		"idle-suspended-missing-workspace",
		&kelos.SessionIdlePolicy{
			SuspendAfterSeconds: ptr.To(int32(30)),
			DeleteAfterSeconds:  ptr.To(int32(60)),
		},
		time.Now().Add(-10*time.Minute),
	)
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "missing"}
	statefulSet := testSessionStatefulSet(session)
	statefulSet.Spec.Replicas = ptr.To(int32(0))
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &appsv1.StatefulSet{}).
		WithObjects(session, statefulSet).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Session get after delete deadline = %v, want NotFound", err)
	}
}

func TestSessionReconcilePreservesIdleSuspensionAcrossStatefulSetRecreation(t *testing.T) {
	for _, test := range []struct {
		name        string
		terminating bool
	}{
		{name: "missing"},
		{name: "terminating", terminating: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}

			session := newIdleSuspendedSession(
				"idle-suspended-statefulset-"+test.name,
				&kelos.SessionIdlePolicy{
					SuspendAfterSeconds: ptr.To(int32(60)),
					DeleteAfterSeconds:  ptr.To(int32(3600)),
				},
				time.Now().Add(-10*time.Minute),
			)
			objects := []client.Object{session}
			if test.terminating {
				statefulSet := testSessionStatefulSet(session)
				statefulSet.Spec.Replicas = ptr.To(int32(0))
				now := metav1.Now()
				statefulSet.DeletionTimestamp = &now
				statefulSet.Finalizers = []string{"test.kelos.dev/terminating"}
				objects = append(objects, statefulSet)
			}
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kelos.Session{}, &appsv1.StatefulSet{}).
				WithObjects(objects...).
				Build()
			reconciler := testSessionReconciler(cl, scheme)
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

			if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			var updated kelos.Session
			if err := cl.Get(context.Background(), request.NamespacedName, &updated); err != nil {
				t.Fatal(err)
			}
			if !sessionsuspend.IsIdlePolicySuspended(&updated) {
				t.Fatalf("Session status = %#v, want idle suspension", updated.Status)
			}

			if !test.terminating {
				var statefulSet appsv1.StatefulSet
				if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session)}, &statefulSet); err != nil {
					t.Fatal(err)
				}
				if ptr.Deref(statefulSet.Spec.Replicas, 1) != 0 {
					t.Fatalf("StatefulSet replicas = %v, want 0", statefulSet.Spec.Replicas)
				}
			}
		})
	}
}

func TestSessionReconcilePreservesIdleSuspensionWhileRecreationWaitsForDependency(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := newIdleSuspendedSession(
		"idle-suspended-missing-workspace",
		&kelos.SessionIdlePolicy{
			SuspendAfterSeconds: ptr.To(int32(60)),
			DeleteAfterSeconds:  ptr.To(int32(3600)),
		},
		time.Now().Add(-10*time.Minute),
	)
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &appsv1.StatefulSet{}).
		WithObjects(session).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() waiting error = %v", err)
	}
	var updated kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.IsIdlePolicySuspended(&updated) {
		t.Fatalf("Session status = %#v, want idle suspension while waiting for Workspace", updated.Status)
	}

	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace},
		Spec:       kelos.WorkspaceSpec{Repo: "https://github.com/kelos-dev/kelos.git"},
	}
	if err := cl.Create(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() recreation error = %v", err)
	}
	var statefulSet appsv1.StatefulSet
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session)}, &statefulSet); err != nil {
		t.Fatal(err)
	}
	if ptr.Deref(statefulSet.Spec.Replicas, 1) != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0", statefulSet.Spec.Replicas)
	}
}

func TestSessionReconcileDoesNotReapIdleSessionUntilDrained(t *testing.T) {
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(60))},
		time.Now().Add(-10*time.Minute))

	// Runtime reports it is still draining (a turn is in flight).
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() drain-request error = %v", err)
	}
	var draining kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &draining); err != nil {
		t.Fatal(err)
	}
	drainRequest, err := sessionupdate.Decode(draining.Annotations[sessionupdate.IdleDrainRequestAnnotation])
	if err != nil {
		t.Fatalf("decoding installed idle drain request: %v", err)
	}
	report, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: drainRequest.ID,
		PodUID:    types.UID("pod-uid"),
		Phase:     sessionupdate.PhaseDraining,
	})
	if err != nil {
		t.Fatal(err)
	}
	patched := draining.DeepCopy()
	patched.Annotations[sessionupdate.IdleDrainReportAnnotation] = report
	if err := cl.Patch(context.Background(), patched, client.MergeFrom(&draining)); err != nil {
		t.Fatal(err)
	}

	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want a positive requeue while draining", result.RequeueAfter)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); err != nil {
		t.Fatalf("Session was deleted while a turn was still in flight: %v", err)
	}
}

func TestSessionReconcileClearsIdleDrainWhenActivityResumes(t *testing.T) {
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(60))},
		time.Now().Add(-10*time.Minute))

	// The first reconcile requests a drain for the expired idle Session.
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() drain-request error = %v", err)
	}
	var draining kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &draining); err != nil {
		t.Fatal(err)
	}
	if draining.Annotations[sessionupdate.IdleDrainRequestAnnotation] == "" {
		t.Fatal("expected an idle drain request after the first reconcile")
	}

	// A turn is accepted before the runtime drains, so the Session is no longer
	// idle: the runtime reports an active turn.
	active := draining.DeepCopy()
	apiMeta.SetStatusCondition(&active.Status.Conditions, metav1.Condition{
		Type:   kelos.SessionConditionActive,
		Status: metav1.ConditionTrue,
		Reason: "TurnActive",
	})
	if err := cl.Status().Update(context.Background(), active); err != nil {
		t.Fatalf("updating Session status: %v", err)
	}

	// The next reconcile must cancel the drain and keep the Session so the user
	// is not locked out.
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() clear error = %v", err)
	}
	var got kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &got); err != nil {
		t.Fatalf("Session was deleted after activity resumed: %v", err)
	}
	if got.Annotations[sessionupdate.IdleDrainRequestAnnotation] != "" {
		t.Fatalf("idle drain request annotation = %q, want cleared", got.Annotations[sessionupdate.IdleDrainRequestAnnotation])
	}
}

func TestSessionReconcileRequeuesIdleSessionBeforeDeadline(t *testing.T) {
	const deadline = 3600
	cl, reconciler, request := newReadyIdleSessionFixture(t,
		&kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(deadline))},
		time.Now().Add(-time.Minute))

	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > deadline*time.Second {
		t.Fatalf("Reconcile() requeueAfter = %s, want within (0, %ds]", result.RequeueAfter, deadline)
	}
	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); err != nil {
		t.Fatalf("Session was deleted or unreadable before its idle deadline: %v", err)
	}
}

func TestSessionReconcileReapsIdleSessionWithMissingWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("idle-orphan", "codex")
	// Without a persistent workspace there is no journal to recover, so a missing
	// workload is reaped directly.
	session.Spec.VolumeClaimTemplate = nil
	session.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(60))}
	idleSince := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	session.Status.PodUID = types.UID("pod-uid")
	session.Status.LastActivityTime = &idleSince
	session.Status.Conditions = []metav1.Condition{{
		Type:               kelos.SessionConditionActive,
		Status:             metav1.ConditionFalse,
		Reason:             "Idle",
		LastTransitionTime: idleSince,
	}}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Session get after reap = %v, want NotFound", err)
	}
	var statefulSet appsv1.StatefulSet
	key := client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session)}
	if err := cl.Get(context.Background(), key, &statefulSet); !apierrors.IsNotFound(err) {
		t.Fatalf("StatefulSet was recreated instead of reaping the idle Session: %v", err)
	}
}

// TestSessionReconcileRecoversPersistentWorkspaceWithMissingWorkload verifies
// that an idle-expired Session with a persistent workspace whose StatefulSet and
// Pod are both gone is not deleted from its possibly stale Active=False status.
// Its journal may hold unpublished activity, so the runtime is recreated to
// recover it rather than reaping the Session and its workspace.
func TestSessionReconcileRecoversPersistentWorkspaceWithMissingWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("idle-persistent", "codex")
	session.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(60))}
	idleSince := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	session.Status.PodUID = types.UID("pod-uid")
	session.Status.LastActivityTime = &idleSince
	session.Status.Conditions = []metav1.Condition{{
		Type:               kelos.SessionConditionActive,
		Status:             metav1.ConditionFalse,
		Reason:             "Idle",
		LastTransitionTime: idleSince,
	}}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); err != nil {
		t.Fatalf("Session with a persistent workspace was reaped without recovering its journal: %v", err)
	}
	var statefulSet appsv1.StatefulSet
	key := client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session)}
	if err := cl.Get(context.Background(), key, &statefulSet); err != nil {
		t.Fatalf("StatefulSet was not recreated to recover the Session runtime: %v", err)
	}
}

// TestSessionReconcileDrainsSurvivingPodWhenWorkloadMissing verifies that a
// missing StatefulSet does not short-circuit the drain handshake when its
// ordinal Pod is still running (for example, orphaned by garbage collection):
// the controller must request a drain rather than delete the Session outright.
func TestSessionReconcileDrainsSurvivingPodWhenWorkloadMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("idle-orphan-pod", "codex")
	session.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(60))}
	idleSince := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	session.Status.PodUID = types.UID("pod-uid")
	session.Status.LastActivityTime = &idleSince
	session.Status.Conditions = []metav1.Condition{{
		Type:               kelos.SessionConditionActive,
		Status:             metav1.ConditionFalse,
		Reason:             "Idle",
		LastTransitionTime: idleSince,
	}}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        sessionWorkloadName(session) + "-0",
			Namespace:   session.Namespace,
			UID:         types.UID("pod-uid"),
			Annotations: map[string]string{sessionNameAnnotation: session.Name},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want a positive drain requeue", result.RequeueAfter)
	}

	var got kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &got); err != nil {
		t.Fatalf("Session was deleted before draining a surviving Pod: %v", err)
	}
	if got.Annotations[sessionupdate.IdleDrainRequestAnnotation] == "" {
		t.Fatal("expected an idle drain request for the surviving Pod")
	}

	var statefulSet appsv1.StatefulSet
	key := client.ObjectKey{Namespace: session.Namespace, Name: sessionWorkloadName(session)}
	if err := cl.Get(context.Background(), key, &statefulSet); !apierrors.IsNotFound(err) {
		t.Fatalf("StatefulSet was recreated instead of draining the surviving Pod: %v", err)
	}
}

// TestSessionReconcileReapsIdleSessionWithTerminalPodWhenWorkloadMissing
// verifies that when the StatefulSet is missing and its ordinal Pod is terminal
// (Succeeded or Failed), a Session without a persistent workspace is reaped
// directly instead of starting a drain handshake that no live runtime could ever
// acknowledge.
func TestSessionReconcileReapsIdleSessionWithTerminalPodWhenWorkloadMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("idle-terminal-pod", "codex")
	// Without a persistent workspace there is no journal to recover.
	session.Spec.VolumeClaimTemplate = nil
	session.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(60))}
	idleSince := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	session.Status.PodUID = types.UID("pod-uid")
	session.Status.LastActivityTime = &idleSince
	session.Status.Conditions = []metav1.Condition{{
		Type:               kelos.SessionConditionActive,
		Status:             metav1.ConditionFalse,
		Reason:             "Idle",
		LastTransitionTime: idleSince,
	}}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        sessionWorkloadName(session) + "-0",
			Namespace:   session.Namespace,
			UID:         types.UID("pod-uid"),
			Annotations: map[string]string{sessionNameAnnotation: session.Name},
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Session was not reaped despite a terminal Pod (get = %v, want NotFound)", err)
	}
}

// TestSessionReconcileRecoversPersistentWorkspaceWithTerminalPod verifies that
// when the StatefulSet is missing and the ordinal Pod is terminal, a Session with
// a persistent workspace deletes the leftover terminal Pod and recovers its
// runtime rather than deleting the Session and its workspace from a possibly
// stale Active=False status.
func TestSessionReconcileRecoversPersistentWorkspaceWithTerminalPod(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	session := testSession("idle-persistent-terminal", "codex")
	session.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	session.Spec.IdlePolicy = &kelos.SessionIdlePolicy{DeleteAfterSeconds: ptr.To(int32(60))}
	idleSince := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	session.Status.PodUID = types.UID("pod-uid")
	session.Status.LastActivityTime = &idleSince
	session.Status.Conditions = []metav1.Condition{{
		Type:               kelos.SessionConditionActive,
		Status:             metav1.ConditionFalse,
		Reason:             "Idle",
		LastTransitionTime: idleSince,
	}}

	podName := sessionWorkloadName(session) + "-0"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
			Namespace:   session.Namespace,
			UID:         types.UID("pod-uid"),
			Annotations: map[string]string{sessionNameAnnotation: session.Name},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}, &appsv1.StatefulSet{}).
		WithObjects(session, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}

	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want a positive requeue after clearing the terminal Pod", result.RequeueAfter)
	}

	if err := cl.Get(context.Background(), request.NamespacedName, &kelos.Session{}); err != nil {
		t.Fatalf("Session with a persistent workspace was reaped despite a terminal Pod: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: podName}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("terminal Pod was not cleared before recovery (get = %v, want NotFound)", err)
	}
}

// TestSessionIdleDrainRequestIsUniquePerEpisode verifies that each installed
// drain episode yields a distinct request ID while the ID stays stable across
// reconciles of the same episode, so a Drained report left over from a previous
// idle period cannot be mistaken for an acknowledgement of the current request.
func TestSessionIdleDrainRequestIsUniquePerEpisode(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-uid")}}

	// Distinct episodes (for example, a drain re-requested within the same second
	// after a cancelled one) get distinct IDs, even for the same Pod.
	requestOne := newSessionIdleDrainRequest(pod)
	requestTwo := newSessionIdleDrainRequest(pod)
	if requestOne.ID == requestTwo.ID {
		t.Fatal("expected a distinct idle-drain request ID for each drain episode")
	}
	if requestOne.PodUID != pod.UID || requestTwo.PodUID != pod.UID {
		t.Fatalf("idle-drain request pod UID = %q/%q, want %q", requestOne.PodUID, requestTwo.PodUID, pod.UID)
	}

	// An already-installed request is reused across reconciles so its ID is stable.
	encoded, err := sessionupdate.Encode(requestOne)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("idle", "codex")
	session.Annotations = map[string]string{sessionupdate.IdleDrainRequestAnnotation: encoded}
	reused, ok := installedSessionIdleDrainRequest(session, pod)
	if !ok || reused.ID != requestOne.ID {
		t.Fatalf("installedSessionIdleDrainRequest() = %#v, %v, want %#v reused", reused, ok, requestOne)
	}

	// A request installed for a different Pod is not reused.
	otherPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("other-pod")}}
	if _, ok := installedSessionIdleDrainRequest(session, otherPod); ok {
		t.Fatal("installedSessionIdleDrainRequest() reused a request installed for a different Pod")
	}

	// A stale Drained report from a previous episode does not complete a fresh one.
	report, err := sessionupdate.EncodeReport(sessionupdate.Report{
		RequestID: requestOne.ID,
		PodUID:    pod.UID,
		Phase:     sessionupdate.PhaseDrained,
	})
	if err != nil {
		t.Fatal(err)
	}
	session.Annotations[sessionupdate.IdleDrainReportAnnotation] = report
	done, err := sessionIdleDrainComplete(session, requestTwo, pod)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("a stale Drained report from a previous episode must not complete the current drain")
	}
}

// TestSetSessionIdleDrainRequestClearsStaleReport verifies that installing a
// drain request drops any report from a previous idle period so a stale Drained
// acknowledgement cannot satisfy the new request before the runtime observes it.
func TestSetSessionIdleDrainRequestClearsStaleReport(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, kelos.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	session := testSession("idle-clear-report", "codex")
	session.Annotations = map[string]string{sessionupdate.IdleDrainReportAnnotation: "stale-report"}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	reconciler := testSessionReconciler(cl, scheme)

	if err := reconciler.setSessionIdleDrainRequest(context.Background(), session, "encoded-request"); err != nil {
		t.Fatalf("setSessionIdleDrainRequest() error = %v", err)
	}

	var got kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[sessionupdate.IdleDrainRequestAnnotation] != "encoded-request" {
		t.Fatalf("idle drain request annotation = %q, want %q", got.Annotations[sessionupdate.IdleDrainRequestAnnotation], "encoded-request")
	}
	if got.Annotations[sessionupdate.IdleDrainReportAnnotation] != "" {
		t.Fatalf("idle drain report annotation = %q, want cleared", got.Annotations[sessionupdate.IdleDrainReportAnnotation])
	}
}

func findVolume(volumes []corev1.Volume, name string) *corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}
	return nil
}
