# Polls

Two unrelated features are documented here. Pick the one you mean:

- **The weekly music poll** ("cat jam") — posted on a schedule, picks up song links people post in
  reply. This is the one with tables, background jobs, and a diagnostic procedure. See
  [The weekly music poll](#the-weekly-music-poll) below.
- **`/wpoll` ad-hoc polls** — Simple Poll-style polls a person creates with a slash command, with
  vote buttons. See [`/wpoll` — ad-hoc polls](#wpoll--ad-hoc-polls) at the bottom.

If you are here because a deploy went wrong rather than because poll rows look odd, the incident
procedure is in [deploy_recovery.md](deploy_recovery.md) — including why `helm rollback` after the
manman V1 removal makes things worse rather than better, and the two ways to actually recover.

---

# The weekly music poll

Every week the bot posts a "any cat jammers?" message into a configured channel, and people reply
with song links. A background job sweeps those replies up and records one `musicpollresponse` row per
link. This section is written for the operator looking at the tables — including the case that looks
like breakage and is not.

## How it works

Three scheduled tasks in the task pool (`bot/task/musicpoll.py`) do the work:

| Task | Cadence | What it does |
|---|---|---|
| `MusicPollPostPoll` | every 7 days | Posts "any cat jammers?" to each configured channel and inserts a `musicpollinstance` row, then backfills that instance's `next_instance_id`. |
| `MusicPollProcessPoll` | every hour | Finds eligible instances and writes one `musicpollresponse` row per URL found in the messages inside each instance's window. |
| `MusicPollArchiveMessages` | daily | Pulls the channel's Slack history into `slackmessage` (89-day lookback, bounded by Slack's 90-day free-tier limit). |

Tables (schema `fcm`):

| Table | Purpose |
|---|---|
| `musicpoll` | The poll definition: `slack_channel_id`, `start_date`, `name`. Rows come from the bot's `music_poll_infos` config, not from a seeder. |
| `musicpollinstance` | One posted week: `music_poll_id`, `slack_message_id` of the "any cat jammers?" message, `created_at`, and `next_instance_id` pointing at the *following* week. |
| `musicpollresponse` | One row per URL found: `music_poll_instance_id`, `slack_user_id`, `slack_message_id`, `url`. |

The pickup window for an instance is the half-open interval
`[instance.created_at, successor.created_at)` over `slackmessage.ts`, in the instance's channel. Two
consequences follow, and they are the whole diagnostic:

```sql
-- db/dal/slack_dal.py: find_poll_instance_messages
slackmessage.ts >= poll_instance.created_at
AND slackmessage.ts <  (SELECT created_at FROM musicpollinstance WHERE id = poll_instance.next_instance_id)
```

```sql
-- db/dal/music_poll_dal.py: get_unprocessed_music_poll_instances
musicpollinstance.next_instance_id IS NOT NULL
AND NOT EXISTS (musicpollresponse for this instance)
```

## Diagnosing it: start here

**The pickup is healthy.** The chain is running — the post job posts each week, the archive job
stores the channel, the process job runs hourly. The job is not failing and has not been failing. If
you are reading this because responses look absent, keep reading before you conclude anything is
broken.

### The current week is open, and that is correct

**A poll week legitimately produces no response rows until its successor instance is created.**
Eligibility requires a non-NULL `next_instance_id`, and it cannot be non-NULL until the *next* week's
instance exists — because the window's upper bound **is** the successor's `created_at`. There is no
upper bound until then, so there is nothing to process, so there are no response rows. The most
recent instance's `next_instance_id` is still `NULL`, and that is the correct state, not a stall.

**Therefore the most recent N link messages may be sitting stored and unprocessed at any moment, and
that is correct.** Here `N` means the link messages posted since the latest *any cat jammers?*
message. The archive job stores them in `slackmessage` on its own cadence; nothing turns them into
`musicpollresponse` rows until the week closes. Stored-but-unprocessed link messages in the current
window are the expected steady state, not a fault. Reading them as breakage is the single most common
wrong conclusion here.

### When it *is* stuck

**A genuinely stuck poll is distinguishable because it persists past the successor.** Once the
successor instance exists, `next_instance_id` is populated and the process job has a real window to
sweep. At that point, an instance with stored link messages inside its window and no response rows
for it is the actual fault signal. The open window is not.

Check which case you are in:

```sql
-- Still open? (next_instance_id IS NULL) => no responses expected. Nothing is wrong.
SELECT mi.id, mi.created_at, mi.next_instance_id,
       (SELECT count(*) FROM fcm.musicpollresponse r
         WHERE r.music_poll_instance_id = mi.id) AS responses
  FROM fcm.musicpollinstance mi
 WHERE mi.next_instance_id IS NULL
 ORDER BY mi.created_at DESC;

-- Closed but empty? Only THIS is the fault signal: successor exists, messages in the window,
-- and no response rows.
SELECT mi.id,
       mi.created_at                AS window_starts,
       succ.created_at              AS window_ends,
       count(m.id)                  AS messages_in_window
  FROM fcm.musicpollinstance mi
  JOIN fcm.musicpollinstance succ ON succ.id = mi.next_instance_id
  LEFT JOIN fcm.slackmessage m
         ON m.slack_channel_id = (SELECT slack_channel_id FROM fcm.musicpoll WHERE id = mi.music_poll_id)
        AND m.ts >= mi.created_at
        AND m.ts <  succ.created_at
 WHERE NOT EXISTS (SELECT 1 FROM fcm.musicpollresponse r
                    WHERE r.music_poll_instance_id = mi.id)
 GROUP BY mi.id, mi.created_at, succ.created_at
 ORDER BY mi.created_at DESC;
```

If the first query returns a row and the second does not, you are looking at a healthy open week.

## The timezone hazard — a real latent bug, and a different problem from the above

The window comparison straddles two columns that do not agree about timezones:

- **`slackmessage.ts` holds no timezone.** It is a `timestamp without time zone` (see migration
  `92d6ff362a93`). The value written into it comes from `datetime.datetime.fromtimestamp(float(ts))`
  at `util.py:14` — a **naive** datetime in the *app container's local time*.
- **`musicpollinstance.created_at` is timezone-aware.** It is a `timestamptz`, and the value written
  into it comes from a **naive** `datetime.datetime.now()` at `models/music_poll.py:29`.

Postgres resolves a `timestamp without time zone` against a `timestamptz` using the **session's**
`TimeZone` setting. So the window comparison is correct only for as long as the app container's
timezone equals the database session's. Nothing enforces that, and nothing warns about it.

**Changing the pod's `TZ` — an ordinary, entirely reasonable operator action — silently breaks
pickup.** The naive values shift by the new offset, the window stops matching the messages that are
actually in it, and votes stop being recorded. The archival job is unaffected, so the link messages
still visibly land in `slackmessage`; they just never become `musicpollresponse` rows.

**The symptom to recognise: votes silently not appearing, while link messages are visibly stored.**
Read on its own, that is indistinguishable from a healthy open week. That ambiguity is exactly why the
successor rule above has to be read first — check the `next_instance_id` queries before concluding
anything from absent responses.

**This hazard is not the explanation for the current state.** The open window is the complete
explanation for why the latest week has no response rows, and no evidence ties the timezone
representation to it. Treat the two as independent: diagnose with the successor rule, and treat a
`TZ` change as its own risk to avoid rather than a cause to look for.

To confirm the two sides currently agree:

```sql
SHOW TimeZone;                 -- database session timezone
```

and, in the app pod:

```bash
date                            # container local time
```

If either command has a `TZ` set that the other does not, pickup is already broken or about to be.

**The fix is deliberately not done here.** Making this correct is a data-representation decision —
store one side as UTC-aware, or the other as naive — with a backfill implication for every
`slackmessage.ts` row already written. That is out of scope for this milestone's engineering, and
documenting it does not discharge it. Until it is fixed, the operational rule is: **leave `TZ` unset
on the FCM pods, and keep the database session timezone consistent with the container's.**

---

# `/wpoll` — ad-hoc polls

Simple Poll-style polls created with a slash command. Delivers capability C12 (see [product/02-capability-map.md](../product/02-capability-map.md)).

## Usage

Type `/wpoll` with nothing after it to open the **Create a poll** form: a question, option fields (3 to start, **+ Add another option** up to 10), an **Anonymous votes** checkbox, and a **Votes per person** picker. Mistakes are shown next to the field; if the bot isn't in the channel, the form stays open with an "invite me" error.

For quick polls, the one-line form also works:

```
/wpoll "Question?" "Option 1" "Option 2" [anonymous] [limit N]
```

- The question and each option go in double quotes (curly quotes from Slack clients also work). 2–10 options.
- `anonymous` — show counts only, not who voted.
- `limit N` — each person gets at most `N` active votes. With `limit 1`, clicking another option moves the vote; with `N > 1`, an extra click is rejected with an ephemeral message.
- Clicking an option you already voted for removes that vote.
- **Close poll** (creator only) freezes the results and removes the vote buttons.

Bad input is answered with an ephemeral usage message; nothing is stored.

## How it works

Everything runs in the `bot` deployment over the existing bolt Socket Mode connection — no new service, no HTTP endpoint.

1. A bare `/wpoll` opens the modal (`bot/poll/modal.py`); **+ Add another option** re-renders it with `views.update` (unchanged `block_id`s keep typed values). On submit, the form is validated and the poll is posted *before* the modal is acked, so a posting failure can be shown in the form.
2. `/wpoll <text>` (`bot/handlers/poll.py`) parses the text (`bot/poll/parse.py`), records the command in `slackcommand`, inserts the poll and options, posts the Block Kit message (`bot/poll/render.py`), then stores the message `ts` on the poll.
3. A vote button click arrives as a `block_actions` event on the socket. `cast_poll_vote` (`db/dal/poll_dal.py`) locks the poll row, applies the toggle/limit rules, and returns a fresh snapshot; the handler re-renders the message with `chat.update`.
4. A per-poll in-process lock keeps vote → render → `chat.update` ordered so a stale render never overwrites a newer one (the bot runs as a single replica).

## Data (schema `fcm`)

| Table | Purpose |
|---|---|
| `poll` | Question, channel, creator, message `ts`, `anonymous`, `vote_limit` (NULL = unlimited), `created_at`, `closed_at` |
| `polloption` | Options in display order (`position`, unique per poll) |
| `pollvote` | Every vote ever cast. Un-voting stamps `removed_at` instead of deleting, so full vote history is kept. Current votes: `removed_at IS NULL`; a partial unique index allows one active vote per person per option |

Slack users and channels are stored by Slack ID, not as FKs to `slackuser`/`slackchannel`, because those are filled by a periodic sync and may not yet contain a new voter.

## Slack app setup

The Slack app config must include a `/wpoll` slash command and have Interactivity enabled (needed for buttons and the modal) (Socket Mode apps need no request URL). The bot must be a member of the channel to post the poll; otherwise the creator gets an ephemeral "invite me" message.
