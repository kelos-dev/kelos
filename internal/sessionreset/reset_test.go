package sessionreset

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestStateRoundTrip(t *testing.T) {
	want := State{RequestID: "request-1", Phase: PhaseDeletingStorage}
	value, err := EncodeState(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeState(value)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DecodeState(EncodeState()) = %#v, want %#v", got, want)
	}
}

func TestRequestMarksSessionOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &kelos.Session{}
	session.Name = "chat"
	session.Namespace = "default"
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	key := client.ObjectKeyFromObject(session)

	updated, requested, err := Request(context.Background(), cl, key, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if !requested || updated.Annotations[RequestAnnotation] != "request-1" {
		t.Fatalf("Request() = requested %t annotations %#v", requested, updated.Annotations)
	}

	updated, requested, err = Request(context.Background(), cl, key, "request-2")
	if err != nil {
		t.Fatal(err)
	}
	if requested || updated.Annotations[RequestAnnotation] != "request-1" {
		t.Fatalf("second Request() = requested %t annotations %#v", requested, updated.Annotations)
	}
}
