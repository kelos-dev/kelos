package sessionserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionreset"
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
		`id="session-source"`,
		`id="session-source-status"`,
		`name="namespace" required value="default" autocomplete="off" readonly>`,
		`id="credential-secret"`,
		`id="workspace-select"`,
		`name="initialBranch" id="session-initial-branch"`,
		`name="initialPrompt" id="session-initial-prompt"`,
		`With emptyDir, Pod replacement loses history and may submit this prompt again; use a persistent volume to prevent replay.`,
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

func TestSessionSourceJavaScriptPreservesSelectedSource(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(source)
	for description, expected := range map[string]string{
		"shared loading and submission guard": "function updateCreationBusyState() {\n  const busy = state.sourceLoading || state.creatingSession;\n  elements.sessionSource.disabled = busy;\n  elements.createButton.disabled = busy;\n}",
		"submit loading guard":                "if (state.sourceLoading || state.creatingSession) return;",
		"namespace invalidation reset":        "state.sourceGeneration += 1;\n  setSourceLoading(false);",
		"explicit StorageClass tracking":      "state.sourceStorageClassNamePresent = Boolean(claim && 'storageClassName' in claim);",
		"explicit empty StorageClass copy":    "if (storageClassName || state.sourceStorageClassNamePresent) {\n          payload.volumeClaimTemplate.storageClassName = storageClassName;\n        }",
		"advanced reference warning":          "in YAML for additional namespace-scoped references.",
		"source initial branch population":    "elements.form.elements.initialBranch.value = manifest.spec.initialBranch || '';",
		"source initial prompt population":    "elements.form.elements.initialPrompt.value = manifest.spec.initialPrompt || '';",
		"initial branch form submission":      "const initialBranch = values.get('initialBranch').trim();\n      if (initialBranch) payload.initialBranch = initialBranch;",
		"initial prompt form submission":      "const initialPrompt = values.get('initialPrompt');\n      if (initialPrompt.trim()) payload.initialPrompt = initialPrompt;",
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("Session source JavaScript is missing %s: %s", description, expected)
		}
	}
	if strings.Contains(javascript, "No namespace-scoped references.") {
		t.Error("Session source JavaScript claims there are no namespace-scoped references without checking advanced settings")
	}
}

func TestSessionJavaScriptCachesIncrementalHistory(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(source)
	for description, expected := range map[string]string{
		"bounded Session view cache":       "const maxCachedSessionViews = 5;",
		"Session incarnation cache key":    "function sessionViewKey(session) {",
		"recreated Session invalidation":   "state.selected.uid !== current.uid",
		"view state preservation":          "function saveCurrentSessionView() {",
		"incremental history subscription": "historyBounds: true",
		"journal identity subscription":    "journalId: state.currentView?.journalID || '',",
		"stale history reset":              "if (event.reset ||",
		"replaced journal reset":           "state.currentView.journalID !== event.journalId",
		"deferred history rendering":       "function finishHistoryReplay() {",
		"selection bottom pin":             "state.pinHistoryToBottom = true;",
		"frame-batched bottom anchor":      "function scheduleBottomAnchor() {",
		"instant bottom positioning":       "elements.messages.scrollTop = elements.messages.scrollHeight;",
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("Session JavaScript is missing %s: %s", description, expected)
		}
	}
}

func TestSessionSummaryIncludesUID(t *testing.T) {
	summary := summarize(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Name: "chat", Namespace: "team-a", UID: types.UID("session-incarnation"),
	}})
	if summary.UID != "session-incarnation" {
		t.Fatalf("Session summary UID = %q, want session-incarnation", summary.UID)
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

func TestApplicationRendersMarkdownSafely(t *testing.T) {
	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`const pre = document.createElement('pre')`,
		`const code = document.createElement('code')`,
		`if (/^[a-z0-9_+-]+$/i.test(language))`,
		`code.textContent = content`,
		"code.className = `language-${language.toLowerCase()}`",
		`const paragraph = document.createElement('p')`,
		"const element = document.createElement(`h${heading[1].length}`)",
		`const list = document.createElement(firstItem.ordered ? 'ol' : 'ul')`,
		`const blockquote = document.createElement('blockquote')`,
		`const table = document.createElement('table')`,
		`const header = document.createElement('th')`,
		`const cell = document.createElement('td')`,
		`const element = document.createElement(tags[0])`,
		`if (url.protocol !== 'http:' && url.protocol !== 'https:') return false`,
		`appendInlineMarkdown(link, label, depth + 1, false, scanBudget)`,
		`completedAssistantText(event.text, state.assistantTextByTurn.get(key))`,
		`state.assistantTextByTurn.set(key, text)`,
		`renderMessageMarkdown(bubble, state.assistantTextByTurn.get(key) || '')`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Markdown rendering JavaScript does not contain %q", expected)
		}
	}
	for _, unsafe := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML"} {
		if strings.Contains(string(javascript), unsafe) {
			t.Errorf("Markdown rendering JavaScript contains unsafe DOM API %q", unsafe)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`.message-bubble h1, .message-bubble h2`,
		`.message-bubble blockquote {`,
		`.message-bubble .markdown-table-container {`,
		`.message-bubble table {`,
		`.message-bubble th, .message-bubble td {`,
		`.message-bubble .table-align-right {`,
		`.message-bubble .inline-code {`,
		`.message-bubble .task-list-item { list-style: none;`,
		`display: grid; grid-template-columns: auto minmax(0, 1fr)`,
		`.message-bubble pre {`,
		`overflow-x: auto`,
		`content: attr(data-language)`,
		`--code-bg:#0d1410`,
		`--code-ink:#e3ebe5`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Markdown rendering styles do not contain %q", expected)
		}
	}
	if strings.Contains(string(styles), `.message-bubble .task-list {`) {
		t.Error("Markdown rendering styles suppress markers for ordinary items in mixed task lists")
	}
}

func TestApplicationMarkdownBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/markdown_renderer_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running Markdown renderer tests: %v\n%s", err, output)
	}
}

func TestApplicationSessionHistoryBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/session_history_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running Session history tests: %v\n%s", err, output)
	}
}

func TestApplicationSectionChooserBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/section_chooser_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running section chooser tests: %v\n%s", err, output)
	}
}

func TestSessionFormAPICreatesPersistentSession(t *testing.T) {
	server := testServer(t)
	payload := `{
		"name":"persistent-chat",
		"namespace":"default",
		"worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"workspace"}},
		"initialBranch":"feature/persistent-chat",
		"initialPrompt":"Investigate the issue interactively",
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
	if session.Spec.InitialBranch != "feature/persistent-chat" {
		t.Fatalf("initialBranch = %q, want %q", session.Spec.InitialBranch, "feature/persistent-chat")
	}
	if session.Spec.InitialPrompt != "Investigate the issue interactively" {
		t.Fatalf("initialPrompt = %q", session.Spec.InitialPrompt)
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
  initialBranch: feature/yaml-chat
  initialPrompt: Investigate the issue interactively
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
    effort: high
    image: example.com/codex:latest
    workspaceRef:
      name: workspace
    podOverrides:
      serviceAccountName: kelos-controller
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
	if session.Spec.Worker.Model != "gpt-5" || session.Spec.Worker.Effort != "high" || session.Spec.Worker.Image != "example.com/codex:latest" {
		t.Fatalf("worker settings = %#v", session.Spec.Worker)
	}
	if session.Spec.Worker.PodOverrides == nil || session.Spec.Worker.PodOverrides.ServiceAccountName != "kelos-controller" {
		t.Fatalf("worker Pod overrides = %#v", session.Spec.Worker.PodOverrides)
	}
	if session.Spec.InitialBranch != "feature/yaml-chat" {
		t.Fatalf("initialBranch = %q, want %q", session.Spec.InitialBranch, "feature/yaml-chat")
	}
	if session.Spec.InitialPrompt != "Investigate the issue interactively" {
		t.Fatalf("initialPrompt = %q", session.Spec.InitialPrompt)
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
	if !strings.Contains(body, `id="session-progress"`) {
		t.Error("Session composer does not contain the agent progress region")
	}
	if strings.Contains(body, `id="stop-session"`) {
		t.Error("Session header contains a separate interrupt action")
	}
}

func TestSessionResetControlWarnsAndUsesResetEndpoint(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `id="reset-session" aria-label="Reset session"`) {
		t.Fatal("Session header does not contain the reset action")
	}

	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"destructive warning":       "This permanently deletes its conversation history and all workspace changes.",
		"reset API request":         "/reset`, {method: 'POST'}",
		"reset history clearing":    "resetCurrentSessionView();\n    discardSessionView(session);",
		"reset connection blocking": "state.selected.resetting",
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session reset control is missing %s: %s", description, expected)
		}
	}
}

func TestSessionComposerKeepsDraftsPerSession(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"draft storage":           `promptDrafts: new Map()`,
		"draft save key":          `state.promptDrafts.set(sessionKey(session), elements.input.value)`,
		"draft clear key":         `state.promptDrafts.delete(sessionKey(session))`,
		"Session selection save":  "function selectSession(session) {\n  savePromptDraft(state.selected);",
		"Session draft restore":   `state.promptDrafts.get(sessionKey(session))`,
		"prompt submission clear": "state.socket.send(JSON.stringify({type: 'message', text}));\n    clearPromptDraft(state.selected);",
		"Session deletion clear":  "selectSession(null);\n    clearPromptDraft(session);",
		"composer input save":     "elements.input.addEventListener('input', () => {\n  savePromptDraft(state.selected);",
	} {
		if !strings.Contains(string(source), expected) {
			t.Errorf("Session composer is missing %s: %s", description, expected)
		}
	}
}

func TestSessionComposerAllowsMultilinePromptsOnTouchDevices(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(source)
	for description, expected := range map[string]string{
		"touch device detection":  `window.matchMedia('(pointer: coarse)').matches`,
		"touch composer hint":     "? `Tap ↑ to ${action} · Return for a new line`",
		"desktop composer hint":   ": `Enter to ${action} · Shift+Enter for a new line`;",
		"desktop-only Enter send": `!event.isComposing && !usesTouchComposer()`,
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("Session composer is missing %s: %s", description, expected)
		}
	}
}

func TestSessionUIAdaptsToPhoneViewport(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}

	for description, expected := range map[string]string{
		"edge-to-edge viewport support": `viewport-fit=cover`,
		"Android keyboard resizing":     `interactive-widget=resizes-content`,
		"dismissible sidebar backdrop":  `id="sidebar-scrim"`,
	} {
		if !strings.Contains(string(index), expected) {
			t.Errorf("Session page is missing %s: %s", description, expected)
		}
	}
	for description, expected := range map[string]string{
		"dynamic viewport height":    `height: 100dvh`,
		"landscape phone breakpoint": `(max-height: 500px) and (pointer: coarse)`,
		"two-row phone header":       `grid-template-areas: "menu heading reset delete" "tabs tabs connection connection"`,
		"48-pixel touch targets":     `.icon-button { width: 48px; height: 48px; }`,
		"phone safe-area padding":    `env(safe-area-inset-bottom)`,
		"non-zooming form fields":    `.composer textarea, .yaml-panel textarea, .form-grid input`,
		"phone-sized dialog":         `max-height: calc(100dvh - 16px`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session styles are missing %s: %s", description, expected)
		}
	}
	for description, expected := range map[string]string{
		"sidebar backdrop action": `elements.sidebarScrim.addEventListener('click', () => setSidebarOpen(false))`,
		"sidebar Escape action":   `event.key === 'Escape' && elements.sidebar.classList.contains('open')`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session behavior is missing %s: %s", description, expected)
		}
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
		"section":   "Planning",
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
	if len(sessions) != 1 || sessions[0].Name != "chat" || sessions[0].Namespace != "team-a" || sessions[0].Provider != "codex" || sessions[0].Section != "Planning" {
		t.Fatalf("listed Sessions = %#v", sessions)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/reset", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("reset status = %d body = %s", response.Code, response.Body.String())
	}
	var resetSummary sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &resetSummary); err != nil {
		t.Fatal(err)
	}
	if !resetSummary.Resetting {
		t.Fatalf("reset Session summary = %#v", resetSummary)
	}
	var resetSession kelos.Session
	if err := server.client.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &resetSession); err != nil {
		t.Fatal(err)
	}
	if resetSession.Annotations[sessionreset.RequestAnnotation] == "" {
		t.Fatal("reset endpoint did not request a Session reset")
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/sessions/team-a/chat", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestSessionSectionAPI(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "chat",
			Namespace:   "team-a",
			Annotations: map[string]string{"owner": "platform"},
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPatch, "/api/sessions/team-a/chat/section", strings.NewReader(`{"section":"  In progress  "}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("set section status = %d body = %s", response.Code, response.Body.String())
	}
	var summary sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Section != "In progress" {
		t.Fatalf("set section summary = %#v", summary)
	}

	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[sessionSectionAnnotation] != "In progress" || updated.Annotations["owner"] != "platform" {
		t.Fatalf("Session annotations after setting section = %v", updated.Annotations)
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/sessions/team-a/chat/section", strings.NewReader(`{"section":""}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("clear section status = %d body = %s", response.Code, response.Body.String())
	}
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &updated); err != nil {
		t.Fatal(err)
	}
	if _, exists := updated.Annotations[sessionSectionAnnotation]; exists || updated.Annotations["owner"] != "platform" {
		t.Fatalf("Session annotations after clearing section = %v", updated.Annotations)
	}
}

func TestNormalizeSessionSection(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		want      string
		wantError bool
	}{
		{name: "trimmed", value: "  Customer work  ", want: "Customer work"},
		{name: "empty", value: "  ", want: ""},
		{name: "unicode", value: strings.Repeat("界", maxSessionSectionLength), want: strings.Repeat("界", maxSessionSectionLength)},
		{name: "too long", value: strings.Repeat("a", maxSessionSectionLength+1), wantError: true},
		{name: "control character", value: "one\ntwo", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeSessionSection(test.value)
			if (err != nil) != test.wantError {
				t.Fatalf("normalizeSessionSection(%q) error = %v, wantError %t", test.value, err, test.wantError)
			}
			if got != test.want {
				t.Fatalf("normalizeSessionSection(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestSummarizeIncludesRuntimeStatus(t *testing.T) {
	createdAt := metav1.NewTime(time.Now().Add(-time.Hour))
	lastActivityAt := metav1.NewTime(time.Now().Add(-time.Minute))
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "chat",
			Namespace:         "default",
			CreationTimestamp: createdAt,
			Annotations:       map[string]string{sessionSectionAnnotation: "Reviews"},
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
		Status: kelos.SessionStatus{
			Phase:            kelos.SessionPhaseReady,
			LastActivityTime: &lastActivityAt,
			Branch:           "feature/session-status",
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionActive,
				Status: metav1.ConditionTrue,
				Reason: "TurnActive",
			}},
			PullRequest: &kelos.SessionPullRequest{
				URL:   "https://github.com/kelos-dev/kelos/pull/42",
				State: kelos.SessionPullRequestStateOpen,
			},
		},
	}

	summary := summarize(session)
	if summary.Active == nil || !*summary.Active || summary.Branch != session.Status.Branch || summary.PullRequest == nil || *summary.PullRequest != *session.Status.PullRequest || summary.Section != "Reviews" {
		t.Fatalf("summarize() = %#v", summary)
	}
	if summary.CreatedAt == nil || !summary.CreatedAt.Equal(&createdAt) {
		t.Fatalf("summarize() CreatedAt = %v, want %v", summary.CreatedAt, createdAt)
	}
	if summary.LastActivityAt == nil || !summary.LastActivityAt.Equal(&lastActivityAt) {
		t.Fatalf("summarize() LastActivityAt = %v, want %v", summary.LastActivityAt, lastActivityAt)
	}
}

func TestListSessionsOrdersByRecentActivityAcrossPodReplacement(t *testing.T) {
	server := testServer(t)
	now := time.Now()
	recentlyIdleTime := metav1.NewTime(now.Add(-30 * time.Minute))
	activeTime := metav1.NewTime(now.Add(-time.Hour))
	recreatedActivityTime := metav1.NewTime(now.Add(-90 * time.Minute))
	for _, session := range []*kelos.Session{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "newly-created", Namespace: "default", CreationTimestamp: metav1.NewTime(now)},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "recently-idle", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-3 * time.Hour))},
			Status: kelos.SessionStatus{LastActivityTime: &recentlyIdleTime, Conditions: []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionFalse,
				Reason:             "Idle",
				LastTransitionTime: recentlyIdleTime,
			}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "active", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-4 * time.Hour))},
			Status: kelos.SessionStatus{LastActivityTime: &activeTime, Conditions: []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionTrue,
				Reason:             "TurnActive",
				LastTransitionTime: activeTime,
			}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "recreated", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Hour))},
			Status: kelos.SessionStatus{LastActivityTime: &recreatedActivityTime, Conditions: []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionFalse,
				Reason:             "Idle",
				LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
			}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "unknown", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour))},
			Status: kelos.SessionStatus{Conditions: []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionUnknown,
				Reason:             "Unavailable",
				LastTransitionTime: metav1.NewTime(now.Add(time.Hour)),
			}}},
		},
	} {
		if err := server.client.Create(context.Background(), session); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
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
	want := []string{"newly-created", "recently-idle", "active", "recreated", "unknown"}
	if len(sessions) != len(want) {
		t.Fatalf("listed Sessions = %d, want %d", len(sessions), len(want))
	}
	for i := range want {
		if sessions[i].Name != want[i] {
			t.Fatalf("Session order = %#v, want %v", sessions, want)
		}
	}
}

func TestSessionViewsIncludeRuntimeStatus(t *testing.T) {
	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"active display status":      `if (session.active === true) return 'Active';`,
		"idle display status":        `if (session.active === false) return 'Idle';`,
		"activity status in sidebar": "activity.textContent = `· ${displayStatus}`;",
		"activity status in header":  `sessionDisplayStatus(session)`,
		"branch text":                `branch.textContent = session.branch;`,
		"validated pull request URL": `const url = safeHTTPURL(pullRequest?.url);`,
		"pull request state label":   "link.textContent = state ? `${pullRequestLabel(url)} · ${state}` : pullRequestLabel(url);",
		"pull request state color":   `if (state) link.dataset.state = state.toLowerCase();`,
		"pull request link target":   `link.target = '_blank';`,
		"sidebar pull request link":  `createPullRequestLink(session.pullRequest, 'session-item-pull-request');`,
		"header pull request link":   `createPullRequestLink(session.pullRequest, 'session-meta-pull-request');`,
		"sidebar activity timestamp": `createSessionTimestamp(session, true, 'session-item-time');`,
		"header activity timestamp":  `createSessionTimestamp(session, false, 'session-meta-time');`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session views are missing %s: %s", description, expected)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(styles), `.session-item-branch { margin-top: 5px;`) {
		t.Error("Session sidebar does not style branch information")
	}
	for state, expected := range map[string]string{
		"idle":   `.phase-dot.ready, .phase-dot.idle {`,
		"active": `.phase-dot.active {`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session views do not style %s activity", state)
		}
	}
	for state, expected := range map[string]string{
		"draft":  `.pull-request-link[data-state="draft"] { color: var(--faint); }`,
		"open":   `.pull-request-link[data-state="open"] { color: var(--pr-open); }`,
		"merged": `.pull-request-link[data-state="merged"] { color: var(--pr-merged); }`,
		"closed": `.pull-request-link[data-state="closed"] { color: var(--pr-closed); }`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session views do not style %s pull requests", state)
		}
	}
}

func TestSessionUISections(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"selected Session section control": `id="session-section" type="button"`,
		"new Session section chooser":      `name="section" id="session-section-select"`,
		"new section creation field":       `name="sectionCustom" id="session-section-custom" maxlength="64"`,
		"section assignment dialog":        `id="section-dialog"`,
		"assignment section chooser":       `name="sectionChoice" id="section-choice"`,
	} {
		if !strings.Contains(string(index), expected) {
			t.Errorf("Session page is missing %s: %s", description, expected)
		}
	}

	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"section grouping":  `const sessionsBySection = new Map();`,
		"unsectioned group": `name.textContent = section || 'Unsectioned';`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session behavior is missing %s: %s", description, expected)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"section headings": `.session-section-heading {`,
		"section control":  `.session-section-button {`,
		"section dialog":   `.section-dialog {`,
		"section field":    `.section-field {`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session styles are missing %s: %s", description, expected)
		}
	}
}

func TestSessionAPIAcceptsFullWorkerSpec(t *testing.T) {
	server := testServer(t)
	payload := `{
  "name": "full-worker",
  "namespace": "default",
  "worker": {
    "type": "codex",
    "credentials": {"type": "none"},
    "model": "gpt-5",
    "effort": "high",
    "image": "example.com/codex:latest",
    "podOverrides": {
      "serviceAccountName": "kelos-controller"
    }
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
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "full-worker"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Spec.Worker.Effort != "high" || session.Spec.Worker.Image != "example.com/codex:latest" {
		t.Fatalf("worker settings = %#v", session.Spec.Worker)
	}
	if session.Spec.Worker.PodOverrides == nil || session.Spec.Worker.PodOverrides.ServiceAccountName != "kelos-controller" {
		t.Fatalf("worker Pod overrides = %#v", session.Spec.Worker.PodOverrides)
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
	if got := strings.Join(options.Sessions, ","); got != "claude,codex,codex-duplicate,none" {
		t.Errorf("Session options = %q, want %q", got, "claude,codex,codex-duplicate,none")
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
	if got := strings.Join(options.Sessions, ","); got != "other" {
		t.Errorf("team-a Session options = %q, want %q", got, "other")
	}
}

func TestSessionSourceAPIReturnsReusableSpec(t *testing.T) {
	storageClassName := ""
	server := testServer(t)
	source := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "codex-review",
			Namespace:   "team-a",
			Labels:      map[string]string{"purpose": "review"},
			Annotations: map[string]string{"owner": "platform"},
		},
		Spec: kelos.SessionSpec{
			InitialBranch: "feature/codex-review",
			InitialPrompt: "Investigate the issue interactively",
			Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
				},
				Model:           "gpt-5",
				Effort:          "high",
				Image:           "example.com/codex:latest",
				WorkspaceRef:    &kelos.WorkspaceReference{Name: "kelos"},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "defaults"}, {Name: "review-tools"}},
				PodOverrides: &kelos.PodOverrides{Env: []corev1.EnvVar{{
					Name:  "REVIEW_MODE",
					Value: "strict",
				}}},
			},
			VolumeClaimTemplate: &corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &storageClassName,
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("20Gi"),
				}},
			},
		},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "codex-review-0"},
	}
	if err := server.client.Create(t.Context(), source); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/codex-review", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("source Session status = %d body = %s", response.Code, response.Body.String())
	}
	var detail sessionSourceDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != "codex-review" || detail.Namespace != "team-a" {
		t.Fatalf("source Session identity = %s/%s", detail.Namespace, detail.Name)
	}
	manifest := detail.Manifest
	if manifest.Metadata.Name != "" || manifest.Metadata.Namespace != "team-a" {
		t.Fatalf("Session manifest identity = %s/%s", manifest.Metadata.Namespace, manifest.Metadata.Name)
	}
	if len(manifest.Metadata.Labels) != 0 || len(manifest.Metadata.Annotations) != 0 {
		t.Fatalf("Session manifest copied source metadata: labels %v annotations %v", manifest.Metadata.Labels, manifest.Metadata.Annotations)
	}
	worker := manifest.Spec.Worker
	if manifest.Spec.InitialBranch != "feature/codex-review" || manifest.Spec.InitialPrompt != "Investigate the issue interactively" {
		t.Fatalf("Session manifest initialBranch/prompt = %q/%q", manifest.Spec.InitialBranch, manifest.Spec.InitialPrompt)
	}
	if worker.Type != "codex" || worker.Model != "gpt-5" || worker.Effort != "high" || worker.Image != "example.com/codex:latest" || worker.Credentials == nil || worker.Credentials.SecretRef.Name != "codex-credentials" {
		t.Fatalf("Session manifest worker = %#v", worker)
	}
	if worker.WorkspaceRef == nil || worker.WorkspaceRef.Name != "kelos" || len(worker.AgentConfigRefs) != 2 {
		t.Fatalf("Session manifest references = workspace %#v AgentConfigs %#v", worker.WorkspaceRef, worker.AgentConfigRefs)
	}
	if worker.PodOverrides == nil || len(worker.PodOverrides.Env) != 1 || worker.PodOverrides.Env[0].Name != "REVIEW_MODE" {
		t.Fatalf("Session manifest Pod overrides = %#v", worker.PodOverrides)
	}
	if manifest.Spec.VolumeClaimTemplate == nil {
		t.Fatal("Session manifest volumeClaimTemplate is nil")
	}
	if manifest.Spec.VolumeClaimTemplate.StorageClassName == nil || *manifest.Spec.VolumeClaimTemplate.StorageClassName != "" {
		t.Fatalf("storageClassName = %v, want explicit empty string", manifest.Spec.VolumeClaimTemplate.StorageClassName)
	}
	wantStorage := resource.MustParse("20Gi")
	if storage := manifest.Spec.VolumeClaimTemplate.Resources.Requests[corev1.ResourceStorage]; storage.Cmp(wantStorage) != 0 {
		t.Fatalf("storage request = %s, want %s", storage.String(), wantStorage.String())
	}
	for _, expected := range []string{
		"apiVersion: kelos.dev/v1alpha2",
		"kind: Session",
		`name: ""`,
		"namespace: team-a",
		"name: codex-credentials",
		"effort: high",
		"image: example.com/codex:latest",
		"name: REVIEW_MODE",
		"initialBranch: feature/codex-review",
		"initialPrompt: Investigate the issue interactively",
		`storageClassName: ""`,
		"storage: 20Gi",
	} {
		if !strings.Contains(detail.YAML, expected) {
			t.Errorf("generated Session YAML does not contain %q:\n%s", expected, detail.YAML)
		}
	}
	for _, unexpected := range []string{"codex-review-0", "purpose: review", "owner: platform", "status:"} {
		if strings.Contains(detail.YAML, unexpected) {
			t.Errorf("generated Session YAML contains source runtime data %q:\n%s", unexpected, detail.YAML)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/missing", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing source Session status = %d body = %s", response.Code, response.Body.String())
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
