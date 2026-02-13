#!/bin/bash
# axon_entrypoint.sh — reference implementation of the Axon agent image
# interface for Claude Code.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - AXON_MODEL env var: model name (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

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

# Write user-level instructions (additive, no conflict with repo)
if [ -n "${AXON_AGENTS_MD:-}" ]; then
  mkdir -p ~/.claude
  printf '%s' "$AXON_AGENTS_MD" >~/.claude/CLAUDE.md
fi

# Pass each plugin directory via --plugin-dir
if [ -n "${AXON_PLUGIN_DIR:-}" ] && [ -d "${AXON_PLUGIN_DIR}" ]; then
  for dir in "${AXON_PLUGIN_DIR}"/*/; do
    [ -d "$dir" ] && ARGS+=("--plugin-dir" "$dir")
  done
fi

claude "${ARGS[@]}"
AGENT_EXIT_CODE=$?

/axon/capture-outputs.sh

exit $AGENT_EXIT_CODE
