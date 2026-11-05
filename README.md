<h1 align="center">Kelos</h1>

<p align="center"><strong>Define and run coding-agent workflows on Kubernetes.</strong></p>

<p align="center">
  <a href="https://github.com/kelos-dev/kelos/actions/workflows/ci.yaml"><img src="https://github.com/kelos-dev/kelos/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://github.com/kelos-dev/kelos/releases/latest"><img src="https://img.shields.io/github/v/release/kelos-dev/kelos" alt="Release"></a>
  <a href="https://github.com/kelos-dev/kelos"><img src="https://img.shields.io/github/stars/kelos-dev/kelos?style=flat" alt="GitHub Stars"></a>
  <a href="https://github.com/kelos-dev/kelos"><img src="https://img.shields.io/github/go-mod/go-version/kelos-dev/kelos" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-blue.svg" alt="License"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#usage">Usage</a> &middot;
  <a href="#how-it-works">How It Works</a> &middot;
  <a href="#documentation">Documentation</a> &middot;
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

Kelos lets you define what coding agents do and the environments they run in:
repositories, files, skills, tools, compute, environment variables, and secrets.
Reuse those definitions for individual tasks, persistent sessions, and automated
pipelines on Kubernetes.

<p align="center">
  <picture>
    <source media="(max-width: 600px)" srcset="docs/images/kelos-resources-mobile.svg" />
    <img alt="Automated Tasks and interactive Sessions reference shared Workspace and AgentConfig resources and run on Kubernetes with configurable compute, volumes, environment variables, and secrets" src="docs/images/kelos-resources.svg" />
  </picture>
</p>

<p align="center">
  <img alt="Kelos Console with example data showing Test coverage and Code review subsessions within one Session, with Code review selected, a shared workspace indicator, a code diff, and tool output" src="docs/images/kelos-session-web-chat.png" />
  <br>
  <a href="#use-the-kelos-console">Set up the Console</a>
</p>

Kelos supports Claude Code, OpenAI Codex, Google Gemini, OpenCode, Cursor, and
[custom agent images](docs/agent-image-interface.md).

## Example: a coding task with its environment

Combine a shared repository and skills with compute and environment settings
for a task that fixes tests and opens a pull request:

```yaml
apiVersion: kelos.dev/v1alpha2
kind: Task
metadata:
  name: fix-tests
spec:
  prompt: "Fix the failing tests, run the test suite, and open a pull request."
  worker:
    type: codex
    credentials:
      type: oauth
      secretRef:
        name: codex-credentials
    workspaceRef:
      name: my-workspace
    agentConfigRefs:
      - name: team-config
    podOverrides:
      resources:
        requests:
          cpu: "2"
          memory: 4Gi
      env:
        - name: APP_ENV
          value: test
```

The referenced resources must already exist: a
[Workspace](docs/reference.md#workspace) defines the repository, injected files,
setup command, and Git credential Secret reference; an
[AgentConfig](docs/reference.md#agentconfig) supplies your team's instructions,
skills, and tools; and a [credential Secret](docs/reference.md#task-credential-secret-format)
authenticates the agent. For ordered stages and parallel tasks, see the
[pipeline examples](examples/07-task-pipeline/).

For repeated runs with Codex OAuth credentials, enable
[token refresh](docs/reference.md#codex-oauth-token-refresh) on the credential Secret.

## Why Kelos?

- Define agent compute, container images, volumes, environment variables, and
  secret references alongside the work to run.
- Reuse repositories, setup commands, instructions, skills, and tools across agents.
- Keep workflow and environment definitions in Git as Kubernetes resources.
- Keep interactive Sessions alive and reconnect from terminal or web clients.
- Trigger work from GitHub, Jira, Linear, cron schedules, or generic webhooks.
- Observe, limit, and operate agent workloads with familiar Kubernetes controls.

## Quick Start

You need a Kubernetes 1.28+ cluster with
[cert-manager](https://cert-manager.io/docs/installation/) installed.

Install the CLI:

```bash
curl -fsSL https://raw.githubusercontent.com/kelos-dev/kelos/main/hack/install.sh | bash
```

Homebrew and source installs are also available:

```bash
brew tap kelos-dev/tap
brew install kelos

# Or:
go install github.com/kelos-dev/kelos/cmd/kelos@latest
```

Install Kelos:

```bash
kelos install
```

For Helm installations and upgrades, see the
[chart documentation](internal/manifests/charts/kelos/README.md).

Install the first-party Kelos skill in your coding agent:

```bash
npx skills add kelos-dev/kelos
```

The skill teaches your agent how to configure Kelos, create the right
resources, operate workloads, and troubleshoot failures. You can now use Kelos
without writing configuration files or assembling CLI commands by hand.

## Usage

### Use the Kelos skill

Ask your coding agent to use the `/kelos` skill. Describe the outcome you want,
the agent provider, and the repository; the skill handles the Kelos resources
and commands.

#### Set up a repository

```text
Using the /kelos skill, configure Kelos on my current cluster for OpenAI Codex
and https://github.com/your-org/your-repo.

Name the Workspace my-workspace. Use my existing local Codex OAuth credentials.
Ask before creating or replacing any Kubernetes Secret, then verify the setup
with a small read-only Task.
```

#### Create your first Task

```text
Using the /kelos skill, create a Codex Task in my-workspace with this
prompt: "Add a hello world program in Python."

Run the Task, watch it until it finishes, and show me the logs and result.
```

#### Start a persistent session

```text
Using the /kelos skill, create a persistent Codex Session named my-session for
my-workspace, then connect me to it.
```

#### Automate issue fixes

```text
Using the /kelos skill, create a webhook-based TaskSpawner for GitHub issues
labeled "agent-ready" in your-org/your-repo.

Each task should reproduce the issue, add a tested fix, and open a pull request
without merging it. Limit concurrency to 3 and runtime to 1 hour. Show me the
resources before applying them.
```

#### Build your own self-development setup

```text
Using the /kelos skill, use this repository's self-development/ setup as a
reference and create a minimal self-development setup for
https://github.com/your-org/your-repo.

Start with issue pick-up and pull request review. Reuse shared configuration,
prefer GitHub webhooks, and show me the resources before applying them.
```

#### Troubleshoot a failed workload

```text
Using the /kelos skill, investigate why Task my-task failed in the default
namespace. Do not print Secret values. Fix configuration problems if it is safe
to do so, then retry and watch the Task.
```

These prompts are starting points. Your agent will inspect the cluster and ask
for missing choices or credentials when needed.

Use scoped credentials, protected branches, and workload limits for autonomous
agents.

### Use the CLI directly

Initialize `~/.kelos/config.yaml`, then add your agent credentials using the
comments in the generated file:

```bash
kelos init
```

#### Create a Workspace

```bash
kelos create workspace my-workspace \
  --repo https://github.com/your-org/your-repo.git \
  --ref main
```

#### Create a Task

Create and watch a Task in that Workspace:

```bash
kelos run \
  --workspace my-workspace \
  --prompt "Add a hello world program in Python" \
  --watch
```

The command prints the generated Task name. Use it to stream the agent logs:

```bash
kelos logs TASK_NAME -f
```

#### Start a Session

Replace the credential placeholder in the
[Session example](examples/16-session/), then apply and connect to it:

```bash
kubectl apply -f examples/16-session/
kelos session connect interactive-review
```

##### Use the Kelos Console

To inspect Kelos resources and create, organize, and chat with Sessions in a
browser, enable the optional Console server. Follow the
[Kelos Console guide](internal/manifests/charts/kelos/README.md#kelos-console)
to configure its shared token and access the cluster-internal web application.

See the [configuration reference](docs/reference.md#configuration) for other
agents and authentication options.

## What You Can Build

- **Developer-guided work:** reconnect to a long-running Session from a terminal
  or browser.
- **Event-driven workers:** turn issues, pull requests, webhooks, and schedules
  into agent Tasks.
- **Agent pipelines:** sequence implementation, review, testing, and delivery.
- **Repository fleets:** fan out the same change across many services.
- **Self-development loops:** let specialized agents triage, implement, review,
  and maintain a project continuously.

Kelos uses Kelos to develop itself. The
[self-development setup](self-development/README.md) shows the agents and
workflows running the project.

## How It Works

Tasks and Sessions reference shared Workspace and AgentConfig resources through
their worker configuration. Each worker selects its agent, model, image, and
Kubernetes runtime settings. The controller turns Kelos resources into Pods,
Jobs, and StatefulSets.

### Core Primitives

| Resource | Purpose |
| --- | --- |
| `Task` | Run one agent job |
| `TaskPipeline` | Run ordered Task stages with matrix fan-out |
| `Session` | Keep an interactive agent conversation running |
| `Workspace` | Give agents a Git repository, injected files, and setup commands |
| `AgentConfig` | Share instructions, skills, plugins, and MCP servers |
| `TaskSpawner` | Create Tasks from events or schedules |
| `SessionSpawner` | Create Sessions from GitHub webhooks |
| `WorkerPool` | Maintain reusable agent workers |

Task status records useful results such as branches, commits, pull requests,
and token usage. The resources remain ordinary Kubernetes objects, so advanced
users can review them, keep them in Git, or manage them with existing delivery
tools.

See the [API and CLI reference](docs/reference.md) for the underlying fields and
commands.

## Documentation

| Need | Documentation |
| --- | --- |
| Resource fields and CLI commands | [Reference](docs/reference.md) |
| Ready-to-apply patterns | [Examples](examples/) |
| GitHub, Jira, CI, and webhook connections | [Integration guide](docs/integration.md) |
| Console and interactive Sessions | [Session example](examples/16-session/) and [Console guide](internal/manifests/charts/kelos/README.md#kelos-console) |
| Custom agent containers | [Agent image interface](docs/agent-image-interface.md) |
| Helm installation and upgrades | [Chart documentation](internal/manifests/charts/kelos/README.md) |
| Contributing | [Contributing guide](CONTRIBUTING.md) |

## Development

Use the project Makefile:

```bash
make update
make verify
make test
make test-integration
make build
```

See the [contributing guide](CONTRIBUTING.md) for development setup and pull
request requirements.

## License

[Apache License 2.0](LICENSE)
