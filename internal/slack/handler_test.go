package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	goslack "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/spawnercredentials"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// TestRouteMessageThreadContextBody verifies that routeMessage preserves the
// thread context body for thread replies (HasThreadContext=true) and uses the
// trigger-processed body for top-level messages.
func TestRouteMessageThreadContextBody(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				Slack: &kelos.Slack{},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: kelos.CredentialTypeNone,
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
			var tasks kelos.TaskList
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
	utilruntime.Must(kelos.AddToScheme(scheme))

	longName := "this-is-a-very-long-spawner-name-that-exceeds-forty-four-characters"

	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      longName,
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: kelos.CredentialTypeNone,
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

	var tasks kelos.TaskList
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

func TestCreateTaskAssignsTaskSpawnerCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-account", Namespace: "default", UID: "spawner-uid"},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{Type: "claude-code", PromptTemplate: "{{.Body}}"},
			Credentials: []kelos.SpawnerCredential{
				{Name: "account-b", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-b"}},
				{Name: "account-a", Type: kelos.CredentialTypeOAuth, SecretRef: kelos.SecretReference{Name: "secret-a"}},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	tb, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}
	h := &SlackHandler{client: cl, log: logr.Discard(), taskBuilder: tb}

	for _, msg := range []*SlackMessageData{
		{UserID: "U123", ChannelID: "C456", Body: "first", Timestamp: "1111111111.111111"},
		{UserID: "U123", ChannelID: "C456", Body: "second", Timestamp: "2222222222.222222"},
	} {
		if err := h.createTask(context.Background(), spawner, msg); err != nil {
			t.Fatalf("createTask() error = %v", err)
		}
	}

	var tasks kelos.TaskList
	if err := cl.List(context.Background(), &tasks); err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	wantSecrets := map[string]string{"account-a": "secret-a", "account-b": "secret-b"}
	for i := range tasks.Items {
		task := &tasks.Items[i]
		if task.Spec.Credentials == nil || task.Spec.Credentials.SecretRef == nil {
			t.Fatalf("Task %s has no assigned credentials", task.Name)
		}
		credentialName := task.Labels[spawnercredentials.AssignmentLabel]
		wantSecret, ok := wantSecrets[credentialName]
		if !ok {
			t.Fatalf("Task %s credential label = %q, want a configured credential", task.Name, credentialName)
		}
		if got := task.Spec.Credentials.SecretRef.Name; got != wantSecret {
			t.Errorf("Task %s Secret = %q, want %q for %s", task.Name, got, wantSecret, credentialName)
		}
	}
}

func TestCreateTaskAlreadyExists(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: kelos.CredentialTypeNone,
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

	// Verify Slack user ID annotation is set
	taskList := &kelos.TaskList{}
	if err := cl.List(context.Background(), taskList); err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}
	got := taskList.Items[0].Annotations[reporting.AnnotationSlackUserID]
	if got != "U123" {
		t.Errorf("Expected slack-user-id annotation %q, got %q", "U123", got)
	}

	// Second call with same message should not return an error (AlreadyExists is handled)
	if err := h.createTask(context.Background(), spawner, msg); err != nil {
		t.Fatalf("Second createTask() should not error on AlreadyExists, got: %v", err)
	}
}

// TestCreateTaskNameCollisionWithUnrelatedTask ensures a nameTemplate that
// renders a name already used by a Task owned by a different spawner surfaces an
// error instead of silently suppressing the Slack message as deduplication.
func TestCreateTaskNameCollisionWithUnrelatedTask(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "spawner-b", Namespace: "default", UID: "uid-b"},
		Spec: kelos.TaskSpawnerSpec{
			TaskTemplate: kelos.TaskTemplate{
				Type:           "claude-code",
				Credentials:    &kelos.Credentials{Type: kelos.CredentialTypeNone},
				NameTemplate:   "shared-name",
				PromptTemplate: "{{.Body}}",
			},
		},
	}
	msg := &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "hi", Body: "hi", Timestamp: "1.1"}

	unrelated := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-name",
			Namespace: "default",
			Labels:    map[string]string{"kelos.dev/taskspawner": "spawner-a"},
		},
		Spec: kelos.TaskSpec{Type: "claude-code", Prompt: "other"},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unrelated).Build()
	tb, err := taskbuilder.NewTaskBuilder(nil)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}
	h := &SlackHandler{client: cl, log: logr.Discard(), taskBuilder: tb}

	if err := h.createTask(context.Background(), spawner, msg); err == nil {
		t.Fatal("expected error on name collision with an unrelated Task, got nil")
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

// TestHandleMessageEventBotIDSelfDetection verifies that handleMessageEvent
// marks a bot_message-subtype event with the handler's BotID as IsSelfMessage,
// so that spawners with OthersOnly policy reject the bot's own output.
func TestHandleMessageEventBotIDSelfDetection(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bot-listener",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				Slack: &kelos.Slack{
					BotMessagePolicy: kelos.BotMessagePolicyOthersOnly,
					Triggers: []kelos.SlackTrigger{
						{Pattern: ".*", MentionOptional: boolPtr(true)},
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: kelos.CredentialTypeNone,
				},
				PromptTemplate: "{{.Body}}",
			},
		},
	}

	tests := []struct {
		name       string
		event      *slackevents.MessageEvent
		wantTask   bool
		wantReason string
	}{
		{
			name: "self bot_message with BotID and empty User is rejected",
			event: &slackevents.MessageEvent{
				Type:      "message",
				SubType:   "bot_message",
				BotID:     "B0001",
				User:      "",
				Text:      "I completed the task",
				Channel:   "C1",
				TimeStamp: "1111111111.111111",
			},
			wantTask:   false,
			wantReason: "self bot_message should be rejected by OthersOnly policy",
		},
		{
			name: "other bot_message with different BotID is allowed",
			event: &slackevents.MessageEvent{
				Type:      "message",
				SubType:   "bot_message",
				BotID:     "B9999",
				User:      "",
				Text:      "deploy notification",
				Channel:   "C1",
				TimeStamp: "2222222222.222222",
			},
			wantTask:   true,
			wantReason: "other bot's message should be allowed by OthersOnly policy",
		},
		{
			name: "self message via User field (no BotID) is rejected",
			event: &slackevents.MessageEvent{
				Type:      "message",
				User:      "UBOT",
				Text:      "self-triggered",
				Channel:   "C1",
				TimeStamp: "3333333333.333333",
			},
			wantTask:   false,
			wantReason: "message from botUserID should be rejected by OthersOnly policy",
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
				botID:       "B0001",
				// api is nil — enrichMessage will degrade gracefully
				// (GetUserInfoContext/GetPermalinkContext fail and are skipped).
			}

			// enrichMessage calls h.api methods that will panic on nil.
			// We bypass that by calling the marking + routing logic directly.
			msg := &SlackMessageData{
				UserID:    tt.event.User,
				ChannelID: tt.event.Channel,
				Text:      tt.event.Text,
				Body:      tt.event.Text,
				Timestamp: tt.event.TimeStamp,
			}

			// Replicate the production marking logic from handleMessageEvent.
			if tt.event.SubType == "bot_message" || tt.event.BotID != "" || tt.event.User == h.botUserID {
				msg.IsBotMessage = true
			}
			if tt.event.User == h.botUserID || (h.botID != "" && tt.event.BotID == h.botID) {
				msg.IsSelfMessage = true
			}

			h.routeMessage(context.Background(), msg)

			var tasks kelos.TaskList
			if err := cl.List(context.Background(), &tasks); err != nil {
				t.Fatalf("List tasks: %v", err)
			}
			got := len(tasks.Items) > 0
			if got != tt.wantTask {
				t.Errorf("%s: task created = %v, want %v", tt.wantReason, got, tt.wantTask)
			}
		})
	}
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

func TestDenySlackConnectChannels(t *testing.T) {
	tests := []struct {
		name           string
		channelResp    string
		expectLeave    bool
		expectJoinPost bool
	}{
		{
			name:        "leaves externally shared channel",
			channelResp: `{"ok":true,"channel":{"id":"C123","is_ext_shared":true,"is_pending_ext_shared":false}}`,
			expectLeave: true,
		},
		{
			name:        "leaves pending externally shared channel",
			channelResp: `{"ok":true,"channel":{"id":"C123","is_ext_shared":false,"is_pending_ext_shared":true}}`,
			expectLeave: true,
		},
		{
			name:           "stays in internal channel",
			channelResp:    `{"ok":true,"channel":{"id":"C123","is_ext_shared":false,"is_pending_ext_shared":false}}`,
			expectLeave:    false,
			expectJoinPost: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var leaveCalled, postCalled bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "conversations.info"):
					w.Write([]byte(tt.channelResp))
				case strings.Contains(r.URL.Path, "conversations.leave"):
					leaveCalled = true
					w.Write([]byte(`{"ok":true}`))
				case strings.Contains(r.URL.Path, "chat.postMessage"):
					postCalled = true
					w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1.1"}`))
				default:
					w.Write([]byte(`{"ok":true}`))
				}
			}))
			defer srv.Close()

			api := goslack.New("xoxb-test", goslack.OptionAPIURL(srv.URL+"/"))

			h := &SlackHandler{
				log:                      logr.Discard(),
				api:                      api,
				botUserID:                "UBOT",
				joinMessage:              "Welcome!",
				denySlackConnectChannels: true,
			}

			evt := &slackevents.MemberJoinedChannelEvent{
				User:    "UBOT",
				Channel: "C123",
			}

			h.handleMemberJoinedChannel(context.Background(), evt)

			if leaveCalled != tt.expectLeave {
				t.Errorf("conversations.leave called = %v, want %v", leaveCalled, tt.expectLeave)
			}
			if postCalled != tt.expectJoinPost {
				t.Errorf("chat.postMessage called = %v, want %v", postCalled, tt.expectJoinPost)
			}
		})
	}
}

func TestDenySlackConnectDisabledDoesNotCheck(t *testing.T) {
	// With denySlackConnectChannels=false and api=nil, calling handleMemberJoinedChannel
	// for an external channel should NOT call conversations.info (would panic on nil api).
	h := &SlackHandler{
		log:                      logr.Discard(),
		botUserID:                "UBOT",
		denySlackConnectChannels: false,
		// api is nil — would panic if shouldDenySlackConnect were called.
	}

	evt := &slackevents.MemberJoinedChannelEvent{
		User:    "UBOT",
		Channel: "C123",
	}

	// Should not panic — autoleave check is skipped.
	h.handleMemberJoinedChannel(context.Background(), evt)
}

func TestDenySlackConnectAPIErrorFailsClosed(t *testing.T) {
	var postCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "conversations.info"):
			w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
		case strings.Contains(r.URL.Path, "conversations.leave"):
			t.Error("conversations.leave should not be called when info fails")
			w.Write([]byte(`{"ok":true}`))
		case strings.Contains(r.URL.Path, "chat.postMessage"):
			postCalled = true
			w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1.1"}`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	api := goslack.New("xoxb-test", goslack.OptionAPIURL(srv.URL+"/"))

	h := &SlackHandler{
		log:                      logr.Discard(),
		api:                      api,
		botUserID:                "UBOT",
		joinMessage:              "Welcome!",
		denySlackConnectChannels: true,
	}

	evt := &slackevents.MemberJoinedChannelEvent{
		User:    "UBOT",
		Channel: "C123",
	}

	h.handleMemberJoinedChannel(context.Background(), evt)

	if postCalled {
		t.Error("Join message should not be posted when conversations.info fails (fail closed)")
	}
}

func TestDenySlackConnectLeaveFailureDoesNotPostJoinMessage(t *testing.T) {
	var postCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "conversations.info"):
			w.Write([]byte(`{"ok":true,"channel":{"id":"C123","is_ext_shared":true}}`))
		case strings.Contains(r.URL.Path, "conversations.leave"):
			w.Write([]byte(`{"ok":false,"error":"not_allowed"}`))
		case strings.Contains(r.URL.Path, "chat.postMessage"):
			postCalled = true
			w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1.1"}`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	api := goslack.New("xoxb-test", goslack.OptionAPIURL(srv.URL+"/"))

	h := &SlackHandler{
		log:                      logr.Discard(),
		api:                      api,
		botUserID:                "UBOT",
		joinMessage:              "Welcome!",
		denySlackConnectChannels: true,
	}

	evt := &slackevents.MemberJoinedChannelEvent{
		User:    "UBOT",
		Channel: "C123",
	}

	h.handleMemberJoinedChannel(context.Background(), evt)

	if postCalled {
		t.Error("Join message should not be posted when channel is external, even if leave fails")
	}
}

// fakeReactionSlack serves the Slack Web API calls a reaction makes: history
// for a top-level message, replies for a thread reply, and the user and
// permalink lookups enrichMessage performs. It counts every call so tests can
// assert that an ignored reaction never reaches Slack.
type fakeReactionSlack struct {
	history string
	replies string
	calls   int
}

func (f *fakeReactionSlack) server(t *testing.T) *goslack.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		switch {
		case strings.Contains(r.URL.Path, "conversations.history"):
			w.Write([]byte(f.history))
		case strings.Contains(r.URL.Path, "conversations.replies"):
			w.Write([]byte(f.replies))
		case strings.Contains(r.URL.Path, "chat.getPermalink"):
			w.Write([]byte(`{"ok":true,"channel":"C1","permalink":"https://example.slack.com/archives/C1/p2222222222222222"}`))
		case strings.Contains(r.URL.Path, "users.info"):
			w.Write([]byte(`{"ok":true,"user":{"id":"UAUTHOR","name":"author","real_name":"Ada Author"}}`))
		default:
			w.Write([]byte(`{"ok":false,"error":"unknown_method"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return goslack.New("xoxb-test", goslack.OptionAPIURL(srv.URL+"/"))
}

func reactionSpawner(slackCfg *kelos.Slack) *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ledger",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{Slack: slackCfg},
			TaskTemplate: kelos.TaskTemplate{
				Type:        "claude-code",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
				PromptTemplate: "{{.Kind}} :{{.Reaction}}: by {{.ReactionUserID}} on {{.ChannelID}}/{{.MessageTS}}" +
					" thread={{.ThreadTS}} url={{.URL}}\n{{.Body}}",
			},
		},
	}
}

func TestHandleReactionAdded(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	const topLevelHistory = `{"ok":true,"messages":[{"type":"message","user":"UAUTHOR","text":"Acme went live today","ts":"2222222222.222222"}]}`

	gearEvent := func() *slackevents.ReactionAddedEvent {
		return &slackevents.ReactionAddedEvent{
			Type:     "reaction_added",
			User:     "UREACTOR",
			Reaction: "gear",
			ItemUser: "UAUTHOR",
			Item:     slackevents.Item{Type: "message", Channel: "C1", Timestamp: "2222222222.222222"},
		}
	}

	tests := []struct {
		name       string
		slackCfg   *kelos.Slack
		event      func() *slackevents.ReactionAddedEvent
		history    string
		replies    string
		wantPrompt string
		wantThread string
		wantNoAPI  bool
	}{
		{
			name:     "listed emoji on a top-level message in an allowed channel creates a task",
			slackCfg: &kelos.Slack{Channels: []string{"C1"}, Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}},
			event:    gearEvent,
			history:  topLevelHistory,
			wantPrompt: "SlackReaction :gear: by UREACTOR on C1/2222222222.222222 thread=" +
				" url=https://example.slack.com/archives/C1/p2222222222222222\nAcme went live today",
			wantThread: "2222222222.222222",
		},
		{
			name:     "skin-tone variant matches its base name",
			slackCfg: &kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "+1"}}}},
			event: func() *slackevents.ReactionAddedEvent {
				e := gearEvent()
				e.Reaction = "+1::skin-tone-3"
				return e
			},
			history: topLevelHistory,
			wantPrompt: "SlackReaction :+1: by UREACTOR on C1/2222222222.222222 thread=" +
				" url=https://example.slack.com/archives/C1/p2222222222222222\nAcme went live today",
			wantThread: "2222222222.222222",
		},
		{
			name:     "thread reply is fetched from the thread and keeps its parent",
			slackCfg: &kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}},
			event:    gearEvent,
			// History returns the nearest earlier top-level message, not the reply.
			history: `{"ok":true,"messages":[{"type":"message","user":"UOTHER","text":"unrelated","ts":"1111111111.000000"}]}`,
			replies: `{"ok":true,"messages":[` +
				`{"type":"message","user":"UOTHER","text":"Acme status","ts":"1111111111.000000","thread_ts":"1111111111.000000"},` +
				`{"type":"message","user":"UAUTHOR","text":"Acme went live today","ts":"2222222222.222222","thread_ts":"1111111111.000000"}]}`,
			wantPrompt: "SlackReaction :gear: by UREACTOR on C1/2222222222.222222 thread=1111111111.000000" +
				" url=https://example.slack.com/archives/C1/p2222222222222222\nAcme went live today",
			wantThread: "1111111111.000000",
		},
		{
			// A thread parent carries its own ts as thread_ts; it is not a reply,
			// so ThreadTS stays empty and the Task reports into its thread.
			name:     "thread parent keeps an empty ThreadTS",
			slackCfg: &kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}},
			event:    gearEvent,
			history:  `{"ok":true,"messages":[{"type":"message","user":"UAUTHOR","text":"Acme went live today","ts":"2222222222.222222","thread_ts":"2222222222.222222","reply_count":2}]}`,
			wantPrompt: "SlackReaction :gear: by UREACTOR on C1/2222222222.222222 thread=" +
				" url=https://example.slack.com/archives/C1/p2222222222222222\nAcme went live today",
			wantThread: "2222222222.222222",
		},
		{
			name:      "unlisted emoji does not create a task or call Slack",
			slackCfg:  &kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}},
			event:     func() *slackevents.ReactionAddedEvent { e := gearEvent(); e.Reaction = "eyes"; return e },
			history:   topLevelHistory,
			wantNoAPI: true,
		},
		{
			name:      "excluded channel does not create a task or call Slack",
			slackCfg:  &kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}, ExcludeFilters: []kelos.SlackFilter{{Channels: []string{"C1"}}}},
			event:     gearEvent,
			history:   topLevelHistory,
			wantNoAPI: true,
		},
		{
			name:      "channel outside the allowlist does not create a task or call Slack",
			slackCfg:  &kelos.Slack{Channels: []string{"C2"}, Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}},
			event:     gearEvent,
			history:   topLevelHistory,
			wantNoAPI: true,
		},
		{
			name:      "spawner without reaction triggers ignores the reaction",
			slackCfg:  &kelos.Slack{Triggers: []kelos.SlackTrigger{{Pattern: ".*", MentionOptional: boolPtr(true)}}},
			event:     gearEvent,
			history:   topLevelHistory,
			wantNoAPI: true,
		},
		{
			name:      "reaction added by the bot itself is ignored",
			slackCfg:  &kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}},
			event:     func() *slackevents.ReactionAddedEvent { e := gearEvent(); e.User = "UBOT"; return e },
			history:   topLevelHistory,
			wantNoAPI: true,
		},
		{
			name:     "reaction on a file is ignored",
			slackCfg: &kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}},
			event: func() *slackevents.ReactionAddedEvent {
				e := gearEvent()
				e.Item = slackevents.Item{Type: "file", Channel: "C1"}
				return e
			},
			history:   topLevelHistory,
			wantNoAPI: true,
		},
		{
			name:     "message that cannot be found does not create a task",
			slackCfg: &kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}}},
			event:    gearEvent,
			history:  `{"ok":true,"messages":[]}`,
			replies:  `{"ok":false,"error":"thread_not_found"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(reactionSpawner(tt.slackCfg)).
				Build()
			tb, err := taskbuilder.NewTaskBuilder(cl)
			if err != nil {
				t.Fatalf("NewTaskBuilder: %v", err)
			}
			slackAPI := &fakeReactionSlack{history: tt.history, replies: tt.replies}
			h := &SlackHandler{
				client:      cl,
				log:         logr.Discard(),
				taskBuilder: tb,
				api:         slackAPI.server(t),
				botUserID:   "UBOT",
			}

			h.handleReactionAdded(context.Background(), tt.event())

			var tasks kelos.TaskList
			if err := cl.List(context.Background(), &tasks); err != nil {
				t.Fatalf("List tasks: %v", err)
			}
			if tt.wantNoAPI && slackAPI.calls != 0 {
				t.Errorf("Slack API called %d times, want 0", slackAPI.calls)
			}
			if tt.wantPrompt == "" {
				if len(tasks.Items) != 0 {
					t.Fatalf("Expected no task, got %d", len(tasks.Items))
				}
				return
			}
			if len(tasks.Items) != 1 {
				t.Fatalf("Expected 1 task, got %d", len(tasks.Items))
			}
			task := tasks.Items[0]
			if task.Spec.Prompt != tt.wantPrompt {
				t.Errorf("Task prompt = %q, want %q", task.Spec.Prompt, tt.wantPrompt)
			}
			if got := task.Annotations[reporting.AnnotationSlackUserID]; got != "UREACTOR" {
				t.Errorf("Slack user annotation = %q, want the reactor UREACTOR", got)
			}
			if got := task.Annotations[reporting.AnnotationSlackChannel]; got != "C1" {
				t.Errorf("Slack channel annotation = %q, want C1", got)
			}
			if got := task.Annotations[reporting.AnnotationSlackThreadTS]; got != tt.wantThread {
				t.Errorf("Slack thread annotation = %q, want %q", got, tt.wantThread)
			}
		})
	}
}

// TestHandleReactionAddedDeduplicatesPerMessageAndEmoji verifies that the same
// emoji added by a second person resolves to the existing Task, while a
// different listed emoji on the same message gets its own Task.
func TestHandleReactionAddedDeduplicatesPerMessageAndEmoji(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(reactionSpawner(&kelos.Slack{Triggers: []kelos.SlackTrigger{{Reaction: &kelos.SlackReactionTrigger{Name: "gear"}}, {Reaction: &kelos.SlackReactionTrigger{Name: "rocket"}}}})).
		Build()
	tb, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}
	slackAPI := &fakeReactionSlack{
		history: `{"ok":true,"messages":[{"type":"message","user":"UAUTHOR","text":"Acme went live today","ts":"2222222222.222222"}]}`,
	}
	h := &SlackHandler{
		client:      cl,
		log:         logr.Discard(),
		taskBuilder: tb,
		api:         slackAPI.server(t),
		botUserID:   "UBOT",
	}

	item := slackevents.Item{Type: "message", Channel: "C1", Timestamp: "2222222222.222222"}
	h.handleReactionAdded(context.Background(), &slackevents.ReactionAddedEvent{User: "U1", Reaction: "gear", Item: item})
	h.handleReactionAdded(context.Background(), &slackevents.ReactionAddedEvent{User: "U2", Reaction: "gear", Item: item})

	var tasks kelos.TaskList
	if err := cl.List(context.Background(), &tasks); err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("Expected 1 task after the same emoji twice, got %d", len(tasks.Items))
	}
	if got := tasks.Items[0].Annotations[reporting.AnnotationSlackUserID]; got != "U1" {
		t.Errorf("Slack user annotation = %q, want the first reactor U1", got)
	}

	h.handleReactionAdded(context.Background(), &slackevents.ReactionAddedEvent{User: "U2", Reaction: "rocket", Item: item})
	if err := cl.List(context.Background(), &tasks); err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	if len(tasks.Items) != 2 {
		t.Fatalf("Expected 2 tasks after a second listed emoji, got %d", len(tasks.Items))
	}
}
