package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	grpcclient "github.com/whale-net/everything/libs/go/grpcclient"
	pb "github.com/whale-net/everything/manman/protos"
)

// GRPCClient wraps the manman gRPC API client
type GRPCClient struct {
	conn   *grpcclient.Client
	client pb.ManManAPIClient
}

// NewGRPCClient creates a new gRPC client connection using the shared grpcclient library.
// TLS is auto-configured from GRPC_* environment variables.
func NewGRPCClient(ctx context.Context, addr string) (*GRPCClient, error) {
	connCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpcclient.NewClient(connCtx, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to gRPC server at %s: %w", addr, err)
	}

	return &GRPCClient{
		conn:   conn,
		client: pb.NewManManAPIClient(conn.GetConnection()),
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	return c.conn.Close()
}

// ListActionDefinitions lists actions filtered by level and entity ID
func (c *GRPCClient) ListActionDefinitions(ctx context.Context, level string, entityID int64) ([]*pb.ActionDefinition, error) {
	slog.Debug("listing action definitions", "level", level, "entity_id", entityID)

	apiCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req := &pb.ListActionDefinitionsRequest{}
	switch level {
	case "game":
		req.GameId = &entityID
	case "game_config":
		req.ConfigId = &entityID
	case "server_game_config":
		req.SgcId = &entityID
	}

	resp, err := c.client.ListActionDefinitions(apiCtx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list action definitions: %w", err)
	}

	return resp.Actions, nil
}

// GetActionDefinition gets a single action with its input fields and options
func (c *GRPCClient) GetActionDefinition(ctx context.Context, actionID int64) (*pb.ActionDefinition, []*pb.ActionInputField, error) {
	slog.Debug("getting action definition", "action_id", actionID)

	apiCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := c.client.GetActionDefinition(apiCtx, &pb.GetActionDefinitionRequest{
		ActionId: actionID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get action definition: %w", err)
	}

	return resp.Action, resp.InputFields, nil
}

// CreateActionDefinition creates a new action definition with fields and options
func (c *GRPCClient) CreateActionDefinition(ctx context.Context, action *pb.ActionDefinition, fields []*pb.ActionInputField, options []*pb.ActionInputOption) (int64, error) {
	slog.Info("creating action definition", "name", action.Name, "level", action.DefinitionLevel)

	apiCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := c.client.CreateActionDefinition(apiCtx, &pb.CreateActionDefinitionRequest{
		Action:       action,
		InputFields:  fields,
		InputOptions: options,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create action definition: %w", err)
	}

	return resp.ActionId, nil
}

// DeleteActionDefinition deletes an action definition
func (c *GRPCClient) DeleteActionDefinition(ctx context.Context, actionID int64) error {
	slog.Info("deleting action definition", "action_id", actionID)

	apiCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := c.client.DeleteActionDefinition(apiCtx, &pb.DeleteActionDefinitionRequest{
		ActionId: actionID,
	})
	if err != nil {
		return fmt.Errorf("failed to delete action definition: %w", err)
	}

	return nil
}

// GetGame retrieves a game by ID
func (c *GRPCClient) GetGame(ctx context.Context, gameID int64) (*pb.Game, error) {
	apiCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := c.client.GetGame(apiCtx, &pb.GetGameRequest{GameId: gameID})
	if err != nil {
		return nil, fmt.Errorf("failed to get game: %w", err)
	}
	return resp.Game, nil
}

// GetGameConfig retrieves a game config by ID
func (c *GRPCClient) GetGameConfig(ctx context.Context, configID int64) (*pb.GameConfig, error) {
	apiCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := c.client.GetGameConfig(apiCtx, &pb.GetGameConfigRequest{ConfigId: configID})
	if err != nil {
		return nil, fmt.Errorf("failed to get game config: %w", err)
	}
	return resp.Config, nil
}

// GetServerGameConfig retrieves a server game config by ID
func (c *GRPCClient) GetServerGameConfig(ctx context.Context, sgcID int64) (*pb.ServerGameConfig, error) {
	apiCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := c.client.GetServerGameConfig(apiCtx, &pb.GetServerGameConfigRequest{ServerGameConfigId: sgcID})
	if err != nil {
		return nil, fmt.Errorf("failed to get server game config: %w", err)
	}
	return resp.Config, nil
}
