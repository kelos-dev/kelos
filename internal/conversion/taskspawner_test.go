package conversion

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestTaskSpawnerConvert_GitHubWebhookExcludePullRequestAuthorsRoundTrip(t *testing.T) {
	src := &v1alpha2.TaskSpawner{
		Spec: v1alpha2.TaskSpawnerSpec{
			When: v1alpha2.When{
				GitHubWebhook: &v1alpha2.GitHubWebhook{
					Events:                    []string{"pull_request"},
					ExcludeAuthors:            []string{"bot-user"},
					ExcludePullRequestAuthors: []string{"bot-user", "dependabot[bot]"},
					Filters: []v1alpha2.GitHubWebhookFilter{
						{
							Event:  "pull_request",
							Action: "ready_for_review",
						},
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
	// v1alpha1 cannot represent excludePullRequestAuthors — the top-level list survives
	// only via the preservation annotation.
	raw, ok := down.Annotations[preservedGitHubWebhookExcludePullRequestAuthorsAnnotation]
	if !ok || raw != `["bot-user","dependabot[bot]"]` {
		t.Errorf("preservation annotation = %q, want the excludePullRequestAuthors JSON", raw)
	}

	up := &v1alpha2.TaskSpawner{}
	if err := taskSpawnerToHub(context.Background(), down, up); err != nil {
		t.Fatalf("taskSpawnerToHub() error = %v", err)
	}
	if up.Spec.When.GitHubWebhook == nil {
		t.Fatal("expected githubWebhook config after up-conversion")
	}
	got := up.Spec.When.GitHubWebhook.ExcludePullRequestAuthors
	if len(got) != 2 || got[0] != "bot-user" || got[1] != "dependabot[bot]" {
		t.Errorf("excludePullRequestAuthors not restored: %#v", got)
	}
	if _, ok := up.Annotations[preservedGitHubWebhookExcludePullRequestAuthorsAnnotation]; ok {
		t.Error("preservation annotation not cleaned up after restore")
	}
	// The filter's own fields survive the round-trip alongside the restored list.
	if len(up.Spec.When.GitHubWebhook.Filters) != 1 {
		t.Fatalf("expected 1 filter after round-trip, got %d", len(up.Spec.When.GitHubWebhook.Filters))
	}
	if filter := up.Spec.When.GitHubWebhook.Filters[0]; filter.Action != "ready_for_review" {
		t.Errorf("filter action = %q, want ready_for_review", filter.Action)
	}
}

func TestTaskSpawnerFromHub_NoExcludePullRequestAuthorsOmitsAnnotation(t *testing.T) {
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
	if _, ok := down.Annotations[preservedGitHubWebhookExcludePullRequestAuthorsAnnotation]; ok {
		t.Error("expected no preservation annotation when excludePullRequestAuthors is unset")
	}
}

func TestTaskSpawnerToHub_MalformedExcludePullRequestAuthorsAnnotationIgnored(t *testing.T) {
	src := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				preservedGitHubWebhookExcludePullRequestAuthorsAnnotation: "{not-json",
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
	if got := up.Spec.When.GitHubWebhook.ExcludePullRequestAuthors; len(got) != 0 {
		t.Errorf("excludePullRequestAuthors = %#v, want empty from malformed annotation", got)
	}
}

// TestTaskSpawnerToHub_OutOfSchemaExcludePullRequestAuthorsAnnotationIgnored
// covers a v1alpha1 object carrying a syntactically valid preservation
// annotation whose contents violate the field's own CRD constraints. The API
// server does not re-validate conversion webhook output, so the restore path
// must reject it rather than emit an invalid v1alpha2 object.
func TestTaskSpawnerToHub_OutOfSchemaExcludePullRequestAuthorsAnnotationIgnored(t *testing.T) {
	tooManyItems, _ := json.Marshal(make([]string, excludePullRequestAuthorsMaxItems+1))
	overLongItem, _ := json.Marshal([]string{strings.Repeat("a", excludePullRequestAuthorsMaxItemLength+1)})

	tests := []struct {
		name       string
		annotation string
	}{
		{name: "more items than MaxItems", annotation: string(tooManyItems)},
		{name: "item longer than MaxLength", annotation: string(overLongItem)},
		{name: "empty item violates MinLength", annotation: `["bot-user",""]`},
		{name: "not a JSON array", annotation: `{"bot-user":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &v1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						preservedGitHubWebhookExcludePullRequestAuthorsAnnotation: tt.annotation,
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
			if got := up.Spec.When.GitHubWebhook.ExcludePullRequestAuthors; len(got) != 0 {
				t.Errorf("excludePullRequestAuthors = %#v, want empty from out-of-schema annotation", got)
			}
		})
	}
}

// TestTaskSpawnerToHub_ExcludePullRequestAuthorsAtSchemaLimits verifies the
// validation rejects only what the CRD would: a list exactly at MaxItems whose
// longest entry is exactly MaxLength still restores.
func TestTaskSpawnerToHub_ExcludePullRequestAuthorsAtSchemaLimits(t *testing.T) {
	authors := make([]string, excludePullRequestAuthorsMaxItems)
	for i := range authors {
		authors[i] = fmt.Sprintf("bot-%d", i)
	}
	authors[0] = strings.Repeat("a", excludePullRequestAuthorsMaxItemLength)
	raw, err := json.Marshal(authors)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	src := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				preservedGitHubWebhookExcludePullRequestAuthorsAnnotation: string(raw),
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
	got := up.Spec.When.GitHubWebhook.ExcludePullRequestAuthors
	if len(got) != excludePullRequestAuthorsMaxItems {
		t.Fatalf("restored %d authors, want %d", len(got), excludePullRequestAuthorsMaxItems)
	}
	if got[0] != authors[0] {
		t.Errorf("longest entry not restored intact: %q", got[0])
	}
}
