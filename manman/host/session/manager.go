package session

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/host/config"
	pb "github.com/whale-net/everything/manman/protos"
)

const (
	// internalDataDir is the well-known path inside this container where session data is stored
	// The host's data directory should be mounted here
	internalDataDir = "/var/lib/manman/sessions"
)

// SessionManager manages the lifecycle of game server sessions
type SessionManager struct {
	dockerClient *docker.Client
	stateManager *Manager
	environment  string
	hostDataDir  string // Path on the host where session data lives (for Docker bind mounts)
	grpcClient   pb.ManManAPIClient
	renderer     *config.Renderer
	rmqPublisher interface {
		PublishLog(ctx context.Context, sessionID int64, source string, message string) error
	}
}

// NewSessionManager creates a new session manager
func NewSessionManager(dockerClient *docker.Client, environment string, hostDataDir string, grpcClient pb.ManManAPIClient, rmqPublisher interface {
	PublishLog(ctx context.Context, sessionID int64, source string, message string) error
}) *SessionManager {
	return &SessionManager{
		dockerClient: dockerClient,
		stateManager: NewManager(),
		environment:  environment,
		hostDataDir:  hostDataDir,
		grpcClient:   grpcClient,
		renderer:     config.NewRenderer(nil),
		rmqPublisher: rmqPublisher,
	}
}

// StartSessionCommand represents a command to start a session
type StartSessionCommand struct {
	SessionID     int64
	SGCID         int64
	ServerID      int64
	Image         string
	Command       []string
	Env          []string
	PortBindings map[string]string // containerPort -> hostPort
	Volumes      []VolumeMount     // many volumes
	Force        bool
}

type VolumeMount struct {
	Name          string
	ContainerPath string
	HostSubpath   string
	Options       map[string]string
}

func (sm *SessionManager) getContainerName(serverID, sgcID int64) string {
	if sm.environment != "" {
		return fmt.Sprintf("game-%s-%d-%d", sm.environment, serverID, sgcID)
	}
	return fmt.Sprintf("game-%d-%d", serverID, sgcID)
}

func (sm *SessionManager) getNetworkName(sessionID int64) string {
	if sm.environment != "" {
		return fmt.Sprintf("session-%s-%d", sm.environment, sessionID)
	}
	return fmt.Sprintf("session-%d", sessionID)
}

// getSGCInternalDir returns the path to SGC data inside this container
func (sm *SessionManager) getSGCInternalDir(sgcID int64) string {
	dirName := fmt.Sprintf("sgc-%d", sgcID)
	if sm.environment != "" {
		dirName = fmt.Sprintf("sgc-%s-%d", sm.environment, sgcID)
	}
	return filepath.Join(internalDataDir, dirName)
}

// getSGCHostDir returns the path to SGC data on the host (for Docker bind mounts)
func (sm *SessionManager) getSGCHostDir(sgcID int64) string {
	dirName := fmt.Sprintf("sgc-%d", sgcID)
	if sm.environment != "" {
		dirName = fmt.Sprintf("sgc-%s-%d", sm.environment, sgcID)
	}
	return filepath.Join(sm.hostDataDir, dirName)
}

// StartSession starts a new game server session
func (sm *SessionManager) StartSession(ctx context.Context, cmd *StartSessionCommand) error {
	sessionID := cmd.SessionID
	sgcID := cmd.SGCID

	// 1. Check if the exact same session already exists
	if _, exists := sm.stateManager.GetSession(sessionID); exists {
		return &rmq.PermanentError{Err: fmt.Errorf("session %d already exists", sessionID)}
	}

	// 2. Check if any session is already active for this SGC
	if existingSession, exists := sm.stateManager.GetSessionBySGCID(sgcID); exists {
		if !cmd.Force {
			return &rmq.PermanentError{Err: fmt.Errorf("GSC %d already has an active session %d", sgcID, existingSession.SessionID)}
		}

		// Force start: stop the existing session first
		log.Printf("[session %d] force start: stopping existing active session %d for GSC %d", sessionID, existingSession.SessionID, sgcID)
		if err := sm.StopSession(ctx, existingSession.SessionID, true); err != nil {
			log.Printf("[session %d] warning: failed to stop existing session %d: %v", sessionID, existingSession.SessionID, err)
			// Continue anyway, we'll attempt container cleanup below
		}
	}

	// 3. If force is requested (or even if not, as a safety measure), clean up any existing container for this SGC
	if cmd.Force {
		containerName := sm.getContainerName(cmd.ServerID, cmd.SGCID)
		log.Printf("[session %d] force start requested, cleaning up existing container %s", sessionID, containerName)

		// 1. Attempt graceful shutdown first
		log.Printf("[session %d] attempting graceful shutdown of %s...", sessionID, containerName)
		shutdownTimeout := 10 * time.Second // TODO: make configurable in far future iteration
		if err := sm.dockerClient.StopContainer(ctx, containerName, &shutdownTimeout); err != nil {
			log.Printf("[session %d] graceful shutdown failed or container not found: %v", sessionID, err)
		} else {
			log.Printf("[session %d] graceful shutdown of %s succeeded", sessionID, containerName)
		}

		// 2. Force stop and remove
		log.Printf("[session %d] removing container %s...", sessionID, containerName)
		if err := sm.dockerClient.RemoveContainer(ctx, containerName, true); err != nil {
			log.Printf("[session %d] warning: removal failed: %v", sessionID, err)
		} else {
			log.Printf("[session %d] removal of %s succeeded", sessionID, containerName)
		}
	}

	// Create session state
	state := &State{
		SessionID: sessionID,
		SGCID:     sgcID,
		Status:    manman.SessionStatusPending,
	}
	sm.stateManager.AddSession(state)
	log.Printf("[session %d] added to state manager, status: starting", sessionID)
	state.UpdateStatus(manman.SessionStatusStarting)

	// 1. Create Docker network
	networkName := sm.getNetworkName(sessionID)
	log.Printf("[session %d] creating network %s...", sessionID, networkName)
	networkLabels := map[string]string{
		"manman.type":        "network",
		"manman.session_id":  fmt.Sprintf("%d", sessionID),
		"manman.server_id":   fmt.Sprintf("%d", cmd.ServerID),
		"manman.environment": sm.environment,
	}
	networkID, err := sm.dockerClient.CreateNetwork(ctx, networkName, networkLabels)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			log.Printf("[session %d] network %s already exists, attempting to reuse", sessionID, networkName)
			id, netErr := sm.dockerClient.GetNetworkIDByName(ctx, networkName)
			if netErr != nil {
				log.Printf("[session %d] error: failed to get existing network ID: %v", sessionID, netErr)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return fmt.Errorf("failed to get existing network ID: %w", netErr)
			}
			networkID = id
			log.Printf("[session %d] reused network ID: %s", sessionID, networkID)
		} else {
			log.Printf("[session %d] error: failed to create network: %v", sessionID, err)
			state.UpdateStatus(manman.SessionStatusCrashed)
			sm.stateManager.RemoveSession(sessionID)
			return fmt.Errorf("failed to create network: %w", err)
		}
	}
	log.Printf("[session %d] network created/resolved: %s", sessionID, networkID)
	state.NetworkID = networkID
	state.NetworkName = networkName

	// 2. Fetch and render configurations
	log.Printf("[session %d] fetching configuration strategies...", sessionID)
	configResp, err := sm.grpcClient.GetSessionConfiguration(ctx, &pb.GetSessionConfigurationRequest{
		SessionId: sessionID,
	})
	if err != nil {
		log.Printf("[session %d] warning: failed to fetch configurations: %v", sessionID, err)
		// Don't fail the session start - configurations are optional
		configResp = &pb.GetSessionConfigurationResponse{Configurations: nil}
	} else {
		log.Printf("[session %d] fetched %d configuration strategies", sessionID, len(configResp.Configurations))

		// Render configurations
		if len(configResp.Configurations) > 0 {
			sgcInternalDir := sm.getSGCInternalDir(cmd.SGCID)
			renderedFiles, err := sm.renderer.RenderConfigurations(configResp.Configurations, sgcInternalDir)
			if err != nil {
				log.Printf("[session %d] error: failed to render configurations: %v", sessionID, err)
				sm.cleanupSession(ctx, state)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return fmt.Errorf("failed to render configurations: %w", err)
			}

			// Write rendered files to disk
			if len(renderedFiles) > 0 {
				log.Printf("[session %d] writing %d configuration files...", sessionID, len(renderedFiles))
				if err := sm.renderer.WriteRenderedFiles(renderedFiles); err != nil {
					log.Printf("[session %d] error: failed to write configuration files: %v", sessionID, err)
					sm.cleanupSession(ctx, state)
					state.UpdateStatus(manman.SessionStatusCrashed)
					sm.stateManager.RemoveSession(sessionID)
					return fmt.Errorf("failed to write configuration files: %w", err)
				}
				log.Printf("[session %d] successfully wrote configuration files", sessionID)
			}
		}
	}

	// 3. Create game container
	log.Printf("[session %d] creating container for image %s...", sessionID, cmd.Image)
	containerID, err := sm.createGameContainer(ctx, state, cmd)
	if err != nil {
		if isNameConflictError(err) {
			if !cmd.Force {
				log.Printf("[session %d] container name conflict but force=false, failing", sessionID)
				sm.cleanupSession(ctx, state)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return &rmq.PermanentError{Err: fmt.Errorf("container name conflict for GSC %d", sgcID)}
			}

			// Handle idempotent start: container with this name already exists
			log.Printf("[session %d] container name conflict, attempting to handle...", sessionID)
			containerID, err = sm.handleNameConflict(ctx, cmd)
			if err != nil {
				log.Printf("[session %d] error: failed to handle name conflict: %v", sessionID, err)
				sm.cleanupSession(ctx, state)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return fmt.Errorf("failed to handle name conflict: %w", err)
			}
			log.Printf("[session %d] resolved name conflict, container ID: %s", sessionID, containerID)
		} else if strings.Contains(err.Error(), "No such image") {
			// Try to pull the image and retry creation once
			log.Printf("[session %d] image %s not found, attempting to pull...", sessionID, cmd.Image)
			if pullErr := sm.dockerClient.PullImage(ctx, cmd.Image); pullErr != nil {
				log.Printf("[session %d] error: failed to pull image %s: %v", sessionID, cmd.Image, pullErr)
				sm.cleanupSession(ctx, state)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return &rmq.PermanentError{Err: fmt.Errorf("failed to pull image %s: %w", cmd.Image, pullErr)}
			}

			// Retry creation
			log.Printf("[session %d] retrying container creation after pull...", sessionID)
			containerID, err = sm.createGameContainer(ctx, state, cmd)
			if err != nil {
				log.Printf("[session %d] error: failed to create container after pull: %v", sessionID, err)
				sm.cleanupSession(ctx, state)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return &rmq.PermanentError{Err: fmt.Errorf("failed to create game container after pull: %w", err)}
			}
		} else {
			log.Printf("[session %d] error: failed to create container: %v", sessionID, err)
			sm.cleanupSession(ctx, state)
			state.UpdateStatus(manman.SessionStatusCrashed)
			sm.stateManager.RemoveSession(sessionID)
			return fmt.Errorf("failed to create game container: %w", err)
		}
	}
	log.Printf("[session %d] container created: %s", sessionID, containerID)
	state.GameContainerID = containerID

	// 3. Start game container
	log.Printf("[session %d] starting container %s...", sessionID, containerID)
	if err := sm.dockerClient.StartContainer(ctx, containerID); err != nil {
		// If already running (from name conflict recovery), that's fine
		if !strings.Contains(err.Error(), "already started") {
			log.Printf("[session %d] error: failed to start container: %v", sessionID, err)
			sm.cleanupSession(ctx, state)
			state.UpdateStatus(manman.SessionStatusCrashed)
			sm.stateManager.RemoveSession(sessionID)
			return fmt.Errorf("failed to start game container: %w", err)
		}
		log.Printf("[session %d] container was already started", sessionID)
	}
	log.Printf("[session %d] container %s started", sessionID, containerID)

	// 4. Attach to container for stdin/stdout
	log.Printf("[session %d] attaching to container for logging...", sessionID)
	attachResp, err := sm.dockerClient.AttachToContainer(ctx, containerID)
	if err != nil {
		log.Printf("[session %d] error: failed to attach to container: %v", sessionID, err)
		sm.cleanupSession(ctx, state)
		state.UpdateStatus(manman.SessionStatusCrashed)
		sm.stateManager.RemoveSession(sessionID)
		return fmt.Errorf("failed to attach to game container: %w", err)
	}
	state.AttachResp = &attachResp
	log.Printf("[session %d] successfully attached to container", sessionID)

	// 5. Start output reader goroutine
	log.Printf("[session %d] spawning output reader goroutine", sessionID)
	sm.startOutputReader(state)

	now := time.Now()
	state.StartedAt = &now
	state.UpdateStatus(manman.SessionStatusRunning)
	log.Printf("[session %d] startup complete, status: running", sessionID)

	return nil
}

// StopSession stops a game server session
func (sm *SessionManager) StopSession(ctx context.Context, sessionID int64, force bool) error {
	state, ok := sm.stateManager.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}

	state.UpdateStatus(manman.SessionStatusStopping)

	// 1. Close attach connection
	if state.AttachResp != nil {
		state.AttachResp.Close()
		state.AttachResp = nil
	}

	// 2. Stop game container
	if state.GameContainerID != "" {
		var timeout *time.Duration
		if !force {
			t := 30 * time.Second
			timeout = &t
		}
		if err := sm.dockerClient.StopContainer(ctx, state.GameContainerID, timeout); err != nil {
			log.Printf("[session %d] warning: error stopping container: %v", sessionID, err)
		}

		// 3. Get exit code
		status, err := sm.dockerClient.GetContainerStatus(ctx, state.GameContainerID)
		if err == nil {
			exitCode := status.ExitCode
			state.ExitCode = &exitCode
		}

		// 4. Remove game container
		_ = sm.dockerClient.RemoveContainer(ctx, state.GameContainerID, true)
	}

	// 5. Remove network
	if state.NetworkID != "" {
		_ = sm.dockerClient.RemoveNetwork(ctx, state.NetworkID)
	}

	now := time.Now()
	state.StoppedAt = &now
	state.UpdateStatus(manman.SessionStatusStopped)

	// 6. Remove from state manager
	sm.stateManager.RemoveSession(sessionID)

	return nil
}

// KillSession forcefully kills a session
func (sm *SessionManager) KillSession(ctx context.Context, sessionID int64) error {
	return sm.StopSession(ctx, sessionID, true)
}

// SendInput sends stdin input to a running session
func (sm *SessionManager) SendInput(ctx context.Context, sessionID int64, input []byte) error {
	state, ok := sm.stateManager.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}
	if state.AttachResp == nil {
		return fmt.Errorf("session %d: no active stdin connection", sessionID)
	}
	_, err := state.AttachResp.Conn.Write(input)
	return err
}

// createGameContainer creates the game container directly
func (sm *SessionManager) createGameContainer(ctx context.Context, state *State, cmd *StartSessionCommand) (string, error) {
	// Get paths for this SGC
	sgcInternalDir := sm.getSGCInternalDir(state.SGCID) // Where to create dirs (inside this container)
	sgcHostDir := sm.getSGCHostDir(state.SGCID)         // What to tell Docker (host path)

	// Prepare volume mounts from configuration strategies
	// Each volume creates a subdirectory under the SGC data dir (e.g., sgc-dev-1/data, sgc-dev-1/config)
	// and mounts it to the specified container path (e.g., /data, /config)
	// No hardcoded defaults - all volumes must be explicitly configured in the database
	var volumes []string

	for _, vol := range cmd.Volumes {
		subDir := vol.HostSubpath
		if subDir == "" {
			// Use volume name as default subdirectory to avoid clashing
			subDir = vol.Name
		}

		// Create directory at internal path (mounted from host)
		internalPath := filepath.Join(sgcInternalDir, strings.TrimPrefix(subDir, "/"))
		if err := os.MkdirAll(internalPath, 0755); err != nil {
			return "", fmt.Errorf("failed to create volume directory %s: %w", internalPath, err)
		}

		// Tell Docker to bind mount from host path
		hostPath := filepath.Join(sgcHostDir, strings.TrimPrefix(subDir, "/"))
		mountStr := fmt.Sprintf("%s:%s", hostPath, vol.ContainerPath)
		// TODO: handle options (readonly etc)
		volumes = append(volumes, mountStr)
	}

	config := docker.ContainerConfig{
		Image:     cmd.Image,
		Name:      sm.getContainerName(cmd.ServerID, cmd.SGCID),
		Command:   cmd.Command,
		Env:       cmd.Env,
		NetworkID: state.NetworkID,
		Volumes:   volumes,
		Ports:     cmd.PortBindings,
		Labels: map[string]string{
			"manman.type":        "game",
			"manman.session_id":  fmt.Sprintf("%d", state.SessionID),
			"manman.sgc_id":      fmt.Sprintf("%d", state.SGCID),
			"manman.server_id":   fmt.Sprintf("%d", cmd.ServerID),
			"manman.environment": sm.environment,
			"manman.created_at":  time.Now().Format(time.RFC3339),
		},
		OpenStdin:  true,
		AutoRemove: false,
	}

	return sm.dockerClient.CreateContainer(ctx, config)
}

// handleNameConflict handles an idempotent start when a container with the same name already exists
func (sm *SessionManager) handleNameConflict(ctx context.Context, cmd *StartSessionCommand) (string, error) {
	containerName := sm.getContainerName(cmd.ServerID, cmd.SGCID)
	status, err := sm.dockerClient.GetContainerStatus(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect existing container %s: %w", containerName, err)
	}

	if status.Running {
		// Already running — idempotent success
		log.Printf("[session %d] container %s already running, reusing", cmd.SessionID, containerName)
		return status.ContainerID, nil
	}

	// Stopped — restart it
	log.Printf("[session %d] container %s stopped, restarting", cmd.SessionID, containerName)
	if err := sm.dockerClient.StartContainer(ctx, status.ContainerID); err != nil {
		return "", fmt.Errorf("failed to restart existing container %s: %w", containerName, err)
	}
	return status.ContainerID, nil
}

func isNameConflictError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already in use") || strings.Contains(msg, "Conflict")
}

// cleanupSession cleans up containers and network for a session
func (sm *SessionManager) cleanupSession(ctx context.Context, state *State) {
	if state.AttachResp != nil {
		state.AttachResp.Close()
		state.AttachResp = nil
	}

	if state.GameContainerID != "" {
		_ = sm.dockerClient.RemoveContainer(ctx, state.GameContainerID, true)
	}

	if state.NetworkID != "" {
		_ = sm.dockerClient.RemoveNetwork(ctx, state.NetworkID)
	}
}

// startOutputReader spawns a goroutine that reads the Docker multiplexed stream
func (sm *SessionManager) startOutputReader(state *State) {
	go func() {
		// Buffer for batching log messages
		const bufferSize = 50
		const flushInterval = 1 * time.Second

		logBuffer := make([]string, 0, bufferSize)
		sourceBuffer := make([]string, 0, bufferSize)
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()

		// Channel to receive log messages from reader goroutine
		logChan := make(chan struct {
			message string
			source  string
		}, 10)

		// Start a separate goroutine to read from Docker stream
		go func() {
			defer close(logChan)
			for {
				// Read 8-byte header: [streamType, 0, 0, 0, size(4 bytes big-endian)]
				header := make([]byte, 8)
				if _, err := io.ReadFull(state.AttachResp.Reader, header); err != nil {
					// EOF or closed — container exited or attach was closed
					return
				}
				size := binary.BigEndian.Uint32(header[4:8])
				data := make([]byte, size)
				if _, err := io.ReadFull(state.AttachResp.Reader, data); err != nil {
					return
				}

				message := string(data)
				var source string
				if header[0] == 2 {
					source = "stderr"
					log.Printf("[session %d stderr] %s", state.SessionID, message)
				} else {
					source = "stdout"
					log.Printf("[session %d stdout] %s", state.SessionID, message)
				}

				// Send to channel (non-blocking to avoid deadlock)
				select {
				case logChan <- struct {
					message string
					source  string
				}{message, source}:
				default:
					log.Printf("[session %d] warning: log channel full, dropping message", state.SessionID)
				}
			}
		}()

		flushLogs := func() {
			if len(logBuffer) == 0 {
				return
			}

			// Publish logs to RabbitMQ in background
			if sm.rmqPublisher != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()

				for i := 0; i < len(logBuffer); i++ {
					// Fire-and-forget publish, don't block on errors
					if err := sm.rmqPublisher.PublishLog(ctx, state.SessionID, sourceBuffer[i], logBuffer[i]); err != nil {
						log.Printf("[session %d] warning: failed to publish log to RabbitMQ: %v", state.SessionID, err)
					}
				}
			}

			// Clear buffers
			logBuffer = logBuffer[:0]
			sourceBuffer = sourceBuffer[:0]
		}

		// Flush logs on exit
		defer flushLogs()

		// Main event loop
		for {
			select {
			case <-ticker.C:
				// Periodic flush every second
				flushLogs()

			case logMsg, ok := <-logChan:
				if !ok {
					// Channel closed - container exited
					sm.handleContainerExit(state)
					return
				}

				// Add to buffer
				logBuffer = append(logBuffer, logMsg.message)
				sourceBuffer = append(sourceBuffer, logMsg.source)

				// Flush if buffer is full
				if len(logBuffer) >= bufferSize {
					flushLogs()
				}
			}
		}
	}()
}

// handleContainerExit is called when the output reader detects the stream has closed
func (sm *SessionManager) handleContainerExit(state *State) {
	if state.GetStatus() != "running" {
		return // already stopping/stopped — not a crash
	}
	ctx := context.Background()
	status, err := sm.dockerClient.GetContainerStatus(ctx, state.GameContainerID)
	if err != nil || status.Running {
		return
	}
	exitCode := status.ExitCode
	state.ExitCode = &exitCode
	state.UpdateStatus("crashed")
	log.Printf("[session %d] container exited with code %d — marked crashed", state.SessionID, exitCode)
}

// CleanupOrphans performs a single pass of orphan game container cleanup
func (sm *SessionManager) CleanupOrphans(ctx context.Context, serverID int64) error {
	fmt.Printf("Starting orphan cleanup for server %d (env=%s)\n", serverID, sm.environment)

	activeSGCs := sm.stateManager.GetActiveSGCIDs()

	gameFilters := map[string]string{
		"manman.type":      "game",
		"manman.server_id": fmt.Sprintf("%d", serverID),
	}
	if sm.environment != "" {
		gameFilters["manman.environment"] = sm.environment
	}
	games, err := sm.dockerClient.ListContainers(ctx, gameFilters)
	if err != nil {
		return fmt.Errorf("failed to list game containers: %w", err)
	}

	now := time.Now()
	gracePeriod := 5 * time.Minute

	for _, game := range games {
		status, err := sm.dockerClient.GetContainerStatus(ctx, game.ID)
		if err != nil {
			continue
		}

		// Double check labels if they weren't filtered by Docker API
		if svrID, ok := status.Labels["manman.server_id"]; !ok || svrID != fmt.Sprintf("%d", serverID) {
			continue
		}

		if sm.environment != "" {
			if env, ok := status.Labels["manman.environment"]; !ok || env != sm.environment {
				continue
			}
		} else {
			// If we don't have an environment set, skip containers that DO have one set
			if env, ok := status.Labels["manman.environment"]; ok && env != "" {
				continue
			}
		}

		sgcIDStr, ok := status.Labels["manman.sgc_id"]
		if !ok {
			continue
		}
		sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
		if err != nil {
			continue
		}

		if activeSGCs[sgcID] {
			continue
		}

		createdAtStr, ok := status.Labels["manman.created_at"]
		if !ok {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			continue
		}

		age := now.Sub(createdAt)
		if age < gracePeriod {
			continue
		}

		fmt.Printf("Cleaning up orphaned game container %s (sgc_id=%d, age=%v)\n", game.ID, sgcID, age)
		if status.Running {
			_ = sm.dockerClient.StopContainer(ctx, game.ID, nil)
		}
		_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
	}

	fmt.Println("Orphan cleanup completed")
	return nil
}

// GetSessionStats returns statistics about all sessions
func (sm *SessionManager) GetSessionStats() SessionStats {
	return sm.stateManager.GetSessionStats()
}

// GetSessionState retrieves the state for a specific session
func (sm *SessionManager) GetSessionState(sessionID int64) (*State, bool) {
	return sm.stateManager.GetSession(sessionID)
}
