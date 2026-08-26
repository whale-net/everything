package main

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/grpcclient"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// LeafLabClient wraps the typed gRPC client this UI uses against
// leaflab-api. Every call forwards the logged-in user's own access
// token via libs/go/grpcauth (userAuthOpt, set up in NewApp) — the UI holds
// no service account credentials of its own for leaflab data (FR-40).
type LeafLabClient struct {
	conn  *grpcclient.Client
	API   pb.LeafLabAPIClient
}

// NewLeafLabClient dials leaflab-api at address. userAuthOpt must be
// a grpcauth per-RPC credentials dial option that reads the user's token
// from the outgoing call's context (see grpcauth.WithUserToken /
// NewUserTokenDialOption).
func NewLeafLabClient(ctx context.Context, address string, userAuthOpt grpc.DialOption) (*LeafLabClient, error) {
	conn, err := grpcclient.NewClient(ctx, address, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to leaflab-api at %s: %w", address, err)
	}
	return &LeafLabClient{
		conn:  conn,
		API:   pb.NewLeafLabAPIClient(conn.GetConnection()),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *LeafLabClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
