#!/bin/bash
# Install Tilt MCP server to Claude Code

set -e

# Get the repository root
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "Building Tilt MCP server..."
cd "$REPO_ROOT"
bazel build //tools/tilt-mcp:tilt-mcp-server

echo "Installing to Claude Code..."
# The bazel-bin symlink points to the actual binary
SERVER_PATH="$REPO_ROOT/bazel-bin/tools/tilt-mcp/tilt-mcp-server"

if [ ! -f "$SERVER_PATH" ]; then
    echo "Error: Server binary not found at $SERVER_PATH"
    exit 1
fi

# Add to Claude Code MCP configuration
claude mcp add tilt-mcp -- "$SERVER_PATH"

echo ""
echo "✓ Tilt MCP server installed successfully!"
echo ""
echo "Verify installation:"
echo "  claude mcp list"
echo ""
echo "Test queries in a Claude Code session:"
echo "  - What's the status of my Tilt session?"
echo "  - List all Tilt resources"
echo "  - Show me logs for postgres-dev"
echo "  - Trigger rebuild of manmanv2-api"
