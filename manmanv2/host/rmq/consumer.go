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
	HandleDownloadAddon(ctx context.Context, cmd *DownloadAddonCommand) error
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
		fmt.Sprintf("command.host.%d.workshop.download", serverID),
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
	downloadAddonKey := fmt.Sprintf("command.host.%d.workshop.download", serverID)

	consumer.RegisterHandler(startKey, c.handleStartSession)
	consumer.RegisterHandler(stopKey, c.handleStopSession)
	consumer.RegisterHandler(killKey, c.handleKillSession)
	consumer.RegisterHandler(sendInputKey, c.handleSendInput)
	consumer.RegisterHandler(downloadAddonKey, c.handleDownloadAddon)

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
	slog.Info("received command", "command", "start_session", "session_id", cmd.SessionID, "sgc_id", cmd.SGCID, "routing_key", msg.RoutingKey)
	if err := c.handler.HandleStartSession(ctx, &cmd); err != nil {
		return err
	}
	slog.Info("command completed", "command", "start_session", "session_id", cmd.SessionID)
	return nil
}

func (c *Consumer) handleStopSession(ctx context.Context, msg rmq.Message) error {
	var cmd StopSessionCommand
	if err := json.Unmarshal(msg.Body, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal stop session command: %w", err)
	}
	slog.Info("received command", "command", "stop_session", "session_id", cmd.SessionID, "force", cmd.Force, "routing_key", msg.RoutingKey)
	if err := c.handler.HandleStopSession(ctx, &cmd); err != nil {
		return err
	}
	slog.Info("command completed", "command", "stop_session", "session_id", cmd.SessionID)
	return nil
}

func (c *Consumer) handleKillSession(ctx context.Context, msg rmq.Message) error {
	var cmd KillSessionCommand
	if err := json.Unmarshal(msg.Body, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal kill session command: %w", err)
	}
	slog.Info("received command", "command", "kill_session", "session_id", cmd.SessionID, "routing_key", msg.RoutingKey)
	if err := c.handler.HandleKillSession(ctx, &cmd); err != nil {
		return err
	}
	slog.Info("command completed", "command", "kill_session", "session_id", cmd.SessionID)
	return nil
}

func (c *Consumer) handleSendInput(ctx context.Context, msg rmq.Message) error {
	var cmd SendInputCommand
	if err := json.Unmarshal(msg.Body, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal send input command: %w", err)
	}
	slog.Debug("received command", "command", "send_input", "session_id", cmd.SessionID, "input_length", len(cmd.Input), "routing_key", msg.RoutingKey)
	if err := c.handler.HandleSendInput(ctx, &cmd); err != nil {
		return err
	}
	slog.Debug("command completed", "command", "send_input", "session_id", cmd.SessionID)
	return nil
}

func (c *Consumer) handleDownloadAddon(ctx context.Context, msg rmq.Message) error {
	var cmd DownloadAddonCommand
	if err := json.Unmarshal(msg.Body, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal download addon command: %w", err)
	}
	slog.Info("received command", "command", "download_addon", "installation_id", cmd.InstallationID, "sgc_id", cmd.SGCID, "addon_id", cmd.AddonID, "workshop_id", cmd.WorkshopID, "routing_key", msg.RoutingKey)
	if err := c.handler.HandleDownloadAddon(ctx, &cmd); err != nil {
		return err
	}
	slog.Info("command completed", "command", "download_addon", "installation_id", cmd.InstallationID)
	return nil
}
