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
// #1643, mcpauth's TokenVerifier at the HTTP layer, which logs nothing
// and returns a single fixed error (see libs/go/mcpauth's NFR1) -- happen
// before a call ever reaches the registry; PersonMiddleware's rejections
// log directly against the package-level logger below.
package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/whale-net/everything/libs/go/logging"
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
	ctx, span := tracer.Start(ctx, "mcp.tool/"+toolName)
	defer span.End()
	span.SetAttributes(attribute.String("mcp.tool", toolName))

	start := time.Now()
	result, out, err := fn(ctx)
	duration := time.Since(start)

	attrs := []any{"tool", toolName, "duration_ms", duration.Milliseconds()}
	if person := PersonFromContext(ctx); person != nil {
		attrs = append(attrs, "person_id", person.ID)
		span.SetAttributes(attribute.String("mcp.person_id", person.ID.String()))
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		logger.WarnContext(ctx, "mcp tool call failed", append(attrs, "error", err.Error())...)
		return result, out, err
	}
	logger.InfoContext(ctx, "mcp tool call handled", attrs...)
	return result, out, err
}
