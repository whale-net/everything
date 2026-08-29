#!/bin/bash
# Install the agentsync MCP server to Claude Code.
#
# Builds the server via Bazel, then registers it with 'claude mcp add'.
# Run once per machine.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "Building agentsync-mcp..."
cd "$REPO_ROOT"
bazel build //tools/agentsync-mcp:agentsync-mcp-server

SERVER_PATH="$REPO_ROOT/bazel-bin/tools/agentsync-mcp/agentsync-mcp-server"

if [ ! -f "$SERVER_PATH" ]; then
    echo "Error: server binary not found at $SERVER_PATH"
    exit 1
fi

claude mcp add agentsync-mcp -- "$SERVER_PATH"

echo ""
echo "agentsync-mcp installed."
echo ""
echo "Verify: claude mcp list"
echo ""
echo "Example usage across two Claude Code sessions:"
echo "  Parent:  start_session() -> session_id; tell each child the session_id"
echo "  Child:   join_session(session_id, 'child-a')"
echo "  Either:  sync(session_id, my_id, 'message') to send + block for a reply"
