package sessionturn

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionsuspend"
)

func readyScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	return scheme
}

func testSession(phase kelos.SessionPhase, podName string) *kelos.Session {
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "thread-session", Namespace: "default"},
		Spec: kelos.SessionSpec{
			Worker: kelos.WorkerSpec{Type: "claude-code"},
		},
		Status: kelos.SessionStatus{Phase: phase, PodName: podName},
	}
}

func sessionKey() client.ObjectKey {
	return client.ObjectKey{Namespace: "default", Name: "thread-session"}
}

func TestWaitReadyReturnsPodName(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).
		WithObjects(testSession(kelos.SessionPhaseReady, "thread-session-0")).Build()

	podName, err := WaitReady(context.Background(), cl, sessionKey(), time.Millisecond)
	if err != nil {
		t.Fatalf("WaitReady returned an error: %v", err)
	}
	if podName != "thread-session-0" {
		t.Errorf("podName = %q, want thread-session-0", podName)
	}
}

func TestWaitReadyPollsUntilReady(t *testing.T) {
	var calls atomic.Int32
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).
		WithObjects(testSession(kelos.SessionPhasePending, "")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				session, ok := obj.(*kelos.Session)
				if !ok {
					return nil
				}
				if calls.Add(1) >= 3 {
					session.Status.Phase = kelos.SessionPhaseReady
					session.Status.PodName = "thread-session-0"
				}
				return nil
			},
		}).Build()

	podName, err := WaitReady(context.Background(), cl, sessionKey(), time.Millisecond)
	if err != nil {
		t.Fatalf("WaitReady returned an error: %v", err)
	}
	if podName != "thread-session-0" {
		t.Errorf("podName = %q, want thread-session-0", podName)
	}
	if calls.Load() < 3 {
		t.Errorf("polled %d times, want at least 3", calls.Load())
	}
}

func TestWaitReadyFailsOnFailedSession(t *testing.T) {
	session := testSession(kelos.SessionPhaseFailed, "")
	session.Status.Message = "workspace clone failed"
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).WithObjects(session).Build()

	_, err := WaitReady(context.Background(), cl, sessionKey(), time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady returned no error for a failed Session")
	}
	if !strings.Contains(err.Error(), "workspace clone failed") {
		t.Errorf("error = %v, want the Session status message", err)
	}
}

func TestWaitReadyFailsOnUserSuspendedSession(t *testing.T) {
	session := testSession(kelos.SessionPhaseSuspended, "")
	suspend := true
	session.Spec.Suspend = &suspend
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).WithObjects(session).Build()

	_, err := WaitReady(context.Background(), cl, sessionKey(), time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "is suspended") {
		t.Fatalf("error = %v, want a suspended Session to be reported, not resumed", err)
	}
}

func TestWaitReadyRequestsResumeForIdleSuspendedSession(t *testing.T) {
	session := testSession(kelos.SessionPhaseSuspended, "")
	session.Status.Conditions = []metav1.Condition{{
		Type:               kelos.SessionConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             kelos.SessionReasonIdlePolicyTriggered,
		LastTransitionTime: metav1.Now(),
	}}
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).WithObjects(session).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := WaitReady(ctx, cl, sessionKey(), time.Millisecond); err == nil {
		t.Fatal("WaitReady returned before the Session became ready")
	}

	var current kelos.Session
	if err := cl.Get(context.Background(), sessionKey(), &current); err != nil {
		t.Fatalf("getting Session: %v", err)
	}
	if !sessionsuspend.ResumeRequested(&current) {
		t.Error("WaitReady did not request a resume for an idle-suspended Session")
	}
}

func TestAcknowledgeResumeIsANoOpWithoutARequest(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).
		WithObjects(testSession(kelos.SessionPhaseReady, "thread-session-0")).Build()

	if err := AcknowledgeResume(context.Background(), cl, sessionKey()); err != nil {
		t.Fatalf("AcknowledgeResume returned an error: %v", err)
	}
	var current kelos.Session
	if err := cl.Get(context.Background(), sessionKey(), &current); err != nil {
		t.Fatalf("getting Session: %v", err)
	}
	if _, ok := current.Annotations[sessionsuspend.ResumeAcknowledgementAnnotation]; ok {
		t.Error("AcknowledgeResume recorded an acknowledgement with no pending request")
	}
}

func TestAcknowledgeResumeRecordsPendingRequest(t *testing.T) {
	session := testSession(kelos.SessionPhaseReady, "thread-session-0")
	session.Annotations = map[string]string{sessionsuspend.ResumeRequestAnnotation: "request-1"}
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).WithObjects(session).Build()

	if err := AcknowledgeResume(context.Background(), cl, sessionKey()); err != nil {
		t.Fatalf("AcknowledgeResume returned an error: %v", err)
	}
	var current kelos.Session
	if err := cl.Get(context.Background(), sessionKey(), &current); err != nil {
		t.Fatalf("getting Session: %v", err)
	}
	if current.Annotations[sessionsuspend.ResumeAcknowledgementAnnotation] != "request-1" {
		t.Errorf("acknowledgement = %q, want request-1", current.Annotations[sessionsuspend.ResumeAcknowledgementAnnotation])
	}
}

func TestWaitReadyWaitsForASessionTheCacheHasNotSeen(t *testing.T) {
	// The bridge creates a Session and then reads it back through an informer
	// cache, so the first Get can legitimately miss. That must not fail the
	// thread's first message.
	var calls atomic.Int32
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).
		WithObjects(testSession(kelos.SessionPhaseReady, "thread-session-0")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kelos.Session); ok && calls.Add(1) <= 2 {
					return apierrors.NewNotFound(
						schema.GroupResource{Group: "kelos.dev", Resource: "sessions"}, key.Name)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()

	podName, err := WaitReady(context.Background(), cl, sessionKey(), time.Millisecond)
	if err != nil {
		t.Fatalf("WaitReady failed on a cache miss: %v", err)
	}
	if podName != "thread-session-0" {
		t.Errorf("podName = %q, want thread-session-0", podName)
	}
}

func TestWaitReadyGivesUpOnAMissingSessionAtTheDeadline(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := WaitReady(ctx, cl, sessionKey(), time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady returned no error for a Session that never appeared")
	}
	if !strings.Contains(err.Error(), "to appear") {
		t.Errorf("error = %v, want it to say the Session never appeared", err)
	}
}
