package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/manman/api/repository"
	"github.com/whale-net/everything/manman/host/rmq"
)

// HostStatusHandler handles status.host.* messages
type HostStatusHandler struct {
	repo      *repository.Repository
	publisher Publisher
	logger    *slog.Logger
}

// NewHostStatusHandler creates a new host status handler
func NewHostStatusHandler(repo *repository.Repository, publisher Publisher, logger *slog.Logger) *HostStatusHandler {
	return &HostStatusHandler{
		repo:      repo,
		publisher: publisher,
		logger:    logger,
	}
}

// Handle processes a host status update message
func (h *HostStatusHandler) Handle(ctx context.Context, routingKey string, body []byte) error {
	var msg rmq.HostStatusUpdate
	if err := json.Unmarshal(body, &msg); err != nil {
		h.logger.Error("failed to unmarshal host status message",
			"error", err,
			"routing_key", routingKey,
		)
		return &PermanentError{Err: err}
	}

	h.logger.Info("processing host status update",
		"server_id", msg.ServerID,
		"status", msg.Status,
		"routing_key", routingKey,
	)

	// Update server status and last_seen
	now := time.Now()
	err := h.repo.Servers.UpdateStatusAndLastSeen(ctx, msg.ServerID, msg.Status, now)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("server not found",
				"server_id", msg.ServerID,
				"status", msg.Status,
			)
			return &PermanentError{Err: fmt.Errorf("server %d not found", msg.ServerID)}
		}

		h.logger.Error("failed to update server status",
			"error", err,
			"server_id", msg.ServerID,
		)
		return err // Transient error - retry
	}

	// Publish to external exchange
	externalRoutingKey := fmt.Sprintf("manman.host.%s", msg.Status)
	if err := h.publisher.PublishExternal(ctx, externalRoutingKey, msg); err != nil {
		h.logger.Error("failed to publish host status to external exchange",
			"error", err,
			"server_id", msg.ServerID,
			"status", msg.Status,
		)
		// Don't fail the message processing if external publish fails
		// The internal state is already updated
	}

	h.logger.Info("host status updated successfully",
		"server_id", msg.ServerID,
		"status", msg.Status,
	)

	return nil
}
