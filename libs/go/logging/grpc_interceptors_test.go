package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryServerLoggingInterceptor_LogsSuccessAndPassesThrough(t *testing.T) {
	var buf bytes.Buffer
	Configure(Config{ServiceName: "test", JSONFormat: true, Writer: &buf})
	buf.Reset()

	interceptor := NewUnaryServerLoggingInterceptor("grpc")
	wantResp := "ok"
	resp, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{FullMethod: "/svc/Method"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			return wantResp, nil
		})
	require.NoError(t, err)
	require.Equal(t, wantResp, resp)

	var m map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m))
	require.Equal(t, "grpc request", m["message"])
	require.Equal(t, "/svc/Method", m["grpc_method"])
	require.Equal(t, "OK", m["grpc_code"])
	require.Equal(t, "INFO", m["severity"])
}

func TestUnaryServerLoggingInterceptor_LogsErrorAsWarnOrError(t *testing.T) {
	var buf bytes.Buffer
	Configure(Config{ServiceName: "test", JSONFormat: true, Writer: &buf})
	buf.Reset()

	interceptor := NewUnaryServerLoggingInterceptor("grpc")
	wantErr := status.Error(codes.NotFound, "missing")
	_, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{FullMethod: "/svc/Method"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, wantErr
		})
	require.Equal(t, wantErr, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m))
	require.Equal(t, "NotFound", m["grpc_code"])
	require.Equal(t, "WARN", m["severity"])
	require.Equal(t, wantErr.Error(), m["error"])
}

func TestUnaryServerLoggingInterceptor_LogsInternalAsError(t *testing.T) {
	var buf bytes.Buffer
	Configure(Config{ServiceName: "test", JSONFormat: true, Writer: &buf})
	buf.Reset()

	interceptor := NewUnaryServerLoggingInterceptor("grpc")
	_, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{FullMethod: "/svc/Method"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, errors.New("boom")
		})
	require.Error(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m))
	require.Equal(t, "Unknown", m["grpc_code"])
	require.Equal(t, "ERROR", m["severity"])
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

func TestStreamServerLoggingInterceptor_LogsSuccess(t *testing.T) {
	var buf bytes.Buffer
	Configure(Config{ServiceName: "test", JSONFormat: true, Writer: &buf})
	buf.Reset()

	interceptor := NewStreamServerLoggingInterceptor("grpc")
	err := interceptor(nil, &fakeServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/svc/Stream"},
		func(srv interface{}, ss grpc.ServerStream) error {
			return nil
		})
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m))
	require.Equal(t, "/svc/Stream", m["grpc_method"])
	require.Equal(t, "OK", m["grpc_code"])
}
