package session

import (
	"context"
	"fmt"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/host/grpc"
	pb "github.com/whale-net/everything/manman/protos"
)

// SessionManager manages the lifecycle of game server sessions
type SessionManager struct {
	// dockerClient *docker.Client  // Commented out - docker SDK dependency issues
	stateManager  *Manager
	wrapperImage  string // Docker image for the wrapper container
}

// NewSessionManager creates a new session manager
func NewSessionManager(dockerClient *docker.Client, wrapperImage string) *SessionManager {
	return &SessionManager{
		dockerClient: dockerClient,
		stateManager: NewManager(),
		wrapperImage: wrapperImage,
	}
}

// StartSession starts a new game server session
func (sm *SessionManager) StartSession(ctx context.Context, cmd *StartSessionCommand) error {
	sessionID := cmd.SessionID
	sgcID := cmd.SGCID

	// Check if session already exists to prevent orphaned containers
	if _, exists := sm.stateManager.GetSession(sessionID); exists {
		return fmt.Errorf("session %d already exists", sessionID)
	}

	// Create session state
	state := &State{
		SessionID: sessionID,
		SGCID:     sgcID,
		Status:    manman.SessionStatusPending,
	}
	sm.stateManager.AddSession(state)
	state.UpdateStatus(manman.SessionStatusStarting)

	// 1. Create Docker network
	networkName := fmt.Sprintf("session-%d", sessionID)
	networkLabels := map[string]string{
		"manman.session_id": fmt.Sprintf("%d", sessionID),
		"manman.server_id":   fmt.Sprintf("%d", cmd.ServerID),
	}
	networkID, err := sm.dockerClient.CreateNetwork(ctx, networkName, networkLabels)
	if err != nil {
		state.UpdateStatus(manman.SessionStatusCrashed)
		return fmt.Errorf("failed to create network: %w", err)
	}
	state.NetworkID = networkID
	state.NetworkName = networkName

	// 2. Create wrapper container
	wrapperContainerID, err := sm.createWrapperContainer(ctx, state, cmd)
	if err != nil {
		sm.cleanupSession(ctx, state)
		state.UpdateStatus(manman.SessionStatusCrashed)
		return fmt.Errorf("failed to create wrapper container: %w", err)
	}
	state.WrapperContainerID = wrapperContainerID

	// 3. Start wrapper container
	if err := sm.dockerClient.StartContainer(ctx, wrapperContainerID); err != nil {
		sm.cleanupSession(ctx, state)
		state.UpdateStatus(manman.SessionStatusCrashed)
		return fmt.Errorf("failed to start wrapper container: %w", err)
	}

	// 4. Wait for wrapper to be ready and connect gRPC
	// TODO: Implement health check / readiness probe
	time.Sleep(2 * time.Second) // Temporary: wait for wrapper to start

	grpcAddress := fmt.Sprintf("%s:50051", networkName) // Wrapper exposes gRPC on network
	grpcClient, err := grpcclient.NewClient(ctx, grpcAddress)
	if err != nil {
		sm.cleanupSession(ctx, state)
		state.UpdateStatus(manman.SessionStatusCrashed)
		return fmt.Errorf("failed to connect to wrapper: %w", err)
	}
	state.GRPCClient = grpcClient
	state.WrapperClient = grpc.NewWrapperControlClient(grpcClient)

	// 5. Call wrapper.Start() with game config
	startReq := &pb.StartRequest{
		SessionId: sessionID,
		SgcId:     sgcID,
		Parameters: cmd.ParametersJSON,
	}

	// Convert game config maps to protobuf messages
	// This is a simplified version - in practice, you'd need proper conversion
	if cmd.GameConfig != nil {
		startReq.GameConfig = convertGameConfig(cmd.GameConfig)
	}
	if cmd.ServerGameConfig != nil {
		startReq.ServerGameConfig = convertServerGameConfig(cmd.ServerGameConfig)
	}

	startResp, err := state.WrapperClient.Start(ctx, startReq)
	if err != nil {
		sm.cleanupSession(ctx, state)
		state.UpdateStatus(manman.SessionStatusCrashed)
		return fmt.Errorf("failed to start game via wrapper: %w", err)
	}

	if !startResp.Success {
		sm.cleanupSession(ctx, state)
		state.UpdateStatus(manman.SessionStatusCrashed)
		return fmt.Errorf("wrapper failed to start game: %s", startResp.ErrorMessage)
	}

	state.GameContainerID = startResp.ContainerId
	now := time.Now()
	state.StartedAt = &now
	state.UpdateStatus(manman.SessionStatusRunning)

	return nil
}

// StopSession stops a game server session
func (sm *SessionManager) StopSession(ctx context.Context, sessionID int64, force bool) error {
	state, ok := sm.stateManager.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}

	state.UpdateStatus(manman.SessionStatusStopping)

	// 1. Call wrapper.Stop()
	if state.WrapperClient != nil {
		stopReq := &pb.StopRequest{
			SessionId: sessionID,
			Force:     force,
		}
		stopResp, err := state.WrapperClient.Stop(ctx, stopReq)
		if err != nil {
			// Log error but continue with cleanup
			fmt.Printf("Error stopping wrapper: %v\n", err)
		} else if stopResp.ExitCode != 0 {
			exitCode := int(stopResp.ExitCode)
			state.ExitCode = &exitCode
		}
	}

	// 2. Cleanup containers and network
	if err := sm.cleanupSession(ctx, state); err != nil {
		return fmt.Errorf("failed to cleanup session: %w", err)
	}

	now := time.Now()
	state.StoppedAt = &now
	state.UpdateStatus(manman.SessionStatusStopped)

	// 3. Remove from state manager
	sm.stateManager.RemoveSession(sessionID)

	return nil
}

// KillSession forcefully kills a session
func (sm *SessionManager) KillSession(ctx context.Context, sessionID int64) error {
	return sm.StopSession(ctx, sessionID, true)
}

// createWrapperContainer creates the wrapper container
func (sm *SessionManager) createWrapperContainer(ctx context.Context, state *State, cmd *StartSessionCommand) (string, error) {
	dataPath := fmt.Sprintf("/data/session-%d", state.SessionID)

	config := docker.ContainerConfig{
		Image:     sm.wrapperImage,
		Name:      fmt.Sprintf("wrapper-%d", state.SessionID),
		NetworkID: state.NetworkID,
		Labels: map[string]string{
			"manman.session_id": fmt.Sprintf("%d", state.SessionID),
			"manman.wrapper":    "true",
		},
		Volumes: []string{
			fmt.Sprintf("%s:/data", dataPath),
		},
		Env: []string{
			fmt.Sprintf("SESSION_ID=%d", state.SessionID),
			fmt.Sprintf("NETWORK_NAME=%s", state.NetworkName),
		},
	}

	return sm.dockerClient.CreateContainer(ctx, config)
}

// cleanupSession cleans up containers and network for a session
func (sm *SessionManager) cleanupSession(ctx context.Context, state *State) error {
	var errs []error

	// Close gRPC connection
	if state.GRPCClient != nil {
		if err := state.GRPCClient.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close gRPC client: %w", err))
		}
	}

	// Stop and remove game container (wrapper handles this, but we'll try to clean up if needed)
	if state.GameContainerID != "" {
		// Wrapper should have stopped it, but we'll remove it if it still exists
		_ = sm.dockerClient.RemoveContainer(ctx, state.GameContainerID, true)
	}

	// Stop and remove wrapper container
	if state.WrapperContainerID != "" {
		if err := sm.dockerClient.StopContainer(ctx, state.WrapperContainerID, nil); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop wrapper container: %w", err))
		}
		if err := sm.dockerClient.RemoveContainer(ctx, state.WrapperContainerID, true); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove wrapper container: %w", err))
		}
	}

	// Remove network
	if state.NetworkID != "" {
		if err := sm.dockerClient.RemoveNetwork(ctx, state.NetworkID); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove network: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %v", errs)
	}

	return nil
}

// Helper functions for converting config maps to protobuf
func convertGameConfig(config map[string]interface{}) *pb.GameConfig {
	// Simplified conversion - in practice, you'd need proper type checking
	gc := &pb.GameConfig{}
	if id, ok := config["config_id"].(float64); ok {
		gc.ConfigId = int64(id)
	}
	if gameID, ok := config["game_id"].(float64); ok {
		gc.GameId = int64(gameID)
	}
	if name, ok := config["name"].(string); ok {
		gc.Name = name
	}
	if image, ok := config["image"].(string); ok {
		gc.Image = image
	}
	return gc
}

func convertServerGameConfig(config map[string]interface{}) *pb.ServerGameConfig {
	sgc := &pb.ServerGameConfig{}
	if sgcID, ok := config["sgc_id"].(float64); ok {
		sgc.SgcId = int64(sgcID)
	}
	if serverID, ok := config["server_id"].(float64); ok {
		sgc.ServerId = int64(serverID)
	}
	if gameConfigID, ok := config["game_config_id"].(float64); ok {
		sgc.GameConfigId = int64(gameConfigID)
	}
	return sgc
}

// StartSessionCommand represents a command to start a session
// This mirrors the rmq.StartSessionCommand but includes ServerID and ParametersJSON
type StartSessionCommand struct {
	SessionID        int64
	SGCID            int64
	ServerID         int64
	GameConfig       map[string]interface{}
	ServerGameConfig map[string]interface{}
	ParametersJSON   string
}
