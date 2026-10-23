package sessionruntime

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const goalUsage = "usage: /goal [<objective>|clear|edit <objective>|pause|resume]"
const loopUsage = "usage: /loop [<interval> <prompt>|pause|resume|clear] (interval must be at least 1s, for example 5m)"

type sessionCommandKind string

const (
	sessionCommandMessage sessionCommandKind = "message"
	sessionCommandShell   sessionCommandKind = "shell"
	sessionCommandGoal    sessionCommandKind = "goal"
	sessionCommandLoop    sessionCommandKind = "loop"
)

type sessionCommand struct {
	kind sessionCommandKind
	text string
	goal goalCommand
	loop loopCommand
}

type goalCommandAction string

const (
	goalCommandShow   goalCommandAction = "show"
	goalCommandCreate goalCommandAction = "create"
	goalCommandEdit   goalCommandAction = "edit"
	goalCommandClear  goalCommandAction = "clear"
	goalCommandPause  goalCommandAction = "pause"
	goalCommandResume goalCommandAction = "resume"
)

type goalCommand struct {
	action    goalCommandAction
	objective string
}

func parseSessionCommand(text, agentType string) (sessionCommand, error) {
	trimmed := strings.TrimSpace(text)
	if hasCommandPrefix(trimmed, "/loop") {
		return parseLoopCommand(strings.TrimSpace(strings.TrimPrefix(trimmed, "/loop")))
	}
	if strings.HasPrefix(trimmed, "!") {
		command := strings.TrimSpace(strings.TrimPrefix(trimmed, "!"))
		if command == "" {
			return sessionCommand{}, errors.New("shell command must not be empty")
		}
		return sessionCommand{kind: sessionCommandShell, text: command}, nil
	}
	if !hasCommandPrefix(trimmed, "/goal") {
		return sessionCommand{kind: sessionCommandMessage, text: text}, nil
	}

	arguments := strings.TrimSpace(strings.TrimPrefix(trimmed, "/goal"))
	if arguments == "" {
		return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandShow}}, nil
	}
	fields := strings.Fields(arguments)
	if agentType == "claude-code" {
		if len(fields) == 1 {
			switch strings.ToLower(fields[0]) {
			case "clear", "stop", "off", "reset", "none", "cancel":
				return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandClear}}, nil
			}
		}
		return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandCreate, objective: arguments}}, nil
	}
	switch fields[0] {
	case "clear":
		if len(fields) == 1 {
			return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandClear}}, nil
		}
	case "pause":
		if len(fields) == 1 {
			return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandPause}}, nil
		}
	case "resume":
		if len(fields) == 1 {
			return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandResume}}, nil
		}
	case "edit":
		objective := strings.TrimSpace(strings.TrimPrefix(arguments, fields[0]))
		if objective == "" {
			return sessionCommand{}, errors.New(goalUsage)
		}
		return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandEdit, objective: objective}}, nil
	}
	return sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandCreate, objective: arguments}}, nil
}

type loopCommand struct {
	action   string
	interval time.Duration
	prompt   string
}

func parseLoopCommand(arguments string) (sessionCommand, error) {
	command := loopCommand{action: "show"}
	if arguments == "" {
		return sessionCommand{kind: sessionCommandLoop, loop: command}, nil
	}
	fields := strings.Fields(arguments)
	if len(fields) == 1 && (fields[0] == "pause" || fields[0] == "resume" || fields[0] == "clear") {
		command.action = fields[0]
		return sessionCommand{kind: sessionCommandLoop, loop: command}, nil
	}
	interval, err := time.ParseDuration(fields[0])
	if err != nil || interval < time.Second || len(fields) < 2 {
		return sessionCommand{}, errors.New(loopUsage)
	}
	command.action = "create"
	command.interval = interval
	command.prompt = strings.TrimSpace(strings.TrimPrefix(arguments, fields[0]))
	return sessionCommand{kind: sessionCommandLoop, loop: command}, nil
}

func hasCommandPrefix(text, command string) bool {
	if text == command {
		return true
	}
	if !strings.HasPrefix(text, command) || len(text) == len(command) {
		return false
	}
	value, _ := utf8.DecodeRuneInString(text[len(command):])
	return unicode.IsSpace(value)
}
