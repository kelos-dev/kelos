# Cody Slack Thread Sessions

## Status

Draft product and implementation spec.

This is independent from proactive Slack broadcast/outbox work. The two designs
can share Slack delivery primitives later, but this feature is about inbound
conversation shape and Cody runtime continuity.

## Summary

Today each Slack message that matches a Cody `TaskSpawner` creates one Kelos
`Task`. For thread replies, Kelos fetches the full Slack thread and passes that
thread text into the new task. This gives Cody context, but not continuity:
each follow-up is a separate Kubernetes Job and a separate Cody execution.

Add a thread-scoped session mode for Slack:

- one Slack root thread maps to one Cody `AgentSession`;
- every Cody turn still requires an explicit `@cody` mention;
- unmentioned human side conversation does not wake Cody;
- when a user explicitly mentions Cody again, Kelos passes only the conversation
  segment since Cody last answered, plus session metadata and prior state;
- turns run sequentially inside the same logical Cody session.

The important product behavior is that humans can talk normally in the thread
without triggering Cody, then explicitly pass the next relevant message to Cody
with `@cody`.

## Current Behavior

Current Slack handling is one-shot:

1. `kelos-slack-server` receives a Slack message or app mention.
2. If the message is a thread reply, it fetches the full Slack thread and places
   the formatted thread into the work item body.
3. The message is matched against Slack `TaskSpawner` filters.
4. A new `Task` is created for the matching message timestamp.
5. The controller creates one Kubernetes Job.
6. The Cody image runs `codex exec --json "$PROMPT"` once and exits.
7. Slack reporting posts or updates one answer in the same thread.

This means follow-ups are fresh Cody processes with a full-thread transcript.
The thread provides text continuity, but the runtime has no durable session,
turn queue, or precise context boundary.

## Goals

- Keep explicit `@cody` required for every user-to-Cody turn.
- Allow human side conversation inside the same Slack thread without waking
  Cody.
- Preserve one logical Cody session per Slack root thread.
- Queue explicit follow-up turns when Cody is already running.
- Build a refined context window from the last Cody terminal response to the
  current explicit mention, instead of passing the entire thread every time.
- Preserve existing persona routing for the first turn: normal Cody, `!alpha`,
  `!dev`, `!review`, `!ticket`, etc.
- Keep Slack responses confined to the originating thread.
- Make session lifecycle, queued turns, and failures observable.

## Non-Goals

- No follow-up turns without an explicit `@cody` mention.
- No scheduled or autonomous Slack turns.
- No cross-thread session.
- No multi-agent concurrency inside one Slack thread.
- No route switching inside an active session in the first cut.
- No hidden Slack metadata embedded in messages as the source of truth.
- No broad Slack transcript retention outside Kubernetes session and turn
  status needed for operation.

## Product Behavior

### Starting a Session

A session starts when a Slack message matches an existing Slack `TaskSpawner`.
The first message uses today's routing rules:

```text
@cody can you debug why apps is stuck?
@cody !alpha create a test ticket
@cody !dev fix ALPM-123
```

Kelos creates:

- one `AgentSession` for the Slack root thread;
- one first `AgentTurn` for the triggering Slack message;
- one session runner for that session.

The session key is deterministic:

```text
slack_team_id + slack_channel_id + slack_root_ts + taskspawner_namespace + taskspawner_name
```

Using the `TaskSpawner` identity in the key prevents accidental collisions when
separate Cody routes intentionally coexist in the same Slack thread.

### Follow-Up Turns

Follow-up turns require an explicit Cody mention:

```text
<human side discussion, no Cody trigger>

@cody okay based on the above, create the PR
```

Rules:

- A thread reply without `@cody` never creates a turn.
- A thread reply with `@cody` routes to the existing active session for that
  thread.
- The follow-up does not need to repeat the original route prefix.
- If the follow-up includes a route prefix, it must match the session's
  originating route. Otherwise Kelos rejects the turn with a clear Slack reply.
- If Cody is already running, Kelos enqueues the turn and posts a short queued
  acknowledgement.
- Turns execute FIFO.

### Side Conversation Context

Unmentioned side conversation is not ignored. It is included as context when a
later explicit mention happens.

Example:

```text
Cody: The failure is likely the Aikido proxy auth path.

Human A: I think this was after the token rotation.
Human B: Also cody-tools was restarted at 09:14.

Human A: @cody check that angle and tell us the likely fix
```

The next Cody turn receives:

- previous Cody terminal response metadata;
- all non-Cody messages after that Cody response and up to the explicit mention;
- the explicit mention message itself;
- session metadata such as channel, thread URL, previous turn IDs, and current
  route.

It does not receive the entire historical thread unless this is the first turn
or the session has no recorded Cody response anchor.

## Proposed API Shape

### TaskSpawner Slack Session Mode

Add an optional session block under `spec.when.slack`.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-debug-slack
spec:
  when:
    slack:
      triggers:
        - pattern: .+
      session:
        enabled: true
        requireMentionForTurns: true
        contextWindow: SinceLastAgentMessage
        idleTimeout: 1h
        maxQueuedTurns: 5
```

Defaults:

- `enabled: false`
- `requireMentionForTurns: true`
- `contextWindow: SinceLastAgentMessage`
- `idleTimeout: 1h`
- `maxQueuedTurns: 5`

Validation:

- `requireMentionForTurns` must be `true` in the first cut.
- `contextWindow` only supports `SinceLastAgentMessage` in the first cut.
- `idleTimeout` must be positive.
- `maxQueuedTurns` must be greater than zero when set.

Existing `TaskSpawner` behavior is unchanged when `session.enabled` is false.

## New Resources

### AgentSession

Add a namespaced `AgentSession` CRD.

Purpose: durable session state for one Cody Slack thread.

Sketch:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: AgentSession
metadata:
  name: cody-debug-slack-c123-1779412279
  namespace: kelos-system
  labels:
    kelos.dev/taskspawner: cody-debug-slack
    kelos.dev/source: slack
spec:
  source:
    type: SlackThread
    teamID: T123
    channelID: C099UM9LVD1
    rootTS: "1779412279.782289"
    threadURL: https://...
  taskSpawnerRef:
    name: cody-debug-slack
  taskTemplateSnapshot:
    type: codex
    image: docker.io/alpheya/codex:main
    credentials:
      type: oauth
      secretRef:
        name: cody-codex-credentials
  route:
    initialText: "@cody ..."
    triggerName: default
  idleTimeout: 1h
  maxQueuedTurns: 5
status:
  phase: Running
  currentTurn: cody-debug-slack-c123-1779412279-turn-0003
  runnerPodName: cody-session-...
  lastAgentMessageTS: "1779415000.111222"
  lastCompletedTurn: cody-debug-slack-c123-1779412279-turn-0002
  queuedTurns: 1
  createdAt: "2026-05-23T08:31:00Z"
  lastActivityAt: "2026-05-23T08:45:00Z"
  closeReason: ""
```

Phases:

- `Pending`
- `Starting`
- `Idle`
- `Running`
- `Closing`
- `Closed`
- `Error`

### AgentTurn

Add a namespaced `AgentTurn` CRD.

Purpose: one explicit user-to-Cody turn inside a session.

Sketch:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: AgentTurn
metadata:
  name: cody-debug-slack-c123-1779412279-turn-0003
  namespace: kelos-system
spec:
  sessionRef:
    name: cody-debug-slack-c123-1779412279
  source:
    type: SlackMessage
    teamID: T123
    channelID: C099UM9LVD1
    rootTS: "1779412279.782289"
    messageTS: "1779416200.333444"
    userID: U123
    permalink: https://...
  input:
    text: "@cody check the token rotation angle"
    body: "check the token rotation angle"
  context:
    mode: SinceLastAgentMessage
    fromTSExclusive: "1779415000.111222"
    toTSInclusive: "1779416200.333444"
status:
  phase: Queued
  sequence: 3
  startedAt: null
  completedAt: null
  agentMessageTS: ""
  message: ""
```

Phases:

- `Queued`
- `Running`
- `Succeeded`
- `Failed`
- `Canceled`

## Slack Routing

### Root Messages

For root messages, use existing Slack `TaskSpawner` matching. If the matching
spawner has `session.enabled: true`, create or reuse an `AgentSession` and add
the first `AgentTurn`. If not, create a one-shot `Task` as today.

### Thread Replies

For thread replies:

1. Ignore messages from Cody itself.
2. If the reply does not mention Cody, do not create a turn.
3. If the reply mentions Cody and an active matching `AgentSession` exists,
   create an `AgentTurn` for that session.
4. If the reply mentions Cody and no session exists, apply root-message
   `TaskSpawner` matching to decide whether this message should start a new
   session for the existing thread.
5. If multiple sessions match the same thread, require the route prefix to
   disambiguate. If ambiguous, post a deterministic error and do not run Cody.

### Route Prefix Handling

The first turn owns the session route.

Follow-up behavior:

- `@cody please continue` routes to the existing session.
- `@cody !alpha please continue` is accepted only if the existing session was
  created by the alpha spawner.
- `@cody !dev please continue` inside a normal debug session is rejected as a
  route mismatch.

Switching personas remains explicit. A user should start the target persona
route with its normal `@cody !...` command rather than relying on automatic
cross-persona routing.

## Context Window Construction

### Anchor Selection

For each turn, define the context anchor as:

1. `AgentSession.status.lastAgentMessageTS` when present.
2. Otherwise the Slack root timestamp, exclusive, for thread replies.
3. Otherwise empty for the first root turn.

The anchor must be written by Kelos when it successfully posts Cody's terminal
response, not inferred from Slack text. Slack text is not a reliable state
store.

### Transcript Range

For turn `N`, fetch Slack replies for the session thread and include messages:

```text
anchor_ts < message_ts <= triggering_message_ts
```

For first root turn, include the root message as the user input and omit prior
thread transcript unless the session is being created from an existing thread
reply.

### Inclusion Rules

Include:

- human messages in the range;
- non-Cody bot messages in the range as context, not as trusted commands;
- text from Slack attachments when available;
- timestamps, display names, and Slack user IDs for traceability;
- the current explicit `@cody` message.

Exclude:

- Cody progress messages such as "Working on your request";
- Cody activity updates;
- prior Cody terminal responses before the anchor;
- messages after the triggering mention, even if they arrive before execution
  starts.

### Prompt Shape

The session runner receives a structured prompt per turn:

```text
You are continuing an existing Cody Slack session.

Session:
- Slack thread: <url>
- Route: cody-debug-slack
- Turn: 3
- Previous Cody answer timestamp: 1779415000.111222

Conversation since your last answer:
Human A [U123 at 1779416100.000001]:
I think this was after the token rotation.

Human B [U456 at 1779416150.000002]:
Also cody-tools was restarted at 09:14.

Current explicit request:
Human A [U123 at 1779416200.333444]:
@cody check that angle and tell us the likely fix
```

This format gives Cody the side conversation needed for the next action without
replaying the entire old thread.

## Session Runner

The current `/kelos_entrypoint.sh` contract is one-shot. Thread sessions need a
new runner mode.

Add a session runner for Cody that:

1. starts once per `AgentSession`;
2. prepares the workspace, AgentConfig, MCP servers, tools, and credentials;
3. watches `AgentTurn` resources for the session;
4. executes one turn at a time;
5. posts progress and final output through Kelos Slack reporting;
6. stays alive until idle timeout, explicit close, fatal error, or controller
   shutdown.

Implementation detail can vary:

- preferred: one session runner pod per active `AgentSession`;
- acceptable initial shortcut: one controller-managed worker that invokes Cody
  per turn with persistent session storage and workspace volume.

The product contract is the same either way: one Slack thread has one durable
Kelos session and FIFO turns.

## Cody Runtime Continuity

The target is stronger than today's full-thread replay, but weaker than
assuming an interactive TTY with Codex if the CLI cannot support it safely.

Required:

- preserve workspace files across turns;
- preserve Cody/Codex local state that is safe to persist, including relevant
  home directory state when supported;
- pass only the refined per-turn context window;
- keep the same AgentConfig and MCP configuration for all turns in the session.

Preferred:

- use a real Codex resume/session mechanism if the installed Codex CLI exposes
  one that is stable in non-interactive mode.

If Codex cannot accept true incremental turns yet, Kelos should still expose the
same `AgentSession`/`AgentTurn` API and run each turn by replaying compact
session state plus the delta transcript. That is still a product improvement,
but the implementation should log it as `runtime_continuity=logical`, not claim
true process continuity.

## Slack Output

For each turn:

1. Post or update an in-thread "Working on your request" message.
2. If queued behind another turn, post a short queued acknowledgement.
3. Stream progress/activity using the existing Slack reporter patterns where
   possible.
4. Post the terminal Cody response in the same Slack thread.
5. Record the terminal response timestamp on both `AgentTurn.status` and
   `AgentSession.status.lastAgentMessageTS`.

The timestamp write is mandatory. Future context windows depend on it.

## Commands

Only explicit mentions are commands.

Initial command set:

- `@cody stop` or `@cody close`: close the session after any active turn
  finishes, unless later implementation supports cancellation.
- `@cody cancel`: cancel queued turns from the same user if they have not
  started.
- normal `@cody <request>`: enqueue a new turn.

No command is recognized unless the message mentions Cody.

## Failure Handling

### Slack Thread Fetch Failure

If Kelos cannot fetch the thread context for a follow-up, fail the turn before
running Cody and post a clear error in-thread. Do not silently run with only the
latest message, because that breaks the session contract.

### Context Window Too Large

If the delta transcript is too large:

1. summarize older messages inside the delta using a deterministic system
   summarizer or Cody itself in a bounded preprocessing step;
2. preserve the current explicit request verbatim;
3. include a note in the prompt that the intermediate context was compressed.

The first cut may instead fail visibly if summarization is not implemented.

### Runner Crash

If the runner crashes:

- mark the current turn `Failed`;
- mark the session `Error`;
- post a thread error with the session and turn names;
- do not create a new one-shot `Task` as a hidden replacement execution path.

### Ambiguous Session

If a Slack thread has multiple active sessions and the mention does not
disambiguate, Kelos posts an error and does not run Cody.

### Queue Overflow

If `maxQueuedTurns` is exceeded, reject the new turn with a thread reply.

## Security

- Cody still receives only the credentials configured by the originating
  `TaskSpawner`/`AgentConfig`.
- Follow-up turns cannot switch to another route or permission surface.
- Bot-authored messages cannot start or continue sessions.
- Unmentioned side conversation is context only, never an executable command.
- Slack token stays in `kelos-slack-server` or the Slack delivery service, not
  inside Cody.

## Observability

Every log and metric should carry:

- `agent_session`
- `agent_turn`
- `taskspawner`
- `slack_team_id`
- `slack_channel_id`
- `slack_root_ts`
- `slack_message_ts`
- `runtime_continuity` (`process`, `codex-resume`, or `logical`)

Useful metrics:

- sessions created, active, closed, errored;
- turn queue depth;
- turn latency;
- thread fetch failures;
- context window size;
- queued turn rejects;
- route mismatch rejects;
- session idle closures.

This aligns naturally with the OTEL work: session and turn IDs become trace
attributes for Slack input, Cody runtime, MCP calls, tool calls, and Slack
output.

## Rollout

1. Add CRDs and controllers with `session.enabled` defaulting to false.
2. Add Slack routing support behind `TaskSpawner.spec.when.slack.session`.
3. Enable first on a dedicated `!session` Slack route.
4. Validate explicit follow-ups, side-conversation context, queueing, and idle
   closure.
5. Enable on `!alpha` after `!session` is stable.
6. Enable on normal Cody after alpha behavior is stable.

## Tests

Unit tests:

- root mention creates `AgentSession` and first `AgentTurn` when session mode is
  enabled;
- root mention still creates one-shot `Task` when session mode is disabled;
- thread reply without `@cody` creates no turn;
- thread reply with `@cody` creates a turn on the existing session;
- side conversation since last Cody answer is included in context;
- messages before last Cody answer are excluded;
- current explicit request is included verbatim;
- route mismatch is rejected;
- ambiguous active sessions are rejected;
- queue overflow is rejected;
- Slack thread fetch failure fails the turn before Cody runs.

Integration tests:

- two explicit mentions in one thread run as ordered turns in one session;
- unmentioned side conversation between turns does not wake Cody but appears in
  the next turn context;
- terminal Cody message timestamp updates `AgentSession.status.lastAgentMessageTS`;
- idle timeout closes the session after no active or queued turns;
- session errors do not create hidden replacement tasks.

## Acceptance Criteria

- A first `@cody` message creates one session and one turn.
- A later unmentioned side conversation creates no work.
- A later `@cody` message in the same thread creates a second turn in the same
  session.
- The second turn prompt includes only messages since Cody's last terminal
  answer, plus the current explicit mention.
- Cody replies in the same Slack thread.
- Concurrent explicit follow-ups are queued and processed FIFO.
- A failed thread fetch prevents Cody from running with incomplete context.
- Existing one-shot Slack TaskSpawner behavior remains unchanged unless
  `session.enabled` is set.

## Open Questions

- Which Codex CLI version and command should be used for true session resume, if
  any?
- Should the first cut implement cancellation for a running turn, or only queued
  turn cancellation?
- Should session IDs include the Slack team ID from Events API once available in
  all relevant event paths?
- Should explicit follow-ups be accepted from any thread participant, or should
  this be configurable per `TaskSpawner` later?
