package v1alpha2

import (
	"reflect"
	"testing"
)

// whenFieldTrackers records, for every When field, whether it is a code-host
// source. Adding a field to When without listing it here fails the test, so a
// new provider cannot be forgotten in Tracker().
var whenFieldTrackers = map[string]bool{
	"GitHubIssues":       true,
	"GitHubPullRequests": true,
	"GitHubWebhook":      true,
	"GitLab":             true,
	"GitLabWebhook":      true,
	"Cron":               false,
	"Jira":               false,
	"LinearWebhook":      false,
	"GenericWebhook":     false,
	"Slack":              false,
}

func TestWhenTrackerCoversEveryField(t *testing.T) {
	whenType := reflect.TypeOf(When{})
	for i := 0; i < whenType.NumField(); i++ {
		field := whenType.Field(i)
		wantTracker, listed := whenFieldTrackers[field.Name]
		if !listed {
			t.Fatalf("When.%s is not classified in whenFieldTrackers; decide whether Tracker() must cover it", field.Name)
		}
		var when When
		reflect.ValueOf(&when).Elem().Field(i).Set(reflect.New(field.Type.Elem()))
		if _, ok := when.Tracker(); ok != wantTracker {
			t.Errorf("When{%s}.Tracker() ok = %v, want %v", field.Name, ok, wantTracker)
		}
	}
	if len(whenFieldTrackers) != whenType.NumField() {
		t.Errorf("whenFieldTrackers lists %d fields, When has %d", len(whenFieldTrackers), whenType.NumField())
	}
}

func TestWhenTracker(t *testing.T) {
	tests := []struct {
		name string
		when When
		want TrackerSource
	}{
		{
			name: "github issues with deprecated enabled flag",
			when: When{GitHubIssues: &GitHubIssues{Repo: "org/upstream", PriorityLabels: []string{"p0"}, Reporting: &GitHubReporting{Enabled: true}}},
			want: TrackerSource{Provider: WorkspaceProviderGitHub, Repo: "org/upstream", PriorityLabels: []string{"p0"}, Comments: &CommentsReporting{Mode: CommentModePerTask}},
		},
		{
			name: "github pull requests with sticky comments and checks",
			when: When{GitHubPullRequests: &GitHubPullRequests{Reporting: &GitHubReporting{
				Comments: &CommentsReporting{Mode: CommentModeSticky},
				Checks:   &GitHubChecksReporting{Name: "kelos"},
			}}},
			want: TrackerSource{Provider: WorkspaceProviderGitHub, Comments: &CommentsReporting{Mode: CommentModeSticky}, Checks: &GitHubChecksReporting{Name: "kelos"}},
		},
		{
			name: "github webhook restriction",
			when: When{GitHubWebhook: &GitHubWebhook{Repository: "org/repo"}},
			want: TrackerSource{Provider: WorkspaceProviderGitHub, Webhook: true, Repo: "org/repo"},
		},
		{
			name: "gitlab project with comments",
			when: When{GitLab: &GitLab{Project: "group/repo", Reporting: &GitLabReporting{Comments: &CommentsReporting{}}}},
			want: TrackerSource{Provider: WorkspaceProviderGitLab, Repo: "group/repo", Comments: &CommentsReporting{}},
		},
		{
			name: "gitlab webhook without reporting",
			when: When{GitLabWebhook: &GitLabWebhook{Project: "group/repo"}},
			want: TrackerSource{Provider: WorkspaceProviderGitLab, Webhook: true, Repo: "group/repo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.when.Tracker()
			if !ok {
				t.Fatal("Tracker() ok = false, want true")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Tracker() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTrackerSourceCommentMode(t *testing.T) {
	if got := (TrackerSource{}).CommentMode(); got != CommentModePerTask {
		t.Errorf("CommentMode() without comments = %q, want PerTask", got)
	}
	if got := (TrackerSource{Comments: &CommentsReporting{}}).CommentMode(); got != CommentModePerTask {
		t.Errorf("CommentMode() with empty mode = %q, want PerTask", got)
	}
	if got := (TrackerSource{Comments: &CommentsReporting{Mode: CommentModeSticky}}).CommentMode(); got != CommentModeSticky {
		t.Errorf("CommentMode() = %q, want Sticky", got)
	}
}

func TestWhenPollInterval(t *testing.T) {
	tests := map[string]When{
		"2m":  {GitHubIssues: &GitHubIssues{PollInterval: "2m"}},
		"3m":  {GitHubPullRequests: &GitHubPullRequests{PollInterval: "3m"}},
		"4m":  {Jira: &Jira{PollInterval: "4m"}},
		"30s": {GitLab: &GitLab{PollInterval: "30s"}},
		"":    {GitHubWebhook: &GitHubWebhook{}},
	}
	for want, when := range tests {
		if got := when.PollInterval(); got != want {
			t.Errorf("PollInterval() = %q, want %q", got, want)
		}
	}
}
