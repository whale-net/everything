package mcpobs

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
)

// NewHTTPHandler wraps an MCP server's HTTP handler with otelhttp and
// rewrites each request's traceparent header to name the otelhttp server
// span. go-sdk hands those headers to tool calls (mcp.RequestExtra.Header)
// but not the request context, so this is how InstrumentToolCall finds the
// POST's span to nest under. Use it in place of otelhttp.NewHandler.
func NewHTTPHandler(h http.Handler, operation string) http.Handler {
	inject := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		propagation.TraceContext{}.Inject(r.Context(), propagation.HeaderCarrier(r.Header))
		h.ServeHTTP(w, r)
	})
	return otelhttp.NewHandler(inject, operation)
}

// RequestHeader returns the HTTP headers of the request that carried req,
// or nil when req did not arrive over HTTP.
func RequestHeader(req mcp.Request) http.Header {
	if req == nil {
		return nil
	}
	if extra := req.GetExtra(); extra != nil {
		return extra.Header
	}
	return nil
}
