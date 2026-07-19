package capture

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kelos-dev/kelos/internal/claudecode"
)

// usageAccumulator consumes the agent's JSON-lines output one line at a
// time and emits a final token usage map. Per-agent implementations keep
// only the state they need (last "result" line or running counters), so
// memory cost is O(1) in the size of the stream.
type usageAccumulator interface {
	addLine(line []byte)
	result() map[string]string
}

// newUsageAccumulator returns the accumulator for the given agent type, or
// nil if the type is empty or unknown.
func newUsageAccumulator(agentType string) usageAccumulator {
	switch agentType {
	case "claude-code":
		return &lastResultAccumulator{extract: extractClaudeCode}
	case "codex":
		return &sumAccumulator{event: "turn.completed", extract: extractCodexUsage, extractResponse: extractCodexResponse}
	case "gemini":
		return &lastResultAccumulator{extract: extractGemini, extractResponse: extractGeminiResponse}
	case "opencode":
		return &sumAccumulator{event: "step_finish", extract: extractOpencodeUsage, extractResponse: extractOpencodeResponse}
	case "cursor":
		return &lastResultAccumulator{extract: extractCursor}
	default:
		return nil
	}
}

// StreamUsage reads JSON lines from r, forwards every byte to w (with a
// final newline appended if the input did not end in one, matching what
// `tee` produced previously), and returns the parsed token usage for the
// given agent type. Returns nil usage for empty/unknown agent types or
// when no usage data is present. For Claude Code, an incomplete final result
// returns the parsed usage together with a non-nil error.
//
// Bytes from r are forwarded to w exactly as read, in order — there is no
// scanner buffer between us and r, so an arbitrarily long line is still
// forwarded faithfully (memory cost is bounded by the longest line, which
// the agent producer is already holding).
func StreamUsage(agentType string, r io.Reader, w io.Writer) (usage map[string]string, err error) {
	acc := newUsageAccumulator(agentType)
	bw := bufio.NewWriter(w)
	defer func() {
		if ferr := bw.Flush(); err == nil {
			err = ferr
		}
	}()
	br := bufio.NewReader(r)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := bw.Write(line); werr != nil {
				return nil, werr
			}
			// ReadBytes returns the bytes up to and including the
			// delimiter; if the stream ended without a newline,
			// append one so forwarded output matches what `tee`
			// produced.
			if line[len(line)-1] != '\n' {
				if werr := bw.WriteByte('\n'); werr != nil {
					return nil, werr
				}
			}
			if acc != nil {
				body := line
				if body[len(body)-1] == '\n' {
					body = body[:len(body)-1]
				}
				if len(body) > 0 {
					acc.addLine(body)
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, readErr
		}
	}
	if acc == nil {
		return nil, nil
	}
	usage = acc.result()
	if agentType == "claude-code" {
		lastResultAccumulator, ok := acc.(*lastResultAccumulator)
		if ok && lastResultAccumulator.last != nil {
			completion := extractClaudeCodeCompletion(lastResultAccumulator.last)
			if completion.Status() != claudecode.ResultCompleted {
				return usage, errors.New(completion.FailureMessage())
			}
		}
	}
	return usage, nil
}

// lastResultAccumulator keeps only the most recent line whose "type" is
// "result" and runs extract on it at EOF. Used by claude-code, gemini, and
// cursor whose totals live in the final result line.
type lastResultAccumulator struct {
	last            map[string]any
	extract         func(map[string]any) map[string]string
	extractResponse func(map[string]any) string
	response        string
}

func (a *lastResultAccumulator) addLine(line []byte) {
	m := parseLine(line)
	if m == nil {
		return
	}
	if m["type"] == "result" {
		a.last = m
	}
	if a.extractResponse != nil {
		if r := a.extractResponse(m); r != "" {
			a.response = r
		}
	}
}

func (a *lastResultAccumulator) result() map[string]string {
	if a.last == nil && a.response == "" {
		return nil
	}
	var r map[string]string
	if a.last != nil {
		r = a.extract(a.last)
	}
	if a.response != "" {
		if r == nil {
			r = make(map[string]string)
		}
		if _, ok := r["response"]; !ok {
			r["response"] = base64.StdEncoding.EncodeToString([]byte(a.response))
		}
	}
	return r
}

// sumAccumulator adds per-event input/output token counts as the stream is
// read. Used by codex and opencode whose totals are spread across many
// per-turn or per-step events.
type sumAccumulator struct {
	event           string
	extract         func(map[string]any) (int64, int64)
	extractResponse func(map[string]any) string
	in, out         int64
	response        string
}

func (a *sumAccumulator) addLine(line []byte) {
	m := parseLine(line)
	if m == nil {
		return
	}
	if m["type"] == a.event {
		i, o := a.extract(m)
		a.in += i
		a.out += o
	}
	if a.extractResponse != nil {
		if r := a.extractResponse(m); r != "" {
			a.response = r
		}
	}
}

func (a *sumAccumulator) result() map[string]string {
	r := tokenResult(a.in, a.out)
	if a.response != "" {
		if r == nil {
			r = make(map[string]string)
		}
		r["response"] = base64.StdEncoding.EncodeToString([]byte(a.response))
	}
	return r
}

// extractClaudeCode reads cost, token counts, and the agent response from a claude-code
// {"type":"result","total_cost_usd":N,"usage":{"input_tokens":N,"output_tokens":N},"result":"..."} line.
func extractClaudeCode(m map[string]any) map[string]string {
	result := make(map[string]string)
	if v, ok := m["total_cost_usd"]; ok {
		result["cost-usd"] = formatNumber(v)
	}
	if usage, ok := m["usage"].(map[string]any); ok {
		if v, ok := usage["input_tokens"]; ok {
			result["input-tokens"] = formatNumber(v)
		}
		if v, ok := usage["output_tokens"]; ok {
			result["output-tokens"] = formatNumber(v)
		}
	}
	if resp, ok := m["result"].(string); ok && resp != "" {
		result["response"] = base64.StdEncoding.EncodeToString([]byte(resp))
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func extractClaudeCodeCompletion(m map[string]any) claudecode.Result {
	subtype, _ := m["subtype"].(string)
	isError, _ := m["is_error"].(bool)
	stopReason, _ := m["stop_reason"].(string)
	terminalReason, _ := m["terminal_reason"].(string)
	return claudecode.Result{
		Subtype:        subtype,
		IsError:        isError,
		StopReason:     stopReason,
		TerminalReason: terminalReason,
	}
}

// extractCursor reads token counts and the agent response from a cursor
// {"type":"result","usage":{"inputTokens":N,"outputTokens":N},"result":"..."} line.
// Cursor uses camelCase field names instead of claude-code's snake_case.
func extractCursor(m map[string]any) map[string]string {
	result := make(map[string]string)
	if usage, ok := m["usage"].(map[string]any); ok {
		if v, ok := usage["inputTokens"]; ok {
			result["input-tokens"] = formatNumber(v)
		}
		if v, ok := usage["outputTokens"]; ok {
			result["output-tokens"] = formatNumber(v)
		}
	}
	if resp, ok := m["result"].(string); ok && resp != "" {
		result["response"] = base64.StdEncoding.EncodeToString([]byte(resp))
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// extractGemini reads token counts from a gemini
// {"type":"result","stats":{"inputTokens":N,"outputTokens":N}} line.
func extractGemini(m map[string]any) map[string]string {
	stats, ok := m["stats"].(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string)
	if v, ok := stats["inputTokens"]; ok {
		result["input-tokens"] = formatNumber(v)
	}
	if v, ok := stats["outputTokens"]; ok {
		result["output-tokens"] = formatNumber(v)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// extractCodexUsage pulls (input, output) from a codex
// {"type":"turn.completed","usage":{"input_tokens":N,"output_tokens":N}} event.
func extractCodexUsage(m map[string]any) (int64, int64) {
	usage, ok := m["usage"].(map[string]any)
	if !ok {
		return 0, 0
	}
	return toInt64(usage["input_tokens"]), toInt64(usage["output_tokens"])
}

// extractOpencodeUsage pulls (input, output) from an opencode
// {"type":"step_finish","part":{"tokens":{"input":N,"output":N}}} event.
func extractOpencodeUsage(m map[string]any) (int64, int64) {
	part, ok := m["part"].(map[string]any)
	if !ok {
		return 0, 0
	}
	tokens, ok := part["tokens"].(map[string]any)
	if !ok {
		return 0, 0
	}
	return toInt64(tokens["input"]), toInt64(tokens["output"])
}

// extractGeminiResponse returns the assistant message content from a gemini
// {"type":"message","role":"assistant","delta":false,"content":"..."} event.
// Returns "" for non-matching events so the caller keeps the last non-empty value.
func extractGeminiResponse(m map[string]any) string {
	if m["type"] != "message" {
		return ""
	}
	if m["role"] != "assistant" {
		return ""
	}
	// delta messages are streaming chunks; only full messages count.
	if delta, ok := m["delta"].(bool); ok && delta {
		return ""
	}
	content, _ := m["content"].(string)
	return content
}

// extractCodexResponse returns the agent message text from a codex
// {"type":"item.completed","item":{"type":"agent_message","text":"..."}} event.
func extractCodexResponse(m map[string]any) string {
	if m["type"] != "item.completed" {
		return ""
	}
	item, ok := m["item"].(map[string]any)
	if !ok {
		return ""
	}
	if item["type"] != "agent_message" {
		return ""
	}
	text, _ := item["text"].(string)
	return text
}

// extractOpencodeResponse returns the text content from an opencode
// {"type":"text","text":"..."} event.
func extractOpencodeResponse(m map[string]any) string {
	if m["type"] != "text" {
		return ""
	}
	text, _ := m["text"].(string)
	return text
}

// tokenResult builds a result map from token sums, returning nil if both are zero.
func tokenResult(input, output int64) map[string]string {
	if input == 0 && output == 0 {
		return nil
	}
	result := make(map[string]string)
	if input != 0 {
		result["input-tokens"] = fmt.Sprintf("%d", input)
	}
	if output != 0 {
		result["output-tokens"] = fmt.Sprintf("%d", output)
	}
	return result
}

// parseLine unmarshals a JSON line using json.Number to preserve number format.
func parseLine(line []byte) map[string]any {
	d := json.NewDecoder(strings.NewReader(string(line)))
	d.UseNumber()
	var m map[string]any
	if d.Decode(&m) != nil {
		return nil
	}
	return m
}

// formatNumber converts a JSON number value to a string, preserving the
// original format from the JSON source.
func formatNumber(v any) string {
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return fmt.Sprint(v)
}

// toInt64 converts a json.Number to int64, returning 0 on failure.
func toInt64(v any) int64 {
	n, ok := v.(json.Number)
	if !ok {
		return 0
	}
	i, err := n.Int64()
	if err != nil {
		return 0
	}
	return i
}
