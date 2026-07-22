package sessionreset

import (
	"context"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	// RequestAnnotation asks the Session controller to replace the Session workspace.
	RequestAnnotation = "kelos.dev/session-reset-request"
	// StateAnnotation records controller-owned progress for an in-flight reset.
	StateAnnotation = "kelos.dev/session-reset-state"
)

// Phase describes the current step of a Session reset.
type Phase string

const (
	PhaseStopping        Phase = "Stopping"
	PhaseDeletingStorage Phase = "DeletingStorage"
	PhaseStarting        Phase = "Starting"
)

// State makes a reset request idempotent across reconciles and controller restarts.
type State struct {
	RequestID string `json:"requestID"`
	Phase     Phase  `json:"phase"`
}

// EncodeState serializes controller-owned reset progress for a Session annotation.
func EncodeState(state State) (string, error) {
	value, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encoding Session reset state: %w", err)
	}
	return string(value), nil
}

// DecodeState parses controller-owned reset progress from a Session annotation.
func DecodeState(value string) (State, error) {
	var state State
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return State{}, fmt.Errorf("decoding Session reset state: %w", err)
	}
	if state.RequestID == "" {
		return State{}, fmt.Errorf("decoding Session reset state: requestID must be set")
	}
	switch state.Phase {
	case PhaseStopping, PhaseDeletingStorage, PhaseStarting:
	default:
		return State{}, fmt.Errorf("decoding Session reset state: unsupported phase %q", state.Phase)
	}
	return state, nil
}

// Request adds a reset request to a Session unless one is already in progress.
func Request(ctx context.Context, cl client.Client, key client.ObjectKey, requestID string) (*kelos.Session, bool, error) {
	if requestID == "" {
		return nil, false, fmt.Errorf("requesting Session %q reset: request ID must not be empty", key.Name)
	}
	var session kelos.Session
	if err := cl.Get(ctx, key, &session); err != nil {
		return nil, false, fmt.Errorf("getting Session %q for reset: %w", key.Name, err)
	}
	if session.Annotations[RequestAnnotation] != "" {
		return &session, false, nil
	}

	original := session.DeepCopy()
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[RequestAnnotation] = requestID
	delete(session.Annotations, StateAnnotation)
	if err := cl.Patch(ctx, &session, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
		return nil, false, fmt.Errorf("requesting Session %q reset: %w", key.Name, err)
	}
	return &session, true, nil
}
