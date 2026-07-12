package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

func TestSessionTerminalReconnectsToReplacementPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	var output bytes.Buffer
	var stderr bytes.Buffer
	firstConnected := make(chan struct{})
	secondConnected := make(chan struct{})
	secondMessage := make(chan struct{})
	firstRequestID := make(chan string, 1)
	var sessionReads atomic.Int32
	dependencies := sessionReconnectDependencies{
		getSession: func(context.Context, string, string) (*kelos.Session, error) {
			switch sessionReads.Add(1) {
			case 1:
				return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "session-pod-1"}}, nil
			case 2:
				return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseFailed, Message: "Session runtime is restarting"}}, nil
			default:
				return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "session-pod-2"}}, nil
			}
		},
		openStream: func(_ context.Context, _ string, podName string) (*sessionPodStream, error) {
			switch podName {
			case "session-pod-1":
				return fakeSessionPodStream(t, func(decoder *json.Decoder, encoder *json.Encoder) {
					var subscribe sessionruntime.ClientRequest
					if err := decoder.Decode(&subscribe); err != nil {
						t.Error(err)
						return
					}
					if subscribe.Type != "subscribe" || subscribe.Since != 0 {
						t.Errorf("first subscribe = %#v", subscribe)
					}
					_ = encoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
					close(firstConnected)
					var request sessionruntime.ClientRequest
					if err := decoder.Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if request.Type != "message" || request.Text != "before" {
						t.Errorf("first message = %#v", request)
					}
					if request.RequestID == "" {
						t.Error("first message has no request ID")
					}
					firstRequestID <- request.RequestID
				}), nil
			case "session-pod-2":
				return fakeSessionPodStream(t, func(decoder *json.Decoder, encoder *json.Encoder) {
					var subscribe sessionruntime.ClientRequest
					if err := decoder.Decode(&subscribe); err != nil {
						t.Error(err)
						return
					}
					if subscribe.Type != "subscribe" || subscribe.Since != 0 {
						t.Errorf("replacement subscribe = %#v", subscribe)
					}
					_ = encoder.Encode(sessionruntime.Event{ID: 1, Type: sessionruntime.EventRuntimeRecovered, Text: "Session runtime restarted"})
					_ = encoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
					close(secondConnected)
					var request sessionruntime.ClientRequest
					if err := decoder.Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if request.Type != "message" || request.Text != "after" {
						t.Errorf("replacement message = %#v", request)
					}
					if request.RequestID == "" || request.RequestID == <-firstRequestID {
						t.Errorf("replacement request ID = %q", request.RequestID)
					}
					_ = encoder.Encode(sessionruntime.Event{ID: 2, Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-2", Text: "after"})
					_ = encoder.Encode(sessionruntime.Event{ID: 3, Type: sessionruntime.EventAssistantMessage, TurnID: "turn-2", Text: "recovered"})
					_ = encoder.Encode(sessionruntime.Event{ID: 4, Type: sessionruntime.EventTurnCompleted, TurnID: "turn-2", Status: "completed"})
					close(secondMessage)
					<-ctx.Done()
				}), nil
			default:
				t.Fatalf("unexpected Pod %q", podName)
				return nil, nil
			}
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- connectSessionWithDependencies(ctx, "default", "chat", input, &output, &stderr, false, dependencies)
	}()
	select {
	case <-firstConnected:
	case <-ctx.Done():
		t.Fatal("first Session stream did not connect")
	}
	_, _ = io.WriteString(inputWriter, "before\n")
	select {
	case <-secondConnected:
	case <-ctx.Done():
		t.Fatal("replacement Session stream did not connect")
	}
	_, _ = io.WriteString(inputWriter, "after\n")
	select {
	case <-secondMessage:
	case <-ctx.Done():
		t.Fatal("replacement Session did not receive input")
	}
	_, _ = io.WriteString(inputWriter, "/quit\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("terminal did not exit")
	}
	if got := output.String(); !strings.Contains(got, "Session runtime restarted") || !strings.Contains(got, "agent › recovered") {
		t.Fatalf("terminal output = %q", got)
	}
	if got := stderr.String(); !strings.Contains(got, "connection lost") || !strings.Contains(got, "Waiting for Session \"chat\" to recover") || !strings.Contains(got, "Reconnected") || !strings.Contains(got, "delivery was not confirmed") {
		t.Fatalf("terminal stderr = %q", got)
	}
}

func fakeSessionPodStream(t *testing.T, handler func(*json.Decoder, *json.Encoder)) *sessionPodStream {
	t.Helper()
	runtimeRequests, clientRequests := io.Pipe()
	clientEvents, runtimeEvents := io.Pipe()
	streamCtx, cancel := context.WithCancel(t.Context())
	go func() {
		handler(json.NewDecoder(runtimeRequests), json.NewEncoder(runtimeEvents))
		_ = runtimeEvents.Close()
		_ = runtimeRequests.Close()
	}()
	go func() {
		<-streamCtx.Done()
		_ = runtimeRequests.Close()
		_ = runtimeEvents.Close()
	}()
	return &sessionPodStream{requests: clientRequests, events: clientEvents, cancel: cancel}
}
