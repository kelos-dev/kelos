# Cody Personas And Handoffs

## Status

Draft spec based on Kelos `origin/main` at `16f8031` and the Alpheya Cody
manifests in `k8s-platform-gitops/origin/main` as of 2026-05-22.

This document is intentionally split into two parts:

1. What we can ship as GitOps-only Cody configuration using the Kelos features
   that already exist.
2. What needs a small Kelos change to make agent-to-agent handoff automatic
   instead of manual or statically predeclared.

## Summary

Cody should become a set of named, scoped personas instead of one broad
Slack-debug agent that self-classifies every request. A persona is a complete
runtime route: trigger, prompt template, AgentConfig stack, credentials,
service account, tool access, concurrency limits, and output contract.

The first release should not add Kelos API surface. It should add multiple
TaskSpawners and AgentConfigs in `k8s-platform-gitops`:

- `cody-ticket-creator`
- `cody-dev`
- `cody-pr-reviewer`
- `cody-debugger` or the existing stable debug route

Independent work is solved by invoking a specific persona through explicit
mentioned bang-prefixed Slack commands such as `@cody !ticket`,
`@cody !dev`, and `@cody !review`. Phase 1 has no router persona and no
GitHub triggers.
Automatic handoff needs one additional Kelos primitive: a controller path that
watches finished Tasks, reads structured `status.results`, and creates child
Tasks from a configured handoff template.

The recommended Kelos extension is `TaskSpawner.spec.handoffs[]`, not a new
general workflow engine. Cody handoff is event/result driven and should remain
close to the TaskSpawner that created the original Task.

## Current State

### Kelos primitives already available

Kelos already has most of the building blocks needed for personas:

| Capability | Existing behavior | Useful for personas |
| --- | --- | --- |
| `TaskSpawner` | Creates one Task per matching source event. | A persona can be one TaskSpawner. |
| `when.slack.triggers[]` | RE2 patterns matched after the bot mention is stripped. | Route mentioned commands such as `@cody !ticket ...`, `@cody !dev ...`, and `@cody !review ...`. |
| `when.slack.triggers[].mentionOptional` | Lets a trigger fire without an `@cody` mention. | Not used for Phase 1 personas; the `@cody` mention remains required. |
| `when.slack.excludePatterns[]` | Rejects messages that match any exclude pattern. | Reserve `!ticket`, `!dev`, and `!review` so the existing catch-all debugger does not also answer. |
| `when.slack.channels[]` | Restricts Slack channel IDs. | Not used in Phase 1; do not introduce a channel whitelist for personas. |
| `when.slack.allowedBotIDs[]` | Allows trusted Slack bot authors to trigger a spawner. | Lets a workflow bot invoke Cody without allowing all bot loops. |
| `when.githubWebhook` | Filters GitHub events/actions/body regex/labels/file patterns. | Future option for PR lifecycle triggers after Phase 1. |
| `when.githubPullRequests` | Polls PRs by state, labels, review state, file patterns, and comment policy. | Future option for review queues after Phase 1. |
| `commentPolicy` | Supports trigger comments plus `allowedUsers`, `allowedTeams`, and `minimumPermission`. | Safer GitHub command invocation than open comments. |
| `taskTemplate.agentConfigRefs[]` | Merges multiple AgentConfigs in order. | Compose shared Cody base, tool bundles, and persona-specific instructions. |
| `Task.spec.dependsOn` | Waits for named Tasks to succeed; fails downstream if dependency fails; detects cycles. | Supports statically declared pipelines. |
| Branch locking | Serializes Tasks with the same workspace and branch. | Protects dev -> review handoff on the same branch. |
| `status.outputs` / `status.results` | Captures key-value outputs between `---KELOS_OUTPUTS_START---` and `---KELOS_OUTPUTS_END---`. | Handoff can be driven by structured keys. |
| Dependency prompt templating | Downstream prompts can read dependency outputs through `.Deps`. | Static pipelines can pass branch, commit, PR, and other results. |
| `contextSources` | Fetches HTTP context before task creation and exposes `.Context.NAME`. | Can enrich a persona prompt with Jira, service-map, or policy context. |
| `metadata.labels` / `metadata.annotations` | Renders labels and annotations onto spawned Tasks. | Track persona, source thread, lineage, and cost domains. |
| `maxConcurrency` / `maxTotalTasks` / TTL | Limits active and lifetime task creation. | Cost and blast-radius control per persona. |

There is no first-class dynamic handoff today. A TaskSpawner has one
`taskTemplate` and creates one Task from one source event. Static pipelines are
possible when all Task names are known ahead of time, but a Slack-spawned Task
cannot currently decide that the next persona should run and have Kelos create
that child Task automatically.

### Current Cody deployment in GitOps

`k8s-platform-gitops/origin/main` currently defines Cody under
`non-prod/kelos`:

| File | Current role |
| --- | --- |
| `taskspawner-cody-debug.yaml` | Stable Slack route. Matches every normal `@cody ...` mention with `.+`, excluding `^!(alpha|exp)\b`. |
| `taskspawner-cody-debug-alpha.yaml` | Experimental Slack route for `!alpha` and `!exp`. |
| `agentconfig-cody-debugger.yaml` | Stable Cody operating manual, Context7 MCP, GitHub/Jira PR rules, and as of `origin/main`, read-only Aikido proxy guidance. |
| `agentconfig-cody-debug-alpha.yaml` | Experimental copy with extra Aikido context. |
| `agentconfig-cody-atlassian-mcp.yaml` | Reusable Atlassian MCP add-on pointed at `wgen4.atlassian.net`. |
| `deployment-cody-tools.yaml` | Internal `cody-tools` service for Atlassian MCP and Aikido proxy. |
| `rbac-cody-debugger.yaml` | Cluster-wide read-only diagnostics for the `cody-debugger` service account. |
| `rbac-cody-debugger-secrets.yaml` | Tenant-namespace Secret reads for debugging DB/Redis style issues. |

The stable and alpha TaskSpawners both run:

- agent type `codex`
- image `docker.io/alpheya/codex:main`
- credentials Secret `cody-codex-credentials`
- service account `cody-debugger`
- GitHub App environment variables from `cody-github-app`
- JWT signing variables from `cody-jwt-signing`
- label `cody.alpheya.com/tools-client: "true"`
- AgentConfigs composed with `cody-atlassian-mcp`

Important naming detail: `cody-github-app` and `cody-webhook-github` are
Kubernetes Secret names. The actual GitHub App currently wired through those
Secrets is the existing `cursor` GitHub App, ID `3429269`; the Key Vault keys
still use the `cursor-github-app-*` prefix.

This is a strong debugger route but too broad for all future Cody jobs. Ticket
creation and PR review do not need the same cluster Secret read scope or JWT
claims as service debugging.

### Existing output contract

The reference agent images call `/kelos/kelos-capture` after the agent exits.
It currently emits:

- `branch`
- `pr`
- `commit`
- `base-branch`
- `cost-usd`
- `input-tokens`
- `output-tokens`
- `response` as base64 user-visible text

The controller parses `key: value` lines into `Task.status.results`. This is
enough for static pipelines, but not enough for robust dynamic handoff because
the agent has no deterministic helper for emitting extra result keys such as
`handoff.target` or `handoff.prompt`.

## Goals

- Let users invoke a specific Cody persona directly.
- Let personas run independently when their trigger is independent.
- Let a persona hand off to another persona when the current run discovers
  that a different scoped agent should continue.
- Keep persona behavior scoped by trigger and AgentConfig in Phase 1, while
  reusing the existing Cody runtime permissions to avoid RBAC churn.
- Keep invocation and reporting UX predictable: one request should not accidentally
  spawn several personas unless that is explicitly configured.
- Preserve the existing Cody debugger route while new personas are introduced.
- Phase 1 must have no impact outside the newly reserved persona prefixes:
  normal `@cody ...` and existing `@cody !alpha` / `@cody !exp` requests
  continue to route exactly as they do today.
- Make every automatic handoff auditable through Task labels, owner references,
  status results, source reporting, and Kubernetes events.

## Non-Goals

- Do not build a general Airflow/Temporal-style workflow engine in Kelos.
- Do not let agents create arbitrary Tasks by talking directly to the
  Kubernetes API.
- Do not rely on prompt-only safety for persona permissions.
- Do not redesign Cody RBAC in Phase 1; persona-specific service accounts are
  a later hardening step.
- Do not require a new Slack app or Slack native slash commands for the first
  release.
- Do not replace human code review. Cody dev output remains PR-based.
- Do not introduce a router persona in Phase 1.
- Do not introduce GitHub webhook, GitHub PR polling, GitHub comment, or GitHub
  label triggers in Phase 1.
- Do not modify the existing `cody-debug-slack` matching behavior except to
  reserve `!ticket`, `!dev`, and `!review` for the new persona routes.

## Persona Model

A Cody persona is not just an `AgentConfig`. It is the full operational route.

| Layer | Persona-specific? | Example |
| --- | --- | --- |
| Trigger | Yes | Phase 1 Slack commands such as `@cody !ticket ...`; future GitHub PR comments or labels can be added later. |
| Prompt template | Yes | "Create an ALPM ticket from this Slack thread." |
| AgentConfig stack | Yes | `cody-base`, `cody-ticket-creator`, `cody-atlassian-mcp`. |
| Credentials | Usually shared for model, scoped for tools | Codex OAuth shared, Jira token via `cody-tools`. |
| Service account | Later | Phase 1 reuses `cody-debugger`; persona-specific service accounts come later. |
| Pod env | Yes | Dev may need GitHub App env; ticket creator may not. |
| Metadata | Yes | `cody.alpheya.com/persona: ticket-creator`. |
| Concurrency | Yes | Reviewer can run higher concurrency than debugger. |
| Handoff policy | Yes | Ticket creator may hand off to dev; reviewer should not loop to itself. |

### Initial personas

| Persona | Invocation | Primary job | Tools | Allowed handoff |
| --- | --- | --- | --- | --- |
| `ticket-creator` | `@cody !ticket ...`, Jira/GitHub issue trigger later | Create or update Jira with concise acceptance criteria and evidence. | Atlassian MCP, Slack thread context, optional GitHub read. | `dev` when the ticket is actionable and the user asked for implementation. |
| `dev` | `@cody !dev ...` or handoff from ticket/debug | Implement small code or GitOps changes and open a PR. | GitHub App, repo workspace, optional read-only cluster for debugging. | `pr-reviewer` when a PR was opened. |
| `pr-reviewer` | `@cody !review <PR URL>` or handoff from dev | Review a PR for correctness, tests, security, and Cody-specific risks. | GitHub read, repo workspace, optional changed-file context. | `dev` only for explicitly requested fix follow-up, and with loop guard. |
| `debugger` | existing normal `@cody ...` route and existing `@cody !alpha` / `@cody !exp` route | Diagnose non-prod service/platform issues and open PRs only when evidence-backed. | Current debug toolkit, read-only cluster, GitHub App, Atlassian MCP, Aikido proxy. | `ticket-creator` for backlog-only work, `dev` for a small confirmed fix. |

The key design choice is that most persona selection should happen before the
agent runs, through explicit TaskSpawner triggers. Phase 1 intentionally has
no router persona: ambiguous natural-language requests continue to use the
existing Cody debugger route.

## Phase 1: GitOps-Only Independent Personas

Phase 1 should require no Kelos code changes and should preserve existing Cody
behavior except for the newly reserved persona prefixes.

The Phase 1 compatibility rule is strict:

- Edit `taskspawner-cody-debug.yaml` only to add `!ticket`, `!dev`, and
  `!review` to `excludePatterns`.
- Do not otherwise change the stable catch-all `@cody ...` trigger.
- Do not change the existing `@cody !alpha` / `@cody !exp` route.
- Persona Slack commands require the Cody mention:
  - `@cody !ticket ...`
  - `@cody !dev ...`
  - `@cody !review ...`
- The persona TaskSpawners should not set `mentionOptional: true`.
- Do not add Slack `channels[]` in Phase 1; personas should follow the same
  channel reachability model as current Cody.
- Do not add GitHub webhook, GitHub PR polling, GitHub comment, or GitHub label
  triggers in Phase 1.

This intentionally changes routing for `@cody !ticket`, `@cody !dev`, and
`@cody !review`: those prefixes become reserved persona entrypoints instead
of falling through to the stable debugger. Everything else continues through
the current Cody routes.

### AgentConfig split

Create a small shared base plus narrow persona configs:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: AgentConfig
metadata:
  name: cody-base
  namespace: kelos-system
spec:
  agentsMD: |
    # Cody base

    You are Cody, an Alpheya internal agent. Stay scoped to the
    requested persona. Prefer evidence over guesses. Do not expose
    secrets. Use PRs for mutations. Reply through the source that invoked
    the task.
```

Then add one config per persona:

- `cody-ticket-creator`
- `cody-dev`
- `cody-pr-reviewer`
- keep `cody-debugger` as-is initially

Continue keeping tool bundles separate:

- `cody-atlassian-mcp`
- future `cody-github-tools`
- future `cody-aikido-tools`

Use `agentConfigRefs` to compose:

```yaml
agentConfigRefs:
  - name: cody-base
  - name: cody-ticket-creator
  - name: cody-atlassian-mcp
```

This matches the current Kelos merge behavior: `agentsMD` is concatenated,
plugins and skills are appended, and MCP servers are merged by name with later
entries winning.

### Slack routes

Add specific Slack TaskSpawners for mentioned bang commands. Kelos strips the
leading bot mention before trigger matching, so the regex still starts with
`^!ticket` / `^!dev` / `^!review`.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-ticket-slack
  namespace: kelos-system
spec:
  maxConcurrency: 2
  when:
    slack:
      triggers:
        - pattern: '^!ticket\b'
  taskTemplate:
    type: codex
    credentials:
      type: oauth
      secretRef:
        name: cody-codex-credentials
    image: docker.io/alpheya/codex:main
    agentConfigRefs:
      - name: cody-base
      - name: cody-ticket-creator
      - name: cody-atlassian-mcp
    promptTemplate: |
      Slack request for Cody ticket creator.

      Remove the leading `!ticket` routing word before
      interpreting the request.

      Slack message:
        {{.Body}}

      Slack thread: {{.URL}}
    metadata:
      labels:
        cody.alpheya.com/persona: ticket-creator
        cody.alpheya.com/source: slack
    ttlSecondsAfterFinished: 3600
```

For a dev route:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-dev-slack
  namespace: kelos-system
spec:
  maxConcurrency: 1
  when:
    slack:
      triggers:
        - pattern: '^!dev\b'
  taskTemplate:
    type: codex
    credentials:
      type: oauth
      secretRef:
        name: cody-codex-credentials
    image: docker.io/alpheya/codex:main
    agentConfigRefs:
      - name: cody-base
      - name: cody-dev
      - name: cody-atlassian-mcp
    promptTemplate: |
      Slack request for Cody dev.

      Remove the leading `!dev` routing word before interpreting the
      request.

      Slack message:
        {{.Body}}

      Slack thread: {{.URL}}
    metadata:
      labels:
        cody.alpheya.com/persona: dev
        cody.alpheya.com/source: slack
    podOverrides:
      labels:
        cody.alpheya.com/tools-client: "true"
      serviceAccountName: cody-debugger
      env:
        # GitHub App env needed for clone/push/PR.
        - name: GITHUB_APP_CLIENT_ID
          valueFrom:
            secretKeyRef:
              name: cody-github-app
              key: GITHUB_APP_CLIENT_ID
        - name: GITHUB_APP_INSTALLATION_ID
          valueFrom:
            secretKeyRef:
              name: cody-github-app
              key: GITHUB_APP_INSTALLATION_ID
        - name: GITHUB_APP_PRIVATE_KEY
          valueFrom:
            secretKeyRef:
              name: cody-github-app
              key: GITHUB_APP_PRIVATE_KEY
```

For a PR reviewer in Phase 1, use the same mentioned command pattern and
require the Slack request to include a PR URL:

```yaml
when:
  slack:
    triggers:
      - pattern: '^!review\b'
```

Add `!ticket`, `!dev`, and `!review` to the existing
`cody-debug-slack.excludePatterns` so a single mentioned command produces
only one persona task:

```yaml
excludePatterns:
  - '^!(alpha|exp)\b'
  - '^!(ticket|dev|review)\b'
```

### Phase 1 runtime permissions

Phase 1 should reuse the existing `cody-debugger` service account and pod
environment pattern for all new Slack personas. This keeps the first GitOps PR
focused on routing and persona prompts, and avoids introducing new RBAC while
the UX is still being validated.

This is an explicit MVP tradeoff. The current debugger route is intentionally
powerful for non-prod diagnosis, including broad read-only cluster access and
tenant Secret reads. Persona-specific service accounts remain the right later
hardening step once the routes prove useful.

| Persona | Service account | GitHub App env | JWT signing env | Tenant Secret reads | Cody tools |
| --- | --- | --- | --- | --- | --- |
| ticket creator | existing `cody-debugger` for Phase 1 | Yes, inherited from current Cody pod env | Yes, inherited from current Cody pod env | Yes, inherited from current Cody SA | Atlassian MCP |
| dev | existing `cody-debugger` for Phase 1 | Yes | Yes, inherited from current Cody pod env | Yes, inherited from current Cody SA | Atlassian MCP, GitHub |
| PR reviewer | existing `cody-debugger` for Phase 1 | Yes | Yes, inherited from current Cody pod env | Yes, inherited from current Cody SA | GitHub |
| debugger | existing `cody-debugger` SA | Yes | Yes, current scope | Yes, tenant only | Atlassian MCP, Aikido, cluster tools |

When Phase 1 is stable, split these into narrower service accounts and
TaskSpawner-specific pod env. The likely final shape is no tenant Secret reads
for ticket creator or PR reviewer, and a narrower dev role unless runtime
debugging is explicitly part of that route.

### Phase 1 handoff behavior

Phase 1 does not do automatic agent-to-agent handoff. It supports:

- Direct invocation of independent personas.
- Human-mediated handoff, where one persona replies with "invoke `@cody !dev
  ALPM-123 ...`" or creates the Jira ticket and includes the next command.
- Static pipelines only when a human or CI creates multiple named `Task`
  resources with `dependsOn`, like the existing `examples/07-task-pipeline`.

This is still valuable because it reduces the largest current problem: every
request currently enters through the same debugger prompt and service account.

## Phase 2: First-Class Dynamic Handoff

Phase 2 adds Kelos support for automatic handoff while keeping the API small.

### Handoff contract

The parent agent emits structured result keys:

| Result key | Required | Meaning |
| --- | --- | --- |
| `handoff.target` | Yes | Persona name to run next, for example `dev` or `pr-reviewer`. |
| `handoff.prompt` | Yes | Prompt payload for the next persona. Use base64 if multi-line. |
| `handoff.reason` | Yes | Short audit reason. |
| `handoff.mode` | No | `continue`, `parallel`, or `manual-review`; default `continue`. |
| `handoff.branch` | No | Branch the child should use. Defaults to parent result `branch` if present. |
| `handoff.workspace` | No | Workspace override name if the child should use a different workspace. |
| `handoff.repo` | No | Repository hint for logging/reporting. |
| `handoff.source-url` | No | Jira, Slack, GitHub, or alert URL the child should cite. |
| `handoff.max-depth` | No | Optional agent-requested cap; controller still enforces its own cap. |

Do not ask the model to format these in normal prose and hope the controller
can parse them. Add a deterministic helper to the agent image:

```bash
kelos-handoff emit \
  --target dev \
  --reason "ALPM-123 is actionable and user asked to implement" \
  --prompt-file /tmp/handoff-prompt.md \
  --branch cody/alpm-123-fix-advisor-route
```

The helper writes key-value lines to a known file such as
`/tmp/kelos-extra-outputs`. Then `/kelos/kelos-capture` appends those lines
between the existing output markers. This keeps the controller's result parser
unchanged and makes handoff auditable in `Task.status.results`.

As a fallback, `kelos-capture` may later parse a fenced `kelos-handoff` block
from the final agent response, but helper-first is safer because it avoids
model prose ambiguity.

### Proposed API

Add an optional `handoffs` field to `TaskSpawnerSpec`:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-ticket-slack
spec:
  when:
    slack:
      triggers:
        - pattern: '^!ticket\b'
  taskTemplate:
    # parent persona template
  handoffs:
    - name: ticket-to-dev
      targetPersona: dev
      terminalPhases:
        - Succeeded
      when:
        result:
          key: handoff.target
          operator: In
          values:
            - dev
      inherit:
        labels:
          - cody.alpheya.com/source
        annotations:
          - kelos.dev/slack-channel
          - kelos.dev/slack-thread
        branchFromResult: handoff.branch
        appendParentDependsOn: true
      taskTemplate:
        type: codex
        credentials:
          type: oauth
          secretRef:
            name: cody-codex-credentials
        image: docker.io/alpheya/codex:main
        agentConfigRefs:
          - name: cody-base
          - name: cody-dev
          - name: cody-atlassian-mcp
        promptTemplate: |
          Cody ticket creator handed this to Cody dev.

          Parent task: {{.Upstream.Name}}
          Reason: {{index .Upstream.Results "handoff.reason"}}

          Handoff prompt:
          {{b64dec (index .Upstream.Results "handoff.prompt")}}
        metadata:
          labels:
            cody.alpheya.com/persona: dev
            cody.alpheya.com/handoff: ticket-to-dev
```

The final field names can be adjusted during implementation, but the API
should preserve these properties:

- Additive and backward compatible.
- Handoff rules live beside the source TaskSpawner.
- Child Tasks use normal `TaskTemplate`.
- Matching is based on parent Task phase and `status.results`.
- The controller, not the agent, creates the child Task.
- The parent Task is added to child `dependsOn` when requested so `.Deps`
  remains available and lineage is explicit.

### Template variables for handoff children

Handoff child templates need a new variable set:

| Variable | Meaning |
| --- | --- |
| `.Upstream.Name` | Parent Task name. |
| `.Upstream.Namespace` | Parent namespace. |
| `.Upstream.Phase` | Parent terminal phase. |
| `.Upstream.Results` | Parent `status.results`. |
| `.Upstream.Outputs` | Parent `status.outputs`. |
| `.Upstream.Labels` | Parent labels. |
| `.Upstream.Annotations` | Parent annotations. |
| `.Lineage.Root` | First Task in the chain. |
| `.Lineage.Depth` | Number of handoffs from root to this child. |

If template functions are added, keep them tiny and deterministic:

- `b64dec`
- `default`
- `trunc`

Do not add arbitrary scripting to templates.

### Controller behavior

Implement handoff handling in the controller path that already watches Task
status changes:

1. Watch Tasks that reach a terminal phase.
2. Ignore Tasks without `kelos.dev/taskspawner`.
3. Load the owning TaskSpawner.
4. Evaluate `spec.handoffs[]` in order.
5. Enforce loop guards:
   - max depth, default `3`
   - no self-handoff unless `allowSelf: true`
   - no duplicate child for the same parent plus handoff name
   - optional `allowMultiple: false` default
6. Render the child TaskTemplate with `.Upstream` and `.Lineage`.
7. Name the child deterministically:
   - `<parent-name>-<handoff-name>-<hash>`
   - keep under 63 characters
8. Add labels:
   - `kelos.dev/taskspawner: <spawner>`
   - `kelos.dev/parent-task: <parent>`
   - `kelos.dev/handoff: <handoff-name>`
   - `kelos.dev/lineage-root: <root>`
   - `cody.alpheya.com/persona: <targetPersona>`
9. Set owner reference to the TaskSpawner.
10. Create the child Task with create-if-not-exists semantics.

The child should count toward the TaskSpawner's active task count. That gives
`maxConcurrency` and `maxTotalTasks` one consistent meaning for both source
tasks and handoff tasks.

### Reporting behavior

Handoff should be visible to users:

- Slack source: post or update the originating thread with "handed off to
  `<persona>`" and the child Task name. Final child response should land in the
  same thread when Slack annotations are inherited.
- GitHub source: if reporting is enabled, add a concise status comment or check
  run update that says which persona took over.
- Kubernetes: record Events on the parent and child Tasks.
- CLI: `kelos get task -d` should show parent/handoff/lineage labels.

The source-specific annotations are not standardized enough today to rely on
blind copying forever. The Phase 2 implementation should document the reserved
annotation keys used by Slack and GitHub reporting before making inheritance
official.

## Why Not A New Workflow CRD First?

A new `TaskWorkflow` or `AgentWorkflow` CRD would make sense if we needed:

- predeclared DAGs
- fan-out/fan-in
- retries per stage
- conditional stage expressions
- long-running workflow state
- manual approval gates

Cody handoff needs less than that. The common case is:

1. A source event starts a persona.
2. That persona emits a small structured handoff request.
3. Kelos creates one next Task with a different AgentConfig and possibly a
   different service account.

`TaskSpawner.spec.handoffs[]` keeps the feature close to the existing source
and reporting model, and avoids creating a second abstraction before the Cody
use case proves it needs one.

Static pipelines with `Task.dependsOn` should remain the answer for planned,
predeclared workflows.

## GitOps Implementation Plan

### Step 1: Reserve the new persona prefixes

Edit `taskspawner-cody-debug.yaml` only to reserve the new persona prefixes.
The stable `@cody ...` route and the existing alpha/experimental prefixes must
continue to behave exactly as they do today for every non-reserved prefix.

The existing stable route should keep excluding alpha/experimental prefixes and
also exclude the new persona prefixes:

```yaml
excludePatterns:
  - '^!(alpha|exp)\b'
  - '^!(ticket|dev|review)\b'
```

The new persona TaskSpawners must use mentioned bang-prefixed Slack commands
and must not set `mentionOptional: true`.

### Step 2: Add shared base and persona AgentConfigs

Add:

- `agentconfig-cody-base.yaml`
- `agentconfig-cody-ticket-creator.yaml`
- `agentconfig-cody-dev.yaml`
- `agentconfig-cody-pr-reviewer.yaml`

Keep `agentconfig-cody-atlassian-mcp.yaml` reusable.

Do not refactor `agentconfig-cody-debugger.yaml` in the same PR unless the
behavior is being changed deliberately. The stable debugger is already active.

### Step 3: Add persona TaskSpawners

Add:

- `taskspawner-cody-ticket.yaml`
- `taskspawner-cody-dev.yaml`
- `taskspawner-cody-pr-reviewer-slack.yaml`

Start with Slack for ticket, dev, and review. `@cody !review` should require a
PR URL in the Slack message. Do not add GitHub webhook, GitHub PR polling,
GitHub comment, or GitHub label triggers in Phase 1.

### Step 4: Reuse the existing service account

Do not add new RBAC or service accounts in Phase 1. Use
`podOverrides.serviceAccountName: cody-debugger` and the existing Cody pod env
shape for the new Slack personas. Track persona-specific service accounts as a
follow-up hardening task.

### Step 5: Add kustomization entries

Add the new manifests to `non-prod/kelos/kustomization.yaml` after the current
Cody resources. Keep ordering readable: secrets/tools, RBAC, AgentConfigs,
TaskSpawners.

### Step 6: Canary

Start with:

- `maxConcurrency: 1` for `cody-dev`
- `maxConcurrency: 2` for ticket creator
- `maxConcurrency: 2` or `4` for reviewer, depending on check-run cost
- no bot `allowedBotIDs` unless a trusted workflow bot is explicitly used

## Kelos Implementation Plan

### Step 1: Extra output helper

Add `kelos-handoff` or a more generic `kelos-output` helper to agent images:

```bash
kelos-output set handoff.target dev
kelos-output set handoff.reason "ticket is ready"
kelos-output set-file handoff.prompt /tmp/handoff.md --base64
```

Then update `internal/capture/capture.go` to append `/tmp/kelos-extra-outputs`
lines after built-in capture outputs.

Tests:

- helper writes valid `key: value` lines
- multi-line prompt is base64 encoded
- capture includes extra outputs
- invalid keys are rejected

### Step 2: API structs and CRD

Add `TaskSpawnerSpec.Handoffs []TaskHandoff`.

Core structs:

```go
type TaskHandoff struct {
    Name string `json:"name"`
    TargetPersona string `json:"targetPersona,omitempty"`
    TerminalPhases []TaskPhase `json:"terminalPhases,omitempty"`
    When TaskHandoffWhen `json:"when,omitempty"`
    Inherit TaskHandoffInherit `json:"inherit,omitempty"`
    TaskTemplate TaskTemplate `json:"taskTemplate"`
}
```

Keep CEL validation focused:

- names are DNS-label compatible
- `taskTemplate` is required
- `terminalPhases` only `Succeeded` or `Failed`
- result operator is one of `Exists`, `Equals`, `In`, `NotIn`

### Step 3: Controller

Add a handoff reconciler path after a Task reaches terminal status.

Tests:

- creates child on matching result
- does not create child before parent terminal phase
- no duplicate child on repeated reconciles
- max depth stops loops
- self-handoff is blocked by default
- child inherits selected labels/annotations
- child appends parent to `dependsOn` when configured
- child uses deterministic name under 63 chars
- no child on non-matching result

### Step 4: Prompt rendering

Extend taskbuilder or add a handoff-specific builder to render with
`.Upstream` and `.Lineage`.

Do not change existing TaskSpawner source template variables for normal source
events.

### Step 5: Reporting

Teach Slack/GitHub reporting to recognize handoff children by labels and
thread/source annotations.

Tests:

- parent success with handoff reports handoff start
- child final response reports to same Slack thread
- GitHub check/comment points at child Task when child is running

## Safety And Governance

### Loop prevention

Default policy:

- max lineage depth `3`
- no self-handoff
- one child per parent per handoff rule
- `allowMultiple: false`
- no handoff on failed parent unless explicitly configured

### Authorization

Slack has channel and bot allowlists but no per-user authorization in the
TaskSpawner API today. For future GitHub commands, use GitHub `commentPolicy`
for review/dev commands that need user authorization. Phase 1 must not add
Slack channel allowlists; it should use the same channel reachability model as
current Cody.

### Secrets

Phase 1 intentionally reuses the current Cody TaskSpawner pod env shape. This
means the new personas inherit the same GitHub App and JWT signing env as the
debugger route.

Later hardening should split secrets by persona:

- ticket creator should not receive JWT signing keys
- PR reviewer should not receive tenant Secret read RBAC
- debugger keeps current credentials until a separate least-privilege cleanup

### Cost controls

Every persona TaskSpawner should set:

- `maxConcurrency`
- `ttlSecondsAfterFinished`
- `podOverrides.resources`
- `podOverrides.activeDeadlineSeconds` for long-running personas

For Phase 2 handoff, child Tasks should count against the parent TaskSpawner's
limits.

### Audit

Every Task should have:

```yaml
metadata:
  labels:
    cody.alpheya.com/persona: <persona>
    cody.alpheya.com/source: <slack|github|jira|handoff>
```

Handoff children should additionally have:

```yaml
metadata:
  labels:
    kelos.dev/parent-task: <parent>
    kelos.dev/handoff: <handoff-name>
    kelos.dev/lineage-root: <root>
```

## Open Questions

- Should the ticket creator create only ALPM issues, or should it support
  project selection?
- Should handoff prompts be base64-only to avoid line-break issues in
  `status.results`?
- Should Kelos add Slack user allowlists before exposing `@cody !dev` outside
  platform-owned channels?

## Recommended Next Step

Ship Phase 1 first:

1. Add `cody-base`, `cody-ticket-creator`, `cody-dev`, and
   `cody-pr-reviewer` AgentConfigs.
2. Add Slack TaskSpawners for `@cody !ticket`, `@cody !dev`, and
   `@cody !review` commands with no `channels[]` and no `mentionOptional`.
3. Keep GitHub webhook, GitHub PR polling, GitHub comment, and GitHub label
   triggers out of Phase 1.
4. Update `cody-debug-slack.excludePatterns` only to reserve `!ticket`,
   `!dev`, and `!review`; do not change `cody-debug-alpha-slack`.
5. Reuse `serviceAccountName: cody-debugger` and the existing Cody pod env for
   the new Phase 1 persona TaskSpawners.
6. Keep handoff manual in the first GitOps PR.

After the personas prove useful, implement Phase 2 handoff in Kelos with
`TaskSpawner.spec.handoffs[]` and the deterministic extra-output helper.
