package source

import (
	"testing"
	"time"
)

func gitlabTestNote(body, user, createdAt string) gitlabNote {
	return gitlabNote{Body: body, Author: gitlabUser{Username: user}, CreatedAt: createdAt}
}

func TestEvaluateGitLabCommentPolicy(t *testing.T) {
	trigger := "/kelos fix"
	exclude := []string{"/kelos needs-input", "/kelos stop"}
	t1 := "2026-01-01T00:00:00Z"
	t2 := "2026-01-02T00:00:00Z"
	t3 := "2026-01-03T00:00:00Z"

	tests := []struct {
		name        string
		policy      gitlabCommentPolicy
		description string
		author      string
		notes       []gitlabNote
		wantAllowed bool
		wantTime    string
	}{
		{
			name:        "no policy allows everything",
			policy:      gitlabCommentPolicy{},
			wantAllowed: true,
		},
		{
			name:        "trigger only without any command",
			policy:      gitlabCommentPolicy{TriggerComment: trigger},
			notes:       []gitlabNote{gitlabTestNote("please look", "bob", t1)},
			wantAllowed: false,
		},
		{
			name:        "trigger in note carries its time",
			policy:      gitlabCommentPolicy{TriggerComment: trigger},
			notes:       []gitlabNote{gitlabTestNote("please look", "bob", t1), gitlabTestNote(trigger, "alice", t2)},
			wantAllowed: true,
			wantTime:    t2,
		},
		{
			name:        "trigger in description carries no time",
			policy:      gitlabCommentPolicy{TriggerComment: trigger},
			description: "Broken build\n" + trigger,
			author:      "alice",
			wantAllowed: true,
		},
		{
			name:        "trigger must start a line",
			policy:      gitlabCommentPolicy{TriggerComment: trigger},
			notes:       []gitlabNote{gitlabTestNote("maybe run "+trigger+" later", "alice", t1)},
			wantAllowed: false,
		},
		{
			name:        "system notes never match",
			policy:      gitlabCommentPolicy{TriggerComment: trigger},
			notes:       []gitlabNote{{Body: trigger, System: true, Author: gitlabUser{Username: "alice"}, CreatedAt: t1}},
			wantAllowed: false,
		},
		{
			name:        "unauthorized note author is ignored",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, AllowedUsers: []string{"alice"}},
			notes:       []gitlabNote{gitlabTestNote(trigger, "mallory", t1)},
			wantAllowed: false,
		},
		{
			name:        "unauthorized description author is ignored",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, AllowedUsers: []string{"alice"}},
			description: trigger,
			author:      "mallory",
			wantAllowed: false,
		},
		{
			name:        "allowed users match case-insensitively and without @",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, AllowedUsers: []string{"@Alice"}},
			notes:       []gitlabNote{gitlabTestNote(trigger, "alice", t1)},
			wantAllowed: true,
			wantTime:    t1,
		},
		{
			name:        "exclude only blocks on any exclude command",
			policy:      gitlabCommentPolicy{ExcludeComments: exclude},
			notes:       []gitlabNote{gitlabTestNote("/kelos stop", "bob", t1)},
			wantAllowed: false,
		},
		{
			name:        "exclude only allows without commands",
			policy:      gitlabCommentPolicy{ExcludeComments: exclude},
			notes:       []gitlabNote{gitlabTestNote("looks fine", "bob", t1)},
			wantAllowed: true,
		},
		{
			name:        "exclude in description blocks",
			policy:      gitlabCommentPolicy{ExcludeComments: exclude},
			description: "/kelos needs-input",
			author:      "bob",
			wantAllowed: false,
		},
		{
			name:        "latest command wins: exclude after trigger",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, ExcludeComments: exclude},
			notes:       []gitlabNote{gitlabTestNote(trigger, "alice", t1), gitlabTestNote("/kelos needs-input", "alice", t2)},
			wantAllowed: false,
		},
		{
			name:        "latest command wins: trigger after exclude",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, ExcludeComments: exclude},
			notes:       []gitlabNote{gitlabTestNote("/kelos needs-input", "alice", t1), gitlabTestNote(trigger, "alice", t3)},
			wantAllowed: true,
			wantTime:    t3,
		},
		{
			name:        "ties on time fall back to note order",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, ExcludeComments: exclude},
			notes:       []gitlabNote{gitlabTestNote(trigger, "alice", t1), gitlabTestNote("/kelos stop", "alice", t1)},
			wantAllowed: false,
		},
		{
			name:        "unparseable time falls back to note order",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, ExcludeComments: exclude},
			notes:       []gitlabNote{gitlabTestNote("/kelos stop", "alice", "bad"), gitlabTestNote(trigger, "alice", "bad")},
			wantAllowed: true,
		},
		{
			name:        "note commands outrank description commands",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, ExcludeComments: exclude},
			description: "/kelos stop",
			author:      "alice",
			notes:       []gitlabNote{gitlabTestNote(trigger, "alice", t1)},
			wantAllowed: true,
			wantTime:    t1,
		},
		{
			name:        "description exclude wins over description trigger",
			policy:      gitlabCommentPolicy{TriggerComment: trigger, ExcludeComments: exclude},
			description: trigger + "\n/kelos stop",
			author:      "alice",
			wantAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authorizer := newGitLabCommentAuthorizer(tt.policy)
			allowed, triggerTime := evaluateGitLabCommentPolicy(tt.description, tt.author, tt.notes, tt.policy, authorizer)
			if allowed != tt.wantAllowed {
				t.Fatalf("allowed = %v, want %v", allowed, tt.wantAllowed)
			}
			var want time.Time
			if tt.wantTime != "" {
				want, _ = time.Parse(time.RFC3339, tt.wantTime)
			}
			if !triggerTime.Equal(want) {
				t.Fatalf("triggerTime = %v, want %v", triggerTime, want)
			}
		})
	}
}

func TestGitLabConversationNotes(t *testing.T) {
	notes := []gitlabNote{
		{Body: "discussion", Author: gitlabUser{Username: "bob"}},
		{Body: "added label", System: true},
		{Body: "inline", Type: "DiffNote", Position: &gitlabNotePosition{NewPath: "a.go", NewLine: 1}},
		{Body: "reply", Author: gitlabUser{Username: "alice"}},
	}
	got := gitlabNoteBodies(gitlabConversationNotes(notes))
	if len(got) != 2 || got[0] != "discussion" || got[1] != "reply" {
		t.Fatalf("expected discussion notes only, got %q", got)
	}
}
