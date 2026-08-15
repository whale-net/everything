package main

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/grpcclient"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// RegistryClient wraps the typed gRPC clients this UI uses against
// app-registry-api. Every call forwards the logged-in user's own access
// token via libs/go/grpcauth (userAuthOpt, set up in NewApp) — the UI holds
// no service account credentials of its own for registry data (FR-40).
//
// EnvironmentRegistry, AppRegistry, and PromotionRegistry are wired up (the
// dashboard/deployments-matrix screens, #643, need identity + state reads
// from all three). ArtifactRegistry is added when a later screen-work issue
// needs it.
type RegistryClient struct {
	conn        *grpcclient.Client
	Environment pb.EnvironmentRegistryClient
	App         pb.AppRegistryClient
	Promotion   pb.PromotionRegistryClient
}

// NewRegistryClient dials app-registry-api at address. userAuthOpt must be
// a grpcauth per-RPC credentials dial option that reads the user's token
// from the outgoing call's context (see grpcauth.WithUserToken /
// NewUserTokenDialOption).
func NewRegistryClient(ctx context.Context, address string, userAuthOpt grpc.DialOption) (*RegistryClient, error) {
	conn, err := grpcclient.NewClient(ctx, address, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to app-registry-api at %s: %w", address, err)
	}
	return &RegistryClient{
		conn:        conn,
		Environment: pb.NewEnvironmentRegistryClient(conn.GetConnection()),
		App:         pb.NewAppRegistryClient(conn.GetConnection()),
		Promotion:   pb.NewPromotionRegistryClient(conn.GetConnection()),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *RegistryClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
