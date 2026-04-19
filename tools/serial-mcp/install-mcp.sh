#!/bin/bash
# Install the LeafLab Serial MCP server to Claude Code.
#
# Builds both the daemon and server via Bazel, then registers the server
# with 'claude mcp add'. Run once; the server auto-starts the daemon on
# first use each session.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "Building serial-mcp..."
cd "$REPO_ROOT"
bazel build //tools/serial-mcp:serial-mcp-server

SERVER_PATH="$REPO_ROOT/bazel-bin/tools/serial-mcp/serial-mcp-server"

if [ ! -f "$SERVER_PATH" ]; then
    echo "Error: server binary not found at $SERVER_PATH"
    exit 1
fi

claude mcp add serial-mcp -- "$SERVER_PATH"

echo ""
echo "serial-mcp installed."
echo ""
echo "Verify: claude mcp list"
echo ""
echo "Example queries in a Claude Code session:"
echo "  - Show me the last 50 lines of serial output"
echo "  - Search serial output for ERROR"
echo "  - What's the serial daemon status?"
