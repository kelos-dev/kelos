package cli

import (
	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

const sessionPromptRequestPrefix = "prompts-"

func (m *sessionTUIModel) previousInput() tea.Cmd {
	if m.promptRequestID != "" || m.connectionStatus != "" {
		return nil
	}
	if m.historyAt == -1 {
		m.draft = m.input.Value()
		m.history = nil
		m.promptCursor = ""
		return m.requestPromptHistory()
	}
	if m.historyAt > 0 {
		m.historyAt--
		m.input.SetValue(m.history[m.historyAt])
		m.input.CursorEnd()
		m.resizeComposer()
	} else if m.promptCursor != "" {
		return m.requestPromptHistory()
	}
	return nil
}

func (m *sessionTUIModel) requestPromptHistory() tea.Cmd {
	m.promptRequestID = sessionPromptRequestPrefix + string(uuid.NewUUID())
	if err := m.requests.Encode(sessionruntime.ClientRequest{Type: "prompts", RequestID: m.promptRequestID, HistoryCursor: m.promptCursor}); err != nil {
		m.err = err
		return m.quit()
	}
	return nil
}

func (m *sessionTUIModel) receivePromptHistory(event sessionruntime.Event) sessionTUICommands {
	if m.promptRequestID == "" || event.RequestID != m.promptRequestID {
		return sessionTUICommands{}
	}
	m.promptRequestID = ""
	if event.Type == sessionruntime.EventError {
		m.resetPromptHistory()
		m.appendBlock(sessionTUIBlockError, "Could not load prompt history: "+event.Text)
		command := m.queueReadyBlocks()
		m.refreshActiveView()
		return sessionTUICommands{history: command}
	}
	m.promptCursor = event.HistoryCursor
	var prompts []string
	for _, prompt := range event.Prompts {
		if prompt.Text != "" {
			prompts = append(prompts, prompt.Text)
		}
	}
	if len(prompts) == 0 {
		if m.promptCursor != "" {
			return sessionTUICommands{ui: m.requestPromptHistory()}
		}
		return sessionTUICommands{}
	}
	m.history = append(prompts, m.history...)
	m.historyAt = len(prompts) - 1
	m.input.SetValue(m.history[m.historyAt])
	m.input.CursorEnd()
	m.resizeComposer()
	return sessionTUICommands{}
}

func (m *sessionTUIModel) resetPromptHistory() {
	m.promptRequestID = ""
	m.promptCursor = ""
	m.history = nil
	if m.historyAt >= 0 {
		m.input.SetValue(m.draft)
		m.resizeComposer()
	}
	m.historyAt = -1
	m.draft = ""
}
