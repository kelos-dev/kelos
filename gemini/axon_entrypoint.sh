#!/bin/bash
# axon_entrypoint.sh — Axon agent image interface implementation for
# Google Gemini CLI.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - AXON_MODEL env var: model name (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

PROMPT="${1:?Prompt argument is required}"

ARGS=(
  "--yolo"
  "--output-format" "stream-json"
  "-p" "$PROMPT"
)

if [ -n "${AXON_MODEL:-}" ]; then
  ARGS+=("--model" "$AXON_MODEL")
fi

# Write user-level instructions (global scope read by Gemini CLI)
if [ -n "${AXON_AGENTS_MD:-}" ]; then
  mkdir -p ~/.gemini
  printf '%s' "$AXON_AGENTS_MD" >~/.gemini/GEMINI.md
fi

# Install each plugin as a Gemini extension with skills and agents
if [ -n "${AXON_PLUGIN_DIR:-}" ] && [ -d "${AXON_PLUGIN_DIR}" ]; then
  for plugindir in "${AXON_PLUGIN_DIR}"/*/; do
    [ -d "$plugindir" ] || continue
    pluginname=$(basename "$plugindir")
    # Sanitize plugin name for safe JSON interpolation
    safename=$(printf '%s' "$pluginname" | tr -d '"\\\n\r')
    extdir="$HOME/.gemini/extensions/${pluginname}"
    mkdir -p "$extdir"
    printf '{"name":"%s"}' "$safename" >"$extdir/gemini-extension.json"
    # Copy skills directory
    if [ -d "${plugindir}skills" ]; then
      cp -r "${plugindir}skills" "$extdir/skills"
    fi
    # Copy agents directory
    if [ -d "${plugindir}agents" ]; then
      cp -r "${plugindir}agents" "$extdir/agents"
    fi
  done
fi

gemini "${ARGS[@]}" | tee /tmp/agent-output.jsonl
AGENT_EXIT_CODE=${PIPESTATUS[0]}

/axon/axon-capture

exit $AGENT_EXIT_CODE
