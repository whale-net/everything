#!/bin/bash
# Install Tilt MCP server to Cursor IDE

set -e

# Get the repository root
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "Building Tilt MCP server..."
cd "$REPO_ROOT"
bazel build //tools/tilt-mcp:tilt-mcp-server

echo "Installing to Cursor..."
# The bazel-bin symlink points to the actual binary
SERVER_PATH="$REPO_ROOT/bazel-bin/tools/tilt-mcp/tilt-mcp-server"

if [ ! -f "$SERVER_PATH" ]; then
    echo "Error: Server binary not found at $SERVER_PATH"
    exit 1
fi

# Determine Cursor config location based on OS
if [[ "$OSTYPE" == "linux-gnu"* ]]; then
    CONFIG_DIR="$HOME/.cursor"
elif [[ "$OSTYPE" == "darwin"* ]]; then
    CONFIG_DIR="$HOME/Library/Application Support/Cursor"
elif [[ "$OSTYPE" == "msys" ]] || [[ "$OSTYPE" == "win32" ]]; then
    CONFIG_DIR="$APPDATA/Cursor"
else
    echo "Error: Unsupported OS: $OSTYPE"
    exit 1
fi

# Path to MCP settings file (Cursor uses mcp.json in the root)
MCP_SETTINGS="$CONFIG_DIR/mcp.json"

# Function to add tilt-mcp to existing config
add_tilt_mcp_to_config() {
    local temp_file=$(mktemp)
    
    # Use jq to merge if available, otherwise manual merge
    if command -v jq &> /dev/null; then
        jq --arg cmd "$SERVER_PATH" '.mcpServers["tilt-mcp"] = {"command": $cmd, "args": []}' "$MCP_SETTINGS" > "$temp_file"
        mv "$temp_file" "$MCP_SETTINGS"
        echo "✓ Added tilt-mcp to existing MCP configuration"
    else
        # Manual merge - warn user
        echo ""
        echo "⚠️  MCP config already exists at: $MCP_SETTINGS"
        echo ""
        echo "Please manually add the following to the 'mcpServers' object:"
        echo ""
        cat <<EOF
    "tilt-mcp": {
      "command": "$SERVER_PATH",
      "args": []
    }
EOF
        echo ""
        echo "Install 'jq' for automatic config merging: sudo apt install jq"
        return 1
    fi
}

# Create or update settings.json
if [ ! -f "$MCP_SETTINGS" ]; then
    echo "Creating new MCP settings file..."
    mkdir -p "$(dirname "$MCP_SETTINGS")"
    cat > "$MCP_SETTINGS" <<EOF
{
  "mcpServers": {
    "tilt-mcp": {
      "command": "$SERVER_PATH",
      "args": []
    }
  }
}
EOF
    echo "✓ Created $MCP_SETTINGS"
else
    echo "MCP config file found. Merging configuration..."
    add_tilt_mcp_to_config || exit 1
fi

echo ""
echo "✓ Tilt MCP server configuration complete!"
echo ""
echo "Configuration file: $MCP_SETTINGS"
echo "Server binary: $SERVER_PATH"
echo ""
echo "Next steps:"
echo "  1. Restart Cursor IDE"
echo "  2. Open Cursor Settings (Cmd/Ctrl + ,)"
echo "  3. Navigate to Features > MCP to verify the server is listed"
echo ""
echo "Test queries in a Cursor AI chat:"
echo "  - What's the status of my Tilt session?"
echo "  - List all Tilt resources"
echo "  - Show me logs for postgres-dev"
echo "  - Trigger rebuild of manmanv2-api"
