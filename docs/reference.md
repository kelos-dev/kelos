# Reference

## Task

| Field | Description | Required |
|-------|-------------|----------|
| `spec.type` | Agent type (`claude-code`, `codex`, `gemini`, `opencode`, or `cursor`) | Yes |
| `spec.prompt` | Task prompt for the agent | Yes |
| `spec.credentials.type` | `api-key`, `oauth`, or `none`. Use `none` to skip built-in credential injection (e.g., for Bedrock, Vertex AI, or Azure OpenAI credentials provided via `podOverrides.env`) | Yes |
| `spec.credentials.secretRef.name` | Secret name with credentials (not required when `type` is `none`) | Conditional |
| `spec.model` | Model override (e.g., `claude-sonnet-4-20250514`) | No |
| `spec.image` | Custom agent image override (see [Agent Image Interface](agent-image-interface.md)) | No |
| `spec.workspaceRef.name` | Name of a Workspace resource to use | No |
| `spec.agentConfigRef.name` | Name of an AgentConfig resource to use | No |
| `spec.dependsOn` | Task names that must succeed before this Task starts (creates `Waiting` phase) | No |
| `spec.branch` | Git branch to work on; only one Task with the same branch runs at a time (mutex) | No |
| `spec.ttlSecondsAfterFinished` | Auto-delete task after N seconds (0 for immediate) | No |
| `spec.podOverrides.resources` | CPU/memory requests and limits for the agent container | No |
| `spec.podOverrides.activeDeadlineSeconds` | Maximum duration in seconds before the agent pod is terminated | No |
| `spec.podOverrides.env` | Additional environment variables (built-in vars take precedence on conflict) | No |
| `spec.podOverrides.nodeSelector` | Node selection labels to constrain which nodes run agent pods | No |
| `spec.podOverrides.serviceAccountName` | Service account name for the agent pod; use with workload identity systems (IRSA, GKE Workload Identity, Azure) | No |

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

## Workspace

| Field | Description | Required |
|-------|-------------|----------|
| `spec.repo` | Git repository URL to clone (HTTPS, git://, or SSH) | Yes |
| `spec.ref` | Branch, tag, or commit SHA to checkout (defaults to repo's default branch) | No |
| `spec.secretRef.name` | Secret containing credentials for git auth and `gh` CLI (see [authentication methods](#workspace-authentication) below) | No |
| `spec.remotes[].name` | Git remote name to add after cloning (must not be `"origin"`) | Yes (per remote) |
| `spec.remotes[].url` | Git remote URL | Yes (per remote) |
| `spec.files[].path` | Relative file path inside the repository (e.g., `CLAUDE.md`) | Yes (per file) |
| `spec.files[].content` | File content to write | Yes (per file) |

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

The secret contains three keys, and the controller automatically exchanges them for a short-lived installation token before each task run:

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

## AgentConfig

| Field | Description | Required |
|-------|-------------|----------|
| `spec.agentsMD` | Agent instructions (e.g. `AGENTS.md`, `CLAUDE.md`) written to `~/.claude/CLAUDE.md` (additive with repo files) | No |
| `spec.plugins[].name` | Plugin name (used as directory name and namespace) | Yes (per plugin) |
| `spec.plugins[].skills[].name` | Skill name (becomes `skills/<name>/SKILL.md`) | Yes (per skill) |
| `spec.plugins[].skills[].content` | Skill content (markdown with frontmatter) | Yes (per skill) |
| `spec.plugins[].agents[].name` | Agent name (becomes `agents/<name>.md`) | Yes (per agent) |
| `spec.plugins[].agents[].content` | Agent content (markdown with frontmatter) | Yes (per agent) |
| `spec.skills[].source` | skills.sh package in `owner/repo` format (e.g., `vercel-labs/agent-skills`) | Yes (per skill) |
| `spec.skills[].skill` | Specific skill name from the package (installs all if omitted) | No |
| `spec.mcpServers[].name` | MCP server name (used as key in agent config) | Yes (per server) |
| `spec.mcpServers[].type` | Transport type: `stdio`, `http`, or `sse` | Yes (per server) |
| `spec.mcpServers[].command` | Executable to run (stdio only) | No |
| `spec.mcpServers[].args` | Command-line arguments (stdio only) | No |
| `spec.mcpServers[].url` | Server endpoint (http/sse only) | No |
| `spec.mcpServers[].headers` | HTTP headers (http/sse only) | No |
| `spec.mcpServers[].env` | Environment variables for server process (stdio only) | No |

## TaskSpawner

| Field | Description | Required |
|-------|-------------|----------|
| `spec.taskTemplate.workspaceRef.name` | Workspace resource (repo URL, auth, and clone target for spawned Tasks) | Yes (when using `githubIssues`, `githubPullRequests`, `githubWebhook`, or `linearWebhook`) |
| `spec.when.githubIssues.repo` | Override repository to poll for issues (in `owner/repo` format or full URL); defaults to workspace repo URL | No |
| `spec.when.githubIssues.labels` | Filter issues by labels | No |
| `spec.when.githubIssues.excludeLabels` | Exclude issues with these labels | No |
| `spec.when.githubIssues.state` | Filter by state: `open`, `closed`, `all` (default: `open`) | No |
| `spec.when.githubIssues.types` | Filter by type: `issues`, `pulls` (default: `issues`) | No |
| `spec.when.githubIssues.triggerComment` | **Deprecated: use `commentPolicy.triggerComment` instead.** Requires a matching command in the issue body or comments to include the issue. When combined with `excludeComments`, the latest matching command wins | No |
| `spec.when.githubIssues.excludeComments` | **Deprecated: use `commentPolicy.excludeComments` instead.** Exclude issues whose most recent matching command is an exclude comment. When combined with `triggerComment`, the latest matching command wins | No |
| `spec.when.githubIssues.commentPolicy.triggerComment` | Requires a matching command in the issue body or comments to include the issue. Replaces deprecated top-level `triggerComment` | No |
| `spec.when.githubIssues.commentPolicy.excludeComments` | Blocks items whose most recent matching command is an exclude comment. Replaces deprecated top-level `excludeComments` | No |
| `spec.when.githubIssues.commentPolicy.allowedUsers` | Restrict comment control to specific GitHub usernames | No |
| `spec.when.githubIssues.commentPolicy.allowedTeams` | Restrict comment control to specific GitHub teams in `org/team-slug` format | No |
| `spec.when.githubIssues.commentPolicy.minimumPermission` | Minimum repo permission required for comment control: `read`, `triage`, `write`, `maintain`, or `admin` | No |
| `spec.when.githubIssues.assignee` | Filter by assignee username; use `"*"` for any assignee or `"none"` for unassigned | No |
| `spec.when.githubIssues.author` | Filter by issue author username | No |
| `spec.when.githubIssues.excludeAuthors` | Exclude issues created by any of these usernames (client-side) | No |
| `spec.when.githubIssues.priorityLabels` | Priority-order labels for task selection when `maxConcurrency` is set; index 0 is highest priority | No |
| `spec.when.githubIssues.reporting.enabled` | Post status comments (started, succeeded, failed) back to the GitHub issue | No |
| `spec.when.githubIssues.pollInterval` | Per-source poll interval override (e.g., `"30s"`, `"5m"`); takes precedence over `spec.pollInterval` | No |
| `spec.when.githubPullRequests.repo` | Override repository to poll for PRs (in `owner/repo` format or full URL); defaults to workspace repo URL | No |
| `spec.when.githubPullRequests.labels` | Filter pull requests by labels | No |
| `spec.when.githubPullRequests.excludeLabels` | Exclude pull requests with these labels | No |
| `spec.when.githubPullRequests.state` | Filter by state: `open`, `closed`, `all` (default: `open`) | No |
| `spec.when.githubPullRequests.reviewState` | Filter by aggregated review state: `approved`, `changes_requested`, `any` (default: `any`) | No |
| `spec.when.githubPullRequests.triggerComment` | **Deprecated: use `commentPolicy.triggerComment` instead.** Requires a matching command in the PR body or comments to include the PR. When combined with `excludeComments`, the latest matching command wins | No |
| `spec.when.githubPullRequests.excludeComments` | **Deprecated: use `commentPolicy.excludeComments` instead.** Exclude PRs whose most recent matching command is an exclude comment. When combined with `triggerComment`, the latest matching command wins | No |
| `spec.when.githubPullRequests.commentPolicy.triggerComment` | Requires a matching command in the PR body or comments to include the PR. Replaces deprecated top-level `triggerComment` | No |
| `spec.when.githubPullRequests.commentPolicy.excludeComments` | Blocks PRs whose most recent matching command is an exclude comment. Replaces deprecated top-level `excludeComments` | No |
| `spec.when.githubPullRequests.commentPolicy.allowedUsers` | Restrict comment control to specific GitHub usernames | No |
| `spec.when.githubPullRequests.commentPolicy.allowedTeams` | Restrict comment control to specific GitHub teams in `org/team-slug` format | No |
| `spec.when.githubPullRequests.commentPolicy.minimumPermission` | Minimum repo permission required for comment control: `read`, `triage`, `write`, `maintain`, or `admin` | No |
| `spec.when.githubPullRequests.author` | Filter by PR author username | No |
| `spec.when.githubPullRequests.excludeAuthors` | Exclude PRs opened by any of these usernames (client-side) | No |
| `spec.when.githubPullRequests.draft` | Filter by draft state | No |
| `spec.when.githubPullRequests.priorityLabels` | Priority-order labels for task selection when `maxConcurrency` is set; index 0 is highest priority | No |
| `spec.when.githubPullRequests.reporting.enabled` | Post status comments (started, succeeded, failed) back to the GitHub pull request | No |
| `spec.when.githubPullRequests.pollInterval` | Per-source poll interval override (e.g., `"30s"`, `"5m"`); takes precedence over `spec.pollInterval` | No |
| `spec.when.githubWebhook.events` | GitHub event types to listen for (e.g., `"issues"`, `"pull_request"`, `"push"`, `"issue_comment"`) | Yes (when using githubWebhook) |
| `spec.when.githubWebhook.repository` | Restrict webhooks to a specific repository (`owner/repo` format); if empty, webhooks from any repository are accepted | No |
| `spec.when.githubWebhook.excludeAuthors` | Exclude webhook events sent by any of these usernames; applied before filter evaluation | No |
| `spec.when.githubWebhook.filters[].event` | GitHub event type this filter applies to | Yes (per filter) |
| `spec.when.githubWebhook.filters[].action` | Filter by webhook action (e.g., `"opened"`, `"created"`, `"submitted"`) | No |
| `spec.when.githubWebhook.filters[].labels` | Require the issue/PR to have all of these labels | No |
| `spec.when.githubWebhook.filters[].excludeLabels` | Exclude issues/PRs with any of these labels | No |
| `spec.when.githubWebhook.filters[].state` | Filter by issue/PR state (`"open"`, `"closed"`) | No |
| `spec.when.githubWebhook.filters[].branch` | Filter push events by branch name (exact match or glob) | No |
| `spec.when.githubWebhook.filters[].draft` | Filter PRs by draft status | No |
| `spec.when.githubWebhook.filters[].author` | Filter by the event sender's username | No |
| `spec.when.githubWebhook.filters[].excludeAuthors` | Exclude events sent by any of these usernames | No |
| `spec.when.githubWebhook.filters[].bodyContains` | Filter by substring match on the comment/review body | No |
| `spec.when.linearWebhook.types` | Linear resource types to listen for (e.g., `"Issue"`, `"Comment"`) | Yes (when using linearWebhook) |
| `spec.when.linearWebhook.filters[].type` | Scope filter to a specific resource type | No |
| `spec.when.linearWebhook.filters[].action` | Filter by webhook action: `create`, `update`, or `remove` | No |
| `spec.when.linearWebhook.filters[].states` | Filter by workflow state names (e.g., `"Todo"`, `"In Progress"`) | No |
| `spec.when.linearWebhook.filters[].labels` | Require the issue to have all of these labels | No |
| `spec.when.linearWebhook.filters[].excludeLabels` | Exclude issues with any of these labels | No |
| `spec.when.jira.pollInterval` | Per-source poll interval override (e.g., `"30s"`, `"5m"`); takes precedence over `spec.pollInterval` | No |
| `spec.when.cron.schedule` | Cron schedule expression (e.g., `"0 * * * *"`) | Yes (when using cron) |
| `spec.taskTemplate.type` | Agent type (`claude-code`, `codex`, `gemini`, `opencode`, or `cursor`) | Yes |
| `spec.taskTemplate.credentials` | Credentials for the agent (same as Task) | Yes |
| `spec.taskTemplate.model` | Model override | No |
| `spec.taskTemplate.image` | Custom agent image override (see [Agent Image Interface](agent-image-interface.md)) | No |
| `spec.taskTemplate.agentConfigRef.name` | Name of an AgentConfig resource for spawned Tasks | No |
| `spec.taskTemplate.promptTemplate` | Go text/template for prompt (see [template variables](#prompttemplate-variables) below) | No |
| `spec.taskTemplate.dependsOn` | Task names that spawned Tasks depend on | No |
| `spec.taskTemplate.branch` | Git branch template for spawned Tasks (supports Go template variables, e.g., `kelos-task-{{.Number}}`) | No |
| `spec.taskTemplate.ttlSecondsAfterFinished` | Auto-delete spawned tasks after N seconds | No |
| `spec.taskTemplate.podOverrides` | Pod customization for spawned Tasks (resources, timeout, env, nodeSelector, serviceAccountName) | No |
| `spec.pollInterval` | How often to poll the source (default: `5m`). Deprecated: use per-source `pollInterval` instead | No |
| `spec.maxConcurrency` | Limit max concurrent running tasks (important for cost control) | No |
| `spec.maxTotalTasks` | Lifetime limit on total tasks created by this spawner | No |
| `spec.suspend` | Pause the spawner without deleting it; resume with `spec.suspend: false` (default: `false`) | No |

<a id="prompttemplate-variables"></a>

### promptTemplate Variables

The `promptTemplate` field uses Go `text/template` syntax. Available variables depend on the source type:

| Variable | Description | GitHub Issues | GitHub Pull Requests | GitHub Webhook | Linear Webhook | Cron |
|----------|-------------|---------------|----------------------|----------------|----------------|------|
| `{{.ID}}` | Unique identifier | Issue/PR number as string (e.g., `"42"`) | Pull request number as string | Issue/PR number or commit ID | Linear resource ID | Date-time string (e.g., `"20260207-0900"`) |
| `{{.Number}}` | Issue or PR number | Issue/PR number (e.g., `42`) | Pull request number | Issue/PR number (when available) | Empty | `0` |
| `{{.Title}}` | Title of the work item | Issue/PR title | Pull request title | Issue/PR title or "Push to &lt;branch&gt;" | Resource title | Trigger time (RFC3339) |
| `{{.Body}}` | Body text | Issue/PR body | Pull request body | Issue/PR/comment body | Empty | Empty |
| `{{.URL}}` | URL to the source item | GitHub HTML URL | GitHub PR URL | Issue/PR HTML URL | Empty | Empty |
| `{{.Labels}}` | Comma-separated labels | Issue/PR labels | Pull request labels | Empty | Issue labels | Empty |
| `{{.Comments}}` | Concatenated comments | Issue/PR comments | PR conversation comments | Empty | Empty | Empty |
| `{{.Kind}}` | Type of work item | `"Issue"` or `"PR"` | `"PR"` | `"webhook"` | `"LinearWebhook"` | `"Issue"` |
| `{{.Event}}` | GitHub event type | Empty | Empty | Event type (e.g., `"issues"`, `"pull_request"`, `"push"`) | Empty | Empty |
| `{{.Action}}` | Webhook action | Empty | Empty | Action (e.g., `"opened"`, `"created"`, `"submitted"`) | Action (e.g., `"create"`, `"update"`, `"remove"`) | Empty |
| `{{.Sender}}` | Event sender username | Empty | Empty | Username of person who triggered the event | Empty | Empty |
| `{{.Branch}}` | Git branch to update | Empty | PR head branch (e.g., `"kelos-task-42"`) | PR source branch or push branch | Empty | Empty |
| `{{.Ref}}` | Git ref | Empty | Empty | Git ref for push events (e.g., `"refs/heads/main"`) | Empty | Empty |
| `{{.Repository}}` | Full repository name | Empty | Empty | Repository in `owner/repo` format | Empty | Empty |
| `{{.RepositoryOwner}}` | Repository owner | Empty | Empty | Repository owner login | Empty | Empty |
| `{{.RepositoryName}}` | Repository name | Empty | Empty | Repository name only | Empty | Empty |
| `{{.Payload}}` | Raw event payload | Empty | Empty | Full parsed GitHub webhook payload | Full parsed Linear webhook payload | Empty |
| `{{.ReviewState}}` | Aggregated review state | Empty | `approved`, `changes_requested`, or empty | Empty | Empty | Empty |
| `{{.ReviewComments}}` | Formatted inline review comments | Empty | Inline PR review comments | Empty | Empty | Empty |
| `{{.Type}}` | Resource type | Empty | Empty | Empty | Resource type (e.g., `"Issue"`, `"Comment"`) | Empty |
| `{{.State}}` | Workflow state | Empty | Empty | Empty | Current state name (e.g., `"Todo"`, `"In Progress"`) | Empty |
| `{{.IssueID}}` | Parent issue ID | Empty | Empty | Empty | Parent issue ID (Comment events only) | Empty |
| `{{.Time}}` | Trigger time (RFC3339) | Empty | Empty | Empty | Empty | Cron tick time (e.g., `"2026-02-07T09:00:00Z"`) |
| `{{.Schedule}}` | Cron schedule expression | Empty | Empty | Empty | Empty | Schedule string (e.g., `"0 * * * *"`) |

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
model: claude-sonnet-4-5-20250929
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
| `namespace` | Default Kubernetes namespace |
| `agentConfig` | Default AgentConfig resource name |

## CLI Reference

The `kelos` CLI lets you manage the full lifecycle without writing YAML.

### Core Commands

| Command | Description |
|---------|-------------|
| `kelos install` | Install Kelos CRDs and controller into the cluster |
| `kelos uninstall` | Uninstall Kelos from the cluster |
| `kelos init` | Initialize `~/.kelos/config.yaml` |
| `kelos version` | Print version information |

### Resource Management

| Command | Description |
|---------|-------------|
| `kelos run` | Create and run a new Task |
| `kelos create workspace` | Create a Workspace resource |
| `kelos create agentconfig` | Create an AgentConfig resource |
| `kelos get <resource> [name]` | List resources or view a specific resource (`tasks`, `taskspawners`, `workspaces`) |
| `kelos delete <resource> <name>` | Delete a resource |
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
When the same key is set multiple ways, precedence is: chart defaults, then `--values` files, then compatibility install flags, then explicit `--set`, `--set-string`, and `--set-file` overrides.

### `kelos run` Flags

- `--prompt, -p`: Task prompt (required unless `--prompt-file` is set)
- `--prompt-file`: Read task prompt from a file path; use `-` to read from stdin (mutually exclusive with `--prompt`)
- `--type, -t`: Agent type (default: `claude-code`)
- `--model`: Model override
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

### `kelos get` Flags

- `--output, -o`: Output format (`yaml` or `json`)
- `--detail, -d`: Show detailed information for a specific resource
- `--all-namespaces, -A`: List resources across all namespaces

### Common Flags

- `--config`: Path to config file (default `~/.kelos/config.yaml`)
- `--namespace, -n`: Kubernetes namespace
- `--kubeconfig`: Path to kubeconfig file
- `--dry-run`: Print resources without creating them (supported by `run`, `create`, `install`)
- `--yes, -y`: Skip confirmation prompts

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
