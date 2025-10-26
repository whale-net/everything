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

// getRunningServers fetches the list of running servers for a user from Experience API
func (app *App) getRunningServers(ctx context.Context, userID string) ([]Server, error) {
	log.Printf("Getting running servers for user: %s", userID)

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

	gameServers, httpResp, err := client.DefaultAPI.GetGameServersGameserverGet(apiCtx).Execute()
	if err != nil {
		if httpResp != nil {
			log.Printf("API error: status=%d", httpResp.StatusCode)
		}
		return nil, fmt.Errorf("failed to get game servers: %w", err)
	}

	servers := make([]Server, 0, len(gameServers))
	for _, gs := range gameServers {
		// Get server ID and name
		serverID := strconv.Itoa(int(gs.GameServerConfigId))
		serverName := gs.Name

		servers = append(servers, Server{
			ID:     serverID,
			Name:   serverName,
			Status: "active", // GameServerConfig doesn't have status, assuming active
			IP:     "",       // Not available in config
			Port:   "",       // Not available in config
		})
	}

	return servers, nil
}
