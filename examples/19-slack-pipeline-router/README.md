# 19 — Slack Pipeline Router

One Slack front door that routes each request to the right agent. A
TaskSpawner with `pipelineTemplate` creates a TaskPipeline for every matching
message. The first stage classifies the request, and conditional stages run
only the handler that matches.

## Use Case

Send every request to one channel instead of asking people to pick the right
agent. Adding a new handler means adding a stage and a value to the route
prompt, not editing trigger regexes across several spawners.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agents |
| `taskspawner.yaml` | TaskSpawner | Slack source with a routing pipeline template |

## How It Works

```text
Slack message
    │
    ▼
route       replies "docs", "incident", or "none"
    ▼
docs        runs only when route replied "docs"
    ▼
incident    runs only when route replied "incident"
    ▼
fallback    runs for any other reply
```

- The message's `ID`, `Title`, `Body`, `URL`, and `Kind` become the
  TaskPipeline's `params`, and every stage reads the request as
  `{{.Params.Body}}`.
- Each conditional stage decodes the `route` stage's base64 `response` result
  with `b64dec` and compares it against its `values`. Stages whose conditions
  are not met are `Skipped` and create no Task.
- Every stage Task inherits the Slack reporting annotations, so the route
  stage's answer and the selected handler's result are both posted to the
  thread that started the pipeline.
- `maxConcurrency` counts active TaskPipelines.

The route stage can only select stages listed in this manifest. Treat every
conditional stage as reachable by any message in the channel.

## Steps

1. Install Kelos with the Slack server enabled. Create a Secret with
   `SLACK_BOT_TOKEN` and `SLACK_APP_TOKEN`, and set these Helm values:

```yaml
slackServer:
  enabled: true
  secretName: kelos-slack # Secret with SLACK_BOT_TOKEN and SLACK_APP_TOKEN
```

2. Edit `credentials-secret.yaml` and set your Claude OAuth token.
3. Edit `taskspawner.yaml` and set your Slack channel ID.
4. Apply the resources:

```bash
kubectl apply -f examples/19-slack-pipeline-router/
```

5. Invite the bot to the channel and mention it with a request. Watch the
   pipeline:

```bash
kubectl get taskpipelines -l kelos.dev/taskspawner=support-front-door -w
```

6. Inspect which stage ran:

```bash
kubectl get taskpipeline <name> -o jsonpath='{range .status.stageStatuses[*]}{.name}{"\t"}{.phase}{"\n"}{end}'
```

## Cleanup

```bash
kubectl delete -f examples/19-slack-pipeline-router/
```

## Notes

- `pipelineTemplate` supports only the `slack` source and cannot be combined
  with `spec.credentials`. Each stage sets its own `worker` or `workerPoolRef`;
  use a WorkerPool for the route stage to avoid a cold pod start per message.
- `kelos run --from taskspawner/support-front-door` is not supported for
  pipeline spawners.
- See the [TaskSpawner pipeline template reference](../../docs/reference.md#taskspawner-pipeline-templates)
  and [conditional stages](../../docs/reference.md#conditional-stages).
