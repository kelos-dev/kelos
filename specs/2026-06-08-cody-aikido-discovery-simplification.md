# Cody Aikido Discovery Simplification

Status: draft
Date: 2026-06-09

## Context

The Aikido security workflow should create Cody sessions for individual Aikido
issue groups on latest `main`. Each session babysits remediation across PRs,
builds, package/image propagation, and follow-up work during a bounded daily
window.

The current discovery path does too much before creating any work:

- It resolves Aikido code repositories for a branch.
- It fetches `/issues/export` rows for each repository/status/issue type
  combination.
- It groups export rows by issue group ID.
- It fetches `/issues/groups/{groupID}` for every discovered issue group.
- Only after all of that does it create work.

The latest manual run proved the failure mode. `cody-tools` showed:

- 1 successful `/repositories/code` request.
- 16 successful `/issues/export` requests.
- 3 successful `/issues/groups/{id}` requests.
- the 4th `/issues/groups/{id}` request hit Aikido `429`.

That means discovery made 20 successful Aikido API calls in roughly 5 seconds,
then failed on the 21st call before creating a session for the current run.

## Goals

- Make the Aikido controller a patient sequential worker.
- Create each issue-group session as soon as enough context for that group is
  available.
- Respect Aikido `Retry-After` and continue the run instead of failing at the
  first throttle.
- Keep `cody-tools` synchronous; do not turn it into an async queue.
- Remove Aikido API access from agent sessions for v1.
- Start a new bounded session for the same issue group on each daily controller
  run, similar to the DevOps babysitter sessions.
- Keep duplicate-PR avoidance as a prompt-level guardrail only.

## Non-Goals

- Do not add deterministic PR locks, PR registries, or controller-owned PR
  dedupe beyond the existing session scope.
- Do not add Aikido write APIs to agent sessions.
- Do not make `cody-tools` responsible for queueing, session creation, or
  orchestration.
- Do not persist discovery progress across CronJob runs in v1.
- Do not require an Aikido CRD redesign for v1.

## Architecture Decision

Use this split:

- `cody-tools`: synchronous Aikido gateway.
- Aikido controller/spawner: long-running sequential worker.
- Cody agent session: remediation worker with GitHub/repo/build access, but no
  Aikido API access.

`cody-tools` should enforce the shared Aikido limit and surface upstream
`Retry-After`. The Aikido controller should own long waits, ordering, and when
to create sessions. Making `cody-tools` async would create a second
orchestrator with queueing, correlation, and dedupe responsibilities that
belong in the controller.

## Desired Runtime

### Daily Discovery Run

1. A daily Aikido controller CronJob starts.
2. It fetches candidate issue rows from Aikido through `cody-tools`.
3. It groups candidates by issue group ID.
4. It sorts groups deterministically.
5. It processes issue groups one at a time.
6. For each issue group:
   - fetch or assemble just enough Aikido context for that group;
   - create the daily AgentSession and first AgentTurn immediately;
   - move to the next issue group.
7. If Aikido returns `429` with `Retry-After`:
   - wait for the requested time when the remaining run budget allows;
   - retry the same request;
   - continue down the issue group list.
8. Stop early only when the controller is approaching its own hard run
   deadline.

The normal behavior is not "fail and let the next cron continue". The normal
behavior is to patiently drain the issue list in the same daily run.

### Per-Issue Daily Sessions

Each daily run creates a new session scope per issue group:

```text
aikido/<branch>/<run-date>/<issue-group-id>
```

Example:

```text
aikido/main/2026-06-09/24589148
```

This gives:

- one Slack root per issue group per daily run;
- no cross-day session reuse;
- sessions bounded to roughly 24 hours;
- behavior aligned with the DevOps babysitter pattern.

Recommended session bounds:

```yaml
session:
  enabled: true
  scopeTemplate: 'aikido/{{.Branch}}/{{.RunDate}}/{{ index .Metadata "aikido.kelos.dev/issue-group-id" }}'
  maxAge: 25h
  idleTimeout: 25h
  maxQueuedTurns: 1
```

Recommended task runtime bound:

```yaml
podOverrides:
  activeDeadlineSeconds: 90000 # 25h
```

### In-Day Heartbeats

A daily Aikido discovery run alone would create only one turn. To babysit
remediation during the 24-hour window, add a cheap non-Aikido heartbeat source
for active Aikido sessions from the current run date.

Heartbeat behavior:

- run every 15-30 minutes;
- list active Aikido sessions for today;
- if a session has no queued/running turn, create a follow-up AgentTurn;
- do not call Aikido;
- prompt the agent to continue remediation using GitHub, repository, CI,
  package, and build state.

The next daily Aikido controller run is the verification pass. If the issue
still appears in Aikido, that next run starts a new daily session. If the issue
is gone, the controller starts nothing for that issue group.

## Aikido Snapshot Contract

Agent sessions should not call Aikido in v1. The controller gives the agent a
rich snapshot in the first turn.

Minimum snapshot fields:

```text
issue group ID
Aikido URL
branch
run date
status
severity
issue type
affected repositories
affected packages/images/files
CVE IDs
vulnerable/current/fixed versions when present
bounded evidence rows
discovery timestamp
```

The first prompt should state:

```text
This Aikido snapshot was captured by the controller. Do not call Aikido from
the agent session. Use GitHub/repository/build evidence for remediation. The
next daily Aikido controller run will verify whether the issue still exists.
```

## Sequential Processing Details

### 1. Candidate Selection

Use the existing TaskSpawner fields:

```yaml
when:
  aikido:
    branch: main
    repositories:
      - template-nestjs-be
    statuses:
      - open
    severities:
      - critical
      - high
    issueTypes:
      - open_source
      - docker_container
      - sast
```

Candidate selection may still fetch `/repositories/code` and `/issues/export`
pages. Those calls should also go through the rate-limited `cody-tools` gateway
and should be sequential, not concurrent.

### 2. Grouping

Group export rows by issue group ID.

For each group:

- choose a stable title from the first non-empty title/summary;
- choose the highest severity from grouped rows;
- aggregate affected packages/images/files;
- aggregate CVE IDs;
- aggregate repositories;
- preserve a bounded evidence sample.

### 3. Per-Group Snapshot And Session Creation

Process sorted group IDs one by one.

For each group:

1. Build a snapshot from export rows.
2. If required snapshot fields are missing, make a bounded detail request for
   that one group.
3. If that detail request receives `429`, wait according to `Retry-After`,
   retry, then continue.
4. Create the AgentSession/AgentTurn for that group immediately after the
   snapshot is ready.
5. Move to the next group.

This ensures progress is not lost if a later group hits rate limits or the run
deadline.

## Rate Limit And Deadline Behavior

This spec complements `2026-06-08-cody-aikido-rate-limiting.md`.

Responsibilities:

- `cody-tools` performs short bounded waits and returns `Retry-After`.
- the Aikido controller performs long waits and keeps processing the issue list.
- the controller stops only when remaining run time is too small to wait safely.

Recommended controller bounds:

```text
Aikido discovery CronJob activeDeadlineSeconds: 7200   # 2 hours
Aikido per-request max wait from controller: Retry-After + jitter, capped by remaining run budget
CronJob concurrencyPolicy: Forbid
```

If the run deadline is reached:

- log which issue group was being processed;
- record that the run was truncated by deadline;
- do not create partial or low-confidence sessions for unprocessed groups.

## Duplicate PR Guardrail

Do not add controller-level deterministic PR controls in v1.

The prompt should instruct the agent to manage duplicates the same way the
DevOps agent does:

- search GitHub for existing open Cody/security PRs before opening a new one;
- include the Aikido issue group ID in PR titles/bodies/branches where useful;
- continue or update existing matching PRs when appropriate;
- avoid opening duplicate PRs for the same issue group and target branch.

This remains a prompt-level guardrail only. The controller should not own PR
dedupe logic.

## Implementation Plan

### Kelos

1. Keep `cody-tools` synchronous.
2. Add/consume `Retry-After` behavior from the rate-limit spec.
3. Change Aikido discovery to process issue groups sequentially.
4. Create the AgentSession/AgentTurn immediately after each group's snapshot is
   ready.
5. Add `RunDate` to Aikido template variables.
6. Change the default Aikido session scope to include run date.
7. Remove Aikido proxy environment variables from Aikido agent session pods.
8. Add or configure an in-day non-Aikido heartbeat source for active Aikido
   sessions.
9. Add structured logs:
   - candidate rows fetched;
   - group count;
   - current group ID;
   - Retry-After waits;
   - session/turn created;
   - deadline truncation.

### Skills

1. Update the Aikido security prompt to treat controller snapshot data as the
   only Aikido source of truth for that turn.
2. Tell the agent not to call Aikido.
3. Add the prompt-level duplicate PR guardrail.
4. Keep remediation focused on latest `main`, GitHub, repository changes, CI,
   package/image propagation, and consumer bumps.

### GitOps

1. Update the Aikido TaskSpawner session scope to include run date.
2. Set 25-hour session/runtime bounds.
3. Remove Aikido proxy env wiring from Aikido agent pods.
4. Add the in-day heartbeat configuration if implemented as a TaskSpawner.
5. Keep the daily Aikido discovery schedule.

## Tests

### Unit Tests

- Candidate selection uses sequential Aikido proxy calls.
- Export rows are grouped by issue group ID.
- A session/turn is created immediately after a group's snapshot is ready.
- Later `429` does not discard already-created sessions.
- Controller waits on `Retry-After` and retries the same group.
- Controller stops when the run deadline cannot safely accommodate the wait.
- Aikido session scope includes branch, run date, and issue group ID.
- Aikido agent pod config does not include Aikido proxy access.
- Agent prompt includes the duplicate PR guardrail.

### Manual Test

1. Configure a fake Aikido server to allow 2 calls/minute and return
   `Retry-After`.
2. Trigger the Aikido controller manually.
3. Confirm it creates session 1, waits, creates session 2, waits, and continues.
4. Confirm agent session pods do not receive Aikido proxy env vars.
5. Confirm the next daily run creates a new session scope for the same issue
   group instead of heartbeating yesterday's session.

## Migration Notes

- Existing active Aikido sessions can expire naturally.
- New sessions use the run-date scope and produce new daily Slack roots.
- The first rollout may leave old one-shot Task status in TaskSpawner status
  until a successful new generation updates status.

## Open Questions

- Should the in-day heartbeat be implemented as a generic AgentSession
  heartbeat controller or as an Aikido-specific TaskSpawner source first?
- What is the right daily run deadline: 1 hour, 2 hours, or longer?
- Which Aikido snapshot fields are sufficient for each issue type without
  giving the agent direct Aikido access?
- Should the controller ever request an Aikido re-scan, or should verification
  rely only on the next scheduled Aikido state?
