# mcpobs — shared MCP tracing/logging middleware

The observability half of the MCP contract every domain's MCP server and
MCP client should share: each MCP tool call shows up as its own trace
containing that call and its DB work, and a client's outbound calls carry
a trace so the two ends can be correlated.

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

### Why each tool call is its own trace, not a child of the HTTP span

`InstrumentToolCall` deliberately does **not** nest the tool span under
whatever span `ctx` already carries. On the streamable-HTTP transport
that would look right and be wrong.

The go-sdk builds one jsonrpc2 connection per MCP **session**, at
`initialize` time, from *that request's* context (`mcp.connect` →
`jsonrpc2.NewConnection(ctx, …)`). Tool handlers are dispatched off that
connection's read loop, so their context descends from the `initialize`
request — not from the POST that carried the call. The transport never
threads the per-request HTTP context down: `servePOST` publishes the bare
JSON-RPC message onto the connection's incoming channel, and
`RequestExtra` exposes only `TokenInfo`/`Header`, no `Context`.

Inheriting that context produces two failure modes that look like
unrelated bugs in a tracing backend:

- **The POST span looks empty.** Each tool call's own POST produces a
  span whose only child is the auth `UPDATE mcp_credential SET
  last_used_at`. The tool span and its queries are in a *different* trace.
- **One trace accumulates the whole session.** Every tool call in the
  session shares the dead `initialize` span as ancestor, so the trace's
  duration is the session's lifetime and its span count grows without
  bound (observed: 989 spans over 7 minutes), with children that start
  minutes after their parent ended.

Starting a fresh trace per call costs the trace-level link to the HTTP
request, which carries no information the tool span doesn't already have
(same tool, same persona) and cannot be recovered from the transport
anyway.

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
