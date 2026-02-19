<h1 align="center">Axon</h1>

<p align="center"><strong>The Kubernetes-native framework for orchestrating autonomous AI coding agents.</strong></p>

<p align="center">
  <a href="https://github.com/axon-core/axon/actions/workflows/ci.yaml"><img src="https://github.com/axon-core/axon/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://github.com/axon-core/axon/releases/latest"><img src="https://img.shields.io/github/v/release/axon-core/axon" alt="Release"></a>
  <a href="https://github.com/axon-core/axon"><img src="https://img.shields.io/github/stars/axon-core/axon?style=flat" alt="GitHub Stars"></a>
  <a href="https://github.com/axon-core/axon"><img src="https://img.shields.io/github/go-mod/go-version/axon-core/axon" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-blue.svg" alt="License"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#examples">Examples</a> &middot;
  <a href="docs/reference.md">Reference</a> &middot;
  <a href="examples/">YAML Manifests</a>
</p>

Point Axon at a GitHub issue and get a PR back — fully autonomous, running in Kubernetes. Each agent runs in an isolated, ephemeral Pod with a freshly cloned git workspace. Fan out across repositories, chain tasks into pipelines, and react to events automatically.

Supports **Claude Code**, **OpenAI Codex**, **Google Gemini**, **OpenCode**, and [custom agent images](docs/agent-image-interface.md).

## Demo

```bash
# Run multiple tasks in parallel across your repo
$ axon run -p "Fix the bug described in issue #42 and open a PR" --name fix-42
$ axon run -p "Add unit tests for the auth module" --name add-tests
$ axon run -p "Update API docs for v2 endpoints" --name update-docs

# Watch all tasks progress simultaneously
$ axon get tasks
NAME          STATUS    AGE
fix-42        Running   2m
add-tests     Running   1m
update-docs   Running   45s
```

https://github.com/user-attachments/assets/837cd8d5-4071-42dd-be32-114c649386ff

See [Autonomous self-development pipeline](#autonomous-self-development-pipeline) for a full end-to-end example.

## Why Axon?

AI coding agents are evolving from interactive CLI tools into autonomous background workers. Axon provides the infrastructure to manage this transition at scale.

- **Orchestration, not just execution** — Don't just run an agent; manage its entire lifecycle. Chain tasks with `dependsOn` and pass results (branch names, PR URLs, token usage) between pipeline stages. Use `TaskSpawner` to build event-driven workers that react to GitHub issues, PRs, or schedules.
- **Host-isolated autonomy** — Each task runs in an isolated, ephemeral Pod with a freshly cloned git workspace. Agents have no access to your host machine — use [scoped tokens and branch protection](#security-considerations) to control repository access.
- **Standardized interface** — Plug in any agent (Claude, Codex, Gemini, OpenCode, or your own) using a simple [container interface](docs/agent-image-interface.md). Axon handles credential injection, workspace management, and Kubernetes plumbing.
- **Scalable parallelism** — Fan out agents across multiple repositories. Kubernetes handles scheduling, resource management, and queueing — scale is limited by your cluster capacity and API provider quotas.
- **Observable & CI-native** — Every agent run is a first-class Kubernetes resource with deterministic outputs (branch names, PR URLs, commit SHAs, token usage) captured into status. Monitor via `kubectl`, manage via the `axon` CLI or declarative YAML (GitOps-ready), and integrate with ArgoCD or GitHub Actions.

## Quick Start

Get running in 5 minutes (most of the time is gathering credentials).

### Prerequisites

- Kubernetes cluster (1.28+) — don't have one? Create a local cluster with [kind](https://kind.sigs.k8s.io/): `kind create cluster`
- kubectl configured

### 1. Install the CLI

```bash
curl -fsSL https://raw.githubusercontent.com/axon-core/axon/main/hack/install.sh | bash
```

<details>
<summary>Alternative: install from source</summary>

```bash
go install github.com/axon-core/axon/cmd/axon@latest
```

</details>

### 2. Install Axon

```bash
axon install
```

This installs the Axon controller and CRDs into the `axon-system` namespace.

Verify the installation:

```bash
kubectl get pods -n axon-system
kubectl get crds | grep axon.io
```

### 3. Initialize Your Config

```bash
axon init
```

Edit `~/.axon/config.yaml`:

```yaml
oauthToken: <your-oauth-token>
workspace:
  repo: https://github.com/your-org/your-repo.git
  ref: main
  token: <github-token>  # optional, for private repos and pushing changes
```

<details>
<summary>How to get your credentials</summary>

**Claude OAuth token** (recommended for Claude Code):
Run `claude auth login` locally, then copy the token from `~/.claude/credentials.json`.

**Anthropic API key** (alternative for Claude Code):
Create one at [console.anthropic.com](https://console.anthropic.com). Set `apiKey` instead of `oauthToken` in your config.

**Codex OAuth credentials** (for OpenAI Codex):
Run `codex auth login` locally, then reference the auth file in your config:
```yaml
oauthToken: "@~/.codex/auth.json"
type: codex
```
Or set `apiKey` with an OpenAI API key instead.

**GitHub token** (for pushing branches and creating PRs):
Create a [Personal Access Token](https://github.com/settings/tokens) with `repo` scope (and `workflow` if your repo uses GitHub Actions).

</details>

> **Warning:** Without a workspace, the agent runs in an ephemeral pod — any files it creates are lost when the pod terminates. Always set up a workspace to get persistent results.

### 4. Run Your First Task

```bash
$ axon run -p "Add a hello world program in Python"
task/task-r8x2q created

$ axon logs task-r8x2q -f
```

The task name (e.g. `task-r8x2q`) is auto-generated. Use `--name` to set a custom name, or `-w` to automatically watch task logs.

The agent clones your repo, makes changes, and can push a branch or open a PR.

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

### Core Primitives

Axon is built on four resources:

1. **Tasks** — Ephemeral units of work that wrap an AI agent run.
2. **Workspaces** — Persistent or ephemeral environments (git repos) where agents operate.
3. **AgentConfigs** — Reusable bundles of agent instructions (`AGENTS.md`, `CLAUDE.md`), plugins (skills and agents), and MCP servers.
4. **TaskSpawners** — Orchestration engines that react to external triggers (GitHub, Cron) to automatically manage agent lifecycles.

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

## Examples

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

### Chain tasks with dependencies

Use `dependsOn` to chain tasks into pipelines. A task in `Waiting` phase stays paused until all its dependencies succeed:

```bash
axon run -p "Scaffold a new user service" --name scaffold --branch feature/user-service
axon run -p "Write tests for the user service" --depends-on scaffold --branch feature/user-service
```

Tasks sharing the same `branch` are serialized automatically — only one runs at a time.

<details>
<summary>YAML equivalent</summary>

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

</details>

### Inject agent instructions and MCP servers

Use `AgentConfig` to bundle project-wide instructions, plugins, and MCP servers:

```yaml
apiVersion: axon.io/v1alpha1
kind: AgentConfig
metadata:
  name: my-config
spec:
  agentsMD: |
    # Project Rules
    Follow TDD. Always write tests first.
  mcpServers:
    - name: github
      type: http
      url: https://api.githubcopilot.com/mcp/
      headers:
        Authorization: "Bearer <token>"
```

```bash
axon run -p "Fix the bug" --agent-config my-config
```

- `agentsMD` is written to `~/.claude/CLAUDE.md` (user-level, additive with the repo's own instructions).
- `plugins` are mounted as plugin directories and passed via `--plugin-dir`.
- `mcpServers` are written to the agent's native MCP configuration. Supports `stdio`, `http`, and `sse` transport types.

See the [full AgentConfig spec](docs/reference.md#agentconfig) for plugins, skills, and agents configuration.

### Autonomous self-development pipeline

This is a real-world TaskSpawner that picks up every open issue, investigates it, opens (or updates) a PR, self-reviews, and ensures CI passes — fully autonomously. When the agent can't make progress, it labels the issue `axon/needs-input` and stops. Remove the label to re-queue it.

```
 ┌────────────────────────────────────────────────────────────────┐
 │                        Feedback Loop                           │
 │                                                                │
 │  ┌─────────────┐  polls  ┌────────────────┐                    │
 │  │ TaskSpawner │───────▶ │ GitHub Issues  │                    │
 │  └──────┬──────┘         │ (open, no      │                    │
 │         │                │  needs-input)  │                    │
 │         │ creates        └────────────────┘                    │
 │         ▼                                                      │
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

The key pattern is `excludeLabels: [axon/needs-input]` — this creates a feedback loop where the agent works autonomously until it needs human input, then pauses. Removing the label re-queues the issue on the next poll.

> Browse all ready-to-apply YAML manifests in the [`examples/`](examples/) directory.

## Orchestration Patterns

- **Autonomous Self-Development** — Build a feedback loop where agents pick up issues, write code, self-review, and fix CI flakes until the task is complete. See the [self-development pipeline](#autonomous-self-development-pipeline).
- **Event-Driven Bug Fixing** — Automatically spawn agents to investigate and fix bugs as soon as they are labeled in GitHub. See [Auto-fix GitHub issues](#auto-fix-github-issues-with-taskspawner).
- **Fleet-Wide Refactoring** — Orchestrate a "fan-out" where dozens of agents apply the same refactoring pattern across a fleet of microservices in parallel.
- **Hands-Free CI/CD** — Embed agents as first-class steps in your deployment pipelines to generate documentation or perform automated migrations.
- **AI Worker Pools** — Maintain a pool of specialized agents (e.g., "The Security Fixer") that developers can trigger via simple Kubernetes resources.

## Reference

| Resource | Key Fields | Full Spec |
|----------|-----------|-----------|
| **Task** | `type`, `prompt`, `credentials`, `workspaceRef`, `dependsOn`, `branch` | [Reference](docs/reference.md#task) |
| **Workspace** | `repo`, `ref`, `secretRef`, `files` | [Reference](docs/reference.md#workspace) |
| **AgentConfig** | `agentsMD`, `plugins`, `mcpServers` | [Reference](docs/reference.md#agentconfig) |
| **TaskSpawner** | `when`, `taskTemplate`, `pollInterval`, `maxConcurrency` | [Reference](docs/reference.md#taskspawner) |

<details>
<summary><strong>CLI Reference</strong></summary>

| Command | Description |
|---------|-------------|
| `axon install` | Install Axon CRDs and controller into the cluster |
| `axon uninstall` | Uninstall Axon from the cluster |
| `axon init` | Initialize `~/.axon/config.yaml` |
| `axon run` | Create and run a new Task |
| `axon get <resource>` | List resources (`tasks`, `taskspawners`, `workspaces`) |
| `axon delete <resource> <name>` | Delete a resource |
| `axon logs <task-name> [-f]` | View or stream logs from a task |
| `axon suspend taskspawner <name>` | Pause a TaskSpawner |
| `axon resume taskspawner <name>` | Resume a paused TaskSpawner |

See [full CLI reference](docs/reference.md#cli-reference) for all flags and options.

</details>

## Security Considerations

Axon runs agents in isolated, ephemeral Pods with no access to your host machine, SSH keys, or other processes. The risk surface is limited to what the injected credentials allow.

**What agents CAN do:** Push branches, create PRs, and call the GitHub API using the injected `GITHUB_TOKEN`.

**What agents CANNOT do:** Access your host, read other pods, reach other repositories, or access any credentials beyond what you explicitly inject.

Best practices:

- **Scope your GitHub tokens.** Use [fine-grained Personal Access Tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens#fine-grained-personal-access-tokens) restricted to specific repositories instead of broad `repo`-scoped classic tokens.
- **Enable branch protection.** Require PR reviews before merging to `main`. Agents can push branches and open PRs, but protected branches prevent direct pushes to your default branch.
- **Use `maxConcurrency` and `maxTotalTasks`.** Limit how many tasks a TaskSpawner can create to prevent runaway agent activity.
- **Use `podOverrides.activeDeadlineSeconds`.** Set a timeout to prevent tasks from running indefinitely.
- **Audit via Kubernetes.** Every agent run is a first-class Kubernetes resource — use `kubectl get tasks` and cluster audit logs to track what was created and by whom.

> **About `--dangerously-skip-permissions`:** Claude Code uses this flag for non-interactive operation. Despite the name, the actual risk is minimal — agents run inside ephemeral containers with no host access. The flag simply disables interactive approval prompts, which is necessary for autonomous execution.

Axon uses standard Kubernetes RBAC — use namespace isolation to separate teams. Each TaskSpawner automatically creates a scoped ServiceAccount and RoleBinding.

## Cost and Limits

Running AI agents costs real money. Here's how to stay in control:

**Model costs vary significantly.** Opus is the most capable but most expensive model. Use `spec.model` (or `model` in config) to choose cheaper models like Sonnet for routine tasks and reserve Opus for complex work. As a rough guide, a typical 15-minute Sonnet task costs ~$1-3, while an Opus task of the same length can cost ~$5-15 depending on context size.

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

## FAQ

<details>
<summary><strong>What agents does Axon support?</strong></summary>

Axon supports **Claude Code**, **OpenAI Codex**, **Google Gemini**, and **OpenCode** out of the box. You can also bring your own agent image using the [container interface](docs/agent-image-interface.md).

</details>

<details>
<summary><strong>Can I use Axon without Kubernetes?</strong></summary>

No. Axon is built on Kubernetes Custom Resources and requires a Kubernetes cluster. For local development, use [kind](https://kind.sigs.k8s.io/) (`kind create cluster`) to create a single-node cluster on your machine.

</details>

<details>
<summary><strong>Is it safe to give agents repo access?</strong></summary>

Agents run in isolated, ephemeral Pods with no host access. Their capabilities are limited to what you inject — typically a scoped GitHub token. Use fine-grained PATs, branch protection, and `maxConcurrency` to control the blast radius. See [Security Considerations](#security-considerations).

</details>

<details>
<summary><strong>How much does it cost to run?</strong></summary>

Costs depend on the model and task complexity. As a rough guide: a 15-minute Sonnet task costs ~$1-3, while Opus costs ~$5-15 for the same duration. Use `maxConcurrency`, timeouts, and model selection to stay in budget. See [Cost and Limits](#cost-and-limits).

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

## Contributing

1. Fork the repo and create a feature branch.
2. Make your changes and run `make verify` to ensure everything passes.
3. Open a pull request with a clear description of the change.

For significant changes, please open an issue first to discuss the approach.

We welcome contributions of all kinds — see [good first issues](https://github.com/axon-core/axon/labels/good%20first%20issue) for places to start.

## License

[Apache License 2.0](LICENSE)
