package e2e

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/onsi/gomega"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

func TestSessionEventHistoryPreservesErrors(t *testing.T) {
	gomega.RegisterTestingT(t)
	historicalError := sessionruntime.Event{
		ID: 1, Type: sessionruntime.EventError, TurnID: "turn-1", Text: "Session provider stopped", Status: "failed",
	}
	liveReply := sessionruntime.Event{
		ID: 4, Type: sessionruntime.EventAssistantDelta, TurnID: "turn-2", Text: "state preserved",
	}
	connection := sessionEventConnection(t, []sessionruntime.Event{
		historicalError,
		{ID: 2, Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "failed"},
		{ID: 3, Type: sessionruntime.EventRuntimeRecovered},
		{Type: sessionruntime.EventHistoryEnd},
		liveReply,
	})

	var history []sessionruntime.Event
	for {
		event := readSessionEvent(connection)
		if event.Type == sessionruntime.EventHistoryEnd {
			break
		}
		history = append(history, event)
	}
	if len(history) != 3 || !reflect.DeepEqual(history[0], historicalError) {
		t.Fatalf("Replayed history = %#v, want the retained error and recovery events", history)
	}
	if history[1].Type != sessionruntime.EventTurnCompleted || history[1].Status != "failed" || history[2].Type != sessionruntime.EventRuntimeRecovered {
		t.Fatalf("Recovery events = %#v, want a failed turn and runtime recovery", history[1:])
	}
	got := waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
		return event.Type == sessionruntime.EventAssistantDelta
	})
	if !reflect.DeepEqual(got, liveReply) {
		t.Fatalf("Live reply = %#v, want %#v", got, liveReply)
	}
}

func TestSessionRecoveryWaitValidatesTerminatedTurn(t *testing.T) {
	for _, tt := range []struct {
		name      string
		errorText string
		errorTurn string
		status    string
		wantError string
	}{
		{name: "interrupted turn", status: "interrupted"},
		{name: "provider stopped during Pod deletion", errorText: "Session provider stopped", errorTurn: "turn-1", status: "failed"},
		{name: "unrelated failure", errorText: "Invalid provider output", errorTurn: "turn-1", status: "failed", wantError: "Terminated turn turn-1 failed without a provider-stop error"},
		{name: "missing provider-stop error", status: "failed", wantError: "Terminated turn turn-1 failed without a provider-stop error"},
		{name: "provider-stop error from another turn", errorText: "Session provider stopped", errorTurn: "turn-2", status: "failed", wantError: "Terminated turn turn-1 failed without a provider-stop error"},
		{name: "unexpected successful completion", status: "completed", wantError: "Unexpected completion for terminated turn turn-1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gomega.RegisterTestingT(t)
			var events []sessionruntime.Event
			if tt.errorText != "" {
				events = append(events, sessionruntime.Event{Type: sessionruntime.EventError, TurnID: tt.errorTurn, Text: tt.errorText, Status: "failed"})
			}
			completion := sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: tt.status}
			recovery := sessionruntime.Event{Type: sessionruntime.EventRuntimeRecovered}
			if tt.status == "interrupted" {
				events = append(events, recovery, completion)
			} else {
				events = append(events, completion, recovery)
			}
			events = append(events, sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
			err := gomega.InterceptGomegaFailure(func() {
				waitForSessionRecovery(sessionEventConnection(t, events), "turn-1")
			})
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Recovery failure = %v, want success", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Recovery failure = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestSessionHistoryWaitAllowsReplayedErrors(t *testing.T) {
	gomega.RegisterTestingT(t)
	liveReply := sessionruntime.Event{
		ID: 2, Type: sessionruntime.EventAssistantDelta, TurnID: "turn-2", Text: "state preserved",
	}
	connection := sessionEventConnection(t, []sessionruntime.Event{
		{ID: 1, Type: sessionruntime.EventError, Text: "Session provider stopped", Status: "failed"},
		{Type: sessionruntime.EventHistoryEnd},
		liveReply,
	})

	waitForSessionHistory(connection)
	got := waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
		return event.Type == sessionruntime.EventAssistantDelta
	})
	if !reflect.DeepEqual(got, liveReply) {
		t.Fatalf("Live reply = %#v, want %#v", got, liveReply)
	}
}

func TestSessionEventWaitRejectsLiveErrors(t *testing.T) {
	gomega.RegisterTestingT(t)
	connection := sessionEventConnection(t, []sessionruntime.Event{
		{ID: 1, Type: sessionruntime.EventError, Text: "Session provider stopped", Status: "failed"},
	})

	matched := false
	err := gomega.InterceptGomegaFailure(func() {
		waitForSessionEvent(connection, func(sessionruntime.Event) bool {
			matched = true
			return true
		})
	})
	if err == nil || !strings.Contains(err.Error(), "Session runtime error: Session provider stopped (failed)") {
		t.Fatalf("Live error failure = %v, want a Session runtime error", err)
	}
	if matched {
		t.Fatal("Live error reached the event matcher")
	}
}

func sessionEventConnection(t *testing.T, events []sessionruntime.Event) *websocket.Conn {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			t.Errorf("Upgrading Session event connection: %v", err)
			return
		}
		defer connection.Close()
		for _, event := range events {
			if err := connection.WriteJSON(event); err != nil {
				// Rejection tests can close the connection before consuming every event.
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}
