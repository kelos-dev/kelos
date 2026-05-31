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
	"github.com/kelos-dev/kelos/internal/webhook"
)

func TestSelfDevelopmentGitHubSpawnersUseWebhooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file   string
		events []string
	}{
		{file: "kelos-workers.yaml", events: []string{"issue_comment"}},
		{file: "kelos-planner.yaml", events: []string{"issue_comment"}},
		{file: "kelos-reviewer.yaml", events: []string{"issue_comment", "pull_request_review"}},
		{file: "kelos-api-reviewer.yaml", events: []string{"issue_comment", "pull_request_review"}},
		{file: "kelos-pr-responder.yaml", events: []string{"issue_comment", "pull_request_review"}},
		{file: "kelos-squash-commits.yaml", events: []string{"issue_comment", "pull_request_review"}},
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
			if gr := ts.Spec.When.GitHubWebhook.GatewayRef; gr == nil || gr.Name != "kelos" {
				t.Fatalf("expected %s to route through WebhookGateway %q via gatewayRef, got %+v", tt.file, "kelos", gr)
			}
		})
	}
}

// TestKelosReviewerTriggerableByBot verifies the kelos-reviewer spawner accepts
// a `/kelos review` request posted by the Kelos bot itself, not only by human
// maintainers. Workers post `/kelos review` after pushing their changes, so the
// reviewer must not exclude `kelos-bot[bot]`.
func TestKelosReviewerTriggerableByBot(t *testing.T) {
	t.Parallel()
	assertReviewerTriggerableByBot(t, "kelos-reviewer.yaml", "/kelos review")
}

// TestKelosAPIReviewerTriggerableByBot verifies the kelos-api-reviewer spawner
// accepts a `/kelos api-review` request posted by the Kelos bot itself, not only
// by human maintainers. Workers post `/kelos api-review` after pushing API
// changes, so the reviewer must not exclude `kelos-bot[bot]`.
func TestKelosAPIReviewerTriggerableByBot(t *testing.T) {
	t.Parallel()
	assertReviewerTriggerableByBot(t, "kelos-api-reviewer.yaml", "/kelos api-review")
}

// assertReviewerTriggerableByBot checks that the given reviewer spawner triggers
// for both the Kelos bot and a maintainer posting bodyPattern on an open PR, but
// not for an unauthorized user.
func assertReviewerTriggerableByBot(t *testing.T, file, bodyPattern string) {
	t.Helper()

	ts := readSelfDevelopmentTaskSpawner(t, file)
	spawner := ts.Spec.When.GitHubWebhook
	if spawner == nil {
		t.Fatalf("expected %s to use githubWebhook", file)
	}

	reviewComment := func(author string) []byte {
		return []byte(`{
			"action": "created",
			"sender": {"login": "` + author + `"},
			"issue": {
				"number": 1,
				"state": "open",
				"pull_request": {"url": "https://api.github.com/repos/kelos-dev/kelos/pulls/1"}
			},
			"comment": {"body": "` + bodyPattern + `"},
			"repository": {"full_name": "kelos-dev/kelos", "owner": {"login": "kelos-dev"}, "name": "kelos"}
		}`)
	}

	tests := []struct {
		name   string
		author string
		want   bool
	}{
		{name: "kelos bot triggers review", author: "kelos-bot[bot]", want: true},
		{name: "maintainer triggers review", author: "gjkim42", want: true},
		{name: "unauthorized user does not trigger review", author: "mallory", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eventData, err := webhook.ParseGitHubWebhook("issue_comment", reviewComment(tt.author))
			if err != nil {
				t.Fatalf("ParseGitHubWebhook() error = %v", err)
			}
			got, err := webhook.MatchesGitHubEvent(spawner, "issue_comment", eventData)
			if err != nil {
				t.Fatalf("MatchesGitHubEvent() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSelfDevelopmentWebhookGateway verifies the self-development WebhookGateway
// manifest is a well-formed github gateway whose name matches the gatewayRef the
// spawners use.
func TestSelfDevelopmentWebhookGateway(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "self-development", "webhookgateway.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var gw kelosv1alpha1.WebhookGateway
	if err := sigyaml.Unmarshal(data, &gw); err != nil {
		t.Fatalf("decoding WebhookGateway from %s: %v", path, err)
	}

	if gw.Name != "kelos" {
		t.Fatalf("expected gateway name %q (to match the spawners' gatewayRef), got %q", "kelos", gw.Name)
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
