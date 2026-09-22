package mcpobs

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// WrapClientTransport wraps rt (a domain-specific http.RoundTripper --
// e.g. one that injects a per-server auth credential) with otelhttp so
// every request it issues carries the caller's active trace as a W3C
// traceparent header. A domain MCP server's own otelhttp.NewHandler wrap
// extracts this on the receiving end via libs/go/logging's globally
// registered propagator -- without this wrap, an MCP client's calls into
// that server always start a disconnected root trace instead of
// continuing the caller's own, however well InstrumentToolCall
// (observability.go) instruments the receiving side.
func WrapClientTransport(rt http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(rt)
}
