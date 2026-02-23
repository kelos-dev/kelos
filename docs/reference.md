# Reference

## Task

| Field | Description | Required |
|-------|-------------|----------|
| `spec.type` | Agent type (`claude-code`, `codex`, `gemini`, or `opencode`) | Yes |
| `spec.prompt` | Task prompt for the agent | Yes |
| `spec.credentials.type` | `api-key` or `oauth` | Yes |
| `spec.credentials.secretRef.name` | Secret name with credentials | Yes |
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
| `spec.secretRef.name` | Secret containing `GITHUB_TOKEN` for git auth and `gh` CLI | No |
| `spec.files[]` | Files to inject into the cloned repository before the agent starts | No |

## AgentConfig

| Field | Description | Required |
|-------|-------------|----------|
| `spec.agentsMD` | Agent instructions (e.g. `AGENTS.md`, `CLAUDE.md`) written to `~/.claude/CLAUDE.md` (additive with repo files) | No |
| `spec.plugins[].name` | Plugin name (used as directory name and namespace) | Yes (per plugin) |
| `spec.plugins[].skills[].name` | Skill name (becomes `skills/<name>/SKILL.md`) | Yes (per skill) |
| `spec.plugins[].skills[].content` | Skill content (markdown with frontmatter) | Yes (per skill) |
| `spec.plugins[].agents[].name` | Agent name (becomes `agents/<name>.md`) | Yes (per agent) |
| `spec.plugins[].agents[].content` | Agent content (markdown with frontmatter) | Yes (per agent) |
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
| `spec.taskTemplate.workspaceRef.name` | Workspace resource (repo URL, auth, and clone target for spawned Tasks) | Yes (when using githubIssues) |
| `spec.when.githubIssues.labels` | Filter issues by labels | No |
| `spec.when.githubIssues.excludeLabels` | Exclude issues with these labels | No |
| `spec.when.githubIssues.state` | Filter by state: `open`, `closed`, `all` (default: `open`) | No |
| `spec.when.githubIssues.types` | Filter by type: `issues`, `pulls` (default: `issues`) | No |
| `spec.when.cron.schedule` | Cron schedule expression (e.g., `"0 * * * *"`) | Yes (when using cron) |
| `spec.taskTemplate.type` | Agent type (`claude-code`, `codex`, `gemini`, or `opencode`) | Yes |
| `spec.taskTemplate.credentials` | Credentials for the agent (same as Task) | Yes |
| `spec.taskTemplate.model` | Model override | No |
| `spec.taskTemplate.image` | Custom agent image override (see [Agent Image Interface](agent-image-interface.md)) | No |
| `spec.taskTemplate.agentConfigRef.name` | Name of an AgentConfig resource for spawned Tasks | No |
| `spec.taskTemplate.promptTemplate` | Go text/template for prompt (see [template variables](#prompttemplate-variables) below) | No |
| `spec.taskTemplate.dependsOn` | Task names that spawned Tasks depend on | No |
| `spec.taskTemplate.branch` | Git branch template for spawned Tasks (supports Go template variables, e.g., `axon-task-{{.Number}}`) | No |
| `spec.taskTemplate.ttlSecondsAfterFinished` | Auto-delete spawned tasks after N seconds | No |
| `spec.taskTemplate.podOverrides` | Pod customization for spawned Tasks (resources, timeout, env, nodeSelector) | No |
| `spec.pollInterval` | How often to poll the source (default: `5m`) | No |
| `spec.maxConcurrency` | Limit max concurrent running tasks (important for cost control) | No |
| `spec.maxTotalTasks` | Lifetime limit on total tasks created by this spawner | No |
| `spec.suspend` | Pause the spawner without deleting it; resume with `spec.suspend: false` (default: `false`) | No |

<a id="prompttemplate-variables"></a>

### promptTemplate Variables

The `promptTemplate` field uses Go `text/template` syntax. Available variables depend on the source type:

| Variable | Description | GitHub Issues | Cron |
|----------|-------------|---------------|------|
| `{{.ID}}` | Unique identifier | Issue/PR number as string (e.g., `"42"`) | Date-time string (e.g., `"20260207-0900"`) |
| `{{.Number}}` | Issue or PR number | Issue/PR number (e.g., `42`) | `0` |
| `{{.Title}}` | Title of the work item | Issue/PR title | Trigger time (RFC3339) |
| `{{.Body}}` | Body text | Issue/PR body | Empty |
| `{{.URL}}` | URL to the source item | GitHub HTML URL | Empty |
| `{{.Labels}}` | Comma-separated labels | Issue/PR labels | Empty |
| `{{.Comments}}` | Concatenated comments | Issue/PR comments | Empty |
| `{{.Kind}}` | Type of work item | `"Issue"` or `"PR"` | `"Issue"` |
| `{{.Time}}` | Trigger time (RFC3339) | Empty | Cron tick time (e.g., `"2026-02-07T09:00:00Z"`) |
| `{{.Schedule}}` | Cron schedule expression | Empty | Schedule string (e.g., `"0 * * * *"`) |

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
| `status.deploymentName` | Name of the Deployment running the spawner |
| `status.totalDiscovered` | Total number of items discovered from the source |
| `status.totalTasksCreated` | Total number of Tasks created by this spawner |
| `status.activeTasks` | Number of currently active (non-terminal) Tasks |
| `status.lastDiscoveryTime` | Last time the source was polled |
| `status.message` | Additional information about the current status |
| `status.conditions` | Standard Kubernetes conditions for detailed status |

## Configuration

Axon reads defaults from `~/.axon/config.yaml` (override with `--config`). CLI flags always take precedence over config file values.

```yaml
# ~/.axon/config.yaml
oauthToken: <your-oauth-token>
# or: apiKey: <your-api-key>
model: claude-sonnet-4-5-20250929
namespace: my-namespace
```

### Credentials

| Field | Description |
|-------|-------------|
| `oauthToken` | OAuth token — Axon auto-creates the Kubernetes secret. Use `none` for an empty credential |
| `apiKey` | API key — Axon auto-creates the Kubernetes secret. Use `none` for an empty credential (e.g., free-tier OpenCode models) |
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

**Specify inline — Axon auto-creates the Workspace resource and secret:**

```yaml
workspace:
  repo: https://github.com/your-org/repo.git
  ref: main
  token: <your-github-token>  # optional, for private repos and gh CLI
```

| Field | Description |
|-------|-------------|
| `workspace.name` | Name of an existing Workspace resource |
| `workspace.repo` | Git repository URL — Axon auto-creates a Workspace resource |
| `workspace.ref` | Git reference (branch, tag, or commit SHA) |
| `workspace.token` | GitHub token — Axon auto-creates the secret and injects `GITHUB_TOKEN` |

If both `name` and `repo` are set, `name` takes precedence. The `--workspace` CLI flag overrides all config values.

### Other Settings

| Field | Description |
|-------|-------------|
| `type` | Default agent type (`claude-code`, `codex`, `gemini`, or `opencode`) |
| `model` | Default model override |
| `namespace` | Default Kubernetes namespace |
| `agentConfig` | Default AgentConfig resource name |

## CLI Reference

The `axon` CLI lets you manage the full lifecycle without writing YAML.

### Core Commands

| Command | Description |
|---------|-------------|
| `axon install` | Install Axon CRDs and controller into the cluster |
| `axon uninstall` | Uninstall Axon from the cluster |
| `axon init` | Initialize `~/.axon/config.yaml` |
| `axon version` | Print version information |

### Resource Management

| Command | Description |
|---------|-------------|
| `axon run` | Create and run a new Task |
| `axon create workspace` | Create a Workspace resource |
| `axon create agentconfig` | Create an AgentConfig resource |
| `axon get <resource> [name]` | List resources or view a specific resource (`tasks`, `taskspawners`, `workspaces`) |
| `axon delete <resource> <name>` | Delete a resource |
| `axon logs <task-name> [-f]` | View or stream logs from a task |
| `axon suspend taskspawner <name>` | Pause a TaskSpawner (stops polling, running tasks continue) |
| `axon resume taskspawner <name>` | Resume a paused TaskSpawner |

### `axon run` Flags

- `--prompt, -p`: Task prompt (required)
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

### `axon get` Flags

- `--output, -o`: Output format (`yaml` or `json`)
- `--detail, -d`: Show detailed information for a specific resource
- `--all-namespaces, -A`: List resources across all namespaces

### Common Flags

- `--config`: Path to config file (default `~/.axon/config.yaml`)
- `--namespace, -n`: Kubernetes namespace
- `--kubeconfig`: Path to kubeconfig file
- `--dry-run`: Print resources without creating them (supported by `run`, `create`, `install`)
- `--yes, -y`: Skip confirmation prompts
