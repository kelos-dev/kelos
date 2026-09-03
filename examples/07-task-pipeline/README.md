# 07 — Task Pipeline

A managed multi-step workflow. One agent scaffolds a feature, a second writes
tests on the same branch, and a third opens a pull request.

## Use Case

Break complex work into named stages while managing the workflow as one
resource. Stages run in order, and each stage can fan out into parallel Tasks.
The pipeline status summarizes progress with bounded per-stage counts.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `workspace.yaml` | Workspace | Git repository to clone |
| `pipeline.yaml` | TaskPipeline | Three-stage feature development workflow |

## How It Works

```text
scaffold
    │  creates branch, writes code
    │  results: branch, commit
    ▼
write-tests
    │  reads scaffold's result from .Stages
    │  results: branch, commit
    ▼
open-pr
       opens a pull request
```

The controller creates one owned Task for each stage after the preceding stage
has succeeded. This example's child Task names are `auth-feature-scaffold`,
`auth-feature-write-tests`, and `auth-feature-open-pr`.

Downstream prompt templates receive earlier results under `.Stages`, keyed by
stage name. Each value is a list because a matrix stage can create more
than one Task:

```text
{{index .Stages "scaffold" 0 "Results" "branch"}}
```

Missing template keys fail the stage and pipeline. The failure is reported by
the TaskPipeline `Ready` condition; Kelos does not pass the unrendered template
to an agent.

All stages use the same branch, so the Workspace contains the commits produced
by the preceding stage when the next Task starts.

## Steps

1. Edit the secrets and replace the placeholder values.
2. Edit `workspace.yaml` and set your repository URL.
3. Apply the resources:

```bash
kubectl apply -f examples/07-task-pipeline/
```

4. Watch the pipeline and its child Tasks:

```bash
kubectl get taskpipeline auth-feature -w
kubectl get tasks -l kelos.dev/taskpipeline=auth-feature
```

5. View stage progress and child Task results:

```bash
kubectl get taskpipeline auth-feature -o yaml
kubectl get tasks -l kelos.dev/taskpipeline=auth-feature -o yaml
```

6. Stream logs from a stage's child Task:

```bash
kelos logs auth-feature-scaffold -f
kelos logs auth-feature-write-tests -f
kelos logs auth-feature-open-pr -f
```

7. Delete the pipeline and all owned Tasks:

```bash
kubectl delete -f examples/07-task-pipeline/
```

## Notes

- A failed child Task prevents later stages from starting. Child Tasks that are
  already active are allowed to finish before the pipeline becomes `Failed`.
- `TaskPipeline.spec.stages` is immutable. Delete and recreate the resource to
  run a modified workflow.
- Matrix fan-out and aggregate result templates are covered in the
  [TaskPipeline reference](../../docs/reference.md#taskpipeline).
