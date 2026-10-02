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
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func roleTestServer(t *testing.T, reviewer AccessReviewer) *Server {
	t.Helper()
	server := oidcTestServer(t, reviewer)
	if err := rbacv1.AddToScheme(server.client.Scheme()); err != nil {
		t.Fatal(err)
	}
	for _, role := range consoleRoles {
		if err := server.client.Create(t.Context(), &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: consoleRoleName(role.Name)}}); err != nil {
			t.Fatal(err)
		}
	}
	return server
}

func roleRequest(server *Server, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	server.ServeHTTP(response, oidcRequest(method, path, body))
	return response
}

func TestConsoleRolesAssignListAndRemove(t *testing.T) {
	var checks []authorizationv1.ResourceAttributes
	server := roleTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		if review.Spec.User != "oidc:alice" || review.Spec.ResourceAttributes.Group != rbacv1.GroupName || review.Spec.ResourceAttributes.Namespace != "team-a" {
			t.Fatalf("review = %#v", review.Spec)
		}
		checks = append(checks, *review.Spec.ResourceAttributes)
		review.Status.Allowed = true
		return review, nil
	}))
	response := roleRequest(server, http.MethodPost, "/api/admin/roles?namespace=team-a", `{"subject":"bob","role":"admin"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("assign: %d %s", response.Code, response.Body.String())
	}
	if len(checks) != 2 || checks[0] != rbacAccess("create", "rolebindings", "team-a", "") || checks[1] != rbacAccess("bind", "clusterroles", "team-a", "kelos-console-admin") {
		t.Fatalf("grant checks = %#v", checks)
	}
	var assignment consoleRoleAssignment
	if err := json.Unmarshal(response.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	var binding rbacv1.RoleBinding
	key := client.ObjectKey{Namespace: "team-a", Name: assignment.Binding}
	if err := server.client.Get(t.Context(), key, &binding); err != nil {
		t.Fatal(err)
	}
	if !managedConsoleRoleBinding(&binding, "admin", "oidc:bob") || assignment.ResourceVersion == "" {
		t.Fatalf("assignment = %#v, binding = %#v", assignment, binding)
	}
	response = roleRequest(server, http.MethodPost, "/api/admin/roles?namespace=team-a", `{"subject":"bob","role":"admin"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", response.Code, response.Body.String())
	}
	response = roleRequest(server, http.MethodGet, "/api/admin/roles?namespace=team-a", "")
	var inventory consoleRoleInventory
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !inventory.Enabled || inventory.UsernamePrefix != "oidc:" || len(inventory.Roles) != 2 || len(inventory.Assignments) != 1 || !inventory.Assignments[0].CanRemove {
		t.Fatalf("inventory: %d %s", response.Code, response.Body.String())
	}
	path := "/api/admin/roles/team-a/" + assignment.Binding
	binding.Annotations = map[string]string{"note": "updated"}
	if err := server.client.Update(t.Context(), &binding); err != nil {
		t.Fatal(err)
	}
	response = roleRequest(server, http.MethodDelete, path+"?resourceVersion="+assignment.ResourceVersion, "")
	if response.Code != http.StatusConflict {
		t.Fatalf("stale removal: %d %s", response.Code, response.Body.String())
	}
	response = roleRequest(server, http.MethodDelete, path+"?resourceVersion="+binding.ResourceVersion, "")
	if response.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", response.Code, response.Body.String())
	}
	if err := server.client.Get(t.Context(), key, &rbacv1.RoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("removed binding remains: %v", err)
	}
}

func TestConsoleRolesRequireOIDCAndRBAC(t *testing.T) {
	static := testServer(t)
	response := adminRequest(static, http.MethodGet, "/api/admin/roles", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("static inventory: %d %s", response.Code, response.Body.String())
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		response = adminRequest(static, method, "/api/admin/roles", "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("static mutation: %d", response.Code)
		}
	}
	for _, verb := range []string{"list", "create", "bind", "get", "delete"} {
		for _, unavailable := range []bool{false, true} {
			t.Run(verb+"/"+map[bool]string{false: "denied", true: "unavailable"}[unavailable], func(t *testing.T) {
				server := roleTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
					if review.Spec.ResourceAttributes.Verb == verb {
						if unavailable {
							return nil, errors.New("review unavailable")
						}
						return review, nil
					}
					review.Status.Allowed = true
					return review, nil
				}))
				method, path, body := http.MethodPost, "/api/admin/roles?namespace=team-a", `{"subject":"alice","role":"admin"}`
				if verb == "list" {
					method = http.MethodGet
				} else if verb == "get" || verb == "delete" {
					method, path = http.MethodDelete, "/api/admin/roles/team-a/example?resourceVersion=1"
				}
				response := roleRequest(server, method, path, body)
				want := http.StatusForbidden
				if unavailable {
					want = http.StatusServiceUnavailable
				}
				if response.Code != want {
					t.Fatalf("status = %d, want %d: %s", response.Code, want, response.Body.String())
				}
				var bindings rbacv1.RoleBindingList
				if err := server.client.List(t.Context(), &bindings); err != nil || len(bindings.Items) != 0 {
					t.Fatalf("denied request mutated bindings: %#v, %v", bindings.Items, err)
				}
			})
		}
	}
}

func TestConsoleRolesValidateAssignments(t *testing.T) {
	server := roleTestServer(t, reviewFunc(allowReview))
	for _, body := range []string{
		`{"subject":"alice","role":"cluster-admin"}`, `{"subject":"","role":"user"}`,
		`{"subject":" alice","role":"user"}`, `{"subject":"a,b","role":"user"}`,
		`{"subject":"alice\nadmin","role":"user"}`, `{"subject":"alice","role":"user","namespace":"other"}`,
	} {
		response := roleRequest(server, http.MethodPost, "/api/admin/roles?namespace=team-a", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid assignment %s: %d %s", body, response.Code, response.Body.String())
		}
	}
	if err := server.client.Delete(t.Context(), &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "kelos-console-admin"}}); err != nil {
		t.Fatal(err)
	}
	response := roleRequest(server, http.MethodPost, "/api/admin/roles", `{"subject":"alice","role":"admin"}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing role: %d %s", response.Code, response.Body.String())
	}
}

func TestConsoleRolesPreserveExternalAssignments(t *testing.T) {
	server := roleTestServer(t, reviewFunc(allowReview))
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "team-a"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kelos-console-user"},
		Subjects: []rbacv1.Subject{
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: "oidc:alice"},
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: "another:bob"},
			{Kind: "Group", APIGroup: rbacv1.GroupName, Name: "oidc:developers"},
		},
	}
	if err := server.client.Create(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	other := binding.DeepCopy()
	other.Namespace, other.ResourceVersion = "team-b", ""
	if err := server.client.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	for _, roleName := range []string{"user", "admin", "cluster-admin"} {
		unrelated := binding.DeepCopy()
		unrelated.Name, unrelated.ResourceVersion = roleName, ""
		unrelated.RoleRef.Name = roleName
		if err := server.client.Create(t.Context(), unrelated); err != nil {
			t.Fatal(err)
		}
	}
	response := roleRequest(server, http.MethodGet, "/api/admin/roles?namespace=team-a", "")
	var inventory consoleRoleInventory
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Assignments) != 1 || inventory.Assignments[0].Username != "oidc:alice" || inventory.Assignments[0].Managed || inventory.Assignments[0].CanRemove {
		t.Fatalf("external assignments = %#v", inventory.Assignments)
	}
	response = roleRequest(server, http.MethodDelete, "/api/admin/roles/team-a/existing?resourceVersion="+binding.ResourceVersion, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("external binding removal: %d %s", response.Code, response.Body.String())
	}
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(binding), &rbacv1.RoleBinding{}); err != nil {
		t.Fatalf("external binding was removed: %v", err)
	}
}

func TestApplicationAdminRoleBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if output, err := exec.Command(node, "testdata/admin_roles_test.js").CombinedOutput(); err != nil {
		t.Fatalf("running Admin role tests: %v\n%s", err, output)
	}
}
