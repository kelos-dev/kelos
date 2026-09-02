package webhook

import (
	"context"
	"net/http"

	"github.com/go-logr/logr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
)

type gitlabProvider struct{}

// authenticate reports the source name as event type: the X-Gitlab-Event
// header carries a display name ("Merge Request Hook"), and the payload
// object_kind that parse extracts is the canonical event type.
func (gitlabProvider) authenticate(r *http.Request, body, secret []byte, _ func() []*kelos.TaskSpawner) (string, string, error) {
	return string(GitLabSource), gitlabRequestDeliveryID(r, body), ValidateGitLabToken(r.Header.Get(GitLabTokenHeader), secret)
}

func (gitlabProvider) gatewayRef(spawner *kelos.TaskSpawner) (*kelos.GatewayReference, bool) {
	if spawner.Spec.When.GitLabWebhook == nil {
		return nil, false
	}
	return spawner.Spec.When.GitLabWebhook.GatewayRef, true
}

func (gitlabProvider) parse(log logr.Logger, eventType string, body []byte) (*ParsedWebhook, string, error) {
	eventData, err := ParseGitLabWebhook(body)
	if err != nil {
		return nil, eventType, err
	}
	if eventData.Event != "" {
		eventType = eventData.Event
	} else {
		log.Info("GitLab webhook payload has no 'object_kind' field, will not match any Events filter")
	}
	return &ParsedWebhook{GitLab: eventData, ID: eventData.ID, Title: eventData.Title}, eventType, nil
}

func (gitlabProvider) prepare(context.Context, *WebhookHandler, logr.Logger, *ParsedWebhook, []*kelos.TaskSpawner) {
}

func (gitlabProvider) match(_ context.Context, _ *WebhookHandler, spawner *kelos.TaskSpawner, _ string, parsed *ParsedWebhook) (bool, error) {
	if spawner.Spec.When.GitLabWebhook == nil {
		return false, nil
	}
	return MatchesGitLabEvent(spawner.Spec.When.GitLabWebhook, parsed.GitLab)
}

func (gitlabProvider) templateVars(_ *kelos.TaskSpawner, _ string, parsed *ParsedWebhook) map[string]interface{} {
	return ExtractGitLabWorkItem(parsed.GitLab)
}

// annotate stamps note reporting for events that map to an issue or merge
// request.
func (gitlabProvider) annotate(task *kelos.Task, spawner *kelos.TaskSpawner, _ string, parsed *ParsedWebhook, gatewayName string) {
	tracker, _ := spawner.Spec.When.Tracker()
	if parsed.GitLab == nil || parsed.GitLab.Number == 0 || tracker.Comments == nil {
		return
	}
	kind := "issue"
	if parsed.GitLab.Kind == "MR" {
		kind = reporting.SourceKindMergeRequest
	}
	annotations := reportingAnnotations(tracker, kind, parsed.GitLab.Number, gatewayName)
	annotations[reporting.AnnotationSourceProvider] = reporting.SourceProviderGitLab
	annotations[reporting.AnnotationSourceRepo] = parsed.GitLab.Project
	annotations[reporting.AnnotationSourceBaseURL] = gitlabInstanceURL(parsed.GitLab.ProjectURL, parsed.GitLab.Project)
	addAnnotations(task, annotations)
}
