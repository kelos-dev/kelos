#!/bin/bash
# kelos_entrypoint.sh — Kelos agent image interface implementation for
# Google Antigravity CLI (agy).
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - KELOS_MODEL env var: model name (optional)
#   - KELOS_AGENTS_MD env var: user-level instructions (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured
#
# Credentials: the controller does not inject built-in credential env vars
# for this agent type (Task.spec.credentials.type must be "none"). Operators
# who need authenticated runs supply credentials via Task.spec.podOverrides.env
# using whatever variable the agy binary expects.

set -uo pipefail

PROMPT="${1:?Prompt argument is required}"

ARGS=(
  "--print"
  "--output-format" "stream-json"
)

if [ -n "${KELOS_MODEL:-}" ]; then
  ARGS+=("--model" "$KELOS_MODEL")
fi

ARGS+=("$PROMPT")

# Write user-level instructions (global scope read by Antigravity CLI).
if [ -n "${KELOS_AGENTS_MD:-}" ]; then
  mkdir -p ~/.config/agy
  printf '%s' "$KELOS_AGENTS_MD" >~/.config/agy/AGENTS.md
fi

# Run pre-agent setup command if configured. KELOS_SETUP_COMMAND is the
# JSON-encoded exec-form array from Workspace.spec.setupCommand. A non-zero
# exit aborts the task before the agent starts.
if [ -n "${KELOS_SETUP_COMMAND:-}" ]; then
  printf '\n---KELOS_SETUP_COMMAND_START---\n' >&2
  node -e '
const { spawnSync } = require("child_process");
const cmd = JSON.parse(process.env.KELOS_SETUP_COMMAND);
if (!Array.isArray(cmd) || cmd.length === 0 || cmd.some(a => typeof a !== "string")) {
  console.error("KELOS_SETUP_COMMAND must be a non-empty JSON array of strings");
  process.exit(2);
}
const r = spawnSync(cmd[0], cmd.slice(1), { stdio: "inherit" });
if (r.error) { console.error(r.error.message); process.exit(127); }
process.exit(r.status ?? 1);
'
  SETUP_EXIT_CODE=$?
  if [ "$SETUP_EXIT_CODE" -ne 0 ]; then
    printf '\n---KELOS_SETUP_COMMAND_FAILED--- exit=%s\n' "$SETUP_EXIT_CODE" >&2
    exit "$SETUP_EXIT_CODE"
  fi
  printf '\n---KELOS_SETUP_COMMAND_DONE---\n' >&2
fi

agy "${ARGS[@]}" | tee /tmp/agent-output.jsonl
AGENT_EXIT_CODE=${PIPESTATUS[0]}

/kelos/kelos-capture

exit $AGENT_EXIT_CODE
