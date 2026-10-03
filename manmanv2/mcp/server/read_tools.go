package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Registry entries for the read tools; all are open to any authenticated gamer.
var (
	ListDeploymentsTool = Tool{Name: "list_deployments", MinPersona: PersonaGamer}
	GetDeploymentTool   = Tool{Name: "get_deployment", MinPersona: PersonaGamer, TargetArg: "deployment_id"}
	ListServersTool     = Tool{Name: "list_servers", MinPersona: PersonaGamer}
	GetServerTool       = Tool{Name: "get_server", MinPersona: PersonaGamer, TargetArg: "server_id"}
)

// ReadTools lists the read tools for registry construction.
var ReadTools = []Tool{ListDeploymentsTool, GetDeploymentTool, ListServersTool, GetServerTool}

// ReadAPI is the subset of the control API the read tools call.
type ReadAPI interface {
	ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error)
	GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error)
	ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error)
	GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error)
	ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error)
	GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error)
	GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error)
}

type portBindingOut struct {
	ContainerPort int32  `json:"container_port"`
	HostPort      int32  `json:"host_port"`
	Protocol      string `json:"protocol"`
}

type deploymentOut struct {
	ID            int64            `json:"id"`
	Name          string           `json:"name"`
	Game          string           `json:"game"`
	HostID        int64            `json:"host_id"`
	HostName      string           `json:"host_name"`
	Status        string           `json:"status"`
	SessionStatus string           `json:"session_status"`
	SessionID     int64            `json:"session_id,omitempty"`
	Image         string           `json:"image,omitempty"`
	PortBindings  []portBindingOut `json:"port_bindings,omitempty"`
}

type serverOut struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	PublicAddress string `json:"public_address"`
	DrainState    string `json:"drain_state,omitempty"`
}

type listDeploymentsOut struct {
	Deployments []deploymentOut `json:"deployments"`
}
type getDeploymentIn struct {
	DeploymentID int64 `json:"deployment_id" jsonschema:"the deployment (server game config) id"`
}
type listServersOut struct {
	Servers []serverOut `json:"servers"`
}
type getServerIn struct {
	ServerID int64 `json:"server_id" jsonschema:"the host server id"`
}

const noSession = "none"

// AddReadTools registers the deployment and server read tools on srv.
func AddReadTools(srv *mcp.Server, api ReadAPI) {
	r := &reader{api: api}
	mcp.AddTool(srv, &mcp.Tool{Name: ListDeploymentsTool.Name, Description: "List every deployment: id, name, game, host, and current session status."}, r.listDeployments)
	mcp.AddTool(srv, &mcp.Tool{Name: GetDeploymentTool.Name, Description: "Get one deployment: configuration summary, port bindings, and current session state."}, r.getDeployment)
	mcp.AddTool(srv, &mcp.Tool{Name: ListServersTool.Name, Description: "List every host: id, name, status, and public address."}, r.listServers)
	mcp.AddTool(srv, &mcp.Tool{Name: GetServerTool.Name, Description: "Get one host: id, name, status, and public address."}, r.getServer)
}

type reader struct{ api ReadAPI }

// toolErr maps a backend error to a tool error; NotFound gets a stable message.
func toolErr(what string, id int64, err error) error {
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("%s %d not found", what, id)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (r *reader) listServers(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listServersOut, error) {
	servers, err := r.allServers(ctx)
	if err != nil {
		return nil, listServersOut{}, fmt.Errorf("list servers: %w", err)
	}
	out := listServersOut{Servers: []serverOut{}}
	for _, s := range servers {
		out.Servers = append(out.Servers, toServerOut(s))
	}
	return nil, out, nil
}

func (r *reader) getServer(ctx context.Context, _ *mcp.CallToolRequest, in getServerIn) (*mcp.CallToolResult, serverOut, error) {
	resp, err := r.api.GetServer(ctx, &manmanpb.GetServerRequest{ServerId: in.ServerID})
	if err != nil {
		return nil, serverOut{}, toolErr("server", in.ServerID, err)
	}
	if resp.GetServer() == nil {
		return nil, serverOut{}, fmt.Errorf("server %d not found", in.ServerID)
	}
	return nil, toServerOut(resp.Server), nil
}

func (r *reader) listDeployments(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listDeploymentsOut, error) {
	servers, err := r.allServers(ctx)
	if err != nil {
		return nil, listDeploymentsOut{}, fmt.Errorf("list servers: %w", err)
	}
	hostNames := map[int64]string{}
	for _, s := range servers {
		hostNames[s.ServerId] = s.Name
	}
	live, err := r.liveSessions(ctx, 0)
	if err != nil {
		return nil, listDeploymentsOut{}, fmt.Errorf("list sessions: %w", err)
	}
	names := newNameCache(r.api)
	out := listDeploymentsOut{Deployments: []deploymentOut{}}
	for token := ""; ; {
		resp, err := r.api.ListServerGameConfigs(ctx, &manmanpb.ListServerGameConfigsRequest{PageSize: 100, PageToken: token})
		if err != nil {
			return nil, out, fmt.Errorf("list deployments: %w", err)
		}
		for _, c := range resp.Configs {
			d := r.build(ctx, names, c, hostNames[c.ServerId], live[c.ServerGameConfigId], false)
			out.Deployments = append(out.Deployments, d)
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	return nil, out, nil
}

func (r *reader) getDeployment(ctx context.Context, _ *mcp.CallToolRequest, in getDeploymentIn) (*mcp.CallToolResult, deploymentOut, error) {
	resp, err := r.api.GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{ServerGameConfigId: in.DeploymentID})
	if err != nil {
		return nil, deploymentOut{}, toolErr("deployment", in.DeploymentID, err)
	}
	c := resp.GetConfig()
	if c == nil {
		return nil, deploymentOut{}, fmt.Errorf("deployment %d not found", in.DeploymentID)
	}
	var hostName string
	if h, err := r.api.GetServer(ctx, &manmanpb.GetServerRequest{ServerId: c.ServerId}); err == nil && h.GetServer() != nil {
		hostName = h.Server.Name
	}
	live, err := r.liveSessions(ctx, in.DeploymentID)
	if err != nil {
		return nil, deploymentOut{}, fmt.Errorf("list sessions: %w", err)
	}
	return nil, r.build(ctx, newNameCache(r.api), c, hostName, live[c.ServerGameConfigId], true), nil
}

func (r *reader) build(ctx context.Context, names *nameCache, c *manmanpb.ServerGameConfig, host string, sess *manmanpb.Session, detail bool) deploymentOut {
	gc, game := names.resolve(ctx, c.GameConfigId)
	d := deploymentOut{
		ID: c.ServerGameConfigId, Name: gc.GetName(), Game: game, HostID: c.ServerId, HostName: host,
		Status: c.Status, SessionStatus: noSession,
	}
	if sess != nil {
		d.SessionStatus, d.SessionID = sess.Status, sess.SessionId
	}
	if detail {
		d.Image = gc.GetImage()
		for _, p := range c.PortBindings {
			d.PortBindings = append(d.PortBindings, portBindingOut{p.ContainerPort, p.HostPort, p.Protocol})
		}
	}
	return d
}

// liveSessions returns the live session per deployment; sgcID 0 means all.
func (r *reader) liveSessions(ctx context.Context, sgcID int64) (map[int64]*manmanpb.Session, error) {
	out := map[int64]*manmanpb.Session{}
	for token := ""; ; {
		resp, err := r.api.ListSessions(ctx, &manmanpb.ListSessionsRequest{ServerGameConfigId: sgcID, LiveOnly: true, PageSize: 100, PageToken: token})
		if err != nil {
			return nil, err
		}
		for _, s := range resp.Sessions {
			if _, seen := out[s.ServerGameConfigId]; !seen {
				out[s.ServerGameConfigId] = s
			}
		}
		if token = resp.NextPageToken; token == "" {
			return out, nil
		}
	}
}

func (r *reader) allServers(ctx context.Context) ([]*manmanpb.Server, error) {
	var all []*manmanpb.Server
	for token := ""; ; {
		resp, err := r.api.ListServers(ctx, &manmanpb.ListServersRequest{PageSize: 100, PageToken: token})
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Servers...)
		if token = resp.NextPageToken; token == "" {
			return all, nil
		}
	}
}

func toServerOut(s *manmanpb.Server) serverOut {
	return serverOut{ID: s.ServerId, Name: s.Name, Status: s.Status, PublicAddress: s.HostPublicAddress, DrainState: s.DrainState}
}

// nameCache resolves game config and game names once per call.
type nameCache struct {
	api     ReadAPI
	configs map[int64]*manmanpb.GameConfig
	games   map[int64]string
}

func newNameCache(api ReadAPI) *nameCache {
	return &nameCache{api: api, configs: map[int64]*manmanpb.GameConfig{}, games: map[int64]string{}}
}

func (n *nameCache) resolve(ctx context.Context, configID int64) (*manmanpb.GameConfig, string) {
	gc, ok := n.configs[configID]
	if !ok {
		if resp, err := n.api.GetGameConfig(ctx, &manmanpb.GetGameConfigRequest{ConfigId: configID}); err == nil {
			gc = resp.GetConfig()
		}
		n.configs[configID] = gc
	}
	if gc == nil {
		return nil, ""
	}
	g, ok := n.games[gc.GameId]
	if !ok {
		if resp, err := n.api.GetGame(ctx, &manmanpb.GetGameRequest{GameId: gc.GameId}); err == nil {
			g = resp.GetGame().GetName()
		}
		n.games[gc.GameId] = g
	}
	return gc, g
}
