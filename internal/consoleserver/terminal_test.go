package consoleserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	executil "k8s.io/utils/exec"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionreset"
)

type terminalTestExecutor struct {
	stream func(context.Context, remotecommand.StreamOptions) error
}

func (e terminalTestExecutor) Stream(options remotecommand.StreamOptions) error {
	return e.StreamWithContext(context.Background(), options)
}

func (e terminalTestExecutor) StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error {
	return e.stream(ctx, options)
}

func terminalTestServer(t *testing.T, session *kelos.Session) *Server {
	t.Helper()
	s := testServer(t)
	var err error
	s.clientset, err = kubernetes.NewForConfig(s.restConfig)
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		s.client = fake.NewClientBuilder().WithScheme(s.client.Scheme()).WithObjects(session).Build()
	}
	return s
}

func terminalTestSession() *kelos.Session {
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a"},
		Status:     kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "chat-pod"},
	}
}

func dialTerminal(t *testing.T, server *Server) *websocket.Conn {
	t.Helper()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/sessions/team-a/chat/exec",
		http.Header{"Authorization": []string{"Bearer secret-token"}, "Origin": []string{httpServer.URL}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return connection
}

func TestSessionTerminalStreamsInputOutputAndResize(t *testing.T) {
	s := terminalTestServer(t, terminalTestSession())
	input := "echo héllo\t\r\x03"
	output := []byte("\x1b[32mhéllo\x1b[0m\r\n")
	s.terminalExecutor = func(config *rest.Config, method string, target *url.URL) (remotecommand.Executor, error) {
		if config != s.restConfig || method != http.MethodPost || target.Path != "/api/v1/namespaces/team-a/pods/chat-pod/exec" {
			t.Errorf("exec target = %s %s", method, target)
		}
		query := target.Query()
		if query.Get("container") != kelos.AgentContainerName || query.Get("stdin") != "true" ||
			query.Get("stdout") != "true" || query.Get("tty") != "true" || query.Get("stderr") == "true" {
			t.Errorf("exec options = %v", query)
		}
		if !reflect.DeepEqual(query["command"], []string{"/bin/sh", "-c", "export TERM=xterm-256color; if [ -x /bin/bash ]; then exec /bin/bash -i; fi; exec /bin/sh -i"}) {
			t.Errorf("command = %v", query["command"])
		}
		return terminalTestExecutor{stream: func(ctx context.Context, options remotecommand.StreamOptions) error {
			if !options.Tty || options.Stderr != nil {
				t.Error("stream must use a TTY with merged output")
			}
			for {
				size := options.TerminalSizeQueue.Next()
				if size == nil {
					return errors.New("resize stream ended")
				}
				if size.Width == 120 && size.Height == 40 {
					break
				}
			}
			data := make([]byte, len(input))
			if _, err := io.ReadFull(options.Stdin, data); err != nil {
				return err
			}
			if string(data) != input {
				t.Errorf("stdin = %q, want %q", data, input)
			}
			_, err := options.Stdout.Write(output)
			return err
		}}, nil
	}
	connection := dialTerminal(t, s)
	if err := connection.WriteJSON(map[string]any{"type": "resize", "cols": 120, "rows": 40}); err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, []byte(input)); err != nil {
		t.Fatal(err)
	}
	messageType, data, err := connection.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || string(data) != string(output) {
		t.Fatalf("terminal output = %d %q, error = %v", messageType, data, err)
	}
	var event map[string]string
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "exit" {
		t.Fatalf("completion = %v", event)
	}
}

func TestSessionTerminalRejectsUnavailableSessions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*kelos.Session)
	}{
		{name: "pending", change: func(s *kelos.Session) { s.Status.Phase = kelos.SessionPhasePending }},
		{name: "suspended", change: func(s *kelos.Session) { s.Status.Phase = kelos.SessionPhaseSuspended }},
		{name: "failed", change: func(s *kelos.Session) { s.Status.Phase = kelos.SessionPhaseFailed }},
		{name: "missing pod", change: func(s *kelos.Session) { s.Status.PodName = "" }},
		{name: "suspending", change: func(s *kelos.Session) { s.Spec.Suspend = ptr.To(true) }},
		{name: "resetting", change: func(s *kelos.Session) { s.Annotations = map[string]string{sessionreset.RequestAnnotation: "requested"} }},
		{name: "deleting", change: func(s *kelos.Session) {
			s.DeletionTimestamp = ptr.To(metav1.Now())
			s.Finalizers = []string{"kelos.dev/session"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := terminalTestSession()
			test.change(session)
			s := terminalTestServer(t, session)
			request := httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/chat/exec", nil)
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "chat") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSessionTerminalRequiresAuthenticationAndSameOrigin(t *testing.T) {
	s := terminalTestServer(t, terminalTestSession())
	s.terminalExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
		return terminalTestExecutor{stream: func(context.Context, remotecommand.StreamOptions) error {
			t.Error("unauthorized terminal must not start")
			return nil
		}}, nil
	}
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	for _, test := range []struct {
		name   string
		header http.Header
		status int
	}{
		{name: "no authentication", status: http.StatusUnauthorized},
		{name: "foreign origin", header: http.Header{"Authorization": []string{"Bearer secret-token"}, "Origin": []string{"https://other.example"}}, status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/sessions/team-a/chat/exec", test.header)
			if connection != nil {
				_ = connection.Close()
			}
			if response != nil {
				defer response.Body.Close()
			}
			if err == nil || response == nil || response.StatusCode != test.status {
				t.Fatalf("handshake = %v, error = %v", response, err)
			}
		})
	}
}

func TestSessionTerminalReportsHTTPFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		session *kelos.Session
		status  int
	}{
		{name: "missing session", status: http.StatusNotFound},
		{name: "exec setup failed", session: terminalTestSession(), status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := terminalTestServer(t, test.session)
			s.terminalExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
				return nil, errors.New("Kubernetes transport is unavailable")
			}
			request := httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/chat/exec", nil)
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), "chat") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSessionTerminalDisconnectCancelsExec(t *testing.T) {
	s := terminalTestServer(t, terminalTestSession())
	started := make(chan struct{})
	stopped := make(chan struct{})
	s.terminalExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
		return terminalTestExecutor{stream: func(ctx context.Context, options remotecommand.StreamOptions) error {
			close(started)
			for {
				size := options.TerminalSizeQueue.Next()
				if size == nil {
					return ctx.Err()
				}
				if size.Width == 120 && size.Height == 40 {
					break
				}
			}
			if _, err := options.Stdout.Write([]byte("resized")); err != nil {
				return err
			}
			<-ctx.Done()
			_, err := io.ReadAll(options.Stdin)
			if err == nil {
				t.Error("stdin must close when the client disconnects")
			}
			close(stopped)
			return ctx.Err()
		}}, nil
	}
	connection := dialTerminal(t, s)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal did not start")
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, []byte("pending input")); err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteJSON(map[string]any{"type": "resize", "cols": 120, "rows": 40}); err != nil {
		t.Fatal(err)
	}
	_, output, err := connection.ReadMessage()
	if err != nil || string(output) != "resized" {
		t.Fatalf("resize while stdin is unread = %q, error = %v", output, err)
	}
	_ = connection.Close()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("exec did not stop after disconnect")
	}
}

func TestSessionTerminalStopsWhenInputBufferIsFull(t *testing.T) {
	s := terminalTestServer(t, terminalTestSession())
	stopped := make(chan struct{})
	s.terminalExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
		return terminalTestExecutor{stream: func(ctx context.Context, options remotecommand.StreamOptions) error {
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		}}, nil
	}
	connection := dialTerminal(t, s)
	for range 33 {
		if err := connection.WriteMessage(websocket.BinaryMessage, []byte("input")); err != nil {
			t.Fatal(err)
		}
	}
	var event map[string]string
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "error" || !strings.Contains(event["text"], "terminal input buffer is full") {
		t.Fatalf("input overflow event = %v", event)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("terminal reported completion before exec stopped")
	}
}

func TestSessionTerminalReportsNonzeroShellExit(t *testing.T) {
	s := terminalTestServer(t, terminalTestSession())
	s.terminalExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
		return terminalTestExecutor{stream: func(context.Context, remotecommand.StreamOptions) error {
			return executil.CodeExitError{Code: 1, Err: errors.New("command terminated with exit code 1")}
		}}, nil
	}
	connection := dialTerminal(t, s)
	var event map[string]string
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "exit" || event["text"] != "" {
		t.Fatalf("shell exit event = %v", event)
	}
}

func TestTerminalInputPreservesChunksAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := &terminalInput{ctx: ctx, chunks: make(chan []byte, 2)}
	input.chunks <- []byte("first")
	input.chunks <- []byte("second")
	var output strings.Builder
	for output.Len() < len("firstsecond") {
		data := make([]byte, 3)
		n, err := input.Read(data)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(data[:n])
	}
	if output.String() != "firstsecond" {
		t.Fatalf("terminal input = %q", output.String())
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := input.Read(make([]byte, 1))
		readDone <- err
	}()
	cancel()
	select {
	case err := <-readDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled stdin read = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stdin read did not stop after cancellation")
	}
}

func TestSessionTerminalReportsStreamFailure(t *testing.T) {
	s := terminalTestServer(t, terminalTestSession())
	s.terminalExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
		return terminalTestExecutor{stream: func(context.Context, remotecommand.StreamOptions) error {
			return errors.New("pod is unavailable")
		}}, nil
	}
	connection := dialTerminal(t, s)
	var event map[string]string
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "error" || !strings.Contains(event["text"], "chat") || !strings.Contains(event["text"], "pod is unavailable") {
		t.Fatalf("error event = %v", event)
	}
}

func TestSessionTerminalRejectsInvalidResize(t *testing.T) {
	for _, message := range []string{`{"type":"resize","cols":0,"rows":24}`, `{"type":"resize","cols":65536,"rows":24}`, `{"type":"input"}`, `invalid`} {
		t.Run(message, func(t *testing.T) {
			s := terminalTestServer(t, terminalTestSession())
			s.terminalExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
				return terminalTestExecutor{stream: func(ctx context.Context, options remotecommand.StreamOptions) error {
					<-ctx.Done()
					return ctx.Err()
				}}, nil
			}
			connection := dialTerminal(t, s)
			if err := connection.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
				t.Fatal(err)
			}
			var event map[string]string
			if err := connection.ReadJSON(&event); err != nil {
				t.Fatal(err)
			}
			if event["type"] != "error" || !strings.Contains(event["text"], "invalid terminal resize") {
				t.Fatalf("error event = %v", event)
			}
		})
	}
}

func TestApplicationTerminalBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if output, err := exec.Command(node, "testdata/terminal_test.js").CombinedOutput(); err != nil {
		t.Fatalf("terminal behavior: %v\n%s", err, output)
	}
}

func TestSessionTerminalShellCompletion(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("/bin/bash is not installed")
	}
	s := terminalTestServer(t, terminalTestSession())
	var command []string
	s.terminalExecutor = func(_ *rest.Config, _ string, target *url.URL) (remotecommand.Executor, error) {
		command = target.Query()["command"]
		return nil, errors.New("capture shell command")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/chat/exec", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	s.ServeHTTP(httptest.NewRecorder(), request)
	if len(command) == 0 {
		t.Fatal("Session terminal did not request a shell")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	args := append([]string{"testdata/terminal_shell_test.py"}, command...)
	if output, err := exec.CommandContext(ctx, python, args...).CombinedOutput(); err != nil {
		t.Fatalf("shell completion: %v\n%s", err, output)
	}
	output, err := exec.CommandContext(ctx, python, "testdata/terminal_shell_test.py", "/bin/sh", "-c", "printf 'startup failed\\n'; exit 1").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Shell exited before its prompt: b'startup failed\\r\\n'") {
		t.Fatalf("shell exit diagnostics: %v\n%s", err, output)
	}
}

func TestTerminalStylesUsePerPageNonce(t *testing.T) {
	s := testServer(t)
	previous := ""
	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", "Bearer secret-token")
		response := httptest.NewRecorder()
		s.ServeHTTP(response, request)
		match := regexp.MustCompile(`<meta name="terminal-style-nonce" content="([A-Za-z0-9]+)">`).FindStringSubmatch(response.Body.String())
		if response.Code != http.StatusOK || len(match) != 2 {
			t.Fatal("Console page is missing its terminal style nonce")
		}
		policy := response.Header().Get("Content-Security-Policy")
		if match[1] == previous || !strings.Contains(policy, "style-src 'self' 'nonce-"+match[1]+"'") || strings.Contains(policy, "unsafe-inline") {
			t.Fatalf("invalid terminal style policy: %s", policy)
		}
		previous = match[1]
	}
}
