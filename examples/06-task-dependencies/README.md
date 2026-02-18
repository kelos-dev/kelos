# 06 — Task Dependencies

A three-stage pipeline that chains Tasks with `dependsOn`. Each stage waits
for the previous one to succeed before starting, and downstream prompts
reference upstream outputs using Go template syntax.

## Use Case

Break a feature into sequential stages — implement, test, review — where
each agent builds on the previous agent's work on the same branch.

## Pipeline

```
implement ──> write-tests ──> review
```

1. **implement** — scaffolds the feature and pushes to a branch.
2. **write-tests** — waits for `implement`, then adds tests on the same branch.
3. **review** — waits for `write-tests`, then opens and reviews a PR.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `workspace.yaml` | Workspace | Git repository to clone |
| `tasks.yaml` | Task (x3) | The three-stage pipeline |

## How Dependencies Work

- A Task with `dependsOn: [other-task]` stays in `Waiting` phase until
  every listed dependency reaches `Succeeded`.
- If any dependency fails, the dependent Task fails immediately.
- Downstream prompts can reference upstream outputs with Go `text/template`
  syntax:

  | Template Expression | Resolves To |
  |---------------------|-------------|
  | `{{ index .Deps "implement" "Outputs" }}` | Raw output lines from `implement` |
  | `{{ index .Deps "implement" "Results" "branch" }}` | The `branch` result value |
  | `{{ index .Deps "implement" "Results" "pr" }}` | The `pr` URL result value |
  | `{{ index .Deps "implement" "Name" }}` | The dependency task name |

  Agents automatically capture `branch`, `pr`, `commit`, `base-branch`,
  `cost-usd`, `input-tokens`, and `output-tokens` as results. See the
  [reference docs](../../docs/reference.md) for the full list.

## Steps

1. **Edit the secrets** — replace placeholders in both `github-token-secret.yaml`
   and `credentials-secret.yaml` with your real tokens.

2. **Edit `workspace.yaml`** — set your repository URL and branch.

3. **Edit `tasks.yaml`** — customize the prompts for your feature.

4. **Apply the resources:**

```bash
kubectl apply -f examples/06-task-dependencies/
```

5. **Watch the pipeline progress:**

```bash
kubectl get tasks -w
```

You should see:

```
NAME          TYPE         PHASE      AGE
implement     claude-code  Running    10s
write-tests   claude-code  Waiting    10s
review        claude-code  Waiting    10s
```

Then as `implement` succeeds:

```
NAME          TYPE         PHASE      AGE
implement     claude-code  Succeeded  5m
write-tests   claude-code  Running    5m
review        claude-code  Waiting    5m
```

6. **Stream logs from a specific stage:**

```bash
axon logs implement -f
axon logs write-tests -f
```

7. **Cleanup:**

```bash
kubectl delete -f examples/06-task-dependencies/
```

## CLI Equivalent

You can create the same pipeline with `axon run`:

```bash
axon run -p "Add a /healthz endpoint" \
  --name implement \
  --branch feature/healthz

axon run -p "Write tests for the /healthz endpoint" \
  --name write-tests \
  --branch feature/healthz \
  --depends-on implement

axon run -p "Review the changes and open a PR" \
  --name review \
  --branch feature/healthz \
  --depends-on write-tests
```

## Notes

- All three tasks share the same `branch: feature/healthz`. Axon's branch
  mutex guarantees that only one task runs on a given branch at a time,
  even without `dependsOn`. The `dependsOn` field adds the additional
  guarantee that a task only starts after its dependencies **succeed**
  (not just finish).
- If `implement` fails, both `write-tests` and `review` fail immediately
  with the message "Dependency 'implement' failed".
- Prompt templates are resolved once, right before the Job is created.
  If a template variable cannot be resolved, the raw prompt is used as
  a fallback.
