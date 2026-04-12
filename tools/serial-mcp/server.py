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


def _ensure_daemon(port: str = DEFAULT_PORT, baud: int = DEFAULT_BAUD) -> None:
    """Start the daemon if it is not already running.

    Called at the top of every tool so any Claude session automatically gets
    a running daemon without manual intervention.
    """
    STATE_DIR.mkdir(parents=True, exist_ok=True)

    if PID_FILE.exists():
        try:
            pid = int(PID_FILE.read_text().strip())
            os.kill(pid, 0)  # signal 0 = existence check, no-op if alive
            return  # daemon is running
        except (ProcessLookupError, ValueError):
            PID_FILE.unlink(missing_ok=True)  # stale PID

    proc = subprocess.Popen(
        [str(_daemon_path()), "--port", port, "--baud", str(baud)],
        start_new_session=True,   # detach from MCP server's process group
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    # Allow time for the daemon to write its PID file.
    time.sleep(0.4)
    if not PID_FILE.exists():
        # Daemon started but hasn't written the file yet — record PID ourselves.
        PID_FILE.write_text(str(proc.pid))


# ── Log helpers ───────────────────────────────────────────────────────────────

def _read_lines(path: Path) -> list[str]:
    if not path.exists():
        return []
    return path.read_bytes().decode("utf-8", errors="replace").splitlines()


# ── MCP tools ─────────────────────────────────────────────────────────────────

@mcp.tool()
def serial_tail(lines: int = 50) -> dict[str, Any]:
    """Return the last N lines from the serial output buffer.

    Starts the daemon automatically if not running.

    Args:
        lines: Number of lines to return (default 50, capped at 500).

    Returns:
        dict with 'lines' list, 'returned' count, and 'total_buffered' count.
    """
    _ensure_daemon()
    lines = min(max(lines, 1), 500)
    all_lines = _read_lines(OUTPUT_LOG)
    tail = all_lines[-lines:]
    return {
        "lines": tail,
        "returned": len(tail),
        "total_buffered": len(all_lines),
    }


@mcp.tool()
def serial_grep(pattern: str, max_lines: int = 200) -> dict[str, Any]:
    """Search serial output with a regex pattern across live and rotated logs.

    Searches output.log.old first (older), then output.log (newer), so
    results are in chronological order. Returns the most recent matches
    when truncation occurs.

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
        last_serial_line, and last_state_change.
    """
    _ensure_daemon()

    daemon_alive = False
    daemon_pid: int | None = None
    if PID_FILE.exists():
        try:
            pid = int(PID_FILE.read_text().strip())
            os.kill(pid, 0)
            daemon_alive = True
            daemon_pid = pid
        except (ProcessLookupError, ValueError):
            pass

    output_size = OUTPUT_LOG.stat().st_size if OUTPUT_LOG.exists() else 0

    last_serial_line: str | None = None
    output_lines = _read_lines(OUTPUT_LOG)
    if output_lines:
        last_serial_line = output_lines[-1]

    last_state_change: str | None = None
    daemon_lines = _read_lines(DAEMON_LOG)
    if daemon_lines:
        last_state_change = daemon_lines[-1]

    return {
        "daemon_alive": daemon_alive,
        "daemon_pid": daemon_pid,
        "output_log_bytes": output_size,
        "last_serial_line": last_serial_line,
        "last_state_change": last_state_change,
    }


# ── Entry point ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    mcp.run(transport="stdio")
