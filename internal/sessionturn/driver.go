package sessionturn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// runtimeExecutable is the in-Pod client that bridges stdio to the Session
// runtime socket. It matches the command the console server and the CLI exec.
const runtimeExecutable = "/kelos/bin/kelos-session-runtime"

// DefaultTurnTimeout bounds one driven turn. Observed Slack agent runs have a
// p99 around 45 minutes, so the bound is generous rather than tight: it exists
// to release the stream on a wedged turn, not to cut work short.
const DefaultTurnTimeout = 60 * time.Minute

// Driver submits prompts to Session runtimes over the Pod exec subresource.
type Driver struct {
	// Clientset builds the exec request.
	Clientset kubernetes.Interface
	// RestConfig authenticates the exec stream.
	RestConfig *rest.Config
	// NewExecutor creates the exec stream. Tests replace it.
	NewExecutor func(*rest.Config, string, *url.URL) (remotecommand.Executor, error)
	// Timeout bounds one turn. Zero means DefaultTurnTimeout.
	Timeout time.Duration
}

// turnTimeout is the bound on one turn, falling back to DefaultTurnTimeout
// when no positive timeout is configured.
func (d *Driver) turnTimeout() time.Duration {
	if d.Timeout <= 0 {
		return DefaultTurnTimeout
	}
	return d.Timeout
}

// NewDriver creates a Driver from a Kubernetes REST configuration.
func NewDriver(restConfig *rest.Config) (*Driver, error) {
	if restConfig == nil {
		return nil, errors.New("Kubernetes REST configuration must not be nil")
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating Kubernetes client: %w", err)
	}
	return &Driver{
		Clientset:   clientset,
		RestConfig:  restConfig,
		NewExecutor: remotecommand.NewSPDYExecutor,
	}, nil
}

// Submit drives one conversation turn in the Session Pod and returns the
// assistant reply. It returns once the runtime reports the turn complete, the
// turn timeout elapses, or the stream drops.
func (d *Driver) Submit(ctx context.Context, namespace, podName, prompt string) (Result, error) {
	if podName == "" {
		return Result{}, errors.New("Session Pod name must not be empty")
	}
	// The exec stream deliberately outlives the turn's own deadline: when a turn
	// is abandoned, RunTurn sends an interrupt so the Session does not keep
	// running it, and that write needs the stream still open.
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	turnCtx, cancelTurn := context.WithTimeout(ctx, d.turnTimeout())
	defer cancelTurn()

	request := d.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec")
	request.VersionedParams(&corev1.PodExecOptions{
		Container: kelos.AgentContainerName,
		Command:   []string{runtimeExecutable, "client"},
		Stdin:     true,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, clientgoscheme.ParameterCodec)
	executor, err := d.NewExecutor(d.RestConfig, http.MethodPost, request.URL())
	if err != nil {
		return Result{}, fmt.Errorf("creating Session exec connection: %w", err)
	}

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	var stderr bytes.Buffer

	streamDone := make(chan error, 1)
	go func() {
		err := executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{
			Stdin:  stdinReader,
			Stdout: stdoutWriter,
			Stderr: &stderr,
			Tty:    false,
		})
		// Releasing both halves unblocks RunTurn when the stream ends first:
		// without this a stream that fails before reading stdin would leave the
		// first request write with no reader.
		_ = stdoutWriter.CloseWithError(io.EOF)
		_ = stdinReader.CloseWithError(io.EOF)
		streamDone <- err
	}()

	result, turnErr := RunTurn(turnCtx, stdinWriter, stdoutReader, prompt)

	// Releasing both pipes ends the in-Pod client and unblocks the exec stream.
	_ = stdinWriter.Close()
	_ = stdoutReader.Close()
	var (
		streamErr    error
		streamClosed bool
	)
	select {
	case streamErr = <-streamDone:
		streamClosed = true
	case <-time.After(streamCloseGrace):
	}

	if turnErr == nil {
		return result, nil
	}
	// stderr is only safe to read once the streaming goroutine has returned.
	if streamClosed {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return result, fmt.Errorf("driving Session turn: %s: %w", message, turnErr)
		}
		if streamErr != nil {
			return result, fmt.Errorf("driving Session turn: %w", errors.Join(turnErr, streamErr))
		}
	}
	return result, fmt.Errorf("driving Session turn: %w", turnErr)
}

// streamCloseGrace bounds the wait for the exec stream to unwind after a turn
// finishes, so a stuck stream cannot hold the caller past its own turn.
const streamCloseGrace = 5 * time.Second
