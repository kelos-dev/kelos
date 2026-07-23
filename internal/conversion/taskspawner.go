package conversion

import (
	"context"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

// preservedNameTemplateAnnotation carries taskTemplate.nameTemplate (a
// v1alpha2-only field) across a v1alpha1 round-trip so a client that reads and
// writes the object through v1alpha1 does not silently drop it. v1alpha1 does
// not gain the capability — the value only survives in this annotation.
const preservedNameTemplateAnnotation = "kelos.dev/v1alpha2-name-template"

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
	return convertViaJSON(&src.Status, &dst.Status)
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
