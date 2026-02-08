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
		if err == nil {
			// If a session becomes running, mark all other active sessions for this SGC as stopped.
			// This handles "force start" scenarios and prevents multiple "running" sessions in DB.
			if stopErr := h.repo.Sessions.StopOtherSessionsForSGC(ctx, msg.SessionID, msg.SGCID); stopErr != nil {
				h.logger.Error("failed to stop other sessions for SGC",
					"error", stopErr,
					"session_id", msg.SessionID,
					"sgc_id", msg.SGCID,
				)
				// Don't fail the update if this cleanup fails, but log it.
			}
		}
	case manman.SessionStatusStopped, manman.SessionStatusCrashed, manman.SessionStatusLost:
		err = h.repo.Sessions.UpdateSessionEnd(ctx, msg.SessionID, msg.Status, now, msg.ExitCode)
		// Deallocate ports when session enters terminal state
		if err == nil {
			if deallocErr := h.repo.ServerPorts.DeallocatePortsBySessionID(ctx, msg.SessionID); deallocErr != nil {
				h.logger.Error("failed to deallocate ports for stopped/crashed session",
					"error", deallocErr,
					"session_id", msg.SessionID,
					"status", msg.Status,
				)
				// Don't fail the message - ports can be cleaned up later
			} else {
				h.logger.Info("deallocated ports for terminal session",
					"session_id", msg.SessionID,
					"status", msg.Status,
				)
			}
		}
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
		msg.Status == manman.SessionStatusCrashed ||
		msg.Status == manman.SessionStatusLost {
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

// StartStaleSessionChecker starts a background goroutine to check for stale sessions
func (h *SessionStatusHandler) StartStaleSessionChecker(ctx context.Context, checkInterval, staleThreshold time.Duration) {
	ticker := time.NewTicker(checkInterval)
	go func() {
		defer ticker.Stop()
		h.logger.Info("starting stale session checker", "interval", checkInterval, "threshold", staleThreshold)

		for {
			select {
			case <-ctx.Done():
				h.logger.Info("stopping stale session checker")
				return
			case <-ticker.C:
				if err := h.checkStaleSessions(ctx, staleThreshold); err != nil {
					h.logger.Error("failed to check stale sessions", "error", err)
				}
			}
		}
	}()
}

func (h *SessionStatusHandler) checkStaleSessions(ctx context.Context, threshold time.Duration) error {
	sessions, err := h.repo.Sessions.GetStaleSessions(ctx, threshold)
	if err != nil {
		return fmt.Errorf("failed to get stale sessions: %w", err)
	}

	if len(sessions) == 0 {
		return nil
	}

	h.logger.Info("found stale sessions", "count", len(sessions))

	for _, session := range sessions {
		h.logger.Warn("marking stale session as lost",
			"session_id", session.SessionID,
			"status", session.Status,
			"updated_at", session.UpdatedAt,
		)

		// Create status update message
		update := rmq.SessionStatusUpdate{
			SessionID: session.SessionID,
			SGCID:     session.SGCID,
			Status:    manman.SessionStatusLost,
		}

		// We process it directly through the handler logic to ensure consistency
		// (update DB, publish events, etc.)
		// But Handle takes a routing key and body byte array.
		// Instead, we can just call the repository update and publish manually,
		// OR we can mock the message.
		// A cleaner way is to extract the update logic into a shared method, but for now
		// let's just do what Handle does but for "lost" status.

		now := time.Now()
		// Mark as lost (terminal state)
		if err := h.repo.Sessions.UpdateSessionEnd(ctx, session.SessionID, manman.SessionStatusLost, now, nil); err != nil {
			h.logger.Error("failed to mark session as lost", "session_id", session.SessionID, "error", err)
			continue
		}

		// Publish event
		externalRoutingKey := fmt.Sprintf("manman.session.%s", manman.SessionStatusLost)
		if err := h.publisher.PublishExternal(ctx, externalRoutingKey, update); err != nil {
			h.logger.Error("failed to publish lost session event", "session_id", session.SessionID, "error", err)
		}
	}

	return nil
}

// isValidTransition checks if a session status transition is valid
func isValidTransition(from, to string) bool {
	// Define valid state machine transitions
	validTransitions := map[string][]string{
		manman.SessionStatusPending:  {manman.SessionStatusStarting, manman.SessionStatusCrashed, manman.SessionStatusLost},
		manman.SessionStatusStarting: {manman.SessionStatusRunning, manman.SessionStatusCrashed, manman.SessionStatusLost},
		manman.SessionStatusRunning:  {manman.SessionStatusStopping, manman.SessionStatusCrashed, manman.SessionStatusLost},
		manman.SessionStatusStopping: {manman.SessionStatusStopped, manman.SessionStatusCrashed, manman.SessionStatusLost},
		manman.SessionStatusStopped:  {}, // Terminal state
		manman.SessionStatusCrashed:  {}, // Terminal state
		manman.SessionStatusLost:     {}, // Terminal state
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
