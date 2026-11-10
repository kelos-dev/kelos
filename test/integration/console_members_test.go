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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/consoleserver"
	"github.com/kelos-dev/kelos/internal/helmchart"
	"github.com/kelos-dev/kelos/internal/manifests"
)

func TestConsoleMembership(t *testing.T) {
	for _, mode := range []string{consoleserver.AuthModeOIDC, consoleserver.AuthModeGitHub} {
		t.Run(mode, func(t *testing.T) { testConsoleMembership(t, mode) })
	}
}

func testConsoleMembership(t *testing.T, mode string) {
	prefix := mode + ":"

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
	auth := map[string]interface{}{"mode": mode}
	if mode == consoleserver.AuthModeOIDC {
		auth["oidc"] = map[string]interface{}{
			"issuerURL": "https://identity.example", "clientID": "kelos-console", "redirectURL": "https://console.example/oauth2/callback",
			"secretName": "console-oidc", "usernamePrefix": prefix, "groupsPrefix": prefix,
		}
	} else {
		auth["github"] = map[string]interface{}{
			"clientID": "kelos-console", "redirectURL": "https://console.example/oauth2/callback",
			"secretName": "console-github", "usernamePrefix": prefix, "groupsPrefix": prefix,
		}
	}
	chart, err := helmchart.Render(manifests.ChartFS, map[string]interface{}{"consoleServer": map[string]interface{}{
		"enabled": true, "auth": auth,
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
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: prefix + "alice"}},
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
		AuthMode:  mode,
		ProxyAuth: &consoleserver.ProxyAuthConfig{ExternalURL: "https://console.example", UsernamePrefix: prefix, GroupsPrefix: prefix, Reviewer: clientset.AuthorizationV1().SubjectAccessReviews(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))},
		Client:    serviceClient, Clientset: clientset, RESTConfig: serviceConfig, HTTPClient: http.DefaultClient, DefaultNamespace: "team-a",
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
	waitForStatus("alice", "/api/admin/members?namespace=team-a", http.StatusOK)
	waitForStatus("bob", "/api/sessions?namespace=team-a", http.StatusForbidden)
	for _, user := range []string{"alice", "bob"} {
		response := request(user, http.MethodPost, "/api/admin/members?namespace=team-b", `{"subject":"bob","role":"admin"}`)
		if response.Code != http.StatusForbidden {
			t.Fatalf("cross-namespace grant: %d %s", response.Code, response.Body.String())
		}
	}
	response := request("alice", http.MethodPost, "/api/admin/members?namespace=team-a", `{"subject":"bob","role":"user"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("grant User: %d %s", response.Code, response.Body.String())
	}
	waitForStatus("bob", "/api/sessions?namespace=team-a", http.StatusOK)
	t.Run("TaskSpawnerSuspension", func(t *testing.T) {
		spawner := &kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: "scheduled-task", Namespace: "team-a"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{Cron: &kelos.Cron{Schedule: "0 9 * * *"}},
				TaskTemplate: kelos.TaskTemplate{
					Worker:         &kelos.WorkerSpec{Type: "claude-code", Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone}},
					PromptTemplate: "Scheduled task",
				},
			},
		}
		if err := kube.Create(t.Context(), spawner); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"alice", "bob"} {
			userConfig := rest.CopyConfig(config)
			userConfig.Impersonate = rest.ImpersonationConfig{UserName: prefix + user}
			userClient, err := client.New(userConfig, client.Options{Scheme: scheme})
			if err != nil {
				t.Fatal(err)
			}
			var original kelos.TaskSpawner
			if err := userClient.Get(t.Context(), client.ObjectKeyFromObject(spawner), &original); err != nil {
				t.Fatal(err)
			}
			edited := original.DeepCopy()
			edited.Spec.TaskTemplate.PromptTemplate = "Operator-supplied template"
			if err := userClient.Patch(t.Context(), edited, client.MergeFrom(&original)); !apierrors.IsForbidden(err) {
				t.Fatalf("%s direct TaskSpawner patch: %v, want forbidden", user, err)
			}
			if err := userClient.Update(t.Context(), edited); !apierrors.IsForbidden(err) {
				t.Fatalf("%s direct TaskSpawner update: %v, want forbidden", user, err)
			}
			for _, action := range []string{"suspend", "resume"} {
				response := request(user, http.MethodPost, "/api/resources/taskspawners/team-a/scheduled-task/"+action, "")
				if response.Code != http.StatusOK {
					t.Fatalf("%s %s: %d %s", user, action, response.Code, response.Body.String())
				}
				var updated kelos.TaskSpawner
				if err := kube.Get(t.Context(), client.ObjectKeyFromObject(spawner), &updated); err != nil {
					t.Fatal(err)
				}
				if updated.Spec.Suspend == nil || *updated.Spec.Suspend != (action == "suspend") || updated.Spec.TaskTemplate.PromptTemplate != spawner.Spec.TaskTemplate.PromptTemplate {
					t.Fatalf("%s %s: spec = %#v", user, action, updated.Spec)
				}
				response = request(user, http.MethodPost, "/api/resources/taskspawners/team-b/scheduled-task/"+action, "")
				if response.Code != http.StatusForbidden {
					t.Fatalf("cross-namespace %s: %d %s", action, response.Code, response.Body.String())
				}
			}
		}
	})
	t.Run("RunTaskSpawner", func(t *testing.T) {
		contextServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer context-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("context data"))
		}))
		defer contextServer.Close()
		spawner := &kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: "manual-run", Namespace: "team-a"},
			Spec: kelos.TaskSpawnerSpec{
				When:    kelos.When{Cron: &kelos.Cron{Schedule: "0 9 * * *"}},
				Suspend: ptr.To(true),
				TaskTemplate: kelos.TaskTemplate{
					Worker:         &kelos.WorkerSpec{Type: "claude-code", Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone}},
					PromptTemplate: "{{.Reason}}: {{.Context.details}}",
					ContextSources: []kelos.ContextSource{{Name: "details", HTTP: &kelos.HTTPContextSource{
						URL: contextServer.URL, AllowInsecure: true,
						HeadersFrom: []kelos.HTTPHeaderSource{{Header: "Authorization", SecretName: "context-auth", SecretKey: "token"}},
					}}},
				},
			},
		}
		for _, object := range []client.Object{spawner, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "context-auth", Namespace: "team-a"},
			Data:       map[string][]byte{"token": []byte("Bearer context-token")},
		}, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "team-a"},
		}, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "context-auth", Namespace: "team-b"},
		}} {
			if err := kube.Create(t.Context(), object); err != nil {
				t.Fatal(err)
			}
		}
		response := request("bob", http.MethodPost, "/api/resources/taskspawners/team-a/manual-run/run", `{"values":"Reason: Operator request"}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "context-auth") {
			t.Fatalf("context fetch without a Secret grant: %d %s", response.Code, response.Body.String())
		}
		var tasks kelos.TaskList
		if err := kube.List(t.Context(), &tasks, client.InNamespace("team-a")); err != nil || len(tasks.Items) != 0 {
			t.Fatalf("Tasks after denied context fetch = %#v, error = %v", tasks.Items, err)
		}
		for _, object := range []client.Object{&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "console-context-secrets", Namespace: "team-a"},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"context-auth"}, Verbs: []string{"get"}}},
		}, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "console-context-secrets", Namespace: "team-a"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "console-context-secrets"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "kelos-console-server", Namespace: "kelos-system"}},
		}} {
			if err := kube.Create(t.Context(), object); err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			var secret corev1.Secret
			err := serviceClient.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "context-auth"}, &secret)
			if err == nil {
				break
			}
			if !apierrors.IsForbidden(err) || time.Now().After(deadline) {
				t.Fatalf("waiting for named Secret grant: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, key := range []client.ObjectKey{{Namespace: "team-a", Name: "unrelated"}, {Namespace: "team-b", Name: "context-auth"}} {
			var secret corev1.Secret
			if err := serviceClient.Get(t.Context(), key, &secret); !apierrors.IsForbidden(err) {
				t.Fatalf("Secret %s outside named namespace grant: %v", key, err)
			}
		}
		for _, user := range []string{"alice", "bob"} {
			response := request(user, http.MethodPost, "/api/resources/taskspawners/team-a/manual-run/run", `{"values":"Reason: Operator request"}`)
			if response.Code != http.StatusCreated {
				t.Fatalf("%s run: %d %s", user, response.Code, response.Body.String())
			}
			var result struct{ Name, Namespace string }
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var task kelos.Task
			if err := kube.Get(t.Context(), client.ObjectKey{Namespace: result.Namespace, Name: result.Name}, &task); err != nil {
				t.Fatal(err)
			}
			if task.Namespace != "team-a" || task.Spec.Prompt != "Operator request: context data" || task.Annotations["kelos.dev/created-from-taskspawner"] != spawner.Name {
				t.Fatalf("created Task = %#v", task)
			}
		}
		response = request("bob", http.MethodPost, "/api/resources/taskspawners/team-b/manual-run/run", `{}`)
		if response.Code != http.StatusForbidden {
			t.Fatalf("cross-namespace run: %d %s", response.Code, response.Body.String())
		}
	})
	response = request("bob", http.MethodPost, "/api/admin/members?namespace=team-a", `{"subject":"bob","role":"admin"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("self-promotion: %d %s", response.Code, response.Body.String())
	}
	getMember := func() (string, string) {
		t.Helper()
		response := request("alice", http.MethodGet, "/api/admin/members?namespace=team-a", "")
		var inventory struct {
			Members []struct {
				ID       string `json:"id"`
				Username string `json:"username"`
				Version  string `json:"version"`
			} `json:"members"`
		}
		if response.Code != http.StatusOK {
			t.Fatalf("list members: %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
			t.Fatal(err)
		}
		for _, member := range inventory.Members {
			if member.Username == prefix+"bob" {
				return member.ID, member.Version
			}
		}
		t.Fatal("bob is not a member")
		return "", ""
	}
	id, version := getMember()
	response = request("alice", http.MethodPut, "/api/admin/members/team-a/"+id+"?version="+version, `{"role":"admin"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("promote member: %d %s", response.Code, response.Body.String())
	}
	waitForStatus("bob", "/api/admin/members?namespace=team-a", http.StatusOK)
	response = request("bob", http.MethodPost, "/api/admin/members?namespace=team-a", `{"subject":"carol","role":"user"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("Admin adds member: %d %s", response.Code, response.Body.String())
	}
	id, version = getMember()
	response = request("alice", http.MethodPut, "/api/admin/members/team-a/"+id+"?version="+version, `{"role":"user"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("demote member: %d %s", response.Code, response.Body.String())
	}
	waitForStatus("bob", "/api/admin/members?namespace=team-a", http.StatusForbidden)
	waitForStatus("bob", "/api/sessions?namespace=team-a", http.StatusOK)
	response = request("bob", http.MethodGet, "/api/namespaces", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"namespaces":["team-a"]}` {
		t.Fatalf("member namespace discovery: %d %s", response.Code, response.Body.String())
	}
	for _, test := range []struct{ namespace, body string }{
		{"team-a", `{"namespaces":["team-a"]}`},
		{"team-b", `{"namespaces":[]}`},
		{"missing", `{"namespaces":[]}`},
	} {
		response = request("bob", http.MethodGet, "/api/namespaces?namespace="+test.namespace, "")
		if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != test.body {
			t.Fatalf("namespace %s discovery: %d %s", test.namespace, response.Code, response.Body.String())
		}
	}
	id, version = getMember()
	response = request("alice", http.MethodDelete, "/api/admin/members/team-a/"+id+"?version="+version, "")
	if response.Code != http.StatusOK {
		t.Fatalf("remove member: %d %s", response.Code, response.Body.String())
	}
	waitForStatus("bob", "/api/sessions?namespace=team-a", http.StatusForbidden)
	response = request("bob", http.MethodGet, "/api/namespaces", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"namespaces":[]}` {
		t.Fatalf("removed member namespace discovery: %d %s", response.Code, response.Body.String())
	}
	response = request("bob", http.MethodGet, "/api/namespaces?namespace=team-a", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"namespaces":[]}` {
		t.Fatalf("removed member selected namespace discovery: %d %s", response.Code, response.Body.String())
	}
	forbidden := bootstrap.DeepCopy()
	forbidden.Name, forbidden.ResourceVersion, forbidden.UID = "forbidden-grant", "", ""
	forbidden.RoleRef.Name = "cluster-admin"
	if err := serviceClient.Create(t.Context(), forbidden); !apierrors.IsForbidden(err) {
		t.Fatalf("Console ServiceAccount can bind an unrestricted role: %v", err)
	}
}
