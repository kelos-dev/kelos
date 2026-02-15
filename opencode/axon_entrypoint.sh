#!/bin/bash
# axon_entrypoint.sh — Axon agent image interface implementation for
# OpenCode CLI.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - AXON_MODEL env var: model name (optional, provider/model format)
#   - OPENCODE_API_KEY env var: API key forwarded to the provider
#   - AXON_AGENTS_MD env var: user-level instructions (optional)
#   - AXON_PLUGIN_DIR env var: plugin directory with skills/agents (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

PROMPT="${1:?Prompt argument is required}"

# Map OPENCODE_API_KEY to the correct provider environment variable
# based on the provider prefix in AXON_MODEL.
if [ -n "${OPENCODE_API_KEY:-}" ] && [ -n "${AXON_MODEL:-}" ]; then
  PROVIDER="${AXON_MODEL%%/*}"
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
      echo "Warning: Unrecognized provider prefix '$PROVIDER' in AXON_MODEL, defaulting to Anthropic" >&2
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

if [ -n "${AXON_MODEL:-}" ]; then
  ARGS+=("--model" "$AXON_MODEL")
fi

# Write user-level instructions (global scope read by OpenCode CLI)
if [ -n "${AXON_AGENTS_MD:-}" ]; then
  mkdir -p ~/.config/opencode
  printf '%s' "$AXON_AGENTS_MD" >~/.config/opencode/AGENTS.md
fi

# Install each plugin's skills and agents into OpenCode's global config
if [ -n "${AXON_PLUGIN_DIR:-}" ] && [ -d "${AXON_PLUGIN_DIR}" ]; then
  for plugindir in "${AXON_PLUGIN_DIR}"/*/; do
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

opencode "${ARGS[@]}" | tee /tmp/agent-output.jsonl
AGENT_EXIT_CODE=${PIPESTATUS[0]}

/axon/axon-capture

exit $AGENT_EXIT_CODE
