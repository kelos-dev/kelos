#!/bin/bash
# kelos_entrypoint.sh — Kelos agent image interface implementation for
# OpenCode CLI.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - KELOS_MODEL env var: model name (optional, provider/model format)
#   - OPENCODE_API_KEY env var: API key forwarded to the provider
#   - KELOS_AGENTS_MD env var: user-level instructions (optional)
#   - KELOS_PLUGIN_DIR env var: plugin directory with skills/agents (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

opencode_config_dir="${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}"
export OPENCODE_CONFIG_DIR="$opencode_config_dir"
mkdir -p "$opencode_config_dir"

# Map OPENCODE_API_KEY to the correct provider environment variable
# based on the provider prefix in KELOS_MODEL.
if [ -n "${OPENCODE_API_KEY:-}" ] && [ -n "${KELOS_MODEL:-}" ]; then
  PROVIDER="${KELOS_MODEL%%/*}"
  case "$PROVIDER" in
    anthropic) export ANTHROPIC_API_KEY="$OPENCODE_API_KEY" ;;
    openai) export OPENAI_API_KEY="$OPENCODE_API_KEY" ;;
    google) export GEMINI_API_KEY="$OPENCODE_API_KEY" ;;
    groq) export GROQ_API_KEY="$OPENCODE_API_KEY" ;;
    xai) export XAI_API_KEY="$OPENCODE_API_KEY" ;;
    openrouter) export OPENROUTER_API_KEY="$OPENCODE_API_KEY" ;;
    zai | zai-coding-plan) export ZHIPU_API_KEY="$OPENCODE_API_KEY" ;;
    opencode | zen)
      # Zen/OpenCode models: no provider-specific key mapping needed.
      ;;
    *)
      echo "Warning: Unrecognized provider prefix '$PROVIDER' in KELOS_MODEL, defaulting to Anthropic" >&2
      export ANTHROPIC_API_KEY="$OPENCODE_API_KEY"
      ;;
  esac
elif [ -n "${OPENCODE_API_KEY:-}" ]; then
  # Default to Anthropic when no model is specified.
  export ANTHROPIC_API_KEY="$OPENCODE_API_KEY"
fi

if [ -n "${KELOS_EFFORT:-}" ]; then
  node -e '
const fs = require("fs");
const path = require("path");
const cfgPath = path.join(process.env.OPENCODE_CONFIG_DIR, "opencode.json");
let existing = {};
try { existing = JSON.parse(fs.readFileSync(cfgPath, "utf8")); } catch {}
const model = process.env.KELOS_MODEL || existing.model;
const effort = process.env.KELOS_EFFORT;
const provider = model ? model.split("/")[0] : "";
const normalized = effort.toLowerCase();
function variantFor(provider, effort) {
  if (provider === "anthropic") {
    return effort === "max" || effort === "xhigh" ? "max" : "high";
  }
  if (provider === "google") {
    return effort === "minimal" || effort === "low" ? "low" : "high";
  }
  return effort;
}
if (model) existing.model = model;
existing.agent = existing.agent || {};
existing.agent.build = Object.assign({}, existing.agent.build || {}, {
  mode: "primary",
  variant: variantFor(provider, normalized),
  options: Object.assign({}, (existing.agent.build && existing.agent.build.options) || {}, {
    reasoningEffort: effort,
  }),
});
if (model) existing.agent.build.model = model;
fs.writeFileSync(cfgPath, JSON.stringify(existing, null, 2));
'
fi

# Write user-level instructions (global scope read by OpenCode CLI)
if [ -n "${KELOS_AGENTS_MD:-}" ]; then
  printf '%s' "$KELOS_AGENTS_MD" >"$opencode_config_dir/AGENTS.md"
fi

# Install each plugin's skills and agents into OpenCode's global config
installed_skill_targets=""
if [ -n "${KELOS_PLUGIN_DIR:-}" ] && [ -d "${KELOS_PLUGIN_DIR}" ]; then
  for plugindir in "${KELOS_PLUGIN_DIR}"/*/; do
    [ -d "$plugindir" ] || continue
    pluginname=$(basename "$plugindir")
    # Copy skills into the configured OpenCode skills directory.
    if [ -d "${plugindir}skills" ]; then
      for skilldir in "${plugindir}skills"/*/; do
        [ -d "$skilldir" ] || continue
        skillname=$(basename "$skilldir")
        targetdir="$opencode_config_dir/skills/${pluginname}:${skillname}"
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
    # Copy agents into the configured OpenCode agents directory.
    if [ -d "${plugindir}agents" ]; then
      mkdir -p "$opencode_config_dir/agents"
      for agentfile in "${plugindir}agents"/*.md; do
        [ -f "$agentfile" ] || continue
        cp "$agentfile" "$opencode_config_dir/agents/"
      done
    fi
  done
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
  "run"
  "--format" "json"
  "--auto"
  "$PROMPT"
)
if [ -z "${KELOS_EFFORT:-}" ] && [ -n "${KELOS_MODEL:-}" ]; then
  ARGS+=("--model" "$KELOS_MODEL")
fi

opencode "${ARGS[@]}" | /kelos/kelos-capture
AGENT_EXIT_CODE=${PIPESTATUS[0]}

exit $AGENT_EXIT_CODE
