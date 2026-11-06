package logview

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

const (
	scanInitialBuffer = 64 * 1024
	scanMaxLine       = 10 * 1024 * 1024
)

// Parse reads Claude Code's NDJSON from r and calls emit for each event.
// Other agents can add adapters that emit the same Event types.
func Parse(r io.Reader, emit func(Event)) error {
	return scanLines(r, func(line []byte) {
		if !parseClaudeLine(line, emit) {
			emit(Raw{Line: string(line)})
		}
	})
}

// scanLines calls handle for every non-empty line of r.
func scanLines(r io.Reader, handle func([]byte)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, scanInitialBuffer), scanMaxLine)
	for scanner.Scan() {
		if line := scanner.Bytes(); len(line) > 0 {
			handle(line)
		}
	}
	return scanner.Err()
}

// contentText flattens a tool result content that is either a string or
// a list of {"type":"text","text":...} blocks.
func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var texts []string
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		texts = append(texts, block.Text)
	}
	return strings.Join(texts, "\n")
}
