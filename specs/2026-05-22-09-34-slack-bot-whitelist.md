# Slack Bot Allowlist

## Context

Cody did not respond when a Slack workflow/bot posted `@cody ...`, but did
respond when a human replied in the same thread. Current Kelos behavior explains
that difference:

- `internal/slack/handler.go` calls `shouldProcess(...)` before matching any
  `TaskSpawner`.
- `shouldProcess(...)` rejects every Slack `message` event with subtype
  `bot_message`.
- The test suite explicitly asserts that `bot_message` is filtered.

Slack's event docs match this shape. A bot-authored channel message is delivered
as `type: "message"`, `subtype: "bot_message"`, and includes `bot_id`. Slack
also documents `app_mention` as a separate event shape for direct app mentions;
`slack-go` exposes `BotID` on `AppMentionEvent` when another bot triggers that
mention.

## Goal

Allow selected trusted Slack bots to trigger Cody/Kelos by mentioning the bot,
while preserving the default protection against bot loops.

## Non-Goals

- No per-channel bot allowlist.
- No username-based allowlist; Slack usernames/icons are display metadata and
  can be overridden.
- No broad "allow all bots" mode.
- No change to existing human-message matching semantics.
- No Helm chart value or Slack server deployment environment change.

## Proposed Configuration

Use a `TaskSpawner.spec.when.slack` allowlist keyed by Slack `bot_id`.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-debug-slack
spec:
  when:
    slack:
      allowedBotIDs:
        - B123ABC456
      triggers:
        - pattern: .+
```

This keeps trust scoped to the Slack automation that needs it. A general
human-only TaskSpawner can keep the default behavior by leaving
`allowedBotIDs` empty, while Cody can explicitly allow a trusted workflow bot.

This requires a `TaskSpawner` CRD schema change, but it avoids any Helm chart or
Slack server deployment configuration change. The centralized Slack server still
receives all events; it simply defers the bot allow/deny decision to each
matching Slack `TaskSpawner`.

## Runtime Behavior

Default behavior stays unchanged:

- human messages with `@cody` continue through the existing Slack matching path;
- bot messages are ignored unless their Slack `bot_id` is listed on the
  matching `TaskSpawner`;
- Cody's own messages are always ignored.

For `message` events:

1. If `subtype == "bot_message"`:
   - require `bot_id` to be present;
   - reject if `bot_id` is Cody's own bot id;
   - keep the message eligible for routing instead of rejecting it globally;
   - each `TaskSpawner` rejects it unless `bot_id` is listed in
     `spec.when.slack.allowedBotIDs`.
2. Continue rejecting `message_changed`, `message_deleted`, and
   `message_replied`.
3. Continue rejecting messages with no text or attachments.

For `app_mention` events:

1. Add a handler path that maps `AppMentionEvent` into the same
   `SlackMessageData` shape used by `MessageEvent`.
2. If `AppMentionEvent.BotID` is present, mark the message as bot-authored and
   apply the same per-TaskSpawner allowlist rules.
3. If both `message` and `app_mention` subscriptions deliver the same mention,
   existing task-name idempotency should prevent duplicate task creation because
   task names are derived from channel + timestamp.

After a trusted bot message is allowed through, the normal `TaskSpawner`
matching still applies:

- it must contain Cody's Slack mention unless the trigger has
  `mentionOptional: true`;
- trigger regexes still match the message text after leading mentions are
  stripped;
- `excludePatterns` still reject matching text.

## Implementation Plan

### Kelos

- Store Cody's own Slack `bot_id` from `auth.test`; `slack-go` already exposes
  `AuthTestResponse.BotID`.
- Extend `SlackHandler` with:
  - `botID string` for Cody's own Slack bot id.
- Extend `SlackMessageData` with optional sender metadata:
  - `BotID string`;
  - `IsBotMessage bool`.
- Change the pre-routing filter so `bot_message` is not rejected globally.
  Instead, reject only Cody's own bot id, unsupported subtypes, and messages
  without content.
- Add `AllowedBotIDs []string` to the Slack trigger config in
  `api/v1alpha1/taskspawner_types.go`.
- Update `MatchesSpawner` so bot-authored messages require
  `msg.BotID in slackCfg.AllowedBotIDs`; human messages ignore this field.
- Add an `AppMentionEvent` case in `handleEventsAPI`, reusing the same routing
  path as message events.
- Include `bot_id` in filtered-message debug logs so future investigations can
  tell which bot was rejected without needing raw Slack payloads.
- Optionally annotate spawned Tasks with `kelos.dev/slack-bot-id` when the
  trigger came from an allowed bot.
- Regenerate CRDs/deepcopy code using the repo's standard generation command.

### Platform GitOps

Set Cody's Slack `TaskSpawner` with the trusted workflow bot id:

```yaml
when:
  slack:
    allowedBotIDs:
      - <security-workflow-bot-id>
    triggers:
      - pattern: .+
```

No HelmRelease value change is required. The only platform GitOps change is the
`TaskSpawner` manifest after the updated CRD/controller image is deployed.

## Finding The Bot ID

Preferred ways:

- Inspect the Slack event payload for the workflow message and read `bot_id`.
- Call Slack `conversations.replies` for the thread root/reply and read
  `messages[].bot_id`.
- Temporarily increase `kelos-slack-server` verbosity after the implementation
  lands; rejected bot-message logs should include `bot_id`.

Do not configure by display name such as "Fix these security issues". Slack docs
state `bot_message` may carry `username`/`icons` overrides; `bot_id` is the stable
bot-level identifier.

## Tests

Add or update focused tests:

- the pre-routing filter allows eligible non-self `bot_message` events through
  to `TaskSpawner` matching.
- the pre-routing filter rejects bot messages with missing `bot_id`.
- the pre-routing filter rejects Cody's own bot id before `MatchesSpawner`.
- `MatchesSpawner` rejects bot messages when the allowlist is empty.
- `MatchesSpawner` rejects bot messages with missing `bot_id`.
- `MatchesSpawner` allows `bot_message` only when `bot_id` is allowlisted.
- Existing human-message cases still pass unchanged.
- Handler test: allowed bot message with `@bot` creates one Task.
- Handler test: non-allowlisted bot message creates no Task.
- Handler test: allowed bot `app_mention` creates one Task.
- CRD/render test: `spec.when.slack.allowedBotIDs` appears in the generated
  TaskSpawner schema with Slack bot id validation.

Suggested local validation:

```bash
go test ./internal/slack ./cmd/kelos-slack-server
go test ./internal/helmchart ./internal/cli
```

## Acceptance Criteria

- With no allowlist configured, bot-authored Slack messages remain ignored.
- With one trusted bot id configured, that bot can mention Cody and create the
  same kind of Slack task a human mention creates.
- Cody replies in the originating Slack thread.
- Cody's own Slack messages do not retrigger Cody.
- Human-triggered Cody behavior is unchanged.
- The live Slack server logs provide enough metadata to identify rejected bot ids
  during future debugging.
