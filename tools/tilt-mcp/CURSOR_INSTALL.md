# Tilt MCP Server - Cursor Installation

## Quick Install

Run the installation script:

```bash
cd /home/alex/whale_net/everything/tools/tilt-mcp
chmod +x install-mcp-cursor.sh
./install-mcp-cursor.sh
```

Then restart Cursor IDE.

## Manual Installation

If you prefer to install manually:

### 1. Build the MCP Server

```bash
cd /home/alex/whale_net/everything
bazel build //tools/tilt-mcp:tilt-mcp-server
```

### 2. Locate Cursor MCP Settings

The MCP configuration file location depends on your OS:

- **Linux**: `~/.cursor/mcp/settings.json`
- **macOS**: `~/Library/Application Support/Cursor/mcp/settings.json`
- **Windows**: `%APPDATA%\Cursor\mcp\settings.json`

### 3. Add Tilt MCP Server Configuration

Create or edit the `settings.json` file:

```json
{
  "mcpServers": {
    "tilt-mcp": {
      "command": "/home/alex/whale_net/everything/bazel-bin/tools/tilt-mcp/tilt-mcp-server",
      "args": []
    }
  }
}
```

**Note**: Use the absolute path to the `tilt-mcp-server` binary from your repository.

If you already have other MCP servers configured, add the `tilt-mcp` entry to the existing `mcpServers` object:

```json
{
  "mcpServers": {
    "existing-server": {
      "command": "...",
      "args": []
    },
    "tilt-mcp": {
      "command": "/home/alex/whale_net/everything/bazel-bin/tools/tilt-mcp/tilt-mcp-server",
      "args": []
    }
  }
}
```

### 4. Restart Cursor

Close and reopen Cursor IDE for the changes to take effect.

### 5. Verify Installation

1. Open Cursor Settings (Cmd/Ctrl + ,)
2. Navigate to **Features > MCP**
3. You should see `tilt-mcp` listed as an available server

## Usage

Once installed, you can query Tilt status through Cursor's AI chat:

### Available Commands

1. **Get Session Status**
   - "What's the status of my Tilt session?"
   - "Show Tilt session info"

2. **List Resources**
   - "List all Tilt resources"
   - "What resources are running in Tilt?"

3. **View Logs**
   - "Show me logs for postgres-dev"
   - "Get the last 50 lines of logs for manmanv2-api"

4. **Trigger Rebuilds**
   - "Trigger rebuild of manmanv2-api"
   - "Force update manmanv2-processor"

## Troubleshooting

### Server Not Appearing in Cursor

1. Check the configuration file exists and is valid JSON
2. Verify the binary path is correct and the file exists
3. Check Cursor's developer tools (Help > Toggle Developer Tools) for errors
4. Restart Cursor completely (quit and relaunch)

### "tilt command not found"

Ensure Tilt is installed and in your PATH:

```bash
which tilt
tilt version
```

Install Tilt if needed: https://docs.tilt.dev/install.html

### "Failed to get Tilt session status"

Start a Tilt session first:

```bash
cd /home/alex/whale_net/everything/manman-v2
tilt up
```

### Rebuilding the Server

If you update the server code, rebuild and restart Cursor:

```bash
bazel build //tools/tilt-mcp:tilt-mcp-server
# Then restart Cursor IDE
```

## Alternative: Using uv (Development)

For development or if you prefer not to use the Bazel binary:

```json
{
  "mcpServers": {
    "tilt-mcp": {
      "command": "uv",
      "args": [
        "run",
        "--directory",
        "/home/alex/whale_net/everything/tools/tilt-mcp",
        "fastmcp",
        "run",
        "server.py"
      ]
    }
  }
}
```

This requires `uv` to be installed: https://docs.astral.sh/uv/

## Features

- **Session Status**: Monitor overall Tilt session health
- **Resource Listing**: See all resources and their states
- **Log Retrieval**: Fetch logs from any resource (ANSI codes automatically stripped)
- **Resource Triggering**: Force rebuilds without leaving Cursor

## Security Note

The MCP server executes `tilt` CLI commands on your local machine. It only accesses the Tilt session you have running. Make sure you trust the code before installing.
