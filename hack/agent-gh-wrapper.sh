#!/bin/sh
# gh wrapper for Kelos agent containers.
#
# Installed at /usr/local/bin/gh so it shadows the distro-packaged
# /usr/bin/gh. On each invocation, if KELOS_GITHUB_TOKEN_FILE is set
# and readable, the wrapper exports the file contents into the
# variable gh actually reads (GH_TOKEN for github.com, GH_ENTERPRISE_TOKEN
# for GitHub Enterprise hosts), then execs the real gh. This lets the
# controller refresh the token in-place via the mounted Secret without
# the long-running agent process picking up stale env vars.

set -u

if [ -n "${KELOS_GITHUB_TOKEN_FILE:-}" ] && [ -r "${KELOS_GITHUB_TOKEN_FILE}" ]; then
  __kelos_token=$(cat "${KELOS_GITHUB_TOKEN_FILE}")
  if [ -n "${GH_HOST:-}" ] && [ "${GH_HOST}" != "github.com" ]; then
    export GH_ENTERPRISE_TOKEN="${__kelos_token}"
  else
    export GH_TOKEN="${__kelos_token}"
  fi
  unset __kelos_token
fi

exec /usr/bin/gh "$@"
