# agentsync-mcp

MCP server for cross-agent-session communication: lets one Claude Code
session (a "parent") start a shared session and hand its id to one or more
other sessions ("children"), which join and then rendezvous with `sync()` —
send a message and block until a peer replies.

## How it works

```
Parent session            agentsync-mcp             Child session
     │                          │                          │
     ├─ start_session() ───────►│                          │
     │◄──────── session_id ─────┤                          │
     │                          │◄──── join_session(sid) ──┤
     ├─ join_session(sid) ─────►│                          │
     │                          │                          │
     ├─ sync(sid, "go") ───────►│──── delivered ──────────►│  (child was
     │   (blocks)               │                          │   polling/
     │                          │◄──── sync(sid, "done") ──┤   blocked here)
     │◄──── {"message":"done"} ─┤                          │  (now blocks)
```

There is **no background daemon**. Each Claude Code session's MCP server
process (spawned fresh over stdio) talks directly to one shared JSON file
per session under `~/.local/share/agentsync-mcp/sessions/`, guarded by an
`flock`. `sync()` posts its outgoing message under the lock, then polls its
own mailbox every ~0.4s (never holding the lock while it sleeps) until a
reply arrives or a terminal condition — peer left, session ended, or
timeout — is reached. This is the whole mechanism: no sockets, no extra
process, no new dependency beyond `fastmcp` (already vendored).

## Installation

```bash
./tools/agentsync-mcp/install-mcp.sh
claude mcp list   # verify
```

## Available tools

### `start_session(session_id=None)`

Creates a session. **Does not join the caller** — a parent typically calls
this, then tells each child the returned `session_id` so they can
`join_session` themselves. Errors if `session_id` already exists.

### `join_session(session_id, participant_id)`

Joins (or re-joins after `leave_session`) as `participant_id`. Returns the
currently-active participant list.

### `session_status(session_id, slim=False)`

Reports membership. `slim=True` returns just `{"participants": [ids]}` —
cheap enough to poll in a loop while waiting for a peer to show up.
`slim=False` (default) adds timestamps and per-participant pending-message
counts.

### `sync(session_id, participant_id, message, timeout_s=300)`

Delivers `message` to every other currently-active participant, then blocks
until:

- a peer calls `sync()` too → returns `{"status": "message", "from", "message", "at"}`
- every other participant has left → `{"status": "peer_left"}`
- the session was ended → `{"status": "session_ended"}`
- `timeout_s` elapses (capped at 1800) → `{"status": "timeout"}`

Two agents ping-ponging `sync()` calls take turns sleeping: whichever one
is waiting wakes the instant the other calls `sync()`.

### `leave_session(session_id, participant_id)`

Call this when an agent is wrapping up its context. Wakes any peer blocked
in `sync()` (within one poll interval) with `"peer_left"` instead of
leaving it to wait out its full timeout.

### `end_session(session_id)`

Ends the session outright. Wakes any blocked `sync()` calls with
`"session_ended"`. Ended sessions are garbage-collected automatically ~24h
later.

## Example: parent + one child, one round-trip

```
# Parent
start_session("demo")
# (tell the child session: join "demo" as "child")
join_session("demo", "parent")
sync("demo", "parent", "please summarize file X")   # blocks

# Child (separate Claude Code session)
join_session("demo", "child")
sync("demo", "child", "on it")                       # wakes parent's sync,
                                                       # then blocks itself
# ... child does the work, then:
sync("demo", "child", "done: summary is ...")         # wakes parent

# Parent's earlier sync() call returns:
# {"status": "message", "from": "child", "message": "on it", ...}
# and the parent's next sync() call picks up "done: summary is ..."
```

## Limitations

- Local machine only — participants must share the same
  `~/.local/share/agentsync-mcp` (or `AGENTSYNC_STATE_DIR`), i.e. run on the
  same host.
- Polling-based, not a true blocking wait — replies land within one
  `POLL_INTERVAL_S` (~0.4s), not instantly.
- No message history beyond the unread mailbox — `sync()` consumes exactly
  one message per call.
