package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"github.com/muesli/termenv"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const (
	sessionTUIDefaultWidth           = 80
	sessionTUIDefaultHeight          = 24
	sessionTUIComposerMinHeight      = 3
	sessionTUIComposerMaxVisibleRows = 8
	sessionTUIComposerGap            = 1
	sessionTUIComposerPrompt         = "> "
	sessionTUIStatusBarHeight        = 1
	sessionTUIPendingMaxHeight       = 8
	sessionTUIIndent                 = 2
	sessionTUIToolOutputMaxLines     = 5
	sessionTUIFrameInterval          = time.Second / 30
	sessionTUIProgressInterval       = time.Second
	sessionTUIReflowDelay            = 75 * time.Millisecond
)

// sessionTUIClearHistory resets the scroll region, screen, and scrollback before history is replayed.
const sessionTUIClearHistory = "\x1b[r\x1b[0m\x1b[H\x1b[2J\x1b[3J\x1b[H"

type sessionTUIBlockKind int

const (
	sessionTUIBlockUser sessionTUIBlockKind = iota
	sessionTUIBlockAssistant
	sessionTUIBlockTool
	sessionTUIBlockToolOutput
	sessionTUIBlockToolStatus
	sessionTUIBlockNotice
	sessionTUIBlockWarning
	sessionTUIBlockError
	sessionTUIBlockInput
	sessionTUIBlockDiff
	sessionTUIBlockTurnSeparator
)

type sessionTUIBlock struct {
	kind     sessionTUIBlockKind
	text     string
	stream   *strings.Builder
	rendered string
	dirty    bool
	toolID   string
	toolName string
}

type sessionTUIStatusSegment struct {
	text     string
	priority int
}

type sessionTUIEventResult struct {
	event sessionruntime.Event
	err   error
}

type sessionTUIRefreshMsg struct{}

type sessionTUIProgressMsg struct{}

type sessionTUIReflowMsg struct {
	generation int
}

type sessionTUIHistoryPrintedMsg struct {
	id uint64
}

type sessionTUITerminateMsg struct{}

type sessionTUIHistoryWrite struct {
	id               uint64
	rendered         string
	commitEnd        int
	reflowGeneration int
}

type sessionTUICommands struct {
	ui      tea.Cmd
	history tea.Cmd
}

type sessionTUIStyles struct {
	base         lipgloss.Style
	user         lipgloss.Style
	muted        lipgloss.Style
	warning      lipgloss.Style
	error        lipgloss.Style
	accent       lipgloss.Style
	inputHeading lipgloss.Style
	tool         lipgloss.Style
	success      lipgloss.Style
	failure      lipgloss.Style
	pending      lipgloss.Style
	diffMetadata lipgloss.Style
	diffHeader   lipgloss.Style
	diffAdded    lipgloss.Style
	diffRemoved  lipgloss.Style
}

type sessionTUIModel struct {
	events             *json.Decoder
	requests           *json.Encoder
	input              textarea.Model
	styles             sessionTUIStyles
	blocks             []sessionTUIBlock
	pendingTurnID      string
	pendingTurnText    string
	pendingTurnInput   string
	pendingRevision    int64
	pendingEditTurnID  string
	pendingEditRev     int64
	toolNames          map[string]string
	toolOutputAt       map[string]int
	width              int
	height             int
	ready              bool
	replayingHistory   bool
	recoveryActive     bool
	turnActive         bool
	streamingAt        int
	connectionStatus   string
	connectionStarted  time.Time
	activeTurnID       string
	activeTurnStarted  time.Time
	waitingForInput    bool
	turnInterrupting   bool
	progressScheduled  bool
	history            []string
	historyAt          int
	draft              string
	promptCursor       string
	promptRequestID    string
	err                error
	refreshScheduled   bool
	activeView         string
	committed          int
	historyQueued      int
	historyWrites      []sessionTUIHistoryWrite
	historyWriting     bool
	nextHistoryID      uint64
	hideNextHistory    bool
	historyHiddenUntil int
	historyCursor      string
	historyNoticeAt    int
	historyPageLoading bool
	historyPageReading bool
	historyPageCursor  string
	historyPageEvents  []sessionruntime.Event
	historyRequestID   string
	historyPageStarted time.Time
	historyAllReported bool
	printHistory       func(string) tea.Cmd
	waitForTermination tea.Cmd
	now                func() time.Time
	sizeInitialized    bool
	reflowGeneration   int
	quitRequested      bool
	quitting           bool
	runtimeStatus      sessionruntime.RuntimeStatus
	pendingAttachments []sessionruntime.Attachment
}

func runSessionTUI(ctx context.Context, input io.Reader, output io.Writer, events *json.Decoder, requests *json.Encoder, color bool) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	programDone := make(chan struct{})
	defer close(programDone)
	waitForTermination := func() tea.Msg {
		select {
		case <-ctx.Done():
		case <-signals:
		case <-programDone:
			return nil
		}
		return sessionTUITerminateMsg{}
	}
	var defaultColors *sessionTUIDefaultColors
	if color {
		defaultColors = probeSessionTUIDefaultColors(input, output)
	}
	model := newSessionTUIModel(events, requests, output, color, defaultColors, waitForTermination)
	program := tea.NewProgram(
		model,
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithoutSignalHandler(),
	)
	finalModel, err := program.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("running Session terminal UI: %w", err)
	}
	if model, ok := finalModel.(*sessionTUIModel); ok && model.err != nil {
		return model.err
	}
	return nil
}

func newSessionTUIModel(events *json.Decoder, requests *json.Encoder, output io.Writer, color bool, defaultColors *sessionTUIDefaultColors, waitForTermination tea.Cmd) *sessionTUIModel {
	renderer := newSessionTUIRenderer(output, color)
	styles := newSessionTUIStyles(renderer, color, defaultColors)
	now := time.Now
	input := textarea.New()
	input.Prompt = ""
	input.Placeholder = ""
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.MaxHeight = 0
	input.MaxWidth = 0
	input.SetHeight(1)
	input.SetWidth(sessionTUIDefaultWidth - len(sessionTUIComposerPrompt))
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	input.FocusedStyle = sessionTUITextAreaStyle(styles.user)
	input.BlurredStyle = sessionTUITextAreaStyle(styles.user)

	model := &sessionTUIModel{
		events:             events,
		requests:           requests,
		input:              input,
		styles:             styles,
		toolNames:          make(map[string]string),
		toolOutputAt:       make(map[string]int),
		width:              sessionTUIDefaultWidth,
		height:             sessionTUIDefaultHeight,
		connectionStatus:   sessionTerminalStatusConnecting,
		connectionStarted:  now(),
		replayingHistory:   true,
		historyAt:          -1,
		streamingAt:        -1,
		historyNoticeAt:    -1,
		printHistory:       func(rendered string) tea.Cmd { return tea.Println(rendered) },
		waitForTermination: waitForTermination,
		now:                now,
	}
	return model
}

func newSessionTUIRenderer(output io.Writer, color bool) *lipgloss.Renderer {
	renderer := lipgloss.NewRenderer(output)
	if !color {
		return renderer
	}
	profile := termenv.NewOutput(output, termenv.WithUnsafe()).ColorProfile()
	if profile == termenv.Ascii {
		profile = termenv.ANSI
	}
	renderer.SetColorProfile(profile)
	return renderer
}

func newSessionTUIStyles(renderer *lipgloss.Renderer, color bool, defaultColors *sessionTUIDefaultColors) sessionTUIStyles {
	base := renderer.NewStyle()
	styles := sessionTUIStyles{
		base:         base,
		user:         base,
		muted:        base,
		warning:      base,
		error:        base,
		accent:       base,
		inputHeading: base,
		tool:         base,
		success:      base,
		failure:      base,
		pending:      base,
		diffMetadata: base,
		diffHeader:   base,
		diffAdded:    base,
		diffRemoved:  base,
	}
	if !color {
		return styles
	}

	if defaultColors != nil && (renderer.ColorProfile() == termenv.TrueColor || renderer.ColorProfile() == termenv.ANSI256) {
		styles.user = base.Background(lipgloss.Color(sessionTUIUserMessageBackground(defaultColors.background)))
	}
	styles.muted = base.Faint(true)
	styles.warning = base.Foreground(lipgloss.Color("3")).Bold(true)
	styles.error = base.Foreground(lipgloss.Color("1")).Bold(true)
	styles.accent = base.Foreground(lipgloss.Color("6"))
	styles.inputHeading = base.Foreground(lipgloss.Color("4")).Bold(true)
	styles.tool = base.Foreground(lipgloss.Color("6")).Bold(true)
	styles.success = base.Foreground(lipgloss.Color("2"))
	styles.failure = base.Foreground(lipgloss.Color("1"))
	styles.pending = base.Foreground(lipgloss.Color("3"))
	styles.diffMetadata = base.Faint(true)
	styles.diffHeader = base.Bold(true)
	styles.diffAdded = base.Foreground(lipgloss.Color("2"))
	styles.diffRemoved = base.Foreground(lipgloss.Color("1"))
	return styles
}

func sessionTUITextAreaStyle(background lipgloss.Style) textarea.Style {
	return textarea.Style{
		Base:             background,
		CursorLine:       background,
		CursorLineNumber: background,
		EndOfBuffer:      background,
		LineNumber:       background,
		Placeholder:      background.Faint(true),
		Prompt:           background.Bold(true),
		Text:             background,
	}
}

func (m *sessionTUIModel) Init() tea.Cmd {
	return tea.Batch(m.readEvent(), m.waitForTermination, m.scheduleProgress())
}

func (m *sessionTUIModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		if m.quitRequested {
			return m, nil
		}
		return m, m.resize(message.Width, message.Height)
	case sessionTUIEventResult:
		if m.quitRequested {
			return m, nil
		}
		if message.err != nil {
			if !errors.Is(message.err, io.EOF) {
				m.err = message.err
			}
			return m, m.quit()
		}
		commands := m.applyEvent(message.event)
		return m, tea.Batch(m.readEvent(), commands.ui, commands.history)
	case sessionTUIRefreshMsg:
		if m.quitRequested {
			return m, nil
		}
		m.refreshScheduled = false
		m.refreshActiveView()
		return m, nil
	case sessionTUIProgressMsg:
		if m.quitRequested {
			return m, nil
		}
		m.progressScheduled = false
		if !m.progressVisible() {
			return m, nil
		}
		return m, m.scheduleProgress()
	case sessionTUIReflowMsg:
		if m.quitRequested || message.generation != m.reflowGeneration {
			return m, nil
		}
		return m, m.queueHistoryReflow(message.generation)
	case sessionTUIHistoryPrintedMsg:
		return m, m.finishHistoryWrite(message.id)
	case sessionTUITerminateMsg:
		return m, m.quit()
	case tea.KeyMsg:
		if m.quitRequested {
			return m, nil
		}
		if (message.Type != tea.KeyUp && message.Type != tea.KeyDown) || message.Alt || message.Paste {
			m.promptRequestID = ""
		}
		if message.Paste && m.ready {
			if path, ok := sessionTerminalDroppedFile(string(message.Runes)); ok {
				return m, m.attachFile(path)
			}
		}
		switch message.Type {
		case tea.KeyCtrlC:
			if m.turnActive {
				return m, m.interruptTurn()
			}
			return m, m.quit()
		case tea.KeyEnter:
			if m.ready {
				return m, m.submitInput()
			}
			return m, nil
		case tea.KeyPgUp:
			if m.ready && m.input.Value() == "" {
				return m, m.requestOlderHistory()
			}
			return m, nil
		case tea.KeyCtrlJ:
			if m.ready {
				m.input.SetHeight(min(m.input.Height()+1, m.composerMaxHeight()))
			}
		case tea.KeyEsc:
			if m.canInterruptTurn() {
				return m, m.interruptTurn()
			}
			return m, nil
		case tea.KeyUp:
			if m.ready && message.Alt && m.recallPendingTurn() {
				return m, nil
			}
			if m.ready && !message.Alt && m.pendingEditTurnID == "" && (m.historyAt >= 0 || (m.input.Line() == 0 && m.input.LineInfo().RowOffset == 0)) {
				return m, m.previousInput()
			}
		case tea.KeyDown:
			if m.ready && !message.Alt && m.pendingEditTurnID == "" && (m.historyAt >= 0 || m.promptRequestID != "") {
				m.promptRequestID = ""
				m.nextInput()
				return m, nil
			}
		}
		m.promptRequestID = ""
	}

	if !m.ready || m.quitRequested {
		return m, nil
	}
	var cmd tea.Cmd
	previousValue := m.input.Value()
	m.input, cmd = m.input.Update(message)
	if m.input.Value() != previousValue {
		m.promptRequestID = ""
		m.historyAt = -1
	}
	m.resizeComposer()
	return m, cmd
}

func (m *sessionTUIModel) View() string {
	if m.quitting {
		return ""
	}
	footer := m.footerView()
	if m.activeView == "" {
		return footer
	}
	return m.activeView + "\n" + footer
}

func (m *sessionTUIModel) readEvent() tea.Cmd {
	return func() tea.Msg {
		var event sessionruntime.Event
		err := m.events.Decode(&event)
		return sessionTUIEventResult{event: event, err: err}
	}
}

func (m *sessionTUIModel) submitInput() tea.Cmd {
	m.promptRequestID = ""
	line := m.input.Value()
	if m.pendingEditTurnID != "" {
		request := sessionruntime.ClientRequest{
			Type:             "message.edit",
			TurnID:           m.pendingEditTurnID,
			Text:             line,
			ExpectedRevision: m.pendingEditRev,
		}
		if strings.TrimSpace(line) == "" {
			request.Type = "message.remove"
			request.Text = ""
		}
		if err := m.requests.Encode(request); err != nil {
			m.err = err
			return m.quit()
		}
		m.pendingEditTurnID = ""
		m.pendingEditRev = 0
		m.historyAt = -1
		m.draft = ""
		m.input.Reset()
		m.resizeComposer()
		return nil
	}
	if line == "/quit" || line == "/exit" {
		return m.quit()
	}
	if strings.TrimSpace(line) == "/history" {
		m.input.Reset()
		m.resizeComposer()
		return m.requestOlderHistory()
	}
	request := sessionTerminalRequest(line)
	if request.Type == "" && len(m.pendingAttachments) > 0 && strings.TrimSpace(line) == "" {
		request = sessionruntime.ClientRequest{Type: "message"}
	}
	if request.Type == "" {
		m.input.Reset()
		return nil
	}
	if request.Type == sessionTerminalRequestAttachment {
		m.input.Reset()
		m.resizeComposer()
		return m.attachFile(request.Text)
	}
	if request.Type == "message" {
		request.AttachmentIDs = sessionAttachmentIDs(m.pendingAttachments)
	}
	if err := m.requests.Encode(request); err != nil {
		m.err = err
		return m.quit()
	}
	if request.Type == "message" {
		m.pendingAttachments = nil
	}
	m.historyAt = -1
	m.draft = ""
	m.input.Reset()
	m.resizeComposer()
	return nil
}

func (m *sessionTUIModel) attachFile(path string) tea.Cmd {
	if len(m.pendingAttachments) >= sessionruntime.MaxAttachmentsPerMessage {
		m.appendBlock(sessionTUIBlockError, fmt.Sprintf("error: a message supports at most %d attachments", sessionruntime.MaxAttachmentsPerMessage))
		m.refreshActiveView()
		return m.queueReadyBlocks()
	}
	if err := m.requests.Encode(sessionruntime.ClientRequest{Type: sessionTerminalRequestAttachment, Text: path}); err != nil {
		m.err = err
		return m.quit()
	}
	return nil
}

func sessionTerminalDroppedFile(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	if strings.HasPrefix(value, "file://") {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Host != "" && parsed.Host != "localhost") {
			return "", false
		}
		value = parsed.Path
	}
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
		value = value[1 : len(value)-1]
	}
	value = strings.ReplaceAll(value, `\ `, " ")
	info, err := os.Stat(value)
	if err != nil || !info.Mode().IsRegular() || info.Size() > sessionruntime.MaxAttachmentBytes {
		return "", false
	}
	return value, true
}

func (m *sessionTUIModel) recallPendingTurn() bool {
	if m.pendingTurnID == "" || m.input.Value() != "" || m.historyAt != -1 || len(m.pendingAttachments) > 0 {
		return false
	}
	m.pendingEditTurnID = m.pendingTurnID
	m.promptRequestID = ""
	m.pendingEditRev = m.pendingRevision
	m.input.SetValue(m.pendingTurnInput)
	m.input.CursorEnd()
	m.resizeComposer()
	return true
}

func (m *sessionTUIModel) nextInput() {
	if m.historyAt == -1 {
		return
	}
	if m.historyAt < len(m.history)-1 {
		m.historyAt++
		m.input.SetValue(m.history[m.historyAt])
	} else {
		m.historyAt = -1
		m.input.SetValue(m.draft)
	}
	m.input.CursorEnd()
	m.resizeComposer()
}

func (m *sessionTUIModel) applyEvent(event sessionruntime.Event) sessionTUICommands {
	var commands sessionTUICommands
	if event.Type == sessionruntime.EventPrompts || (event.Type == sessionruntime.EventError && strings.HasPrefix(event.RequestID, sessionPromptRequestPrefix)) {
		return m.receivePromptHistory(event)
	}
	if m.historyPageReading {
		if event.Type == sessionruntime.EventHistoryEnd && event.HistoryPage {
			return m.finishOlderHistoryPage()
		}
		if event.Type == sessionTerminalEventDiagnostic || event.Type == sessionTerminalEventAttachmentAdded || (event.Type == sessionruntime.EventHistoryStart && !event.HistoryPage) {
			m.cancelOlderHistoryPage()
		} else {
			m.historyPageEvents = append(m.historyPageEvents, event)
			return commands
		}
	}
	recoveredCompletion := sessionTerminalRecoveredCompletion(m.recoveryActive, event)
	if m.recoveryActive && !sessionTerminalRuntimeRecoveryEvent(event) {
		m.recoveryActive = false
	}
	switch event.Type {
	case sessionruntime.EventHistoryStart:
		if event.HistoryPage {
			m.historyPageLoading = true
			m.historyPageReading = true
			m.historyPageCursor = event.HistoryCursor
			m.historyPageEvents = nil
			return commands
		}
		m.replayingHistory = true
		if event.Reset || !m.ready {
			m.historyCursor = event.HistoryCursor
			m.historyAllReported = false
		}
		if event.HistoryLimited {
			m.historyNoticeAt = len(m.blocks)
			m.appendBlock(sessionTUIBlockNotice, "Earlier Session history is available. Use /history or Page Up to load the previous page.")
		}
		if event.Reset {
			m.resetPromptHistory()
			m.cancelOlderHistoryPage()
			m.turnActive = false
			m.activeTurnID = ""
			m.activeTurnStarted = time.Time{}
			m.waitingForInput = false
			m.turnInterrupting = false
		}
	case sessionruntime.EventRuntimeStatus:
		if event.Runtime != nil {
			m.runtimeStatus = *event.Runtime
		}
	case sessionruntime.EventHistoryEnd:
		m.replayingHistory = false
		m.applyHistoryState(event.HistoryState)
		// A terminal-height transcript in both the managed view and native scrollback
		// makes Bubble Tea move the smaller footer to the top when the copy is removed.
		m.hideNextHistory = !m.ready
		m.appendBlock(sessionTUIBlockNotice, "Connected. Enter sends, Ctrl+J inserts a newline, Up/Down browses prompts, Ctrl+C or Esc interrupts active work, Page Up loads earlier history, and dragging a file attaches it (or use /attach PATH). Press Alt+Up on an empty composer to edit pending work. Use !COMMAND, /goal, /answer INPUT QUESTION VALUE, or /quit.")
		m.ready = true
		m.connectionStatus = ""
		commands.ui = tea.Batch(m.input.Focus(), m.scheduleProgress())
	case sessionruntime.EventRuntimeRecovered:
		m.recoveryActive = true
		m.appendBlock(sessionTUIBlockWarning, event.Text)
	case sessionTerminalEventDiagnostic:
		if event.Text != "" {
			m.appendBlock(sessionTUIBlockWarning, event.Text)
		}
		switch event.Status {
		case sessionTerminalStatusConnecting, sessionTerminalStatusReconnecting:
			m.resetPromptHistory()
			m.cancelOlderHistoryPage()
			if m.connectionStatus != event.Status {
				m.connectionStarted = m.now()
			}
			m.connectionStatus = event.Status
			commands.ui = m.scheduleProgress()
		case sessionTerminalStatusConnected:
			m.connectionStatus = ""
		}
	case sessionruntime.EventUserMessage:
		if event.TurnID == "" {
			m.finishStreaming()
			m.appendBlock(sessionTUIBlockUser, sessionTerminalMessageText(event.Text, event.Attachments))
		} else {
			m.setPendingUser(event.TurnID, event.Text, event.Attachments, event.Revision)
		}
	case sessionruntime.EventUserMessageUpdated:
		if m.pendingTurnID == event.TurnID {
			m.pendingTurnText = sessionTerminalMessageText(event.Text, event.Attachments)
			m.pendingTurnInput = event.Text
			m.pendingRevision = max(1, event.Revision)
		}
	case sessionruntime.EventUserMessageRemoved:
		if m.discardPendingTurn(event.TurnID) {
			m.appendBlock(sessionTUIBlockNotice, "Pending message removed.")
		}
	case sessionTerminalEventAttachmentAdded:
		m.pendingAttachments = append(m.pendingAttachments, event.Attachments...)
		for _, attachment := range event.Attachments {
			m.appendBlock(sessionTUIBlockNotice, fmt.Sprintf("Attached %s (%d bytes). Send a message to include it.", attachment.Name, attachment.SizeBytes))
		}
	case sessionruntime.EventTurnStarted:
		m.turnActive = true
		m.acceptPendingTurn(event.TurnID)
		if m.activeTurnID != event.TurnID {
			m.activeTurnStarted = sessionTerminalTurnStartedAt(event, m.now(), m.replayingHistory)
		}
		m.activeTurnID = event.TurnID
		m.waitingForInput = false
		m.turnInterrupting = false
		commands.ui = m.scheduleProgress()
	case sessionruntime.EventAssistantDelta:
		m.appendAssistantDelta(event.Text)
	case sessionruntime.EventAssistantMessage:
		if m.streamingAt >= 0 {
			m.finishStreaming()
		} else if event.Text != "" {
			m.appendBlock(sessionTUIBlockAssistant, event.Text)
		}
	case sessionruntime.EventToolStarted:
		m.finishStreaming()
		m.appendToolStart(event)
	case sessionruntime.EventToolDelta:
		m.appendToolDelta(event)
	case sessionruntime.EventToolCompleted:
		streamed := m.completeToolOutput(event)
		toolName, needsAttribution := m.toolCompletionAttribution(event)
		output := strings.TrimRight(sanitizeSessionTUIToolOutput(event.Output), "\n")
		if output != "" && !streamed {
			m.appendToolOutput(event.ToolID, toolName, output)
		}
		if output == "" || !sessionTUIToolSucceeded(event.Status) {
			status := event.Status
			if output == "" && needsAttribution && toolName != "" {
				status = toolName + ": " + status
			}
			m.appendBlock(sessionTUIBlockToolStatus, status)
		}
	case sessionruntime.EventGoalUpdated:
		m.finishStreaming()
		m.appendBlock(sessionTUIBlockNotice, sessionGoalText(event.Goal, event.Status))
	case sessionruntime.EventInputRequested:
		m.finishStreaming()
		m.waitingForInput = true
		m.appendBlock(sessionTUIBlockInput, sessionTUIInputRequestText(event))
	case sessionruntime.EventInputResolved:
		m.waitingForInput = false
		m.appendBlock(sessionTUIBlockNotice, fmt.Sprintf("Input %s %s.", event.InputID, event.Status))
	case sessionruntime.EventTurnInterrupting:
		m.finishStreaming()
		m.turnInterrupting = true
		m.appendBlock(sessionTUIBlockWarning, "Interrupting active work…")
	case sessionruntime.EventFileDiff:
		m.finishStreaming()
		m.appendBlock(sessionTUIBlockDiff, event.Diff)
	case sessionruntime.EventTurnCompleted:
		m.turnActive = false
		m.acceptPendingTurn(event.TurnID)
		m.finishStreaming()
		elapsed, hasElapsed := sessionTerminalTurnElapsed(m.activeTurnID, m.activeTurnStarted, event, m.now(), m.replayingHistory, recoveredCompletion)
		if event.TurnID == "" || event.TurnID == m.activeTurnID {
			m.activeTurnID = ""
			m.activeTurnStarted = time.Time{}
			m.waitingForInput = false
			m.turnInterrupting = false
		}
		if event.Status == "interrupted" {
			m.appendBlock(sessionTUIBlockWarning, "Turn interrupted.")
		}
		if hasElapsed {
			m.appendBlock(sessionTUIBlockTurnSeparator, formatSessionTUIElapsed(elapsed))
		}
	case sessionruntime.EventError:
		m.finishStreaming()
		if event.RequestID != "" && event.RequestID == m.historyRequestID {
			m.cancelOlderHistoryPage()
		}
		if event.Status == "rejected" {
			m.turnInterrupting = false
		}
		m.appendBlock(sessionTUIBlockError, "error: "+event.Text)
	}
	if event.Type == sessionruntime.EventAssistantDelta && m.ready {
		commands.ui = m.scheduleRefresh()
		return commands
	}
	if m.ready || event.Type == sessionruntime.EventHistoryEnd || event.Type == sessionTerminalEventDiagnostic {
		commands.history = m.queueReadyBlocks()
		m.refreshActiveView()
	}
	return commands
}

func (m *sessionTUIModel) applyHistoryState(state *sessionruntime.HistoryState) {
	if state == nil {
		return
	}
	m.pendingTurnID = ""
	m.pendingTurnText = ""
	m.pendingTurnInput = ""
	m.pendingRevision = 0
	if state.PendingTurn != nil {
		m.pendingTurnID = state.PendingTurn.TurnID
		m.pendingTurnText = sessionTerminalMessageText(state.PendingTurn.Text, state.PendingTurn.Attachments)
		m.pendingTurnInput = state.PendingTurn.Text
		m.pendingRevision = max(1, state.PendingTurn.Revision)
	}
	if m.pendingEditTurnID != "" && m.pendingEditTurnID != m.pendingTurnID {
		m.cancelPendingEdit(m.pendingEditTurnID)
	}
	m.activeTurnID = state.ActiveTurnID
	m.turnActive = state.ActiveTurnID != ""
	if state.ActiveTurnStarted != nil {
		m.activeTurnStarted = *state.ActiveTurnStarted
	} else if m.turnActive {
		m.activeTurnStarted = m.now()
	}
	m.waitingForInput = state.WaitingForInput
	m.turnInterrupting = state.TurnInterrupting
}

func (m *sessionTUIModel) requestOlderHistory() tea.Cmd {
	if m.historyPageLoading {
		return nil
	}
	if m.historyCursor == "" {
		if m.historyAllReported {
			return nil
		}
		m.historyAllReported = true
		m.appendBlock(sessionTUIBlockNotice, "All retained Session history is loaded.")
		command := m.queueReadyBlocks()
		m.refreshActiveView()
		return command
	}
	requestID := string(uuid.NewUUID())
	if err := m.requests.Encode(sessionruntime.ClientRequest{
		Type:          "history",
		RequestID:     requestID,
		HistoryCursor: m.historyCursor,
	}); err != nil {
		m.err = err
		return m.quit()
	}
	m.historyPageLoading = true
	m.historyRequestID = requestID
	m.historyPageStarted = m.now()
	return m.scheduleProgress()
}

func (m *sessionTUIModel) finishOlderHistoryPage() sessionTUICommands {
	blocks := m.replayHistoryPage(m.historyPageEvents)
	m.historyCursor = m.historyPageCursor
	m.historyAllReported = m.historyCursor == ""
	m.historyPageLoading = false
	m.historyPageReading = false
	m.historyPageCursor = ""
	m.historyPageEvents = nil
	m.historyRequestID = ""

	noticeChanged := false
	if m.historyCursor == "" && m.historyNoticeAt >= 0 && m.historyNoticeAt < len(m.blocks) {
		m.blocks[m.historyNoticeAt].text = "All retained Session history is loaded."
		m.blocks[m.historyNoticeAt].dirty = true
		noticeChanged = true
	}
	command := m.prependHistoryBlocks(blocks)
	if len(blocks) == 0 && noticeChanged {
		m.reflowGeneration++
		command = m.queueHistoryReflow(m.reflowGeneration)
	}
	m.refreshActiveView()
	return sessionTUICommands{history: command}
}

func (m *sessionTUIModel) cancelOlderHistoryPage() {
	m.historyPageLoading = false
	m.historyPageReading = false
	m.historyPageCursor = ""
	m.historyPageEvents = nil
	m.historyRequestID = ""
}

func (m *sessionTUIModel) replayHistoryPage(events []sessionruntime.Event) []sessionTUIBlock {
	replay := &sessionTUIModel{
		styles:          m.styles,
		toolNames:       make(map[string]string),
		toolOutputAt:    make(map[string]int),
		width:           m.width,
		height:          m.height,
		streamingAt:     -1,
		historyNoticeAt: -1,
		now:             m.now,
	}
	for _, event := range events {
		replay.applyEvent(event)
	}
	replay.finishStreaming()
	return replay.blocks
}

func (m *sessionTUIModel) prependHistoryBlocks(blocks []sessionTUIBlock) tea.Cmd {
	if len(blocks) == 0 {
		return nil
	}
	count := len(blocks)
	m.blocks = append(blocks, m.blocks...)
	if m.streamingAt >= 0 {
		m.streamingAt += count
	}
	if m.historyNoticeAt >= 0 {
		m.historyNoticeAt += count
	}
	for toolID, index := range m.toolOutputAt {
		m.toolOutputAt[toolID] = index + count
	}
	m.committed += count
	m.historyQueued += count
	for index := range m.historyWrites {
		if m.historyWrites[index].commitEnd > 0 {
			m.historyWrites[index].commitEnd += count
		}
	}
	m.invalidateTranscript()
	m.reflowGeneration++
	return m.queueHistoryReflow(m.reflowGeneration)
}

func (m *sessionTUIModel) scheduleRefresh() tea.Cmd {
	if m.refreshScheduled {
		return nil
	}
	m.refreshScheduled = true
	return tea.Tick(sessionTUIFrameInterval, func(time.Time) tea.Msg {
		return sessionTUIRefreshMsg{}
	})
}

func (m *sessionTUIModel) scheduleProgress() tea.Cmd {
	if m.progressScheduled || !m.progressVisible() {
		return nil
	}
	m.progressScheduled = true
	return tea.Tick(sessionTUIProgressInterval, func(time.Time) tea.Msg {
		return sessionTUIProgressMsg{}
	})
}

func (m *sessionTUIModel) progressVisible() bool {
	connecting := m.connectionStatus == sessionTerminalStatusReconnecting ||
		(m.connectionStatus == sessionTerminalStatusConnecting && !m.ready)
	return connecting || m.historyPageLoading || m.activeTurnID != ""
}

func (m *sessionTUIModel) canInterruptTurn() bool {
	return m.ready && m.connectionStatus == "" && m.activeTurnID != "" && !m.turnInterrupting
}

func (m *sessionTUIModel) interruptTurn() tea.Cmd {
	if m.turnInterrupting {
		return nil
	}
	if err := m.requests.Encode(sessionruntime.ClientRequest{Type: "interrupt"}); err != nil {
		m.err = err
		return m.quit()
	}
	m.turnInterrupting = true
	return m.scheduleProgress()
}

func sessionTUIInputRequestText(event sessionruntime.Event) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Input %s requested:", event.InputID)
	for _, question := range event.Questions {
		fmt.Fprintf(&text, "\n  %s — %s", question.ID, question.Question)
		for _, option := range question.Options {
			fmt.Fprintf(&text, "\n    %s — %s", option.Label, option.Description)
		}
	}
	fmt.Fprintf(&text, "\nUse /answer %s QUESTION_ID VALUE, or /cancel-input %s. Separate multiple values with commas.", event.InputID, event.InputID)
	return text.String()
}

func (m *sessionTUIModel) appendAssistantDelta(text string) {
	if m.streamingAt < 0 {
		m.blocks = append(m.blocks, sessionTUIBlock{
			kind:   sessionTUIBlockAssistant,
			stream: &strings.Builder{},
			dirty:  true,
		})
		m.streamingAt = len(m.blocks) - 1
	}
	m.blocks[m.streamingAt].stream.WriteString(text)
	m.blocks[m.streamingAt].dirty = true
}

func (m *sessionTUIModel) finishStreaming() {
	if m.streamingAt >= 0 {
		block := &m.blocks[m.streamingAt]
		block.text = block.stream.String()
		block.stream = nil
	}
	m.streamingAt = -1
}

func (m *sessionTUIModel) appendBlock(kind sessionTUIBlockKind, text string) {
	m.blocks = append(m.blocks, sessionTUIBlock{kind: kind, text: text, dirty: true})
}

func (m *sessionTUIModel) appendToolStart(event sessionruntime.Event) {
	name := sanitizeSessionTUIToolOutput(event.ToolName)
	if event.ToolID != "" {
		m.toolNames[event.ToolID] = name
	}
	m.blocks = append(m.blocks, sessionTUIBlock{
		kind:     sessionTUIBlockTool,
		text:     name,
		dirty:    true,
		toolID:   event.ToolID,
		toolName: name,
	})
}

func (m *sessionTUIModel) appendToolOutput(toolID, toolName, output string) {
	m.blocks = append(m.blocks, sessionTUIBlock{
		kind:     sessionTUIBlockToolOutput,
		text:     output,
		dirty:    true,
		toolID:   toolID,
		toolName: toolName,
	})
}

func (m *sessionTUIModel) appendToolDelta(event sessionruntime.Event) {
	if event.Output == "" {
		return
	}
	index, exists := m.toolOutputAt[event.ToolID]
	if !exists {
		m.blocks = append(m.blocks, sessionTUIBlock{
			kind:     sessionTUIBlockToolOutput,
			stream:   &strings.Builder{},
			dirty:    true,
			toolID:   event.ToolID,
			toolName: "",
		})
		index = len(m.blocks) - 1
		m.toolOutputAt[event.ToolID] = index
		m.streamingAt = index
	}
	block := &m.blocks[index]
	if block.stream == nil {
		block.stream = &strings.Builder{}
		block.stream.WriteString(block.text)
		block.text = ""
	}
	block.stream.WriteString(sanitizeSessionTUIToolOutput(event.Output))
	block.dirty = true
}

func (m *sessionTUIModel) completeToolOutput(event sessionruntime.Event) bool {
	index, exists := m.toolOutputAt[event.ToolID]
	if !exists {
		return false
	}
	if m.streamingAt == index {
		m.finishStreaming()
	}
	block := &m.blocks[index]
	if event.Output != "" {
		block.text = strings.TrimRight(sanitizeSessionTUIToolOutput(event.Output), "\n")
	}
	block.stream = nil
	block.dirty = true
	delete(m.toolOutputAt, event.ToolID)
	return true
}

func (m *sessionTUIModel) toolCompletionAttribution(event sessionruntime.Event) (string, bool) {
	name := sanitizeSessionTUIToolOutput(event.ToolName)
	if name == "" {
		name = m.toolNames[event.ToolID]
	}
	if name == "" {
		name = event.ToolID
	}
	if event.ToolID != "" {
		delete(m.toolNames, event.ToolID)
	}
	if len(m.blocks) == 0 {
		return name, true
	}
	previous := m.blocks[len(m.blocks)-1]
	adjacent := previous.kind == sessionTUIBlockTool && previous.toolID == event.ToolID
	if adjacent {
		return "", false
	}
	return name, true
}

func (m *sessionTUIModel) setPendingUser(turnID, text string, attachments []sessionruntime.Attachment, revision int64) {
	m.pendingTurnID = turnID
	m.pendingTurnText = sessionTerminalMessageText(text, attachments)
	m.pendingTurnInput = text
	m.pendingRevision = max(1, revision)
}

func (m *sessionTUIModel) acceptPendingTurn(turnID string) bool {
	if m.pendingTurnID != turnID {
		return false
	}
	text := m.pendingTurnText
	m.pendingTurnID = ""
	m.pendingTurnText = ""
	m.pendingTurnInput = ""
	m.pendingRevision = 0
	m.cancelPendingEdit(turnID)
	m.finishStreaming()
	m.appendBlock(sessionTUIBlockUser, text)
	return true
}

func (m *sessionTUIModel) discardPendingTurn(turnID string) bool {
	if m.pendingTurnID != turnID {
		return false
	}
	m.pendingTurnID = ""
	m.pendingTurnText = ""
	m.pendingTurnInput = ""
	m.pendingRevision = 0
	m.cancelPendingEdit(turnID)
	return true
}

func (m *sessionTUIModel) cancelPendingEdit(turnID string) {
	if m.pendingEditTurnID != turnID {
		return
	}
	m.pendingEditTurnID = ""
	m.pendingEditRev = 0
	m.historyAt = -1
	m.draft = ""
	m.input.Reset()
	m.resizeComposer()
}

func (m *sessionTUIModel) resize(width, height int) tea.Cmd {
	if width <= 0 || height <= 0 {
		return nil
	}
	changed := m.sizeInitialized && (m.width != width || m.height != height)
	m.width = width
	m.height = height
	m.input.SetWidth(max(1, width-len(sessionTUIComposerPrompt)))
	m.resizeComposer()
	m.invalidateTranscript()
	if m.ready {
		m.refreshActiveView()
	}
	if !m.sizeInitialized {
		m.sizeInitialized = true
		return nil
	}
	if !changed || m.historyQueued == 0 {
		return nil
	}
	m.reflowGeneration++
	generation := m.reflowGeneration
	return tea.Tick(sessionTUIReflowDelay, func(time.Time) tea.Msg {
		return sessionTUIReflowMsg{generation: generation}
	})
}

func (m *sessionTUIModel) invalidateTranscript() {
	for index := range m.blocks {
		m.blocks[index].dirty = true
	}
}

func (m *sessionTUIModel) renderTranscript() string {
	return m.renderBlockRange(0, len(m.blocks))
}

func (m *sessionTUIModel) renderBlockRange(start, end int) string {
	blocks := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		block := &m.blocks[index]
		if block.dirty {
			block.rendered = m.renderBlock(*block)
			block.dirty = false
		}
		if block.rendered != "" {
			blocks = append(blocks, block.rendered)
			if block.kind == sessionTUIBlockUser {
				blocks = append(blocks, "")
			}
		}
	}
	return strings.Join(blocks, "\n")
}

func (m *sessionTUIModel) queueReadyBlocks() tea.Cmd {
	if !m.ready {
		return nil
	}
	end := len(m.blocks)
	if m.streamingAt >= m.historyQueued {
		end = m.streamingAt
	}
	if end <= m.historyQueued {
		return nil
	}
	m.nextHistoryID++
	m.historyWrites = append(m.historyWrites, sessionTUIHistoryWrite{
		id:        m.nextHistoryID,
		rendered:  m.renderBlockRange(m.historyQueued, end),
		commitEnd: end,
	})
	if m.hideNextHistory {
		m.historyHiddenUntil = end
		m.hideNextHistory = false
	}
	m.historyQueued = end
	return m.startHistoryWrite()
}

func (m *sessionTUIModel) queueHistoryReflow(generation int) tea.Cmd {
	m.nextHistoryID++
	m.historyWrites = append(m.historyWrites, sessionTUIHistoryWrite{
		id:               m.nextHistoryID,
		rendered:         sessionTUIClearHistory + m.renderBlockRange(0, m.historyQueued),
		reflowGeneration: generation,
	})
	return m.startHistoryWrite()
}

func (m *sessionTUIModel) startHistoryWrite() tea.Cmd {
	if m.historyWriting {
		return nil
	}
	for len(m.historyWrites) > 0 {
		write := m.historyWrites[0]
		if write.reflowGeneration != 0 && write.reflowGeneration != m.reflowGeneration {
			m.popHistoryWrite()
			continue
		}
		if write.rendered == "" {
			m.completeHistoryWrite(write)
			continue
		}
		m.historyWriting = true
		cmd := m.printHistory(write.rendered)
		if cmd == nil {
			m.completeHistoryWrite(write)
			continue
		}
		printed := func() tea.Msg { return sessionTUIHistoryPrintedMsg{id: write.id} }
		return tea.Sequence(cmd, printed)
	}
	if m.quitRequested {
		return tea.Quit
	}
	return nil
}

func (m *sessionTUIModel) finishHistoryWrite(id uint64) tea.Cmd {
	if !m.historyWriting || len(m.historyWrites) == 0 || m.historyWrites[0].id != id {
		return nil
	}
	m.completeHistoryWrite(m.historyWrites[0])
	return m.startHistoryWrite()
}

func (m *sessionTUIModel) completeHistoryWrite(write sessionTUIHistoryWrite) {
	m.popHistoryWrite()
	m.historyWriting = false
	if write.commitEnd > m.committed {
		m.committed = write.commitEnd
	}
	if m.committed >= m.historyHiddenUntil {
		m.historyHiddenUntil = 0
	}
	m.refreshActiveView()
}

func (m *sessionTUIModel) popHistoryWrite() {
	m.historyWrites[0] = sessionTUIHistoryWrite{}
	m.historyWrites = m.historyWrites[1:]
	if len(m.historyWrites) == 0 {
		m.historyWrites = nil
	}
}

func (m *sessionTUIModel) refreshActiveView() {
	start := max(m.committed, m.historyHiddenUntil)
	m.activeView = m.renderBlockRange(start, len(m.blocks))
}

func (m *sessionTUIModel) quit() tea.Cmd {
	if m.quitRequested {
		return nil
	}
	m.finishStreaming()
	history := m.queueReadyBlocks()
	m.quitRequested = true
	m.quitting = true
	if history != nil || m.historyWriting || len(m.historyWrites) > 0 {
		return history
	}
	return tea.Quit
}

func (m *sessionTUIModel) renderBlock(block sessionTUIBlock) string {
	text := block.text
	if block.stream != nil {
		text = block.stream.String()
	}
	switch block.kind {
	case sessionTUIBlockUser:
		return m.renderUserBlock(text)
	case sessionTUIBlockAssistant:
		return m.renderAssistantBlock(text)
	case sessionTUIBlockTool:
		return m.renderToolBlock(text)
	case sessionTUIBlockToolOutput:
		return m.renderToolOutputBlock(text, block.toolName)
	case sessionTUIBlockToolStatus:
		return m.renderIndentedBlock(text, sessionTUIIndent*2, m.statusStyle(text))
	case sessionTUIBlockNotice:
		return m.renderIndentedBlock(text, 0, m.styles.muted)
	case sessionTUIBlockWarning:
		return m.renderIndentedBlock(text, 0, m.styles.warning)
	case sessionTUIBlockError:
		return m.renderIndentedBlock(text, 0, m.styles.error)
	case sessionTUIBlockInput:
		return m.renderIndentedBlock(text, 0, m.styles.inputHeading)
	case sessionTUIBlockDiff:
		return m.renderDiff(text)
	case sessionTUIBlockTurnSeparator:
		return m.styles.muted.Render(formatSessionTurnSeparatorText(text, m.width))
	default:
		return ""
	}
}

func (m *sessionTUIModel) renderAssistantBlock(text string) string {
	if text == "" {
		return ""
	}
	contentWidth := max(1, m.width-sessionTUIIndent)
	wrapped := m.styles.base.Width(contentWidth).Render(strings.ReplaceAll(text, "\r\n", "\n"))
	lines := strings.Split(wrapped, "\n")
	for index, line := range lines {
		prefix := strings.Repeat(" ", sessionTUIIndent)
		if index == 0 {
			prefix = m.styles.muted.Render("• ")
		}
		lines[index] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func (m *sessionTUIModel) renderToolBlock(text string) string {
	if text == "" {
		return ""
	}
	contentWidth := max(1, m.width-sessionTUIIndent)
	wrapped := m.styles.base.Width(contentWidth).Render("Ran " + strings.ReplaceAll(text, "\r\n", "\n"))
	lines := strings.Split(wrapped, "\n")
	for index, line := range lines {
		prefix := strings.Repeat(" ", sessionTUIIndent)
		if index == 0 {
			prefix = m.styles.muted.Render("• ")
		}
		lines[index] = prefix + m.styles.tool.Render(line)
	}
	return strings.Join(lines, "\n")
}

func (m *sessionTUIModel) renderToolOutputBlock(output, toolName string) string {
	if toolName != "" {
		output = toolName + ": " + output
	}
	contentWidth := max(1, m.width-sessionTUIIndent*2)
	wrapped := m.styles.base.Width(contentWidth).Render(output)
	lines := strings.Split(wrapped, "\n")
	if len(lines) > sessionTUIToolOutputMaxLines {
		head := (sessionTUIToolOutputMaxLines - 1) / 2
		tail := sessionTUIToolOutputMaxLines - 1 - head
		omitted := len(lines) - head - tail
		ellipsis := m.styles.base.Width(contentWidth).Render(fmt.Sprintf("… +%d lines", omitted))
		lines = append(append(lines[:head:head], ellipsis), lines[len(lines)-tail:]...)
	}
	for index, line := range lines {
		prefix := strings.Repeat(" ", sessionTUIIndent*2)
		if index == 0 {
			prefix = "  └ "
		}
		lines[index] = m.styles.muted.Render(prefix + line)
	}
	return strings.Join(lines, "\n")
}

func sessionTUIToolSucceeded(status string) bool {
	switch strings.ToLower(status) {
	case "completed", "success":
		return true
	default:
		return false
	}
}

func sanitizeSessionTUIToolOutput(output string) string {
	output = ansi.Strip(strings.ReplaceAll(output, "\r\n", "\n"))
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, output)
}

func (m *sessionTUIModel) renderUserBlock(text string) string {
	return m.renderUserBlockWithStatus(text, "")
}

func (m *sessionTUIModel) renderPendingUserBlock(text string) string {
	return m.renderUserBlockWithStatus(text, "Pending")
}

func (m *sessionTUIModel) renderUserBlockWithStatus(text, status string) string {
	width := max(1, m.width)
	contentWidth := max(1, width-3)
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	wrapped := m.styles.base.Width(contentWidth).Render(text)
	lines := strings.Split(wrapped, "\n")
	rows := make([]string, 0, len(lines)+2)
	heading := ""
	if status != "" {
		heading = "  " + status
	}
	rows = append(rows, m.renderUserRow(heading))
	for index, line := range lines {
		prefix := "  "
		if index == 0 {
			prefix = "> "
		}
		rows = append(rows, m.renderUserRow(prefix+line))
	}
	rows = append(rows, m.renderUserRow(""))
	return strings.Join(rows, "\n")
}

func (m *sessionTUIModel) renderUserRow(text string) string {
	width := max(1, m.width)
	return m.styles.user.Width(width).MaxWidth(width).Render(text)
}

func (m *sessionTUIModel) renderIndentedBlock(text string, indent int, style lipgloss.Style) string {
	if text == "" {
		return ""
	}
	contentWidth := max(1, m.width-indent)
	wrapped := m.styles.base.Width(contentWidth).Render(strings.ReplaceAll(text, "\r\n", "\n"))
	prefix := strings.Repeat(" ", indent)
	lines := strings.Split(wrapped, "\n")
	for index, line := range lines {
		lines[index] = prefix + style.Render(line)
	}
	return strings.Join(lines, "\n")
}

func (m *sessionTUIModel) renderDiff(diff string) string {
	lines := []string{m.styles.accent.Render("  --- file changes ---")}
	contentWidth := max(1, m.width-sessionTUIIndent)
	for _, line := range strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		style := m.styles.base
		switch {
		case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "index "):
			style = m.styles.diffMetadata
		case strings.HasPrefix(line, "@@"):
			style = m.styles.accent
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			style = m.styles.diffHeader
		case strings.HasPrefix(line, "+"):
			style = m.styles.diffAdded
		case strings.HasPrefix(line, "-"):
			style = m.styles.diffRemoved
		}
		wrapped := m.styles.base.Width(contentWidth).Render(line)
		for _, wrappedLine := range strings.Split(wrapped, "\n") {
			lines = append(lines, "  "+style.Render(wrappedLine))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *sessionTUIModel) statusStyle(status string) lipgloss.Style {
	switch strings.ToLower(status) {
	case "completed", "success", "answered":
		return m.styles.success
	case "failed", "error", "cancelled", "canceled":
		return m.styles.failure
	case "running", "pending", "interrupting", "interrupted":
		return m.styles.pending
	default:
		return m.styles.muted
	}
}

func (m *sessionTUIModel) composerView() string {
	blank := m.renderUserRow("")
	if !m.ready {
		loading := m.renderUserRow("> " + m.styles.muted.Render("Loading Session…"))
		return blank + "\n" + loading + "\n" + blank
	}
	middle := strings.TrimSuffix(m.input.View(), "\n")
	attachmentLine := ""
	if len(m.pendingAttachments) > 0 {
		names := make([]string, len(m.pendingAttachments))
		for index := range m.pendingAttachments {
			names[index] = m.pendingAttachments[index].Name
		}
		attachmentLine = m.renderUserRow("  " + m.styles.muted.Render("Attached: "+strings.Join(names, ", ")))
	}
	lines := strings.Split(middle, "\n")
	continuation := strings.Repeat(" ", len(sessionTUIComposerPrompt))
	for index := range lines {
		prefix := continuation
		if index == 0 {
			prefix = m.styles.user.Bold(true).Render(sessionTUIComposerPrompt)
		}
		lines[index] = prefix + lines[index]
	}
	middle = strings.Join(lines, "\n")
	middle = m.renderUserRow(middle)
	if attachmentLine != "" {
		return attachmentLine + "\n" + middle + "\n" + blank
	}
	return blank + "\n" + middle + "\n" + blank
}

func (m *sessionTUIModel) resizeComposer() {
	maxHeight := m.composerMaxHeight()
	wrapped := ansi.Wordwrap(m.input.Value(), max(1, m.input.Width()), "")
	inputHeight := strings.Count(ansi.Hardwrap(wrapped, max(1, m.input.Width()), true), "\n") + 1
	m.input.SetHeight(min(max(1, inputHeight), maxHeight))
}

func (m *sessionTUIModel) composerMaxHeight() int {
	progressHeight := 0
	if m.progressVisible() {
		progressHeight = 1
	}
	return min(
		sessionTUIComposerMaxVisibleRows,
		max(1, m.height-sessionTUIComposerMinHeight-sessionTUIComposerGap-progressHeight-sessionTUIStatusBarHeight),
	)
}

func (m *sessionTUIModel) composerHeight() int {
	height := m.input.Height() + sessionTUIComposerMinHeight - 1
	if len(m.pendingAttachments) > 0 {
		height++
	}
	return height
}

func (m *sessionTUIModel) footerHeight() int {
	return m.composerHeight() + sessionTUIComposerGap + sessionTUIStatusBarHeight
}

func (m *sessionTUIModel) footerView() string {
	parts := make([]string, 0, 5)
	if progress := m.progressView(); progress != "" {
		parts = append(parts, progress)
	}
	pending := m.pendingView()
	if pending != "" {
		parts = append(parts, pending)
	}
	parts = append(parts, "", m.composerView(), m.statusBarView())
	return strings.Join(parts, "\n")
}

func (m *sessionTUIModel) statusBarView() string {
	width := max(1, m.width)
	padding := min(sessionTUIIndent, max(0, (width-1)/2))
	contentWidth := max(1, width-padding*2)
	status := sessionTUIFitStatusSegments(m.statusBarSegments(), contentWidth)
	content := strings.Repeat(" ", padding) + m.styles.muted.Render(status)
	content += strings.Repeat(" ", max(0, contentWidth-lipgloss.Width(status))+padding)
	return m.styles.base.Width(width).MaxWidth(width).Render(content)
}

func (m *sessionTUIModel) statusBarSegments() []sessionTUIStatusSegment {
	status := m.runtimeStatus
	segments := make([]sessionTUIStatusSegment, 0, 11)
	add := func(text string, priority int) {
		if text != "" {
			segments = append(segments, sessionTUIStatusSegment{text: text, priority: priority})
		}
	}

	add(status.SessionName, 82)
	add(status.AgentType, 65)
	add(strings.TrimSpace(status.Model+" "+status.Effort), 100)
	add(sessionTUIPath(status.WorkingDir, status.HomeDir), 95)
	add(status.Branch, 90)
	if status.PullRequestNumber > 0 {
		add(fmt.Sprintf("PR #%d", status.PullRequestNumber), 85)
	}
	if status.Usage != nil {
		if status.Usage.ContextWindow > 0 {
			add(fmt.Sprintf("Context %d%% used", sessionTUIContextUsedPercent(*status.Usage)), 78)
		}
	}
	if status.WeeklyLimit != nil {
		remaining := 100 - min(100, max(0, status.WeeklyLimit.UsedPercent))
		add(fmt.Sprintf("weekly %d%% left", remaining), 72)
	}
	if status.Usage != nil {
		add(formatSessionTUITokens(status.Usage.InputTokens)+" in", 50)
		add(formatSessionTUITokens(status.Usage.OutputTokens)+" out", 50)
	}
	if len(segments) == 0 {
		add(m.statusBarFallback(), 100)
	}
	return segments
}

func (m *sessionTUIModel) statusBarFallback() string {
	switch {
	case !m.ready:
		return "Connecting"
	case m.turnActive:
		return "Working"
	case m.pendingTurnID != "":
		return "Pending"
	default:
		return "Ready"
	}
}

func sessionTUIFitStatusSegments(segments []sessionTUIStatusSegment, width int) string {
	visible := append([]sessionTUIStatusSegment(nil), segments...)
	render := func() string {
		values := make([]string, 0, len(visible))
		for _, segment := range visible {
			values = append(values, segment.text)
		}
		return strings.Join(values, " · ")
	}
	for len(visible) > 1 && lipgloss.Width(render()) > width {
		remove := 0
		for index := 1; index < len(visible); index++ {
			if visible[index].priority <= visible[remove].priority {
				remove = index
			}
		}
		visible = append(visible[:remove], visible[remove+1:]...)
	}
	return truncateSessionTUIStatus(render(), width)
}

func truncateSessionTUIStatus(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	var truncated strings.Builder
	for _, character := range value {
		candidate := truncated.String() + string(character) + "…"
		if lipgloss.Width(candidate) > width {
			break
		}
		truncated.WriteRune(character)
	}
	return truncated.String() + "…"
}

func sessionTUIPath(workingDir, homeDir string) string {
	homeDir = strings.TrimSuffix(homeDir, "/")
	if homeDir == "" {
		return workingDir
	}
	if workingDir == homeDir {
		return "~"
	}
	if strings.HasPrefix(workingDir, homeDir+"/") {
		return "~/" + strings.TrimPrefix(workingDir, homeDir+"/")
	}
	return workingDir
}

func sessionTUIContextUsedPercent(usage sessionruntime.RuntimeUsage) int64 {
	const baselineTokens int64 = 12_000
	if usage.ContextWindow <= baselineTokens {
		return 100
	}
	effectiveWindow := usage.ContextWindow - baselineTokens
	used := max(int64(0), usage.ContextTokens-baselineTokens)
	return min(int64(100), (used*100+effectiveWindow/2)/effectiveWindow)
}

func formatSessionTUITokens(value int64) string {
	value = max(int64(0), value)
	if value < 1_000 {
		return strconv.FormatInt(value, 10)
	}
	scaled := float64(value)
	suffix := "K"
	switch {
	case value >= 1_000_000_000_000:
		scaled /= 1_000_000_000_000
		suffix = "T"
	case value >= 1_000_000_000:
		scaled /= 1_000_000_000
		suffix = "B"
	case value >= 1_000_000:
		scaled /= 1_000_000
		suffix = "M"
	default:
		scaled /= 1_000
	}
	decimals := 0
	if scaled < 10 {
		decimals = 2
	} else if scaled < 100 {
		decimals = 1
	}
	formatted := strconv.FormatFloat(scaled, 'f', decimals, 64)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	return formatted + suffix
}

func (m *sessionTUIModel) pendingView() string {
	if m.pendingTurnID == "" {
		return ""
	}
	lines := strings.Split(m.renderPendingUserBlock(m.pendingTurnText), "\n")
	progressHeight := 0
	if m.progressVisible() {
		progressHeight = 1
	}
	maxHeight := min(sessionTUIPendingMaxHeight, max(0, m.height-m.footerHeight()-progressHeight))
	if len(lines) > maxHeight {
		lines = lines[:maxHeight]
	}
	return strings.Join(lines, "\n")
}

func (m *sessionTUIModel) progressView() string {
	if !m.progressVisible() {
		return ""
	}
	label := ""
	started := m.activeTurnStarted
	showInterrupt := false
	switch {
	case m.connectionStatus == sessionTerminalStatusReconnecting:
		label = "Reconnecting"
		started = m.connectionStarted
	case m.connectionStatus == sessionTerminalStatusConnecting && !m.ready:
		label = "Connecting"
		started = m.connectionStarted
	case m.historyPageLoading:
		label = "Loading history"
		started = m.historyPageStarted
	case m.turnInterrupting:
		label = "Interrupting"
	case m.waitingForInput:
		label = "Waiting for input"
		showInterrupt = true
	default:
		label = "Working"
		showInterrupt = true
	}
	elapsed := m.now().Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}
	details := formatSessionTUIElapsed(elapsed)
	if showInterrupt {
		details += " • esc to interrupt"
	}
	text := truncateSessionTUIProgress("• "+label+" ("+details+")", max(1, m.width))
	return m.styles.pending.Width(max(1, m.width)).MaxWidth(max(1, m.width)).Render(text)
}

func formatSessionTUIElapsed(elapsed time.Duration) string {
	seconds := uint64(elapsed / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 60*60 {
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %02dm %02ds", seconds/(60*60), (seconds/60)%60, seconds%60)
}

func formatSessionTurnSeparatorText(elapsed string, width int) string {
	width = max(1, width)
	label := "─ Worked for " + elapsed + " "
	label = truncateSessionTUIStatus(label, width)
	return label + strings.Repeat("─", max(0, width-lipgloss.Width(label)))
}

func truncateSessionTUIProgress(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
