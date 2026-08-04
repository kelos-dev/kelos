package conversion

import (
	"context"
	"encoding/json"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/scoring"
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

// preservedSlackScoringAnnotation carries when.slack.scoring (a v1alpha2-only
// field) across a v1alpha1 round-trip. Without it a client that reads and
// writes the object through v1alpha1 would silently disable result scoring on a
// stored v1alpha2 TaskSpawner.
const preservedSlackScoringAnnotation = "kelos.dev/v1alpha2-slack-scoring"

// maxRestoredSlackReactions mirrors the MaxItems marker on
// SlackScoring.Reactions. Restored entries never passed that validation, so the
// cap is re-applied when reading the preservation annotation.
const maxRestoredSlackReactions = 32

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
	restorePreservedSlackScoring(src.Annotations, &dst.Spec.When)
	deleteAnnotation(dst.Annotations, preservedSlackScoringAnnotation)
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
	if err := setPreservedSlackScoring(dst, src.Spec.When.Slack); err != nil {
		return err
	}
	return convertViaJSON(&src.Status, &dst.Status)
}

// setPreservedSlackScoring records when.slack.scoring into an annotation on the
// v1alpha1 object so it survives a v1alpha1 round-trip. The annotation is
// cleared when the Slack source has no scoring configured.
func setPreservedSlackScoring(dst *v1alpha1.TaskSpawner, slack *v1alpha2.Slack) error {
	if slack == nil || slack.Scoring == nil {
		deleteAnnotation(dst.Annotations, preservedSlackScoringAnnotation)
		return nil
	}
	data, err := json.Marshal(slack.Scoring)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedSlackScoringAnnotation] = string(data)
	return nil
}

// restorePreservedSlackScoring restores a scoring block dropped by a v1alpha1
// round-trip, unless the Slack source already carries one.
func restorePreservedSlackScoring(annotations map[string]string, dst *v1alpha2.When) {
	raw, ok := annotations[preservedSlackScoringAnnotation]
	if !ok || raw == "" {
		return
	}
	if dst.Slack == nil || dst.Slack.Scoring != nil {
		return
	}
	var restored v1alpha2.SlackScoring
	if err := json.Unmarshal([]byte(raw), &restored); err != nil {
		// The annotation is best-effort preservation data and can be set by
		// users; malformed data must not block API version conversion.
		return
	}

	// The annotation is user-writable and never saw the v1alpha2 schema: CRD
	// validation runs against the request version, and v1alpha1 has no scoring
	// field. Everything the schema would have enforced is therefore re-applied
	// here — per-entry constraints, the item cap, and name uniqueness — because an
	// out-of-enum verdict would otherwise reach TaskScore.spec.verdict, where the
	// enum *is* enforced, and fail every score creation for that reaction.
	// Offending entries are dropped rather than rejected so conversion is never
	// blocked.
	valid := make([]v1alpha2.SlackReactionScore, 0, len(restored.Reactions))
	seen := make(map[string]struct{}, len(restored.Reactions))
	for _, entry := range restored.Reactions {
		if len(valid) >= maxRestoredSlackReactions {
			break
		}
		if !scoring.ValidReactionScore(entry) {
			continue
		}
		// listMapKey=name makes duplicates unrepresentable in a stored v1alpha2
		// object, so keeping more than one would leave the mapping ambiguous.
		if _, duplicate := seen[entry.Name]; duplicate {
			continue
		}
		seen[entry.Name] = struct{}{}
		valid = append(valid, entry)
	}
	restored.Reactions = valid

	dst.Slack.Scoring = &restored
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
