package sessionupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
)

const (
	RequestAnnotation     = "kelos.dev/session-runtime-update-request"
	ForceUpdateAnnotation = "kelos.dev/force-session-runtime-update"
)

// Request asks one specific Session Pod to drain before a runtime update.
type Request struct {
	ID     string    `json:"id"`
	PodUID types.UID `json:"podUID"`
}

// NewRequest returns a stable request for one Pod and desired StatefulSet revision.
func NewRequest(podUID types.UID, revision string) Request {
	sum := sha256.Sum256([]byte(string(podUID) + "\x00" + revision))
	return Request{ID: hex.EncodeToString(sum[:16]), PodUID: podUID}
}

// Encode serializes a request for storage in a Session annotation.
func Encode(request Request) (string, error) {
	value, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encoding Session runtime update request: %w", err)
	}
	return string(value), nil
}

// Decode parses a request stored in a Session annotation.
func Decode(value string) (Request, error) {
	var request Request
	if err := json.Unmarshal([]byte(value), &request); err != nil {
		return Request{}, fmt.Errorf("decoding Session runtime update request: %w", err)
	}
	if request.ID == "" || request.PodUID == "" {
		return Request{}, fmt.Errorf("decoding Session runtime update request: id and podUID must be set")
	}
	return request, nil
}
