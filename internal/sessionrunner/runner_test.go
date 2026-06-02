package sessionrunner

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/capture"
	kelosfake "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/fake"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	// Clear relevant env vars.
	os.Unsetenv("KELOS_POD_NAME")
	os.Unsetenv("KELOS_POD_NAMESPACE")
	os.Unsetenv("KELOS_AGENT_TYPE")
	os.Unsetenv("KELOS_TASKSPAWNER")
	os.Unsetenv("KELOS_IDLE_TIMEOUT")
	os.Unsetenv("KELOS_MAX_TASKS_PER_SESSION")
	os.Unsetenv("KELOS_MAX_SESSION_DURATION")

	cfg := ConfigFromEnv()

	if cfg.IdleTimeout != defaultIdleTimeout {
		t.Errorf("IdleTimeout: expected %v, got %v", defaultIdleTimeout, cfg.IdleTimeout)
	}
	if cfg.MaxSessionDuration != defaultMaxSessionDuration {
		t.Errorf("MaxSessionDuration: expected %v, got %v", defaultMaxSessionDuration, cfg.MaxSessionDuration)
	}
	if cfg.MaxTasksPerSession != 0 {
		t.Errorf("MaxTasksPerSession: expected 0, got %d", cfg.MaxTasksPerSession)
	}
}

func TestConfigFromEnv_CustomValues(t *testing.T) {
	t.Setenv("KELOS_POD_NAME", "session-pod-0")
	t.Setenv("KELOS_POD_NAMESPACE", "test-ns")
	t.Setenv("KELOS_AGENT_TYPE", "claude-code")
	t.Setenv("KELOS_TASKSPAWNER", "my-spawner")
	t.Setenv("KELOS_TOKEN_SECRET", "my-token-secret")
	t.Setenv("KELOS_IDLE_TIMEOUT", "15m")
	t.Setenv("KELOS_MAX_TASKS_PER_SESSION", "5")
	t.Setenv("KELOS_MAX_SESSION_DURATION", "4h")
	t.Setenv("KELOS_TOKEN_REFRESH_INTERVAL", "20m")

	cfg := ConfigFromEnv()

	if cfg.PodName != "session-pod-0" {
		t.Errorf("PodName: expected 'session-pod-0', got %q", cfg.PodName)
	}
	if cfg.PodNamespace != "test-ns" {
		t.Errorf("PodNamespace: expected 'test-ns', got %q", cfg.PodNamespace)
	}
	if cfg.AgentType != "claude-code" {
		t.Errorf("AgentType: expected 'claude-code', got %q", cfg.AgentType)
	}
	if cfg.TaskSpawner != "my-spawner" {
		t.Errorf("TaskSpawner: expected 'my-spawner', got %q", cfg.TaskSpawner)
	}
	if cfg.TokenSecret != "my-token-secret" {
		t.Errorf("TokenSecret: expected 'my-token-secret', got %q", cfg.TokenSecret)
	}
	if cfg.IdleTimeout != 15*time.Minute {
		t.Errorf("IdleTimeout: expected 15m, got %v", cfg.IdleTimeout)
	}
	if cfg.MaxTasksPerSession != 5 {
		t.Errorf("MaxTasksPerSession: expected 5, got %d", cfg.MaxTasksPerSession)
	}
	if cfg.MaxSessionDuration != 4*time.Hour {
		t.Errorf("MaxSessionDuration: expected 4h, got %v", cfg.MaxSessionDuration)
	}
	if cfg.TokenRefreshInterval != 20*time.Minute {
		t.Errorf("TokenRefreshInterval: expected 20m, got %v", cfg.TokenRefreshInterval)
	}
}

func TestConfigFromEnv_AuthFailurePatterns(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		want   []string
	}{
		{
			name:   "empty env var",
			envVal: "",
			want:   nil,
		},
		{
			name:   "single pattern",
			envVal: "custom error",
			want:   []string{"custom error"},
		},
		{
			name:   "multiple patterns with whitespace",
			envVal: " pattern one , pattern two , pattern three ",
			want:   []string{"pattern one", "pattern two", "pattern three"},
		},
		{
			name:   "trailing comma ignored",
			envVal: "foo,bar,",
			want:   []string{"foo", "bar"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != "" {
				t.Setenv("KELOS_AUTH_FAILURE_PATTERNS", tt.envVal)
			} else {
				os.Unsetenv("KELOS_AUTH_FAILURE_PATTERNS")
			}

			cfg := ConfigFromEnv()

			if tt.want == nil {
				if cfg.AuthFailurePatterns != nil {
					t.Errorf("expected nil, got %v", cfg.AuthFailurePatterns)
				}
				return
			}
			if len(cfg.AuthFailurePatterns) != len(tt.want) {
				t.Fatalf("expected %d patterns, got %d: %v", len(tt.want), len(cfg.AuthFailurePatterns), cfg.AuthFailurePatterns)
			}
			for i, want := range tt.want {
				if cfg.AuthFailurePatterns[i] != want {
					t.Errorf("pattern[%d]: expected %q, got %q", i, want, cfg.AuthFailurePatterns[i])
				}
			}
		})
	}
}

func TestConfigFromEnv_InvalidDuration(t *testing.T) {
	t.Setenv("KELOS_IDLE_TIMEOUT", "not-a-duration")

	cfg := ConfigFromEnv()

	// Should fall back to default.
	if cfg.IdleTimeout != defaultIdleTimeout {
		t.Errorf("IdleTimeout: expected default %v on invalid input, got %v", defaultIdleTimeout, cfg.IdleTimeout)
	}
}

func TestConfigFromEnv_InvalidMaxTasks(t *testing.T) {
	t.Setenv("KELOS_MAX_TASKS_PER_SESSION", "abc")

	cfg := ConfigFromEnv()

	if cfg.MaxTasksPerSession != 0 {
		t.Errorf("MaxTasksPerSession: expected 0 on invalid input, got %d", cfg.MaxTasksPerSession)
	}
}

func TestParseOutputs(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect []string
	}{
		{
			name:   "no markers",
			input:  "some random log output\n",
			expect: nil,
		},
		{
			name:   "empty between markers",
			input:  "---KELOS_OUTPUTS_START---\n---KELOS_OUTPUTS_END---\n",
			expect: nil,
		},
		{
			name:   "single output",
			input:  "log line\n---KELOS_OUTPUTS_START---\nbranch: main\n---KELOS_OUTPUTS_END---\n",
			expect: []string{"branch: main"},
		},
		{
			name:   "multiple outputs",
			input:  "---KELOS_OUTPUTS_START---\nbranch: feat\ncommit: abc123\nresponse: dGVzdA==\n---KELOS_OUTPUTS_END---\n",
			expect: []string{"branch: feat", "commit: abc123", "response: dGVzdA=="},
		},
		{
			name:   "start without end",
			input:  "---KELOS_OUTPUTS_START---\nbranch: main\n",
			expect: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := capture.ParseOutputs(tc.input)
			if tc.expect == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tc.expect) {
				t.Fatalf("expected %d outputs, got %d: %v", len(tc.expect), len(got), got)
			}
			for i, want := range tc.expect {
				if got[i] != want {
					t.Errorf("output[%d]: expected %q, got %q", i, want, got[i])
				}
			}
		})
	}
}

func TestResultsFromOutputs(t *testing.T) {
	outputs := []string{"branch: main", "commit: abc123", "cost-usd: 0.05"}
	results := capture.ResultsFromOutputs(outputs)

	if results["branch"] != "main" {
		t.Errorf("branch: expected 'main', got %q", results["branch"])
	}
	if results["commit"] != "abc123" {
		t.Errorf("commit: expected 'abc123', got %q", results["commit"])
	}
	if results["cost-usd"] != "0.05" {
		t.Errorf("cost-usd: expected '0.05', got %q", results["cost-usd"])
	}
}

func TestResultsFromOutputs_Empty(t *testing.T) {
	if got := capture.ResultsFromOutputs(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if got := capture.ResultsFromOutputs([]string{}); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestTailWriter_SmallWrite(t *testing.T) {
	tw := newTailWriter(100)
	tw.Write([]byte("hello"))
	if got := tw.String(); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTailWriter_ExactFit(t *testing.T) {
	tw := newTailWriter(5)
	tw.Write([]byte("hello"))
	if got := tw.String(); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTailWriter_Overflow(t *testing.T) {
	tw := newTailWriter(5)
	tw.Write([]byte("hello world"))
	if got := tw.String(); got != "world" {
		t.Errorf("expected 'world', got %q", got)
	}
}

func TestTailWriter_MultipleWrites(t *testing.T) {
	tw := newTailWriter(10)
	tw.Write([]byte("aaaa"))
	tw.Write([]byte("bbbb"))
	tw.Write([]byte("cccc"))
	got := tw.String()
	// Total written: 12 bytes ("aaaabbbbcccc"), buffer is 10, so last 10 = "aabbbbcccc"
	want := "aabbbbcccc"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestTailWriter_PreservesOutputMarkers(t *testing.T) {
	tw := newTailWriter(256)
	// Write a bunch of noise first
	for i := 0; i < 100; i++ {
		tw.Write([]byte("noise line that should be evicted\n"))
	}
	// Then write the markers at the end
	tw.Write([]byte("---KELOS_OUTPUTS_START---\nbranch: main\ncommit: abc\n---KELOS_OUTPUTS_END---\n"))

	got := tw.String()
	outputs := capture.ParseOutputs(got)
	if len(outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d from tail: %q", len(outputs), got[max(0, len(got)-200):])
	}
	if outputs[0] != "branch: main" {
		t.Errorf("output[0]: expected 'branch: main', got %q", outputs[0])
	}
}

func TestRefreshToken_TrimsWhitespace(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("  token-with-whitespace\n")},
	}
	client := fake.NewSimpleClientset(secret)

	t.Setenv("GH_TOKEN", "old")
	t.Setenv("GH_CONFIG_DIR", "")

	r := &Runner{
		config: Config{
			PodNamespace: "test-ns",
			TokenSecret:  "my-secret",
		},
		kubeClient: client,
	}

	err := r.refreshToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("Failed to read token file: %v", err)
	}
	if string(got) != "token-with-whitespace" {
		t.Errorf("Token file should be trimmed, got %q", string(got))
	}
	if os.Getenv("GITHUB_TOKEN") != "token-with-whitespace" {
		t.Errorf("GITHUB_TOKEN env should be trimmed, got %q", os.Getenv("GITHUB_TOKEN"))
	}
}

func TestFilterTokenEnvVars(t *testing.T) {
	env := []string{
		"HOME=/home/user",
		"GITHUB_TOKEN=secret123",
		"GH_TOKEN=secret456",
		"GH_ENTERPRISE_TOKEN=secret789",
		"PATH=/usr/bin",
		"GH_CONFIG_DIR=/workspace/.gh-config",
	}

	filtered := filterTokenEnvVars(env)

	expected := []string{
		"HOME=/home/user",
		"PATH=/usr/bin",
		"GH_CONFIG_DIR=/workspace/.gh-config",
	}

	if len(filtered) != len(expected) {
		t.Fatalf("expected %d vars, got %d: %v", len(expected), len(filtered), filtered)
	}
	for i, want := range expected {
		if filtered[i] != want {
			t.Errorf("filtered[%d]: expected %q, got %q", i, want, filtered[i])
		}
	}
}

func TestStartTokenRefreshLoop(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("refreshed-token")},
	}
	client := fake.NewSimpleClientset(secret)

	t.Setenv("GH_TOKEN", "old-token")
	t.Setenv("GITHUB_TOKEN", "old-token")
	t.Setenv("GH_CONFIG_DIR", "")

	r := &Runner{
		config: Config{
			PodNamespace:         "test-ns",
			TokenSecret:          "my-secret",
			TokenRefreshInterval: 50 * time.Millisecond,
		},
		kubeClient: client,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		for os.Getenv("GITHUB_TOKEN") != "refreshed-token" {
			time.Sleep(5 * time.Millisecond)
		}
		close(done)
	}()

	r.startTokenRefreshLoop(ctx)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for token refresh")
	}
	cancel()

	if got := os.Getenv("GITHUB_TOKEN"); got != "refreshed-token" {
		t.Errorf("GITHUB_TOKEN: expected 'refreshed-token', got %q", got)
	}
	if got := os.Getenv("GH_TOKEN"); got != "refreshed-token" {
		t.Errorf("GH_TOKEN: expected 'refreshed-token', got %q", got)
	}
}

func TestStartTokenRefreshLoop_NoSecret(t *testing.T) {
	r := &Runner{
		config: Config{
			TokenSecret:          "",
			TokenRefreshInterval: 50 * time.Millisecond,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should not panic or start goroutine.
	r.startTokenRefreshLoop(ctx)
}

func TestRefreshToken_WritesTokenFile(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("file-token-123")},
	}
	client := fake.NewSimpleClientset(secret)

	ghConfigDir := tmpDir + "/gh-config"
	t.Setenv("GH_TOKEN", "old")
	t.Setenv("GH_CONFIG_DIR", ghConfigDir)
	t.Setenv("GH_HOST", "")

	r := &Runner{
		config: Config{
			PodNamespace: "test-ns",
			TokenSecret:  "my-secret",
		},
		kubeClient: client,
	}

	err := r.refreshToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify token file was written.
	got, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("Failed to read token file: %v", err)
	}
	if string(got) != "file-token-123" {
		t.Errorf("Token file: expected 'file-token-123', got %q", string(got))
	}

	// Verify gh hosts.yml was written.
	hostsContent, err := os.ReadFile(ghConfigDir + "/hosts.yml")
	if err != nil {
		t.Fatalf("Failed to read hosts.yml: %v", err)
	}
	if !strings.Contains(string(hostsContent), "file-token-123") {
		t.Errorf("hosts.yml should contain token, got: %s", hostsContent)
	}
	if !strings.Contains(string(hostsContent), "github.com") {
		t.Errorf("hosts.yml should contain github.com, got: %s", hostsContent)
	}
}

func TestWriteGHHostsFile_Enterprise(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", tmpDir)
	t.Setenv("GH_HOST", "github.enterprise.com")

	r := &Runner{}
	err := r.writeGHHostsFile("ent-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hostsContent, err := os.ReadFile(tmpDir + "/hosts.yml")
	if err != nil {
		t.Fatalf("Failed to read hosts.yml: %v", err)
	}
	if !strings.Contains(string(hostsContent), "github.enterprise.com") {
		t.Errorf("hosts.yml should contain enterprise host, got: %s", hostsContent)
	}
	if !strings.Contains(string(hostsContent), "ent-token") {
		t.Errorf("hosts.yml should contain token, got: %s", hostsContent)
	}
}

func TestWriteGHHostsFile_MergesExistingEntries(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", tmpDir)
	t.Setenv("GH_HOST", "github.com")

	// Pre-populate hosts.yml with an enterprise entry and extra fields.
	existing := "github.enterprise.com:\n  oauth_token: enterprise-token\n  user: x-access-token\n  git_protocol: ssh\ngithub.com:\n  oauth_token: old-token\n  user: x-access-token\n  git_protocol: https\n"
	if err := os.WriteFile(tmpDir+"/hosts.yml", []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	r := &Runner{}
	err := r.writeGHHostsFile("new-public-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hostsContent, err := os.ReadFile(tmpDir + "/hosts.yml")
	if err != nil {
		t.Fatalf("Failed to read hosts.yml: %v", err)
	}
	content := string(hostsContent)
	if !strings.Contains(content, "github.enterprise.com") {
		t.Errorf("hosts.yml should preserve enterprise host, got: %s", content)
	}
	if !strings.Contains(content, "enterprise-token") {
		t.Errorf("hosts.yml should preserve enterprise token, got: %s", content)
	}
	if !strings.Contains(content, "github.com") {
		t.Errorf("hosts.yml should contain github.com, got: %s", content)
	}
	if !strings.Contains(content, "new-public-token") {
		t.Errorf("hosts.yml should contain new token, got: %s", content)
	}
	// Verify unknown fields are preserved.
	if !strings.Contains(content, "git_protocol") {
		t.Errorf("hosts.yml should preserve git_protocol field, got: %s", content)
	}
	if !strings.Contains(content, "ssh") {
		t.Errorf("hosts.yml should preserve enterprise git_protocol: ssh, got: %s", content)
	}
	if !strings.Contains(content, "https") {
		t.Errorf("hosts.yml should preserve github.com git_protocol: https, got: %s", content)
	}
}

func TestWriteGHHostsFile_HostWithPort(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", tmpDir)
	t.Setenv("GH_HOST", "github.corp.com:8080")

	r := &Runner{}
	err := r.writeGHHostsFile("port-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hostsContent, err := os.ReadFile(tmpDir + "/hosts.yml")
	if err != nil {
		t.Fatalf("Failed to read hosts.yml: %v", err)
	}
	content := string(hostsContent)
	if !strings.Contains(content, "github.corp.com:8080") {
		t.Errorf("hosts.yml should contain host with port, got: %s", content)
	}
	if !strings.Contains(content, "port-token") {
		t.Errorf("hosts.yml should contain token, got: %s", content)
	}
}

func TestWriteGHHostsFile_MalformedYAML(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", tmpDir)
	t.Setenv("GH_HOST", "github.com")

	// Write invalid YAML to hosts.yml.
	if err := os.WriteFile(tmpDir+"/hosts.yml", []byte("{{invalid yaml"), 0600); err != nil {
		t.Fatal(err)
	}

	r := &Runner{}
	err := r.writeGHHostsFile("some-token")
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
	if !strings.Contains(err.Error(), "parsing existing hosts.yml") {
		t.Errorf("error should mention parsing, got: %v", err)
	}
}

func TestAtomicWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/test-file"

	if err := atomicWriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("expected 'hello', got %q", string(got))
	}

	// Verify no temp files left behind.
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) != 1 {
		t.Errorf("expected 1 file in dir, got %d", len(entries))
	}
}

func TestRefreshToken(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	tests := []struct {
		name        string
		secret      *corev1.Secret
		tokenSecret string
		envBefore   map[string]string
		wantErr     bool
		wantGH      string
		wantGHE     string
	}{
		{
			name:        "no token secret configured",
			tokenSecret: "",
			wantErr:     false,
		},
		{
			name:        "happy path sets GH_TOKEN",
			tokenSecret: "my-secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
				Data:       map[string][]byte{"GITHUB_TOKEN": []byte("new-token-123")},
			},
			envBefore: map[string]string{"GH_TOKEN": "old-token"},
			wantGH:    "new-token-123",
		},
		{
			name:        "enterprise token",
			tokenSecret: "my-secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
				Data:       map[string][]byte{"GITHUB_TOKEN": []byte("ent-token-456")},
			},
			envBefore: map[string]string{"GH_ENTERPRISE_TOKEN": "old-ent"},
			wantGHE:   "ent-token-456",
		},
		{
			name:        "neither GH var set does not inject",
			tokenSecret: "my-secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
				Data:       map[string][]byte{"GITHUB_TOKEN": []byte("some-token")},
			},
			envBefore: map[string]string{},
			wantGH:    "",
			wantGHE:   "",
		},
		{
			name:        "missing secret returns error",
			tokenSecret: "nonexistent",
			wantErr:     true,
		},
		{
			name:        "empty GITHUB_TOKEN key returns error",
			tokenSecret: "my-secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
				Data:       map[string][]byte{"GITHUB_TOKEN": {}},
			},
			wantErr: true,
		},
		{
			name:        "missing GITHUB_TOKEN key returns error",
			tokenSecret: "my-secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
				Data:       map[string][]byte{"OTHER_KEY": []byte("value")},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean env.
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GH_ENTERPRISE_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")
			os.Unsetenv("GH_TOKEN")
			os.Unsetenv("GH_ENTERPRISE_TOKEN")
			os.Unsetenv("GITHUB_TOKEN")

			for k, v := range tt.envBefore {
				t.Setenv(k, v)
			}

			var client *fake.Clientset
			if tt.secret != nil {
				client = fake.NewSimpleClientset(tt.secret)
			} else {
				client = fake.NewSimpleClientset()
			}

			r := &Runner{
				config: Config{
					PodNamespace: "test-ns",
					TokenSecret:  tt.tokenSecret,
				},
				kubeClient: client,
			}

			err := r.refreshToken(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.tokenSecret == "" {
				return
			}

			if tt.wantGH != "" {
				if got := os.Getenv("GH_TOKEN"); got != tt.wantGH {
					t.Errorf("GH_TOKEN: expected %q, got %q", tt.wantGH, got)
				}
			}
			if tt.wantGHE != "" {
				if got := os.Getenv("GH_ENTERPRISE_TOKEN"); got != tt.wantGHE {
					t.Errorf("GH_ENTERPRISE_TOKEN: expected %q, got %q", tt.wantGHE, got)
				}
			}
			if tt.wantGH == "" && tt.wantGHE == "" && tt.tokenSecret != "" {
				if got := os.Getenv("GH_TOKEN"); got != "" {
					t.Errorf("GH_TOKEN should not be set, got %q", got)
				}
				if got := os.Getenv("GH_ENTERPRISE_TOKEN"); got != "" {
					t.Errorf("GH_ENTERPRISE_TOKEN should not be set, got %q", got)
				}
			}
		})
	}
}

func TestRefreshToken_SkipsExpiredToken(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	// Token expired 10 minutes ago.
	expiredAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret",
			Namespace: "test-ns",
			Annotations: map[string]string{
				"kelos.dev/token-expires-at": expiredAt,
			},
		},
		Data: map[string][]byte{"GITHUB_TOKEN": []byte("stale-token")},
	}
	client := fake.NewSimpleClientset(secret)

	t.Setenv("GH_CONFIG_DIR", "")

	r := &Runner{
		config: Config{
			PodNamespace: "test-ns",
			TokenSecret:  "my-secret",
		},
		kubeClient: client,
	}

	err := r.refreshToken(context.Background())
	if err == nil {
		t.Fatal("Expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("Error should mention 'expired', got: %v", err)
	}

	// Token file should NOT be written.
	if _, statErr := os.Stat(tokenFilePath); statErr == nil {
		t.Error("Token file should not have been written for expired token")
	}
}

func TestRefreshToken_AcceptsNonExpiredToken(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	// Token expires 30 minutes from now.
	expiresAt := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret",
			Namespace: "test-ns",
			Annotations: map[string]string{
				"kelos.dev/token-expires-at": expiresAt,
			},
		},
		Data: map[string][]byte{"GITHUB_TOKEN": []byte("fresh-token")},
	}
	client := fake.NewSimpleClientset(secret)

	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("GH_TOKEN", "")

	r := &Runner{
		config: Config{
			PodNamespace: "test-ns",
			TokenSecret:  "my-secret",
		},
		kubeClient: client,
	}

	err := r.refreshToken(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("Failed to read token file: %v", err)
	}
	if string(got) != "fresh-token" {
		t.Errorf("Token file: expected 'fresh-token', got %q", string(got))
	}
}

func TestRefreshToken_NoAnnotationStillWorks(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	// No annotation at all — backwards compatibility.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret",
			Namespace: "test-ns",
		},
		Data: map[string][]byte{"GITHUB_TOKEN": []byte("legacy-token")},
	}
	client := fake.NewSimpleClientset(secret)

	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("GH_TOKEN", "")

	r := &Runner{
		config: Config{
			PodNamespace: "test-ns",
			TokenSecret:  "my-secret",
		},
		kubeClient: client,
	}

	err := r.refreshToken(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("Failed to read token file: %v", err)
	}
	if string(got) != "legacy-token" {
		t.Errorf("Token file: expected 'legacy-token', got %q", string(got))
	}
}

func TestProcessTask_Success(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "test-ns",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: "test-ns",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("valid-token")},
	}

	kubeClient := fake.NewSimpleClientset(pod, secret)
	kelosClient := kelosfake.NewSimpleClientset(task)

	r := &Runner{
		config: Config{
			PodName:      "session-pod-0",
			PodNamespace: "test-ns",
			AgentType:    "claude-code",
			TokenSecret:  "my-secret",
		},
		kubeClient:  kubeClient,
		kelosClient: kelosClient,
		workspace:   &WorkspaceManager{runGitCmd: func(_ context.Context, _ ...string) error { return nil }},
		runAgentFn: func(_ context.Context, t *kelosv1alpha1.Task) (string, error) {
			if t.Spec.Prompt != "do something" {
				return "", errors.New("unexpected prompt")
			}
			return "", nil
		},
	}

	err := r.processTask(context.Background(), "test-task")
	if err != nil {
		t.Fatalf("processTask should succeed, got error: %v", err)
	}

	// Verify task status was updated.
	updated, err := kelosClient.ApiV1alpha1().Tasks("test-ns").Get(context.Background(), "test-task", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updated.Status.CompletionTime == nil {
		t.Error("Expected CompletionTime to be set on success")
	}
}

func TestProcessTask_TokenExpired(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "test-ns",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: "test-ns",
		},
	}
	// Secret with an expired token annotation.
	expiredTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "my-secret",
			Namespace:   "test-ns",
			Annotations: map[string]string{annotationTokenExpiresAt: expiredTime},
		},
		Data: map[string][]byte{"GITHUB_TOKEN": []byte("expired-token")},
	}

	kubeClient := fake.NewSimpleClientset(pod, secret)
	kelosClient := kelosfake.NewSimpleClientset(task)

	r := &Runner{
		config: Config{
			PodName:      "session-pod-0",
			PodNamespace: "test-ns",
			AgentType:    "claude-code",
			TokenSecret:  "my-secret",
		},
		kubeClient:  kubeClient,
		kelosClient: kelosClient,
		workspace:   &WorkspaceManager{runGitCmd: func(_ context.Context, _ ...string) error { return nil }},
		runAgentFn: func(_ context.Context, _ *kelosv1alpha1.Task) (string, error) {
			t.Fatal("Agent should not be invoked when token is expired")
			return "", nil
		},
	}

	err := r.processTask(context.Background(), "test-task")
	if !errors.Is(err, errTokenExpired) {
		t.Fatalf("Expected errTokenExpired, got: %v", err)
	}

	// CompletionTime must NOT be set on failure.
	updated, err := kelosClient.ApiV1alpha1().Tasks("test-ns").Get(context.Background(), "test-task", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updated.Status.CompletionTime != nil {
		t.Error("CompletionTime must not be set when processTask returns an error")
	}
}

func TestProcessTask_AgentReportedFailure(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "test-ns",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: "test-ns",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("valid-token")},
	}

	kubeClient := fake.NewSimpleClientset(pod, secret)
	kelosClient := kelosfake.NewSimpleClientset(task)

	r := &Runner{
		config: Config{
			PodName:      "session-pod-0",
			PodNamespace: "test-ns",
			AgentType:    "claude-code",
			TokenSecret:  "my-secret",
		},
		kubeClient:  kubeClient,
		kelosClient: kelosClient,
		workspace:   &WorkspaceManager{runGitCmd: func(_ context.Context, _ ...string) error { return nil }},
		runAgentFn: func(_ context.Context, _ *kelosv1alpha1.Task) (string, error) {
			// Agent exits 0 but reports is_error=true in result.
			return `{"type":"result","subtype":"error","is_error":true,"result":"something went wrong"}`, nil
		},
	}

	err := r.processTask(context.Background(), "test-task")
	if !errors.Is(err, errAgentReportedFailure) {
		t.Fatalf("Expected errAgentReportedFailure, got: %v", err)
	}

	// CompletionTime must NOT be set on failure — the SessionReconciler infers
	// success from CompletionTime when the pod annotation write is missed.
	updated, err := kelosClient.ApiV1alpha1().Tasks("test-ns").Get(context.Background(), "test-task", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get task: %v", err)
	}
	if updated.Status.CompletionTime != nil {
		t.Error("CompletionTime must not be set when processTask returns an error")
	}
}

func TestProcessTask_AuthFailureInOutput(t *testing.T) {
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "test-ns",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: "test-ns",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "test-ns"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("valid-token")},
	}

	kubeClient := fake.NewSimpleClientset(pod, secret)
	kelosClient := kelosfake.NewSimpleClientset(task)

	r := &Runner{
		config: Config{
			PodName:      "session-pod-0",
			PodNamespace: "test-ns",
			AgentType:    "claude-code",
			TokenSecret:  "my-secret",
		},
		kubeClient:  kubeClient,
		kelosClient: kelosClient,
		workspace:   &WorkspaceManager{runGitCmd: func(_ context.Context, _ ...string) error { return nil }},
		runAgentFn: func(_ context.Context, _ *kelosv1alpha1.Task) (string, error) {
			// Agent emits a result JSON line indicating auth failure with session-ending phrase.
			return `{"type":"result","subtype":"error","is_error":true,"result":"Bad credentials (HTTP 401). This session must end."}`, errors.New("exit status 1")
		},
	}

	err := r.processTask(context.Background(), "test-task")
	if !errors.Is(err, errTokenExpired) {
		t.Fatalf("Expected errTokenExpired (auth failure in output), got: %v", err)
	}
}

func TestGetAssignedTask_SkipsAlreadyProcessed(t *testing.T) {
	// After the runner writes a status annotation, getAssignedTask must
	// return empty to prevent double-execution (the runner's own annotation
	// write bumps resourceVersion, which would look like a new assignment).
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: "test-ns",
			Annotations: map[string]string{
				annotationAssignedTask: "task-A",
				annotationTaskStatus:   "succeeded",
			},
		},
	}
	kubeClient := fake.NewSimpleClientset(pod)

	r := &Runner{
		config: Config{
			PodName:      "session-pod-0",
			PodNamespace: "test-ns",
		},
		kubeClient: kubeClient,
	}

	assignment, err := r.getAssignedTask(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if assignment.name != "" {
		t.Errorf("Expected empty assignment when status is set, got %q", assignment.name)
	}
}

func TestGetAssignedTask_ReturnsAssignmentWhenNoStatus(t *testing.T) {
	// A fresh assignment (no status annotation) should be returned.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: "test-ns",
			Annotations: map[string]string{
				annotationAssignedTask: "task-B",
			},
		},
	}
	kubeClient := fake.NewSimpleClientset(pod)

	r := &Runner{
		config: Config{
			PodName:      "session-pod-0",
			PodNamespace: "test-ns",
		},
		kubeClient: kubeClient,
	}

	assignment, err := r.getAssignedTask(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if assignment.name != "task-B" {
		t.Errorf("Expected assignment name 'task-B', got %q", assignment.name)
	}
}

func TestGetAssignedTask_ReturnsEmptyWhenNoAssignment(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: "test-ns",
		},
	}
	kubeClient := fake.NewSimpleClientset(pod)

	r := &Runner{
		config: Config{
			PodName:      "session-pod-0",
			PodNamespace: "test-ns",
		},
		kubeClient: kubeClient,
	}

	assignment, err := r.getAssignedTask(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if assignment.name != "" {
		t.Errorf("Expected empty assignment, got %q", assignment.name)
	}
}

func TestProcessTask_MissingSecret(t *testing.T) {
	// A NotFound secret should fail fast with errTokenExpired (via errCredentialUnavailable).
	tmpDir := t.TempDir()
	origTokenFilePath := tokenFilePath
	tokenFilePath = tmpDir + "/token"
	t.Cleanup(func() { tokenFilePath = origTokenFilePath })

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "test-ns",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-pod-0",
			Namespace: "test-ns",
		},
	}
	// No secret created — it's missing.
	kubeClient := fake.NewSimpleClientset(pod)
	kelosClient := kelosfake.NewSimpleClientset(task)

	r := &Runner{
		config: Config{
			PodName:      "session-pod-0",
			PodNamespace: "test-ns",
			AgentType:    "claude-code",
			TokenSecret:  "nonexistent-secret",
		},
		kubeClient:  kubeClient,
		kelosClient: kelosClient,
		workspace:   &WorkspaceManager{runGitCmd: func(_ context.Context, _ ...string) error { return nil }},
		runAgentFn: func(_ context.Context, _ *kelosv1alpha1.Task) (string, error) {
			t.Fatal("Agent should not be invoked when secret is missing")
			return "", nil
		},
	}

	err := r.processTask(context.Background(), "test-task")
	if !errors.Is(err, errTokenExpired) {
		t.Fatalf("Expected errTokenExpired for missing secret, got: %v", err)
	}
}
