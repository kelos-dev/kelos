package logview

import (
	"encoding/json"
	"strings"
)

// workspaceRoot is where agent pods check out the repository; paths under
// it are shown relative to it.
const workspaceRoot = "/workspace/repo/"

// summaryKeys are the tool input fields that best describe a call, most
// specific first.
var summaryKeys = []string{
	"file_path", "absolute_path", "notebook_path",
	"command", "pattern", "query", "path", "url", "description", "prompt",
}

// newToolCall describes a tool invocation from its name and JSON input.
func newToolCall(id, name string, input json.RawMessage) ToolCall {
	call := ToolCall{ID: id, Name: displayName(name)}
	var fields map[string]any
	if len(input) == 0 || json.Unmarshal(input, &fields) != nil {
		return call
	}
	call.Summary = inputSummary(fields)
	call.Diff = editHunks(fields)
	if content, ok := fields["content"].(string); ok && call.Name == "Write" {
		call.WriteLines = len(splitLines(content))
	}
	call.Todos = parseTodos(fields["todos"])
	return call
}

// displayName maps Claude's edit tools to the displayed action name.
func displayName(name string) string {
	if name == "Edit" || name == "MultiEdit" {
		return "Update"
	}
	return name
}

func inputSummary(fields map[string]any) string {
	for _, key := range summaryKeys {
		if value, ok := fields[key].(string); ok && value != "" {
			switch key {
			case "file_path", "absolute_path", "notebook_path", "path":
				return relativePath(value)
			default:
				return value
			}
		}
	}
	return ""
}

// editHunks returns the diff of an edit-style tool input, which carries
// the replaced text either directly or as a list of edits (MultiEdit).
func editHunks(fields map[string]any) []Hunk {
	if hunks, ok := replacement(fields); ok {
		return hunks
	}
	edits, _ := fields["edits"].([]any)
	var hunks []Hunk
	for _, edit := range edits {
		if editFields, ok := edit.(map[string]any); ok {
			if editHunks, ok := replacement(editFields); ok {
				hunks = append(hunks, editHunks...)
			}
		}
	}
	return hunks
}

func replacement(fields map[string]any) ([]Hunk, bool) {
	oldText, oldOK := fields["old_string"].(string)
	newText, newOK := fields["new_string"].(string)
	if oldOK && newOK {
		return replacementHunks(oldText, newText), true
	}
	return nil, false
}

// parseTodos reads a Claude Code TodoWrite input list.
func parseTodos(raw any) []Todo {
	items, _ := raw.([]any)
	var todos []Todo
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			continue
		}
		todo := Todo{Status: TodoPending}
		todo.Text, _ = fields["content"].(string)
		if status, ok := fields["status"].(string); ok && status != "" {
			todo.Status = TodoStatus(status)
		}
		todos = append(todos, todo)
	}
	return todos
}

// relativePath strips the workspace checkout prefix from paths.
func relativePath(value string) string {
	return strings.TrimPrefix(value, workspaceRoot)
}
