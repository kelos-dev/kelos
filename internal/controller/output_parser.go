package controller

import "strings"

const (
	outputStartMarker = "---KELOS_OUTPUTS_START---"
	outputEndMarker   = "---KELOS_OUTPUTS_END---"
)

// ParseOutputs extracts output lines from the last complete
// ---KELOS_OUTPUTS_START--- / ---KELOS_OUTPUTS_END--- marker pair in logData.
// Using the last pair ensures that sequential tasks on a persistent worker pod
// do not inherit stale outputs from a previous task.
func ParseOutputs(logData string) []string {
	endIdx := strings.LastIndex(logData, outputEndMarker)
	if endIdx == -1 {
		return nil
	}
	// Search for the start marker before the end marker.
	startIdx := strings.LastIndex(logData[:endIdx], outputStartMarker)
	if startIdx == -1 {
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
