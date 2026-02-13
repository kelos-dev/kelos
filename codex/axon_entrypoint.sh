#!/bin/bash
# axon_entrypoint.sh — Axon agent image interface implementation for
# OpenAI Codex CLI.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - AXON_MODEL env var: model name (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

PROMPT="${1:?Prompt argument is required}"

ARGS=(
    "exec"
    "--dangerously-bypass-approvals-and-sandbox"
    "--json"
    "$PROMPT"
)

if [ -n "${AXON_MODEL:-}" ]; then
    ARGS+=("--model" "$AXON_MODEL")
fi

# Write user-level instructions (global scope read by Codex CLI)
if [ -n "${AXON_AGENTS_MD:-}" ]; then
    mkdir -p ~/.codex
    printf '%s' "$AXON_AGENTS_MD" > ~/.codex/AGENTS.md
fi

# Install each plugin as a Codex skill directory under ~/.codex/skills
if [ -n "${AXON_PLUGIN_DIR:-}" ] && [ -d "${AXON_PLUGIN_DIR}" ]; then
    for plugindir in "${AXON_PLUGIN_DIR}"/*/; do
        [ -d "$plugindir" ] || continue
        # Copy skills into ~/.codex/skills/<plugin>/<skill>/SKILL.md
        if [ -d "${plugindir}skills" ]; then
            for skilldir in "${plugindir}skills"/*/; do
                [ -d "$skilldir" ] || continue
                skillname=$(basename "$skilldir")
                pluginname=$(basename "$plugindir")
                targetdir="$HOME/.codex/skills/${pluginname}-${skillname}"
                mkdir -p "$targetdir"
                if [ -f "${skilldir}SKILL.md" ]; then
                    cp "${skilldir}SKILL.md" "$targetdir/SKILL.md"
                fi
            done
        fi
    done
    ARGS+=("--enable" "skills")
fi

codex "${ARGS[@]}"
AGENT_EXIT_CODE=$?

/axon/capture-outputs.sh

exit $AGENT_EXIT_CODE
