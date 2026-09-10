package conversion

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestTaskSpawnerToHub_FoldsLegacyCommentAndPollInterval(t *testing.T) {
	src := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "ts", Namespace: "default"},
		Spec: v1alpha1.TaskSpawnerSpec{
			PollInterval: "7m",
			When: v1alpha1.When{
				GitHubIssues: &v1alpha1.GitHubIssues{
					Repo:            "owner/repo",
					TriggerComment:  "/kelos go",
					ExcludeComments: []string{"/kelos stop"},
				},
			},
		},
	}

	dst := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}

	gi := dst.Spec.When.GitHubIssues
	if gi == nil {
		t.Fatal("githubIssues nil after conversion")
	}
	if gi.PollInterval != "7m" {
		t.Errorf("githubIssues.pollInterval = %q, want 7m (folded from root)", gi.PollInterval)
	}
	if gi.CommentPolicy == nil {
		t.Fatal("commentPolicy nil; legacy fields were not folded")
	}
	if gi.CommentPolicy.TriggerComment != "/kelos go" {
		t.Errorf("commentPolicy.triggerComment = %q", gi.CommentPolicy.TriggerComment)
	}
	if len(gi.CommentPolicy.ExcludeComments) != 1 || gi.CommentPolicy.ExcludeComments[0] != "/kelos stop" {
		t.Errorf("commentPolicy.excludeComments = %v", gi.CommentPolicy.ExcludeComments)
	}
}

func TestTaskSpawnerToHub_DoesNotOverrideSourcePollInterval(t *testing.T) {
	src := &v1alpha1.TaskSpawner{
		Spec: v1alpha1.TaskSpawnerSpec{
			PollInterval: "7m",
			When: v1alpha1.When{
				GitHubPullRequests: &v1alpha1.GitHubPullRequests{
					Repo:         "owner/repo",
					PollInterval: "2m",
				},
			},
		},
	}
	dst := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if got := dst.Spec.When.GitHubPullRequests.PollInterval; got != "2m" {
		t.Errorf("githubPullRequests.pollInterval = %q, want 2m (source value kept)", got)
	}
}

func TestTaskSpawnerToHub_MergesLegacyCommentFieldsIntoPolicy(t *testing.T) {
	src := &v1alpha1.TaskSpawner{
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{
				GitHubIssues: &v1alpha1.GitHubIssues{
					CommentPolicy: &v1alpha1.GitHubCommentPolicy{
						MinimumPermission: "write",
					},
					TriggerComment:  "/go",
					ExcludeComments: []string{"/stop"},
				},
				GitHubPullRequests: &v1alpha1.GitHubPullRequests{
					CommentPolicy: &v1alpha1.GitHubCommentPolicy{
						AllowedUsers: []string{"alice"},
					},
					TriggerComment: "/review",
				},
			},
		},
	}

	dst := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}

	gi := dst.Spec.When.GitHubIssues
	if gi.CommentPolicy == nil {
		t.Fatal("githubIssues.commentPolicy nil")
	}
	if gi.CommentPolicy.MinimumPermission != "write" {
		t.Errorf("githubIssues.commentPolicy.minimumPermission = %q, want write", gi.CommentPolicy.MinimumPermission)
	}
	if gi.CommentPolicy.TriggerComment != "/go" {
		t.Errorf("githubIssues.commentPolicy.triggerComment = %q, want /go", gi.CommentPolicy.TriggerComment)
	}
	if len(gi.CommentPolicy.ExcludeComments) != 1 || gi.CommentPolicy.ExcludeComments[0] != "/stop" {
		t.Errorf("githubIssues.commentPolicy.excludeComments = %#v, want [/stop]", gi.CommentPolicy.ExcludeComments)
	}

	pr := dst.Spec.When.GitHubPullRequests
	if pr.CommentPolicy == nil {
		t.Fatal("githubPullRequests.commentPolicy nil")
	}
	if len(pr.CommentPolicy.AllowedUsers) != 1 || pr.CommentPolicy.AllowedUsers[0] != "alice" {
		t.Errorf("githubPullRequests.commentPolicy.allowedUsers = %#v, want [alice]", pr.CommentPolicy.AllowedUsers)
	}
	if pr.CommentPolicy.TriggerComment != "/review" {
		t.Errorf("githubPullRequests.commentPolicy.triggerComment = %q, want /review", pr.CommentPolicy.TriggerComment)
	}
}

func TestTaskSpawnerToHub_FoldsTaskTemplateAgentConfigRefIntoRefs(t *testing.T) {
	src := &v1alpha1.TaskSpawner{
		Spec: v1alpha1.TaskSpawnerSpec{
			TaskTemplate: v1alpha1.TaskTemplate{
				AgentConfigRef: &v1alpha1.AgentConfigReference{Name: "legacy-config"},
			},
		},
	}

	dst := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if len(dst.Spec.TaskTemplate.AgentConfigRefs) != 1 {
		t.Fatalf("taskTemplate.agentConfigRefs length = %d, want 1", len(dst.Spec.TaskTemplate.AgentConfigRefs))
	}
	if dst.Spec.TaskTemplate.AgentConfigRefs[0].Name != "legacy-config" {
		t.Errorf("taskTemplate.agentConfigRefs[0].name = %q, want legacy-config", dst.Spec.TaskTemplate.AgentConfigRefs[0].Name)
	}
}

func TestTaskSpawnerFromHub_BackfillsLegacyTaskTemplateFieldsFromWorker(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "ts", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			TaskTemplate: v1alpha2.TaskTemplate{
				Worker: &v1alpha2.WorkerSpec{
					Type: "codex",
					Credentials: &v1alpha2.Credentials{
						Type:      v1alpha2.CredentialTypeAPIKey,
						SecretRef: &v1alpha2.SecretReference{Name: "creds"},
					},
					Model:        "gpt-5",
					Effort:       "high",
					Image:        "agent:latest",
					WorkspaceRef: &v1alpha2.WorkspaceReference{Name: "workspace"},
					AgentConfigRefs: []v1alpha2.AgentConfigReference{
						{Name: "config"},
					},
					PodOverrides: &v1alpha2.PodOverrides{
						Env: []corev1.EnvVar{{Name: "FOO", Value: "bar"}},
					},
				},
			},
		},
	}

	dst := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}

	template := dst.Spec.TaskTemplate
	if template.Type != "codex" {
		t.Errorf("type = %q, want codex", template.Type)
	}
	if template.Credentials.Type != v1alpha1.CredentialTypeAPIKey || template.Credentials.SecretRef == nil || template.Credentials.SecretRef.Name != "creds" {
		t.Errorf("credentials not backfilled: %#v", template.Credentials)
	}
	if template.Model != "gpt-5" || template.Effort != "high" || template.Image != "agent:latest" {
		t.Errorf("model/effort/image not backfilled: %#v", template)
	}
	if template.WorkspaceRef == nil || template.WorkspaceRef.Name != "workspace" {
		t.Errorf("workspaceRef not backfilled: %#v", template.WorkspaceRef)
	}
	if len(template.AgentConfigRefs) != 1 || template.AgentConfigRefs[0].Name != "config" {
		t.Errorf("agentConfigRefs not backfilled: %#v", template.AgentConfigRefs)
	}
	if template.PodOverrides == nil || len(template.PodOverrides.Env) != 1 || template.PodOverrides.Env[0].Name != "FOO" {
		t.Errorf("podOverrides not backfilled: %#v", template.PodOverrides)
	}
}

func TestTaskSpawnerConvert_WebhookFilterFieldsPreserved(t *testing.T) {
	src := &v1alpha1.TaskSpawner{
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{
				GitHubWebhook: &v1alpha1.GitHubWebhook{
					Filters: []v1alpha1.GitHubWebhookFilter{
						{BodyContains: "/deploy v1.2+x", BodyPattern: "ship-it", Tag: "v*"},
					},
				},
			},
		},
	}
	dst := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	gotFilter := dst.Spec.When.GitHubWebhook.Filters[0]
	if gotFilter.BodyContains != "/deploy v1.2+x" {
		t.Errorf("bodyContains = %q, want it preserved", gotFilter.BodyContains)
	}
	if gotFilter.BodyPattern != "ship-it" {
		t.Errorf("bodyPattern = %q, want it preserved", gotFilter.BodyPattern)
	}
	if gotFilter.Tag != "v*" {
		t.Errorf("tag = %q, want it preserved", gotFilter.Tag)
	}

	back := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), dst, back); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	backFilter := back.Spec.When.GitHubWebhook.Filters[0]
	if backFilter.BodyContains != "/deploy v1.2+x" || backFilter.BodyPattern != "ship-it" || backFilter.Tag != "v*" {
		t.Errorf("round-trip filter = %+v, want fields preserved", backFilter)
	}
}

func TestTaskSpawnerConvert_ModernFieldsRoundTrip(t *testing.T) {
	optional := "5m"
	src := &v1alpha1.TaskSpawner{
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{
				GitHubIssues: &v1alpha1.GitHubIssues{
					Repo:         "owner/repo",
					PollInterval: optional,
					CommentPolicy: &v1alpha1.GitHubCommentPolicy{
						TriggerComment:    "/go",
						MinimumPermission: "write",
					},
				},
			},
		},
	}
	hub := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), src, hub); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	back := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, back); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	gi := back.Spec.When.GitHubIssues
	if gi.PollInterval != "5m" || gi.CommentPolicy == nil || gi.CommentPolicy.TriggerComment != "/go" || gi.CommentPolicy.MinimumPermission != "write" {
		t.Errorf("modern fields not preserved: %#v", gi)
	}
}

// TestTaskSpawnerConvert_SlackExcludeFiltersRoundTrip verifies that the
// v1alpha2-only slack excludeFilters field survives a v1alpha1 round-trip via
// its preservation annotation while shared Slack fields are carried directly.
func TestTaskSpawnerConvert_SlackExcludeFiltersRoundTrip(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				Slack: &v1alpha2.Slack{
					Channels:       []string{"C0123456789"},
					ExcludeFilters: []v1alpha2.SlackFilter{{Channels: []string{"C9876543210", "D0123456789"}}},
				},
			},
		},
	}

	down := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), src, down); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if down.Spec.When.Slack == nil {
		t.Fatal("expected slack config after down-conversion")
	}
	if len(down.Spec.When.Slack.Channels) != 1 || down.Spec.When.Slack.Channels[0] != "C0123456789" {
		t.Errorf("shared channels not preserved: %#v", down.Spec.When.Slack.Channels)
	}
	// v1alpha1 cannot represent excludeFilters — the rules survive only via the
	// preservation annotation.
	if raw, ok := down.Annotations[preservedSlackExcludeFiltersAnnotation]; !ok ||
		raw != `[{"channels":["C9876543210","D0123456789"]}]` {
		t.Errorf("preservation annotation = %q, want the excludeFilters JSON", raw)
	}

	up := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), down, up); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if up.Spec.When.Slack == nil {
		t.Fatal("expected slack config after up-conversion")
	}
	got := up.Spec.When.Slack.ExcludeFilters
	if len(got) != 1 {
		t.Fatalf("restored %d exclusion rules, want 1: %#v", len(got), got)
	}
	if channels := got[0].Channels; len(channels) != 2 ||
		channels[0] != "C9876543210" || channels[1] != "D0123456789" {
		t.Errorf("exclusion rule not restored intact: %#v", got[0])
	}
	if _, ok := up.Annotations[preservedSlackExcludeFiltersAnnotation]; ok {
		t.Error("preservation annotation not cleaned up after restore")
	}
}

func TestTaskSpawnerToHub_MalformedSlackExcludeFiltersAnnotationIgnored(t *testing.T) {
	// The preservation annotation is user-editable; a malformed value must not
	// block conversion to the storage version. It is treated as absent and
	// stripped from the hub object so the internal key does not leak into the
	// v1alpha2 view.
	spoke := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat",
			Namespace: "default",
			Annotations: map[string]string{
				preservedSlackExcludeFiltersAnnotation: "[not valid json",
			},
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{Slack: &v1alpha1.Slack{Channels: []string{"C0123456789"}}},
		},
	}

	hub := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if hub.Spec.When.Slack == nil {
		t.Fatal("expected slack config after up-conversion")
	}
	if got := hub.Spec.When.Slack.ExcludeFilters; len(got) != 0 {
		t.Errorf("excludeFilters = %#v, want none from a malformed annotation", got)
	}
	if _, ok := hub.Annotations[preservedSlackExcludeFiltersAnnotation]; ok {
		t.Error("malformed preservation annotation should still be stripped from the hub object")
	}
}

// marshalChannelRule builds one exclusion rule carrying n unique, well-formed
// Slack channel IDs, for exercising the per-rule maxItems boundary.
func marshalChannelRule(t *testing.T, n int) string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("C%09d", i))
	}
	return marshalSlackRules(t, []v1alpha2.SlackFilter{{Channels: ids}})
}

// marshalChannelRules builds n exclusion rules, each carrying one channel ID,
// for exercising the rule-count maxItems boundary.
func marshalChannelRules(t *testing.T, n int) string {
	t.Helper()
	rules := make([]v1alpha2.SlackFilter, 0, n)
	for i := 0; i < n; i++ {
		rules = append(rules, v1alpha2.SlackFilter{Channels: []string{fmt.Sprintf("C%09d", i)}})
	}
	return marshalSlackRules(t, rules)
}

func marshalSlackRules(t *testing.T, rules []v1alpha2.SlackFilter) string {
	t.Helper()
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(raw)
}

func TestTaskSpawnerToHub_InvalidSlackExcludeFiltersAnnotationIgnored(t *testing.T) {
	// The API server does not re-validate conversion output, so annotation data
	// that violates the v1alpha2 constraints must not be restored — otherwise a
	// v1alpha1 write could plant values the v1alpha2 schema would have rejected.
	tests := []struct {
		name string
		raw  string
	}{
		{"channel id that fails the item pattern", `[{"channels":["c0123456789"]}]`},
		{"channel id that is too short", `[{"channels":["C123"]}]`},
		{"more channels than maxItems allows", marshalChannelRule(t, v1alpha2.SlackFilterChannelsMaxItems+1)},
		{"more rules than maxItems allows", marshalChannelRules(t, v1alpha2.SlackExcludeFiltersMaxItems+1)},
		{"duplicate channels in a set", `[{"channels":["C0123456789","C0123456789"]}]`},
		// A rule with no criteria matches every message, so restoring one would
		// silently stop the spawner from firing anywhere.
		{"rule with no criteria", `[{}]`},
		{"rule with an empty channel list", `[{"channels":[]}]`},
		{"a bare channel list rather than rules", `["C0123456789"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spoke := &v1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "chat",
					Namespace:   "default",
					Annotations: map[string]string{preservedSlackExcludeFiltersAnnotation: tt.raw},
				},
				Spec: v1alpha1.TaskSpawnerSpec{
					When: v1alpha1.When{Slack: &v1alpha1.Slack{Channels: []string{"C0123456789"}}},
				},
			}

			hub := &v1alpha2.TaskSpawner{}
			if err := taskSpawnerToHub(context.Background(), spoke, hub); err != nil {
				t.Fatalf("taskSpawnerToHub() error = %v", err)
			}
			if hub.Spec.When.Slack == nil {
				t.Fatal("expected slack config after up-conversion")
			}
			if got := hub.Spec.When.Slack.ExcludeFilters; len(got) != 0 {
				t.Errorf("excludeFilters = %#v, want none from annotation data that violates the field constraints", got)
			}
			if _, ok := hub.Annotations[preservedSlackExcludeFiltersAnnotation]; ok {
				t.Error("invalid preservation annotation should still be stripped from the hub object")
			}
		})
	}
}

func TestTaskSpawnerToHub_MaxSlackExcludeFiltersAnnotationRestored(t *testing.T) {
	// The boundary case must still restore: exactly maxItems valid, unique IDs.
	spoke := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat",
			Namespace: "default",
			Annotations: map[string]string{
				preservedSlackExcludeFiltersAnnotation: marshalChannelRule(t, v1alpha2.SlackFilterChannelsMaxItems),
			},
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{Slack: &v1alpha1.Slack{}},
		},
	}

	hub := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	got := hub.Spec.When.Slack.ExcludeFilters
	if len(got) != 1 {
		t.Fatalf("restored %d exclusion rules, want 1", len(got))
	}
	if len(got[0].Channels) != v1alpha2.SlackFilterChannelsMaxItems {
		t.Errorf("restored %d channels, want %d", len(got[0].Channels), v1alpha2.SlackFilterChannelsMaxItems)
	}
}

func TestTaskSpawnerFromHub_NoSlackExcludeFiltersOmitsAnnotation(t *testing.T) {
	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat",
			Namespace: "default",
			Annotations: map[string]string{
				preservedSlackExcludeFiltersAnnotation: `[{"channels":["C9876543210"]}]`,
			},
		},
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{Slack: &v1alpha2.Slack{Channels: []string{"C0123456789"}}},
		},
	}
	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if _, ok := spoke.Annotations[preservedSlackExcludeFiltersAnnotation]; ok {
		t.Error("annotation should be cleared when excludeFilters is empty")
	}
}

// TestTaskSpawnerConvert_CheckRunFilterFieldsDownConvert verifies that the
// v1alpha2-only check_run filter fields (Conclusion, CheckName) convert down to
// v1alpha1 without error. v1alpha1 has no equivalent fields, so they are dropped
// on down-conversion while the filter's shared fields are preserved.
func TestTaskSpawnerConvert_CheckRunFilterFieldsDownConvert(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubWebhook: &v1alpha2.GitHubWebhook{
					Events: []string{"check_run"},
					Filters: []v1alpha2.GitHubWebhookFilter{
						{
							Event:      "check_run",
							Action:     "completed",
							Conclusion: "failure",
							CheckName:  "lint*",
						},
					},
				},
			},
		},
	}

	dst := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}

	if dst.Spec.When.GitHubWebhook == nil || len(dst.Spec.When.GitHubWebhook.Filters) != 1 {
		t.Fatalf("expected one webhook filter after down-conversion, got %#v", dst.Spec.When.GitHubWebhook)
	}
	filter := dst.Spec.When.GitHubWebhook.Filters[0]
	if filter.Event != "check_run" || filter.Action != "completed" {
		t.Errorf("shared filter fields not preserved: %#v", filter)
	}
}

func TestTaskSpawnerFromHub_BackfillsLegacyFields(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubIssues: &v1alpha2.GitHubIssues{
					Repo:         "owner/repo",
					PollInterval: "7m",
					CommentPolicy: &v1alpha2.GitHubCommentPolicy{
						TriggerComment:  "/go",
						ExcludeComments: []string{"/stop"},
					},
				},
				Jira: &v1alpha2.Jira{
					Project:      "OPS",
					PollInterval: "7m",
				},
			},
		},
	}

	dst := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}

	if dst.Spec.PollInterval != "7m" {
		t.Errorf("pollInterval = %q, want 7m", dst.Spec.PollInterval)
	}
	gi := dst.Spec.When.GitHubIssues
	if gi.CommentPolicy != nil {
		t.Errorf("commentPolicy = %#v, want nil when policy fits legacy fields", gi.CommentPolicy)
	}
	if gi.TriggerComment != "/go" {
		t.Errorf("triggerComment = %q, want /go", gi.TriggerComment)
	}
	if len(gi.ExcludeComments) != 1 || gi.ExcludeComments[0] != "/stop" {
		t.Errorf("excludeComments = %#v, want [/stop]", gi.ExcludeComments)
	}
}

func TestTaskSpawnerFromHub_LeavesPolicyWithAuthorizationModern(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubPullRequests: &v1alpha2.GitHubPullRequests{
					Repo: "owner/repo",
					CommentPolicy: &v1alpha2.GitHubCommentPolicy{
						TriggerComment:    "/go",
						MinimumPermission: "write",
					},
				},
			},
		},
	}

	dst := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}

	pr := dst.Spec.When.GitHubPullRequests
	if pr.CommentPolicy == nil || pr.CommentPolicy.MinimumPermission != "write" {
		t.Fatalf("commentPolicy with authorization not preserved: %#v", pr.CommentPolicy)
	}
	if pr.TriggerComment != "" {
		t.Errorf("triggerComment = %q, want empty to keep v1alpha1 validation valid", pr.TriggerComment)
	}
}

func TestTaskSpawnerConvert_NameTemplateRoundTrips(t *testing.T) {
	const nameTemplate = "responder-{{.Repository}}-pr-{{.Number}}"
	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "responder", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			When:         v1alpha2.When{GitHubWebhook: &v1alpha2.GitHubWebhook{Events: []string{"pull_request"}}},
			TaskTemplate: v1alpha2.TaskTemplate{NameTemplate: nameTemplate},
		},
	}

	// hub -> spoke: v1alpha1 has no nameTemplate field, so it is preserved in an
	// internal annotation rather than dropped.
	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if got := spoke.Annotations[preservedNameTemplateAnnotation]; got != nameTemplate {
		t.Fatalf("preserved annotation = %q, want %q", got, nameTemplate)
	}

	// spoke -> hub: the field is restored and the internal annotation removed.
	back := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if got := back.Spec.TaskTemplate.NameTemplate; got != nameTemplate {
		t.Errorf("round-tripped NameTemplate = %q, want %q", got, nameTemplate)
	}
	if _, ok := back.Annotations[preservedNameTemplateAnnotation]; ok {
		t.Error("internal preservation annotation leaked onto hub object")
	}
}

func TestTaskSpawnerConvert_GatewayRefRoundTrips(t *testing.T) {
	tests := []struct {
		name   string
		when   v1alpha2.When
		getRef func(v1alpha2.When) *v1alpha2.GatewayReference
	}{
		{
			name: "github",
			when: v1alpha2.When{GitHubWebhook: &v1alpha2.GitHubWebhook{
				Events:     []string{"issues"},
				GatewayRef: &v1alpha2.GatewayReference{Name: "github-gateway"},
			}},
			getRef: func(when v1alpha2.When) *v1alpha2.GatewayReference { return when.GitHubWebhook.GatewayRef },
		},
		{
			name: "linear",
			when: v1alpha2.When{LinearWebhook: &v1alpha2.LinearWebhook{
				Types:      []string{"Issue"},
				GatewayRef: &v1alpha2.GatewayReference{Name: "linear-gateway"},
			}},
			getRef: func(when v1alpha2.When) *v1alpha2.GatewayReference { return when.LinearWebhook.GatewayRef },
		},
		{
			name: "generic",
			when: v1alpha2.When{GenericWebhook: &v1alpha2.GenericWebhook{
				Source:       "source",
				FieldMapping: map[string]string{"id": "$.id"},
				GatewayRef:   &v1alpha2.GatewayReference{Name: "generic-gateway"},
			}},
			getRef: func(when v1alpha2.When) *v1alpha2.GatewayReference { return when.GenericWebhook.GatewayRef },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := &v1alpha2.TaskSpawner{Spec: v1alpha2.TaskSpawnerSpec{When: tt.when}}
			spoke := &v1alpha1.TaskSpawner{}
			if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
				t.Fatalf("taskSpawnerFromHub() error = %v", err)
			}
			if spoke.Annotations[preservedWebhookGatewayRefsAnnotation] == "" {
				t.Fatal("gateway reference preservation annotation is empty")
			}

			back := &v1alpha2.TaskSpawner{}
			if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
				t.Fatalf("taskSpawnerToHub() error = %v", err)
			}
			if got := tt.getRef(back.Spec.When); got == nil || got.Name != tt.getRef(tt.when).Name {
				t.Fatalf("round-tripped gatewayRef = %+v, want %+v", got, tt.getRef(tt.when))
			}
			if _, ok := back.Annotations[preservedWebhookGatewayRefsAnnotation]; ok {
				t.Fatal("preservation annotation remained on hub")
			}
		})
	}
}

func TestTaskSpawnerConvert_GitHubCommentsReportingRoundTrips(t *testing.T) {
	tests := []struct {
		name               string
		configureHub       func(*v1alpha2.When, *v1alpha2.GitHubReporting)
		spokeReporting     func(*v1alpha1.When) *v1alpha1.GitHubReporting
		roundTripReporting func(*v1alpha2.When) *v1alpha2.GitHubReporting
	}{
		{
			name: "issues",
			configureHub: func(when *v1alpha2.When, reporting *v1alpha2.GitHubReporting) {
				when.GitHubIssues = &v1alpha2.GitHubIssues{Reporting: reporting}
			},
			spokeReporting: func(when *v1alpha1.When) *v1alpha1.GitHubReporting {
				return when.GitHubIssues.Reporting
			},
			roundTripReporting: func(when *v1alpha2.When) *v1alpha2.GitHubReporting {
				return when.GitHubIssues.Reporting
			},
		},
		{
			name: "pull requests",
			configureHub: func(when *v1alpha2.When, reporting *v1alpha2.GitHubReporting) {
				when.GitHubPullRequests = &v1alpha2.GitHubPullRequests{Reporting: reporting}
			},
			spokeReporting: func(when *v1alpha1.When) *v1alpha1.GitHubReporting {
				return when.GitHubPullRequests.Reporting
			},
			roundTripReporting: func(when *v1alpha2.When) *v1alpha2.GitHubReporting {
				return when.GitHubPullRequests.Reporting
			},
		},
		{
			name: "webhook",
			configureHub: func(when *v1alpha2.When, reporting *v1alpha2.GitHubReporting) {
				when.GitHubWebhook = &v1alpha2.GitHubWebhook{Reporting: reporting}
			},
			spokeReporting: func(when *v1alpha1.When) *v1alpha1.GitHubReporting {
				return when.GitHubWebhook.Reporting
			},
			roundTripReporting: func(when *v1alpha2.When) *v1alpha2.GitHubReporting {
				return when.GitHubWebhook.Reporting
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := &v1alpha2.TaskSpawner{ObjectMeta: metav1.ObjectMeta{Name: "reporter", Namespace: "default"}}
			tt.configureHub(&hub.Spec.When, &v1alpha2.GitHubReporting{
				Comments: &v1alpha2.GitHubCommentsReporting{Mode: v1alpha2.GitHubCommentModeSticky},
			})

			spoke := &v1alpha1.TaskSpawner{}
			if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
				t.Fatalf("taskSpawnerFromHub() error = %v", err)
			}
			if !tt.spokeReporting(&spoke.Spec.When).Enabled {
				t.Error("v1alpha1 fallback did not enable comment reporting")
			}
			if _, ok := spoke.Annotations[preservedGitHubCommentsReportingAnnotation]; !ok {
				t.Fatal("expected preserved GitHub comments reporting annotation on spoke")
			}

			back := &v1alpha2.TaskSpawner{}
			if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
				t.Fatalf("taskSpawnerToHub() error = %v", err)
			}
			reporting := tt.roundTripReporting(&back.Spec.When)
			if reporting.Comments == nil || reporting.Comments.Mode != v1alpha2.GitHubCommentModeSticky {
				t.Fatalf("round-tripped comments = %#v, want Sticky", reporting.Comments)
			}
			if reporting.Enabled {
				t.Error("deprecated enabled field was not restored to false")
			}
			if _, ok := back.Annotations[preservedGitHubCommentsReportingAnnotation]; ok {
				t.Error("internal preservation annotation leaked onto hub object")
			}
		})
	}
}

func TestTaskSpawnerConvert_V1Alpha1CanDisablePreservedCommentsReporting(t *testing.T) {
	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "reporter", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubWebhook: &v1alpha2.GitHubWebhook{
					Reporting: &v1alpha2.GitHubReporting{
						Comments: &v1alpha2.GitHubCommentsReporting{Mode: v1alpha2.GitHubCommentModeSticky},
					},
				},
			},
		},
	}

	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	spoke.Spec.When.GitHubWebhook.Reporting.Enabled = false

	back := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	reporting := back.Spec.When.GitHubWebhook.Reporting
	if reporting.Enabled || reporting.Comments != nil {
		t.Errorf("round-tripped reporting = %#v, want comments disabled", reporting)
	}
}

func TestTaskSpawnerConvert_CredentialsRoundTrip(t *testing.T) {
	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-account", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{Cron: &v1alpha2.Cron{Schedule: "0 9 * * 1"}},
			TaskTemplate: v1alpha2.TaskTemplate{
				Worker: &v1alpha2.WorkerSpec{Type: "claude-code"},
			},
			Credentials: []v1alpha2.SpawnerCredential{
				{Name: "account-b", Type: v1alpha2.CredentialTypeOAuth, SecretRef: v1alpha2.SecretReference{Name: "secret-b"}},
				{Name: "account-a", Type: v1alpha2.CredentialTypeAPIKey, SecretRef: v1alpha2.SecretReference{Name: "secret-a"}},
			},
		},
	}

	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if _, ok := spoke.Annotations[preservedTaskSpawnerCredentialsAnnotation]; !ok {
		t.Fatal("expected preserved TaskSpawner credentials annotation on spoke")
	}
	if got := spoke.Spec.TaskTemplate.Credentials.SecretRef; got == nil || got.Name != "secret-a" {
		t.Fatalf("v1alpha1 fallback SecretRef = %#v, want secret-a", got)
	}

	back := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if _, ok := back.Annotations[preservedTaskSpawnerCredentialsAnnotation]; ok {
		t.Error("internal preservation annotation leaked onto hub object")
	}
	if back.Spec.TaskTemplate.Credentials != nil {
		t.Errorf("fallback taskTemplate.credentials was not cleared: %#v", back.Spec.TaskTemplate.Credentials)
	}
	if len(back.Spec.Credentials) != 2 {
		t.Fatalf("round-tripped credentials len = %d, want 2", len(back.Spec.Credentials))
	}
	byName := map[string]v1alpha2.SpawnerCredential{}
	for _, credential := range back.Spec.Credentials {
		byName[credential.Name] = credential
	}
	if got := byName["account-a"].SecretRef.Name; got != "secret-a" {
		t.Errorf("account-a SecretRef.Name = %q, want secret-a", got)
	}
	if got := byName["account-b"].SecretRef.Name; got != "secret-b" {
		t.Errorf("account-b SecretRef.Name = %q, want secret-b", got)
	}
}

func TestTaskSpawnerConvert_EditedV1Alpha1CredentialsReplacePool(t *testing.T) {
	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-account", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{Cron: &v1alpha2.Cron{Schedule: "0 9 * * 1"}},
			TaskTemplate: v1alpha2.TaskTemplate{
				Worker: &v1alpha2.WorkerSpec{Type: "claude-code"},
			},
			Credentials: []v1alpha2.SpawnerCredential{
				{Name: "account-b", Type: v1alpha2.CredentialTypeOAuth, SecretRef: v1alpha2.SecretReference{Name: "secret-b"}},
				{Name: "account-a", Type: v1alpha2.CredentialTypeAPIKey, SecretRef: v1alpha2.SecretReference{Name: "secret-a"}},
			},
		},
	}

	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	spoke.Spec.TaskTemplate.Credentials = v1alpha1.Credentials{
		Type:      v1alpha1.CredentialTypeOAuth,
		SecretRef: &v1alpha1.SecretReference{Name: "edited-secret"},
	}

	back := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if len(back.Spec.Credentials) != 0 {
		t.Fatalf("round-tripped credentials = %#v, want no credential pool", back.Spec.Credentials)
	}
	if got := back.Spec.TaskTemplate.Credentials; got == nil || got.Type != v1alpha2.CredentialTypeOAuth || got.SecretRef == nil || got.SecretRef.Name != "edited-secret" {
		t.Errorf("round-tripped taskTemplate.credentials = %#v, want edited OAuth credential", got)
	}
	if _, ok := back.Annotations[preservedTaskSpawnerCredentialsAnnotation]; ok {
		t.Error("internal preservation annotation leaked onto hub object")
	}
}

func TestTaskSpawnerConvert_ContextGitHubAppAuthRoundTrips(t *testing.T) {
	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "enrich", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{GitHubWebhook: &v1alpha2.GitHubWebhook{Events: []string{"pull_request"}}},
			TaskTemplate: v1alpha2.TaskTemplate{
				ContextSources: []v1alpha2.ContextSource{
					{
						Name: "pr",
						HTTP: &v1alpha2.HTTPContextSource{
							URL: "https://api.github.com/repos/o/r/pulls/1",
							GitHubAppAuth: &v1alpha2.GitHubAppContextAuth{
								SecretRef:  v1alpha2.SecretReference{Name: "gh-app"},
								APIBaseURL: "https://github.example.com/api/v3",
							},
						},
					},
					{
						Name: "plain",
						HTTP: &v1alpha2.HTTPContextSource{URL: "https://example.com/data"},
					},
				},
			},
		},
	}

	// hub -> spoke: v1alpha1 has no githubAppAuth field, so it is preserved in
	// an internal annotation rather than dropped.
	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if _, ok := spoke.Annotations[preservedContextGitHubAppAuthAnnotation]; !ok {
		t.Fatal("expected preserved githubAppAuth annotation on spoke")
	}

	// spoke -> hub: the field is restored onto the matching context source and
	// the internal annotation removed.
	back := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if _, ok := back.Annotations[preservedContextGitHubAppAuthAnnotation]; ok {
		t.Error("internal preservation annotation leaked onto hub object")
	}

	sources := back.Spec.TaskTemplate.ContextSources
	if len(sources) != 2 {
		t.Fatalf("round-tripped contextSources len = %d, want 2", len(sources))
	}
	got := sources[0].HTTP.GitHubAppAuth
	if got == nil {
		t.Fatal("githubAppAuth not restored on 'pr' context source")
	}
	if got.SecretRef.Name != "gh-app" {
		t.Errorf("restored SecretRef.Name = %q, want %q", got.SecretRef.Name, "gh-app")
	}
	if got.APIBaseURL != "https://github.example.com/api/v3" {
		t.Errorf("restored APIBaseURL = %q, want %q", got.APIBaseURL, "https://github.example.com/api/v3")
	}
	if sources[1].HTTP.GitHubAppAuth != nil {
		t.Error("unexpected githubAppAuth restored on 'plain' context source")
	}
}

func TestTaskSpawnerToHub_MalformedContextGitHubAppAuthAnnotationIgnored(t *testing.T) {
	// The preservation annotation is user-editable; a malformed value must not
	// block conversion to the storage version. It is treated as absent and
	// stripped from the hub object.
	spoke := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "enrich",
			Namespace: "default",
			Annotations: map[string]string{
				preservedContextGitHubAppAuthAnnotation: "{not valid json",
			},
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{GitHubWebhook: &v1alpha1.GitHubWebhook{Events: []string{"pull_request"}}},
			TaskTemplate: v1alpha1.TaskTemplate{
				ContextSources: []v1alpha1.ContextSource{
					{Name: "pr", HTTP: &v1alpha1.HTTPContextSource{URL: "https://api.github.com/repos/o/r/pulls/1"}},
				},
			},
		},
	}

	hub := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if _, ok := hub.Annotations[preservedContextGitHubAppAuthAnnotation]; ok {
		t.Error("malformed internal preservation annotation leaked onto hub object")
	}
	sources := hub.Spec.TaskTemplate.ContextSources
	if len(sources) != 1 {
		t.Fatalf("contextSources len = %d, want 1", len(sources))
	}
	if sources[0].HTTP.GitHubAppAuth != nil {
		t.Error("githubAppAuth should not be restored from a malformed annotation")
	}
}

func TestTaskSpawnerFromHub_NoContextGitHubAppAuthOmitsAnnotation(t *testing.T) {
	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "enrich", Namespace: "default"},
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{Cron: &v1alpha2.Cron{Schedule: "0 9 * * 1"}},
			TaskTemplate: v1alpha2.TaskTemplate{
				ContextSources: []v1alpha2.ContextSource{
					{Name: "plain", HTTP: &v1alpha2.HTTPContextSource{URL: "https://example.com/data"}},
				},
			},
		},
	}
	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if _, ok := spoke.Annotations[preservedContextGitHubAppAuthAnnotation]; ok {
		t.Error("annotation should not be set when no context source uses GitHub App auth")
	}
}

func TestTaskSpawnerFromHub_NoNameTemplateOmitsAnnotation(t *testing.T) {
	hub := &v1alpha2.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "responder", Namespace: "default"},
		Spec:       v1alpha2.TaskSpawnerSpec{When: v1alpha2.When{Cron: &v1alpha2.Cron{Schedule: "0 9 * * 1"}}},
	}
	spoke := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if _, ok := spoke.Annotations[preservedNameTemplateAnnotation]; ok {
		t.Error("annotation should not be set when nameTemplate is empty")
	}
}

func TestTaskSpawnerConvert_GitHubWebhookExcludeFiltersRoundTrip(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubWebhook: &v1alpha2.GitHubWebhook{
					Events:         []string{"pull_request"},
					ExcludeAuthors: []string{"bot-user"},
					Filters: []v1alpha2.GitHubWebhookFilter{
						{Event: "pull_request", Action: "ready_for_review"},
					},
					ExcludeFilters: []v1alpha2.GitHubWebhookFilter{
						{PullRequestAuthor: "bot-user"},
						{Event: "pull_request", Action: "closed"},
					},
				},
			},
		},
	}

	down := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), src, down); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if down.Spec.When.GitHubWebhook == nil {
		t.Fatal("expected githubWebhook config after down-conversion")
	}
	// Shared fields survive the down-conversion directly.
	if got := down.Spec.When.GitHubWebhook.ExcludeAuthors; len(got) != 1 || got[0] != "bot-user" {
		t.Errorf("shared excludeAuthors not preserved: %#v", got)
	}
	if len(down.Spec.When.GitHubWebhook.Filters) != 1 {
		t.Errorf("accepting filters not preserved: %#v", down.Spec.When.GitHubWebhook.Filters)
	}
	// v1alpha1 cannot represent excludeFilters — the rules survive only via the
	// preservation annotation.
	if _, ok := down.Annotations[preservedGitHubWebhookExcludeFiltersAnnotation]; !ok {
		t.Fatal("expected the excludeFilters preservation annotation")
	}

	up := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), down, up); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if up.Spec.When.GitHubWebhook == nil {
		t.Fatal("expected githubWebhook config after up-conversion")
	}
	got := up.Spec.When.GitHubWebhook.ExcludeFilters
	if len(got) != 2 {
		t.Fatalf("restored %d exclude filters, want 2: %#v", len(got), got)
	}
	if got[0].PullRequestAuthor != "bot-user" || got[0].Event != "" {
		t.Errorf("unscoped rule not restored intact: %#v", got[0])
	}
	if got[1].Event != "pull_request" || got[1].Action != "closed" {
		t.Errorf("scoped rule not restored intact: %#v", got[1])
	}
	if _, ok := up.Annotations[preservedGitHubWebhookExcludeFiltersAnnotation]; ok {
		t.Error("preservation annotation not cleaned up after restore")
	}
}

func TestTaskSpawnerFromHub_NoExcludeFiltersOmitsAnnotation(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubWebhook: &v1alpha2.GitHubWebhook{
					Events: []string{"pull_request"},
				},
			},
		},
	}

	down := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), src, down); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	if _, ok := down.Annotations[preservedGitHubWebhookExcludeFiltersAnnotation]; ok {
		t.Error("expected no preservation annotation when excludeFilters is unset")
	}
}

// TestTaskSpawnerToHub_OutOfSchemaExcludeFiltersAnnotationIgnored covers a
// v1alpha1 object carrying a syntactically valid preservation annotation whose
// contents violate the constraints the CRD enforces on excludeFilters. The API
// server does not re-validate conversion webhook output, so the restore path
// must reject it rather than emit an invalid v1alpha2 object.
func TestTaskSpawnerToHub_OutOfSchemaExcludeFiltersAnnotationIgnored(t *testing.T) {
	tooManyRules := make([]v1alpha2.GitHubWebhookFilter, v1alpha2.GitHubWebhookExcludeFiltersMaxItems+1)
	for i := range tooManyRules {
		tooManyRules[i] = v1alpha2.GitHubWebhookFilter{PullRequestAuthor: fmt.Sprintf("bot-%d", i)}
	}
	tooMany, err := json.Marshal(tooManyRules)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	overLongAuthor, err := json.Marshal([]v1alpha2.GitHubWebhookFilter{
		{PullRequestAuthor: strings.Repeat("a", v1alpha2.GitHubWebhookFilterPullRequestAuthorMaxLength+1)},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	tests := []struct {
		name       string
		annotation string
	}{
		{name: "more rules than MaxItems", annotation: string(tooMany)},
		{name: "pull request author over MaxLength", annotation: string(overLongAuthor)},
		{name: "rule with no criteria would exclude everything", annotation: `[{}]`},
		{name: "rule using unsupported filePatterns", annotation: `[{"filePatterns":{"include":["**/*.go"]}}]`},
		{name: "not a JSON array", annotation: `{"pullRequestAuthor":"bot"}`},
		// Rejected criteria: a negative criterion inverts inside an exclusion
		// rule, and bodyContains is deprecated. CEL refuses all four on a
		// v1alpha2 write, so the restore path must refuse them too.
		{name: "rule using excludeAuthors inverts", annotation: `[{"excludeAuthors":["dependabot[bot]"]}]`},
		{name: "rule using excludeLabels inverts", annotation: `[{"excludeLabels":["skip"]}]`},
		{name: "rule using excludeBodyPatterns inverts", annotation: `[{"excludeBodyPatterns":["^/deploy"]}]`},
		{name: "rule using deprecated bodyContains", annotation: `[{"bodyContains":"deploy"}]`},
		// Event-scoped criteria without an event scope: skipped, and so
		// satisfied, outside their arm of the matcher, which would make the
		// rule reject every delivery.
		{name: "unscoped draft matches everything", annotation: `[{"draft":true}]`},
		{name: "unscoped labels matches everything", annotation: `[{"labels":["wip"]}]`},
		{name: "unscoped state matches everything", annotation: `[{"state":"closed"}]`},
		{name: "unscoped conclusion matches everything", annotation: `[{"conclusion":"failure"}]`},
		{name: "rule whose only criterion is an event scope", annotation: `[{"event":"pull_request"}]`},
		// An empty but non-nil list is not a criterion the matcher acts on, so
		// a rule carrying only one matches every event of that type.
		{name: "scoped rule whose only criterion is an empty list", annotation: `[{"event":"pull_request","labels":[]}]`},
		{name: "unscoped rule whose only criterion is an empty list", annotation: `[{"labels":[]}]`},
		// An out-of-enum value is not merely invalid: the matcher switches on it,
		// no arm matches, and the criterion is left satisfied, so the rule
		// rejects every event of that type.
		{name: "commentOn outside its enum", annotation: `[{"event":"issue_comment","commentOn":"Bogus"}]`},
		{name: "conclusion outside its enum", annotation: `[{"event":"check_run","conclusion":"bogus"}]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &v1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						preservedGitHubWebhookExcludeFiltersAnnotation: tt.annotation,
					},
				},
				Spec: v1alpha1.TaskSpawnerSpec{
					When: v1alpha1.When{
						GitHubWebhook: &v1alpha1.GitHubWebhook{
							Events: []string{"pull_request"},
						},
					},
				},
			}

			up := &v1alpha2.TaskSpawner{}
			if err := taskSpawnerToHub(context.Background(), src, up); err != nil {
				t.Fatalf("taskSpawnerToHub() error = %v", err)
			}
			if up.Spec.When.GitHubWebhook == nil {
				t.Fatal("expected githubWebhook config after up-conversion")
			}
			if got := up.Spec.When.GitHubWebhook.ExcludeFilters; len(got) != 0 {
				t.Errorf("excludeFilters = %#v, want empty from out-of-schema annotation", got)
			}
		})
	}
}

// TestTaskSpawnerConvert_FilterPullRequestAuthorDropsOnRoundTrip documents that
// pullRequestAuthor on an accepting filter is dropped by a v1alpha1 round-trip,
// the same way the other v1alpha2-only filter criteria (conclusion, checkName)
// down-convert. Only the source-level excludeFilters list is preserved.
func TestTaskSpawnerConvert_FilterPullRequestAuthorDropsOnRoundTrip(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubWebhook: &v1alpha2.GitHubWebhook{
					Events: []string{"pull_request"},
					Filters: []v1alpha2.GitHubWebhookFilter{
						{Event: "pull_request", PullRequestAuthor: "bot-user"},
					},
				},
			},
		},
	}

	down := &v1alpha1.TaskSpawner{}
	if err := taskSpawnerFromHub(context.Background(), src, down); err != nil {
		t.Fatalf("taskSpawnerFromHub() error = %v", err)
	}
	up := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), down, up); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if len(up.Spec.When.GitHubWebhook.Filters) != 1 {
		t.Fatalf("expected 1 filter after round-trip, got %d", len(up.Spec.When.GitHubWebhook.Filters))
	}
	filter := up.Spec.When.GitHubWebhook.Filters[0]
	if filter.Event != "pull_request" {
		t.Errorf("filter event = %q, want pull_request", filter.Event)
	}
	if filter.PullRequestAuthor != "" {
		t.Errorf("filter pullRequestAuthor = %q, want dropped", filter.PullRequestAuthor)
	}
}

// TestValidGitHubWebhookExcludeFilters_CoversEveryClassifiedCriterion drives the
// restore-path checker from the API package's criteria buckets, so a criterion
// added to a bucket without teaching the checker about it fails here rather than
// silently restoring through the user-writable preservation annotation.
func TestValidGitHubWebhookExcludeFilters_CoversEveryClassifiedCriterion(t *testing.T) {
	// filterWith builds a rule whose only criterion is the named JSON tag.
	filterWith := func(t *testing.T, criterion string) v1alpha2.GitHubWebhookFilter {
		t.Helper()
		filter := v1alpha2.GitHubWebhookFilter{}
		value := reflect.ValueOf(&filter).Elem()
		typ := value.Type()
		for i := 0; i < typ.NumField(); i++ {
			if strings.Split(typ.Field(i).Tag.Get("json"), ",")[0] != criterion {
				continue
			}
			field := value.Field(i)
			switch field.Kind() {
			case reflect.String:
				// A criterion that constrains its values needs a real one; "x"
				// would be refused by the enum check rather than by the bucket
				// rule under test.
				sample := "x"
				if allowed, ok := v1alpha2.GitHubWebhookFilterCriterionEnum(criterion); ok {
					sample = ""
					for _, v := range allowed {
						if v != "" {
							sample = v
							break
						}
					}
					if sample == "" {
						t.Fatalf("criterion %q has no non-empty enum value to use", criterion)
					}
				}
				field.SetString(sample)
			case reflect.Slice:
				field.Set(reflect.MakeSlice(field.Type(), 1, 1))
				if field.Index(0).Kind() == reflect.String {
					field.Index(0).SetString("x")
				}
			case reflect.Ptr:
				field.Set(reflect.New(field.Type().Elem()))
				if field.Elem().Kind() == reflect.Bool {
					field.Elem().SetBool(true)
				}
			default:
				t.Fatalf("criterion %q has unhandled kind %s", criterion, field.Kind())
			}
			return filter
		}
		t.Fatalf("criterion %q is not a field of GitHubWebhookFilter", criterion)
		return filter
	}

	for _, criterion := range v1alpha2.GitHubWebhookExcludeFilterRejectedCriteria() {
		t.Run("rejected/"+criterion, func(t *testing.T) {
			if validGitHubWebhookExcludeFilters([]v1alpha2.GitHubWebhookFilter{filterWith(t, criterion)}) {
				t.Errorf("a rule using the rejected criterion %q was accepted on restore", criterion)
			}
		})
	}

	for _, criterion := range v1alpha2.GitHubWebhookExcludeFilterEventScopedCriteria() {
		t.Run("eventScoped/"+criterion, func(t *testing.T) {
			unscoped := filterWith(t, criterion)
			if validGitHubWebhookExcludeFilters([]v1alpha2.GitHubWebhookFilter{unscoped}) {
				t.Errorf("an unscoped rule using the event-scoped criterion %q was accepted on restore", criterion)
			}
			scoped := unscoped
			scoped.Event = "pull_request"
			if !validGitHubWebhookExcludeFilters([]v1alpha2.GitHubWebhookFilter{scoped}) {
				t.Errorf("a rule scoping %q to an event type was rejected on restore", criterion)
			}
		})
	}

	for _, criterion := range v1alpha2.GitHubWebhookExcludeFilterSafeUnscopedCriteria() {
		t.Run("safeUnscoped/"+criterion, func(t *testing.T) {
			if !validGitHubWebhookExcludeFilters([]v1alpha2.GitHubWebhookFilter{filterWith(t, criterion)}) {
				t.Errorf("an unscoped rule using %q was rejected on restore, but it is safe unscoped", criterion)
			}
		})
	}
}

// TestValidGitHubWebhookExcludeFilters_AcceptsEveryEnumValue pins that the enum
// check refuses only genuinely out-of-enum values: every value a criterion's
// marker admits must still restore, so tightening the check cannot quietly
// start dropping valid rules.
func TestValidGitHubWebhookExcludeFilters_AcceptsEveryEnumValue(t *testing.T) {
	for _, criterion := range v1alpha2.GitHubWebhookFilterEnumCriteria() {
		allowed, ok := v1alpha2.GitHubWebhookFilterCriterionEnum(criterion)
		if !ok {
			t.Fatalf("criterion %q reported as an enum but has no values", criterion)
		}
		for _, value := range allowed {
			if value == "" {
				continue // an unset criterion is covered by the no-criteria case
			}
			filter := v1alpha2.GitHubWebhookFilter{Event: "issue_comment"}
			switch criterion {
			case "commentOn":
				filter.CommentOn = value
			case "conclusion":
				filter.Conclusion = value
			default:
				t.Fatalf("criterion %q has no fixture here; add one alongside the enum", criterion)
			}
			if !validGitHubWebhookExcludeFilters([]v1alpha2.GitHubWebhookFilter{filter}) {
				t.Errorf("a rule with %s=%q was rejected on restore, but the marker admits it", criterion, value)
			}
		}
	}
}
