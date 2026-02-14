# Axon

**The Kubernetes-native framework for orchestrating autonomous AI coding agents.**

[![CI](https://github.com/axon-core/axon/actions/workflows/ci.yaml/badge.svg)](https://github.com/axon-core/axon/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/axon-core/axon)](https://github.com/axon-core/axon)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

Axon is an orchestration framework that turns AI coding agents into scalable, autonomous Kubernetes workloads. By providing a standardized interface for agents (Claude Code, OpenAI Codex, Google Gemini) and powerful orchestration primitives, Axon allows you to build complex, self-healing AI development pipelines that run with full autonomy in isolated, ephemeral Pods.

## Framework Core

Axon is built on four main primitives that enable sophisticated agent orchestration:

1.  **Tasks**: Ephemeral units of work that wrap an AI agent run.
2.  **Workspaces**: Persistent or ephemeral environments (git repos) where agents operate.
3.  **AgentConfigs**: Reusable bundles of agent instructions and plugins.
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
  - [Inject agent instructions and plugins](#inject-agent-instructions-and-plugins)
  - [Auto-fix GitHub issues with TaskSpawner](#auto-fix-github-issues-with-taskspawner)
  - [Run tasks on a schedule (Cron)](#run-tasks-on-a-schedule-cron)
  - [Autonomous self-development pipeline](#autonomous-self-development-pipeline)
  - [Copy-paste YAML manifests](#copy-paste-yaml-manifests)
- [Framework Capabilities](#framework-capabilities)
- [Orchestration Patterns](#orchestration-patterns)
- [Reference](#reference)
- [Uninstall](#uninstall)
- [Development](#development)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

## Why Axon?

AI coding agents are evolving from interactive CLI tools into autonomous background workers. Axon provides the necessary infrastructure to manage this transition at scale.

- **Orchestration, not just execution** — Don't just run an agent; manage its entire lifecycle. Use `TaskSpawner` to build event-driven AI workers that react to GitHub issues, PRs, or schedules.
- **Safe autonomy** — Agents run with `--dangerously-skip-permissions` inside isolated, ephemeral Pods. They get full speed and autonomy within a zero-risk blast radius for your host and infrastructure.
- **Standardized Interface** — Plug in any agent (Claude, Codex, Gemini, or your own) using a simple container interface. Axon handles the Kubernetes plumbing, credential injection, and workspace management.
- **Massive Parallelism** — Fan out hundreds of agents across multiple repositories. Kubernetes handles the scheduling, resource management, and queueing.
- **Observable & CI-Native** — Every agent run is a first-class Kubernetes resource. Monitor progress via `kubectl`, integrate with ArgoCD, or trigger via GitHub Actions.

## How It Works

Axon orchestrates the flow from external events to autonomous execution:

```
  Triggers (GitHub, Cron) ──┐
                            │
  Manual (CLI, YAML) ───────┼──▶  TaskSpawner  ──▶  Tasks  ──▶  Isolated Pods
                            │          │              │             │
  API (CI/CD, Webhooks) ────┘          └─(Lifecycle)──┴─(Execution)─┴─(Success/Fail)
```

You define what needs to be done, and Axon handles the "how" — from cloning the right repo and injecting credentials to running the agent and capturing its outputs (like PR URLs and branch names).

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

- Kubernetes cluster (1.28+)
- kubectl configured

### 1. Install the CLI

```bash
go install github.com/axon-core/axon/cmd/axon@latest
```

### 2. Install Axon

```bash
axon install
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

### 4. Run Your First Task

```bash
axon run -p "Add a hello world program in Python"
axon logs <task-name> -f
```

The agent clones your repo, makes changes, and can push a branch or open a PR.

> **Note:** Without a workspace, the agent runs in an ephemeral pod — any files it
> creates are lost when the pod terminates. Set up a workspace to get persistent results.

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
      name: claude-oauth
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

### Inject agent instructions and plugins

Use `AgentConfig` to bundle agent instructions and Claude Code plugins. Tasks reference it via `agentConfigRef`:

```yaml
apiVersion: axon.io/v1alpha1
kind: AgentConfig
metadata:
  name: my-config
spec:
  agentsMD: |
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
      name: claude-oauth
  workspaceRef:
    name: my-workspace
  agentConfigRef:
    name: my-config
```

Or via the CLI:

```bash
axon run -p "Fix the bug" --agents-md "Follow TDD" --skill deploy=@skills/deploy.md --agent reviewer=@agents/reviewer.md
```

- `agentsMD` is written to `~/.claude/CLAUDE.md` (user-level, additive with the repo's own files)
- `plugins` are mounted as plugin directories and passed via `--plugin-dir`

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
        name: claude-credentials
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
  name: nightly-build-fix
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
        name: claude-credentials
    promptTemplate: "Run the full test suite and fix any flakes."
```

```bash
kubectl apply -f cron-spawner.yaml
```

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

## Framework Capabilities

| Capability | Details |
|---------|---------|
| **Event-Driven** | Automatically spawn Tasks from GitHub Issues, PRs, or Cron schedules using `TaskSpawner`. |
| **Pluggable Agents** | Bring any agent (Claude, Codex, Gemini) or your own custom image by implementing a simple shell interface. |
| **Safe Autonomy** | Run agents with full system access inside isolated, ephemeral Pods with zero risk to the host. |
| **Workspace Isolation** | Each run gets a dedicated, freshly cloned git workspace with automated credential injection. |
| **Standardized Outputs** | Axon captures deterministic outputs (branch names, PR URLs) from agent runs into Kubernetes status. |
| **Lifecycle Management** | Built-in TTL management, owner references, and automatic cleanup of finished runs. |
| **Infrastructure Scale** | Fan out hundreds of agents. Kubernetes handles scheduling, resource limits (CPU/MEM), and priorities. |
| **Credential Safety** | Securely manage API keys and OAuth tokens using Kubernetes Secrets, injected only when needed. |
| **Observable** | Track agent progress through `Pending` → `Running` → `Succeeded`/`Failed` using standard K8s tools. |
| **CLI & YAML** | Manage the entire lifecycle via the `axon` CLI or declarative YAML manifests (GitOps ready). |

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
| `spec.ttlSecondsAfterFinished` | Auto-delete task after N seconds (0 for immediate) | No |

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
| `spec.agentsMD` | Agent instructions written to `~/.claude/CLAUDE.md` (additive with repo files) | No |
| `spec.plugins[].name` | Plugin name (used as directory name and namespace) | Yes (per plugin) |
| `spec.plugins[].skills[].name` | Skill name (becomes `skills/<name>/SKILL.md`) | Yes (per skill) |
| `spec.plugins[].skills[].content` | Skill content (markdown with frontmatter) | Yes (per skill) |
| `spec.plugins[].agents[].name` | Agent name (becomes `agents/<name>.md`) | Yes (per agent) |
| `spec.plugins[].agents[].content` | Agent content (markdown with frontmatter) | Yes (per agent) |

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
| `spec.taskTemplate.ttlSecondsAfterFinished` | Auto-delete spawned tasks after N seconds | No |
| `spec.pollInterval` | How often to poll the source (default: `5m`) | No |
| `spec.maxConcurrency` | Limit max concurrent running tasks | No |

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
| `status.phase` | Current phase: `Pending`, `Running`, `Succeeded`, or `Failed` |
| `status.jobName` | Name of the Job created for this Task |
| `status.podName` | Name of the Pod running the Task |
| `status.startTime` | When the Task started running |
| `status.completionTime` | When the Task completed |
| `status.message` | Additional information about the current status |

</details>

<details>
<summary><strong>TaskSpawner Status</strong></summary>

| Field | Description |
|-------|-------------|
| `status.phase` | Current phase: `Pending`, `Running`, or `Failed` |
| `status.deploymentName` | Name of the Deployment running the spawner |
| `status.totalDiscovered` | Total number of items discovered from the source |
| `status.totalTasksCreated` | Total number of Tasks created by this spawner |
| `status.activeTasks` | Number of currently active (non-terminal) Tasks |
| `status.lastDiscoveryTime` | Last time the source was polled |
| `status.message` | Additional information about the current status |

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
| `axon get <resource>` | List resources (`tasks`, `taskspawners`, `workspaces`) |
| `axon delete <resource> <name>` | Delete a resource |
| `axon logs <task-name> [-f]` | View or stream logs from a task |

### Common Flags

- `--config`: Path to config file (default `~/.axon/config.yaml`)
- `--namespace, -n`: Kubernetes namespace
- `--kubeconfig`: Path to kubeconfig file
- `--dry-run`: Print resources without creating them (supported by `run`, `create`, `install`)
- `--output, -o`: Output format (`yaml` or `json`) (supported by `get`)
- `--yes, -y`: Skip confirmation prompts

</details>

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

## Roadmap

- **Task dependencies** — chain tasks so one waits for another to finish before starting, enabling agent pipelines in pure Kubernetes.

```
 Task: scaffold service ──▶ Task: write tests ──▶ Task: generate docs
```

## Contributing

1. Fork the repo and create a feature branch.
2. Make your changes and run `make verify` to ensure everything passes.
3. Open a pull request with a clear description of the change.

For significant changes, please open an issue first to discuss the approach.

## License

[Apache License 2.0](LICENSE)
