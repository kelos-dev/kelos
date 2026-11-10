package agentlog

import (
	"bytes"
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestFormatSelectsParserByAgentType(t *testing.T) {
	// Each input produces output only from its own agent's parser.
	tests := []struct {
		agentType  string
		input      string
		wantStdout string
		wantStderr string
	}{
		{agentType: "codex", input: `{"type":"turn.started"}`, wantStderr: "\n--- Turn 1 ---\n"},
		{agentType: "gemini", input: `{"type":"init","model":"gemini-2.5-pro"}`, wantStderr: "[init] model=gemini-2.5-pro\n"},
		{agentType: "opencode", input: `{"type":"text","part":{"type":"text","text":"Hello world"}}`, wantStdout: "Hello world"},
		{agentType: "claude-code", input: `{"type":"system","subtype":"init","model":"claude-sonnet-5"}`, wantStderr: "[init] model=claude-sonnet-5\n"},
		{agentType: "", input: `{"type":"system","subtype":"init","model":"claude-sonnet-5"}`, wantStderr: "[init] model=claude-sonnet-5\n"},
	}
	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := Format(tt.agentType, strings.NewReader(tt.input+"\n"), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestResolveType(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pool := &kelos.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "team-a"},
		Spec:       kelos.WorkerPoolSpec{Worker: kelos.WorkerSpec{Type: "gemini"}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pool).Build()

	tests := []struct {
		name string
		spec kelos.TaskSpec
		want string
	}{
		{name: "worker type", spec: kelos.TaskSpec{Worker: &kelos.WorkerSpec{Type: "codex"}, Type: "opencode"}, want: "codex"},
		{name: "legacy type", spec: kelos.TaskSpec{Type: "opencode"}, want: "opencode"},
		{name: "worker pool type", spec: kelos.TaskSpec{WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"}}, want: "gemini"},
		{name: "unset", spec: kelos.TaskSpec{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelos.Task{Spec: tt.spec}
			got, err := ResolveType(context.Background(), cl, "team-a", task)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ResolveType() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("missing worker pool", func(t *testing.T) {
		task := &kelos.Task{Spec: kelos.TaskSpec{WorkerPoolRef: &kelos.WorkerPoolReference{Name: "missing"}}}
		_, err := ResolveType(context.Background(), cl, "team-a", task)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("ResolveType() error = %v, want NotFound", err)
		}
	})
}
