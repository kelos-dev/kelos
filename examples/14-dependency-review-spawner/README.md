# Use Case: Automated Dependency Review

**Related Issues**: #981 (Supply Chain Compliance), #945 (IaC Lifecycle)

## Problem

Dependency update bots (Renovate, Dependabot) create PRs frequently, but each still requires human review to assess whether the upgrade is safe. For teams with dozens of dependencies, this creates a constant review backlog. Most patch/minor bumps are safe, but teams can't tell which ones need attention without reading changelogs and checking usage.

## Solution

A Kelos TaskSpawner that triggers when Renovate/Dependabot opens a PR. The agent:
1. Identifies the package and version bump type
2. Searches the codebase for all usages of the affected package
3. Reads the changelog from the PR body
4. Assesses risk and either auto-approves or requests human review
5. Identifies the best human reviewer when escalation is needed

## Architecture

```
Renovate/Dependabot    GitHub Webhook     Kelos              Agent Pod
opens PR          -->  pull_request  -->  TaskSpawner  -->  (read-only)
                       event              creates Task       |
                                                            v
                                                      Reads diff + changelog
                                                      Searches codebase usage
                                                      Posts review comment
                                                      Auto-approves if safe
```

## Key Patterns Demonstrated

- **Author-filtered webhook**: Only triggers on bot-authored PRs
- **Structured review output**: Consistent format for all reviews
- **Conditional auto-approve**: Safe upgrades approved automatically, risky ones escalated
- **Reviewer identification**: Uses git history to find the best human reviewer

## Prerequisites

- GitHub webhook configured to send `pull_request` events to Kelos
- `gh` CLI available in the agent image
- A shared read-only AgentConfig

## Files

- `taskspawner.yaml` — The TaskSpawner configuration

## Customization

- Adjust the author filter for your bot username (Renovate vs Dependabot)
- Add package-specific rules (e.g., always escalate security-sensitive packages)
- Modify the risk assessment criteria for your team's needs
- Change the fallback reviewer to your team's default
