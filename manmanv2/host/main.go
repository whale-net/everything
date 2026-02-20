package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/docker"
	grpcclient "github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/logging"
	rmqlib "github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/host/rmq"
	"github.com/whale-net/everything/manmanv2/host/session"
	"github.com/whale-net/everything/manmanv2/host/workshop"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Configure structured logging
	logging.Configure(logging.Config{
		ServiceName: "host-manager",
		Domain:      "manmanv2",
		JSONFormat:  getEnv("LOG_FORMAT", "json") == "json",
	})
	defer logging.Shutdown(ctx)

	logger := logging.Get("main")

	// Get configuration from environment
	rabbitMQURL := getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	dockerSocket := getEnv("DOCKER_SOCKET", "/var/run/docker.sock")
	apiAddress := getEnv("API_ADDRESS", "localhost:50051")
	serverName := getEnv("SERVER_NAME", "")
	environment := getEnv("ENVIRONMENT", "")

	// HOST_DATA_DIR is the path on the host where session data is stored
	// This container must have that path mounted at /var/lib/manman/sessions:
	//   -v ${HOST_DATA_DIR}:/var/lib/manman/sessions
	// Example: -v /home/manman_dev/manman-v2/manager-data:/var/lib/manman/sessions
	//
	// Why both paths are needed:
	// - Internal path (/var/lib/manman/sessions): Where we create directories
	// - Host path (HOST_DATA_DIR): What we tell Docker for game container bind mounts
	hostDataDir := os.Getenv("HOST_DATA_DIR")

	if hostDataDir == "" {
		return fmt.Errorf("HOST_DATA_DIR must be provided (path on host where session data is stored, e.g., /home/manman_dev/manman-v2/manager-data)")
	}

	// Self-registration mode: Register with API and get server_id
	logger.Info("starting host manager (self-registration mode)")
	serverID, err := selfRegister(ctx, apiAddress, serverName, environment, dockerSocket)
	if err != nil {
		return fmt.Errorf("failed to self-register: %w", err)
	}
	logger.Info("registered with control plane", "server_id", serverID)

	// Initialize Docker client
	logger.Info("connecting to Docker")
	dockerClient, err := docker.NewClient(dockerSocket)
	if err != nil {
		return fmt.Errorf("failed to initialize Docker client: %w", err)
	}
	defer dockerClient.Close()

	// Initialize RabbitMQ connection
	logger.Info("connecting to RabbitMQ")
	rmqConn, err := rmqlib.NewConnectionFromURL(rabbitMQURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer rmqConn.Close()

	// Initialize gRPC client for control API
	grpcClient, err := initializeGRPCClient(ctx, apiAddress)
	if err != nil {
		return fmt.Errorf("failed to initialize gRPC client: %w", err)
	}

	// Initialize RabbitMQ publisher
	rmqPublisher, err := rmq.NewPublisher(rmqConn, serverID)
	if err != nil {
		return fmt.Errorf("failed to create RabbitMQ publisher: %w", err)
	}
	defer rmqPublisher.Close()

	// Initialize download orchestrator for workshop addon downloads
	downloadOrchestrator := workshop.NewDownloadOrchestrator(
		dockerClient,
		grpcClient,
		serverID,
		environment,
		hostDataDir,
		5, // max concurrent downloads
		rmqPublisher,
	)

	// Initialize session manager with gRPC client for configuration fetching and RMQ publisher for logs
	sessionManager := session.NewSessionManager(dockerClient, environment, hostDataDir, grpcClient, downloadOrchestrator, rmqPublisher)

	// Recover orphaned sessions on startup
	logger.Info("recovering orphaned sessions")
	if err := sessionManager.RecoverOrphanedSessions(ctx, serverID); err != nil {
		logger.Warn("failed to recover some sessions", "error", err)
	}

	// Publish initial host status and health
	if err := rmqPublisher.PublishHostStatus(ctx, "online"); err != nil {
		logger.Warn("failed to publish host status", "error", err)
	}

	// Publish initial health with session stats
	stats := sessionManager.GetSessionStats()
	if err := rmqPublisher.PublishHealth(ctx, convertSessionStats(&stats)); err != nil {
		logger.Warn("failed to publish initial health", "error", err)
	}

	// Initialize command handler
	commandHandler := &CommandHandlerImpl{
		sessionManager:       sessionManager,
		publisher:            rmqPublisher,
		serverID:             serverID,
		downloadOrchestrator: downloadOrchestrator,
	}

	// Initialize RabbitMQ consumer
	rmqConsumer, err := rmq.NewConsumer(rmqConn, serverID, commandHandler)
	if err != nil {
		return fmt.Errorf("failed to create RabbitMQ consumer: %w", err)
	}
	defer rmqConsumer.Close()

	// Start consuming commands
	logger.Info("starting command consumer")
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
					logger.Warn("failed to publish health", "error", err)
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
					logger.Warn("orphan cleanup failed", "error", err)
				}
			}
		}
	}()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("host manager is running")

	// Wait for shutdown signal
	<-sigCh
	logger.Info("shutting down")

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
	sessionManager      *session.SessionManager
	publisher           *rmq.Publisher
	serverID            int64
	downloadOrchestrator *workshop.DownloadOrchestrator
}

// HandleStartSession handles a start session command
func (h *CommandHandlerImpl) HandleStartSession(ctx context.Context, cmd *rmq.StartSessionCommand) error {
	slog.Info("processing start session command",
		"session_id", cmd.SessionID, "sgc_id", cmd.SGCID,
		"image", cmd.GameConfig.Image,
		"ports", len(cmd.ServerGameConfig.PortBindings),
		"volumes", len(cmd.GameConfig.Volumes),
		"force", cmd.Force)
	
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
		// Include protocol in key to support both TCP and UDP on same port
		ports[fmt.Sprintf("%d/%s", pb.ContainerPort, pb.Protocol)] = fmt.Sprintf("%d/%s", pb.HostPort, pb.Protocol)
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
	slog.Info("publishing session status", "session_id", cmd.SessionID, "status", "starting")
	if err := h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "starting",
	}); err != nil {
		slog.Error("failed to publish starting status", "session_id", cmd.SessionID, "error", err)
		return fmt.Errorf("failed to publish starting status: %w", err)
	}

	if err := h.sessionManager.StartSession(ctx, sessionCmd); err != nil {
		slog.Error("failed to start session", "session_id", cmd.SessionID, "error", err)
		_ = h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
			SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "crashed",
		})
		// Wrap in PermanentError to avoid infinite RMQ retries
		return &rmqlib.PermanentError{Err: fmt.Errorf("failed to start session: %w", err)}
	}

	slog.Info("publishing session status", "session_id", cmd.SessionID, "status", "running")
	return h.publisher.PublishSessionStatus(ctx, &rmq.SessionStatusUpdate{
		SessionID: cmd.SessionID, SGCID: cmd.SGCID, Status: "running",
	})
}

// HandleStopSession handles a stop session command
func (h *CommandHandlerImpl) HandleStopSession(ctx context.Context, cmd *rmq.StopSessionCommand) error {
	slog.Info("processing stop session command", "session_id", cmd.SessionID, "force", cmd.Force)

	// Get session state to retrieve SGCID before stopping
	state, exists := h.sessionManager.GetSessionState(cmd.SessionID)
	if !exists {
		slog.Warn("session not found for stop command", "session_id", cmd.SessionID)
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
			slog.Info("session not found during stop, proceeding to mark as stopped", "session_id", cmd.SessionID)
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
	slog.Info("processing kill session command", "session_id", cmd.SessionID)

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
	// Truncate input for logging if too long
	inputPreview := string(cmd.Input)
	if len(inputPreview) > 50 {
		inputPreview = inputPreview[:50] + "..."
	}
	slog.Info("processing send input command", "session_id", cmd.SessionID, "input_preview", inputPreview)

	if err := h.sessionManager.SendInput(ctx, cmd.SessionID, cmd.Input); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return &rmqlib.PermanentError{Err: err}
		}
		return fmt.Errorf("failed to send input to session %d: %w", cmd.SessionID, err)
	}
	return nil
}

// HandleDownloadAddon handles a workshop addon download command
func (h *CommandHandlerImpl) HandleDownloadAddon(ctx context.Context, cmd *rmq.DownloadAddonCommand) error {
	slog.Info("processing download addon command",
		"installation_id", cmd.InstallationID,
		"sgc_id", cmd.SGCID,
		"addon_id", cmd.AddonID,
		"workshop_id", cmd.WorkshopID,
		"steam_app_id", cmd.SteamAppID)

	// Convert rmq.DownloadAddonCommand to workshop.DownloadAddonCommand
	workshopCmd := &workshop.DownloadAddonCommand{
		InstallationID: cmd.InstallationID,
		SGCID:          cmd.SGCID,
		AddonID:        cmd.AddonID,
		WorkshopID:     cmd.WorkshopID,
		SteamAppID:     cmd.SteamAppID,
		InstallPath:    cmd.InstallPath,
	}

	// Call download orchestrator in a goroutine to avoid blocking RabbitMQ consumer
	go h.downloadOrchestrator.HandleDownloadCommand(ctx, workshopCmd)

	return nil
}

// selfRegister generates a server name (if not provided) and registers with the control plane
// initializeGRPCClient creates a gRPC client connection to the control API
func initializeGRPCClient(ctx context.Context, apiAddress string) (pb.ManManAPIClient, error) {
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
			slog.Warn("failed to connect to API, retrying", "error", err, "backoff", backoff)
			time.Sleep(backoff)
			client, err = grpcclient.NewClientWithTLS(connCtx, apiAddress, tlsConfig)
			if err == nil {
				break
			}
			backoff *= 2
		}
		if err != nil {
			return nil, fmt.Errorf("failed to connect to API after retries: %w", err)
		}
	}

	return pb.NewManManAPIClient(client.GetConnection()), nil
}

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
			slog.Warn("no ENVIRONMENT set, consider setting it to avoid duplicate servers on restart")
		}
	}

	grpcClient, err := initializeGRPCClient(ctx, apiAddress)
	if err != nil {
		return 0, err
	}

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

	slog.Info("registered as server", "server_name", serverName)
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
