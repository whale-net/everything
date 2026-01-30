package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	pb "github.com/whale-net/everything/manman/protos"
)

// server implements the WrapperControl service
type server struct {
	pb.UnimplementedWrapperControlServer
	stateManager  *StateManager
	dockerClient  *docker.Client
	sessionID     string // Session ID from environment
	networkName   string // Network name from environment
}

// newServer creates a new wrapper control server
func newServer(dockerClient *docker.Client, sessionID, networkName string, previousState *SessionState) *server {
	stateManager := NewStateManager()

	// Restore previous state if available
	if previousState != nil {
		stateManager.SetSession(previousState)
		log.Printf("Restored session %d from previous state", previousState.SessionID)
	}

	return &server{
		stateManager: stateManager,
		dockerClient: dockerClient,
		sessionID:    sessionID,
		networkName:  networkName,
	}
}

// Start starts a game server container
func (s *server) Start(ctx context.Context, req *pb.StartRequest) (*pb.StartResponse, error) {
	log.Printf("Start request received: session_id=%d, sgc_id=%d", req.SessionId, req.SgcId)

	// Validate request
	if req.GameConfig == nil {
		return &pb.StartResponse{
			Success:      false,
			ErrorMessage: "game_config is required",
		}, nil
	}

	// Create session state
	session := &SessionState{
		SessionID: req.SessionId,
		SGCID:     req.SgcId,
		Status:    "pending",
	}
	s.stateManager.SetSession(session)

	// Update status to starting
	session.UpdateStatus("starting")
	log.Printf("Session %d: status updated to starting", req.SessionId)

	// Build container configuration
	containerName := fmt.Sprintf("game-%d", req.SessionId)
	dataPath := fmt.Sprintf("/data/game")

	// Build environment variables
	env := []string{}
	if req.GameConfig.EnvTemplate != nil {
		for key, value := range req.GameConfig.EnvTemplate {
			env = append(env, fmt.Sprintf("%s=%s", key, value))
		}
	}

	// Build port bindings
	ports := make(map[string]string)
	if req.ServerGameConfig != nil && req.ServerGameConfig.PortBindings != nil {
		for _, pb := range req.ServerGameConfig.PortBindings {
			containerPort := fmt.Sprintf("%d", pb.ContainerPort)
			hostPort := fmt.Sprintf("%d", pb.HostPort)
			ports[containerPort] = hostPort
		}
	}

	// Parse command arguments from args_template
	var command []string
	if req.GameConfig.ArgsTemplate != "" {
		// Simple split - in production, you'd want proper template parsing
		command = []string{"/bin/sh", "-c", req.GameConfig.ArgsTemplate}
	}

	// Create container config
	containerConfig := docker.ContainerConfig{
		Image:      req.GameConfig.Image,
		Name:       containerName,
		Command:    command,
		Env:        env,
		NetworkID:  s.networkName,
		Volumes:    []string{fmt.Sprintf("/data:%s", dataPath)},
		Ports:      ports,
		AutoRemove: false, // Keep container for log retrieval
		Labels: map[string]string{
			"manman.session_id": fmt.Sprintf("%d", req.SessionId),
			"manman.game":       "true",
		},
	}

	// Create the game container
	log.Printf("Session %d: Creating game container with image %s", req.SessionId, req.GameConfig.Image)
	containerID, err := s.dockerClient.CreateContainer(ctx, containerConfig)
	if err != nil {
		session.UpdateStatus("crashed")
		return &pb.StartResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to create container: %v", err),
		}, nil
	}
	session.GameContainerID = containerID
	log.Printf("Session %d: Created container %s", req.SessionId, containerID)

	// Persist state with container ID
	if err := session.SaveState(); err != nil {
		log.Printf("Session %d: Warning - failed to save state: %v", req.SessionId, err)
	}

	// Start the game container
	log.Printf("Session %d: Starting container", req.SessionId)
	if err := s.dockerClient.StartContainer(ctx, containerID); err != nil {
		session.UpdateStatus("crashed")
		// Try to clean up the container
		_ = s.dockerClient.RemoveContainer(ctx, containerID, true)
		return &pb.StartResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to start container: %v", err),
		}, nil
	}

	// Update status to running
	session.UpdateStatus("running")
	log.Printf("Session %d: Container started successfully", req.SessionId)

	// Persist final state
	if err := session.SaveState(); err != nil {
		log.Printf("Session %d: Warning - failed to save state: %v", req.SessionId, err)
	}

	return &pb.StartResponse{
		Success:      true,
		ErrorMessage: "",
		ContainerId:  containerID,
	}, nil
}

// Stop stops a game server container gracefully
func (s *server) Stop(ctx context.Context, req *pb.StopRequest) (*pb.StopResponse, error) {
	log.Printf("Stop request received: session_id=%d, force=%v", req.SessionId, req.Force)

	session, exists := s.stateManager.GetSession(req.SessionId)
	if !exists {
		return &pb.StopResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("session %d not found", req.SessionId),
			ExitCode:     -1,
		}, nil
	}

	// Check if we have a container to stop
	if session.GameContainerID == "" {
		return &pb.StopResponse{
			Success:      false,
			ErrorMessage: "no container associated with this session",
			ExitCode:     -1,
		}, nil
	}

	// Update status
	session.UpdateStatus("stopping")
	log.Printf("Session %d: status updated to stopping", req.SessionId)

	// Stop the container
	var stopTimeout *time.Duration
	if !req.Force {
		// Graceful shutdown with 30 second timeout
		timeout := 30 * time.Second
		stopTimeout = &timeout
	}

	log.Printf("Session %d: Stopping container %s (force=%v)", req.SessionId, session.GameContainerID, req.Force)
	if err := s.dockerClient.StopContainer(ctx, session.GameContainerID, stopTimeout); err != nil {
		log.Printf("Session %d: Error stopping container: %v", req.SessionId, err)
		// Continue to try to get exit code even if stop failed
	}

	// Get the container's exit code
	status, err := s.dockerClient.GetContainerStatus(ctx, session.GameContainerID)
	exitCode := 0
	if err != nil {
		log.Printf("Session %d: Failed to get container status: %v", req.SessionId, err)
		exitCode = -1
	} else {
		exitCode = status.ExitCode
		log.Printf("Session %d: Container exited with code %d", req.SessionId, exitCode)
	}

	// Remove the container
	log.Printf("Session %d: Removing container", req.SessionId)
	if err := s.dockerClient.RemoveContainer(ctx, session.GameContainerID, true); err != nil {
		log.Printf("Session %d: Warning - failed to remove container: %v", req.SessionId, err)
	}

	// Update session state
	session.UpdateStatus("stopped")
	session.ExitCode = exitCode

	// Persist final state
	if err := session.SaveState(); err != nil {
		log.Printf("Session %d: Warning - failed to save state: %v", req.SessionId, err)
	}

	return &pb.StopResponse{
		Success:      true,
		ErrorMessage: "",
		ExitCode:     int32(exitCode),
	}, nil
}

// SendInput sends input to the game server process (stdin)
// Note: This implementation requires containers to be created with stdin support
func (s *server) SendInput(ctx context.Context, req *pb.SendInputRequest) (*pb.SendInputResponse, error) {
	log.Printf("SendInput request received: session_id=%d, input_length=%d bytes", req.SessionId, len(req.Input))

	session, exists := s.stateManager.GetSession(req.SessionId)
	if !exists {
		return &pb.SendInputResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("session %d not found", req.SessionId),
		}, nil
	}

	// Check if we have a container
	if session.GameContainerID == "" {
		return &pb.SendInputResponse{
			Success:      false,
			ErrorMessage: "no container associated with this session",
		}, nil
	}

	// For Phase 3, we log the input that would be sent
	// In Phase 4+, we would attach to the container and write to stdin
	// This requires the container to be created with -i (interactive) flag
	log.Printf("Session %d: Input received (length=%d): %s", req.SessionId, len(req.Input), string(req.Input))

	// TODO: Implement actual stdin forwarding using Docker attach
	// This requires modifying the container creation to include:
	// - OpenStdin: true
	// - StdinOnce: false
	// - AttachStdin: true
	// Then use s.dockerClient.AttachToContainer() to write to stdin

	return &pb.SendInputResponse{
		Success:      true,
		ErrorMessage: "",
	}, nil
}

// GetStatus returns the current status of the wrapper and game server
func (s *server) GetStatus(ctx context.Context, req *pb.GetStatusRequest) (*pb.GetStatusResponse, error) {
	log.Printf("GetStatus request received: session_id=%d", req.SessionId)

	session, exists := s.stateManager.GetSession(req.SessionId)
	if !exists {
		// Return default state for unknown sessions
		return &pb.GetStatusResponse{
			Status:      "pending",
			ContainerId: "",
			ExitCode:    0,
			IsRunning:   false,
		}, nil
	}

	// If we have a container ID, query its actual status
	isRunning := false
	exitCode := session.ExitCode
	if session.GameContainerID != "" {
		containerStatus, err := s.dockerClient.GetContainerStatus(ctx, session.GameContainerID)
		if err != nil {
			log.Printf("Session %d: Failed to get container status: %v", req.SessionId, err)
			// Fall back to session state
		} else {
			isRunning = containerStatus.Running
			if !isRunning {
				exitCode = containerStatus.ExitCode
				// Update session exit code if container has stopped
				if session.ExitCode == 0 && containerStatus.ExitCode != 0 {
					session.ExitCode = containerStatus.ExitCode
				}
				// If container is not running and status is still "running", update to crashed
				if session.Status == "running" {
					session.UpdateStatus("crashed")
				}
			}
		}
	}

	return &pb.GetStatusResponse{
		Status:      session.Status,
		ContainerId: session.GameContainerID,
		ExitCode:    int32(exitCode),
		IsRunning:   isRunning,
	}, nil
}

// StreamOutput streams stdout/stderr from the game server
func (s *server) StreamOutput(req *pb.StreamOutputRequest, stream pb.WrapperControl_StreamOutputServer) error {
	log.Printf("StreamOutput request received: session_id=%d, stdout=%v, stderr=%v",
		req.SessionId, req.Stdout, req.Stderr)

	session, exists := s.stateManager.GetSession(req.SessionId)
	if !exists {
		// Send error message
		if err := stream.Send(&pb.StreamOutputResponse{
			Data:     []byte(fmt.Sprintf("session %d not found", req.SessionId)),
			IsStderr: true,
			Eof:      true,
		}); err != nil {
			return err
		}
		return nil
	}

	// Check if we have a container to stream from
	if session.GameContainerID == "" {
		if err := stream.Send(&pb.StreamOutputResponse{
			Data:     []byte("no container associated with this session"),
			IsStderr: true,
			Eof:      true,
		}); err != nil {
			return err
		}
		return nil
	}

	// Get container logs
	ctx := stream.Context()
	logs, err := s.dockerClient.GetContainerLogs(ctx, session.GameContainerID, true, "all")
	if err != nil {
		log.Printf("Session %d: Failed to get container logs: %v", req.SessionId, err)
		if err := stream.Send(&pb.StreamOutputResponse{
			Data:     []byte(fmt.Sprintf("failed to get logs: %v", err)),
			IsStderr: true,
			Eof:      true,
		}); err != nil {
			return err
		}
		return nil
	}
	defer logs.Close()

	// Stream logs to client
	// Docker logs format uses an 8-byte header: [STREAM_TYPE, 0, 0, 0, SIZE1, SIZE2, SIZE3, SIZE4]
	// STREAM_TYPE: 1=stdout, 2=stderr
	// SIZE: 4-byte big-endian uint32
	for {
		select {
		case <-ctx.Done():
			log.Printf("Session %d: StreamOutput cancelled by client", req.SessionId)
			return ctx.Err()
		default:
			// Read header (8 bytes)
			header := make([]byte, 8)
			n, err := logs.Read(header)
			if err != nil {
				if err.Error() != "EOF" {
					log.Printf("Session %d: Error reading log header: %v", req.SessionId, err)
				}
				// Send EOF
				if err := stream.Send(&pb.StreamOutputResponse{
					Data:     []byte{},
					IsStderr: false,
					Eof:      true,
				}); err != nil {
					return err
				}
				log.Printf("Session %d: StreamOutput completed", req.SessionId)
				return nil
			}
			if n < 8 {
				continue
			}

			// Parse header
			streamType := header[0]
			size := uint32(header[4])<<24 | uint32(header[5])<<16 | uint32(header[6])<<8 | uint32(header[7])

			// Read data
			data := make([]byte, size)
			_, err = logs.Read(data)
			if err != nil {
				log.Printf("Session %d: Error reading log data: %v", req.SessionId, err)
				break
			}

			// Send to client based on stream type
			isStderr := streamType == 2
			if (req.Stdout && !isStderr) || (req.Stderr && isStderr) {
				if err := stream.Send(&pb.StreamOutputResponse{
					Data:     data,
					IsStderr: isStderr,
					Eof:      false,
				}); err != nil {
					log.Printf("Session %d: Error sending output: %v", req.SessionId, err)
					return err
				}
			}
		}
	}
}
