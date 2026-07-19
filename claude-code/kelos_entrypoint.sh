#!/bin/bash
# kelos_entrypoint.sh — reference implementation of the Kelos agent image
# interface for Claude Code.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - KELOS_SESSION_SETUP_ONLY=1: prepare configuration and exit without a prompt
#   - KELOS_MODEL env var: model name (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

claude_config_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"

# Write user-level instructions (additive, no conflict with repo)
if [ -n "${KELOS_AGENTS_MD:-}" ]; then
  mkdir -p "$claude_config_dir"
  printf '%s' "$KELOS_AGENTS_MD" >"$claude_config_dir/CLAUDE.md"
fi

# Write MCP server configuration to the user-scoped Claude configuration.
# This avoids overwriting the repository's own .mcp.json while
# still making the servers available to Claude Code.
if [ -n "${KELOS_MCP_SERVERS:-}" ]; then
  node -e '
const fs = require("fs");
const os = require("os");
const path = require("path");
const cfgPath = process.env.CLAUDE_CONFIG_DIR
  ? path.join(process.env.CLAUDE_CONFIG_DIR, ".claude.json")
  : path.join(os.homedir(), ".claude.json");
let existing = {};
try { existing = JSON.parse(fs.readFileSync(cfgPath, "utf8")); } catch {}
const mcp = JSON.parse(process.env.KELOS_MCP_SERVERS);
existing.mcpServers = Object.assign(existing.mcpServers || {}, mcp.mcpServers || {});
fs.mkdirSync(path.dirname(cfgPath), { recursive: true });
fs.writeFileSync(cfgPath, JSON.stringify(existing, null, 2));
'
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

if [ "${KELOS_SESSION_SETUP_ONLY:-}" = "1" ]; then
  exit 0
fi

PROMPT="${1:?Prompt argument is required}"
ARGS=(
  "--dangerously-skip-permissions"
  "--output-format" "stream-json"
  "--verbose"
  "-p" "$PROMPT"
)

if [ -n "${KELOS_MODEL:-}" ]; then
  ARGS+=("--model" "$KELOS_MODEL")
fi

if [ -n "${KELOS_EFFORT:-}" ]; then
  ARGS+=("--effort" "$KELOS_EFFORT")
fi

# Pass each plugin directory via --plugin-dir
if [ -n "${KELOS_PLUGIN_DIR:-}" ] && [ -d "${KELOS_PLUGIN_DIR}" ]; then
  for dir in "${KELOS_PLUGIN_DIR}"/*/; do
    [ -d "$dir" ] && ARGS+=("--plugin-dir" "$dir")
  done
fi

claude "${ARGS[@]}" | /kelos/kelos-capture
PIPE_EXIT_CODES=("${PIPESTATUS[@]}")
AGENT_EXIT_CODE=${PIPE_EXIT_CODES[0]}
CAPTURE_EXIT_CODE=${PIPE_EXIT_CODES[1]}

if [ "$AGENT_EXIT_CODE" -ne 0 ]; then
  exit "$AGENT_EXIT_CODE"
fi

exit "$CAPTURE_EXIT_CODE"
