package mcpobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// A traceparent header parents the tool span, even when ctx carries the
// stale session span.
func TestInstrumentToolCall_ParentsFromHeader(t *testing.T) {
	tracer, exporter := testTracer(t)

	sessionCtx, sessionSpan := tracer.Start(context.Background(), "initialize")
	callerCtx, callerSpan := tracer.Start(context.Background(), "POST /mcp")
	header := http.Header{}
	propagation.TraceContext{}.Inject(callerCtx, propagation.HeaderCarrier(header))

	_, _, err := InstrumentToolCall(sessionCtx, tracer, noopLogger(), "get_task", header, nil,
		func(context.Context) (*mcp.CallToolResult, int, error) { return nil, 0, nil })
	require.NoError(t, err)
	callerSpan.End()
	sessionSpan.End()

	tool := spanNamed(t, exporter.GetSpans(), "mcp.tool/get_task")
	assert.Equal(t, callerSpan.SpanContext().TraceID(), tool.SpanContext.TraceID())
	assert.Equal(t, callerSpan.SpanContext().SpanID(), tool.Parent.SpanID())
}

// End to end: a client's traceparent reaches NewHTTPHandler, and the tool
// call nests under the handler's server span in the client's trace.
func TestNewHTTPHandler_ToolSpanNestsUnderServerSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })
	tracer := tp.Tracer("test")

	h := NewHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulates go-sdk: only the headers reach the tool handler.
		_, _, err := InstrumentToolCall(context.Background(), tracer, noopLogger(), "get_task", r.Header, nil,
			func(context.Context) (*mcp.CallToolResult, int, error) { return nil, 0, nil })
		require.NoError(t, err)
	}), "test-mcp")

	clientCtx, clientSpan := tracer.Start(context.Background(), "client")
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	propagation.TraceContext{}.Inject(clientCtx, propagation.HeaderCarrier(req.Header))
	h.ServeHTTP(httptest.NewRecorder(), req)
	clientSpan.End()

	spans := exporter.GetSpans()
	server := spanNamed(t, spans, "POST")
	tool := spanNamed(t, spans, "mcp.tool/get_task")
	assert.Equal(t, clientSpan.SpanContext().SpanID(), server.Parent.SpanID())
	assert.Equal(t, server.SpanContext.SpanID(), tool.Parent.SpanID())
	assert.Equal(t, clientSpan.SpanContext().TraceID(), tool.SpanContext.TraceID())
}

func TestRequestHeader(t *testing.T) {
	assert.Nil(t, RequestHeader(nil))
	assert.Nil(t, RequestHeader(&mcp.CallToolRequest{}))
	h := http.Header{"Traceparent": {"x"}}
	assert.Equal(t, h, RequestHeader(&mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: h}}))
}
