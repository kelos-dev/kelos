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
| `ANTHROPIC_API_KEY` | API key for Anthropic (api-key credential type) | When credential type is `api-key` |
| `CLAUDE_CODE_OAUTH_TOKEN` | OAuth token (oauth credential type) | When credential type is `oauth` |
| `GITHUB_TOKEN` | GitHub token for workspace access | When workspace has a `secretRef` |
| `GH_TOKEN` | GitHub token (alias for GitHub CLI) | When workspace has a `secretRef` |

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

## Reference implementation

See `claude-code/axon_entrypoint.sh` for a reference implementation that wraps
the `claude` CLI.
