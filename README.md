# Axon

**Run autonomous AI agents safely — at scale, in CI, on Kubernetes.**

[![CI](https://github.com/axon-core/axon/actions/workflows/ci.yaml/badge.svg)](https://github.com/axon-core/axon/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/axon-core/axon)](https://github.com/axon-core/axon)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

Axon is a Kubernetes controller that runs AI coding agents (Claude Code, OpenAI Codex, Google Gemini) in isolated, ephemeral Pods with full autonomy. You get the speed of unrestricted permissions without the risk — and the ability to fan out hundreds of agents in parallel across repos and CI pipelines.

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

https://github.com/user-attachments/assets/b45228ef-4885-4103-8edf-97de1a32c6db

See [Examples](#examples) for a full autonomous self-development pipeline.

## Why Axon?

AI coding agents are most powerful when they run fully autonomous — no permission prompts, no human in the loop. But on your laptop, that means the agent can touch your filesystem, network, and everything else on the host. And there's no easy way to fan out dozens of agents across repos or plug them into CI.

**Kubernetes solves both problems at once.** Inside a Pod, "dangerously skip permissions" isn't dangerous anymore — the agent gets full autonomy *within* an isolated, ephemeral container while the blast radius stays at zero for the host.

- **Safe autonomy** — agents run with `--dangerously-skip-permissions` inside isolated, ephemeral Pods. Full speed, zero risk to the host.
- **Scale out** — launch hundreds of agents in parallel across repositories. Kubernetes handles scheduling and resource management.
- **CI-native** — trigger agents from any pipeline. A Task is just a Kubernetes resource — create it with `kubectl`, Helm, Argo, or your own tooling.
- **Observable** — watch agents move through `Pending → Running → Succeeded/Failed` with kubectl.
- **Simple** — a handful of CRDs, one controller, zero dependencies beyond a running cluster.

## How It Works

```
 Task: refactor auth ──┐                ┌──▶ Isolated Pod ──▶ Succeeded
                       ├──▶  Axon  ─────┼──▶ Isolated Pod ──▶ Succeeded
 Task: add tests ──────┤                └──▶ Isolated Pod ──▶ Failed
                       │
 Task: update docs ────┘
```

You apply a Task, Axon runs it as an isolated Job with full autonomy, and tracks it through `Pending → Running → Succeeded/Failed`. Currently supported agents: **Claude Code**, **OpenAI Codex**, and **Google Gemini**.

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

## Features

| Feature | Details |
|---------|---------|
| Safe Autonomy | Agents run with `--dangerously-skip-permissions` inside isolated, ephemeral Pods |
| Scale Out | Run hundreds of agents in parallel — Kubernetes handles scheduling |
| CI-Native | Trigger agents from any pipeline via `kubectl`, Helm, Argo, or your own tooling |
| Git Workspace | Clone a repo into the agent's working directory via a Workspace resource, with optional `GITHUB_TOKEN` for private repos and PR creation |
| Config File | Set token, model, namespace, and workspace in `~/.axon/config.yaml` — secrets are auto-created |
| TaskSpawner | Automatically create Tasks from GitHub Issues (or other sources) via a long-running spawner |
| CLI | `axon install`, `axon uninstall`, `axon init`, `axon run`, `axon get`, `axon logs`, `axon delete` — manage the full lifecycle without writing YAML |
| Full Lifecycle | `Pending` → `Running` → `Succeeded` / `Failed` |
| Owner References | Delete a Task and its Job + Pod are automatically cleaned up |
| Credential Management | API key and OAuth supported via Kubernetes Secrets |
| Model Selection | Override the default model per-task with `spec.model` |
| Status Tracking | Job name, pod name, start/completion times, and messages |
| Leader Election | Safe multi-replica deployment out of the box |
| Minimal Footprint | Distroless container, 10m CPU / 64Mi memory requests |
| Extensible | Pluggable agent type — add new agents via the `switch` in `job_builder.go` |

## Use Cases

- **Hands-free CI** — let an autonomous agent generate, refactor, or fix code as a pipeline step, with no permission prompts blocking the run.
- **Batch refactoring at scale** — spin up dozens of agents in parallel to apply the same prompt across microservices, each safely isolated in its own Pod.
- **Scheduled maintenance** — pair with a CronJob or workflow engine to run recurring code-health agents on a schedule.
- **Developer self-service** — expose agent execution through an internal portal so any developer can run a fully autonomous AI agent without local setup.
- **AI in your internal platform** — embed Axon as the execution layer for AI-powered features in your developer platform.

## Reference

<details>
<summary><strong>Task Spec</strong></summary>

| Field | Description | Required |
|-------|-------------|----------|
| `spec.type` | Agent type (`claude-code`, `codex`, or `gemini`) | Yes |
| `spec.prompt` | Task prompt for the agent | Yes |
| `spec.credentials.type` | `api-key` or `oauth` | Yes |
| `spec.credentials.secretRef.name` | Secret name with credentials | Yes |
| `spec.model` | Model override (e.g., `claude-sonnet-4-20250514`) | No |
| `spec.image` | Custom agent image override (see [Agent Image Interface](docs/agent-image-interface.md)) | No |
| `spec.workspaceRef.name` | Name of a Workspace resource to use | No |
| `spec.ttlSecondsAfterFinished` | Auto-delete task after N seconds (0 for immediate) | No |

</details>

<details>
<summary><strong>Workspace Spec</strong></summary>

| Field | Description | Required |
|-------|-------------|----------|
| `spec.repo` | Git repository URL to clone (HTTPS, git://, or SSH) | Yes |
| `spec.ref` | Branch, tag, or commit SHA to checkout (defaults to repo's default branch) | No |
| `spec.secretRef.name` | Secret containing `GITHUB_TOKEN` for git auth and `gh` CLI | No |

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
| `spec.taskTemplate.type` | Agent type (`claude-code`, `codex`, or `gemini`) | Yes |
| `spec.taskTemplate.credentials` | Credentials for the agent (same as Task) | Yes |
| `spec.taskTemplate.model` | Model override | No |
| `spec.taskTemplate.image` | Custom agent image override (see [Agent Image Interface](docs/agent-image-interface.md)) | No |
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
| `oauthToken` | OAuth token — Axon auto-creates the Kubernetes secret |
| `apiKey` | API key — Axon auto-creates the Kubernetes secret |
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
| `type` | Default agent type (`claude-code`, `codex`, or `gemini`) |
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
