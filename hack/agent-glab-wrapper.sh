#!/bin/sh
# glab wrapper for Kelos agent containers.
#
# Installed at /usr/local/bin/glab so it shadows the packaged /usr/bin/glab.
# On each invocation, if KELOS_GITLAB_TOKEN_FILE is set and readable, the
# wrapper exports the file contents as GITLAB_TOKEN, then execs the real glab.
# This lets the controller refresh the token in-place via the mounted Secret
# without the long-running agent process picking up stale env vars.
#
# The first invocation also registers the GITLAB_HOST instance in glab's
# config. glab derives the API protocol and port from that host entry, not
# from GITLAB_HOST, so without it a plain-http or non-standard-port instance
# is called over https and `glab auth status` reports the host as
# unauthenticated.

set -u

if [ -n "${KELOS_GITLAB_TOKEN_FILE:-}" ] && [ -r "${KELOS_GITLAB_TOKEN_FILE}" ]; then
  GITLAB_TOKEN=$(cat "${KELOS_GITLAB_TOKEN_FILE}")
  export GITLAB_TOKEN
fi

__kelos_marker="${GLAB_CONFIG_DIR:-}/.kelos-host-configured"
if [ -n "${GITLAB_HOST:-}" ] && [ -n "${GLAB_CONFIG_DIR:-}" ] && [ -n "${GITLAB_TOKEN:-}" ] && [ ! -f "${__kelos_marker}" ]; then
  case "${GITLAB_HOST}" in
    http://*)
      __kelos_proto=http
      __kelos_host=${GITLAB_HOST#http://}
      ;;
    https://*)
      __kelos_proto=https
      __kelos_host=${GITLAB_HOST#https://}
      ;;
    *)
      __kelos_proto=https
      __kelos_host=${GITLAB_HOST}
      ;;
  esac
  __kelos_host=${__kelos_host%%/*}
  mkdir -p "${GLAB_CONFIG_DIR}"
  if printf '%s' "${GITLAB_TOKEN}" | /usr/bin/glab auth login \
    --hostname "${__kelos_host}" --api-host "${__kelos_host}" \
    --api-protocol "${__kelos_proto}" --git-protocol "${__kelos_proto}" \
    --stdin >/dev/null 2>&1; then
    : >"${__kelos_marker}"
  fi
  unset __kelos_proto __kelos_host
fi
unset __kelos_marker

exec /usr/bin/glab "$@"
