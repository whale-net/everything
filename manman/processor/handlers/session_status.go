package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
	"github.com/whale-net/everything/manman/host/rmq"
)

// SessionStatusHandler handles status.session.* messages
type SessionStatusHandler struct {
	repo      *repository.Repository
	publisher Publisher
	logger    *slog.Logger
}

// NewSessionStatusHandler creates a new session status handler
func NewSessionStatusHandler(repo *repository.Repository, publisher Publisher, logger *slog.Logger) *SessionStatusHandler {
	return &SessionStatusHandler{
		repo:      repo,
		publisher: publisher,
		logger:    logger,
	}
}

// Handle processes a session status update message
func (h *SessionStatusHandler) Handle(ctx context.Context, routingKey string, body []byte) error {
	var msg rmq.SessionStatusUpdate
	if err := json.Unmarshal(body, &msg); err != nil {
		h.logger.Error("failed to unmarshal session status message",
			"error", err,
			"routing_key", routingKey,
		)
		return &PermanentError{Err: err}
	}

	h.logger.Info("processing session status update",
		"session_id", msg.SessionID,
		"status", msg.Status,
		"routing_key", routingKey,
	)

	// Get current session to validate transition
	currentSession, err := h.repo.Sessions.Get(ctx, msg.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("session not found",
				"session_id", msg.SessionID,
				"status", msg.Status,
			)
			return &PermanentError{Err: fmt.Errorf("session %d not found", msg.SessionID)}
		}

		h.logger.Error("failed to get session",
			"error", err,
			"session_id", msg.SessionID,
		)
		return err // Transient error - retry
	}

	// Validate state transition
	if !isValidTransition(currentSession.Status, msg.Status) {
		h.logger.Warn("invalid session state transition",
			"session_id", msg.SessionID,
			"from", currentSession.Status,
			"to", msg.Status,
		)
		return &PermanentError{Err: fmt.Errorf("invalid transition from %s to %s", currentSession.Status, msg.Status)}
	}

	// Update session based on new status
	now := time.Now()
	switch msg.Status {
	case manman.SessionStatusRunning:
		err = h.repo.Sessions.UpdateSessionStart(ctx, msg.SessionID, now)
	case manman.SessionStatusStopped, manman.SessionStatusCrashed:
		err = h.repo.Sessions.UpdateSessionEnd(ctx, msg.SessionID, msg.Status, now, msg.ExitCode)
	default:
		err = h.repo.Sessions.UpdateStatus(ctx, msg.SessionID, msg.Status)
	}

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("session not found during update",
				"session_id", msg.SessionID,
				"status", msg.Status,
			)
			return &PermanentError{Err: fmt.Errorf("session %d not found", msg.SessionID)}
		}

		h.logger.Error("failed to update session status",
			"error", err,
			"session_id", msg.SessionID,
			"status", msg.Status,
		)
		return err // Transient error - retry
	}

	// Publish to external exchange for terminal states and running state
	if msg.Status == manman.SessionStatusRunning ||
		msg.Status == manman.SessionStatusStopped ||
		msg.Status == manman.SessionStatusCrashed {
		externalRoutingKey := fmt.Sprintf("manman.session.%s", msg.Status)
		if err := h.publisher.PublishExternal(ctx, externalRoutingKey, msg); err != nil {
			h.logger.Error("failed to publish session status to external exchange",
				"error", err,
				"session_id", msg.SessionID,
				"status", msg.Status,
			)
			// Don't fail the message processing if external publish fails
		}
	}

	h.logger.Info("session status updated successfully",
		"session_id", msg.SessionID,
		"status", msg.Status,
	)

	return nil
}

// isValidTransition checks if a session status transition is valid
func isValidTransition(from, to string) bool {
	// Define valid state machine transitions
	validTransitions := map[string][]string{
		manman.SessionStatusPending:  {manman.SessionStatusStarting, manman.SessionStatusCrashed},
		manman.SessionStatusStarting: {manman.SessionStatusRunning, manman.SessionStatusCrashed},
		manman.SessionStatusRunning:  {manman.SessionStatusStopping, manman.SessionStatusCrashed},
		manman.SessionStatusStopping: {manman.SessionStatusStopped, manman.SessionStatusCrashed},
		manman.SessionStatusStopped:  {}, // Terminal state
		manman.SessionStatusCrashed:  {}, // Terminal state
	}

	allowedStates, ok := validTransitions[from]
	if !ok {
		return false
	}

	// Allow staying in same state (idempotent updates)
	if from == to {
		return true
	}

	for _, allowed := range allowedStates {
		if allowed == to {
			return true
		}
	}

	return false
}
