#!/bin/bash
# axon_entrypoint.sh — Axon agent image interface implementation for
# OpenCode CLI.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - AXON_MODEL env var: model name (optional, provider/model format)
#   - OPENCODE_API_KEY env var: API key forwarded to the provider
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

opencode "${ARGS[@]}"
AGENT_EXIT_CODE=$?

/axon/capture-outputs.sh

exit $AGENT_EXIT_CODE
