package consoleserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func memberTestServer(t *testing.T, reviewer AccessReviewer) *Server {
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

func memberRequest(server *Server, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	server.ServeHTTP(response, oidcRequest(method, path, body))
	return response
}

func memberInventory(t *testing.T, server *Server) consoleMemberInventory {
	t.Helper()
	response := memberRequest(server, http.MethodGet, "/api/admin/members?namespace=team-a", "")
	var inventory consoleMemberInventory
	if response.Code != http.StatusOK {
		t.Fatalf("list members: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	return inventory
}

func memberURL(member consoleMember) string {
	return "/api/admin/members/team-a/" + member.ID + "?version=" + member.Version
}

func TestConsoleMembersLifecycle(t *testing.T) {
	var checks []authorizationv1.ResourceAttributes
	server := memberTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		if review.Spec.User != "oidc:alice" || review.Spec.ResourceAttributes.Group != rbacv1.GroupName || review.Spec.ResourceAttributes.Namespace != "team-a" {
			t.Fatalf("review = %#v", review.Spec)
		}
		checks = append(checks, *review.Spec.ResourceAttributes)
		review.Status.Allowed = true
		return review, nil
	}))
	response := memberRequest(server, http.MethodPost, "/api/admin/members?namespace=team-a", `{"subject":"bob","role":"user"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("add member: %d %s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(checks, []authorizationv1.ResourceAttributes{rbacAccess("list", "rolebindings", "team-a", ""), rbacAccess("create", "rolebindings", "team-a", ""), rbacAccess("bind", "clusterroles", "team-a", "kelos-console-user")}) {
		t.Fatalf("add checks = %#v", checks)
	}
	for _, role := range []string{"user", "admin"} {
		response = memberRequest(server, http.MethodPost, "/api/admin/members?namespace=team-a", `{"subject":"bob","role":"`+role+`"}`)
		if response.Code != http.StatusConflict {
			t.Fatalf("duplicate membership: %d %s", response.Code, response.Body.String())
		}
	}
	for _, role := range []string{"admin", "user"} {
		inventory := memberInventory(t, server)
		if len(inventory.Members) != 1 || !inventory.Members[0].CanChange || !inventory.Members[0].CanRemove {
			t.Fatalf("members = %#v", inventory.Members)
		}
		previous := inventory.Members[0]
		checks = nil
		response = memberRequest(server, http.MethodPut, memberURL(previous), `{"role":"`+role+`"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("change role: %d %s", response.Code, response.Body.String())
		}
		foundBind := false
		for _, check := range checks {
			foundBind = foundBind || check == rbacAccess("bind", "clusterroles", "team-a", consoleRoleName(role))
		}
		if !foundBind {
			t.Fatalf("change lacks exact bind check: %#v", checks)
		}
		var bindings rbacv1.RoleBindingList
		if err := server.client.List(t.Context(), &bindings); err != nil {
			t.Fatal(err)
		}
		if len(bindings.Items) != 1 || bindingConsoleRole(&bindings.Items[0]) != role || !managedMemberBinding(&bindings.Items[0], "oidc:bob") {
			t.Fatalf("changed bindings = %#v", bindings.Items)
		}
		response = memberRequest(server, http.MethodDelete, memberURL(previous), "")
		if response.Code != http.StatusConflict {
			t.Fatalf("stale deletion: %d %s", response.Code, response.Body.String())
		}
	}
	member := memberInventory(t, server).Members[0]
	response = memberRequest(server, http.MethodDelete, memberURL(member), "")
	if response.Code != http.StatusOK || len(memberInventory(t, server).Members) != 0 {
		t.Fatalf("remove member: %d %s", response.Code, response.Body.String())
	}
}

func TestConsoleMembersRequireProxyAuthenticationAndRBAC(t *testing.T) {
	response := adminRequest(testServer(t), http.MethodGet, "/api/admin/members", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("static inventory: %d %s", response.Code, response.Body.String())
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		response = adminRequest(testServer(t), method, "/api/admin/members", "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("static mutation: %d", response.Code)
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		for _, verb := range []string{"list", "delete", "create", "bind"} {
			if method == http.MethodDelete && (verb == "create" || verb == "bind") {
				continue
			}
			for _, unavailable := range []bool{false, true} {
				t.Run(method+"/"+verb+"/"+map[bool]string{false: "denied", true: "unavailable"}[unavailable], func(t *testing.T) {
					server := memberTestServer(t, reviewFunc(allowReview))
					binding := memberBinding("oidc:bob", "user", "team-a")
					if err := server.client.Create(t.Context(), binding); err != nil {
						t.Fatal(err)
					}
					member := memberInventory(t, server).Members[0]
					server.proxyAuth.Reviewer = reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
						if review.Spec.ResourceAttributes.Verb == verb {
							if unavailable {
								return nil, errors.New("review unavailable")
							}
							return review, nil
						}
						review.Status.Allowed = true
						return review, nil
					})
					response := memberRequest(server, method, memberURL(member), `{"role":"admin"}`)
					want := http.StatusForbidden
					if unavailable {
						want = http.StatusServiceUnavailable
					}
					if response.Code != want {
						t.Fatalf("status = %d, want %d: %s", response.Code, want, response.Body.String())
					}
					var bindings rbacv1.RoleBindingList
					if err := server.client.List(t.Context(), &bindings); err != nil || len(bindings.Items) != 1 || bindings.Items[0].Name != binding.Name {
						t.Fatalf("denied operation mutated membership: %#v %v", bindings.Items, err)
					}
				})
			}
		}
	}
}

func TestConsoleGitHubMembersLifecycle(t *testing.T) {
	server := memberTestServer(t, reviewFunc(allowReview))
	server.authMode = AuthModeGitHub
	server.proxyAuth.UsernamePrefix = "github:company:"
	server.proxyAuth.GroupsPrefix = "github:company:"
	response := memberRequest(server, http.MethodPost, "/api/admin/members?namespace=team-a", `{"subject":"OctoCat","role":"user"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("grant GitHub access: %d %s", response.Code, response.Body.String())
	}
	inventory := memberInventory(t, server)
	if !inventory.Enabled || inventory.AuthMode != AuthModeGitHub || inventory.UsernamePrefix != "github:company:" || inventory.CurrentUser != "github:company:alice" || len(inventory.Members) != 1 {
		t.Fatalf("GitHub inventory = %#v", inventory)
	}
	member := inventory.Members[0]
	if member.Username != "github:company:OctoCat" || member.Role != "user" || !member.CanChange || !member.CanRemove {
		t.Fatalf("GitHub member = %#v", member)
	}
	response = memberRequest(server, http.MethodPut, memberURL(member), `{"role":"admin"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("promote GitHub member: %d %s", response.Code, response.Body.String())
	}
	member = memberInventory(t, server).Members[0]
	var binding rbacv1.RoleBinding
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: memberBindingName("github:company:OctoCat", "admin")}, &binding); err != nil {
		t.Fatal(err)
	}
	if !managedMemberBinding(&binding, "github:company:OctoCat") || bindingConsoleRole(&binding) != "admin" {
		t.Fatalf("GitHub grant = %#v", binding)
	}
	response = memberRequest(server, http.MethodDelete, memberURL(member), "")
	if response.Code != http.StatusOK || len(memberInventory(t, server).Members) != 0 {
		t.Fatalf("remove GitHub member: %d %s", response.Code, response.Body.String())
	}
}

func TestConsoleMembersExternalAccess(t *testing.T) {
	server := memberTestServer(t, reviewFunc(allowReview))
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: "team-a"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kelos-console-admin"},
		Subjects: []rbacv1.Subject{
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: "oidc:bob"},
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: "another:alice"},
			{Kind: "Group", APIGroup: rbacv1.GroupName, Name: "oidc:developers"},
		},
	}
	if err := server.client.Create(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"user", "admin", "cluster-admin"} {
		other := binding.DeepCopy()
		other.Name, other.ResourceVersion, other.RoleRef.Name = name, "", name
		if err := server.client.Create(t.Context(), other); err != nil {
			t.Fatal(err)
		}
	}
	other := binding.DeepCopy()
	other.Namespace, other.ResourceVersion = "team-b", ""
	if err := server.client.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	inventory := memberInventory(t, server)
	if len(inventory.Members) != 1 || inventory.Members[0].Username != "oidc:bob" || inventory.Members[0].CanChange || inventory.Members[0].CanRemove || len(inventory.Groups) != 1 || inventory.Groups[0].Name != "oidc:developers" {
		t.Fatalf("external access = %#v", inventory)
	}
	response := memberRequest(server, http.MethodPost, "/api/admin/members?namespace=team-a", `{"subject":"bob","role":"user"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "access managed outside Console") {
		t.Fatalf("external member add: %d %s", response.Code, response.Body.String())
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		response := memberRequest(server, method, memberURL(inventory.Members[0]), `{"role":"user"}`)
		if response.Code != http.StatusForbidden {
			t.Fatalf("external mutation: %d %s", response.Code, response.Body.String())
		}
	}
	if err := server.client.Create(t.Context(), memberBinding("oidc:bob", "user", "team-a")); err != nil {
		t.Fatal(err)
	}
	member := memberInventory(t, server).Members[0]
	if !member.CanRemove || member.CanChange || member.Role != "admin" || len(member.Sources) != 2 {
		t.Fatalf("combined access = %#v", member)
	}
	response = memberRequest(server, http.MethodDelete, memberURL(member), "")
	if response.Code != http.StatusOK {
		t.Fatalf("remove direct membership: %d %s", response.Code, response.Body.String())
	}
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(binding), &rbacv1.RoleBinding{}); err != nil {
		t.Fatalf("external access removed: %v", err)
	}
}

type failingMemberClient struct {
	client.Client
	failCreate, failDelete bool
}

func (c failingMemberClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if c.failCreate {
		return errors.New("storage unavailable")
	}
	return c.Client.Create(ctx, object, opts...)
}
func (c failingMemberClient) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	if c.failDelete {
		return errors.New("storage unavailable")
	}
	return c.Client.Delete(ctx, object, opts...)
}

func TestConsoleMembersIncompleteChange(t *testing.T) {
	server := memberTestServer(t, reviewFunc(allowReview))
	if err := server.client.Create(t.Context(), memberBinding("oidc:bob", "user", "team-a")); err != nil {
		t.Fatal(err)
	}
	member := memberInventory(t, server).Members[0]
	original := server.client
	server.client = failingMemberClient{Client: original, failCreate: true}
	response := memberRequest(server, http.MethodPut, memberURL(member), `{"role":"admin"}`)
	if response.Code != http.StatusInternalServerError || memberInventory(t, server).Members[0].Role != "user" {
		t.Fatalf("failed grant did not preserve access: %d %s", response.Code, response.Body.String())
	}
	server.client = failingMemberClient{Client: original, failDelete: true}
	response = memberRequest(server, http.MethodPut, memberURL(member), `{"role":"admin"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("partial change: %d %s", response.Code, response.Body.String())
	}
	inventory := memberInventory(t, server)
	if len(inventory.Members) != 1 || inventory.Members[0].Role != "admin" || len(inventory.Members[0].Sources) != 2 {
		t.Fatalf("partial membership = %#v", inventory.Members)
	}
	server.client = original
	response = memberRequest(server, http.MethodPut, memberURL(inventory.Members[0]), `{"role":"user"}`)
	if response.Code != http.StatusOK || len(memberInventory(t, server).Members[0].Sources) != 1 {
		t.Fatalf("retry did not converge: %d %s", response.Code, response.Body.String())
	}
}

func TestConsoleMembersValidation(t *testing.T) {
	server := memberTestServer(t, reviewFunc(allowReview))
	for _, body := range []string{`{"subject":"bob","role":"cluster-admin"}`, `{"subject":"","role":"user"}`, `{"subject":" bob","role":"user"}`, `{"subject":"a,b","role":"user"}`, `{"subject":"a\nb","role":"user"}`, `{"subject":"bob","role":"user","namespace":"other"}`} {
		response := memberRequest(server, http.MethodPost, "/api/admin/members?namespace=team-a", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid member %s: %d %s", body, response.Code, response.Body.String())
		}
	}
	for _, id := range []string{"!", base64.RawURLEncoding.EncodeToString([]byte("other:bob"))} {
		response := memberRequest(server, http.MethodDelete, "/api/admin/members/team-a/"+id+"?version=1", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid ID: %d", response.Code)
		}
	}
	if err := server.client.Delete(t.Context(), &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "kelos-console-admin"}}); err != nil {
		t.Fatal(err)
	}
	response := memberRequest(server, http.MethodPost, "/api/admin/members", `{"subject":"bob","role":"admin"}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing role: %d %s", response.Code, response.Body.String())
	}
}

func TestConsoleNamespaceDiscovery(t *testing.T) {
	server := memberTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		attr := review.Spec.ResourceAttributes
		if attr.Group != "kelos.dev" || attr.Verb != "list" {
			t.Fatalf("namespace review: %#v", attr)
		}
		review.Status.Allowed = attr.Namespace == "team-a" && attr.Resource == "sessions" || attr.Namespace == "team-b" && attr.Resource == "workspaces"
		return review, nil
	}))
	for _, name := range []string{"team-b", "secret-team", "team-a"} {
		if err := server.client.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	response := memberRequest(server, http.MethodGet, "/api/namespaces", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"namespaces":["team-a","team-b"]}` {
		t.Fatalf("discovery: %d %s", response.Code, response.Body.String())
	}
	server.proxyAuth.Reviewer = reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		return review, nil
	})
	response = memberRequest(server, http.MethodGet, "/api/namespaces", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"namespaces":[]}` {
		t.Fatalf("no access: %d %s", response.Code, response.Body.String())
	}
	server.proxyAuth.Reviewer = reviewFunc(func(context.Context, *authorizationv1.SubjectAccessReview, metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		return nil, errors.New("unavailable")
	})
	response = memberRequest(server, http.MethodGet, "/api/namespaces", "")
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "secret-team") {
		t.Fatalf("failed authorization: %d %s", response.Code, response.Body.String())
	}
	static := testServer(t)
	static.client = server.client
	response = adminRequest(static, http.MethodGet, "/api/namespaces", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"namespaces":["secret-team","team-a","team-b"]}` {
		t.Fatalf("static namespaces: %d %s", response.Code, response.Body.String())
	}
}

func TestApplicationMemberBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if output, err := exec.Command(node, "testdata/members_test.js").CombinedOutput(); err != nil {
		t.Fatalf("running member tests: %v\n%s", err, output)
	}
}

func TestConsoleMembersDoNotRequireBindingGet(t *testing.T) {
	server := memberTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		attributes := review.Spec.ResourceAttributes
		review.Status.Allowed = attributes.Verb != "get" || attributes.Resource != "rolebindings"
		return review, nil
	}))
	if err := server.client.Create(t.Context(), memberBinding("oidc:bob", "user", "team-a")); err != nil {
		t.Fatal(err)
	}
	member := memberInventory(t, server).Members[0]
	if !member.CanChange || !member.CanRemove {
		t.Fatalf("member controls require get: %#v", member)
	}
	response := memberRequest(server, http.MethodPut, memberURL(member), `{"role":"admin"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("change without get: %d %s", response.Code, response.Body.String())
	}
	response = memberRequest(server, http.MethodDelete, memberURL(memberInventory(t, server).Members[0]), "")
	if response.Code != http.StatusOK || len(memberInventory(t, server).Members) != 0 {
		t.Fatalf("remove without get: %d %s", response.Code, response.Body.String())
	}
}
