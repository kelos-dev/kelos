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
2. Edit `taskspawner.yaml` and replace both `myorg/myrepo` placeholders with
   your repository. Set `spec.when.githubWebhook.repository` to the repository
   name in `owner/repo` format and set the Workspace's `spec.repo` to its clone
   URL. Both values must identify the repository configured in step 1.
3. Apply the manifests:

   ```bash
   kubectl apply -f taskspawner.yaml
   ```

## Notes

- **Naming / deduplication:** `nameTemplate: "ci-fix-{{.ID}}"` names the Task by
  the check run's unique ID, so redeliveries of the same run collapse into one
  Task while a rerun — or a failure on a new commit — is a distinct check run and
  gets its own Task. Naming by PR number instead would silently suppress later
  failures on the same PR.
- **Do not set `excludeAuthors`:** the sender of a `check_run` event is the CI
  app (e.g. `github-actions[bot]`), not the PR author, and spawner-level
  `excludeAuthors` is applied *before* the conclusion/checkName filters — so
  excluding bot senders would drop every check run. Scope the workflow with the
  `conclusion` and `checkName` filters instead. `excludePullRequestAuthors` does
  not help here either: GitHub's `check_run.pull_requests[]` entries carry no
  `user`, so a `check_run` event has no pull-request author to match against and
  is never excluded by that field.
- **Check runs without a linked PR:** a `check_run` is not always associated with
  a pull request (checks on pushes to `main`, tags, or some fork PRs). In those
  cases `Branch` and `Number` are absent. The manifest renders PR-dependent
  variables with `{{index . "Branch"}}` (which yields an empty string on a
  missing key instead of failing) and guards the prompt with
  `{{if index . "Number"}}`, so an unlinked check run produces a Task that
  exits without changes rather than causing the webhook delivery to error.
- Start with cheap, low-risk checks (lint/format) before expanding to test and
  build failures.
