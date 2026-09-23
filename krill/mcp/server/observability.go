// Per-tool-call observability. mcp/main.go's logging.Configure (tracing +
// OTLP export) and the otelhttp.NewHandler wrap around the streamable-HTTP
// handler (transport.go) already give this binary process-level
// startup/shutdown logs and a generic HTTP span per request -- but every
// MCP tool call multiplexes over that single HTTP endpoint as JSON-RPC, so
// an HTTP span alone never shows which tool ran, for which persona, or
// whether it succeeded. instrumentToolCall is the choke point that fixes
// that, mirroring audience_score_system/mcp/server/observability.go's
// precedent: RegisterRead/RegisterWrite/registerOpsGated (registry.go)
// wrap every registered tool's full call -- including the
// unauthenticated/forbidden paths they check before invoking the product
// handler -- through it, so a tool author gets tracing/logging the same
// way they already get persona authorization, by going through the
// registry rather than by remembering to add it themselves.
//
// The actual span/log/error handling is libs/go/mcpobs.InstrumentToolCall
// (shared with audience_score_system/mcp/server's identical wrapper) --
// this file only supplies this package's own caller-identity attribute
// (a resolved Persona string).
package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpobs"
)

var tracer = logging.Tracer("krill/mcp/server")

// instrumentToolCall runs fn (a tool call already past mcp.AddTool's
// decode step) inside a trace span named after tool, and logs its outcome
// (success/failure, duration, resolved persona if any) once fn returns.
// Called from RegisterRead/RegisterWrite/registerOpsGated for every
// registered tool, so this always wraps the full call -- including the
// unauthenticated/forbidden paths those functions check before invoking
// the product handler -- not just the product handler itself.
func instrumentToolCall[Out any](ctx context.Context, toolName string, fn func(context.Context) (*mcp.CallToolResult, Out, error)) (*mcp.CallToolResult, Out, error) {
	return mcpobs.InstrumentToolCall(ctx, tracer, logger, toolName, func(ctx context.Context) (string, string, bool) {
		if persona := PersonaFromContext(ctx); persona != "" {
			return "persona", string(persona), true
		}
		return "", "", false
	}, fn)
}
