# Cody Aikido Discovery Simplification

Status: draft
Date: 2026-06-08

## Context

The Aikido security workflow should create one long-running Cody session per selected Aikido issue group. Each session then babysits remediation across PRs, builds, consumer bumps, heartbeats, and final Aikido verification.

The current discovery path does more than it needs to:

- It resolves Aikido code repositories for a branch.
- It fetches `/issues/export` rows for each repository/status/issue type combination.
- It groups export rows by issue group ID.
- It then fetches `/issues/groups/{groupID}` for every discovered issue group before returning work items.

That last enrichment step is expensive and was part of the path that hit Aikido's 20 calls/minute limit. It also mixes two concerns:

- deterministic discovery: decide which issue groups need sessions
- investigation/remediation: understand and fix a specific issue group

Discovery should be a cheap queue-builder. The agent session should do the deeper Aikido reads.

## Goals

- Make Aikido discovery deterministic, cheap, and bounded.
- Use `/issues/export` rows as the primary discovery input.
- Remove mandatory per-group detail calls from discovery.
- Spawn or heartbeat one AgentSession per selected issue group.
- Let the session agent fetch detailed Aikido issue/group data during its own turns.
- Keep enough metadata in the WorkItem for prompt rendering, dedupe, Slack naming, and session scoping.

## Non-Goals

- Do not change the Aikido session lifecycle.
- Do not change Slack session behavior.
- Do not add Aikido write APIs.
- Do not build deep RCA or fix planning into discovery.
- Do not require an Aikido CRD redesign for v1.

## Desired Shape

Discovery should only answer:

```text
Which Aikido issue groups should Cody start or continue sessions for?
```

It should not answer:

```text
What is the root cause?
Which repos need PRs?
Which package bumps are required?
Is the fix verified?
```

Those questions belong to the live Cody session because they may require multiple turns, GitHub checks, image rebuilds, consumer bumps, and Aikido re-scans.

## Proposed Discovery Flow

### 1. Resolve the configured branch and repositories

Use the existing TaskSpawner fields:

```yaml
when:
  aikido:
    branch: main
    repositories:
      - platform-services
      - portfolio-service
    statuses:
      - open
    severities:
      - critical
      - high
    issueTypes:
      - open_source
```

Behavior:

- `branch` defaults to `main` when issue export is used.
- `repositories` is an allowlist of exact Aikido code repository names.
- `statuses` defaults to `open`.
- `severities` filters rows locally after export.
- `issueTypes` is passed to Aikido export calls when configured.

### 2. Fetch export rows only

For each selected code repository/status/issue type, call `/issues/export`.

Discovery should parse and retain:

- issue group ID
- issue ID
- title or summary
- severity
- status
- issue type
- affected package, if present
- CVE IDs, if present
- repository/code repository names
- Aikido URL, if present
- branch

No `/issues/groups/{groupID}` calls should be required for the normal path.

### 3. Group rows by issue group ID

Group export rows by `issueGroupId`.

For each group:

- choose a stable title from the first non-empty title/summary
- choose highest severity from grouped rows
- aggregate affected packages
- aggregate CVE IDs
- aggregate repositories
- preserve a small bounded sample of row-level evidence for the first prompt

The WorkItem body should explicitly say that discovery used export data and that the agent should fetch detailed group data before making fixes.

### 4. Apply deterministic caps

Discovery should have caps so one daily run cannot exhaust Aikido or create too many sessions.

Recommended v1 defaults:

```text
CODY_AIKIDO_DISCOVERY_MAX_API_CALLS=15
CODY_AIKIDO_DISCOVERY_MAX_GROUPS=10
CODY_AIKIDO_DISCOVERY_MAX_ROWS_PER_GROUP=10
CODY_AIKIDO_DISCOVERY_TIMEOUT=3m
```

Behavior:

- Stop optional paging when `MAX_API_CALLS` is reached.
- Sort selected issue groups deterministically before applying `MAX_GROUPS`.
- Prefer higher severity first, then older/newer issue ordering if available, then numeric group ID.
- Keep only a bounded evidence sample in the prompt body.

### 5. Deduplicate by AgentSession scope

Use the existing Aikido session scope template:

```text
aikido/{{.Branch}}/{{ index .Metadata "aikido.kelos.dev/issue-group-id" }}
```

Spawner behavior remains:

- If no active session exists for the scope, create an AgentSession and first AgentTurn.
- If an active session exists, enqueue a heartbeat turn, respecting `maxQueuedTurns`.
- If the session is completed/expired and the issue group is still open, create or roll into the next session according to existing session lifecycle rules.

Discovery itself should not attempt deeper dedupe by PR title, package name, or repository. The session agent owns remediation state.

## WorkItem Contract

Discovery should return enough information for a useful first turn without requiring group-detail enrichment.

Minimum WorkItem fields:

```text
ID: issue group ID
Kind: aikido
Title: concise issue group title from export row
URL: Aikido group URL when available
Branch: configured branch
Metadata:
  aikido.kelos.dev/issue-group-id
  aikido.kelos.dev/branch
  aikido.kelos.dev/severity
  aikido.kelos.dev/status
  aikido.kelos.dev/issue-type
  aikido.kelos.dev/repositories
  aikido.kelos.dev/code-repositories
  aikido.kelos.dev/affected-packages
  aikido.kelos.dev/cve-ids
```

Body should contain:

```text
Aikido issue group ID: <id>
Branch: <branch>
Severity: <severity>
Status: <status>
Issue type: <issue type>
Affected packages: <bounded list>
Repositories: <bounded list>
Discovery source: Aikido issue export

Discovery only selected this issue group for remediation. Before opening fixes,
fetch the latest Aikido group/details through the Cody Aikido proxy and confirm
that the issue is still open on the target branch.
```

## Agent Responsibilities

The session agent should:

- Fetch current Aikido group/detail data at the start of a turn.
- Confirm the issue still exists for the target branch before modifying repos.
- Determine which repos need fixes.
- Check existing Cody/security PRs before opening new ones.
- Open PRs and track checks/builds.
- Wait for package/image/build propagation when required.
- Bump consumers when needed.
- Re-trigger or request an Aikido scan when supported.
- Verify closure through Aikido before marking the session done.

This keeps the complex reasoning and long-running state inside the AgentSession, where heartbeats already exist.

## Rate Limiting Interaction

This spec complements `2026-06-08-cody-aikido-rate-limiting.md`.

Expected impact:

- Discovery uses fewer API calls because it no longer fetches every issue group detail.
- The shared `cody-tools` limiter still protects all Aikido calls.
- If discovery receives local/upstream `429`, it can return partial groups from already fetched export rows.
- Live agents still respect `429` and defer deeper checks to a future heartbeat when needed.

The rate limiter is still required because live sessions and discovery share the same Aikido account quota.

## Implementation Plan

### Kelos

1. Change `internal/source/aikido.go` so `discoverIssueExport` builds WorkItems directly from grouped export rows.
2. Remove mandatory `fetchIssueGroup` calls from the issue-export discovery path.
3. Keep legacy `/issues/groups` discovery path only for configurations that do not use branch/issue export.
4. Add deterministic sorting and `MAX_GROUPS` cap for issue-export discovery.
5. Add bounded row samples per group to the prompt body.
6. Add tests proving issue-export discovery succeeds without `/issues/groups/{id}`.
7. Add tests for grouping multiple rows into one WorkItem.
8. Add tests for severity filtering, repository aggregation, package/CVE aggregation, and deterministic ordering.
9. Add tests for group caps and bounded row samples.

### Skills

1. Update the Aikido security prompt to treat discovery metadata as a starting point only.
2. Instruct the agent to fetch current Aikido details before edits.
3. Instruct the agent not to retry Aikido aggressively on `429`; let heartbeat continue.

### GitOps

No GitOps change is required for the simplification itself unless new caps are configured as environment variables.

If caps are environment variables, add them to the Kelos deployment values alongside the rate-limit settings.

## Tests

### Unit Tests

- Issue-export discovery does not call `/issues/groups/{id}`.
- Multiple export rows for the same group produce one WorkItem.
- The WorkItem has stable metadata for session scope rendering.
- Severity filters still apply.
- Repository filters still apply.
- Issue type filters still apply.
- Groups are sorted deterministically before capping.
- Prompt body includes a bounded evidence sample.
- Missing optional export fields do not fail discovery when the group ID is present.
- Missing group ID still fails that row or discovery with a clear error, depending on chosen strictness.

### Manual Test

1. Run the Aikido TaskSpawner against a fake Aikido proxy that serves export rows but fails `/issues/groups/{id}`.
2. Confirm discovery still creates AgentSessions.
3. Confirm first agent turns fetch current Aikido detail themselves.
4. Trigger with a low Aikido proxy rate limit and confirm discovery no longer burns the whole quota on enrichment.

## Migration Notes

- Existing active sessions keep their current scope because the scope remains based on branch and issue group ID.
- Newly created sessions may have slightly less detailed first-turn prompts, but the agent will fetch fresh details at turn start.
- This change should reduce spawner failures and make daily discovery more predictable.

## Open Questions

- Should `MAX_GROUPS` be an environment variable or a TaskSpawner CRD field?
- Should discovery skip rows missing group ID, or fail the run so we notice unexpected Aikido response shape changes?
- Should we keep an optional debug flag to re-enable group-detail enrichment during development?
- Should issue-export grouping prefer newest issue rows, oldest issue rows, or highest severity rows in the bounded prompt sample?
