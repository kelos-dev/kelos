package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

const sessionRuntimeClient = "/kelos/bin/kelos-session-runtime"

var errSessionTerminalClosed = errors.New("Session terminal closed")

type sessionConnectDependencies struct {
	resolveConfig func() (*rest.Config, string, error)
	connect       func(context.Context, *rest.Config, string, string, io.Reader, io.Writer, io.Writer, bool) error
}

func newSessionCommand(cfg *ClientConfig) *cobra.Command {
	command := &cobra.Command{
		Use:     "session",
		Aliases: []string{"sessions"},
		Short:   "Interact with Sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newSessionConnectCommand(cfg), newSessionResetCommand(cfg))
	return command
}

func newSessionConnectCommand(cfg *ClientConfig) *cobra.Command {
	dependencies := sessionConnectDependencies{
		resolveConfig: cfg.resolveConfig,
		connect:       connectSession,
	}
	return newSessionConnectCommandWithDependencies(cfg, dependencies)
}

func newSessionConnectCommandWithDependencies(cfg *ClientConfig, dependencies sessionConnectDependencies) *cobra.Command {
	colorMode := "auto"
	command := &cobra.Command{
		Use:   "connect NAME",
		Short: "Connect to a Session using terminal chat",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			color, err := terminalColorEnabled(colorMode, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			name := args[0]
			restConfig, namespace, err := dependencies.resolveConfig()
			if err != nil {
				return err
			}
			if err := dependencies.connect(cmd.Context(), restConfig, namespace, name, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), color); err != nil {
				return fmt.Errorf("connecting to Session %q: %w", name, err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&colorMode, "color", "auto", "Color output: auto, always, or never")
	command.ValidArgsFunction = completeSessionNames(cfg)
	return command
}

func terminalColorEnabled(mode string, output io.Writer) (bool, error) {
	switch mode {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto":
		if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
			return false, nil
		}
		file, ok := output.(*os.File)
		if !ok {
			return false, nil
		}
		info, err := file.Stat()
		if err != nil {
			return false, fmt.Errorf("detecting terminal color support: %w", err)
		}
		return info.Mode()&os.ModeCharDevice != 0, nil
	default:
		return false, fmt.Errorf("invalid color mode %q: must be auto, always, or never", mode)
	}
}

type sessionPodStream struct {
	requests io.WriteCloser
	events   io.ReadCloser
	cancel   context.CancelFunc
	done     <-chan struct{}
	once     sync.Once
}

func (s *sessionPodStream) Close() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		_ = s.requests.Close()
		_ = s.events.Close()
		if s.done != nil {
			<-s.done
		}
	})
}

type sessionReconnectDependencies struct {
	getSession  func(context.Context, string, string) (*kelos.Session, error)
	openStream  func(context.Context, string, string, io.Writer) (*sessionPodStream, error)
	runTerminal func(context.Context, io.Reader, io.Writer, io.Reader, io.Writer, bool) error
}

type sessionEventResult struct {
	event sessionruntime.Event
	err   error
}

type sessionTerminalEventDelivery struct {
	event sessionruntime.Event
	done  chan error
}

type sessionTerminalEventSink struct {
	ctx        context.Context
	deliveries chan sessionTerminalEventDelivery
	done       chan struct{}
	errMu      sync.Mutex
	err        error
}

func newSessionTerminalEventSink(ctx context.Context, output io.Writer) *sessionTerminalEventSink {
	sink := &sessionTerminalEventSink{
		ctx:        ctx,
		deliveries: make(chan sessionTerminalEventDelivery),
		done:       make(chan struct{}),
	}
	go sink.run(json.NewEncoder(output))
	return sink
}

func (s *sessionTerminalEventSink) run(encoder *json.Encoder) {
	for {
		select {
		case delivery := <-s.deliveries:
			err := encoder.Encode(delivery.event)
			delivery.done <- err
			if err != nil {
				s.finish(err)
				return
			}
		case <-s.ctx.Done():
			s.finish(s.ctx.Err())
			return
		}
	}
}

func (s *sessionTerminalEventSink) send(event sessionruntime.Event) error {
	delivery := sessionTerminalEventDelivery{event: event, done: make(chan error, 1)}
	select {
	case s.deliveries <- delivery:
	case <-s.done:
		return s.result()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	select {
	case err := <-delivery.done:
		return err
	case <-s.done:
		return s.result()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *sessionTerminalEventSink) finish(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
	close(s.done)
}

func (s *sessionTerminalEventSink) result() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

type sessionTerminalDiagnosticWriter struct {
	mu      sync.Mutex
	pending string
	events  *sessionTerminalEventSink
}

func (w *sessionTerminalDiagnosticWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pending += string(data)
	for {
		newline := strings.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		line := strings.TrimSuffix(w.pending[:newline], "\r")
		w.pending = w.pending[newline+1:]
		if err := w.send(line); err != nil {
			return len(data), err
		}
	}
	return len(data), nil
}

func (w *sessionTerminalDiagnosticWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := strings.TrimSuffix(w.pending, "\r")
	w.pending = ""
	return w.send(line)
}

func (w *sessionTerminalDiagnosticWriter) send(line string) error {
	if line == "" {
		return nil
	}
	return w.events.send(sessionruntime.Event{Type: sessionTerminalEventDiagnostic, Text: line})
}

type pendingSessionRequest struct {
	request sessionruntime.ClientRequest
	sent    bool
}

func connectSession(ctx context.Context, restConfig *rest.Config, namespace, name string, stdin io.Reader, stdout, stderr io.Writer, color bool) error {
	controllerClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating Kubernetes client: %w", err)
	}
	dependencies := sessionReconnectDependencies{
		getSession: func(ctx context.Context, namespace, name string) (*kelos.Session, error) {
			session := &kelos.Session{}
			if err := controllerClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, session); err != nil {
				return nil, err
			}
			return session, nil
		},
		openStream: func(ctx context.Context, namespace, podName string, diagnostics io.Writer) (*sessionPodStream, error) {
			return openSessionPodStream(ctx, restConfig, namespace, podName, diagnostics)
		},
		runTerminal: runSessionTerminal,
	}
	return connectSessionWithDependencies(ctx, namespace, name, stdin, stdout, stderr, color, dependencies)
}

func openSessionPodStream(ctx context.Context, restConfig *rest.Config, namespace, podName string, stderr io.Writer) (*sessionPodStream, error) {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating Kubernetes client: %w", err)
	}
	request := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec")
	request.VersionedParams(&corev1.PodExecOptions{
		Container: kelos.AgentContainerName,
		Command:   []string{sessionRuntimeClient, "client"},
		Stdin:     true,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, clientgoscheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(restConfig, "POST", request.URL())
	if err != nil {
		return nil, fmt.Errorf("creating exec connection: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	requestReader, requestWriter := io.Pipe()
	eventReader, eventWriter := io.Pipe()
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		err := executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{
			Stdin:  requestReader,
			Stdout: eventWriter,
			Stderr: stderr,
			Tty:    false,
		})
		_ = eventWriter.CloseWithError(err)
		_ = requestReader.CloseWithError(err)
	}()
	return &sessionPodStream{requests: requestWriter, events: eventReader, cancel: cancel, done: streamDone}, nil
}

func connectSessionWithDependencies(
	ctx context.Context,
	namespace, name string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	color bool,
	dependencies sessionReconnectDependencies,
) error {
	terminalCtx, cancelTerminal := context.WithCancel(ctx)
	eventReader, eventWriter := io.Pipe()
	requestReader, requestWriter := io.Pipe()
	defer eventReader.Close()
	defer eventWriter.Close()
	defer requestReader.Close()
	defer requestWriter.Close()

	eventCtx, cancelEvents := context.WithCancel(ctx)
	defer cancelEvents()
	eventSink := newSessionTerminalEventSink(eventCtx, eventWriter)
	terminalDone := make(chan error, 1)
	terminalExited := make(chan struct{})
	go func() {
		defer close(terminalExited)
		err := dependencies.runTerminal(terminalCtx, stdin, stdout, eventReader, requestWriter, color)
		cancelEvents()
		terminalDone <- err
	}()
	defer func() {
		cancelTerminal()
		<-terminalExited
	}()
	diagnostics := stderr
	var diagnosticWriter *sessionTerminalDiagnosticWriter
	if sessionTerminalDiagnosticsUseTUI(stdin, stdout, stderr) {
		diagnosticWriter = &sessionTerminalDiagnosticWriter{events: eventSink}
		diagnostics = diagnosticWriter
		defer diagnosticWriter.Flush()
	}

	requests := make(chan sessionruntime.ClientRequest, 32)
	requestDecodeDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(requestReader)
		for {
			var request sessionruntime.ClientRequest
			if err := decoder.Decode(&request); err != nil {
				requestDecodeDone <- err
				return
			}
			select {
			case requests <- request:
			case <-terminalCtx.Done():
				requestDecodeDone <- terminalCtx.Err()
				return
			}
		}
	}()

	var lastEventID int64
	connectedBefore := false
	pendingRequests := make([]pendingSessionRequest, 0)
	for {
		session, err := waitForReadySession(terminalCtx, namespace, name, diagnostics, dependencies.getSession, terminalDone, connectedBefore)
		if err != nil {
			if errors.Is(err, errSessionTerminalClosed) {
				return nil
			}
			return err
		}
		stream, err := dependencies.openStream(terminalCtx, namespace, session.Status.PodName, diagnostics)
		if err != nil {
			fmt.Fprintf(diagnostics, "Session connection failed; retrying: %v\n", err)
			if err := waitForSessionRetry(terminalCtx, terminalDone); err != nil {
				if errors.Is(err, errSessionTerminalClosed) {
					return nil
				}
				return err
			}
			continue
		}
		announceReconnect := connectedBefore
		encoder := json.NewEncoder(stream.requests)
		if err := encoder.Encode(sessionruntime.ClientRequest{Type: "subscribe", Since: lastEventID}); err != nil {
			stream.Close()
			if diagnosticWriter != nil {
				_ = diagnosticWriter.Flush()
			}
			fmt.Fprintf(diagnostics, "Session connection failed; retrying: %v\n", err)
			if err := waitForSessionRetry(terminalCtx, terminalDone); err != nil {
				if errors.Is(err, errSessionTerminalClosed) {
					return nil
				}
				return err
			}
			continue
		}

		events := make(chan sessionEventResult)
		go func() {
			decoder := json.NewDecoder(stream.events)
			for {
				var event sessionruntime.Event
				if err := decoder.Decode(&event); err != nil {
					select {
					case events <- sessionEventResult{err: err}:
					case <-terminalCtx.Done():
					}
					return
				}
				select {
				case events <- sessionEventResult{event: event}:
				case <-terminalCtx.Done():
					return
				}
			}
		}()

		reconnect := false
		historyComplete := false
		for !reconnect {
			select {
			case <-ctx.Done():
				stream.Close()
				return ctx.Err()
			case err := <-terminalDone:
				stream.Close()
				return err
			case err := <-requestDecodeDone:
				stream.Close()
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			case request := <-requests:
				if request.Type == "subscribe" {
					continue
				}
				if request.RequestID == "" {
					request.RequestID = string(uuid.NewUUID())
				}
				pendingRequests = append(pendingRequests, pendingSessionRequest{request: request})
				if !historyComplete {
					continue
				}
				pendingRequests[len(pendingRequests)-1].sent = true
				if err := encoder.Encode(request); err != nil {
					fmt.Fprintf(diagnostics, "Session connection lost while sending input: %v\n", err)
					reconnect = true
				}
			case result := <-events:
				if result.err != nil {
					reconnect = true
					continue
				}
				event := result.event
				if event.RequestID != "" {
					pendingRequests = removePendingSessionRequest(pendingRequests, event.RequestID)
				}
				if event.Type == sessionruntime.EventHistoryEnd {
					if announceReconnect {
						fmt.Fprintln(diagnostics, "Reconnected to Session runtime")
						announceReconnect = false
					}
					connectedBefore = true
					historyComplete = true
					for i := range pendingRequests {
						if pendingRequests[i].sent {
							continue
						}
						pendingRequests[i].sent = true
						if err := encoder.Encode(pendingRequests[i].request); err != nil {
							fmt.Fprintf(diagnostics, "Session connection lost while sending input: %v\n", err)
							reconnect = true
							break
						}
					}
				}
				if event.ID > lastEventID {
					lastEventID = event.ID
				}
				if err := eventSink.send(event); err != nil {
					stream.Close()
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if eventCtx.Err() != nil {
						<-terminalExited
						return <-terminalDone
					}
					return err
				}
			}
		}
		stream.Close()
		if diagnosticWriter != nil {
			_ = diagnosticWriter.Flush()
		}
		pendingRequests = discardUnconfirmedSessionRequests(pendingRequests, diagnostics)
		fmt.Fprintln(diagnostics, "Session connection lost; waiting for the runtime to recover")
		if err := waitForSessionRetry(terminalCtx, terminalDone); err != nil {
			if errors.Is(err, errSessionTerminalClosed) {
				return nil
			}
			return err
		}
	}
}

func removePendingSessionRequest(requests []pendingSessionRequest, requestID string) []pendingSessionRequest {
	for i := range requests {
		if requests[i].request.RequestID == requestID {
			return append(requests[:i], requests[i+1:]...)
		}
	}
	return requests
}

func discardUnconfirmedSessionRequests(requests []pendingSessionRequest, stderr io.Writer) []pendingSessionRequest {
	retained := requests[:0]
	for _, request := range requests {
		if !request.sent {
			retained = append(retained, request)
			continue
		}
		fmt.Fprintf(stderr, "Session %s request delivery was not confirmed; it was not retried\n", request.request.Type)
	}
	return retained
}

func waitForReadySession(
	ctx context.Context,
	namespace, name string,
	stderr io.Writer,
	getSession func(context.Context, string, string) (*kelos.Session, error),
	terminalDone <-chan error,
	retryFailed bool,
) (*kelos.Session, error) {
	reportedWaiting := false
	for {
		session, err := getSession(ctx, namespace, name)
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Session %q was deleted", name)
		}
		if err != nil {
			fmt.Fprintf(stderr, "Getting Session %q failed; retrying: %v\n", name, err)
		} else if session.Status.Phase == kelos.SessionPhaseFailed {
			if !retryFailed {
				return nil, fmt.Errorf("Session %q failed: %s", name, session.Status.Message)
			}
			if !reportedWaiting {
				fmt.Fprintf(stderr, "Waiting for Session %q to recover\n", name)
				reportedWaiting = true
			}
		} else if session.Status.Phase == kelos.SessionPhaseReady && session.Status.PodName != "" {
			return session, nil
		} else if !reportedWaiting {
			fmt.Fprintf(stderr, "Waiting for Session %q to become ready\n", name)
			reportedWaiting = true
		}
		if err := waitForSessionRetry(ctx, terminalDone); err != nil {
			return nil, err
		}
	}
}

func waitForSessionRetry(ctx context.Context, terminalDone <-chan error) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-terminalDone:
		if err == nil {
			return errSessionTerminalClosed
		}
		return err
	case <-timer.C:
		return nil
	}
}
