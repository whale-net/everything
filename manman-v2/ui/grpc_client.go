package main

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/libs/go/grpcclient"
	manmanpb "github.com/whale-net/everything/manman/protos"
)

// ControlClient wraps the ManManAPI gRPC client
type ControlClient struct {
	conn *grpcclient.Client
	api  manmanpb.ManManAPIClient
}

// NewControlClient creates a new control API client
func NewControlClient(ctx context.Context, addr string) (*ControlClient, error) {
	conn, err := grpcclient.NewClient(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to control API: %w", err)
	}

	api := manmanpb.NewManManAPIClient(conn.GetConnection())

	return &ControlClient{
		conn: conn,
		api:  api,
	}, nil
}

// Close closes the gRPC connection
func (c *ControlClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// GetAPI returns the ManManAPI client
func (c *ControlClient) GetAPI() manmanpb.ManManAPIClient {
	return c.api
}

// Helper methods for common operations

// ListServers retrieves all servers
func (c *ControlClient) ListServers(ctx context.Context) ([]*manmanpb.Server, error) {
	resp, err := c.api.ListServers(ctx, &manmanpb.ListServersRequest{
		PageSize: 100, // Get all servers in one request for now
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}
	return resp.Servers, nil
}

// ListGames retrieves all games
func (c *ControlClient) ListGames(ctx context.Context) ([]*manmanpb.Game, error) {
	resp, err := c.api.ListGames(ctx, &manmanpb.ListGamesRequest{
		PageSize: 100,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list games: %w", err)
	}
	return resp.Games, nil
}

// GetGame retrieves a single game by ID
func (c *ControlClient) GetGame(ctx context.Context, gameID int64) (*manmanpb.Game, error) {
	resp, err := c.api.GetGame(ctx, &manmanpb.GetGameRequest{
		GameId: gameID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get game: %w", err)
	}
	return resp.Game, nil
}

// CreateGame creates a new game
func (c *ControlClient) CreateGame(ctx context.Context, name, steamAppID string, metadata *manmanpb.GameMetadata) (*manmanpb.Game, error) {
	resp, err := c.api.CreateGame(ctx, &manmanpb.CreateGameRequest{
		Name:       name,
		SteamAppId: steamAppID,
		Metadata:   metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create game: %w", err)
	}
	return resp.Game, nil
}

// UpdateGame updates an existing game
func (c *ControlClient) UpdateGame(ctx context.Context, gameID int64, name, steamAppID string, metadata *manmanpb.GameMetadata) (*manmanpb.Game, error) {
	resp, err := c.api.UpdateGame(ctx, &manmanpb.UpdateGameRequest{
		GameId:     gameID,
		Name:       name,
		SteamAppId: steamAppID,
		Metadata:   metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update game: %w", err)
	}
	return resp.Game, nil
}

// DeleteGame deletes a game
func (c *ControlClient) DeleteGame(ctx context.Context, gameID int64) error {
	_, err := c.api.DeleteGame(ctx, &manmanpb.DeleteGameRequest{
		GameId: gameID,
	})
	if err != nil {
		return fmt.Errorf("failed to delete game: %w", err)
	}
	return nil
}

// ListGameConfigs retrieves all game configs for a specific game
func (c *ControlClient) ListGameConfigs(ctx context.Context, gameID int64) ([]*manmanpb.GameConfig, error) {
	resp, err := c.api.ListGameConfigs(ctx, &manmanpb.ListGameConfigsRequest{
		GameId:   gameID,
		PageSize: 100,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list game configs: %w", err)
	}
	return resp.Configs, nil
}

// GetGameConfig retrieves a single game config by ID
func (c *ControlClient) GetGameConfig(ctx context.Context, configID int64) (*manmanpb.GameConfig, error) {
	resp, err := c.api.GetGameConfig(ctx, &manmanpb.GetGameConfigRequest{
		ConfigId: configID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get game config: %w", err)
	}
	return resp.Config, nil
}

// CreateGameConfig creates a new game config
func (c *ControlClient) CreateGameConfig(ctx context.Context, req *manmanpb.CreateGameConfigRequest) (*manmanpb.GameConfig, error) {
	resp, err := c.api.CreateGameConfig(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create game config: %w", err)
	}
	return resp.Config, nil
}

// UpdateGameConfig updates an existing game config
func (c *ControlClient) UpdateGameConfig(ctx context.Context, req *manmanpb.UpdateGameConfigRequest) (*manmanpb.GameConfig, error) {
	resp, err := c.api.UpdateGameConfig(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to update game config: %w", err)
	}
	return resp.Config, nil
}

// DeleteGameConfig deletes a game config
func (c *ControlClient) DeleteGameConfig(ctx context.Context, configID int64) error {
	_, err := c.api.DeleteGameConfig(ctx, &manmanpb.DeleteGameConfigRequest{
		ConfigId: configID,
	})
	if err != nil {
		return fmt.Errorf("failed to delete game config: %w", err)
	}
	return nil
}

// ListSessions retrieves sessions with optional filters
func (c *ControlClient) ListSessions(ctx context.Context, liveOnly bool) ([]*manmanpb.Session, error) {
	resp, err := c.api.ListSessions(ctx, &manmanpb.ListSessionsRequest{
		PageSize: 100,
		LiveOnly: liveOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	return resp.Sessions, nil
}

// ListSessionsWithFilters retrieves sessions with custom filters.
func (c *ControlClient) ListSessionsWithFilters(ctx context.Context, req *manmanpb.ListSessionsRequest) ([]*manmanpb.Session, error) {
	resp, err := c.api.ListSessions(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	return resp.Sessions, nil
}

// GetSession retrieves a single session by ID.
func (c *ControlClient) GetSession(ctx context.Context, req *manmanpb.GetSessionRequest) (*manmanpb.GetSessionResponse, error) {
	resp, err := c.api.GetSession(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}
	return resp, nil
}

// GetHistoricalLogs retrieves historical logs for a Server Game Config.
func (c *ControlClient) GetHistoricalLogs(ctx context.Context, req *manmanpb.GetHistoricalLogsRequest) (*manmanpb.GetHistoricalLogsResponse, error) {
	resp, err := c.api.GetHistoricalLogs(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get historical logs: %w", err)
	}
	return resp, nil
}

// StopSession stops a running session by ID.
func (c *ControlClient) StopSession(ctx context.Context, sessionID int64) (*manmanpb.Session, error) {
	resp, err := c.api.StopSession(ctx, &manmanpb.StopSessionRequest{
		SessionId: sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to stop session: %w", err)
	}
	return resp.Session, nil
}

// StartSession starts a new session for a server game config.
func (c *ControlClient) StartSession(ctx context.Context, serverGameConfigID int64, parameters map[string]string, force bool) (*manmanpb.Session, error) {
	resp, err := c.api.StartSession(ctx, &manmanpb.StartSessionRequest{
		ServerGameConfigId: serverGameConfigID,
		Parameters:         parameters,
		Force:              force,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	return resp.Session, nil
}

// ListConfigurationStrategies retrieves all strategies for a game.
func (c *ControlClient) ListConfigurationStrategies(ctx context.Context, req *manmanpb.ListConfigurationStrategiesRequest) (*manmanpb.ListConfigurationStrategiesResponse, error) {
	return c.api.ListConfigurationStrategies(ctx, req)
}

// ListServerGameConfigs retrieves server game configs for a server.
func (c *ControlClient) ListServerGameConfigs(ctx context.Context, serverID int64) ([]*manmanpb.ServerGameConfig, error) {
	resp, err := c.api.ListServerGameConfigs(ctx, &manmanpb.ListServerGameConfigsRequest{
		ServerId: serverID,
		PageSize: 100,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list server game configs: %w", err)
	}
	return resp.Configs, nil
}

// DeployGameConfig deploys a game config to a server.
func (c *ControlClient) DeployGameConfig(ctx context.Context, serverID, gameConfigID int64, parameters map[string]string) (*manmanpb.ServerGameConfig, error) {
	resp, err := c.api.DeployGameConfig(ctx, &manmanpb.DeployGameConfigRequest{
		ServerId:     serverID,
		GameConfigId: gameConfigID,
		Parameters:  parameters,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to deploy game config: %w", err)
	}
	return resp.Config, nil
}
