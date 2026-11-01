package helmchart

import (
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/manifests"
)

func TestRenderConsoleWebPush(t *testing.T) {
	values := map[string]interface{}{
		"enabled": true, "secretName": "console-token",
		"webPush": map[string]interface{}{"enabled": true, "storageNamespace": "console-notifications", "secretName": "console-vapid", "subject": "mailto:admin@example.com"},
	}
	data, err := Render(manifests.ChartFS, map[string]interface{}{"consoleServer": values})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"--web-push-namespace=console-notifications", "--web-push-subject=mailto:admin@example.com",
		"--web-push-private-key-file=/var/run/secrets/kelos-web-push/private-key",
		"secretName: console-vapid", "name: web-push-keys", "mountPath: /var/run/secrets/kelos-web-push",
		"kind: Role\nmetadata:\n  name: kelos-console-web-push\n  namespace: console-notifications",
		"resources: [secrets]", "resources: [leases]", "kind: RoleBinding\nmetadata:\n  name: kelos-console-web-push",
	} {
		if !strings.Contains(string(data), expected) {
			t.Errorf("missing %q", expected)
		}
	}
	for _, key := range []string{"subject", "secretName", "storageNamespace"} {
		push := values["webPush"].(map[string]interface{})
		original := push[key]
		push[key] = ""
		if _, err := Render(manifests.ChartFS, map[string]interface{}{"consoleServer": values}); err == nil {
			t.Errorf("missing %s accepted", key)
		}
		push[key] = original
	}
	for _, field := range []struct{ key, value string }{{"storageNamespace", "kelos-system"}, {"subject", "admin@example.com"}} {
		push := values["webPush"].(map[string]interface{})
		original := push[field.key]
		push[field.key] = field.value
		if _, err := Render(manifests.ChartFS, map[string]interface{}{"consoleServer": values}); err == nil {
			t.Errorf("invalid %s accepted", field.key)
		}
		push[field.key] = original
	}
	values["webPush"].(map[string]interface{})["enabled"] = false
	data, err = Render(manifests.ChartFS, map[string]interface{}{"consoleServer": values})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "kelos-console-web-push") || strings.Contains(string(data), "--web-push-") || strings.Contains(string(data), "name: web-push-keys") || strings.Contains(string(data), "mountPath: /var/run/secrets/kelos-web-push") {
		t.Fatal("disabled Web Push rendered configuration or permissions")
	}
}
