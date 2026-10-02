package helmchart

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/kelos-dev/kelos/internal/manifests"
)

func consoleOIDCValues() map[string]interface{} {
	return map[string]interface{}{
		"enabled": true,
		"auth": map[string]interface{}{"mode": "oidc", "oidc": map[string]interface{}{
			"issuerURL": "https://identity.example", "clientID": "kelos-console",
			"redirectURL": "https://console.example/oauth2/callback", "secretName": "console-oidc",
			"usernamePrefix": "oidc:", "groupsPrefix": "oidc:",
			"reverseProxy": true, "trustedProxyIPs": []interface{}{"10.1.0.0/24"},
		}},
	}
}

func TestRenderConsoleOIDC(t *testing.T) {
	data, err := Render(manifests.ChartFS, map[string]interface{}{"consoleServer": consoleOIDCValues()})
	if err != nil {
		t.Fatal(err)
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var deployment appsv1.Deployment
	var service corev1.Service
	var headers corev1.ConfigMap
	var userRole, adminRole, serverRole rbacv1.ClusterRole
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var target interface{}
		switch object.GetKind() + "/" + object.GetName() {
		case "Deployment/kelos-console-server":
			target = &deployment
		case "Service/kelos-console-server":
			target = &service
		case "ConfigMap/kelos-console-oidc":
			target = &headers
		case "ClusterRole/kelos-console-user":
			target = &userRole
		case "ClusterRole/kelos-console-admin":
			target = &adminRole
		case "ClusterRole/kelos-console-server-role":
			target = &serverRole
		}
		if target != nil {
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, target); err != nil {
				t.Fatal(err)
			}
		}
		if object.GetKind() == "RoleBinding" || object.GetKind() == "ClusterRoleBinding" {
			name, _, _ := unstructured.NestedString(object.Object, "roleRef", "name")
			if name == "kelos-console-user" || name == "kelos-console-admin" {
				t.Fatal("chart must not bind human access")
			}
		}
	}
	if len(deployment.Spec.Template.Spec.Containers) != 2 {
		t.Fatalf("containers = %#v", deployment.Spec.Template.Spec.Containers)
	}
	console, proxy := deployment.Spec.Template.Spec.Containers[0], deployment.Spec.Template.Spec.Containers[1]
	if len(console.Ports) != 0 {
		t.Fatal("OIDC console exposes a port")
	}
	if deployment.Spec.Template.Spec.AutomountServiceAccountToken == nil || *deployment.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("OIDC pod automatically mounts its service account token")
	}
	if len(console.VolumeMounts) != 1 || console.VolumeMounts[0].Name != "kube-api-access" || console.VolumeMounts[0].MountPath != "/var/run/secrets/kubernetes.io/serviceaccount" || !console.VolumeMounts[0].ReadOnly {
		t.Fatalf("console service account mount = %#v", console.VolumeMounts)
	}
	for _, mount := range proxy.VolumeMounts {
		if mount.Name == "kube-api-access" {
			t.Fatal("proxy mounts the Kubernetes credentials")
		}
	}
	var projection *corev1.ProjectedVolumeSource
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == "kube-api-access" {
			projection = volume.Projected
		}
	}
	if projection == nil || len(projection.Sources) != 3 {
		t.Fatalf("service account projection = %#v", projection)
	}
	token, ca, namespace := projection.Sources[0].ServiceAccountToken, projection.Sources[1].ConfigMap, projection.Sources[2].DownwardAPI
	if token == nil || token.Path != "token" || token.ExpirationSeconds == nil || *token.ExpirationSeconds != 3600 {
		t.Fatalf("token projection = %#v", token)
	}
	if ca == nil || ca.Name != "kube-root-ca.crt" || len(ca.Items) != 1 || ca.Items[0].Key != "ca.crt" || ca.Items[0].Path != "ca.crt" {
		t.Fatalf("CA projection = %#v", ca)
	}
	if namespace == nil || len(namespace.Items) != 1 || namespace.Items[0].Path != "namespace" || namespace.Items[0].FieldRef == nil || namespace.Items[0].FieldRef.FieldPath != "metadata.namespace" {
		t.Fatalf("namespace projection = %#v", namespace)
	}
	for _, expected := range []string{"--bind-address=127.0.0.1:8080", "--auth-mode=oidc", "--external-url=https://console.example"} {
		if !containsArgument(console.Args, expected) {
			t.Errorf("console missing %s", expected)
		}
	}
	if proxy.Image != "quay.io/oauth2-proxy/oauth2-proxy:v7.15.5" {
		t.Fatalf("proxy image = %s", proxy.Image)
	}
	if len(proxy.Ports) != 1 || proxy.Ports[0].Name != "http" || proxy.Ports[0].ContainerPort != 4180 {
		t.Fatalf("proxy ports = %#v", proxy.Ports)
	}
	if len(service.Spec.Ports) != 1 || service.Spec.Ports[0].TargetPort.StrVal != "http" {
		t.Fatalf("Service = %#v", service.Spec)
	}
	if console.ReadinessProbe.HTTPGet.Port.IntVal != 4180 || console.LivenessProbe.HTTPGet.Port.IntVal != 4180 {
		t.Fatal("console probes must go through proxy")
	}
	if proxy.ReadinessProbe.HTTPGet.Path != "/readyz" || proxy.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Fatal("proxy probes must reach Kelos")
	}
	for _, expected := range []string{
		"--alpha-config=/etc/oauth2-proxy/oauth2-proxy.yaml", "--skip-auth-route=GET=^/(healthz|readyz)$",
		"--cookie-secure=true", "--cookie-httponly=true", "--cookie-name=__Host-kelos-console",
		"--cookie-samesite=lax", "--cookie-expire=8h", "--cookie-refresh=5m", "--api-route=^/api/",
		"--reverse-proxy=true", "--trusted-proxy-ip=10.1.0.0/24", "--auth-logging=false", "--request-logging=false",
	} {
		if !containsArgument(proxy.Args, expected) {
			t.Errorf("proxy missing %s", expected)
		}
	}
	for _, arg := range proxy.Args {
		if strings.HasPrefix(arg, "--trusted-ip=") {
			t.Fatal("IP auth exemption is forbidden")
		}
		if strings.HasPrefix(arg, "--skip-auth-route=") && arg != "--skip-auth-route=GET=^/(healthz|readyz)$" {
			t.Fatalf("unexpected exemption: %s", arg)
		}
	}
	for _, expected := range []string{"bindAddress: 0.0.0.0:4180", "uri: http://127.0.0.1:8080/", "proxyWebSockets: true", "code_challenge_method: S256", "insecureSkipNonce: false", "insecureSkipIssuerVerification: false", "insecureAllowUnverifiedEmail: false"} {
		if !strings.Contains(headers.Data["oauth2-proxy.yaml"], expected) {
			t.Errorf("proxy configuration missing %s", expected)
		}
	}
	var headerConfig struct {
		Headers []struct {
			Name     string `json:"name"`
			Preserve bool   `json:"preserveRequestValue"`
			Values   []struct {
				Source struct {
					Claim string `json:"claim"`
				} `json:"claimSource"`
			} `json:"values"`
		} `json:"injectRequestHeaders"`
	}
	if err := yaml.Unmarshal([]byte(headers.Data["oauth2-proxy.yaml"]), &headerConfig); err != nil {
		t.Fatal(err)
	}
	injected, stripped := map[string]string{}, map[string]bool{}
	for _, header := range headerConfig.Headers {
		if header.Preserve {
			t.Fatalf("header preserves caller value: %s", header.Name)
		}
		stripped[header.Name] = true
		for _, value := range header.Values {
			injected[header.Name] = value.Source.Claim
		}
	}
	if len(injected) != 2 || injected["X-Kelos-User"] != "user" || injected["X-Kelos-Groups"] != "groups" {
		t.Fatalf("injected claims = %v", injected)
	}
	for _, name := range []string{"Authorization", "Cookie", "X-Forwarded-Access-Token", "X-Forwarded-User", "X-Forwarded-Groups"} {
		if !stripped[name] {
			t.Errorf("missing strip rule for %s", name)
		}
	}
	if len(userRole.Rules) == 0 {
		t.Fatal("missing user role")
	}
	for _, userRule := range userRole.Rules {
		found := false
		for _, adminRule := range adminRole.Rules {
			if reflect.DeepEqual(userRule, adminRule) {
				found = true
			}
		}
		if !found {
			t.Fatalf("Admin role does not include user rule %#v", userRule)
		}
	}
	for _, role := range []rbacv1.ClusterRole{adminRole, serverRole} {
		foundBind, foundBindings := false, false
		for _, rule := range role.Rules {
			if !containsArgument(rule.APIGroups, rbacv1.GroupName) {
				continue
			}
			switch {
			case reflect.DeepEqual(rule.Resources, []string{"clusterroles"}):
				foundBind = containsArgument(rule.Verbs, "bind") && reflect.DeepEqual(rule.ResourceNames, []string{"kelos-console-user", "kelos-console-admin"})
			case reflect.DeepEqual(rule.Resources, []string{"rolebindings"}):
				foundBindings = reflect.DeepEqual(rule.Verbs, []string{"get", "list", "create", "delete"})
			default:
				t.Fatalf("unexpected RBAC access in %s: %#v", role.Name, rule)
			}
		}
		if !foundBind || !foundBindings {
			t.Fatalf("missing constrained role management permissions in %s", role.Name)
		}
	}
	for _, rule := range userRole.Rules {
		for _, group := range rule.APIGroups {
			if group != "kelos.dev" {
				t.Fatalf("human role grants group %q", group)
			}
		}
	}
	foundReview := false
	for _, rule := range serverRole.Rules {
		if containsArgument(rule.Resources, "subjectaccessreviews") {
			foundReview = len(rule.Verbs) == 1 && rule.Verbs[0] == "create" && len(rule.APIGroups) == 1 && rule.APIGroups[0] == "authorization.k8s.io"
		}
	}
	if !foundReview {
		t.Fatal("missing access review permission")
	}
	for _, resource := range []string{"workspaces", "agentconfigs", "workerpools"} {
		for _, verb := range []string{"create", "update", "delete"} {
			found := false
			for _, rule := range serverRole.Rules {
				if containsArgument(rule.Resources, resource) && containsArgument(rule.Verbs, verb) {
					found = true
				}
			}
			if !found {
				t.Errorf("server role lacks %s on %s", verb, resource)
			}
			for _, rule := range userRole.Rules {
				if containsArgument(rule.Resources, resource) && containsArgument(rule.Verbs, verb) {
					t.Errorf("user role grants administrative %s on %s", verb, resource)
				}
			}
		}
	}
}

func containsArgument(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestRenderConsoleOIDCRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]interface{}, map[string]interface{})
	}{
		{"static secret", func(c, o map[string]interface{}) { c["secretName"] = "static-secret" }},
		{"static cookie", func(c, o map[string]interface{}) { c["secureCookie"] = true }},
		{"static key", func(c, o map[string]interface{}) { c["tokenKey"] = "other" }},
		{"missing secret", func(c, o map[string]interface{}) { delete(o, "secretName") }},
		{"missing client", func(c, o map[string]interface{}) { delete(o, "clientID") }},
		{"HTTP issuer", func(c, o map[string]interface{}) { o["issuerURL"] = "http://identity.example" }},
		{"HTTP callback", func(c, o map[string]interface{}) { o["redirectURL"] = "http://console.example/oauth2/callback" }},
		{"callback path", func(c, o map[string]interface{}) { o["redirectURL"] = "https://console.example/other" }},
		{"empty prefix", func(c, o map[string]interface{}) { o["usernamePrefix"] = "" }},
		{"reserved prefix", func(c, o map[string]interface{}) { o["groupsPrefix"] = "system:" }},
		{"partial reserved username prefix", func(c, o map[string]interface{}) { o["usernamePrefix"] = "sys" }},
		{"partial reserved group prefix", func(c, o map[string]interface{}) { o["groupsPrefix"] = "system" }},
		{"no proxy IPs", func(c, o map[string]interface{}) { delete(o, "trustedProxyIPs") }},
		{"catch-all IPv4", func(c, o map[string]interface{}) { o["trustedProxyIPs"] = []interface{}{"0.0.0.0/0"} }},
		{"catch-all IPv6", func(c, o map[string]interface{}) { o["trustedProxyIPs"] = []interface{}{"::/0"} }},
		{"padded catch-all IPv4", func(c, o map[string]interface{}) { o["trustedProxyIPs"] = []interface{}{"0.0.0.0/00"} }},
		{"padded catch-all IPv6", func(c, o map[string]interface{}) { o["trustedProxyIPs"] = []interface{}{"::/000"} }},
		{"unversioned proxy", func(c, o map[string]interface{}) { o["image"] = "quay.io/oauth2-proxy/oauth2-proxy:latest" }},
		{"unpatched proxy", func(c, o map[string]interface{}) { o["image"] = "quay.io/oauth2-proxy/oauth2-proxy:v7.15.4" }},
		{"unsupported cookie override", func(c, o map[string]interface{}) { o["cookieSecure"] = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			console := consoleOIDCValues()
			oidc := console["auth"].(map[string]interface{})["oidc"].(map[string]interface{})
			test.change(console, oidc)
			if _, err := Render(manifests.ChartFS, map[string]interface{}{"consoleServer": console}); err == nil {
				t.Fatal("invalid configuration rendered successfully")
			}
		})
	}
}
