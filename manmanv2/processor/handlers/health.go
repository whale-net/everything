package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/host/rmq"
)

// HealthHandler handles health.* messages (heartbeats)
type HealthHandler struct {
	repo                *repository.Repository
	publisher           Publisher
	logger              *slog.Logger
	staleCheckTicker    *time.Ticker
	staleCheckDone      chan bool
	staleHostThreshold  int
}

// NewHealthHandler creates a new health handler
func NewHealthHandler(repo *repository.Repository, publisher Publisher, staleHostThreshold int, logger *slog.Logger) *HealthHandler {
	return &HealthHandler{
		repo:               repo,
		publisher:          publisher,
		logger:             logger,
		staleCheckDone:     make(chan bool),
		staleHostThreshold: staleHostThreshold,
	}
}

// StartStaleHostChecker starts a background goroutine to check for stale hosts
func (h *HealthHandler) StartStaleHostChecker(ctx context.Context) {
	h.staleCheckTicker = time.NewTicker(60 * time.Second)

	go func() {
		h.logger.Info("started stale host checker", "interval", "60s", "threshold_seconds", h.staleHostThreshold)

		for {
			select {
			case <-h.staleCheckDone:
				h.logger.Info("stopping stale host checker")
				return
			case <-ctx.Done():
				h.logger.Info("stopping stale host checker (context cancelled)")
				return
			case <-h.staleCheckTicker.C:
				h.checkStaleHosts(ctx)
			}
		}
	}()
}

// Stop stops the stale host checker
func (h *HealthHandler) Stop() {
	if h.staleCheckTicker != nil {
		h.staleCheckTicker.Stop()
		close(h.staleCheckDone)
	}
}

// checkStaleHosts finds and marks stale hosts as offline
func (h *HealthHandler) checkStaleHosts(ctx context.Context) {
	staleServers, err := h.repo.Servers.ListStaleServers(ctx, h.staleHostThreshold)
	if err != nil {
		h.logger.Error("failed to list stale servers", "error", err)
		return
	}

	if len(staleServers) == 0 {
		return
	}

	h.logger.Warn("detected stale hosts", "count", len(staleServers))

	serverIDs := make([]int64, len(staleServers))
	for i, server := range staleServers {
		serverIDs[i] = server.ServerID
		h.logger.Warn("marking server as stale",
			"server_id", server.ServerID,
			"server_name", server.Name,
			"last_seen", server.LastSeen,
		)
	}

	// Mark all stale servers as offline
	if err := h.repo.Servers.MarkServersOffline(ctx, serverIDs); err != nil {
		h.logger.Error("failed to mark servers offline", "error", err)
		return
	}

	// Publish stale host events to external exchange
	for _, server := range staleServers {
		staleEvent := rmq.HostStatusUpdate{
			ServerID: server.ServerID,
			Status:   manman.ServerStatusOffline,
		}

		if err := h.publisher.PublishExternal(ctx, "manman.host.stale", staleEvent); err != nil {
			h.logger.Error("failed to publish stale host event",
				"error", err,
				"server_id", server.ServerID,
			)
		}
	}

	h.logger.Info("marked stale servers as offline", "count", len(staleServers))
}

// Handle processes a health heartbeat message
func (h *HealthHandler) Handle(ctx context.Context, routingKey string, body []byte) error {
	var msg rmq.HealthUpdate
	if err := json.Unmarshal(body, &msg); err != nil {
		h.logger.Error("failed to unmarshal health message",
			"error", err,
			"routing_key", routingKey,
		)
		return &PermanentError{Err: err}
	}

	h.logger.Debug("processing health heartbeat",
		"server_id", msg.ServerID,
		"routing_key", routingKey,
	)

	// Log session statistics if provided (debug level — heartbeats are frequent)
	if msg.SessionStats != nil {
		h.logger.Debug("session statistics",
			"server_id", msg.ServerID,
			"total", msg.SessionStats.Total,
			"running", msg.SessionStats.Running,
			"pending", msg.SessionStats.Pending,
			"stopped", msg.SessionStats.Stopped,
			"crashed", msg.SessionStats.Crashed,
		)
	}

	// Update last_seen timestamp
	now := time.Now()
	err := h.repo.Servers.UpdateLastSeen(ctx, msg.ServerID, now)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("server not found",
				"server_id", msg.ServerID,
			)
			return &PermanentError{Err: fmt.Errorf("server %d not found", msg.ServerID)}
		}

		h.logger.Error("failed to update server last_seen",
			"error", err,
			"server_id", msg.ServerID,
		)
		return err // Transient error - retry
	}

	return nil
}
