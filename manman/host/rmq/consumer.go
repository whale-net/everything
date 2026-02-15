package rmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/whale-net/everything/libs/go/rmq"
)

// CommandHandler handles incoming commands
type CommandHandler interface {
	HandleStartSession(ctx context.Context, cmd *StartSessionCommand) error
	HandleStopSession(ctx context.Context, cmd *StopSessionCommand) error
	HandleKillSession(ctx context.Context, cmd *KillSessionCommand) error
	HandleSendInput(ctx context.Context, cmd *SendInputCommand) error
}

// Consumer consumes commands from RabbitMQ
type Consumer struct {
	consumer *rmq.Consumer
	handler  CommandHandler
	serverID int64
}

// NewConsumer creates a new command consumer
func NewConsumer(conn *rmq.Connection, serverID int64, handler CommandHandler) (*Consumer, error) {
	queueName := fmt.Sprintf("host-%d-commands", serverID)
	
	consumer, err := rmq.NewConsumer(conn, queueName)
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer: %w", err)
	}

	// Bind to command exchange with routing keys for this server
	exchange := "manman"
	routingKeys := []string{
		fmt.Sprintf("command.host.%d.session.start", serverID),
		fmt.Sprintf("command.host.%d.session.stop", serverID),
		fmt.Sprintf("command.host.%d.session.kill", serverID),
		fmt.Sprintf("command.host.%d.session.send_input", serverID),
	}

	if err := consumer.BindExchange(exchange, routingKeys); err != nil {
		consumer.Close()
		return nil, fmt.Errorf("failed to bind exchange: %w", err)
	}

	c := &Consumer{
		consumer: consumer,
		handler:  handler,
		serverID: serverID,
	}

	// Register handlers for each routing key pattern
	// Use exact match for now - the consumer will match based on routing key
	startKey := fmt.Sprintf("command.host.%d.session.start", serverID)
	stopKey := fmt.Sprintf("command.host.%d.session.stop", serverID)
	killKey := fmt.Sprintf("command.host.%d.session.kill", serverID)
	sendInputKey := fmt.Sprintf("command.host.%d.session.send_input", serverID)

	consumer.RegisterHandler(startKey, c.handleStartSession)
	consumer.RegisterHandler(stopKey, c.handleStopSession)
	consumer.RegisterHandler(killKey, c.handleKillSession)
	consumer.RegisterHandler(sendInputKey, c.handleSendInput)

	return c, nil
}

// Start starts consuming commands
func (c *Consumer) Start(ctx context.Context) error {
	return c.consumer.Start(ctx)
}

// Close closes the consumer
func (c *Consumer) Close() error {
	return c.consumer.Close()
}

func (c *Consumer) handleStartSession(ctx context.Context, msg rmq.Message) error {
	var cmd StartSessionCommand
	if err := json.Unmarshal(msg.Body, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal start session command: %w", err)
	}
	slog.Info("received start session command", "session_id", cmd.SessionID, "sgc_id", cmd.SGCID)
	return c.handler.HandleStartSession(ctx, &cmd)
}

func (c *Consumer) handleStopSession(ctx context.Context, msg rmq.Message) error {
	var cmd StopSessionCommand
	if err := json.Unmarshal(msg.Body, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal stop session command: %w", err)
	}
	slog.Info("received stop session command", "session_id", cmd.SessionID, "force", cmd.Force)
	return c.handler.HandleStopSession(ctx, &cmd)
}

func (c *Consumer) handleKillSession(ctx context.Context, msg rmq.Message) error {
	var cmd KillSessionCommand
	if err := json.Unmarshal(msg.Body, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal kill session command: %w", err)
	}
	slog.Info("received kill session command", "session_id", cmd.SessionID)
	return c.handler.HandleKillSession(ctx, &cmd)
}

func (c *Consumer) handleSendInput(ctx context.Context, msg rmq.Message) error {
	var cmd SendInputCommand
	if err := json.Unmarshal(msg.Body, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal send input command: %w", err)
	}
	slog.Info("received send input command", "session_id", cmd.SessionID, "input_length", len(cmd.Input))
	return c.handler.HandleSendInput(ctx, &cmd)
}
