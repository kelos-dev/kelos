# Cody Personas Phase 2 Slack-Mediated Handoff Implementation Spec

## Status

Implementation-ready shortcut spec.

This is an alternative to
`specs/2026-05-22-14-30-cody-personas-phase-2-handoffs-implementation.md`.
Keep that Kelos-native handoff spec as the longer-term design. Implement this
Slack-mediated approach first to get persona handoffs and PR babysitting without
adding `TaskSpawner.spec.handoffs` or a new handoff reconciler.

Scope:

- Slack-only persona handoffs.
- Explicit `@cody !babysit <PR>` PR babysitting.
- Small Kelos changes in capture, Slack reporting, and Slack routing.
- Cody GitOps changes for internal bot-only handoff TaskSpawners.

Out of scope:

- No router persona.
- No GitHub triggers.
- No `TaskSpawner.spec.handoffs`.
- No Kelos handoff reconciler.
- No channel-level Slack whitelist.
- No new Cody service accounts or RBAC split.

## Summary

Instead of creating child Tasks directly from Kubernetes state, Kelos uses Slack
as the handoff bus.

Flow:

1. A parent Cody persona finishes and emits structured handoff results.
2. The Slack reporter posts a signed internal Slack message in the same thread:

   ```text
   <@CODY_BOT_USER_ID> !handoff review
   handoff-id: ...
   parent-task: ...
   target: review
   signature: v1:...

   ---
   Review this PR...
   ```

3. The Slack server accepts only signed self-bot `!handoff` messages.
4. Bot-only internal TaskSpawners match `!handoff dev`, `!handoff review`, and
   `!handoff dev-fix`.
5. The next persona runs from the Slack handoff message and reports back in the
   same thread.

This keeps the implementation close to existing Slack routing. Handoff state is
visible in Slack and auditable through Task annotations, but Slack delivery now
becomes part of workflow execution.

## Why This First

Compared with Kelos-native handoffs, this shortcut avoids:

- new `TaskSpawner.spec.handoffs` API
- child Task creation from the Task controller
- dependency prompt plumbing for `.Upstream`
- controller-side loop reconciler logic
- CRD validation for handoff predicates and loop state

It still requires small Kelos changes:

- custom result outputs so agents can emit handoff intent
- reporter-side Slack handoff dispatch
- signed self-bot Slack handoff routing
- internal handoff TaskSpawners in GitOps

## Current Behavior To Account For

Kelos already supports bot-authored Slack messages through
`spec.when.slack.allowedBotIDs`, but self-authored Cody bot messages are filtered
before TaskSpawner matching.

Relevant behavior:

- `internal/slack/handler.go` stores Cody's Slack bot user ID and bot ID from
  `auth.test`.
- `shouldProcess(...)` rejects messages from Cody's own bot user or bot ID.
- `internal/slack/filter.go` rejects bot-authored messages unless
  `allowedBotIDs` includes the message `bot_id`.
- `internal/slack/handler.go` annotates bot-origin Tasks with
  `kelos.dev/slack-bot-id`.
- Slack Task names are derived from channel plus Slack message timestamp.
- The Slack reporter already patches Task annotations after reporting phases.

Because the reporter posts with Cody's own Slack bot token today, a reporter
posted `@cody !dev ...` message will not trigger Cody unless we add a narrow
self-bot handoff exception.

## Design

### Handoff Output Contract

Agents emit handoff intent through `kelos-output`.

Required keys:

| Key | Required | Purpose |
| --- | --- | --- |
| `handoff.slack.target` | Yes | One of `dev`, `review`, or `dev-fix`. |
| `handoff.slack.prompt` | Yes | Child prompt body. One line, less than 16 KiB. |
| `handoff.slack.reason` | Recommended | Short audit reason. |

Optional keys:

| Key | Purpose |
| --- | --- |
| `handoff.slack.prompt.base64` | Multiline child prompt when one-line prompt is not enough. |
| `handoff.slack.subject-key` | Subject key, usually `pr.url`. |
| `handoff.slack.subject` | Subject value, usually the PR URL. |
| `handoff.slack.loop-name` | Loop name, usually `pr-babysit`. |
| `handoff.slack.loop-attempt` | Current loop attempt. |
| `handoff.slack.loop-max` | Max fix attempts. |
| `handoff.slack.increment-attempt` | `true` when the next child is a fix attempt. |

Examples:

Ticket to dev:

```bash
kelos-output set handoff.slack.target dev
kelos-output set handoff.slack.reason "Ticket ALPM-123 is ready for implementation."
kelos-output set handoff.slack.prompt "Implement ALPM-123 and open a PR."
kelos-output set ticket ALPM-123
```

Dev to review:

```bash
kelos-output set handoff.slack.target review
kelos-output set handoff.slack.reason "PR is open and ready for review."
kelos-output set handoff.slack.prompt "Review the PR for correctness, regressions, and missing tests."
kelos-output set handoff.slack.subject-key pr.url
kelos-output set handoff.slack.subject "https://github.com/donchev7/kelos/pull/123"
```

Reviewer to dev-fix inside babysitting:

```bash
kelos-output set handoff.slack.target dev-fix
kelos-output set review.result changes-requested
kelos-output set handoff.slack.subject-key pr.url
kelos-output set handoff.slack.subject "https://github.com/donchev7/kelos/pull/123"
kelos-output set handoff.slack.loop-name pr-babysit
kelos-output set handoff.slack.loop-attempt "0"
kelos-output set handoff.slack.loop-max "2"
kelos-output set handoff.slack.increment-attempt "true"
kelos-output set handoff.slack.prompt "Fix the missing controller test assertion."
```

### `kelos-output` And Capture

Implement the same `kelos-output` helper and capture extension described in the
Kelos-native Phase 2 spec.

Minimum requirements:

- append custom `key: value` lines to `/tmp/kelos-extra-outputs`
- reject reserved built-in keys
- validate result key shape
- enforce size limits
- have `kelos-capture` append valid extra outputs into Task outputs/results

This shortcut relies on structured Task results. Do not parse handoff intent
from the human-readable Slack response.

### Slack Handoff Dispatcher

Add a dispatcher to `internal/reporting`.

The dispatcher runs after a terminal `succeeded` Slack report is posted for a
Task. It inspects `task.Status.Results`.

Dispatch preconditions:

- Task has `kelos.dev/slack-reporting=enabled`.
- Task has Slack channel and thread annotations.
- Task phase is `Succeeded`.
- Task has label `cody.alpheya.com/slack-handoff=enabled`.
- `status.results["handoff.slack.target"]` is set.
- `status.results["handoff.slack.prompt"]` or
  `status.results["handoff.slack.prompt.base64"]` is set.

Allowed targets:

| Target | Internal Slack command | Persona |
| --- | --- | --- |
| `dev` | `!handoff dev` | Normal Cody dev. |
| `review` | `!handoff review` | Cody PR reviewer. |
| `dev-fix` | `!handoff dev-fix` | Cody dev in PR babysitter fix mode. |

The dispatcher must reject unknown targets and post a visible blocked message in
the Slack thread.

`handoff.slack.prompt.base64`, when present, must be base64-decoded before the
Slack handoff message is posted. If decoding fails, block the handoff and post
`handoff blocked: invalid prompt encoding`.

### Handoff Message Format

The reporter posts a thread reply as Cody's own Slack bot.

Format:

```text
<@CODY_BOT_USER_ID> !handoff <target>
handoff-id: <stable id>
parent-task: <task namespace>/<task name>
parent-uid: <task uid>
target: <target>
reason: <short reason>
subject-key: <optional subject key>
subject: <optional subject value>
loop-name: <optional loop name>
loop-attempt: <optional attempt>
loop-max: <optional max>
signature: v1:<hmac>

---
<child prompt>
```

Rules:

- Keep the mention first so normal mention stripping works.
- Keep `!handoff <target>` first after the mention.
- Use plain text. Do not rely on Slack Block Kit content for routing.
- Include `handoff-id` in the visible text so duplicate detection can search
  the thread if needed.
- The child prompt begins after the first `---` separator.

The `handoff-id` is deterministic:

```text
sha256(parent task uid + target + subject + loop name + next attempt + prompt hash)
```

### Signature

Add a Slack handoff signing secret to the Slack server process:

```text
SLACK_HANDOFF_DISPATCH_ENABLED=true
SLACK_HANDOFF_SIGNING_SECRET
```

`SLACK_HANDOFF_DISPATCH_ENABLED` must default to `false` so the code can be
released before GitOps is ready. Signed self-bot routing can be enabled only
when this flag is true.

The reporter signs:

```text
v1:<base64url(hmac-sha256(secret, canonical handoff fields))>
```

Canonical fields:

- handoff id
- parent task namespace/name
- parent task UID
- target
- subject key
- subject value
- loop name
- loop attempt
- loop max
- prompt hash

The Slack handler verifies the signature before routing self-bot handoff
messages. Invalid signatures are ignored and logged.

This is intentionally redundant with the self-bot check. It prevents accidental
self-replies that happen to start with `!handoff` from becoming Tasks.

### Self-Bot Handoff Routing

Modify Slack routing narrowly:

1. Continue rejecting normal Cody self messages.
2. Allow Cody self messages through only when all are true:
   - message contains Cody's mention
   - stripped body starts with `!handoff `
   - message has a valid handoff signature
   - message has content after the `---` separator
3. Route the message through normal TaskSpawner matching.

Add `requireBotAuthor` to Slack source config:

```go
type Slack struct {
    Channels []string `json:"channels,omitempty"`
    Triggers []SlackTrigger `json:"triggers,omitempty"`
    ExcludePatterns []string `json:"excludePatterns,omitempty"`
    AllowedBotIDs []string `json:"allowedBotIDs,omitempty"`

    // RequireBotAuthor makes this Slack source reject human-authored messages.
    // +optional
    RequireBotAuthor bool `json:"requireBotAuthor,omitempty"`
}
```

Matching behavior:

- If `requireBotAuthor=true`, reject human-authored messages.
- If `requireBotAuthor=true`, require `msg.BotID` to be present.
- If `allowedBotIDs` is non-empty, require `msg.BotID` to be listed.
- Existing human TaskSpawners keep `requireBotAuthor=false`.

Internal handoff TaskSpawners set:

```yaml
when:
  slack:
    requireBotAuthor: true
    allowedBotIDs:
      - <cody-slack-bot-id>
    triggers:
      - pattern: '^!handoff review\b'
```

This makes `!handoff` bot-only while leaving user-facing `@cody !dev` and
`@cody !review` unchanged.

### Template Variables

Extend Slack template variables for bot-origin messages:

```text
.BotID
.IsBotMessage
.SlackThreadTS
.SlackTimestamp
```

For internal handoff messages, also expose parsed fields:

```text
.Handoff.ID
.Handoff.ParentTask
.Handoff.ParentUID
.Handoff.Target
.Handoff.Reason
.Handoff.SubjectKey
.Handoff.Subject
.Handoff.LoopName
.Handoff.LoopAttempt
.Handoff.LoopMax
.Handoff.Prompt
```

This avoids asking AgentConfigs to parse the internal message header manually.

## Loop Semantics

The Slack-mediated loop is enforced by the dispatcher before it posts the next
Slack handoff message.

Rules:

- `loop-name` must stay the same for the chain.
- `subject-key` and `subject` must stay the same for the chain.
- `loop-attempt` defaults to `0`.
- `loop-max` defaults to `2` for PR babysitting.
- If `handoff.slack.increment-attempt=true`, dispatcher increments attempt by
  one before posting the child message.
- If the next attempt would exceed `loop-max`, dispatcher does not post a
  handoff command. It posts a visible "handoff blocked: max attempts reached"
  message in Slack.

PR babysitting uses:

- `loop-name=pr-babysit`
- `subject-key=pr.url`
- `subject=<PR URL>`
- `loop-max=2`

Only reviewer-to-dev-fix increments the attempt. Dev-fix-to-review keeps the
same attempt number.

## Idempotency

The dispatcher must avoid posting duplicate handoff commands.

Parent Task annotations:

```yaml
kelos.dev/slack-handoff-id: <handoff id>
kelos.dev/slack-handoff-state: posting|posted|blocked|failed
kelos.dev/slack-handoff-message-ts: <Slack message timestamp>
kelos.dev/slack-handoff-target: <target>
```

Dispatch sequence:

1. Compute `handoff-id`.
2. If parent annotation has the same `handoff-id` and state `posted`,
   do nothing.
3. Patch parent Task to `state=posting`.
4. Post Slack handoff message.
5. Patch parent Task to `state=posted` with the Slack message timestamp.

If step 4 fails, patch `state=failed` and post or update a visible blocked
message when possible.

If step 5 fails after Slack accepted the message, a retry could duplicate the
handoff. To reduce that risk, retries for `state=posting` must first call
`conversations.replies` and search for the visible `handoff-id` in the thread.
If found, patch `state=posted` with that message timestamp instead of posting
again.

## Slack UX

The handoff command is visible in the Slack thread. This is deliberate for the
shortcut.

Example:

```text
Cody handoff: review
parent-task: kelos/cody-dev-slack-abc123
reason: PR is open and ready for review.
```

The actual routable text must still contain:

```text
<@CODY_BOT_USER_ID> !handoff review
```

Keep the message compact. Long implementation details belong in the child
prompt after `---`.

## Cody GitOps

### Stable debugger exclusion

Add `!handoff` and `!babysit` to the stable debugger exclusion list:

```yaml
excludePatterns:
  - '^!(alpha|exp)\b'
  - '^!(ticket|dev|review|babysit|handoff)\b'
```

### User-facing babysitter route

Add:

- `agentconfig-cody-pr-babysitter.yaml`
- `taskspawner-cody-pr-babysitter-slack.yaml`

Trigger:

```text
@cody !babysit <PR URL or PR context>
```

Behavior:

- normalize the PR URL
- emit `handoff.slack.target=review`
- emit `handoff.slack.subject-key=pr.url`
- emit `handoff.slack.subject=<PR URL>`
- emit `handoff.slack.loop-name=pr-babysit`
- emit `handoff.slack.loop-attempt=0`
- emit `handoff.slack.loop-max=2`
- emit a reviewer prompt

Add `cody.alpheya.com/slack-handoff=enabled` to any user-facing persona
TaskSpawner that may dispatch a Slack handoff:

- `cody-ticket-slack`
- `cody-dev-slack`
- `cody-pr-babysitter-slack`

Do not add this label to `cody-pr-reviewer-slack` one-shot review unless we
later decide one-shot reviews may dispatch follow-up handoffs.

### Internal handoff TaskSpawners

Add bot-only internal TaskSpawners.

`cody-handoff-dev-slack`:

```yaml
when:
  slack:
    requireBotAuthor: true
    allowedBotIDs:
      - <cody-slack-bot-id>
    triggers:
      - pattern: '^!handoff dev\b'
taskTemplate:
  agentConfigRefs:
    - name: cody-base
    - name: cody-dev
    - name: cody-atlassian-mcp
  metadata:
    labels:
      cody.alpheya.com/persona: dev
      cody.alpheya.com/slack-handoff: enabled
```

`cody-handoff-review-slack`:

```yaml
when:
  slack:
    requireBotAuthor: true
    allowedBotIDs:
      - <cody-slack-bot-id>
    triggers:
      - pattern: '^!handoff review\b'
taskTemplate:
  agentConfigRefs:
    - name: cody-base
    - name: cody-pr-reviewer
    - name: cody-atlassian-mcp
  metadata:
    labels:
      cody.alpheya.com/persona: pr-reviewer
      cody.alpheya.com/slack-handoff: enabled
```

`cody-handoff-dev-fix-slack`:

```yaml
when:
  slack:
    requireBotAuthor: true
    allowedBotIDs:
      - <cody-slack-bot-id>
    triggers:
      - pattern: '^!handoff dev-fix\b'
taskTemplate:
  agentConfigRefs:
    - name: cody-base
    - name: cody-dev
    - name: cody-atlassian-mcp
  metadata:
    labels:
      cody.alpheya.com/persona: dev
      cody.alpheya.com/mode: pr-babysitter-fix
      cody.alpheya.com/slack-handoff: enabled
```

All internal handoff TaskSpawners reuse the Phase 1 Cody runtime fields:

- `type: codex`
- `image: docker.io/alpheya/codex:main`
- `credentials.secretRef.name: cody-codex-credentials`
- `serviceAccountName: cody-debugger`
- current GitHub App env
- current JWT env
- `cody.alpheya.com/tools-client: "true"`

### Prompt templates

Internal handoff prompt templates should use parsed handoff fields, not raw
Slack parsing.

Example for review:

```gotemplate
Cody internal handoff to PR reviewer.

Parent task: {{ .Handoff.ParentTask }}
Reason: {{ .Handoff.Reason }}
PR: {{ .Handoff.Subject }}
Loop: {{ .Handoff.LoopName }}
Attempt: {{ .Handoff.LoopAttempt }} of {{ .Handoff.LoopMax }}

Task:
{{ .Handoff.Prompt }}
```

Example for dev-fix:

```gotemplate
Cody internal handoff to dev-fix mode.

Parent task: {{ .Handoff.ParentTask }}
PR: {{ .Handoff.Subject }}
Loop: {{ .Handoff.LoopName }}
Attempt: {{ .Handoff.LoopAttempt }} of {{ .Handoff.LoopMax }}

Apply only the reviewer-requested fixes to the existing PR branch. Do not
change unrelated scope.

Requested fixes:
{{ .Handoff.Prompt }}
```

## AgentConfig Updates

### Ticket creator

- Emit `handoff.slack.target=dev` only when the user asked for implementation
  after ticket creation.
- Emit `handoff.slack.prompt`.
- Emit `ticket` and `ticket.url` when available.

### Dev

Normal dev mode:

- Emit `handoff.slack.target=review` only after a PR is opened and ready.
- Emit `handoff.slack.subject-key=pr.url`.
- Emit `handoff.slack.subject=<PR URL>`.
- Emit a review prompt.

PR babysitter fix mode:

- Only address reviewer findings for the existing PR.
- Push to the same PR branch.
- Emit `fix.pushed=true`.
- Emit `handoff.slack.target=review` after pushing fixes.
- Preserve `subject-key`, `subject`, `loop-name`, `loop-attempt`, and
  `loop-max` from the handoff prompt.
- Do not increment attempt on dev-fix-to-review.

### PR reviewer

One-shot review mode:

- Do not emit `handoff.slack.target=dev-fix`.
- If fixes are required, tell the user to invoke `@cody !dev ...` or
  `@cody !babysit ...`.

PR babysitter mode:

- If clean, emit `review.result=clean` and no handoff target.
- If blocked, emit `review.result=blocked` and no handoff target.
- If changes are required and another fix attempt is available, emit:
  - `review.result=changes-requested`
  - `handoff.slack.target=dev-fix`
  - `handoff.slack.increment-attempt=true`
  - `handoff.slack.prompt`
  - the same subject and loop metadata

### PR babysitter

- Parse the PR URL from the user request.
- If no PR URL is available, ask the user for one and emit no handoff target.
- Emit first review handoff metadata with attempt `0` and max `2`.
- Do not review or edit code directly.

## End-To-End Flows

### Ticket to dev to review

1. User sends `@cody !ticket create a ticket and implement it if clear`.
2. Ticket persona creates `ALPM-123` and emits `handoff.slack.target=dev`.
3. Slack reporter posts signed `@cody !handoff dev` in the same thread.
4. Internal dev TaskSpawner creates a dev Task.
5. Dev opens a PR and emits `handoff.slack.target=review`.
6. Slack reporter posts signed `@cody !handoff review`.
7. Internal reviewer TaskSpawner creates a review Task.
8. Reviewer posts final findings in the same thread.

### PR babysitter

1. User sends `@cody !babysit https://github.com/donchev7/kelos/pull/123`.
2. Babysitter emits first review handoff with `loop-name=pr-babysit`,
   `loop-attempt=0`, and `loop-max=2`.
3. Slack reporter posts signed `@cody !handoff review`.
4. Reviewer emits either:
   - `review.result=clean`, no handoff target
   - `review.result=blocked`, no handoff target
   - `handoff.slack.target=dev-fix` with findings
5. Reporter increments attempt and posts signed `@cody !handoff dev-fix`.
6. Dev-fix pushes focused fixes and emits `handoff.slack.target=review`.
7. Reporter posts signed `@cody !handoff review` with the same attempt.
8. Loop stops when clean, blocked, or the next fix attempt would exceed max.

## Unhappy Paths And Fallbacks

| Situation | Behavior |
| --- | --- |
| Agent emits no `handoff.slack.target` | Parent succeeds; no handoff message is posted. |
| Agent emits unknown target | Reporter posts "handoff blocked: unknown target" in Slack and annotates parent `blocked`. |
| Agent emits malformed loop attempt/max | Reporter posts "handoff blocked: invalid loop metadata". |
| Next attempt exceeds max | Reporter posts "handoff blocked: max attempts reached". |
| Subject changes mid-loop | Reporter blocks and posts a Slack-visible message. |
| Slack post fails | Parent Task remains succeeded; reporter annotates handoff `failed` and retries on next report cycle. |
| Slack post succeeds but annotation patch fails | Reporter searches the thread for `handoff-id` before retrying to avoid duplicates. |
| Slack event for handoff message is lost | Handoff message remains visible but no child Task appears; user can manually invoke the next persona or ask Cody to retry. |
| Internal handoff signature is invalid | Slack server ignores the message and logs the reason. |
| Human posts `@cody !handoff review ...` | Internal TaskSpawner rejects it because `requireBotAuthor=true`. |
| Normal Cody final reply mentions `@cody` | Still ignored unless it is a signed self-bot `!handoff` message. |
| Internal TaskSpawner is at max concurrency | Slack server drops the message as it does today; visible handoff message remains in thread. |
| Child Task fails | Loop stops unless the user manually invokes another persona. |

The biggest operational fallback is manual: because the handoff command is
visible in Slack, a user can copy the relevant context and invoke `@cody !dev`,
`@cody !review`, or `@cody !babysit` directly.

## Known Tradeoffs

- Slack becomes part of workflow execution, not just reporting.
- A Slack outage or event delivery issue can interrupt a handoff chain.
- Internal handoff messages add noise to the thread.
- There is no Kubernetes-level dependency edge between parent and child Tasks.
- Idempotency is best-effort across Slack post success plus Kubernetes patch
  failure.
- Max concurrency behavior remains drop-oriented. There is no queue for
  internal handoff messages in this shortcut.
- Long-term, Kelos-native handoffs are cleaner for reliability and audit.

## Implementation Plan

### 1. Add `kelos-output` and capture extras

Same implementation as the Kelos-native spec.

Acceptance criteria:

- custom outputs are present in `Task.status.results`
- malformed outputs are surfaced in logs
- reserved built-in keys cannot be overridden

### 2. Add handoff result parsing

Files:

- `internal/reporting/handoff.go`
- `internal/reporting/handoff_test.go`

Acceptance criteria:

- validates target allowlist
- validates prompt presence
- validates loop integers
- validates subject consistency
- computes deterministic handoff IDs
- computes signatures

### 3. Add Slack dispatcher

Files:

- `internal/reporting/watcher.go`
- `internal/reporting/slack.go`

Acceptance criteria:

- posts handoff after successful final report
- patches parent Task handoff annotations
- posts blocked messages for invalid handoffs
- avoids duplicate posts using handoff ID
- searches thread for handoff ID before retrying a stuck `posting` state

### 4. Add signed self-bot handoff routing

Files:

- `internal/slack/handler.go`
- `internal/slack/filter.go`
- `api/v1alpha1/taskspawner_types.go`
- generated deepcopy and CRD manifests

Acceptance criteria:

- normal self messages are ignored
- signed self `!handoff` messages are eligible for matching
- invalid signatures are ignored
- `requireBotAuthor=true` rejects human messages
- bot-authored messages still require `allowedBotIDs` when set

### 5. Add parsed handoff template variables

Files:

- `internal/slack/filter.go`
- `internal/slack/handler.go`

Acceptance criteria:

- `.Handoff.*` template variables are available for internal handoff messages
- existing Slack template variables remain unchanged

### 6. GitOps rollout

After the Kelos image and CRD are released:

1. Add `SLACK_HANDOFF_SIGNING_SECRET`.
2. Set `SLACK_HANDOFF_DISPATCH_ENABLED=true`.
3. Read Cody's Slack bot ID from `auth.test` logs and configure it in
   `allowedBotIDs` for internal handoff TaskSpawners.
4. Add `!handoff` and `!babysit` to debugger excludes.
5. Add `cody-pr-babysitter` AgentConfig and TaskSpawner.
6. Add internal bot-only handoff TaskSpawners.
7. Add `cody.alpheya.com/slack-handoff=enabled` labels to persona routes that
   may dispatch handoffs.
8. Update AgentConfigs to emit `handoff.slack.*` outputs.

## Manual Setup

Required:

- Generate `SLACK_HANDOFF_SIGNING_SECRET` as a random secret value and mount it
  into the Slack server deployment.
- Set `SLACK_HANDOFF_DISPATCH_ENABLED=true` only after internal handoff
  TaskSpawners are applied.
- Capture Cody's Slack bot ID from the Slack server `auth.test` startup log.
  This is the `botID`, not the bot user ID.
- Configure that bot ID in `allowedBotIDs` for internal handoff TaskSpawners.

No new Slack app is required for the default same-bot design.

## Tests

Unit tests:

- `kelos-output` validation
- capture extra outputs
- handoff parser target validation
- handoff parser loop validation
- handoff ID determinism
- HMAC signature validation
- prompt extraction after `---`

Slack handler tests:

- normal self bot message ignored
- signed self `!handoff` message accepted
- invalid signature rejected
- human `!handoff` rejected by `requireBotAuthor`
- allowed bot ID still works for non-self bots
- existing human `@cody !dev` behavior unchanged

Reporter tests:

- successful Task with handoff output posts one internal Slack message
- no handoff output posts none
- unknown target posts blocked message
- max attempts posts blocked message
- subject mismatch posts blocked message
- repeated report does not duplicate handoff
- stuck `posting` state searches thread by handoff ID before repost

Manual tests:

```text
@cody !ticket create a test-only ticket and do not implement it
```

Expected: no handoff.

```text
@cody !ticket create a test-only docs ticket and implement it if clear
```

Expected: signed `!handoff dev` appears, then dev runs.

```text
@cody !dev make a tiny docs-only test PR
```

Expected: signed `!handoff review` appears after PR creation, then reviewer
runs.

```text
@cody !babysit https://github.com/donchev7/kelos/pull/<test-pr>
```

Expected: review/fix/re-review loop runs until clean, blocked, or max attempts.

```text
@cody !handoff review
```

Expected: no Task, because human-authored internal handoffs are rejected.

## Rollback

Kelos rollback:

- Disable Slack handoff dispatch with an env flag:

  ```text
  SLACK_HANDOFF_DISPATCH_ENABLED=false
  ```

- Existing user-facing Slack persona routes continue to work.
- Internal `!handoff` messages stop being posted.

GitOps rollback:

- Remove internal handoff TaskSpawners.
- Remove `cody-pr-babysitter-slack` if babysitting is problematic.
- Keep Phase 1 `!ticket`, `!dev`, and `!review` routes.

Manual recovery:

- If a handoff chain stalls, use the visible Slack thread context to invoke the
  next persona manually.

## Acceptance Criteria

This shortcut is complete when:

- agents can emit `handoff.slack.*` results
- successful Slack-reported Tasks can post signed internal handoff messages
- only signed Cody self-bot `!handoff` messages can trigger internal handoff
  TaskSpawners
- humans cannot trigger internal `!handoff` TaskSpawners
- ticket-to-dev and dev-to-review work through Slack messages
- `@cody !babysit` runs a bounded review/dev-fix loop on one PR
- blocked handoffs are visible in Slack
- normal Phase 1 Cody behavior remains unchanged
