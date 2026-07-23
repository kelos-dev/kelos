package examples

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
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
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
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
		{file: "kelos-glm-reviewer.yaml", events: []string{"issue_comment", "pull_request_review"}},
		{file: "kelos-glm-api-reviewer.yaml", events: []string{"issue_comment", "pull_request_review"}},
		{file: "kelos-pr-responder.yaml", events: []string{"issue_comment", "pull_request_review"}},
		{file: "kelos-squash-commits.yaml", events: []string{"issue_comment", "pull_request_review"}},
		{file: "kelos-triage.yaml", events: []string{"issues"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			spawner := readGitHubWebhookFromDir(t, "self-development", tt.file)

			if spawner == nil {
				t.Fatalf("expected %s to use githubWebhook", tt.file)
			}

			if got := spawner.Repository; got != "kelos-dev/kelos" {
				t.Fatalf("expected %s repository to be kelos-dev/kelos, got %q", tt.file, got)
			}
			if !reflect.DeepEqual(spawner.Events, tt.events) {
				t.Fatalf("expected %s events %v, got %v", tt.file, tt.events, spawner.Events)
			}
			if len(spawner.Filters) == 0 {
				t.Fatalf("expected %s to define webhook filters", tt.file)
			}
		})
	}
}

func TestSelfDevelopmentBaseAgentProvidesSharedInstructionsAndSkills(t *testing.T) {
	t.Parallel()

	config := readAgentConfigFromDir(t, "self-development", "base-agent.yaml")
	wantSkills := []kelos.SkillsShSpec{{Source: "gjkim42/kanon-repo"}}
	if !reflect.DeepEqual(config.Spec.Skills, wantSkills) {
		t.Fatalf("base-agent skills = %v, want %v", config.Spec.Skills, wantSkills)
	}
	for _, instruction := range []string{
		"Use plain English in code comments and docstrings",
		"Use `gh` CLI for GitHub operations",
		"Use dependency injection for components",
	} {
		if !strings.Contains(config.Spec.AgentsMD, instruction) {
			t.Fatalf("base-agent agentsMD does not contain %q", instruction)
		}
	}
}

func TestSelfDevelopmentRoleAgentConfigsDoNotDuplicateBaseSkills(t *testing.T) {
	t.Parallel()

	files := []struct {
		dir  string
		file string
	}{
		{dir: "self-development", file: "agentconfig.yaml"},
		{dir: "self-development", file: "kelos-api-reviewer.yaml"},
		{dir: "self-development", file: "kelos-fake-strategist.yaml"},
		{dir: "self-development", file: "kelos-fake-user.yaml"},
		{dir: "self-development", file: "kelos-glm-api-reviewer.yaml"},
		{dir: "self-development", file: "kelos-glm-reviewer.yaml"},
		{dir: "self-development", file: "kelos-image-update.yaml"},
		{dir: "self-development", file: "kelos-planner.yaml"},
		{dir: "self-development", file: "kelos-reviewer.yaml"},
		{dir: "self-development", file: "kelos-self-update.yaml"},
		{dir: "self-development/agora", file: "agentconfig.yaml"},
		{dir: "self-development/agora", file: "agora-fake-strategist.yaml"},
		{dir: "self-development/agora", file: "agora-fake-user.yaml"},
		{dir: "self-development/agora", file: "agora-planner.yaml"},
		{dir: "self-development/agora", file: "agora-reviewer.yaml"},
		{dir: "self-development/agora", file: "agora-workers.yaml"},
		{dir: "self-development/kanon", file: "agentconfig.yaml"},
		{dir: "self-development/kanon", file: "kanon-fake-strategist.yaml"},
		{dir: "self-development/kanon", file: "kanon-fake-user.yaml"},
		{dir: "self-development/kanon", file: "kanon-planner.yaml"},
		{dir: "self-development/kanon", file: "kanon-reviewer.yaml"},
		{dir: "self-development/kanon", file: "kanon-workers.yaml"},
	}

	for _, tt := range files {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			config := readAgentConfigFromDir(t, tt.dir, tt.file)
			if len(config.Spec.Skills) != 0 {
				t.Fatalf("%s/%s duplicates base-agent skills: %v", tt.dir, tt.file, config.Spec.Skills)
			}
		})
	}
}

func TestSelfDevelopmentSpawnersUseBaseAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir  string
		file string
		refs []kelos.AgentConfigReference
	}{
		{dir: "self-development", file: "kelos-api-reviewer.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-api-reviewer-agent"}}},
		{dir: "self-development", file: "kelos-config-update.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-dev-agent"}}},
		{dir: "self-development", file: "kelos-fake-strategist.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-fake-strategist-agent"}}},
		{dir: "self-development", file: "kelos-fake-user.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-fake-user-agent"}}},
		{dir: "self-development", file: "kelos-glm-api-reviewer.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-glm-api-reviewer-agent"}}},
		{dir: "self-development", file: "kelos-glm-reviewer.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-glm-reviewer-agent"}}},
		{dir: "self-development", file: "kelos-image-update.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-image-update-agent"}}},
		{dir: "self-development", file: "kelos-planner.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-planner-agent"}}},
		{dir: "self-development", file: "kelos-pr-responder.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-dev-agent"}}},
		{dir: "self-development", file: "kelos-reviewer.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-reviewer-agent"}}},
		{dir: "self-development", file: "kelos-self-update.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-self-update-agent"}}},
		{dir: "self-development", file: "kelos-squash-commits.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-dev-agent"}}},
		{dir: "self-development", file: "kelos-triage.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-dev-agent"}}},
		{dir: "self-development", file: "kelos-workers.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}}},
		{dir: "self-development/agora", file: "agora-config-update.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-dev-agent"}}},
		{dir: "self-development/agora", file: "agora-fake-strategist.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "agora-fake-strategist-agent"}}},
		{dir: "self-development/agora", file: "agora-fake-user.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "agora-fake-user-agent"}}},
		{dir: "self-development/agora", file: "agora-planner.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "agora-planner-agent"}}},
		{dir: "self-development/agora", file: "agora-pr-responder.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "agora-dev-agent"}}},
		{dir: "self-development/agora", file: "agora-reviewer.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "agora-reviewer-agent"}}},
		{dir: "self-development/agora", file: "agora-self-update.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-dev-agent"}}},
		{dir: "self-development/agora", file: "agora-squash-commits.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "agora-dev-agent"}}},
		{dir: "self-development/agora", file: "agora-triage.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "agora-dev-agent"}}},
		{dir: "self-development/agora", file: "agora-workers.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "agora-workers-agent"}}},
		{dir: "self-development/kanon", file: "kanon-config-update.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-dev-agent"}}},
		{dir: "self-development/kanon", file: "kanon-fake-strategist.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kanon-fake-strategist-agent"}}},
		{dir: "self-development/kanon", file: "kanon-fake-user.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kanon-fake-user-agent"}}},
		{dir: "self-development/kanon", file: "kanon-planner.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kanon-planner-agent"}}},
		{dir: "self-development/kanon", file: "kanon-pr-responder.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kanon-dev-agent"}}},
		{dir: "self-development/kanon", file: "kanon-reviewer.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kanon-reviewer-agent"}}},
		{dir: "self-development/kanon", file: "kanon-self-update.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-dev-agent"}}},
		{dir: "self-development/kanon", file: "kanon-squash-commits.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kanon-dev-agent"}}},
		{dir: "self-development/kanon", file: "kanon-triage.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kanon-dev-agent"}}},
		{dir: "self-development/kanon", file: "kanon-workers.yaml", refs: []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kanon-workers-agent"}}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			taskSpawner, sessionSpawner := readSpawnerFromDir(t, tt.dir, tt.file)
			var got []kelos.AgentConfigReference
			if taskSpawner != nil {
				got = taskSpawner.Spec.TaskTemplate.Worker.AgentConfigRefs
			} else {
				got = sessionSpawner.Spec.SessionTemplate.Worker.AgentConfigRefs
			}
			if !reflect.DeepEqual(got, tt.refs) {
				t.Fatalf("%s/%s agentConfigRefs = %v, want %v", tt.dir, tt.file, got, tt.refs)
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
		{dir: "self-development", file: "kelos-glm-api-reviewer.yaml"},
		{dir: "self-development", file: "kelos-glm-reviewer.yaml"},
		{dir: "self-development", file: "kelos-image-update.yaml"},
		{dir: "self-development", file: "kelos-planner.yaml"},
		{dir: "self-development", file: "kelos-pr-responder.yaml"},
		{dir: "self-development", file: "kelos-reviewer.yaml"},
		{dir: "self-development", file: "kelos-self-update.yaml"},
		{dir: "self-development", file: "kelos-squash-commits.yaml"},
		{dir: "self-development", file: "kelos-triage.yaml"},
		{dir: "self-development/kanon", file: "kanon-config-update.yaml"},
		{dir: "self-development/kanon", file: "kanon-fake-strategist.yaml"},
		{dir: "self-development/kanon", file: "kanon-fake-user.yaml"},
		{dir: "self-development/kanon", file: "kanon-planner.yaml"},
		{dir: "self-development/kanon", file: "kanon-pr-responder.yaml"},
		{dir: "self-development/kanon", file: "kanon-reviewer.yaml"},
		{dir: "self-development/kanon", file: "kanon-self-update.yaml"},
		{dir: "self-development/kanon", file: "kanon-squash-commits.yaml"},
		{dir: "self-development/kanon", file: "kanon-triage.yaml"},
		{dir: "self-development/kanon", file: "kanon-workers.yaml"},
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

func TestDevelopmentSessionSpawnerUsesPersistentWorkspace(t *testing.T) {
	t.Parallel()

	_, spawner := readSpawnerFromDir(t, "self-development", "kelos-workers.yaml")
	if spawner == nil {
		t.Fatal("SessionSpawner is nil")
	}
	if spawner.Spec.SessionTemplate.VolumeClaimTemplate == nil {
		t.Fatal("SessionSpawner sessionTemplate.volumeClaimTemplate is nil")
	}
	if spawner.Spec.SessionTemplate.InitialPrompt == "" {
		t.Fatal("SessionSpawner sessionTemplate.initialPrompt is empty")
	}
	if !strings.Contains(spawner.Spec.SessionTemplate.InitialPrompt, "Your workspace is backed by a PVC") {
		t.Fatal("SessionSpawner initialPrompt does not describe its persistent workspace")
	}
	wantAgentConfigs := []kelos.AgentConfigReference{{Name: "base-agent"}}
	if !reflect.DeepEqual(spawner.Spec.SessionTemplate.Worker.AgentConfigRefs, wantAgentConfigs) {
		t.Fatalf("SessionSpawner agentConfigRefs = %v, want %v", spawner.Spec.SessionTemplate.Worker.AgentConfigRefs, wantAgentConfigs)
	}
}

func TestDevelopmentSessionSpawnerMatchesEveryIssueAndPullRequestComment(t *testing.T) {
	t.Parallel()

	_, sessionSpawner := readSpawnerFromDir(t, "self-development", "kelos-workers.yaml")
	if sessionSpawner == nil {
		t.Fatal("SessionSpawner is nil")
	}
	spawner := sessionSpawner.Spec.When.GitHubWebhook
	if spawner == nil {
		t.Fatal("SessionSpawner spec.when.githubWebhook is nil")
	}
	if !reflect.DeepEqual(spawner.ExcludeAuthors, []string{"kelos-bot[bot]"}) {
		t.Fatalf("excludeAuthors = %v, want [kelos-bot[bot]]", spawner.ExcludeAuthors)
	}
	if len(spawner.Filters) != 1 {
		t.Fatalf("filters length = %d, want 1", len(spawner.Filters))
	}
	filter := spawner.Filters[0]
	wantFilter := kelos.GitHubWebhookFilter{Event: "issue_comment", Action: "created"}
	if !reflect.DeepEqual(filter, wantFilter) {
		t.Fatalf("filter = %#v, want %#v", filter, wantFilter)
	}

	for _, commentOn := range []string{kelos.CommentOnIssue, kelos.CommentOnPullRequest} {
		commentOn := commentOn
		for _, body := range []string{"Please investigate this", "/kelos pick-up"} {
			body := body
			t.Run(commentOn+"/"+body, func(t *testing.T) {
				payloadFilter := filter
				payloadFilter.CommentOn = commentOn
				payload := developmentWebhookPayload(t, spawner.Repository, payloadFilter, body)
				eventData, err := webhook.ParseGitHubWebhook("issue_comment", payload)
				if err != nil {
					t.Fatalf("ParseGitHubWebhook() error = %v", err)
				}

				got, err := webhook.MatchesGitHubEvent(spawner, "issue_comment", eventData)
				if err != nil {
					t.Fatalf("MatchesGitHubEvent() error = %v", err)
				}
				if !got {
					t.Fatal("MatchesGitHubEvent() = false, want true")
				}
			})
		}
	}

	botFilter := filter
	botFilter.Author = "kelos-bot[bot]"
	botPayload := developmentWebhookPayload(t, spawner.Repository, botFilter, "/kelos pick-up")
	botEvent, err := webhook.ParseGitHubWebhook("issue_comment", botPayload)
	if err != nil {
		t.Fatalf("ParseGitHubWebhook(bot) error = %v", err)
	}
	if got, err := webhook.MatchesGitHubEvent(spawner, "issue_comment", botEvent); err != nil || got {
		t.Fatalf("MatchesGitHubEvent(bot) = %v, %v, want false, nil", got, err)
	}
}

func TestDevelopmentSessionSpawnerUsesPRBranchAndIssueFallback(t *testing.T) {
	t.Parallel()

	_, spawner := readSpawnerFromDir(t, "self-development", "kelos-workers.yaml")
	if spawner == nil {
		t.Fatal("SessionSpawner is nil")
	}

	tests := []struct {
		name   string
		branch string
		want   string
	}{
		{name: "pull request", branch: "feature-branch", want: "feature-branch"},
		{name: "issue", branch: "", want: "kelos-task-42"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := sessionbuilder.Render("initialBranch", spawner.Spec.SessionTemplate.InitialBranch, map[string]interface{}{
				"Branch": tt.branch,
				"Number": 42,
			})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("initialBranch = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDevelopmentCommandPatternsMatchCommandLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir     string
		file    string
		command string
	}{
		{dir: "self-development", file: "kelos-planner.yaml", command: "/kelos plan"},
		{dir: "self-development", file: "kelos-reviewer.yaml", command: "/kelos review"},
		{dir: "self-development", file: "kelos-api-reviewer.yaml", command: "/kelos api-review"},
		{dir: "self-development", file: "kelos-glm-reviewer.yaml", command: "/kelos glm-review"},
		{dir: "self-development", file: "kelos-glm-api-reviewer.yaml", command: "/kelos glm-api-review"},
		{dir: "self-development", file: "kelos-pr-responder.yaml", command: "/kelos pick-up"},
		{dir: "self-development", file: "kelos-squash-commits.yaml", command: "/kelos squash-commits"},
		{dir: "self-development/kanon", file: "kanon-workers.yaml", command: "/kelos pick-up"},
		{dir: "self-development/kanon", file: "kanon-planner.yaml", command: "/kelos plan"},
		{dir: "self-development/kanon", file: "kanon-reviewer.yaml", command: "/kelos review"},
		{dir: "self-development/kanon", file: "kanon-pr-responder.yaml", command: "/kelos pick-up"},
		{dir: "self-development/kanon", file: "kanon-squash-commits.yaml", command: "/kelos squash-commits"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			spawner := readGitHubWebhookFromDir(t, tt.dir, tt.file)
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
				t.Run(filter.Event+"/"+filter.Author, func(t *testing.T) {
					bodyTests := []struct {
						name string
						body string
						want bool
					}{
						{name: "exact command", body: tt.command, want: true},
						{name: "blank line and trailing whitespace", body: "\n" + tt.command + " \n", want: true},
						{name: "leading whitespace before command", body: "\t " + tt.command, want: false},
						{name: "embedded in sentence", body: "Please run " + tt.command, want: false},
						{name: "trailing text", body: tt.command + " after CI passes", want: false},
						{name: "following line prose", body: tt.command + "\nRebase on origin/main", want: true},
						{name: "following line prose with CRLF", body: tt.command + "\r\nRebase on origin/main", want: true},
						{name: "quoted markdown", body: "> " + tt.command, want: false},
						{name: "inline code", body: "`" + tt.command + "`", want: false},
						{name: "command line after prose", body: "Please run:\n" + tt.command, want: true},
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

func TestReviewersUseStickyPRComments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir            string
		file           string
		repository     string
		marker         string
		templatePrefix string
	}{
		{
			dir:            "self-development",
			file:           "kelos-reviewer.yaml",
			repository:     "kelos-dev/kelos",
			marker:         "<!-- kelos-reviewer:sticky-review -->",
			templatePrefix: "Format the PR comment body as:",
		},
		{
			dir:            "self-development",
			file:           "kelos-api-reviewer.yaml",
			repository:     "kelos-dev/kelos",
			marker:         "<!-- kelos-api-reviewer:sticky-review -->",
			templatePrefix: "Format the PR comment body as:",
		},
		{
			dir:            "self-development",
			file:           "kelos-glm-reviewer.yaml",
			repository:     "kelos-dev/kelos",
			marker:         "<!-- kelos-glm-reviewer:sticky-review -->",
			templatePrefix: "Format the PR comment body as:",
		},
		{
			dir:            "self-development",
			file:           "kelos-glm-api-reviewer.yaml",
			repository:     "kelos-dev/kelos",
			marker:         "<!-- kelos-glm-api-reviewer:sticky-review -->",
			templatePrefix: "Format the PR comment body as:",
		},
		{
			dir:            "self-development/kanon",
			file:           "kanon-reviewer.yaml",
			repository:     "kelos-dev/kanon",
			marker:         "<!-- kanon-reviewer:sticky-review -->",
			templatePrefix: "Format the PR comment body as:",
		},
		{
			dir:            "self-development/agora",
			file:           "agora-reviewer.yaml",
			repository:     "kelos-dev/agora",
			marker:         "<!-- agora-reviewer:sticky-review -->",
			templatePrefix: "Format the PR comment body as:",
		},
	}

	forbidden := []string{
		`gh pr review`,
		`/pulls/{{.Number}}/reviews`,
		`comments=[`,
		`.user.login == "kelos-bot[bot]"`,
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			ts := readTaskSpawnerFromDir(t, tt.dir, tt.file)
			prompt := ts.Spec.TaskTemplate.PromptTemplate

			if !strings.Contains(prompt, tt.marker) {
				t.Fatalf("%s prompt does not contain sticky marker %q", tt.file, tt.marker)
			}
			templateStart := strings.Index(prompt, tt.templatePrefix)
			if templateStart == -1 {
				t.Fatalf("%s prompt does not contain PR body template heading %q", tt.file, tt.templatePrefix)
			}
			markerStart := strings.Index(prompt[templateStart:], tt.marker)
			if markerStart == -1 {
				t.Fatalf("%s PR body template does not contain sticky marker %q", tt.file, tt.marker)
			}
			required := []string{
				`comment_author=$(gh api user --jq .login)`,
				`repos/` + tt.repository + `/issues/{{.Number}}/comments?per_page=100`,
				`--paginate`,
				`--slurp`,
				`.user.login == env.STICKY_COMMENT_AUTHOR`,
				`--method PATCH`,
				`repos/` + tt.repository + `/issues/comments/$comment_id`,
				`repos/` + tt.repository + `/issues/{{.Number}}/comments`,
			}
			for _, want := range required {
				if !strings.Contains(prompt, want) {
					t.Fatalf("%s/%s prompt does not contain sticky comment flow fragment %q", tt.dir, tt.file, want)
				}
			}
			for _, notWant := range forbidden {
				if strings.Contains(prompt, notWant) {
					t.Fatalf("%s/%s prompt contains forbidden review submission fragment %q", tt.dir, tt.file, notWant)
				}
			}
		})
	}
}

func TestKelosAPIReviewersUseConcreteIssueCommentBodyFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file string
		path string
	}{
		{
			file: "kelos-api-reviewer.yaml",
			path: "/tmp/kelos-api-reviewer-comment.md",
		},
		{
			file: "kelos-glm-api-reviewer.yaml",
			path: "/tmp/kelos-glm-api-reviewer-comment.md",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			ts := readSelfDevelopmentTaskSpawner(t, tt.file)
			prompt := ts.Spec.TaskTemplate.PromptTemplate
			want := `gh issue comment {{.Number}} --body-file ` + tt.path
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s prompt does not contain concrete issue comment command %q", tt.file, want)
			}
			if strings.Contains(prompt, `--body-file <file>`) {
				t.Fatalf("%s prompt contains placeholder issue comment body file", tt.file)
			}
		})
	}
}

func TestDevelopmentReviewersUseReviewSkills(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir      string
		file     string
		workflow string
	}{
		{dir: "self-development", file: "kelos-reviewer.yaml", workflow: "Use the `review-all` skill with `origin/main` as the base"},
		{dir: "self-development/agora", file: "agora-reviewer.yaml", workflow: "Use the `review-all` skill with `origin/main` as the base"},
		{dir: "self-development/kanon", file: "kanon-reviewer.yaml", workflow: "Use the `review-all` skill with `origin/main` as the base"},
		{dir: "self-development", file: "kelos-api-reviewer.yaml", workflow: "Use the `api-review` skill for the review analysis"},
		{dir: "self-development", file: "kelos-glm-api-reviewer.yaml", workflow: "Use the `api-review` skill for the review analysis"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			spawner := readTaskSpawnerFromDir(t, tt.dir, tt.file)
			if !strings.Contains(spawner.Spec.TaskTemplate.PromptTemplate, tt.workflow) {
				t.Fatalf("%s/%s prompt does not require %q", tt.dir, tt.file, tt.workflow)
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
	assertReviewerTriggerableByBot(t, "self-development", "kelos-reviewer.yaml", "kelos-dev/kelos", "/kelos review")
}

// TestKelosAPIReviewerTriggerableByBot verifies the kelos-api-reviewer spawner
// accepts a `/kelos api-review` request posted by the Kelos bot itself, not only
// by human maintainers. Workers post `/kelos api-review` after pushing API
// changes, so the reviewer must not exclude `kelos-bot[bot]`.
func TestKelosAPIReviewerTriggerableByBot(t *testing.T) {
	t.Parallel()
	assertReviewerTriggerableByBot(t, "self-development", "kelos-api-reviewer.yaml", "kelos-dev/kelos", "/kelos api-review")
}

func TestKanonReviewerTriggerableByBot(t *testing.T) {
	t.Parallel()
	assertReviewerTriggerableByBot(t, "self-development/kanon", "kanon-reviewer.yaml", "kelos-dev/kanon", "/kelos review")
}

func TestKelosGLMReviewerTriggerableByBot(t *testing.T) {
	t.Parallel()
	assertReviewerTriggerableByBot(t, "self-development", "kelos-glm-reviewer.yaml", "kelos-dev/kelos", "/kelos glm-review")
}

func TestKelosGLMAPIReviewerTriggerableByBot(t *testing.T) {
	t.Parallel()
	assertReviewerTriggerableByBot(t, "self-development", "kelos-glm-api-reviewer.yaml", "kelos-dev/kelos", "/kelos glm-api-review")
}

func TestAgoraReviewerTriggerableByBot(t *testing.T) {
	t.Parallel()
	assertReviewerTriggerableByBot(t, "self-development/agora", "agora-reviewer.yaml", "kelos-dev/agora", "/kelos review")
}

func TestAgoraIssueCreatorsUseTriageAcceptedLabel(t *testing.T) {
	t.Parallel()

	tests := []string{
		"agora-fake-strategist.yaml",
		"agora-fake-user.yaml",
		"agora-pr-responder.yaml",
		"agora-self-update.yaml",
		"agora-workers.yaml",
	}

	for _, file := range tests {
		file := file
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			ts := readTaskSpawnerFromDir(t, "self-development/agora", file)
			prompt := ts.Spec.TaskTemplate.PromptTemplate
			if !strings.Contains(prompt, "triage-accepted") {
				t.Fatalf("%s prompt does not mention triage-accepted", file)
			}
		})
	}
}

func TestStickyIssueSlotCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir    string
		file   string
		marker string
	}{
		{dir: "self-development", file: "kelos-fake-strategist.yaml", marker: "kelos-fake-strategist"},
		{dir: "self-development", file: "kelos-fake-user.yaml", marker: "kelos-fake-user"},
		{dir: "self-development", file: "kelos-self-update.yaml", marker: "kelos-self-update"},
		{dir: "self-development/agora", file: "agora-fake-strategist.yaml", marker: "agora-fake-strategist"},
		{dir: "self-development/agora", file: "agora-fake-user.yaml", marker: "agora-fake-user"},
		{dir: "self-development/agora", file: "agora-self-update.yaml", marker: "agora-self-update"},
		{dir: "self-development/kanon", file: "kanon-fake-strategist.yaml", marker: "kanon-fake-strategist"},
		{dir: "self-development/kanon", file: "kanon-fake-user.yaml", marker: "kanon-fake-user"},
		{dir: "self-development/kanon", file: "kanon-self-update.yaml", marker: "kanon-self-update"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			ts := readTaskSpawnerFromDir(t, tt.dir, tt.file)
			assertStickyIssueCommandsUseTriageAccepted(t, tt.dir+"/"+tt.file, ts.Spec.TaskTemplate.PromptTemplate)
			assertStickyIssueLookupUsesExactMarker(t, tt.dir+"/"+tt.file, ts.Spec.TaskTemplate.PromptTemplate, tt.marker)
		})
	}
}

func TestManualFakeStrategistTaskStickyIssueCommands(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "self-development", "tasks", "fake-strategist-task.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	assertStickyIssueCommandsUseTriageAccepted(t, path, string(data))
	assertStickyIssueLookupUsesExactMarker(t, path, string(data), "kelos-fake-strategist")

	var task kelos.Task
	if err := sigyaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	wantAgentConfigs := []kelos.AgentConfigReference{{Name: "base-agent"}, {Name: "kelos-fake-strategist-agent"}}
	if !reflect.DeepEqual(task.Spec.Worker.AgentConfigRefs, wantAgentConfigs) {
		t.Fatalf("manual fake strategist agentConfigRefs = %v, want %v", task.Spec.Worker.AgentConfigRefs, wantAgentConfigs)
	}
}

func TestInteractiveDevelopmentSessionUsesBaseAgent(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "self-development", "cs")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	start := bytes.Index(data, []byte("apiVersion:"))
	if start < 0 {
		t.Fatalf("finding embedded Session manifest in %s", path)
	}
	end := bytes.Index(data[start:], []byte("\nEOF"))
	if end < 0 {
		t.Fatalf("finding embedded Session manifest in %s", path)
	}

	var session kelos.Session
	if err := sigyaml.Unmarshal(data[start:start+end], &session); err != nil {
		t.Fatalf("decoding embedded Session from %s: %v", path, err)
	}
	wantAgentConfigs := []kelos.AgentConfigReference{{Name: "base-agent"}}
	if !reflect.DeepEqual(session.Spec.Worker.AgentConfigRefs, wantAgentConfigs) {
		t.Fatalf("interactive Session agentConfigRefs = %v, want %v", session.Spec.Worker.AgentConfigRefs, wantAgentConfigs)
	}
}

func TestAgoraPlannerOnlyTriggersForIssues(t *testing.T) {
	t.Parallel()

	ts := readTaskSpawnerFromDir(t, "self-development/agora", "agora-planner.yaml")
	spawner := ts.Spec.When.GitHubWebhook
	if spawner == nil {
		t.Fatal("expected agora-planner.yaml to use githubWebhook")
	}
	if len(spawner.Filters) != 1 {
		t.Fatalf("agora-planner filters length = %d, want 1", len(spawner.Filters))
	}

	filter := spawner.Filters[0]
	issuePayload := developmentWebhookPayload(t, spawner.Repository, filter, "/kelos plan")
	issueEvent, err := webhook.ParseGitHubWebhook("issue_comment", issuePayload)
	if err != nil {
		t.Fatalf("ParseGitHubWebhook(issue) error = %v", err)
	}
	if got, err := webhook.MatchesGitHubEvent(spawner, "issue_comment", issueEvent); err != nil || !got {
		t.Fatalf("MatchesGitHubEvent(issue) = %v, %v, want true, nil", got, err)
	}

	prPayload := []byte(`{
		"action": "created",
		"sender": {"login": "gjkim42"},
		"issue": {
			"number": 1,
			"title": "Test PR",
			"state": "open",
			"html_url": "https://github.com/kelos-dev/agora/pull/1",
			"pull_request": {
				"url": "https://api.github.com/repos/kelos-dev/agora/pulls/1",
				"html_url": "https://github.com/kelos-dev/agora/pull/1"
			}
		},
		"comment": {"body": "/kelos plan"},
		"repository": {"full_name": "kelos-dev/agora", "owner": {"login": "kelos-dev"}, "name": "agora"}
	}`)
	prEvent, err := webhook.ParseGitHubWebhook("issue_comment", prPayload)
	if err != nil {
		t.Fatalf("ParseGitHubWebhook(pr) error = %v", err)
	}
	if got, err := webhook.MatchesGitHubEvent(spawner, "issue_comment", prEvent); err != nil || got {
		t.Fatalf("MatchesGitHubEvent(pr) = %v, %v, want false, nil", got, err)
	}
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
		{dir: "self-development", file: "kelos-glm-reviewer.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-glm-api-reviewer.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-self-update.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-fake-strategist.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-triage.yaml", effort: "high"},
		{dir: "self-development", file: "kelos-pr-responder.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-config-update.yaml", effort: "xhigh"},
		{dir: "self-development", file: "kelos-image-update.yaml", effort: "medium"},
		{dir: "self-development", file: "kelos-fake-user.yaml", effort: "medium"},
		{dir: "self-development", file: "kelos-squash-commits.yaml", effort: "medium"},
		{dir: "self-development/kanon", file: "kanon-workers.yaml", effort: "xhigh"},
		{dir: "self-development/kanon", file: "kanon-planner.yaml", effort: "xhigh"},
		{dir: "self-development/kanon", file: "kanon-reviewer.yaml", effort: "xhigh"},
		{dir: "self-development/kanon", file: "kanon-self-update.yaml", effort: "xhigh"},
		{dir: "self-development/kanon", file: "kanon-fake-strategist.yaml", effort: "xhigh"},
		{dir: "self-development/kanon", file: "kanon-triage.yaml", effort: "high"},
		{dir: "self-development/kanon", file: "kanon-pr-responder.yaml", effort: "xhigh"},
		{dir: "self-development/kanon", file: "kanon-config-update.yaml", effort: "xhigh"},
		{dir: "self-development/kanon", file: "kanon-fake-user.yaml", effort: "medium"},
		{dir: "self-development/kanon", file: "kanon-squash-commits.yaml", effort: "medium"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.dir+"/"+tt.file, func(t *testing.T) {
			t.Parallel()

			worker := readSpawnerWorkerFromDir(t, tt.dir, tt.file)
			if worker.Effort != tt.effort {
				t.Fatalf("Worker.Effort = %q, want %q", worker.Effort, tt.effort)
			}
		})
	}
}

func TestKelosGLMReviewersUseOpenCodeGLM52(t *testing.T) {
	t.Parallel()

	tests := []string{
		"kelos-glm-reviewer.yaml",
		"kelos-glm-api-reviewer.yaml",
	}

	for _, file := range tests {
		file := file
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			ts := readTaskSpawnerFromDir(t, "self-development", file)
			if ts.Spec.TaskTemplate.Worker == nil {
				t.Fatal("TaskTemplate.Worker is nil")
			}
			worker := ts.Spec.TaskTemplate.Worker
			if worker.Type != "opencode" {
				t.Fatalf("TaskTemplate.Worker.Type = %q, want opencode", worker.Type)
			}
			if worker.Model != "zai/glm-5.2" {
				t.Fatalf("TaskTemplate.Worker.Model = %q, want zai/glm-5.2", worker.Model)
			}
			if worker.Credentials == nil {
				t.Fatal("TaskTemplate.Worker.Credentials is nil")
			}
			if worker.Credentials.Type != kelos.CredentialTypeAPIKey {
				t.Fatalf("TaskTemplate.Worker.Credentials.Type = %q, want %q", worker.Credentials.Type, kelos.CredentialTypeAPIKey)
			}
		})
	}
}

func TestKelosGLMReviewersDoNotMatchCodexReviewCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file         string
		codexCommand string
	}{
		{file: "kelos-glm-reviewer.yaml", codexCommand: "/kelos review"},
		{file: "kelos-glm-api-reviewer.yaml", codexCommand: "/kelos api-review"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			ts := readTaskSpawnerFromDir(t, "self-development", tt.file)
			spawner := ts.Spec.When.GitHubWebhook
			if spawner == nil {
				t.Fatalf("expected %s to use githubWebhook", tt.file)
			}
			for _, filter := range spawner.Filters {
				filter := filter
				payload := developmentWebhookPayload(t, spawner.Repository, filter, tt.codexCommand)
				eventData, err := webhook.ParseGitHubWebhook(filter.Event, payload)
				if err != nil {
					t.Fatalf("ParseGitHubWebhook() error = %v", err)
				}
				got, err := webhook.MatchesGitHubEvent(spawner, filter.Event, eventData)
				if err != nil {
					t.Fatalf("MatchesGitHubEvent() error = %v", err)
				}
				if got {
					t.Fatalf("%s matched Codex command %q for %s/%s", tt.file, tt.codexCommand, filter.Event, filter.Author)
				}
			}
		})
	}
}

func TestSelfDevelopmentUsesWorkerSpec(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "self-development")
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		t.Run(rel, func(t *testing.T) {
			assertSelfDevelopmentYAMLUsesWorkerSpec(t, path)
		})
		return nil
	}); err != nil {
		t.Fatalf("walking self-development YAML files: %v", err)
	}
}

// assertReviewerTriggerableByBot checks that the given reviewer spawner triggers
// for both the Kelos bot and a maintainer posting bodyPattern on an open PR, but
// not for an unauthorized user.
func assertReviewerTriggerableByBot(t *testing.T, dir, file, repository, bodyPattern string) {
	t.Helper()

	ts := readTaskSpawnerFromDir(t, dir, file)
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
				"pull_request": {"url": "https://api.github.com/repos/` + repository + `/pulls/1"}
			},
			"comment": {"body": "` + bodyPattern + `"},
			"repository": {"full_name": "` + repository + `", "owner": {"login": "kelos-dev"}, "name": "` + strings.TrimPrefix(repository, "kelos-dev/") + `"}
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

func assertSelfDevelopmentYAMLUsesWorkerSpec(t *testing.T, path string) {
	t.Helper()

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
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
		}
		if err := sigyaml.Unmarshal(doc, &meta); err != nil {
			t.Fatalf("decoding document metadata from %s: %v", path, err)
		}
		if strings.HasPrefix(meta.APIVersion, "kelos.dev/") && meta.APIVersion != "kelos.dev/v1alpha2" {
			t.Fatalf("%s uses %s, want kelos.dev/v1alpha2", meta.Kind, meta.APIVersion)
		}

		var raw map[string]any
		if err := sigyaml.Unmarshal(doc, &raw); err != nil {
			t.Fatalf("decoding raw YAML document from %s: %v", path, err)
		}

		switch meta.Kind {
		case "TaskSpawner":
			var ts kelos.TaskSpawner
			if err := sigyaml.Unmarshal(doc, &ts); err != nil {
				t.Fatalf("decoding TaskSpawner from %s: %v", path, err)
			}
			assertNoLegacyAgentConfigRef(t, path, raw, "spec", "taskTemplate")
			assertTaskTemplateUsesWorkerSpec(t, path, ts.Spec.TaskTemplate)
		case "SessionSpawner":
			var spawner kelos.SessionSpawner
			if err := sigyaml.Unmarshal(doc, &spawner); err != nil {
				t.Fatalf("decoding SessionSpawner from %s: %v", path, err)
			}
			assertWorkerSpecComplete(t, path, spawner.Spec.SessionTemplate.Worker)
		case "Task":
			var task kelos.Task
			if err := sigyaml.Unmarshal(doc, &task); err != nil {
				t.Fatalf("decoding Task from %s: %v", path, err)
			}
			assertNoLegacyAgentConfigRef(t, path, raw, "spec")
			assertTaskUsesWorkerSpec(t, path, task.Spec)
		}
	}
}

func assertNoLegacyAgentConfigRef(t *testing.T, path string, raw map[string]any, keys ...string) {
	t.Helper()

	current := any(raw)
	for _, key := range keys {
		mapping, ok := current.(map[string]any)
		if !ok {
			return
		}
		current, ok = mapping[key]
		if !ok {
			return
		}
	}
	mapping, ok := current.(map[string]any)
	if !ok {
		return
	}
	if _, ok := mapping["agentConfigRef"]; ok {
		t.Fatalf("%s %s.agentConfigRef is deprecated; use %s.worker.agentConfigRefs", path, strings.Join(keys, "."), strings.Join(keys, "."))
	}
}

func assertTaskTemplateUsesWorkerSpec(t *testing.T, path string, template kelos.TaskTemplate) {
	t.Helper()

	if template.Worker == nil && template.WorkerPoolRef == nil {
		t.Fatalf("%s taskTemplate must set worker or workerPoolRef", path)
	}
	if template.Worker != nil {
		assertWorkerSpecComplete(t, path, *template.Worker)
	}
	if template.Type != "" {
		t.Fatalf("%s taskTemplate.type is deprecated; use taskTemplate.worker.type", path)
	}
	if template.Credentials != nil {
		t.Fatalf("%s taskTemplate.credentials is deprecated; use taskTemplate.worker.credentials", path)
	}
	if template.Model != "" {
		t.Fatalf("%s taskTemplate.model is deprecated; use taskTemplate.worker.model", path)
	}
	if template.Effort != "" {
		t.Fatalf("%s taskTemplate.effort is deprecated; use taskTemplate.worker.effort", path)
	}
	if template.Image != "" {
		t.Fatalf("%s taskTemplate.image is deprecated; use taskTemplate.worker.image", path)
	}
	if template.WorkspaceRef != nil {
		t.Fatalf("%s taskTemplate.workspaceRef is deprecated; use taskTemplate.worker.workspaceRef", path)
	}
	if len(template.AgentConfigRefs) != 0 {
		t.Fatalf("%s taskTemplate.agentConfigRefs is deprecated; use taskTemplate.worker.agentConfigRefs", path)
	}
	if template.PodOverrides != nil {
		t.Fatalf("%s taskTemplate.podOverrides is deprecated; use taskTemplate.worker.podOverrides", path)
	}
}

func assertTaskUsesWorkerSpec(t *testing.T, path string, spec kelos.TaskSpec) {
	t.Helper()

	if spec.Worker == nil && spec.WorkerPoolRef == nil {
		t.Fatalf("%s spec must set worker or workerPoolRef", path)
	}
	if spec.Worker != nil {
		assertWorkerSpecComplete(t, path, *spec.Worker)
	}
	if spec.Type != "" {
		t.Fatalf("%s spec.type is deprecated; use spec.worker.type", path)
	}
	if spec.Credentials != nil {
		t.Fatalf("%s spec.credentials is deprecated; use spec.worker.credentials", path)
	}
	if spec.Model != "" {
		t.Fatalf("%s spec.model is deprecated; use spec.worker.model", path)
	}
	if spec.Effort != "" {
		t.Fatalf("%s spec.effort is deprecated; use spec.worker.effort", path)
	}
	if spec.Image != "" {
		t.Fatalf("%s spec.image is deprecated; use spec.worker.image", path)
	}
	if spec.WorkspaceRef != nil {
		t.Fatalf("%s spec.workspaceRef is deprecated; use spec.worker.workspaceRef", path)
	}
	if len(spec.AgentConfigRefs) != 0 {
		t.Fatalf("%s spec.agentConfigRefs is deprecated; use spec.worker.agentConfigRefs", path)
	}
	if spec.PodOverrides != nil {
		t.Fatalf("%s spec.podOverrides is deprecated; use spec.worker.podOverrides", path)
	}
}

func assertWorkerSpecComplete(t *testing.T, path string, worker kelos.WorkerSpec) {
	t.Helper()

	if len(worker.AgentConfigRefs) == 0 || worker.AgentConfigRefs[0].Name != "base-agent" {
		t.Fatalf("%s worker.agentConfigRefs must start with base-agent, got %v", path, worker.AgentConfigRefs)
	}
	if worker.Type == "" {
		t.Fatalf("%s worker.type is empty", path)
	}
	if worker.Credentials == nil {
		t.Fatalf("%s worker.credentials is nil", path)
	}
}

func readTaskSpawnerFromDir(t *testing.T, dir, file string) *kelos.TaskSpawner {
	t.Helper()

	taskSpawner, _ := readSpawnerFromDir(t, dir, file)
	if taskSpawner == nil {
		t.Fatalf("no TaskSpawner found in %s", filepath.Join("..", "..", dir, file))
	}
	return taskSpawner
}

func readGitHubWebhookFromDir(t *testing.T, dir, file string) *kelos.GitHubWebhook {
	t.Helper()

	taskSpawner, sessionSpawner := readSpawnerFromDir(t, dir, file)
	if taskSpawner != nil {
		return taskSpawner.Spec.When.GitHubWebhook
	}
	return sessionSpawner.Spec.When.GitHubWebhook
}

func readSpawnerWorkerFromDir(t *testing.T, dir, file string) *kelos.WorkerSpec {
	t.Helper()

	taskSpawner, sessionSpawner := readSpawnerFromDir(t, dir, file)
	if taskSpawner != nil {
		if taskSpawner.Spec.TaskTemplate.Worker == nil {
			t.Fatal("TaskTemplate.Worker is nil")
		}
		return taskSpawner.Spec.TaskTemplate.Worker
	}
	return &sessionSpawner.Spec.SessionTemplate.Worker
}

func readSpawnerFromDir(t *testing.T, dir, file string) (*kelos.TaskSpawner, *kelos.SessionSpawner) {
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
		switch meta.Kind {
		case "TaskSpawner":
			var spawner kelos.TaskSpawner
			if err := sigyaml.Unmarshal(doc, &spawner); err != nil {
				t.Fatalf("decoding TaskSpawner from %s: %v", path, err)
			}
			return &spawner, nil
		case "SessionSpawner":
			var spawner kelos.SessionSpawner
			if err := sigyaml.Unmarshal(doc, &spawner); err != nil {
				t.Fatalf("decoding SessionSpawner from %s: %v", path, err)
			}
			return nil, &spawner
		}
	}

	t.Fatalf("no TaskSpawner or SessionSpawner found in %s", path)
	return nil, nil
}

func readAgentConfigFromDir(t *testing.T, dir, file string) *kelos.AgentConfig {
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
		if meta.Kind != "AgentConfig" {
			continue
		}

		var config kelos.AgentConfig
		if err := sigyaml.Unmarshal(doc, &config); err != nil {
			t.Fatalf("decoding AgentConfig from %s: %v", path, err)
		}
		return &config
	}

	t.Fatalf("no AgentConfig found in %s", path)
	return nil
}

func assertStickyIssueCommandsUseTriageAccepted(t *testing.T, name, prompt string) {
	t.Helper()

	wantUpdate := `gh issue edit <number> --title "<title>" --body-file /tmp/issue-body.md --add-label triage-accepted`
	wantCreate := `gh issue create --title "<title>" --body-file /tmp/issue-body.md --label generated-by-kelos --label triage-accepted`
	if !strings.Contains(prompt, wantUpdate) {
		t.Fatalf("%s prompt does not add triage-accepted when updating a sticky issue slot", name)
	}
	if !strings.Contains(prompt, wantCreate) {
		t.Fatalf("%s prompt does not add triage-accepted when creating a sticky issue slot", name)
	}
}

func assertStickyIssueLookupUsesExactMarker(t *testing.T, name, prompt, marker string) {
	t.Helper()

	want := `gh issue list --state open --label generated-by-kelos --limit 1000 --json number,title,body,assignees --jq 'map(select((.body // "") | contains("<!-- kelos-taskspawner=` + marker + ` -->")))'`
	if !strings.Contains(prompt, want) {
		t.Fatalf("%s prompt does not filter sticky issue slots by the exact marker", name)
	}
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
