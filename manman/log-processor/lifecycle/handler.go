package lifecycle

import (
	"context"
	"encoding/json"
	"log"

	"github.com/whale-net/everything/libs/go/rmq"
)

// SessionLifecycleEvent represents a session lifecycle event
type SessionLifecycleEvent struct {
	SessionID int64  `json:"session_id"`
	SGCID     int64  `json:"sgc_id"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}

// ConsumerManager interface for managing session consumers
type ConsumerManager interface {
	CreateConsumerForSession(ctx context.Context, sessionID int64) error
	DeleteConsumerForSession(sessionID int64) error
}

// Handler handles session lifecycle events
type Handler struct {
	consumer        *rmq.Consumer
	consumerManager ConsumerManager
}

// NewHandler creates a new lifecycle handler
func NewHandler(conn *rmq.Connection, consumerManager ConsumerManager) (*Handler, error) {
	// Create consumer for lifecycle events from external exchange
	consumer, err := rmq.NewConsumerWithOpts(conn, "log-processor-lifecycle", true, false)
	if err != nil {
		return nil, err
	}

	// Bind to session lifecycle events
	if err := consumer.BindExchange("external", []string{
		"manman.session.running",
		"manman.session.stopped",
		"manman.session.crashed",
	}); err != nil {
		consumer.Close()
		return nil, err
	}

	return &Handler{
		consumer:        consumer,
		consumerManager: consumerManager,
	}, nil
}

// Start starts consuming lifecycle events
func (h *Handler) Start(ctx context.Context) error {
	h.consumer.RegisterHandler("#", func(ctx context.Context, msg rmq.Message) error {
		var event SessionLifecycleEvent
		if err := json.Unmarshal(msg.Body, &event); err != nil {
			log.Printf("[lifecycle] failed to unmarshal event: %v", err)
			return nil // Don't retry on unmarshal errors
		}

		log.Printf("[lifecycle] received event: session_id=%d status=%s", event.SessionID, event.Status)

		switch event.Status {
		case "running":
			// Create queue and consumer for this session
			if err := h.consumerManager.CreateConsumerForSession(ctx, event.SessionID); err != nil {
				log.Printf("[lifecycle] failed to create consumer for session %d: %v", event.SessionID, err)
				return err // Retry on error
			}

		case "stopped", "crashed":
			// Delete queue and close consumer
			if err := h.consumerManager.DeleteConsumerForSession(event.SessionID); err != nil {
				log.Printf("[lifecycle] failed to delete consumer for session %d: %v", event.SessionID, err)
				return err // Retry on error
			}

		case "lost":
			// Do nothing - session may recover
			log.Printf("[lifecycle] session %d marked as lost, keeping queue alive", event.SessionID)
		}

		return nil
	})

	return h.consumer.Start(ctx)
}

// Close closes the lifecycle handler
func (h *Handler) Close() error {
	return h.consumer.Close()
}
