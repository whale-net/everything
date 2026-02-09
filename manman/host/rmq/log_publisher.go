package rmq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// LogMessage represents a log message for RabbitMQ publishing
type LogMessage struct {
	SessionID int64  `json:"session_id"`
	Timestamp int64  `json:"timestamp"` // Unix milliseconds
	Source    string `json:"source"`    // "stdout" | "stderr" | "host"
	Message   string `json:"message"`
}

// PublishLog publishes a log message to RabbitMQ
// This is a non-blocking, fire-and-forget operation
func (p *Publisher) PublishLog(ctx context.Context, sessionID int64, source string, message string) error {
	logMsg := LogMessage{
		SessionID: sessionID,
		Timestamp: time.Now().UnixMilli(),
		Source:    source,
		Message:   message,
	}

	routingKey := fmt.Sprintf("logs.session.%d", sessionID)
	return p.publisher.Publish(ctx, "manman", routingKey, logMsg)
}

// PublishLogBatch publishes multiple log messages in a single call
// Used for buffered log publishing
func (p *Publisher) PublishLogBatch(ctx context.Context, messages []LogMessage) error {
	for _, msg := range messages {
		// Use routing key specific to each session
		routingKey := fmt.Sprintf("logs.session.%d", msg.SessionID)
		if err := p.publisher.Publish(ctx, "manman", routingKey, msg); err != nil {
			// Log the error but continue with other messages
			// This is fire-and-forget, so we don't fail the entire batch
			fmt.Printf("[log-publisher] failed to publish log for session %d: %v\n", msg.SessionID, err)
		}
	}
	return nil
}

// MarshalLogMessage marshals a log message to JSON for debugging
func MarshalLogMessage(msg LogMessage) ([]byte, error) {
	return json.Marshal(msg)
}
