# Axon

**The Kubernetes-native framework for orchestrating autonomous AI coding agents.**

[![CI](https://github.com/axon-core/axon/actions/workflows/ci.yaml/badge.svg)](https://github.com/axon-core/axon/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/axon-core/axon)](https://github.com/axon-core/axon)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

Axon is an orchestration framework that turns AI coding agents into scalable, autonomous Kubernetes workloads. By providing a standardized interface for agents (Claude Code, OpenAI Codex, Google Gemini, OpenCode) and powerful orchestration primitives, Axon allows you to build complex, self-healing AI development pipelines that run with full autonomy in isolated, ephemeral Pods.

## Framework Core

Axon is built on four main primitives that enable sophisticated agent orchestration:

1.  **Tasks**: Ephemeral units of work that wrap an AI agent run.
2.  **Workspaces**: Persistent or ephemeral environments (git repos) where agents operate.
3.  **AgentConfigs**: Reusable bundles of agent instructions (`AGENTS.md`, `CLAUDE.md`), plugins (skills and agents), and MCP servers.
4.  **TaskSpawners**: Orchestration engines that react to external triggers (GitHub, Cron) to automatically manage agent lifecycles.

## Demo

```bash
# Initialize your config
$ axon init
# Edit ~/.axon/config.yaml with your token and workspace:
#   oauthToken: <your-oauth-token>
#   workspace:
#     repo: https://github.com/your-org/your-repo.git
#     ref: main
#     token: <github-token>

# Run a task against your repo
$ axon run -p "Fix the bug described in issue #42 and open a PR with the fix"
task/task-a5b3c created

# Stream the logs
$ axon logs task-a5b3c -f
```

https://github.com/user-attachments/assets/837cd8d5-4071-42dd-be32-114c649386ff

See [Examples](#examples) for a full autonomous self-development pipeline.

## Table of Contents

- [Framework Core](#framework-core)
- [Demo](#demo)
- [Why Axon?](#why-axon)
- [How It Works](#how-it-works)
- [Quick Start](#quick-start)
  - [Prerequisites](#prerequisites)
  - [1. Install the CLI](#1-install-the-cli)
  - [2. Install Axon](#2-install-axon)
  - [3. Initialize Your Config](#3-initialize-your-config)
  - [4. Run Your First Task](#4-run-your-first-task)
- [Examples](#examples)
  - [Run against a git repo](#run-against-a-git-repo)
  - [Create PRs automatically](#create-prs-automatically)
  - [Inject agent instructions, plugins, and MCP servers](#inject-agent-instructions-plugins-and-mcp-servers)
  - [Auto-fix GitHub issues with TaskSpawner](#auto-fix-github-issues-with-taskspawner)
  - [Run tasks on a schedule (Cron)](#run-tasks-on-a-schedule-cron)
  - [Chain tasks with dependencies](#chain-tasks-with-dependencies)
  - [Autonomous self-development pipeline](#autonomous-self-development-pipeline)
  - [Copy-paste YAML manifests](#copy-paste-yaml-manifests)
- [Orchestration Patterns](#orchestration-patterns)
- [Reference](#reference)
- [Security Considerations](#security-considerations)
- [Cost and Limits](#cost-and-limits)
- [Uninstall](#uninstall)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Why Axon?

AI coding agents are evolving from interactive CLI tools into autonomous background workers. Axon provides the necessary infrastructure to manage this transition at scale.

- **Orchestration, not just execution** — Don't just run an agent; manage its entire lifecycle. Chain tasks with `dependsOn` and pass results (branch names, PR URLs, token usage) between pipeline stages. Use `TaskSpawner` to build event-driven workers that react to GitHub issues, PRs, or schedules.
- **Host-isolated autonomy** — Each task runs in an isolated, ephemeral Pod with a freshly cloned git workspace. Agents have no access to your host machine — use [scoped tokens and branch protection](#security-considerations) to control repository access.
- **Standardized Interface** — Plug in any agent (Claude, Codex, Gemini, OpenCode, or your own) using a simple [container interface](docs/agent-image-interface.md). Axon handles credential injection, workspace management, and Kubernetes plumbing.
- **Scalable Parallelism** — Fan out agents across multiple repositories. Kubernetes handles scheduling, resource management, and queueing — scale is limited by your cluster capacity and API provider quotas.
- **Observable & CI-Native** — Every agent run is a first-class Kubernetes resource with deterministic outputs (branch names, PR URLs, commit SHAs, token usage) captured into status. Monitor via `kubectl`, manage via the `axon` CLI or declarative YAML (GitOps-ready), and integrate with ArgoCD or GitHub Actions.

## How It Works

Axon orchestrates the flow from external events to autonomous execution:

```
  Triggers (GitHub, Cron) ──┐
                            │
  Manual (CLI, YAML) ───────┼──▶  TaskSpawner  ──▶  Tasks  ──▶  Isolated Pods
                            │          │              │             │
  API (CI/CD, Webhooks) ────┘          └─(Lifecycle)──┴─(Execution)─┴─(Success/Fail)
```

You define what needs to be done, and Axon handles the "how" — from cloning the right repo and injecting credentials to running the agent and capturing its outputs (branch names, commit SHAs, PR URLs, and token usage).

<details>
<summary>TaskSpawner — Automatic Task Creation from External Sources</summary>

TaskSpawner watches external sources (e.g., GitHub Issues) and automatically creates Tasks for each discovered item.

```
                    polls         new issues
 TaskSpawner ─────────────▶ GitHub Issues
      │        ◀─────────────
      │
      ├──creates──▶ Task: fix-bugs-1
      └──creates──▶ Task: fix-bugs-2
```

</details>

## Quick Start

### Prerequisites

- Kubernetes cluster (1.28+) — don't have one? Create a local cluster with [kind](https://kind.sigs.k8s.io/): `kind create cluster`
- kubectl configured

### 1. Install the CLI

Download a pre-built binary from the [latest GitHub release](https://github.com/axon-core/axon/releases/latest):

```bash
curl -fsSL https://raw.githubusercontent.com/axon-core/axon/main/hack/install.sh | bash
```

Alternatively, install from source:

```bash
go install github.com/axon-core/axon/cmd/axon@latest
```

### 2. Install Axon

```bash
axon install
```

This installs the Axon controller and CRDs into the `axon-system` namespace. Tasks you create will run in your current kubeconfig namespace (default: `default`) unless you specify `--namespace` or set `namespace` in your config file.

To verify the installation:

```bash
kubectl get pods -n axon-system
kubectl get crds | grep axon.io
```

### 3. Initialize Your Config

```bash
axon init
# Edit ~/.axon/config.yaml with your token and workspace:
#   oauthToken: <your-oauth-token>
#   workspace:
#     repo: https://github.com/your-org/your-repo.git
#     ref: main
#     token: <github-token>  # optional, for private repos and pushing changes
```

<details>
<summary>How to get your credentials</summary>

**Claude OAuth token** (recommended for Claude Code):
Run `claude auth login` locally, then copy the token from `~/.claude/credentials.json`.

**Anthropic API key** (alternative):
Create one at [console.anthropic.com](https://console.anthropic.com). Set `apiKey` instead of `oauthToken` in your config.

**GitHub token** (for pushing branches and creating PRs):
Create a [Personal Access Token](https://github.com/settings/tokens) with `repo` scope (and `workflow` if your repo uses GitHub Actions).

</details>

### 4. Run Your First Task

```bash
$ axon run -p "Add a hello world program in Python"
task/task-r8x2q created

$ axon logs task-r8x2q -f
```

The task name (e.g. `task-r8x2q`) is auto-generated. Use `--name` to set a custom name, or `-w` to automatically watch task logs.

The agent clones your repo, makes changes, and can push a branch or open a PR.

> **Note:** Without a workspace, the agent runs in an ephemeral pod — any files it
> creates are lost when the pod terminates. Set up a workspace to get persistent results.

> **Tip:** If something goes wrong, check the controller logs with
> `kubectl logs deployment/axon-controller-manager -n axon-system`.

<details>
<summary>Using kubectl and YAML instead of the CLI</summary>

Create a `Workspace` resource to define a git repository:

```yaml
apiVersion: axon.io/v1alpha1
kind: Workspace
metadata:
  name: my-workspace
spec:
  repo: https://github.com/your-org/your-repo.git
  ref: main
```

Then reference it from a `Task`:

```yaml
apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: hello-world
spec:
  type: claude-code
  prompt: "Create a hello world program in Python"
  credentials:
    type: oauth
    secretRef:
      name: claude-oauth-token
  workspaceRef:
    name: my-workspace
```

```bash
kubectl apply -f workspace.yaml
kubectl apply -f task.yaml
kubectl get tasks -w
```

</details>

<details>
<summary>Using an API key instead of OAuth</summary>

Set `apiKey` instead of `oauthToken` in `~/.axon/config.yaml`:

```yaml
apiKey: <your-api-key>
```

Or pass `--secret` to `axon run` with a pre-created secret (api-key is the default credential type), or set `spec.credentials.type: api-key` in YAML.

</details>

## Examples

### Run against a git repo

Add `workspace` to your config:

```yaml
# ~/.axon/config.yaml
oauthToken: <your-oauth-token>
workspace:
  repo: https://github.com/your-org/repo.git
  ref: main
```

```bash
axon run -p "Add unit tests"
```

Axon auto-creates the Workspace resource from your config.

Or reference an existing Workspace resource with `--workspace`:

```bash
axon run -p "Add unit tests" --workspace my-workspace
```

### Create PRs automatically

Add a `token` to your workspace config:

```yaml
workspace:
  repo: https://github.com/your-org/repo.git
  ref: main
  token: <your-github-token>
```

```bash
axon run -p "Fix the bug described in issue #42 and open a PR with the fix"
```

The `gh` CLI and `GITHUB_TOKEN` are available inside the agent container, so the agent can push branches and create PRs autonomously.

### Inject agent instructions, plugins, and MCP servers

Use `AgentConfig` to bundle project-wide instructions (like `AGENTS.md` or `CLAUDE.md`), Claude Code plugins (skills and agents), and MCP servers. Tasks reference it via `agentConfigRef`:

```yaml
apiVersion: axon.io/v1alpha1
kind: AgentConfig
metadata:
  name: my-config
spec:
  agentsMD: |
    # Project Rules
    Follow TDD. Always write tests first.
  plugins:
    - name: team-tools
      skills:
        - name: deploy
          content: |
            ---
            name: deploy
            description: Deploy the application
            ---
            Deploy instructions here...
      agents:
        - name: reviewer
          content: |
            ---
            name: reviewer
            description: Code review specialist
            ---
            You are a code reviewer...
  mcpServers:
    - name: github
      type: http
      url: https://api.githubcopilot.com/mcp/
      headers:
        Authorization: "Bearer <token>"
    - name: my-tools
      type: stdio
      command: npx
      args: ["-y", "@my-org/mcp-tools"]
```

Reference it from a Task:

```yaml
apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: my-task
spec:
  type: claude-code
  prompt: "Fix the bug"
  credentials:
    type: oauth
    secretRef:
      name: claude-oauth-token
  workspaceRef:
    name: my-workspace
  agentConfigRef:
    name: my-config
```

Or via the CLI:

```bash
# Create an AgentConfig from local files
axon create agentconfig my-config \
  --agents-md @AGENTS.md \
  --skill deploy=@skills/deploy.md \
  --agent reviewer=@agents/reviewer.md \
  --mcp 'github={"type":"http","url":"https://api.githubcopilot.com/mcp/"}' \
  --mcp 'my-tools=@mcp/my-tools.json'

# Reference it when running a task
axon run -p "Fix the bug" --agent-config my-config
```

- `agentsMD` is written to `~/.claude/CLAUDE.md` (user-level, additive with the repo's own instructions like `AGENTS.md` or `CLAUDE.md`).
- `plugins` are mounted as plugin directories and passed via `--plugin-dir`.
- `mcpServers` are written to the agent's native MCP configuration (e.g., `~/.claude.json` for Claude Code, `~/.codex/config.toml` for Codex, `~/.gemini/settings.json` for Gemini). Supports `stdio`, `http`, and `sse` transport types.

### Auto-fix GitHub issues with TaskSpawner

Create a TaskSpawner to automatically turn GitHub issues into agent tasks:

```yaml
apiVersion: axon.io/v1alpha1
kind: TaskSpawner
metadata:
  name: fix-bugs
spec:
  when:
    githubIssues:
      labels: [bug]
      state: open
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: oauth
      secretRef:
        name: claude-oauth-token
    promptTemplate: "Fix: {{.Title}}\n{{.Body}}"
  pollInterval: 5m
```

```bash
kubectl apply -f taskspawner.yaml
```

TaskSpawner polls for new issues matching your filters and creates a Task for each one.

### Run tasks on a schedule (Cron)

Create a TaskSpawner that runs on a cron schedule (e.g., every hour):

```yaml
apiVersion: axon.io/v1alpha1
kind: TaskSpawner
metadata:
  name: hourly-test-fix
spec:
  when:
    cron:
      schedule: "0 * * * *" # Run every hour
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: oauth
      secretRef:
        name: claude-oauth-token
    promptTemplate: "Run the full test suite and fix any flakes."
```

```bash
kubectl apply -f cron-spawner.yaml
```

### Chain tasks with dependencies

Use `dependsOn` to chain tasks into pipelines. A task in `Waiting` phase stays paused until all its dependencies succeed:

```yaml
apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: scaffold
spec:
  type: claude-code
  prompt: "Scaffold a new user service with CRUD endpoints"
  credentials:
    type: oauth
    secretRef:
      name: claude-oauth-token
  workspaceRef:
    name: my-workspace
  branch: feature/user-service
---
apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: write-tests
spec:
  type: claude-code
  prompt: "Write comprehensive tests for the user service"
  credentials:
    type: oauth
    secretRef:
      name: claude-oauth-token
  workspaceRef:
    name: my-workspace
  branch: feature/user-service
  dependsOn: [scaffold]
```

```bash
kubectl apply -f pipeline.yaml
kubectl get tasks -w
# scaffold     claude-code   Running   ...
# write-tests  claude-code   Waiting   ...  (waits for scaffold to succeed)
```

Or via the CLI:

```bash
axon run -p "Scaffold a new user service" --name scaffold --branch feature/user-service
axon run -p "Write tests for the user service" --depends-on scaffold --branch feature/user-service
```

Tasks sharing the same `branch` are serialized automatically — only one runs at a time.

### Autonomous self-development pipeline

This is a real-world TaskSpawner that picks up every open issue, investigates it, opens (or updates) a PR, self-reviews, and ensures CI passes — fully autonomously. When the agent can't make progress, it labels the issue `axon/needs-input` and stops. Remove the label to re-queue it.

```
 ┌──────────────────────────────────────────────────────────────────┐
 │                        Feedback Loop                             │
 │                                                                  │
 │  ┌─────────────┐  polls  ┌────────────────┐                     │
 │  │ TaskSpawner │───────▶ │ GitHub Issues  │                     │
 │  └──────┬──────┘         │ (open, no      │                     │
 │         │                │  needs-input)  │                     │
 │         │ creates        └────────────────┘                     │
 │         ▼                                                       │
 │  ┌─────────────┐  runs   ┌─────────────┐  opens PR   ┌───────┐ │
 │  │    Task     │───────▶ │    Agent    │────────────▶│ Human │ │
 │  └─────────────┘  in Pod │   (Claude)  │  or labels  │Review │ │
 │                          └─────────────┘  needs-input└───┬───┘ │
 │                                                          │     │
 │                                           removes label ─┘     │
 │                                           (re-queues issue)    │
 └────────────────────────────────────────────────────────────────┘
```

See [`self-development/axon-workers.yaml`](self-development/axon-workers.yaml) for the full manifest and the [`self-development/` README](self-development/README.md) for setup instructions.

The key pattern here is `excludeLabels: [axon/needs-input]` — this creates a feedback loop where the agent works autonomously until it needs human input, then pauses. Removing the label re-queues the issue on the next poll.

### Copy-paste YAML manifests

The [`examples/`](examples/) directory contains self-contained, ready-to-apply YAML manifests for common use cases — from a simple Task with an API key to a full TaskSpawner driven by GitHub Issues or a cron schedule. Each example includes all required resources and clear `# TODO:` placeholders.

## Orchestration Patterns

- **Autonomous Self-Development** — Build a feedback loop where agents pick up issues, write code, self-review, and fix CI flakes until the task is complete.
- **Event-Driven Bug Fixing** — Automatically spawn agents to investigate and fix bugs as soon as they are labeled in GitHub.
- **Fleet-Wide Refactoring** — Orchestrate a "fan-out" where dozens of agents apply the same refactoring pattern across a fleet of microservices in parallel.
- **Hands-Free CI/CD** — Embed agents as first-class steps in your deployment pipelines to generate documentation or perform automated migrations.
- **AI Worker Pools** — Maintain a pool of specialized agents (e.g., "The Security Fixer") that developers can trigger via simple Kubernetes resources.

## Reference

<details>
<summary><strong>Task Spec</strong></summary>

| Field | Description | Required |
|-------|-------------|----------|
| `spec.type` | Agent type (`claude-code`, `codex`, `gemini`, or `opencode`) | Yes |
| `spec.prompt` | Task prompt for the agent | Yes |
| `spec.credentials.type` | `api-key` or `oauth` | Yes |
| `spec.credentials.secretRef.name` | Secret name with credentials | Yes |
| `spec.model` | Model override (e.g., `claude-sonnet-4-20250514`) | No |
| `spec.image` | Custom agent image override (see [Agent Image Interface](docs/agent-image-interface.md)) | No |
| `spec.workspaceRef.name` | Name of a Workspace resource to use | No |
| `spec.agentConfigRef.name` | Name of an AgentConfig resource to use | No |
| `spec.dependsOn` | Task names that must succeed before this Task starts (creates `Waiting` phase) | No |
| `spec.branch` | Git branch to work on; only one Task with the same branch runs at a time (mutex) | No |
| `spec.ttlSecondsAfterFinished` | Auto-delete task after N seconds (0 for immediate) | No |
| `spec.podOverrides.resources` | CPU/memory requests and limits for the agent container | No |
| `spec.podOverrides.activeDeadlineSeconds` | Maximum duration in seconds before the agent pod is terminated | No |
| `spec.podOverrides.env` | Additional environment variables (built-in vars take precedence on conflict) | No |
| `spec.podOverrides.nodeSelector` | Node selection labels to constrain which nodes run agent pods | No |

</details>

<details>
<summary><strong>Workspace Spec</strong></summary>

| Field | Description | Required |
|-------|-------------|----------|
| `spec.repo` | Git repository URL to clone (HTTPS, git://, or SSH) | Yes |
| `spec.ref` | Branch, tag, or commit SHA to checkout (defaults to repo's default branch) | No |
| `spec.secretRef.name` | Secret containing `GITHUB_TOKEN` for git auth and `gh` CLI | No |
| `spec.files[]` | Files to inject into the cloned repository before the agent starts | No |

</details>

<details>
<summary><strong>AgentConfig Spec</strong></summary>

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

</details>

<details>
<summary><strong>TaskSpawner Spec</strong></summary>

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
| `spec.taskTemplate.image` | Custom agent image override (see [Agent Image Interface](docs/agent-image-interface.md)) | No |
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

</details>

<a id="prompttemplate-variables"></a>
<details>
<summary><strong>promptTemplate Variables</strong></summary>

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

</details>

<details>
<summary><strong>Task Status</strong></summary>

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

</details>

<details>
<summary><strong>TaskSpawner Status</strong></summary>

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

</details>

<details>
<summary><strong>Configuration</strong></summary>

Axon reads defaults from `~/.axon/config.yaml` (override with `--config`). CLI flags always take precedence over config file values.

```yaml
# ~/.axon/config.yaml
oauthToken: <your-oauth-token>
# or: apiKey: <your-api-key>
model: claude-sonnet-4-5-20250929
namespace: my-namespace
```

#### Credentials

| Field | Description |
|-------|-------------|
| `oauthToken` | OAuth token — Axon auto-creates the Kubernetes secret. Use `none` for an empty credential |
| `apiKey` | API key — Axon auto-creates the Kubernetes secret. Use `none` for an empty credential (e.g., free-tier OpenCode models) |
| `secret` | (Advanced) Use a pre-created Kubernetes secret |
| `credentialType` | Credential type when using `secret` (`api-key` or `oauth`) |

**Precedence:** `--secret` flag > `secret` in config > `oauthToken`/`apiKey` in config.

#### Workspace

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

#### Other Settings

| Field | Description |
|-------|-------------|
| `type` | Default agent type (`claude-code`, `codex`, `gemini`, or `opencode`) |
| `model` | Default model override |
| `namespace` | Default Kubernetes namespace |
| `agentConfig` | Default AgentConfig resource name |

</details>

<details>
<summary><strong>CLI Reference</strong></summary>

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
| `axon get <resource>` | List resources (`tasks`, `taskspawners`, `workspaces`) |
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

### Common Flags

- `--config`: Path to config file (default `~/.axon/config.yaml`)
- `--namespace, -n`: Kubernetes namespace
- `--kubeconfig`: Path to kubeconfig file
- `--dry-run`: Print resources without creating them (supported by `run`, `create`, `install`)
- `--output, -o`: Output format (`yaml` or `json`) (supported by `get`)
- `--yes, -y`: Skip confirmation prompts

</details>

## Security Considerations

Axon runs agents in isolated, ephemeral Pods — they cannot access your host machine, SSH keys, or other processes. However, agents **do** have write access to your repositories and GitHub API via injected credentials. Here's how to manage the risk:

- **Scope your GitHub tokens.** Use [fine-grained Personal Access Tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens#fine-grained-personal-access-tokens) restricted to specific repositories instead of broad `repo`-scoped classic tokens.
- **Enable branch protection.** Require PR reviews before merging to `main`. Agents can push branches and open PRs, but protected branches prevent direct pushes to your default branch.
- **Use `maxConcurrency` and `maxTotalTasks`.** Limit how many tasks a TaskSpawner can create to prevent runaway agent activity.
- **Use `podOverrides.activeDeadlineSeconds`.** Set a timeout to prevent tasks from running indefinitely.
- **Audit via Kubernetes.** Every agent run is a first-class Kubernetes resource — use `kubectl get tasks` and cluster audit logs to track what was created and by whom.

> **Why `--dangerously-skip-permissions`?** Claude Code uses this flag to run without interactive approval prompts, which is necessary for autonomous execution. The name sounds alarming, but in Axon's context the agent runs inside an ephemeral container with no host access — the flag simply allows non-interactive operation. The actual risk surface is limited to what the injected credentials allow (repository writes, GitHub API calls).

Axon uses standard Kubernetes RBAC — use namespace isolation to separate teams. Each TaskSpawner automatically creates a scoped ServiceAccount and RoleBinding.

## Cost and Limits

Running AI agents costs real money. Here's how to stay in control:

**Model costs vary significantly.** Opus is the most capable but most expensive model. Use `spec.model` (or `model` in config) to choose cheaper models like Sonnet for routine tasks and reserve Opus for complex work.

**Use `maxConcurrency` to cap spend.** Without it, a TaskSpawner can create unlimited concurrent tasks. If 100 issues match your filter on first poll, that's 100 simultaneous agent runs. Always set a limit:

```yaml
spec:
  maxConcurrency: 3      # max 3 tasks running at once
  maxTotalTasks: 50       # stop after 50 total tasks
```

**Use `podOverrides.activeDeadlineSeconds` to limit runtime.** Set a timeout per task to prevent agents from running indefinitely:

```yaml
spec:
  podOverrides:
    activeDeadlineSeconds: 3600  # kill after 1 hour
```

Or via the CLI:

```bash
axon run -p "Fix the bug" --timeout 30m
```

**Use `suspend` for emergencies.** If costs are spiraling, pause a spawner immediately:

```bash
axon suspend taskspawner my-spawner
# ... investigate ...
axon resume taskspawner my-spawner
```

**Rate limits.** API providers enforce concurrency and token limits. If a task hits a rate limit mid-execution, it will likely fail. Use `maxConcurrency` to stay within your provider's limits.

## Uninstall

```bash
axon uninstall
```

## Development

Build, test, and iterate with `make`:

```bash
make update             # generate code, CRDs, fmt, tidy
make verify             # generate + vet + tidy-diff check
make test               # unit tests
make test-integration   # integration tests (envtest)
make test-e2e           # e2e tests (requires cluster)
make build              # build binary
make image              # build docker image
```

## Contributing

1. Fork the repo and create a feature branch.
2. Make your changes and run `make verify` to ensure everything passes.
3. Open a pull request with a clear description of the change.

For significant changes, please open an issue first to discuss the approach.

## License

[Apache License 2.0](LICENSE)
