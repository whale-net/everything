package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	rmqlib "github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manman/host/rmq"
	"github.com/whale-net/everything/manman/host/session"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Fatal error: %v", err)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get configuration from environment
	serverIDStr := getEnv("SERVER_ID", "")
	if serverIDStr == "" {
		return fmt.Errorf("SERVER_ID environment variable is required")
	}
	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid SERVER_ID: %w", err)
	}

	rabbitMQURL := getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	dockerSocket := getEnv("DOCKER_SOCKET", "/var/run/docker.sock")

	log.Printf("Starting ManManV2 Host Manager (server_id=%d)", serverID)

	// Initialize Docker client
	log.Println("Connecting to Docker...")
	dockerClient, err := docker.NewClient(dockerSocket)
	if err != nil {
		return fmt.Errorf("failed to initialize Docker client: %w", err)
	}
	defer dockerClient.Close()

	// Initialize RabbitMQ connection
	log.Println("Connecting to RabbitMQ...")
	rmqConn, err := rmqlib.NewConnectionFromURL(rabbitMQURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer rmqConn.Close()

	// Initialize session manager
	sessionManager := session.NewSessionManager(dockerClient)

	// Recover orphaned sessions on startup
	log.Println("Recovering orphaned sessions...")
	if err := sessionManager.RecoverOrphanedSessions(ctx, serverID); err != nil {
		log.Printf("Warning: Failed to recover some sessions: %v", err)
	}

	// Initialize RabbitMQ publisher
	rmqPublisher, err := rmq.NewPublisher(rmqConn, serverID)
	if err != nil {
		return fmt.Errorf("failed to create RabbitMQ publisher: %w", err)
	}
	defer rmqPublisher.Close()

	// Publish initial host status and health
	if err := rmqPublisher.PublishHostStatus(ctx, "online"); err != nil {
		log.Printf("Warning: Failed to publish host status: %v", err)
	}

	// Publish initial health with session stats
	stats := sessionManager.GetSessionStats()
	if err := rmqPublisher.PublishHealth(ctx, convertSessionStats(&stats)); err != nil {
		log.Printf("Warning: Failed to publish initial health: %v", err)
	}

	// Initialize command handler
	commandHandler := &CommandHandlerImpl{
		sessionManager: sessionManager,
		publisher:      rmqPublisher,
		serverID:       serverID,
	}

	// Initialize RabbitMQ consumer
	rmqConsumer, err := rmq.NewConsumer(rmqConn, serverID, commandHandler)
	if err != nil {
		return fmt.Errorf("failed to create RabbitMQ consumer: %w", err)
	}
	defer rmqConsumer.Close()

	// Start consuming commands
	log.Println("Starting command consumer...")
	if err := rmqConsumer.Start(ctx); err != nil {
		return fmt.Errorf("failed to start consumer: %w", err)
	}

	// Start health check publisher
	healthTicker := time.NewTicker(5 * time.Second)
	defer healthTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-healthTicker.C:
				// Get current session statistics
				stats := sessionManager.GetSessionStats()
				if err := rmqPublisher.PublishHealth(ctx, convertSessionStats(&stats)); err != nil {
					log.Printf("Warning: Failed to publish health: %v", err)
				}
			}
		}
	}()

	// Start periodic orphan cleanup (every 5 minutes)
	orphanCleanupTicker := time.NewTicker(5 * time.Minute)
	defer orphanCleanupTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-orphanCleanupTicker.C:
				if err := sessionManager.CleanupOrphans(ctx, serverID); err != nil {
					log.Printf("Warning: Orphan cleanup failed: %v", err)
				}
			}
		}
	}()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Println("ManManV2 Host Manager is running. Press Ctrl+C to stop.")

	// Wait for shutdown signal
	<-sigCh
	log.Println("Shutting down...")

	// Publish offline status
	_ = rmqPublisher.PublishHostStatus(ctx, "offline")

	// Cancel context to stop all goroutines
	cancel()

	// Give some time for cleanup
	time.Sleep(2 * time.Second)

	return nil
}

// CommandHandlerImpl implements the CommandHandler interface
type CommandHandlerImpl struct {
	sessionManager *session.SessionManager
	publisher      *rmq.Publisher
	serverID       int64
}

// HandleStartSession handles a start session command
func (h *CommandHandlerImpl) HandleStartSession(ctx context.Context, cmd *rmq.StartSessionCommand) error {
	env := make([]string, 0, len(cmd.GameConfig.EnvTemplate))
	for k, v := range cmd.GameConfig.EnvTemplate {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	var command []string
	if cmd.GameConfig.ArgsTemplate != "" {
		command = []string{"/bin/sh", "-c", cmd.GameConfig.ArgsTemplate}
	}

	ports := make(map[string]string, len(cmd.ServerGameConfig.PortBindings))
	for _, pb := range cmd.ServerGameConfig.PortBindings {
		ports[fmt.Sprintf("%d", pb.ContainerPort)] = fmt.Sprintf("%d", pb.HostPort)
	}

	sessionCmd := &session.StartSessionCommand{
		SessionID:    cmd.SessionID,
		SGCID:        cmd.SGCID,
		ServerID:     h.serverID,
		Image:        cmd.GameConfig.Image,
		Command:      command,
		Env:          env,
		PortBindings: ports,
	}

	// Publish starting status before attempting container creation
	if err := h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "starting",
	}); err != nil {
		return fmt.Errorf("failed to publish starting status: %w", err)
	}

	if err := h.sessionManager.StartSession(ctx, sessionCmd); err != nil {
		_ = h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
			SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "crashed",
		})
		return fmt.Errorf("failed to start session: %w", err)
	}
	return h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "running",
	})
}

// HandleStopSession handles a stop session command
func (h *CommandHandlerImpl) HandleStopSession(ctx context.Context, cmd *rmq.StopSessionCommand) error {
	// Get session state to retrieve SGCID before stopping
	state, exists := h.sessionManager.GetSessionState(cmd.SessionID)
	if !exists {
		return fmt.Errorf("session %d not found", cmd.SessionID)
	}
	sgcID := state.SGCID

	// Publish stopping status before attempting to stop container
	if err := h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID, SGCID: sgcID, Status: "stopping",
	}); err != nil {
		return fmt.Errorf("failed to publish stopping status: %w", err)
	}

	if err := h.sessionManager.StopSession(ctx, cmd.SessionID, cmd.Force); err != nil {
		return fmt.Errorf("failed to stop session: %w", err)
	}

	// Publish stopped status after container is stopped
	statusUpdate := &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID,
		SGCID:     sgcID,
		Status:    "stopped",
	}
	return h.publisher.PublishSessionStatus(ctx, statusUpdate)
}

// HandleKillSession handles a kill session command
func (h *CommandHandlerImpl) HandleKillSession(ctx context.Context, cmd *rmq.KillSessionCommand) error {
	if err := h.sessionManager.KillSession(ctx, cmd.SessionID); err != nil {
		return fmt.Errorf("failed to kill session: %w", err)
	}

	// Publish status update
	statusUpdate := &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID,
		Status:    "stopped",
	}
	return h.publisher.PublishSessionStatus(ctx, statusUpdate)
}

// HandleSendInput handles a send input command
func (h *CommandHandlerImpl) HandleSendInput(ctx context.Context, cmd *rmq.SendInputCommand) error {
	if err := h.sessionManager.SendInput(ctx, cmd.SessionID, cmd.Input); err != nil {
		return fmt.Errorf("failed to send input to session %d: %w", cmd.SessionID, err)
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// convertSessionStats converts session.SessionStats to rmq.SessionStats
func convertSessionStats(stats *session.SessionStats) *rmq.SessionStats {
	if stats == nil {
		return nil
	}
	return &rmq.SessionStats{
		Total:    stats.Total,
		Pending:  stats.Pending,
		Starting: stats.Starting,
		Running:  stats.Running,
		Stopping: stats.Stopping,
		Stopped:  stats.Stopped,
		Crashed:  stats.Crashed,
	}
}
