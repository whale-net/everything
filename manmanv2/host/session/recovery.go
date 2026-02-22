package session

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/whale-net/everything/manmanv2"
)

// RecoverOrphanedSessions recovers sessions from existing game containers.
// It looks for game containers directly — no wrapper involved.
func (sm *SessionManager) RecoverOrphanedSessions(ctx context.Context, serverID int64) error {
	slog.Info("starting orphan recovery", "server_id", serverID, "environment", sm.environment)

	// 1. Find all game containers. Filter by server_id and environment
	gameFilters := map[string]string{
		"manman.type":      "game",
		"manman.server_id": fmt.Sprintf("%d", serverID),
	}
	if sm.environment != "" {
		gameFilters["manman.environment"] = sm.environment
	}
	games, err := sm.dockerClient.ListContainers(ctx, gameFilters)
	if err != nil {
		return fmt.Errorf("failed to list game containers: %w", err)
	}

	slog.Info("found game containers", "count", len(games))

	for _, game := range games {
		status, err := sm.dockerClient.GetContainerStatus(ctx, game.ID)
		if err != nil {
			slog.Warn("failed to get status for game container", "container_id", game.ID, "error", err)
			continue
		}

		// Double check labels
		if svrID, ok := status.Labels["manman.server_id"]; !ok || svrID != fmt.Sprintf("%d", serverID) {
			continue
		}

		if sm.environment != "" {
			if env, ok := status.Labels["manman.environment"]; !ok || env != sm.environment {
				continue
			}
		} else {
			// If we don't have an environment set, skip containers that DO have one set
			if env, ok := status.Labels["manman.environment"]; ok && env != "" {
				continue
			}
		}

		// Extract session ID and SGC ID from labels
		sessionID, sgcID, err := extractIDsFromLabels(status.Labels)
		if err != nil {
			slog.Warn("could not extract IDs from game container, removing", "container_id", game.ID, "error", err)
			_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
			continue
		}

		// Skip if already tracked in state manager
		if _, exists := sm.stateManager.GetSession(sessionID); exists {
			slog.Debug("session already tracked, skipping", "session_id", sessionID, "sgc_id", sgcID)
			continue
		}

		if status.Running {
			// Re-attach to running game container using logs API
			logReader, err := sm.dockerClient.GetContainerLogs(ctx, game.ID, true, "all")
			if err != nil {
				slog.Warn("failed to get logs from running container, removing", "session_id", sessionID, "container_id", game.ID, "error", err)
				_ = sm.dockerClient.StopContainer(ctx, game.ID, nil)
				_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
				continue
			}

			networkName := sm.getNetworkName(sessionID)
			state := &State{
				SessionID:       sessionID,
				SGCID:           sgcID,
				GameContainerID: game.ID,
				LogReader:       logReader,
				AttachResp:      nil, // Will attach lazily when command is sent
				AttachStrategy:  "lazy",
				NetworkName:     networkName,
				Status:          manman.SessionStatusRunning,
			}

			sm.stateManager.AddSession(state)
			sm.startLogReader(state)
			slog.Info("session recovered", "session_id", sessionID, "sgc_id", sgcID)
		} else {
			// Not running — nothing to recover, remove it
			slog.Info("game container not running, removing", "session_id", sessionID, "sgc_id", sgcID)
			_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
		}
	}

	// 2. Clean up orphaned networks
	sm.cleanupOrphanedNetworks(ctx, serverID)

	slog.Info("orphan recovery completed")
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
	slog.Debug("TODO: implement network cleanup", "server_id", serverID)
}
