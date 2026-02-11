package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"golang.org/x/term"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func newVizCommand(cfg *ClientConfig) *cobra.Command {
	var (
		prompt         string
		agentType      string
		secret         string
		credentialType string
		model          string
		image          string
		name           string
		workspace      string
		yes            bool
		demo           bool
	)

	cmd := &cobra.Command{
		Use:   "viz [task-name]",
		Short: "Run a task with real-time RPG visualization",
		Long: `Create and run a task with a real-time RPG-themed terminal visualization,
or watch an existing task.

  axon viz -p "prompt"      Create a new task and visualize
  axon viz <task-name>      Visualize an existing task
  axon viz --demo           Run a simulated demo (no cluster needed)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Demo mode: simulate a full task lifecycle without a cluster.
			if demo {
				return runVizDemo()
			}

			if !term.IsTerminal(int(os.Stdout.Fd())) {
				return fmt.Errorf("viz requires a terminal (stdout is not a TTY)")
			}

			ctx := context.Background()

			// Watch existing task
			if len(args) > 0 {
				return vizExistingTask(ctx, cfg, args[0])
			}

			if prompt == "" {
				return fmt.Errorf("--prompt is required when creating a new task\nUsage: axon viz -p \"prompt\" or axon viz <task-name>")
			}

			// Apply config defaults (mirrors run.go logic).
			if c := cfg.Config; c != nil {
				if !cmd.Flags().Changed("secret") && c.Secret != "" {
					secret = c.Secret
				}
				if !cmd.Flags().Changed("credential-type") && c.CredentialType != "" {
					credentialType = c.CredentialType
				}
				if !cmd.Flags().Changed("type") && c.Type != "" {
					agentType = c.Type
				}
				if !cmd.Flags().Changed("model") && c.Model != "" {
					model = c.Model
				}
				if !cmd.Flags().Changed("workspace") && c.Workspace.Name != "" {
					workspace = c.Workspace.Name
				}
			}

			// Auto-create secret from token if no explicit secret is set.
			if secret == "" && cfg.Config != nil {
				if cfg.Config.OAuthToken != "" && cfg.Config.APIKey != "" {
					return fmt.Errorf("config file must specify either oauthToken or apiKey, not both")
				}
				if token := cfg.Config.OAuthToken; token != "" {
					oauthKey := oauthSecretKey(agentType)
					if err := ensureCredentialSecret(cfg, "axon-credentials", oauthKey, token, yes); err != nil {
						return err
					}
					secret = "axon-credentials"
					credentialType = "oauth"
				} else if key := cfg.Config.APIKey; key != "" {
					apiKey := apiKeySecretKey(agentType)
					if err := ensureCredentialSecret(cfg, "axon-credentials", apiKey, key, yes); err != nil {
						return err
					}
					secret = "axon-credentials"
					credentialType = "api-key"
				}
			}

			if secret == "" {
				return fmt.Errorf("no credentials configured (set oauthToken/apiKey in config file, or use --secret flag)")
			}

			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}
			cs, _, err := cfg.NewClientset()
			if err != nil {
				return err
			}

			// Auto-create Workspace CR from inline config if no --workspace flag.
			if workspace == "" && cfg.Config != nil && cfg.Config.Workspace.Repo != "" {
				wsCfg := cfg.Config.Workspace
				wsName := "axon-workspace"
				ws := &axonv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wsName,
						Namespace: ns,
					},
					Spec: axonv1alpha1.WorkspaceSpec{
						Repo: wsCfg.Repo,
						Ref:  wsCfg.Ref,
					},
				}
				if wsCfg.Token != "" {
					if err := ensureCredentialSecret(cfg, "axon-workspace-credentials", "GITHUB_TOKEN", wsCfg.Token, yes); err != nil {
						return err
					}
					ws.Spec.SecretRef = &axonv1alpha1.SecretReference{
						Name: "axon-workspace-credentials",
					}
				}
				if err := cl.Create(ctx, ws); err != nil {
					if !apierrors.IsAlreadyExists(err) {
						return fmt.Errorf("creating workspace: %w", err)
					}
					existing := &axonv1alpha1.Workspace{}
					if err := cl.Get(ctx, client.ObjectKey{Name: wsName, Namespace: ns}, existing); err != nil {
						return fmt.Errorf("fetching existing workspace: %w", err)
					}
					if !reflect.DeepEqual(existing.Spec, ws.Spec) {
						if !yes {
							ok, confirmErr := confirmOverride(fmt.Sprintf("workspace/%s", wsName))
							if confirmErr != nil {
								return confirmErr
							}
							if !ok {
								return fmt.Errorf("aborted")
							}
						}
						existing.Spec = ws.Spec
						if err := cl.Update(ctx, existing); err != nil {
							return fmt.Errorf("updating workspace: %w", err)
						}
					}
				}
				workspace = wsName
			}

			// Create the task.
			if name == "" {
				name = "task-" + rand.String(5)
			}

			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   agentType,
					Prompt: prompt,
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialType(credentialType),
						SecretRef: axonv1alpha1.SecretReference{
							Name: secret,
						},
					},
					Model: model,
					Image: image,
				},
			}
			if workspace != "" {
				task.Spec.WorkspaceRef = &axonv1alpha1.WorkspaceReference{
					Name: workspace,
				}
			}
			task.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("Task"))

			if err := cl.Create(ctx, task); err != nil {
				return fmt.Errorf("creating task: %w", err)
			}
			fmt.Fprintf(os.Stderr, "task/%s created\n", name)

			// Initialize RPG state with first 3 stages already done.
			state := NewRPGState(prompt, agentType, ns, name, model)
			state.SetStage(StageCredentials, StatusDone)
			if workspace != "" {
				state.SetStage(StageWorkspace, StatusDone)
			} else {
				state.SetStage(StageWorkspace, StatusSkipped)
			}
			state.SetStage(StageDispatch, StatusDone)

			return runViz(ctx, cl, cs, ns, task, state)
		},
	}

	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "task prompt")
	cmd.Flags().StringVarP(&agentType, "type", "t", "claude-code", "agent type (claude-code, codex, gemini)")
	cmd.Flags().StringVar(&secret, "secret", "", "secret name with credentials (overrides config)")
	cmd.Flags().StringVar(&credentialType, "credential-type", "api-key", "credential type (api-key, oauth)")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&image, "image", "", "custom agent image")
	cmd.Flags().StringVar(&name, "name", "", "task name (auto-generated if omitted)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace resource name")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&demo, "demo", false, "run a simulated demo (no cluster or credentials needed)")

	_ = cmd.RegisterFlagCompletionFunc("credential-type", cobra.FixedCompletions([]string{"api-key", "oauth"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"claude-code", "codex", "gemini"}, cobra.ShellCompDirectiveNoFileComp))

	cmd.ValidArgsFunction = completeTaskNames(cfg)

	return cmd
}

// vizExistingTask watches an existing task with the RPG visualization.
func vizExistingTask(ctx context.Context, cfg *ClientConfig, taskName string) error {
	cl, ns, err := cfg.NewClient()
	if err != nil {
		return err
	}
	cs, _, err := cfg.NewClientset()
	if err != nil {
		return err
	}

	task := &axonv1alpha1.Task{}
	if err := cl.Get(ctx, client.ObjectKey{Name: taskName, Namespace: ns}, task); err != nil {
		return fmt.Errorf("getting task: %w", err)
	}

	state := NewRPGState(task.Spec.Prompt, task.Spec.Type, ns, taskName, task.Spec.Model)
	state.SetStage(StageCredentials, StatusDone)
	if task.Spec.WorkspaceRef != nil {
		state.SetStage(StageWorkspace, StatusDone)
	} else {
		state.SetStage(StageWorkspace, StatusSkipped)
	}
	state.SetStage(StageDispatch, StatusDone)

	return runViz(ctx, cl, cs, ns, task, state)
}

// runViz drives the RPG visualization render loop and background polling.
func runViz(ctx context.Context, cl client.Client, cs *kubernetes.Clientset, ns string, task *axonv1alpha1.Task, state *RPGState) error {
	// Fetch the latest task state (it may have progressed since creation).
	if err := cl.Get(ctx, client.ObjectKey{Name: task.Name, Namespace: ns}, task); err != nil {
		return fmt.Errorf("getting task: %w", err)
	}

	// Enter alternate screen buffer and hide cursor.
	fmt.Print("\033[?1049h\033[?25l")

	vizCtx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	// Start render loop goroutine.
	stopRender := make(chan struct{})
	renderDone := make(chan struct{})
	go func() {
		defer close(renderDone)
		vizRenderLoop(vizCtx, state, stopRender)
	}()

	// cleanup stops the render loop and restores the terminal.
	cleanup := func() {
		close(stopRender)
		<-renderDone
		fmt.Print("\033[?1049l\033[?25h")
	}

	// If the task is already in a terminal phase, show the final state briefly.
	switch task.Status.Phase {
	case axonv1alpha1.TaskPhaseSucceeded:
		vizSetFinalStages(state, task, true)
		state.SetFinished(true, "Quest completed!", task.Status.Outputs, 0, 0)
		sleepCtx(vizCtx, 3*time.Second)
		cleanup()
		vizPrintSummary(state)
		return nil
	case axonv1alpha1.TaskPhaseFailed:
		vizSetFinalStages(state, task, false)
		msg := "Quest failed"
		if task.Status.Message != "" {
			msg = task.Status.Message
		}
		state.SetFinished(false, msg, nil, 0, 0)
		sleepCtx(vizCtx, 3*time.Second)
		cleanup()
		vizPrintSummary(state)
		return nil
	}

	// Start background status poller and log streamer.
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		vizPollAndStream(vizCtx, cl, cs, ns, task, state)
	}()

	// Wait for quest completion or user interrupt.
	select {
	case <-pollDone:
		sleepCtx(vizCtx, 3*time.Second)
	case <-vizCtx.Done():
	}

	cleanup()
	vizPrintSummary(state)
	return nil
}

// vizRenderLoop continuously renders the RPG frame at ~10fps.
func vizRenderLoop(ctx context.Context, state *RPGState, stop <-chan struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			state.AdvanceSpinner()
			w, h := vizGetTermSize()
			fmt.Print(state.Render(w, h))
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// vizPollAndStream polls the task status and streams pod logs into the battle log.
func vizPollAndStream(ctx context.Context, cl client.Client, cs *kubernetes.Clientset, ns string, task *axonv1alpha1.Task, state *RPGState) {
	taskName := task.Name
	agentType := task.Spec.Type
	hasWorkspace := task.Spec.WorkspaceRef != nil

	var logStreamStarted bool

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		current := &axonv1alpha1.Task{}
		if err := cl.Get(ctx, client.ObjectKey{Name: taskName, Namespace: ns}, current); err != nil {
			sleepCtx(ctx, 2*time.Second)
			continue
		}

		switch current.Status.Phase {
		case "", axonv1alpha1.TaskPhasePending:
			state.SetStage(StageSummon, StatusActive)

		case axonv1alpha1.TaskPhaseRunning:
			state.SetStage(StageSummon, StatusDone)

			if !logStreamStarted && current.Status.PodName != "" {
				logStreamStarted = true
				podName := current.Status.PodName
				go vizStreamLogs(ctx, cs, ns, podName, agentType, hasWorkspace, state)
			}

		case axonv1alpha1.TaskPhaseSucceeded:
			vizSetFinalStages(state, current, true)
			state.SetFinished(true, "Quest completed!", current.Status.Outputs, 0, 0)
			return

		case axonv1alpha1.TaskPhaseFailed:
			vizSetFinalStages(state, current, false)
			msg := "Quest failed"
			if current.Status.Message != "" {
				msg = current.Status.Message
			}
			state.SetFinished(false, msg, nil, 0, 0)
			return
		}

		sleepCtx(ctx, 2*time.Second)
	}
}

// vizStreamLogs streams pod logs (init container + agent) into the battle log.
func vizStreamLogs(ctx context.Context, cs *kubernetes.Clientset, ns, podName, agentType string, hasWorkspace bool, state *RPGState) {
	// Stream init container logs if workspace exists.
	if hasWorkspace {
		state.SetStage(StagePortal, StatusActive)
		state.AddBattle(BattleEntry{Type: "status", Content: "Opening portal to git realm..."})
		vizStreamRawLogs(ctx, cs, ns, podName, "git-clone", state)
		state.SetStage(StagePortal, StatusDone)
		state.AddBattle(BattleEntry{Type: "status", Content: "Portal opened. Repository cloned."})
	} else {
		state.SetStage(StagePortal, StatusSkipped)
	}

	// Stream agent container logs.
	state.SetStage(StageBattle, StatusActive)
	state.AddBattle(BattleEntry{Type: "status", Content: "Hero enters the arena..."})
	vizStreamAgentLogs(ctx, cs, ns, podName, agentType, state)
}

// vizStreamRawLogs streams raw text logs from a container into the battle log.
func vizStreamRawLogs(ctx context.Context, cs *kubernetes.Clientset, ns, podName, container string, state *RPGState) {
	opts := &corev1.PodLogOptions{Follow: true, Container: container}
	for {
		stream, err := cs.CoreV1().Pods(ns).GetLogs(podName, opts).Stream(ctx)
		if err != nil {
			if isContainerNotReady(err) {
				sleepCtx(ctx, 2*time.Second)
				continue
			}
			return
		}
		defer stream.Close()

		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" {
				state.AddBattle(BattleEntry{Type: "status", Content: line})
			}
		}
		return
	}
}

// vizStreamAgentLogs streams NDJSON agent logs and parses them into battle entries.
func vizStreamAgentLogs(ctx context.Context, cs *kubernetes.Clientset, ns, podName, agentType string, state *RPGState) {
	opts := &corev1.PodLogOptions{Follow: true, Container: agentType}
	for {
		stream, err := cs.CoreV1().Pods(ns).GetLogs(podName, opts).Stream(ctx)
		if err != nil {
			if isContainerNotReady(err) {
				sleepCtx(ctx, 2*time.Second)
				continue
			}
			return
		}
		defer stream.Close()

		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			for _, entry := range vizParseLogEvents(agentType, line, state) {
				state.AddBattle(entry)
			}
		}
		return
	}
}

// --- Log event parsers ---

// vizParseLogEvents routes NDJSON log lines to the appropriate agent-specific parser.
func vizParseLogEvents(agentType string, line []byte, state *RPGState) []BattleEntry {
	switch agentType {
	case "codex":
		return vizParseCodexEvents(line, state)
	case "gemini":
		return vizParseGeminiEvents(line, state)
	default:
		return vizParseClaudeEvents(line, state)
	}
}

func vizParseClaudeEvents(line []byte, state *RPGState) []BattleEntry {
	var event StreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return nil
	}

	var entries []BattleEntry

	switch event.Type {
	case "system":
		if event.Subtype == "init" && event.Model != "" {
			state.mu.Lock()
			state.Model = event.Model
			state.mu.Unlock()
			entries = append(entries, BattleEntry{
				Type:    "status",
				Content: fmt.Sprintf("Model: %s", event.Model),
			})
		}
	case "assistant":
		state.mu.Lock()
		state.CurrentTurn++
		turn := state.CurrentTurn
		state.mu.Unlock()

		entries = append(entries, BattleEntry{Type: "turn", Turn: turn})

		if event.Message != nil {
			for _, block := range event.Message.Content {
				switch block.Type {
				case "text":
					if block.Text != "" {
						entries = append(entries, BattleEntry{
							Type:    "text",
							Turn:    turn,
							Content: rpgFirstLine(block.Text),
						})
					}
				case "tool_use":
					entries = append(entries, BattleEntry{
						Type:    "tool",
						Turn:    turn,
						Tool:    block.Name,
						Content: toolInputSummary(block.Name, block.Input),
					})
				}
			}
		}
	case "result":
		state.mu.Lock()
		state.TotalCost = event.TotalCostUSD
		state.NumTurns = event.NumTurns
		state.mu.Unlock()

		var msg string
		if event.IsError {
			msg = fmt.Sprintf("Quest ended with errors (%d turns, $%.4f)", event.NumTurns, event.TotalCostUSD)
		} else {
			msg = fmt.Sprintf("Quest completed (%d turns, $%.4f)", event.NumTurns, event.TotalCostUSD)
		}
		entries = append(entries, BattleEntry{Type: "result", Content: msg})
	}

	return entries
}

func vizParseCodexEvents(line []byte, state *RPGState) []BattleEntry {
	var event CodexEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return nil
	}

	var entries []BattleEntry

	switch event.Type {
	case "turn.started":
		state.mu.Lock()
		state.CurrentTurn++
		turn := state.CurrentTurn
		state.mu.Unlock()
		entries = append(entries, BattleEntry{Type: "turn", Turn: turn})
	case "item.started":
		if event.Item != nil && event.Item.Type == "command_execution" {
			cmd := rpgFirstLine(event.Item.Command)
			entries = append(entries, BattleEntry{Type: "tool", Tool: "Bash", Content: cmd})
		}
	case "item.completed":
		if event.Item != nil && event.Item.Type == "agent_message" && event.Item.Text != "" {
			entries = append(entries, BattleEntry{Type: "text", Content: rpgFirstLine(event.Item.Text)})
		}
	}

	return entries
}

func vizParseGeminiEvents(line []byte, state *RPGState) []BattleEntry {
	var event GeminiEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return nil
	}

	var entries []BattleEntry

	switch event.Type {
	case "init":
		if event.Model != "" {
			state.mu.Lock()
			state.Model = event.Model
			state.mu.Unlock()
			entries = append(entries, BattleEntry{
				Type:    "status",
				Content: fmt.Sprintf("Model: %s", event.Model),
			})
		}
	case "message":
		if event.Role == "assistant" && event.Content != "" && !event.Delta {
			state.mu.Lock()
			state.CurrentTurn++
			turn := state.CurrentTurn
			state.mu.Unlock()
			entries = append(entries, BattleEntry{Type: "turn", Turn: turn})
			entries = append(entries, BattleEntry{Type: "text", Turn: turn, Content: rpgFirstLine(event.Content)})
		}
	case "tool_use":
		if event.ToolName != "" {
			summ := geminiToolSummary(event.ToolName, event.Parameters)
			entries = append(entries, BattleEntry{Type: "tool", Tool: event.ToolName, Content: summ})
		}
	case "result":
		var msg string
		if event.Status == "error" {
			msg = "Quest ended with errors"
		} else {
			msg = "Quest completed"
		}
		if event.Stats != nil {
			msg += fmt.Sprintf(" (input=%d, output=%d tokens)", event.Stats.TotalInputTokens, event.Stats.TotalOutputTokens)
		}
		entries = append(entries, BattleEntry{Type: "result", Content: msg})
	}

	return entries
}

// --- Helpers ---

// vizSetFinalStages updates all stages to their final state.
func vizSetFinalStages(state *RPGState, task *axonv1alpha1.Task, success bool) {
	state.SetStage(StageSummon, StatusDone)
	if task.Spec.WorkspaceRef != nil {
		state.SetStage(StagePortal, StatusDone)
	} else {
		state.SetStage(StagePortal, StatusSkipped)
	}
	if success {
		state.SetStage(StageBattle, StatusDone)
		state.SetStage(StageResolution, StatusDone)
	} else {
		state.SetStage(StageBattle, StatusError)
		state.SetStage(StageResolution, StatusError)
	}
}

// vizPrintSummary prints a text summary after leaving the alternate screen.
func vizPrintSummary(state *RPGState) {
	state.mu.Lock()
	defer state.mu.Unlock()

	fmt.Println()
	if state.Success {
		fmt.Printf("%s%sQuest completed!%s\n", ansiBold, ansiGreen, ansiReset)
	} else {
		fmt.Printf("%s%sQuest failed%s\n", ansiBold, ansiRed, ansiReset)
		if state.Message != "" && state.Message != "Quest failed" {
			fmt.Printf("  %s\n", state.Message)
		}
	}

	fmt.Printf("Task: %s  |  %s", state.TaskName, state.HeroType)
	if state.NumTurns > 0 {
		fmt.Printf("  |  %d turns", state.NumTurns)
	}
	if state.TotalCost > 0 {
		fmt.Printf("  |  $%.4f", state.TotalCost)
	}
	elapsed := time.Since(state.StartTime).Truncate(time.Second)
	fmt.Printf("  |  %s\n", rpgFmtDuration(elapsed))

	if len(state.Outputs) > 0 {
		fmt.Println("Outputs:")
		for _, o := range state.Outputs {
			fmt.Printf("  %s\n", o)
		}
	}
	fmt.Println()
}

// vizGetTermSize returns the terminal width and height, falling back to 80x24.
func vizGetTermSize() (int, int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w == 0 || h == 0 {
		return 80, 24
	}
	return w, h
}

// sleepCtx sleeps for the given duration or until the context is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// --- Demo mode ---

// runVizDemo runs a simulated task lifecycle for testing the RPG visualization
// without a Kubernetes cluster or credentials.
func runVizDemo() error {
	state := NewRPGState(
		"Fix the authentication bug and add unit tests",
		"claude-code",
		"default",
		"task-demo",
		"claude-sonnet-4-20250514",
	)

	// Enter alternate screen buffer and hide cursor.
	fmt.Print("\033[?1049h\033[?25l")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	stopRender := make(chan struct{})
	renderDone := make(chan struct{})
	go func() {
		defer close(renderDone)
		vizRenderLoop(ctx, state, stopRender)
	}()

	cleanup := func() {
		close(stopRender)
		<-renderDone
		fmt.Print("\033[?1049l\033[?25h")
	}

	// Simulate the full quest lifecycle.
	if !demoSimulate(ctx, state) {
		cleanup()
		return nil
	}

	// Let the user see the final state.
	sleepCtx(ctx, 3*time.Second)
	cleanup()
	vizPrintSummary(state)
	return nil
}

// demoSimulate drives fake stage transitions and battle log events.
// Returns false if the context was cancelled (user pressed Ctrl+C).
func demoSimulate(ctx context.Context, state *RPGState) bool {
	sleep := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Stage 1: Credentials
	state.SetStage(StageCredentials, StatusActive)
	if !sleep(800 * time.Millisecond) {
		return false
	}
	state.SetStage(StageCredentials, StatusDone)

	// Stage 2: Workspace
	state.SetStage(StageWorkspace, StatusActive)
	if !sleep(600 * time.Millisecond) {
		return false
	}
	state.SetStage(StageWorkspace, StatusDone)

	// Stage 3: Dispatch
	state.SetStage(StageDispatch, StatusActive)
	if !sleep(500 * time.Millisecond) {
		return false
	}
	state.SetStage(StageDispatch, StatusDone)

	// Stage 4: Summon hero
	state.SetStage(StageSummon, StatusActive)
	if !sleep(2 * time.Second) {
		return false
	}
	state.SetStage(StageSummon, StatusDone)

	// Stage 5: Portal (git clone)
	state.SetStage(StagePortal, StatusActive)
	state.AddBattle(BattleEntry{Type: "status", Content: "Opening portal to git realm..."})
	if !sleep(800 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "status", Content: "Cloning into '/workspace'..."})
	if !sleep(1200 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "status", Content: "Portal opened. Repository cloned."})
	state.SetStage(StagePortal, StatusDone)

	// Stage 6: Battle
	state.SetStage(StageBattle, StatusActive)
	state.AddBattle(BattleEntry{Type: "status", Content: "Hero enters the arena..."})
	state.AddBattle(BattleEntry{Type: "status", Content: "Model: claude-sonnet-4-20250514"})
	if !sleep(1 * time.Second) {
		return false
	}

	// Turn 1: Exploration
	state.AddBattle(BattleEntry{Type: "turn", Turn: 1})
	state.AddBattle(BattleEntry{Type: "text", Turn: 1, Content: "I'll start by examining the authentication module to understand the bug."})
	if !sleep(600 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 1, Tool: "Read", Content: "src/auth/handler.go"})
	if !sleep(400 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 1, Tool: "Read", Content: "src/auth/middleware.go"})
	if !sleep(400 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 1, Tool: "Grep", Content: "func.*Login"})
	if !sleep(400 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 1, Tool: "Read", Content: "src/auth/token.go"})
	if !sleep(1 * time.Second) {
		return false
	}

	// Turn 2: Fix the bug
	state.AddBattle(BattleEntry{Type: "turn", Turn: 2})
	state.AddBattle(BattleEntry{Type: "text", Turn: 2, Content: "Found the bug! The token expiry check uses `<` instead of `<=`. Fixing now."})
	if !sleep(800 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 2, Tool: "Edit", Content: "src/auth/token.go"})
	if !sleep(600 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 2, Tool: "Bash", Content: "go build ./..."})
	if !sleep(1200 * time.Millisecond) {
		return false
	}

	// Turn 3: Write tests
	state.AddBattle(BattleEntry{Type: "turn", Turn: 3})
	state.AddBattle(BattleEntry{Type: "text", Turn: 3, Content: "Now I'll add comprehensive unit tests for the auth module."})
	if !sleep(600 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 3, Tool: "Write", Content: "src/auth/handler_test.go"})
	if !sleep(500 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 3, Tool: "Write", Content: "src/auth/token_test.go"})
	if !sleep(500 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 3, Tool: "Bash", Content: "go test ./src/auth/... -v"})
	if !sleep(1500 * time.Millisecond) {
		return false
	}

	// Turn 4: Push and PR
	state.AddBattle(BattleEntry{Type: "turn", Turn: 4})
	state.AddBattle(BattleEntry{Type: "text", Turn: 4, Content: "All tests pass. Pushing branch and opening a PR."})
	if !sleep(500 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 4, Tool: "Bash", Content: "git checkout -b fix/auth-token-expiry"})
	if !sleep(400 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 4, Tool: "Bash", Content: "git add -A && git commit -m \"fix: token expiry boundary check\""})
	if !sleep(400 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 4, Tool: "Bash", Content: "git push origin fix/auth-token-expiry"})
	if !sleep(600 * time.Millisecond) {
		return false
	}
	state.AddBattle(BattleEntry{Type: "tool", Turn: 4, Tool: "Bash", Content: "gh pr create --title \"Fix auth token expiry\" --body \"...\""})
	if !sleep(800 * time.Millisecond) {
		return false
	}

	// Result
	state.AddBattle(BattleEntry{Type: "result", Content: "Quest completed (4 turns, $0.0312)"})

	state.mu.Lock()
	state.CurrentTurn = 4
	state.NumTurns = 4
	state.TotalCost = 0.0312
	state.mu.Unlock()

	state.SetStage(StageBattle, StatusDone)
	state.SetStage(StageResolution, StatusDone)
	state.SetFinished(true, "Quest completed!",
		[]string{"https://github.com/your-org/repo/pull/42"},
		0.0312, 4,
	)

	return true
}
