// Package agentlog formats agent container output into readable turns.
package agentlog

import (
	"context"
	"io"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// Format writes agentType's output from r in readable form. Agent text goes
// to stdout; status and tool lines go to stderr. Unknown types use the
// Claude Code parser.
func Format(agentType string, r io.Reader, stdout, stderr io.Writer) error {
	switch agentType {
	case "codex":
		return ParseAndFormatCodexLogs(r, stdout, stderr)
	case "gemini":
		return ParseAndFormatGeminiLogs(r, stdout, stderr)
	case "opencode":
		return ParseAndFormatOpenCodeLogs(r, stdout, stderr)
	default:
		return ParseAndFormatLogs(r, stdout, stderr)
	}
}

// ResolveType returns the Task's effective agent type from spec.worker.type,
// legacy spec.type, or the referenced WorkerPool. It returns "" when none is
// set, and the lookup error when the WorkerPool cannot be read.
func ResolveType(ctx context.Context, cl client.Reader, namespace string, task *kelos.Task) (string, error) {
	if task.Spec.Worker != nil && task.Spec.Worker.Type != "" {
		return task.Spec.Worker.Type, nil
	}
	if task.Spec.Type != "" {
		return task.Spec.Type, nil
	}
	if task.Spec.WorkerPoolRef != nil {
		var pool kelos.WorkerPool
		if err := cl.Get(ctx, client.ObjectKey{Name: task.Spec.WorkerPoolRef.Name, Namespace: namespace}, &pool); err != nil {
			return "", err
		}
		return pool.Spec.Worker.Type, nil
	}
	return "", nil
}
