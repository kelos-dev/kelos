package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	pollInterval      = 5 * time.Second
	turnTimeout       = 4 * time.Hour
	appServerCommand  = "codex"
	sessionLabel      = "kelos.dev/agent-session"
	runnerClientName  = "kelos_session_runner"
	runnerClientTitle = "Kelos Session Runner"
	maxActivityRunes  = 180
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type appServerClient struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	encoderMu   sync.Mutex
	nextID      atomic.Int64
	responses   chan rpcMessage
	events      chan rpcMessage
	diagnostics chan appServerDiagnostic
}

type appServerDiagnostic struct {
	Kind     string
	Message  string
	Activity string
}

type turnInterruptedError struct {
	message string
}

func (e turnInterruptedError) Error() string {
	if e.message != "" {
		return e.message
	}
	return "Codex turn interrupted"
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	log := ctrl.Log.WithName("session-runner")
	ctx := ctrl.SetupSignalHandler()

	namespace := os.Getenv("KELOS_AGENT_SESSION_NAMESPACE")
	name := os.Getenv("KELOS_AGENT_SESSION_NAME")
	if namespace == "" || name == "" {
		log.Error(errors.New("missing session env"), "KELOS_AGENT_SESSION_NAMESPACE and KELOS_AGENT_SESSION_NAME are required")
		os.Exit(2)
	}

	if err := prepareCodexRuntime(ctx); err != nil {
		log.Error(err, "Failed to prepare Codex runtime")
		os.Exit(1)
	}

	k8sClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: newScheme()})
	if err != nil {
		log.Error(err, "Failed to create Kubernetes client")
		os.Exit(1)
	}

	app, err := startAppServer(ctx)
	if err != nil {
		log.Error(err, "Failed to start Codex app-server")
		_ = markSessionError(ctx, k8sClient, namespace, name, err.Error())
		os.Exit(1)
	}
	defer app.stop()

	if err := app.initialize(ctx); err != nil {
		log.Error(err, "Failed to initialize Codex app-server")
		_ = markSessionError(ctx, k8sClient, namespace, name, err.Error())
		os.Exit(1)
	}

	if err := runSession(ctx, k8sClient, app, namespace, name); err != nil {
		log.Error(err, "Session runner failed")
		_ = markSessionError(ctx, k8sClient, namespace, name, err.Error())
		os.Exit(1)
	}
}

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
	return scheme
}

func prepareCodexRuntime(ctx context.Context) error {
	if _, err := os.Stat("/usr/local/bin/kelos-agent-setup"); err == nil {
		cmd := exec.CommandContext(ctx, "/usr/local/bin/kelos-agent-setup")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("running kelos-agent-setup: %w", err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		return err
	}
	if auth := os.Getenv("CODEX_AUTH_JSON"); auth != "" {
		cleaned := strings.NewReplacer("\n", "", "\r", "").Replace(auth)
		if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(cleaned), 0o600); err != nil {
			return err
		}
	}
	if agentsMD := os.Getenv("KELOS_AGENTS_MD"); agentsMD != "" {
		if err := os.WriteFile(filepath.Join(codexDir, "AGENTS.md"), []byte(agentsMD), 0o600); err != nil {
			return err
		}
	}
	if pluginDir := os.Getenv("KELOS_PLUGIN_DIR"); pluginDir != "" {
		if err := installCodexPluginSkills(pluginDir, codexDir); err != nil {
			return err
		}
	}
	if mcp := os.Getenv("KELOS_MCP_SERVERS"); mcp != "" {
		toml, err := mcpServersTOML(mcp)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(codexDir, "config.toml"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(toml); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	if setup := os.Getenv("KELOS_SETUP_COMMAND"); setup != "" {
		var args []string
		if err := json.Unmarshal([]byte(setup), &args); err != nil {
			return fmt.Errorf("parsing KELOS_SETUP_COMMAND: %w", err)
		}
		if len(args) == 0 {
			return fmt.Errorf("KELOS_SETUP_COMMAND is empty")
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("running KELOS_SETUP_COMMAND: %w", err)
		}
	}
	return nil
}

func installCodexPluginSkills(pluginDir, codexDir string) error {
	plugins, err := os.ReadDir(pluginDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, plugin := range plugins {
		if !plugin.IsDir() {
			continue
		}
		skillsDir := filepath.Join(pluginDir, plugin.Name(), "skills")
		skills, err := os.ReadDir(skillsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, skill := range skills {
			if !skill.IsDir() {
				continue
			}
			source := filepath.Join(skillsDir, skill.Name(), "SKILL.md")
			data, err := os.ReadFile(source)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			targetDir := filepath.Join(codexDir, "skills", plugin.Name()+"-"+skill.Name())
			if err := os.MkdirAll(targetDir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), data, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func mcpServersTOML(raw string) (string, error) {
	var wrapper struct {
		MCPServers map[string]struct {
			Command string            `json:"command,omitempty"`
			Args    []string          `json:"args,omitempty"`
			URL     string            `json:"url,omitempty"`
			Headers map[string]string `json:"headers,omitempty"`
			Env     map[string]string `json:"env,omitempty"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return "", fmt.Errorf("parsing KELOS_MCP_SERVERS: %w", err)
	}
	var b strings.Builder
	for name, server := range wrapper.MCPServers {
		fmt.Fprintf(&b, "[mcp_servers.%q]\n", name)
		if server.Command != "" {
			fmt.Fprintf(&b, "command = %q\n", server.Command)
		}
		if len(server.Args) > 0 {
			data, _ := json.Marshal(server.Args)
			fmt.Fprintf(&b, "args = %s\n", data)
		}
		if server.URL != "" {
			fmt.Fprintf(&b, "url = %q\n", server.URL)
		}
		if len(server.Headers) > 0 {
			fmt.Fprintf(&b, "http_headers = %s\n", tomlInlineMap(server.Headers))
		}
		if len(server.Env) > 0 {
			fmt.Fprintf(&b, "env = %s\n", tomlInlineMap(server.Env))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func tomlInlineMap(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%q = %q", key, values[key]))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func startAppServer(ctx context.Context) (*appServerClient, error) {
	cmd := exec.CommandContext(ctx, appServerCommand, "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &appServerClient{
		cmd:         cmd,
		stdin:       stdin,
		responses:   make(chan rpcMessage, 32),
		events:      make(chan rpcMessage, 256),
		diagnostics: make(chan appServerDiagnostic, 64),
	}
	go c.readLoop(stdout)
	go c.stderrLoop(stderr)
	return c, nil
}

func (c *appServerClient) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.ID != 0 {
			c.responses <- msg
		} else {
			c.events <- msg
		}
	}
	close(c.responses)
	close(c.events)
}

func (c *appServerClient) stderrLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = fmt.Fprintln(os.Stderr, line)
		if diag, ok := classifyAppServerDiagnostic(line); ok {
			select {
			case c.diagnostics <- diag:
			default:
			}
		}
	}
}

func (c *appServerClient) drainDiagnostics() {
	for {
		select {
		case <-c.diagnostics:
		default:
			return
		}
	}
}

func (c *appServerClient) stop() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.stdin.Close()
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
}

func (c *appServerClient) initialize(ctx context.Context) error {
	params := map[string]interface{}{
		"clientInfo": map[string]string{
			"name":    runnerClientName,
			"title":   runnerClientTitle,
			"version": "0.1.0",
		},
		"capabilities": map[string]interface{}{
			"experimentalApi": true,
		},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify(ctx, "initialized", nil)
}

func (c *appServerClient) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.send(req); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case msg, ok := <-c.responses:
			if !ok {
				return nil, fmt.Errorf("app-server exited before %s response", method)
			}
			if msg.ID != id {
				continue
			}
			if msg.Error != nil {
				return nil, fmt.Errorf("%s failed: %s", method, msg.Error.Message)
			}
			return msg.Result, nil
		}
	}
}

func (c *appServerClient) notify(ctx context.Context, method string, params interface{}) error {
	req := map[string]interface{}{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	return c.send(req)
}

func (c *appServerClient) send(req interface{}) error {
	c.encoderMu.Lock()
	defer c.encoderMu.Unlock()
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

func runSession(ctx context.Context, cl client.Client, app *appServerClient, namespace, name string) error {
	var session kelosv1alpha1.AgentSession
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		return err
	}
	threadID := session.Status.CodexThreadID
	if threadID == "" {
		result, err := app.call(ctx, "thread/start", map[string]interface{}{
			"persistExtendedHistory": true,
		})
		if err != nil {
			return err
		}
		threadID = extractString(result, "thread", "id")
		if threadID == "" {
			return fmt.Errorf("thread/start response did not include thread.id")
		}
		if err := patchSessionStatus(ctx, cl, namespace, name, func(s *kelosv1alpha1.AgentSession) {
			now := metav1.Now()
			s.Status.CodexThreadID = threadID
			s.Status.Phase = kelosv1alpha1.AgentSessionPhaseIdle
			s.Status.LastActivityAt = &now
			s.Status.Message = "Codex App Server thread started"
		}); err != nil {
			return err
		}
	} else {
		if _, err := app.call(ctx, "thread/resume", map[string]interface{}{
			"threadId":               threadID,
			"persistExtendedHistory": true,
			"excludeTurns":           true,
		}); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
			return err
		}
		if session.Status.Phase == kelosv1alpha1.AgentSessionPhaseClosed || session.Status.Phase == kelosv1alpha1.AgentSessionPhaseClosing {
			return nil
		}
		turn, ok, err := nextQueuedTurn(ctx, cl, namespace, name)
		if err != nil {
			return err
		}
		if ok {
			if err := runTurn(ctx, cl, app, &session, &turn, threadID); err != nil {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func nextQueuedTurn(ctx context.Context, cl client.Client, namespace, sessionName string) (kelosv1alpha1.AgentTurn, bool, error) {
	var list kelosv1alpha1.AgentTurnList
	if err := cl.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabels{sessionLabel: sessionName}); err != nil {
		return kelosv1alpha1.AgentTurn{}, false, err
	}
	var queued []kelosv1alpha1.AgentTurn
	for _, turn := range list.Items {
		if turn.Status.Phase == "" || turn.Status.Phase == kelosv1alpha1.AgentTurnPhaseQueued {
			queued = append(queued, turn)
		}
	}
	if len(queued) == 0 {
		return kelosv1alpha1.AgentTurn{}, false, nil
	}
	sort.Slice(queued, func(i, j int) bool {
		return queued[i].Spec.Sequence < queued[j].Spec.Sequence
	})
	return queued[0], true, nil
}

func runTurn(ctx context.Context, cl client.Client, app *appServerClient, session *kelosv1alpha1.AgentSession, turn *kelosv1alpha1.AgentTurn, threadID string) error {
	now := metav1.Now()
	if err := patchTurnStatus(ctx, cl, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.Phase = kelosv1alpha1.AgentTurnPhaseRunning
		t.Status.StartedAt = &now
		t.Status.Message = "Codex turn running"
	}); err != nil {
		return err
	}
	if err := patchSessionStatus(ctx, cl, session.Namespace, session.Name, func(s *kelosv1alpha1.AgentSession) {
		s.Status.Phase = kelosv1alpha1.AgentSessionPhaseRunning
		s.Status.CurrentTurn = turn.Name
		s.Status.LastActivityAt = &now
	}); err != nil {
		return err
	}

	prompt := renderTurnPrompt(session, turn)
	params := map[string]interface{}{
		"threadId": threadID,
		"input": []map[string]interface{}{{
			"type":          "text",
			"text":          prompt,
			"text_elements": []interface{}{},
		}},
		"approvalPolicy": "never",
		"sandboxPolicy":  map[string]string{"type": "dangerFullAccess"},
	}
	if cwd, ok := workingDir(); ok {
		params["cwd"] = cwd
	}
	if model := os.Getenv("KELOS_MODEL"); model != "" {
		params["model"] = model
	}
	result, err := app.call(ctx, "turn/start", params)
	if err != nil {
		return failTurn(ctx, cl, session, turn, err.Error())
	}
	app.drainDiagnostics()
	codexTurnID := extractString(result, "turn", "id")
	if codexTurnID != "" {
		if err := patchTurnStatus(ctx, cl, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
			t.Status.CodexTurnID = codexTurnID
		}); err != nil {
			return err
		}
	}
	turnCtx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	finalText, err := waitForTurn(turnCtx, app, session.Name, codexTurnID, cl, turn)
	if err != nil {
		var interrupted turnInterruptedError
		if errors.As(err, &interrupted) {
			return cancelTurn(ctx, cl, session, turn, interrupted.Error())
		}
		return failTurn(ctx, cl, session, turn, err.Error())
	}
	completed := metav1.Now()
	if err := patchTurnStatus(ctx, cl, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.Phase = kelosv1alpha1.AgentTurnPhaseSucceeded
		t.Status.CompletedAt = &completed
		t.Status.ResultText = finalText
		t.Status.Message = "Codex turn succeeded"
	}); err != nil {
		return err
	}
	return patchSessionStatus(ctx, cl, session.Namespace, session.Name, func(s *kelosv1alpha1.AgentSession) {
		s.Status.Phase = kelosv1alpha1.AgentSessionPhaseIdle
		s.Status.CurrentTurn = ""
		s.Status.LastCompletedTurn = turn.Name
		s.Status.LastActivityAt = &completed
		s.Status.Message = "Codex turn succeeded"
	})
}

func waitForTurn(ctx context.Context, app *appServerClient, sessionName, codexTurnID string, cl client.Client, turn *kelosv1alpha1.AgentTurn) (string, error) {
	var final strings.Builder
	var lastAppServerError string
	summary := newAppServerTurnSummary()
	log := ctrl.Log.WithName("session-runner").WithValues(
		"agent_session", sessionName,
		"agent_turn", turn.Name,
		"namespace", turn.Namespace,
		"codex_turn_id", codexTurnID,
	)
	defer summary.log(log)
	for {
		select {
		case <-ctx.Done():
			if lastAppServerError != "" {
				return "", fmt.Errorf("%w: last app-server error: %s", ctx.Err(), lastAppServerError)
			}
			return "", ctx.Err()
		case diag := <-app.diagnostics:
			if diag.Kind == "" {
				continue
			}
			log.Info("codex_app_server_diagnostic",
				"diagnostic_kind", diag.Kind,
				"message", sanitizeForLog(diag.Message),
			)
			if diag.Activity != "" {
				_ = patchTurnActivity(context.Background(), cl, turn.Namespace, turn.Name, diag.Activity)
			}
		case msg, ok := <-app.events:
			if !ok {
				if lastAppServerError != "" {
					return "", fmt.Errorf("app-server exited while turn was running: %s", lastAppServerError)
				}
				return "", fmt.Errorf("app-server exited while turn was running")
			}
			summary.record(msg)
			logAppServerEvent(log, msg)
			switch msg.Method {
			case "turn/started":
				if activity := turnStartedActivity(msg.Params); activity != "" {
					_ = patchTurnActivity(context.Background(), cl, turn.Namespace, turn.Name, activity)
				}
			case "thread/status/changed":
				if activity := threadStatusActivity(msg.Params); activity != "" {
					_ = patchTurnActivity(context.Background(), cl, turn.Namespace, turn.Name, activity)
				}
			case "turn/plan/updated":
				if activity := turnPlanActivity(msg.Params); activity != "" {
					_ = patchTurnActivity(context.Background(), cl, turn.Namespace, turn.Name, activity)
				}
			case "item/started":
				if activity := startedItemActivity(msg.Params); activity != "" {
					_ = patchTurnActivity(context.Background(), cl, turn.Namespace, turn.Name, activity)
				}
			case "item/agentMessage/delta":
				delta := extractString(msg.Params, "delta")
				if delta == "" {
					delta = extractString(msg.Params, "text")
				}
				if delta != "" {
					final.WriteString(delta)
				}
			case "item/completed":
				if text := extractString(msg.Params, "item", "text"); text != "" {
					final.Reset()
					final.WriteString(text)
				}
				activity := completedItemActivity(msg.Params)
				if activity != "" {
					_ = patchTurnActivity(context.Background(), cl, turn.Namespace, turn.Name, activity)
				}
			case "error":
				if message := extractString(msg.Params, "error", "message"); message != "" {
					lastAppServerError = sanitizeForLog(message)
				} else {
					lastAppServerError = "Codex App Server reported an error"
				}
				_ = patchTurnActivity(context.Background(), cl, turn.Namespace, turn.Name, "Codex app-server reported an error; waiting for final turn status")
			case "turn/completed":
				status := extractString(msg.Params, "turn", "status")
				switch status {
				case "completed":
					if final.String() == "" {
						final.WriteString(extractString(msg.Params, "turn", "finalMessage"))
					}
					return strings.TrimSpace(final.String()), nil
				case "failed":
					if lastAppServerError != "" {
						return "", errors.New(lastAppServerError)
					}
					if message := extractString(msg.Params, "turn", "error", "message"); message != "" {
						return "", errors.New(sanitizeForLog(message))
					}
					return "", fmt.Errorf("Codex turn failed")
				case "interrupted":
					message := extractString(msg.Params, "turn", "error", "message")
					if message == "" {
						message = "Codex turn interrupted"
					}
					return "", turnInterruptedError{message: sanitizeForLog(message)}
				default:
					if status == "" {
						status = "missing"
					}
					return "", fmt.Errorf("Codex turn completed with unsupported status %q", status)
				}
			}
		}
	}
}

type appServerTurnSummary struct {
	counts             map[string]int
	agentDeltaBytes    int
	commandOutputBytes int
}

func newAppServerTurnSummary() *appServerTurnSummary {
	return &appServerTurnSummary{counts: map[string]int{}}
}

func (s *appServerTurnSummary) record(msg rpcMessage) {
	if s == nil {
		return
	}
	s.counts[msg.Method]++
	switch msg.Method {
	case "item/agentMessage/delta":
		delta := extractString(msg.Params, "delta")
		if delta == "" {
			delta = extractString(msg.Params, "text")
		}
		s.agentDeltaBytes += len(delta)
	case "item/commandExecution/outputDelta":
		delta := extractString(msg.Params, "delta")
		if delta == "" {
			delta = extractString(msg.Params, "text")
		}
		s.commandOutputBytes += len(delta)
	}
}

func (s *appServerTurnSummary) log(log logr.Logger) {
	if s == nil {
		return
	}
	log.Info("codex_app_server_turn_summary",
		"event_counts", s.counts,
		"agent_delta_bytes", s.agentDeltaBytes,
		"command_output_delta_bytes", s.commandOutputBytes,
	)
}

func logAppServerEvent(log logr.Logger, msg rpcMessage) {
	switch msg.Method {
	case "item/agentMessage/delta", "item/commandExecution/outputDelta":
		return
	}
	values := []interface{}{"event_type", msg.Method}
	switch msg.Method {
	case "item/started", "item/completed", "item/updated":
		values = append(values,
			"item_type", extractString(msg.Params, "item", "type"),
			"item_status", extractString(msg.Params, "item", "status"),
			"command", commandSummary(msg.Params),
			"tool", sanitizeForLog(extractString(msg.Params, "item", "tool")),
		)
	case "turn/completed":
		values = append(values, "turn_status", extractString(msg.Params, "turn", "status"))
	case "thread/status/changed":
		values = append(values, "thread_status", extractString(msg.Params, "status"))
	case "error":
		values = append(values, "error", sanitizeForLog(extractString(msg.Params, "error", "message")))
	}
	log.Info("codex_app_server_event", values...)
}

func turnStartedActivity(params json.RawMessage) string {
	if id := extractString(params, "turn", "id"); id != "" {
		return "Codex app-server started turn"
	}
	return ""
}

func threadStatusActivity(params json.RawMessage) string {
	status := extractString(params, "status")
	if status == "" {
		status = extractString(params, "thread", "status")
	}
	if status == "" {
		return ""
	}
	return truncateRunes("Codex session status: "+sanitizeForLog(status), maxActivityRunes)
}

func turnPlanActivity(params json.RawMessage) string {
	if plan := extractString(params, "plan"); plan != "" {
		return truncateRunes("Updated plan: "+sanitizeForLog(plan), maxActivityRunes)
	}
	return "Updated plan"
}

func startedItemActivity(params json.RawMessage) string {
	itemType := extractString(params, "item", "type")
	switch itemType {
	case "commandExecution":
		if cmd := commandSummary(params); cmd != "" {
			return "Running command: " + cmd
		}
		return "Running command"
	case "mcpToolCall":
		if tool := sanitizeForLog(extractString(params, "item", "tool")); tool != "" {
			return "Calling MCP tool: " + tool
		}
		return "Calling MCP tool"
	}
	return ""
}

func completedItemActivity(params json.RawMessage) string {
	itemType := extractString(params, "item", "type")
	switch itemType {
	case "commandExecution":
		cmd := commandSummary(params)
		status := extractString(params, "item", "status")
		if cmd != "" && status != "" {
			return truncateRunes(fmt.Sprintf("%s command: %s", status, cmd), maxActivityRunes)
		}
	case "mcpToolCall":
		tool := sanitizeForLog(extractString(params, "item", "tool"))
		status := extractString(params, "item", "status")
		if tool != "" && status != "" {
			return truncateRunes(fmt.Sprintf("%s MCP tool: %s", status, tool), maxActivityRunes)
		}
	}
	return ""
}

func commandSummary(params json.RawMessage) string {
	cmd := extractString(params, "item", "command")
	if cmd == "" {
		cmd = extractString(params, "command")
	}
	return truncateRunes(sanitizeForLog(cmd), 160)
}

var secretValuePattern = regexp.MustCompile(`(?i)(authorization:\s*bearer\s+|token=|_authtoken=|password=|[A-Z0-9_]*(?:TOKEN|KEY|SECRET)=)([^\s'"` + "`" + `]+)`)

func sanitizeForLog(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = secretValuePattern.ReplaceAllString(s, "${1}<redacted>")
	return strings.TrimSpace(s)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}

func classifyAppServerDiagnostic(line string) (appServerDiagnostic, bool) {
	if strings.Contains(line, "write_stdin failed: stdin is closed") {
		return appServerDiagnostic{
			Kind:     "interactive_tool_without_pty",
			Message:  line,
			Activity: "Tool session stdin closed; rerun interactive commands with tty=true",
		}, true
	}
	return appServerDiagnostic{}, false
}

func failTurn(ctx context.Context, cl client.Client, session *kelosv1alpha1.AgentSession, turn *kelosv1alpha1.AgentTurn, message string) error {
	now := metav1.Now()
	if err := patchTurnStatus(ctx, cl, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.Phase = kelosv1alpha1.AgentTurnPhaseFailed
		t.Status.CompletedAt = &now
		t.Status.Message = message
	}); err != nil {
		return err
	}
	return patchSessionStatus(ctx, cl, session.Namespace, session.Name, func(s *kelosv1alpha1.AgentSession) {
		s.Status.Phase = kelosv1alpha1.AgentSessionPhaseIdle
		s.Status.CurrentTurn = ""
		s.Status.LastCompletedTurn = turn.Name
		s.Status.LastActivityAt = &now
		s.Status.Message = message
	})
}

func cancelTurn(ctx context.Context, cl client.Client, session *kelosv1alpha1.AgentSession, turn *kelosv1alpha1.AgentTurn, message string) error {
	now := metav1.Now()
	if err := patchTurnStatus(ctx, cl, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.Phase = kelosv1alpha1.AgentTurnPhaseCanceled
		t.Status.CompletedAt = &now
		t.Status.Message = message
	}); err != nil {
		return err
	}
	return patchSessionStatus(ctx, cl, session.Namespace, session.Name, func(s *kelosv1alpha1.AgentSession) {
		s.Status.Phase = kelosv1alpha1.AgentSessionPhaseIdle
		s.Status.CurrentTurn = ""
		s.Status.LastCompletedTurn = turn.Name
		s.Status.LastActivityAt = &now
		s.Status.Message = message
	})
}

func renderTurnPrompt(session *kelosv1alpha1.AgentSession, turn *kelosv1alpha1.AgentTurn) string {
	if session.Spec.Source.Type == "Aikido" || turn.Spec.Source.Type == "AikidoIssueGroup" {
		return renderAikidoTurnPrompt(session, turn)
	}
	if session.Spec.Source.Type == "Cron" || turn.Spec.Source.Type == "CronTick" {
		return renderCronTurnPrompt(session, turn)
	}
	transcript := strings.TrimSpace(turn.Spec.Context.Transcript)
	if transcript == "" {
		transcript = "(none)"
	}
	request := strings.TrimSpace(turn.Spec.Input.Body)
	if request == "" {
		request = strings.TrimSpace(turn.Spec.Input.Text)
	}
	prev := session.Status.LastAgentMessageTS
	if prev == "" {
		prev = "none"
	}
	if turn.Spec.Sequence == 1 {
		return fmt.Sprintf(`You are running Cody through a Kelos Slack AgentSession.

Session:
- Kelos session: %s/%s
- Slack thread: %s
- TaskSpawner route: %s/%s
- Turn sequence: %d

Route prompt:
%s

Reply once in the same Slack thread through the Kelos reporter.
For shell commands that require follow-up stdin, an interactive prompt, or a long-running session you need to write to later, start the command with a TTY. If a previous command reports that stdin is closed, rerun it as an interactive TTY session instead of retrying write_stdin.
If you need more information, ask for it in your final answer.`,
			session.Namespace,
			session.Name,
			session.Spec.Source.ThreadURL,
			session.Namespace,
			session.Spec.TaskSpawnerRef.Name,
			turn.Spec.Sequence,
			request,
		)
	}
	return fmt.Sprintf(`You are continuing an existing Cody Slack session.

Session:
- Kelos session: %s/%s
- Slack thread: %s
- TaskSpawner route: %s/%s
- Turn sequence: %d
- Previous Cody answer timestamp: %s

Conversation since your last terminal answer:
%s

Current explicit request:
%s [%s at %s]:
%s

Reply once in the same Slack thread through the Kelos reporter.
Treat leading route prefixes such as !session as routing metadata only.
Do not assume messages outside the provided delta happened after your last answer.
For shell commands that require follow-up stdin, an interactive prompt, or a long-running session you need to write to later, start the command with a TTY. If a previous command reports that stdin is closed, rerun it as an interactive TTY session instead of retrying write_stdin.
If you need more information, ask for it in your final answer.`,
		session.Namespace,
		session.Name,
		session.Spec.Source.ThreadURL,
		session.Namespace,
		session.Spec.TaskSpawnerRef.Name,
		turn.Spec.Sequence,
		prev,
		transcript,
		turn.Spec.Source.UserID,
		turn.Spec.Source.UserID,
		turn.Spec.Source.MessageTS,
		request,
	)
}

func renderAikidoTurnPrompt(session *kelosv1alpha1.AgentSession, turn *kelosv1alpha1.AgentTurn) string {
	request := strings.TrimSpace(turn.Spec.Input.Body)
	if request == "" {
		request = strings.TrimSpace(turn.Spec.Input.Text)
	}
	sourceName := strings.TrimSpace(session.Spec.Source.DisplayName)
	if sourceName == "" {
		sourceName = session.Spec.Source.Type
	}
	sourceKey := strings.TrimSpace(session.Spec.Source.Key)
	if sourceKey == "" {
		sourceKey = "(none)"
	}
	schedule := strings.TrimSpace(turn.Spec.Source.Schedule)
	if schedule == "" {
		schedule = session.Spec.Source.Schedule
	}
	if schedule == "" {
		schedule = "(none)"
	}
	turnID := strings.TrimSpace(turn.Spec.Source.ID)
	if turnID == "" {
		turnID = "(none)"
	}
	turnTime := strings.TrimSpace(turn.Spec.Source.Time)
	if turnTime == "" {
		turnTime = "(unknown)"
	}
	action := "running"
	if turn.Spec.Sequence > 1 {
		action = "continuing"
	}
	return fmt.Sprintf(`You are %s Cody through a Kelos Aikido security remediation AgentSession.

Session:
- Kelos session: %s/%s
- Source: %s
- Session scope: %s
- TaskSpawner route: %s/%s
- Turn sequence: %d
- Aikido schedule: %s
- Aikido turn: %s at %s

Aikido issue prompt:
%s

Use the existing Codex App Server thread context to maintain continuity across Aikido follow-up turns in this session.
Before creating a PR, search for existing open remediation PRs using the Aikido group ID, issue IDs, CVE IDs, affected package, and repository names.
Create or update fixes only against latest main unless explicitly instructed otherwise.
Do not merge PRs.
If remediation needs shared package, image, or consumer rebuilds, continue babysitting those steps across future turns.
Reply once through the Kelos reporter.
For shell commands that require follow-up stdin, an interactive prompt, or a long-running session you need to write to later, start the command with a TTY. If a previous command reports that stdin is closed, rerun it as an interactive TTY session instead of retrying write_stdin.
If you need more information, ask for it in your final answer.`,
		action,
		session.Namespace,
		session.Name,
		sourceName,
		sourceKey,
		session.Namespace,
		session.Spec.TaskSpawnerRef.Name,
		turn.Spec.Sequence,
		schedule,
		turnID,
		turnTime,
		request,
	)
}

func renderCronTurnPrompt(session *kelosv1alpha1.AgentSession, turn *kelosv1alpha1.AgentTurn) string {
	request := strings.TrimSpace(turn.Spec.Input.Body)
	if request == "" {
		request = strings.TrimSpace(turn.Spec.Input.Text)
	}
	sourceName := strings.TrimSpace(session.Spec.Source.DisplayName)
	if sourceName == "" {
		sourceName = session.Spec.Source.Type
	}
	sourceKey := strings.TrimSpace(session.Spec.Source.Key)
	if sourceKey == "" {
		sourceKey = "(none)"
	}
	schedule := strings.TrimSpace(turn.Spec.Source.Schedule)
	if schedule == "" {
		schedule = session.Spec.Source.Schedule
	}
	if schedule == "" {
		schedule = "(none)"
	}
	tickID := strings.TrimSpace(turn.Spec.Source.ID)
	if tickID == "" {
		tickID = "(none)"
	}
	tickTime := strings.TrimSpace(turn.Spec.Source.Time)
	if tickTime == "" {
		tickTime = "(unknown)"
	}
	action := "running"
	if turn.Spec.Sequence > 1 {
		action = "continuing"
	}
	return fmt.Sprintf(`You are %s Cody through a Kelos cron AgentSession.

Session:
- Kelos session: %s/%s
- Source: %s
- Session scope: %s
- TaskSpawner route: %s/%s
- Turn sequence: %d
- Cron schedule: %s
- Cron tick: %s at %s

Cron tick prompt:
%s

Use the existing Codex App Server thread context to maintain continuity across cron ticks in this session.
Reply once through the Kelos reporter.
For shell commands that require follow-up stdin, an interactive prompt, or a long-running session you need to write to later, start the command with a TTY. If a previous command reports that stdin is closed, rerun it as an interactive TTY session instead of retrying write_stdin.
If you need more information, ask for it in your final answer.`,
		action,
		session.Namespace,
		session.Name,
		sourceName,
		sourceKey,
		session.Namespace,
		session.Spec.TaskSpawnerRef.Name,
		turn.Spec.Sequence,
		schedule,
		tickID,
		tickTime,
		request,
	)
}

func workingDir() (string, bool) {
	if _, err := os.Stat("/workspace/repo"); err == nil {
		return "/workspace/repo", true
	}
	return "", false
}

func patchTurnStatus(ctx context.Context, cl client.Client, namespace, name string, mutate func(*kelosv1alpha1.AgentTurn)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var turn kelosv1alpha1.AgentTurn
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &turn); err != nil {
			return err
		}
		mutate(&turn)
		return cl.Status().Update(ctx, &turn)
	})
}

func patchTurnActivity(ctx context.Context, cl client.Client, namespace, name, activity string) error {
	if activity == "" {
		return nil
	}
	return patchTurnStatus(ctx, cl, namespace, name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.Activity = truncateRunes(sanitizeForLog(activity), maxActivityRunes)
	})
}

func patchSessionStatus(ctx context.Context, cl client.Client, namespace, name string, mutate func(*kelosv1alpha1.AgentSession)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var session kelosv1alpha1.AgentSession
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
			return err
		}
		mutate(&session)
		return cl.Status().Update(ctx, &session)
	})
}

func markSessionError(ctx context.Context, cl client.Client, namespace, name, message string) error {
	return patchSessionStatus(ctx, cl, namespace, name, func(s *kelosv1alpha1.AgentSession) {
		now := metav1.Now()
		s.Status.Phase = kelosv1alpha1.AgentSessionPhaseError
		s.Status.Message = message
		s.Status.LastActivityAt = &now
	})
}

func extractString(raw json.RawMessage, path ...string) string {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	for _, key := range path {
		m, ok := v.(map[string]interface{})
		if !ok {
			return ""
		}
		v = m[key]
	}
	switch typed := v.(type) {
	case string:
		return typed
	case []interface{}:
		data, _ := json.Marshal(typed)
		return string(data)
	default:
		return ""
	}
}
