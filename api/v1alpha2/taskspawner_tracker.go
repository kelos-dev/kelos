package v1alpha2

// TrackerSource is the provider-neutral view of a code-host source on a
// TaskSpawner. Polling sources (githubIssues, githubPullRequests, gitlab) and
// webhook sources (githubWebhook, gitlabWebhook) all project onto it, so code
// that only needs the provider, the repository, or the reporting
// configuration does not branch on the concrete source.
type TrackerSource struct {
	// Provider is the code host, using the Workspace provider values.
	Provider string
	// Webhook is true for event-driven sources and false for polling sources.
	Webhook bool
	// Repo scopes the source to a repository other than the Workspace's: the
	// GitHub "owner/repo" override or restriction, or the GitLab project path.
	// Empty means the Workspace repository.
	Repo string
	// PriorityLabels orders discovered items for polling sources that support it.
	PriorityLabels []string
	// Comments is the status-comment configuration, or nil when comment
	// reporting is off. The deprecated GitHub reporting.enabled flag maps to
	// PerTask comments.
	Comments *CommentsReporting
	// Checks is the GitHub Check Run configuration, or nil.
	Checks *GitHubChecksReporting
}

// CommentMode returns the effective comment mode: the configured one, or
// PerTask when comments are enabled without an explicit mode.
func (t TrackerSource) CommentMode() CommentMode {
	if t.Comments != nil && t.Comments.Mode != "" {
		return t.Comments.Mode
	}
	return CommentModePerTask
}

// Tracker returns the code-host view of the configured source. ok is false
// when the source is not backed by a code host (cron, jira, linear, generic
// webhook, slack) or when no source is set.
func (w When) Tracker() (TrackerSource, bool) {
	switch {
	case w.GitHubIssues != nil:
		return githubTracker(w.GitHubIssues.Repo, w.GitHubIssues.PriorityLabels, w.GitHubIssues.Reporting, false), true
	case w.GitHubPullRequests != nil:
		return githubTracker(w.GitHubPullRequests.Repo, w.GitHubPullRequests.PriorityLabels, w.GitHubPullRequests.Reporting, false), true
	case w.GitHubWebhook != nil:
		return githubTracker(w.GitHubWebhook.Repository, nil, w.GitHubWebhook.Reporting, true), true
	case w.GitLab != nil:
		return gitlabTracker(w.GitLab.Project, w.GitLab.Reporting, false), true
	case w.GitLabWebhook != nil:
		return gitlabTracker(w.GitLabWebhook.Project, w.GitLabWebhook.Reporting, true), true
	}
	return TrackerSource{}, false
}

// PollInterval returns the configured poll interval of the polling source, or
// "" when the source uses the default or is not polled.
func (w When) PollInterval() string {
	switch {
	case w.GitHubIssues != nil:
		return w.GitHubIssues.PollInterval
	case w.GitHubPullRequests != nil:
		return w.GitHubPullRequests.PollInterval
	case w.Jira != nil:
		return w.Jira.PollInterval
	case w.GitLab != nil:
		return w.GitLab.PollInterval
	}
	return ""
}

func githubTracker(repo string, priorityLabels []string, reporting *GitHubReporting, webhook bool) TrackerSource {
	t := TrackerSource{Provider: WorkspaceProviderGitHub, Webhook: webhook, Repo: repo, PriorityLabels: priorityLabels}
	if reporting == nil {
		return t
	}
	t.Comments = reporting.Comments
	if t.Comments == nil && reporting.Enabled {
		t.Comments = &CommentsReporting{Mode: CommentModePerTask}
	}
	t.Checks = reporting.Checks
	return t
}

func gitlabTracker(project string, reporting *GitLabReporting, webhook bool) TrackerSource {
	t := TrackerSource{Provider: WorkspaceProviderGitLab, Webhook: webhook, Repo: project}
	if reporting != nil {
		t.Comments = reporting.Comments
	}
	return t
}
