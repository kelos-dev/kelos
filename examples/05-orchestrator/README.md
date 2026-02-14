# 05 — Orchestrator Pattern

An orchestrator workflow where independent Tasks run in parallel and a final
Task synthesizes their outputs. This demonstrates the `dependsOn` field and
prompt templates that reference dependency results.

## Use Case

Break a large task into smaller, independent research or work steps that run
concurrently, then combine their outputs in a single orchestrator Task.

## How It Works

```
research-api-design ──┐
                      ├──▶ synthesize-design
research-data-model ──┘
```

1. **Stage 1** — `research-api-design` and `research-data-model` run in
   parallel (no dependencies).
2. **Stage 2** — `synthesize-design` has `dependsOn` set to both stage-1 Tasks.
   It stays in the **Waiting** phase until both dependencies succeed, then its
   prompt template is rendered with the dependency outputs before the agent
   starts.

## Prompt Templates

Tasks that declare `dependsOn` can use Go `text/template` syntax in their
prompt to reference dependency outputs:

```yaml
prompt: |
  # Iterate over output lines
  {{ range (index .Deps "research-api-design" "Outputs") }}- {{ . }}
  {{ end }}

  # Access a specific structured result by key
  Schema: {{ index .Deps "research-data-model" "Results" "schema" }}
```

Available template data per dependency:

| Key       | Type              | Description                          |
|-----------|-------------------|--------------------------------------|
| `Outputs` | `[]string`        | Free-form output lines from the agent |
| `Results` | `map[string]string` | Structured key-value results         |
| `Name`    | `string`          | The dependency Task name             |

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `secret.yaml` | Secret | Anthropic API key for all Tasks |
| `tasks.yaml` | Task (×3) | Two research Tasks and one orchestrator Task |

## Steps

1. **Edit `secret.yaml`** — replace the placeholder with your real Anthropic API key.

2. **Apply the resources:**

```bash
kubectl apply -f examples/05-orchestrator/
```

3. **Watch the Tasks:**

```bash
kubectl get tasks -w
```

You should see both research Tasks start immediately, while
`synthesize-design` stays in `Waiting` until they succeed.

4. **Stream the orchestrator logs:**

```bash
kubectl logs -l job-name=synthesize-design -f
```

5. **Cleanup:**

```bash
kubectl delete -f examples/05-orchestrator/
```
