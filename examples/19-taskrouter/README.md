# 19 — TaskRouter

One Slack front door for several agents. A TaskRouter reads an incoming
message, decides which TaskSpawner should handle it, and dispatches the work
there. Results come back to the same thread.

## Use Case

Without a router, every TaskSpawner with a Slack source sees every message and
each one that matches creates its own Task, so the arbitration lives in
`@`-mentions and trigger regexes that users have to know. A router replaces that
with one prompt and a list of routes: users describe their problem, and the
router picks the agent.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `spawners.yaml` | TaskSpawner | The two agents being routed between, both `OnDemand` |
| `router.yaml` | TaskRouter | The routing decision and the route menu |

This example assumes you already have a `claude-credentials` Secret and the
`docs-workspace` and `code-workspace` Workspaces from the earlier examples, plus
`kelos-slack-server` deployed with the bot invited to the channel.

## How it works

1. A message in the channel matches the router's Slack source.
2. The router creates a short-lived **decision Task** that sees the routing
   prompt, the route descriptions, and the request — nothing else.
3. The decision emits one route name. Anything that is not a listed route name
   counts as no route.
4. The controller builds the Task the selected route's TaskSpawner would have
   built, carrying the Slack thread with it, so the agent's result lands in the
   same thread.

## Try it

```bash
kubectl apply -f spawners.yaml
kubectl apply -f router.yaml

# Post a message in the channel, then watch the decision and the dispatched work
kubectl get taskrouter support-router -w
kubectl get tasks -l kelos.dev/taskrouter=support-router
```

`kubectl describe taskrouter support-router` shows the last decision and the
`RoutesResolved` condition, which goes False when a route points at a
TaskSpawner that does not exist — checked on every reconcile, so a typo in a
`targetRef` shows up right after `kubectl apply` rather than on the first
request.

## Notes

- **The decision cannot invent a target.** It selects a name from `spec.routes`,
  so a prompt-injection attempt in a message costs at most the wrong route on
  the menu you wrote. Assume any listed route is reachable by any request.
- **The router does not narrate its own routing.** A decision posts only when it
  answered instead of dispatching, so a routed request produces the dispatched
  agent's replies and nothing more.
- **`fallback` decides what "no route" means.** `Reply` answers in the thread,
  `Route` sends it to a default route, and `Fail` records it on the router's
  status. Omitting `fallback` fails the request. It also decides whether the
  decision Task speaks: under `Reply` it posts its explanation, and under
  `Route` or `Fail` it stays silent so only the dispatched work narrates itself.
- **Dropping `triggerMode: OnDemand`** lets a spawner keep its own Slack source
  while still being a route target. That is supported; the dispatched Task is
  named the way the spawner would have named it, so the two paths collapse into
  one Task rather than doubling up.
- **Decisions are collected after a day.** Set
  `decision.ttlSecondsAfterHandled` to keep them longer for auditing, or shorter
  to reclaim them sooner. A decision the router has not acted on is never
  collected.
- **Latency.** Each decision starts a pod. Point `decision.workerPoolRef` at a
  pre-warmed WorkerPool to avoid the cold start; a pooled decision takes its
  environment from the pool rather than from `podOverrides`, and
  `decision.timeoutSeconds` is rejected alongside it — bound those on the pool.
