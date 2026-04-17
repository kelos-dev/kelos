package slack

import (
	"context"
	"os"
	"path/filepath"
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
				Slack: &v1alpha1.Slack{
					MentionUserIDs: []string{"UBOT"},
					TriggerCommand: "/solve",
				},
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
			name: "top-level message uses trigger-processed body",
			msg: &SlackMessageData{
				UserID:    "U1",
				ChannelID: "C1",
				Text:      "<@UBOT> /solve fix the bug",
				Body:      "<@UBOT> /solve fix the bug",
				Timestamp: "1111111111.111111",
			},
			// TriggerCommand="/solve" strips the prefix, leaving just "fix the bug"
			wantBody: "fix the bug",
		},
		{
			name: "thread reply with context preserves thread body",
			msg: &SlackMessageData{
				UserID:           "U1",
				ChannelID:        "C1",
				Text:             "<@UBOT> /solve can you take a look",
				Body:             "Slack thread conversation:\n\nUser: original question\n\nUser: <@UBOT> /solve can you take a look\n",
				ThreadTS:         "1111111111.000000",
				Timestamp:        "2222222222.222222",
				HasThreadContext: true,
			},
			// HasThreadContext=true means the thread body is preserved as-is
			wantBody: "Slack thread conversation:\n\nUser: original question\n\nUser: <@UBOT> /solve can you take a look\n",
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

func TestReadJoinMessage(t *testing.T) {
	tests := []struct {
		name        string
		fileContent string
		noFile      bool
		wantMsg     string
		wantErr     bool
	}{
		{
			name:        "reads and trims message from file",
			fileContent: "Hello! I'm Kelos bot. Mention me to get started.\n",
			wantMsg:     "Hello! I'm Kelos bot. Mention me to get started.",
		},
		{
			name:    "empty file path returns empty string",
			noFile:  true,
			wantMsg: "",
		},
		{
			name:        "empty file content returns empty string",
			fileContent: "   \n",
			wantMsg:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &SlackHandler{log: logr.Discard()}

			if !tt.noFile {
				dir := t.TempDir()
				path := filepath.Join(dir, "join-message.txt")
				if err := os.WriteFile(path, []byte(tt.fileContent), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				h.joinMessageFile = path
			}

			got, err := h.readJoinMessage()
			if (err != nil) != tt.wantErr {
				t.Fatalf("readJoinMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.wantMsg {
				t.Errorf("readJoinMessage() = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}

func TestReadJoinMessageMissingFile(t *testing.T) {
	h := &SlackHandler{
		log:             logr.Discard(),
		joinMessageFile: "/nonexistent/path/join-message.txt",
	}

	_, err := h.readJoinMessage()
	if err == nil {
		t.Fatal("Expected error for missing file, got nil")
	}
}

func TestHandleMemberJoinedChannelIgnoresOtherUsers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "join-message.txt")
	if err := os.WriteFile(path, []byte("Welcome!"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := &SlackHandler{
		log:             logr.Discard(),
		botUserID:       "UBOT",
		joinMessageFile: path,
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
