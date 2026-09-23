# `/poll` — ad-hoc polls

Simple Poll-style polls created with a slash command. Delivers capability C12 (see [product/02-capability-map.md](../product/02-capability-map.md)).

## Usage

```
/poll "Question?" "Option 1" "Option 2" [anonymous] [limit N]
```

- The question and each option go in double quotes (curly quotes from Slack clients also work). 2–10 options.
- `anonymous` — show counts only, not who voted.
- `limit N` — each person gets at most `N` active votes. With `limit 1`, clicking another option moves the vote; with `N > 1`, an extra click is rejected with an ephemeral message.
- Clicking an option you already voted for removes that vote.
- **Close poll** (creator only) freezes the results and removes the vote buttons.

Bad input is answered with an ephemeral usage message; nothing is stored.

## How it works

Everything runs in the `bot` deployment over the existing bolt Socket Mode connection — no new service, no HTTP endpoint.

1. `/poll` (`bot/handlers/poll.py`) parses the text (`bot/poll/parse.py`), records the command in `slackcommand`, inserts the poll and options, posts the Block Kit message (`bot/poll/render.py`), then stores the message `ts` on the poll.
2. A vote button click arrives as a `block_actions` event on the socket. `cast_poll_vote` (`db/dal/poll_dal.py`) locks the poll row, applies the toggle/limit rules, and returns a fresh snapshot; the handler re-renders the message with `chat.update`.
3. A per-poll in-process lock keeps vote → render → `chat.update` ordered so a stale render never overwrites a newer one (the bot runs as a single replica).

## Data (schema `fcm`)

| Table | Purpose |
|---|---|
| `poll` | Question, channel, creator, message `ts`, `anonymous`, `vote_limit` (NULL = unlimited), `created_at`, `closed_at` |
| `polloption` | Options in display order (`position`, unique per poll) |
| `pollvote` | Every vote ever cast. Un-voting stamps `removed_at` instead of deleting, so full vote history is kept. Current votes: `removed_at IS NULL`; a partial unique index allows one active vote per person per option |

Slack users and channels are stored by Slack ID, not as FKs to `slackuser`/`slackchannel`, because those are filled by a periodic sync and may not yet contain a new voter.

## Slack app setup

The Slack app config must include a `/poll` slash command and have Interactivity enabled (Socket Mode apps need no request URL). The bot must be a member of the channel to post the poll; otherwise the creator gets an ephemeral "invite me" message.
