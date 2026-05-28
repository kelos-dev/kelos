# Cody Slack Thread Sessions - Codex App Server Implementation Spec

## Status

Draft implementation spec.

This document expands `2026-05-23-12-31-cody-slack-thread-sessions.md` into
an implementable Kelos design. The main runtime decision is to use Codex App
Server as the primary session runtime for Cody thread sessions.

## References Checked

- OpenAI Codex App Server docs:
  https://developers.openai.com/codex/app-server
- OpenAI Codex SDK docs:
  https://developers.openai.com/codex/sdk
- OpenAI Codex non-interactive mode docs:
  https://developers.openai.com/codex/noninteractive
- Local Kelos Codex image pins `@openai/codex@0.132.0`.
- Local developer machine currently has `codex-cli 0.128.0`.

The OpenAI docs describe App Server as a JSON-RPC control surface with
`thread/start`, `thread/resume`, `turn/start`, `turn/steer`,
`thread/read`, and `thread/turns/list`. They also document streaming
thread, turn, item, and agent-message events. Non-interactive mode supports
`codex exec resume`, but that remains a one-shot process interface.

## Decision

Use Codex App Server for Slack thread sessions.

The first implementation should not build sessions on top of repeated
`codex exec resume` calls. `codex exec resume` is useful for manual or
pipeline continuation, but it does not give Kelos a clean long-lived control
surface for:

- starting and resuming an explicit Codex thread;
- submitting multiple turns to that thread;
- subscribing to turn and item events;
- distinguishing active-turn steering from queued future turns;
- inspecting thread status without parsing CLI session files.

Codex App Server maps directly onto the desired Cody product model:

```text
Slack root thread -> Kelos AgentSession -> Codex App Server thread
Explicit @cody message -> Kelos AgentTurn -> Codex App Server turn
```

## Implementation Goals

- Add opt-in Slack session mode without changing existing one-shot
  `TaskSpawner` behavior.
- Keep every user-to-Cody turn explicitly gated by an `@cody` mention.
- Preserve side conversation as context only when a later explicit turn is
  created.
- Keep Slack credentials centralized in `kelos-slack-server`; Cody and the
  session runner do not call Slack APIs.
- Run one ordered Codex turn at a time for a Slack thread.
- Store the Codex App Server `threadId` on the Kelos session status.
- Make failure states explicit. Do not silently create a one-shot `Task` if a
  session turn fails.

## Non-Goals

- No unmentioned Slack follow-ups.
- No scheduled or autonomous turns.
- No route switching inside an active session.
- No support for non-Codex session runners in the first implementation.
- No `turn/steer` in the first implementation. Follow-up mentions are queued as
  future turns, not injected into the active turn.
- No hidden fallback from App Server to `codex exec resume`.
- No Slack transcript replay as the source of runtime continuity.

## Public API Changes

### TaskSpawner Slack Session Config

Add `session` to `TaskSpawner.spec.when.slack`.

File:

- `api/v1alpha1/taskspawner_types.go`

Sketch:

```go
type Slack struct {
    Channels []string `json:"channels,omitempty"`
    Triggers []SlackTrigger `json:"triggers,omitempty"`
    ExcludePatterns []string `json:"excludePatterns,omitempty"`
    Session *SlackSession `json:"session,omitempty"`
}

type SlackSessionContextWindow string

const (
    SlackSessionContextWindowSinceLastAgentMessage SlackSessionContextWindow = "SinceLastAgentMessage"
)

type SlackSession struct {
    Enabled bool `json:"enabled,omitempty"`
    RequireMentionForTurns *bool `json:"requireMentionForTurns,omitempty"`
    ContextWindow SlackSessionContextWindow `json:"contextWindow,omitempty"`
    IdleTimeout *metav1.Duration `json:"idleTimeout,omitempty"`
    MaxQueuedTurns *int32 `json:"maxQueuedTurns,omitempty"`
}
```

Validation:

- `requireMentionForTurns` may only be `true` in the first implementation.
- `contextWindow` may only be `SinceLastAgentMessage` in the first
  implementation.
- `idleTimeout` must be positive when set.
- `maxQueuedTurns` must be at least `1` when set.

Defaults in code:

- `enabled: false`
- `requireMentionForTurns: true`
- `contextWindow: SinceLastAgentMessage`
- `idleTimeout: 1h`
- `maxQueuedTurns: 5`

Do not set a kubebuilder default for `enabled`; preserving nil/empty as current
one-shot behavior is safer for existing clusters.

### AgentSession CRD

Add a new namespaced CRD.

Files:

- `api/v1alpha1/agentsession_types.go`
- `api/v1alpha1/groupversion_info.go`
- generated deepcopies, clientsets, informers, listers, CRD manifests

Purpose: durable session state for one Slack thread and one originating
`TaskSpawner`.

Sketch:

```go
type AgentSessionPhase string

const (
    AgentSessionPhasePending AgentSessionPhase = "Pending"
    AgentSessionPhaseStarting AgentSessionPhase = "Starting"
    AgentSessionPhaseIdle AgentSessionPhase = "Idle"
    AgentSessionPhaseRunning AgentSessionPhase = "Running"
    AgentSessionPhaseClosing AgentSessionPhase = "Closing"
    AgentSessionPhaseClosed AgentSessionPhase = "Closed"
    AgentSessionPhaseError AgentSessionPhase = "Error"
)

type AgentSession struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec AgentSessionSpec `json:"spec,omitempty"`
    Status AgentSessionStatus `json:"status,omitempty"`
}

type AgentSessionSpec struct {
    Source AgentSessionSource `json:"source"`
    TaskSpawnerRef TaskSpawnerReference `json:"taskSpawnerRef"`
    TaskTemplateSnapshot TaskSpec `json:"taskTemplateSnapshot"`
    Route AgentSessionRoute `json:"route"`
    IdleTimeout metav1.Duration `json:"idleTimeout"`
    MaxQueuedTurns int32 `json:"maxQueuedTurns"`
}

type AgentSessionSource struct {
    Type string `json:"type"` // SlackThread
    TeamID string `json:"teamID,omitempty"`
    ChannelID string `json:"channelID"`
    RootTS string `json:"rootTS"`
    ThreadURL string `json:"threadURL,omitempty"`
}

type AgentSessionRoute struct {
    TriggerIndex *int32 `json:"triggerIndex,omitempty"`
    TriggerPattern string `json:"triggerPattern,omitempty"`
    InitialText string `json:"initialText,omitempty"`
}

type AgentSessionStatus struct {
    Phase AgentSessionPhase `json:"phase,omitempty"`
    CodexThreadID string `json:"codexThreadID,omitempty"`
    RunnerJobName string `json:"runnerJobName,omitempty"`
    RunnerPodName string `json:"runnerPodName,omitempty"`
    CurrentTurn string `json:"currentTurn,omitempty"`
    LastCompletedTurn string `json:"lastCompletedTurn,omitempty"`
    LastAgentMessageTS string `json:"lastAgentMessageTS,omitempty"`
    LastActivityAt *metav1.Time `json:"lastActivityAt,omitempty"`
    QueuedTurns int32 `json:"queuedTurns,omitempty"`
    Message string `json:"message,omitempty"`
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

Naming:

```text
<taskspawner-name>-slack-session-<sha12>
```

Hash input:

```text
teamID + "\n" + channelID + "\n" + rootTS + "\n" + namespace + "\n" + taskSpawnerName
```

Labels:

- `kelos.dev/source=slack`
- `kelos.dev/taskspawner=<name>`
- `kelos.dev/slack-channel=<channelID>`
- `kelos.dev/slack-root-ts=<rootTS sanitized>`
- `kelos.dev/agent-session=<sessionName>`

The `TaskTemplateSnapshot` is intentional. A running Slack session should not
change image, credentials, AgentConfig, MCP setup, or service account because
someone edits the originating `TaskSpawner` while the session is alive.

### AgentTurn CRD

Add a new namespaced CRD.

Files:

- `api/v1alpha1/agentturn_types.go`
- generated deepcopies, clientsets, informers, listers, CRD manifests

Purpose: one explicit `@cody` turn in an `AgentSession`.

Sketch:

```go
type AgentTurnPhase string

const (
    AgentTurnPhaseQueued AgentTurnPhase = "Queued"
    AgentTurnPhaseRunning AgentTurnPhase = "Running"
    AgentTurnPhaseSucceeded AgentTurnPhase = "Succeeded"
    AgentTurnPhaseFailed AgentTurnPhase = "Failed"
    AgentTurnPhaseCanceled AgentTurnPhase = "Canceled"
)

type AgentTurn struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec AgentTurnSpec `json:"spec,omitempty"`
    Status AgentTurnStatus `json:"status,omitempty"`
}

type AgentTurnSpec struct {
    SessionRef AgentSessionReference `json:"sessionRef"`
    Sequence int32 `json:"sequence"`
    Source AgentTurnSource `json:"source"`
    Input AgentTurnInput `json:"input"`
    Context AgentTurnContext `json:"context"`
}

type AgentTurnSource struct {
    Type string `json:"type"` // SlackMessage
    TeamID string `json:"teamID,omitempty"`
    ChannelID string `json:"channelID"`
    RootTS string `json:"rootTS"`
    MessageTS string `json:"messageTS"`
    UserID string `json:"userID,omitempty"`
    BotID string `json:"botID,omitempty"`
    Permalink string `json:"permalink,omitempty"`
}

type AgentTurnInput struct {
    Text string `json:"text"`
    Body string `json:"body"`
}

type AgentTurnContext struct {
    Mode SlackSessionContextWindow `json:"mode"`
    FromTSExclusive string `json:"fromTSExclusive,omitempty"`
    ToTSInclusive string `json:"toTSInclusive"`
    Transcript string `json:"transcript"`
    TranscriptBytes int32 `json:"transcriptBytes,omitempty"`
}

type AgentTurnStatus struct {
    Phase AgentTurnPhase `json:"phase,omitempty"`
    StartedAt *metav1.Time `json:"startedAt,omitempty"`
    CompletedAt *metav1.Time `json:"completedAt,omitempty"`
    CodexTurnID string `json:"codexTurnID,omitempty"`
    SlackProgressMessageTS string `json:"slackProgressMessageTS,omitempty"`
    SlackAgentMessageTS string `json:"slackAgentMessageTS,omitempty"`
    ResultText string `json:"resultText,omitempty"`
    Activity string `json:"activity,omitempty"`
    Message string `json:"message,omitempty"`
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

Labels:

- `kelos.dev/source=slack`
- `kelos.dev/agent-session=<sessionName>`
- `kelos.dev/taskspawner=<taskSpawnerName>`
- `kelos.dev/slack-reporting=enabled`

Retention:

- Finished turns should be deleted when the owning session is deleted.
- The session owns turns via owner references.
- The first implementation does not need a separate TTL field on
  `AgentTurn`.

CRD size protection:

- Set a hard maximum for `AgentTurn.spec.context.transcript`, for example
  `128 KiB`.
- If the Slack delta transcript exceeds the limit, fail the turn before
  starting Cody and post an explicit Slack error.
- Do not silently truncate the current explicit request.

## Component Changes

### `kelos-slack-server`

Files:

- `cmd/kelos-slack-server/main.go`
- `internal/slack/handler.go`
- `internal/slack/thread.go`
- `internal/slack/filter.go`
- `internal/reporting/watcher.go`
- new `internal/reporting/slack_turn.go`

Responsibilities:

1. Receive Slack Socket Mode events.
2. Match messages to Slack `TaskSpawner`s.
3. For one-shot spawners, keep creating `Task` objects exactly as today.
4. For session-enabled spawners, create or reuse `AgentSession`.
5. Create `AgentTurn` only when the message is an explicit Cody mention.
6. Fetch and materialize the side-conversation delta at turn creation time.
7. Report queued, running, succeeded, failed, and canceled turn states to Slack.
8. Write terminal Slack message timestamps back to `AgentTurn.status` and
   `AgentSession.status.lastAgentMessageTS`.

The Slack server remains the only component with Slack API credentials.

### Slack Routing Refactor

Current behavior fetches the full Slack thread before routing a thread reply.
Session mode should split routing from transcript construction:

1. Enrich the incoming Slack event into `SlackMessageData`.
2. Match configured Slack spawners against the message text.
3. If the matching spawner is not session-enabled, keep the current full-thread
   context behavior and create a `Task`.
4. If the matching spawner is session-enabled, use session routing and do not
   create a one-shot `Task`.

For session-enabled spawners:

- root message: create session and first turn;
- thread reply with no Cody mention: create nothing;
- thread reply with Cody mention and existing session: create a turn;
- thread reply with Cody mention and no existing session: allow it to start a
  session only if exactly one session-enabled spawner matches;
- multiple matching sessions: reject with an in-thread ambiguity message.

### Route Identity

The first turn owns the session route:

```text
namespace + "/" + taskSpawnerName + optional triggerIndex
```

Follow-up behavior:

- `@cody continue` is accepted for the existing session.
- `@cody !alpha continue` is accepted only for an alpha-originated session.
- `@cody !dev continue` in a normal session is rejected.

Implementation detail:

- extend `MatchesSpawner` or add a sibling matcher that can return match
  metadata: matched trigger index, stripped text, and whether a mention was
  present.
- preserve existing `MatchesSpawner` behavior for one-shot paths.

### Delta Transcript Construction

Add a helper around `FetchThreadReplies`:

```go
func BuildSlackDeltaTranscript(
    replies []slack.Message,
    botUserID string,
    botID string,
    fromTSExclusive string,
    toTSInclusive string,
) (string, int, error)
```

Inclusion rules:

- include human messages in range;
- include allowed non-Cody bot messages in range as context;
- include attachment text when present;
- include author display name, user ID or bot ID, and Slack timestamp;
- include the explicit triggering message verbatim.

Exclusion rules:

- exclude Cody-authored progress and terminal messages;
- exclude messages at or before the anchor;
- exclude messages after the triggering mention;
- exclude message subtype edits/deletes/replies already ignored by the handler.

Anchor:

1. `AgentSession.status.lastAgentMessageTS` when set.
2. Otherwise root timestamp exclusive for a follow-up that starts a session from
   an existing thread.
3. Empty for a root first turn.

The resulting transcript is stored on `AgentTurn.spec.context.transcript`.
The session runner consumes the CRD; it does not call Slack.

### Slack Turn Reporting

Add an `AgentTurn` reporter parallel to `SlackTaskReporter`.

Behavior:

- `Queued`: post a short queued acknowledgement only when another turn is
  already active or queued before it.
- `Running`: post or update an in-thread progress message.
- `Succeeded`: update the progress message to Cody's final response when
  possible; post additional chunks if needed.
- `Failed`: update the progress message to a clear failure message.
- `Canceled`: update the progress message to a cancellation message.

Terminal timestamp rule:

- record the timestamp of the final Cody response set on
  `AgentTurn.status.slackAgentMessageTS`;
- update `AgentSession.status.lastAgentMessageTS` to that timestamp after all
  terminal Slack writes succeed.

This status write is mandatory because future turns use it as the context
anchor.

### AgentSession Controller

Add an `AgentSessionReconciler`.

Files:

- `internal/controller/agentsession_controller.go`
- `internal/controller/session_job_builder.go`
- controller setup in `cmd/kelos-controller/main.go`
- tests under `internal/controller`

Responsibilities:

1. Add a finalizer to each session.
2. Create one runner Job for `Pending` sessions.
3. Update session phase to `Starting` and then `Idle` once the runner records a
   ready condition.
4. Track runner Job and Pod names.
5. Close idle sessions after `spec.idleTimeout` when there are no running or
   queued turns.
6. Delete runner Jobs when sessions are closed or deleted.
7. Mark sessions `Error` if the runner Job fails.
8. Never create a one-shot `Task` as a hidden replacement path.

Session Job shape:

- one Kubernetes Job per `AgentSession`;
- same Codex image as the session's `TaskTemplateSnapshot.image`;
- command `/kelos-session-runner`;
- workspace, AgentConfig, MCP, credentials, env, service account, volumes,
  security context, and image pull secrets copied from the task template
  snapshot using the same semantics as current `JobBuilder`;
- workspace `emptyDir` survives for the life of the runner pod and is shared by
  all turns in that session;
- `CODEX_HOME` must also live in a path that survives App Server child process
  restarts inside the same pod, for example the agent user's normal home
  directory or a mounted session volume.

Implementation note:

- refactor existing `JobBuilder` enough to share workspace clone, AgentConfig
  injection, credentials, plugin volume, MCP config, and pod override logic;
- avoid copy-pasting the entire `buildAgentJob` path into a divergent session
  builder.

### AgentTurn Controller

Do not add an active execution controller for turns in the first design.

The session runner owns turn execution because it is the component connected to
Codex App Server. A lightweight `AgentTurnReconciler` is optional only for
finalizer/cleanup behavior. Creation, status changes, and Slack reporting can
work without a separate turn controller.

## Codex Session Runner

Add a new Go binary:

- `cmd/kelos-session-runner`

Build and image changes:

- compile `cmd/kelos-session-runner` for agent images;
- copy `bin/kelos-session-runner-linux-${TARGETARCH}` into `codex/Dockerfile`;
- make `/kelos-session-runner` executable;
- keep `/kelos_entrypoint.sh` for one-shot tasks.

Runtime inputs:

- `KELOS_AGENT_SESSION_NAME`
- `KELOS_AGENT_SESSION_NAMESPACE`
- `KELOS_MODEL` when configured
- `KELOS_AGENTS_MD` when configured
- `KELOS_MCP_SERVERS` when configured
- `KELOS_PLUGIN_DIR` when configured
- existing credential env vars such as `CODEX_AUTH_JSON` or `CODEX_API_KEY`
- existing workspace and setup env vars

Startup sequence:

1. Run the same pre-agent setup currently performed by
   `/kelos_entrypoint.sh`, including:
   - `kelos-agent-setup`;
   - writing `~/.codex/auth.json` from `CODEX_AUTH_JSON`;
   - writing `~/.codex/AGENTS.md` from `KELOS_AGENTS_MD`;
   - writing `~/.codex/config.toml` MCP server blocks from
     `KELOS_MCP_SERVERS`;
   - running `KELOS_SETUP_COMMAND` if configured.
2. Start `codex app-server --listen stdio://` as a child process.
3. Open a JSON-RPC client over the child process stdin/stdout.
4. Load the `AgentSession`.
5. If `status.codexThreadID` is empty, call `thread/start`.
6. If `status.codexThreadID` is set because the App Server child process
   restarted inside the same runner pod, call `thread/resume`.
7. If this is a new pod and the stored Codex thread files are unavailable,
   mark the session `Error`; do not claim continuity from the Kubernetes
   status field alone.
8. Patch `AgentSession.status.codexThreadID` and a ready condition.
9. Watch queued `AgentTurn` objects for the session.
10. Execute one turn at a time until idle timeout, close, fatal error, or pod
   shutdown.

The runner should use controller-runtime client-go informers or Kubernetes
watch APIs, not shell out to `kubectl`.

### App Server Calls

Thread creation:

```json
{
  "method": "thread/start",
  "id": 1,
  "params": {}
}
```

Thread resume after App Server child process restart in the same runner pod:

```json
{
  "method": "thread/resume",
  "id": 2,
  "params": {
    "threadId": "thr_..."
  }
}
```

Turn start:

```json
{
  "method": "turn/start",
  "id": 3,
  "params": {
    "threadId": "thr_...",
    "input": [
      {
        "type": "text",
        "text": "<rendered Kelos turn prompt>"
      }
    ],
    "cwd": "/workspace/repo",
    "model": "<KELOS_MODEL if set>"
  }
}
```

Sandbox and approval settings should preserve today's Cody behavior. Current
one-shot Cody runs use `codex exec --dangerously-bypass-approvals-and-sandbox`
inside a Kubernetes-controlled pod. The App Server call should use the
equivalent App Server fields for the installed Codex version. Verify the exact
field and enum names from `codex app-server generate-json-schema` for
`@openai/codex@0.132.0` before implementation.

Expected intent:

```json
{
  "approvalPolicy": "never",
  "sandboxPolicy": {
    "type": "dangerFullAccess"
  }
}
```

If the 0.132.0 schema uses different names for the same behavior, use the
schema names and document them in the PR.

### App Server Event Handling

The runner reads JSON-RPC notifications until the turn reaches a terminal
state.

Handle at minimum:

- `thread/started`
- `thread/status/changed`
- `turn/started` or the `turn/start` result
- `turn/completed`
- `turn/failed` or equivalent terminal error event
- `item/started`
- `item/completed`
- `item/agentMessage/delta`

The exact event names should be verified against the generated App Server
TypeScript bindings or JSON schema for the image Codex version.

Status updates:

- append agent-message deltas into an in-memory final response buffer;
- periodically patch `AgentTurn.status.activity` with concise activity text;
- patch `AgentTurn.status.resultText` only at terminal success;
- patch `AgentTurn.status.message` with errors at terminal failure;
- patch `AgentSession.status.lastActivityAt` on every meaningful event.

Unsupported server requests:

- If App Server asks the client for interactive user input, the first
  implementation should fail the current turn with `UserInputUnsupported`.
- Cody can still ask the human for more information in its terminal Slack
  response. The human can then explicitly mention `@cody` again to create the
  next queued turn.

### Rendered Turn Prompt

The runner renders one text item per `AgentTurn`.

Template:

```text
You are continuing an existing Cody Slack session.

Session:
- Kelos session: <namespace>/<session>
- Slack thread: <thread URL>
- TaskSpawner route: <namespace>/<taskspawner>
- Turn sequence: <sequence>
- Previous Cody answer timestamp: <lastAgentMessageTS or none>

Conversation since your last terminal answer:
<AgentTurn.spec.context.transcript or "(none)">

Current explicit request:
<author> [<user id or bot id> at <message ts>]:
<AgentTurn.spec.input.text>

Reply once in the same Slack thread through the Kelos reporter.
Do not assume messages outside the provided delta happened after your last
answer. If you need more information, ask for it in your final answer.
```

Because App Server persists the Codex thread, the runner should not replay old
Cody terminal responses or full historical Slack thread text into every turn.
The App Server thread plus the delta transcript are the continuity model.

### Turn Queue Semantics

The runner processes queued turns FIFO by `spec.sequence`.

Rules:

- only one `AgentTurn` may be `Running` per `AgentSession`;
- if the runner sees multiple queued turns, it picks the lowest sequence;
- if a running turn exists after App Server child process restart, resume the
  thread only when the local Codex thread files are still present and App Server
  can prove the turn is still active; otherwise mark the turn failed;
- the first implementation does not use `turn/steer` for active turns;
- `@cody stop` or `@cody close` creates a control turn that marks the session
  `Closing` after the active turn completes;
- `@cody cancel` cancels queued turns created by the same Slack user.

### Runner Shutdown

On SIGTERM:

1. stop accepting new turns;
2. if no turn is running, patch session `Closing`;
3. if a turn is running, patch activity as `Runner shutting down`;
4. exit with non-zero if the active turn cannot be completed cleanly.

The controller then marks the session `Error` if the Job fails. It does not
silently create a replacement one-shot task.

## RBAC

Controller needs:

- get/list/watch/create/update/patch/delete `agentsessions`;
- update/patch `agentsessions/status`;
- get/list/watch/create/update/patch/delete `agentturns`;
- update/patch `agentturns/status`;
- create/update/patch/delete Jobs;
- get/list/watch Pods.

Slack server needs:

- get/list/watch `taskspawners`;
- get/list/watch/create/update/patch `agentsessions`;
- update/patch `agentsessions/status`;
- get/list/watch/create/update/patch `agentturns`;
- update/patch `agentturns/status`;
- get/list/watch `tasks` for existing reporting loops;
- existing Slack task reporting permissions.

Session runner service account needs:

- get/list/watch `agentsessions`;
- patch/update `agentsessions/status`;
- get/list/watch `agentturns`;
- patch/update `agentturns/status`.

The runner job also preserves the originating task template service account.
For Alpheya Cody sessions this is expected to remain `cody-debugger`, because
Cody needs the same cluster investigation surface as one-shot Cody tasks.

## Generated Artifacts

Implementation must update generated and embedded manifests:

- `api/v1alpha1/zz_generated.deepcopy.go`
- `pkg/generated/clientset/...`
- `pkg/generated/informers/...`
- `pkg/generated/listers/...`
- `internal/manifests/install-crd.yaml`
- `internal/manifests/charts/kelos/templates/crds/*.yaml`
- `internal/manifests/charts/kelos/templates/rbac.yaml`

Run:

```bash
make update
make verify
```

If `make verify` requires tools that are missing locally, install through the
existing Makefile targets rather than hand-editing generated files.

## File-Level Work Plan

### Step 1 - API Types

Edit:

- `api/v1alpha1/taskspawner_types.go`
- new `api/v1alpha1/agentsession_types.go`
- new `api/v1alpha1/agentturn_types.go`
- `api/v1alpha1/groupversion_info.go`

Then run generators.

### Step 2 - Slack Session Routing

Edit:

- `internal/slack/filter.go`
- `internal/slack/handler.go`
- `internal/slack/thread.go`
- tests in `internal/slack/*_test.go`

Add:

- session name helper;
- match metadata helper;
- delta transcript builder;
- session create/reuse helper;
- turn create helper;
- route mismatch and ambiguity Slack errors.

Keep the existing one-shot `createTask` path unchanged for
`session.enabled != true`.

### Step 3 - Session Controller and Job Builder

Edit/add:

- `internal/controller/agentsession_controller.go`
- `internal/controller/session_job_builder.go`
- `internal/controller/job_builder.go` shared helper refactor
- `cmd/kelos-controller/main.go`
- tests in `internal/controller`

The session builder should reuse existing workspace clone and AgentConfig
materialization behavior.

### Step 4 - Session Runner

Add:

- `cmd/kelos-session-runner/main.go`
- `internal/codexappserver/client.go`
- `internal/codexappserver/client_test.go`
- possibly `internal/sessionrunner/*`

Edit:

- `codex/Dockerfile`
- `Makefile`

The App Server JSON-RPC client should be unit-tested with fake stdout/stdin
streams so turn state transitions do not require real Codex in unit tests.

### Step 5 - Slack Turn Reporter

Edit/add:

- `internal/reporting/slack_turn.go`
- `internal/reporting/slack_turn_test.go`
- `cmd/kelos-slack-server/main.go`

The reporting loop should list `AgentTurn`s with
`kelos.dev/slack-reporting=enabled` and report status transitions like the
existing task reporter.

### Step 6 - Manifests, RBAC, Helm

Edit generated output via `make update`.

Review:

- Helm CRD files render valid YAML;
- controller ClusterRole includes sessions and turns;
- slack-server ClusterRole includes sessions and turns;
- image build includes `kelos-session-runner` in the Codex image.

### Step 7 - Rollout Config

GitOps should first add a separate test Slack route using `!session`.

This should be a distinct `TaskSpawner`, not a change to the existing normal
or `!alpha` Cody routes. The first test route should match:

```yaml
when:
  slack:
    triggers:
      - pattern: ^!session\b
    session:
      enabled: true
```

That keeps today's broad `@cody ...` and `@cody !alpha ...` behavior on the
existing one-shot `Task` path while the session runtime is validated.

After `!session` is stable, GitOps can enable `session.enabled: true` for
`!alpha`. Normal Cody remains one-shot until alpha is proven stable.

Normal Cody stays one-shot until alpha validates:

- root mention starts a session;
- unmentioned replies do nothing;
- explicit follow-up creates a second turn in same session;
- side conversation delta is present;
- Cody has the same MCP/tool surface;
- App Server thread ID persists across turns.

## Error Handling

### Slack Fetch Failure

If Slack replies cannot be fetched while building a turn context, do not create
a runnable turn. Post a Slack error and, if a turn object was already created,
mark it `Failed` with reason `SlackThreadFetchFailed`.

### Transcript Too Large

If the materialized transcript exceeds the configured limit, mark the turn
`Failed` with reason `ContextTooLarge` and post a Slack error. Do not run Cody
with partial context.

### App Server Startup Failure

If `codex app-server` fails to start or initialize:

- mark `AgentSession` as `Error`;
- mark the active turn, if any, as `Failed`;
- include the runner Job and Pod name in status;
- rely on Slack turn reporter to notify the thread.

### App Server Turn Failure

If App Server reports a terminal turn error:

- mark `AgentTurn` as `Failed`;
- keep `AgentSession` `Idle` if the App Server thread remains usable;
- mark `AgentSession` `Error` only if the App Server process or thread becomes
  unusable.

### Runner Crash

If the runner Job fails:

- mark session `Error`;
- mark current running turn `Failed`;
- do not create a replacement one-shot `Task`;
- require a new explicit `@cody` after the issue is fixed to create a new
  session or turn.

### Slack Reporting Failure

If Slack terminal reporting fails:

- leave the turn terminal phase intact;
- set a condition `SlackReportFailed`;
- do not update `AgentSession.status.lastAgentMessageTS`;
- the next explicit follow-up should fail with `MissingLastAgentMessageAnchor`
  rather than use a guessed anchor.

## Observability

Logs and OTEL attributes:

- `agent_session`
- `agent_turn`
- `codex_thread_id`
- `codex_turn_id`
- `taskspawner`
- `slack_channel_id`
- `slack_root_ts`
- `slack_message_ts`
- `turn_sequence`
- `runtime_continuity=codex-app-server`

Metrics:

- `kelos_agent_sessions_total{phase,taskspawner}`
- `kelos_agent_sessions_active{taskspawner}`
- `kelos_agent_turns_total{phase,taskspawner}`
- `kelos_agent_turn_queue_depth{session,taskspawner}`
- `kelos_agent_turn_duration_seconds{taskspawner}`
- `kelos_agent_turn_context_bytes{taskspawner}`
- `kelos_codex_appserver_events_total{event}`
- `kelos_slack_turn_report_failures_total{reason}`

The future OTEL implementation should create one trace per `AgentTurn` and use
the `AgentSession` as a linking attribute across turns.

## Testing Plan

### Unit Tests

Slack:

- session disabled creates current one-shot `Task`;
- session enabled root mention creates one `AgentSession` and first
  `AgentTurn`;
- session enabled thread reply without mention creates nothing;
- explicit thread reply creates a new `AgentTurn` on existing session;
- side conversation delta excludes messages before the last agent timestamp;
- Cody-authored progress/final messages are excluded from transcript;
- route mismatch is rejected;
- ambiguity is rejected;
- transcript too large fails before Cody execution.

Controller:

- pending session creates one runner Job;
- session Job uses task template snapshot image, credentials, env, AgentConfig,
  MCP servers, service account, volumes, and workspace;
- session idle timeout closes only when no queued/running turns exist;
- failed runner Job marks session error.

Runner:

- starts App Server and sends `thread/start` for new session;
- sends `thread/resume` only when status has `codexThreadID` and local Codex
  thread files are present;
- sends `turn/start` with rendered turn prompt;
- accumulates agent-message deltas into final result text;
- maps terminal success/failure to `AgentTurn.status`;
- processes queued turns FIFO;
- does not call Slack APIs.

Reporter:

- queued turn reports queued acknowledgement only when applicable;
- running turn posts progress;
- succeeded turn posts final answer and writes terminal Slack timestamp;
- failed turn posts clear failure;
- Slack report failure sets condition and does not move session anchor.

### Integration Tests

Use envtest for CRDs/controllers and fake Slack/App Server clients where
possible.

Required integration coverage:

- Slack root mention through controller creates session runner Job.
- Two explicit mentions in one thread become two ordered turns.
- Unmentioned side conversation appears in second turn transcript only.
- App Server fake event stream completes a turn and Slack reporter records the
  anchor timestamp.
- App Server child process restart resumes stored `codexThreadID` only when the
  same runner pod still has the Codex thread files.

### Manual Cluster Test

On the dedicated `!session` route only:

1. Post `@cody !session summarize this thread test`.
2. Confirm one `AgentSession`, one `AgentTurn`, one runner Job.
3. Reply without mentioning Cody.
4. Confirm no new turn.
5. Reply `@cody use the above context and continue`.
6. Confirm second turn uses same `AgentSession` and same `codexThreadID`.
7. Confirm Slack response lands in the same thread.
8. Confirm `AgentSession.status.lastAgentMessageTS` updates after each terminal
   response.

Useful commands:

```bash
kubectl -n kelos-system get agentsessions
kubectl -n kelos-system get agentturns
kubectl -n kelos-system describe agentsession <name>
kubectl -n kelos-system logs job/<runner-job-name>
```

## Rollout Plan

1. Merge Kelos implementation with session mode default off.
2. Build and push new Kelos controller, Slack server, and Codex images.
3. Apply new CRDs before enabling session config.
4. Update GitOps to add a dedicated `!session` Cody Slack route with
   `session.enabled: true`.
5. Validate `!session` with manual Slack tests.
6. Enable session mode on `!alpha` after `!session` is stable.
7. Watch session/turn logs and Slack reporting for at least one working day.
8. Enable on normal Cody after alpha validation.

## Open Validation Items

- Confirm the exact App Server sandbox and approval field names in
  `@openai/codex@0.132.0`.
- Confirm `codex app-server generate-json-schema` is available and stable in
  the image version.
- Confirm whether `thread/start` should receive `cwd` or whether `cwd` should
  be supplied only on `turn/start`.
- Confirm App Server terminal event names for success/failure in 0.132.0.
- Confirm where App Server persists thread state under `CODEX_HOME` for
  `@openai/codex@0.132.0`.
- Confirm whether a session-scoped volume for `CODEX_HOME` is worth adding in
  the first implementation. Without that, a new runner pod must mark the
  session failed instead of pretending continuity exists from
  `status.codexThreadID` alone.
