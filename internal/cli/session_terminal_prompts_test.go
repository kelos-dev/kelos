package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

func readSessionTUIPromptRequest(t *testing.T, requests *bytes.Buffer) sessionruntime.ClientRequest {
	t.Helper()
	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "prompts" || request.RequestID == "" {
		t.Fatalf("prompt history request = %#v", request)
	}
	return request
}

func TestSessionTUIPromptHistoryPagesMultilinePrompts(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.connectionStatus = ""
	model.input.SetValue("unsent draft")
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request := readSessionTUIPromptRequest(t, requests)
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if requests.Len() != 0 {
		t.Fatal("Up sent a duplicate request while prompts were loading")
	}
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, HistoryCursor: "earlier", Prompts: []sessionruntime.Prompt{
		{ID: 3, Text: "third\nprompt"},
		{ID: 4, Text: "fourth\nprompt"},
	}})
	if got := model.input.Value(); got != "fourth\nprompt" {
		t.Fatalf("latest prompt = %q", got)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := model.input.Value(); got != "third\nprompt" {
		t.Fatalf("previous multiline prompt = %q", got)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request = readSessionTUIPromptRequest(t, requests)
	if request.HistoryCursor != "earlier" {
		t.Fatalf("earlier prompt cursor = %q", request.HistoryCursor)
	}
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, Prompts: []sessionruntime.Prompt{
		{ID: 1, Text: "first\nprompt"},
		{ID: 2, Text: "second\nprompt"},
	}})
	if got := model.input.Value(); got != "second\nprompt" {
		t.Fatalf("earlier page prompt = %q", got)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := model.input.Value(); got != "first\nprompt" || requests.Len() != 0 {
		t.Fatalf("oldest prompt = %q, extra requests = %q", got, requests.String())
	}
	for _, want := range []string{"second\nprompt", "third\nprompt", "fourth\nprompt", "unsent draft"} {
		model.Update(tea.KeyMsg{Type: tea.KeyDown})
		if got := model.input.Value(); got != want {
			t.Fatalf("Down recalled %q, want %q", got, want)
		}
	}
	if len(model.blocks) != 0 || requests.Len() != 0 {
		t.Fatal("browsing prompts changed the conversation")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request = readSessionTUIPromptRequest(t, requests)
	if request.HistoryCursor != "" {
		t.Fatalf("starting another browse did not refresh the latest prompts: %#v", request)
	}
}

func TestSessionTUIPromptRecallSubmitsTextWithoutEditingPendingWork(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.connectionStatus = ""
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "pending", Text: "pending work", Revision: 2})
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request := readSessionTUIPromptRequest(t, requests)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, Prompts: []sessionruntime.Prompt{
		{ID: 1, Text: "earlier request", Attachments: []sessionruntime.Attachment{{ID: "file-1", Name: "notes.txt"}}},
	}})
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	var submitted sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.Type != "message" || submitted.Text != "earlier request" || submitted.TurnID != "" || len(submitted.AttachmentIDs) != 0 {
		t.Fatalf("recalled submission = %#v", submitted)
	}
}

func TestSessionTUIPromptHistoryPreservesMultilineEditing(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.connectionStatus = ""
	model.input.Focus()
	model.input.SetValue("first line\nsecond line")
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if model.input.Line() != 0 || requests.Len() != 0 {
		t.Fatal("Up inside a multiline draft did not move the cursor")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request := readSessionTUIPromptRequest(t, requests)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, Prompts: []sessionruntime.Prompt{{ID: 1, Text: "saved\nprompt"}}})
	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := model.input.Value(); got != "first line\nsecond line" {
		t.Fatalf("restored multiline draft = %q", got)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request = readSessionTUIPromptRequest(t, requests)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, Prompts: []sessionruntime.Prompt{{ID: 1, Text: "saved\nprompt"}}})
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" edit")})
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := model.input.Value(); got != "saved\nprompt edit" || model.input.Line() != 0 || requests.Len() != 0 {
		t.Fatalf("editing recalled prompt = %q, line = %d, requests = %q", got, model.input.Line(), requests.String())
	}
}

func TestSessionTUIPromptHistoryIgnoresCancelledReplies(t *testing.T) {
	for _, action := range []string{"typing", "cursor", "down", "submit", "reset", "reconnect", "file drop", "interrupt", "escape", "history"} {
		t.Run(action, func(t *testing.T) {
			model, requests := newSessionTUITestModel()
			model.ready = true
			model.connectionStatus = ""
			model.turnActive = true
			model.input.Focus()
			model.input.SetValue("draft")
			if action == "history" {
				model.input.SetValue("")
				model.historyCursor = "earlier-transcript"
			}
			model.Update(tea.KeyMsg{Type: tea.KeyUp})
			request := readSessionTUIPromptRequest(t, requests)
			switch action {
			case "typing":
				model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" edited")})
			case "cursor":
				model.Update(tea.KeyMsg{Type: tea.KeyLeft})
			case "down":
				model.Update(tea.KeyMsg{Type: tea.KeyDown})
			case "submit":
				model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			case "reset":
				model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, Reset: true})
			case "reconnect":
				model.applyEvent(sessionruntime.Event{Type: sessionTerminalEventDiagnostic, Status: sessionTerminalStatusReconnecting})
			case "file drop":
				path := filepath.Join(t.TempDir(), "notes.txt")
				if err := os.WriteFile(path, []byte("notes"), 0600); err != nil {
					t.Fatal(err)
				}
				model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path), Paste: true})
			case "interrupt":
				model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			case "escape":
				model.Update(tea.KeyMsg{Type: tea.KeyEsc})
			case "history":
				model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
			}
			want := model.input.Value()
			blocks, interrupting := len(model.blocks), model.turnInterrupting
			model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, Prompts: []sessionruntime.Prompt{{ID: 1, Text: "stale prompt"}}})
			model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventError, RequestID: request.RequestID, Status: "rejected", Text: "expired cursor"})
			if got := model.input.Value(); got != want {
				t.Fatalf("delayed prompt replaced composer with %q, want %q", got, want)
			}
			if len(model.blocks) != blocks || model.turnInterrupting != interrupting {
				t.Fatal("cancelled prompt error changed the transcript or interrupt state")
			}
		})
	}
}

func TestSessionTUIPromptHistoryPreservesDraftOnCursorMovement(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyLeft, tea.KeyRight, tea.KeyHome, tea.KeyEnd} {
		t.Run(tea.KeyMsg{Type: key}.String(), func(t *testing.T) {
			model, requests := newSessionTUITestModel()
			model.ready = true
			model.connectionStatus = ""
			model.input.Focus()
			model.input.SetValue("unsent draft")
			model.Update(tea.KeyMsg{Type: tea.KeyUp})
			request := readSessionTUIPromptRequest(t, requests)
			model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, HistoryCursor: "earlier", Prompts: []sessionruntime.Prompt{{ID: 1, Text: "recalled prompt"}}})
			model.Update(tea.KeyMsg{Type: tea.KeyUp})
			request = readSessionTUIPromptRequest(t, requests)
			model.Update(tea.KeyMsg{Type: key})
			model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, Prompts: []sessionruntime.Prompt{{ID: 0, Text: "delayed prompt"}}})
			if model.input.Value() != "recalled prompt" {
				t.Fatal("cursor movement allowed a delayed prompt to replace the composer")
			}
			model.Update(tea.KeyMsg{Type: tea.KeyDown})
			if got := model.input.Value(); got != "unsent draft" {
				t.Fatalf("Down restored %q, want unsent draft", got)
			}
		})
	}
}

func TestSessionTUIPromptHistoryRestartsAfterReconnectOrPageError(t *testing.T) {
	for _, action := range []string{"reconnect", "page error"} {
		t.Run(action, func(t *testing.T) {
			model, requests := newSessionTUITestModel()
			model.ready = true
			model.connectionStatus = ""
			model.input.SetValue("unsent draft")
			model.Update(tea.KeyMsg{Type: tea.KeyUp})
			request := readSessionTUIPromptRequest(t, requests)
			model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, HistoryCursor: "earlier", Prompts: []sessionruntime.Prompt{{ID: 1, Text: "recalled prompt"}}})
			if action == "reconnect" {
				model.applyEvent(sessionruntime.Event{Type: sessionTerminalEventDiagnostic, Status: sessionTerminalStatusReconnecting})
				model.Update(tea.KeyMsg{Type: tea.KeyUp})
				if requests.Len() != 0 {
					t.Fatal("prompt request was queued during reconnect")
				}
				model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
			} else {
				model.Update(tea.KeyMsg{Type: tea.KeyUp})
				request = readSessionTUIPromptRequest(t, requests)
				model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventError, RequestID: request.RequestID, Text: "expired cursor"})
			}
			if model.input.Value() != "unsent draft" {
				t.Fatal("prompt history did not restore the draft")
			}
			model.Update(tea.KeyMsg{Type: tea.KeyUp})
			request = readSessionTUIPromptRequest(t, requests)
			if request.HistoryCursor != "" {
				t.Fatalf("prompt history reused cursor %q", request.HistoryCursor)
			}
		})
	}
}

func TestSessionTUIPromptHistoryErrorAndResetPreserveDraft(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.connectionStatus = ""
	model.turnActive = true
	model.activeTurnID = "working"
	model.turnInterrupting = true
	model.input.SetValue("draft")
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request := readSessionTUIPromptRequest(t, requests)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventError, RequestID: request.RequestID, Status: "rejected", Text: "unavailable"})
	if model.input.Value() != "draft" || !model.turnActive || model.activeTurnID != "working" || !model.turnInterrupting {
		t.Fatal("prompt lookup error changed the draft or active turn")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request = readSessionTUIPromptRequest(t, requests)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, HistoryCursor: "older", Prompts: []sessionruntime.Prompt{{ID: 1, Text: "saved prompt"}}})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, Reset: true})
	if model.input.Value() != "draft" {
		t.Fatalf("reset did not restore the draft: %q", model.input.Value())
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request = readSessionTUIPromptRequest(t, requests)
	if request.HistoryCursor != "" {
		t.Fatalf("reset retained a prompt cursor: %q", request.HistoryCursor)
	}
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID})
	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if model.input.Value() != "draft" {
		t.Fatal("prompt browsing recalled history from before the reset")
	}
}

func TestSessionTUIPromptHistorySkipsAttachmentOnlyPages(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.connectionStatus = ""
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request := readSessionTUIPromptRequest(t, requests)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, HistoryCursor: "earlier", Prompts: []sessionruntime.Prompt{{ID: 2}}})
	request = readSessionTUIPromptRequest(t, requests)
	if request.HistoryCursor != "earlier" {
		t.Fatalf("attachment-only page did not advance: %#v", request)
	}
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, Prompts: []sessionruntime.Prompt{{ID: 1, Text: "earlier text"}}})
	if got := model.input.Value(); got != "earlier text" {
		t.Fatalf("recalled prompt = %q", got)
	}
}
