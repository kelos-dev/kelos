package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const (
	sessionTerminalEventDiagnostic      = "terminal.diagnostic"
	sessionTerminalEventAttachmentAdded = "terminal.attachment-added"
	sessionTerminalRequestAttachment    = "terminal.attach"

	sessionTerminalStatusConnected    = "connected"
	sessionTerminalStatusConnecting   = "connecting"
	sessionTerminalStatusReconnecting = "reconnecting"

	sessionANSIReset   = "\x1b[0m"
	sessionANSIBold    = "\x1b[1m"
	sessionANSIDim     = "\x1b[2m"
	sessionANSIRed     = "\x1b[31m"
	sessionANSIGreen   = "\x1b[32m"
	sessionANSIYellow  = "\x1b[33m"
	sessionANSIBlue    = "\x1b[34m"
	sessionANSICyan    = "\x1b[36m"
	sessionANSIReverse = "\x1b[7m"
)

type sessionTerminalFormatter struct {
	color bool
}

type sessionPlainAssistantState struct {
	streaming bool
	lineOpen  bool
}

type sessionPlainToolState struct {
	streamed bool
	lineOpen bool
}

func (f sessionTerminalFormatter) style(style, text string) string {
	if !f.color || style == "" || text == "" {
		return text
	}
	return style + text + sessionANSIReset
}

func (f sessionTerminalFormatter) userMessage(text string) string {
	if !f.color {
		return "you › " + text
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = f.style(sessionANSIReverse, "  "+lines[i]+"  ")
	}
	return strings.Join(lines, "\n")
}

func (f sessionTerminalFormatter) assistantPrefix() string {
	if f.color {
		return ""
	}
	return "agent › "
}

func (f sessionTerminalFormatter) tool(name string) string {
	return "  " + f.style(sessionANSIBold+sessionANSICyan, "↳ "+name)
}

func (f sessionTerminalFormatter) toolStatus(status string) string {
	return "    " + f.status(status)
}

func (f sessionTerminalFormatter) status(status string) string {
	var style string
	switch strings.ToLower(status) {
	case "completed", "success", "answered":
		style = sessionANSIGreen
	case "failed", "error", "cancelled", "canceled":
		style = sessionANSIRed
	case "running", "pending", "interrupting", "interrupted":
		style = sessionANSIYellow
	default:
		style = sessionANSIDim
	}
	return f.style(style, status)
}

func (f sessionTerminalFormatter) muted(text string) string {
	return f.style(sessionANSIDim, text)
}

func (f sessionTerminalFormatter) turnSeparator(elapsed time.Duration, width int) string {
	return f.muted(formatSessionTurnSeparator(elapsed, width))
}

func (f sessionTerminalFormatter) warning(text string) string {
	return f.style(sessionANSIBold+sessionANSIYellow, text)
}

func (f sessionTerminalFormatter) inputHeading(text string) string {
	return f.style(sessionANSIBold+sessionANSIBlue, text)
}

func (f sessionTerminalFormatter) accent(text string) string {
	return f.style(sessionANSICyan, text)
}

func (f sessionTerminalFormatter) error(text string) string {
	return f.style(sessionANSIBold+sessionANSIRed, text)
}

func (f sessionTerminalFormatter) diff(diff string) string {
	if !f.color {
		return diff
	}
	lines := strings.Split(diff, "\n")
	for i, line := range lines {
		var style string
		switch {
		case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "index "):
			style = sessionANSIDim
		case strings.HasPrefix(line, "@@"):
			style = sessionANSICyan
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			style = sessionANSIBold
		case strings.HasPrefix(line, "+"):
			style = sessionANSIGreen
		case strings.HasPrefix(line, "-"):
			style = sessionANSIRed
		}
		lines[i] = f.style(style, line)
	}
	return strings.Join(lines, "\n")
}

func runSessionTerminal(ctx context.Context, input io.Reader, output io.Writer, events io.Reader, requests io.Writer, color bool) error {
	encoder := json.NewEncoder(requests)
	if err := encoder.Encode(sessionruntime.ClientRequest{Type: "subscribe"}); err != nil {
		return err
	}

	if sessionTerminalIsInteractive(input, output) {
		return runSessionTUI(ctx, input, output, json.NewDecoder(events), encoder, color)
	}
	return runSessionPlainTerminal(ctx, input, output, json.NewDecoder(events), encoder, color)
}

func sessionTerminalIsInteractive(input io.Reader, output io.Writer) bool {
	if !sessionTerminalSupportsTUI(os.Getenv("TERM")) {
		return false
	}
	inputFile, inputOK := input.(*os.File)
	outputFile, outputOK := output.(*os.File)
	return inputOK && outputOK && term.IsTerminal(int(inputFile.Fd())) && term.IsTerminal(int(outputFile.Fd()))
}

func sessionTerminalSupportsTUI(termType string) bool {
	return !strings.EqualFold(termType, "dumb")
}

func sessionTerminalDiagnosticsUseTUI(input io.Reader, output, diagnostics io.Writer) bool {
	if !sessionTerminalIsInteractive(input, output) {
		return false
	}
	outputFile, outputOK := output.(*os.File)
	diagnosticFile, diagnosticOK := diagnostics.(*os.File)
	if !outputOK || !diagnosticOK {
		return false
	}
	outputInfo, outputErr := outputFile.Stat()
	diagnosticInfo, diagnosticErr := diagnosticFile.Stat()
	return outputErr == nil && diagnosticErr == nil && os.SameFile(outputInfo, diagnosticInfo)
}

func runSessionPlainTerminal(ctx context.Context, input io.Reader, output io.Writer, decoder *json.Decoder, encoder *json.Encoder, color bool) error {
	return runSessionPlainTerminalWithWidth(ctx, input, output, decoder, encoder, color, func() int {
		return sessionPlainTerminalWidth(output)
	})
}

func runSessionPlainTerminalWithWidth(ctx context.Context, input io.Reader, output io.Writer, decoder *json.Decoder, encoder *json.Encoder, color bool, terminalWidth func() int) error {
	var writeMu sync.Mutex
	var historyMu sync.Mutex
	var attachmentMu sync.Mutex
	pendingAttachments := make([]sessionruntime.Attachment, 0)
	historyCursor := ""
	pendingHistoryCursor := ""
	historyLoading := false
	historyRequestID := ""
	initialHistorySeen := false
	write := func(format string, args ...any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		fmt.Fprintf(output, format, args...)
	}
	formatter := sessionTerminalFormatter{color: color}
	done := make(chan error, 1)
	go func() {
		liveAssistant := sessionPlainAssistantState{}
		pageAssistant := sessionPlainAssistantState{}
		closeAssistantLine := func(assistant *sessionPlainAssistantState) {
			if assistant.lineOpen {
				write("\n")
				assistant.lineOpen = false
			}
		}
		finishAssistant := func(assistant *sessionPlainAssistantState) {
			closeAssistantLine(assistant)
			assistant.streaming = false
		}
		replayingHistory := true
		historyPageReading := false
		recoveryActive := false
		activeTurnID := ""
		streamedTools := make(map[string]sessionPlainToolState)
		var activeTurnStarted time.Time
		for {
			var event sessionruntime.Event
			if err := decoder.Decode(&event); err != nil {
				done <- err
				return
			}
			if historyPageReading && event.Type == sessionruntime.EventHistoryStart && !event.HistoryPage {
				finishAssistant(&pageAssistant)
				historyPageReading = false
				historyMu.Lock()
				pendingHistoryCursor = ""
				historyLoading = false
				historyRequestID = ""
				historyMu.Unlock()
			}
			pageEvent := historyPageReading || (event.Type == sessionruntime.EventHistoryStart && event.HistoryPage)
			assistant := &liveAssistant
			if pageEvent {
				assistant = &pageAssistant
			}
			recoveredCompletion := false
			if !pageEvent {
				recoveredCompletion = sessionTerminalRecoveredCompletion(recoveryActive, event)
				if recoveryActive && !sessionTerminalRuntimeRecoveryEvent(event) {
					recoveryActive = false
				}
			}
			switch event.Type {
			case sessionruntime.EventHistoryStart:
				replayingHistory = true
				historyMu.Lock()
				if event.HistoryPage {
					pendingHistoryCursor = event.HistoryCursor
				} else if event.Reset || !initialHistorySeen {
					historyCursor = event.HistoryCursor
				}
				if !event.HistoryPage {
					initialHistorySeen = true
				}
				historyMu.Unlock()
				if event.Reset {
					finishAssistant(&liveAssistant)
					activeTurnID = ""
					activeTurnStarted = time.Time{}
				}
				if event.HistoryPage {
					closeAssistantLine(&liveAssistant)
					pageAssistant = sessionPlainAssistantState{}
					historyPageReading = true
					write("\n%s\n", formatter.muted("Earlier Session history:"))
				}
			case sessionruntime.EventHistoryEnd:
				replayingHistory = false
				if event.HistoryPage {
					finishAssistant(&pageAssistant)
					historyMu.Lock()
					historyCursor = pendingHistoryCursor
					pendingHistoryCursor = ""
					if event.RequestID == historyRequestID {
						historyLoading = false
						historyRequestID = ""
					}
					historyMu.Unlock()
					historyPageReading = false
					write("\n")
					continue
				}
				if event.HistoryState != nil {
					activeTurnID = event.HistoryState.ActiveTurnID
					activeTurnStarted = time.Time{}
					if event.HistoryState.ActiveTurnStarted != nil {
						activeTurnStarted = *event.HistoryState.ActiveTurnStarted
					}
				}
				closeAssistantLine(&liveAssistant)
				liveAssistant.streaming = liveAssistant.streaming && activeTurnID != ""
				if event.HistoryState != nil && event.HistoryState.PendingTurn != nil {
					write("\n%s\n", formatter.muted("Pending message:"))
					turn := event.HistoryState.PendingTurn
					write("%s\n", formatter.userMessage(sessionTerminalMessageText(turn.Text, turn.Attachments)))
				}
				historyMu.Lock()
				hasEarlierHistory := historyCursor != ""
				historyMu.Unlock()
				if hasEarlierHistory {
					write("\n%s\n", formatter.muted("Earlier Session history is available. Use /history to load the previous page."))
				}
				write("\n%s\n\n", formatter.muted("Connected. Type a message, !COMMAND, /goal, /loop, /attach PATH, /history, /interrupt, /answer INPUT QUESTION VALUE, or /quit."))
			case sessionruntime.EventRuntimeRecovered:
				if !pageEvent {
					recoveryActive = true
				}
				finishAssistant(assistant)
				write("%s\n", formatter.warning(event.Text))
			case sessionruntime.EventUserMessage, sessionruntime.EventUserMessageUpdated:
				finishAssistant(assistant)
				write("%s\n", formatter.userMessage(sessionTerminalMessageText(event.Text, event.Attachments)))
				if color {
					write("\n")
				}
			case sessionruntime.EventUserMessageRemoved:
				write("%s\n", formatter.muted("Pending message removed."))
			case sessionTerminalEventAttachmentAdded:
				attachmentMu.Lock()
				pendingAttachments = append(pendingAttachments, event.Attachments...)
				attachmentMu.Unlock()
				for _, attachment := range event.Attachments {
					write("%s\n", formatter.muted(fmt.Sprintf("Attached %s (%d bytes). Send a message to include it.", attachment.Name, attachment.SizeBytes)))
				}
			case sessionruntime.EventTurnStarted:
				if pageEvent {
					continue
				}
				if activeTurnID != event.TurnID || activeTurnStarted.IsZero() {
					activeTurnStarted = sessionTerminalTurnStartedAt(event, time.Now(), replayingHistory)
				}
				activeTurnID = event.TurnID
			case sessionruntime.EventAssistantDelta:
				if !assistant.lineOpen {
					write("%s", formatter.assistantPrefix())
					assistant.lineOpen = true
				}
				assistant.streaming = true
				write("%s", event.Text)
			case sessionruntime.EventAssistantMessage:
				if assistant.streaming {
					finishAssistant(assistant)
				} else if event.Text != "" {
					write("%s%s\n", formatter.assistantPrefix(), event.Text)
				}
			case sessionruntime.EventToolStarted:
				finishAssistant(assistant)
				write("%s\n", formatter.tool(sanitizeSessionTUIToolOutput(event.ToolName)))
			case sessionruntime.EventToolDelta:
				output := sanitizeSessionTUIToolOutput(event.Output)
				if output != "" {
					streamedTools[event.ToolID] = sessionPlainToolState{streamed: true, lineOpen: !strings.HasSuffix(output, "\n")}
				}
				write("%s", output)
			case sessionruntime.EventToolCompleted:
				if state := streamedTools[event.ToolID]; state.streamed {
					if state.lineOpen {
						write("\n")
					}
					delete(streamedTools, event.ToolID)
				} else if event.Output != "" {
					write("%s\n", sanitizeSessionTUIToolOutput(event.Output))
				}
				write("%s\n", formatter.toolStatus(event.Status))
			case sessionruntime.EventGoalUpdated:
				finishAssistant(assistant)
				write("%s\n", formatter.muted(sessionGoalText(event.Goal, event.Status)))
			case sessionruntime.EventLoopUpdated:
				finishAssistant(assistant)
				write("%s\n", formatter.muted(sessionLoopText(event.Loop, event.Status)))
			case sessionruntime.EventInputRequested:
				finishAssistant(assistant)
				write("\n%s\n", formatter.inputHeading(fmt.Sprintf("Input %s requested:", event.InputID)))
				for _, question := range event.Questions {
					write("  %s — %s\n", formatter.accent(question.ID), question.Question)
					for _, option := range question.Options {
						write("%s\n", formatter.muted(fmt.Sprintf("    %s — %s", option.Label, option.Description)))
					}
				}
				write("%s\n", formatter.muted(fmt.Sprintf("Use /answer %s QUESTION_ID VALUE, or /cancel-input %s. Separate multiple values with commas.", event.InputID, event.InputID)))
			case sessionruntime.EventInputResolved:
				write("\nInput %s %s.\n", event.InputID, formatter.status(event.Status))
			case sessionruntime.EventTurnInterrupting:
				finishAssistant(assistant)
				write("\n%s\n", formatter.warning("Interrupting active work…"))
			case sessionruntime.EventFileDiff:
				finishAssistant(assistant)
				write("\n%s\n%s\n", formatter.accent("--- file changes ---"), formatter.diff(event.Diff))
			case sessionruntime.EventTurnCompleted:
				finishAssistant(assistant)
				if event.Status == "interrupted" {
					write("%s\n", formatter.warning("Turn interrupted."))
				}
				if elapsed, ok := sessionTerminalTurnElapsed(activeTurnID, activeTurnStarted, event, time.Now(), replayingHistory, recoveredCompletion); ok {
					write("%s\n\n", formatter.turnSeparator(elapsed, terminalWidth()))
					activeTurnID = ""
					activeTurnStarted = time.Time{}
				} else if !replayingHistory {
					write("\n")
				}
			case sessionruntime.EventError:
				finishAssistant(assistant)
				historyMu.Lock()
				if event.RequestID != "" && event.RequestID == historyRequestID {
					historyLoading = false
					historyRequestID = ""
					pendingHistoryCursor = ""
				}
				historyMu.Unlock()
				write("%s\n", formatter.error("error: "+event.Text))
			}
		}
	}()

	terminalCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan string)
	inputDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-terminalCtx.Done():
				return
			}
		}
		inputDone <- scanner.Err()
		close(lines)
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case line, ok := <-lines:
			if !ok {
				return <-inputDone
			}
			if line == "/quit" || line == "/exit" {
				return nil
			}
			if strings.TrimSpace(line) == "/history" {
				historyMu.Lock()
				if historyLoading {
					historyMu.Unlock()
					write("%s\n", formatter.muted("Session history is already loading."))
					continue
				}
				cursor := historyCursor
				if cursor == "" {
					historyMu.Unlock()
					write("%s\n", formatter.muted("All retained Session history is loaded."))
					continue
				}
				requestID := string(uuid.NewUUID())
				historyLoading = true
				historyRequestID = requestID
				historyMu.Unlock()
				if err := encoder.Encode(sessionruntime.ClientRequest{Type: "history", RequestID: requestID, HistoryCursor: cursor}); err != nil {
					return err
				}
				continue
			}
			request := sessionTerminalRequest(line)
			if request.Type == "" {
				continue
			}
			if request.Type == "message" {
				attachmentMu.Lock()
				if request.Text == "" && len(pendingAttachments) == 0 {
					attachmentMu.Unlock()
					continue
				}
				request.AttachmentIDs = sessionAttachmentIDs(pendingAttachments)
				if err := encoder.Encode(request); err != nil {
					attachmentMu.Unlock()
					return err
				}
				pendingAttachments = nil
				attachmentMu.Unlock()
				continue
			}
			if err := encoder.Encode(request); err != nil {
				return err
			}
		}
	}
}

func sessionPlainTerminalWidth(output io.Writer) int {
	file, ok := output.(*os.File)
	if !ok {
		return sessionTUIDefaultWidth
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil || width <= 0 {
		return sessionTUIDefaultWidth
	}
	return width
}

func sessionTerminalEventTime(event sessionruntime.Event, fallback time.Time) time.Time {
	if event.Timestamp != nil {
		return *event.Timestamp
	}
	return fallback
}

func sessionTerminalTurnStartedAt(event sessionruntime.Event, fallback time.Time, replayingHistory bool) time.Time {
	if event.Timestamp != nil {
		return *event.Timestamp
	}
	if replayingHistory {
		return time.Time{}
	}
	return fallback
}

func sessionTerminalRecoveredCompletion(recoveryActive bool, event sessionruntime.Event) bool {
	return recoveryActive && event.Type == sessionruntime.EventTurnCompleted && event.Status == "interrupted"
}

func sessionTerminalRuntimeRecoveryEvent(event sessionruntime.Event) bool {
	return event.Type == sessionruntime.EventRuntimeRecovered ||
		(event.Type == sessionruntime.EventInputResolved && event.Status == "cancelled") ||
		(event.Type == sessionruntime.EventTurnCompleted && event.Status == "interrupted")
}

func sessionTerminalTurnElapsed(activeTurnID string, started time.Time, event sessionruntime.Event, fallback time.Time, replayingHistory, recoveredCompletion bool) (time.Duration, bool) {
	if started.IsZero() || recoveredCompletion || (replayingHistory && event.Timestamp == nil) || (event.TurnID != "" && activeTurnID != "" && event.TurnID != activeTurnID) {
		return 0, false
	}
	elapsed := sessionTerminalEventTime(event, fallback).Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed, true
}

func formatSessionTurnSeparator(elapsed time.Duration, width int) string {
	return formatSessionTurnSeparatorText(formatSessionTUIElapsed(elapsed), width)
}

func sessionTerminalRequest(line string) sessionruntime.ClientRequest {
	line = strings.TrimSpace(line)
	if line == "/send" {
		return sessionruntime.ClientRequest{Type: "message"}
	}
	if strings.HasPrefix(line, "/attach ") {
		path := strings.TrimSpace(strings.TrimPrefix(line, "/attach "))
		if path != "" {
			return sessionruntime.ClientRequest{Type: sessionTerminalRequestAttachment, Text: path}
		}
		return sessionruntime.ClientRequest{}
	}
	if line == "/interrupt" {
		return sessionruntime.ClientRequest{Type: "interrupt"}
	}
	if parts := strings.Fields(line); len(parts) == 2 && parts[0] == "/cancel-input" {
		return sessionruntime.ClientRequest{Type: "input", InputID: parts[1], Cancel: true}
	}
	if parts := strings.SplitN(line, " ", 4); len(parts) == 4 && parts[0] == "/answer" {
		values := make([]string, 0)
		for _, value := range strings.Split(parts[3], ",") {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
		if len(values) > 0 {
			return sessionruntime.ClientRequest{
				Type:    "input",
				InputID: parts[1],
				Answers: map[string][]string{parts[2]: values},
			}
		}
		return sessionruntime.ClientRequest{}
	}
	if line == "" {
		return sessionruntime.ClientRequest{}
	}
	return sessionruntime.ClientRequest{Type: "message", Text: line}
}

func sessionGoalText(goal *sessionruntime.Goal, status string) string {
	if goal == nil {
		if status == "cleared" {
			return "Goal cleared."
		}
		return "No goal is currently set."
	}
	usage := ""
	if goal.TokenBudget != nil {
		usage = fmt.Sprintf(" (%d/%d tokens)", goal.TokensUsed, *goal.TokenBudget)
	} else if goal.TokensUsed > 0 {
		usage = fmt.Sprintf(" (%d tokens)", goal.TokensUsed)
	}
	return fmt.Sprintf("Goal %s%s: %s", goal.Status, usage, goal.Objective)
}

func sessionLoopText(loop *sessionruntime.Loop, status string) string {
	if loop == nil {
		if status == "cleared" {
			return "Loop cleared."
		}
		return "No loop is currently set."
	}
	return fmt.Sprintf("Loop %s (every %s): %s", loop.Status, loop.Interval, loop.Prompt)
}

func sessionTerminalMessageText(text string, attachments []sessionruntime.Attachment) string {
	if len(attachments) == 0 {
		return text
	}
	names := make([]string, len(attachments))
	for index := range attachments {
		names[index] = attachments[index].Name
	}
	attachmentText := "Attachments: " + strings.Join(names, ", ")
	if strings.TrimSpace(text) == "" {
		return attachmentText
	}
	return text + "\n" + attachmentText
}

func sessionAttachmentIDs(attachments []sessionruntime.Attachment) []string {
	ids := make([]string, len(attachments))
	for index := range attachments {
		ids[index] = attachments[index].ID
	}
	return ids
}
