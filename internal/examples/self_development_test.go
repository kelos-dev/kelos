package examples

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
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
		})
	}
}

func TestDevelopmentTaskSpawnersIgnoreDisruptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir  string
		file string
	}{
		{dir: "self-development", file: "kelos-api-reviewer.yaml"},
		{dir: "self-development", file: "kelos-config-update.yaml"},
		{dir: "self-development", file: "kelos-fake-strategist.yaml"},
		{dir: "self-development", file: "kelos-fake-user.yaml"},
		{dir: "self-development", file: "kelos-image-update.yaml"},
		{dir: "self-development", file: "kelos-planner.yaml"},
		{dir: "self-development", file: "kelos-pr-responder.yaml"},
		{dir: "self-development", file: "kelos-reviewer.yaml"},
		{dir: "self-development", file: "kelos-self-update.yaml"},
		{dir: "self-development", file: "kelos-squash-commits.yaml"},
		{dir: "self-development", file: "kelos-triage.yaml"},
		{dir: "self-development", file: "kelos-workers.yaml"},
		{dir: "kanon-development", file: "kanon-config-update.yaml"},
		{dir: "kanon-development", file: "kanon-fake-strategist.yaml"},
		{dir: "kanon-development", file: "kanon-fake-user.yaml"},
		{dir: "kanon-development", file: "kanon-planner.yaml"},
		{dir: "kanon-development", file: "kanon-pr-responder.yaml"},
		{dir: "kanon-development", file: "kanon-reviewer.yaml"},
		{dir: "kanon-development", file: "kanon-self-update.yaml"},
		{dir: "kanon-development", file: "kanon-squash-commits.yaml"},
		{dir: "kanon-development", file: "kanon-triage.yaml"},
		{dir: "kanon-development", file: "kanon-workers.yaml"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			ts := readTaskSpawnerFromDir(t, tt.dir, tt.file)
			assertIgnoresDisruptions(t, tt.dir+"/"+tt.file, ts.Spec.TaskTemplate.PodFailurePolicy)
		})
	}
}

func TestDevelopmentCommandPatternsRequireExactCommandBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir     string
		file    string
		command string
	}{
		{dir: "self-development", file: "kelos-workers.yaml", command: "/kelos pick-up"},
		{dir: "self-development", file: "kelos-planner.yaml", command: "/kelos plan"},
		{dir: "self-development", file: "kelos-reviewer.yaml", command: "/kelos review"},
		{dir: "self-development", file: "kelos-api-reviewer.yaml", command: "/kelos api-review"},
		{dir: "self-development", file: "kelos-pr-responder.yaml", command: "/kelos pick-up"},
		{dir: "self-development", file: "kelos-squash-commits.yaml", command: "/kelos squash-commits"},
		{dir: "kanon-development", file: "kanon-workers.yaml", command: "/kelos pick-up"},
		{dir: "kanon-development", file: "kanon-planner.yaml", command: "/kelos plan"},
		{dir: "kanon-development", file: "kanon-reviewer.yaml", command: "/kelos review"},
		{dir: "kanon-development", file: "kanon-pr-responder.yaml", command: "/kelos pick-up"},
		{dir: "kanon-development", file: "kanon-squash-commits.yaml", command: "/kelos squash-commits"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			ts := readTaskSpawnerFromDir(t, tt.dir, tt.file)
			spawner := ts.Spec.When.GitHubWebhook
			if spawner == nil {
				t.Fatalf("expected %s/%s to use githubWebhook", tt.dir, tt.file)
			}

			foundBodyPattern := false
			for _, filter := range spawner.Filters {
				filter := filter
				if filter.BodyPattern == "" {
					continue
				}
				foundBodyPattern = true
				allowsCommandLine := tt.dir == "self-development" && tt.command == "/kelos pick-up"

				t.Run(filter.Event+"/"+filter.Author, func(t *testing.T) {
					bodyTests := []struct {
						name string
						body string
						want bool
					}{
						{name: "exact command", body: tt.command, want: true},
						{name: "surrounding whitespace", body: "\n\t " + tt.command + " \n", want: true},
						{name: "embedded in sentence", body: "Please run " + tt.command, want: false},
						{name: "trailing text", body: tt.command + " after CI passes", want: false},
						{name: "following line prose", body: tt.command + "\nRebase on origin/main", want: allowsCommandLine},
						{name: "quoted markdown", body: "> " + tt.command, want: false},
						{name: "inline code", body: "`" + tt.command + "`", want: false},
						{name: "command line after prose", body: "Please run:\n" + tt.command, want: allowsCommandLine},
					}

					for _, bodyTest := range bodyTests {
						bodyTest := bodyTest
						t.Run(bodyTest.name, func(t *testing.T) {
							payload := developmentWebhookPayload(t, spawner.Repository, filter, bodyTest.body)
							eventData, err := webhook.ParseGitHubWebhook(filter.Event, payload)
							if err != nil {
								t.Fatalf("ParseGitHubWebhook() error = %v", err)
							}

							got, err := webhook.MatchesGitHubEvent(spawner, filter.Event, eventData)
							if err != nil {
								t.Fatalf("MatchesGitHubEvent() error = %v", err)
							}
							if got != bodyTest.want {
								t.Fatalf("MatchesGitHubEvent() = %v, want %v", got, bodyTest.want)
							}
						})
					}
				})
			}
			if !foundBodyPattern {
				t.Fatalf("expected %s/%s to define a bodyPattern filter", tt.dir, tt.file)
			}
		})
	}
}

func assertIgnoresDisruptions(t *testing.T, name string, policy *batchv1.PodFailurePolicy) {
	t.Helper()

	if policy == nil {
		t.Fatalf("%s taskTemplate.podFailurePolicy is nil", name)
	}
	if len(policy.Rules) != 2 {
		t.Fatalf("%s podFailurePolicy rules length = %d, want 2", name, len(policy.Rules))
	}
	disruptionRule := policy.Rules[0]
	if disruptionRule.Action != batchv1.PodFailurePolicyActionIgnore {
		t.Fatalf("%s first podFailurePolicy action = %q, want %q", name, disruptionRule.Action, batchv1.PodFailurePolicyActionIgnore)
	}
	if len(disruptionRule.OnPodConditions) != 1 {
		t.Fatalf("%s first podFailurePolicy onPodConditions length = %d, want 1", name, len(disruptionRule.OnPodConditions))
	}
	if got := disruptionRule.OnPodConditions[0].Type; got != corev1.DisruptionTarget {
		t.Fatalf("%s first podFailurePolicy condition type = %q, want %q", name, got, corev1.DisruptionTarget)
	}
	if got := disruptionRule.OnPodConditions[0].Status; got != corev1.ConditionTrue {
		t.Fatalf("%s first podFailurePolicy condition status = %q, want %q", name, got, corev1.ConditionTrue)
	}

	failRule := policy.Rules[1]
	if failRule.Action != batchv1.PodFailurePolicyActionFailJob {
		t.Fatalf("%s second podFailurePolicy action = %q, want %q", name, failRule.Action, batchv1.PodFailurePolicyActionFailJob)
	}
	if failRule.OnExitCodes == nil {
		t.Fatalf("%s second podFailurePolicy onExitCodes is nil", name)
	}
	if got := failRule.OnExitCodes.Operator; got != batchv1.PodFailurePolicyOnExitCodesOpNotIn {
		t.Fatalf("%s second podFailurePolicy onExitCodes operator = %q, want %q", name, got, batchv1.PodFailurePolicyOnExitCodesOpNotIn)
	}
	if !reflect.DeepEqual(failRule.OnExitCodes.Values, []int32{0}) {
		t.Fatalf("%s second podFailurePolicy onExitCodes values = %v, want [0]", name, failRule.OnExitCodes.Values)
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

func TestDevelopmentTaskSpawnersSetExpectedEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir    string
		file   string
		effort string
	}{
		{dir: "self-development", file: "kelos-api-reviewer.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-workers.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-planner.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-reviewer.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-self-update.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-fake-strategist.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-triage.yaml", effort: "high"},
		{dir: "self-development", file: "kelos-pr-responder.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-config-update.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-image-update.yaml", effort: "medium"},
		{dir: "self-development", file: "kelos-fake-user.yaml", effort: "medium"},
		{dir: "self-development", file: "kelos-squash-commits.yaml", effort: "medium"},
		{dir: "kanon-development", file: "kanon-workers.yaml", effort: "xhigh"},
		{dir: "kanon-development", file: "kanon-planner.yaml", effort: "xhigh"},
		{dir: "kanon-development", file: "kanon-reviewer.yaml", effort: "xhigh"},
		{dir: "kanon-development", file: "kanon-self-update.yaml", effort: "xhigh"},
		{dir: "kanon-development", file: "kanon-fake-strategist.yaml", effort: "xhigh"},
		{dir: "kanon-development", file: "kanon-triage.yaml", effort: "high"},
		{dir: "kanon-development", file: "kanon-pr-responder.yaml", effort: "xhigh"},
		{dir: "kanon-development", file: "kanon-config-update.yaml", effort: "xhigh"},
		{dir: "kanon-development", file: "kanon-fake-user.yaml", effort: "medium"},
		{dir: "kanon-development", file: "kanon-squash-commits.yaml", effort: "medium"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			ts := readTaskSpawnerFromDir(t, tt.dir, tt.file)
			if ts.Spec.TaskTemplate.Effort != tt.effort {
				t.Fatalf("TaskTemplate.Effort = %q, want %q", ts.Spec.TaskTemplate.Effort, tt.effort)
			}
		})
	}
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

func readSelfDevelopmentTaskSpawner(t *testing.T, file string) *kelos.TaskSpawner {
	t.Helper()

	return readTaskSpawnerFromDir(t, "self-development", file)
}

func readTaskSpawnerFromDir(t *testing.T, dir, file string) *kelos.TaskSpawner {
	t.Helper()

	path := filepath.Join("..", "..", dir, file)
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

		var ts kelos.TaskSpawner
		if err := sigyaml.Unmarshal(doc, &ts); err != nil {
			t.Fatalf("decoding TaskSpawner from %s: %v", path, err)
		}
		return &ts
	}

	t.Fatalf("no TaskSpawner found in %s", path)
	return nil
}

func developmentWebhookPayload(t *testing.T, repository string, filter kelos.GitHubWebhookFilter, body string) []byte {
	t.Helper()

	owner, name, ok := strings.Cut(repository, "/")
	if !ok {
		t.Fatalf("repository %q is not owner/name", repository)
	}

	author := filter.Author
	if author == "" {
		author = "gjkim42"
	}
	action := filter.Action
	if action == "" {
		action = "created"
	}
	state := filter.State
	if state == "" {
		state = "open"
	}

	repositoryPayload := map[string]any{
		"full_name": repository,
		"name":      name,
		"owner": map[string]any{
			"login": owner,
		},
	}

	var payload map[string]any
	switch filter.Event {
	case "issue_comment":
		issueURL := "https://github.com/" + repository + "/issues/1"
		issue := map[string]any{
			"number":   1,
			"title":    "Test issue",
			"state":    state,
			"html_url": issueURL,
		}
		if filter.CommentOn == kelos.CommentOnPullRequest {
			issue["html_url"] = "https://github.com/" + repository + "/pull/1"
			issue["pull_request"] = map[string]any{
				"url":      "https://api.github.com/repos/" + repository + "/pulls/1",
				"html_url": "https://github.com/" + repository + "/pull/1",
			}
		}

		payload = map[string]any{
			"action":     action,
			"sender":     map[string]any{"login": author},
			"repository": repositoryPayload,
			"issue":      issue,
			"comment":    map[string]any{"body": body},
		}
	case "pull_request_review":
		payload = map[string]any{
			"action":     action,
			"sender":     map[string]any{"login": author},
			"repository": repositoryPayload,
			"pull_request": map[string]any{
				"number":   1,
				"title":    "Test PR",
				"body":     "Test PR body",
				"html_url": "https://github.com/" + repository + "/pull/1",
				"state":    state,
				"draft":    false,
				"head":     map[string]any{"ref": "feature-branch"},
			},
			"review": map[string]any{"body": body},
		}
	default:
		t.Fatalf("unsupported event %q", filter.Event)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}
