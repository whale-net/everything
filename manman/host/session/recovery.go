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
// following the Phase 4 orphan prevention strategy
func (sm *SessionManager) RecoverOrphanedSessions(ctx context.Context, serverID int64) error {
	fmt.Printf("Starting orphan recovery for server %d\n", serverID)

	// 1. Find all ManMan wrapper containers for this server
	filters := map[string]string{
		"manman.type":      "wrapper",
		"manman.server_id": fmt.Sprintf("%d", serverID),
	}
	wrappers, err := sm.dockerClient.ListContainers(ctx, filters)
	if err != nil {
		return fmt.Errorf("failed to list wrapper containers: %w", err)
	}

	fmt.Printf("Found %d wrapper containers\n", len(wrappers))

	// 2. For each wrapper, attempt to reconnect or clean up
	for _, wrapper := range wrappers {
		// Get container status
		status, err := sm.dockerClient.GetContainerStatus(ctx, wrapper.ID)
		if err != nil {
			fmt.Printf("Warning: Failed to get status for wrapper %s: %v\n", wrapper.ID, err)
			continue
		}

		// Extract session ID from container labels or name
		sessionID, err := extractSessionIDFromLabels(status.Labels)
		if err != nil {
			sessionID, err = extractSessionID(wrapper.Name, status)
			if err != nil {
				fmt.Printf("Warning: Could not extract session ID from wrapper %s, skipping\n", wrapper.ID)
				continue
			}
		}

		// Check if session already exists
		if _, exists := sm.stateManager.GetSession(sessionID); exists {
			fmt.Printf("Session %d already recovered, skipping\n", sessionID)
			continue
		}

		// Try to reconnect to wrapper via gRPC
		if status.Running {
			networkName := fmt.Sprintf("session-%d", sessionID)
			grpcAddress := fmt.Sprintf("%s:50051", networkName)

			grpcClient, err := grpcclient.NewClient(ctx, grpcAddress)
			if err == nil {
				// Wrapper is alive! Restore session state
				fmt.Printf("Session %d: Wrapper is alive, reconnecting\n", sessionID)
				if err := sm.recoverLiveSession(ctx, sessionID, wrapper.ID, status, grpcClient); err != nil {
					fmt.Printf("Session %d: Failed to recover live session: %v\n", sessionID, err)
					_ = grpcClient.Close()
				}
				continue
			}
			fmt.Printf("Session %d: Wrapper container running but gRPC unreachable: %v\n", sessionID, err)
		}

		// Wrapper is dead or unreachable - clean up orphans
		fmt.Printf("Session %d: Wrapper is dead, cleaning up orphans\n", sessionID)
		sm.cleanupOrphanedSession(ctx, sessionID, wrapper.ID)
	}

	// 3. Clean up orphaned networks
	sm.cleanupOrphanedNetworks(ctx, serverID)

	fmt.Println("Orphan recovery completed")
	return nil
}

// recoverSession recovers a single session from a wrapper container
func (sm *SessionManager) recoverSession(ctx context.Context, sessionID int64, wrapperContainerID string, status *docker.ContainerStatus) error {
	// Create session state
	state := &State{
		SessionID:         sessionID,
		WrapperContainerID: wrapperContainerID,
		Status:            manman.SessionStatusRunning, // Assume running if container is running
	}

	if !status.Running {
		state.Status = manman.SessionStatusStopped
		if status.ExitCode != 0 {
			state.ExitCode = &status.ExitCode
			state.Status = manman.SessionStatusCrashed
		}
	}

	// Try to reconnect gRPC client
	// Extract network name from container or use default pattern
	networkName := fmt.Sprintf("session-%d", sessionID)
	grpcAddress := fmt.Sprintf("%s:50051", networkName)

	grpcClient, err := grpcclient.NewClient(ctx, grpcAddress)
	if err != nil {
		// If we can't connect, mark as lost but keep state
		fmt.Printf("Warning: Could not reconnect to wrapper for session %d: %v\n", sessionID, err)
		state.Status = manman.SessionStatusCrashed
	} else {
		state.GRPCClient = grpcClient
		state.WrapperClient = grpc.NewWrapperControlClient(grpcClient)

		// Try to get status from wrapper
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
		}
	}

	sm.stateManager.AddSession(state)
	return nil
}

// extractSessionIDFromLabels extracts session ID from container labels
func extractSessionIDFromLabels(labels map[string]string) (int64, error) {
	if sessionIDStr, ok := labels["manman.session_id"]; ok {
		sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid session_id in labels: %v", err)
		}
		return sessionID, nil
	}
	return 0, fmt.Errorf("manman.session_id label not found")
}

// extractSessionID extracts session ID from container name or labels
func extractSessionID(containerName string, status *docker.ContainerStatus) (int64, error) {
	// Docker returns container names with leading slash (e.g., /wrapper-123)
	// Strip leading slash if present
	name := containerName
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}

	// Try to extract from name pattern: wrapper-{session_id}
	if len(name) > 8 && name[:8] == "wrapper-" {
		sessionIDStr := name[8:]
		sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
		if err == nil {
			return sessionID, nil
		}
	}

	return 0, fmt.Errorf("could not extract session ID from container name: %s", containerName)
}

// recoverLiveSession recovers a session where the wrapper is still alive
func (sm *SessionManager) recoverLiveSession(ctx context.Context, sessionID int64, wrapperContainerID string, status *docker.ContainerStatus, grpcClient *grpcclient.Client) error {
	// Extract SGCID from labels
	sgcID := int64(0)
	if sgcIDStr, ok := status.Labels["manman.sgc_id"]; ok {
		if parsed, err := strconv.ParseInt(sgcIDStr, 10, 64); err == nil {
			sgcID = parsed
		}
	}

	// Create session state
	state := &State{
		SessionID:          sessionID,
		SGCID:              sgcID,
		WrapperContainerID: wrapperContainerID,
		Status:             manman.SessionStatusRunning,
		GRPCClient:         grpcClient,
		WrapperClient:      grpc.NewWrapperControlClient(grpcClient),
	}

	// Try to get status from wrapper
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
	}

	// Add to state manager
	sm.stateManager.AddSession(state)
	fmt.Printf("Session %d: Successfully recovered live session\n", sessionID)
	return nil
}

// cleanupOrphanedSession cleans up containers for an orphaned session
func (sm *SessionManager) cleanupOrphanedSession(ctx context.Context, sessionID int64, wrapperContainerID string) {
	// Find orphaned game containers for this session
	gameFilters := map[string]string{
		"manman.type":       "game",
		"manman.session_id": fmt.Sprintf("%d", sessionID),
	}
	gameContainers, err := sm.dockerClient.ListContainers(ctx, gameFilters)
	if err != nil {
		fmt.Printf("Session %d: Failed to list game containers: %v\n", sessionID, err)
	} else {
		for _, gameContainer := range gameContainers {
			gameStatus, err := sm.dockerClient.GetContainerStatus(ctx, gameContainer.ID)
			if err != nil {
				fmt.Printf("Session %d: Failed to get game container status: %v\n", sessionID, err)
				continue
			}

			if gameStatus.Running {
				fmt.Printf("Session %d: Orphaned game container %s is running, terminating\n", sessionID, gameContainer.ID)
				_ = sm.dockerClient.StopContainer(ctx, gameContainer.ID, nil)
			}
			fmt.Printf("Session %d: Removing game container %s\n", sessionID, gameContainer.ID)
			_ = sm.dockerClient.RemoveContainer(ctx, gameContainer.ID, true)
		}
	}

	// Remove wrapper container
	fmt.Printf("Session %d: Removing wrapper container %s\n", sessionID, wrapperContainerID)
	_ = sm.dockerClient.StopContainer(ctx, wrapperContainerID, nil)
	_ = sm.dockerClient.RemoveContainer(ctx, wrapperContainerID, true)
}

// cleanupOrphanedNetworks removes networks that don't have any containers
func (sm *SessionManager) cleanupOrphanedNetworks(ctx context.Context, serverID int64) {
	// Note: Docker client may not support filtering networks by labels
	// This is a placeholder for the cleanup logic
	// In practice, you'd need to list all networks and filter manually
	fmt.Printf("TODO: Implement network cleanup for server %d\n", serverID)
}
