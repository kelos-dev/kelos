package slack

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	"github.com/slack-go/slack/slackevents"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// TestRouteMessageThreadContextBody verifies that routeMessage preserves the
// thread context body for thread replies (HasThreadContext=true) and uses the
// trigger-processed body for top-level messages.
func TestRouteMessageThreadContextBody(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	spawner := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{
				Slack: &v1alpha1.Slack{},
			},
			TaskTemplate: v1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: v1alpha1.Credentials{
					Type: v1alpha1.CredentialTypeNone,
				},
				PromptTemplate: "{{.Body}}",
			},
		},
	}

	tests := []struct {
		name     string
		msg      *SlackMessageData
		wantBody string
	}{
		{
			name: "top-level message uses raw text as body",
			msg: &SlackMessageData{
				UserID:    "U1",
				ChannelID: "C1",
				Text:      "<@UBOT> fix the bug",
				Body:      "<@UBOT> fix the bug",
				Timestamp: "1111111111.111111",
			},
			wantBody: "<@UBOT> fix the bug",
		},
		{
			name: "top-level message with attachments preserves full body",
			msg: &SlackMessageData{
				UserID:    "U1",
				ChannelID: "C1",
				Text:      "<@UBOT> fix the bug",
				Body:      "<@UBOT> fix the bug\n[Attachment: error log]\nStackTrace: panic at line 42",
				Timestamp: "3333333333.333333",
			},
			wantBody: "<@UBOT> fix the bug\n[Attachment: error log]\nStackTrace: panic at line 42",
		},
		{
			name: "thread reply with context preserves thread body",
			msg: &SlackMessageData{
				UserID:           "U1",
				ChannelID:        "C1",
				Text:             "<@UBOT> can you take a look",
				Body:             "Slack thread conversation:\n\nUser: original question\n\nUser: <@UBOT> can you take a look\n",
				ThreadTS:         "1111111111.000000",
				Timestamp:        "2222222222.222222",
				HasThreadContext: true,
			},
			// HasThreadContext=true means the thread body is preserved as-is
			wantBody: "Slack thread conversation:\n\nUser: original question\n\nUser: <@UBOT> can you take a look\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(spawner.DeepCopy()).
				Build()

			tb, err := taskbuilder.NewTaskBuilder(cl)
			if err != nil {
				t.Fatalf("NewTaskBuilder: %v", err)
			}

			h := &SlackHandler{
				client:      cl,
				log:         logr.Discard(),
				taskBuilder: tb,
				botUserID:   "UBOT",
			}

			h.routeMessage(context.Background(), tt.msg)

			// Verify a task was created with the expected body
			var tasks v1alpha1.TaskList
			if err := cl.List(context.Background(), &tasks); err != nil {
				t.Fatalf("List tasks: %v", err)
			}
			if len(tasks.Items) != 1 {
				t.Fatalf("Expected 1 task, got %d", len(tasks.Items))
			}
			if tasks.Items[0].Spec.Prompt != tt.wantBody {
				t.Errorf("Task prompt = %q, want %q", tasks.Items[0].Spec.Prompt, tt.wantBody)
			}
		})
	}
}

// TestMessageEventAttachmentsOnRegularMessage verifies that the slack-go
// library's custom UnmarshalJSON populates Message (and thus
// Message.Attachments) even for regular top-level messages that have no
// subtype. This is the invariant that hasContent and enrichMessage rely on.
func TestMessageEventAttachmentsOnRegularMessage(t *testing.T) {
	tests := []struct {
		name            string
		json            string
		wantText        string
		wantAttachments int
		wantMessageNil  bool
	}{
		{
			name:            "text only",
			json:            `{"type":"message","text":"hello","user":"U1","ts":"1.1","channel":"C1"}`,
			wantText:        "hello",
			wantAttachments: 0,
		},
		{
			name: "text with attachment",
			json: `{"type":"message","text":"see attached","user":"U1","ts":"1.1","channel":"C1",
				"attachments":[{"fallback":"log","text":"error log"}]}`,
			wantText:        "see attached",
			wantAttachments: 1,
		},
		{
			name: "attachment only (no text)",
			json: `{"type":"message","text":"","user":"U1","ts":"1.1","channel":"C1",
				"attachments":[{"fallback":"log","text":"error log"}]}`,
			wantText:        "",
			wantAttachments: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ev slackevents.MessageEvent
			if err := json.Unmarshal([]byte(tt.json), &ev); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if ev.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", ev.Text, tt.wantText)
			}
			if ev.Message == nil {
				t.Fatal("Message is nil; UnmarshalJSON should always populate it for regular messages")
			}
			if got := len(ev.Message.Attachments); got != tt.wantAttachments {
				t.Errorf("len(Message.Attachments) = %d, want %d", got, tt.wantAttachments)
			}

			// Verify hasContent logic matches
			hasContent := ev.Text != "" ||
				(ev.Message != nil && len(ev.Message.Attachments) > 0)
			wantContent := tt.wantText != "" || tt.wantAttachments > 0
			if hasContent != wantContent {
				t.Errorf("hasContent = %v, want %v", hasContent, wantContent)
			}
		})
	}
}

func TestCreateTaskLongSpawnerName(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	longName := "this-is-a-very-long-spawner-name-that-exceeds-forty-four-characters"

	spawner := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      longName,
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			TaskTemplate: v1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: v1alpha1.Credentials{
					Type: v1alpha1.CredentialTypeNone,
				},
				PromptTemplate: "{{.Body}}",
			},
		},
	}

	tb, err := taskbuilder.NewTaskBuilder(nil)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	h := &SlackHandler{
		client:      cl,
		log:         logr.Discard(),
		taskBuilder: tb,
	}

	msg1 := &SlackMessageData{
		UserID:    "U123",
		ChannelID: "C456",
		Text:      "first message",
		Body:      "first message",
		Timestamp: "1111111111.111111",
	}

	msg2 := &SlackMessageData{
		UserID:    "U123",
		ChannelID: "C456",
		Text:      "second message",
		Body:      "second message",
		Timestamp: "2222222222.222222",
	}

	if err := h.createTask(context.Background(), spawner, msg1); err != nil {
		t.Fatalf("First createTask() error: %v", err)
	}
	if err := h.createTask(context.Background(), spawner, msg2); err != nil {
		t.Fatalf("Second createTask() error: %v", err)
	}

	var tasks v1alpha1.TaskList
	if err := cl.List(context.Background(), &tasks); err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	if len(tasks.Items) != 2 {
		t.Errorf("Expected 2 tasks with long spawner name, got %d (name collision)", len(tasks.Items))
	}
	for _, task := range tasks.Items {
		if len(task.Name) > 63 {
			t.Errorf("Task name exceeds 63 chars: %q (len=%d)", task.Name, len(task.Name))
		}
	}
}

func TestCreateTaskAlreadyExists(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	spawner := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			TaskTemplate: v1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: v1alpha1.Credentials{
					Type: v1alpha1.CredentialTypeNone,
				},
				PromptTemplate: "{{.Body}}",
			},
		},
	}

	msg := &SlackMessageData{
		UserID:    "U123",
		ChannelID: "C456",
		Text:      "hello",
		Body:      "hello",
		Timestamp: "1234567890.123456",
	}

	tb, err := taskbuilder.NewTaskBuilder(nil)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	h := &SlackHandler{
		client:      cl,
		log:         logr.Discard(),
		taskBuilder: tb,
	}

	// First call should succeed
	if err := h.createTask(context.Background(), spawner, msg); err != nil {
		t.Fatalf("First createTask() error: %v", err)
	}

	// Second call with same message should not return an error (AlreadyExists is handled)
	if err := h.createTask(context.Background(), spawner, msg); err != nil {
		t.Fatalf("Second createTask() should not error on AlreadyExists, got: %v", err)
	}
}

func TestHandleMemberJoinedChannelIgnoresOtherUsers(t *testing.T) {
	h := &SlackHandler{
		log:         logr.Discard(),
		botUserID:   "UBOT",
		joinMessage: "Welcome!",
		// api is nil — if handleMemberJoinedChannel tries to post for a
		// non-bot user it will panic, which is the desired failure mode here.
	}

	evt := &slackevents.MemberJoinedChannelEvent{
		User:    "UOTHER",
		Channel: "C123",
	}

	// Should return without attempting to post (no panic = pass).
	h.handleMemberJoinedChannel(context.Background(), evt)
}

func TestHandleMemberJoinedChannelSkipsEmptyMessage(t *testing.T) {
	h := &SlackHandler{
		log:       logr.Discard(),
		botUserID: "UBOT",
		// joinMessage is empty — should not attempt to post.
		// api is nil — would panic if it tried.
	}

	evt := &slackevents.MemberJoinedChannelEvent{
		User:    "UBOT",
		Channel: "C123",
	}

	h.handleMemberJoinedChannel(context.Background(), evt)
}
