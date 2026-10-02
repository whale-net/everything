package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

type fakeReadAPI struct{ ReadAPI }

func (fakeReadAPI) ListServers(context.Context, *manmanpb.ListServersRequest, ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{Servers: []*manmanpb.Server{{ServerId: 1, Name: "host-a", Status: "online", HostPublicAddress: "1.2.3.4"}}}, nil
}
func (fakeReadAPI) GetServer(_ context.Context, in *manmanpb.GetServerRequest, _ ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	if in.ServerId != 1 {
		return nil, status.Error(codes.NotFound, "nope")
	}
	return &manmanpb.GetServerResponse{Server: &manmanpb.Server{ServerId: 1, Name: "host-a", Status: "online", HostPublicAddress: "1.2.3.4"}}, nil
}
func (fakeReadAPI) ListServerGameConfigs(context.Context, *manmanpb.ListServerGameConfigsRequest, ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	return &manmanpb.ListServerGameConfigsResponse{Configs: []*manmanpb.ServerGameConfig{{ServerGameConfigId: 7, ServerId: 1, GameConfigId: 3, Status: "active"}}}, nil
}
func (fakeReadAPI) GetServerGameConfig(_ context.Context, in *manmanpb.GetServerGameConfigRequest, _ ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	if in.ServerGameConfigId != 7 {
		return nil, status.Error(codes.NotFound, "nope")
	}
	return &manmanpb.GetServerGameConfigResponse{Config: &manmanpb.ServerGameConfig{
		ServerGameConfigId: 7, ServerId: 1, GameConfigId: 3, Status: "active",
		PortBindings: []*manmanpb.PortBinding{{ContainerPort: 25565, HostPort: 30000, Protocol: "TCP"}},
	}}, nil
}
func (fakeReadAPI) ListSessions(context.Context, *manmanpb.ListSessionsRequest, ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	return &manmanpb.ListSessionsResponse{Sessions: []*manmanpb.Session{{SessionId: 9, ServerGameConfigId: 7, Status: "running"}}}, nil
}
func (fakeReadAPI) GetGameConfig(context.Context, *manmanpb.GetGameConfigRequest, ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return &manmanpb.GetGameConfigResponse{Config: &manmanpb.GameConfig{ConfigId: 3, GameId: 2, Name: "survival", Image: "mc:1"}}, nil
}
func (fakeReadAPI) GetGame(context.Context, *manmanpb.GetGameRequest, ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	return &manmanpb.GetGameResponse{Game: &manmanpb.Game{GameId: 2, Name: "Minecraft"}}, nil
}

func callRead(t *testing.T, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	AddReadTools(srv, fakeReadAPI{})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func resultText(r *mcp.CallToolResult) string {
	b, _ := json.Marshal(r.Content)
	return string(b)
}

func TestReadToolsHappyPath(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"list_servers", nil, []string{"host-a", "1.2.3.4"}},
		{"get_server", map[string]any{"server_id": 1}, []string{"host-a", "online"}},
		{"list_deployments", nil, []string{"survival", "Minecraft", "host-a", "running"}},
		{"get_deployment", map[string]any{"deployment_id": 7}, []string{"25565", "30000", "mc:1", "running"}},
	}
	for _, c := range cases {
		r := callRead(t, c.tool, c.args)
		if r.IsError {
			t.Fatalf("%s: unexpected error %s", c.tool, resultText(r))
		}
		s := resultText(r)
		for _, w := range c.want {
			if !strings.Contains(s, w) {
				t.Errorf("%s: output %s missing %q", c.tool, s, w)
			}
		}
	}
}

func TestReadToolsNotFound(t *testing.T) {
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"get_server", map[string]any{"server_id": 99}, "server 99 not found"},
		{"get_deployment", map[string]any{"deployment_id": 99}, "deployment 99 not found"},
	} {
		r := callRead(t, c.tool, c.args)
		if !r.IsError || !strings.Contains(resultText(r), c.want) {
			t.Errorf("%s: want tool error %q, got isError=%v %s", c.tool, c.want, r.IsError, resultText(r))
		}
	}
}
