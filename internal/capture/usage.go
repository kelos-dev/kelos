package capture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ParseUsage extracts token usage metrics from the agent output file.
// Returns nil if the file doesn't exist or the agent type is unknown.
func ParseUsage(agentType, filePath string) map[string]string {
	if agentType == "" {
		return nil
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines [][]byte
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	if len(lines) == 0 {
		return nil
	}

	switch agentType {
	case "claude-code":
		return parseClaudeCode(lines)
	case "codex":
		return parseCodex(lines)
	case "gemini":
		return parseGemini(lines)
	case "opencode":
		return parseOpencode(lines)
	default:
		return nil
	}
}

// parseClaudeCode extracts cost and token counts from the last
// {"type":"result","total_cost_usd":N,"usage":{"input_tokens":N,"output_tokens":N}} line.
func parseClaudeCode(lines [][]byte) map[string]string {
	last := findLastByType(lines, "result")
	if last == nil {
		return nil
	}
	result := make(map[string]string)
	if v, ok := last["total_cost_usd"]; ok {
		result["cost-usd"] = formatNumber(v)
	}
	// Token counts live under the "usage" object in current claude-code versions.
	if usage, ok := last["usage"].(map[string]any); ok {
		if v, ok := usage["input_tokens"]; ok {
			result["input-tokens"] = formatNumber(v)
		}
		if v, ok := usage["output_tokens"]; ok {
			result["output-tokens"] = formatNumber(v)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// parseCodex sums input_tokens and output_tokens across all
// {"type":"turn.completed",...} events.
func parseCodex(lines [][]byte) map[string]string {
	var inputTotal, outputTotal int64
	for _, line := range lines {
		m := parseLine(line)
		if m == nil || m["type"] != "turn.completed" {
			continue
		}
		usage, ok := m["usage"].(map[string]any)
		if !ok {
			continue
		}
		inputTotal += toInt64(usage["input_tokens"])
		outputTotal += toInt64(usage["output_tokens"])
	}
	return tokenResult(inputTotal, outputTotal)
}

// parseGemini extracts token counts from the last {"type":"result","stats":{...}} line.
func parseGemini(lines [][]byte) map[string]string {
	last := findLastByType(lines, "result")
	if last == nil {
		return nil
	}
	stats, ok := last["stats"].(map[string]any)
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

// parseOpencode sums input and output tokens across all
// {"type":"step_finish",...} events.
func parseOpencode(lines [][]byte) map[string]string {
	var inputTotal, outputTotal int64
	for _, line := range lines {
		m := parseLine(line)
		if m == nil || m["type"] != "step_finish" {
			continue
		}
		part, ok := m["part"].(map[string]any)
		if !ok {
			continue
		}
		tokens, ok := part["tokens"].(map[string]any)
		if !ok {
			continue
		}
		inputTotal += toInt64(tokens["input"])
		outputTotal += toInt64(tokens["output"])
	}
	return tokenResult(inputTotal, outputTotal)
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

// findLastByType returns the last JSON object with the given "type" field.
func findLastByType(lines [][]byte, typ string) map[string]any {
	var last map[string]any
	for _, line := range lines {
		m := parseLine(line)
		if m == nil {
			continue
		}
		if m["type"] == typ {
			last = m
		}
	}
	return last
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
