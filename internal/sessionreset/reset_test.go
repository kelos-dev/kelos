package sessionreset

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

func TestRequestRetriesPatchConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &kelos.Session{}
	session.Name = "chat"
	session.Namespace = "default"
	cl := &conflictOnceClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build(),
	}
	key := client.ObjectKeyFromObject(session)

	updated, requested, err := Request(context.Background(), cl, key, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if !requested || updated.Annotations[RequestAnnotation] != "request-1" {
		t.Fatalf("Request() = requested %t annotations %#v", requested, updated.Annotations)
	}
	if cl.patches != 2 {
		t.Fatalf("Patch() called %d times, want 2", cl.patches)
	}
}
