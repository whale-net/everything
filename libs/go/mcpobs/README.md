# mcpobs — shared MCP tracing/logging middleware

The observability half of the MCP contract every domain's MCP server and
MCP client should share: each MCP tool call gets its own span, nested
under the POST that carried it, with its DB work underneath — and a
client's outbound calls carry a trace so both ends land in one trace.

## Server HTTP handler: `NewHTTPHandler`

Use `mcpobs.NewHTTPHandler(handler, "<service>")` in place of
`otelhttp.NewHandler` around an MCP server's streamable-HTTP handler
(outside the auth middleware). It is `otelhttp.NewHandler` plus one step:
it rewrites the request's `traceparent` header to name the server span,
which is how tool-call spans find the POST to nest under (see below).

## Server side: `InstrumentToolCall`

Wrap every tool registration path a domain's MCP server has (read, write,
or any gated variant) so each call gets its own `mcp.tool/<name>` span —
with the call's DB spans underneath it — plus a structured log line
recording outcome and duration.

```go
var (
    tracer = logging.Tracer("mydomain/mcp/server")
    logger = logging.Get("mcp/server")
)

func instrumentToolCall[Out any](ctx context.Context, toolName string, req *mcp.CallToolRequest, fn func(context.Context) (*mcp.CallToolResult, Out, error)) (*mcp.CallToolResult, Out, error) {
    return mcpobs.InstrumentToolCall(ctx, tracer, logger, toolName, mcpobs.RequestHeader(req), func(ctx context.Context) (string, string, bool) {
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

### Servers without a registry choke point: `ToolCallMiddleware`

When tools are registered from many files (manmanv2, whagent_net), add the
same instrumentation as a receiving middleware instead. Add it **last** so
it runs outermost and also traces calls refused by auth middleware:

```go
srv.AddReceivingMiddleware(mcpobs.ToolCallMiddleware(
    logging.Tracer("mydomain/mcp"), logging.Get("mydomain/mcp/tools"), callerAttr))
```

`callerAttr` reads the caller from the raw `mcp.Request` (e.g. its
`TokenInfo`), since inner middleware has not resolved it yet; pass `nil` to
omit it.

### Why the tool span is parented from the request header, not `ctx`

`InstrumentToolCall` deliberately does **not** nest the tool span under
whatever span `ctx` already carries. On the streamable-HTTP transport
that would look right and be wrong.

The go-sdk builds one jsonrpc2 connection per MCP **session**, at
`initialize` time, from *that request's* context (`mcp.connect` →
`jsonrpc2.NewConnection(ctx, …)`). Tool handlers are dispatched off that
connection's read loop, so their context descends from the `initialize`
request — not from the POST that carried the call. Inheriting it welds
every call in a session into one trace whose duration is the session's
lifetime (observed: 989 spans over 7 minutes).

What the transport *does* thread down is the POST's headers
(`mcp.RequestExtra.Header`). `NewHTTPHandler` writes the server span into
`traceparent`, and `InstrumentToolCall` extracts it, giving one trace per
call across services:

```
caller span (e.g. whagent_net worker, via WrapClientTransport)
└── POST                   (NewHTTPHandler)
    ├── auth credential queries
    └── mcp.tool/<name>
        └── tool's DB / gRPC spans
```

Without `NewHTTPHandler` the header still holds the *caller's*
traceparent, so the tool span joins the caller's trace as a sibling of
the POST. With no `traceparent` at all (nil header, or a client that
doesn't propagate), the tool span starts a new root trace.

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

Without this, a domain MCP server's `NewHTTPHandler` wrap has no incoming
trace context to extract, so each POST starts a new root trace instead of
continuing the caller's. See
`whagent_net/worker/tools/client.go` for the real call site.
