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
package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/whale-net/everything/libs/go/logging"
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
	ctx, span := tracer.Start(ctx, "mcp.tool/"+toolName)
	defer span.End()
	span.SetAttributes(attribute.String("mcp.tool", toolName))

	start := time.Now()
	result, out, err := fn(ctx)
	duration := time.Since(start)

	attrs := []any{"tool", toolName, "duration_ms", duration.Milliseconds()}
	if persona := PersonaFromContext(ctx); persona != "" {
		attrs = append(attrs, "persona", string(persona))
		span.SetAttributes(attribute.String("mcp.persona", string(persona)))
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
