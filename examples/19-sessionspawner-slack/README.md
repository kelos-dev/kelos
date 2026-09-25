# Slack Threads with a Persistent Session

This example gives every Slack thread its own Claude Code conversation. The
first message that mentions the bot in a matching channel creates a Session for
that thread; every later message in the thread that also mentions the bot runs
as another turn in the same conversation, so the agent keeps the context instead
of starting over. A reply that does not mention the bot is ignored and never
reaches the Session.

It requires the Kelos Slack server, which owns the Socket Mode connection and
drives the turns. Install the chart with `slackServer.enabled=true` and the bot
and app tokens in place, then invite the bot to the channel.

Replace the API key and channel ID placeholders, then apply the files:

```bash
kubectl apply -f examples/19-sessionspawner-slack/
```

Mention the bot in the channel. It replies in a thread with
`Working on your request...` and edits that reply once the turn finishes. Reply
in the same thread to continue the conversation:

```
@kelos read internal/slack/handler.go and tell me how messages are routed
  ↳ @kelos now do the same for internal/webhook/handler.go
```

Inspect the Session behind a thread. The bot names it in the reply it posts, and
the spawner's own Sessions carry its UID in the `kelos.dev/sessionspawner`
label:

```bash
kubectl get sessions -l kelos.dev/sessionspawner="$(kubectl get sessionspawner slack-thread -o jsonpath='{.metadata.uid}')"
kelos session connect <session-name>
```

Two things are worth deciding before this runs anywhere busy:

- **Lifecycle.** Every matching thread gets a Pod and a 10Gi volume.
  `idlePolicy.suspendAfterSeconds` stops the runtime after 30 idle minutes, and
  `idlePolicy.deleteAfterSeconds` deletes the Session — and its workspace —
  after a day. Deleting discards uncommitted work, so raise it for threads whose
  branches matter.
- **Scope.** `spec.when.slack.channels` keeps the experiment to one channel.
  Remove it only once the lifecycle numbers hold up.

A Slack TaskSpawner and this SessionSpawner both act on a message they both
match, which produces two replies. Scope them to different channels or triggers.
