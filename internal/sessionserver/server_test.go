package sessionserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestAuthenticationProtectsApplicationAndAPI(t *testing.T) {
	server := testServer(t)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/login" {
		t.Fatalf("GET / status = %d location = %q", response.Code, response.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"wrong"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid login status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"secret-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid login status = %d body = %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("authentication cookie = %#v", cookies)
	}

	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Kelos Sessions") {
		t.Fatalf("authenticated application status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestSessionFormUsesResourceSelectors(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		`id="namespace-form"`,
		`id="active-namespace"`,
		`name="namespace" required value="default" autocomplete="off" readonly>`,
		`id="credential-secret"`,
		`id="workspace-select"`,
		`id="agent-config-select"`,
		`id="selected-agent-configs"`,
		`id="session-mode-yaml"`,
		`id="session-yaml"`,
		`id="volume-claim-enabled"`,
		`<option value="opencode">OpenCode</option>`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("new Session form does not contain %s", expected)
		}
	}
}

func TestApplicationIncludesFileChangesView(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d body = %s", response.Code, response.Body.String())
	}

	for _, expected := range []string{
		`id="conversation-tab" type="button" role="tab" aria-selected="true" aria-controls="messages" tabindex="0"`,
		`id="changes-tab" type="button" role="tab" aria-selected="false" aria-controls="changes-view" tabindex="-1"`,
		`id="changes-count"`,
		`id="changes-view"`,
		`id="changes-list"`,
		`No file changes yet.`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("Session application does not contain %s", expected)
		}
	}

	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`state.fileChanges.set(file.name, file.diff)`,
		`state.diffs.set(key, block)`,
		`renderFileChangeList(block.list, block.files, openFiles)`,
		`const path = normalizeDiffPath(header.slice(prefix.length))`,
		`const rawPath = value.split('\t', 1)[0]`,
		`return new TextDecoder().decode(new Uint8Array(bytes))`,
		`!line.startsWith('+++ ')`,
		`!line.startsWith('--- ')`,
		`elements.viewTabs.addEventListener('keydown', handleViewTabKeydown)`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("file changes JavaScript does not contain %q", expected)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`.diff-card .file-change`,
		`.diff-line.added`,
		`.diff-line.removed`,
		`--diff-added-bg:#173424`,
		`--diff-removed-bg:#3a2020`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("file changes styles do not contain %q", expected)
		}
	}
}

func TestSessionFormAPICreatesPersistentSession(t *testing.T) {
	server := testServer(t)
	payload := `{
		"name":"persistent-chat",
		"namespace":"default",
		"worker":{"type":"codex","credentials":{"type":"none"}},
		"volumeClaimTemplate":{
			"accessModes":["ReadWriteOnce"],
			"storageClassName":"fast",
			"resources":{"requests":{"storage":"20Gi"}}
		}
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "persistent-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	claim := session.Spec.VolumeClaimTemplate
	if claim == nil {
		t.Fatal("volumeClaimTemplate is nil")
	}
	if len(claim.AccessModes) != 1 || claim.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("accessModes = %v", claim.AccessModes)
	}
	if claim.StorageClassName == nil || *claim.StorageClassName != "fast" {
		t.Fatalf("storageClassName = %v", claim.StorageClassName)
	}
	wantStorage := resource.MustParse("20Gi")
	if storage := claim.Resources.Requests[corev1.ResourceStorage]; storage.Cmp(wantStorage) != 0 {
		t.Fatalf("storage request = %s, want %s", storage.String(), wantStorage.String())
	}
}

func TestSessionYAMLApplyAPI(t *testing.T) {
	server := testServer(t)
	manifest := `apiVersion: kelos.dev/v1alpha2
kind: Session
metadata:
  name: yaml-chat
  labels:
    source: web
spec:
  volumeClaimTemplate:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 5Gi
  worker:
    type: codex
    credentials:
      type: none
    model: gpt-5
`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/apply?namespace=team-a", strings.NewReader(manifest))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Content-Type", "application/yaml")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "yaml-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Labels["source"] != "web" {
		t.Fatalf("Session labels = %v", session.Labels)
	}
	if session.Spec.Worker.Model != "gpt-5" {
		t.Fatalf("worker model = %q, want %q", session.Spec.Worker.Model, "gpt-5")
	}
	if session.Spec.VolumeClaimTemplate == nil {
		t.Fatal("volumeClaimTemplate is nil")
	}
	wantStorage := resource.MustParse("5Gi")
	if storage := session.Spec.VolumeClaimTemplate.Resources.Requests[corev1.ResourceStorage]; storage.Cmp(wantStorage) != 0 {
		t.Fatalf("storage request = %s, want %s", storage.String(), wantStorage.String())
	}

	updated := strings.Replace(manifest, "source: web", "source: yaml", 1)
	request = httptest.NewRequest(http.MethodPost, "/api/sessions/apply?namespace=team-a", strings.NewReader(updated))
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("reapply status = %d body = %s", response.Code, response.Body.String())
	}
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "yaml-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Labels["source"] != "yaml" {
		t.Fatalf("Session labels after reapply = %v", session.Labels)
	}
}

func TestSessionYAMLApplyAPIRejectsInvalidManifests(t *testing.T) {
	server := testServer(t)
	for _, test := range []struct {
		name       string
		manifest   string
		wantStatus int
	}{
		{
			name:       "wrong kind",
			manifest:   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: config\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "other namespace",
			manifest:   "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: chat\n  namespace: team-a\nspec:\n  worker:\n    type: codex\n    credentials:\n      type: none\n",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "unknown field",
			manifest:   "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: chat\nspec:\n  unknown: value\n  worker:\n    type: codex\n    credentials:\n      type: none\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "custom image",
			manifest:   "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: chat\nspec:\n  worker:\n    type: codex\n    credentials:\n      type: none\n    image: example.invalid/unsafe:latest\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "pod overrides",
			manifest:   "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: chat\nspec:\n  worker:\n    type: codex\n    credentials:\n      type: none\n    podOverrides:\n      serviceAccountName: kelos-controller\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "multiple documents",
			manifest:   "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: one\nspec:\n  worker:\n    type: codex\n    credentials:\n      type: none\n---\napiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: two\nspec:\n  worker:\n    type: codex\n    credentials:\n      type: none\n",
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/sessions/apply", strings.NewReader(test.manifest))
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestSessionComposerUsesOneSendAndInterruptAction(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `id="send-message" type="submit" aria-label="Send message" data-action="send"`) {
		t.Error("Session composer does not contain the send action")
	}
	if !strings.Contains(body, `id="queued-prompts"`) {
		t.Error("Session composer does not contain the queued prompts region")
	}
	if strings.Contains(body, `id="stop-session"`) {
		t.Error("Session header contains a separate interrupt action")
	}
}

func TestSessionAPIHappyPath(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"defaultNamespace":"default"`) {
		t.Fatalf("config status = %d body = %s", response.Code, response.Body.String())
	}

	payload := map[string]any{
		"name":      "chat",
		"namespace": "team-a",
		"worker": map[string]any{
			"type":        "codex",
			"credentials": map[string]string{"type": "none"},
		},
	}
	body, _ := json.Marshal(payload)
	request = httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions?namespace=team-a", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", response.Code, response.Body.String())
	}
	var sessions []sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Name != "chat" || sessions[0].Namespace != "team-a" || sessions[0].Provider != "codex" {
		t.Fatalf("listed Sessions = %#v", sessions)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/sessions/team-a/chat", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestSessionAPIRejectsUnsafeWorkerFields(t *testing.T) {
	server := testServer(t)
	for _, test := range []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{
			name:       "custom image",
			payload:    `{"name":"chat","namespace":"default","worker":{"type":"codex","credentials":{"type":"none"},"image":"example.invalid/unsafe:latest"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "pod overrides",
			payload:    `{"name":"chat","namespace":"default","worker":{"type":"codex","credentials":{"type":"none"},"podOverrides":{"serviceAccountName":"kelos-controller"}}}`,
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(test.payload))
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestSessionAPIListsRequestedNamespace(t *testing.T) {
	server := testServer(t)
	for _, namespace := range []string{"default", "team-a"} {
		session := &kelos.Session{
			ObjectMeta: metav1.ObjectMeta{Name: "chat-" + namespace, Namespace: namespace},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type:        "codex",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
			}},
		}
		if err := server.client.Create(t.Context(), session); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name          string
		path          string
		wantNamespace string
	}{
		{name: "default namespace", path: "/api/sessions", wantNamespace: "default"},
		{name: "requested namespace", path: "/api/sessions?namespace=team-a", wantNamespace: "team-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("list status = %d body = %s", response.Code, response.Body.String())
			}
			var sessions []sessionSummary
			if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 1 || sessions[0].Namespace != test.wantNamespace {
				t.Fatalf("listed Sessions = %#v", sessions)
			}
		})
	}
}

func TestSessionOptionsAPI(t *testing.T) {
	server := testServer(t)
	for _, workspace := range []kelos.Workspace{
		{ObjectMeta: metav1.ObjectMeta{Name: "zeta", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"}},
	} {
		if err := server.client.Create(t.Context(), &workspace); err != nil {
			t.Fatal(err)
		}
	}
	for _, agentConfig := range []kelos.AgentConfig{
		{ObjectMeta: metav1.ObjectMeta{Name: "tools", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"}},
	} {
		if err := server.client.Create(t.Context(), &agentConfig); err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []kelos.Session{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "codex", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "codex-duplicate", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeAPIKey,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "none", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type:        "codex",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "other-credentials"},
				},
			}},
		},
	} {
		if err := server.client.Create(t.Context(), &session); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/options", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("options status = %d body = %s", response.Code, response.Body.String())
	}
	var options sessionOptions
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	wantCredentials := []credentialOption{
		{Name: "claude-credentials", Type: kelos.CredentialTypeAPIKey, Provider: "claude-code"},
		{Name: "codex-credentials", Type: kelos.CredentialTypeOAuth, Provider: "codex"},
	}
	if len(options.Credentials) != len(wantCredentials) {
		t.Fatalf("credential options = %#v, want %#v", options.Credentials, wantCredentials)
	}
	for i := range wantCredentials {
		if options.Credentials[i] != wantCredentials[i] {
			t.Errorf("credential option %d = %#v, want %#v", i, options.Credentials[i], wantCredentials[i])
		}
	}
	if got := strings.Join(options.Workspaces, ","); got != "alpha,zeta" {
		t.Errorf("workspace options = %q, want %q", got, "alpha,zeta")
	}
	if got := strings.Join(options.AgentConfigs, ","); got != "defaults,tools" {
		t.Errorf("AgentConfig options = %q, want %q", got, "defaults,tools")
	}

	request = httptest.NewRequest(http.MethodGet, "/api/options?namespace=team-a", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("team-a options status = %d body = %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Credentials) != 1 || options.Credentials[0].Name != "other-credentials" {
		t.Errorf("team-a credential options = %#v", options.Credentials)
	}
	if got := strings.Join(options.Workspaces, ","); got != "other" {
		t.Errorf("team-a workspace options = %q, want %q", got, "other")
	}
	if got := strings.Join(options.AgentConfigs, ","); got != "other" {
		t.Errorf("team-a AgentConfig options = %q, want %q", got, "other")
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	for _, token := range []string{"", " \t"} {
		_, err := New(Config{Token: token})
		if err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Fatalf("New() token %q error = %v", token, err)
		}
	}
}

func TestNewRejectsEmptyDefaultNamespace(t *testing.T) {
	_, err := New(Config{Token: "secret-token"})
	if err == nil || !strings.Contains(err.Error(), "namespace must not be empty") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestConnectSessionBridgesReadySession(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a"},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "chat-pod"},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	bridged := make(chan struct{})
	server.bridge = func(_ context.Context, connection *sessionSocket, namespace, podName string) error {
		defer close(bridged)
		if namespace != "team-a" || podName != "chat-pod" {
			t.Errorf("bridge target = %s/%s, want team-a/chat-pod", namespace, podName)
		}
		var request map[string]any
		if err := connection.ReadJSON(&request); err != nil {
			return err
		}
		if request["type"] != "subscribe" {
			t.Errorf("bridge request type = %v, want subscribe", request["type"])
		}
		return connection.WriteJSON(map[string]any{"type": "history.end"})
	}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	header := http.Header{"Authorization": []string{"Bearer secret-token"}}
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/sessions/team-a/chat/connect", header)
	if err != nil {
		if response != nil {
			t.Fatalf("connecting WebSocket: %v (status %d)", err, response.StatusCode)
		}
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "subscribe", "since": 0}); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "history.end" {
		t.Fatalf("event type = %v, want history.end", event["type"])
	}
	select {
	case <-bridged:
	case <-time.After(time.Second):
		t.Fatal("bridge did not complete")
	}
}

func TestSessionSocketSerializesWrites(t *testing.T) {
	const writes = 32
	serverDone := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			serverDone <- err
			return
		}
		socket := &sessionSocket{Conn: connection}
		defer socket.Close()
		var wait sync.WaitGroup
		errors := make(chan error, writes)
		for i := 0; i < writes; i++ {
			wait.Add(1)
			go func(value int) {
				defer wait.Done()
				errors <- socket.WriteJSON(map[string]int{"value": value})
			}(i)
		}
		wait.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}))
	defer httpServer.Close()

	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for i := 0; i < writes; i++ {
		var message map[string]int
		if err := connection.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("concurrent writes failed: %v", err)
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	controllerClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	server, err := New(Config{
		Token:            "secret-token",
		Client:           controllerClient,
		Clientset:        &kubernetes.Clientset{},
		RESTConfig:       &rest.Config{Host: "https://kubernetes.invalid"},
		DefaultNamespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}
