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
func (sm *SessionManager) RecoverOrphanedSessions(ctx context.Context, serverID int64) error {
	// List all containers with manman labels
	// Filter by server_id to only recover sessions for this host
	filters := map[string]string{
		"manman.wrapper":  "true",
		"manman.server_id": fmt.Sprintf("%d", serverID),
	}
	containers, err := sm.dockerClient.ListContainers(ctx, filters)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	for _, container := range containers {
		// Get container details to extract session_id
		status, err := sm.dockerClient.GetContainerStatus(ctx, container.ID)
		if err != nil {
			continue // Skip containers we can't inspect
		}

		// Extract session ID from container labels or name
		sessionID, err := extractSessionID(container.Name, status)
		if err != nil {
			continue // Skip containers without valid session ID
		}

		// Check if session already exists
		if _, exists := sm.stateManager.GetSession(sessionID); exists {
			continue // Already recovered
		}

		// Recover the session
		if err := sm.recoverSession(ctx, sessionID, container.ID, status); err != nil {
			fmt.Printf("Failed to recover session %d: %v\n", sessionID, err)
			continue
		}
	}

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

	// TODO: Extract from container labels if available
	// This would require inspecting the container to get labels

	return 0, fmt.Errorf("could not extract session ID from container name: %s", containerName)
}
