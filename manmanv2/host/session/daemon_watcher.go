package session

import (
	"context"
	"log/slog"
	"time"

	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

const (
	daemonPingInterval = 5 * time.Second
	daemonPingTimeout  = 3 * time.Second
)

// WatchDaemon monitors Docker daemon health on an interval. When the daemon
// becomes unreachable it immediately calls markAllSessionsCrashed so that all
// in-memory sessions are evicted and their crashed events are published to
// RabbitMQ. It then continues pinging until the daemon comes back, logging the
// recovery. WatchDaemon blocks until ctx is cancelled and is intended to be
// run as a goroutine.
func (sm *SessionManager) WatchDaemon(ctx context.Context) {
	ticker := time.NewTicker(daemonPingInterval)
	defer ticker.Stop()

	daemonHealthy := true

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, daemonPingTimeout)
			err := sm.dockerClient.Ping(pingCtx)
			cancel()

			// If the parent context was cancelled, stop watching regardless of ping result.
			if ctx.Err() != nil {
				return
			}

			if err != nil {
				if daemonHealthy {
					daemonHealthy = false
					slog.Warn("Docker daemon unreachable, marking all sessions as crashed", "error", err)
					// Use a fresh background context so that RabbitMQ publish calls
					// complete even if the watcher context is being cancelled.
					sm.markAllSessionsCrashed(context.Background())
				}
			} else if !daemonHealthy {
				daemonHealthy = true
				slog.Info("Docker daemon reconnected")
			}
		}
	}
}

// markAllSessionsCrashed closes connections for every tracked session, publishes
// a crashed status event for each, and removes them all from the state manager.
// It is called when the Docker daemon disappears so that the API reflects the
// correct state without waiting for a user action.
func (sm *SessionManager) markAllSessionsCrashed(ctx context.Context) {
	sessions := sm.stateManager.ListSessions()
	if len(sessions) == 0 {
		return
	}

	slog.Warn("marking sessions as crashed due to daemon disconnect", "count", len(sessions))

	for _, state := range sessions {
		// Close the attach response (stdin pipe to the container).
		if state.AttachResp != nil {
			state.AttachResp.Close()
			state.AttachResp = nil
		}

		// Close the log reader stream.
		if state.LogReader != nil {
			_ = state.LogReader.Close()
			state.LogReader = nil
		}

		state.UpdateStatus("crashed")
		slog.Warn("session marked crashed due to daemon disconnect",
			"session_id", state.SessionID,
			"sgc_id", state.SGCID)

		// Publish crashed status so the API updates its database record.
		statusUpdate := &hostrmq.SessionStatusUpdate{
			SessionID: state.SessionID,
			SGCID:     state.SGCID,
			Status:    "crashed",
		}
		if err := sm.rmqPublisher.PublishSessionStatus(ctx, statusUpdate); err != nil {
			slog.Error("failed to publish crashed status for session",
				"session_id", state.SessionID,
				"error", err)
		}

		sm.stateManager.RemoveSession(state.SessionID)
		slog.Info("removed session from state manager after daemon disconnect",
			"session_id", state.SessionID)
	}
}
