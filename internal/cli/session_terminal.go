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

	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"golang.org/x/term"
)

const (
	sessionTerminalEventDiagnostic = "terminal.diagnostic"

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
	var writeMu sync.Mutex
	write := func(format string, args ...any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		fmt.Fprintf(output, format, args...)
	}
	formatter := sessionTerminalFormatter{color: color}
	done := make(chan error, 1)
	go func() {
		streaming := false
		for {
			var event sessionruntime.Event
			if err := decoder.Decode(&event); err != nil {
				done <- err
				return
			}
			switch event.Type {
			case sessionruntime.EventHistoryEnd:
				write("\n%s\n\n", formatter.muted("Connected. Type a message, /interrupt, /answer INPUT QUESTION VALUE, or /quit."))
			case sessionruntime.EventRuntimeRecovered:
				if streaming {
					write("\n")
					streaming = false
				}
				write("%s\n", formatter.warning(event.Text))
			case sessionruntime.EventUserMessage:
				if streaming {
					write("\n")
					streaming = false
				}
				write("%s\n", formatter.userMessage(event.Text))
				if color {
					write("\n")
				}
			case sessionruntime.EventAssistantDelta:
				if !streaming {
					write("%s", formatter.assistantPrefix())
					streaming = true
				}
				write("%s", event.Text)
			case sessionruntime.EventAssistantMessage:
				if !streaming && event.Text != "" {
					write("%s%s", formatter.assistantPrefix(), event.Text)
				}
			case sessionruntime.EventToolStarted:
				if streaming {
					write("\n")
					streaming = false
				}
				write("%s\n", formatter.tool(event.ToolName))
			case sessionruntime.EventToolCompleted:
				write("%s\n", formatter.toolStatus(event.Status))
			case sessionruntime.EventInputRequested:
				if streaming {
					write("\n")
					streaming = false
				}
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
				if streaming {
					write("\n")
					streaming = false
				}
				write("\n%s\n", formatter.warning("Interrupting active work…"))
			case sessionruntime.EventFileDiff:
				if streaming {
					write("\n")
					streaming = false
				}
				write("\n%s\n%s\n", formatter.accent("--- file changes ---"), formatter.diff(event.Diff))
			case sessionruntime.EventTurnCompleted:
				if streaming {
					write("\n")
					streaming = false
				}
				if event.Status == "interrupted" {
					write("%s\n", formatter.warning("Turn interrupted."))
				}
				write("\n")
			case sessionruntime.EventError:
				if streaming {
					write("\n")
					streaming = false
				}
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
			request := sessionTerminalRequest(line)
			if request.Type == "" {
				continue
			}
			if err := encoder.Encode(request); err != nil {
				return err
			}
		}
	}
}

func sessionTerminalRequest(line string) sessionruntime.ClientRequest {
	line = strings.TrimSpace(line)
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
