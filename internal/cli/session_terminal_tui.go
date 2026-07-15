package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"github.com/muesli/termenv"
)

const (
	sessionTUIDefaultWidth   = 80
	sessionTUIDefaultHeight  = 24
	sessionTUIComposerHeight = 3
	sessionTUIComposerGap    = 1
	sessionTUIFooterHeight   = sessionTUIComposerHeight + sessionTUIComposerGap
	sessionTUIQueueMaxHeight = 8
	sessionTUIIndent         = 2
	sessionTUIFrameInterval  = time.Second / 30
	sessionTUIReflowDelay    = 75 * time.Millisecond
)

// sessionTUIClearHistory resets the scroll region, screen, and scrollback before history is replayed.
const sessionTUIClearHistory = "\x1b[r\x1b[0m\x1b[H\x1b[2J\x1b[3J\x1b[H"

type sessionTUIBlockKind int

const (
	sessionTUIBlockUser sessionTUIBlockKind = iota
	sessionTUIBlockAssistant
	sessionTUIBlockTool
	sessionTUIBlockToolStatus
	sessionTUIBlockNotice
	sessionTUIBlockWarning
	sessionTUIBlockError
	sessionTUIBlockInput
	sessionTUIBlockDiff
)

type sessionTUIBlock struct {
	kind     sessionTUIBlockKind
	text     string
	stream   *strings.Builder
	rendered string
	dirty    bool
}

type sessionTUIEventResult struct {
	event sessionruntime.Event
	err   error
}

type sessionTUIRefreshMsg struct{}

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
	queuedTurns        map[string]string
	queuedTurnOrder    []string
	width              int
	height             int
	ready              bool
	streamingAt        int
	history            []string
	historyAt          int
	draft              string
	err                error
	refreshScheduled   bool
	activeView         string
	committed          int
	historyQueued      int
	historyWrites      []sessionTUIHistoryWrite
	historyWriting     bool
	nextHistoryID      uint64
	printHistory       func(string) tea.Cmd
	waitForTermination tea.Cmd
	sizeInitialized    bool
	reflowGeneration   int
	quitRequested      bool
	quitting           bool
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
	input := textarea.New()
	input.Prompt = "> "
	input.Placeholder = ""
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.MaxHeight = 1
	input.MaxWidth = 0
	input.SetHeight(1)
	input.SetWidth(sessionTUIDefaultWidth)
	input.FocusedStyle = sessionTUITextAreaStyle(styles.user)
	input.BlurredStyle = sessionTUITextAreaStyle(styles.user)

	model := &sessionTUIModel{
		events:             events,
		requests:           requests,
		input:              input,
		styles:             styles,
		queuedTurns:        make(map[string]string),
		width:              sessionTUIDefaultWidth,
		height:             sessionTUIDefaultHeight,
		historyAt:          -1,
		streamingAt:        -1,
		printHistory:       func(rendered string) tea.Cmd { return tea.Println(rendered) },
		waitForTermination: waitForTermination,
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
	return tea.Batch(m.readEvent(), m.waitForTermination)
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
		switch message.Type {
		case tea.KeyCtrlC:
			return m, m.quit()
		case tea.KeyEnter:
			if m.ready {
				return m, m.submitInput()
			}
			return m, nil
		case tea.KeyUp:
			if m.ready && !strings.Contains(m.input.Value(), "\n") {
				m.previousInput()
				return m, nil
			}
		case tea.KeyDown:
			if m.ready && !strings.Contains(m.input.Value(), "\n") {
				m.nextInput()
				return m, nil
			}
		}
	}

	if !m.ready || m.quitRequested {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(message)
	return m, cmd
}

func (m *sessionTUIModel) View() string {
	if m.quitting {
		return ""
	}
	footer := m.footerView()
	if m.activeView == "" {
		return "\n" + footer
	}
	return m.activeView + "\n\n" + footer
}

func (m *sessionTUIModel) readEvent() tea.Cmd {
	return func() tea.Msg {
		var event sessionruntime.Event
		err := m.events.Decode(&event)
		return sessionTUIEventResult{event: event, err: err}
	}
}

func (m *sessionTUIModel) submitInput() tea.Cmd {
	line := m.input.Value()
	if line == "/quit" || line == "/exit" {
		return m.quit()
	}
	request := sessionTerminalRequest(line)
	if request.Type == "" {
		m.input.Reset()
		return nil
	}
	if err := m.requests.Encode(request); err != nil {
		m.err = err
		return m.quit()
	}
	if request.Type != "input" {
		m.history = append(m.history, line)
	}
	m.historyAt = -1
	m.draft = ""
	m.input.Reset()
	return nil
}

func (m *sessionTUIModel) previousInput() {
	if len(m.history) == 0 {
		return
	}
	if m.historyAt == -1 {
		m.draft = m.input.Value()
		m.historyAt = len(m.history) - 1
	} else if m.historyAt > 0 {
		m.historyAt--
	}
	m.input.SetValue(m.history[m.historyAt])
	m.input.CursorEnd()
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
}

func (m *sessionTUIModel) applyEvent(event sessionruntime.Event) sessionTUICommands {
	var commands sessionTUICommands
	switch event.Type {
	case sessionruntime.EventHistoryEnd:
		m.appendBlock(sessionTUIBlockNotice, "Connected. Type a message, /interrupt, /answer INPUT QUESTION VALUE, or /quit.")
		m.ready = true
		commands.ui = m.input.Focus()
	case sessionruntime.EventRuntimeRecovered:
		m.appendBlock(sessionTUIBlockWarning, event.Text)
	case sessionTerminalEventDiagnostic:
		m.appendBlock(sessionTUIBlockWarning, event.Text)
	case sessionruntime.EventUserMessage:
		if event.TurnID == "" {
			m.finishStreaming()
			m.appendBlock(sessionTUIBlockUser, event.Text)
		} else {
			m.appendQueuedUser(event.TurnID, event.Text)
		}
	case sessionruntime.EventTurnStarted:
		m.acceptQueuedTurn(event.TurnID)
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
		m.appendBlock(sessionTUIBlockTool, event.ToolName)
	case sessionruntime.EventToolCompleted:
		m.appendBlock(sessionTUIBlockToolStatus, event.Status)
	case sessionruntime.EventInputRequested:
		m.finishStreaming()
		m.appendBlock(sessionTUIBlockInput, sessionTUIInputRequestText(event))
	case sessionruntime.EventInputResolved:
		m.appendBlock(sessionTUIBlockNotice, fmt.Sprintf("Input %s %s.", event.InputID, event.Status))
	case sessionruntime.EventTurnInterrupting:
		m.finishStreaming()
		m.appendBlock(sessionTUIBlockWarning, "Interrupting active work…")
	case sessionruntime.EventFileDiff:
		m.finishStreaming()
		m.appendBlock(sessionTUIBlockDiff, event.Diff)
	case sessionruntime.EventTurnCompleted:
		m.acceptQueuedTurn(event.TurnID)
		m.finishStreaming()
		if event.Status == "interrupted" {
			m.appendBlock(sessionTUIBlockWarning, "Turn interrupted.")
		}
	case sessionruntime.EventError:
		m.finishStreaming()
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

func (m *sessionTUIModel) scheduleRefresh() tea.Cmd {
	if m.refreshScheduled {
		return nil
	}
	m.refreshScheduled = true
	return tea.Tick(sessionTUIFrameInterval, func(time.Time) tea.Msg {
		return sessionTUIRefreshMsg{}
	})
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

func (m *sessionTUIModel) appendQueuedUser(turnID, text string) bool {
	if _, exists := m.queuedTurns[turnID]; exists {
		return false
	}
	m.queuedTurns[turnID] = text
	m.queuedTurnOrder = append(m.queuedTurnOrder, turnID)
	return true
}

func (m *sessionTUIModel) acceptQueuedTurn(turnID string) bool {
	text, exists := m.queuedTurns[turnID]
	if !exists {
		return false
	}
	delete(m.queuedTurns, turnID)
	for index, queuedTurnID := range m.queuedTurnOrder {
		if queuedTurnID == turnID {
			m.queuedTurnOrder = append(m.queuedTurnOrder[:index], m.queuedTurnOrder[index+1:]...)
			break
		}
	}
	m.finishStreaming()
	m.appendBlock(sessionTUIBlockUser, text)
	return true
}

func (m *sessionTUIModel) resize(width, height int) tea.Cmd {
	if width <= 0 || height <= 0 {
		return nil
	}
	changed := m.sizeInitialized && (m.width != width || m.height != height)
	m.width = width
	m.height = height
	m.input.SetWidth(width)
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
	m.activeView = m.renderBlockRange(m.committed, len(m.blocks))
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
		return m.renderIndentedBlock(text, sessionTUIIndent, m.styles.base)
	case sessionTUIBlockTool:
		return m.renderIndentedBlock("↳ "+text, sessionTUIIndent, m.styles.tool)
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
	default:
		return ""
	}
}

func (m *sessionTUIModel) renderUserBlock(text string) string {
	return m.renderUserBlockWithStatus(text, "")
}

func (m *sessionTUIModel) renderQueuedUserBlock(text string) string {
	return m.renderUserBlockWithStatus(text, "Queued")
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
	middle = m.renderUserRow(middle)
	return blank + "\n" + middle + "\n" + blank
}

func (m *sessionTUIModel) footerView() string {
	queue := m.queueView()
	if queue == "" {
		return m.composerView()
	}
	return queue + "\n" + m.composerView()
}

func (m *sessionTUIModel) queueView() string {
	blocks := make([]string, 0, len(m.queuedTurnOrder))
	for _, turnID := range m.queuedTurnOrder {
		blocks = append(blocks, m.renderQueuedUserBlock(m.queuedTurns[turnID]))
	}
	lines := strings.Split(strings.Join(blocks, "\n"), "\n")
	maxHeight := min(sessionTUIQueueMaxHeight, max(0, m.height-sessionTUIFooterHeight))
	if len(lines) > maxHeight {
		lines = lines[:maxHeight]
	}
	return strings.Join(lines, "\n")
}
