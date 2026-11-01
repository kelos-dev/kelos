package cli

import (
	"encoding/base64"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/kelos-dev/kelos/internal/helmchart"
	"github.com/kelos-dev/kelos/internal/manifests"
)

func consoleWebPushInstallManifest(t *testing.T) []byte {
	t.Helper()
	data, err := helmchart.Render(manifests.ChartFS, map[string]interface{}{
		"consoleServer": map[string]interface{}{
			"enabled": true, "secretName": "console-auth",
			"webPush": map[string]interface{}{
				"enabled": true, "storageNamespace": "console-notifications",
				"secretName": "console-signing", "subject": "https://github.com/kelos-dev/kelos",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func consoleWebPushSigningSecret(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]interface{}{"name": "console-signing", "namespace": "kelos-system"},
		"data": map[string]interface{}{
			"public-key":  base64.StdEncoding.EncodeToString([]byte(publicKey)),
			"private-key": base64.StdEncoding.EncodeToString([]byte(privateKey)),
		},
	}}
}

func TestPrepareConsoleWebPushCreatesAndPreservesKeys(t *testing.T) {
	manifest := consoleWebPushInstallManifest(t)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if err := prepareConsoleWebPush(t.Context(), client, manifest); err != nil {
		t.Fatal(err)
	}
	secret, err := client.Resource(secretGVR).Namespace("kelos-system").Get(t.Context(), "console-signing", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConsoleWebPushSecret(secret); err != nil {
		t.Fatal(err)
	}
	namespaces := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	for _, name := range []string{"kelos-system", "console-notifications"} {
		if _, err := namespaces.Get(t.Context(), name, metav1.GetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	client.ClearActions()
	if err := prepareConsoleWebPush(t.Context(), client, manifest); err != nil {
		t.Fatal(err)
	}
	reused, err := client.Resource(secretGVR).Namespace("kelos-system").Get(t.Context(), "console-signing", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secret.Object, reused.Object) {
		t.Fatal("installation changed an existing signing Secret")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("repeated installation wrote existing resources: %v", action)
		}
	}
}

func TestPrepareConsoleWebPushRejectsInvalidExistingKeys(t *testing.T) {
	for _, mutation := range []string{"missing", "mismatched"} {
		t.Run(mutation, func(t *testing.T) {
			secret := consoleWebPushSigningSecret(t)
			if mutation == "missing" {
				unstructured.RemoveNestedField(secret.Object, "data", "private-key")
			} else {
				other := consoleWebPushSigningSecret(t)
				value, _, _ := unstructured.NestedString(other.Object, "data", "private-key")
				if err := unstructured.SetNestedField(secret.Object, value, "data", "private-key"); err != nil {
					t.Fatal(err)
				}
			}
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), secret)
			err := prepareConsoleWebPush(t.Context(), client, consoleWebPushInstallManifest(t))
			if err == nil || !strings.Contains(err.Error(), "Secret kelos-system/console-signing") {
				t.Fatalf("invalid signing Secret error = %v", err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("invalid key configuration changed resources: %v", action)
				}
			}
		})
	}
}

func TestPrepareConsoleWebPushAcceptsKeyFileLineEndings(t *testing.T) {
	secret := consoleWebPushSigningSecret(t)
	for _, key := range []string{"public-key", "private-key"} {
		value, _ := secretDataValue(secret, key)
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		value = base64.StdEncoding.EncodeToString(append(decoded, '\r', '\n'))
		if err := unstructured.SetNestedField(secret.Object, value, "data", key); err != nil {
			t.Fatal(err)
		}
	}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), secret)
	if err := prepareConsoleWebPush(t.Context(), client, consoleWebPushInstallManifest(t)); err != nil {
		t.Fatal(err)
	}
	reused, err := client.Resource(secretGVR).Namespace("kelos-system").Get(t.Context(), "console-signing", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secret.Object, reused.Object) {
		t.Fatal("installation rewrote existing key files")
	}
}

func TestPrepareConsoleWebPushDoesNotReplaceKeysOnLookupFailure(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(secretGVR.GroupResource(), "console-signing", errors.New("denied"))
	})
	err := prepareConsoleWebPush(t.Context(), client, consoleWebPushInstallManifest(t))
	if err == nil || !strings.Contains(err.Error(), "Secret kelos-system/console-signing") {
		t.Fatalf("lookup failure = %v", err)
	}
	if len(client.Actions()) != 1 {
		t.Fatalf("lookup failure triggered additional operations: %v", client.Actions())
	}
}

func TestPrepareConsoleWebPushUsesConcurrentInstallerKeys(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	winner := consoleWebPushSigningSecret(t)
	client.PrependReactor("create", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		if err := client.Tracker().Create(secretGVR, winner, winner.GetNamespace()); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewAlreadyExists(secretGVR.GroupResource(), winner.GetName())
	})
	if err := prepareConsoleWebPush(t.Context(), client, consoleWebPushInstallManifest(t)); err != nil {
		t.Fatal(err)
	}
	stored, err := client.Resource(secretGVR).Namespace("kelos-system").Get(t.Context(), winner.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Object, winner.Object) {
		t.Fatal("concurrent installation did not retain the winning signing keys")
	}
}

func TestPrepareConsoleWebPushSkipsDisabledNotifications(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		manifest, err := helmchart.Render(manifests.ChartFS, map[string]interface{}{
			"consoleServer": map[string]interface{}{"enabled": enabled, "secretName": "console-auth"},
		})
		if err != nil {
			t.Fatal(err)
		}
		client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
		if err := prepareConsoleWebPush(t.Context(), client, manifest); err != nil {
			t.Fatal(err)
		}
		if len(client.Actions()) != 0 {
			t.Fatal("disabled Web Push accessed Kubernetes")
		}
	}
}

func TestInstallDryRunDoesNotGenerateWebPushKeys(t *testing.T) {
	command := NewRootCommand()
	command.SetArgs([]string{"install", "--dry-run", "--set", "consoleServer.enabled=true,consoleServer.secretName=console-auth,consoleServer.webPush.enabled=true,consoleServer.webPush.storageNamespace=console-notifications,consoleServer.webPush.secretName=console-signing,consoleServer.webPush.subject=https://github.com/kelos-dev/kelos"})
	output := captureStdout(t, func() {
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	objects, err := parseManifests([]byte(output))
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.GetKind() == "Secret" && object.GetName() == "console-signing" {
			t.Fatal("dry run generated a signing Secret")
		}
	}
	if config, err := consoleWebPushInstallSettings([]byte(output)); err != nil || config == nil {
		t.Fatalf("dry run did not render Web Push configuration: config=%v err=%v", config, err)
	}
}

func TestDevInstallPreparesConsoleWebPush(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/deploy-dev.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string
				Run  string
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var valuesText string
	for _, step := range workflow.Jobs["deploy"].Steps {
		if step.Name != "Install kelos" {
			continue
		}
		_, after, found := strings.Cut(step.Run, "cat > /tmp/kelos-dev-values.yaml <<'EOF'\n")
		if !found {
			t.Fatal("dev Helm values not found")
		}
		valuesText, _, found = strings.Cut(after, "\nEOF\n")
		if !found {
			t.Fatal("dev Helm values terminator not found")
		}
	}
	var values map[string]interface{}
	if err := yaml.Unmarshal([]byte(valuesText), &values); err != nil {
		t.Fatal(err)
	}
	manifest, err := helmchart.Render(manifests.ChartFS, values)
	if err != nil {
		t.Fatal(err)
	}
	config, err := consoleWebPushInstallSettings(manifest)
	if err != nil || config == nil {
		t.Fatalf("dev install must enable Console Web Push: config=%v err=%v", config, err)
	}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if err := prepareConsoleWebPush(t.Context(), client, manifest); err != nil {
		t.Fatal(err)
	}
	secret, err := client.Resource(secretGVR).Namespace(config.keyNamespace).Get(t.Context(), config.secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConsoleWebPushSecret(secret); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(t.Context(), config.storageNamespace, metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
}
