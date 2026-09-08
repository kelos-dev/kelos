package webhook

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	sigyaml "sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

func newTestGatewayHandler(t *testing.T, objs ...client.Object) *GatewayHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelos.TaskSpawner{}, &kelos.Session{}, &kelos.SessionSpawner{}).
		Build()

	tb, err := taskbuilder.NewTaskBuilder(fakeClient)
	if err != nil {
		t.Fatal(err)
	}

	return &GatewayHandler{
		client:        fakeClient,
		log:           logr.Discard(),
		taskBuilder:   tb,
		deliveryCache: NewDeliveryCache(context.Background()),
	}
}

func githubGateway(name, namespace, secretName string) *kelos.WebhookGateway {
	return &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kelos.WebhookGatewaySpec{
			GitHub: &kelos.GitHubGateway{
				SecretRef: kelos.SecretReference{Name: secretName},
			},
		},
	}
}

func linearGateway(name, namespace, secretName string) *kelos.WebhookGateway {
	return &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kelos.WebhookGatewaySpec{
			Linear: &kelos.LinearGateway{
				SecretRef: kelos.SecretReference{Name: secretName},
			},
		},
	}
}

func gitlabGateway(name, namespace, secretName string) *kelos.WebhookGateway {
	return &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kelos.WebhookGatewaySpec{
			GitLab: &kelos.GitLabGateway{
				SecretRef: kelos.SecretReference{Name: secretName},
			},
		},
	}
}

func gitlabSpawner(name, namespace, gatewayRef string) *kelos.TaskSpawner {
	glw := &kelos.GitLabWebhook{Events: []string{"merge_request"}}
	if gatewayRef != "" {
		glw.GatewayRef = &kelos.GatewayReference{Name: gatewayRef}
	}
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name)},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{GitLabWebhook: glw},
			TaskTemplate: kelos.TaskTemplate{
				Type:           "claude-code",
				Credentials:    &kelos.Credentials{Type: "api-key"},
				WorkspaceRef:   &kelos.WorkspaceReference{Name: "test-workspace"},
				PromptTemplate: "Handle merge request {{.ID}}",
			},
		},
	}
}

func hmacSecret(name, namespace, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{gatewayWebhookSecretKey: []byte(value)},
	}
}

func githubSpawner(name, namespace, gatewayRef string) *kelos.TaskSpawner {
	ghw := &kelos.GitHubWebhook{Events: []string{"issues"}}
	if gatewayRef != "" {
		ghw.GatewayRef = &kelos.GatewayReference{Name: gatewayRef}
	}
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name)},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{GitHubWebhook: ghw},
			TaskTemplate: kelos.TaskTemplate{
				Type:           "claude-code",
				Credentials:    &kelos.Credentials{Type: "api-key"},
				WorkspaceRef:   &kelos.WorkspaceReference{Name: "test-workspace"},
				PromptTemplate: "Handle issue {{.ID}}",
			},
		},
	}
}

func linearSpawner(name, namespace, gatewayRef string) *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name)},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{LinearWebhook: &kelos.LinearWebhook{
				Types:      []string{"Issue"},
				GatewayRef: &kelos.GatewayReference{Name: gatewayRef},
			}},
			TaskTemplate: kelos.TaskTemplate{
				Type:           "claude-code",
				Credentials:    &kelos.Credentials{Type: "api-key"},
				WorkspaceRef:   &kelos.WorkspaceReference{Name: "test-workspace"},
				PromptTemplate: "Handle issue {{.ID}}",
			},
		},
	}
}

func githubSessionSpawner(name, namespace, gatewayRef string) *kelos.SessionSpawner {
	spawner := newSessionSpawner(name)
	spawner.Namespace = namespace
	spawner.Spec.When.GitHubWebhook.Repository = "org/repo"
	spawner.Spec.When.GitHubWebhook.Events = []string{"issues"}
	spawner.Spec.When.GitHubWebhook.Filters = []kelos.GitHubWebhookFilter{{Event: "issues", Action: "opened"}}
	if gatewayRef != "" {
		spawner.Spec.When.GitHubWebhook.GatewayRef = &kelos.GatewayReference{Name: gatewayRef}
	}
	return spawner
}

func TestParseGatewayPath(t *testing.T) {
	tests := []struct {
		path    string
		wantNS  string
		wantNm  string
		wantErr bool
	}{
		{"/webhook/default/gh", "default", "gh", false},
		{"/webhook/team-a/acme-foo", "team-a", "acme-foo", false},
		{"/webhook/default", "", "", true},
		{"/webhook/default/gh/extra", "", "", true},
		{"/webhook//gh", "", "", true},
		{"/webhook/default/", "", "", true},
		{"/hooks/default/gh", "", "", true},
		{"/", "", "", true},
	}
	for _, tt := range tests {
		ns, name, err := parseGatewayPath(tt.path)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseGatewayPath(%q) err = %v, wantErr %v", tt.path, err, tt.wantErr)
			continue
		}
		if err == nil && (ns != tt.wantNS || name != tt.wantNm) {
			t.Errorf("parseGatewayPath(%q) = (%q, %q), want (%q, %q)", tt.path, ns, name, tt.wantNS, tt.wantNm)
		}
	}
}

func TestGatewayServeHTTP_UnknownPath404(t *testing.T) {
	g := newTestGatewayHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/webhook/default/missing", bytes.NewReader([]byte(issuesPayload)))
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("Expected 404 for unknown gateway, got %d", rr.Code)
	}
}

func TestGatewayServeHTTP_GitLabValidTokenCreatesTask(t *testing.T) {
	g := newTestGatewayHandler(t,
		gitlabGateway("gl", "default", "gl-secret"),
		hmacSecret("gl-secret", "default", testSecret),
		gitlabSpawner("gitlab-a", "default", "gl"),
		gitlabSpawner("gitlab-unbound", "default", ""),
	)

	req := httptest.NewRequest(http.MethodPost, "/webhook/default/gl", bytes.NewReader([]byte(gitlabMergeRequestPayload)))
	req.Header.Set(GitLabTokenHeader, testSecret)
	req.Header.Set(GitLabDeliveryHeader, "uuid-gl-1")
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", rr.Code)
	}
	var taskList kelos.TaskList
	if err := g.client.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task for the gateway-bound spawner only, got %d", len(taskList.Items))
	}
	if got := taskList.Items[0].Labels["kelos.dev/taskspawner"]; got != "gitlab-a" {
		t.Errorf("Expected task owned by gitlab-a, got %q", got)
	}
}

func TestGatewayServeHTTP_GitLabInvalidTokenRejected(t *testing.T) {
	g := newTestGatewayHandler(t,
		gitlabGateway("gl", "default", "gl-secret"),
		hmacSecret("gl-secret", "default", testSecret),
		gitlabSpawner("gitlab-a", "default", "gl"),
	)

	req := httptest.NewRequest(http.MethodPost, "/webhook/default/gl", bytes.NewReader([]byte(gitlabMergeRequestPayload)))
	req.Header.Set(GitLabTokenHeader, "wrong")
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401, got %d", rr.Code)
	}
}

func TestGatewayServeHTTP_GitHubValidSignatureCreatesTask(t *testing.T) {
	g := newTestGatewayHandler(t,
		githubGateway("gh", "default", "gh-secret"),
		hmacSecret("gh-secret", "default", testSecret),
		githubSpawner("spawner-a", "default", "gh"),
	)

	body := []byte(issuesPayload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/default/gh", bytes.NewReader(body))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubDeliveryHeader, "delivery-1")
	req.Header.Set(GitHubSignatureHeader, signPayload(body, []byte(testSecret)))
	rr := httptest.NewRecorder()

	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", rr.Code)
	}
	var taskList kelos.TaskList
	if err := g.client.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task created, got %d", len(taskList.Items))
	}
	if got := taskList.Items[0].Labels["kelos.dev/taskspawner"]; got != "spawner-a" {
		t.Errorf("Expected task owned by spawner-a, got %q", got)
	}
}

func TestGatewayServeHTTP_HeaderlessFallbackDeliveryIDsAreGatewayScoped(t *testing.T) {
	tests := []struct {
		name   string
		source WebhookSource
		body   []byte
	}{
		{name: "GitHub", source: GitHubSource, body: []byte(issuesPayload)},
		{name: "Linear", source: LinearSource, body: []byte(linearIssuePayload)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []client.Object{hmacSecret("webhook-secret", "default", testSecret)}
			for _, name := range []string{"first", "second"} {
				if tt.source == GitHubSource {
					objects = append(objects,
						githubGateway(name, "default", "webhook-secret"),
						githubSpawner(name+"-spawner", "default", name),
					)
				} else {
					objects = append(objects,
						linearGateway(name, "default", "webhook-secret"),
						linearSpawner(name+"-spawner", "default", name),
					)
				}
			}
			g := newTestGatewayHandler(t, objects...)

			for _, name := range []string{"first", "second"} {
				req := httptest.NewRequest(http.MethodPost, "/webhook/default/"+name, bytes.NewReader(tt.body))
				if tt.source == GitHubSource {
					req.Header.Set(GitHubEventHeader, "issues")
					req.Header.Set(GitHubSignatureHeader, signPayload(tt.body, []byte(testSecret)))
				} else {
					req.Header.Set(LinearSignatureHeader, computeHMAC(tt.body, []byte(testSecret)))
				}
				rr := httptest.NewRecorder()
				g.ServeHTTP(rr, req)
				if rr.Code != http.StatusOK {
					t.Fatalf("gateway %s returned %d, want 200", name, rr.Code)
				}
			}

			var tasks kelos.TaskList
			if err := g.client.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
				t.Fatal(err)
			}
			if len(tasks.Items) != 2 {
				t.Fatalf("created %d Tasks, want 2 distinct gateway deliveries", len(tasks.Items))
			}
		})
	}
}

func TestGatewayServeHTTP_GitHubInvalidSignature401(t *testing.T) {
	g := newTestGatewayHandler(t,
		githubGateway("gh", "default", "gh-secret"),
		hmacSecret("gh-secret", "default", testSecret),
		githubSpawner("spawner-a", "default", "gh"),
	)

	body := []byte(issuesPayload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/default/gh", bytes.NewReader(body))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, "sha256=deadbeef")
	rr := httptest.NewRecorder()

	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 for invalid signature, got %d", rr.Code)
	}
}

func TestGatewayServeHTTP_ScopesToGatewayRefAndNamespace(t *testing.T) {
	g := newTestGatewayHandler(t,
		githubGateway("gh", "default", "gh-secret"),
		hmacSecret("gh-secret", "default", testSecret),
		githubSpawner("matching", "default", "gh"),     // fires
		githubSpawner("no-ref", "default", ""),         // served by the per-source handler
		githubSpawner("other-ref", "default", "other"), // references a different gateway
		githubSpawner("cross-ns", "other-ns", "gh"),    // matching ref but wrong namespace
	)

	body := []byte(issuesPayload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/default/gh", bytes.NewReader(body))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubDeliveryHeader, "delivery-scope")
	req.Header.Set(GitHubSignatureHeader, signPayload(body, []byte(testSecret)))
	rr := httptest.NewRecorder()

	g.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", rr.Code)
	}

	var defaultTasks kelos.TaskList
	if err := g.client.List(context.Background(), &defaultTasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(defaultTasks.Items) != 1 {
		t.Fatalf("Expected exactly 1 task in default, got %d", len(defaultTasks.Items))
	}
	if got := defaultTasks.Items[0].Labels["kelos.dev/taskspawner"]; got != "matching" {
		t.Errorf("Expected only 'matching' spawner to fire, got %q", got)
	}

	var otherTasks kelos.TaskList
	if err := g.client.List(context.Background(), &otherTasks, client.InNamespace("other-ns")); err != nil {
		t.Fatal(err)
	}
	if len(otherTasks.Items) != 0 {
		t.Errorf("Expected no tasks in other-ns, got %d", len(otherTasks.Items))
	}
}

func TestGatewayServeHTTP_ScopesSessionSpawnersToGatewayRefAndNamespace(t *testing.T) {
	g := newTestGatewayHandler(t,
		githubGateway("gh", "default", "gh-secret"),
		hmacSecret("gh-secret", "default", testSecret),
		githubSessionSpawner("matching", "default", "gh"),
		githubSessionSpawner("no-ref", "default", ""),
		githubSessionSpawner("other-ref", "default", "other"),
		githubSessionSpawner("cross-ns", "other-ns", "gh"),
	)

	body := []byte(issuesPayload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/default/gh", bytes.NewReader(body))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubDeliveryHeader, "session-delivery-scope")
	req.Header.Set(GitHubSignatureHeader, signPayload(body, []byte(testSecret)))
	rr := httptest.NewRecorder()

	g.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", rr.Code)
	}

	var sessions kelos.SessionList
	if err := g.client.List(context.Background(), &sessions, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 1 {
		t.Fatalf("Expected exactly 1 session in default, got %d", len(sessions.Items))
	}
	if got := sessions.Items[0].Annotations[sessionbuilder.AnnotationSessionSpawnerName]; got != "matching" {
		t.Errorf("Expected only matching SessionSpawner to fire, got %q", got)
	}

	var otherSessions kelos.SessionList
	if err := g.client.List(context.Background(), &otherSessions, client.InNamespace("other-ns")); err != nil {
		t.Fatal(err)
	}
	if len(otherSessions.Items) != 0 {
		t.Errorf("Expected no sessions in other-ns, got %d", len(otherSessions.Items))
	}
}

func TestGatewayServeHTTP_GenericNoVerificationCreatesTask(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gen", Namespace: "default"},
		Spec:       kelos.WebhookGatewaySpec{Generic: &kelos.GenericGateway{}},
	}
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "gen-spawner", Namespace: "default", UID: types.UID("gen-spawner")},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{GenericWebhook: &kelos.GenericWebhook{
				Source:       "sentry",
				FieldMapping: map[string]string{"id": "$.id", "title": "$.title"},
				GatewayRef:   &kelos.GatewayReference{Name: "gen"},
			}},
			TaskTemplate: kelos.TaskTemplate{
				Type:           "claude-code",
				Credentials:    &kelos.Credentials{Type: "api-key"},
				PromptTemplate: "Handle {{.ID}}",
			},
		},
	}
	g := newTestGatewayHandler(t, gw, spawner)

	body := []byte(`{"id":"evt-1","title":"boom"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/default/gen", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 for unauthenticated generic gateway, got %d", rr.Code)
	}
	var taskList kelos.TaskList
	if err := g.client.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task created, got %d", len(taskList.Items))
	}
}

func TestGatewayServeHTTP_SpawnerListErrorReturns500(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	listCalls := 0
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			githubGateway("gh", "default", "gh-secret"),
			hmacSecret("gh-secret", "default", testSecret),
			githubSpawner("spawner", "default", "gh"),
		).
		WithStatusSubresource(&kelos.TaskSpawner{}, &kelos.Session{}, &kelos.SessionSpawner{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kelos.TaskSpawnerList); ok {
					listCalls++
					if listCalls == 1 {
						return fmt.Errorf("simulated API failure")
					}
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()

	tb, err := taskbuilder.NewTaskBuilder(fakeClient)
	if err != nil {
		t.Fatal(err)
	}
	g := &GatewayHandler{client: fakeClient, log: logr.Discard(), taskBuilder: tb, deliveryCache: NewDeliveryCache(context.Background())}

	body := []byte(issuesPayload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/default/gh", bytes.NewReader(body))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubDeliveryHeader, "delivery-list-err")
	req.Header.Set(GitHubSignatureHeader, signPayload(body, []byte(testSecret)))
	rr := httptest.NewRecorder()

	g.ServeHTTP(rr, req)

	// A transient List failure must surface as a retryable 5xx, not a 200 that
	// silently drops the delivery.
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("Expected 500 on spawner List failure, got %d", rr.Code)
	}

	retry := httptest.NewRequest(http.MethodPost, "/webhook/default/gh", bytes.NewReader(body))
	retry.Header.Set(GitHubEventHeader, "issues")
	retry.Header.Set(GitHubDeliveryHeader, "delivery-list-err")
	retry.Header.Set(GitHubSignatureHeader, signPayload(body, []byte(testSecret)))
	retryRecorder := httptest.NewRecorder()
	g.ServeHTTP(retryRecorder, retry)

	if retryRecorder.Code != http.StatusOK {
		t.Fatalf("Expected retry to succeed, got %d", retryRecorder.Code)
	}
	var tasks kelos.TaskList
	if err := g.client.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("Expected retry to create 1 task, got %d", len(tasks.Items))
	}
}

func TestExtractGatewayGenericDeliveryID_NamespaceScoped(t *testing.T) {
	body := []byte(`{"id":"evt-1"}`)
	sp := &kelos.TaskSpawner{
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{GenericWebhook: &kelos.GenericWebhook{
				Source:       "sentry",
				FieldMapping: map[string]string{"id": "$.id"},
			}},
		},
	}
	spawners := []*kelos.TaskSpawner{sp}

	// Same-named gateways in different namespaces must not collide in the
	// process-wide delivery cache.
	a := extractGatewayGenericDeliveryID("ns-a/gw", body, spawners)
	b := extractGatewayGenericDeliveryID("ns-b/gw", body, spawners)
	if a == b {
		t.Errorf("expected distinct delivery IDs across namespaces, both = %q", a)
	}
}

// loadSelfDevManifest decodes a self-development manifest into a typed object.
func loadSelfDevManifest(t *testing.T, file string, out interface{}) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "self-development", file))
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	if err := sigyaml.Unmarshal(data, out); err != nil {
		t.Fatalf("decoding %s: %v", file, err)
	}
}

// TestGatewayServeHTTP_SelfDevelopmentTriageCreatesTask drives the real
// self-development WebhookGateway + kelos-triage spawner end-to-end: a verified
// `issues` delivery for kelos-dev/kelos routes through the gateway and creates a
// Task for the triage spawner.
func TestGatewayServeHTTP_SelfDevelopmentTriageCreatesTask(t *testing.T) {
	const ns = "kelos-system"

	var gw kelos.WebhookGateway
	loadSelfDevManifest(t, "webhookgateway.yaml", &gw)
	gw.Namespace = ns

	var spawner kelos.TaskSpawner
	loadSelfDevManifest(t, "kelos-triage.yaml", &spawner)
	spawner.Namespace = ns
	spawner.UID = "kelos-triage-uid"

	// The self-development gateway points both secretRef and credentialsRef at
	// github-webhook-secret, so a single Secret holds both the HMAC key and the
	// outbound API token.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: gw.Spec.GitHub.SecretRef.Name, Namespace: ns},
		Data: map[string][]byte{
			gatewayWebhookSecretKey: []byte(testSecret),
			"GITHUB_TOKEN":          []byte("test-token"),
		},
	}

	g := newTestGatewayHandler(t, &gw, &spawner, secret)

	// An issue opened on kelos-dev/kelos, open, without the triage-accepted
	// label — matches the spawner's first filter.
	body := []byte(`{
		"action": "opened",
		"sender": {"login": "some-reporter"},
		"repository": {"full_name": "kelos-dev/kelos", "name": "kelos", "owner": {"login": "kelos-dev"}},
		"issue": {
			"number": 99,
			"title": "Something is broken",
			"body": "details",
			"html_url": "https://github.com/kelos-dev/kelos/issues/99",
			"state": "open",
			"labels": []
		}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/webhook/"+ns+"/kelos", bytes.NewReader(body))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubDeliveryHeader, "triage-delivery-1")
	req.Header.Set(GitHubSignatureHeader, signPayload(body, []byte(testSecret)))
	rr := httptest.NewRecorder()

	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var tasks kelos.TaskList
	if err := g.client.List(context.Background(), &tasks, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("expected 1 task from the triage spawner, got %d", len(tasks.Items))
	}
	if got := tasks.Items[0].Labels["kelos.dev/taskspawner"]; got != "kelos-triage" {
		t.Errorf("expected task owned by kelos-triage, got %q", got)
	}
}

func TestGetMatchingSpawners_SkipsGatewayBound(t *testing.T) {
	h := newTestHandler(t,
		githubSpawner("per-source", "default", ""),
		githubSpawner("gateway-bound", "default", "gh"),
	)

	spawners, err := h.getMatchingSpawners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spawners) != 1 {
		t.Fatalf("Expected 1 per-source spawner, got %d", len(spawners))
	}
	if spawners[0].Name != "per-source" {
		t.Errorf("Expected per-source spawner, got %q", spawners[0].Name)
	}
}

func TestGetMatchingSessionSpawners_SkipsGatewayBound(t *testing.T) {
	h := newTestHandler(t,
		githubSessionSpawner("per-source", "default", ""),
		githubSessionSpawner("gateway-bound", "default", "gh"),
	)

	spawners, err := h.getMatchingSessionSpawners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spawners) != 1 || spawners[0].Name != "per-source" {
		t.Fatalf("Matching SessionSpawners = %v, want per-source", spawners)
	}
}
