package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/docker"
	grpcclient "github.com/whale-net/everything/libs/go/grpcclient"
	rmqlib "github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manman/host/rmq"
	"github.com/whale-net/everything/manman/host/session"
	pb "github.com/whale-net/everything/manman/protos"
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
	rabbitMQURL := getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	dockerSocket := getEnv("DOCKER_SOCKET", "/var/run/docker.sock")
	apiAddress := getEnv("API_ADDRESS", "localhost:50051")
	serverName := getEnv("SERVER_NAME", "")
	environment := getEnv("ENVIRONMENT", "")
	hostDataDir := os.Getenv("HOST_DATA_DIR") // Path as seen by Docker daemon (host side)

	if hostDataDir == "" {
		return fmt.Errorf("HOST_DATA_DIR must be provided (absolute path on host for game data)")
	}

	// Self-registration mode: Register with API and get server_id
	log.Println("Starting ManManV2 Host Manager (self-registration mode)")
	serverID, err := selfRegister(ctx, apiAddress, serverName, environment, dockerSocket)
	if err != nil {
		return fmt.Errorf("failed to self-register: %w", err)
	}
	log.Printf("Successfully registered with control plane (server_id=%d)", serverID)

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
	sessionManager := session.NewSessionManager(dockerClient, environment, hostDataDir)

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
	log.Printf("[host] processing start session command for session %d (sgc %d)...", cmd.SessionID, cmd.SGCID)
	
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

	volumes := make([]session.VolumeMount, 0, len(cmd.GameConfig.Volumes))
	for _, v := range cmd.GameConfig.Volumes {
		volumes = append(volumes, session.VolumeMount{
			Name:          v.Name,
			ContainerPath: v.ContainerPath,
			HostSubpath:   v.HostSubpath,
			Options:       v.Options,
		})
	}

	sessionCmd := &session.StartSessionCommand{
		SessionID:    cmd.SessionID,
		SGCID:        cmd.SGCID,
		ServerID:     h.serverID,
		Image:        cmd.GameConfig.Image,
		Command:      command,
		Env:          env,
		PortBindings: ports,
		Volumes:      volumes,
		Force:        cmd.Force,
	}

	// Publish starting status before attempting container creation
	log.Printf("[host] publishing status: starting for session %d", cmd.SessionID)
	if err := h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "starting",
	}); err != nil {
		log.Printf("[host] error: failed to publish starting status for session %d: %v", cmd.SessionID, err)
		return fmt.Errorf("failed to publish starting status: %w", err)
	}

	if err := h.sessionManager.StartSession(ctx, sessionCmd); err != nil {
		log.Printf("[host] error: failed to start session %d: %v", cmd.SessionID, err)
		_ = h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
			SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "crashed",
		})
		// Wrap in PermanentError to avoid infinite RMQ retries
		return &rmqlib.PermanentError{Err: fmt.Errorf("failed to start session: %w", err)}
	}
	
	log.Printf("[host] publishing status: running for session %d", cmd.SessionID)
	return h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "running",
	})
}

// HandleStopSession handles a stop session command
func (h *CommandHandlerImpl) HandleStopSession(ctx context.Context, cmd *rmq.StopSessionCommand) error {
	// Get session state to retrieve SGCID before stopping
	state, exists := h.sessionManager.GetSessionState(cmd.SessionID)
	if !exists {
		log.Printf("[host] session %d not found for stop command, marking as permanent error", cmd.SessionID)
		return &rmqlib.PermanentError{Err: fmt.Errorf("session %d not found", cmd.SessionID)}
	}
	sgcID := state.SGCID

	// Publish stopping status before attempting to stop container
	if err := h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID, SGCID: sgcID, Status: "stopping",
	}); err != nil {
		return fmt.Errorf("failed to publish stopping status: %w", err)
	}

	if err := h.sessionManager.StopSession(ctx, cmd.SessionID, cmd.Force); err != nil {
		// If session is not found, it means it's already stopped/removed (e.g. by StartSession failure)
		// We should still publish "stopped" status to ensure lifecycle completion
		if strings.Contains(err.Error(), "not found") {
			log.Printf("Session %d not found during stop (race condition handled), proceeding to mark as stopped", cmd.SessionID)
		} else {
			return fmt.Errorf("failed to stop session: %w", err)
		}
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
		if strings.Contains(err.Error(), "not found") {
			return &rmqlib.PermanentError{Err: err}
		}
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
		if strings.Contains(err.Error(), "not found") {
			return &rmqlib.PermanentError{Err: err}
		}
		return fmt.Errorf("failed to send input to session %d: %w", cmd.SessionID, err)
	}
	return nil
}

// selfRegister generates a server name (if not provided) and registers with the control plane
func selfRegister(ctx context.Context, apiAddress, serverName, environment, dockerSocket string) (int64, error) {
	// Generate server name if not provided
	if serverName == "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown-host"
		}

		// Use stable naming based on hostname and environment
		// This ensures same server record is reused across restarts
		if environment != "" {
			serverName = fmt.Sprintf("%s-%s", hostname, environment)
		} else {
			// No environment specified - use hostname only
			// If multiple managers on same host without environment, add UUID
			serverName = fmt.Sprintf("%s-%s", hostname, uuid.New().String()[:8])
			log.Printf("Warning: No ENVIRONMENT set. Consider setting it to avoid duplicate servers on restart.")
		}
	}

	// Build TLS config based on environment and auto-detection
	var tlsConfig *grpcclient.TLSConfig
	apiTLSEnabled := shouldUseAPITLS(apiAddress)
	if useTLS := os.Getenv("API_USE_TLS"); useTLS != "" {
		apiTLSEnabled = useTLS == "true"
	}

	if apiTLSEnabled {
		tlsConfig = &grpcclient.TLSConfig{
			Enabled:            true,
			InsecureSkipVerify: getEnv("API_TLS_SKIP_VERIFY", "false") == "true",
			CACertPath:         getEnv("API_CA_CERT_PATH", ""),
			ServerName:         getEnv("API_TLS_SERVER_NAME", ""),
		}
	}

	connCtx, connCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connCancel()

	client, err := grpcclient.NewClientWithTLS(connCtx, apiAddress, tlsConfig)
	if err != nil {
		// Retry connection with exponential backoff
		backoff := 1 * time.Second
		for i := 0; i < 5; i++ {
			log.Printf("Failed to connect to API: %v. Retrying in %v...", err, backoff)
			time.Sleep(backoff)
			client, err = grpcclient.NewClientWithTLS(connCtx, apiAddress, tlsConfig)
			if err == nil {
				break
			}
			backoff *= 2
		}
		if err != nil {
			return 0, fmt.Errorf("failed to connect to API after retries: %w", err)
		}
	}
	defer client.Close()

	grpcClient := pb.NewManManAPIClient(client.GetConnection())

	// Get Docker info for capabilities
	dockerClient, err := docker.NewClient(dockerSocket)
	if err != nil {
		return 0, fmt.Errorf("failed to connect to Docker for capabilities: %w", err)
	}
	defer dockerClient.Close()

	info, err := dockerClient.GetClient().Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get Docker info: %w", err)
	}

	// Build capabilities from Docker info
	capabilities := &pb.ServerCapabilities{
		TotalMemoryMb:          int32(info.MemTotal / (1024 * 1024)),
		AvailableMemoryMb:      int32(info.MemTotal / (1024 * 1024)), // Assume all available initially
		CpuCores:               int32(info.NCPU),
		AvailableCpuMillicores: int32(info.NCPU * 1000), // Assume all available initially
		DockerVersion:          info.ServerVersion,
	}

	// Call RegisterServer
	req := &pb.RegisterServerRequest{
		Name:         serverName,
		Capabilities: capabilities,
		Environment:  environment,
	}

	resp, err := grpcClient.RegisterServer(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("registration failed: %w", err)
	}

	log.Printf("Registered as server '%s'", serverName)
	return resp.ServerId, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// shouldUseAPITLS determines if TLS should be used for API connection based on address
func shouldUseAPITLS(address string) bool {
	lower := strings.ToLower(address)
	return strings.HasPrefix(lower, "https://") || strings.Contains(lower, ":443")
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
