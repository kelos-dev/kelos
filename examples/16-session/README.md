# Interactive Session

This example creates one Claude Code conversation in a dedicated one-replica
StatefulSet. It checks out an `interactive-review` branch and submits an initial
prompt before a client connects. Replace the API key placeholder, then apply the
files:

```bash
kubectl apply -f examples/16-session/
kelos get session interactive-review
```

When the Session is `Ready`, connect from a terminal:

```bash
kelos session connect interactive-review
```

The terminal client prints commands when interaction is required. Use
`/interrupt` to stop active work and `/answer INPUT_ID QUESTION_ID VALUE` for a
provider question. Both terminal and web chat accept `!COMMAND` to run a shell
command in the Session workspace. Claude Code and Codex Sessions also accept
`/goal OBJECTIVE`; use `/goal` to inspect it and `/goal clear` to remove it.
Claude Code replaces an existing goal when another is set; use `/interrupt` to
stop active work before a queued goal command runs. Codex also supports
`/goal edit OBJECTIVE`, `/goal pause`, and `/goal resume`.

All supported agents accept `/loop 5m Check CI and address failures` to repeat a
prompt when the Session is idle. Use `/loop` to inspect it and `/loop pause`,
`/loop resume`, or `/loop clear` to control it. An active loop prevents automatic
idle suspension and deletion; pause or clear it when recurring work is finished.

When enabled, the Kelos Console's Sessions view provides the same conversation.
Disconnecting either client does not stop the agent runtime. If the Pod or
StatefulSet is removed, the controller recreates it on the Session-owned
persistent workspace; active work is marked interrupted and both clients
reconnect without replaying it.

Suspend the runtime without deleting the Session or its persistent workspace,
then resume it later:

```bash
kubectl patch session interactive-review --type merge -p '{"spec":{"suspend":true}}'
kubectl patch session interactive-review --type merge -p '{"spec":{"suspend":false}}'
```

Deleting the Session deletes
its StatefulSet, Pod, governing Service, and persistent workspace and ends the
conversation:

```bash
kelos delete session interactive-review
```

To use another supported provider, set `spec.worker.type` to `codex` or
`opencode` and provide a credentials Secret with the corresponding
`CODEX_API_KEY` or `OPENCODE_API_KEY` key.
