package main

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/grpcclient"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
)

// LeafLabClient wraps the leaflab-api gRPC client. Every call made through
// this client must carry the signed-in user's own access token via
// grpcauth.WithUserToken on the request context (NFR2) — see
// libs/go/htmxauth's Authenticator.WithAccessToken, which every protected
// route in main.go's setupRoutes is wrapped in.
type LeafLabClient struct {
	conn *grpcclient.Client
	api  leaflabapipb.LeafLabAPIClient
}

// NewLeafLabClient dials leaflab-api once at startup. extraOpts carries the
// grpcauth.NewUserTokenDialOption(...) dial option that forwards each
// request's user token.
func NewLeafLabClient(ctx context.Context, addr string, extraOpts ...grpc.DialOption) (*LeafLabClient, error) {
	conn, err := grpcclient.NewClient(ctx, addr, extraOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to leaflab-api: %w", err)
	}

	return &LeafLabClient{
		conn: conn,
		api:  leaflabapipb.NewLeafLabAPIClient(conn.GetConnection()),
	}, nil
}

// Close closes the gRPC connection.
func (c *LeafLabClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ListBoardsWithState lists every known board with its reporting state
// (FR4/FR5 of #1502's boards-list screen, handlers_boards.go).
func (c *LeafLabClient) ListBoardsWithState(ctx context.Context) (*leaflabapipb.ListBoardsWithStateResponse, error) {
	resp, err := c.api.ListBoardsWithState(ctx, &leaflabapipb.ListBoardsWithStateRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list boards: %w", err)
	}
	return resp, nil
}

// GetBoardDetail fetches one board's identity plus every sensor recorded
// for it, each with its own reporting state and (when present) most
// recent reading (FR6/FR7 of #1503's board-detail screen,
// handlers_boards.go). The error is returned wrapped with %w so a caller's
// status.FromError(err) still sees the underlying gRPC status (e.g.
// codes.NotFound for an unknown board_id, codes.Unauthenticated for a
// rejected token) -- status.FromError unwraps via errors.As.
func (c *LeafLabClient) GetBoardDetail(ctx context.Context, boardID int64) (*leaflabapipb.GetBoardDetailResponse, error) {
	resp, err := c.api.GetBoardDetail(ctx, &leaflabapipb.GetBoardDetailRequest{BoardId: boardID})
	if err != nil {
		return nil, fmt.Errorf("failed to get board detail for board %d: %w", boardID, err)
	}
	return resp, nil
}
