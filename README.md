# Axon

**Run autonomous AI agents safely — at scale, in CI, on Kubernetes.**

[![CI](https://github.com/gjkim42/axon/actions/workflows/ci.yaml/badge.svg)](https://github.com/gjkim42/axon/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gjkim42/axon)](https://github.com/gjkim42/axon)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

## Why Axon?

AI coding agents like Claude Code are most powerful when they run with `--dangerously-skip-permissions` — no human in the loop, fully autonomous. But on your laptop, that flag lives up to its name: the agent can touch your filesystem, network, and everything else on the host. And there's no easy way to fan out dozens of agents across repos, or plug them into a CI pipeline.

**Kubernetes solves both problems at once.** Inside a Pod, "dangerously skip permissions" isn't dangerous anymore — the agent gets full autonomy *within* an isolated, ephemeral container while the blast radius stays at zero for the host. And because it's Kubernetes, you get parallelism, scheduling, and CI integration for free.

Axon is the controller that ties it together. Define what you want the agent to do, and Axon runs it as an isolated Job with `--dangerously-skip-permissions` enabled by default.

- **Safe autonomy** — agents run with `--dangerously-skip-permissions` inside isolated, ephemeral Pods. Full speed, zero risk to the host.
- **Scale out** — launch hundreds of agents in parallel across repositories. Kubernetes handles scheduling and resource management.
- **CI-native** — trigger agents from any pipeline. A Task is just a Kubernetes resource — create it with `kubectl`, Helm, Argo, or your own tooling.
- **Observable** — watch agents move through `Pending → Running → Succeeded/Failed` with kubectl.
- **Simple** — one CRD, one controller, zero dependencies beyond a running cluster.

## How It Works

```mermaid
flowchart LR
    T1["Task: refactor auth"] --> AC[Axon Controller]
    T2["Task: add tests"] --> AC
    T3["Task: update docs"] --> AC

    AC --> J1[Job 1] --> P1["Pod 1 (--dangerously-skip-permissions)"] --> S1[Succeeded]
    AC --> J2[Job 2] --> P2["Pod 2 (--dangerously-skip-permissions)"] --> S2[Succeeded]
    AC --> J3[Job 3] --> P3["Pod 3 (--dangerously-skip-permissions)"] --> F3[Failed]
```

### TaskSpawner — Automatic Task Creation from External Sources

TaskSpawner watches external sources (e.g., GitHub Issues) and automatically creates Tasks for each discovered item.

```mermaid
flowchart LR
    TS["TaskSpawner"] -- "polls" --> GH["GitHub Issues"]
    GH -- "new issues" --> TS
    TS -- "creates" --> T1["Task: fix-bugs-1"]
    TS -- "creates" --> T2["Task: fix-bugs-2"]
```

---

1. You **apply a Task** manifest with a prompt, agent type, credential reference, and optionally a git repo to clone.
2. The **Axon controller** creates a Kubernetes **Job**.
3. If a workspace is specified, an **init container clones the repo**. Then the agent Pod starts with `--dangerously-skip-permissions`.
4. The agent runs **fully autonomous** inside the container — no permission prompts, no risk to the host.
5. Axon **tracks status** (phase, pod name, timestamps, messages). Delete the Task and the Job + Pod are automatically cleaned up via owner references.

Currently supported agents: **Claude Code** (`claude-code`).

## Quick Start

### Prerequisites

- Kubernetes cluster (1.28+)
- kubectl configured

### Install

```bash
kubectl apply -f https://raw.githubusercontent.com/gjkim42/axon/main/install-crd.yaml
kubectl apply -f https://raw.githubusercontent.com/gjkim42/axon/main/install.yaml
```

### Install the CLI

```bash
go install github.com/gjkim42/axon/cmd/axon@latest
```

### Run Your First Task

1. Initialize a config file:

```bash
axon init
```

Edit `~/.axon/config.yaml` with your token:

```yaml
oauthToken: <your-oauth-token>
```

2. Run a task:

```bash
axon run -p "Create a hello world program in Python"
```

Axon auto-creates the Kubernetes secret from your token.

3. Stream the logs:

```bash
axon logs <task-name> -f
```

```
[init] model=claude-sonnet-4-20250514

--- Turn 1 ---
I'll create a hello world program in Python.
[tool] Write

--- Turn 2 ---
The file has been created.

[result] completed (2 turns, $0.0035)
```

Run against a git repo — add `workspace` to your config:

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

Have the agent create a PR — add a `token` to your workspace config:

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

Or reference an existing Workspace resource with `--workspace`:

```bash
axon run -p "Add unit tests" --workspace my-workspace
```

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

## Features

| Feature | Details |
|---------|---------|
| Safe Autonomy | Agents run with `--dangerously-skip-permissions` inside isolated, ephemeral Pods |
| Scale Out | Run hundreds of agents in parallel — Kubernetes handles scheduling |
| CI-Native | Trigger agents from any pipeline via `kubectl`, Helm, Argo, or your own tooling |
| Git Workspace | Clone a repo into the agent's working directory via a Workspace resource, with optional `GITHUB_TOKEN` for private repos and PR creation |
| Config File | Set token, model, namespace, and workspace in `~/.axon/config.yaml` — secrets are auto-created |
| TaskSpawner | Automatically create Tasks from GitHub Issues (or other sources) via a long-running spawner |
| CLI | `axon init`, `axon run`, `axon get`, `axon logs`, `axon delete` — manage tasks without writing YAML |
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

### Task Spec

| Field | Description | Required |
|-------|-------------|----------|
| `spec.type` | Agent type (`claude-code`) | Yes |
| `spec.prompt` | Task prompt for the agent | Yes |
| `spec.credentials.type` | `api-key` or `oauth` | Yes |
| `spec.credentials.secretRef.name` | Secret name with credentials | Yes |
| `spec.model` | Model override (e.g., `claude-sonnet-4-20250514`) | No |
| `spec.workspaceRef.name` | Name of a Workspace resource to use | No |

### Workspace Spec

| Field | Description | Required |
|-------|-------------|----------|
| `spec.repo` | Git repository URL to clone (HTTPS, git://, or SSH) | Yes |
| `spec.ref` | Branch, tag, or commit SHA to checkout (defaults to repo's default branch) | No |
| `spec.secretRef.name` | Secret containing `GITHUB_TOKEN` for git auth and `gh` CLI | No |

### TaskSpawner Spec

| Field | Description | Required |
|-------|-------------|----------|
| `spec.when.githubIssues.workspaceRef.name` | Workspace resource (repo URL, auth, and clone target for spawned Tasks) | Yes |
| `spec.when.githubIssues.labels` | Filter issues by labels | No |
| `spec.when.githubIssues.state` | Filter by state: `open`, `closed`, `all` (default: `open`) | No |
| `spec.taskTemplate.type` | Agent type (`claude-code`) | Yes |
| `spec.taskTemplate.credentials` | Credentials for the agent (same as Task) | Yes |
| `spec.taskTemplate.model` | Model override | No |
| `spec.taskTemplate.promptTemplate` | Go text/template for prompt (`{{.Title}}`, `{{.Body}}`, `{{.Number}}`, etc.) | No |
| `spec.pollInterval` | How often to poll the source (default: `5m`) | No |

<details>
<summary>Example TaskSpawner YAML</summary>

```yaml
apiVersion: axon.io/v1alpha1
kind: TaskSpawner
metadata:
  name: fix-bugs
spec:
  when:
    githubIssues:
      workspaceRef:
        name: my-workspace
      labels: [bug]
      state: open
  taskTemplate:
    type: claude-code
    credentials:
      type: oauth
      secretRef:
        name: claude-credentials
    promptTemplate: "Fix: {{.Title}}\n{{.Body}}"
  pollInterval: 5m
```

</details>

### Task Status

| Field | Description |
|-------|-------------|
| `status.phase` | Current phase: `Pending`, `Running`, `Succeeded`, or `Failed` |
| `status.jobName` | Name of the Job created for this Task |
| `status.podName` | Name of the Pod running the Task |
| `status.startTime` | When the Task started running |
| `status.completionTime` | When the Task completed |
| `status.message` | Additional information about the current status |

### Configuration

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
| `model` | Default model override |
| `namespace` | Default Kubernetes namespace |

### CLI

The `axon` CLI lets you manage tasks without writing YAML.

```bash
# Initialize a config file
axon init

# Run a task
axon run -p "Refactor auth to use JWT"

# Run against a git repo (requires a Workspace resource)
axon run -p "Add unit tests" --workspace my-workspace

# Override config file defaults with CLI flags
axon run -p "Fix bug" --secret other-secret --credential-type api-key

# List tasks
axon get tasks

# List task spawners
axon get taskspawners

# View logs (follow mode)
axon logs my-task -f

# Delete a task
axon delete my-task
```

## Uninstall

```bash
kubectl delete -f https://raw.githubusercontent.com/gjkim42/axon/main/install.yaml
kubectl delete -f https://raw.githubusercontent.com/gjkim42/axon/main/install-crd.yaml
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

```mermaid
flowchart LR
    T1["Task: scaffold service"] --> T2["Task: write tests"]
    T2 --> T3["Task: generate docs"]
```

## Contributing

1. Fork the repo and create a feature branch.
2. Make your changes and run `make verify` to ensure everything passes.
3. Open a pull request with a clear description of the change.

For significant changes, please open an issue first to discuss the approach.

## License

[Apache 2.0](LICENSE)
