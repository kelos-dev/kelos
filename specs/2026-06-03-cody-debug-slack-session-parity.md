# Cody Debug Slack Session Parity Implementation Spec

Status: draft implementation spec
Date: 2026-06-03
Owner: Cody / Kelos

## Summary

Convert the regular Slack debug route, `cody-debug-slack`, from one-shot
`Task` execution to an App Server-backed `AgentSession`, while preserving the
route's current behavior as closely as possible.

The desired user-visible change is narrow:

```text
Today:
Slack @cody message -> TaskSpawner -> one-shot Task -> Slack thread reply

After:
Slack @cody message -> TaskSpawner -> AgentSession + AgentTurn -> same Slack thread reply
Follow-up @cody messages in that thread -> additional AgentTurns in the same Codex thread
```

This spec is only for the normal debug route. It does not migrate persona
routes such as `!ticket`, `!dev`, or `!review`, and it does not migrate the
experimental `!alpha` / `!exp` route. If those prefixes are used as follow-ups
inside an already-active debug session thread, they can be consumed by that
debug session.

## Goals

- Preserve the current `cody-debug-slack` matching rules.
- Preserve the current `cody-debug-slack` prompt template for the first turn.
- Preserve the current AgentConfig, MCP, credentials, image, service account,
  environment, and Slack reporting behavior.
- Use the existing `TaskSpawner.when.slack.session` API, `AgentSession`,
  `AgentTurn`, and `kelos-session-runner`.
- Ensure normal follow-up mentions in the same Slack thread continue the same
  Codex App Server thread instead of spawning new one-shot tasks.
- Avoid broad runtime or CRD redesigns.

## Non-Goals

- Do not migrate `!ticket`, `!dev`, `!review`, `!alpha`, or `!exp`.
- Do not replace `TaskSpawner` with `TaskSessionSpawner`.
- Do not introduce `SessionArtifact`, incident state, or proactive heartbeat
  behavior.
- Do not support cross-session handoff.
- Do not let Cody post directly to Slack.
- Do not change the Slack formatting/reporting implementation except where
  required for `AgentTurn` parity.
- Do not change Cody's prompt content except for the minimum session envelope
  needed to run through App Server.

## Current Behavior

### GitOps Route

`k8s-platform-gitops/non-prod/kelos/taskspawner-cody-debug.yaml` currently
defines the stable Slack route:

- `metadata.name: cody-debug-slack`
- `when.slack.allowedBotIDs: [B0B4YHV4DJB]`
- `when.slack.triggers: [{ pattern: ".+" }]`
- `when.slack.excludePatterns` excludes:
  - `^!(alpha|exp)\b`
  - `^!(ticket|dev|review)\b`
- `taskTemplate.type: codex`
- `taskTemplate.image: docker.io/alpheya/codex:main`
- `taskTemplate.agentConfigRefs`:
  - `cody-debugger`
  - `cody-atlassian-mcp`
- `taskTemplate.promptTemplate` renders the Slack body and Slack thread URL.
- `podOverrides` select the `cody-debugger` service account and inject GitHub
  App, Kubernetes, and Alpheya JWT environment.

### One-Shot Slack Flow

In Kelos today:

1. `internal/slack/handler.go` receives the Slack message.
2. `MatchesSpawner` applies channel, mention, trigger, bot, and exclude filters.
3. `createTask` calls `ExtractSlackWorkItem(msg)`.
4. `taskbuilder.BuildTask` renders `taskTemplate.promptTemplate`.
5. The controller runs a one-shot `Task`.
6. Slack reporting replies in the originating Slack thread.

This means the one-shot task receives the exact route prompt, including:

```text
Someone pinged you in Slack. Read your AGENTS.md first...

Slack message:
  {{.Body}}

Slack thread: {{.URL}}

Reply once in the same thread with your answer.
```

## Existing Session Runtime

Kelos already has the required session substrate:

- `TaskSpawner.when.slack.session`
- `AgentSession`
- `AgentTurn`
- `AgentSessionReconciler`
- `kelos-session-runner`
- Slack `AgentTurn` reporting

The current session path is close, but not yet parity-compatible for this
route:

- `createSessionAndFirstTurn` snapshots the `TaskTemplate`, then creates the
  first `AgentTurn`.
- `createTurnForSession` currently stores:
  - `Input.Text = msg.Text`
  - `Input.Body = semanticBody(msg.Text)`
- `kelos-session-runner.renderTurnPrompt` wraps every turn in a generic
  "continuing an existing Cody Slack session" prompt.

The missing parity piece is first-turn prompt rendering. If we only enable
`when.slack.session`, Cody no longer receives the exact `cody-debug-slack`
prompt template on the first message.

## Target Behavior

### First Slack Message In A Thread

For a normal `@cody ...` message matching `cody-debug-slack`:

1. Kelos creates or finds one `AgentSession` for the Slack root thread.
2. Kelos creates sequence `1` `AgentTurn`.
3. The first turn receives the rendered `taskTemplate.promptTemplate`, using
   the same `ExtractSlackWorkItem(msg)` variables used by one-shot Tasks.
4. The session runner starts or resumes one Codex App Server thread.
5. Cody replies through Kelos Slack reporting in the same Slack thread.

There should be no one-shot `Task` for this message.

### Follow-Up Slack Mentions

For a later explicit `@cody ...` reply in the same Slack thread:

1. Kelos finds the active `AgentSession` for the Slack thread.
2. Kelos creates the next `AgentTurn` in FIFO order.
3. The turn includes:
   - the semantic follow-up request body;
   - the bounded Slack transcript since Cody's last terminal answer;
   - the current session metadata;
   - the same Codex App Server thread ID.
4. Cody replies through Kelos Slack reporting in the same Slack thread.

Follow-ups must continue to require an explicit Cody mention.

### Follow-Up Routing

Once a regular debug session is active for a Slack thread, any later explicit
`@cody ...` reply in that thread should be treated as a follow-up turn for that
session.

This intentionally includes messages whose body starts with a reserved prefix
such as `!dev`, `!ticket`, `!review`, `!alpha`, or `!exp`. Top-level messages
outside an active debug session still use normal TaskSpawner matching and
exclusions, but inside the active session the session wins.

## Implementation Plan

### 1. Reuse Session Mode On `TaskSpawner`

No new API is needed. Use the existing block:

```yaml
when:
  slack:
    session:
      enabled: true
      requireMentionForTurns: true
      contextWindow: SinceLastAgentMessage
      idleTimeout: 1h
      maxQueuedTurns: 5
```

`requireMentionForTurns: true` is important because the current regular route
only acts on explicit Cody mentions.

### 2. Render The Route Prompt For The First Turn

Add first-turn prompt rendering to the Slack session creation path.

Required behavior:

- Use the same template variables as one-shot Tasks:
  - `ID`
  - `Title`
  - `Body`
  - `URL`
  - `Kind`
- Use the same `taskTemplate.promptTemplate`.
- Use the same default prompt behavior if the template is empty.
- Treat render failures like one-shot task build failures:
  - do not queue an unusable turn;
  - post a Slack notice in the thread;
  - log the failed route and template context.

Preferred low-rework implementation:

- Extract the prompt rendering helper from `internal/taskbuilder` into a
  package-level helper that both `BuildTask` and Slack session creation can
  call, or add a narrow exported `RenderPromptTemplate` function.
- In `createSessionAndFirstTurn`, render the first-turn prompt before creating
  the `AgentTurn`.
- Pass that rendered prompt into `createTurnForSession`.
- For sequence `1`, set `AgentTurn.Spec.Input.Body` to the rendered route
  prompt.
- For follow-up sequences, keep `Input.Body = semanticBody(msg.Text)`.

This avoids a CRD change. `AgentTurnInput.Body` already means the semantic
request body passed to the runner. For the first turn of a TaskSpawner-backed
session, the semantic request is the rendered route prompt.

### 3. Make The Runner Respect First-Turn Route Prompts

Update `renderTurnPrompt` so sequence `1` does not bury the rendered route
prompt under a generic "current explicit request" wrapper that changes Cody's
behavior too much.

Recommended behavior:

- Always include a small session envelope so Cody knows this is backed by a
  Slack session and must report through Kelos.
- For sequence `1`, present the rendered TaskSpawner prompt as the main request.
- For later turns, keep the current session prompt shape with transcript delta
  and current explicit request.

Example shape for sequence `1`:

```text
You are running Cody through a Kelos Slack AgentSession.

Session:
- Kelos session: <namespace>/<name>
- Slack thread: <url>
- TaskSpawner route: <namespace>/<taskspawner>
- Turn sequence: 1

Route prompt:
<rendered cody-debug-slack promptTemplate>

Reply once in the same Slack thread through the Kelos reporter.
```

This preserves the route-specific instructions while keeping the App Server
runtime constraints visible.

### 4. Preserve Existing Follow-Up Session Prompt

For sequence `> 1`, keep the current `renderTurnPrompt` structure:

- session metadata;
- previous Cody answer timestamp;
- transcript since the last Cody terminal answer;
- current explicit request;
- Slack reporter instruction;
- TTY guidance.

The only intended change for follow-ups is that they use the same Codex thread
instead of a new one-shot task.

### 5. Keep Existing Active-Session Routing

Keep the current active-session follow-up behavior:

- Find the active session by Slack channel and thread timestamp.
- If exactly one active session exists, queue the message as the next
  `AgentTurn`.
- Do not re-apply TaskSpawner trigger or exclude patterns for follow-ups.

This is simpler and matches the intended session mental model: once a Slack
thread has an active Cody debug session, explicit Cody mentions continue that
session.

### 6. GitOps Change

In `k8s-platform-gitops/non-prod/kelos/taskspawner-cody-debug.yaml`, enable
session mode under the existing Slack config and leave everything else
unchanged:

```yaml
spec:
  when:
    slack:
      allowedBotIDs:
        - B0B4YHV4DJB
      triggers:
        - pattern: '.+'
      excludePatterns:
        - '^!(alpha|exp)\b'
        - '^!(ticket|dev|review)\b'
      session:
        enabled: true
        requireMentionForTurns: true
        contextWindow: SinceLastAgentMessage
        idleTimeout: 1h
        maxQueuedTurns: 5
```

Do not change:

- `taskTemplate.promptTemplate`
- `agentConfigRefs`
- `image`
- `credentials`
- `podOverrides`
- `ttlSecondsAfterFinished`

`ttlSecondsAfterFinished` remains harmless for compatibility even though the
session runner does not use Task TTL semantics.

## Tests

### Kelos Unit Tests

Add or update tests around Slack session routing:

- Normal `@cody ...` message matching `cody-debug-slack` with
  `session.enabled: true` creates:
  - one `AgentSession`;
  - one `AgentTurn`;
  - no `Task`.
- First `AgentTurn.Spec.Input.Body` contains the rendered
  `taskTemplate.promptTemplate`, including the Slack body and Slack URL.
- Follow-up `@cody ...` in the same thread creates a second `AgentTurn` on the
  same `AgentSession`.
- Follow-up turn body is the semantic Slack body, not the original first-turn
  prompt.
- Duplicate Slack delivery for the same message does not create a duplicate
  `AgentTurn`.
- If the prompt template render fails, Kelos does not create a broken first
  turn.
- A follow-up message with a reserved prefix such as `!dev` is consumed by the
  active regular debug session.

### Session Runner Tests

Add or update tests for `renderTurnPrompt`:

- Sequence `1` includes the rendered route prompt prominently.
- Sequence `1` includes session metadata and Slack reporter instruction.
- Sequence `> 1` keeps transcript delta and current explicit request behavior.
- TTY guidance remains present.

### GitOps Validation

Validate the kustomize output for the Kelos non-prod overlay after enabling
session mode.

## Manual Verification

After merging and deploying:

1. Send a normal message in the Cody Slack channel:

   ```text
   @cody what is the current health of qa/compliance-service?
   ```

2. Verify Kubernetes resources:

   ```bash
   kubectl -n kelos-system get agentsessions,agentturns
   kubectl -n kelos-system get tasks
   ```

   Expected:

   - one new `AgentSession`;
   - one new `AgentTurn`;
   - no new one-shot `Task` for the Slack message.

3. Verify the session runner:

   ```bash
   kubectl -n kelos-system get jobs,pods -l kelos.dev/component=agent-session
   kubectl -n kelos-system logs job/<runner-job-name>
   ```

4. Send a follow-up in the same Slack thread:

   ```text
   @cody check the latest pod events too
   ```

   Expected:

   - a second `AgentTurn`;
   - same `AgentSession`;
   - same `status.codexThreadID`;
   - Slack reply in the same thread.

5. Send a reserved command prefix in the same thread:

   ```text
   @cody !dev make a small PR for this
   ```

   Expected:

   - the regular debug session consumes the message;
   - a new `AgentTurn` is created on the existing `AgentSession`;
   - no separate persona Task is created for that thread reply.

6. Wait past the idle timeout and verify the session closes. A new normal
   mention in the same thread should create a fresh session if the old one is
   closed.

## Build And Deploy Notes

Expected repository changes:

- Kelos:
  - prompt rendering helper reuse;
  - Slack session first-turn prompt rendering;
  - session runner prompt rendering tests.
- k8s-platform-gitops:
  - enable `when.slack.session` on `cody-debug-slack`.
- skills:
  - no change.

Expected image impact:

- Rebuild and push `docker.io/alpheya/kelos-controller:main` if controller or
  shared Slack code changes.
- Rebuild and push `docker.io/alpheya/kelos-slack-server:main` if Slack handler
  code is in that binary.
- Rebuild and push `docker.io/alpheya/codex:main` if
  `cmd/kelos-session-runner` changes, because the Codex image carries
  `/kelos-session-runner`.

Expected chart impact:

- No Helm chart package is required if there are no CRD, RBAC, or chart
  template changes.
- If a CRD field is added after all, regenerate CRDs and publish the chart
  before applying GitOps changes. The preferred implementation avoids this.

Merge sequence:

1. Merge Kelos runtime changes.
2. Build and push the affected Kelos/Codex images.
3. Merge the k8s-platform-gitops change enabling session mode.
4. Let Flux reconcile.
5. Run the manual Slack verification above.

## Risks And Mitigations

- Resource footprint increases because a session runner stays alive until idle
  timeout. Mitigation: start with `idleTimeout: 1h` and `maxQueuedTurns: 5`.
- The first prompt could drift from the current one-shot prompt if it is wrapped
  too heavily. Mitigation: render the exact TaskSpawner prompt and make it the
  primary sequence-1 request.
- Existing `!` routes are intentionally swallowed by an active debug session.
  Mitigation: document that command prefixes only select routes for top-level
  messages or thread replies without an active session.
- Session failures are surfaced through `AgentSession` / `AgentTurn` status
  instead of `Task` status. Mitigation: add clear Slack notices and update
  operational docs/verification commands.
- Existing old one-shot Tasks and new AgentTurns will coexist during rollout.
  Mitigation: enable session mode only after the runtime image with prompt
  parity is deployed.

## Definition Of Done

- `cody-debug-slack` regular mentions create `AgentSession` / `AgentTurn`, not
  one-shot `Task`s.
- The first turn receives the same rendered route prompt content as the current
  one-shot Task.
- Follow-up mentions in the same Slack thread create additional turns on the
  same Codex thread.
- `agentConfigRefs`, MCP setup, credentials, service account, image, and pod
  environment are unchanged.
- Reserved command prefixes continue the active debug session when used inside
  that session's Slack thread.
- Unit tests cover first-turn prompt parity, follow-up continuation, duplicate
  delivery, render failure, and reserved-prefix follow-up handling.
