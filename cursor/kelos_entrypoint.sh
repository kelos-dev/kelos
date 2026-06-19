#!/bin/bash
# kelos_entrypoint.sh — Kelos agent image interface implementation for
# Cursor CLI.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - CURSOR_API_KEY env var: API key for authentication
#   - KELOS_MODEL env var: model name (optional)
#   - KELOS_AGENTS_MD env var: user-level instructions (optional)
#   - KELOS_PLUGIN_DIR env var: plugin directory with skills/agents (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

PROMPT="${1:?Prompt argument is required}"

ARGS=(
  "-p"
  "--force"
  "--trust"
  "--sandbox" "disabled"
  "--output-format" "stream-json"
  "$PROMPT"
)

if [ -n "${KELOS_MODEL:-}" ]; then
  ARGS=("--model" "$KELOS_MODEL" "${ARGS[@]}")
fi

# Write user-level instructions (global scope read by Cursor CLI)
if [ -n "${KELOS_AGENTS_MD:-}" ] || [ -n "${KELOS_EFFORT:-}" ]; then
  mkdir -p ~/.cursor
  {
    if [ -n "${KELOS_AGENTS_MD:-}" ]; then
      printf '%s\n' "$KELOS_AGENTS_MD"
    fi
    if [ -n "${KELOS_EFFORT:-}" ]; then
      printf '\n# Kelos Effort\nUse %s reasoning effort for this task.\n' "$KELOS_EFFORT"
    fi
  } >~/.cursor/AGENTS.md
fi

# Install each plugin's skills and agents into Cursor's config directories.
# Skills are placed into .cursor/skills/ relative to the working directory
# so the CLI discovers them at runtime. Agents are installed as Cursor
# rules under .cursor/rules/ in the working directory.
installed_skill_targets=""
if [ -n "${KELOS_PLUGIN_DIR:-}" ] && [ -d "${KELOS_PLUGIN_DIR}" ]; then
  for plugindir in "${KELOS_PLUGIN_DIR}"/*/; do
    [ -d "$plugindir" ] || continue
    pluginname=$(basename "$plugindir")
    # Copy skills into .cursor/skills/<plugin>:<skill>/
    if [ -d "${plugindir}skills" ]; then
      for skilldir in "${plugindir}skills"/*/; do
        [ -d "$skilldir" ] || continue
        skillname=$(basename "$skilldir")
        targetdir=".cursor/skills/${pluginname}:${skillname}"
        if [ -f "${skilldir}SKILL.md" ]; then
          sourceid="${pluginname}/${skillname}"
          existing_source=""
          while IFS=$'\t' read -r seen_target seen_source; do
            [ -n "$seen_target" ] || continue
            if [ "$seen_target" = "$targetdir" ]; then
              existing_source="$seen_source"
              break
            fi
          done <<<"$installed_skill_targets"
          if [ -n "$existing_source" ] && [ "$existing_source" != "$sourceid" ]; then
            printf 'Error: Skill target conflict: %s and %s both install to %s\n' "$existing_source" "$sourceid" "$targetdir" >&2
            exit 1
          fi
          installed_skill_targets="${installed_skill_targets}${targetdir}"$'\t'"${sourceid}"$'\n'
          rm -rf "$targetdir"
          mkdir -p "$targetdir"
          cp -R "${skilldir}." "$targetdir/"
        fi
      done
    fi
    # Copy agents into .cursor/rules/ as .mdc rule files
    if [ -d "${plugindir}agents" ]; then
      mkdir -p .cursor/rules
      for agentfile in "${plugindir}agents"/*.md; do
        [ -f "$agentfile" ] || continue
        agentname=$(basename "$agentfile" .md)
        cp "$agentfile" ".cursor/rules/${pluginname}-${agentname}.mdc"
      done
    fi
  done
fi

# Write MCP server configuration to user-scoped ~/.cursor/mcp.json.
# The KELOS_MCP_SERVERS JSON format matches Cursor's native format directly.
if [ -n "${KELOS_MCP_SERVERS:-}" ]; then
  mkdir -p ~/.cursor
  node -e '
const fs = require("fs");
const cfgPath = require("os").homedir() + "/.cursor/mcp.json";
let existing = {};
try { existing = JSON.parse(fs.readFileSync(cfgPath, "utf8")); } catch {}
const mcp = JSON.parse(process.env.KELOS_MCP_SERVERS);
existing.mcpServers = Object.assign(existing.mcpServers || {}, mcp.mcpServers || {});
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

agent "${ARGS[@]}" | /kelos/kelos-capture
AGENT_EXIT_CODE=${PIPESTATUS[0]}

exit $AGENT_EXIT_CODE
