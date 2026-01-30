package session

import (
	"context"
	"fmt"
	"strconv"

	"github.com/whale-net/everything/libs/go/docker"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/host/grpc"
	pb "github.com/whale-net/everything/manman/protos"
)

// RecoverOrphanedSessions recovers sessions from existing Docker containers
// following the Phase 4 orphan prevention strategy with SGC-based naming
func (sm *SessionManager) RecoverOrphanedSessions(ctx context.Context, serverID int64) error {
	fmt.Printf("Starting orphan recovery for server %d\n", serverID)

	// 1. Find all ManMan wrapper containers for this server
	wrapperFilters := map[string]string{
		"manman.type":      "wrapper",
		"manman.server_id": fmt.Sprintf("%d", serverID),
	}
	wrappers, err := sm.dockerClient.ListContainers(ctx, wrapperFilters)
	if err != nil {
		return fmt.Errorf("failed to list wrapper containers: %w", err)
	}

	fmt.Printf("Found %d wrapper containers\n", len(wrappers))

	// 2. For each wrapper, attempt to reconnect or clean up
	for _, wrapper := range wrappers {
		status, err := sm.dockerClient.GetContainerStatus(ctx, wrapper.ID)
		if err != nil {
			fmt.Printf("Warning: Failed to get status for wrapper %s: %v\n", wrapper.ID, err)
			continue
		}

		// Extract session ID and SGC ID from labels
		sessionID, sgcID, err := extractIDsFromLabels(status.Labels)
		if err != nil {
			fmt.Printf("Warning: Could not extract IDs from wrapper %s: %v, cleaning up\n", wrapper.ID, err)
			sm.cleanupDeadWrapper(ctx, wrapper.ID, status.Labels)
			continue
		}

		// Check if session already exists in memory
		if _, exists := sm.stateManager.GetSession(sessionID); exists {
			fmt.Printf("Session %d (SGC %d) already tracked, skipping\n", sessionID, sgcID)
			continue
		}

		// Try to reconnect to wrapper via gRPC if it's running
		if status.Running {
			// Extract network name from labels or use SGC-based pattern
			networkName := extractNetworkName(status.Labels, sessionID, sgcID, serverID)
			grpcAddress := fmt.Sprintf("%s:50051", networkName)

			grpcClient, err := grpcclient.NewClient(ctx, grpcAddress)
			if err == nil {
				// Wrapper is alive! Restore session state
				fmt.Printf("Session %d (SGC %d): Wrapper alive, reconnecting via gRPC\n", sessionID, sgcID)
				if err := sm.recoverLiveSession(ctx, sessionID, sgcID, wrapper.ID, status, grpcClient); err != nil {
					fmt.Printf("Session %d: Failed to recover: %v, cleaning up\n", sessionID, err)
					_ = grpcClient.Close()
					sm.cleanupDeadWrapper(ctx, wrapper.ID, status.Labels)
				}
				continue
			}
			fmt.Printf("Session %d: Wrapper running but gRPC unreachable: %v, cleaning up\n", sessionID, err)
		} else {
			fmt.Printf("Session %d: Wrapper not running (status: %s), cleaning up\n", sessionID, status.Status)
		}

		// Wrapper is dead, stopped, or unreachable - clean up
		sm.cleanupDeadWrapper(ctx, wrapper.ID, status.Labels)
	}

	// 3. Find and clean up orphaned game containers (no wrapper)
	gameFilters := map[string]string{
		"manman.type": "game",
	}
	games, err := sm.dockerClient.ListContainers(ctx, gameFilters)
	if err != nil {
		fmt.Printf("Warning: Failed to list game containers: %v\n", err)
	} else {
		sm.cleanupOrphanedGames(ctx, games, serverID)
	}

	// 4. Clean up orphaned networks
	sm.cleanupOrphanedNetworks(ctx, serverID)

	fmt.Println("Orphan recovery completed")
	return nil
}

// extractIDsFromLabels extracts session ID and SGC ID from container labels
func extractIDsFromLabels(labels map[string]string) (sessionID int64, sgcID int64, err error) {
	sessionIDStr, hasSessionID := labels["manman.session_id"]
	sgcIDStr, hasSGCID := labels["manman.sgc_id"]

	if !hasSessionID || !hasSGCID {
		return 0, 0, fmt.Errorf("missing required labels (session_id: %v, sgc_id: %v)", hasSessionID, hasSGCID)
	}

	sessionID, err = strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid session_id: %v", err)
	}

	sgcID, err = strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid sgc_id: %v", err)
	}

	return sessionID, sgcID, nil
}

// extractNetworkName extracts network name from labels or constructs it
func extractNetworkName(labels map[string]string, sessionID, sgcID, serverID int64) string {
	// For now, use session-based pattern
	// TODO: Consider if network naming should also be SGC-based
	return fmt.Sprintf("session-%d", sessionID)
}

// recoverLiveSession recovers a session where the wrapper is still alive and reachable
func (sm *SessionManager) recoverLiveSession(ctx context.Context, sessionID, sgcID int64, wrapperContainerID string, status *docker.ContainerStatus, grpcClient *grpcclient.Client) error {
	// Extract network info
	networkID := ""
	networkName := extractNetworkName(status.Labels, sessionID, sgcID, 0)

	// Create session state
	state := &State{
		SessionID:          sessionID,
		SGCID:              sgcID,
		WrapperContainerID: wrapperContainerID,
		NetworkID:          networkID,
		NetworkName:        networkName,
		Status:             manman.SessionStatusRunning,
		GRPCClient:         grpcClient,
		WrapperClient:      grpc.NewWrapperControlClient(grpcClient),
	}

	// Query wrapper for current game status
	statusReq := &pb.GetStatusRequest{
		SessionId: sessionID,
	}
	statusResp, err := state.WrapperClient.GetStatus(ctx, statusReq)
	if err == nil {
		state.Status = statusResp.Status
		state.GameContainerID = statusResp.ContainerId
		if statusResp.ExitCode != 0 {
			exitCode := int(statusResp.ExitCode)
			state.ExitCode = &exitCode
		}
	} else {
		fmt.Printf("Session %d: Warning - wrapper connected but GetStatus failed: %v\n", sessionID, err)
	}

	// Add to state manager
	sm.stateManager.AddSession(state)
	fmt.Printf("Session %d (SGC %d): Successfully recovered live session\n", sessionID, sgcID)
	return nil
}

// cleanupDeadWrapper cleans up a dead or unreachable wrapper and its associated containers
func (sm *SessionManager) cleanupDeadWrapper(ctx context.Context, wrapperID string, labels map[string]string) {
	fmt.Printf("Cleaning up dead wrapper %s\n", wrapperID)

	// Extract SGC ID to find associated game containers
	sgcIDStr, ok := labels["manman.sgc_id"]
	if ok {
		// Find associated game containers by SGC ID
		gameFilters := map[string]string{
			"manman.type":   "game",
			"manman.sgc_id": sgcIDStr,
		}
		games, err := sm.dockerClient.ListContainers(ctx, gameFilters)
		if err != nil {
			fmt.Printf("Warning: Failed to list game containers for sgc_id=%s: %v\n", sgcIDStr, err)
		} else {
			for _, game := range games {
				gameStatus, err := sm.dockerClient.GetContainerStatus(ctx, game.ID)
				if err != nil {
					continue
				}
				if gameStatus.Running {
					fmt.Printf("Stopping orphaned game container %s\n", game.ID)
					_ = sm.dockerClient.StopContainer(ctx, game.ID, nil)
				}
				fmt.Printf("Removing orphaned game container %s\n", game.ID)
				_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
			}
		}
	}

	// Remove wrapper container
	_ = sm.dockerClient.StopContainer(ctx, wrapperID, nil)
	_ = sm.dockerClient.RemoveContainer(ctx, wrapperID, true)
	fmt.Printf("Removed wrapper %s\n", wrapperID)
}

// cleanupOrphanedGames cleans up game containers that have no associated wrapper
func (sm *SessionManager) cleanupOrphanedGames(ctx context.Context, games []docker.ContainerStatus, serverID int64) {
	// Get active SGC IDs from session manager
	activeSGCs := sm.stateManager.GetActiveSGCIDs()

	for _, game := range games {
		status, err := sm.dockerClient.GetContainerStatus(ctx, game.ID)
		if err != nil {
			continue
		}

		// Check if this game's SGC is tracked by an active session
		sgcIDStr, ok := status.Labels["manman.sgc_id"]
		if !ok {
			fmt.Printf("Game container %s missing sgc_id label, skipping\n", game.ID)
			continue
		}

		sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
		if err != nil {
			continue
		}

		// If SGC is active, skip (wrapper should exist)
		if activeSGCs[sgcID] {
			continue
		}

		// Orphaned game container - no active session tracking it
		fmt.Printf("Found orphaned game container %s (SGC %d), cleaning up\n", game.ID, sgcID)
		if status.Running {
			_ = sm.dockerClient.StopContainer(ctx, game.ID, nil)
		}
		_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
	}
}

// cleanupOrphanedNetworks removes networks that don't have any containers
func (sm *SessionManager) cleanupOrphanedNetworks(ctx context.Context, serverID int64) {
	// Note: Docker client may not support filtering networks by labels
	// This is a placeholder - implementing network cleanup requires
	// listing all networks and checking container membership
	fmt.Printf("TODO: Implement network cleanup for server %d\n", serverID)
}
