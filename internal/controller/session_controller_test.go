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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

func TestSessionReconcilerUpdatesStatefulSetRuntime(t *testing.T) {
	tests := []struct {
		name           string
		configuredPull corev1.PullPolicy
		wantPull       corev1.PullPolicy
	}{
		{name: "configured pull policy", configuredPull: corev1.PullIfNotPresent, wantPull: corev1.PullIfNotPresent},
		{name: "default pull policy", wantPull: corev1.PullAlways},
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
			statefulSet.Spec.Template.Spec.InitContainers[0].Image = "runtime:old"
			statefulSet.Spec.Template.Spec.InitContainers[0].ImagePullPolicy = corev1.PullAlways
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
	if err == nil || !strings.Contains(err.Error(), `PersistentVolumeClaim "workspace-session-chat-0" is controlled by ConfigMap "other-owner"`) {
		t.Fatalf("ensureSessionWorkspaceClaimOwnership() error = %v", err)
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
	if statefulSet.Spec.ServiceName != workloadKey.Name {
		t.Fatalf("StatefulSet serviceName = %q, want %q", statefulSet.Spec.ServiceName, workloadKey.Name)
	}
	accessName := sessionRuntimeAccessName(session)
	if statefulSet.Spec.Template.Spec.ServiceAccountName != accessName {
		t.Fatalf("Session serviceAccountName = %q, want %q", statefulSet.Spec.Template.Spec.ServiceAccountName, accessName)
	}
	var service corev1.Service
	if err := cl.Get(context.Background(), workloadKey, &service); err != nil {
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
	if len(podSpec.Containers) == 0 || len(podSpec.Containers[0].Command) != 1 || podSpec.Containers[0].Command[0] != sessionRuntimeBinary {
		t.Fatalf("agent command = %v, want %q", podSpec.Containers[0].Command, sessionRuntimeBinary)
	}
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
			if env.ValueFrom == nil || env.ValueFrom.FieldRef == nil || env.ValueFrom.FieldRef.FieldPath != "metadata.uid" {
				t.Fatalf("KELOS_SESSION_POD_UID = %#v", env)
			}
			sawPodUID = true
		}
	}
	if len(wantRuntimeEnv) != 0 || !sawPodUID {
		t.Fatalf("Session runtime environment is missing %v", wantRuntimeEnv)
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            statefulSet.Name + "-0",
			Namespace:       session.Namespace,
			Labels:          statefulSet.Spec.Template.Labels,
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
	if len(updated.Status.Conditions) != 1 || updated.Status.Conditions[0].Type != sessionReadyCondition || updated.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("Session conditions = %#v", updated.Status.Conditions)
	}
}

func TestUpdateSessionStatusClearsStaleWorkspaceStatus(t *testing.T) {
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
			session.Status.Branch = "feature/status"
			session.Status.PullRequest = &kelos.SessionPullRequest{
				URL:   "https://github.com/kelos-dev/kelos/pull/42",
				State: kelos.SessionPullRequestStateOpen,
			}
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
			cleared := updated.Status.Branch == "" && updated.Status.PullRequest == nil
			if cleared != tt.wantClear {
				t.Fatalf("workspace status cleared = %t, want %t: %#v", cleared, tt.wantClear, updated.Status)
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
			if err := prepareSessionWorkspaceInit(containers); err != nil {
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

func TestSessionWorkloadNameBoundsLongNames(t *testing.T) {
	t.Parallel()
	session := testSession(strings.Repeat("a", 253), "codex")
	name := sessionWorkloadName(session)
	if len(name) > 63 {
		t.Fatalf("Session workload name length = %d, want at most 63", len(name))
	}
	if name != sessionWorkloadName(session) {
		t.Fatal("Session workload name is not deterministic")
	}
}

func TestSessionReconcilerMapsStatefulSetPod(t *testing.T) {
	t.Parallel()
	reconciler := &SessionReconciler{}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:        "session-chat-0",
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

func TestSessionReconcilerRecreatesMissingGitHubTokenSecret(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
		Data: map[string][]byte{
			"appID":          []byte("12345"),
			"installationID": []byte("67890"),
			"privateKey":     keyPEM,
		},
	}
	statefulSet := testSessionStatefulSet(session)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSet.Name + "-0",
			Namespace: session.Namespace,
			UID:       types.UID("pod-uid"),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(statefulSet, appsv1.SchemeGroupVersion.WithKind("StatefulSet")),
			},
			Annotations: map[string]string{sessionNameAnnotation: session.Name},
		},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name: GitHubTokenVolumeName,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: sessionGitHubTokenSecretName(session.Name),
			}},
		}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
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

func findVolume(volumes []corev1.Volume, name string) *corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}
	return nil
}
