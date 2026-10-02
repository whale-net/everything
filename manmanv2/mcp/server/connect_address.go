package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/manmanv2/connectaddr"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// ConnectAddressTool is declared at gamer: connect addresses are player-facing.
var ConnectAddressTool = Tool{Name: "get_connect_address", MinPersona: PersonaGamer, TargetArg: "deployment_id"}

// ConnectAddressAPI is the slice of the control API get_connect_address needs.
type ConnectAddressAPI interface {
	GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error)
	GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error)
}

type connectAddressIn struct {
	DeploymentID int64 `json:"deployment_id" jsonschema:"the deployment (server game config) id"`
}

type connectAddressEntry struct {
	IP       string `json:"ip"`
	Port     int32  `json:"port"`
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
}

type connectAddressOut struct {
	Available bool                  `json:"available"`
	Reason    string                `json:"reason,omitempty"`
	Addresses []connectAddressEntry `json:"addresses,omitempty"`
}

// AddConnectAddressTool registers get_connect_address on srv. Backend calls
// carry the caller's token via the request context.
func AddConnectAddressTool(srv *mcp.Server, api ConnectAddressAPI) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        ConnectAddressTool.Name,
		Description: "Get the ip:port connect address(es) for a deployment, or an explicit unavailable result with a reason.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in connectAddressIn) (*mcp.CallToolResult, connectAddressOut, error) {
		cfg, err := api.GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{ServerGameConfigId: in.DeploymentID})
		if err != nil {
			return nil, connectAddressOut{}, backendErr("deployment", in.DeploymentID, err)
		}
		sgc := cfg.GetConfig()
		if sgc == nil {
			return nil, connectAddressOut{}, fmt.Errorf("deployment %d not found", in.DeploymentID)
		}
		srvResp, err := api.GetServer(ctx, &manmanpb.GetServerRequest{ServerId: sgc.GetServerId()})
		if err != nil {
			return nil, connectAddressOut{}, backendErr("host", sgc.GetServerId(), err)
		}
		res := connectaddr.Derive(srvResp.GetServer().GetHostPublicAddress(), sgc.GetPortBindings())
		if res.Unavailable {
			return nil, connectAddressOut{Available: false, Reason: res.Reason}, nil
		}
		out := connectAddressOut{Available: true, Addresses: make([]connectAddressEntry, 0, len(res.Entries))}
		for _, e := range res.Entries {
			out.Addresses = append(out.Addresses, connectAddressEntry{IP: e.IP, Port: e.Port, Protocol: e.Protocol, Address: e.Address()})
		}
		return nil, out, nil
	})
}

func backendErr(what string, id int64, err error) error {
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("%s %d not found", what, id)
	}
	return fmt.Errorf("fetching %s %d: %w", what, id, err)
}
