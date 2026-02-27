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
| `ANTHROPIC_API_KEY` | API key for Anthropic (`claude-code` agent, api-key credential type) | When credential type is `api-key` and agent type is `claude-code` |
| `CODEX_API_KEY` | API key for OpenAI Codex (`codex` agent, `api-key` credential type) | When credential type is `api-key` and agent type is `codex` |
| `CODEX_AUTH_JSON` | Contents of `~/.codex/auth.json` (`codex` agent, `oauth` credential type) | When credential type is `oauth` and agent type is `codex` |
| `GEMINI_API_KEY` | API key for Google Gemini (`gemini` agent, api-key or oauth credential type) | When agent type is `gemini` |
| `OPENCODE_API_KEY` | API key for OpenCode (`opencode` agent, api-key or oauth credential type) | When agent type is `opencode` |
| `CLAUDE_CODE_OAUTH_TOKEN` | OAuth token (`claude-code` agent, oauth credential type) | When credential type is `oauth` and agent type is `claude-code` |
| `GITHUB_TOKEN` | GitHub token for workspace access | When workspace has a `secretRef` |
| `GH_TOKEN` | GitHub token for `gh` CLI (github.com) | When workspace has a `secretRef` and repo is on github.com |
| `GH_ENTERPRISE_TOKEN` | GitHub token for `gh` CLI (GitHub Enterprise) | When workspace has a `secretRef` and repo is on a GitHub Enterprise host |
| `GH_HOST` | Hostname for GitHub Enterprise | When repo is on a GitHub Enterprise host |
| `KELOS_AGENT_TYPE` | The agent type (`claude-code`, `codex`, `gemini`, `opencode`) | Always |
| `KELOS_BASE_BRANCH` | The base branch (workspace `ref`) for the task | When workspace has a non-empty `ref` |
| `KELOS_AGENTS_MD` | User-level instructions from AgentConfig | When `agentConfigRef` is set and `agentsMD` is non-empty |
| `KELOS_PLUGIN_DIR` | Path to plugin directory containing skills and agents | When `agentConfigRef` is set and `plugins` is non-empty |

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

## Output Capture

After the agent exits, the entrypoint should run `/kelos/kelos-capture` to
emit deterministic outputs (branch name, PR URLs) to stdout. The controller
reads Pod logs and extracts lines between the following markers:

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
also extracted by `kelos-capture`, which reads the agent's JSON output from
`/tmp/agent-output.jsonl` and uses `KELOS_AGENT_TYPE` to parse agent-specific
formats. All agents emit `input-tokens` and `output-tokens`; `claude-code`
additionally emits `cost-usd`.

Results can be referenced in dependency prompt templates:

```
{{ index .Deps "task-a" "Results" "branch" }}
```

The `/kelos/kelos-capture` binary is included in all reference images and handles
this automatically. Custom images should either:

1. Include the binary and call it after the agent exits, or
2. Emit the markers directly from their entrypoint.

The entrypoint must **not** use `exec` to run the agent, so that the capture
step runs after the agent exits. Use the following pattern:

```bash
<agent> "${ARGS[@]}" | tee /tmp/agent-output.jsonl
AGENT_EXIT_CODE=${PIPESTATUS[0]}

/kelos/kelos-capture

exit $AGENT_EXIT_CODE
```

The `tee` command copies the agent's stdout to `/tmp/agent-output.jsonl` so
that `kelos-capture` can extract token usage or cost information.
`PIPESTATUS[0]` captures the agent's exit code correctly with `set -uo pipefail`.

Also use `set -uo pipefail` (without `-e`) so the capture script runs even if
the agent exits non-zero.

## Reference implementations

- `claude-code/kelos_entrypoint.sh` — wraps the `claude` CLI (Anthropic Claude Code).
- `codex/kelos_entrypoint.sh` — wraps the `codex` CLI (OpenAI Codex).
- `gemini/kelos_entrypoint.sh` — wraps the `gemini` CLI (Google Gemini).
- `opencode/kelos_entrypoint.sh` — wraps the `opencode` CLI (OpenCode).
