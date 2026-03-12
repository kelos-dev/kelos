# 09 — Open-Source Maintainer Kit

A drop-in toolkit of comment-triggered TaskSpawners that automate common
open-source maintenance tasks: issue triage, auto-fix, PR feedback loops,
stale issue cleanup, and contributor onboarding.

## Use Case

Maintainers type `/bot triage`, `/bot fix`, `/bot update`, or `/bot guide` on
any issue or PR. Kelos picks up the comment and spawns an agent Task
automatically. No label taxonomy required — just comments.

This is a **multi-agent orchestration** example: five TaskSpawners coordinate
the full issue-to-PR-to-review lifecycle.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `github-token-secret.yaml` | Secret | GitHub token for cloning, PR creation, and polling |
| `credentials-secret.yaml` | Secret | Anthropic API key for the agent |
| `workspace.yaml` | Workspace | Git repository to clone into each Task |
| `agentconfig.yaml` | AgentConfig | Shared instructions for all agents |
| `triage-spawner.yaml` | TaskSpawner | `/bot triage` — classify and prioritize issues |
| `worker-spawner.yaml` | TaskSpawner | `/bot fix` — pick up issues and create PRs |
| `pr-responder-spawner.yaml` | TaskSpawner | `/bot update` — address PR review feedback |
| `stale-issue-spawner.yaml` | TaskSpawner | Cron — weekly stale issue cleanup |
| `contributor-guide-spawner.yaml` | TaskSpawner | `/bot guide` — onboarding help for contributors |

## How It Works

```
Maintainer comments "/bot triage" on issue
    └── triage-spawner → agent classifies, labels, and comments

Maintainer comments "/bot fix" on issue
    └── worker-spawner → agent creates branch, implements fix, opens PR

Reviewer comments "/bot update" on PR
    └── pr-responder-spawner → agent addresses review feedback, pushes to branch

Every Monday at 9am
    └── stale-issue-spawner → agent reviews inactive issues, posts updates

Maintainer comments "/bot guide" on good-first-issue
    └── contributor-guide-spawner → agent posts a contributor guide comment
```

## Steps

1. **Edit the secrets** — replace placeholders in `github-token-secret.yaml`
   and `credentials-secret.yaml` with your real tokens.

2. **Edit `workspace.yaml`** — set your repository URL.

3. **Review `agentconfig.yaml`** — customize the instructions for your project.

4. **Apply the resources:**

```bash
kubectl apply -f examples/09-oss-maintainer-kit/
```

5. **Verify the spawners are running:**

```bash
kubectl get taskspawners -w
```

6. **Test it** — comment `/bot triage` on an open issue in your repository.
   The TaskSpawner picks it up on the next poll and creates a Task.

7. **Watch spawned Tasks:**

```bash
kubectl get tasks -w
```

8. **Cleanup:**

```bash
kubectl delete -f examples/09-oss-maintainer-kit/
```

## Customization

- **Swap agent type** — change `type` in any spawner's `taskTemplate` to use
  `codex`, `gemini`, or another supported agent.
- **Add label filters** — add `labels` or `excludeLabels` to any spawner to
  narrow which issues/PRs it responds to.
- **Adjust concurrency** — increase `maxConcurrency` for higher throughput or
  decrease it to limit resource usage.
- **Change the trigger comment** — replace `/bot triage` with any prefix your
  team prefers (e.g., `/kelos triage`, `/ai triage`).
- **Layer in label-based triggers** — for projects with an existing label
  taxonomy, you can remove `triggerComment` and use `labels` instead.
