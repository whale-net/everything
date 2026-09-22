# mcpobs — shared MCP tracing/logging middleware

The observability half of the MCP contract every domain's MCP server and
MCP client should share, so a caller's trace ID correlates end-to-end
instead of showing up as disconnected root traces on either side of the
MCP boundary.

## Server side: `InstrumentToolCall`

Wrap every tool registration path a domain's MCP server has (read, write,
or any gated variant) so each call gets its own `mcp.tool/<name>` child
span — nested under whatever span the inbound HTTP request already
carries — plus a structured log line recording outcome and duration.

```go
var (
    tracer = logging.Tracer("mydomain/mcp/server")
    logger = logging.Get("mcp/server")
)

func instrumentToolCall[Out any](ctx context.Context, toolName string, fn func(context.Context) (*mcp.CallToolResult, Out, error)) (*mcp.CallToolResult, Out, error) {
    return mcpobs.InstrumentToolCall(ctx, tracer, logger, toolName, func(ctx context.Context) (string, string, bool) {
        if caller := CallerFromContext(ctx); caller != nil {
            return "caller_id", caller.ID.String(), true
        }
        return "", "", false
    }, fn)
}
```

See `audience_score_system/mcp/server/observability.go` and
`krill/mcp/server/observability.go` for the two real call sites — each
supplies its own caller-identity attribute (a `Person` UUID vs. a
`Persona` string) but shares this package's span/log/error handling.

This only produces a *child* span if the `ctx` passed in already carries
a parent span from the inbound request. If a domain's MCP server sees
only a root span with no `mcp.tool/<name>` child underneath it despite
using `InstrumentToolCall`, the request's `context.Context` reaching the
registered tool handler is not the same context tree as the span-bearing
HTTP request — check how the MCP transport threads context from the HTTP
handler down into `mcp.ToolHandlerFor`.

## Client side: `WrapClientTransport`

Wrap an MCP client's `http.RoundTripper` (e.g. one that injects a
per-server auth credential) so every request it issues carries the
caller's active trace as a W3C `traceparent` header:

```go
transport := &mcp.StreamableClientTransport{
    Endpoint:   serverURL,
    HTTPClient: &http.Client{Transport: mcpobs.WrapClientTransport(bearerRoundTripper{token: token})},
}
```

Without this, a domain MCP server's own `otelhttp.NewHandler` wrap has no
incoming trace context to extract, and every tool call starts a brand new
disconnected root trace instead of continuing the caller's. See
`whagent_net/worker/tools/client.go` for the real call site.
