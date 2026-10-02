package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/consoleserver"
	"github.com/kelos-dev/kelos/internal/helmchart"
	"github.com/kelos-dev/kelos/internal/manifests"
)

func TestConsoleRoleManagement(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "internal", "manifests")}, ErrorIfCRDPathMissing: true}
	environment.ControlPlane.GetAPIServer().Configure().Set("authorization-mode", "RBAC")
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kelos-system", "team-a", "team-b"} {
		if err := kube.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	chart, err := helmchart.Render(manifests.ChartFS, map[string]interface{}{"consoleServer": map[string]interface{}{
		"enabled": true,
		"auth": map[string]interface{}{"mode": "oidc", "oidc": map[string]interface{}{
			"issuerURL": "https://identity.example", "clientID": "kelos-console", "redirectURL": "https://console.example/oauth2/callback",
			"secretName": "console-oidc", "usernamePrefix": "oidc:", "groupsPrefix": "oidc:",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(chart), 4096)
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if object.GetKind() != "ClusterRole" && object.GetKind() != "ClusterRoleBinding" && object.GetKind() != "ServiceAccount" || !strings.HasPrefix(object.GetName(), "kelos-console-") && object.GetName() != "kelos-console-server" {
			continue
		}
		if err := kube.Create(t.Context(), &object); err != nil {
			t.Fatal(err)
		}
	}
	bootstrap := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "console-administrators", Namespace: "team-a"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kelos-console-admin"},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: "oidc:alice"}},
	}
	if err := kube.Create(t.Context(), bootstrap); err != nil {
		t.Fatal(err)
	}
	serviceConfig := rest.CopyConfig(config)
	serviceConfig.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:kelos-system:kelos-console-server"}
	serviceClient, err := client.New(serviceConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(serviceConfig)
	if err != nil {
		t.Fatal(err)
	}
	server, err := consoleserver.New(consoleserver.Config{
		AuthMode: consoleserver.AuthModeOIDC,
		OIDC:     &consoleserver.OIDCConfig{ExternalURL: "https://console.example", UsernamePrefix: "oidc:", GroupsPrefix: "oidc:", Reviewer: clientset.AuthorizationV1().SubjectAccessReviews(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))},
		Client:   serviceClient, Clientset: clientset, RESTConfig: serviceConfig, DefaultNamespace: "team-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4180"
		r.Header.Set("X-Kelos-User", user)
		r.Header.Set("Origin", "https://console.example")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	waitForStatus := func(user, path string, status int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			response := request(user, http.MethodGet, path, "")
			if response.Code == status {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s %s: %d %s, want %d", user, path, response.Code, response.Body.String(), status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitForStatus("alice", "/api/admin/roles?namespace=team-a", http.StatusOK)
	waitForStatus("bob", "/api/sessions?namespace=team-a", http.StatusForbidden)
	for _, user := range []string{"alice", "bob"} {
		response := request(user, http.MethodPost, "/api/admin/roles?namespace=team-b", `{"subject":"bob","role":"admin"}`)
		if response.Code != http.StatusForbidden {
			t.Fatalf("cross-namespace grant: %d %s", response.Code, response.Body.String())
		}
	}
	response := request("alice", http.MethodPost, "/api/admin/roles?namespace=team-a", `{"subject":"bob","role":"user"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("grant User: %d %s", response.Code, response.Body.String())
	}
	waitForStatus("bob", "/api/sessions?namespace=team-a", http.StatusOK)
	response = request("bob", http.MethodPost, "/api/admin/roles?namespace=team-a", `{"subject":"bob","role":"admin"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("self-promotion: %d %s", response.Code, response.Body.String())
	}
	response = request("alice", http.MethodPost, "/api/admin/roles?namespace=team-a", `{"subject":"bob","role":"admin"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("grant Admin: %d %s", response.Code, response.Body.String())
	}
	var assignment struct {
		Binding         string `json:"binding"`
		ResourceVersion string `json:"resourceVersion"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	waitForStatus("bob", "/api/admin/roles?namespace=team-a", http.StatusOK)
	response = request("bob", http.MethodPost, "/api/admin/roles?namespace=team-a", `{"subject":"carol","role":"user"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("Admin delegates User: %d %s", response.Code, response.Body.String())
	}
	response = request("alice", http.MethodDelete, "/api/admin/roles/team-a/"+assignment.Binding+"?resourceVersion="+assignment.ResourceVersion, "")
	if response.Code != http.StatusOK {
		t.Fatalf("remove Admin: %d %s", response.Code, response.Body.String())
	}
	waitForStatus("bob", "/api/admin/roles?namespace=team-a", http.StatusForbidden)
	waitForStatus("bob", "/api/sessions?namespace=team-a", http.StatusOK)
	forbidden := bootstrap.DeepCopy()
	forbidden.Name, forbidden.ResourceVersion, forbidden.UID = "forbidden-grant", "", ""
	forbidden.RoleRef.Name = "cluster-admin"
	if err := serviceClient.Create(t.Context(), forbidden); !apierrors.IsForbidden(err) {
		t.Fatalf("Console ServiceAccount can bind an unrestricted role: %v", err)
	}
}
