package mcpobs

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func testTracer(t *testing.T) (trace.Tracer, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp.Tracer("test"), exporter
}

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// spanNamed finds an exported span by name. The in-memory exporter returns
// spans in End() order, which is the opposite of the nesting order.
func spanNamed(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no span named %q in %v", name, spanNames(spans))
	return tracetest.SpanStub{}
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.Name)
	}
	return names
}

// A tool call must not inherit the span its context happens to carry. The
// streamable-HTTP transport hands tool handlers a context rooted at the
// session's `initialize` request, so inheriting would weld every call in
// a session onto one dead span and merge them into a single mega-trace.
func TestInstrumentToolCall_StartsNewTrace(t *testing.T) {
	tracer, exporter := testTracer(t)

	outerCtx, outerSpan := tracer.Start(context.Background(), "POST /mcp/design")
	outerSpanID := outerSpan.SpanContext().SpanID()

	_, _, err := InstrumentToolCall(outerCtx, tracer, noopLogger(), "get_task", nil,
		func(context.Context) (*mcp.CallToolResult, int, error) {
			return nil, 0, nil
		})
	require.NoError(t, err)
	outerSpan.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)
	tool := spanNamed(t, spans, "mcp.tool/get_task")
	assert.NotEqual(t, outerSpanID, tool.SpanContext.SpanID())
	assert.False(t, tool.Parent.HasSpanID(), "tool span must be a root, not a child of the inbound span")
	assert.NotEqual(t, tool.SpanContext.TraceID(), outerSpan.SpanContext().TraceID(),
		"tool call must not share the stale session's trace ID")
}

// The tool span must still be the parent of whatever fn traces, so a
// call's DB spans nest under it instead of becoming orphan roots.
func TestInstrumentToolCall_NestsChildSpans(t *testing.T) {
	tracer, exporter := testTracer(t)

	_, _, err := InstrumentToolCall(context.Background(), tracer, noopLogger(), "get_task", nil,
		func(ctx context.Context) (*mcp.CallToolResult, int, error) {
			_, span := tracer.Start(ctx, "db.query")
			span.End()
			return nil, 0, nil
		})
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)
	tool := spanNamed(t, spans, "mcp.tool/get_task")
	query := spanNamed(t, spans, "db.query")
	assert.Equal(t, tool.SpanContext.SpanID(), query.Parent.SpanID())
	assert.Equal(t, tool.SpanContext.TraceID(), query.SpanContext.TraceID())
}

// Stripping the span must not strip the rest of the context: persona
// resolution and cancellation still depend on ctx values surviving.
func TestInstrumentToolCall_PreservesContextValues(t *testing.T) {
	tracer, _ := testTracer(t)

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "carried")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var seen any
	_, _, err := InstrumentToolCall(ctx, tracer, noopLogger(), "get_task", nil,
		func(ctx context.Context) (*mcp.CallToolResult, int, error) {
			seen = ctx.Value(ctxKey{})
			return nil, 0, nil
		})
	require.NoError(t, err)
	assert.Equal(t, "carried", seen)
}
