#!/bin/bash
# kelos_entrypoint.sh — Kelos agent image interface implementation for
# OpenAI Codex CLI.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - KELOS_MODEL env var: model name (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

PROMPT="${1:?Prompt argument is required}"

# Write auth.json from env var for OAuth/ChatGPT credential flow.
# Strip control characters so serde_json's strict parser accepts
# the file (the env var value may contain raw newlines).
if [ -n "${CODEX_AUTH_JSON:-}" ]; then
  mkdir -p ~/.codex
  printf '%s' "$CODEX_AUTH_JSON" | tr -d '\n\r' >~/.codex/auth.json
  printf 'cli_auth_credentials_store = "file"\n' >>~/.codex/config.toml
fi

ARGS=(
  "exec"
  "--dangerously-bypass-approvals-and-sandbox"
  "--json"
  "$PROMPT"
)

if [ -n "${KELOS_MODEL:-}" ]; then
  ARGS+=("--model" "$KELOS_MODEL")
fi

if [ -n "${KELOS_EFFORT:-}" ]; then
  SAFE_EFFORT=$(printf '%s' "$KELOS_EFFORT" | tr -d '"\\\n\r')
  ARGS+=("--config" "model_reasoning_effort=\"$SAFE_EFFORT\"")
fi

# Write user-level instructions (global scope read by Codex CLI)
if [ -n "${KELOS_AGENTS_MD:-}" ]; then
  mkdir -p ~/.codex
  printf '%s' "$KELOS_AGENTS_MD" >~/.codex/AGENTS.md
fi

# Install each plugin as a Codex skill directory under ~/.codex/skills
installed_skill_targets=""
if [ -n "${KELOS_PLUGIN_DIR:-}" ] && [ -d "${KELOS_PLUGIN_DIR}" ]; then
  for plugindir in "${KELOS_PLUGIN_DIR}"/*/; do
    [ -d "$plugindir" ] || continue
    # Copy skills into ~/.codex/skills/<plugin>:<skill>/
    if [ -d "${plugindir}skills" ]; then
      for skilldir in "${plugindir}skills"/*/; do
        [ -d "$skilldir" ] || continue
        skillname=$(basename "$skilldir")
        pluginname=$(basename "$plugindir")
        targetdir="$HOME/.codex/skills/${pluginname}:${skillname}"
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
  done
fi

# Write MCP server configuration to project-scoped config.
# KELOS_MCP_SERVERS contains JSON in .mcp.json format; convert to
# Codex TOML via a small Node.js helper that is available in the image.
if [ -n "${KELOS_MCP_SERVERS:-}" ]; then
  mkdir -p ~/.codex
  node -e '
const cfg = JSON.parse(process.env.KELOS_MCP_SERVERS);
const servers = cfg.mcpServers || {};
let toml = "";
for (const [name, s] of Object.entries(servers)) {
  toml += `[mcp_servers.${JSON.stringify(name)}]\n`;
  if (s.command) toml += `command = ${JSON.stringify(s.command)}\n`;
  if (s.args && s.args.length) toml += `args = ${JSON.stringify(s.args)}\n`;
  if (s.url) toml += `url = ${JSON.stringify(s.url)}\n`;
  if (s.headers) {
    const h = Object.entries(s.headers).map(([k,v]) => `${JSON.stringify(k)} = ${JSON.stringify(v)}`).join(", ");
    toml += `http_headers = { ${h} }\n`;
  }
  if (s.env) {
    const e = Object.entries(s.env).map(([k,v]) => `${JSON.stringify(k)} = ${JSON.stringify(v)}`).join(", ");
    toml += `env = { ${e} }\n`;
  }
  toml += "\n";
}
process.stdout.write(toml);
' >>~/.codex/config.toml
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

codex "${ARGS[@]}" | /kelos/kelos-capture
AGENT_EXIT_CODE=${PIPESTATUS[0]}

exit $AGENT_EXIT_CODE
