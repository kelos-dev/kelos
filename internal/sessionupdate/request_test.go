package sessionupdate

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestRequestRoundTrip(t *testing.T) {
	request := NewRequest(types.UID("pod-uid"), "desired-revision")
	encoded, err := Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, request) {
		t.Fatalf("decoded request = %#v, want %#v", decoded, request)
	}
	if got := NewRequest(types.UID("other-pod"), "desired-revision"); got.ID == request.ID {
		t.Fatalf("request IDs match across Pod UIDs: %q", got.ID)
	}
	if got := NewRequest(types.UID("pod-uid"), "other-revision"); got.ID == request.ID {
		t.Fatalf("request IDs match across StatefulSet revisions: %q", got.ID)
	}
}

func TestDecodeRejectsIncompleteRequest(t *testing.T) {
	if _, err := Decode(`{"id":"request"}`); err == nil {
		t.Fatal("Decode() accepted a request without podUID")
	}
}
