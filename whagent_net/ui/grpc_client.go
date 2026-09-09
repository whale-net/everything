package main

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/grpcclient"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
)

// SessionClient wraps whagentpb.SessionServiceClient -- the same
// SessionService `mcp` dials (whagent_net/mcp/main.go). No session pages
// exist yet (this task's scope, issue #2236), so this has no domain
// methods beyond the raw client; later tasks (FR1-FR4) add wrapper
// methods here the way manmanv2/ui/grpc_client.go's ControlClient does.
type SessionClient struct {
	conn   *grpcclient.Client
	client whagentpb.SessionServiceClient
}

// NewSessionClient dials `api`'s gRPC address. extraOpts must include a
// grpcauth user-token dial option so every RPC forwards the signed-in
// operator's own access token (see NewApp's userAuthOpt) -- this
// constructor takes no auth stance of its own.
func NewSessionClient(ctx context.Context, addr string, extraOpts ...grpc.DialOption) (*SessionClient, error) {
	conn, err := grpcclient.NewClient(ctx, addr, extraOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to api: %w", err)
	}

	return &SessionClient{
		conn:   conn,
		client: whagentpb.NewSessionServiceClient(conn.GetConnection()),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *SessionClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Client returns the raw whagentpb.SessionServiceClient for handlers that
// need it directly.
func (c *SessionClient) Client() whagentpb.SessionServiceClient {
	return c.client
}
