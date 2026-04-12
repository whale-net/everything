# Serial MCP Server

MCP server that streams ESP32 serial output to Claude Code. A background
daemon holds the serial port and buffers output to a log file; the MCP
server exposes tools to query that buffer. Multiple Claude instances share
one daemon with no coordination required.

## How it works

```
ESP32 ──serial──► daemon (background)
                     │  writes ~/.local/share/serial-mcp/output.log
                     │
                  MCP server  ◄──── Claude Code (any number of sessions)
                  serial_tail / serial_grep / serial_status
```

The daemon starts automatically on the first MCP tool call each session and
stays running until reboot — you never need to start it manually.

When `bazel run //leaflab/sensorboard:flash` runs, esptool interrupts the
daemon's read. The daemon catches the error, waits 5 s, and reconnects —
capturing the full boot log by the time you next call `serial_tail`.

## Installation

```bash
# From repo root — build and register with Claude Code
./tools/serial-mcp/install-mcp.sh

# Verify
claude mcp list
```

### Prerequisites

- Python 3.13+
- Bazel
- `pyserial` and `fastmcp` (already in `uv.lock` — no extra steps)

## Available tools

### `serial_tail(lines=50)`

Returns the last N lines from the live serial buffer.

```json
{
  "lines": ["I (312) sensor: BH1750 ready @ 0x23", "I (318) wifi: connecting..."],
  "returned": 2,
  "total_buffered": 147
}
```

- `lines`: number of lines to return (default 50, max 500)

### `serial_grep(pattern, max_lines=200)`

Regex search across both the live log and the previous rotated log, in
chronological order. Returns the most recent matches when truncated.

```json
{
  "pattern": "ERROR",
  "matches": [
    {"source": "output.log", "line": "E (512) i2c: bus init failed"}
  ],
  "count": 1,
  "truncated": false
}
```

- `pattern`: Python regex
- `max_lines`: cap on returned matches (default 200)

### `serial_status()`

Daemon health and log statistics — useful for diagnosing connection issues.

```json
{
  "daemon_alive": true,
  "daemon_pid": 18423,
  "output_log_bytes": 14209,
  "last_serial_line": "I (1024) mqtt: published lux=312.5",
  "last_state_change": "2026-04-12T10:31:05 connected to /dev/ttyUSB0 at 115200 baud"
}
```

## Log files

All files live in `~/.local/share/serial-mcp/`.

| File | Contents | Rotated at |
|---|---|---|
| `output.log` | Raw serial data only | 500 KB |
| `output.log.old` | Previous `output.log` | — |
| `daemon.log` | State changes only (`connected` / `unavailable`) | 100 KB |
| `daemon.log.old` | Previous `daemon.log` | — |
| `daemon.pid` | Daemon process ID | — |

Maximum total disk use: ~1 MB (`output.log` + `.old`) + ~200 KB (`daemon.log` + `.old`).

## Failure behaviour

| Scenario | Behaviour |
|---|---|
| No board plugged in | `output.log` stays empty; daemon retries silently every 5 s; one line written to `daemon.log` on state change |
| Board disconnected mid-session | Same backoff; reconnects when port reappears |
| esptool flashing | Daemon read interrupted; 5 s backoff; reconnects after flash; captures boot log |
| Daemon killed / machine rebooted | Restarted automatically on next MCP tool call |

## Development

### Project structure

```
tools/serial-mcp/
├── BUILD.bazel          — Bazel targets
├── daemon.py            — Serial reader daemon
├── server.py            — FastMCP MCP server
├── install-mcp.sh       — One-time install script
└── README.md            — This file
```

### Build targets

```bash
bazel build //tools/serial-mcp:serial-daemon        # daemon only
bazel build //tools/serial-mcp:serial-mcp-server    # full server + wrapper
```

### Running the daemon manually (debugging)

```bash
bazel run //tools/serial-mcp:serial-daemon -- --port /dev/ttyUSB0 --baud 115200
tail -f ~/.local/share/serial-mcp/output.log
```

### Changing the default port or baud rate

Edit `_ensure_daemon()` in `server.py` — the `DEFAULT_PORT` and `DEFAULT_BAUD`
constants at the top of the file.

## Troubleshooting

**`serial-daemon binary not found`**

Re-run `install-mcp.sh` to rebuild and re-register. Check that
`bazel-bin/tools/serial-mcp/serial-mcp-server` exists after the build.

**`daemon_alive: false` in `serial_status`**

The next tool call will restart it automatically. If it keeps dying, check
`~/.local/share/serial-mcp/daemon.log` for the last recorded state.

**No output after flashing**

The daemon reconnects within ~5 s of esptool releasing the port. Call
`serial_tail` a few seconds after the flash completes.

**Permission denied on `/dev/ttyUSB0`**

```bash
sudo usermod -aG dialout $USER
# log out and back in
```
