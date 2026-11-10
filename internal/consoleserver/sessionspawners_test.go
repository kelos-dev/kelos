package consoleserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
	"github.com/kelos-dev/kelos/internal/spawnercredentials"
)

func consoleTestSessionSpawner() *kelos.SessionSpawner {
	return &kelos.SessionSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook", Namespace: "team-a", UID: "webhook-uid"},
		Spec: kelos.SessionSpawnerSpec{
			When: kelos.SessionSpawnerWhen{GitHubWebhook: &kelos.GitHubWebhook{Events: []string{"issues"}}},
			SessionTemplate: kelos.SessionTemplate{SessionSpec: kelos.SessionSpec{
				Worker: kelos.WorkerSpec{
					Type: "codex", Model: "test-model",
					Credentials:     &kelos.Credentials{Type: kelos.CredentialTypeNone},
					WorkspaceRef:    &kelos.WorkspaceReference{Name: "repository"},
					AgentConfigRefs: []kelos.AgentConfigReference{{Name: "instructions"}},
				},
				InitialPrompt: "{{.TriggerType}}: {{.Title}}",
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					}},
				},
			}},
		},
	}
}

func TestListSessionSpawners(t *testing.T) {
	s := testServer(t)
	for _, key := range []client.ObjectKey{
		{Namespace: "team-a", Name: "triage"},
		{Namespace: "team-a", Name: "review"},
		{Namespace: "team-b", Name: "other"},
	} {
		spawner := consoleTestSessionSpawner()
		spawner.Namespace, spawner.Name = key.Namespace, key.Name
		if err := s.client.Create(t.Context(), spawner); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		namespace string
		names     []string
	}{
		{"team-a", []string{"review", "triage"}},
		{"team-b", []string{"other"}},
		{"empty", []string{}},
	} {
		t.Run(test.namespace, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/sessionspawners?namespace="+test.namespace, nil)
			r.Header.Set("Authorization", "Bearer secret-token")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var names []string
			if err := json.Unmarshal(w.Body.Bytes(), &names); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(names, test.names) {
				t.Fatalf("names = %#v, want %#v", names, test.names)
			}
		})
	}
}

func TestListSessionSpawnersAuthorization(t *testing.T) {
	for _, test := range []struct {
		name    string
		allowed bool
		err     error
		status  int
	}{
		{"allowed", true, nil, http.StatusOK},
		{"denied", false, nil, http.StatusForbidden},
		{"review unavailable", false, errors.New("unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var checks []authorizationv1.ResourceAttributes
			s := oidcTestServer(t, reviewFunc(func(_ context.Context, r *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				checks = append(checks, *r.Spec.ResourceAttributes)
				r.Status.Allowed = test.allowed
				return r, test.err
			}))
			if err := s.client.Create(t.Context(), consoleTestSessionSpawner()); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, oidcRequest(http.MethodGet, "/api/sessionspawners?namespace=team-a", ""))
			if w.Code != test.status {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			want := []authorizationv1.ResourceAttributes{access("list", "sessionspawners", "team-a", "")}
			if !reflect.DeepEqual(checks, want) {
				t.Fatalf("access checks = %#v, want %#v", checks, want)
			}
			if !test.allowed && strings.Contains(w.Body.String(), "webhook") {
				t.Fatal("unauthorized list disclosed SessionSpawner names")
			}
		})
	}
}

func TestRunSessionSpawner(t *testing.T) {
	for _, test := range []struct {
		name, values, prompt, branch string
		configure                    func(*kelos.SessionSpawner)
	}{
		{"defaults", "", "manual: Manual run of SessionSpawner webhook", "", nil},
		{"YAML values", "Title: Fix login\nNumber: 42", "manual: Fix login", "issue-42", nil},
		{"JSON values", `{"Title":"Fix login","Number":42}`, "manual: Fix login", "issue-42", nil},
		{"long name", "", "manual: Manual run of SessionSpawner " + strings.Repeat("a", 200), "", func(s *kelos.SessionSpawner) {
			s.Name = strings.Repeat("a", 200)
		}},
		{"credential selection", "", "manual: Manual run of SessionSpawner webhook", "", func(s *kelos.SessionSpawner) {
			s.Spec.SessionTemplate.Worker.Credentials = nil
			s.Spec.Credentials = []kelos.SpawnerCredential{{Name: "account", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "account-secret"}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testServer(t)
			spawner := consoleTestSessionSpawner()
			if test.branch != "" {
				spawner.Spec.SessionTemplate.InitialBranch = "issue-{{.Number}}"
			}
			if test.configure != nil {
				test.configure(spawner)
			}
			if err := s.client.Create(t.Context(), spawner); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]string{"values": test.values})
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for range 2 {
				r := httptest.NewRequest(http.MethodPost, "/api/resources/sessionspawners/team-a/"+spawner.Name+"/run", strings.NewReader(string(body)))
				r.Header.Set("Authorization", "Bearer secret-token")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != http.StatusCreated {
					t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
				}
				var result sessionSummary
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Namespace != "team-a" || result.Provider != "codex" || names[result.Name] {
					t.Fatalf("created Session = %#v", result)
				}
				names[result.Name] = true
				if len(result.Name) > 63 || len(validation.IsDNS1123Subdomain(result.Name)) != 0 || !strings.Contains(result.Name, "-manual-") {
					t.Fatalf("invalid Session name %q", result.Name)
				}
				var session kelos.Session
				if err := s.client.Get(t.Context(), client.ObjectKey{Namespace: result.Namespace, Name: result.Name}, &session); err != nil {
					t.Fatal(err)
				}
				wantSpec := spawner.Spec.SessionTemplate.SessionSpec.DeepCopy()
				wantSpec.InitialPrompt = test.prompt
				wantSpec.InitialBranch = test.branch
				if test.name == "credential selection" {
					wantSpec.Worker.Credentials = &kelos.Credentials{Type: kelos.CredentialTypeOAuth, SecretRef: &kelos.SecretReference{Name: "account-secret"}}
					if session.Labels[spawnercredentials.AssignmentLabel] != "account" {
						t.Fatalf("credential assignment = %#v", session.Labels)
					}
				}
				if !equality.Semantic.DeepEqual(session.Spec, *wantSpec) {
					t.Fatalf("Session spec = %#v, want %#v", session.Spec, *wantSpec)
				}
				if !metav1.IsControlledBy(&session, spawner) || session.Labels[sessionbuilder.LabelSessionSpawner] != string(spawner.UID) || session.Annotations[sessionbuilder.AnnotationSessionSpawnerName] != spawner.Name {
					t.Fatalf("Session ownership = %#v", session.ObjectMeta)
				}
				if session.Annotations["kelos.dev/trigger-type"] != "manual" {
					t.Fatalf("annotations = %#v", session.Annotations)
				}
				if _, err := time.Parse(time.RFC3339, session.Annotations["kelos.dev/trigger-time"]); err != nil {
					t.Fatal(err)
				}
			}
			var unchanged kelos.SessionSpawner
			if err := s.client.Get(t.Context(), client.ObjectKeyFromObject(spawner), &unchanged); err != nil {
				t.Fatal(err)
			}
			if !equality.Semantic.DeepEqual(unchanged.Spec, spawner.Spec) || !equality.Semantic.DeepEqual(unchanged.Status, spawner.Status) {
				t.Fatal("manual run modified SessionSpawner spec or delivery status")
			}
		})
	}
}

func TestRunSessionSpawnerRejectsInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"missing", `{}`, http.StatusNotFound},
		{"invalid request", `{`, http.StatusBadRequest},
		{"trailing JSON", `{} {}`, http.StatusBadRequest},
		{"invalid values", `{"values":"Title: ["}`, http.StatusBadRequest},
		{"array values", `{"values":"[one, two]"}`, http.StatusBadRequest},
		{"scalar values", `{"values":"hello"}`, http.StatusBadRequest},
		{"null values", `{"values":"null"}`, http.StatusBadRequest},
		{"missing prompt value", `{}`, http.StatusBadRequest},
		{"missing branch value", `{}`, http.StatusBadRequest},
		{"conflicting credentials", `{}`, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testServer(t)
			if test.name != "missing" {
				spawner := consoleTestSessionSpawner()
				switch test.name {
				case "missing prompt value":
					spawner.Spec.SessionTemplate.InitialPrompt = "{{.Missing}}"
				case "missing branch value":
					spawner.Spec.SessionTemplate.InitialBranch = "{{.Missing}}"
				case "conflicting credentials":
					spawner.Spec.Credentials = []kelos.SpawnerCredential{{Name: "account", Type: kelos.CredentialTypeNone}}
				}
				if err := s.client.Create(t.Context(), spawner); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(http.MethodPost, "/api/resources/sessionspawners/team-a/webhook/run", strings.NewReader(test.body))
			r.Header.Set("Authorization", "Bearer secret-token")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != test.status || !strings.Contains(w.Body.String(), "webhook") {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var sessions kelos.SessionList
			if err := s.client.List(t.Context(), &sessions); err != nil || len(sessions.Items) != 0 {
				t.Fatalf("Sessions = %#v, error = %v", sessions.Items, err)
			}
		})
	}
}

func TestRunSessionSpawnerAuthorization(t *testing.T) {
	for _, test := range []struct {
		name, deniedResource string
		status               int
	}{
		{"allowed", "", http.StatusCreated},
		{"cannot read spawner", "sessionspawners", http.StatusForbidden},
		{"cannot create session", "sessions", http.StatusForbidden},
		{"review unavailable", "error", http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var checks []authorizationv1.ResourceAttributes
			s := oidcTestServer(t, reviewFunc(func(_ context.Context, r *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				if test.deniedResource == "error" {
					return nil, errors.New("unavailable")
				}
				checks = append(checks, *r.Spec.ResourceAttributes)
				r.Status.Allowed = r.Spec.ResourceAttributes.Resource != test.deniedResource
				return r, nil
			}))
			if err := s.client.Create(t.Context(), consoleTestSessionSpawner()); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, oidcRequest(http.MethodPost, "/api/resources/sessionspawners/team-a/webhook/run", `{}`))
			if w.Code != test.status {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if test.status == http.StatusCreated {
				want := []authorizationv1.ResourceAttributes{access("get", "sessionspawners", "team-a", "webhook"), access("create", "sessions", "team-a", "")}
				if !reflect.DeepEqual(checks, want) {
					t.Fatalf("access checks = %#v, want %#v", checks, want)
				}
			}
			var sessions kelos.SessionList
			if err := s.client.List(t.Context(), &sessions); err != nil {
				t.Fatal(err)
			}
			if test.status != http.StatusCreated && len(sessions.Items) != 0 {
				t.Fatal("unauthorized run created a Session")
			}
		})
	}
}
