package sessionupdate

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
)

const ReportAnnotation = "kelos.dev/session-runtime-update-report"

// Phase describes how far a Session Pod has progressed through draining.
type Phase string

const (
	PhaseDraining Phase = "Draining"
	PhaseDrained  Phase = "Drained"
)

// Report acknowledges a runtime update request from one Session Pod.
type Report struct {
	RequestID string    `json:"requestID"`
	PodUID    types.UID `json:"podUID"`
	Phase     Phase     `json:"phase"`
}

// EncodeReport serializes a report for storage in a Session annotation.
func EncodeReport(report Report) (string, error) {
	value, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("encoding Session runtime update report: %w", err)
	}
	return string(value), nil
}

// DecodeReport parses a report stored in a Session annotation.
func DecodeReport(value string) (Report, error) {
	var report Report
	if err := json.Unmarshal([]byte(value), &report); err != nil {
		return Report{}, fmt.Errorf("decoding Session runtime update report: %w", err)
	}
	if report.RequestID == "" || report.PodUID == "" {
		return Report{}, fmt.Errorf("decoding Session runtime update report: requestID and podUID must be set")
	}
	if report.Phase != PhaseDraining && report.Phase != PhaseDrained {
		return Report{}, fmt.Errorf("decoding Session runtime update report: unsupported phase %q", report.Phase)
	}
	return report, nil
}
