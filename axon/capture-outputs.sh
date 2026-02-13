#!/bin/bash
# Captures deterministic outputs (branch, PRs) from the workspace after
# the agent finishes. Emits structured markers to stdout for the controller
# to parse from Pod logs.

OUTPUTS=""

# Capture current branch
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  BRANCH=$(git branch --show-current 2>/dev/null)
  if [ -n "$BRANCH" ]; then
    OUTPUTS="branch: $BRANCH"
    # Query PRs for this branch (requires GH_TOKEN / GITHUB_TOKEN)
    if command -v gh >/dev/null 2>&1; then
      PR_URLS=$(timeout 10 gh pr list --head "$BRANCH" --json url --jq '.[].url' 2>/dev/null)
      for url in $PR_URLS; do
        OUTPUTS="$OUTPUTS"$'\n'"$url"
      done
    fi
  fi
fi

if [ -n "$OUTPUTS" ]; then
  echo "---AXON_OUTPUTS_START---"
  echo "$OUTPUTS"
  echo "---AXON_OUTPUTS_END---"
fi
