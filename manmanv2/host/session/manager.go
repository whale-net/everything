package session

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/host/config"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

const (
	// InternalDataDir is the well-known path inside this container where session data is stored.
	// The host's data directory should be mounted here. Exported so the workshop orchestrator
	// can create volume directories under the same tree before spawning download containers.
	InternalDataDir = "/var/lib/manman/sessions"

	// logCheckpointFileName holds the RFC3339Nano resume point (one nanosecond
	// past the last game container log line published to RabbitMQ) for an SGC.
	// It lives under the SGC's data dir (bind-mounted from the host), so it
	// survives host-manager restarts and lets RecoverOrphanedSessions resume
	// from it via `since` instead of replaying a running container's entire
	// log history.
	logCheckpointFileName = ".last_log_ts"
)

// SessionManager manages the lifecycle of game server sessions
type SessionManager struct {
	dockerClient         *docker.Client
	stateManager         *Manager
	environment          string
	hostDataDir          string // Path on the host where session data lives (for Docker bind mounts)
	grpcClient           pb.ManManAPIClient
	renderer             *config.Renderer
	workshopOrchestrator WorkshopOrchestrator
	rmqPublisher         interface {
		PublishLog(ctx context.Context, sessionID int64, source string, message string) error
		PublishSessionStatus(ctx context.Context, update *hostrmq.SessionStatusUpdate) error
	}
}

// WorkshopOrchestrator defines the interface for workshop addon downloads
type WorkshopOrchestrator interface {
	EnsureLibraryAddonsInstalled(ctx context.Context, sgcID int64, heartbeatFn func()) error
}

// NewSessionManager creates a new session manager
func NewSessionManager(
	dockerClient *docker.Client,
	environment string,
	hostDataDir string,
	grpcClient pb.ManManAPIClient,
	workshopOrchestrator WorkshopOrchestrator,
	rmqPublisher interface {
		PublishLog(ctx context.Context, sessionID int64, source string, message string) error
		PublishSessionStatus(ctx context.Context, update *hostrmq.SessionStatusUpdate) error
	},
) *SessionManager {
	return &SessionManager{
		dockerClient:         dockerClient,
		stateManager:         NewManager(),
		environment:          environment,
		hostDataDir:          hostDataDir,
		grpcClient:           grpcClient,
		workshopOrchestrator: workshopOrchestrator,
		renderer:             config.NewRenderer(nil),
		rmqPublisher:         rmqPublisher,
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
	VolumeType    string
	Options       map[string]string
	IsEnabled     bool
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
	return filepath.Join(InternalDataDir, dirName)
}

// getSGCHostDir returns the path to SGC data on the host (for Docker bind mounts)
func (sm *SessionManager) getSGCHostDir(sgcID int64) string {
	dirName := fmt.Sprintf("sgc-%d", sgcID)
	if sm.environment != "" {
		dirName = fmt.Sprintf("sgc-%s-%d", sm.environment, sgcID)
	}
	return filepath.Join(sm.hostDataDir, dirName)
}

// readLogCheckpoint returns the persisted resume point for an SGC's game
// container — one nanosecond past the last log line published — formatted
// for Docker's `since` log filter directly. Returns "" if no checkpoint has
// been persisted (e.g. first recovery after upgrading to this feature), so
// callers should treat that as "replay everything".
func (sm *SessionManager) readLogCheckpoint(sgcID int64) string {
	path := filepath.Join(sm.getSGCInternalDir(sgcID), logCheckpointFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	ts := strings.TrimSpace(string(data))
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		slog.Warn("ignoring unparseable log checkpoint", "sgc_id", sgcID, "error", err)
		return ""
	}
	return ts
}

// writeLogCheckpoint persists the point to resume reading from on a future
// recovery: one nanosecond past the last log line published for an SGC's game
// container. Docker's `since` log filter is inclusive (it only drops lines
// strictly before `since`), so storing the last-published timestamp itself
// would cause that exact line to be replayed on every restart; storing it +1ns
// makes `since` resolve to "everything after what we've already sent" instead.
func (sm *SessionManager) writeLogCheckpoint(sgcID int64, ts time.Time) {
	dir := sm.getSGCInternalDir(sgcID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("failed to create sgc dir for log checkpoint", "sgc_id", sgcID, "error", err)
		return
	}
	path := filepath.Join(dir, logCheckpointFileName)
	tmpFile, err := os.CreateTemp(dir, logCheckpointFileName+".tmp-*")
	if err != nil {
		slog.Warn("failed to create temp file for log checkpoint", "sgc_id", sgcID, "error", err)
		return
	}
	tmp := tmpFile.Name()
	_, writeErr := tmpFile.WriteString(ts.Add(time.Nanosecond).UTC().Format(time.RFC3339Nano))
	closeErr := tmpFile.Close()
	if writeErr != nil || closeErr != nil {
		slog.Warn("failed to write log checkpoint", "sgc_id", sgcID, "write_error", writeErr, "close_error", closeErr)
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Warn("failed to rename log checkpoint into place", "sgc_id", sgcID, "error", err)
		_ = os.Remove(tmp)
	}
}

// clearLogCheckpoint removes any persisted log checkpoint for an SGC. Called
// when a new game container is created for the SGC so that, if host-manager
// crashes before the new container publishes (and checkpoints) its own first
// log line, a later RecoverOrphanedSessions falls back to replaying that new
// container's logs from the start rather than resuming from a checkpoint that
// actually describes a prior, now-replaced container instance — which could
// otherwise silently skip the new container's early logs under host clock
// skew (e.g. an NTP step) across the crash/restart.
func (sm *SessionManager) clearLogCheckpoint(sgcID int64) {
	path := filepath.Join(sm.getSGCInternalDir(sgcID), logCheckpointFileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to clear log checkpoint", "sgc_id", sgcID, "error", err)
	}
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
		slog.Info("force start: stopping existing active session", "session_id", sessionID, "existing_session_id", existingSession.SessionID, "sgc_id", sgcID)
		if err := sm.StopSession(ctx, existingSession.SessionID, true); err != nil {
			slog.Warn("failed to stop existing session", "session_id", sessionID, "existing_session_id", existingSession.SessionID, "error", err)
			// Continue anyway, we'll attempt container cleanup below
		}
	}

	// 3. If force is requested (or even if not, as a safety measure), clean up any existing container for this SGC
	if cmd.Force {
		containerName := sm.getContainerName(cmd.ServerID, cmd.SGCID)
		slog.Info("force start requested, cleaning up existing container", "session_id", sessionID, "container", containerName)

		shutdownTimeout := 10 * time.Second
		if err := sm.dockerClient.StopContainer(ctx, containerName, &shutdownTimeout); err != nil {
			slog.Debug("graceful shutdown failed or container not found", "session_id", sessionID, "error", err)
		} else {
			slog.Info("graceful shutdown succeeded", "session_id", sessionID, "container", containerName)
		}

		if err := sm.dockerClient.RemoveContainer(ctx, containerName, true); err != nil {
			slog.Warn("container removal failed", "session_id", sessionID, "error", err)
		} else {
			slog.Info("container removed", "session_id", sessionID, "container", containerName)
		}
	}

	// Create session state
	state := &State{
		SessionID: sessionID,
		SGCID:     sgcID,
		Status:    manman.SessionStatusPending,
	}
	sm.stateManager.AddSession(state)
	slog.Debug("session added to state manager", "session_id", sessionID)
	state.UpdateStatus(manman.SessionStatusStarting)

	// 1. Skip custom network - use default bridge for external port access
	slog.Info("using default bridge network", "session_id", sessionID)
	state.NetworkID = ""
	state.NetworkName = ""

	// 2. Fetch and render configurations
	slog.Info("fetching configuration strategies", "session_id", sessionID)
	configResp, err := sm.grpcClient.GetSessionConfiguration(ctx, &pb.GetSessionConfigurationRequest{
		SessionId: sessionID,
	})
	if err != nil {
		slog.Warn("failed to fetch configurations", "session_id", sessionID, "error", err)
		// Don't fail the session start - configurations are optional
		configResp = &pb.GetSessionConfigurationResponse{Configurations: nil}
	} else {
		slog.Info("fetched configuration strategies", "session_id", sessionID, "count", len(configResp.Configurations))

		// Render configurations
		if len(configResp.Configurations) > 0 {
			sgcInternalDir := sm.getSGCInternalDir(cmd.SGCID)
			renderedFiles, err := sm.renderer.RenderConfigurations(configResp.Configurations, sgcInternalDir)
			if err != nil {
				slog.Error("failed to render configurations", "session_id", sessionID, "error", err)
				sm.cleanupSession(ctx, state)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return fmt.Errorf("failed to render configurations: %w", err)
			}

			// Write rendered files to disk
			if len(renderedFiles) > 0 {
				slog.Info("writing configuration files", "session_id", sessionID, "count", len(renderedFiles))
				if err := sm.renderer.WriteRenderedFiles(renderedFiles); err != nil {
					slog.Error("failed to write configuration files", "session_id", sessionID, "error", err)
					sm.cleanupSession(ctx, state)
					state.UpdateStatus(manman.SessionStatusCrashed)
					sm.stateManager.RemoveSession(sessionID)
					return fmt.Errorf("failed to write configuration files: %w", err)
				}
				slog.Debug("configuration files written", "session_id", sessionID)
			}
		}
	}

	// 3. Download workshop addons from libraries (blocking)
	if sm.workshopOrchestrator != nil {
		slog.Info("downloading workshop addons from libraries", "session_id", sessionID, "sgc_id", cmd.SGCID)

		// Heartbeat function to keep session alive during download
		heartbeatFn := func() {
			if sm.rmqPublisher != nil {
				update := &hostrmq.SessionStatusUpdate{
					SessionID: sessionID,
					Status:    manman.SessionStatusStarting,
				}
				if err := sm.rmqPublisher.PublishSessionStatus(ctx, update); err != nil {
					slog.Warn("failed to publish heartbeat", "session_id", sessionID, "error", err)
				}
			}
		}

		if err := sm.workshopOrchestrator.EnsureLibraryAddonsInstalled(ctx, cmd.SGCID, heartbeatFn); err != nil {
			slog.Error("failed to download workshop addons", "session_id", sessionID, "error", err)
			sm.cleanupSession(ctx, state)
			state.UpdateStatus(manman.SessionStatusCrashed)
			sm.stateManager.RemoveSession(sessionID)
			return fmt.Errorf("failed to download workshop addons: %w", err)
		}
		slog.Info("workshop addons downloaded successfully", "session_id", sessionID)
	}

	// 4. Pull game image (always pull to ensure latest version is used)
	// TODO: add cache fallback
	slog.Info("pulling image", "session_id", sessionID, "image", cmd.Image)
	const maxPullAttempts = 3
	var pullErr error
	for attempt := 1; attempt <= maxPullAttempts; attempt++ {
		pullErr = sm.dockerClient.PullImage(ctx, cmd.Image)
		if pullErr == nil {
			break
		}
		slog.Warn("failed to pull image, retrying", "session_id", sessionID, "image", cmd.Image, "attempt", attempt, "error", pullErr)
		if attempt < maxPullAttempts {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	if pullErr != nil {
		slog.Error("failed to pull image after retries", "session_id", sessionID, "image", cmd.Image, "error", pullErr)
		sm.cleanupSession(ctx, state)
		state.UpdateStatus(manman.SessionStatusCrashed)
		sm.stateManager.RemoveSession(sessionID)
		return &rmq.PermanentError{Err: fmt.Errorf("failed to pull image %s: %w", cmd.Image, pullErr)}
	}

	// 5. Create game container
	slog.Info("creating container", "session_id", sessionID, "image", cmd.Image)
	containerID, err := sm.createGameContainer(ctx, state, cmd)
	if err != nil {
		if isNameConflictError(err) {
			if !cmd.Force {
				slog.Error("container name conflict, force=false", "session_id", sessionID, "sgc_id", sgcID)
				sm.cleanupSession(ctx, state)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return &rmq.PermanentError{Err: fmt.Errorf("container name conflict for GSC %d", sgcID)}
			}

			slog.Info("container name conflict, handling", "session_id", sessionID)
			containerID, err = sm.handleNameConflict(ctx, cmd)
			if err != nil {
				slog.Error("failed to handle name conflict", "session_id", sessionID, "error", err)
				sm.cleanupSession(ctx, state)
				state.UpdateStatus(manman.SessionStatusCrashed)
				sm.stateManager.RemoveSession(sessionID)
				return fmt.Errorf("failed to handle name conflict: %w", err)
			}
			slog.Info("resolved name conflict", "session_id", sessionID, "container_id", containerID)
		} else {
			slog.Error("failed to create container", "session_id", sessionID, "error", err)
			sm.cleanupSession(ctx, state)
			state.UpdateStatus(manman.SessionStatusCrashed)
			sm.stateManager.RemoveSession(sessionID)
			return fmt.Errorf("failed to create game container: %w", err)
		}
	}
	slog.Info("container created", "session_id", sessionID, "container_id", containerID)
	state.GameContainerID = containerID

	// Any checkpoint on disk describes a prior container instance for this SGC
	// (or none at all) — never this brand-new one. Clear it before starting the
	// container so a crash before this container's first checkpoint write can't
	// leave a stale, unrelated checkpoint for RecoverOrphanedSessions to use.
	sm.clearLogCheckpoint(sgcID)

	// 6. Start game container
	slog.Info("starting container", "session_id", sessionID, "container_id", containerID)
	if err := sm.dockerClient.StartContainer(ctx, containerID); err != nil {
		if !strings.Contains(err.Error(), "already started") {
			slog.Error("failed to start container", "session_id", sessionID, "error", err)
			sm.cleanupSession(ctx, state)
			state.UpdateStatus(manman.SessionStatusCrashed)
			sm.stateManager.RemoveSession(sessionID)
			return fmt.Errorf("failed to start game container: %w", err)
		}
		slog.Info("container was already started", "session_id", sessionID)
	}
	slog.Info("container started", "session_id", sessionID, "container_id", containerID)

	// 7. Stream logs using Docker logs API (doesn't interfere with stdin during startup)
	// timestamps=true so the log reader can persist a checkpoint of the last log
	// emitted, letting a later restart re-attach without replaying already-sent logs.
	slog.Info("starting log stream", "session_id", sessionID)
	logReader, err := sm.dockerClient.GetContainerLogs(ctx, containerID, true, "all", "", true)
	if err != nil {
		slog.Error("failed to get container logs", "session_id", sessionID, "error", err)
		sm.cleanupSession(ctx, state)
		state.UpdateStatus(manman.SessionStatusCrashed)
		sm.stateManager.RemoveSession(sessionID)
		return fmt.Errorf("failed to get container logs: %w", err)
	}
	state.LogReader = logReader
	state.AttachStrategy = "persistent"
	state.IsTTY = true             // Always use TTY mode

	// 8. Start log reader goroutine
	slog.Debug("spawning log reader", "session_id", sessionID)
	sm.startLogReader(state)

	now := time.Now()
	state.StartedAt = &now
	state.UpdateStatus(manman.SessionStatusRunning)
	slog.Info("session startup complete", "session_id", sessionID)

	return nil
}

// StopSession stops a game server session
func (sm *SessionManager) StopSession(ctx context.Context, sessionID int64, force bool) error {
	state, ok := sm.stateManager.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}

	slog.Info("stopping session", "session_id", sessionID, "sgc_id", state.SGCID, "force", force, "container_id", state.GameContainerID)
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
			slog.Warn("error stopping container", "session_id", sessionID, "error", err)
		}

		// 3. Get exit code
		status, err := sm.dockerClient.GetContainerStatus(ctx, state.GameContainerID)
		if err == nil {
			exitCode := status.ExitCode
			state.ExitCode = &exitCode
			slog.Info("container stopped", "session_id", sessionID, "exit_code", exitCode)
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
	slog.Info("session stopped and removed from state", "session_id", sessionID, "sgc_id", state.SGCID)

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

	state.mu.Lock()
	defer state.mu.Unlock()

	// Lazy attach: attach only when sending command
	if state.AttachResp == nil {
		slog.Debug("attaching to container for command", "session_id", sessionID)
		attachResp, err := sm.dockerClient.AttachToContainer(ctx, state.GameContainerID)
		if err != nil {
			return fmt.Errorf("failed to attach to container: %w", err)
		}
		state.AttachResp = &attachResp
	}

	// Write command to stdin
	_, err := state.AttachResp.Conn.Write(input)
	if err != nil {
		slog.Error("failed to write stdin", "session_id", sessionID, "error", err)
		return err
	}
	slog.Debug("sent stdin input", "session_id", sessionID, "bytes", len(input))

	// For lazy strategy, detach after sending command
	if state.AttachStrategy == "lazy" {
		state.AttachResp.Close()
		state.AttachResp = nil
		slog.Debug("detached after command", "session_id", sessionID)
	}

	return nil
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

	// Volume Permission Strategy:
	// We use 0777 (world-writable) permissions for all volume directories to support:
	// 1. Host process (runs as root) - needs to create/modify files
	// 2. Game containers (e.g., CS2 runs as steam user, UID 1000) - need to write game data
	// 3. Auxiliary containers (e.g., steam workshop downloader) - may run as different UIDs
	// 4. Multi-container scenarios - multiple containers accessing the same volumes
	//
	// This is appropriate for our use case because:
	// - The host environment is trusted
	// - Containers are isolated via Docker
	// - We always run Docker as root for simplicity
	// - Complex UID mapping would be harder to maintain across different game server images
	//
	// Alternative approaches considered:
	// - chown to specific UID (e.g., 1000): Doesn't work for multi-container scenarios
	// - User namespaces: Adds complexity and may not be compatible with all game server images
	for _, vol := range cmd.Volumes {
		var mountStr string
		
		if vol.VolumeType == "named" {
			// Use Docker named volume (auto-initialized with container's files on first use)
			volumeName := sm.getNamedVolumeName(state.SGCID, vol.Name)
			mountStr = fmt.Sprintf("%s:%s", volumeName, vol.ContainerPath)
			slog.Info("using named volume", "volume", volumeName, "container_path", vol.ContainerPath)
		} else {
			// Use bind mount (default behavior)
			subDir := vol.HostSubpath
			if subDir == "" {
				subDir = vol.Name
			}

			internalPath := filepath.Join(sgcInternalDir, strings.TrimPrefix(subDir, "/"))
			if err := os.MkdirAll(internalPath, 0777); err != nil {
				return "", fmt.Errorf("failed to create volume directory %s: %w", internalPath, err)
			}

			hostPath := filepath.Join(sgcHostDir, strings.TrimPrefix(subDir, "/"))
			mountStr = fmt.Sprintf("%s:%s", hostPath, vol.ContainerPath)
		}
		
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

func (sm *SessionManager) getNamedVolumeName(sgcID int64, volumeName string) string {
	if sm.environment != "" {
		return fmt.Sprintf("manman-sgc-%s-%d-%s", sm.environment, sgcID, volumeName)
	}
	return fmt.Sprintf("manman-sgc-%d-%s", sgcID, volumeName)
}

// handleNameConflict handles an idempotent start when a container with the same name already exists
func (sm *SessionManager) handleNameConflict(ctx context.Context, cmd *StartSessionCommand) (string, error) {
	containerName := sm.getContainerName(cmd.ServerID, cmd.SGCID)
	status, err := sm.dockerClient.GetContainerStatus(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect existing container %s: %w", containerName, err)
	}

	if status.Running {
		slog.Info("container already running, reusing", "session_id", cmd.SessionID, "container", containerName)
		return status.ContainerID, nil
	}

	slog.Info("container stopped, restarting", "session_id", cmd.SessionID, "container", containerName)
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
	if state.LogReader != nil {
		state.LogReader.Close()
		state.LogReader = nil
	}

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
func (sm *SessionManager) startLogReader(state *State) {
	sm.startStreamReaderWithFormat(state, state.LogReader, state.IsTTY)
}

func (sm *SessionManager) startOutputReader(state *State) {
	sm.startStreamReaderWithFormat(state, state.AttachResp.Reader, state.IsTTY)
}

// startStreamReader reads from a Docker multiplexed stream and publishes logs to RabbitMQ
func (sm *SessionManager) startStreamReader(state *State, reader io.Reader) {
	sm.startStreamReaderWithFormat(state, reader, false) // Default: multiplexed format
}

// startStreamReaderWithFormat reads from a Docker stream (multiplexed or TTY) and publishes logs to RabbitMQ
func (sm *SessionManager) startStreamReaderWithFormat(state *State, reader io.Reader, isTTY bool) {
	go func() {
		const bufferSize = 50
		const flushInterval = 1 * time.Second

		var mu sync.Mutex
		logBuffer := make([]string, 0, bufferSize)
		sourceBuffer := make([]string, 0, bufferSize)
		tsBuffer := make([]time.Time, 0, bufferSize) // parallel to logBuffer; zero value if unparseable
		var stdoutCount, stderrCount, errorCount, warnCount int

		// flushSeq serializes whole flushLogs executions. Both the 1s ticker and
		// addMessage's shouldFlush path can call flushLogs, and without this,
		// two concurrent calls could publish and checkpoint out of order — e.g.
		// a smaller/faster batch's checkpoint write landing after a larger/
		// slower batch's, regressing the on-disk checkpoint backwards and
		// causing the skipped-over lines to replay on the next restart, or
		// corrupting the checkpoint file via concurrent writers. This is
		// separate from mu (which only guards the shared buffers) so a flush's
		// publish/checkpoint I/O never blocks the reader goroutine from
		// continuing to append new lines.
		var flushSeq sync.Mutex

		// flushLogs snapshots and clears the buffer under the lock, publishes each
		// line, then persists a log checkpoint — but only up through the longest
		// prefix of the batch that was actually handed to the publisher without
		// error. This must run in that order (publish, THEN checkpoint) and must
		// never jump past a failed line: RecoverOrphanedSessions resumes reading
		// container logs from the checkpoint's `since` value, so checkpointing a
		// line before confirming its publish attempt succeeded — or checkpointing
		// past a line whose publish failed — would permanently drop that line the
		// next time host-manager restarts and re-attaches.
		flushLogs := func() {
			flushSeq.Lock()
			defer flushSeq.Unlock()

			mu.Lock()
			if len(logBuffer) == 0 {
				mu.Unlock()
				return
			}
			logsCopy := append([]string(nil), logBuffer...)
			sourcesCopy := append([]string(nil), sourceBuffer...)
			tsCopy := append([]time.Time(nil), tsBuffer...)
			sc, strc, ec, wc := stdoutCount, stderrCount, errorCount, warnCount
			logBuffer = logBuffer[:0]
			sourceBuffer = sourceBuffer[:0]
			tsBuffer = tsBuffer[:0]
			stdoutCount, stderrCount, errorCount, warnCount = 0, 0, 0, 0
			mu.Unlock()

			if sc > 0 || strc > 0 {
				slog.Info("session log metrics",
					"session_id", state.SessionID,
					"total", sc+strc,
					"stdout", sc,
					"stderr", strc,
					"errors", ec,
					"warnings", wc)
			}

			var checkpointTS time.Time
			if sm.rmqPublisher != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				failed := false
				for i := range logsCopy {
					if err := sm.rmqPublisher.PublishLog(ctx, state.SessionID, sourcesCopy[i], logsCopy[i]); err != nil {
						slog.Warn("failed to publish log to RabbitMQ", "session_id", state.SessionID, "error", err)
						failed = true
						continue // best-effort: still attempt the rest of the batch
					}
					if !failed && !tsCopy[i].IsZero() {
						checkpointTS = tsCopy[i] // only advance through the unbroken successful prefix
					}
				}
			}

			if !checkpointTS.IsZero() {
				sm.writeLogCheckpoint(state.SGCID, checkpointTS)
			}
		}

		addMessage := func(raw, source string) {
			message := raw
			var ts time.Time
			if parsedTS, rest, ok := docker.SplitLogTimestamp(raw); ok {
				message = rest
				ts = parsedTS
			}
			isStderr := source == "stderr"
			msgLower := strings.ToLower(message)
			isError := strings.Contains(msgLower, "error") || strings.Contains(msgLower, "exception") || strings.Contains(msgLower, "fatal")
			isWarn := strings.Contains(msgLower, "warn")

			mu.Lock()
			logBuffer = append(logBuffer, message)
			tsBuffer = append(tsBuffer, ts)
			sourceBuffer = append(sourceBuffer, source)
			if isStderr {
				stderrCount++
			} else {
				stdoutCount++
			}
			if isError {
				errorCount++
			}
			if isWarn {
				warnCount++
			}
			shouldFlush := len(logBuffer) >= bufferSize
			mu.Unlock()
			if shouldFlush {
				flushLogs()
			}
		}

		done := make(chan struct{})

		go func() {
			defer close(done)
			defer sm.handleContainerExit(state)
			if isTTY {
				scanner := bufio.NewScanner(reader)
				for scanner.Scan() {
					addMessage(scanner.Text(), "stdout")
				}
			} else {
				for {
					header := make([]byte, 8)
					if _, err := io.ReadFull(reader, header); err != nil {
						return
					}
					size := binary.BigEndian.Uint32(header[4:8])
					data := make([]byte, size)
					if _, err := io.ReadFull(reader, data); err != nil {
						return
					}
					source := "stdout"
					if header[0] == 2 {
						source = "stderr"
					}
					addMessage(string(data), source)
				}
			}
		}()

		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		defer flushLogs()

		for {
			select {
			case <-ticker.C:
				flushLogs()
			case <-done:
				return
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
	slog.Warn("container exited, marked crashed", "session_id", state.SessionID, "exit_code", exitCode)

	// Publish crashed status to RabbitMQ
	statusUpdate := &hostrmq.SessionStatusUpdate{
		SessionID: state.SessionID,
		SGCID:     state.SGCID,
		Status:    "crashed",
		ExitCode:  &exitCode,
	}
	if err := sm.rmqPublisher.PublishSessionStatus(ctx, statusUpdate); err != nil {
		slog.Error("failed to publish crashed status", "session_id", state.SessionID, "error", err)
	}

	// Clean up the crashed container
	slog.Info("removing crashed container", "session_id", state.SessionID, "container_id", state.GameContainerID)
	if err := sm.dockerClient.RemoveContainer(ctx, state.GameContainerID, true); err != nil {
		slog.Warn("failed to remove crashed container", "session_id", state.SessionID, "error", err)
	}

	// Remove session from state manager to allow new sessions for this SGC
	sm.stateManager.RemoveSession(state.SessionID)
	slog.Info("removed session from state manager after crash", "session_id", state.SessionID)
}

// CleanupOrphans performs a single pass of orphan game container cleanup
func (sm *SessionManager) CleanupOrphans(ctx context.Context, serverID int64) error {
	slog.Info("starting orphan cleanup", "server_id", serverID, "environment", sm.environment)

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

		slog.Info("cleaning up orphaned game container", "container_id", game.ID, "sgc_id", sgcID, "age", age)
		if status.Running {
			_ = sm.dockerClient.StopContainer(ctx, game.ID, nil)
		}
		_ = sm.dockerClient.RemoveContainer(ctx, game.ID, true)
	}

	slog.Info("orphan cleanup completed")
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

// GetSessionStateBySGCID retrieves the active session state for a given SGC
func (sm *SessionManager) GetSessionStateBySGCID(sgcID int64) (*State, bool) {
	return sm.stateManager.GetSessionBySGCID(sgcID)
}
