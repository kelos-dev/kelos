<h1 align="center">Kelos</h1>

<p align="center"><strong>Turn your development workflow into YAML. Orchestrate autonomous AI coding agents on Kubernetes.</strong></p>

<p align="center">
  <a href="https://github.com/kelos-dev/kelos/actions/workflows/ci.yaml"><img src="https://github.com/kelos-dev/kelos/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://github.com/kelos-dev/kelos/releases/latest"><img src="https://img.shields.io/github/v/release/kelos-dev/kelos" alt="Release"></a>
  <a href="https://github.com/kelos-dev/kelos"><img src="https://img.shields.io/github/stars/kelos-dev/kelos?style=flat" alt="GitHub Stars"></a>
  <a href="https://github.com/kelos-dev/kelos"><img src="https://img.shields.io/github/go-mod/go-version/kelos-dev/kelos" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-blue.svg" alt="License"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#kelos-developing-kelos">Kelos Developing Kelos</a> &middot;
  <a href="#examples">Examples</a> &middot;
  <a href="docs/reference.md">Reference</a> &middot;
  <a href="examples/">YAML Manifests</a>
</p>

Kelos lets you **define your development workflow as YAML** and run it continuously on Kubernetes. Declare what triggers agents, what they do, and how they hand off — Kelos handles the rest.

We use Kelos to develop Kelos. Five TaskSpawners run 24/7: triaging issues, fixing bugs, testing DX, brainstorming improvements, and tuning their own prompts. [See the full pipeline below.](#kelos-developing-kelos)

Supports **Claude Code**, **OpenAI Codex**, **Google Gemini**, **OpenCode**, and [custom agent images](docs/agent-image-interface.md).

## How It Works

Kelos orchestrates the flow from external events to autonomous execution:

<img width="2310" height="1582" alt="kelos-resources" src="https://github.com/user-attachments/assets/e36bdfef-5dfe-4e8f-ac74-9e66ab17f621" />

You define what needs to be done, and Kelos handles the "how" — from cloning the right repo and injecting credentials to running the agent and capturing its outputs (branch names, commit SHAs, PR URLs, and token usage).

### Core Primitives

Kelos is built on four resources:

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

## Kelos Developing Kelos

Kelos develops itself. Five TaskSpawners run 24/7, each handling a different part of the development lifecycle — fully autonomous.

<img width="2694" height="1966" alt="kelos-self-development" src="https://github.com/user-attachments/assets/7e8978ab-8b2f-496d-b3e3-d25ea9f01fbf" />

| TaskSpawner | Trigger | Model | Description |
|---|---|---|---|
| **kelos-workers** | GitHub Issues (`actor/kelos`) | Opus | Picks up issues, creates or updates PRs, self-reviews, and ensures CI passes |
| **kelos-triage** | GitHub Issues (`needs-actor`) | Opus | Classifies issues by kind/priority, detects duplicates, and recommends an actor |
| **kelos-fake-user** | Cron (daily 09:00 UTC) | Sonnet | Tests DX as a new user — follows docs, tries CLI workflows, files issues for problems found |
| **kelos-fake-strategist** | Cron (every 12 hours) | Opus | Explores new use cases, workflow improvements, and integration opportunities |
| **kelos-self-update** | Cron (daily 06:00 UTC) | Opus | Reviews and tunes prompts, configs, and workflow files — the pipeline improves itself |

Here's a trimmed snippet of `kelos-workers.yaml` — enough to show the pattern:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: kelos-workers
spec:
  when:
    githubIssues:
      labels: [actor/kelos]
      excludeLabels: [kelos/needs-input]
      priorityLabels:
        - priority/critical-urgent
        - priority/important-soon
  maxConcurrency: 3
  taskTemplate:
    model: opus
    type: claude-code
    branch: "kelos-task-{{.Number}}"
    promptTemplate: |
      You are a coding agent. You either
      - create a PR to fix the issue
      - update an existing PR to fix the issue
      - comment on the issue or the PR if you cannot fix it
      ...
  pollInterval: 1m
```

The key pattern is `excludeLabels: [kelos/needs-input]` — this creates a feedback loop where the agent works autonomously until it needs human input, then pauses. Removing the label re-queues the issue on the next poll.

See the full manifest at [`self-development/kelos-workers.yaml`](self-development/kelos-workers.yaml) and the [`self-development/` README](self-development/README.md) for setup instructions.

## Why Kelos?

AI coding agents are evolving from interactive CLI tools into autonomous background workers — managed like infrastructure, not invoked like commands. Kelos provides the framework to manage this transition at scale.

- **Workflow as YAML** — Define your development workflow declaratively: what triggers agents, what they do, and how they hand off. Version-control it, review it in PRs, and GitOps it like any other infrastructure.
- **Orchestration, not just execution** — Don't just run an agent; manage its entire lifecycle. Chain tasks with `dependsOn` and pass results (branch names, PR URLs, token usage) between pipeline stages. Use `TaskSpawner` to build event-driven workers that react to GitHub issues, PRs, or schedules.
- **Host-isolated autonomy** — Each task runs in an isolated, ephemeral Pod with a freshly cloned git workspace. Agents have no access to your host machine — use [scoped tokens and branch protection](#security-considerations) to control repository access.
- **Standardized interface** — Plug in any agent (Claude, Codex, Gemini, OpenCode, or your own) using a simple [container interface](docs/agent-image-interface.md). Kelos handles credential injection, workspace management, and Kubernetes plumbing.
- **Scalable parallelism** — Fan out agents across multiple repositories. Kubernetes handles scheduling, resource management, and queueing — scale is limited by your cluster capacity and API provider quotas.
- **Observable & CI-native** — Every agent run is a first-class Kubernetes resource with deterministic outputs (branch names, PR URLs, commit SHAs, token usage) captured into status. Monitor via `kubectl`, manage via the `kelos` CLI or declarative YAML (GitOps-ready), and integrate with ArgoCD or GitHub Actions.

## Quick Start

Get running in 5 minutes (most of the time is gathering credentials).

### Prerequisites

- Kubernetes cluster (1.28+)

<details>
<summary>Don't have a cluster? Create one locally with kind</summary>

1. [Install kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) (requires Docker)
2. Create a cluster:
   ```bash
   kind create cluster
   ```

This creates a single-node cluster and configures your kubeconfig automatically.

</details>

### 1. Install the CLI

```bash
curl -fsSL https://raw.githubusercontent.com/kelos-dev/kelos/main/hack/install.sh | bash
```

<details>
<summary>Alternative: install from source</summary>

```bash
go install github.com/kelos-dev/kelos/cmd/kelos@latest
```

</details>

### 2. Install Kelos

```bash
kelos install
```

This installs the Kelos controller and CRDs into the `kelos-system` namespace.

Verify the installation:

```bash
kubectl get pods -n kelos-system
kubectl get crds | grep kelos.dev
```

### 3. Initialize Your Config

```bash
kelos init
```

Edit `~/.kelos/config.yaml`:

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

**GitHub App** (recommended for production/org use):
For organizations, [GitHub Apps](https://docs.github.com/en/apps) are preferred over PATs — they offer fine-grained permissions, higher rate limits, and don't depend on a specific user account. Use `githubApp` instead of `token` in your workspace config:
```yaml
workspace:
  repo: https://github.com/your-org/repo.git
  ref: main
  githubApp:
    appID: "12345"
    installationID: "67890"
    privateKeyPath: ~/.config/my-app.private-key.pem
```
See the [Workspace reference](docs/reference.md#workspace) for details.

</details>

> **Warning:** Without a workspace, the agent runs in an ephemeral pod — any files it creates are lost when the pod terminates. Always set up a workspace to get persistent results.

### 4. Run Your First Task

```bash
$ kelos run -p "Add a hello world program in Python"
task/task-r8x2q created

$ kelos logs task-r8x2q -f
```

The task name (e.g. `task-r8x2q`) is auto-generated. Use `--name` to set a custom name, or `-w` to automatically watch task logs.

The agent clones your repo, makes changes, and can push a branch or open a PR.

> **Tip:** If something goes wrong, check the controller logs with
> `kubectl logs deployment/kelos-controller-manager -n kelos-system`.

<details>
<summary>Using kubectl and YAML instead of the CLI</summary>

Create a `Workspace` resource to define a git repository:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: Workspace
metadata:
  name: my-workspace
spec:
  repo: https://github.com/your-org/your-repo.git
  ref: main
```

Then reference it from a `Task`:

```yaml
apiVersion: kelos.dev/v1alpha1
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

Set `apiKey` instead of `oauthToken` in `~/.kelos/config.yaml`:

```yaml
apiKey: <your-api-key>
```

Or pass `--secret` to `kelos run` with a pre-created secret (api-key is the default credential type), or set `spec.credentials.type: api-key` in YAML.

</details>

## Examples

### Auto-fix GitHub issues with TaskSpawner

Create a TaskSpawner to automatically turn GitHub issues into agent tasks:

```yaml
apiVersion: kelos.dev/v1alpha1
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

### Chain tasks into pipelines

Use `dependsOn` to chain tasks into pipelines. A task in `Waiting` phase stays paused until all its dependencies succeed:

```bash
kelos run -p "Scaffold a new user service" --name scaffold --branch feature/user-service
kelos run -p "Write tests for the user service" --depends-on scaffold --branch feature/user-service
```

Tasks sharing the same `branch` are serialized automatically — only one runs at a time.

<details>
<summary>YAML equivalent</summary>

```yaml
apiVersion: kelos.dev/v1alpha1
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
apiVersion: kelos.dev/v1alpha1
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

Downstream tasks can reference upstream results in their prompt using `{{.Deps}}`:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: Task
metadata:
  name: open-pr
spec:
  type: claude-code
  prompt: |
    Open a PR for branch {{index .Deps "write-tests" "Results" "branch"}}.
  credentials:
    type: oauth
    secretRef:
      name: claude-oauth-token
  workspaceRef:
    name: my-workspace
  branch: feature/user-service
  dependsOn: [write-tests]
```

The `.Deps` map is keyed by dependency Task name. Each entry has `Results` (key-value map with branch, commit, pr, etc.) and `Outputs` (raw output lines). See [examples/07-task-pipeline](examples/07-task-pipeline/) for a full three-stage pipeline.

### Create PRs automatically

Add a `token` to your workspace config:

```yaml
workspace:
  repo: https://github.com/your-org/repo.git
  ref: main
  token: <your-github-token>
```

```bash
kelos run -p "Fix the bug described in issue #42 and open a PR with the fix"
```

The `gh` CLI and `GITHUB_TOKEN` are available inside the agent container, so the agent can push branches and create PRs autonomously.

### Inject agent instructions and MCP servers

Use `AgentConfig` to bundle project-wide instructions, plugins, and MCP servers:

```yaml
apiVersion: kelos.dev/v1alpha1
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
kelos run -p "Fix the bug" --agent-config my-config
```

- `agentsMD` is written to `~/.claude/CLAUDE.md` (user-level, additive with the repo's own instructions).
- `plugins` are mounted as plugin directories and passed via `--plugin-dir`.
- `mcpServers` are written to the agent's native MCP configuration. Supports `stdio`, `http`, and `sse` transport types.

See the [full AgentConfig spec](docs/reference.md#agentconfig) for plugins, skills, and agents configuration.

> Browse all ready-to-apply YAML manifests in the [`examples/`](examples/) directory.

## Orchestration Patterns

- **Autonomous Self-Development** — Build a feedback loop where agents pick up issues, write code, self-review, and fix CI flakes until the task is complete. See the [self-development pipeline](#kelos-developing-kelos).
- **Event-Driven Bug Fixing** — Automatically spawn agents to investigate and fix bugs as soon as they are labeled in GitHub. See [Auto-fix GitHub issues](#auto-fix-github-issues-with-taskspawner).
- **Fleet-Wide Refactoring** — Orchestrate a "fan-out" where dozens of agents apply the same refactoring pattern across a fleet of microservices in parallel.
- **Hands-Free CI/CD** — Embed agents as first-class steps in your deployment pipelines to generate documentation or perform automated migrations.
- **AI Worker Pools** — Maintain a pool of specialized agents (e.g., "The Security Fixer") that developers can trigger via simple Kubernetes resources.

## Reference

| Resource | Key Fields | Full Spec |
|----------|-----------|-----------|
| **Task** | `type`, `prompt`, `credentials`, `workspaceRef`, `dependsOn`, `branch` | [Reference](docs/reference.md#task) |
| **Workspace** | `repo`, `ref`, `secretRef` (PAT or GitHub App), `files` | [Reference](docs/reference.md#workspace) |
| **AgentConfig** | `agentsMD`, `plugins`, `mcpServers` | [Reference](docs/reference.md#agentconfig) |
| **TaskSpawner** | `when`, `taskTemplate`, `pollInterval`, `maxConcurrency` | [Reference](docs/reference.md#taskspawner) |

<details>
<summary><strong>CLI Reference</strong></summary>

| Command | Description |
|---------|-------------|
| `kelos install` | Install Kelos CRDs and controller into the cluster |
| `kelos uninstall` | Uninstall Kelos from the cluster |
| `kelos init` | Initialize `~/.kelos/config.yaml` |
| `kelos run` | Create and run a new Task |
| `kelos get <resource> [name]` | List resources or view a specific resource (`tasks`, `taskspawners`, `workspaces`) |
| `kelos delete <resource> <name>` | Delete a resource |
| `kelos logs <task-name> [-f]` | View or stream logs from a task |
| `kelos suspend taskspawner <name>` | Pause a TaskSpawner |
| `kelos resume taskspawner <name>` | Resume a paused TaskSpawner |

See [full CLI reference](docs/reference.md#cli-reference) for all flags and options.

</details>

## Security Considerations

Kelos runs agents in isolated, ephemeral Pods with no access to your host machine, SSH keys, or other processes. The risk surface is limited to what the injected credentials allow.

**What agents CAN do:** Push branches, create PRs, and call the GitHub API using the injected `GITHUB_TOKEN`.

**What agents CANNOT do:** Access your host, read other pods, reach other repositories, or access any credentials beyond what you explicitly inject.

Best practices:

- **Scope your GitHub tokens.** Use [fine-grained Personal Access Tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens#fine-grained-personal-access-tokens) restricted to specific repositories instead of broad `repo`-scoped classic tokens.
- **Enable branch protection.** Require PR reviews before merging to `main`. Agents can push branches and open PRs, but protected branches prevent direct pushes to your default branch.
- **Use `maxConcurrency` and `maxTotalTasks`.** Limit how many tasks a TaskSpawner can create to prevent runaway agent activity.
- **Use `podOverrides.activeDeadlineSeconds`.** Set a timeout to prevent tasks from running indefinitely.
- **Audit via Kubernetes.** Every agent run is a first-class Kubernetes resource — use `kubectl get tasks` and cluster audit logs to track what was created and by whom.

> **About `--dangerously-skip-permissions`:** Claude Code uses this flag for non-interactive operation. Despite the name, the actual risk is minimal — agents run inside ephemeral containers with no host access. The flag simply disables interactive approval prompts, which is necessary for autonomous execution.

Kelos uses standard Kubernetes RBAC — use namespace isolation to separate teams. Each TaskSpawner automatically creates a scoped ServiceAccount and RoleBinding.

## Cost and Limits

Running AI agents costs real money. Here's how to stay in control:

**Model costs vary significantly.** Opus is the most capable but most expensive model. Use `spec.model` (or `model` in config) to choose cheaper models like Sonnet for routine tasks and reserve Opus for complex work. Check the [API pricing](https://docs.anthropic.com/en/docs/about-claude/pricing) page for current rates.

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
kelos run -p "Fix the bug" --timeout 30m
```

**Use `suspend` for emergencies.** If costs are spiraling, pause a spawner immediately:

```bash
kelos suspend taskspawner my-spawner
# ... investigate ...
kelos resume taskspawner my-spawner
```

**Rate limits.** API providers enforce concurrency and token limits. If a task hits a rate limit mid-execution, it will likely fail. Use `maxConcurrency` to stay within your provider's limits.

## FAQ

<details>
<summary><strong>What agents does Kelos support?</strong></summary>

Kelos supports **Claude Code**, **OpenAI Codex**, **Google Gemini**, and **OpenCode** out of the box. You can also bring your own agent image using the [container interface](docs/agent-image-interface.md).

</details>

<details>
<summary><strong>Can I use Kelos without Kubernetes?</strong></summary>

No. Kelos is built on Kubernetes Custom Resources and requires a Kubernetes cluster. For local development, use [kind](https://kind.sigs.k8s.io/) (`kind create cluster`) to create a single-node cluster on your machine.

</details>

<details>
<summary><strong>Is it safe to give agents repo access?</strong></summary>

Agents run in isolated, ephemeral Pods with no host access. Their capabilities are limited to what you inject — typically a scoped GitHub token. Use fine-grained PATs, branch protection, and `maxConcurrency` to control the blast radius. See [Security Considerations](#security-considerations).

</details>

<details>
<summary><strong>How much does it cost to run?</strong></summary>

Costs depend on the model and task complexity. Check the [API pricing](https://docs.anthropic.com/en/docs/about-claude/pricing) page for current rates. Use `maxConcurrency`, timeouts, and model selection to stay in budget. See [Cost and Limits](#cost-and-limits).

</details>

## Uninstall

```bash
kelos uninstall
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

We welcome contributions of all kinds — see [good first issues](https://github.com/kelos-dev/kelos/labels/good%20first%20issue) for places to start.

## License

[Apache License 2.0](LICENSE)
