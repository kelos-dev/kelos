package examples

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// TestKanonDevelopmentGitHubSpawnersUseGateway verifies the kanon-development
// github-webhook spawners are routed through the "kanon" WebhookGateway.
func TestKanonDevelopmentGitHubSpawnersUseGateway(t *testing.T) {
	t.Parallel()

	files := []string{
		"kanon-workers.yaml",
		"kanon-planner.yaml",
		"kanon-reviewer.yaml",
		"kanon-pr-responder.yaml",
		"kanon-squash-commits.yaml",
		"kanon-triage.yaml",
	}

	for _, file := range files {
		file := file
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			ts := readKanonTaskSpawner(t, file)
			ghw := ts.Spec.When.GitHubWebhook
			if ghw == nil {
				t.Fatalf("expected %s to use githubWebhook", file)
			}
			if got := ghw.Repository; got != "kelos-dev/kanon" {
				t.Fatalf("expected %s repository to be kelos-dev/kanon, got %q", file, got)
			}
			if gr := ghw.GatewayRef; gr == nil || gr.Name != "kanon" {
				t.Fatalf("expected %s to route through WebhookGateway %q via gatewayRef, got %+v", file, "kanon", gr)
			}
		})
	}
}

// TestKanonDevelopmentWebhookGateway verifies the kanon WebhookGateway manifest
// is a well-formed github gateway whose name matches the spawners' gatewayRef.
func TestKanonDevelopmentWebhookGateway(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "kanon-development", "webhookgateway.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var gw kelosv1alpha1.WebhookGateway
	if err := sigyaml.Unmarshal(data, &gw); err != nil {
		t.Fatalf("decoding WebhookGateway from %s: %v", path, err)
	}

	if gw.Name != "kanon" {
		t.Fatalf("expected gateway name %q (to match the spawners' gatewayRef), got %q", "kanon", gw.Name)
	}
	if gw.Spec.GitHub == nil {
		t.Fatalf("expected a github gateway")
	}
	if gw.Spec.Linear != nil || gw.Spec.Generic != nil {
		t.Fatalf("expected only the github provider sub-struct to be set")
	}
	if gw.Spec.GitHub.SecretRef.Name == "" {
		t.Fatalf("expected github.secretRef.name to be set for inbound HMAC verification")
	}
	if gw.Spec.GitHub.CredentialsRef == nil || gw.Spec.GitHub.CredentialsRef.Name == "" {
		t.Fatalf("expected github.credentialsRef.name to be set for reporting/enrichment")
	}
}

func readKanonTaskSpawner(t *testing.T, file string) *kelosv1alpha1.TaskSpawner {
	t.Helper()

	path := filepath.Join("..", "..", "kanon-development", file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	reader := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading YAML document from %s: %v", path, err)
		}

		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}

		var meta struct {
			Kind string `yaml:"kind"`
		}
		if err := sigyaml.Unmarshal(doc, &meta); err != nil {
			t.Fatalf("decoding document metadata from %s: %v", path, err)
		}
		if meta.Kind != "TaskSpawner" {
			continue
		}

		var ts kelosv1alpha1.TaskSpawner
		if err := sigyaml.Unmarshal(doc, &ts); err != nil {
			t.Fatalf("decoding TaskSpawner from %s: %v", path, err)
		}
		return &ts
	}

	t.Fatalf("no TaskSpawner found in %s", path)
	return nil
}
