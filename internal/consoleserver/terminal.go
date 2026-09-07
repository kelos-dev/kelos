package consoleserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	executil "k8s.io/utils/exec"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionreset"
)

func (s *Server) execSession(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	session, ok := s.readySession(writer, request, namespace, name)
	if !ok {
		return
	}
	if session.DeletionTimestamp != nil || session.Annotations[sessionreset.RequestAnnotation] != "" ||
		(session.Spec.Suspend != nil && *session.Spec.Suspend) {
		writeError(writer, http.StatusConflict, fmt.Sprintf("Session %q is stopping", name))
		return
	}
	execRequest := s.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(session.Status.PodName).SubResource("exec")
	execRequest.VersionedParams(&corev1.PodExecOptions{
		Container: kelos.AgentContainerName,
		Command:   []string{"/bin/sh", "-c", "export TERM=xterm-256color; exec /bin/sh -i"},
		Stdin:     true,
		Stdout:    true,
		TTY:       true,
	}, clientgoscheme.ParameterCodec)
	executor, err := s.terminalExecutor(s.restConfig, http.MethodPost, execRequest.URL())
	if err != nil {
		writeError(writer, http.StatusBadGateway, fmt.Sprintf("opening terminal for Session %q: %v", name, err))
		return
	}
	connection, err := s.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	socket := &sessionSocket{Conn: connection}
	defer socket.Close()
	err = bridgeTerminal(request.Context(), socket, executor)
	event := map[string]string{"type": "exit"}
	var exitError executil.CodeExitError
	if err != nil && !errors.As(err, &exitError) {
		event = map[string]string{"type": "error", "text": fmt.Sprintf("Session %q terminal: %v", name, err)}
	}
	socket.writeMu.Lock()
	defer socket.writeMu.Unlock()
	_ = socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = socket.Conn.WriteJSON(event)
}

type terminalSizeQueue struct {
	ctx   context.Context
	sizes chan remotecommand.TerminalSize
}

func (q *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case size := <-q.sizes:
		return &size
	case <-q.ctx.Done():
		return nil
	}
}

type terminalOutput struct {
	ctx        context.Context
	connection *sessionSocket
}

func (w terminalOutput) Write(data []byte) (int, error) {
	w.connection.writeMu.Lock()
	defer w.connection.writeMu.Unlock()
	if err := w.connection.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, err
	}
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if err := w.connection.Conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func bridgeTerminal(ctx context.Context, connection *sessionSocket, executor remotecommand.Executor) error {
	ctx, cancel := context.WithCancel(ctx)
	stdin := &terminalInput{ctx: ctx, chunks: make(chan []byte, 32)}
	sizes := &terminalSizeQueue{ctx: ctx, sizes: make(chan remotecommand.TerminalSize, 1)}
	sizes.sizes <- remotecommand.TerminalSize{Width: 80, Height: 24}
	streamDone := make(chan struct{})
	var streamErr error
	go func() {
		defer close(streamDone)
		streamErr = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:             stdin,
			Stdout:            terminalOutput{ctx: ctx, connection: connection},
			Tty:               true,
			TerminalSizeQueue: sizes,
		})
	}()
	inputDone := make(chan struct{})
	var inputErr error
	go func() {
		defer close(inputDone)
		inputErr = readTerminalInput(connection, stdin.chunks, sizes)
	}()
	defer func() {
		cancel()
		// Interrupt socket I/O without closing the connection before the final event.
		_ = connection.UnderlyingConn().SetReadDeadline(time.Now())
		_ = connection.UnderlyingConn().SetWriteDeadline(time.Now())
		<-streamDone
		<-inputDone
	}()
	select {
	case <-streamDone:
		return streamErr
	case <-inputDone:
		return inputErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

type terminalInput struct {
	ctx     context.Context
	chunks  chan []byte
	current []byte
}

func (r *terminalInput) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	for len(r.current) == 0 {
		select {
		case r.current = <-r.chunks:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	n := copy(data, r.current)
	r.current = r.current[n:]
	return n, nil
}

func readTerminalInput(connection *sessionSocket, input chan<- []byte, sizes *terminalSizeQueue) error {
	connection.SetReadLimit(64 * 1024)
	for {
		messageType, data, err := connection.ReadMessage()
		if err != nil {
			return err
		}
		switch messageType {
		case websocket.BinaryMessage:
			// Keep reading disconnects and resizes while the shell is not reading stdin.
			select {
			case input <- data:
			default:
				return errors.New("terminal input buffer is full")
			}
		case websocket.TextMessage:
			var message struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if err := json.Unmarshal(data, &message); err != nil || message.Type != "resize" || message.Cols == 0 || message.Rows == 0 {
				return errors.New("invalid terminal resize message")
			}
			// Only the latest dimensions matter when resizes outpace the remote stream.
			select {
			case <-sizes.sizes:
			default:
			}
			sizes.sizes <- remotecommand.TerminalSize{Width: message.Cols, Height: message.Rows}
		}
	}
}
