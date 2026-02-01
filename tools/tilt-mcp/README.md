# Tilt MCP Server

A Model Context Protocol (MCP) server that wraps Tilt CLI commands, enabling Claude Code to monitor and control Tilt resources without Node.js dependencies.

## Features

- **Session Status**: Get overall Tilt session information
- **Resource Listing**: List all resources with their current status
- **Log Retrieval**: Fetch logs for specific resources (ANSI codes stripped)
- **Resource Triggering**: Force rebuild/update of resources

## Installation

### Prerequisites

- Python 3.13+
- Tilt installed and in PATH
- Bazel (for building)

### Build with Bazel

```bash
# From repository root
bazel build //tools/tilt-mcp:tilt-mcp-server
```

### Install with uv (alternative)

```bash
# From repository root
cd tools/tilt-mcp
uv venv
source .venv/bin/activate  # or `.venv/Scripts/activate` on Windows
uv pip install fastmcp
```

## Available Tools

### tilt_status

Get overall Tilt session status.

**Parameters**: None

**Returns**:
```json
{
  "name": "Tiltfile",
  "creationTimestamp": "2024-01-01T00:00:00Z",
  "status": {
    "ready": true
  },
  "targets": ["api", "postgres"]
}
```

### tilt_get_resources

List all Tilt resources with current status.

**Parameters**: None

**Returns**:
```json
{
  "resources": [
    {
      "name": "postgres-dev",
      "runtimeStatus": "ok",
      "updateStatus": "ready",
      "conditions": []
    }
  ],
  "count": 1
}
```

### tilt_logs

Retrieve logs for a specific resource.

**Parameters**:
- `resource` (required): Resource name (e.g., "postgres-dev")
- `lines` (optional): Number of lines to retrieve (default: 100)

**Returns**:
```json
{
  "resource": "postgres-dev",
  "lines": 100,
  "logs": "INFO Starting server\nReady to accept connections",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

### tilt_trigger

Force rebuild/update of a resource.

**Parameters**:
- `resource` (required): Resource name to trigger

**Returns**:
```json
{
  "success": true,
  "resource": "manmanv2-api",
  "message": "Successfully triggered rebuild of manmanv2-api",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

## Usage with Claude Code

### Configure the MCP Server

#### Option 1: Using Bazel Binary (Recommended)

```bash
claude mcp add tilt-mcp -- /home/alex/whale_net/everything/bazel-bin/tools/tilt-mcp/tilt-mcp-server
```

#### Option 2: Using uv

```bash
claude mcp add tilt-mcp -- uv run --directory /home/alex/whale_net/everything/tools/tilt-mcp fastmcp run server.py
```

### Verify Installation

```bash
claude mcp list
```

You should see `tilt-mcp` in the list of available MCP servers.

### Example Queries

Start a Claude Code session and try:

- "What's the status of my Tilt session?"
- "List all Tilt resources"
- "Show me logs for postgres-dev"
- "Show the last 50 lines of logs for manmanv2-api"
- "Trigger rebuild of manmanv2-api"

## Development

### Running Tests

```bash
# With Bazel
bazel test //tools/tilt-mcp:test_server

# With pytest directly
cd tools/tilt-mcp
pytest test_server.py -v
```

### Project Structure

```
tools/tilt-mcp/
├── BUILD.bazel           # Bazel build configuration
├── server.py             # Main MCP server implementation
├── test_server.py        # Unit tests
└── README.md             # This file
```

### Key Implementation Details

- **ANSI Stripping**: Logs are cleaned of terminal control codes for readability
- **Error Handling**: All commands return structured error responses on failure
- **Timeouts**: Commands timeout after 10s (status/resources/trigger) or 30s (logs)
- **Transport**: Uses stdio transport for MCP communication

## Troubleshooting

### "tilt command not found"

Ensure Tilt is installed and in your PATH:

```bash
which tilt
tilt version
```

### "Failed to get Tilt session status"

Verify Tilt is running:

```bash
tilt get session
```

If not running, start Tilt:

```bash
cd manman-v2  # or your project directory
tilt up
```

### "Failed to trigger resource: <name>"

Check the resource exists:

```bash
tilt get uiresources
```

Verify the resource name matches exactly (case-sensitive).

### Connection Refused

Tilt API server may not be accessible. Check Tilt is running and healthy:

```bash
tilt status
```

## License

Part of the Everything monorepo.
