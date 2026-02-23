# 07 — Task Pipeline

A multi-step pipeline that chains Tasks using `dependsOn` and passes results
between stages. One agent scaffolds a feature, a second writes tests on the
same branch, and a third opens a PR.

## Use Case

Break complex work into specialized steps. Each agent focuses on one job and
hands off structured results (branch name, commit SHA) to the next stage. The
controller ensures ordering, detects cycles, and fails fast if an upstream
stage fails.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `workspace.yaml` | Workspace | Git repository to clone |
| `pipeline.yaml` | Task (x3) | Three chained Tasks forming a pipeline |

## How It Works

```
scaffold (Task)
    │  creates branch, writes code
    │  outputs: branch, commit
    │
    ▼
write-tests (Task, dependsOn: [scaffold])
    │  checks out the same branch
    │  reads scaffold's branch via {{.Deps.scaffold.Results.branch}}
    │  outputs: branch, commit
    │
    ▼
open-pr (Task, dependsOn: [write-tests])
    │  reads branch from write-tests results
    │  opens a pull request
    │  outputs: pr URL
```

## Key Concepts

- **`dependsOn`** — a Task lists the names of Tasks that must succeed before
  it starts. The controller moves the Task to `Waiting` phase until all
  dependencies reach `Succeeded`. If any dependency fails, the downstream
  Task fails immediately.

- **Result passing** — when a Task completes, the controller captures
  structured key-value outputs (branch, commit, PR URL, etc.) into
  `status.results`. Downstream Tasks can reference these in their prompt
  using Go template syntax:

  ```
  {{index .Deps "scaffold" "Results" "branch"}}
  ```

  The `.Deps` map is keyed by dependency Task name and contains `Results`
  (the key-value map) and `Outputs` (raw output lines).

- **Branch serialization** — Tasks sharing the same `branch` value are
  serialized automatically. Only one runs at a time, so the second Task
  always sees the first Task's commits.

- **Cycle detection** — the controller detects circular dependencies via DFS
  and fails the Task immediately.

## Steps

1. **Edit the secrets** — replace placeholders in `credentials-secret.yaml`
   and `github-token-secret.yaml`.

2. **Edit `workspace.yaml`** — set your repository URL.

3. **Apply the resources:**

```bash
kubectl apply -f examples/07-task-pipeline/
```

4. **Watch the pipeline progress:**

```bash
kubectl get tasks -w
```

You should see `scaffold` run first, then `write-tests` move from `Waiting`
to `Running`, and finally `open-pr`.

5. **View results from a completed Task:**

```bash
axon get task scaffold -o yaml | grep -A 10 results:
```

6. **Stream logs from any stage:**

```bash
axon logs scaffold -f
axon logs write-tests -f
axon logs open-pr -f
```

7. **Cleanup:**

```bash
kubectl delete -f examples/07-task-pipeline/
```

## CLI Equivalent

You can create the same pipeline with the CLI:

```bash
axon run -p "Scaffold a user authentication module" \
  --name scaffold --branch feature/auth --workspace my-workspace -w

axon run -p 'Write tests for the auth module on branch {{index .Deps "scaffold" "Results" "branch"}}' \
  --name write-tests --depends-on scaffold --branch feature/auth --workspace my-workspace -w

axon run -p 'Open a PR for branch {{index .Deps "write-tests" "Results" "branch"}}' \
  --name open-pr --depends-on write-tests --branch feature/auth --workspace my-workspace -w
```

## Notes

- All three Tasks share the same `branch` value. This means even without
  `dependsOn`, the branch lock would serialize them. Adding `dependsOn`
  ensures strict ordering and enables result passing.
- If `scaffold` fails, both `write-tests` and `open-pr` fail immediately
  with a "dependency failed" message.
- Set `ttlSecondsAfterFinished` on each Task if you want automatic cleanup
  after the pipeline completes.
