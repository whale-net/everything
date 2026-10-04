package mcpobs

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
)

func TestToolCallMiddleware_SpansToolCalls(t *testing.T) {
	tracer, exporter := testTracer(t)
	mw := ToolCallMiddleware(tracer, noopLogger(), func(mcp.Request) (string, string, bool) {
		return "persona", "admin", true
	})
	h := mw(func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		_, span := tracer.Start(ctx, "grpc.call")
		span.End()
		return &mcp.CallToolResult{}, nil
	})

	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Name: "list_servers"}}
	res, err := h(context.Background(), "tools/call", req)
	require.NoError(t, err)
	assert.NotNil(t, res)

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)
	tool := spanNamed(t, spans, "mcp.tool/list_servers")
	child := spanNamed(t, spans, "grpc.call")
	assert.Equal(t, tool.SpanContext.SpanID(), child.Parent.SpanID())
	var persona string
	for _, a := range tool.Attributes {
		if a.Key == "mcp.persona" {
			persona = a.Value.AsString()
		}
	}
	assert.Equal(t, "admin", persona)
}

func TestToolCallMiddleware_RecordsErrors(t *testing.T) {
	tracer, exporter := testTracer(t)
	h := ToolCallMiddleware(tracer, noopLogger(), nil)(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return nil, errors.New("unauthenticated")
	})

	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Name: "start_deployment"}}
	_, err := h(context.Background(), "tools/call", req)
	require.Error(t, err)

	tool := spanNamed(t, exporter.GetSpans(), "mcp.tool/start_deployment")
	assert.Equal(t, codes.Error, tool.Status.Code)
}

func TestToolCallMiddleware_IgnoresOtherMethods(t *testing.T) {
	tracer, exporter := testTracer(t)
	called := false
	h := ToolCallMiddleware(tracer, noopLogger(), nil)(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		called = true
		return nil, nil
	})

	_, err := h(context.Background(), "tools/list", &mcp.ServerRequest[*mcp.ListToolsParams]{})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Empty(t, exporter.GetSpans())
}
