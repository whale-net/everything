package session

import (
	"context"
	"fmt"
	"strconv"

	"github.com/whale-net/everything/manman"
)

// RecoverOrphanedSessions recovers sessions from existing game containers.
// It looks for game containers directly — no wrapper involved.
func (sm *SessionManager) RecoverOrphanedSessions(ctx context.Context, serverID int64) error {
	fmt.Printf("Starting orphan recovery for server %d\n", serverID)

	// 1. Find all game containers. Filter by server_id if the label is present
	// (containers created after this change); fall back to scanning all game
	// containers and skipping those belonging to other servers.
	gameFilters := map[string]string{
		"manman.type": "game",
	}
	games, err := sm.dockerClient.ListContainers(ctx, gameFilters)
	if err != nil {
		return fmt.Errorf("failed to list game containers: %w", err)
	}

	fmt.Printf("Found %d game containers\n", len(games))

	for _, game := range games {
		status, err := sm.dockerClient.GetContainerStatus(ctx, game.ID)
		if err != nil {
			fmt.Printf("Warning: Failed to get status for game container %s: %v\n", game.ID, err)
			continue
		}

		// If server_id label is present and doesn't match, skip
		if svrID, ok := status.Labels["manman.server_id"]; ok {
			if svrID != fmt.Sprintf("%d", serverID) {
				continue
			}
		}

		// Extract session ID and SGC ID from labels
		sessionID, sgcID, err := extractIDsFromLabels(status.Labels)
		if err != nil {
			fmt.Printf("Warning: Could not extract IDs from game container %s: %v, removing\n", game.ID, err)
			_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
			continue
		}

		// Skip if already tracked in state manager
		if _, exists := sm.stateManager.GetSession(sessionID); exists {
			fmt.Printf("Session %d (SGC %d) already tracked, skipping\n", sessionID, sgcID)
			continue
		}

		if status.Running {
			// Re-attach to running game container
			attachResp, err := sm.dockerClient.AttachToContainer(ctx, game.ID)
			if err != nil {
				fmt.Printf("Session %d: Failed to attach to running container %s: %v, removing\n", sessionID, game.ID, err)
				_ = sm.dockerClient.StopContainer(ctx, game.ID, nil)
				_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
				continue
			}

			networkName := fmt.Sprintf("session-%d", sessionID)
			state := &State{
				SessionID:       sessionID,
				SGCID:           sgcID,
				GameContainerID: game.ID,
				AttachResp:      &attachResp,
				NetworkName:     networkName,
				Status:          manman.SessionStatusRunning,
			}

			sm.stateManager.AddSession(state)
			sm.startOutputReader(state)
			fmt.Printf("Session %d (SGC %d): Successfully recovered, re-attached\n", sessionID, sgcID)
		} else {
			// Not running — nothing to recover, remove it
			fmt.Printf("Session %d (SGC %d): game container not running, removing\n", sessionID, sgcID)
			_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
		}
	}

	// 2. Clean up orphaned networks
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

// cleanupOrphanedNetworks removes networks that don't have any containers
func (sm *SessionManager) cleanupOrphanedNetworks(ctx context.Context, serverID int64) {
	// Note: Docker client may not support filtering networks by labels
	// This is a placeholder - implementing network cleanup requires
	// listing all networks and checking container membership
	fmt.Printf("TODO: Implement network cleanup for server %d\n", serverID)
}
