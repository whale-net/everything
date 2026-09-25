# `@mention` — whagent-net AI sessions

Talk to a whagent-net AI agent by `@mention`ing the bot in a linked channel.

## Usage

- Link a channel to a whagent-net agent definition first (see "Channel setup" below) — one agent per channel today.
- `@mention` the bot anywhere in a linked channel: `@fcm <your message>`. The bot starts a new whagent-net session and replies in a thread under your message.
- The first reply is a standalone link to the session's whagent-net web page; it stays put for the life of the thread. Each turn then gets its own "Thinking…" placeholder message, edited in place once the turn resolves. The agent's reply is sent as a Slack `markdown` block so its standard Markdown (headings, lists, `**bold**`, code fences) renders; replies over Slack's 12,000-character markdown block limit fall back to plain text.
- Send more messages in that same thread to continue the session (with or without `@mention`ing the bot). A message sent while the bot is still working on a turn is queued and combined with any other queued messages into the next turn — it does not start a second, overlapping turn.
- If the channel isn't linked to an agent, mentioning the bot replies with a short "no agent configured for this channel" message and nothing else happens.

## How it works

One Temporal workflow (`SlackThreadAgentWorkflow`, `temporal/whagent/workflow.py`) per Slack thread, started on `app_mention` (`bot/handlers/whagent.py`) and identified by `fcm-<app_env>-whagent-thread-<channel>-<thread_ts>`:

1. Calls whagent-net's `StartSession` (service-account auth) with the mention text as the first turn, posts a standalone session-link message, then a placeholder message whose `ts` is captured.
2. Polls `GetSession` until the turn's state leaves `RUNNING`, then confirms with `ReadTranscript` that a genuinely new assistant reply has landed past a per-turn seq watermark before treating the turn as resolved — a `GetSession` read can still report the *previous* turn's terminal state for a moment right after `SendTurn` returns, so the state flag alone isn't proof this turn is done. Edits the placeholder message (`chat.update`) with the response once resolved. A failed turn gets a distinct failure message instead. A capped turn (hit its agent definition's turn/cost cap) still shows its real reply when the capping turn produced one — only appended with a cap notice, since the turn that trips a cap still runs to completion and commits its own transcript event first (`whagent_net/worker/caps.go`); a cap that trips mid-tool-loop, before any final reply is committed, falls back to the notice alone.
3. Thread replies reach the workflow via `relay_thread_reply` (`bot/handlers/whagent.py`), called from the single `message` listener in `bot/handlers/events.py` — Bolt runs only the first matching listener per event, so a second `@app.event("message")` would never fire. Any reply that lands in the thread while a turn is in flight is buffered (a `queue_message` signal) rather than triggering a new turn immediately; once the current turn resolves, everything queued is joined into one combined `SendTurn` call, posted as a new placeholder message, and the loop repeats.
4. An idle thread (no reply and no in-flight turn) times out after 30 minutes and the workflow ends, marking `SlackThreadSession.status = CLOSED`.

Auth is a single shared service-account client-credentials grant (`WHAGENT_CLIENT_ID`/`WHAGENT_CLIENT_SECRET` against `WHAGENT_KEYCLOAK_TOKEN_URL`) — every session in every channel runs as that one service identity; there's no per-Slack-user identity resolution yet.

## Channel setup

There's no admin command yet — link a channel by hand-inserting a row:

```sql
INSERT INTO fcm.slackchannelagentlink (slack_channel_id, whagent_agent_id, enabled)
VALUES (<slackchannel.id for the target channel>, '<whagent-net agent_id>', true);
```

`slack_channel_id` is the `slackchannel` table's own row id (not Slack's string channel id) — look it up by Slack channel id first. The linked `whagent_agent_id` must already exist as an `agent_definition` row in whagent-net (see `whagent_net/README.md` "Agent definition config") and must grant the fcm service account's Keycloak client its `required_role`.

## Data (schema `fcm`)

| Table | Purpose |
|---|---|
| `slackchannelagentlink` | Which whagent-net `agent_id` a channel uses. Hand-inserted for now; `enabled` lets a link be disabled without deleting it. |
| `slackthreadsession` | Maps an active Slack thread (`slack_channel_id`, `thread_ts`) to its whagent-net `whagent_session_id` and status (`ACTIVE`/`CLOSED`), so a reply in a tracked thread is recognized as a continuation rather than a new mention. |

## Roadmap notes

- v1 is mention-only (no slash command trigger) and single-agent-per-channel.
- No per-Slack-user identity resolution — every session runs as the one shared service account.
- Channel↔agent linking is hand-inserted SQL; an admin command/UI is a planned follow-up.
