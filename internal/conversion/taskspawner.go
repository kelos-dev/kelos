package conversion

import (
	"context"
	"encoding/json"
	"regexp"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

// preservedNameTemplateAnnotation carries taskTemplate.nameTemplate (a
// v1alpha2-only field) across a v1alpha1 round-trip so a client that reads and
// writes the object through v1alpha1 does not silently drop it. v1alpha1 does
// not gain the capability — the value only survives in this annotation.
const preservedNameTemplateAnnotation = "kelos.dev/v1alpha2-name-template"

// preservedContextGitHubAppAuthAnnotation carries the githubAppAuth blocks of
// taskTemplate.contextSources (a v1alpha2-only field) across a v1alpha1
// round-trip, keyed by context source name. Without it a client that reads and
// writes the object through v1alpha1 would silently drop GitHub App
// authentication from a stored v1alpha2 TaskSpawner.
const preservedContextGitHubAppAuthAnnotation = "kelos.dev/v1alpha2-context-github-app-auth"

// preservedTaskSpawnerCredentialsAnnotation carries spec.credentials across a
// v1alpha1 round-trip. v1alpha1 receives one credential as a valid fallback,
// while the complete set remains in this annotation for restoration.
const preservedTaskSpawnerCredentialsAnnotation = "kelos.dev/v1alpha2-taskspawner-credentials"

// preservedGitHubCommentsReportingAnnotation carries v1alpha2 comment
// reporting configuration across a v1alpha1 round-trip. v1alpha1 receives
// enabled: true as a functional fallback while the complete configuration is
// restored when the object returns to v1alpha2.
const preservedGitHubCommentsReportingAnnotation = "kelos.dev/v1alpha2-github-comments-reporting"

// preservedWebhookGatewayRefsAnnotation carries v1alpha2 gateway references
// across a v1alpha1 round-trip without exposing the capability in v1alpha1.
const preservedWebhookGatewayRefsAnnotation = "kelos.dev/v1alpha2-webhook-gateway-refs"

type preservedWebhookGatewayRefs struct {
	GitHub  *v1alpha2.GatewayReference `json:"github,omitempty"`
	Linear  *v1alpha2.GatewayReference `json:"linear,omitempty"`
	Generic *v1alpha2.GatewayReference `json:"generic,omitempty"`
}

// preservedSlackExcludeChannelsAnnotation carries spec.when.slack.excludeChannels
// (a v1alpha2-only field) across a v1alpha1 round-trip so a client that reads
// and writes the object through v1alpha1 does not silently drop it. v1alpha1
// does not gain the capability — the value only survives in this annotation.
const preservedSlackExcludeChannelsAnnotation = "kelos.dev/v1alpha2-slack-exclude-channels"

// slackExcludeChannelsMaxItems and slackExcludeChannelIDPattern mirror the
// validation markers on v1alpha2 Slack.ExcludeChannels. The API server does not
// re-validate the output of a conversion webhook, so annotation data — which any
// v1alpha1 client can write by hand — would otherwise reach the hub object
// having bypassed the field's own constraints.
const slackExcludeChannelsMaxItems = 64

var slackExcludeChannelIDPattern = regexp.MustCompile(`^[CGD][A-Z0-9]{8,}$`)

type preservedGitHubCommentsReporting struct {
	GitHubIssues       *preservedGitHubCommentsSource `json:"githubIssues,omitempty"`
	GitHubPullRequests *preservedGitHubCommentsSource `json:"githubPullRequests,omitempty"`
	GitHubWebhook      *preservedGitHubCommentsSource `json:"githubWebhook,omitempty"`
}

type preservedGitHubCommentsSource struct {
	Enabled  bool                             `json:"enabled,omitempty"`
	Comments v1alpha2.GitHubCommentsReporting `json:"comments"`
}

func taskSpawnerToHub(_ context.Context, src *v1alpha1.TaskSpawner, dst *v1alpha2.TaskSpawner) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if err := convertViaJSON(&src.Status, &dst.Status); err != nil {
		return err
	}
	foldTaskSpawnerForward(&src.Spec, &dst.Spec)
	restorePreservedNameTemplate(src.Annotations, &dst.Spec.TaskTemplate)
	deleteAnnotation(dst.Annotations, preservedNameTemplateAnnotation)
	if err := restorePreservedContextGitHubAppAuth(src.Annotations, &dst.Spec.TaskTemplate); err != nil {
		return err
	}
	deleteAnnotation(dst.Annotations, preservedContextGitHubAppAuthAnnotation)
	if err := restorePreservedTaskSpawnerCredentials(src.Annotations, &dst.Spec); err != nil {
		return err
	}
	deleteAnnotation(dst.Annotations, preservedTaskSpawnerCredentialsAnnotation)
	restorePreservedGitHubCommentsReporting(src.Annotations, &dst.Spec.When)
	deleteAnnotation(dst.Annotations, preservedGitHubCommentsReportingAnnotation)
	restorePreservedWebhookGatewayRefs(src.Annotations, &dst.Spec.When)
	deleteAnnotation(dst.Annotations, preservedWebhookGatewayRefsAnnotation)
	restorePreservedSlackExcludeChannels(src.Annotations, dst.Spec.When.Slack)
	deleteAnnotation(dst.Annotations, preservedSlackExcludeChannelsAnnotation)
	return nil
}

func taskSpawnerFromHub(_ context.Context, src *v1alpha2.TaskSpawner, dst *v1alpha1.TaskSpawner) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if err := backfillTaskTemplateLegacyWorkerFields(&src.Spec.TaskTemplate, &dst.Spec.TaskTemplate); err != nil {
		return err
	}
	backfillTaskSpawnerLegacy(&dst.Spec)
	setPreservedNameTemplateAnnotation(dst, src.Spec.TaskTemplate.NameTemplate)
	if err := setPreservedContextGitHubAppAuth(dst, src.Spec.TaskTemplate); err != nil {
		return err
	}
	if err := setPreservedTaskSpawnerCredentials(dst, src.Spec.Credentials); err != nil {
		return err
	}
	if err := setPreservedGitHubCommentsReporting(dst, src.Spec.When); err != nil {
		return err
	}
	if err := setPreservedWebhookGatewayRefs(dst, src.Spec.When); err != nil {
		return err
	}
	if err := setPreservedSlackExcludeChannels(dst, src.Spec.When.Slack); err != nil {
		return err
	}
	return convertViaJSON(&src.Status, &dst.Status)
}

func setPreservedWebhookGatewayRefs(dst *v1alpha1.TaskSpawner, when v1alpha2.When) error {
	preserved := preservedWebhookGatewayRefs{}
	if when.GitHubWebhook != nil {
		preserved.GitHub = when.GitHubWebhook.GatewayRef
	}
	if when.LinearWebhook != nil {
		preserved.Linear = when.LinearWebhook.GatewayRef
	}
	if when.GenericWebhook != nil {
		preserved.Generic = when.GenericWebhook.GatewayRef
	}
	if preserved.GitHub == nil && preserved.Linear == nil && preserved.Generic == nil {
		deleteAnnotation(dst.Annotations, preservedWebhookGatewayRefsAnnotation)
		return nil
	}
	data, err := json.Marshal(preserved)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedWebhookGatewayRefsAnnotation] = string(data)
	return nil
}

func restorePreservedWebhookGatewayRefs(annotations map[string]string, when *v1alpha2.When) {
	raw, ok := annotations[preservedWebhookGatewayRefsAnnotation]
	if !ok || raw == "" {
		return
	}
	var preserved preservedWebhookGatewayRefs
	if err := json.Unmarshal([]byte(raw), &preserved); err != nil {
		return
	}
	if when.GitHubWebhook != nil && when.GitHubWebhook.GatewayRef == nil {
		when.GitHubWebhook.GatewayRef = preserved.GitHub
	}
	if when.LinearWebhook != nil && when.LinearWebhook.GatewayRef == nil {
		when.LinearWebhook.GatewayRef = preserved.Linear
	}
	if when.GenericWebhook != nil && when.GenericWebhook.GatewayRef == nil {
		when.GenericWebhook.GatewayRef = preserved.Generic
	}
}

func setPreservedNameTemplateAnnotation(dst *v1alpha1.TaskSpawner, nameTemplate string) {
	if nameTemplate == "" {
		deleteAnnotation(dst.Annotations, preservedNameTemplateAnnotation)
		return
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedNameTemplateAnnotation] = nameTemplate
}

func restorePreservedNameTemplate(annotations map[string]string, dst *v1alpha2.TaskTemplate) {
	if dst.NameTemplate != "" {
		return
	}
	if v, ok := annotations[preservedNameTemplateAnnotation]; ok {
		dst.NameTemplate = v
	}
}

// setPreservedSlackExcludeChannels records spec.when.slack.excludeChannels in
// an annotation on the v1alpha1 object so the field survives a v1alpha1
// round-trip. The annotation is cleared when there is nothing to preserve.
func setPreservedSlackExcludeChannels(dst *v1alpha1.TaskSpawner, slack *v1alpha2.Slack) error {
	if slack == nil || len(slack.ExcludeChannels) == 0 {
		deleteAnnotation(dst.Annotations, preservedSlackExcludeChannelsAnnotation)
		return nil
	}
	data, err := json.Marshal(slack.ExcludeChannels)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedSlackExcludeChannelsAnnotation] = string(data)
	return nil
}

// restorePreservedSlackExcludeChannels restores excludeChannels dropped by a
// v1alpha1 round-trip, unless the v1alpha2 object already carries the field.
func restorePreservedSlackExcludeChannels(annotations map[string]string, slack *v1alpha2.Slack) {
	if slack == nil || len(slack.ExcludeChannels) > 0 {
		return
	}
	raw, ok := annotations[preservedSlackExcludeChannelsAnnotation]
	if !ok || raw == "" {
		return
	}
	var excludeChannels []string
	if err := json.Unmarshal([]byte(raw), &excludeChannels); err != nil || len(excludeChannels) == 0 {
		// The annotation is best-effort preservation data and can be set by
		// users; malformed data must not block API version conversion.
		return
	}
	if !validSlackExcludeChannels(excludeChannels) {
		return
	}
	slack.ExcludeChannels = excludeChannels
}

// validSlackExcludeChannels reports whether restored annotation data satisfies
// the constraints declared on v1alpha2 Slack.ExcludeChannels: at most
// slackExcludeChannelsMaxItems entries, each a well-formed channel ID, no
// duplicates (the field is a set). Data that fails any of these is treated the
// same as malformed JSON — ignored entirely, rather than partially applied, so
// conversion can never produce a hub object that a v1alpha2 write would have
// rejected.
func validSlackExcludeChannels(excludeChannels []string) bool {
	if len(excludeChannels) > slackExcludeChannelsMaxItems {
		return false
	}
	seen := make(map[string]struct{}, len(excludeChannels))
	for _, id := range excludeChannels {
		if !slackExcludeChannelIDPattern.MatchString(id) {
			return false
		}
		if _, dup := seen[id]; dup {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

// setPreservedContextGitHubAppAuth records the githubAppAuth block of each
// context source (keyed by source name) into an annotation on the v1alpha1
// object so it survives a v1alpha1 round-trip. The annotation is cleared when
// no context source uses GitHub App auth.
func setPreservedContextGitHubAppAuth(dst *v1alpha1.TaskSpawner, template v1alpha2.TaskTemplate) error {
	preserved := map[string]v1alpha2.GitHubAppContextAuth{}
	for _, cs := range template.ContextSources {
		if cs.HTTP != nil && cs.HTTP.GitHubAppAuth != nil {
			preserved[cs.Name] = *cs.HTTP.GitHubAppAuth
		}
	}
	if len(preserved) == 0 {
		deleteAnnotation(dst.Annotations, preservedContextGitHubAppAuthAnnotation)
		return nil
	}
	data, err := json.Marshal(preserved)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedContextGitHubAppAuthAnnotation] = string(data)
	return nil
}

// restorePreservedContextGitHubAppAuth restores githubAppAuth blocks dropped by
// a v1alpha1 round-trip onto the matching context sources (by name), unless the
// source already carries the field.
func restorePreservedContextGitHubAppAuth(annotations map[string]string, dst *v1alpha2.TaskTemplate) error {
	raw, ok := annotations[preservedContextGitHubAppAuthAnnotation]
	if !ok || raw == "" {
		return nil
	}
	preserved := map[string]v1alpha2.GitHubAppContextAuth{}
	if err := json.Unmarshal([]byte(raw), &preserved); err != nil {
		// The annotation is best-effort preservation data and can be set by
		// users; malformed data must not block API version conversion.
		return nil
	}
	for i := range dst.ContextSources {
		cs := &dst.ContextSources[i]
		if cs.HTTP == nil || cs.HTTP.GitHubAppAuth != nil {
			continue
		}
		if auth, ok := preserved[cs.Name]; ok {
			restored := auth
			cs.HTTP.GitHubAppAuth = &restored
		}
	}
	return nil
}

func setPreservedTaskSpawnerCredentials(dst *v1alpha1.TaskSpawner, credentials []v1alpha2.SpawnerCredential) error {
	if len(credentials) == 0 {
		deleteAnnotation(dst.Annotations, preservedTaskSpawnerCredentialsAnnotation)
		return nil
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedTaskSpawnerCredentialsAnnotation] = string(data)

	fallback := taskSpawnerCredentialFallback(credentials)
	dst.Spec.TaskTemplate.Credentials = v1alpha1.Credentials{
		Type: v1alpha1.CredentialType(fallback.Type),
		SecretRef: &v1alpha1.SecretReference{
			Name: fallback.SecretRef.Name,
		},
	}
	return nil
}

func restorePreservedTaskSpawnerCredentials(annotations map[string]string, dst *v1alpha2.TaskSpawnerSpec) error {
	raw, ok := annotations[preservedTaskSpawnerCredentialsAnnotation]
	if !ok || raw == "" {
		return nil
	}
	var credentials []v1alpha2.SpawnerCredential
	if err := json.Unmarshal([]byte(raw), &credentials); err != nil || len(credentials) == 0 {
		return nil
	}
	if !matchesProjectedSpawnerCredential(dst.TaskTemplate.Credentials, taskSpawnerCredentialFallback(credentials)) {
		return nil
	}
	dst.Credentials = credentials
	dst.TaskTemplate.Credentials = nil
	if dst.TaskTemplate.Worker != nil {
		dst.TaskTemplate.Worker.Credentials = nil
	}
	return nil
}

func setPreservedGitHubCommentsReporting(dst *v1alpha1.TaskSpawner, when v1alpha2.When) error {
	preserved := preservedGitHubCommentsReporting{
		GitHubIssues:       preservedCommentsSource(gitHubIssuesReporting(when)),
		GitHubPullRequests: preservedCommentsSource(gitHubPullRequestsReporting(when)),
		GitHubWebhook:      preservedCommentsSource(gitHubWebhookReporting(when)),
	}
	if preserved.GitHubIssues == nil && preserved.GitHubPullRequests == nil && preserved.GitHubWebhook == nil {
		deleteAnnotation(dst.Annotations, preservedGitHubCommentsReportingAnnotation)
		return nil
	}

	data, err := json.Marshal(preserved)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedGitHubCommentsReportingAnnotation] = string(data)

	if preserved.GitHubIssues != nil {
		dst.Spec.When.GitHubIssues.Reporting.Enabled = true
	}
	if preserved.GitHubPullRequests != nil {
		dst.Spec.When.GitHubPullRequests.Reporting.Enabled = true
	}
	if preserved.GitHubWebhook != nil {
		dst.Spec.When.GitHubWebhook.Reporting.Enabled = true
	}
	return nil
}

func restorePreservedGitHubCommentsReporting(annotations map[string]string, when *v1alpha2.When) {
	raw, ok := annotations[preservedGitHubCommentsReportingAnnotation]
	if !ok || raw == "" {
		return
	}
	var preserved preservedGitHubCommentsReporting
	if err := json.Unmarshal([]byte(raw), &preserved); err != nil {
		return
	}
	restoreCommentsSource(preserved.GitHubIssues, gitHubIssuesReporting(*when))
	restoreCommentsSource(preserved.GitHubPullRequests, gitHubPullRequestsReporting(*when))
	restoreCommentsSource(preserved.GitHubWebhook, gitHubWebhookReporting(*when))
}

func preservedCommentsSource(reporting *v1alpha2.GitHubReporting) *preservedGitHubCommentsSource {
	if reporting == nil || reporting.Comments == nil {
		return nil
	}
	return &preservedGitHubCommentsSource{
		Enabled:  reporting.Enabled,
		Comments: *reporting.Comments,
	}
}

func restoreCommentsSource(preserved *preservedGitHubCommentsSource, reporting *v1alpha2.GitHubReporting) {
	if preserved == nil || reporting == nil || !reporting.Enabled {
		return
	}
	comments := preserved.Comments
	reporting.Comments = &comments
	reporting.Enabled = preserved.Enabled
}

func gitHubIssuesReporting(when v1alpha2.When) *v1alpha2.GitHubReporting {
	if when.GitHubIssues == nil {
		return nil
	}
	return when.GitHubIssues.Reporting
}

func gitHubPullRequestsReporting(when v1alpha2.When) *v1alpha2.GitHubReporting {
	if when.GitHubPullRequests == nil {
		return nil
	}
	return when.GitHubPullRequests.Reporting
}

func gitHubWebhookReporting(when v1alpha2.When) *v1alpha2.GitHubReporting {
	if when.GitHubWebhook == nil {
		return nil
	}
	return when.GitHubWebhook.Reporting
}

func taskSpawnerCredentialFallback(credentials []v1alpha2.SpawnerCredential) v1alpha2.SpawnerCredential {
	fallback := credentials[0]
	for _, credential := range credentials[1:] {
		if credential.Name < fallback.Name {
			fallback = credential
		}
	}
	return fallback
}

func matchesProjectedSpawnerCredential(credentials *v1alpha2.Credentials, projected v1alpha2.SpawnerCredential) bool {
	return credentials != nil &&
		credentials.Type == projected.Type &&
		credentials.SecretRef != nil &&
		credentials.SecretRef.Name == projected.SecretRef.Name
}

func foldTaskSpawnerForward(src *v1alpha1.TaskSpawnerSpec, dst *v1alpha2.TaskSpawnerSpec) {
	foldTaskTemplateAgentConfigRefForward(&src.TaskTemplate, &dst.TaskTemplate)

	if src.PollInterval != "" {
		if gi := dst.When.GitHubIssues; gi != nil && gi.PollInterval == "" {
			gi.PollInterval = src.PollInterval
		}
		if pr := dst.When.GitHubPullRequests; pr != nil && pr.PollInterval == "" {
			pr.PollInterval = src.PollInterval
		}
		if j := dst.When.Jira; j != nil && j.PollInterval == "" {
			j.PollInterval = src.PollInterval
		}
	}

	if gi := src.When.GitHubIssues; gi != nil && dst.When.GitHubIssues != nil {
		foldLegacyCommentPolicy(&dst.When.GitHubIssues.CommentPolicy, gi.TriggerComment, gi.ExcludeComments)
	}
	if pr := src.When.GitHubPullRequests; pr != nil && dst.When.GitHubPullRequests != nil {
		foldLegacyCommentPolicy(&dst.When.GitHubPullRequests.CommentPolicy, pr.TriggerComment, pr.ExcludeComments)
	}
}

func foldTaskTemplateAgentConfigRefForward(src *v1alpha1.TaskTemplate, dst *v1alpha2.TaskTemplate) {
	if len(dst.AgentConfigRefs) == 0 && src.AgentConfigRef != nil {
		dst.AgentConfigRefs = []v1alpha2.AgentConfigReference{{Name: src.AgentConfigRef.Name}}
	}
}

func backfillTaskTemplateLegacyWorkerFields(src *v1alpha2.TaskTemplate, dst *v1alpha1.TaskTemplate) error {
	if src.WorkerPoolRef != nil || src.Worker == nil {
		return nil
	}
	worker := src.Worker

	if dst.Type == "" {
		dst.Type = worker.Type
	}
	if dst.Credentials.Type == "" && worker.Credentials != nil {
		if err := convertViaJSON(worker.Credentials, &dst.Credentials); err != nil {
			return err
		}
	}
	if dst.Model == "" {
		dst.Model = worker.Model
	}
	if dst.Effort == "" {
		dst.Effort = worker.Effort
	}
	if dst.Image == "" {
		dst.Image = worker.Image
	}
	if dst.WorkspaceRef == nil && worker.WorkspaceRef != nil {
		if err := convertViaJSON(worker.WorkspaceRef, &dst.WorkspaceRef); err != nil {
			return err
		}
	}
	if len(dst.AgentConfigRefs) == 0 && len(worker.AgentConfigRefs) > 0 {
		if err := convertViaJSON(&worker.AgentConfigRefs, &dst.AgentConfigRefs); err != nil {
			return err
		}
	}
	if dst.PodOverrides == nil && worker.PodOverrides != nil {
		if err := convertViaJSON(worker.PodOverrides, &dst.PodOverrides); err != nil {
			return err
		}
	}
	return nil
}

func foldLegacyCommentPolicy(policy **v1alpha2.GitHubCommentPolicy, trigger string, exclude []string) {
	if trigger == "" && len(exclude) == 0 {
		return
	}
	if *policy == nil {
		*policy = &v1alpha2.GitHubCommentPolicy{}
	}
	if trigger != "" && (*policy).TriggerComment == "" {
		(*policy).TriggerComment = trigger
	}
	if len(exclude) > 0 && len((*policy).ExcludeComments) == 0 {
		(*policy).ExcludeComments = copyStrings(exclude)
	}
}

func backfillTaskSpawnerLegacy(spec *v1alpha1.TaskSpawnerSpec) {
	if spec.PollInterval == "" {
		spec.PollInterval = commonPollingInterval(spec.When)
	}
	if gi := spec.When.GitHubIssues; gi != nil {
		backfillGitHubIssuesLegacy(gi)
	}
	if pr := spec.When.GitHubPullRequests; pr != nil {
		backfillGitHubPullRequestsLegacy(pr)
	}
}

func commonPollingInterval(when v1alpha1.When) string {
	var common string
	for _, interval := range []string{
		pollIntervalFromGitHubIssues(when.GitHubIssues),
		pollIntervalFromGitHubPullRequests(when.GitHubPullRequests),
		pollIntervalFromJira(when.Jira),
	} {
		if interval == "" {
			continue
		}
		if common == "" {
			common = interval
			continue
		}
		if common != interval {
			return ""
		}
	}
	return common
}

func pollIntervalFromGitHubIssues(source *v1alpha1.GitHubIssues) string {
	if source == nil {
		return ""
	}
	return source.PollInterval
}

func pollIntervalFromGitHubPullRequests(source *v1alpha1.GitHubPullRequests) string {
	if source == nil {
		return ""
	}
	return source.PollInterval
}

func pollIntervalFromJira(source *v1alpha1.Jira) string {
	if source == nil {
		return ""
	}
	return source.PollInterval
}

func backfillGitHubIssuesLegacy(source *v1alpha1.GitHubIssues) {
	if source.CommentPolicy == nil || !commentPolicyFitsLegacyFields(source.CommentPolicy) {
		return
	}
	source.TriggerComment = source.CommentPolicy.TriggerComment
	source.ExcludeComments = copyStrings(source.CommentPolicy.ExcludeComments)
	source.CommentPolicy = nil
}

func backfillGitHubPullRequestsLegacy(source *v1alpha1.GitHubPullRequests) {
	if source.CommentPolicy == nil || !commentPolicyFitsLegacyFields(source.CommentPolicy) {
		return
	}
	source.TriggerComment = source.CommentPolicy.TriggerComment
	source.ExcludeComments = copyStrings(source.CommentPolicy.ExcludeComments)
	source.CommentPolicy = nil
}

func commentPolicyFitsLegacyFields(policy *v1alpha1.GitHubCommentPolicy) bool {
	return len(policy.AllowedUsers) == 0 &&
		len(policy.AllowedTeams) == 0 &&
		policy.MinimumPermission == ""
}
