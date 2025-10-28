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

// getCurrentServers fetches the list of currently running server instances from Experience API
// Now includes crashed instances to show servers that recently failed
func (app *App) getCurrentServers(ctx context.Context, userID string) ([]experience_api.GameServerInstance, error) {
	log.Printf("Getting current servers for user: %s", userID)

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

	instances, err := app.getCurrentServers(ctx, userID)
	if err != nil {
		return nil, err
	}

	servers := make([]Server, 0, len(instances))
	for _, inst := range instances {
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

		servers = append(servers, Server{
			ID:         serverID,
			InstanceID: instanceID,
			Name:       fmt.Sprintf("Server Config %s", serverID),
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
