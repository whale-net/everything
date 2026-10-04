---
name: architecture-observability
description: Wire or review logging, tracing, and metrics for a service in this repo — the logging.Configure flags every long-running Go app needs, flushing on exit, otelhttp/otelgrpc wrapping, per-tool-call MCP tracing via libs/go/mcpobs, and the INFO/WARNING/ERROR level rules. Use when adding a new binary or MCP server, when a service "doesn't show up" in Grafana/Tempo/Loki, or when reviewing a main.go.
---

# Observability

This is the canonical, harness-neutral source for this repo's observability
wiring. It is symlinked into `.claude/skills/architecture-observability` for
Claude Code; `AGENTS.md` § Observability keeps only the short,
always-loaded rules. Library detail lives in `libs/go/logging/README.md`,
`libs/go/mcpobs/README.md`, and `libs/python/logging/README.md`.

## Failure mode this prevents

Nothing errors when a service is missing tracing. `otelhttp`/`otelgrpc`
wrappers silently record to the global no-op tracer until
`logging.Configure` installs a real provider, so a service looks
instrumented in code review and is absent from Tempo and Loki in prod.

## Every long-running Go service (`release_app` server, worker, UI, MCP)

```go
logging.Configure(logging.Config{
    ServiceName:   "<domain>-<component>", // matches the Helm app name
    Domain:        "<domain>",
    JSONFormat:    true,
    EnableOTLP:    true, // logs to the collector
    EnableTracing: true, // spans to the collector
})
defer logging.Shutdown(ctx) //nolint:errcheck
```

- **Both flags, always.** The zero value is off. Helm already injects
  `OTEL_EXPORTER_OTLP_ENDPOINT`; turning a signal off is an env concern
  (`OTEL_SDK_DISABLED`, `OTEL_TRACES_DISABLED`), never a code default.
- **Flush on every exit path.** `os.Exit` skips defers, so a
  `main` that does `if err := run(); err != nil { os.Exit(1) }` must call
  `logging.Shutdown` before exiting or it drops the final spans and logs.
- One-shot binaries (migrations, CLIs, demos, local emulators) are exempt.

## Inbound and outbound wrapping

| Surface | Wrap with |
|---------|-----------|
| HTTP server | `otelhttp.NewHandler(handler, "<service>")` |
| gRPC server | `grpc.StatsHandler(otelgrpc.NewServerHandler())` plus `logging.New{Unary,Stream}ServerLoggingInterceptor` first in the chain |
| gRPC client | `libs/go/grpcclient` (adds `otelgrpc` for you) |
| HTTP client | `otelhttp.NewTransport`, or `logging.WrapDefaultHTTPTransport()` for `http.DefaultClient` |
| MCP server | `otelhttp` on the HTTP handler **and** `libs/go/mcpobs` per tool call (below) |
| MCP client | `mcpobs.WrapClientTransport` |

## MCP servers need per-tool-call spans

The go-sdk streamable-HTTP transport hands tool handlers a context rooted at
the session's `initialize` request, not the POST that carried the call.
Without `mcpobs`, every tool call in a session (and its gRPC/DB children)
lands in one ever-growing trace, and the per-POST span is empty. Pick one:

- **One registry choke point** (krill, audience_score_system): wrap each
  registration with `mcpobs.InstrumentToolCall`.
- **Tools registered in many places** (manmanv2, whagent_net): add
  `mcpobs.ToolCallMiddleware` as the **last** `AddReceivingMiddleware`
  call, so it runs outermost and also traces auth refusals.

## Log levels

INFO = notable and completed normally; WARNING = adjusted to keep going
(fallback, retry succeeded); ERROR = the operation cannot continue. Expected
control flow is never WARNING or ERROR. See `AGENTS.md` § Logging Levels.

## Review checklist for a new `main.go`

1. `logging.Configure` has `EnableOTLP: true` and `EnableTracing: true`.
2. `logging.Shutdown` runs on success and failure exits.
3. Every inbound surface (HTTP, gRPC, MCP tool call) is wrapped per the table.
4. Outbound clients propagate context (`grpcclient`, `otelhttp.NewTransport`,
   `mcpobs.WrapClientTransport`).
5. After deploy: the service name appears in Tempo with child spans for its
   downstream calls, and in Loki with `trace_id` on log lines.
