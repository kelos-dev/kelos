package sessionruntime

import (
	"context"
	"reflect"
	"testing"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	clientfake "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/fake"
)

func TestSessionStatusPublisherPatchesLiveSession(t *testing.T) {
	const sessionName = "chat"
	podUID := types.UID("live-pod")
	clientset := clientfake.NewSimpleClientset(&kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: "default"},
		Status: kelos.SessionStatus{
			Phase:  kelos.SessionPhaseReady,
			PodUID: podUID,
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionReady,
				Status: metav1.ConditionTrue,
				Reason: "PodReady",
			}},
		},
	})
	publisher, err := NewSessionStatusPublisher(clientset.ApiV1alpha2().Sessions("default"), sessionName, podUID)
	if err != nil {
		t.Fatal(err)
	}
	want := ObservedSessionStatus{
		Active: true,
		WorkspaceStatus: &WorkspaceStatus{
			Branch: "feature/session-status",
			PullRequest: &kelos.SessionPullRequest{
				URL:   "https://github.com/kelos-dev/kelos/pull/42",
				State: kelos.SessionPullRequestStateOpen,
			},
		},
	}
	if err := publisher(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := clientset.ApiV1alpha2().Sessions("default").Get(context.Background(), sessionName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	active := apiMeta.FindStatusCondition(got.Status.Conditions, kelos.SessionConditionActive)
	ready := apiMeta.FindStatusCondition(got.Status.Conditions, kelos.SessionConditionReady)
	if active == nil || active.Status != metav1.ConditionTrue || active.Reason != "TurnActive" || ready == nil || ready.Status != metav1.ConditionTrue || got.Status.Branch != want.WorkspaceStatus.Branch || !reflect.DeepEqual(got.Status.PullRequest, want.WorkspaceStatus.PullRequest) {
		t.Fatalf("Session runtime status = %#v, want %#v", got.Status, want)
	}
	if got.Status.Phase != kelos.SessionPhaseReady || got.Status.PodUID != podUID {
		t.Fatalf("publisher changed controller-owned status: %#v", got.Status)
	}

	want.Active = false
	if err := publisher(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err = clientset.ApiV1alpha2().Sessions("default").Get(context.Background(), sessionName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	active = apiMeta.FindStatusCondition(got.Status.Conditions, kelos.SessionConditionActive)
	if active == nil || active.Status != metav1.ConditionFalse || active.Reason != "Idle" {
		t.Fatalf("Session Active condition = %#v, want False with reason Idle", active)
	}
}

func TestSessionStatusPublisherPreservesWorkspaceStatusWhenUnobserved(t *testing.T) {
	const sessionName = "chat"
	podUID := types.UID("live-pod")
	pullRequest := &kelos.SessionPullRequest{
		URL:   "https://github.com/kelos-dev/kelos/pull/42",
		State: kelos.SessionPullRequestStateOpen,
	}
	clientset := clientfake.NewSimpleClientset(&kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: "default"},
		Status: kelos.SessionStatus{
			Phase:       kelos.SessionPhaseReady,
			PodUID:      podUID,
			Branch:      "feature/session-status",
			PullRequest: pullRequest,
		},
	})
	publisher, err := NewSessionStatusPublisher(clientset.ApiV1alpha2().Sessions("default"), sessionName, podUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher(context.Background(), ObservedSessionStatus{Active: true}); err != nil {
		t.Fatal(err)
	}

	got, err := clientset.ApiV1alpha2().Sessions("default").Get(context.Background(), sessionName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	active := apiMeta.FindStatusCondition(got.Status.Conditions, kelos.SessionConditionActive)
	if active == nil || active.Status != metav1.ConditionTrue {
		t.Fatalf("Session Active condition = %#v, want True", active)
	}
	if got.Status.Branch != "feature/session-status" || !reflect.DeepEqual(got.Status.PullRequest, pullRequest) {
		t.Fatalf("Session workspace status = %#v, want existing status preserved", got.Status)
	}
}

func TestSessionStatusPublisherRequiresLiveReadyPod(t *testing.T) {
	tests := []struct {
		name          string
		phase         kelos.SessionPhase
		currentPodUID types.UID
	}{
		{name: "replaced Pod", phase: kelos.SessionPhaseReady, currentPodUID: types.UID("replacement-pod")},
		{name: "pending Session", phase: kelos.SessionPhasePending, currentPodUID: types.UID("reporting-pod")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const sessionName = "chat"
			clientset := clientfake.NewSimpleClientset(&kelos.Session{
				ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: "default"},
				Status: kelos.SessionStatus{
					Phase:  tt.phase,
					PodUID: tt.currentPodUID,
				},
			})
			publisher, err := NewSessionStatusPublisher(clientset.ApiV1alpha2().Sessions("default"), sessionName, types.UID("reporting-pod"))
			if err != nil {
				t.Fatal(err)
			}
			if err := publisher(context.Background(), ObservedSessionStatus{
				Active:          true,
				WorkspaceStatus: &WorkspaceStatus{Branch: "stale"},
			}); err == nil {
				t.Fatal("publisher error = nil, want status patch rejection")
			}
			got, err := clientset.ApiV1alpha2().Sessions("default").Get(context.Background(), sessionName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if apiMeta.FindStatusCondition(got.Status.Conditions, kelos.SessionConditionActive) != nil || got.Status.Branch != "" || got.Status.PullRequest != nil {
				t.Fatalf("rejected publisher changed Session runtime status: %#v", got.Status)
			}
		})
	}
}
