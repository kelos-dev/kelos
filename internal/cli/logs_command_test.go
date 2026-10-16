package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	logsTestNamespace   = "test-ns"
	logsTestTaskName    = "test-task"
	logsTestPodName     = "test-pod"
	logsTestLogRequests = 1
	logsTestInput       = `{"type":"assistant","message":{"content":[{"type":"text","text":"hello from Claude"}]}}`
	logsTestKubeconfig  = `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
users:
- name: test
  user: {}
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
`
)

func TestLogsCommandPretty(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantError string
		wantLog   bool
	}{
		{name: "Claude renders pretty output", agentType: agentTypeClaudeCode, wantLog: true},
		{name: "other agent is rejected", agentType: agentTypeCodex, wantError: `task "test-task": --pretty supports only Claude Code logs`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logRequests atomic.Int32
			server := newLogsTestServer(tt.agentType, &logRequests)
			defer server.Close()
			cfg := logsTestClientConfig(t, server.URL)
			output, err := executeLogsTestCommand(t, cfg)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Execute() error = %v, want %q", err, tt.wantError)
				}
			} else if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if tt.wantLog && output != "⏺ hello from Claude\n" {
				t.Errorf("output = %q, want rendered Claude text", output)
			}
			if !tt.wantLog && output != "" {
				t.Errorf("unsupported agent output = %q, want none", output)
			}
			var wantLogRequests int32
			if tt.wantLog {
				wantLogRequests = logsTestLogRequests
			}
			if got := logRequests.Load(); got != wantLogRequests {
				t.Errorf("pod log requests = %d, want %d", got, wantLogRequests)
			}
		})
	}
}

func executeLogsTestCommand(t *testing.T, cfg *ClientConfig) (string, error) {
	t.Helper()
	stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = stdout
	defer func() { os.Stdout = oldStdout }()
	cmd := newLogsCommand(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{logsTestTaskName, "--pretty"})
	execErr := cmd.Execute()
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(output), execErr
}

func logsTestClientConfig(t *testing.T, serverURL string) *ClientConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, fmt.Appendf(nil, logsTestKubeconfig, serverURL), 0o600); err != nil {
		t.Fatal(err)
	}
	return &ClientConfig{Kubeconfig: path, Namespace: logsTestNamespace}
}

func newLogsTestServer(agentType string, logRequests *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveLogsTestDiscovery(w, r) {
			return
		}
		switch r.URL.Path {
		case "/apis/kelos.dev/v1alpha2/namespaces/" + logsTestNamespace + "/tasks/" + logsTestTaskName:
			writeLogsTestJSON(w, &kelos.Task{
				TypeMeta:   metav1.TypeMeta{APIVersion: "kelos.dev/v1alpha2", Kind: "Task"},
				ObjectMeta: metav1.ObjectMeta{Name: logsTestTaskName, Namespace: logsTestNamespace},
				Spec:       kelos.TaskSpec{Type: agentType},
			})
		case "/api/v1/namespaces/" + logsTestNamespace + "/pods":
			writeLogsTestJSON(w, &corev1.PodList{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"},
				Items:    []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: logsTestPodName, Namespace: logsTestNamespace}}},
			})
		case "/api/v1/namespaces/" + logsTestNamespace + "/pods/" + logsTestPodName + "/log":
			logRequests.Add(1)
			fmt.Fprintln(w, logsTestInput)
		default:
			http.NotFound(w, r)
		}
	}))
}

func serveLogsTestDiscovery(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api":
		writeLogsTestJSON(w, &metav1.APIVersions{Versions: []string{"v1"}})
	case "/apis":
		version := metav1.GroupVersionForDiscovery{GroupVersion: "kelos.dev/v1alpha2", Version: "v1alpha2"}
		writeLogsTestJSON(w, &metav1.APIGroupList{Groups: []metav1.APIGroup{{
			Name: "kelos.dev", Versions: []metav1.GroupVersionForDiscovery{version}, PreferredVersion: version,
		}}})
	case "/api/v1":
		writeLogsTestJSON(w, &metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Namespaced: true, Kind: "Pod"},
		}})
	case "/apis/kelos.dev/v1alpha2":
		writeLogsTestJSON(w, &metav1.APIResourceList{GroupVersion: "kelos.dev/v1alpha2", APIResources: []metav1.APIResource{
			{Name: "tasks", Namespaced: true, Kind: "Task"},
		}})
	default:
		return false
	}
	return true
}

func writeLogsTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
