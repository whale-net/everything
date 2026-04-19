#!/usr/bin/env python3
"""LeafLab Serial MCP Server.

Exposes ESP32 serial output to Claude Code via the Model Context Protocol.
On first tool call, automatically starts the serial daemon if it is not
already running — no manual setup required between sessions.

The daemon binary path is resolved from the SERIAL_DAEMON_PATH environment
variable (set by the Bazel sh_binary wrapper) with a fallback to a sibling
named 'serial-daemon' for local development.

State directory (shared with daemon): ~/.local/share/serial-mcp/
  output.log      — live serial data
  output.log.old  — previous log after rotation
  daemon.pid      — daemon process ID
  daemon.log      — daemon state-change events
"""

import os
import re
import signal
import subprocess
import time
from pathlib import Path
from typing import Any

from fastmcp import FastMCP

# ── State paths ───────────────────────────────────────────────────────────────

STATE_DIR = Path.home() / ".local" / "share" / "serial-mcp"
OUTPUT_LOG = STATE_DIR / "output.log"
OUTPUT_LOG_OLD = STATE_DIR / "output.log.old"
DAEMON_LOG = STATE_DIR / "daemon.log"
PID_FILE = STATE_DIR / "daemon.pid"

DEFAULT_PORT = "/dev/ttyUSB0"
DEFAULT_BAUD = 115200

# ── MCP server ────────────────────────────────────────────────────────────────

mcp = FastMCP("serial-mcp")

# ── ANSI / binary filtering ───────────────────────────────────────────────────

_ANSI_RE = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


def _clean(line: str) -> str | None:
    """Strip ANSI codes; return None if the line is binary garbage."""
    cleaned = _ANSI_RE.sub("", line).strip()
    if not cleaned:
        return None
    # Discard lines that are mostly non-printable (boot ROM at wrong baud rate).
    printable = sum(1 for c in cleaned if 0x20 <= ord(c) <= 0x7E or c in "\t")
    if len(cleaned) > 0 and printable / len(cleaned) < 0.7:
        return None
    return cleaned


# ── Daemon management ─────────────────────────────────────────────────────────

def _daemon_path() -> Path:
    """Resolve the serial-daemon binary.

    Prefers the SERIAL_DAEMON_PATH env var set by the Bazel wrapper.
    Falls back to a sibling named 'serial-daemon' for local runs.
    """
    env = os.environ.get("SERIAL_DAEMON_PATH")
    if env:
        return Path(env)
    candidate = Path(__file__).parent / "serial-daemon"
    if candidate.exists():
        return candidate
    raise RuntimeError(
        "serial-daemon binary not found. "
        "Set SERIAL_DAEMON_PATH or run via 'bazel run //tools/serial-mcp:serial-mcp-server'."
    )


def _daemon_pid() -> int | None:
    """Return the daemon PID if it is alive, else None."""
    if not PID_FILE.exists():
        return None
    try:
        pid = int(PID_FILE.read_text().strip())
        os.kill(pid, 0)  # existence check
        return pid
    except (ValueError, ProcessLookupError, OSError):
        PID_FILE.unlink(missing_ok=True)
        return None


def _ensure_daemon(port: str = DEFAULT_PORT, baud: int = DEFAULT_BAUD) -> None:
    """Start the daemon if it is not already running.

    Called at the top of every tool so any Claude session automatically gets
    a running daemon without manual intervention.
    """
    STATE_DIR.mkdir(parents=True, exist_ok=True)

    if _daemon_pid() is not None:
        return  # already running

    proc = subprocess.Popen(
        [str(_daemon_path()), "--port", port, "--baud", str(baud)],
        start_new_session=True,   # detach from MCP server's process group
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    # Allow time for the daemon to write its PID file.
    time.sleep(0.4)
    if not PID_FILE.exists():
        PID_FILE.write_text(str(proc.pid))


# ── Log helpers ───────────────────────────────────────────────────────────────

def _read_lines(path: Path) -> list[str]:
    if not path.exists():
        return []
    raw = path.read_bytes().decode("utf-8", errors="replace").splitlines()
    return [c for line in raw if (c := _clean(line)) is not None]


# ── MCP tools ─────────────────────────────────────────────────────────────────

@mcp.tool()
def serial_tail(lines: int = 50) -> dict[str, Any]:
    """Return the last N lines of clean serial output (ANSI stripped, binary filtered).

    Starts the daemon automatically if not running.

    Args:
        lines: Number of lines to return (default 50, capped at 500).

    Returns:
        dict with 'lines' list, 'returned' count, and 'total_lines' count.
    """
    _ensure_daemon()
    lines = min(max(lines, 1), 500)
    all_lines = _read_lines(OUTPUT_LOG)
    tail = all_lines[-lines:]
    return {
        "lines": tail,
        "returned": len(tail),
        "total_lines": len(all_lines),
    }


@mcp.tool()
def serial_grep(pattern: str, max_lines: int = 200) -> dict[str, Any]:
    """Search serial output with a regex pattern across live and rotated logs.

    Searches output.log.old first (older), then output.log (newer), so
    results are in chronological order. Returns the most recent matches
    when truncation occurs. ANSI codes are stripped and binary lines filtered
    before matching.

    Starts the daemon automatically if not running.

    Args:
        pattern:   Python regex to match against each line.
        max_lines: Maximum matches to return (default 200).

    Returns:
        dict with 'matches' list (each entry has 'source' and 'line'),
        'count', and 'truncated' flag.
    """
    _ensure_daemon()
    try:
        rx = re.compile(pattern)
    except re.error as e:
        return {"error": f"Invalid regex: {e}", "matches": [], "count": 0}

    matches: list[dict[str, str]] = []
    for source, path in [("output.log.old", OUTPUT_LOG_OLD), ("output.log", OUTPUT_LOG)]:
        for line in _read_lines(path):
            if rx.search(line):
                matches.append({"source": source, "line": line})

    truncated = len(matches) > max_lines
    if truncated:
        matches = matches[-max_lines:]  # keep most recent

    return {
        "pattern": pattern,
        "matches": matches,
        "count": len(matches),
        "truncated": truncated,
    }


@mcp.tool()
def serial_status() -> dict[str, Any]:
    """Return daemon health and log statistics.

    Starts the daemon automatically if not running.

    Returns:
        dict with daemon_alive, daemon_pid, output_log_bytes,
        last_serial_line (clean text), and last_state_change.
    """
    _ensure_daemon()

    pid = _daemon_pid()
    daemon_alive = pid is not None

    output_size = OUTPUT_LOG.stat().st_size if OUTPUT_LOG.exists() else 0

    last_serial_line: str | None = None
    output_lines = _read_lines(OUTPUT_LOG)
    if output_lines:
        last_serial_line = output_lines[-1]

    last_state_change: str | None = None
    if DAEMON_LOG.exists():
        daemon_lines = DAEMON_LOG.read_text().splitlines()
        if daemon_lines:
            last_state_change = daemon_lines[-1]

    return {
        "daemon_alive": daemon_alive,
        "daemon_pid": pid,
        "output_log_bytes": output_size,
        "last_serial_line": last_serial_line,
        "last_state_change": last_state_change,
    }


@mcp.tool()
def serial_pause() -> dict[str, Any]:
    """Stop the serial daemon so esptool (or any other tool) can claim the port.

    Sends SIGTERM to the daemon and waits until it has exited and released
    the port (up to 5 s). Call serial_resume() after flashing to restart it.

    Returns:
        dict with 'paused' (bool) and 'message'.
    """
    pid = _daemon_pid()
    if pid is None:
        return {"paused": True, "message": "daemon was not running"}

    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        PID_FILE.unlink(missing_ok=True)
        return {"paused": True, "message": "daemon already exited"}

    # Wait for the daemon to remove its PID file (it does so in _cleanup).
    deadline = time.monotonic() + 5.0
    while time.monotonic() < deadline:
        if _daemon_pid() is None:
            return {"paused": True, "message": f"daemon (pid {pid}) stopped"}
        time.sleep(0.1)

    # Force-remove stale PID file if daemon didn't exit cleanly.
    PID_FILE.unlink(missing_ok=True)
    return {"paused": True, "message": f"daemon (pid {pid}) killed (did not exit within 5 s)"}


@mcp.tool()
def serial_resume() -> dict[str, Any]:
    """Restart the serial daemon after a serial_pause() call.

    Safe to call even if the daemon is already running — it will not start
    a second instance.

    Returns:
        dict with 'running' (bool), 'daemon_pid', and 'message'.
    """
    _ensure_daemon()
    pid = _daemon_pid()
    if pid is not None:
        return {"running": True, "daemon_pid": pid, "message": "daemon running"}
    return {"running": False, "daemon_pid": None, "message": "daemon failed to start"}


# ── Entry point ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    mcp.run(transport="stdio")
