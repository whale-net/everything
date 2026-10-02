package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

type fakeConnectAPI struct {
	configs map[int64]*manmanpb.ServerGameConfig
	hosts   map[int64]*manmanpb.Server
}

func (f fakeConnectAPI) GetServerGameConfig(_ context.Context, in *manmanpb.GetServerGameConfigRequest, _ ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	c, ok := f.configs[in.GetServerGameConfigId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such deployment")
	}
	return &manmanpb.GetServerGameConfigResponse{Config: c}, nil
}

func (f fakeConnectAPI) GetServer(_ context.Context, in *manmanpb.GetServerRequest, _ ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	s, ok := f.hosts[in.GetServerId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such host")
	}
	return &manmanpb.GetServerResponse{Server: s}, nil
}

func callConnectAddress(t *testing.T, api ConnectAddressAPI, id int64) (*mcp.CallToolResult, error) {
	t.Helper()
	reg := NewRegistry(ConnectAddressTool)
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "v0"}, nil)
	srv.AddReceivingMiddleware(Middleware(reg, LogAuditor{Logger: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}))
	AddConnectAddressTool(srv, api)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(HTTPAuth(fakeVerifier{"g": claims("u", "gamer")}, "")(h))
	defer ts.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearerRT{"g"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	return s.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_connect_address", Arguments: map[string]any{"deployment_id": id}})
}

func decodeOut(t *testing.T, res *mcp.CallToolResult) connectAddressOut {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out connectAddressOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGetConnectAddress(t *testing.T) {
	api := fakeConnectAPI{
		configs: map[int64]*manmanpb.ServerGameConfig{
			1: {ServerId: 10, PortBindings: []*manmanpb.PortBinding{{HostPort: 25565, Protocol: "TCP"}, {HostPort: 19132, Protocol: "UDP"}}},
			2: {ServerId: 10},
			3: {ServerId: 11, PortBindings: []*manmanpb.PortBinding{{HostPort: 25565, Protocol: "TCP"}}},
		},
		hosts: map[int64]*manmanpb.Server{
			10: {HostPublicAddress: "1.2.3.4"},
			11: {},
		},
	}

	t.Run("multi binding", func(t *testing.T) {
		res, err := callConnectAddress(t, api, 1)
		if err != nil || res.IsError {
			t.Fatalf("err=%v res=%+v", err, res)
		}
		out := decodeOut(t, res)
		want := []connectAddressEntry{
			{"1.2.3.4", 25565, "TCP", "1.2.3.4:25565"},
			{"1.2.3.4", 19132, "UDP", "1.2.3.4:19132"},
		}
		if !out.Available || len(out.Addresses) != 2 || out.Addresses[0] != want[0] || out.Addresses[1] != want[1] {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("no bindings", func(t *testing.T) {
		res, err := callConnectAddress(t, api, 2)
		if err != nil || res.IsError {
			t.Fatalf("err=%v", err)
		}
		out := decodeOut(t, res)
		if out.Available || out.Reason == "" || len(out.Addresses) != 0 {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("no public address", func(t *testing.T) {
		res, err := callConnectAddress(t, api, 3)
		if err != nil || res.IsError {
			t.Fatalf("err=%v", err)
		}
		out := decodeOut(t, res)
		if out.Available || out.Reason == "" || len(out.Addresses) != 0 {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("unknown deployment", func(t *testing.T) {
		res, err := callConnectAddress(t, api, 99)
		msg := ""
		if err != nil {
			msg = err.Error()
		} else if res.IsError {
			b, _ := json.Marshal(res.Content)
			msg = string(b)
		} else {
			t.Fatalf("expected error, got %+v", res)
		}
		if !strings.Contains(msg, "not found") {
			t.Fatalf("msg %q lacks not found", msg)
		}
	})
}
