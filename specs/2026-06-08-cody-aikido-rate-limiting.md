# Cody Aikido Rate Limiting

Status: draft
Date: 2026-06-09

## Context

The Aikido security workflow hit Aikido's account-level request cap during
discovery:

```text
discovering items: fetching Aikido issue group 24589148: Aikido proxy returned status 429:
{"error":"You have reached the maximum number of calls per minute. (20 calls per minute)"}
```

`cody-tools` logs for the same run showed the exact call pattern:

- 1 successful `/repositories/code` request.
- 16 successful `/issues/export` requests.
- 3 successful `/issues/groups/{id}` requests.
- the next `/issues/groups/{id}` request returned `429`.

So discovery got through candidate export and failed during issue-group detail
enrichment. This confirms two needs:

- Aikido calls must be rate limited centrally at the `cody-tools` proxy.
- The Aikido controller must process issue groups sequentially and wait on
  `Retry-After` instead of failing the whole run.

Agent sessions should not call Aikido in v1. The controller owns Aikido API
access and gives each agent session a bounded Aikido snapshot.

## Goals

- Keep Aikido calls under the observed account limit of 20 calls per minute.
- Keep `cody-tools` synchronous; do not turn it into an async queue.
- Make `cody-tools` a disciplined gateway: rate limit, cooldown, timeout,
  retry once when safe, and surface `Retry-After`.
- Let the Aikido controller own long waits, issue ordering, and session
  creation.
- Respect controller run deadlines; do not let requests wait forever.
- Produce logs that make it obvious whether Cody waited locally, obeyed an
  upstream cooldown, or stopped because the controller run budget was exhausted.

## Non-Goals

- Do not build a distributed rate limiter in the first implementation.
- Do not add write access to Aikido.
- Do not add Aikido access to Cody agent sessions.
- Do not make `cody-tools` persist work queues or discovery progress.
- Do not persist Aikido rate-limit state across pod restarts.

## Proposed Design

### 1. Keep `cody-tools` Synchronous

All outbound Aikido API calls made through `/aikido` should pass through one
in-memory limiter inside `cody-tools`.

`cody-tools` should handle one HTTP request synchronously:

1. wait briefly for local rate-limit capacity;
2. call Aikido with an upstream timeout;
3. parse upstream `429` / `Retry-After`;
4. optionally retry once if the local request budget allows;
5. return either the upstream response or a controlled local `429`.

It should not enqueue work, create sessions, or continue processing after the
HTTP request returns.

Default behavior:

```text
CODY_TOOLS_AIKIDO_RATE_LIMIT_PER_MINUTE=18
CODY_TOOLS_AIKIDO_RATE_LIMIT_BURST=1
CODY_TOOLS_AIKIDO_MAX_WAIT=20s
CODY_TOOLS_AIKIDO_UPSTREAM_TIMEOUT=30s
CODY_TOOLS_AIKIDO_RETRY_AFTER_CAP=90s
```

Rationale:

- 18/minute leaves headroom under Aikido's observed 20/minute cap.
- Burst `1` prevents a discovery run from consuming the whole minute
  immediately.
- `MAX_WAIT` keeps the proxy from hanging callers indefinitely.
- The controller, not `cody-tools`, owns waits longer than the per-request
  budget.

### 2. Return Agent-Friendly Local 429s

When local capacity is unavailable and the wait budget is exhausted,
`cody-tools` should return:

```text
HTTP 429
Retry-After: <seconds until next likely token or cooldown expiry>
```

Body:

```json
{
  "error": "cody Aikido rate limit reached",
  "retryAfterSeconds": 12
}
```

The Aikido controller can then sleep and retry the same request when its own
run budget allows.

### 3. Honor Upstream Retry-After

If Aikido returns `429`, `cody-tools` should parse `Retry-After` and apply a
shared in-memory cooldown before future upstream calls.

Behavior:

- Parse `Retry-After` as seconds or HTTP date.
- If absent or invalid, use a conservative fallback such as 60 seconds.
- Cap cooldown at `CODY_TOOLS_AIKIDO_RETRY_AFTER_CAP`.
- Retry the same request at most once when the request still has enough local
  wait budget.
- Otherwise return `429` with `Retry-After` so the controller can wait.

This avoids repeatedly hitting Aikido while the account is already throttled.

### 4. Classify Controller Callers

Add optional headers for Cody-owned clients:

```text
X-Cody-Aikido-Client: discovery | verification
X-Cody-Aikido-Budget-Seconds: <integer>
X-Cody-TaskSpawner: <name>
X-Cody-Aikido-Run-Date: <YYYY-MM-DD>
```

Initial use:

- Aikido discovery sends `X-Cody-Aikido-Client: discovery`.
- Future controller-owned verification can send `verification`.
- Agent sessions should not call Aikido, so they should not need an `agent`
  caller class in v1.
- If `X-Cody-Aikido-Budget-Seconds` is present, `cody-tools` uses the lower of
  that budget and the configured max wait.

### 5. Controller Waits And Sequential Processing

The Aikido controller should call `cody-tools` sequentially and handle
`Retry-After` as part of its normal loop.

Behavior:

- Fetch candidate export rows sequentially.
- Group rows by issue group.
- Process issue groups one at a time.
- For each group, fetch or assemble enough snapshot data.
- Create the AgentSession/AgentTurn for that group immediately.
- If `cody-tools` returns `429 + Retry-After`, sleep, retry the same request,
  then continue.
- Stop only when the remaining controller run budget cannot safely accommodate
  the wait.

Recommended controller bounds:

```text
Aikido discovery CronJob activeDeadlineSeconds=7200
CronJob concurrencyPolicy=Forbid
```

The normal path is to patiently drain the issue list in one daily run. Early
exit is a safety fallback for hard deadlines, not the intended control flow.

### 6. No Aikido Calls From Agent Sessions

Agent sessions should not receive Aikido proxy environment variables or prompt
instructions for Aikido API use.

Instead:

- the controller passes an Aikido snapshot into the first turn;
- in-day heartbeat turns continue remediation using GitHub/repo/build state;
- the next daily Aikido controller run verifies whether the issue still exists.

This prevents live agents from consuming the shared Aikido quota and keeps all
Aikido API access deterministic and observable.

## Runtime Flow

### Daily Discovery

1. Aikido CronJob starts.
2. Controller fetches repository/export data through `cody-tools`.
3. `cody-tools` applies rate limit and upstream timeout.
4. Controller groups issue rows by issue group.
5. Controller processes groups sequentially.
6. If `Retry-After` is returned, controller waits and retries.
7. After each group snapshot is ready, controller creates that group's daily
   AgentSession/AgentTurn.
8. Controller continues until the list is drained or the run deadline is near.

### In-Day Heartbeat

1. A cheap heartbeat source finds active Aikido sessions for the current run
   date.
2. It creates follow-up AgentTurns when no turn is queued/running.
3. Those turns do not call Aikido.
4. Agents continue remediation using GitHub/repo/build state.

### Next Daily Verification

1. Next daily Aikido controller run reads Aikido again.
2. If the issue still exists, it creates a new daily session scope.
3. If the issue no longer exists, it creates nothing for that group.

## Observability

Add structured logs in `cody-tools`:

```text
aikido.client_class
aikido.path
aikido.wait_ms
aikido.cooldown_until
aikido.status
aikido.retry_after_seconds
aikido.local_rate_limited
aikido.upstream_rate_limited
aikido.taskspawner
aikido.run_date
```

Add structured logs in the Aikido controller:

```text
aikido.discovery.api_calls
aikido.discovery.rows_fetched
aikido.discovery.groups_discovered
aikido.discovery.current_group_id
aikido.discovery.retry_after_wait_seconds
aikido.discovery.sessions_created
aikido.discovery.deadline_truncated
```

Metrics can follow once the behavior is proven.

## Implementation Plan

### Kelos

1. Add an Aikido limiter type in `cmd/cody-tools`.
2. Wire limiter config from environment.
3. Apply limiter and upstream timeout inside `forwardAikido` / `doAikido`.
4. Parse upstream `Retry-After` and maintain shared cooldown.
5. Return controlled local `429` responses with `Retry-After`.
6. Add controller headers to `internal/source/aikido.go`.
7. Change Aikido discovery to wait/retry on `Retry-After` until the controller
   run deadline is near.
8. Remove Aikido proxy env wiring from Aikido agent session pods.
9. Add tests for local wait, context cancellation, upstream `429`,
   `Retry-After`, controller waits, and no agent Aikido env.

### GitOps

1. Add `cody-tools` environment variables for rate-limit defaults.
2. Keep `cody-tools` at one replica for v1, or explicitly document that
   multiple replicas multiply the effective Aikido request rate.
3. Add/confirm Aikido discovery CronJob deadline and `concurrencyPolicy:
   Forbid`.
4. Roll out updated `cody-tools`, `kelos-spawner`, and chart changes.

### Skills

1. Remove instructions that tell Aikido security agents to call Aikido.
2. Tell agents to rely on the controller-provided Aikido snapshot.
3. Add prompt-level duplicate PR guidance:
   - search for existing matching Cody/security PRs before opening new ones;
   - include the Aikido issue group ID where useful;
   - avoid duplicate PRs for the same issue group and target branch.

## Tests

### Unit Tests

- `cody-tools` waits for a token before calling upstream.
- `cody-tools` returns local `429` when max wait expires.
- `cody-tools` respects request context cancellation while waiting.
- Upstream `429` with `Retry-After` creates cooldown.
- Upstream `429` retries at most once when budget allows.
- Aikido discovery sends Cody classification headers.
- Aikido controller sleeps and retries when `cody-tools` returns `Retry-After`.
- Aikido controller stops when the run deadline cannot safely accommodate the
  wait.
- Aikido agent pod config does not include Aikido proxy env vars.

### Manual Test

1. Deploy `cody-tools` with a very low temporary limit, for example 2/minute.
2. Trigger the Aikido TaskSpawner manually.
3. Confirm the controller creates one session, waits on `Retry-After`, then
   creates the next session.
4. Confirm agent session pods do not have Aikido proxy env vars.
5. Restore production defaults.

## Rollout

1. Merge Kelos changes.
2. Build and push updated images.
3. Package and push the Kelos chart if environment variables or chart defaults
   change.
4. Merge GitOps chart/image/config changes.
5. Merge Skills prompt update.
6. Trigger one manual Aikido run and monitor `cody-tools`, spawner logs, session
   pods, and Slack output.

## Open Questions

- Is 18/minute the right default, or should it be lower to leave room for humans
  and other automations using the same Aikido account?
- Are there multiple `cody-tools` replicas in any environment? If yes, v1
  in-memory limiting is not enough to enforce the global account cap.
- Should controller run deadline be 1 hour, 2 hours, or longer?
- Should the next daily controller run only verify by reading Aikido, or should
  it also request a re-scan when Aikido supports that safely?
