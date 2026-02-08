#!/bin/bash
# axon_entrypoint.sh — reference implementation of the Axon agent image
# interface for Claude Code.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - AXON_MODEL env var: model name (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -euo pipefail

PROMPT="${1:?Prompt argument is required}"

ARGS=(
    "--dangerously-skip-permissions"
    "--output-format" "stream-json"
    "--verbose"
    "-p" "$PROMPT"
)

if [ -n "${AXON_MODEL:-}" ]; then
    ARGS+=("--model" "$AXON_MODEL")
fi

exec claude "${ARGS[@]}"
