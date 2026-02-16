package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/whale-net/everything/libs/go/rmq"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

// ExternalEventSubscriber demonstrates how to consume events from the external exchange
type ExternalEventSubscriber struct {
	consumer *rmq.Consumer
	logger   *slog.Logger
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Setup logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Get RabbitMQ URL from environment
	rabbitmqURL := os.Getenv("RABBITMQ_URL")
	if rabbitmqURL == "" {
		rabbitmqURL = "amqp://guest:guest@localhost:5672/"
	}

	queueName := os.Getenv("QUEUE_NAME")
	if queueName == "" {
		queueName = "external-subscriber-events"
	}

	exchangeName := os.Getenv("EXTERNAL_EXCHANGE")
	if exchangeName == "" {
		exchangeName = "external"
	}

	logger.Info("starting external event subscriber",
		"queue", queueName,
		"exchange", exchangeName,
	)

	// Connect to RabbitMQ
	conn, err := rmq.NewConnectionFromURL(rabbitmqURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer conn.Close()

	// Create consumer
	consumer, err := rmq.NewConsumer(conn, queueName)
	if err != nil {
		return fmt.Errorf("failed to create consumer: %w", err)
	}
	defer consumer.Close()

	// Bind to external exchange with routing key patterns
	// Subscribe to all manman events
	routingKeys := []string{
		"manman.#", // All manman events
	}

	err = consumer.BindExchange(exchangeName, routingKeys)
	if err != nil {
		return fmt.Errorf("failed to bind to exchange: %w", err)
	}

	logger.Info("bound to external exchange", "routing_keys", routingKeys)

	// Create subscriber
	subscriber := &ExternalEventSubscriber{
		consumer: consumer,
		logger:   logger,
	}

	// Register message handler
	consumer.RegisterHandler("#", subscriber.handleEvent)

	// Start consuming
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := consumer.Start(ctx); err != nil {
			logger.Error("consumer error", "error", err)
		}
	}()

	logger.Info("subscriber started, waiting for events...")

	// Wait for signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("shutting down...")
	cancel()

	return nil
}

// handleEvent processes incoming events from the external exchange
func (s *ExternalEventSubscriber) handleEvent(ctx context.Context, msg rmq.Message) error {
	s.logger.Info("received external event",
		"routing_key", msg.RoutingKey,
		"size", len(msg.Body),
	)

	// Route to appropriate handler based on routing key
	switch {
	case matchesPattern(msg.RoutingKey, "manman.host.*"):
		return s.handleHostEvent(msg.RoutingKey, msg.Body)
	case matchesPattern(msg.RoutingKey, "manman.session.*"):
		return s.handleSessionEvent(msg.RoutingKey, msg.Body)
	default:
		s.logger.Warn("unknown event type", "routing_key", msg.RoutingKey)
		return nil
	}
}

// handleHostEvent processes host lifecycle events
func (s *ExternalEventSubscriber) handleHostEvent(routingKey string, body []byte) error {
	var event hostrmq.HostStatusUpdate
	if err := json.Unmarshal(body, &event); err != nil {
		s.logger.Error("failed to unmarshal host event", "error", err)
		return nil // Don't requeue malformed messages
	}

	s.logger.Info("host event",
		"routing_key", routingKey,
		"server_id", event.ServerID,
		"status", event.Status,
	)

	// Example: Send Slack notification
	switch event.Status {
	case "online":
		s.logger.Info("🟢 Host came online", "server_id", event.ServerID)
		// sendSlackNotification(fmt.Sprintf("Host %d is now online", event.ServerID))
	case "offline":
		s.logger.Info("🔴 Host went offline", "server_id", event.ServerID)
		// sendSlackNotification(fmt.Sprintf("Host %d is offline", event.ServerID))
	}

	// Example: Update monitoring dashboard
	// updatePrometheusMetric("host_status", event.ServerID, event.Status)

	return nil
}

// handleSessionEvent processes session lifecycle events
func (s *ExternalEventSubscriber) handleSessionEvent(routingKey string, body []byte) error {
	var event hostrmq.SessionStatusUpdate
	if err := json.Unmarshal(body, &event); err != nil {
		s.logger.Error("failed to unmarshal session event", "error", err)
		return nil
	}

	s.logger.Info("session event",
		"routing_key", routingKey,
		"session_id", event.SessionID,
		"sgc_id", event.SGCID,
		"status", event.Status,
		"exit_code", event.ExitCode,
	)

	// Example: Send notifications
	switch event.Status {
	case "running":
		s.logger.Info("🎮 Session started", "session_id", event.SessionID)
		// sendSlackNotification(fmt.Sprintf("Game session %d started", event.SessionID))
	case "stopped":
		s.logger.Info("🛑 Session stopped", "session_id", event.SessionID, "exit_code", event.ExitCode)
		// sendSlackNotification(fmt.Sprintf("Session %d stopped (exit: %v)", event.SessionID, event.ExitCode))
	case "crashed":
		s.logger.Warn("💥 Session crashed", "session_id", event.SessionID, "exit_code", event.ExitCode)
		// sendSlackAlert(fmt.Sprintf("Session %d crashed with exit code %v", event.SessionID, event.ExitCode))
	}

	// Example: Update metrics
	// recordSessionMetric(event.Status, event.ExitCode)

	return nil
}

// matchesPattern checks if a routing key matches a pattern (simple implementation)
func matchesPattern(routingKey, pattern string) bool {
	if pattern == "#" {
		return true
	}

	// Simple wildcard matching for demonstration
	// For production, use the same logic as in processor/handlers/handler.go
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(routingKey) >= len(prefix) && routingKey[:len(prefix)] == prefix
	}

	return routingKey == pattern
}
