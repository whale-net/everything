#!/usr/bin/env python3
"""LeafLab serial monitor daemon.

Runs as a detached background process. Opens the configured serial port
non-exclusively, buffers all output to a shared log file, and reconnects
automatically after port loss (e.g. esptool flashing).

State directory: ~/.local/share/serial-mcp/
  output.log      — raw serial data only; never contains status messages
  output.log.old  — previous output.log after rotation (> 500 KB)
  daemon.log      — state-change lines only: connected / unavailable
  daemon.log.old  — previous daemon.log after rotation (> 100 KB)
  daemon.pid      — this process's PID (removed on clean exit)

Guarantees:
  - output.log is never written to when the port is unavailable.
  - daemon.log receives at most one line per state transition (not per retry).
  - Both logs are capped: output.log ≤ 1 MB total, daemon.log ≤ 200 KB total.
"""

import argparse
import os
import signal
import sys
import time
from pathlib import Path

import serial

# ── State directory ───────────────────────────────────────────────────────────

STATE_DIR = Path.home() / ".local" / "share" / "serial-mcp"
OUTPUT_LOG = STATE_DIR / "output.log"
DAEMON_LOG = STATE_DIR / "daemon.log"
PID_FILE = STATE_DIR / "daemon.pid"

OUTPUT_MAX_BYTES = 500 * 1024    # rotate output.log at 500 KB
DAEMON_LOG_MAX_BYTES = 100 * 1024  # rotate daemon.log at 100 KB
RETRY_INTERVAL_S = 5             # seconds between reconnect attempts


# ── Helpers ───────────────────────────────────────────────────────────────────

def _rotate(path: Path, max_bytes: int) -> None:
    """Rename path → path.old when it exceeds max_bytes, capping total disk use."""
    if path.exists() and path.stat().st_size >= max_bytes:
        path.rename(path.with_suffix(path.suffix + ".old"))


def _log_state(msg: str) -> None:
    """Append a single timestamped state-change line to daemon.log."""
    _rotate(DAEMON_LOG, DAEMON_LOG_MAX_BYTES)
    ts = time.strftime("%Y-%m-%dT%H:%M:%S")
    with DAEMON_LOG.open("a") as f:
        f.write(f"{ts} {msg}\n")
        f.flush()


def _cleanup(signum=None, frame=None) -> None:
    PID_FILE.unlink(missing_ok=True)
    sys.exit(0)


# ── Main loop ─────────────────────────────────────────────────────────────────

def run(port: str, baud: int) -> None:
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    PID_FILE.write_text(str(os.getpid()))

    signal.signal(signal.SIGTERM, _cleanup)
    signal.signal(signal.SIGINT, _cleanup)

    # Track the last logged state so we emit at most one line per transition.
    last_state: str | None = None

    while True:
        try:
            # exclusive=False lets esptool open the port concurrently.
            # When esptool resets the chip via DTR/RTS, our readline() will
            # raise SerialException — we catch it and reconnect below.
            with serial.Serial(port, baud, exclusive=False, timeout=1) as ser:
                if last_state != "connected":
                    _log_state(f"connected to {port} at {baud} baud")
                    last_state = "connected"

                while True:
                    line = ser.readline()
                    if not line:
                        # Timeout with no data — port still open, keep polling.
                        continue
                    _rotate(OUTPUT_LOG, OUTPUT_MAX_BYTES)
                    with OUTPUT_LOG.open("ab") as f:
                        f.write(line)
                        f.flush()

        except (serial.SerialException, OSError):
            if last_state != "unavailable":
                _log_state(
                    f"port {port} unavailable or disconnected, "
                    f"retrying every {RETRY_INTERVAL_S}s"
                )
                last_state = "unavailable"
            time.sleep(RETRY_INTERVAL_S)


# ── Entry point ───────────────────────────────────────────────────────────────

def main() -> None:
    p = argparse.ArgumentParser(description="LeafLab serial monitor daemon")
    p.add_argument("--port", default="/dev/ttyUSB0", help="Serial port (default: /dev/ttyUSB0)")
    p.add_argument("--baud", type=int, default=115200, help="Baud rate (default: 115200)")
    args = p.parse_args()
    run(args.port, args.baud)


if __name__ == "__main__":
    main()
