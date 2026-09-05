# 07 — Task Pipeline

Managed multi-step workflows. The sequential example scaffolds a feature,
writes tests, and opens a pull request. The matrix example fans out a review
across components and focus areas, then consolidates the findings.

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
| `matrix-pipeline.yaml` | TaskPipeline | Matrix fan-out followed by a summary stage |

## Sequential Pipeline

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

## Matrix Fan-Out Pipeline

```text
review(api, correctness) ───────┐
review(api, tests) ─────────────┤
review(controller, correctness) ├──▶ summarize
review(controller, tests) ──────┘
```

The `review` stage defines `component` and `focus` matrix parameters. Kelos
creates one parallel Task for each combination, for a total of four Tasks. The
`summarize` stage starts after all four succeed and iterates over their matrix
values and captured `response` results through `.Stages`. Agent responses are
base64-encoded in Task results, so the summary prompt tells the agent to decode
them before producing the report.

## Steps

1. Edit the secrets and replace the placeholder values.
2. Edit `workspace.yaml` and set your repository URL.
3. Apply the shared resources:

```bash
kubectl apply -f examples/07-task-pipeline/credentials-secret.yaml
kubectl apply -f examples/07-task-pipeline/github-token-secret.yaml
kubectl apply -f examples/07-task-pipeline/workspace.yaml
```

4. Apply the sequential pipeline:

```bash
kubectl apply -f examples/07-task-pipeline/pipeline.yaml
```

5. Watch the pipeline in one terminal:

```bash
kubectl get taskpipeline auth-feature -w
```

   Watch its child Tasks in a second terminal:

```bash
kubectl get tasks -l kelos.dev/taskpipeline=auth-feature -w
```

6. View stage progress and child Task results:

```bash
kubectl get taskpipeline auth-feature -o yaml
kubectl get tasks -l kelos.dev/taskpipeline=auth-feature -o yaml
```

7. Stream logs from a stage's child Task:

```bash
kelos logs auth-feature-scaffold -f
kelos logs auth-feature-write-tests -f
kelos logs auth-feature-open-pr -f
```

## Run the Matrix Example

After applying the secrets and Workspace from step 3, apply the matrix pipeline
instead of the sequential pipeline in step 4, or run both alongside each other:

```bash
kubectl apply -f examples/07-task-pipeline/matrix-pipeline.yaml
kubectl get taskpipeline matrix-review -w
```

Watch the four review Tasks run in parallel, followed by the summary Task:

```bash
kubectl get tasks -l kelos.dev/taskpipeline=matrix-review -w
```

## Cleanup

Delete both examples and all owned Tasks. Resources that were not applied are
ignored:

```bash
kubectl delete --ignore-not-found -f examples/07-task-pipeline/
```

## Notes

- A failed child Task prevents later stages from starting. Child Tasks that are
  already active are allowed to finish before the pipeline becomes `Failed`.
- `TaskPipeline.spec.stages` is immutable. Delete and recreate the resource to
  run a modified workflow.
- Matrix parameters form a Cartesian product. The full matrix API and template
  context are covered in the
  [TaskPipeline reference](../../docs/reference.md#taskpipeline).
