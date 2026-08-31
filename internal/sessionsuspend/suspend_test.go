package sessionsuspend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

type conflictOnceClient struct {
	client.Client
	patches int
}

func (c *conflictOnceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.patches++
	if c.patches == 1 {
		return apierrors.NewConflict(
			schema.GroupResource{Group: "kelos.dev", Resource: "sessions"},
			obj.GetName(),
			errors.New("conflict"),
		)
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestIsIdlePolicySuspended(t *testing.T) {
	session := idleSuspendedSession()
	if !IsIdlePolicySuspended(session) {
		t.Fatal("IsIdlePolicySuspended() = false, want true")
	}

	session.Spec.Suspend = new(bool)
	*session.Spec.Suspend = true
	if IsIdlePolicySuspended(session) {
		t.Fatal("IsIdlePolicySuspended() = true for manually suspended Session")
	}
}

func TestRequestResumeSetsSingleWakeRequest(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	key := client.ObjectKeyFromObject(session)

	updated, requested, err := RequestResume(context.Background(), cl, key)
	if err != nil {
		t.Fatal(err)
	}
	if !requested || !ResumeRequested(updated) {
		t.Fatalf("RequestResume() = requested %t annotations %#v", requested, updated.Annotations)
	}
	if _, err := uuid.Parse(updated.Annotations[ResumeRequestAnnotation]); err != nil {
		t.Fatalf("resume request = %q, want UUID: %v", updated.Annotations[ResumeRequestAnnotation], err)
	}
	if requestedAt, ok := ResumeRequestTime(updated); ok {
		t.Fatalf("ResumeRequestTime() = %v, true before controller observation", requestedAt)
	}

	updated, requested, err = RequestResume(context.Background(), cl, key)
	if err != nil {
		t.Fatal(err)
	}
	if requested || !ResumeRequested(updated) {
		t.Fatalf("repeated RequestResume() = requested %t annotations %#v", requested, updated.Annotations)
	}
}

func TestAcknowledgeResumeRecordsConnection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	requestValue := "request-id"
	session.Annotations = map[string]string{ResumeRequestAnnotation: requestValue}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	key := client.ObjectKeyFromObject(session)

	acknowledged, err := AcknowledgeResume(context.Background(), cl, key, requestValue)
	if err != nil {
		t.Fatal(err)
	}
	if !acknowledged {
		t.Fatal("AcknowledgeResume() acknowledged = false, want true")
	}
	var updated kelos.Session
	if err := cl.Get(context.Background(), key, &updated); err != nil {
		t.Fatal(err)
	}
	if !ResumeRequested(&updated) || !ResumeAcknowledged(&updated) {
		t.Fatalf("resume request was cleared before the controller observed the acknowledgement: %#v", updated.Annotations)
	}
	if acknowledgedAt, ok := ResumeAcknowledgementTime(&updated); ok {
		t.Fatalf("ResumeAcknowledgementTime() = %v, true before controller observation", acknowledgedAt)
	}

	acknowledged, err = AcknowledgeResume(context.Background(), cl, key, requestValue)
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged {
		t.Fatal("repeated AcknowledgeResume() acknowledged = true, want false")
	}
}

func TestAcknowledgeResumeDoesNotAcknowledgeNewerRequest(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	newRequest := "new-request"
	session.Annotations = map[string]string{ResumeRequestAnnotation: newRequest}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	key := client.ObjectKeyFromObject(session)

	acknowledged, err := AcknowledgeResume(context.Background(), cl, key, "old-request")
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged {
		t.Fatal("AcknowledgeResume() acknowledged = true for stale request, want false")
	}
	var updated kelos.Session
	if err := cl.Get(context.Background(), key, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[ResumeRequestAnnotation] != newRequest {
		t.Fatalf("resume request = %q, want %q", updated.Annotations[ResumeRequestAnnotation], newRequest)
	}
}

func TestRequestResumeIgnoresManualSuspension(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	session.Spec.Suspend = new(bool)
	*session.Spec.Suspend = true
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()

	updated, requested, err := RequestResume(context.Background(), cl, client.ObjectKeyFromObject(session))
	if err != nil {
		t.Fatal(err)
	}
	if requested || ResumeRequested(updated) {
		t.Fatalf("RequestResume() = requested %t annotations %#v", requested, updated.Annotations)
	}
}

func TestRequestResumeRetriesPatchConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	cl := &conflictOnceClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build(),
	}

	_, requested, err := RequestResume(context.Background(), cl, client.ObjectKeyFromObject(session))
	if err != nil {
		t.Fatal(err)
	}
	if !requested {
		t.Fatal("RequestResume() requested = false, want true")
	}
	if cl.patches != 2 {
		t.Fatalf("Patch() called %d times, want 2", cl.patches)
	}
}

func TestRecordConsoleActivityRefreshesLease(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := idleSuspendedSession()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	key := client.ObjectKeyFromObject(session)

	before := time.Now().UTC()
	if err := RecordConsoleActivity(context.Background(), cl, key); err != nil {
		t.Fatal(err)
	}
	var updated kelos.Session
	if err := cl.Get(context.Background(), key, &updated); err != nil {
		t.Fatal(err)
	}
	activityTime, ok := ConsoleActivityTime(&updated)
	if !ok || activityTime.Before(before) || activityTime.After(time.Now().UTC()) {
		t.Fatalf("ConsoleActivityTime() = %v, %t, want a current timestamp", activityTime, ok)
	}
	if remaining := ConsoleActivityLeaseRemaining(&updated, activityTime); remaining != ConsoleActivityLeaseDuration {
		t.Fatalf("ConsoleActivityLeaseRemaining() = %s, want %s", remaining, ConsoleActivityLeaseDuration)
	}
	if remaining := ConsoleActivityLeaseRemaining(&updated, activityTime.Add(ConsoleActivityLeaseDuration+time.Second)); remaining != 0 {
		t.Fatalf("expired ConsoleActivityLeaseRemaining() = %s, want 0", remaining)
	}
}

func idleSuspendedSession() *kelos.Session {
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "default"},
		Status: kelos.SessionStatus{
			Phase: kelos.SessionPhaseSuspended,
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionReady,
				Status: metav1.ConditionFalse,
				Reason: IdlePolicyReason,
			}},
		},
	}
}
