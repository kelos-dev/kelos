package examples

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestSelfDevelopmentGitHubSpawnersUseWebhooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file   string
		events []string
	}{
		{file: "kelos-workers.yaml", events: []string{"issue_comment"}},
		{file: "kelos-planner.yaml", events: []string{"issue_comment"}},
		{file: "kelos-reviewer.yaml", events: []string{"issue_comment"}},
		{file: "kelos-pr-responder.yaml", events: []string{"issue_comment", "pull_request_review"}},
		{file: "kelos-squash-commits.yaml", events: []string{"issue_comment"}},
		{file: "kelos-triage.yaml", events: []string{"issues"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			ts := readSelfDevelopmentTaskSpawner(t, tt.file)

			if ts.Spec.When.GitHubWebhook == nil {
				t.Fatalf("expected %s to use githubWebhook", tt.file)
			}
			if ts.Spec.When.GitHubIssues != nil {
				t.Fatalf("expected %s to stop using githubIssues", tt.file)
			}
			if ts.Spec.When.GitHubPullRequests != nil {
				t.Fatalf("expected %s to stop using githubPullRequests", tt.file)
			}

			if got := ts.Spec.When.GitHubWebhook.Repository; got != "kelos-dev/kelos" {
				t.Fatalf("expected %s repository to be kelos-dev/kelos, got %q", tt.file, got)
			}
			if !reflect.DeepEqual(ts.Spec.When.GitHubWebhook.Events, tt.events) {
				t.Fatalf("expected %s events %v, got %v", tt.file, tt.events, ts.Spec.When.GitHubWebhook.Events)
			}
			if len(ts.Spec.When.GitHubWebhook.Filters) == 0 {
				t.Fatalf("expected %s to define webhook filters", tt.file)
			}
		})
	}
}

func readSelfDevelopmentTaskSpawner(t *testing.T, file string) *kelosv1alpha1.TaskSpawner {
	t.Helper()

	path := filepath.Join("..", "..", "self-development", file)
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
