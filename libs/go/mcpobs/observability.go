// Package mcpobs is the shared tracing/logging middleware every domain's
// MCP server and MCP client should route through: InstrumentToolCall
// (server side) gives every tool call its own self-contained trace --
// one mcp.tool/<name> span with the call's DB spans underneath it -- and
// WrapClientTransport (client side, see transport.go) makes sure an
// inbound request carries a trace at all by injecting a W3C traceparent
// header on the way out. Before this package existed,
// audience_score_system/mcp/server and krill/mcp/server each carried
// their own byte-identical InstrumentToolCall (only their caller-identity
// attribute differed), and whagent_net's MCP client issued every call
// through a bare http.RoundTripper with no trace context at all -- see
// each of those packages' own observability.go / client.go for how they
// plug into this one now.
package mcpobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// CallerAttr resolves the resolved-caller identity attribute (if any) to
// attach to a tool call's span and log line -- e.g. audience_score_system's
// resolved Person UUID or krill's resolved Persona string. Return
// ok=false when ctx carries no resolved caller; InstrumentToolCall still
// traces/logs the call either way (including an unauthenticated
// rejection, since that's exactly the case with no caller to attribute).
type CallerAttr func(ctx context.Context) (key, value string, ok bool)

// InstrumentToolCall runs fn (a tool call already past mcp.AddTool's
// decode step) inside a trace span named "mcp.tool/"+toolName, and logs
// its outcome (success/failure, duration, resolved caller attribute if
// any) once fn returns. Call this from every tool registration path a
// domain's MCP server has (read, write, or any gated variant) so it
// always wraps the full call -- including whatever auth/authorization
// checks run before the product handler -- not just the product handler
// itself. callerAttr may be nil to skip the caller attribute entirely.
//
// The span deliberately starts a NEW trace rather than nesting under the
// span ctx already carries. go-sdk's streamable-HTTP transport gives a
// tool handler a context descending from the jsonrpc2 connection, which
// is built once per MCP session from the `initialize` request's context
// (mcp.connect -> jsonrpc2.NewConnection) -- not from the POST that
// actually carried this call, which the transport never threads down
// (servePOST publishes the bare JSON-RPC message onto the connection's
// incoming channel). Inheriting it therefore parents every tool call in a
// session to one long-dead `initialize` span, so a "trace" accumulates
// every call the session ever makes: the per-POST span shows nothing but
// the auth UPDATE, while the tool's own span and all its queries land in
// a sibling mega-trace whose duration is the whole session. Starting fresh
// makes each tool call one self-contained trace -- tool span, then its DB
// spans underneath -- at the cost of trace-level linkage to the HTTP
// request, which names no additional information (same tool, same
// persona) and cannot be recovered from the transport anyway.
func InstrumentToolCall[Out any](
	ctx context.Context,
	tracer trace.Tracer,
	logger *slog.Logger,
	toolName string,
	callerAttr CallerAttr,
	fn func(context.Context) (*mcp.CallToolResult, Out, error),
) (*mcp.CallToolResult, Out, error) {
	ctx, span := tracer.Start(trace.ContextWithSpanContext(ctx, trace.SpanContext{}), "mcp.tool/"+toolName)
	defer span.End()
	span.SetAttributes(attribute.String("mcp.tool", toolName))

	start := time.Now()
	result, out, err := fn(ctx)
	duration := time.Since(start)

	attrs := []any{"tool", toolName, "duration_ms", duration.Milliseconds()}
	if callerAttr != nil {
		if key, value, ok := callerAttr(ctx); ok {
			attrs = append(attrs, key, value)
			span.SetAttributes(attribute.String("mcp."+key, value))
		}
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
