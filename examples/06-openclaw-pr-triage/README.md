# 06 — PR & Issue Triage for High-Volume Repos

A TaskSpawner that automatically triages every PR and issue on a fast-moving
repo like [OpenClaw](https://github.com/openclaw/openclaw). Inspired by
[@steipete's problem](https://x.com/steipete/status/2023057089346580828):
hundreds of PRs per day, many duplicates, no way to keep up manually.

## What It Does

For every new PR or issue, an agent:

1. **De-duplicates** — searches all open PRs/issues for semantic overlap and
   flags duplicates with links.
2. **Reviews quality** (PRs) — reads the diff and rates code quality, test
   coverage, scope, and description.
3. **Checks vision alignment** — flags PRs that stray from the project's
   core principles (open-source, privacy-first, self-hosted).
4. **Posts a triage comment** — a structured report with a clear
   recommendation (MERGE / REVISE / CLOSE AS DUPLICATE / NEEDS MAINTAINER REVIEW).

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token |
| `github-token-secret.yaml` | Secret | GitHub token for repo access and commenting |
| `workspace.yaml` | Workspace | Points to `openclaw/openclaw` |
| `taskspawner.yaml` | TaskSpawner | Polls PRs + issues, spawns triage agents |

## Steps

1. **Edit the secrets** — replace placeholders in both secret files.

2. **Apply:**

```bash
kubectl apply -f examples/06-openclaw-pr-triage/
```

3. **Watch it work:**

```bash
kubectl get taskspawners -w
kubectl get tasks -w
```

4. **Cleanup:**

```bash
kubectl delete -f examples/06-openclaw-pr-triage/
```

## Tuning

- `pollInterval: 3m` — how often to check for new PRs/issues.
- `maxConcurrency: 5` — how many triage agents run in parallel.
- `ttlSecondsAfterFinished: 600` — completed tasks are cleaned up after 10 min
  so the spawner can re-process if new activity appears.
- Edit the vision statement in `promptTemplate` to match your project's values.
- Add `excludeLabels: ["triaged"]` to skip already-processed items.
