# 12 — API Contract Validation

An end-to-end API contract validation pipeline that uses PR-triggered
validation, scheduled compatibility audits, and a multi-step consumer
update pipeline to catch and resolve breaking API changes.

## Use Case

Detect breaking changes in API definitions (OpenAPI, protobuf, GraphQL)
when PRs are opened, run weekly audits for unreleased drift, and
coordinate migration across consumer repositories when intentional
breaking changes are approved.

AI agents add value beyond static diff tools by reasoning about semantic
intent, analyzing real consumer impact, proposing backward-compatible
alternatives, and generating targeted migration guides.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Anthropic API key for the agent |
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `workspace.yaml` | Workspace | Git repository containing API definitions |
| `agentconfig.yaml` | AgentConfig | Shared instructions for the API validator agent |
| `taskspawner-webhook.yaml` | TaskSpawner | PR-triggered validation via `/validate-api` comment |
| `taskspawner-cron.yaml` | TaskSpawner | Weekly cross-service compatibility audit |
| `pipeline.yaml` | Task (x2) | Consumer impact analysis and migration guide pipeline |

## Configurations

### 1. PR-Triggered API Validation (Webhook)

Listens for `/validate-api` comments on PRs. The agent checks out the PR
branch, runs structural diff tools if available, classifies each change,
and posts a review summarizing findings with backward-compatible
alternatives for any breaking changes.

### 2. Scheduled Compatibility Audit (Cron)

Runs every Monday at 8:00 AM UTC. The agent compares the current API
surface against the latest tagged release, identifies unreleased breaking
changes and API hygiene issues, and creates a GitHub issue with findings.

### 3. Consumer Update Pipeline (Task Dependencies)

A two-stage pipeline for coordinating migration after an intentional
breaking change is approved:

```
identify-consumers (Task)
    │  analyzes which consumers use the affected endpoints
    │  outputs: migration-plan.md
    │
    ▼
generate-migration-guide (Task, dependsOn: [identify-consumers])
    │  reads migration-plan.md
    │  generates step-by-step migration guide with code examples
    │  opens a PR with the guide
```

## Steps

1. **Edit the secrets** — replace placeholders in `credentials-secret.yaml`
   and `github-token-secret.yaml`.

2. **Edit `workspace.yaml`** — set your API service repository URL.

3. **Review `agentconfig.yaml`** — customize the agent instructions for your
   API format and conventions.

4. **Deploy the webhook-triggered validator:**

```bash
kubectl apply -f credentials-secret.yaml -f github-token-secret.yaml \
  -f workspace.yaml -f agentconfig.yaml -f taskspawner-webhook.yaml
```

5. **Deploy the scheduled audit (optional):**

```bash
kubectl apply -f taskspawner-cron.yaml
```

6. **Run the consumer update pipeline (when needed):**

```bash
kubectl apply -f pipeline.yaml
```

7. **Trigger a validation** — comment `/validate-api` on any PR in the
   configured repository.

8. **Watch spawned Tasks:**

```bash
kubectl get tasks -w
```

9. **Cleanup:**

```bash
kubectl delete -f examples/12-api-contract-validation/
```

## Complementary Issues

This use case works with current Kelos primitives but would benefit from
proposed API extensions:

| Issue | Enhancement | Benefit |
|-------|-------------|---------|
| #778 | `filePatterns` filter | Auto-trigger on API file changes instead of manual `/validate-api` |
| #884 | Cross-repo propagation | Create migration PRs directly in consumer repos |
| #842 | `readOnlyWorkspaces` | Read consumer repos without full write access |
| #881 | `contextSources` | Inject previous audit results into validation prompts |

## Notes

- The webhook TaskSpawner uses `issue_comment` with `bodyContains` as a
  workaround until `filePatterns` filtering (#778) is available.
- The pipeline Tasks share the same `branch` value, which serializes them
  automatically in addition to the explicit `dependsOn` ordering.
- Adjust `maxConcurrency` on the webhook TaskSpawner based on your expected
  PR volume.
