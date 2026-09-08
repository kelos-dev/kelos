package webhook

import (
	"context"
	"net/http"

	"github.com/go-logr/logr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
)

type githubProvider struct{}

func (githubProvider) authenticate(r *http.Request, body, secret []byte, _ func() []*kelos.TaskSpawner) (string, string, error) {
	eventType := r.Header.Get(GitHubEventHeader)
	deliveryID := r.Header.Get(GitHubDeliveryHeader)
	if deliveryID == "" {
		deliveryID = githubDeliveryID(body)
	}
	return eventType, deliveryID, ValidateGitHubSignature(body, r.Header.Get(GitHubSignatureHeader), secret)
}

func (githubProvider) gatewayRef(spawner *kelos.TaskSpawner) (*kelos.GatewayReference, bool) {
	if spawner.Spec.When.GitHubWebhook == nil {
		return nil, false
	}
	return spawner.Spec.When.GitHubWebhook.GatewayRef, true
}

func (githubProvider) parse(_ logr.Logger, eventType string, body []byte) (*ParsedWebhook, string, error) {
	eventData, err := ParseGitHubWebhook(eventType, body)
	if err != nil {
		return nil, eventType, err
	}
	return &ParsedWebhook{GitHub: eventData, ID: eventData.ID, Title: eventData.Title}, eventType, nil
}

// prepare fills the Branch of issue_comment events on pull requests: the
// payload does not include the PR head ref, so it is fetched once per delivery.
func (githubProvider) prepare(ctx context.Context, h *WebhookHandler, log logr.Logger, parsed *ParsedWebhook, _ []*kelos.TaskSpawner) {
	if needsBranchEnrichment(parsed.GitHub) {
		h.enrichGitHubIssueCommentBranch(ctx, log, parsed.GitHub)
	}
}

func (githubProvider) match(ctx context.Context, h *WebhookHandler, spawner *kelos.TaskSpawner, eventType string, parsed *ParsedWebhook) (bool, error) {
	return h.matchesGitHubWebhook(ctx, spawner.Spec.When.GitHubWebhook, eventType, parsed.GitHub, func(ctx context.Context, eventData *GitHubEventData) ([]string, error) {
		return h.enrichPRChangedFiles(ctx, spawner, eventData)
	})
}

func (githubProvider) templateVars(spawner *kelos.TaskSpawner, eventType string, parsed *ParsedWebhook) map[string]interface{} {
	return ExtractGitHubWorkItem(parsed.GitHub, changedFilesForSpawner(spawner.Spec.When.GitHubWebhook, eventType, parsed.GitHub))
}

func (githubProvider) annotate(task *kelos.Task, spawner *kelos.TaskSpawner, eventType string, parsed *ParsedWebhook, gatewayName string) {
	tracker, _ := spawner.Spec.When.Tracker()
	if parsed.GitHub == nil || parsed.GitHub.Number == 0 || (tracker.Comments == nil && tracker.Checks == nil) {
		return
	}
	annotations := reportingAnnotations(tracker, webhookSourceKind(eventType, parsed.GitHub), parsed.GitHub.Number, gatewayName)
	annotations[reporting.AnnotationSourceOwner] = parsed.GitHub.RepositoryOwner
	annotations[reporting.AnnotationSourceRepo] = parsed.GitHub.RepositoryName
	if tracker.Checks != nil && parsed.GitHub.HeadSHA != "" {
		annotations[reporting.AnnotationCheckReporting] = "enabled"
		annotations[reporting.AnnotationSourceSHA] = parsed.GitHub.HeadSHA
		if tracker.Checks.Name != "" {
			annotations[reporting.AnnotationCheckName] = tracker.Checks.Name
		}
	}
	addAnnotations(task, annotations)
}
