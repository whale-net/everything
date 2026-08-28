package main

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/grpcclient"
)

// APIClient wraps the typed gRPC client this UI uses against leaflab-api.
// Every call forwards the logged-in user's own access token via
// libs/go/grpcauth (userAuthOpt, set up in NewApp) — the UI holds no
// service account credentials of its own for leaflab domain data
// (NFR18.1).
type APIClient struct {
	conn    *grpcclient.Client
	LeafLab pb.LeafLabAPIClient
}

// NewAPIClient dials leaflab-api at address. userAuthOpt must be a
// grpcauth per-RPC credentials dial option that reads the user's token
// from the outgoing call's context (see grpcauth.WithUserToken /
// NewUserTokenDialOption).
func NewAPIClient(ctx context.Context, address string, userAuthOpt grpc.DialOption) (*APIClient, error) {
	conn, err := grpcclient.NewClient(ctx, address, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to leaflab-api at %s: %w", address, err)
	}
	return &APIClient{
		conn:    conn,
		LeafLab: pb.NewLeafLabAPIClient(conn.GetConnection()),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *APIClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
