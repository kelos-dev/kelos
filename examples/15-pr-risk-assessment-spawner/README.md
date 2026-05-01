# Use Case: PR Risk Assessment and Auto-Approval

**Related Issues**: #972 (API Contract Validation)

## Problem

Every PR needs review, but not every PR carries the same risk. Docs-only changes and config tweaks sit in the review queue alongside database migrations and auth changes. Teams need a way to fast-track low-risk PRs while flagging high-risk ones for careful human review.

## Solution

A Kelos TaskSpawner that triggers on every non-bot, non-draft PR. The agent:
1. Reads the PR diff and description
2. Optionally incorporates external review tools (e.g., Greptile, CodeRabbit)
3. Rates risk on a 5-point scale (Very Low to Very High)
4. Auto-approves Very Low and Low risk PRs
5. Flags Medium+ PRs for human review with specific concerns

## Architecture

```
PR opened/           GitHub Webhook     Kelos              Agent Pod
ready for review -->  pull_request  -->  TaskSpawner  -->  (read-only)
                      event              creates Task       |
                                                           v
                                                     Reads diff
                                                     Checks critical paths
                                                     Rates risk level
                                                     Auto-approves or flags
```

## Key Patterns Demonstrated

- **Multi-event triggers**: Responds to `opened`, `ready_for_review`, and `/approve` comments
- **Bot exclusion**: Excludes PRs from known bots (handled by dep-review instead)
- **Risk assessment framework**: Configurable risk factors and critical path definitions
- **Conditional auto-approve**: Only approves truly low-risk changes
- **Stale PR guard**: Skips PRs with no commits in 30+ days
- **GitHub Check integration**: Reports status via GitHub Checks API

## Prerequisites

- GitHub webhook configured to send `pull_request` and `issue_comment` events
- `gh` CLI available in the agent image
- A shared read-only AgentConfig

## Files

- `taskspawner.yaml` — The TaskSpawner configuration

## Customization

- Define your critical paths (database, auth, ML, infrastructure)
- Adjust the risk scale thresholds
- Add/remove bot exclusions
- Configure which labels trigger special handling (e.g., `skip-agent-review`)
- Integrate with your external code review tools
