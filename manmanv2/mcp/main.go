// Command mcp is the manmanv2 MCP server: authenticated, persona-gated tools
// over the manmanv2 control API.
package main

import (
	"log/slog"
	"os"

	_ "github.com/whale-net/everything/manmanv2/mcp/server"
)

func main() {
	slog.Error("manmanv2 mcp: not implemented")
	os.Exit(1)
}
