package controller

import "strings"

const (
	outputStartMarker = "---KELOS_OUTPUTS_START---"
	outputEndMarker   = "---KELOS_OUTPUTS_END---"
)

// ParseOutputs extracts output lines from log data between the
// ---KELOS_OUTPUTS_START--- and ---KELOS_OUTPUTS_END--- markers.
func ParseOutputs(logData string) []string {
	startIdx := strings.Index(logData, outputStartMarker)
	if startIdx == -1 {
		return nil
	}
	endIdx := strings.Index(logData, outputEndMarker)
	if endIdx == -1 || endIdx <= startIdx {
		return nil
	}

	between := logData[startIdx+len(outputStartMarker) : endIdx]
	between = strings.TrimSpace(between)
	if between == "" {
		return nil
	}

	lines := strings.Split(between, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// ResultsFromOutputs builds a key-value map from output lines in "key: value" format.
// Lines that do not contain ": " are skipped. If duplicate keys exist, the last value wins.
func ResultsFromOutputs(outputs []string) map[string]string {
	if len(outputs) == 0 {
		return nil
	}
	var result map[string]string
	for _, line := range outputs {
		key, value, ok := strings.Cut(line, ": ")
		if !ok || key == "" {
			continue
		}
		if result == nil {
			result = make(map[string]string)
		}
		result[key] = value
	}
	return result
}
