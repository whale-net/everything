package main

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/libs/go/grpcclient"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// ControlClient wraps the ManManAPI gRPC client
type ControlClient struct {
	conn     *grpcclient.Client
	api      manmanpb.ManManAPIClient
	workshop manmanpb.WorkshopServiceClient
}

// NewControlClient creates a new control API client
func NewControlClient(ctx context.Context, addr string) (*ControlClient, error) {
	conn, err := grpcclient.NewClient(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to control API: %w", err)
	}

	api := manmanpb.NewManManAPIClient(conn.GetConnection())
	workshop := manmanpb.NewWorkshopServiceClient(conn.GetConnection())

	return &ControlClient{
		conn:     conn,
		api:      api,
		workshop: workshop,
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

// SendInput sends stdin input to a running session
func (c *ControlClient) SendInput(ctx context.Context, sessionID int64, input []byte) (*manmanpb.SendInputResponse, error) {
	resp, err := c.api.SendInput(ctx, &manmanpb.SendInputRequest{
		SessionId: sessionID,
		Input:     input,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send input: %w", err)
	}
	return resp, nil
}

// GetSessionActions retrieves available actions for a session
func (c *ControlClient) GetSessionActions(ctx context.Context, sessionID int64) ([]*manmanpb.ActionDefinition, error) {
	resp, err := c.api.GetSessionActions(ctx, &manmanpb.GetSessionActionsRequest{
		SessionId: sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get session actions: %w", err)
	}
	return resp.Actions, nil
}

// ExecuteAction executes an action on a session
func (c *ControlClient) ExecuteAction(ctx context.Context, sessionID, actionID int64, inputValues map[string]string) (*manmanpb.ExecuteActionResponse, error) {
	resp, err := c.api.ExecuteAction(ctx, &manmanpb.ExecuteActionRequest{
		SessionId:   sessionID,
		ActionId:    actionID,
		InputValues: inputValues,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute action: %w", err)
	}
	return resp, nil
}

// CreateActionDefinition creates a new action definition
func (c *ControlClient) CreateActionDefinition(ctx context.Context, action *manmanpb.ActionDefinition, fields []*manmanpb.ActionInputField, options []*manmanpb.ActionInputOption) (int64, error) {
	resp, err := c.api.CreateActionDefinition(ctx, &manmanpb.CreateActionDefinitionRequest{
		Action:       action,
		InputFields:  fields,
		InputOptions: options,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create action definition: %w", err)
	}
	return resp.ActionId, nil
}

// UpdateActionDefinition updates an existing action definition
func (c *ControlClient) UpdateActionDefinition(ctx context.Context, action *manmanpb.ActionDefinition, fields []*manmanpb.ActionInputField, options []*manmanpb.ActionInputOption) error {
	_, err := c.api.UpdateActionDefinition(ctx, &manmanpb.UpdateActionDefinitionRequest{
		Action:       action,
		InputFields:  fields,
		InputOptions: options,
	})
	if err != nil {
		return fmt.Errorf("failed to update action definition: %w", err)
	}
	return nil
}

// DeleteActionDefinition deletes an action definition
func (c *ControlClient) DeleteActionDefinition(ctx context.Context, actionID int64) error {
	_, err := c.api.DeleteActionDefinition(ctx, &manmanpb.DeleteActionDefinitionRequest{
		ActionId: actionID,
	})
	if err != nil {
		return fmt.Errorf("failed to delete action definition: %w", err)
	}
	return nil
}

// ListActionDefinitions lists action definitions filtered by level
func (c *ControlClient) ListActionDefinitions(ctx context.Context, gameID, configID, sgcID *int64) ([]*manmanpb.ActionDefinition, error) {
	req := &manmanpb.ListActionDefinitionsRequest{}
	if gameID != nil {
		req.GameId = gameID
	}
	if configID != nil {
		req.ConfigId = configID
	}
	if sgcID != nil {
		req.SgcId = sgcID
	}

	resp, err := c.api.ListActionDefinitions(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list action definitions: %w", err)
	}
	return resp.Actions, nil
}

// GetActionDefinition gets a single action definition with its input fields
func (c *ControlClient) GetActionDefinition(ctx context.Context, actionID int64) (*manmanpb.ActionDefinition, []*manmanpb.ActionInputField, error) {
	resp, err := c.api.GetActionDefinition(ctx, &manmanpb.GetActionDefinitionRequest{
		ActionId: actionID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get action definition: %w", err)
	}
	return resp.Action, resp.InputFields, nil
}

// Workshop addon methods

func (c *ControlClient) ListWorkshopAddons(ctx context.Context, offset, limit int32, gameID int64) ([]*manmanpb.WorkshopAddon, error) {
	resp, err := c.workshop.ListAddons(ctx, &manmanpb.ListAddonsRequest{
		Offset: offset,
		Limit:  limit,
		GameId: gameID,
	})
	if err != nil {
		return nil, err
	}
	return resp.Addons, nil
}

func (c *ControlClient) GetWorkshopAddon(ctx context.Context, addonID int64) (*manmanpb.WorkshopAddon, error) {
	resp, err := c.workshop.GetAddon(ctx, &manmanpb.GetAddonRequest{
		AddonId: addonID,
	})
	if err != nil {
		return nil, err
	}
	return resp.Addon, nil
}

func (c *ControlClient) ListWorkshopInstallations(ctx context.Context, sgcID int64) ([]*manmanpb.WorkshopInstallation, error) {
	resp, err := c.workshop.ListInstallations(ctx, &manmanpb.ListInstallationsRequest{
		SgcId: sgcID,
	})
	if err != nil {
		return nil, err
	}
	return resp.Installations, nil
}

func (c *ControlClient) InstallAddon(ctx context.Context, sgcID, addonID int64, forceReinstall bool) (*manmanpb.WorkshopInstallation, error) {
	resp, err := c.workshop.InstallAddon(ctx, &manmanpb.InstallAddonRequest{
		SgcId:          sgcID,
		AddonId:        addonID,
		ForceReinstall: forceReinstall,
	})
	if err != nil {
		return nil, err
	}
	return resp.Installation, nil
}

func (c *ControlClient) RemoveInstallation(ctx context.Context, installationID int64) error {
	_, err := c.workshop.RemoveInstallation(ctx, &manmanpb.RemoveInstallationRequest{
		InstallationId: installationID,
	})
	return err
}

func (c *ControlClient) FetchAddonMetadata(ctx context.Context, gameID int64, workshopID, platformType string) (*manmanpb.WorkshopAddon, error) {
	resp, err := c.workshop.FetchAddonMetadata(ctx, &manmanpb.FetchAddonMetadataRequest{
		GameId:       gameID,
		WorkshopId:   workshopID,
		PlatformType: platformType,
	})
	if err != nil {
		return nil, err
	}
	return resp.Addon, nil
}

// Library management methods

func (c *ControlClient) ListLibraries(ctx context.Context, limit, offset int32, gameID int64) ([]*manmanpb.WorkshopLibrary, error) {
	resp, err := c.workshop.ListLibraries(ctx, &manmanpb.ListLibrariesRequest{
		GameId: gameID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	return resp.Libraries, nil
}

func (c *ControlClient) GetLibrary(ctx context.Context, libraryID int64) (*manmanpb.WorkshopLibrary, error) {
	resp, err := c.workshop.GetLibrary(ctx, &manmanpb.GetLibraryRequest{
		LibraryId: libraryID,
	})
	if err != nil {
		return nil, err
	}
	return resp.Library, nil
}

func (c *ControlClient) CreateLibrary(ctx context.Context, gameID int64, name, description string) (*manmanpb.WorkshopLibrary, error) {
	resp, err := c.workshop.CreateLibrary(ctx, &manmanpb.CreateLibraryRequest{
		GameId:      gameID,
		Name:        name,
		Description: description,
	})
	if err != nil {
		return nil, err
	}
	return resp.Library, nil
}

func (c *ControlClient) DeleteLibrary(ctx context.Context, libraryID int64) error {
	_, err := c.workshop.DeleteLibrary(ctx, &manmanpb.DeleteLibraryRequest{
		LibraryId: libraryID,
	})
	return err
}

func (c *ControlClient) DeleteAddon(ctx context.Context, addonID int64) error {
	_, err := c.workshop.DeleteAddon(ctx, &manmanpb.DeleteAddonRequest{
		AddonId: addonID,
	})
	return err
}

func (c *ControlClient) AddAddonToLibrary(ctx context.Context, libraryID, addonID int64) error {
	_, err := c.workshop.AddAddonToLibrary(ctx, &manmanpb.AddAddonToLibraryRequest{
		LibraryId: libraryID,
		AddonId:   addonID,
	})
	return err
}

func (c *ControlClient) RemoveAddonFromLibrary(ctx context.Context, libraryID, addonID int64) error {
	_, err := c.workshop.RemoveAddonFromLibrary(ctx, &manmanpb.RemoveAddonFromLibraryRequest{
		LibraryId: libraryID,
		AddonId:   addonID,
	})
	return err
}

func (c *ControlClient) AddLibraryReference(ctx context.Context, parentID, childID int64) error {
	_, err := c.workshop.AddLibraryReference(ctx, &manmanpb.AddLibraryReferenceRequest{
		ParentLibraryId: parentID,
		ChildLibraryId:  childID,
	})
	return err
}

// GetLibraryAddons returns addons in a library
func (c *ControlClient) GetLibraryAddons(ctx context.Context, libraryID int64) ([]*manmanpb.WorkshopAddon, error) {
	resp, err := c.workshop.GetLibraryAddons(ctx, &manmanpb.GetLibraryAddonsRequest{
		LibraryId: libraryID,
	})
	if err != nil {
		return nil, err
	}
	return resp.Addons, nil
}

// GetChildLibraries returns child libraries
func (c *ControlClient) GetChildLibraries(ctx context.Context, libraryID int64) ([]*manmanpb.WorkshopLibrary, error) {
	resp, err := c.workshop.GetChildLibraries(ctx, &manmanpb.GetChildLibrariesRequest{
		LibraryId: libraryID,
	})
	if err != nil {
		return nil, err
	}
	return resp.Libraries, nil
}
