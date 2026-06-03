# Cody Cron Agent Session Implementation Spec

Status: draft implementation spec
Date: 2026-06-03
Owner: Cody / Kelos

## Summary

Implement session-backed execution for cron `TaskSpawner`s as the second
source-specific `AgentSession` implementation after Slack thread sessions.

The goal is not to fully generalize sessions yet. The goal is to implement
cron sessions cleanly enough that Slack and cron expose the shared shape we
will later lift into a source-neutral execution policy.

Target behavior for infra-health:

```text
TaskSpawner cody-datadog-health-non-prod-qa
  every 5 minutes
    -> create/find today's non-prod/qa AgentSession
    -> create one AgentTurn for this cron tick
    -> run turns through one Codex App Server thread
    -> let Cody maintain continuity across the day
    -> roll over to a new session the next day
```

## Goals

- Add cron-specific session mode to `TaskSpawner`.
- Reuse the existing `AgentSession`, `AgentTurn`, `AgentSessionReconciler`,
  `JobBuilder.BuildSessionRunner`, and `kelos-session-runner`.
- Keep existing one-shot cron behavior unchanged unless cron session mode is
  explicitly enabled.
- Make each cron tick an ordered `AgentTurn` instead of a standalone `Task`.
- Use a deterministic session scope so repeated cron ticks for the same
  workflow/env/day go to the same Codex App Server thread.
- Keep Slack destination-based reporting for infra-health unchanged.
- Add enough source-neutral fields to `AgentSessionSource` and
  `AgentTurnSource` to avoid baking more Slack-only assumptions into the CRDs.
- Use infra-health as the first cron-session workflow.

## Non-Goals

- Do not implement the final generic top-level `execution.mode: session` API.
- Do not migrate GitHub, Jira, Aikido, or generic webhook sources.
- Do not add persistent cross-session memory.
- Do not add `SessionArtifact` in this PR.
- Do not change Slack thread session behavior except for shared source structs
  if needed.
- Do not let Cody post directly to Slack.
- Do not make infra-health dedupe perfect. Existing prompt-level PR checks stay
  in place for the first cron-session cut.

## Current State

### Cron TaskSpawner Path

Cron `TaskSpawner`s currently use Kubernetes `CronJob`s:

- `TaskSpawnerReconciler` builds a `CronJob` through
  `DeploymentBuilder.BuildCronJob`.
- The CronJob runs `cmd/kelos-spawner` in `--one-shot` mode.
- `CronSource.Discover` returns one `WorkItem` per cron tick since
  `status.lastDiscoveryTime`.
- `cmd/kelos-spawner` renders `taskTemplate.promptTemplate` and creates one
  `Task` per discovered cron item.
- It then updates `TaskSpawner.status.lastDiscoveryTime`.

Infra-health is currently configured in the `skills` repo:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-datadog-health-non-prod-qa
  namespace: kelos-system
spec:
  maxConcurrency: 1
  suspend: true
  when:
    cron:
      schedule: "*/5 * * * *"
  taskTemplate:
    type: codex
    image: docker.io/alpheya/codex:main
    agentConfigRefs:
      - name: cody-debugger
      - name: cody-atlassian-mcp
      - name: cody-datadog-mcp
      - name: cody-infra-health-scheduled
    metadata:
      labels:
        cody.alpheya.com/persona: infra-health
        cody.alpheya.com/source: cron
        kelos.dev/slack-reporting: "enabled"
      annotations:
        kelos.dev/slack-reporting: "deferred"
        kelos.dev/slack-destination: cody-devops
        kelos.dev/slack-layout: stable-summary-root
```

### Slack Session Path

Slack sessions already exist:

- `when.slack.session.enabled: true`
- Slack root thread -> deterministic `AgentSession`
- Slack message -> `AgentTurn`
- `AgentSessionReconciler` starts a session runner Job.
- `kelos-session-runner` sends each turn to `codex app-server`.

That implementation is Slack-shaped in a few places:

- `AgentSessionSource` has required-looking Slack fields such as `channelID`
  and `rootTS`.
- `AgentTurnSource` has Slack message fields such as `messageTS`, `userID`,
  and `permalink`.
- `renderTurnPrompt` says "Slack session" even though the runner mechanics are
  useful for any source.

The cron implementation should start moving these pieces toward a neutral
source shape without doing a full API redesign.

## Proposed API

Add an optional session block under `spec.when.cron`.

```yaml
spec:
  when:
    cron:
      schedule: "*/5 * * * *"
      session:
        enabled: true
        scopeTemplate: "{{.TaskSpawner}}/{{.Date}}"
        maxAge: 24h
        idleTimeout: 1h
        maxQueuedTurns: 5
```

### Defaults

- `enabled: false`
- `scopeTemplate: "{{.TaskSpawner}}/{{.Date}}"`
- `maxAge: 24h`
- `idleTimeout: 1h`
- `maxQueuedTurns: 5`

### Scope Template Variables

Expose these variables to `scopeTemplate`:

- `TaskSpawner`: TaskSpawner name.
- `Namespace`: TaskSpawner namespace.
- `Schedule`: cron schedule.
- `Time`: cron tick time in RFC3339.
- `ID`: cron work item ID, currently `YYYYMMDD-HHMM`.
- `Date`: UTC date from the cron tick, `YYYY-MM-DD`.
- `Hour`: UTC hour from the cron tick, `YYYYMMDD-HH`.

Infra-health should use a daily scope:

```yaml
scopeTemplate: "infra-health/non-prod/qa/{{.Date}}"
```

This gives one Codex App Server session per environment/namespace/day.

### API Types

Add:

```go
type Cron struct {
    Schedule string `json:"schedule"`

    // Session configures cron-triggered AgentSession behavior.
    // +optional
    Session *CronSession `json:"session,omitempty"`
}

type CronSession struct {
    // Enabled switches cron ticks from one-shot Task creation to
    // AgentSession/AgentTurn creation.
    // +optional
    Enabled bool `json:"enabled,omitempty"`

    // ScopeTemplate renders a deterministic session scope key.
    // +optional
    ScopeTemplate string `json:"scopeTemplate,omitempty"`

    // MaxAge closes or rolls over a session after this duration.
    // +optional
    MaxAge *metav1.Duration `json:"maxAge,omitempty"`

    // IdleTimeout closes an idle session after no queued or running turns
    // remain for this duration.
    // +optional
    IdleTimeout *metav1.Duration `json:"idleTimeout,omitempty"`

    // MaxQueuedTurns limits queued turns per session.
    // +optional
    MaxQueuedTurns *int32 `json:"maxQueuedTurns,omitempty"`
}
```

Validation:

- `scopeTemplate` max length 512.
- `maxAge`, when set, must be positive.
- `idleTimeout`, when set, must be positive.
- `maxQueuedTurns`, when set, must be greater than zero.

## Source Model Changes

To avoid adding more Slack-only fields, extend the existing source structs.

Compatibility guardrails:

- Do not rename existing Go fields or JSON tags.
- Do not remove any existing Slack fields.
- Do not change Slack session creation payloads; Slack-created sessions and
  turns should continue to populate `channelID`, `rootTS`, `threadURL`,
  `messageTS`, `userID`, `botID`, and `permalink` as they do today.
- Do not change Slack routing labels such as `kelos.dev/slack-channel` and
  `kelos.dev/slack-root-ts`.
- Only relax CRD required-ness so non-Slack sessions do not need fake Slack
  values.
- Any Slack transcript fetching must be gated on `source.type == SlackThread`
  or equivalent Slack-specific fields being present.

### AgentSessionSource

Keep existing Slack fields for compatibility, but mark them optional in the
CRD and add source-neutral fields:

```go
type AgentSessionSource struct {
    Type string `json:"type"` // SlackThread, Cron

    // Key is the rendered deterministic session scope key.
    // +optional
    Key string `json:"key,omitempty"`

    // DisplayName is a human-readable source label for prompts/status.
    // +optional
    DisplayName string `json:"displayName,omitempty"`

    // Slack fields, optional.
    TeamID string `json:"teamID,omitempty"`
    ChannelID string `json:"channelID,omitempty"`
    RootTS string `json:"rootTS,omitempty"`
    ThreadURL string `json:"threadURL,omitempty"`

    // Cron fields, optional.
    Schedule string `json:"schedule,omitempty"`
}
```

### AgentTurnSource

Keep existing Slack fields for compatibility, but mark them optional and add
source-neutral fields:

```go
type AgentTurnSource struct {
    Type string `json:"type"` // SlackMessage, CronTick

    // ID is the source event ID used for dedupe.
    // +optional
    ID string `json:"id,omitempty"`

    // DisplayName is a human-readable source label for prompts/status.
    // +optional
    DisplayName string `json:"displayName,omitempty"`

    // Cron fields, optional.
    Time string `json:"time,omitempty"`
    Schedule string `json:"schedule,omitempty"`

    // Slack fields, optional.
    TeamID string `json:"teamID,omitempty"`
    ChannelID string `json:"channelID,omitempty"`
    RootTS string `json:"rootTS,omitempty"`
    MessageTS string `json:"messageTS,omitempty"`
    UserID string `json:"userID,omitempty"`
    BotID string `json:"botID,omitempty"`
    Permalink string `json:"permalink,omitempty"`
}
```

This is a CRD/chart change.

## Runtime Design

### Session Naming

Use a deterministic Kubernetes-safe name:

```text
<taskspawner-name>-cron-sess-<hash(namespace, taskspawner, rendered-scope)>
```

If the rendered scope changes, a different session is used.

For daily infra-health scope, the scope changes at UTC day boundary.

### Session Creation

When `cmd/kelos-spawner` processes a cron `WorkItem` and
`when.cron.session.enabled` is true:

1. Build the normal template vars with `source.WorkItemToTemplateVars(item)`.
2. Add cron session vars:
   - `TaskSpawner`
   - `Namespace`
   - `Date`
   - `Hour`
3. Render `scopeTemplate`.
4. Create or fetch the matching `AgentSession`.
5. Snapshot `taskTemplate` into `AgentSession.spec.taskTemplateSnapshot`.
6. Set:

```yaml
spec:
  source:
    type: Cron
    key: <rendered-scope>
    displayName: "cron:<taskspawner>"
    schedule: "*/5 * * * *"
  taskSpawnerRef:
    name: <taskspawner>
  idleTimeout: <effective idle timeout>
  maxQueuedTurns: <effective max queued turns>
```

7. If the existing session is `Closed` or `Error`, create a new generation
   instead of trying to reuse the closed object.

Closed-session handling is important because cron scopes can be stable for many
hours. Recommended minimal strategy:

- Include a generation suffix when the previous matching session is terminal:

```text
<taskspawner-name>-cron-sess-<hash>-g2
```

- Keep a label with the stable scope hash:

```yaml
labels:
  kelos.dev/source: cron
  kelos.dev/taskspawner: <name>
  kelos.dev/session-scope-hash: <hash>
```

- When looking for a session, select the newest non-terminal session by that
  scope hash.

For infra-health, daily scope plus `maxAge: 24h` should usually avoid multiple
generations per day.

### Turn Creation

For each cron `WorkItem`, create one `AgentTurn`:

```yaml
spec:
  sessionRef:
    name: <session>
  sequence: <next sequence>
  source:
    type: CronTick
    id: <work-item ID>
    displayName: "cron tick <Time>"
    time: <Time>
    schedule: <Schedule>
  input:
    text: <Time>
    body: <rendered taskTemplate.promptTemplate>
  context:
    mode: None
    toTSInclusive: <work-item ID>
```

The body should be the rendered `taskTemplate.promptTemplate`, just like
one-shot Tasks. This mirrors the Slack prompt-parity work.

Deduplication:

- Before creating a turn, list turns for the session.
- If a turn with `source.type=CronTick` and `source.id=<work-item ID>` exists,
  skip creation.
- This protects against CronJob retries and repeated one-shot spawner runs.

Queue limit:

- Reuse the current `countQueuedOrRunningTurns` logic or factor it into a
  shared helper.
- If the session already has `maxQueuedTurns` queued/running turns, skip this
  tick and log a clear message.
- Do not create a one-shot fallback Task.

### Max Age

Add `maxAge` enforcement to `AgentSessionReconciler`.

Recommended minimal status addition:

- Use `status.lastActivityAt` for idle timeout as today.
- Use `metadata.creationTimestamp` for max age.

Reconcile behavior:

- If `spec.maxAge` is set and `now - creationTimestamp >= maxAge`, move
  session to `Closed` once there are no running turns.
- If max age is reached while a turn is running, do not interrupt it; close
  after the turn finishes and the session becomes idle.

This requires adding `MaxAge` to `AgentSessionSpec`, not just `CronSession`,
because the controller enforces session lifecycle from the session resource.

### Runner Prompt

Update `kelos-session-runner.renderTurnPrompt` to be source-aware.

For Slack:

- Preserve current Slack behavior.

For cron:

```text
You are running Cody through a Kelos cron AgentSession.

Session:
- Kelos session: <namespace>/<name>
- Source: cron
- Scope: <source.key>
- TaskSpawner route: <namespace>/<taskspawner>
- Turn sequence: <n>

Previous turns in this Codex thread may contain earlier cron checks for the
same scope. Use that continuity, but treat this cron tick as a fresh check.

Cron tick:
- Time: <turn.source.time>
- Schedule: <turn.source.schedule>

Route prompt:
<rendered taskTemplate.promptTemplate>

Use Kelos reporting channels configured on the originating TaskSpawner. If the
prompt says to stay silent when there is no material finding, return a final
response prefixed with `NO_SLACK:` so Kelos can suppress deferred Slack
delivery for that turn.
```

For cron sequence `> 1`, still use the rendered route prompt as the main
request. Unlike Slack follow-ups, cron turns are not human replies and do not
need Slack transcript context.

## Controller / RBAC Changes

The spawner CronJob currently creates `Task`s. With cron sessions, it also
needs to create/read:

- `AgentSession`
- `AgentTurn`

Add RBAC to the spawner service account if it does not already have it:

- `agentsessions`: get, list, watch, create, update, patch
- `agentturns`: get, list, watch, create

`AgentSessionReconciler` already handles runner Jobs, but it needs:

- `AgentSession.spec.maxAge` support.
- source-neutral prompt/status behavior.

Because source structs and `AgentSessionSpec` change, regenerate:

- `api/v1alpha1/zz_generated.deepcopy.go`
- CRDs under `internal/manifests/charts/kelos/templates/crds/`
- `internal/manifests/install-crd.yaml`

Publish a new chart after this PR.

## Infra-Health Manifest Change

In `skills`, update:

`cody/infra-health/non-prod/taskspawner-cody-datadog-health-non-prod-qa.yaml`

from:

```yaml
when:
  cron:
    schedule: "*/5 * * * *"
```

to:

```yaml
when:
  cron:
    schedule: "*/5 * * * *"
    session:
      enabled: true
      scopeTemplate: "infra-health/non-prod/qa/{{.Date}}"
      maxAge: 24h
      idleTimeout: 30m
      maxQueuedTurns: 3
```

Recommended for infra-health:

- `maxAge: 24h` gives a daily Codex continuity window.
- `idleTimeout: 30m` lets the runner exit if the cron is suspended or blocked.
- `maxQueuedTurns: 3` avoids a backlog if checks take longer than the schedule.

If this cron remains `suspend: true`, enabling session mode has no runtime
effect until the spawner is unsuspended.

## Tests

### API / CRD Tests

- `CronSession` fields round-trip through fake client objects.
- CRD includes `when.cron.session`.
- CRD marks Slack source fields optional after source model changes.

### Spawner Tests

Add focused tests in `cmd/kelos-spawner`:

- Cron TaskSpawner without `when.cron.session.enabled` still creates one-shot
  `Task`s.
- Cron TaskSpawner with session enabled creates:
  - one `AgentSession`;
  - one `AgentTurn`;
  - no `Task`.
- First cron turn body equals rendered `taskTemplate.promptTemplate`.
- Re-running the spawner for the same cron tick does not create a duplicate
  turn.
- Multiple cron ticks in the same day/scope create multiple turns on the same
  session.
- A new UTC day renders a different scope and creates a different session.
- Terminal session with the same scope creates a new generation.
- `maxQueuedTurns` skips additional turns when queued/running count is at the
  limit.

### Session Runner Tests

- Cron source prompts use "cron AgentSession" language.
- Cron turns include source key, tick time, schedule, route prompt, and task
  spawner.
- Slack prompts remain unchanged.
- Slack sessions created with the existing field shape still render and run.

### Controller Tests

- `AgentSessionReconciler` closes an idle cron session after `idleTimeout`.
- `AgentSessionReconciler` closes a cron session after `maxAge` once no turn is
  running.
- `maxAge` does not interrupt a running turn.

### Skills/GitOps Validation

- `kubectl kustomize cody` in the skills repo.
- Apply/rendered manifest validates against the updated Kelos CRD.

## Rollout Plan

1. Merge Kelos cron-session PR.
2. Build and push affected images:
   - `docker.io/alpheya/kelos-spawner:main`
   - `docker.io/alpheya/kelos-controller:main`
   - `docker.io/alpheya/codex:main`
3. Regenerate/package/push the Helm chart because CRDs/RBAC change.
4. Let k8s-platform-gitops pick up the new chart/image if chart version or
   image config changes are needed.
5. Merge skills PR enabling cron session mode for infra-health.
6. If desired, unsuspend the infra-health TaskSpawner in the same or a later
   skills PR.

## Manual Verification

After deployment:

```bash
kubectl -n kelos-system get taskspawner cody-datadog-health-non-prod-qa \
  -o yaml

kubectl -n kelos-system get agentsessions \
  -l kelos.dev/source=cron,kelos.dev/taskspawner=cody-datadog-health-non-prod-qa

kubectl -n kelos-system get agentturns \
  -l kelos.dev/source=cron,kelos.dev/taskspawner=cody-datadog-health-non-prod-qa
```

Expected:

- first cron tick creates one `AgentSession`;
- each subsequent tick creates one `AgentTurn` on the same session for the day;
- no one-shot `Task` is created for infra-health while session mode is enabled;
- the session runner Job has label `kelos.dev/component=agent-session`;
- deferred Slack delivery suppresses final responses prefixed with
  `NO_SLACK:`;
- when a deferred cron turn has reportable terminal output, Kelos resolves the
  configured `kelos.dev/slack-destination`, creates a stable root message, and
  posts longer details in that root message thread.

## Open Design Choices

- Whether `scopeTemplate` should default to daily scope or spawner-only scope.
  For safety, use daily default.
- Whether `maxAge` should be part of every `AgentSession` or only cron-created
  sessions. For controller simplicity, put it on `AgentSessionSpec`.
- Whether skipped ticks due to `maxQueuedTurns` should update
  `TaskSpawner.status.lastDiscoveryTime`. Recommended: yes, because the system
  intentionally dropped that tick rather than deferring it.
- Whether cron session turns should preserve context from previous turns in the
  prompt. Recommended: rely on the Codex App Server thread history and avoid
  duplicating previous output into each prompt.
- Whether infra-health should stay suspended for the first deploy. Recommended:
  keep it suspended until the runtime is verified, then unsuspend in a small
  skills PR.

## Future Generalization

After Slack and cron are both implemented and tested, lift the common pieces
into a generic execution policy:

```yaml
spec:
  execution:
    mode: session
    session:
      scopeTemplate: ...
      maxAge: ...
      idleTimeout: ...
      maxQueuedTurns: ...
```

At that point:

- Slack can provide default scope = Slack thread.
- Cron can provide default scope = TaskSpawner/date.
- GitHub webhook can provide default scope = PR/issue.
- Generic webhook can provide scope from payload templates.

The cron implementation should therefore avoid infra-health-specific code in
Kelos. Infra-health details belong in the TaskSpawner prompt, AgentConfig, and
`scopeTemplate`.
