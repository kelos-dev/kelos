# CI/CD Failure Auto-Remediation Example

This example demonstrates a TaskSpawner that watches for failing CI checks and
dispatches an agent to diagnose and fix them, using GitHub `check_run` webhook
events.

## Overview

When a CI check (lint, unit tests, build, …) completes with a `failure`
conclusion on a pull request, GitHub sends a `check_run` webhook. This
TaskSpawner filters those events by conclusion and check name and spawns a
`claude-code` Task on the PR's head branch to fix the failure.

The relevant `githubWebhook` filter fields are:

- `conclusion` — matches the check run's conclusion (`success`, `failure`,
  `cancelled`, `timed_out`, `action_required`, `neutral`, `skipped`, `stale`).
- `checkName` — matches the check run's name (exact match or glob, e.g.
  `"lint"`, `"build-*"`).

Both are ignored for non-`check_run` events.

## Template variables for `check_run` events

In addition to the standard webhook variables, `check_run` events expose:

| Variable         | Description                                          |
| ---------------- | ---------------------------------------------------- |
| `{{.CheckName}}` | Check run name (e.g. `"lint"`)                       |
| `{{.Conclusion}}`| Check run conclusion (e.g. `"failure"`)              |
| `{{.CheckRunURL}}`| Link to the check run / CI logs                     |
| `{{.HeadSHA}}`   | Commit SHA under test                                |
| `{{.CheckApp}}`  | App that produced the check (e.g. `"GitHub Actions"`)|
| `{{.Branch}}`    | PR head branch (when the check is linked to a PR)    |
| `{{.Number}}`    | PR number (when the check is linked to a PR)         |

## Prerequisites

1. **Webhook Server**: the kelos-webhook-server deployed with a GitHub source.
2. **GitHub Webhook**: your repository configured to send `Check runs` events to
   your Kelos webhook endpoint.
3. **Secrets**: the webhook signing secret and the agent credentials
   (`claude-credentials`).

## Setup

1. Enable the **Check runs** event on your GitHub repository webhook (Settings →
   Webhooks → your webhook → "Let me select individual events").
2. Apply the manifests:

   ```bash
   kubectl apply -f taskspawner.yaml
   ```

## Notes

- `nameTemplate: "ci-fix-{{.Number}}-{{.CheckName}}"` collapses the burst of
  `check_run` deliveries GitHub sends for the same PR + check into a single Task.
- Scope with `repository` and `excludeAuthors` so the spawner does not react to
  check runs produced by its own automation.
- Start with cheap, low-risk checks (lint/format) before expanding to test and
  build failures.
