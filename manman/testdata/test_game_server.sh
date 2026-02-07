#!/bin/sh
# Test game server script for wrapper integration tests
# Simulates a game server that:
# - Prints predictable output to stdout and stderr
# - Accepts stdin commands (stop, crash, ping)
# - Handles SIGTERM for graceful shutdown

set -e

echo "[STDOUT] Test game server starting..." >&1
echo "[STDERR] Logging to stderr" >&2

# Track if we should shutdown gracefully
SHUTDOWN=false

# Trap SIGTERM for graceful shutdown
trap 'echo "[STDOUT] Received SIGTERM, shutting down gracefully..." >&1; SHUTDOWN=true' TERM

# Print startup complete
echo "[STDOUT] Server ready and accepting commands" >&1
echo "[STDOUT] Available commands: stop, crash, ping, echo <message>" >&1

# Counter for heartbeat
COUNTER=0

# Main loop - read from stdin and print heartbeats
while [ "$SHUTDOWN" = false ]; do
    # Print heartbeat every 2 seconds (non-blocking)
    if [ $((COUNTER % 20)) -eq 0 ]; then
        echo "[STDOUT] Heartbeat $((COUNTER / 20))" >&1
    fi

    # Try to read a command from stdin (with timeout)
    # Use read with timeout to allow heartbeats
    if read -t 0.1 COMMAND 2>/dev/null; then
        case "$COMMAND" in
            stop)
                echo "[STDOUT] Received stop command, shutting down gracefully..." >&1
                echo "[STDERR] Shutdown initiated via stdin" >&2
                SHUTDOWN=true
                ;;
            crash)
                echo "[STDERR] CRASH command received! Simulating crash..." >&2
                exit 42
                ;;
            ping)
                echo "[STDOUT] pong" >&1
                ;;
            echo\ *)
                # Echo back the message
                MSG="${COMMAND#echo }"
                echo "[STDOUT] Echo: $MSG" >&1
                ;;
            "")
                # Empty line, ignore
                ;;
            *)
                echo "[STDERR] Unknown command: $COMMAND" >&2
                ;;
        esac
    fi

    COUNTER=$((COUNTER + 1))
    sleep 0.1
done

echo "[STDOUT] Server stopped cleanly" >&1
exit 0
