# Agent Image Interface

This document describes the interface that custom agent images must implement
to be compatible with Axon.

## Overview

Axon runs agent tasks as Kubernetes Jobs. Each agent container is invoked using
a standard interface so that any compatible image can be used as a drop-in
replacement for the default `claude-code` image.

## Requirements

### 1. Entrypoint

The image must provide an executable at `/axon_entrypoint.sh`. Axon sets
`Command: ["/axon_entrypoint.sh"]` on the container, overriding any
`ENTRYPOINT` in the Dockerfile.

### 2. Prompt argument

The task prompt is passed as the first positional argument (`$1`). Axon sets
`Args: ["<prompt>"]` on the container.

### 3. Environment variables

Axon sets the following reserved environment variables on agent containers:

| Variable | Description | Always set? |
|---|---|---|
| `AXON_MODEL` | The model name to use | Only when `model` is specified in the Task |
| `AXON_PLUGINS` | Comma-separated list of plugin directory mount paths | Only when `plugins` is specified in the Task |
| `ANTHROPIC_API_KEY` | API key for Anthropic (`claude-code` agent, api-key credential type) | When credential type is `api-key` and agent type is `claude-code` |
| `CODEX_API_KEY` | API key for OpenAI Codex (`codex` agent, api-key or oauth credential type) | When agent type is `codex` |
| `GEMINI_API_KEY` | API key for Google Gemini (`gemini` agent, api-key or oauth credential type) | When agent type is `gemini` |
| `CLAUDE_CODE_OAUTH_TOKEN` | OAuth token (`claude-code` agent, oauth credential type) | When credential type is `oauth` and agent type is `claude-code` |
| `GITHUB_TOKEN` | GitHub token for workspace access | When workspace has a `secretRef` |
| `GH_TOKEN` | GitHub token for `gh` CLI (github.com) | When workspace has a `secretRef` and repo is on github.com |
| `GH_ENTERPRISE_TOKEN` | GitHub token for `gh` CLI (GitHub Enterprise) | When workspace has a `secretRef` and repo is on a GitHub Enterprise host |
| `GH_HOST` | Hostname for GitHub Enterprise | When repo is on a GitHub Enterprise host |

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

When a workspace is configured, Axon mounts the cloned repository at
`/workspace/repo` and sets `WorkingDir` on the container accordingly. The
entrypoint script does not need to handle directory changes.

## Output Capture

After the agent exits, the entrypoint should run `/axon/capture-outputs.sh` to
emit deterministic outputs (branch name, PR URLs) to stdout. The controller
reads Pod logs and extracts lines between the following markers:

```
---AXON_OUTPUTS_START---
branch: <branch-name>
https://github.com/org/repo/pull/123
---AXON_OUTPUTS_END---
```

The shared script `/axon/capture-outputs.sh` is included in all reference images
and handles this automatically. Custom images should either:

1. Include the script and call it after the agent exits, or
2. Emit the markers directly from their entrypoint.

The entrypoint must **not** use `exec` to run the agent, so that the capture
step runs after the agent exits. Use the following pattern:

```bash
<agent> "${ARGS[@]}"
AGENT_EXIT_CODE=$?

/axon/capture-outputs.sh

exit $AGENT_EXIT_CODE
```

Also use `set -uo pipefail` (without `-e`) so the capture script runs even if
the agent exits non-zero.

Captured outputs are stored in `TaskStatus.Outputs` and displayed by the CLI.

## Reference implementations

- `claude-code/axon_entrypoint.sh` — wraps the `claude` CLI (Anthropic Claude Code).
- `codex/axon_entrypoint.sh` — wraps the `codex` CLI (OpenAI Codex).
- `gemini/axon_entrypoint.sh` — wraps the `gemini` CLI (Google Gemini).
