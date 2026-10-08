package mcpobs

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"
)

// RequestCallerAttr resolves the caller attribute from the raw request,
// for servers whose caller identity lives on the request (e.g. TokenInfo)
// rather than on a context an inner middleware has not populated yet.
type RequestCallerAttr func(req mcp.Request) (key, value string, ok bool)

// ToolCallMiddleware applies InstrumentToolCall to every tools/call as a
// receiving middleware, for servers that register tools in many places
// instead of through one registry. Add it last so it runs outermost and
// covers auth refusals too. callerAttr may be nil.
func ToolCallMiddleware(tracer trace.Tracer, logger *slog.Logger, callerAttr RequestCallerAttr) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			name := "unknown"
			if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && p != nil && p.Name != "" {
				name = p.Name
			}
			var attr CallerAttr
			if callerAttr != nil {
				attr = func(context.Context) (string, string, bool) { return callerAttr(req) }
			}
			_, res, err := InstrumentToolCall(ctx, tracer, logger, name, RequestHeader(req), attr,
				func(ctx context.Context) (*mcp.CallToolResult, mcp.Result, error) {
					res, err := next(ctx, method, req)
					return nil, res, err
				})
			return res, err
		}
	}
}
