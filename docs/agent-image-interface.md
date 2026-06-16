# Standardized Agent Interface

This document describes the interface that custom agent images must implement
to be compatible with the Kelos orchestration framework.

## Overview

Kelos orchestrates agent tasks as Kubernetes Jobs. By providing a standardized
execution interface, Kelos allows any compatible image to be used as a drop-in
replacement for the default agents.

## Requirements

### 1. Entrypoint

The image must provide an executable at `/kelos_entrypoint.sh`. Kelos sets
`Command: ["/kelos_entrypoint.sh"]` on the container, overriding any
`ENTRYPOINT` in the Dockerfile.

### 2. Prompt argument

The task prompt is passed as the first positional argument (`$1`). Kelos sets
`Args: ["<prompt>"]` on the container.

### 3. Environment variables

Kelos sets the following reserved environment variables on agent containers:

| Variable | Description | Always set? |
|---|---|---|
| `KELOS_MODEL` | The model name to use | Only when `model` is specified in the Task |
| `KELOS_EFFORT` | Agent reasoning effort to use | Only when `effort` is specified in the Task |
| `ANTHROPIC_API_KEY` | API key for Anthropic (`claude-code` agent, api-key credential type) | When credential type is `api-key` and agent type is `claude-code` |
| `CODEX_API_KEY` | API key for OpenAI Codex (`codex` agent, `api-key` credential type) | When credential type is `api-key` and agent type is `codex` |
| `CODEX_AUTH_JSON` | Contents of `~/.codex/auth.json` (`codex` agent, `oauth` credential type) | When credential type is `oauth` and agent type is `codex` |
| `GEMINI_API_KEY` | API key for Google Gemini (`gemini` agent, api-key or oauth credential type) | When agent type is `gemini` |
| `OPENCODE_API_KEY` | API key for OpenCode (`opencode` agent, api-key or oauth credential type) | When agent type is `opencode` |
| `CURSOR_API_KEY` | API key for Cursor CLI (`cursor` agent, api-key or oauth credential type) | When agent type is `cursor` |
| `CLAUDE_CODE_OAUTH_TOKEN` | OAuth token (`claude-code` agent, oauth credential type) | When credential type is `oauth` and agent type is `claude-code` |
| `GITHUB_TOKEN` | GitHub token for workspace access | When workspace has a `secretRef` |
| `GH_TOKEN` | GitHub token for `gh` CLI (github.com) | When workspace has a `secretRef` and repo is on github.com |
| `GH_ENTERPRISE_TOKEN` | GitHub token for `gh` CLI (GitHub Enterprise) | When workspace has a `secretRef` and repo is on a GitHub Enterprise host |
| `GH_HOST` | Hostname for GitHub Enterprise | When repo is on a GitHub Enterprise host |
| `KELOS_AGENT_TYPE` | The agent type (`claude-code`, `codex`, `gemini`, `opencode`, `cursor`) | Always |
| `KELOS_BASE_BRANCH` | The base branch (workspace `ref`) for the task | When workspace has a non-empty `ref` |
| `KELOS_AGENTS_MD` | User-level instructions from AgentConfig | When `agentConfigRefs` is set and `agentsMD` is non-empty |
| `KELOS_PLUGIN_DIR` | Path to plugin directory containing skills and agents. Each subdirectory is one plugin in the `<plugin>/skills/<skill>/SKILL.md` layout; skills.sh packages from `spec.skills` appear under the `skills-sh` plugin | When `agentConfigRefs` is set and `plugins` or `skills` is non-empty |
| `KELOS_KANON_HOME` | Path to a cloned Kanon source repository. Codex and Claude Code entrypoints run `kanon apply --yes --home "$KELOS_KANON_HOME" --agent <agent>` before starting the agent | When `agentConfigRefs` resolves to `spec.kanon` and the agent type is `codex` or `claude-code` |
| `KELOS_SETUP_COMMAND` | JSON-encoded exec-form array from `Workspace.spec.setupCommand`, executed by the entrypoint before the agent starts | When the workspace defines `setupCommand` |

> The names listed in this table are reserved for Kelos behavior. When Kelos
> sets one on a Task, `PodOverrides.Env` entries that reuse the same name are
> dropped so the built-in value wins; do not set them manually.
> `KELOS_SETUP_COMMAND` and `KELOS_KANON_HOME` are additionally dropped from
> `PodOverrides.Env` even when Kelos does not define them, because they drive
> entrypoint behavior.

The bundled agent images handle `KELOS_EFFORT` as follows:

- `codex`: sets Codex `model_reasoning_effort`.
- `claude-code`: passes `--effort`.
- `gemini`: writes a temporary model alias with `thinkingConfig` when the model family supports it, otherwise adds effort guidance to user-level instructions.
- `opencode`: writes agent model options including `reasoningEffort` and provider variants where available.
- `cursor`: adds effort guidance to user-level instructions because Cursor CLI does not expose a documented effort flag.

The bundled Codex and Claude Code images include the Kanon CLI and apply
Kanon-backed AgentConfigs before running the agent. Custom images for those
agent types must include `kanon` on `PATH` and handle `KELOS_KANON_HOME` the
same way if they need to support `AgentConfig.spec.kanon`.

### 4. User ID

The agent image must be configured to run as **UID 61100**. This UID is shared
between the `git-clone` init container and the agent container so that both can
read and write workspace files without additional permission workarounds.

Set this in your Dockerfile:

```dockerfile
RUN useradd -u 61100 -m -s /bin/bash agent
USER agent
```

### 5. Working directory

When a workspace is configured, Kelos mounts the cloned repository at
`/workspace/repo` and sets `WorkingDir` on the container accordingly. The
entrypoint script does not need to handle directory changes.

### 6. User-writable bin directory on PATH

The image must provide a writable directory on `PATH` so that `setupCommand`
can install additional tools and have them resolved by name from the agent
process. The reference images use `$HOME/.local/bin` (the conventional XDG /
PEP 370 user-install location used by `pip install --user`, `npm config set
prefix ~/.local`, and similar tooling). The directory is pre-created and
owned by the agent user, and `PATH` is set so that it (and `$GOPATH/bin`)
take precedence over system directories.

Custom images should follow the same convention. Example:

```dockerfile
RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.local/bin && chown -R agent:agent /home/agent
USER agent
ENV GOPATH="/home/agent/go"
ENV PATH="/home/agent/.local/bin:${GOPATH}/bin:${PATH}"
```

With this in place, a workspace `setupCommand` such as
`["sh","-c","pip install --user some-tool"]` will land the binary on `PATH`
for the agent process that follows.

### 7. Pre-agent setup command

When a workspace defines `Workspace.spec.setupCommand`, the controller
JSON-encodes the exec-form array and sets it on `KELOS_SETUP_COMMAND`. The
entrypoint must decode it and exec the command from the working directory
before invoking the agent. The semantics match Kubernetes
`lifecycle.postStart.exec.command`: the array is passed directly to exec
with no shell interpretation, and a non-zero exit aborts the task before
the agent runs (the entrypoint must propagate the exit code).

The reference entrypoints emit `---KELOS_SETUP_COMMAND_START---`,
`---KELOS_SETUP_COMMAND_DONE---`, and `---KELOS_SETUP_COMMAND_FAILED---`
banners on stderr so users can distinguish setup failures from agent
failures when tailing pod logs.

## Output Capture

The entrypoint should pipe the agent's stdout into `/kelos/kelos-capture`,
which forwards the stream unchanged to its own stdout and emits
deterministic outputs (branch name, PR URLs, token usage) at EOF. The
controller reads Pod logs and extracts lines between the following markers:

```
---KELOS_OUTPUTS_START---
branch: <branch-name>
pr: https://github.com/org/repo/pull/123
commit: <sha>
base-branch: <name>
input-tokens: <number>
output-tokens: <number>
cost-usd: <number>
---KELOS_OUTPUTS_END---
```

Output lines use `key: value` format (separated by `: `). The controller stores
these lines in `TaskStatus.Outputs` and also parses them into a
`TaskStatus.Results` map for structured access. Lines without `: ` are kept
in Outputs but skipped when building Results.

The `commit` and `base-branch` keys are captured by `kelos-capture`.
Token usage and cost keys (`input-tokens`, `output-tokens`, `cost-usd`) are
also extracted by `kelos-capture`, which consumes the agent's JSON output
from stdin and uses `KELOS_AGENT_TYPE` to parse agent-specific formats. All
agents emit `input-tokens` and `output-tokens`; `claude-code` additionally
emits `cost-usd`.

Results can be referenced in dependency prompt templates:

```
{{ index .Deps "task-a" "Results" "branch" }}
```

The `/kelos/kelos-capture` binary is included in all reference images and handles
this automatically. Custom images should either:

1. Include the binary and pipe the agent's stdout through it, or
2. Emit the markers directly from their entrypoint.

The entrypoint must **not** use `exec` to run the agent, so that
`kelos-capture` can emit markers after the agent exits. Use the following
pattern:

```bash
<agent> "${ARGS[@]}" | /kelos/kelos-capture
AGENT_EXIT_CODE=${PIPESTATUS[0]}

exit $AGENT_EXIT_CODE
```

`kelos-capture` forwards every line of the agent stream to its own stdout
unchanged, so pod logs remain identical to the agent's raw output. It
accumulates token usage in memory as the stream is read, so there is no
on-disk copy of the stream and the pod does not need ephemeral storage for
agent output. `PIPESTATUS[0]` captures the agent's exit code correctly with
`set -uo pipefail`.

Also use `set -uo pipefail` (without `-e`) so the capture step runs even if
the agent exits non-zero.

## Reference implementations

- `claude-code/kelos_entrypoint.sh` — wraps the `claude` CLI (Anthropic Claude Code).
- `codex/kelos_entrypoint.sh` — wraps the `codex` CLI (OpenAI Codex).
- `gemini/kelos_entrypoint.sh` — wraps the `gemini` CLI (Google Gemini).
- `opencode/kelos_entrypoint.sh` — wraps the `opencode` CLI (OpenCode).
- `cursor/kelos_entrypoint.sh` — wraps the `agent` CLI (Cursor).
