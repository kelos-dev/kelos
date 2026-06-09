#!/bin/sh

apply_kanon_env_config() {
  agent="${1:?Agent argument is required}"

  if [ -z "${KELOS_KANON_CONFIG:-}" ]; then
    return 1
  fi

  if ! command -v kanon >/dev/null 2>&1; then
    echo "Warning: KELOS_KANON_CONFIG is set but kanon is unavailable; using legacy agent config setup" >&2
    return 1
  fi

  kanon_home="$(mktemp -d "${TMPDIR:-/tmp}/kelos-kanon.XXXXXX")"
  mkdir -p "$kanon_home/instructions"
  printf '%s' "$KELOS_KANON_CONFIG" >"$kanon_home/kanon.yaml"

  if [ -n "${KELOS_AGENTS_MD:-}" ]; then
    printf '%s' "$KELOS_AGENTS_MD" >"$kanon_home/instructions/kelos.md"
  fi

  if ! kanon apply --home "$kanon_home" --agent "$agent" --yes >/dev/null; then
    echo "Failed to apply Kanon agent config" >&2
    exit 1
  fi

  return 0
}
