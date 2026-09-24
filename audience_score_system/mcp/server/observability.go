// Per-tool-call observability. mcp/main.go's logging.Configure (tracing +
// OTLP export) and the otelhttp.NewHandler wrap around NewHTTPHandler
// (transport.go) already give this binary process-level startup/shutdown
// logs and generic HTTP spans -- but every MCP tool call multiplexes over
// that single HTTP endpoint as JSON-RPC, so an HTTP span alone never shows
// which tool ran, for which caller, or whether it succeeded. instrumentToolCall
// is the choke point that fixes that: RegisterRead/RegisterWrite
// (registry.go) wrap every registered tool's full call -- including the
// unauthenticated/permission-denied paths they check before invoking the
// product handler -- through it, so a tool author gets tracing/logging the
// same way they already get Channel-scope authorization and idempotency,
// by going through the registry rather than by remembering to add it
// themselves. auth.go's own rejections (PersonMiddleware) -- and, since
// #1643, auth's TokenVerifier at the HTTP layer, which logs nothing
// and returns a single fixed error (see libs/go/auth's NFR1) -- happen
// before a call ever reaches the registry; PersonMiddleware's rejections
// log directly against the package-level logger below.
//
// The actual span/log/error handling is libs/go/mcpobs.InstrumentToolCall
// (shared with krill/mcp/server's identical wrapper) -- this file only
// supplies this package's own caller-identity attribute (a resolved
// Person's UUID).
package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpobs"
)

var (
	tracer = logging.Tracer("audience_score_system/mcp/server")
	logger = logging.Get("mcp/server")
)

// instrumentToolCall runs fn (a tool call already past mcp.AddTool's
// decode step) inside a trace span named after tool, and logs its outcome
// (success/failure, duration, resolved caller if any) once fn returns.
// Called from RegisterRead/RegisterWrite for every registered tool, so
// this always wraps the full call -- including the unauthenticated/
// permission-denied paths those two functions check before invoking the
// product handler -- not just the product handler itself.
func instrumentToolCall[Out any](ctx context.Context, toolName string, fn func(context.Context) (*mcp.CallToolResult, Out, error)) (*mcp.CallToolResult, Out, error) {
	return mcpobs.InstrumentToolCall(ctx, tracer, logger, toolName, func(ctx context.Context) (string, string, bool) {
		if person := PersonFromContext(ctx); person != nil {
			return "person_id", person.ID.String(), true
		}
		return "", "", false
	}, fn)
}
