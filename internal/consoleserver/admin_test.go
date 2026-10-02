package consoleserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func adminRequest(server *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func TestAdminResourceLifecycle(t *testing.T) {
	for _, test := range []struct{ resource, kind, spec, updatedSpec string }{
		{"workspaces", "Workspace", `{"repo":"https://github.com/example/repo","ref":"main"}`, `{"repo":"https://github.com/example/other"}`},
		{"agentconfigs", "AgentConfig", `{"agentsMD":"Be concise"}`, `{"agentsMD":"Write tests"}`},
		{"workerpools", "WorkerPool", `{"replicas":1,"worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"repo"}},"volumeClaimTemplate":{"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"10Gi"}}}}`, `{"replicas":0,"worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"repo"}},"volumeClaimTemplate":{"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"10Gi"}}}}`},
	} {
		t.Run(test.kind, func(t *testing.T) {
			server := testServer(t)
			path := "/api/admin/" + test.resource + "/team-a"
			manifest := `{"apiVersion":"kelos.dev/v1alpha2","kind":"` + test.kind + `","metadata":{"name":"example","labels":{"team":"a"}},"spec":` + test.spec + `}`
			response := adminRequest(server, http.MethodPost, path, manifest)
			if response.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", response.Code, response.Body.String())
			}
			definition, _ := consoleResourceDefinitionFor(test.resource)
			object := definition.New()
			key := client.ObjectKey{Namespace: "team-a", Name: "example"}
			if err := server.client.Get(t.Context(), key, object); err != nil {
				t.Fatal(err)
			}
			object.SetFinalizers([]string{"example.com/retain"})
			if err := server.client.Update(t.Context(), object); err != nil {
				t.Fatal(err)
			}

			response = adminRequest(server, http.MethodGet, path+"/example", "")
			if response.Code != http.StatusOK {
				t.Fatalf("get: %d %s", response.Code, response.Body.String())
			}
			var detail consoleResourceDetail
			if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			edited, err := decodeAdminManifest(strings.NewReader(detail.YAML))
			if err != nil {
				t.Fatal(err)
			}
			if edited.Metadata.ResourceVersion == "" || strings.Contains(detail.YAML, "finalizers:") || strings.Contains(detail.YAML, "status:") {
				t.Fatalf("editable manifest: %s", detail.YAML)
			}
			edited.Spec = json.RawMessage(test.updatedSpec)
			edited.Metadata.Labels = map[string]string{"team": "b"}
			data, err := json.Marshal(edited)
			if err != nil {
				t.Fatal(err)
			}
			response = adminRequest(server, http.MethodPut, path+"/example", string(data))
			if response.Code != http.StatusOK {
				t.Fatalf("update: %d %s", response.Code, response.Body.String())
			}
			object = definition.New()
			if err := server.client.Get(t.Context(), key, object); err != nil {
				t.Fatal(err)
			}
			if object.GetLabels()["team"] != "b" || len(object.GetFinalizers()) != 1 {
				t.Fatalf("updated metadata: %#v", object)
			}
			actual, _ := json.Marshal(object)
			var saved adminManifest
			if err := json.Unmarshal(actual, &saved); err != nil {
				t.Fatal(err)
			}
			var got, want any
			_ = json.Unmarshal(saved.Spec, &got)
			_ = json.Unmarshal([]byte(test.updatedSpec), &want)
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("saved spec = %s, want %s", gotJSON, wantJSON)
			}
			response = adminRequest(server, http.MethodPut, path+"/example", string(data))
			if response.Code != http.StatusConflict {
				t.Fatalf("stale update: %d %s", response.Code, response.Body.String())
			}
			object.SetFinalizers(nil)
			if err := server.client.Update(t.Context(), object); err != nil {
				t.Fatal(err)
			}
			response = adminRequest(server, http.MethodDelete, path+"/example", "")
			if response.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", response.Code, response.Body.String())
			}
			if err := server.client.Get(t.Context(), key, definition.New()); !apierrors.IsNotFound(err) {
				t.Fatalf("resource still exists: %v", err)
			}
		})
	}
}

func TestAdminRejectsInvalidManifests(t *testing.T) {
	valid := "apiVersion: kelos.dev/v1alpha2\nkind: Workspace\nmetadata:\n  name: repo\nspec:\n  repo: https://github.com/example/repo\n"
	for name, manifest := range map[string]string{
		"empty":              "",
		"multiple documents": valid + "---\n" + valid,
		"duplicate keys":     valid + "  repo: https://github.com/example/other\n",
		"unknown spec":       valid + "  unknown: true\n",
		"unknown field":      valid + "unknown: true\n",
		"wrong kind":         strings.Replace(valid, "Workspace", "Session", 1),
		"wrong version":      strings.Replace(valid, "v1alpha2", "v1alpha1", 1),
		"wrong namespace":    strings.Replace(valid, "  name: repo", "  name: repo\n  namespace: other", 1),
		"missing spec":       "apiVersion: kelos.dev/v1alpha2\nkind: Workspace\nmetadata:\n  name: repo\n",
		"oversized":          valid + strings.Repeat(" ", requestBodyLimit),
	} {
		t.Run(name, func(t *testing.T) {
			response := adminRequest(testServer(t), http.MethodPost, "/api/admin/workspaces/team-a", manifest)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
	for _, manifest := range []string{valid, strings.Replace(valid, "  name: repo", "  name: other\n  resourceVersion: '1'", 1)} {
		response := adminRequest(testServer(t), http.MethodPut, "/api/admin/workspaces/team-a/repo", manifest)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid update: %d %s", response.Code, response.Body.String())
		}
	}
	response := adminRequest(testServer(t), http.MethodPost, "/api/admin/sessions/team-a", valid)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unsupported resource: %d", response.Code)
	}
}

func TestAdminInventoryPermissions(t *testing.T) {
	server := oidcTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		a := review.Spec.ResourceAttributes
		if a.Namespace != "team-a" || a.Group != "kelos.dev" {
			t.Fatalf("access attributes: %#v", a)
		}
		review.Status.Allowed = a.Resource == "workspaces" && (a.Verb == "list" || a.Verb == "get" || a.Verb == "create" || a.Verb == "update" && a.Name == "editable")
		return review, nil
	}))
	for _, key := range []client.ObjectKey{{Namespace: "team-a", Name: "editable"}, {Namespace: "team-a", Name: "read-only"}, {Namespace: "other", Name: "hidden"}} {
		if err := server.client.Create(t.Context(), &kelos.Workspace{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, oidcRequest(http.MethodGet, "/api/admin?namespace=team-a", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("inventory: %d %s", response.Code, response.Body.String())
	}
	var collections []adminResourceCollection
	if err := json.Unmarshal(response.Body.Bytes(), &collections); err != nil {
		t.Fatal(err)
	}
	if len(collections) != 1 || !collections[0].CanCreate || len(collections[0].Items) != 2 {
		t.Fatalf("inventory = %#v", collections)
	}
	for _, item := range collections[0].Items {
		if !item.CanGet || item.CanDelete || item.CanUpdate != (item.Name == "editable") {
			t.Fatalf("permissions = %#v", item)
		}
	}
}

func TestAdminActionsRequireAccess(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			for _, unavailable := range []bool{false, true} {
				server := oidcTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
					if unavailable {
						return nil, errors.New("unavailable")
					}
					return review, nil
				}))
				path := "/api/admin/workspaces/team-a/repo"
				if method == http.MethodPost {
					path = "/api/admin/workspaces/team-a"
				}
				response := httptest.NewRecorder()
				server.ServeHTTP(response, oidcRequest(method, path, ""))
				want := http.StatusForbidden
				if unavailable {
					want = http.StatusServiceUnavailable
				}
				if response.Code != want {
					t.Fatalf("status = %d, want %d: %s", response.Code, want, response.Body.String())
				}
			}
		})
	}
}

func TestApplicationAdminBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if output, err := exec.Command(node, "testdata/admin_test.js").CombinedOutput(); err != nil {
		t.Fatalf("running Admin tests: %v\n%s", err, output)
	}
}
