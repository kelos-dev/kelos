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
web and terminal clients can share and reconnect to. The spec is immutable.
Conversation history is retained on the Session workspace rather than in the
Kubernetes API.

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
| `spec.volumeClaimTemplate` | PersistentVolumeClaimSpec for the Session workspace; omit to use `emptyDir` | No |
| `status.phase` | Infrastructure phase: `Pending`, `Ready`, or `Failed` | Output |
| `status.podName` | Session Pod name | Output |
| `status.podUID` | Identity of the Pod running the live conversation | Output |
| `status.branch` | Currently checked-out git branch in the Session workspace | Output |
| `status.pullRequest.url` | Web URL of the pull request associated with the current branch | Output |
| `status.pullRequest.state` | Pull request lifecycle state: `Draft`, `Open`, `Merged`, or `Closed` | Output |

The web creation dialog can generate a new Session from an existing Session in
the active namespace. This copies the complete `Session.spec` into an editable
form or YAML manifest and leaves the new name blank. It does not copy the source
metadata, conversation, or persistent-volume data.

Use `kelos session connect NAME` for terminal chat. Web chat is served by the
optional shared `kelos-session-server`; both clients use the same event stream
and provider conversation. Both clients can stream agent and tool activity,
answer user-input requests, and interrupt active work without ending the
provider conversation.

Selecting a Session in the web chat opens it at the latest retained message.
Reconnecting preserves an intentional upward scroll position and shows the
history that remains available on the Session workspace.

Web messages render safe Markdown: paragraphs and headings; emphasis,
strong text, strikethrough, and inline code; ordered, unordered, and task lists;
blockquotes and horizontal rules; HTTP(S) links; fenced or indented code blocks;
and pipe tables with optional column alignment. The renderer does not
interpret raw HTML or load embedded images. Fenced code may include a language
label, and wide tables and long code lines scroll horizontally. Tables that
would render more than 10,000 cells remain plain text.

If the Session Pod is deleted or evicted, clients reconnect after its
replacement is ready. Work active at the time of failure is reported as
interrupted and is not submitted again automatically. The terminal client also
does not retry a request whose delivery cannot be confirmed; it reports that
uncertainty so the user can decide whether to submit it again.

Session Pods that use the default runtime image are replaced when a Kelos
upgrade changes that image. Before replacement, Kelos stops accepting new turns
and waits for accepted work to finish. Pending user input delays the update
until it is answered or interrupted. Rejected turns are not retried
automatically; submit them again after the Session reconnects. An explicitly
tagged or digested runtime image remains pinned.

`status.branch` and `status.pullRequest` reflect the Session workspace while it
is ready. The web client shows both values in the Session sidebar and
conversation header.

When `spec.volumeClaimTemplate` is set, conversation history and workspace
changes survive Pod replacement. The claim remains until the Session is
deleted, after which PersistentVolume retention follows the StorageClass
reclaim policy. `Workspace.spec.setupCommand` runs again in each replacement
container. When the field is omitted, the workspace uses `emptyDir`, so its
history and changes do not survive Pod replacement.

The shared web server can create, list, delete, and connect to Sessions across
namespaces while the web application operates on one active namespace at a
time. Users can switch the active namespace live from the sidebar.
`sessionServer.defaultNamespace` sets its initial value, and Session, Workspace,
AgentConfig, and credential options are loaded only from the active namespace.
Selecting an existing Session as a source populates both the form fields and the
editable YAML manifest. Settings that the form cannot represent remain editable
in YAML mode.
The creation form accepts provider, credentials, model, Workspace, AgentConfig
references, and an optional persistent volume claim. YAML mode server-side
applies one `kelos.dev/v1alpha2` Session manifest in the active namespace. The
manifest may include labels, annotations, the complete `WorkerSpec`, and an
optional persistent volume claim.

## WorkerPool

A WorkerPool manages a fleet of persistent worker pods backed by a StatefulSet. Tasks reference a WorkerPool via `spec.workerPoolRef` to execute on pre-warmed infrastructure instead of creating per-task Jobs.

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
| `GITHUB_TOKEN` | GitHub Personal Access Token for git auth and `gh` CLI |

```bash
kubectl create secret generic github-token \
  --from-literal=GITHUB_TOKEN=<your-pat>
```

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
| `spec.when.githubIssues.reporting.enabled` | Post status comments (started, succeeded, failed) back to the GitHub issue | No |
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
| `spec.when.githubPullRequests.reporting.enabled` | Post status comments (started, succeeded, failed) back to the GitHub pull request | No |
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
| `spec.when.githubWebhook.filters[].draft` | Filter PRs by draft status | No |
| `spec.when.githubWebhook.filters[].author` | Filter by the event sender's username | No |
| `spec.when.githubWebhook.filters[].excludeAuthors` | Exclude events sent by any of these usernames | No |
| `spec.when.githubWebhook.filters[].filePatterns.include` | Doublestar globs for changed files to include after `exclude` patterns are removed. Applies to `push` and `pull_request` webhook filters | No |
| `spec.when.githubWebhook.filters[].filePatterns.exclude` | Doublestar globs for changed files to remove before include matching. Events with no remaining changed files are skipped | No |
| `spec.when.githubWebhook.filters[].bodyContains` | **Deprecated.** Filter by case-sensitive substring match on the comment/review body. Use `bodyPattern` instead | No |
| `spec.when.githubWebhook.filters[].bodyPattern` | Require the comment/review body to match a Go re2 regular expression. When combined with `excludeBodyPatterns`, the body must match this pattern AND not match any exclude entry | No |
| `spec.when.githubWebhook.filters[].excludeBodyPatterns` | Exclude events whose comment/review body matches any of these Go re2 regular expressions (OR semantics) | No |
| `spec.when.githubWebhook.filters[].commentOn` | Scope `issue_comment` events to comments posted on a specific subject: `"Issue"` matches plain issues, `"PullRequest"` matches pull requests. Empty matches both. Ignored for other events | No |
| `spec.when.githubWebhook.reporting.enabled` | Post status comments (started, succeeded, failed) back to the originating issue or PR | No |
| `spec.when.githubWebhook.reporting.checks.name` | Creates a GitHub Check Run for tasks spawned by PR-related webhook events, enabling branch protection and merge queue integration. Sets the Check Run name (defaults to `"Kelos: <taskspawner-name>"`, max 100 chars). The token used by the workspace must have `checks:write` permission. Requires `events` to include at least one of `pull_request`, `pull_request_review`, `pull_request_review_comment`, or `pull_request_target` (enforced by CEL validation). | No |
| `spec.when.linearWebhook.types` | Linear resource types to listen for (e.g., `"Issue"`, `"Comment"`) | Yes (when using linearWebhook) |
| `spec.when.linearWebhook.filters[].type` | Scope filter to a specific resource type | No |
| `spec.when.linearWebhook.filters[].action` | Filter by webhook action: `create`, `update`, or `remove` | No |
| `spec.when.linearWebhook.filters[].states` | Filter by workflow state names (e.g., `"Todo"`, `"In Progress"`) | No |
| `spec.when.linearWebhook.filters[].labels` | Require the issue to have all of these labels | No |
| `spec.when.linearWebhook.filters[].excludeLabels` | Exclude issues with any of these labels | No |
| `spec.when.slack.channels` | Restrict which Slack channels the bot listens in (channel IDs like `"C0123456789"`); when empty, listens in all invited channels | No |
| `spec.when.slack.botMessagePolicy` | Controls whether bot-originated messages can trigger this spawner: `None` (default) rejects all bot messages, `All` allows all including self, `OthersOnly` allows other bots but rejects the bot's own output to prevent self-trigger loops | No |
| `spec.when.slack.triggers[].pattern` | RE2 regex matched against message text (unanchored); leading `<@USER_ID>` mentions are stripped before matching; bot mention required unless `mentionOptional` is set; multiple triggers use OR semantics; when empty, every bot mention fires | No |
| `spec.when.slack.triggers[].mentionOptional` | When `true`, fire on pattern match alone without requiring a bot @-mention | No |
| `spec.when.slack.excludePatterns` | RE2 regex patterns that reject messages when any pattern matches (OR semantics); leading `<@USER_ID>` mentions are stripped before matching; does not apply to slash commands | No |
| `spec.when.webhook.source` | Short identifier for the generic webhook source (lowercase alphanumeric with optional hyphens). Determines the URL path (`/webhook/<source>`). The endpoint is currently unauthenticated — see [#1040](https://github.com/kelos-dev/kelos/issues/1040) | Yes (when using webhook) |
| `spec.when.webhook.fieldMapping` | Map of template variable name → JSONPath expression evaluated against the request body. Each key becomes a top-level template variable. Lowercase `id`, `title`, `body`, `url` are also exposed as `{{.ID}}`, `{{.Title}}`, `{{.Body}}`, `{{.URL}}`. The `id` key is required (used for delivery deduplication and Task naming) | Yes (when using webhook) |
| `spec.when.webhook.filters[].field` | JSONPath expression selecting the payload field to match | Yes (per filter) |
| `spec.when.webhook.filters[].value` | Require an exact string match against the extracted field value (mutually exclusive with `pattern`) | Conditional |
| `spec.when.webhook.filters[].pattern` | Require a regex match against the extracted field value (mutually exclusive with `value`) | Conditional |
| `spec.when.jira.pollInterval` | Per-source poll interval (e.g., `"30s"`, `"5m"`). Defaults to `5m` when omitted | No |
| `spec.when.cron.schedule` | Cron schedule expression (e.g., `"0 * * * *"`) | Yes (when using cron) |
| `spec.taskTemplate.worker` | Execution environment for spawned Tasks (see [WorkerSpec](#workerspec)). When used alone, spawned Tasks create Jobs. Mutually exclusive with `workerPoolRef` | One of worker, workerPoolRef, or type+credentials |
| `spec.taskTemplate.workerPoolRef.name` | WorkerPool for persistent execution | One of worker, workerPoolRef, or type+credentials |
| `spec.taskTemplate.type` | **(Deprecated)** Agent type — use `taskTemplate.worker.type` instead | Legacy |
| `spec.taskTemplate.credentials` | **(Deprecated)** Credentials — use `taskTemplate.worker.credentials` instead | Legacy |
| `spec.taskTemplate.model` | **(Deprecated)** Model override — use `taskTemplate.worker.model` instead | Legacy |
| `spec.taskTemplate.effort` | **(Deprecated)** Reasoning effort — use `taskTemplate.worker.effort` instead | Legacy |
| `spec.taskTemplate.image` | **(Deprecated)** Custom agent image — use `taskTemplate.worker.image` instead | Legacy |
| `spec.taskTemplate.workspaceRef.name` | **(Deprecated)** Workspace reference — use `taskTemplate.worker.workspaceRef` instead | Legacy |
| `spec.taskTemplate.agentConfigRefs[].name` | **(Deprecated)** AgentConfig references — use `taskTemplate.worker.agentConfigRefs` instead | Legacy |
| `spec.taskTemplate.promptTemplate` | Go text/template for prompt (see [template variables](#prompttemplate-variables) below) | No |
| `spec.taskTemplate.dependsOn` | Task names that spawned Tasks depend on. Not supported with `workerPoolRef` | No |
| `spec.taskTemplate.branch` | Git branch template for spawned Tasks (supports Go template variables, e.g., `kelos-task-{{.Number}}`). Not supported with `workerPoolRef` | No |
| `spec.taskTemplate.ttlSecondsAfterFinished` | Auto-delete spawned tasks after N seconds | No |
| `spec.taskTemplate.podFailurePolicy` | Kubernetes Job pod failure policy copied to spawned Tasks as `Task.spec.podFailurePolicy` | No |
| `spec.taskTemplate.podOverrides` | **(Deprecated)** Pod customization — use `taskTemplate.worker.podOverrides` instead | Legacy |
| `spec.taskTemplate.metadata.labels` | Labels merged into spawned Tasks; values support the same Go template variables as `branch`/`promptTemplate`; the `kelos.dev/taskspawner` label is always set to the TaskSpawner name and overrides any user value for that key | No |
| `spec.taskTemplate.metadata.annotations` | Annotations merged into spawned Tasks; values support the same Go template variables as `branch`/`promptTemplate`; source annotations (e.g. `kelos.dev/source-kind`) are applied after rendering and override conflicting user values | No |
| `spec.taskTemplate.contextSources` | External data sources fetched in parallel before task creation; each source's value is exposed as `{{.Context.NAME}}` in `branch`, `promptTemplate`, and `metadata` templates (see [Context Sources](#context-sources) below). Maximum 8 entries; names must be unique | No |
| `spec.taskTemplate.upstreamRepo` | Upstream repository in `owner/repo` format; injected as `KELOS_UPSTREAM_REPO` into the agent container. Typically auto-derived from `githubIssues.repo`/`githubPullRequests.repo`, but can be set explicitly for fork workflows | No |
| `spec.maxConcurrency` | Limit max concurrent running tasks (important for cost control) | No |
| `spec.maxTotalTasks` | Lifetime limit on total tasks created by this spawner | No |
| `spec.suspend` | Pause the spawner without deleting it; resume with `spec.suspend: false` (default: `false`) | No |

### Generated Task Names

For `githubIssues`, `githubPullRequests`, `jira`, and `cron` sources, Kelos first
lowercases the work item ID when forming the Task name:
`<TaskSpawner name>-<lowercase work item ID>`.

Lowercasing the Task name does not change the source data exposed to templates
and logs. In particular, `{{.ID}}` remains the raw work item ID (for example,
`ENG-42`). Webhook-backed TaskSpawners use delivery-based Task names instead.

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
`kelos.dev/trigger-type`, and `kelos.dev/trigger-time` annotations.

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
| `{{.Time}}` | Trigger time (RFC3339) | Empty | Empty | Empty | Empty | Empty | Empty | Cron tick time (e.g., `"2026-02-07T09:00:00Z"`) |
| `{{.Schedule}}` | Cron schedule expression | Empty | Empty | Empty | Empty | Empty | Empty | Schedule string (e.g., `"0 * * * *"`) |

> **Generic Webhook only:** any additional keys declared in `spec.when.webhook.fieldMapping` are also exposed as top-level template variables (e.g., `fieldMapping: {severity: "$.level"}` makes `{{.severity}}` available).

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
| `kelos session connect NAME` | Continue a ready Session through terminal chat |
| `kelos create workspace` | Create a Workspace resource |
| `kelos create agentconfig` | Create an AgentConfig resource |
| `kelos get <resource> [name]` | List resources or view a specific resource (`tasks`, `sessions`, `taskspawners`, `workspaces`, `agentconfigs`, `workerpools`) |
| `kelos delete <resource> [name]` | Delete a resource (`tasks`, `sessions`, `taskspawners`, `workspaces`, `agentconfigs`, `workerpools`) |
| `kelos logs <task-name> [-f]` | View or stream logs from a task |
| `kelos suspend taskspawner <name>` | Pause a TaskSpawner (stops polling, running tasks continue) |
| `kelos resume taskspawner <name>` | Resume a paused TaskSpawner |

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

## Telemetry

Kelos collects anonymous, aggregate usage data to help improve the project. A `kelos-telemetry` CronJob runs daily at 06:00 UTC and reports the following:

| Data | Description |
|------|-------------|
| Installation ID | Random UUID, generated once per cluster |
| Kelos version | Installed controller version |
| Kubernetes version | Cluster K8s version |
| Task counts | Total tasks, breakdown by type and phase |
| Feature adoption | Number of TaskSpawners, AgentConfigs, Workspaces, and source types in use |
| Scale | Number of namespaces with Kelos resources |
| Usage totals | Aggregate cost (USD), input tokens, and output tokens |

No personal data, repository names, prompts, or source code is collected.

### Disabling Telemetry

Install (or reinstall) with the `--disable-heartbeat` flag:

```bash
kelos install --disable-heartbeat
```
