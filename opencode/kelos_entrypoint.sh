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

PROMPT="${1:?Prompt argument is required}"

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

ARGS=(
  "run"
  "--format" "json"
  "$PROMPT"
)

if [ -n "${KELOS_MODEL:-}" ]; then
  ARGS+=("--model" "$KELOS_MODEL")
fi

# Write user-level instructions (global scope read by OpenCode CLI)
if [ -n "${KELOS_AGENTS_MD:-}" ]; then
  mkdir -p ~/.config/opencode
  printf '%s' "$KELOS_AGENTS_MD" >~/.config/opencode/AGENTS.md
fi

# Install each plugin's skills and agents into OpenCode's global config
if [ -n "${KELOS_PLUGIN_DIR:-}" ] && [ -d "${KELOS_PLUGIN_DIR}" ]; then
  for plugindir in "${KELOS_PLUGIN_DIR}"/*/; do
    [ -d "$plugindir" ] || continue
    pluginname=$(basename "$plugindir")
    # Copy skills into ~/.config/opencode/skills/<plugin>-<skill>/SKILL.md
    if [ -d "${plugindir}skills" ]; then
      for skilldir in "${plugindir}skills"/*/; do
        [ -d "$skilldir" ] || continue
        skillname=$(basename "$skilldir")
        targetdir="$HOME/.config/opencode/skills/${pluginname}-${skillname}"
        mkdir -p "$targetdir"
        if [ -f "${skilldir}SKILL.md" ]; then
          cp "${skilldir}SKILL.md" "$targetdir/SKILL.md"
        fi
      done
    fi
    # Copy agents into ~/.config/opencode/agents/
    if [ -d "${plugindir}agents" ]; then
      mkdir -p "$HOME/.config/opencode/agents"
      for agentfile in "${plugindir}agents"/*.md; do
        [ -f "$agentfile" ] || continue
        cp "$agentfile" "$HOME/.config/opencode/agents/"
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

opencode "${ARGS[@]}" | tee /tmp/agent-output.jsonl
AGENT_EXIT_CODE=${PIPESTATUS[0]}

/kelos/kelos-capture

exit $AGENT_EXIT_CODE
