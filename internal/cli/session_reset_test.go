package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionreset"
)

func TestRunSessionResetRequestsResetOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := testSession("chat", "default")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	output := &bytes.Buffer{}

	if err := runSessionReset(context.Background(), cl, session.Namespace, session.Name, output); err != nil {
		t.Fatal(err)
	}
	var updated kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	requestID := updated.Annotations[sessionreset.RequestAnnotation]
	if requestID == "" || output.String() != "session/chat reset requested\n" {
		t.Fatalf("reset request = %q output = %q", requestID, output.String())
	}

	output.Reset()
	if err := runSessionReset(context.Background(), cl, session.Namespace, session.Name, output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "session/chat reset already requested\n" {
		t.Fatalf("second reset output = %q", output.String())
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[sessionreset.RequestAnnotation] != requestID {
		t.Fatalf("second reset replaced request %q with %q", requestID, updated.Annotations[sessionreset.RequestAnnotation])
	}
}

func TestConfirmSessionResetDescribesDataLoss(t *testing.T) {
	output := &bytes.Buffer{}
	confirmed, err := confirmSessionReset(strings.NewReader("yes\n"), output, "team-a", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("confirmation was rejected")
	}
	if got := output.String(); !strings.Contains(got, "team-a/chat") || !strings.Contains(got, "permanently deletes") {
		t.Fatalf("confirmation prompt = %q", got)
	}
}
