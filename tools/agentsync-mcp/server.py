#!/usr/bin/env python3
"""Agent-to-agent session orchestration MCP server.

Lets independent Claude Code sessions rendezvous: one agent starts a
session, tells its children the session id, the children join, and then
any participant can call sync() to send a message and block until a peer
replies (or leaves, or the session ends).

There is no background daemon — each MCP server process (one per Claude
Code session, spawned over stdio) talks directly to a shared state file per
session, guarded by an flock. sync() is implemented as post-then-poll: it
writes its outgoing message, then polls its own mailbox at a short interval
until a reply arrives or a terminal condition (peer left / session ended /
timeout) is reached. This keeps the whole thing to one file, no RPC.

State directory: ~/.local/share/agentsync-mcp/sessions/
  <session_id>.json  — participants + per-participant mailboxes
  <session_id>.lock   — flock guarding read-modify-write of the .json file

Override the state directory with AGENTSYNC_STATE_DIR (used by tests).

Each public tool below is a thin `@mcp.tool()` wrapper around a private
`_`-prefixed function, so tests can import the plain functions directly.
"""

import fcntl
import json
import os
import re
import time
import uuid
from pathlib import Path
from typing import Any, Callable

from fastmcp import FastMCP

# ── State paths ───────────────────────────────────────────────────────────────

_ID_RE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")

DEFAULT_SYNC_TIMEOUT_S = 300
MAX_SYNC_TIMEOUT_S = 1800
POLL_INTERVAL_S = 0.4
SESSION_GC_AGE_S = 24 * 60 * 60  # delete ended sessions older than this

mcp = FastMCP("agentsync-mcp")


def _state_dir() -> Path:
    override = os.environ.get("AGENTSYNC_STATE_DIR")
    base = Path(override) if override else Path.home() / ".local" / "share" / "agentsync-mcp"
    sessions = base / "sessions"
    sessions.mkdir(parents=True, exist_ok=True)
    return sessions


def _paths(session_id: str) -> tuple[Path, Path]:
    d = _state_dir()
    return d / f"{session_id}.json", d / f"{session_id}.lock"


def _validate_id(value: str, label: str) -> None:
    if not _ID_RE.match(value):
        raise ValueError(f"{label} must match [A-Za-z0-9_-]{{1,64}}, got {value!r}")


# ── Locked read-modify-write ─────────────────────────────────────────────────
# mutate(state_or_None) -> (new_state_or_None, result). Runs under an
# exclusive flock covering the read, the mutation, and the write, so
# concurrent MCP server processes never interleave updates to one session.


def _transact(session_id: str, mutate: Callable[[dict | None], tuple[dict | None, Any]]) -> Any:
    data_path, lock_path = _paths(session_id)
    lock_path.touch(exist_ok=True)
    with lock_path.open("r+") as lockf:
        fcntl.flock(lockf, fcntl.LOCK_EX)
        try:
            state = json.loads(data_path.read_text()) if data_path.exists() else None
            new_state, result = mutate(state)
            if new_state is None:
                data_path.unlink(missing_ok=True)
            else:
                tmp = data_path.with_suffix(".json.tmp")
                tmp.write_text(json.dumps(new_state))
                tmp.replace(data_path)
            return result
        finally:
            fcntl.flock(lockf, fcntl.LOCK_UN)


def _new_state(session_id: str) -> dict:
    return {
        "session_id": session_id,
        "created_at": time.time(),
        "ended_at": None,
        "participants": {},  # id -> {"joined_at": ts, "left_at": ts|None}
        "mailboxes": {},  # id -> [{"from": ..., "message": ..., "at": ts}, ...]
    }


def _active_participants(state: dict) -> list[str]:
    return [pid for pid, info in state["participants"].items() if info["left_at"] is None]


def _require(state: dict | None, session_id: str) -> dict:
    if state is None:
        raise ValueError(f"session '{session_id}' does not exist")
    return state


def _gc_old_sessions() -> None:
    """Best-effort cleanup of long-ended sessions so the directory doesn't grow forever."""
    now = time.time()
    for data_path in _state_dir().glob("*.json"):
        try:
            state = json.loads(data_path.read_text())
        except (OSError, json.JSONDecodeError):
            continue
        ended_at = state.get("ended_at")
        if ended_at is not None and now - ended_at > SESSION_GC_AGE_S:
            data_path.unlink(missing_ok=True)
            data_path.with_suffix(".lock").unlink(missing_ok=True)


# ── Core implementations (importable directly by tests) ─────────────────────


def _start_session(session_id: str | None = None) -> dict[str, Any]:
    sid = session_id or uuid.uuid4().hex[:8]
    _validate_id(sid, "session_id")

    def mutate(state: dict | None) -> tuple[dict, dict]:
        if state is not None:
            raise ValueError(f"session '{sid}' already exists")
        new_state = _new_state(sid)
        return new_state, new_state

    result = _transact(sid, mutate)
    _gc_old_sessions()
    return {"session_id": result["session_id"], "created_at": result["created_at"]}


def _join_session(session_id: str, participant_id: str) -> dict[str, Any]:
    _validate_id(session_id, "session_id")
    _validate_id(participant_id, "participant_id")

    def mutate(state: dict | None) -> tuple[dict, dict]:
        state = _require(state, session_id)
        if state["ended_at"] is not None:
            raise ValueError(f"session '{session_id}' has already ended")
        now = time.time()
        state["participants"][participant_id] = {"joined_at": now, "left_at": None}
        state["mailboxes"].setdefault(participant_id, [])
        return state, state

    state = _transact(session_id, mutate)
    return {
        "session_id": session_id,
        "participant_id": participant_id,
        "participants": _active_participants(state),
    }


def _leave_session(session_id: str, participant_id: str) -> dict[str, Any]:
    _validate_id(session_id, "session_id")

    def mutate(state: dict | None) -> tuple[dict, dict]:
        state = _require(state, session_id)
        info = state["participants"].get(participant_id)
        if info is None:
            raise ValueError(f"'{participant_id}' never joined session '{session_id}'")
        info["left_at"] = time.time()
        return state, state

    state = _transact(session_id, mutate)
    return {
        "session_id": session_id,
        "participant_id": participant_id,
        "left": True,
        "remaining_participants": _active_participants(state),
    }


def _end_session(session_id: str) -> dict[str, Any]:
    _validate_id(session_id, "session_id")

    def mutate(state: dict | None) -> tuple[dict, dict]:
        state = _require(state, session_id)
        state["ended_at"] = time.time()
        return state, state

    _transact(session_id, mutate)
    return {"session_id": session_id, "ended": True}


def _session_status(session_id: str, slim: bool = False) -> dict[str, Any]:
    _validate_id(session_id, "session_id")

    def mutate(state: dict | None) -> tuple[dict | None, dict]:
        state = _require(state, session_id)
        return state, state

    state = _transact(session_id, mutate)
    if slim:
        return {"session_id": session_id, "participants": _active_participants(state)}
    return {
        "session_id": session_id,
        "created_at": state["created_at"],
        "ended_at": state["ended_at"],
        "participants": {
            pid: {"joined_at": info["joined_at"], "left_at": info["left_at"]}
            for pid, info in state["participants"].items()
        },
        "pending_messages": {pid: len(msgs) for pid, msgs in state["mailboxes"].items()},
    }


def _sync(session_id: str, participant_id: str, message: str, timeout_s: int = DEFAULT_SYNC_TIMEOUT_S) -> dict[str, Any]:
    _validate_id(session_id, "session_id")
    timeout_s = max(1, min(int(timeout_s), MAX_SYNC_TIMEOUT_S))

    def post(state: dict | None) -> tuple[dict, None]:
        state = _require(state, session_id)
        if state["ended_at"] is not None:
            raise ValueError(f"session '{session_id}' has already ended")
        info = state["participants"].get(participant_id)
        if info is None or info["left_at"] is not None:
            raise ValueError(
                f"'{participant_id}' is not an active participant of '{session_id}' "
                "— call join_session first"
            )
        entry = {"from": participant_id, "message": message, "at": time.time()}
        for pid, pinfo in state["participants"].items():
            if pid != participant_id and pinfo["left_at"] is None:
                state["mailboxes"].setdefault(pid, []).append(entry)
        return state, None

    _transact(session_id, post)

    deadline = time.monotonic() + timeout_s
    while True:

        def poll(state: dict | None) -> tuple[dict | None, dict | None]:
            if state is None:
                return state, {"status": "session_ended"}
            if state["ended_at"] is not None:
                return state, {"status": "session_ended"}
            mailbox = state["mailboxes"].get(participant_id, [])
            if mailbox:
                incoming = mailbox.pop(0)
                return state, {
                    "status": "message",
                    "from": incoming["from"],
                    "message": incoming["message"],
                    "at": incoming["at"],
                }
            others_active = [
                pid
                for pid, pinfo in state["participants"].items()
                if pid != participant_id and pinfo["left_at"] is None
            ]
            if not others_active:
                return state, {"status": "peer_left"}
            return state, None  # nothing yet — keep waiting

        outcome = _transact(session_id, poll)
        if outcome is not None:
            return {"session_id": session_id, **outcome}
        if time.monotonic() >= deadline:
            return {"session_id": session_id, "status": "timeout"}
        time.sleep(POLL_INTERVAL_S)


# ── MCP tools (thin wrappers) ─────────────────────────────────────────────────


@mcp.tool()
def start_session(session_id: str | None = None) -> dict[str, Any]:
    """Create a new session. Does NOT join the caller as a participant.

    Typically called by a parent/orchestrator, which then tells its children
    the returned session_id so they can call join_session themselves.

    Args:
        session_id: Optional explicit id (must match [A-Za-z0-9_-]{1,64}).
            If omitted, a random id is generated. Error if it already exists.

    Returns:
        dict with 'session_id' and 'created_at'.
    """
    return _start_session(session_id)


@mcp.tool()
def join_session(session_id: str, participant_id: str) -> dict[str, Any]:
    """Join an existing session as a participant. Idempotent (re-joining after
    leave_session clears the left_at marker).

    Args:
        session_id: Session to join.
        participant_id: This agent's identifier within the session, unique
            per session (e.g. "parent", "child-a").

    Returns:
        dict with 'session_id', 'participant_id', and 'participants'
        (currently-active participant ids, including this one).
    """
    return _join_session(session_id, participant_id)


@mcp.tool()
def leave_session(session_id: str, participant_id: str) -> dict[str, Any]:
    """Mark a participant as having left. Any peer currently blocked in sync()
    waiting on this participant wakes up (within one poll interval) with
    status "peer_left" instead of waiting for its full timeout.

    Call this when an agent is wrapping up its context, so it doesn't leave
    a sibling hanging.

    Args:
        session_id: Session to leave.
        participant_id: This agent's identifier within the session.

    Returns:
        dict with 'left' (bool) and 'remaining_participants'.
    """
    return _leave_session(session_id, participant_id)


@mcp.tool()
def end_session(session_id: str) -> dict[str, Any]:
    """End a session entirely. Any participant currently blocked in sync()
    wakes up (within one poll interval) with status "session_ended".

    Args:
        session_id: Session to end.

    Returns:
        dict with 'ended' (bool).
    """
    return _end_session(session_id)


@mcp.tool()
def session_status(session_id: str, slim: bool = False) -> dict[str, Any]:
    """Report session membership. Cheap enough to poll in a loop while
    waiting for a peer to join.

    Args:
        session_id: Session to inspect.
        slim: If True, return only currently-active participant ids — the
            minimal payload for tight polling loops. If False (default),
            also include join/leave timestamps, ended_at, and per-participant
            pending-message counts.

    Returns:
        slim=True:  {"session_id", "participants": [ids]}
        slim=False: {"session_id", "created_at", "ended_at", "participants":
                     {id: {"joined_at", "left_at"}}, "pending_messages": {id: count}}
    """
    return _session_status(session_id, slim)


@mcp.tool()
def sync(session_id: str, participant_id: str, message: str, timeout_s: int = DEFAULT_SYNC_TIMEOUT_S) -> dict[str, Any]:
    """Send `message` to the other active participant(s), then block until a
    peer replies via its own sync() call. This is the core rendezvous: two
    agents ping-ponging sync() calls take turns being awake — whichever one
    is waiting sleeps until the other calls sync(), which delivers its
    message and immediately wakes the sleeper.

    Returns as soon as one of these happens:
      - a peer sends a message (status "message") — the most common case
      - every other participant has left (status "peer_left")
      - the session was ended (status "session_ended")
      - timeout_s elapses with none of the above (status "timeout")

    Args:
        session_id: Session to synchronize within.
        participant_id: This agent's identifier (must have already called
            join_session).
        message: Message to deliver to the other active participant(s).
        timeout_s: Max seconds to block (default 300, capped at 1800).

    Returns:
        dict with 'status' and, when status == "message", 'from', 'message',
        and 'at' (the peer's message and when it was sent).
    """
    return _sync(session_id, participant_id, message, timeout_s)


# ── Entry point ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    mcp.run(transport="stdio")
