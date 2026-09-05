# Reference

## Task

Exactly one execution source is required: `spec.worker` (preferred), `spec.workerPoolRef`, or legacy flat fields (`spec.type` + `spec.credentials`).

| Field | Description | Required |
|-------|-------------|----------|
| `spec.worker` | Execution environment (see [WorkerSpec](#workerspec) below). Creates a Job. Mutually exclusive with `workerPoolRef` | One of worker, workerPoolRef, or type+credentials |
| `spec.workerPoolRef.name` | Name of a WorkerPool resource. Task is dispatched to a pre-warmed worker pod instead of creating a Job | One of worker, workerPoolRef, or type+credentials |
| `spec.prompt` | Task prompt for the agent | Yes |
| `spec.type` | **(Deprecated)** Agent type — use `spec.worker.type` instead | Legacy |
| `spec.credentials.type` | **(Deprecated)** Credential type — use `spec.worker.credentials` instead | Legacy |
| `spec.credentials.secretRef.name` | **(Deprecated)** Secret name — use `spec.worker.credentials.secretRef` instead | Legacy |
| `spec.model` | **(Deprecated)** Model override — use `spec.worker.model` instead | Legacy |
| `spec.effort` | **(Deprecated)** Reasoning effort — use `spec.worker.effort` instead | Legacy |
| `spec.image` | **(Deprecated)** Custom agent image — use `spec.worker.image` instead | Legacy |
| `spec.workspaceRef.name` | **(Deprecated)** Workspace reference — use `spec.worker.workspaceRef` instead | Legacy |
| `spec.agentConfigRefs[].name` | **(Deprecated)** AgentConfig references — use `spec.worker.agentConfigRefs` instead | Legacy |
| `spec.dependsOn` | Task names that must succeed before this Task starts (creates `Waiting` phase). Not supported with `workerPoolRef` | No |
| `spec.branch` | Git branch to work on; only one Task with the same branch runs at a time (mutex). Not supported with `workerPoolRef` | No |
| `spec.ttlSecondsAfterFinished` | Auto-delete task after N seconds (0 for immediate) | No |
| `spec.podFailurePolicy` | Kubernetes Job pod failure policy copied to `Job.spec.podFailurePolicy`. If omitted, Kelos leaves it unset and Kubernetes default Job failure handling applies | No |
| `spec.podOverrides` | **(Deprecated)** Pod customization — use `spec.worker.podOverrides` instead | Legacy |
| `spec.podOverrides.labels` | Additional labels to apply to the Job and its Pod. Merged with built-in labels; built-in labels take precedence on conflict | No |
| `spec.podOverrides.resources` | CPU/memory requests and limits for the agent container | No |
| `spec.podOverrides.activeDeadlineSeconds` | Maximum duration in seconds before the agent pod is terminated | No |
| `spec.podOverrides.env` | Additional environment variables (built-in vars take precedence on conflict) | No |
| `spec.podOverrides.nodeSelector` | Node selection labels to constrain which nodes run agent pods | No |
| `spec.podOverrides.tolerations` | Tolerations for the agent pod; use with `nodeSelector` or `affinity` to target dedicated node pools (e.g., GPU nodes, agent-specific pools) | No |
| `spec.podOverrides.affinity` | Node, pod, and pod-anti-affinity rules. Use for spreading agents across nodes or expressing scheduling preferences beyond `nodeSelector` | No |
| `spec.podOverrides.imagePullSecrets` | Secrets used to pull container images from private registries. Required when the agent image or any init container image is in a private registry | No |
| `spec.podOverrides.serviceAccountName` | Service account name for the agent pod; use with workload identity systems (IRSA, GKE Workload Identity, Azure) | No |
| `spec.podOverrides.volumes` | Additional volumes to attach to the agent pod. Names must not be `workspace` or use the Kelos-reserved `kelos-` prefix | No |
| `spec.podOverrides.volumeMounts` | Additional volume mounts on the agent container; names must reference either a user-supplied volume from `volumes` or a Kelos-managed volume (`workspace` or a `kelos-` volume such as `kelos-plugin` or `kelos-github-token`) | No |
| `spec.podOverrides.podSecurityContext` | Pod-level security context applied to the agent pod. Fields set here override Kelos defaults; `fsGroup` retains the Kelos default when unset so the agent user keeps workspace access | No |
| `spec.podOverrides.containerSecurityContext` | Security context applied to the agent container. Use to declare `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `readOnlyRootFilesystem: true`, etc., for PSS-restricted namespaces | No |
| `spec.podOverrides.extraContainers` | Additional containers to run alongside the agent container in the same pod (max 8). They share the pod's network namespace (reachable via `localhost`) and can mount user-supplied volumes from `volumes`. Use for sidecars such as a database for integration tests or a proxy. Names must not use the Kelos-reserved `kelos-` prefix, collide with a built-in init container name (`git-clone`, `remote-setup`, `branch-setup`, `workspace-files`, `plugin-setup`, `skills-install`), duplicate another entry, or appear in `extraInitContainers` (see [Extra Containers](#task-extra-containers) below) | No |
| `spec.podOverrides.extraInitContainers` | Additional init containers (max 8), appended after all Kelos built-in init containers so the workspace is ready before they run. Set `restartPolicy: Always` for sidecar semantics (long-running services, K8s 1.29+) or leave it unset for one-shot pre-agent setup. They can mount user-supplied volumes from `volumes` as well as Kelos-managed volumes (`workspace` or a `kelos-` volume such as `kelos-plugin` or `kelos-github-token`); workspace write access requires running as a UID in the pod's `fsGroup`. Same name constraints as `extraContainers` (see [Extra Containers](#task-extra-containers) below) | No |

### Pod Override Volumes

`Task.spec.podOverrides.volumes` and `TaskSpawner.spec.taskTemplate.podOverrides.volumes` are for user-managed volumes. User-supplied volume names must not be `workspace` or start with `kelos-`; Kelos reserves those names for controller-managed pod wiring.

If an existing manifest uses a user volume name such as `kelos-cache`, rename that volume and every matching `Task.spec.podOverrides.volumeMounts`, `Task.spec.podOverrides.extraContainers[].volumeMounts`, or `Task.spec.podOverrides.extraInitContainers[].volumeMounts` reference to a non-reserved name such as `cache`. Apply the same rename under `TaskSpawner.spec.taskTemplate.podOverrides` for spawned task templates.

### Task Pod Failure Policy

`spec.podFailurePolicy` accepts Kubernetes Job `podFailurePolicy` rules except `FailIndex`, which only applies to indexed Jobs and is rejected for Kelos Task Jobs. Kelos copies the field as a complete policy; it does not merge in default rules. Rule order matters because Kubernetes stops evaluating after the first match.

When the field is omitted, Kelos leaves `Job.spec.podFailurePolicy` unset. To ignore infrastructure disruptions while still failing the Job on non-zero container exits, set the policy explicitly:

```yaml
spec:
  podFailurePolicy:
    rules:
      - action: Ignore
        onPodConditions:
          - type: DisruptionTarget
            status: "True"
      - action: FailJob
        onExitCodes:
          operator: NotIn
          values: [0]
```

<a id="task-extra-containers"></a>

### Extra Containers

`spec.podOverrides.extraContainers` and `spec.podOverrides.extraInitContainers` let a Task run user-defined containers alongside the agent. Both lists accept a standard Kubernetes [Container](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#container-v1-core) and are subject to these constraints (validated by the API server and the controller):

- Maximum 8 entries per list.
- Container names must not use the Kelos-reserved `kelos-` prefix, and must not collide with built-in init container names: `git-clone`, `remote-setup`, `branch-setup`, `workspace-files`, `plugin-setup`, `skills-install`.
- A name must not be duplicated within a list, and must not appear in both `extraContainers` and `extraInitContainers` (Kubernetes requires container names to be unique within a pod).
- `extraInitContainers` run after every Kelos built-in init container, so the cloned workspace and installed plugins are already in place. Write access to the `workspace` volume requires running as a UID in the pod's `fsGroup` (the agent UID `61100` by default).

Example — run a PostgreSQL sidecar for integration tests alongside the agent (reachable at `localhost:5432`):

```yaml
apiVersion: kelos.dev/v1alpha2
kind: Task
metadata:
  name: integration-test
spec:
  type: claude-code
  prompt: Run the integration test suite against the local PostgreSQL instance.
  credentials:
    type: api-key
    secretRef:
      name: claude-credentials
  podOverrides:
    extraContainers:
      - name: postgres
        image: postgres:16
        env:
          - name: POSTGRES_PASSWORD
            value: testpass
        ports:
          - containerPort: 5432
```

### Dependency Result Passing

When a Task has `dependsOn`, its `prompt` field supports Go `text/template` syntax for referencing upstream results. The template data has a single key `.Deps` containing a map keyed by dependency Task name:

| Variable | Type | Description |
|----------|------|-------------|
| `{{index .Deps "<name>" "Results" "<key>"}}` | string | A specific key-value result from the dependency (e.g., `branch`, `commit`, `pr`) |
| `{{index .Deps "<name>" "Outputs"}}` | []string | Raw output lines from the dependency |
| `{{index .Deps "<name>" "Name"}}` | string | The dependency Task name |

Example:

```yaml
prompt: |
  The scaffold task created code on branch {{index .Deps "scaffold" "Results" "branch"}}.
  Open a PR for these changes.
dependsOn: [scaffold]
```

If template rendering fails (e.g., missing key), the raw prompt string is used as-is.

### Task Credential Secret Format

The secret referenced by `spec.credentials.secretRef.name` must contain a single key whose name depends on `spec.type` and `spec.credentials.type`:

| Agent type | Credential type | Secret key |
|------------|-----------------|------------|
| `claude-code` | `api-key` | `ANTHROPIC_API_KEY` |
| `claude-code` | `oauth` | `CLAUDE_CODE_OAUTH_TOKEN` |
| `codex` | `api-key` | `CODEX_API_KEY` |
| `codex` | `oauth` | `CODEX_AUTH_JSON` (full `~/.codex/auth.json` content) |
| `gemini` | `api-key` or `oauth` | `GEMINI_API_KEY` |
| `opencode` | `api-key` or `oauth` | `OPENCODE_API_KEY` |
| `cursor` | `api-key` or `oauth` | `CURSOR_API_KEY` |

Example for `claude-code` with an API key:

```bash
kubectl create secret generic claude-credentials \
  --from-literal=ANTHROPIC_API_KEY=<your-api-key>
```

Example for `gemini`:

```bash
kubectl create secret generic gemini-credentials \
  --from-literal=GEMINI_API_KEY=<your-api-key>
```

When `spec.credentials.type` is `none`, no secret is required; supply credentials via `spec.podOverrides.env` (e.g., for Bedrock, Vertex AI, or Azure OpenAI). For details on how these variables are consumed by agent containers, see [Agent Image Interface](agent-image-interface.md).

### Codex OAuth Token Refresh

A `codex` `oauth` credential (`CODEX_AUTH_JSON`) contains a short-lived access
token and a long-lived refresh token. To keep the credential usable between
agent runs, configure a refresh schedule and label each credentials Secret that
Kelos should refresh:

```yaml
# values.yaml
codexAuthRefresher:
  schedule: "0 */6 * * *"
```

```bash
kubectl label secret codex-credentials kelos.dev/codex-oauth-refresh=true
```

Kelos updates only the `CODEX_AUTH_JSON` key; it preserves other keys and does
not log the token. Removing the label stops refreshes. Secrets with a missing or
empty `CODEX_AUTH_JSON` value, or without a refresh token, are not refreshed.
Externally-managed Secrets (ExternalSecrets, Vault, sealed-secrets) overwrite
the refreshed value on their next sync and are not supported.

<a id="workerspec"></a>

### WorkerSpec

`spec.worker` on a Task (or `spec.taskTemplate.worker` on a TaskSpawner) is the preferred way to define execution environment. Mutually exclusive with `workerPoolRef`.

| Field | Description | Required |
|-------|-------------|----------|
| `worker.type` | Agent type (`claude-code`, `codex`, `gemini`, `opencode`, or `cursor`) | Yes for inline Task execution (CEL-enforced) |
| `worker.credentials.type` | `api-key`, `oauth`, or `none` | Yes for inline Task execution (CEL-enforced) |
| `worker.credentials.secretRef.name` | Secret name (not required when `type` is `none`) | Conditional |
| `worker.model` | Model override passed as `KELOS_MODEL` | No |
| `worker.effort` | Reasoning effort passed as `KELOS_EFFORT` | No |
| `worker.image` | Custom agent image override | No |
| `worker.workspaceRef.name` | Name of a Workspace resource | No |
| `worker.agentConfigRefs[].name` | Ordered AgentConfig resources. Configs are merged in order | No |
| `worker.podOverrides` | Pod customization (same fields as the legacy `spec.podOverrides`) | No |

## Session

A Session is one interactive Claude Code, Codex, or OpenCode conversation that
web and terminal clients can share and reconnect to. The spec is immutable
except for `spec.worker.credentials`, `spec.worker.model`,
`spec.suspend`, `spec.idlePolicy`, and fields under `spec.worker.podOverrides`
other than `serviceAccountName`.
Conversation events and history are retained on the Session workspace rather
than in the Kubernetes API. If configured, `spec.initialPrompt` also remains in
the Session resource and is visible through the Kubernetes API.

| Field | Description | Required |
|-------|-------------|----------|
| `spec.worker.type` | Agent provider: `claude-code`, `codex`, or `opencode` | Yes |
| `spec.worker.credentials` | Provider credentials (`api-key`, `oauth`, or `none`) | Yes |
| `spec.worker.model` | Provider model override | No |
| `spec.worker.effort` | Provider reasoning-effort override | No |
| `spec.worker.image` | Agent image override implementing the Session image contract | No |
| `spec.worker.workspaceRef.name` | Workspace cloned into the Session Pod | No |
| `spec.worker.agentConfigRefs[].name` | Ordered AgentConfig resources | No |
| `spec.worker.podOverrides` | Pod resources, scheduling, environment, volumes, and sidecars | No |
| `spec.worker.podOverrides.serviceAccountName` | Service account for the Session Pod; immutable after creation | No |
| `spec.suspend` | Stop the Session runtime without deleting the Session or its persistent workspace (defaults to `false`) | No |
| `spec.initialBranch` | Git branch used to initialize the Session workspace. Checks out the branch from `origin` when it exists, or creates it from the Workspace ref. Requires `spec.worker.workspaceRef` | No |
| `spec.initialPrompt` | Prompt submitted when the Session starts without retained conversation history. An `emptyDir` workspace may submit it again after Pod replacement | No |
| `spec.volumeClaimTemplate` | PersistentVolumeClaimSpec for the Session workspace. Recommended for durable Sessions; omit to use an ephemeral `emptyDir` workspace | No |
| `spec.idlePolicy.suspendAfterSeconds` | Automatically stop the Session runtime once it has been continuously idle for this many seconds without changing `spec.suspend`. Persistent workspace storage is retained. Selecting the Session in the web interface or starting a terminal connection resumes it. Omit to never suspend; zero suspends as soon as it goes idle. When deletion is also configured, this value must be less than `deleteAfterSeconds` | No |
| `spec.idlePolicy.deleteAfterSeconds` | Automatically delete the Session once it has been continuously idle (no active turn, no reported activity) for this many seconds, measured from the later of `status.lastActivityTime` and the creation time. Renewed activity resets the idle period. Before deletion the runtime stops accepting new turns and any in-flight turn completes. Deleting the Session removes its workspace storage. Omit to never delete; zero deletes as soon as it goes idle. When suspension is also configured, this value must be greater than `suspendAfterSeconds` | No |
| `status.phase` | Infrastructure phase: `Pending`, `Ready`, `Suspended`, or `Failed` | Output |
| `status.podName` | Session Pod name | Output |
| `status.podUID` | Identity of the Pod running the live conversation | Output |
| `status.lastActivityTime` | When runtime activity was first reported or last changed; Pod replacement does not change it | Output |
| `status.model` | Model reported by the live Session runtime; empty when the runtime does not report a model | Output |
| `status.conditions[type=Ready]` | Whether the Session infrastructure is ready for clients | Output |
| `status.conditions[type=Active]` | Whether the runtime has an unfinished turn; `reason: WaitingForInput` means the turn is waiting for a user response, and `Unknown` means activity has not been reported | Output |
| `status.branch` | Currently checked-out git branch in the Session workspace | Output |
| `status.pullRequest.url` | Web URL of the pull request associated with the current branch | Output |
| `status.pullRequest.state` | Pull request state: `Draft`, `Open`, `Queued`, `Merged`, or `Closed`. `Queued` means the pull request is in a merge queue | Output |
| `status.pullRequest.checks.state` | Aggregate GitHub check state: `Pending`, `Success`, or `Failure`. Queued pull requests use the merge queue commit's checks. Cancelled checks are failures | Output |
| `status.pullRequest.checks.completed` | Number of GitHub checks that have completed | Output |
| `status.pullRequest.checks.total` | Total number of GitHub checks reported for the pull request | Output |

The Session runtime refreshes pull request and GitHub check status at startup,
after each turn, every 30 seconds while checks are pending or the pull request
is queued, and every five minutes otherwise. The Console Sessions view's
sidebar and selected Session header show the aggregate check state and pending
progress. `status.pullRequest.checks` is omitted when GitHub reports no checks
for the pull request. Failed GitHub refreshes use exponential retry delays from
one minute up to 15 minutes.

The web creation dialog can generate a new Session from an existing Session in
the active namespace. This copies the complete `Session.spec` into an editable
form or YAML manifest and leaves the new name blank. It does not copy the source
metadata, conversation, or persistent-volume data.

Use `kelos session connect NAME` for terminal chat. In an interactive terminal,
press Enter to send a message, Ctrl+J to insert a newline, and Ctrl+C or Esc to
interrupt an active turn. Ctrl+C exits the terminal client when no turn is
active. `/quit` and `/exit` detach the terminal client without interrupting work
that is still running. The terminal initially loads a bounded page of recent
transcript items. Use `/history` or Page Up to load the previous page.
Attach a local file with `/attach PATH`; the next message includes all staged
files. In the interactive terminal UI, dragging a file into a terminal that
supports bracketed paste stages the file directly. Use `/send` in the plain
terminal to send staged files without message text. The web composer accepts
files from its attachment button or by drag and drop. Each message supports up
to eight files of 10 MiB each. A Session retains up to 128 attachments and 100
MiB of attachment data. Attachments share the Session workspace lifecycle, so
they survive Pod replacement only when `spec.volumeClaimTemplate` is configured
and are removed by Session reset or deletion. Retained messages show attachment
names, and the web client provides authenticated previews or downloads while
the Session is ready.

Both terminal and web chat recognize `!COMMAND` and `/goal` before ordinary
messages are submitted. `!COMMAND` runs `/bin/sh -lc COMMAND` directly in the
Session working directory with the Session environment. It does not ask the
agent for approval or start a model turn. The command, exit code, duration, and
retained output are added to the agent conversation context for subsequent
turns. Claude Code includes pending command results with the next ordinary
prompt; Codex and OpenCode record them immediately. Live and retained command
output appears as tool activity, and interrupting the Session turn stops the
command.

`/goal` is available only in Codex Sessions and uses the persisted goal owned by
the Codex conversation:

| Command | Behavior |
|---------|----------|
| `/goal` | Show the current goal |
| `/goal OBJECTIVE` | Start an objective when no unfinished goal exists |
| `/goal edit OBJECTIVE` | Replace the current objective while preserving its status |
| `/goal pause` | Finish the current Codex turn, then stop automatic continuation |
| `/goal resume` | Resume automatic continuation |
| `/goal clear` | Remove the current goal |

An active goal keeps starting Codex turns until Codex marks it complete,
blocked, usage-limited, or budget-limited, or until it is paused or cleared.
Interrupting active goal work pauses the goal before interrupting its current
turn; detaching with `/quit` or `/exit` leaves it running. The goal and its
accounting survive client reconnection and runtime container restart. They
survive Pod replacement only when the Session workspace is persistent.

The terminal client shows live connecting, reconnecting, working,
waiting-for-input, and interrupting progress with elapsed time. After a
completed turn, both interactive and plain terminal output show a `Worked for
...` separator when the duration is known. A separate status bar beneath the
composer shows the Session name, agent type, model and effort when available,
working directory, git branch, and associated pull request number. Sessions
also show reported context use and cumulative input and output tokens once the
agent reports usage; Codex Sessions additionally show weekly limit remaining.
Less important status-bar items are omitted as the terminal narrows. The
model is the same runtime-reported value persisted in
`status.model`. The optional `kelos-console-server` includes a Sessions view
that shows the same live runtime details beneath its composer. The composer
accepts prompt drafts while a selected Session is still
Pending and enables sending after the runtime connects. It shows connection
status separately from working, waiting-for-input,
and interrupting progress, including elapsed time for active work, and adds the
duration to its separator after a completed turn. Duration labels are omitted
from retained history that does not contain event timestamps and from turns
interrupted by runtime recovery. Both clients use the same event stream and
provider conversation. Both clients can stream agent and tool activity, answer
user-input requests, and interrupt active work without ending the provider
conversation. While a turn is active, new submissions are combined into one
pending message that runs next. In the web client, use **Edit** to revise its
text before it starts or **Remove** to discard it; existing attachments remain
on the message when it is edited. In the terminal UI, press **Up** on an empty
composer to edit the pending message. Submitting the edit with no text removes
the pending message. Kelos first
asks the provider to interrupt gracefully, including
while the runtime is draining. If the request fails or the turn does not finish
within 10 seconds, Kelos marks the turn interrupted and restarts the provider.
Clients reconnect to the retained conversation after a provider restart, and a
pending message accepted before the restart resumes automatically.

Completed tool output is retained with Session history up to 512 KiB per tool
result. Larger results keep their beginning and end around an
`… output truncated …` marker. The terminal client strips terminal control
sequences and displays at most five rendered output rows, retaining head and
tail context. The web client displays a five-line preview and provides a control
to expand retained results that exceed five lines.

Selecting a Session in the web chat opens a bounded page at the latest retained
message. Use **Load earlier messages** to prepend the previous page without
moving away from the current scroll position. Reconnecting preserves an
intentional upward scroll position and the loaded conversation view. When the
request for the visible response has scrolled out of view, a compact **Current
request** link follows it while browsing history and scrolls back to the full
request when clicked. The link stays hidden while viewing file changes.

The Session sidebar shows compact relative activity times, and the selected
Session header shows whether it is active now, when it was last active, or when
it was created if runtime activity has not been reported. Hover over the
timestamp to see the exact time in the browser's locale and time zone; the
exact value is also available to assistive technology. A Session waiting for a
user response is labeled **Waiting for input** in the sidebar and header, with a
distinct red indicator in the sidebar.

Use **Rename** beside the selected Session's title or in a Session row's overflow
menu to set a display name for the web chat. The display name appears in the
sidebar, conversation header, and web runtime status without changing the
Session's Kubernetes resource name. An empty
display name restores the resource name. Display names are stored in the
`kelos.dev/session-display-name` annotation, so Session manifests can set the
same annotation directly. Values are trimmed and limited to 64 characters when
set through the web chat.

Sessions can be categorized into sections from the creation form or from the
selector beneath the selected Session's name. The selector applies an existing
section or **Unsectioned** immediately. Choosing **Create new section** reveals
an inline name field. Named sections are sorted alphabetically in the sidebar
until they are reordered. Drag a Session onto a section heading to move it, or
drag a heading to reorder sections. The arrow controls beside a heading provide
the same ordering action without drag and drop. **Unsectioned** can be reordered
like any named section and accepts dropped Sessions. Section order is stored in
the browser separately for each namespace, while Sessions retain their activity
order within a section. Assignments are stored in the
`kelos.dev/session-section` annotation, so Session manifests can set the same
annotation directly. New section names are trimmed and limited to 64 characters.

Web messages render safe Markdown: paragraphs and headings; emphasis,
strong text, strikethrough, and inline code; ordered, unordered, and task lists;
blockquotes and horizontal rules; HTTP(S) links; fenced or indented code blocks;
and pipe tables with optional column alignment. The renderer does not
interpret raw HTML or load embedded images. Fenced code may include a language
label, each code block has a copy control, and wide tables and long code lines
scroll horizontally. Tables that would render more than 10,000 cells remain
plain text.

If the Session Pod is deleted or evicted, clients reconnect after its
replacement is ready. Work active at the time of failure is reported as
interrupted and is not submitted again automatically. The terminal client also
does not retry a request whose delivery cannot be confirmed; it reports that
uncertainty so the user can decide whether to submit it again.

Existing Session StatefulSets reconcile their controller-managed fields when
controller defaults or referenced `Workspace` and `AgentConfig` resources
change. This includes plugin content. Fields that cannot be updated across all
supported Kubernetes versions—the governing Service name, selector, Pod
management policy, volume claim templates, and revision history limit—remain as
originally created.

When reconciliation changes the Pod template of an active Session, Kelos stops
accepting new turns and waits for accepted work to finish before replacing the
Pod. Pending user input delays the update until it is answered or interrupted.
Rejected turns are not retried automatically; submit them again after the
Session reconnects. Suspended Sessions remain at zero replicas while their
StatefulSet is updated and use the updated template when resumed. Changes to
`spec.worker.credentials`, `spec.worker.model`, and mutable fields under
`spec.worker.podOverrides` follow this process. Session Pods that use the default
runtime image also follow it when a Kelos upgrade changes that image; an
explicitly tagged or digested runtime image remains pinned.

`Active=True` means the runtime has an unfinished turn. Its reason is
`WaitingForInput` when the turn needs a user response and `TurnActive` while the
agent is working. `Active=False` means it is idle. Activity becomes `Unknown` when
it cannot be reported, such as while the Session Pod is being replaced. Clients
can use the `WaitingForInput` reason to highlight turns that need a response;
other condition reasons and messages are informational unless documented as
machine-readable below. The Console Sessions view orders Sessions by recent activity,
newest first, using `status.lastActivityTime`. Creation counts as the initial
activity until the runtime first reports its activity state. Replacing a Session
Pod does not change the order. The web client shows activity,
`status.model`, `status.branch`, and the pull request with a colored,
text-labeled state in both the Session sidebar and conversation header.

Idle suspension and deletion use the same idle period, measured from the later
of Session creation and `status.lastActivityTime`. Before either action, the
runtime stops accepting new turns and waits for any in-flight turn to finish.
When `spec.idlePolicy.suspendAfterSeconds` is reached, Kelos scales the runtime
to zero and reports `status.phase: Suspended` with the `IdlePolicyTriggered`
Ready-condition reason. `IdlePolicyTriggered` is stable and machine-readable so
clients can distinguish idle suspension from `spec.suspend: true`. Selecting the
Session in the web interface or starting a terminal connection resumes it; an
existing client's automatic reconnect does not. Kelos keeps the resume request
active until the client receives the runtime's conversation history. A resume
acknowledgement starts a fresh idle period and keeps the runtime protected for a
five-second connection grace. A resume request that is not acknowledged within
10 minutes expires and safely drains any running work before returning the
Session to idle suspension, allowing its deletion deadline to proceed. When both
idle actions are configured, `suspendAfterSeconds` must be less than
`deleteAfterSeconds`; deletion still occurs at `deleteAfterSeconds`, including
while the runtime is suspended.

When `spec.initialBranch` is set, workspace initialization fetches and checks
out that branch from `origin`, or creates it from `Workspace.spec.ref` when the
remote branch does not exist. This establishes the initial branch without
resetting a persistent workspace after the agent has started working.

When `spec.initialPrompt` is non-empty and the workspace has no conversation
history, the runtime records and submits the prompt before becoming ready.
This provides once-per-retained-conversation delivery, not once-per-Session
delivery. Retained history prevents the prompt from being submitted again after
a runtime restart. An `emptyDir` workspace loses that history when its Pod is
replaced, so the replacement starts a new conversation and submits the initial
prompt again.

When `spec.volumeClaimTemplate` is set, conversation history and workspace
changes survive Pod replacement. The claim remains until the Session is
deleted, after which PersistentVolume retention follows the StorageClass
reclaim policy. `Workspace.spec.setupCommand` runs again in each replacement
container. Persistent storage is recommended for durable Sessions. When the
field is omitted, the workspace uses `emptyDir`, which is primarily useful for
development because its history and changes do not survive Pod replacement.

Set `spec.suspend` to `true` or use the Suspend action in the shared web client
to suspend a Session without deleting it. Set the field back to `false` or use
the Resume action in the shared web client to resume.
A suspended Session rejects web connection attempts and the terminal client
waits for it to resume. Configured persistent workspace and conversation data
remain available across suspension. An `emptyDir` workspace is deleted with the
scaled-down Pod and starts empty after resuming.

Reset a Session with `kelos session reset NAME` or the reset action in the
shared web client's Session-row overflow menu. The same menu can rename or
delete that Session without selecting it first. Reset preserves the Session
resource and its immutable spec fields but permanently deletes retained
conversation history and workspace changes.
The controller stops the Session Pod before deleting its PersistentVolumeClaim,
then creates a fresh claim and replacement Pod. An `emptyDir` Session resets by
replacing only its Pod. Workspace initialization and
`Workspace.spec.setupCommand` run again, and a configured `spec.initialPrompt`
is submitted to the new conversation. The StorageClass reclaim policy controls
whether the old underlying PersistentVolume is deleted or retained.

The Console can inspect Kelos resources and create, list, reset, delete, and
connect to Sessions across namespaces while operating on one active namespace
at a time. Users can switch the active namespace live from the sidebar.
`consoleServer.defaultNamespace` sets its initial value, and resource inventory,
Session form options, and credential options are loaded only from the active namespace.
The Resources page shows the incoming and outgoing relationships for one
selected object, with a searchable inventory for inspecting the full namespace.
Selecting a Task from either resource view shows its agent container logs and
manifest. The Logs tab shows up to the latest 2,000 lines with a 2 MiB maximum
and can be refreshed while the Task is running. WorkerPool-backed Task views
include only the selected Task's segment from the recent shared worker Pod log.
The Console reports that the segment is unavailable when its markers are
outside the bounded log window.
Selecting an existing Session as a source populates both the form fields and the
editable YAML manifest. Settings that the form cannot represent remain editable
in YAML mode.
The creation form accepts provider, credentials, model, Workspace, AgentConfig
references, initial branch, initial prompt, and an optional persistent volume
claim. YAML mode server-side
applies one `kelos.dev/v1alpha2` Session manifest in the active namespace. The
manifest may include labels, annotations, `initialBranch`, `initialPrompt`, the complete
`WorkerSpec`, and an optional persistent volume claim.

## SessionSpawner

A SessionSpawner turns matching GitHub webhooks into durable Session
conversations. Each matching webhook delivery attempts to create one Session from
`spec.sessionTemplate`, using the same event and filter mechanism as a
webhook-driven TaskSpawner.

| Field | Description | Required |
|-------|-------------|----------|
| `spec.when.githubWebhook.events` | GitHub event types to listen for, using the same values as TaskSpawner | Yes |
| `spec.when.githubWebhook.repository` | Repository filter in `owner/repo` format; omit to accept any repository | No |
| `spec.when.githubWebhook.gatewayRef.name` | Bind this source to a [WebhookGateway](#webhookgateway) in the same namespace whose `spec.github` field is set. The per-source webhook server ignores this spawner when the reference is present | No |
| `spec.when.githubWebhook.excludeAuthors` | GitHub senders ignored before filter evaluation | No |
| `spec.when.githubWebhook.filters` | GitHub webhook filters using the same fields and OR semantics as TaskSpawner | No |
| `spec.credentials[].name` | Unique name for a credential distributed by this SessionSpawner. The name is recorded in the `kelos.dev/spawner-credential` label on generated Sessions | Yes when `spec.credentials` is set |
| `spec.credentials[].type` | Credential type (`api-key` or `oauth`) | Yes when `spec.credentials` is set |
| `spec.credentials[].secretRef.name` | Secret containing the agent credential | Yes when `spec.credentials` is set |
| `spec.sessionTemplate.worker` | Worker configuration copied to each Session | Yes |
| `spec.sessionTemplate.worker.workspaceRef.name` | Workspace cloned into each Session | Yes |
| `spec.sessionTemplate.initialBranch` | Go text/template rendered for the Session's initial branch | No |
| `spec.sessionTemplate.initialPrompt` | Go text/template submitted when the created Session starts | Yes |
| `spec.sessionTemplate.suspend` | Whether each created Session starts suspended (defaults to `false`) | No |
| `spec.sessionTemplate.volumeClaimTemplate` | Persistent workspace for each Session; recommended so conversation history survives Pod replacement | No |
| `spec.sessionTemplate.idlePolicy.suspendAfterSeconds` | Applied to each created Session as `Session.spec.idlePolicy.suspendAfterSeconds`: automatically stop its runtime after this much continuous idleness without changing `Session.spec.suspend`. When deletion is also configured, this value must be less than `deleteAfterSeconds` | No |
| `spec.sessionTemplate.idlePolicy.deleteAfterSeconds` | Applied to each created Session as `Session.spec.idlePolicy.deleteAfterSeconds`: automatically delete the Session once it has been continuously idle for this many seconds, which removes its workspace storage. Omit to never delete; zero deletes as soon as it goes idle. When suspension is also configured, this value must be greater than `suspendAfterSeconds` | No |
| `status.observedGeneration` | Most recent generation observed by the controller | Output |
| `status.totalSessions` | Current number of Sessions associated with this spawner | Output |
| `status.lastSessionName` | Session most recently created or confirmed to exist | Output |
| `status.lastDeliveryTime` | Time of the most recently attempted matching delivery | Output |
| `status.conditions[type=LastDeliverySucceeded]` | Result of the most recent attempted matching delivery; absent until one is attempted | Output |

`initialPrompt` and `initialBranch` support the same GitHub webhook template
values as TaskSpawner `promptTemplate` and `branch`. Templates use strict
missing-key handling; use `{{with index . "Branch"}}{{.}}{{else}}main{{end}}`
when an event may not provide `Branch`.

Session names use the same deterministic naming behavior as webhook-driven
TaskSpawners: the SessionSpawner name, event type, and delivery-ID hash are
combined and then truncated to the Kubernetes 63-character limit. A redelivery
therefore attempts to create the same Session and is treated as already
processed. If a long SessionSpawner name causes the delivery hash to be
truncated, distinct deliveries can resolve to the same name and the later
delivery is also treated as already processed. Use a shorter SessionSpawner
name until [collision-safe truncation](https://github.com/kelos-dev/kelos/issues/1527)
is implemented.
Created Sessions have a `kelos.dev/sessionspawner` label whose value is the
SessionSpawner UID, a `kelos.dev/sessionspawner-name` annotation for the
human-readable name, and a controller owner reference to the SessionSpawner.

When `spec.credentials` is configured, omit
`spec.sessionTemplate.worker.credentials`. Before creating each Session, the
SessionSpawner selects a credential uniformly at random. It copies the selected
credential into the generated Session and records the selection in the
`kelos.dev/spawner-credential` label. The Session keeps that credential for its
lifetime, including after suspension and resume. Assignments are independent,
so a small number of Sessions may not be distributed evenly.

```yaml
spec:
  credentials:
    - name: account-a
      type: oauth
      secretRef:
        name: claude-account-a
    - name: account-b
      type: oauth
      secretRef:
        name: claude-account-b
  sessionTemplate:
    worker:
      type: claude-code
      workspaceRef:
        name: my-workspace
    initialPrompt: "Handle issue #{{.Number}}: {{.Title}}"
```

`spec.credentials` is mutually exclusive with
`spec.sessionTemplate.worker.credentials`.

Before the first matching delivery, the SessionSpawner
`LastDeliverySucceeded` condition is absent. A creation failure returns an
error to the webhook sender so it can retry and sets the condition to `False`
with an actionable reason and message. Successful creation sets it to `True`;
an individual Session's runtime health is reported on that Session.

An `emptyDir` Session remains supported for development, but its conversation
history is lost on Pod replacement. Use
`spec.sessionTemplate.volumeClaimTemplate` for SessionSpawner workflows that
must retain a conversation across Pod recovery.

## WorkerPool

A WorkerPool manages a fleet of persistent worker pods backed by a StatefulSet. Tasks reference a WorkerPool via `spec.workerPoolRef` to execute on pre-warmed infrastructure instead of creating per-task Jobs.

When a pooled Task is cancelled, the worker kills the Task's agent process
tree and repeatedly sweeps for survivors before accepting another Task, so a
cancelled Task's processes do not keep consuming the worker's resources. The
sweep is bounded: if processes still remain after 10 seconds, the worker logs
the remaining count and accepts the next Task anyway, so cleanup is
best-effort rather than guaranteed.

A WorkerPool's Workspace may use either a PAT-style or a GitHub App secret.
GitHub App credentials are refreshed before they expire, so long-lived workers
keep repository access without restarting (see [Workspace authentication](#workspace-authentication)).

| Field | Description | Required |
|-------|-------------|----------|
| `spec.worker.type` | Agent type | Yes |
| `spec.worker.credentials` | Credentials for the workers | Yes |
| `spec.worker.workspaceRef.name` | Workspace reference | Yes |
| `spec.worker.model` | Default model for workers | No |
| `spec.worker.effort` | Default effort for workers | No |
| `spec.worker.image` | Custom agent image | No |
| `spec.worker.agentConfigRefs[].name` | AgentConfig references | No |
| `spec.worker.podOverrides` | Pod customization for worker pods | No |
| `spec.replicas` | Number of persistent worker pods (defaults to 1) | No |
| `spec.volumeClaimTemplate` | PersistentVolumeClaimSpec for each worker pod's storage | Yes |

## Workspace

| Field | Description | Required |
|-------|-------------|----------|
| `spec.repo` | Git repository URL to clone (HTTPS, git://, or SSH) | Yes |
| `spec.ref` | Branch, tag, or commit SHA to checkout (defaults to repo's default branch) | No |
| `spec.secretRef.name` | Secret containing credentials for git auth and `gh` CLI (see [authentication methods](#workspace-authentication) below) | No |
| `spec.ghproxy` | Enables the workspace-scoped ghproxy when set to `{}`; omitted or `null` disables it | No |
| `spec.remotes[].name` | Git remote name to add after cloning (must not be `"origin"`) | Yes (per remote) |
| `spec.remotes[].url` | Git remote URL | Yes (per remote) |
| `spec.files[].path` | Relative file path inside the repository (e.g., `CLAUDE.md`) | Yes (per file) |
| `spec.files[].content` | File content to write | Yes (per file) |
| `spec.setupCommand` | Exec-form command run in `/workspace/repo` after the repo is cloned, the ref is checked out, remotes are configured, and files are written, but before the agent process starts. Runs as the agent UID with all injected env vars; a non-zero exit fails the Task. Use `["sh", "-c", "<script>"]` for shell pipelines (see [Setup Command](#workspace-setup-command) below) | No |

Set `spec.ghproxy: {}` only for Workspaces that should run a workspace-scoped ghproxy. Existing Workspaces that need to keep ghproxy after upgrading must add that field; omitting it removes workspace ghproxy resources.

### Workspace Setup Command

Use `spec.setupCommand` to install language dependencies, prime build caches, or run any other prerequisite step that must complete before the agent inspects the codebase. The command follows the same exec-form convention as Kubernetes `container.command` and `lifecycle.postStart.exec.command` — the array is passed directly to `exec` with no shell interpretation.

```yaml
apiVersion: kelos.dev/v1alpha2
kind: Workspace
metadata:
  name: node-app-workspace
spec:
  repo: https://github.com/your-org/your-repo.git
  ref: main
  setupCommand: ["sh", "-c", "npm install && npm run build"]
```

Notes:

- Runs after the repo has been cloned and checked out, additional remotes have been added, and any `spec.files` entries have been written.
- Runs before the agent process starts; if it exits non-zero, the agent never runs and the Task fails.
- Executes in `/workspace/repo` as the agent UID (61100), with access to all built-in Kelos env vars and any `Task.spec.podOverrides.env` entries from the Task that references this Workspace.
- The default form is exec-style; for shell pipelines, environment expansion, or multi-step scripts, wrap the command with `["sh", "-c", "<script>"]`.

### Workspace Authentication

The workspace secret referenced by `spec.secretRef.name` supports two authentication methods:

**Personal Access Token (PAT):**

The secret contains a single key:

| Key | Description |
|-----|-------------|
| `GITHUB_TOKEN` | Personal access token for HTTPS git authentication and the GitHub `gh` CLI |

```bash
kubectl create secret generic github-token \
  --from-literal=GITHUB_TOKEN=<your-pat>
```

For repositories that require a username with PAT authentication, include the
username in `spec.repo` and store the PAT in the Secret's `GITHUB_TOKEN` key:

```yaml
spec:
  repo: https://username@bitbucket.example/scm/team/repo.git
  secretRef:
    name: github-token
```

Kelos preserves a username included in the repository URL. When the URL omits
the username, Kelos uses `x-access-token`, which is compatible with GitHub PATs
and GitHub App installation tokens.

**GitHub App (recommended for production/org use):**

The secret contains three keys. Kelos exchanges them for a short-lived
installation token:

| Key | Description |
|-----|-------------|
| `appID` | GitHub App ID |
| `installationID` | GitHub App installation ID for the target organization |
| `privateKey` | PEM-encoded RSA private key (PKCS1 or PKCS8) |

```bash
kubectl create secret generic github-app-creds \
  --from-literal=appID=12345 \
  --from-literal=installationID=67890 \
  --from-file=privateKey=my-app.private-key.pem
```

GitHub Apps are preferred over PATs for production use because they offer fine-grained permissions, higher rate limits, no dependency on a specific user account, and automatically expiring tokens.

Kelos refreshes the installation token before it expires. Tasks and WorkerPools
use the refreshed token without a Pod restart. Custom agent images must follow
the token-handling requirements in the [Agent Image Interface](agent-image-interface.md#github-token-freshness)
to receive refreshed credentials during long-running work.

## AgentConfig

| Field | Description | Required |
|-------|-------------|----------|
| `spec.agentsMD` | Agent instructions written to the agent's user-level instructions file, additive with repo files. The destination depends on the agent type: `~/.claude/CLAUDE.md` (Claude Code), `~/.gemini/GEMINI.md` (Gemini), `~/.codex/AGENTS.md` (Codex), `~/.config/opencode/AGENTS.md` (OpenCode), `~/.cursor/AGENTS.md` (Cursor) | No |
| `spec.plugins[].name` | Plugin name (used as directory name and namespace) | Yes (per plugin) |
| `spec.plugins[].skills[].name` | Skill name (becomes `skills/<name>/SKILL.md`) | Yes (per skill) |
| `spec.plugins[].skills[].content` | Skill content (markdown with frontmatter) | Yes (per skill) |
| `spec.plugins[].agents[].name` | Agent name (becomes `agents/<name>.md`) | Yes (per agent) |
| `spec.plugins[].agents[].content` | Agent content (markdown with frontmatter) | Yes (per agent) |
| `spec.skills[].source` | skills.sh package in `owner/repo` format for github.com (e.g., `vercel-labs/agent-skills`) or a full HTTPS git URL for private/GitHub Enterprise Server repositories (e.g., `https://ghe.example.com/org/private-skills.git`). Installed skills are exposed to the agent as a plugin named `skills-sh`; when `AgentConfig.spec.skills` is set, that name is reserved and must not be used in `AgentConfig.spec.plugins[].name` | Yes (per skill) |
| `spec.skills[].skill` | Specific skill name from the package (installs all if omitted) | No |
| `spec.skills[].secretRef.name` | Secret in the Task namespace containing a `GITHUB_TOKEN` key for HTTPS token auth when installing private skills.sh packages. Missing Secrets, missing `GITHUB_TOKEN`, or empty tokens fail the Task before Job creation. SSH deploy keys are not supported by this field | No |
| `spec.mcpServers[].name` | MCP server name (used as key in agent config) | Yes (per server) |
| `spec.mcpServers[].type` | Transport type: `stdio`, `http`, or `sse` | Yes (per server) |
| `spec.mcpServers[].command` | Executable to run (stdio only) | No |
| `spec.mcpServers[].args` | Command-line arguments (stdio only) | No |
| `spec.mcpServers[].url` | Server endpoint (http/sse only) | No |
| `spec.mcpServers[].headers` | HTTP headers (http/sse only) | No |
| `spec.mcpServers[].headersFrom.secretRef.name` | Secret whose data keys become HTTP header names and values (http/sse only). Values from `headersFrom` override `headers` on key conflicts | No |
| `spec.mcpServers[].env` | Environment variables for the server process (stdio only), as an array of Kubernetes `EnvVar` objects. Literal entries use `name` and `value` | No |
| `spec.mcpServers[].env[].valueFrom.secretKeyRef` | Secret key reference for an MCP env value. Set `name` and `key`; when `optional: true`, a missing Secret or key omits the variable instead of failing the Task | No |
| `spec.mcpServers[].env[].valueFrom.configMapKeyRef` | ConfigMap key reference for an MCP env value. Set `name` and `key`; when `optional: true`, a missing ConfigMap or key omits the variable instead of failing the Task | No |
| `spec.mcpServers[].env[].valueFrom` | Only `secretKeyRef` and `configMapKeyRef` are supported for MCP server env. Other Kubernetes `EnvVarSource` variants are rejected when a Task consumes the AgentConfig | No |
| `spec.mcpServers[].envFrom.secretRef.name` | Secret whose data keys become stdio MCP environment variable names and values. Values from `envFrom` override inline `env` on key conflicts | No |

## TaskSpawner

| Field | Description | Required |
|-------|-------------|----------|
| `spec.taskTemplate.workspaceRef.name` | Workspace resource (repo URL, auth, and clone target for spawned Tasks) | Yes (when using `githubIssues`, `githubPullRequests`, `githubWebhook`, `linearWebhook`, or `webhook`) |
| `spec.when.githubIssues.repo` | Override repository to poll for issues (in `owner/repo` format or full URL); defaults to workspace repo URL | No |
| `spec.when.githubIssues.labels` | Filter issues by labels | No |
| `spec.when.githubIssues.excludeLabels` | Exclude issues with these labels | No |
| `spec.when.githubIssues.state` | Filter by state: `open`, `closed`, `all` (default: `open`) | No |
| `spec.when.githubIssues.types` | Filter by type: `issues`, `pulls` (default: `issues`) | No |
| `spec.when.githubIssues.commentPolicy.triggerComment` | Requires a matching command in the issue body or comments to include the issue | No |
| `spec.when.githubIssues.commentPolicy.excludeComments` | Blocks items whose most recent matching command is an exclude comment | No |
| `spec.when.githubIssues.commentPolicy.allowedUsers` | Restrict comment control to specific GitHub usernames | No |
| `spec.when.githubIssues.commentPolicy.allowedTeams` | Restrict comment control to specific GitHub teams in `org/team-slug` format | No |
| `spec.when.githubIssues.commentPolicy.minimumPermission` | Minimum repo permission required for comment control: `read`, `triage`, `write`, `maintain`, or `admin` | No |
| `spec.when.githubIssues.assignee` | Filter by assignee username; use `"*"` for any assignee or `"none"` for unassigned | No |
| `spec.when.githubIssues.author` | Filter by issue author username | No |
| `spec.when.githubIssues.excludeAuthors` | Exclude issues created by any of these usernames (client-side) | No |
| `spec.when.githubIssues.priorityLabels` | Priority-order labels for task selection when `maxConcurrency` is set; index 0 is highest priority | No |
| `spec.when.githubIssues.reporting.enabled` | **Deprecated:** use `reporting.comments`. Posts status comments back to the GitHub issue using `PerTask` mode | No |
| `spec.when.githubIssues.reporting.comments.mode` | Enables status comments back to the GitHub issue. `PerTask` (default) creates one comment for each Task; `Sticky` maintains one comment per TaskSpawner and issue across Tasks | No |
| `spec.when.githubIssues.pollInterval` | Per-source poll interval (e.g., `"30s"`, `"5m"`). Defaults to `5m` when omitted | No |
| `spec.when.githubPullRequests.repo` | Override repository to poll for PRs (in `owner/repo` format or full URL); defaults to workspace repo URL | No |
| `spec.when.githubPullRequests.labels` | Filter pull requests by labels | No |
| `spec.when.githubPullRequests.excludeLabels` | Exclude pull requests with these labels | No |
| `spec.when.githubPullRequests.state` | Filter by state: `open`, `closed`, `all` (default: `open`) | No |
| `spec.when.githubPullRequests.reviewState` | Filter by aggregated review state: `approved`, `changes_requested`, `any` (default: `any`) | No |
| `spec.when.githubPullRequests.commentPolicy.triggerComment` | Requires a matching command in the PR body or comments to include the PR | No |
| `spec.when.githubPullRequests.commentPolicy.excludeComments` | Blocks PRs whose most recent matching command is an exclude comment | No |
| `spec.when.githubPullRequests.commentPolicy.allowedUsers` | Restrict comment control to specific GitHub usernames | No |
| `spec.when.githubPullRequests.commentPolicy.allowedTeams` | Restrict comment control to specific GitHub teams in `org/team-slug` format | No |
| `spec.when.githubPullRequests.commentPolicy.minimumPermission` | Minimum repo permission required for comment control: `read`, `triage`, `write`, `maintain`, or `admin` | No |
| `spec.when.githubPullRequests.author` | Filter by PR author username | No |
| `spec.when.githubPullRequests.excludeAuthors` | Exclude PRs opened by any of these usernames (client-side) | No |
| `spec.when.githubPullRequests.draft` | Filter by draft state | No |
| `spec.when.githubPullRequests.priorityLabels` | Priority-order labels for task selection when `maxConcurrency` is set; index 0 is highest priority | No |
| `spec.when.githubPullRequests.reporting.enabled` | **Deprecated:** use `reporting.comments`. Posts status comments back to the GitHub pull request using `PerTask` mode | No |
| `spec.when.githubPullRequests.reporting.comments.mode` | Enables status comments back to the GitHub pull request. `PerTask` (default) creates one comment for each Task; `Sticky` maintains one comment per TaskSpawner and pull request across Tasks | No |
| `spec.when.githubPullRequests.reporting.checks.name` | Creates a GitHub Check Run for each PR task, enabling branch protection and merge queue integration. Sets the Check Run name (defaults to `"Kelos: <taskspawner-name>"`, max 100 chars). The token used by the workspace must have `checks:write` permission. Not supported on `githubIssues` (rejected by CEL validation). | No |
| `spec.when.githubPullRequests.filePatterns.include` | Doublestar globs for changed files to include after `exclude` patterns are removed. When omitted, any remaining changed file passes | No |
| `spec.when.githubPullRequests.filePatterns.exclude` | Doublestar globs for changed files to remove before include matching. A PR with no remaining changed files is skipped | No |
| `spec.when.githubPullRequests.pollInterval` | Per-source poll interval (e.g., `"30s"`, `"5m"`). Defaults to `5m` when omitted | No |
| `spec.when.githubWebhook.events` | GitHub event types to listen for (e.g., `"issues"`, `"pull_request"`, `"push"`, `"issue_comment"`) | Yes (when using githubWebhook) |
| `spec.when.githubWebhook.repository` | Restrict webhooks to a specific repository (`owner/repo` format); if empty, webhooks from any repository are accepted | No |
| `spec.when.githubWebhook.excludeAuthors` | Exclude webhook events sent by any of these usernames; applied before filter evaluation | No |
| `spec.when.githubWebhook.filters[].event` | GitHub event type this filter applies to | Yes (per filter) |
| `spec.when.githubWebhook.filters[].action` | Filter by webhook action (e.g., `"opened"`, `"created"`, `"submitted"`) | No |
| `spec.when.githubWebhook.filters[].labels` | Require the issue/PR to have all of these labels | No |
| `spec.when.githubWebhook.filters[].excludeLabels` | Exclude issues/PRs with any of these labels | No |
| `spec.when.githubWebhook.filters[].state` | Filter by issue/PR state (`"open"`, `"closed"`) | No |
| `spec.when.githubWebhook.filters[].branch` | Filter push and create (ref_type=branch) events by branch name (exact match or glob) | No |
| `spec.when.githubWebhook.filters[].tag` | Filter create (ref_type=tag) and release events by tag name (exact match or glob) | No |
| `spec.when.githubWebhook.filters[].conclusion` | Filter `check_run` events by the check run's conclusion. One of `success`, `failure`, `cancelled`, `timed_out`, `action_required`, `neutral`, `skipped`, `stale`. Ignored for other events | No |
| `spec.when.githubWebhook.filters[].checkName` | Filter `check_run` events by the check run's name (exact match or glob, e.g. `"lint"`, `"build-*"`). Ignored for other events | No |
| `spec.when.githubWebhook.filters[].draft` | Filter PRs by draft status | No |
| `spec.when.githubWebhook.filters[].author` | Filter by the event sender's username | No |
| `spec.when.githubWebhook.filters[].excludeAuthors` | Exclude events sent by any of these usernames | No |
| `spec.when.githubWebhook.filters[].filePatterns.include` | Doublestar globs for changed files to include after `exclude` patterns are removed. Applies to `push` and `pull_request` webhook filters | No |
| `spec.when.githubWebhook.filters[].filePatterns.exclude` | Doublestar globs for changed files to remove before include matching. Events with no remaining changed files are skipped | No |
| `spec.when.githubWebhook.filters[].bodyContains` | **Deprecated.** Filter by case-sensitive substring match on the comment/review body. Use `bodyPattern` instead | No |
| `spec.when.githubWebhook.filters[].bodyPattern` | Require the comment/review body to match a Go re2 regular expression. When combined with `excludeBodyPatterns`, the body must match this pattern AND not match any exclude entry | No |
| `spec.when.githubWebhook.filters[].excludeBodyPatterns` | Exclude events whose comment/review body matches any of these Go re2 regular expressions (OR semantics) | No |
| `spec.when.githubWebhook.filters[].commentOn` | Scope `issue_comment` events to comments posted on a specific subject: `"Issue"` matches plain issues, `"PullRequest"` matches pull requests. Empty matches both. Ignored for other events | No |
| `spec.when.githubWebhook.reporting.enabled` | **Deprecated:** use `reporting.comments`. Posts status comments back to the originating issue or PR using `PerTask` mode | No |
| `spec.when.githubWebhook.reporting.comments.mode` | Enables status comments back to the originating issue or PR. `PerTask` (default) creates one comment for each Task; `Sticky` maintains one comment per TaskSpawner and originating issue or PR across Tasks | No |
| `spec.when.githubWebhook.reporting.checks.name` | Creates a GitHub Check Run for tasks spawned by PR-related webhook events, enabling branch protection and merge queue integration. Sets the Check Run name (defaults to `"Kelos: <taskspawner-name>"`, max 100 chars). The token used by the workspace must have `checks:write` permission. Requires `events` to include at least one of `pull_request`, `pull_request_review`, `pull_request_review_comment`, or `pull_request_target`; alternatively, `events` may include `issue_comment` when at least one filter applies to that event and every `issue_comment` filter sets `commentOn: PullRequest` (enforced by CEL validation). | No |
| `spec.when.githubWebhook.gatewayRef.name` | Bind this source to a [WebhookGateway](#webhookgateway) in the same namespace whose `spec.github` field is set. The per-source webhook server ignores this spawner when the reference is present | No |
| `spec.when.linearWebhook.types` | Linear resource types to listen for (e.g., `"Issue"`, `"Comment"`) | Yes (when using linearWebhook) |
| `spec.when.linearWebhook.filters[].type` | Scope filter to a specific resource type | No |
| `spec.when.linearWebhook.filters[].action` | Filter by webhook action: `create`, `update`, or `remove` | No |
| `spec.when.linearWebhook.filters[].states` | Filter by workflow state names (e.g., `"Todo"`, `"In Progress"`) | No |
| `spec.when.linearWebhook.filters[].labels` | Require the issue to have all of these labels | No |
| `spec.when.linearWebhook.filters[].excludeLabels` | Exclude issues with any of these labels | No |
| `spec.when.linearWebhook.gatewayRef.name` | Bind this source to a [WebhookGateway](#webhookgateway) in the same namespace whose `spec.linear` field is set. The per-source webhook server ignores this spawner when the reference is present | No |
| `spec.when.slack.channels` | Restrict which Slack channels the bot listens in (channel IDs like `"C0123456789"`); when empty, listens in all invited channels | No |
| `spec.when.slack.excludeChannels` | Channel IDs this spawner never matches; exclusion always wins, so a channel listed here is rejected even when `channels` is empty (all channels) or names the same channel. Unlike `excludePatterns`, it also applies to slash commands. Direct-message IDs (`"D0123456789"`) are accepted here even though `channels` does not accept them. Filters after delivery, like `channels` — the bot stays in the channel. Stored only in `v1alpha2`; a client that writes the spawner through `v1alpha1` preserves the exclusion in an annotation, so stripping that annotation drops it | No |
| `spec.when.slack.botMessagePolicy` | Controls whether bot-originated messages can trigger this spawner: `None` (default) rejects all bot messages, `All` allows all including self, `OthersOnly` allows other bots but rejects the bot's own output to prevent self-trigger loops | No |
| `spec.when.slack.triggers[].pattern` | RE2 regex matched against message text (unanchored); leading `<@USER_ID>` mentions are stripped before matching; bot mention required unless `mentionOptional` is set; multiple triggers use OR semantics; when empty, every bot mention fires | No |
| `spec.when.slack.triggers[].mentionOptional` | When `true`, fire on pattern match alone without requiring a bot @-mention | No |
| `spec.when.slack.excludePatterns` | RE2 regex patterns that reject messages when any pattern matches (OR semantics); leading `<@USER_ID>` mentions are stripped before matching; does not apply to slash commands | No |
| `spec.when.webhook.source` | Short identifier for the generic webhook source (lowercase alphanumeric with optional hyphens). On the per-source server it determines the URL path (`/webhook/<source>`); that endpoint is unauthenticated (see [#1040](https://github.com/kelos-dev/kelos/issues/1040)). Set `gatewayRef` to route through a [WebhookGateway](#webhookgateway) | Yes (when using webhook) |
| `spec.when.webhook.fieldMapping` | Map of template variable name → JSONPath expression evaluated against the request body. Each key becomes a top-level template variable. Lowercase `id`, `title`, `body`, `url` are also exposed as `{{.ID}}`, `{{.Title}}`, `{{.Body}}`, `{{.URL}}`. The `id` key is required (used for delivery deduplication and Task naming) | Yes (when using webhook) |
| `spec.when.webhook.filters[].field` | JSONPath expression selecting the payload field to match. A field missing from the payload fails the filter (the delivery is skipped); a malformed JSONPath expression skips the spawner for that delivery and logs an error | Yes (per filter) |
| `spec.when.webhook.filters[].value` | Require an exact string match against the extracted field value (mutually exclusive with `pattern`) | Conditional |
| `spec.when.webhook.filters[].pattern` | Require a regex match against the extracted field value (mutually exclusive with `value`) | Conditional |
| `spec.when.webhook.excludeFilters[].field` | JSONPath expression selecting the payload field to match. A delivery matching any exclude filter is skipped (OR semantics), even when every entry in `filters` matched. Unlike `filters[].field`, a field missing from the payload does not match and so does not exclude the delivery; a malformed JSONPath expression skips the spawner for that delivery and logs an error | Yes (per exclude filter) |
| `spec.when.webhook.excludeFilters[].value` | Exclude the delivery on an exact string match against the extracted field value (mutually exclusive with `pattern`) | Conditional |
| `spec.when.webhook.excludeFilters[].pattern` | Exclude the delivery on a regex match against the extracted field value (mutually exclusive with `value`) | Conditional |
| `spec.when.webhook.gatewayRef.name` | Bind this source to a [WebhookGateway](#webhookgateway) in the same namespace whose `spec.generic` field is set. Generic gateway deliveries remain unauthenticated, and the per-source server ignores this spawner when the reference is present | No |
| `spec.when.jira.pollInterval` | Per-source poll interval (e.g., `"30s"`, `"5m"`). Defaults to `5m` when omitted | No |
| `spec.when.cron.schedule` | Cron schedule expression (e.g., `"0 * * * *"`) | Yes (when using cron) |
| `spec.credentials[].name` | Unique name for a credential distributed by this TaskSpawner. The name is recorded in the `kelos.dev/spawner-credential` label on generated Tasks | Yes when `spec.credentials` is set |
| `spec.credentials[].type` | Credential type (`api-key` or `oauth`) | Yes when `spec.credentials` is set |
| `spec.credentials[].secretRef.name` | Secret containing the agent credential. The required Secret key depends on the agent and credential type (see [Task Credential Secret Format](#task-credential-secret-format)) | Yes when `spec.credentials` is set |
| `spec.taskTemplate.worker` | Execution environment for spawned Tasks (see [WorkerSpec](#workerspec)). When used alone, spawned Tasks create Jobs. Mutually exclusive with `workerPoolRef` | One of worker, workerPoolRef, or type |
| `spec.taskTemplate.workerPoolRef.name` | WorkerPool for persistent execution | One of worker, workerPoolRef, or type |
| `spec.taskTemplate.type` | **(Deprecated)** Agent type — use `taskTemplate.worker.type` instead | One of worker, workerPoolRef, or type |
| `spec.taskTemplate.credentials` | **(Deprecated)** Credentials — use `taskTemplate.worker.credentials` instead | Required with type unless `spec.credentials` is set |
| `spec.taskTemplate.model` | **(Deprecated)** Model override — use `taskTemplate.worker.model` instead | Legacy |
| `spec.taskTemplate.effort` | **(Deprecated)** Reasoning effort — use `taskTemplate.worker.effort` instead | Legacy |
| `spec.taskTemplate.image` | **(Deprecated)** Custom agent image — use `taskTemplate.worker.image` instead | Legacy |
| `spec.taskTemplate.workspaceRef.name` | **(Deprecated)** Workspace reference — use `taskTemplate.worker.workspaceRef` instead | Legacy |
| `spec.taskTemplate.agentConfigRefs[].name` | **(Deprecated)** AgentConfig references — use `taskTemplate.worker.agentConfigRefs` instead | Legacy |
| `spec.taskTemplate.promptTemplate` | Go text/template for prompt (see [template variables](#prompttemplate-variables) below) | No |
| `spec.taskTemplate.dependsOn` | Task names that spawned Tasks depend on. Not supported with `workerPoolRef` | No |
| `spec.taskTemplate.branch` | Git branch template for spawned Tasks (supports Go template variables, e.g., `kelos-task-{{.Number}}`). Not supported with `workerPoolRef` | No |
| `spec.taskTemplate.nameTemplate` | Go text/template for the spawned Task's name (overrides the default naming below). The rendered value is lowercased, sanitized to a valid resource name, and truncated to 63 characters. Use a deterministic template (e.g. `{{.Number}}`) to deduplicate Tasks: work items that render to the same name reuse the existing Task instead of creating a duplicate — the recommended way to avoid duplicate Tasks from multiple GitHub webhook deliveries for the same pull request. Names must be unique across the whole namespace; a collision with a Task owned by a different TaskSpawner (or any unrelated Task) is an error, not deduplication (see [Generated Task Names](#generated-task-names)). Keep the identifying part within the first 63 characters. `.Context.NAME` is not available to `nameTemplate` on any source — a Task's identity must not depend on mutable external data | No |
| `spec.taskTemplate.ttlSecondsAfterFinished` | Auto-delete spawned tasks after N seconds | No |
| `spec.taskTemplate.podFailurePolicy` | Kubernetes Job pod failure policy copied to spawned Tasks as `Task.spec.podFailurePolicy` | No |
| `spec.taskTemplate.podOverrides` | **(Deprecated)** Pod customization — use `taskTemplate.worker.podOverrides` instead | Legacy |
| `spec.taskTemplate.metadata.labels` | Labels merged into spawned Tasks; values support the same Go template variables as `branch`/`promptTemplate`; `kelos.dev/taskspawner` and, when `spec.credentials` is configured, `kelos.dev/spawner-credential` are reserved and override conflicting user values | No |
| `spec.taskTemplate.metadata.annotations` | Annotations merged into spawned Tasks; values support the same Go template variables as `branch`/`promptTemplate`; source annotations (e.g. `kelos.dev/source-kind`) are applied after rendering and override conflicting user values | No |
| `spec.taskTemplate.contextSources` | External data sources fetched in parallel before task creation; each source's value is exposed as `{{.Context.NAME}}` in `branch`, `promptTemplate`, and `metadata` templates — but not in `nameTemplate` (see [Context Sources](#context-sources) below). Maximum 8 entries; names must be unique | No |
| `spec.taskTemplate.upstreamRepo` | Upstream repository in `owner/repo` format; injected as `KELOS_UPSTREAM_REPO` into the agent container. Typically auto-derived from `githubIssues.repo`/`githubPullRequests.repo`, but can be set explicitly for fork workflows | No |
| `spec.maxConcurrency` | Limit max concurrent running tasks (important for cost control) | No |
| `spec.maxTotalTasks` | Lifetime limit on total tasks created by this spawner | No |
| `spec.suspend` | Pause the spawner without deleting it; resume with `spec.suspend: false` (default: `false`) | No |

When `spec.credentials` is configured, omit credentials from
`spec.taskTemplate`. Before creating each Task, the TaskSpawner selects the
credential uniformly at random. It copies the selected credential into the
generated Task and records the selection in the
`kelos.dev/spawner-credential` label. The Task keeps that credential for its
lifetime, including Job retries. Assignments are independent, so a small number
of Tasks may not be distributed evenly.

Choose exactly one execution source: `spec.taskTemplate.worker`,
`spec.taskTemplate.workerPoolRef`, or the legacy `spec.taskTemplate.type`.
Inline `worker` and legacy `type` sources require credentials from their
corresponding template field or from `spec.credentials`.

```yaml
spec:
  credentials:
    - name: account-a
      type: oauth
      secretRef:
        name: claude-account-a
    - name: account-b
      type: oauth
      secretRef:
        name: claude-account-b
  taskTemplate:
    worker:
      type: claude-code
      workspaceRef:
        name: my-workspace
    promptTemplate: "Fix issue #{{.Number}}: {{.Title}}"
```

`spec.credentials` is mutually exclusive with
`spec.taskTemplate.worker.credentials`, deprecated
`spec.taskTemplate.credentials`, and `spec.taskTemplate.workerPoolRef`.

### Generated Task Names

For `githubIssues`, `githubPullRequests`, `jira`, and `cron` sources, Kelos first
lowercases the work item ID when forming the Task name:
`<TaskSpawner name>-<lowercase work item ID>`.

Lowercasing the Task name does not change the source data exposed to templates
and logs. In particular, `{{.ID}}` remains the raw work item ID (for example,
`ENG-42`). Webhook-backed TaskSpawners use delivery-based Task names instead.

When `spec.taskTemplate.nameTemplate` is set, it overrides these default schemes
for all sources: the Task name is the rendered template (lowercased, sanitized,
and truncated to 63 characters). A deterministic template deduplicates Tasks —
work items or webhook deliveries that render to the same name reuse the existing
Task instead of creating a duplicate.

Deduplication is **ownership-scoped**, not per spawner name. A Task name is unique
across the entire namespace, so a rendered name is reused only when the existing
Task belongs to the same TaskSpawner (matched by controller owner-reference UID,
or the `kelos.dev/taskspawner` label for ownerless legacy Tasks). If a rendered
name collides with a Task owned by a different TaskSpawner — or any unrelated Task
using that name — Kelos does **not** silently reuse it: the webhook path returns
an error (HTTP 500, so the delivery is retried), the polling path records a
`DiscoveryError` condition and failure on the TaskSpawner, and the Slack path
surfaces the error in the server logs. Ensure each TaskSpawner's
`nameTemplate` renders names that are unique across the namespace (for example by
including the TaskSpawner-specific prefix, and a repository qualifier when one
endpoint serves multiple repositories).

### Manual Task Creation

Run a standalone Task from any TaskSpawner's `taskTemplate`:

```bash
kelos run --from taskspawner/daily-audit
kelos run --from taskspawner/issue-worker -f values.yaml
```

The values file is a YAML or JSON object whose top-level keys are exposed
directly to the Go template. Use `-f -` to read it from stdin. For example:

```yaml
ID: "42"
Number: 42
Title: Fix the login timeout
Body: Reproduce and fix the timeout under load.
Kind: Issue
```

Kelos supplies `TriggerType: manual` and the current UTC time as
`TriggerTime`. Cron TaskSpawners also receive the current UTC time as `Time`
and their configured expression as `Schedule`; these cron values cannot be
overridden by `-f`. A static template or a cron template that only uses the
supplied defaults does not require a values file. Rendering fails if a template
uses a key that is not supplied.

Manual creation bypasses source filters and creates a standalone Task. It does
not apply `spec.suspend`, `spec.maxConcurrency`, or `spec.maxTotalTasks`, does
not enable source reporting, and does not update TaskSpawner status. The Task
has no TaskSpawner owner reference or `kelos.dev/taskspawner` label. Instead,
Kelos records its origin with `kelos.dev/created-from-taskspawner`,
`kelos.dev/trigger-type`, and `kelos.dev/trigger-time` annotations. Manual runs
also ignore `spec.taskTemplate.nameTemplate`: the Task is named from `--name`
when provided, otherwise `<taskspawner>-manual-<suffix>`.

Configured `contextSources` are fetched after the values are resolved, matching
automatic Task creation. `--dry-run` still connects to the cluster to read the
TaskSpawner and any Secrets referenced by context sources.

<a id="prompttemplate-variables"></a>

### promptTemplate Variables

The `promptTemplate` field uses Go `text/template` syntax. Available variables depend on the source type:

| Variable | Description | GitHub Issues | GitHub Pull Requests | GitHub Webhook | Jira | Linear Webhook | Generic Webhook | Cron |
|----------|-------------|---------------|----------------------|----------------|------|----------------|-----------------|------|
| `{{.ID}}` | Unique identifier | Issue/PR number as string (e.g., `"42"`) | Pull request number as string | Issue/PR number or commit ID | Jira issue key (e.g., `"ENG-42"`) | Linear resource ID | Mapped `id` field (required) | Date-time string (e.g., `"20260207-0900"`) |
| `{{.Number}}` | Issue or PR number | Issue/PR number (e.g., `42`) | Pull request number | Issue/PR number (when available) | Numeric suffix of the Jira key (e.g., `42` for `ENG-42`); `0` if the key has no `-N` suffix | Empty | Empty | `0` |
| `{{.Title}}` | Title of the work item | Issue/PR title | Pull request title | Issue/PR title or "Push to &lt;branch&gt;" | Issue summary | Resource title | Mapped `title` field (if present) | Trigger time (RFC3339) |
| `{{.Body}}` | Body text | Issue/PR body | Pull request body | Issue/PR body | Empty (description is not fetched; tracked in [#990](https://github.com/kelos-dev/kelos/issues/990)) | Empty | Mapped `body` field (if present) | Empty |
| `{{.URL}}` | URL to the source item | GitHub HTML URL | GitHub PR URL | Issue/PR HTML URL | Jira browse URL (e.g., `https://your-org.atlassian.net/browse/ENG-42`) | Empty | Mapped `url` field (if present) | Empty |
| `{{.Labels}}` | Comma-separated labels | Issue/PR labels | Pull request labels | Empty | Issue labels | Issue labels | Empty | Empty |
| `{{.Comments}}` | Concatenated comments | Issue/PR comments | PR conversation comments | Empty | Issue comments | Empty | Empty | Empty |
| `{{.Kind}}` | Type of work item | `"Issue"` or `"PR"` | `"PR"` | `"webhook"` | Jira issue type name (e.g., `"Bug"`, `"Story"`), or `"Issue"` if empty | `"LinearWebhook"` | `"GenericWebhook"` | `"Issue"` |
| `{{.Event}}` | GitHub event type | Empty | Empty | Event type (e.g., `"issues"`, `"pull_request"`, `"push"`) | Empty | Empty | Empty | Empty |
| `{{.Action}}` | Webhook action | Empty | Empty | Action (e.g., `"opened"`, `"created"`, `"submitted"`) | Empty | Action (e.g., `"create"`, `"update"`, `"remove"`) | Empty | Empty |
| `{{.Sender}}` | Event sender username | Empty | Empty | Username of person who triggered the event | Empty | Empty | Empty | Empty |
| `{{.Branch}}` | Git branch to update | Empty | PR head branch (e.g., `"kelos-task-42"`) | PR source branch or push branch | Empty | Empty | Empty | Empty |
| `{{.Ref}}` | Git ref | Empty | Empty | Git ref for push events (e.g., `"refs/heads/main"`) or create events (ref name) | Empty | Empty | Empty | Empty |
| `{{.Tag}}` | Tag name | Empty | Empty | Tag name for `create` (ref_type=tag) and `release` events | Empty | Empty | Empty | Empty |
| `{{.RefType}}` | Ref type for create events | Empty | Empty | `"branch"`, `"tag"`, or `"repository"` (create events only) | Empty | Empty | Empty | Empty |
| `{{.Repository}}` | Full repository name | Empty | Empty | Repository in `owner/repo` format | Empty | Empty | Empty | Empty |
| `{{.RepositoryOwner}}` | Repository owner | Empty | Empty | Repository owner login | Empty | Empty | Empty | Empty |
| `{{.RepositoryName}}` | Repository name | Empty | Empty | Repository name only | Empty | Empty | Empty | Empty |
| `{{.Payload}}` | Raw event payload | Empty | Empty | Full parsed GitHub webhook payload | Empty | Full parsed Linear webhook payload | Full parsed JSON body | Empty |
| `{{.ReviewState}}` | Aggregated review state | Empty | `approved`, `changes_requested`, or empty | Empty | Empty | Empty | Empty | Empty |
| `{{.ReviewComments}}` | Formatted inline review comments | Empty | Inline PR review comments | Empty | Empty | Empty | Empty | Empty |
| `{{.Type}}` | Resource type | Empty | Empty | Empty | Empty | Resource type (e.g., `"Issue"`, `"Comment"`) | Empty | Empty |
| `{{.State}}` | Workflow state | Empty | Empty | Empty | Empty | Current state name (e.g., `"Todo"`, `"In Progress"`) | Empty | Empty |
| `{{.IssueID}}` | Parent issue ID | Empty | Empty | Empty | Empty | Parent issue ID (Comment events only) | Empty | Empty |
| `{{.CommentBody}}` | Comment or review body | Empty | Empty | Comment/review body (`issue_comment`, `pull_request_review`, `pull_request_review_comment` events) | Empty | Empty | Empty | Empty |
| `{{.CommentURL}}` | Comment or review URL | Empty | Empty | Comment/review HTML URL (`issue_comment`, `pull_request_review`, `pull_request_review_comment` events) | Empty | Empty | Empty | Empty |
| `{{.ChangedFiles}}` | Changed file paths (list) | Empty | Empty | Push: files from the payload. PR: changed files, but **only** when a matching filter's `filePatterns` forced a fetch (otherwise empty; see note below). Iterate with `{{range .ChangedFiles}}` | Empty | Empty | Empty | Empty |
| `{{.CheckName}}` | Check run name | Empty | Empty | Check run name (`check_run` events, e.g. `"lint"`) | Empty | Empty | Empty | Empty |
| `{{.Conclusion}}` | Check run conclusion | Empty | Empty | Check run conclusion (`check_run` events, e.g. `"failure"`) | Empty | Empty | Empty | Empty |
| `{{.CheckRunURL}}` | Check run URL | Empty | Empty | Link to the check run / CI logs (`check_run` events) | Empty | Empty | Empty | Empty |
| `{{.HeadSHA}}` | Head commit SHA | Empty | Empty | Commit SHA under test (`check_run` events) | Empty | Empty | Empty | Empty |
| `{{.CheckApp}}` | Check app name | Empty | Empty | App that produced the check (`check_run` events, e.g. `"GitHub Actions"`) | Empty | Empty | Empty | Empty |
| `{{.Time}}` | Trigger time (RFC3339) | Empty | Empty | Empty | Empty | Empty | Empty | Cron tick time (e.g., `"2026-02-07T09:00:00Z"`) |
| `{{.Schedule}}` | Cron schedule expression | Empty | Empty | Empty | Empty | Empty | Empty | Schedule string (e.g., `"0 * * * *"`) |

> **Generic Webhook only:** any additional keys declared in `spec.when.webhook.fieldMapping` are also exposed as top-level template variables (e.g., `fieldMapping: {severity: "$.level"}` makes `{{.severity}}` available).

> **`{{.ChangedFiles}}` and `filePatterns`:** For pull request webhook events, the changed-file list is fetched lazily and only when a filter's `filePatterns` needs it to decide a match. As a result, `{{.ChangedFiles}}` is populated for PR events **only when the matching filter declares `filePatterns`**; without it, `{{.ChangedFiles}}` renders as an empty list. Push events populate `{{.ChangedFiles}}` from the payload regardless.

> **Context sources:** when `spec.taskTemplate.contextSources` is configured, each entry's fetched value is exposed as `{{.Context.NAME}}` (e.g., a source named `jira` is available as `{{.Context.jira}}`). The same `.Context` map is also available in `spec.taskTemplate.branch` and `spec.taskTemplate.metadata` templates. See [Context Sources](#context-sources) for details.

<a id="context-sources"></a>

### Context Sources

`spec.taskTemplate.contextSources` lets a TaskSpawner fetch external data at task-creation time and inject the result as template variables. For each work item, all of its sources are fetched in parallel during the spawning cycle, and the fetched value becomes available as `{{.Context.NAME}}` in `promptTemplate`, `branch`, and `metadata` templates. A TaskSpawner may declare up to 8 sources; names must be unique and match `^[a-zA-Z][a-zA-Z0-9_]*$`.

| Field | Description | Required |
|-------|-------------|----------|
| `spec.taskTemplate.contextSources[].name` | Identifier used as the template key (`{{.Context.<name>}}`). Must match `^[a-zA-Z][a-zA-Z0-9_]*$`, 1–64 characters | Yes |
| `spec.taskTemplate.contextSources[].http` | HTTP(S) source configuration. Currently the only supported source kind; exactly one source kind must be set | Yes |
| `spec.taskTemplate.contextSources[].http.url` | Endpoint to fetch. Supports Go `text/template` variables from the work item (e.g., `https://api.example.com/items/{{.Number}}`). HTTPS is required unless `allowInsecure` is set | Yes (per source) |
| `spec.taskTemplate.contextSources[].http.method` | HTTP method: `GET` or `POST` (default: `GET`) | No |
| `spec.taskTemplate.contextSources[].http.headers` | Static HTTP headers. Values support Go `text/template` variables from the work item | No |
| `spec.taskTemplate.contextSources[].http.headersFrom` | HTTP header values sourced from Kubernetes Secrets in the same namespace as the TaskSpawner. Each entry sets `header` to the HTTP header name, `secretName` to the Secret name, and `secretKey` to the key within the Secret. Merged with `headers`; `headersFrom` wins on conflict. Maximum 16 entries | No |
| `spec.taskTemplate.contextSources[].http.githubAppAuth.secretRef.name` | Name of a Secret in the same namespace as the TaskSpawner holding GitHub App credentials (`appID`, `installationID`, `privateKey` keys). When set, an `Authorization: token <installation-token>` header is minted and added to the request. An explicit `Authorization` header from `headers`/`headersFrom` takes precedence and disables this. The token is reused across work items until it nears expiry | No |
| `spec.taskTemplate.contextSources[].http.githubAppAuth.apiBaseURL` | GitHub API base URL used to mint installation tokens (default: `https://api.github.com`). Must be an HTTPS URL, at most 2048 characters. Set for GitHub Enterprise Server, e.g. `https://github.example.com/api/v3` | No |
| `spec.taskTemplate.contextSources[].http.body` | Request body template (Go `text/template`); used with `POST` | No |
| `spec.taskTemplate.contextSources[].http.responseFilter.type` | Filter language for extracting a subset of the response. Currently only `JSONPath` is supported | No |
| `spec.taskTemplate.contextSources[].http.responseFilter.expression` | Filter expression (e.g., `$.data.value` for JSONPath). When set, only the extracted value is stored; otherwise the entire response body is used | Conditional |
| `spec.taskTemplate.contextSources[].http.allowInsecure` | Permit plain HTTP (non-TLS) URLs (default: `false`) | No |
| `spec.taskTemplate.contextSources[].http.timeoutSeconds` | Per-request timeout in seconds, 1–60 (default: `10`) | No |
| `spec.taskTemplate.contextSources[].http.maxResponseBytes` | Maximum response body size in bytes, 1–131072 (default: `32768`, i.e. 32 KiB). Caps the amount injected into the prompt | No |
| `spec.taskTemplate.contextSources[].failurePolicy` | Behavior when the source fails to fetch: `Fail` skips task creation for the work item; `Ignore` substitutes an empty string and logs a warning (default: `Fail`) | No |

Example — fetch a Jira issue description over HTTP and inject it into a prompt triggered by a GitHub issue:

```yaml
apiVersion: kelos.dev/v1alpha2
kind: TaskSpawner
metadata:
  name: enrich-from-jira
spec:
  when:
    githubIssues:
      labels: ["needs-jira-context"]
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: api-key
      secretRef:
        name: claude-credentials
    contextSources:
      - name: jira
        failurePolicy: Ignore
        http:
          # This example assumes the GitHub issue title is the Jira issue key
          # (e.g. "PROJ-123"). Adjust the URL/template to however your issues
          # reference Jira.
          url: "https://your-org.atlassian.net/rest/api/3/issue/{{.Title}}"
          headersFrom:
            - header: Authorization
              secretName: jira-credentials
              secretKey: authorization
          responseFilter:
            type: JSONPath
            expression: "$.fields.description"
          timeoutSeconds: 15
    promptTemplate: |
      Address GitHub issue #{{.Number}}: {{.Title}}

      Linked Jira description:
      {{.Context.jira}}
```

Example — fetch a GitHub API resource authenticated with a GitHub App installation token, reusing an existing GitHub App Secret:

```yaml
    contextSources:
      - name: pr
        http:
          url: "https://api.github.com/repos/my-org/my-repo/pulls/{{.Number}}"
          githubAppAuth:
            secretRef:
              name: my-github-app
          responseFilter:
            type: JSONPath
            expression: "$.body"
```

## WebhookGateway

A `WebhookGateway` is a per-channel authentication and routing boundary for
webhook-driven TaskSpawners and SessionSpawners. It owns one inbound path,
`/webhook/<namespace>/<name>` (surfaced in `status.path`), verifies inbound
deliveries against its own secret (github/linear), and fans out only to
spawners in its own namespace that reference it via `gatewayRef`. This
enables per-tenant secrets and multiple GitHub instances (github.com plus GitHub
Enterprise) without a per-instance Deployment. Enable the gateway server with
`webhookServer.gatewayServer.enabled` in the Helm chart. See
[example 18](../examples/18-webhookgateway).

Exactly one provider sub-struct (`spec.github`, `spec.linear`, or `spec.generic`)
must be set; the one that is present selects the source.

| Field | Description | Required |
| --- | --- | --- |
| `spec.github` | GitHub gateway configuration (see below). Set exactly one of `github`/`linear`/`generic` | Conditional |
| `spec.github.secretRef.name` | Secret holding the inbound HMAC secret (under a `webhook-secret` key) | Yes (for github) |
| `spec.github.apiBaseURL` | GitHub API base URL for outbound calls (PR-file enrichment, status reporting, and GitHub App token minting), e.g. `https://ghe.example.com/api/v3`. Defaults to `https://api.github.com` | No |
| `spec.github.credentialsRef.name` | Secret holding outbound GitHub API credentials — a `GITHUB_TOKEN` key (PAT) or GitHub App keys (`appID`, `installationID`, `privateKey`) | No |
| `spec.linear.secretRef.name` | Secret holding the inbound HMAC secret (under a `webhook-secret` key) | Yes (for linear) |
| `spec.generic` | Generic gateway configuration (no fields yet; deliveries are accepted without verification) | Conditional |
| `status.path` | Derived inbound path, `/webhook/<namespace>/<name>`, relative to the configured webhook host | — |
| `status.phase` | `Authenticated`, `SecretMissing`, or `Unauthenticated` (generic gateways are `Unauthenticated`) | — |

> `generic` gateways are accepted but **not** signature-verified;
> restrict access at the network layer. Task execution (clone/push) credentials
> come from the Workspace's `secretRef`, separate from a gateway's
> `github.credentialsRef`.

### "Gateway" terminology

Several distinct "gateway" concepts coexist:

| Term | What it is |
| --- | --- |
| `WebhookGateway` (CRD) | The per-channel auth/routing resource described above. |
| `gatewayRef` | The field on a TaskSpawner or SessionSpawner webhook source that binds it to a `WebhookGateway`. |
| `Gateway` (gateway.networking.k8s.io) | The Gateway-API ingress object that fronts the webhook server; created by the chart as `kelos-webhook-gateway` when `webhookServer.gateway.enabled`. |
| `webhookServer.gateway*` (Helm values) | `webhookServer.gateway` configures the Gateway-API `Gateway`/`HTTPRoute`; `webhookServer.gatewayServer` enables the gateway-mode webhook server that serves `WebhookGateway` paths. |

## Task Status

| Field | Description |
|-------|-------------|
| `status.phase` | Current phase: `Pending`, `Waiting`, `Running`, `Succeeded`, or `Failed` |
| `status.jobName` | Name of the Job created for this Task |
| `status.podName` | Name of the Pod running the Task |
| `status.startTime` | When the Task started running |
| `status.completionTime` | When the Task completed |
| `status.message` | Additional information about the current status |
| `status.outputs` | Automatically captured outputs: `branch`, `commit`, `base-branch`, `pr`, `cost-usd`, `input-tokens`, `output-tokens` |
| `status.results` | Parsed key-value map from outputs (e.g., `results.branch`, `results.commit`, `results.pr`, `results.input-tokens`) |
| `status.usage.costUSD` | Reported agent cost in USD (non-negative `resource.Quantity`). Parsed from `results["cost-usd"]` |
| `status.usage.inputTokens` | Number of input tokens consumed (non-negative integer). Parsed from `results["input-tokens"]` |
| `status.usage.outputTokens` | Number of output tokens produced (non-negative integer). Parsed from `results["output-tokens"]` |
| `status.conditions` | Standard Kubernetes conditions. Includes `BudgetBlocked` when a matching TaskBudget has been exceeded |

## TaskBudget

TaskBudget defines observed-spend admission limits for Tasks. When a Task's labels match a TaskBudget's `taskSelector` and the accumulated spend in the current period meets or exceeds a limit, the Task stays in `Waiting` phase with a `BudgetBlocked` condition until the period resets.

| Field | Description | Required |
|-------|-------------|----------|
| `spec.taskSelector` | Label selector matching Tasks and TaskRecords in the same namespace. An empty selector (`{}`) selects all Tasks | Yes |
| `spec.period.type` | Period boundary for budget accounting. Currently only `Daily` is supported | Yes |
| `spec.period.timezone` | IANA timezone for period boundaries (default: `UTC`). Rejected at create/update if not a loadable IANA zone | No |
| `spec.maxCostUSD` | Maximum observed cost in USD admitted per period (non-negative `resource.Quantity`) | At least one limit required |
| `spec.maxInputTokens` | Maximum input tokens admitted per period (non-negative integer) | At least one limit required |
| `spec.maxOutputTokens` | Maximum output tokens admitted per period (non-negative integer) | At least one limit required |

### TaskBudget Status

| Field | Description |
|-------|-------------|
| `status.observedGeneration` | Most recent generation observed by the controller |
| `status.currentPeriodStart` | Inclusive start of the current accounting period |
| `status.currentPeriodEnd` | Exclusive end of the current accounting period |
| `status.used.costUSD` | Summed cost from matching TaskRecords in the current period |
| `status.used.inputTokens` | Summed input tokens from matching TaskRecords in the current period |
| `status.used.outputTokens` | Summed output tokens from matching TaskRecords in the current period |
| `status.conditions` | Includes `Degraded` when the budget hits an operational error (e.g. a list error while summing usage) |

### Budget Admission Behavior

- A Task is checked against all TaskBudgets in its namespace before it starts — before Job creation for Job-backed Tasks, and before worker-pod assignment for Tasks using `spec.workerPoolRef`.
- A budget matches if its `taskSelector` selects the Task's labels.
- If any matching budget's limit is met or exceeded (using `>=` comparison), the Task is blocked.
- `spec.taskSelector` operator/value combinations that the controller cannot compile, and timezones that are not loadable IANA zones, are rejected at create/update time — so a malformed selector or timezone cannot be admitted.
- List errors when summing usage block admission (fail closed) and set a `Degraded` condition on the budget.
- The `Degraded` condition is cleared automatically after a successful evaluation.
- A zero limit (e.g., `maxOutputTokens: 0`) blocks all matching Tasks immediately.
- `status.used` reflects matching TaskRecords and resets when the accounting period rolls over.

## TaskRecord

TaskRecord is an immutable terminal record for a completed Task that reported
usage data. It preserves accounting data after the Task itself is deleted by
TTL. Tasks that complete without usage do not generate a TaskRecord.

| Field | Description | Required |
|-------|-------------|----------|
| `spec.taskRef.name` | Name of the source Task | Yes |
| `spec.taskRef.uid` | UID of the source Task | Yes |
| `spec.type` | Effective agent type of the Task (`Task.spec.worker.type`, falling back to `Task.spec.type`) | No |
| `spec.model` | Effective model of the Task (`Task.spec.worker.model`, falling back to `Task.spec.model`) | No |
| `spec.phase` | Terminal Task phase (`Succeeded` or `Failed`) | Yes |
| `spec.startTime` | When the Task started running | No |
| `spec.completionTime` | When the Task completed | Yes |
| `spec.usage.costUSD` | Reported cost in USD | No |
| `spec.usage.inputTokens` | Input tokens consumed | No |
| `spec.usage.outputTokens` | Output tokens produced | No |
| `spec.ttlSecondsAfterCompletion` | Seconds after `completionTime` before automatic deletion. If unset, the record is retained indefinitely. Controller-created records set this to 30 days | No |

## TaskSpawner Status

| Field | Description |
|-------|-------------|
| `status.phase` | Current phase: `Pending`, `Running`, `Suspended`, or `Failed` |
| `status.deploymentName` | Name of the Deployment running the spawner (polling-based sources) |
| `status.cronJobName` | Name of the CronJob running the spawner (cron-based sources) |
| `status.totalDiscovered` | Total number of items discovered from the source |
| `status.totalTasksCreated` | Total number of Tasks created by this spawner |
| `status.activeTasks` | Number of currently active (non-terminal) Tasks |
| `status.lastDiscoveryTime` | Last time the source was polled |
| `status.message` | Additional information about the current status |
| `status.conditions` | Standard Kubernetes conditions for detailed status |

## Configuration

Kelos reads defaults from `~/.kelos/config.yaml` (override with `--config`). CLI flags always take precedence over config file values.

```yaml
# ~/.kelos/config.yaml
oauthToken: <your-oauth-token>
# or: apiKey: <your-api-key>
model: sonnet  # or a versioned ID like 'claude-sonnet-4-6' — see spec.model under Task
effort: high
namespace: my-namespace
```

### Credentials

| Field | Description |
|-------|-------------|
| `oauthToken` | OAuth token — Kelos auto-creates the Kubernetes secret. Use `none` for an empty credential |
| `apiKey` | API key — Kelos auto-creates the Kubernetes secret. Use `none` for an empty credential (e.g., free-tier OpenCode models) |
| `secret` | (Advanced) Use a pre-created Kubernetes secret |
| `credentialType` | Credential type when using `secret` (`api-key` or `oauth`) |

**Precedence:** `--secret` flag > `secret` in config > `oauthToken`/`apiKey` in config.

### Workspace

The `workspace` field supports two forms:

**Reference an existing Workspace resource by name:**

```yaml
workspace:
  name: my-workspace
```

**Specify inline with a PAT — Kelos auto-creates the Workspace resource and secret:**

```yaml
workspace:
  repo: https://github.com/your-org/repo.git
  ref: main
  token: <your-github-token>  # optional, for private repos and gh CLI
```

**Specify inline with a GitHub App (recommended for production/org use):**

```yaml
workspace:
  repo: https://github.com/your-org/repo.git
  ref: main
  githubApp:
    appID: "12345"
    installationID: "67890"
    privateKeyPath: ~/.config/my-app.private-key.pem
```

| Field | Description |
|-------|-------------|
| `workspace.name` | Name of an existing Workspace resource |
| `workspace.repo` | Git repository URL — Kelos auto-creates a Workspace resource |
| `workspace.ref` | Git reference (branch, tag, or commit SHA) |
| `workspace.token` | GitHub PAT — Kelos auto-creates the secret and injects `GITHUB_TOKEN` |
| `workspace.githubApp.appID` | GitHub App ID |
| `workspace.githubApp.installationID` | GitHub App installation ID |
| `workspace.githubApp.privateKeyPath` | Path to PEM-encoded RSA private key file |

The `token` and `githubApp` fields are mutually exclusive. If both `name` and `repo` are set, `name` takes precedence. The `--workspace` CLI flag overrides all config values.

### Other Settings

| Field | Description |
|-------|-------------|
| `type` | Default agent type (`claude-code`, `codex`, `gemini`, `opencode`, or `cursor`) |
| `model` | Default model override |
| `effort` | Default agent reasoning effort |
| `namespace` | Default Kubernetes namespace |
| `agentConfig` | Default AgentConfig resource name |

### Environment Variables

The `env` field defines additional environment variables injected into task pods via `Task.spec.podOverrides.env`. CLI `--env` flags take precedence over config values on name collision.

| Field | Description |
|-------|-------------|
| `env[].name` | Variable name (must match `[A-Za-z_][A-Za-z0-9_]*`) |
| `env[].value` | Plain-text value (mutually exclusive with `valueFrom`) |
| `env[].valueFrom.secretKeyRef` | Reference a Kubernetes Secret (`name` and `key` required). Resolves in the Task pod's namespace. |
| `env[].valueFrom.configMapKeyRef` | Reference a Kubernetes ConfigMap (`name` and `key` required). Resolves in the Task pod's namespace. |

```yaml
env:
  - name: CLAUDE_CODE_USE_BEDROCK
    value: "1"
  - name: AWS_REGION
    value: us-west-2
  - name: MY_SECRET
    valueFrom:
      secretKeyRef:
        name: my-k8s-secret
        key: token
  - name: APP_CONFIG
    valueFrom:
      configMapKeyRef:
        name: my-configmap
        key: app.conf
```

## CLI Reference

The `kelos` CLI lets you manage the full lifecycle without writing YAML.

### Core Commands

| Command | Description |
|---------|-------------|
| `kelos install` | Install Kelos CRDs and controller into the cluster |
| `kelos uninstall` | Uninstall Kelos from the cluster |
| `kelos init` | Initialize `~/.kelos/config.yaml` |
| `kelos version` | Print version information |
| `kelos completion <shell>` | Generate a shell completion script for `bash`, `zsh`, `fish`, or `powershell` |

### Resource Management

| Command | Description |
|---------|-------------|
| `kelos run` | Create and run a new Task |
| `kelos run --from taskspawner/<name>` | Run a standalone Task from a TaskSpawner template |
| `kelos session connect NAME` | Continue a ready Session through terminal chat, resuming it first when it was suspended by its idle policy |
| `kelos session reset NAME` | Permanently clear a Session workspace and start a fresh conversation |
| `kelos create workspace` | Create a Workspace resource |
| `kelos create agentconfig` | Create an AgentConfig resource |
| `kelos get <resource> [name]` | List resources or view a specific resource (`tasks`, `sessions`, `taskspawners`, `workspaces`, `agentconfigs`, `workerpools`) |
| `kelos delete <resource> [name]` | Delete a resource (`tasks`, `sessions`, `taskspawners`, `workspaces`, `agentconfigs`, `workerpools`) |
| `kelos logs <task-name> [-f]` | View or stream logs from a task |
| `kelos suspend taskspawner <name>` | Pause a TaskSpawner (stops polling, running tasks continue) |
| `kelos resume taskspawner <name>` | Resume a paused TaskSpawner |

`kelos logs <task-name> -f` waits while the task Pod is unscheduled, Pending, or initializing its target container, then streams logs once the container is available. If Kubernetes closes an empty agent log stream while the Task is still active, the command reconnects instead of reporting completion. Failed Tasks and non-transient container startup failures return an error instead of retrying indefinitely.

### `kelos install` Flags

- `--values, -f`: Load Helm values from a YAML file; repeat to merge multiple files, or use `-` to read from stdin
- `--set`: Set chart values with Helm `key=value` syntax
- `--set-string`: Set string chart values with Helm `key=value` syntax
- `--set-file`: Set chart values from file contents with Helm `key=path` syntax
- `--version`: Override the image tag used for controller and bundled agent images; shorthand for `image.tag`
- `--image-pull-policy`: Set `imagePullPolicy` on controller-managed images
- `--disable-heartbeat`: Do not install the telemetry heartbeat CronJob
- `--spawner-resource-requests`: Resource requests for spawner containers as comma-separated `name=value` pairs
- `--spawner-resource-limits`: Resource limits for spawner containers as comma-separated `name=value` pairs
- `--ghproxy-resource-requests`: Resource requests for workspace ghproxy containers as comma-separated `name=value` pairs
- `--ghproxy-resource-limits`: Resource limits for workspace ghproxy containers as comma-separated `name=value` pairs
- `--ghproxy-allowed-upstreams`: Comma-separated list of allowed upstream base URLs for ghproxy
- `--ghproxy-cache-ttl`: Cache TTL for workspace ghproxy instances
- `--controller-resource-requests`: Resource requests for the controller container as comma-separated `name=value` pairs, for example `cpu=10m,memory=64Mi`
- `--controller-resource-limits`: Resource limits for the controller container as comma-separated `name=value` pairs, for example `cpu=500m,memory=128Mi`

`kelos install` renders the embedded Helm chart but still manages CRDs separately, so `crds.install` must be omitted or set to `false`.
`kelos install --dry-run` prints the chart manifests and omits CRDs.
When the same key is set multiple ways, precedence is: chart defaults, then `--values` files, then compatibility install flags, then explicit `--set`, `--set-string`, and `--set-file` overrides.

### `kelos run` Flags

- `--prompt, -p`: Task prompt (required unless `--prompt-file` or `--from` is set)
- `--prompt-file`: Read task prompt from a file path; use `-` to read from stdin (mutually exclusive with `--prompt`)
- `--from`: Run the Task template from a `taskspawner/<name>` reference
- `--values, -f`: Read top-level template values from a YAML or JSON file; use `-` to read from stdin (requires `--from`)
- `--type, -t`: Agent type (default: `claude-code`)
- `--model`: Model override
- `--effort`: Agent reasoning effort
- `--image`: Custom agent image
- `--name`: Task name (auto-generated if omitted)
- `--workspace`: Workspace resource name
- `--agent-config`: AgentConfig resource name
- `--depends-on`: Task names this task depends on (repeatable)
- `--branch`: Git branch to work on
- `--timeout`: Maximum execution time (e.g., `30m`, `1h`)
- `--env`: Additional env vars as `NAME=VALUE` (repeatable)
- `--watch, -w`: Watch task status after creation
- `--secret`: Pre-created secret name
- `--credential-type`: Credential type when using `--secret` (default: `api-key`)
- `--dry-run`: Render the Task without creating it; with `--from`, the TaskSpawner and any context-source Secrets are still read

### `kelos get` Flags

- `--output, -o`: Output format (`yaml` or `json`)
- `--detail, -d`: Show detailed information for a specific resource
- `--all-namespaces, -A`: List resources across all namespaces
- `--phase`: (`kelos get task` only) Filter tasks by phase; repeatable or comma-separated. Valid values: `Pending`, `Running`, `Waiting`, `Succeeded`, `Failed`

### `kelos delete` Flags

- `--all`: Delete every resource of the given type in the namespace; mutually exclusive with a resource name. Supported by `task`, `session`, `workspace`, `taskspawner`, `agentconfig`, and `workerpool` subcommands

### `kelos session reset` Flags

- `--yes, -y`: Skip confirmation that conversation history and workspace changes will be permanently deleted

### Common Flags

- `--config`: Path to config file (default `~/.kelos/config.yaml`)
- `--namespace, -n`: Kubernetes namespace
- `--kubeconfig`: Path to kubeconfig file
- `--dry-run`: Print resources without creating them. For `install`, this prints controller manifests only; CRDs are staged separately during real installs
- `--yes, -y`: Skip confirmation prompts

### Shell Completion

`kelos completion <shell>` prints a completion script for `bash`, `zsh`, `fish`, or `powershell`. Source it from your shell to enable `<TAB>` completion of subcommands, flags, and resource names.

Load the script for the current session:

```bash
# bash
source <(kelos completion bash)

# zsh
source <(kelos completion zsh)

# fish
kelos completion fish | source

# powershell
kelos completion powershell | Out-String | Invoke-Expression
```

To persist completion across sessions, add the matching `source` line to your shell's startup file (e.g., `~/.bashrc` or `~/.zshrc`), or write the script to your shell's completions directory. Run `kelos completion <shell> --help` for shell-specific installation paths.

In addition to subcommands and flags, the following arguments complete dynamically by querying the configured cluster — a reachable kubeconfig and the relevant list permission in the active namespace are required:

| Command | Completes |
|---------|-----------|
| `kelos logs <TAB>` | task names |
| `kelos get task <TAB>` | task names |
| `kelos get session <TAB>` | session names |
| `kelos get taskspawner <TAB>` | taskspawner names |
| `kelos get workspace <TAB>` | workspace names |
| `kelos get agentconfig <TAB>` | agentconfig names |
| `kelos get workerpool <TAB>` | workerpool names |
| `kelos delete task <TAB>` | task names |
| `kelos delete session <TAB>` | session names |
| `kelos delete taskspawner <TAB>` | taskspawner names |
| `kelos delete workspace <TAB>` | workspace names |
| `kelos delete agentconfig <TAB>` | agentconfig names |
| `kelos delete workerpool <TAB>` | workerpool names |
| `kelos suspend taskspawner <TAB>` | taskspawner names |
| `kelos resume taskspawner <TAB>` | taskspawner names |
| `kelos session connect <TAB>` | session names |
| `kelos session reset <TAB>` | session names |

Enum-valued flags — `kelos run --type`, `kelos run --credential-type`, `kelos get --output`, and `kelos get task --phase` — complete from their fixed value set without contacting the cluster.

## Prometheus Metrics

The Kelos controller and spawner pods expose Prometheus metrics on their `/metrics` endpoint.

### Controller Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kelos_task_created_total` | Counter | namespace, type | Total Tasks for which a Job was created |
| `kelos_task_completed_total` | Counter | namespace, type, phase | Total Tasks that reached a terminal phase |
| `kelos_task_duration_seconds` | Histogram | namespace, type, phase | Duration of Task execution from start to completion |
| `kelos_task_cost_usd_total` | Counter | namespace, type, spawner, model | Cumulative cost in USD of completed Tasks |
| `kelos_task_input_tokens_total` | Counter | namespace, type, spawner, model | Cumulative input tokens consumed by completed Tasks |
| `kelos_task_output_tokens_total` | Counter | namespace, type, spawner, model | Cumulative output tokens consumed by completed Tasks |
| `kelos_reconcile_errors_total` | Counter | controller | Reconciliation errors |

### Spawner Metrics

Each spawner pod emits metrics scoped to its own TaskSpawner:

| Metric | Type | Description |
|--------|------|-------------|
| `kelos_spawner_discovery_total` | Counter | Completed discovery cycles |
| `kelos_spawner_discovery_errors_total` | Counter | Failed discovery cycles |
| `kelos_spawner_items_discovered_total` | Counter | Work items discovered |
| `kelos_spawner_tasks_created_total` | Counter | Tasks created by this spawner |
| `kelos_spawner_discovery_duration_seconds` | Histogram | Duration of discovery cycles |

### Scraping with Prometheus Operator

The Helm chart can ship optional [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator)
`PodMonitor` resources (`monitoring.coreos.com/v1`). They are disabled by
default so the chart installs on clusters without the Prometheus Operator CRDs.
Enable them via the `podMonitor` values:

| Value | Default | Description |
|-------|---------|-------------|
| `podMonitor.enabled` | `false` | Ship a PodMonitor for the control-plane pods (controller manager, webhook servers, slack server). They all expose the `metrics` port (8080). Target discovery spans both `kelos-system` (where the controller manager runs) and the release namespace (where the webhook and slack servers run); under `kelos install` these are the same namespace. The Console server is not scraped — its 8080 port serves the web app, not metrics. |
| `podMonitor.interval` | `30s` | Scrape interval. |
| `podMonitor.scrapeTimeout` | `10s` | Per-scrape timeout. |
| `podMonitor.honorLabels` | `false` | Keep target-exposed labels when they collide with labels Prometheus would add. |
| `podMonitor.labels` | `{}` | Extra labels on the PodMonitor object(s); commonly used to match a Prometheus instance's `podMonitorSelector`. |
| `podMonitor.annotations` | `{}` | Extra annotations on the PodMonitor object(s). |
| `podMonitor.spawners.enabled` | `false` | Additionally ship a PodMonitor for long-lived spawner pods (event- and poll-based TaskSpawners). Spawner pods run in arbitrary namespaces, so this PodMonitor discovers pod targets across all namespaces (`spec.namespaceSelector.any: true`); the scraping Prometheus's service account must have RBAC to read/scrape pods in those namespaces. Cron-based (one-shot) spawners expose no metrics port and are not scraped. |

## Telemetry

Kelos collects anonymous, aggregate usage data to help improve the project. A `kelos-telemetry` CronJob runs daily at 06:00 UTC and reports the following:

| Data | Description |
|------|-------------|
| Installation ID | Random UUID, generated once per cluster |
| Kelos version | Installed controller version |
| Kubernetes version | Cluster K8s version |
| Task counts | Total Tasks, breakdown by effective agent type and phase |
| Session counts | Total Sessions, breakdown by agent type and phase |
| TaskSpawner sources | TaskSpawner counts by configured source type |
| WorkerPool scale | WorkerPool counts by phase and aggregate desired, current, and ready replicas |
| Resource adoption | Number of each resource in the latest Kelos API version and TaskSpawner source types in use |
| Scale | Number of namespaces with Kelos resources |
| Usage totals | Aggregate cost (USD), input tokens, and output tokens |

No personal data, resource or namespace names, repository names, prompts, or source code is collected. Reports are sent as profile-less PostHog events.

### Disabling Telemetry

Install (or reinstall) with the `--disable-heartbeat` flag:

```bash
kelos install --disable-heartbeat
```
