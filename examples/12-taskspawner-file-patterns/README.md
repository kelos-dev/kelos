# TaskSpawner File Patterns Example

This example shows how to use `filePatterns` to create Tasks only when a
GitHub pull request or webhook event changes relevant paths.

`filePatterns` uses doublestar glob syntax. Kelos removes files matching
`exclude` first, then accepts the item when at least one remaining file
matches `include`. If `include` is omitted, any remaining file passes. If
every changed file is excluded, the item is skipped.

## Resources

- **Pull request poller** (`taskspawner-pr-review.yaml`) - polls open PRs and
  filters them by changed file path
- **GitHub webhook spawner** (`taskspawner-webhook.yaml`) - reacts to
  `pull_request` and `push` events that touch selected paths

Both manifests reference a `Workspace` named `my-workspace` and an OAuth
Secret named `claude-oauth-token`. Create those resources first or update the
manifests to match your cluster.

## Pull Request Polling

`taskspawner-pr-review.yaml` includes two patterns:

- `go-code-reviewer` only reviews PRs with Go files outside `vendor/`
- `code-reviewer-skip-docs` reviews PRs unless every changed file is
  documentation or license text

Apply it with:

```bash
kubectl apply -f taskspawner-pr-review.yaml
```

## GitHub Webhooks

`taskspawner-webhook.yaml` listens for `pull_request` and `push` events and
filters each event with `filePatterns` inside `githubWebhook.filters[]`.

For push events, Kelos reads changed files from the webhook payload. For
pull-request events, Kelos fetches changed files from the GitHub API using
the TaskSpawner workspace credentials, falling back to the webhook server's
global GitHub token resolver when the workspace does not provide a token.

Apply it with:

```bash
kubectl apply -f taskspawner-webhook.yaml
```

The GitHub webhook server must already be deployed and configured for the
repository sending events. See
[`10-taskspawner-github-webhook`](../10-taskspawner-github-webhook/) for the
webhook server setup flow.

## Cleanup

```bash
kubectl delete -f taskspawner-pr-review.yaml
kubectl delete -f taskspawner-webhook.yaml
```
