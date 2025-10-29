package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/whale-net/everything/generated/go/manman/experience_api"
)

// getActiveWorkerID fetches the active worker ID for a user from Experience API
func (app *App) getActiveWorkerID(ctx context.Context, userID string) (string, error) {
	log.Printf("Getting active worker ID for user: %s", userID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	// Create a fresh context with timeout to avoid using the potentially cancelled HTTP request context
	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	worker, httpResp, err := client.DefaultAPI.WorkerCurrentWorkerCurrentGet(apiCtx).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
			// Log response body for debugging datetime parsing issues
			if httpResp.StatusCode == 200 {
				log.Printf("Response succeeded but unmarsh failed - possible datetime format issue")
			}
		}
		return "", fmt.Errorf("failed to get worker: %w", err)
	}

	if worker == nil {
		return "", nil
	}

	return strconv.Itoa(int(worker.WorkerId)), nil
}

// getWorkerStatus fetches the status information for the current worker
func (app *App) getWorkerStatus(ctx context.Context, userID string) (*experience_api.ExternalStatusInfo, error) {
	log.Printf("Getting worker status for user: %s", userID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	// Create a fresh context with timeout
	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, httpResp, err := client.DefaultAPI.GetWorkerStatusWorkerStatusGet(apiCtx).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			log.Printf("No worker status found (404)")
			return nil, nil
		}
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get worker status: %w", err)
	}

	return status, nil
}

// getCurrentServersWithConfigs fetches the full response including instances and configs
func (app *App) getCurrentServersWithConfigs(ctx context.Context, userID string) (*experience_api.CurrentInstanceResponse, error) {
	log.Printf("Getting current servers with configs for user: %s", userID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	// Create a fresh context with timeout
	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use the active instances endpoint with include_crashed=true to get both
	// active and recently crashed instances
	resp, httpResp, err := client.DefaultAPI.GetActiveGameServerInstancesGameserverInstancesActiveGet(apiCtx).
		IncludeCrashed(true).
		Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get current servers: %w", err)
	}

	return resp, nil
}

// getCurrentServers fetches the list of currently running server instances from Experience API
// Now includes crashed instances to show servers that recently failed
func (app *App) getCurrentServers(ctx context.Context, userID string) ([]experience_api.GameServerInstance, error) {
	resp, err := app.getCurrentServersWithConfigs(ctx, userID)
	if err != nil {
		return nil, err
	}
	return resp.GameServerInstances, nil
}

// getServerStatus fetches the status information for a specific game server by config ID
func (app *App) getServerStatus(ctx context.Context, serverConfigID int32) (*experience_api.ExternalStatusInfo, error) {
	log.Printf("Getting status for server config ID: %d", serverConfigID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	// Create a fresh context with timeout
	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, httpResp, err := client.DefaultAPI.GetGameServerStatusGameserverIdStatusGet(apiCtx, serverConfigID).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			log.Printf("No status found for server config %d (404)", serverConfigID)
			return nil, nil
		}
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get server status: %w", err)
	}

	return status, nil
}

// getRunningServers fetches the list of running servers for a user from Experience API
// This is kept for backwards compatibility but now uses the current servers endpoint
func (app *App) getRunningServers(ctx context.Context, userID string) ([]Server, error) {
	log.Printf("Getting running servers for user: %s", userID)

	resp, err := app.getCurrentServersWithConfigs(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Build a map of config ID to config name
	configNames := make(map[int32]string)
	for _, config := range resp.Configs {
		configNames[config.GameServerConfigId] = config.Name
	}

	servers := make([]Server, 0, len(resp.GameServerInstances))
	for _, inst := range resp.GameServerInstances {
		// Get the config details for this instance
		serverID := strconv.Itoa(int(inst.GameServerConfigId))

		// Determine if instance is active or crashed based on end_date
		// EndDate is NullableTime - if not set (nil), instance is active
		isActive := !inst.EndDate.IsSet() || inst.EndDate.Get() == nil
		statusStr := "active"
		statusType := "active"

		if !isActive {
			// Instance has ended - it's crashed
			statusStr = "crashed"
			statusType = "crashed"
		} else {
			// Try to get more detailed status for active instances
			status, statusErr := app.getServerStatus(ctx, inst.GameServerConfigId)
			if statusErr == nil && status != nil {
				statusType = string(status.StatusType)
				statusStr = statusType
			}
		}

		// Get instance details
		instanceID := strconv.Itoa(int(inst.GameServerInstanceId))

		// Get the name from the config map, or fall back to a default
		name := configNames[inst.GameServerConfigId]
		if name == "" {
			name = fmt.Sprintf("Server Config %s", serverID)
		}

		servers = append(servers, Server{
			ID:         serverID,
			InstanceID: instanceID,
			Name:       name,
			Status:     statusStr,
			StatusType: statusType,
			IP:         "", // Not available in current API
			Port:       "", // Not available in current API
		})
	}

	return servers, nil
}

// getAllGameServerConfigs fetches all available game server configurations
func (app *App) getAllGameServerConfigs(ctx context.Context) ([]experience_api.GameServerConfig, error) {
	log.Printf("Getting all game server configs")

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	// Create a fresh context with timeout
	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	configs, httpResp, err := client.DefaultAPI.GetGameServersGameserverGet(apiCtx).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get game server configs: %w", err)
	}

	return configs, nil
}

// getAllGameServers fetches all game server types
func (app *App) getAllGameServers(ctx context.Context) ([]experience_api.GameServer, error) {
	log.Printf("Getting all game server types")

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	// Create a fresh context with timeout
	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	servers, httpResp, err := client.DefaultAPI.ListGameServersGameserverTypesGet(apiCtx).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get game server types: %w", err)
	}

	return servers, nil
}

// startGameServer starts a game server by config ID
func (app *App) startGameServer(ctx context.Context, configID int32) error {
	log.Printf("Starting game server with config ID: %d", configID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	// Create a fresh context with timeout
	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, httpResp, err := client.DefaultAPI.StartGameServerGameserverIdStartPost(apiCtx, configID).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return fmt.Errorf("failed to start game server: %w", err)
	}

	return nil
}

// getInstanceDetails fetches instance details with commands
func (app *App) getInstanceDetails(ctx context.Context, instanceID int) (*experience_api.InstanceDetailsResponseWithCommands, error) {
	log.Printf("Getting instance details for instance ID: %d", instanceID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	details, httpResp, err := client.DefaultAPI.GetInstanceDetailsGameserverInstanceInstanceIdGet(apiCtx, int32(instanceID)).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get instance details: %w", err)
	}

	return details, nil
}

// executeInstanceCommand executes a command on an instance
func (app *App) executeInstanceCommand(ctx context.Context, instanceID int, request experience_api.ExecuteCommandRequest) (*experience_api.ExecuteCommandResponse, error) {
	log.Printf("Executing command on instance %d: type=%s, id=%d", instanceID, request.CommandType, request.CommandId)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, httpResp, err := client.DefaultAPI.ExecuteInstanceCommandGameserverInstanceInstanceIdCommandPost(apiCtx, int32(instanceID)).ExecuteCommandRequest(request).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to execute command: %w", err)
	}

	return response, nil
}

// getAvailableCommands fetches available commands for a game server
func (app *App) getAvailableCommands(ctx context.Context, gameServerID int) ([]experience_api.GameServerCommand, error) {
	log.Printf("Getting available commands for game server ID: %d", gameServerID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	commands, httpResp, err := client.DefaultAPI.GetAvailableCommandsGameserverGameServerIdCommandsGet(apiCtx, int32(gameServerID)).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get available commands: %w", err)
	}

	return commands, nil
}

// getGameServerCommands fetches commands for a game server type
func (app *App) getGameServerCommands(ctx context.Context, gameServerID int32) ([]experience_api.GameServerCommand, error) {
	log.Printf("Getting commands for game server type ID: %d", gameServerID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	commands, httpResp, err := client.DefaultAPI.GetAvailableCommandsGameserverGameServerIdCommandsGet(apiCtx, gameServerID).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get game server commands: %w", err)
	}

	return commands, nil
}

// getGameServerInstanceHistory fetches instance history for a game server
func (app *App) getGameServerInstanceHistory(ctx context.Context, gameServerID int32, limit int) ([]InstanceHistoryItem, error) {
	log.Printf("Getting instance history for game server ID: %d", gameServerID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	historyResp, httpResp, err := client.DefaultAPI.GetGameServerInstanceHistoryGameserverGameServerIdInstancesGet(apiCtx, gameServerID).Limit(int32(limit)).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get instance history: %w", err)
	}

	// Convert the response to our struct (the API returns interface{} so we need to parse it)
	// For now, return empty list - we'll fix this when the OpenAPI spec is corrected
	log.Printf("Instance history response: %+v", historyResp)
	return []InstanceHistoryItem{}, nil
}

// createGameServerCommand creates a new command for a game server type
func (app *App) createGameServerCommand(ctx context.Context, gameServerID int32, name, command, description string, isVisible bool) (*experience_api.GameServerCommand, error) {
	log.Printf("Creating command for game server type ID: %d", gameServerID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build request
	request := experience_api.CreateGameServerCommandRequest{
		Name:      name,
		Command:   command,
		IsVisible: &isVisible,
	}
	if description != "" {
		request.Description = *experience_api.NewNullableString(&description)
	}

	cmd, httpResp, err := client.DefaultAPI.CreateGameServerCommandGameserverTypesGameServerIdCommandPost(apiCtx, gameServerID).CreateGameServerCommandRequest(request).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to create game server command: %w", err)
	}

	return cmd, nil
}

// createConfigCommand creates a new config-specific command
func (app *App) createConfigCommand(ctx context.Context, configID int, request experience_api.CreateConfigCommandRequest) (*experience_api.GameServerConfigCommands, error) {
	log.Printf("Creating config command for config ID: %d", configID)

	cfg := experience_api.NewConfiguration()
	cfg.Servers = experience_api.ServerConfigurations{
		experience_api.ServerConfiguration{
			URL: app.config.ExperienceAPIURL,
		},
	}
	client := experience_api.NewAPIClient(cfg)

	apiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	command, httpResp, err := client.DefaultAPI.CreateConfigCommandGameserverConfigConfigIdCommandPost(apiCtx, int32(configID)).CreateConfigCommandRequest(request).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to create config command: %w", err)
	}

	return command, nil
}
