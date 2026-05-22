# Cody Personas Phase 2 Handoff Implementation Spec

## Status

Implementation-ready Phase 2 spec.

Scope:

- Kelos code changes for result-driven Task handoffs.
- Cody GitOps follow-up after the Kelos image and CRDs are released.
- Slack persona handoffs only.

Out of scope:

- No router persona.
- No GitHub triggers.
- No channel-level Slack whitelist.
- No new Cody service accounts or RBAC split.

This spec builds on:

- `specs/2026-05-22-12-00-cody-personas-and-handoffs.md`
- `specs/2026-05-22-13-06-cody-personas-phase-1-implementation.md`

Phase 1 is assumed to be complete: Cody has explicit Slack persona entrypoints
for `@cody !ticket`, `@cody !dev`, and `@cody !review`, while normal
`@cody ...` debugger behavior remains unchanged.

## Summary

Phase 2 adds automatic Slack-thread handoffs between Cody personas.

A persona Task can finish with structured results such as:

```text
handoff.target: dev
handoff.reason: Ticket ALPM-123 is ready for implementation.
handoff.prompt: Implement ALPM-123 and open a PR.
ticket: ALPM-123
```

Kelos then evaluates handoff rules configured on the parent `TaskSpawner`. If a
rule matches the parent Task phase and results, Kelos creates exactly one child
Task from a handoff `taskTemplate`.

The child Task:

- uses the target persona AgentConfig stack
- reports back to the same Slack thread
- carries lineage labels and annotations
- depends on the parent Task by default
- is subject to the same `TaskSpawner` task limits

The intent is to support practical Cody flows such as:

- `@cody !ticket ...` creates or updates a Jira ticket, then hands off to dev
  when implementation is requested.
- `@cody !dev ALPM-123 ...` opens a PR, then hands off to PR review when a PR
  URL is emitted.

Reviewer-to-dev auto-fix loops are not part of this phase. Reviewers may still
tell the user to invoke `@cody !dev ...` manually.

## Design Principles

- Preserve Phase 1 behavior unless a `TaskSpawner` explicitly configures
  `spec.handoffs`.
- Keep handoff policy declarative and near the source persona route.
- Let agents request handoff only through structured task outputs, not through
  Kubernetes API access.
- Reuse existing Task primitives: `taskTemplate`, metadata templating,
  `dependsOn`, `status.results`, Slack reporting annotations, and
  `maxConcurrency` / `maxTotalTasks`.
- Make handoffs auditable through labels, annotations, owner references,
  Kubernetes events, and parent/child task status.
- Keep this narrower than a workflow engine. Phase 2 only creates child Tasks
  when a parent Task reaches a configured terminal phase and result predicate.

## Current Kelos Behavior

Kelos already supports most of the required pieces:

| Capability | Current behavior | Phase 2 use |
| --- | --- | --- |
| `TaskSpawner` | Creates Tasks from source events. | Owns persona trigger and handoff rules. |
| `TaskTemplate` | Defines type, prompt, AgentConfigs, workspace, pod overrides, labels, annotations, and TTL. | Reused for child handoff Tasks. |
| `Task.status.results` | Parsed from `key: value` lines emitted by `kelos-capture`. | Drives handoff matching and child prompt templates. |
| `dependsOn` | Downstream Tasks can wait on previous Tasks and read dependency outputs through `.Deps`. | Child handoff Tasks depend on the parent by default. |
| Slack reporting annotations | Slack-origin Tasks report status and final responses in the Slack thread. | Child Tasks inherit these annotations to continue in the same thread. |
| TaskSpawner labels | Spawned Tasks receive `kelos.dev/taskspawner`. | Child Tasks use the same label so limits and audit queries include them. |

The missing pieces are:

- a safe way for agents to emit additional result keys
- a CRD field to declare dynamic handoff rules
- a controller path that creates child Tasks when rules match
- Cody GitOps configuration that wires persona-to-persona flows

## Non-Goals

- Do not introduce a router persona.
- Do not add GitHub webhook, GitHub PR polling, GitHub comment, or GitHub label
  triggers.
- Do not let agents create arbitrary Kubernetes Tasks directly.
- Do not add a generic DAG/workflow API.
- Do not automatically create reviewer-to-dev remediation loops.
- Do not change behavior for TaskSpawners that omit `spec.handoffs`.
- Do not change the stable debugger route.
- Do not split Cody service accounts in this phase.
- Do not rely on channel-level Slack filtering.

## Proposed API

Add `handoffs` to `TaskSpawnerSpec`.

```go
type TaskSpawnerSpec struct {
    When TaskSpawnerWhen `json:"when"`
    TaskTemplate TaskTemplate `json:"taskTemplate"`
    PollInterval *metav1.Duration `json:"pollInterval,omitempty"`
    MaxConcurrency *int32 `json:"maxConcurrency,omitempty"`
    Suspend bool `json:"suspend,omitempty"`
    MaxTotalTasks *int32 `json:"maxTotalTasks,omitempty"`

    // Handoffs declares child Tasks to create when a Task spawned by this
    // TaskSpawner reaches a configured phase and result predicate.
    // +optional
    // +kubebuilder:validation:MaxItems=8
    Handoffs []TaskHandoff `json:"handoffs,omitempty"`
}
```

Add the handoff types:

```go
type TaskHandoff struct {
    // Name identifies this handoff rule and is copied to child metadata.
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=40
    // +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
    Name string `json:"name"`

    // TerminalPhases limits which parent phases may trigger this handoff.
    // Defaults to ["Succeeded"].
    // +optional
    // +kubebuilder:validation:MaxItems=2
    TerminalPhases []TaskPhase `json:"terminalPhases,omitempty"`

    // When contains result predicates that must all match.
    // Empty means phase-only matching.
    // +optional
    When TaskHandoffWhen `json:"when,omitempty"`

    // Inherit controls metadata copied from the parent Task.
    // +optional
    Inherit TaskHandoffInherit `json:"inherit,omitempty"`

    // TaskTemplate is rendered and created as the child Task when this
    // handoff matches.
    TaskTemplate TaskTemplate `json:"taskTemplate"`
}

type TaskHandoffWhen struct {
    // Results are ANDed together.
    // +optional
    // +kubebuilder:validation:MaxItems=16
    Results []TaskResultMatch `json:"results,omitempty"`
}

type TaskResultMatch struct {
    // Key is the status.results key to inspect.
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=80
    // +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
    Key string `json:"key"`

    // Operator defaults to Exists.
    // +optional
    Operator TaskResultOperator `json:"operator,omitempty"`

    // Value is used by Equals and NotEquals.
    // +optional
    Value string `json:"value,omitempty"`

    // Values is used by In and NotIn.
    // +optional
    // +kubebuilder:validation:MaxItems=32
    Values []string `json:"values,omitempty"`
}

type TaskResultOperator string

const (
    TaskResultExists    TaskResultOperator = "Exists"
    TaskResultEquals    TaskResultOperator = "Equals"
    TaskResultNotEquals TaskResultOperator = "NotEquals"
    TaskResultIn        TaskResultOperator = "In"
    TaskResultNotIn     TaskResultOperator = "NotIn"
)

type TaskHandoffInherit struct {
    // Labels copies selected parent labels by exact key.
    // +optional
    Labels []string `json:"labels,omitempty"`

    // Annotations copies selected parent annotations by exact key.
    // +optional
    Annotations []string `json:"annotations,omitempty"`

    // SlackThread copies the Slack reporting label and thread annotations.
    // +optional
    SlackThread bool `json:"slackThread,omitempty"`

    // DependsOnParent defaults to true. When true, the child Task depends on
    // the parent Task and can read parent outputs through dependency handling.
    // +optional
    DependsOnParent *bool `json:"dependsOnParent,omitempty"`

    // Lineage defaults to true. When true, Kelos writes parent/root/depth
    // labels and annotations.
    // +optional
    Lineage *bool `json:"lineage,omitempty"`
}
```

### Validation

CRD validation must enforce:

- `handoffs[].name` is a DNS-label-safe value.
- `handoffs[].terminalPhases`, when set, only contains `Succeeded` or
  `Failed`.
- `handoffs[].when.results[].operator`, when set, is one of `Exists`,
  `Equals`, `NotEquals`, `In`, or `NotIn`.
- `Exists` must not set `value` or `values`.
- `Equals` and `NotEquals` must set `value` and must not set `values`.
- `In` and `NotIn` must set at least one `values` entry and must not set
  `value`.
- `handoffs` is optional and defaults to no behavior.

The existing validation that requires `workspaceRef` for GitHub-backed source
types must remain unchanged.

## Agent Result Output Helper

Add a small helper in the reference agent image so agents can emit additional
Kelos result keys without hand-editing capture markers.

Proposed binary name: `kelos-output`.

Usage:

```bash
kelos-output set handoff.target dev
kelos-output set handoff.reason "Ticket ALPM-123 is ready for implementation."
kelos-output set-file handoff.prompt /tmp/handoff-prompt.txt
kelos-output set-file handoff.prompt.base64 /tmp/handoff-prompt.txt --base64
```

Implementation contract:

- Append line-delimited `key: value` entries to
  `/tmp/kelos-extra-outputs`.
- Use atomic append semantics.
- Reject reserved built-in keys:
  - `branch`
  - `pr`
  - `commit`
  - `base-branch`
  - `cost-usd`
  - `input-tokens`
  - `output-tokens`
  - `response`
- Accept keys matching:
  `^[a-z0-9]([a-z0-9.-]{0,78}[a-z0-9])?$`
- Reject keys longer than 80 characters.
- Reject values containing newlines for `set`.
- Limit `set` values to 16 KiB.
- Limit `set-file` values to 64 KiB after optional base64 encoding.
- Return a non-zero exit code with a clear stderr message on validation
  failure.

`kelos-output` is a convenience and safety boundary. Agents can be instructed
to use it, and `kelos-capture` remains responsible for publishing outputs into
the Task status.

### Capture Changes

Update `internal/capture/capture.go`:

1. Continue emitting existing built-in outputs exactly as today.
2. If `/tmp/kelos-extra-outputs` exists, validate each line as `key: value`.
3. Append validated extra output lines after built-ins.
4. Reject extra output lines that use reserved built-in keys.
5. Ignore a missing extra output file.
6. Surface malformed extra output lines in capture stderr, but do not hide the
   primary agent response.

Because reserved built-in keys are rejected, appending extras after built-ins
will not let an agent override `branch`, `pr`, `commit`, token usage, cost, or
the captured response.

## Handoff Reconciliation

Add handoff reconciliation to the Task controller or a small adjacent
reconciler that watches `Task` status updates.

### Trigger Conditions

For each Task update:

1. The Task must have label `kelos.dev/taskspawner`.
2. The Task phase must be terminal: `Succeeded` or `Failed`.
3. The referenced `TaskSpawner` must exist in the same namespace.
4. The `TaskSpawner` must have at least one `spec.handoffs` entry.
5. Each handoff entry must match:
   - parent terminal phase
   - all configured result predicates
   - handoff safety checks

Default `terminalPhases` is `Succeeded`.

### Result Predicate Semantics

All predicates in a handoff rule are ANDed.

| Operator | Match behavior |
| --- | --- |
| unset or `Exists` | `status.results[key]` exists and is not empty. |
| `Equals` | result exists and equals `value`. |
| `NotEquals` | result is missing or does not equal `value`. |
| `In` | result exists and equals one of `values`. |
| `NotIn` | result is missing or not in `values`. |

String comparison is exact and case-sensitive.

### Child Task Identity

Child Task names must be deterministic and Kubernetes-safe:

```text
<parent-task-name>-<handoff-name>-<hash>
```

If that exceeds 63 characters, truncate the parent segment and keep the suffix
stable. The hash input should include:

- parent Task namespace
- parent Task name
- parent Task UID
- handoff name

This makes re-reconciliation idempotent while avoiding collisions after parent
name reuse.

### Duplicate Prevention

Before creating a child Task, the controller must check for an existing Task in
the namespace with all of:

- `kelos.dev/parent-task=<parent name>`
- `kelos.dev/handoff=<handoff name>`
- `kelos.dev/taskspawner=<parent taskspawner name>`

If one exists, do not create another child.

Also tolerate create conflicts by re-reading the expected child name and
treating an existing child as success when labels match.

### Metadata

Every child Task created by a handoff must include:

Labels:

```yaml
kelos.dev/taskspawner: <source taskspawner name>
kelos.dev/parent-task: <parent task name>
kelos.dev/handoff: <handoff name>
kelos.dev/lineage-root: <root task name>
kelos.dev/lineage-depth: "<depth>"
```

Annotations:

```yaml
kelos.dev/parent-task-uid: <parent task uid>
kelos.dev/handoff: <handoff name>
```

When `inherit.slackThread: true`, copy:

Labels:

```yaml
kelos.dev/slack-reporting: enabled
```

Annotations:

```yaml
kelos.dev/slack-reporting: enabled
kelos.dev/slack-channel: <parent value>
kelos.dev/slack-thread-ts: <parent value>
kelos.dev/slack-user-id: <parent value>
```

Only copy Slack annotations that exist on the parent. Missing Slack thread
metadata should not block non-Slack handoffs, but Cody Phase 2 GitOps should set
`inherit.slackThread: true` only for Slack persona handoffs.

When `inherit.labels` or `inherit.annotations` is set, copy only exact listed
keys that exist on the parent. Do not support wildcards in Phase 2.

The handoff `taskTemplate.metadata` is rendered and applied after inherited
metadata. If the same key is present in both inherited metadata and rendered
template metadata, rendered template metadata wins except for reserved Kelos
lineage keys.

Reserved lineage keys cannot be overridden:

- `kelos.dev/taskspawner`
- `kelos.dev/parent-task`
- `kelos.dev/handoff`
- `kelos.dev/lineage-root`
- `kelos.dev/lineage-depth`
- `kelos.dev/parent-task-uid`

### Owner References

The child Task should use the same TaskSpawner owner reference behavior as
normal spawned Tasks.

Do not set the parent Task as a Kubernetes owner reference in Phase 2. Parent
Tasks commonly have TTL cleanup, and a parent owner reference would risk
garbage-collecting useful child Tasks. Parentage is tracked through labels and
annotations instead.

### `dependsOn`

`inherit.dependsOnParent` defaults to true.

When true, append the parent Task name to the child `spec.dependsOn` list unless
it is already present. This gives the child Task access to parent outputs
through existing dependency prompt templating and preserves an explicit runtime
relationship.

When false, do not add an implicit dependency. This should be rare and is not
needed for Cody Phase 2.

### Prompt Template Variables

Child handoff templates need parent context without forcing all information
through `.Deps`.

When rendering a handoff child `TaskTemplate`, add:

```text
.Upstream.Name
.Upstream.Namespace
.Upstream.Phase
.Upstream.Message
.Upstream.Outputs
.Upstream.Results
.Upstream.Labels
.Upstream.Annotations
.Lineage.Root
.Lineage.Parent
.Lineage.Depth
.Handoff.Name
```

Existing source-event template variables must continue to work unchanged for
normal TaskSpawner-created Tasks.

Template rendering should continue to use `missingkey=error`. GitOps handoff
templates should use `index` for optional result keys:

```gotemplate
PR: {{ index .Upstream.Results "pr" }}
Reason: {{ index .Upstream.Results "handoff.reason" }}
```

### Safety Checks

The handoff reconciler must block:

- lineage depth greater than 3
- a handoff whose target template would create a Task with the same handoff name
  and same AgentConfig stack as the parent, unless explicitly allowed in a
  future API
- child creation when the source `TaskSpawner` is suspended
- child creation when `maxTotalTasks` would be exceeded
- child creation when `maxConcurrency` would be exceeded

Concurrency and total-task counting must include both source-event Tasks and
handoff child Tasks because they share the same `kelos.dev/taskspawner` label.

When blocked, record a Kubernetes event on the parent Task and the TaskSpawner.
Do not mutate the parent Task phase.

### Kubernetes Events

Emit events for:

| Event reason | Object | When |
| --- | --- | --- |
| `HandoffCreated` | parent Task and TaskSpawner | A child Task is created. |
| `HandoffSkipped` | parent Task | A rule does not match or an idempotent child already exists. |
| `HandoffBlocked` | parent Task and TaskSpawner | A safety, suspend, concurrency, or total limit prevents creation. |
| `HandoffFailed` | parent Task and TaskSpawner | Child rendering or create fails unexpectedly. |

Event messages should include handoff name, child task name when available, and
the blocking reason.

## Slack Reporting Behavior

No new Slack API behavior is required for the minimum viable Phase 2.

When `inherit.slackThread: true`, the child Task inherits the Slack reporting
label and thread annotations. The existing Slack reporter will then post child
Task accepted/running/final responses in the same Slack thread as the parent.

Expected user experience:

1. User invokes `@cody !ticket ...`.
2. Ticket persona replies in the Slack thread with its final response.
3. If it emitted a matching handoff result, Kelos creates the dev child Task.
4. Dev persona status and final response appear in the same thread.
5. If dev opens a PR and emits a matching handoff result, Kelos creates the PR
   reviewer child Task.
6. Reviewer status and final response appear in the same thread.

Optional later polish: include `kelos.dev/handoff` or persona labels in Slack
status blocks so users can visually distinguish parent and child personas. This
is not required for Phase 2.

## Cody Result Contract

Persona AgentConfigs should instruct Cody to emit these result keys through
`kelos-output` when handoff is appropriate.

### Common handoff keys

| Key | Required | Purpose |
| --- | --- | --- |
| `handoff.target` | Yes | Target persona route, such as `dev` or `pr-reviewer`. |
| `handoff.reason` | Recommended | Short reason for audit and child prompt context. |
| `handoff.prompt` | Recommended | Human-readable child prompt. Keep under 16 KiB. |
| `handoff.prompt.base64` | Optional | Larger or multiline child prompt, base64 encoded. |

Prefer `handoff.prompt` when the prompt is one line or can be compact. Use
`handoff.prompt.base64` when preserving multiline formatting matters.

### Ticket creator outputs

| Key | Purpose |
| --- | --- |
| `ticket` | Jira issue key such as `ALPM-123`. |
| `ticket.url` | Jira issue URL when available. |
| `handoff.target=dev` | Emit only when implementation should start automatically. |

Ticket creator should not hand off to dev for requests that only ask for ticket
creation, refinement, or backlog grooming.

### Dev outputs

| Key | Purpose |
| --- | --- |
| `branch` | Existing built-in output when a branch was created or used. |
| `commit` | Existing built-in output when a commit exists. |
| `pr` | Existing built-in output when a PR was opened. |
| `handoff.target=pr-reviewer` | Emit only when the PR is ready for automated review. |

Dev should not emit reviewer handoff when no PR was opened.

### PR reviewer outputs

| Key | Purpose |
| --- | --- |
| `review.findings` | Optional compact summary count or category. |
| `review.result` | Optional value such as `clean`, `changes-requested`, or `blocked`. |

Reviewer must not automatically hand off back to dev in Phase 2. It should tell
the user how to invoke `@cody !dev ...` if follow-up implementation is needed.

## Cody GitOps Follow-Up

After Kelos Phase 2 is released, update the Cody Slack persona TaskSpawners in
`k8s-platform-gitops/non-prod/kelos`.

This GitOps follow-up is intentionally separate from the Kelos code PR because
it depends on the new CRD and controller behavior being deployed.

### Ticket to dev handoff

Add a `handoffs` entry to `cody-ticket-slack`:

```yaml
handoffs:
  - name: ticket-to-dev
    terminalPhases:
      - Succeeded
    when:
      results:
        - key: handoff.target
          operator: Equals
          value: dev
        - key: ticket
          operator: Exists
    inherit:
      slackThread: true
      dependsOnParent: true
      lineage: true
    taskTemplate:
      type: codex
      credentials:
        type: oauth
        secretRef:
          name: cody-codex-credentials
      image: docker.io/alpheya/codex:main
      ttlSecondsAfterFinished: 3600
      agentConfigRefs:
        - name: cody-base
        - name: cody-dev
        - name: cody-atlassian-mcp
      metadata:
        labels:
          cody.alpheya.com/persona: dev
          cody.alpheya.com/source: slack-handoff
      promptTemplate: |
        Cody dev handoff from ticket creator.

        Parent task: {{ .Upstream.Name }}
        Ticket: {{ index .Upstream.Results "ticket" }}
        Ticket URL: {{ index .Upstream.Results "ticket.url" }}
        Handoff reason: {{ index .Upstream.Results "handoff.reason" }}

        User request for implementation:
        {{ index .Upstream.Results "handoff.prompt" }}
```

Use the same runtime fields as the Phase 1 dev TaskSpawner, including
`podOverrides`, GitHub App env, JWT env, and service account.

### Dev to PR reviewer handoff

Add a `handoffs` entry to `cody-dev-slack`:

```yaml
handoffs:
  - name: dev-to-review
    terminalPhases:
      - Succeeded
    when:
      results:
        - key: handoff.target
          operator: Equals
          value: pr-reviewer
        - key: pr
          operator: Exists
    inherit:
      slackThread: true
      dependsOnParent: true
      lineage: true
    taskTemplate:
      type: codex
      credentials:
        type: oauth
        secretRef:
          name: cody-codex-credentials
      image: docker.io/alpheya/codex:main
      ttlSecondsAfterFinished: 3600
      agentConfigRefs:
        - name: cody-base
        - name: cody-pr-reviewer
        - name: cody-atlassian-mcp
      metadata:
        labels:
          cody.alpheya.com/persona: pr-reviewer
          cody.alpheya.com/source: slack-handoff
      promptTemplate: |
        Cody PR review handoff from dev.

        Parent task: {{ .Upstream.Name }}
        PR: {{ index .Upstream.Results "pr" }}
        Branch: {{ index .Upstream.Results "branch" }}
        Commit: {{ index .Upstream.Results "commit" }}
        Handoff reason: {{ index .Upstream.Results "handoff.reason" }}

        Review this PR using the PR reviewer persona. Focus on correctness,
        regression risk, missing tests, security concerns, and whether the
        implementation satisfies the ticket or Slack request.
```

Use the same runtime fields as the Phase 1 reviewer TaskSpawner.

### AgentConfig updates

Update `cody-ticket-creator` instructions:

- Use `kelos-output set ticket <KEY>` after creating or updating a Jira ticket.
- Use `kelos-output set ticket.url <URL>` when a URL is available.
- Emit `handoff.target=dev` only when the user explicitly asked for
  implementation after ticket creation or the ticket text clearly requires
  immediate implementation.
- Emit `handoff.prompt` with a concise implementation brief.

Update `cody-dev` instructions:

- Use the existing PR creation workflow.
- Emit `handoff.target=pr-reviewer` only after a PR is opened and ready for
  review.
- Emit `handoff.reason` with a short reason.
- Let the built-in capture output provide `branch`, `commit`, and `pr`.

Update `cody-pr-reviewer` instructions:

- Do not emit `handoff.target=dev` in Phase 2.
- If fixes are required, report findings and tell the user the exact
  `@cody !dev ...` command to invoke.

## Example End-to-End Flow

User message:

```text
@cody !ticket create a ticket for the portfolio report export bug and implement it if the ticket is clear
```

Expected sequence:

1. `cody-ticket-slack` creates a ticket Task.
2. Ticket persona creates `ALPM-123`.
3. Ticket persona emits:

   ```text
   ticket: ALPM-123
   ticket.url: https://wgen4.atlassian.net/browse/ALPM-123
   handoff.target: dev
   handoff.reason: User asked to implement after ticket creation.
   handoff.prompt: Implement ALPM-123. Reproduce the portfolio report export bug, add a focused fix and tests, then open a PR.
   ```

4. Handoff reconciler creates a dev child Task in the same Slack thread.
5. Dev persona implements the fix and opens a PR.
6. Dev capture emits built-in `branch`, `commit`, and `pr`; dev also emits:

   ```text
   handoff.target: pr-reviewer
   handoff.reason: PR is open and ready for review.
   ```

7. Handoff reconciler creates a PR reviewer child Task in the same Slack thread.
8. Reviewer posts findings in the same Slack thread.

## Implementation Plan

### 1. Add `kelos-output`

Files to add or modify:

- reference agent image scripts or binaries
- image packaging files
- tests for helper validation

Acceptance criteria:

- `kelos-output set key value` appends a valid output line.
- Reserved keys are rejected.
- Invalid keys are rejected.
- Newlines in `set` values are rejected.
- Size limits are enforced.
- `set-file --base64` writes base64 output.

### 2. Extend capture

Files to modify:

- `internal/capture/capture.go`
- capture tests

Acceptance criteria:

- Existing built-in capture output is unchanged.
- Missing `/tmp/kelos-extra-outputs` is ignored.
- Valid extra outputs appear in `Task.status.outputs` and
  `Task.status.results`.
- Reserved key attempts are rejected and cannot override built-ins.
- Malformed extra output lines are surfaced clearly.

### 3. Add API fields and CRD generation

Files to modify:

- `api/v1alpha1/taskspawner_types.go`
- generated deepcopy files
- CRD manifests
- API docs if present

Acceptance criteria:

- `TaskSpawner.spec.handoffs` is optional.
- Existing TaskSpawner YAML remains valid.
- Validation rejects invalid operators and invalid result match shapes.
- Generated CRDs include the new schema.

### 4. Implement handoff matching and child creation

Recommended files:

- `internal/controller/task_handoff.go`
- `internal/controller/task_handoff_test.go`
- small changes in `internal/controller/task_controller.go`
- reuse `internal/taskbuilder/builder.go`

Acceptance criteria:

- A matching terminal parent creates exactly one child Task.
- Reconciliation is idempotent.
- Non-matching result predicates create no child.
- Failed parents only trigger rules that include `Failed`.
- Child Task inherits Slack reporting when configured.
- Child Task has lineage metadata.
- Child Task depends on parent by default.
- Child prompt template can read `.Upstream.Results`.
- `maxConcurrency`, `maxTotalTasks`, and `suspend` block child creation.
- Handoff blocked/created/failed events are emitted.

### 5. Documentation and examples

Files to add or modify:

- example TaskSpawner YAML
- user-facing handoff docs if Kelos has a docs area
- Cody persona docs if kept in Kelos

Acceptance criteria:

- Docs show `handoffs` YAML.
- Docs show `kelos-output` usage.
- Docs explain Slack thread inheritance and lineage labels.

### 6. Release and GitOps follow-up

After the Kelos PR merges:

1. Build and publish a Kelos controller image that includes the handoff
   reconciler and `kelos-output` support in the reference agent image.
2. Apply CRDs through the existing deployment path.
3. Update `k8s-platform-gitops/non-prod/kelos` with Cody handoff rules.
4. Wait for Flux to apply the GitOps PR.
5. Run manual Slack tests listed below.

## Test Plan

### Unit tests

- `kelos-output` accepts valid keys and values.
- `kelos-output` rejects reserved keys.
- `kelos-output` rejects malformed keys.
- `kelos-output set-file --base64` produces a single-line base64 value.
- Capture appends valid extra outputs.
- Capture rejects reserved extra output keys.
- Result matcher covers `Exists`, `Equals`, `NotEquals`, `In`, and `NotIn`.
- Handoff child naming is deterministic and at most 63 characters.
- Handoff template vars include `.Upstream`, `.Lineage`, and `.Handoff`.

### Controller tests

- Parent `Succeeded` plus matching `handoff.target` creates a child.
- Parent `Running` creates no child.
- Parent `Failed` creates no child unless configured.
- A repeated reconcile does not create a duplicate child.
- Existing child with matching lineage labels is treated as already created.
- Slack annotations are copied when `inherit.slackThread=true`.
- Custom listed labels and annotations are copied.
- Rendered child metadata overrides non-reserved inherited metadata.
- Reserved lineage keys cannot be overridden.
- Child has `spec.dependsOn` containing the parent by default.
- `dependsOnParent=false` skips implicit dependency.
- `suspend=true` blocks child creation.
- `maxConcurrency` blocks child creation.
- `maxTotalTasks` blocks child creation.
- lineage depth greater than 3 blocks child creation.

### Manual Slack tests after GitOps rollout

Use low-risk test requests in a non-prod Slack channel.

Ticket only:

```text
@cody !ticket create a Jira ticket for a test-only docs cleanup request. Do not implement it.
```

Expected:

- ticket persona runs
- Jira ticket is created or updated
- no dev handoff occurs

Ticket to dev:

```text
@cody !ticket create a small test-only docs cleanup ticket and implement it if the scope is clear.
```

Expected:

- ticket persona runs
- dev child Task appears in the same Slack thread if ticket persona emits
  `handoff.target=dev`
- no duplicate dev child appears

Dev only:

```text
@cody !dev make a no-op docs-only test change in the Kelos fork, open a PR, and keep it clearly marked as test-only.
```

Expected:

- dev persona runs
- PR is opened
- reviewer child Task appears only after `pr` and `handoff.target=pr-reviewer`
  exist

Review only:

```text
@cody !review https://github.com/donchev7/kelos/pull/<test-pr>
```

Expected:

- reviewer persona runs independently
- no automatic dev handoff occurs

Negative routing:

```text
@cody debug why the word dev appears in this sentence
```

Expected:

- stable debugger route handles the request
- no dev persona route runs

Unmentioned prefix:

```text
!dev do not run this
```

Expected:

- no Cody Task is created

## Rollback

Kelos code rollback:

- Revert the controller image to a version before `spec.handoffs` support.
- Existing TaskSpawners without `handoffs` are unaffected.
- TaskSpawners with `handoffs` require the newer CRD. Remove `handoffs` before
  rolling CRDs back.

GitOps rollback:

- Remove `handoffs` entries from Cody TaskSpawners.
- Keep Phase 1 persona routes intact.
- Flux should converge without deleting existing completed Tasks until their
  TTL expires.

Operational kill switches:

- Set `spec.suspend: true` on a Cody TaskSpawner to stop both source-event and
  handoff child creation for that route.
- Lower `maxConcurrency` or `maxTotalTasks` to contain a problematic route.
- Remove or change the relevant `handoffs` entry to stop only that handoff.

## Open Questions

1. Should `handoff.prompt.base64` be decoded by Kelos before rendering, or
   should AgentConfigs avoid base64 handoff prompts until a later release?
   Recommendation: keep Phase 2 simple and do not decode automatically.
2. Should Slack reporter include persona and handoff labels in status messages?
   Recommendation: defer. Thread continuity is enough for Phase 2.
3. Should failed parent Tasks support handoff to a remediation persona?
   Recommendation: keep the API capable of `Failed`, but do not configure Cody
   failed-task handoffs in the first GitOps rollout.

## Acceptance Criteria

Phase 2 is complete when:

- Kelos supports optional `TaskSpawner.spec.handoffs`.
- Agents can emit safe custom result keys through `kelos-output`.
- Capture publishes those keys into `Task.status.results`.
- Matching handoff rules create exactly one child Task.
- Child Tasks report in the same Slack thread when configured.
- Cody GitOps can configure ticket-to-dev and dev-to-review handoffs without
  adding GitHub triggers or a router persona.
- Existing Cody behavior remains unchanged for routes without handoffs.
