package main

import (
	"context"
	"fmt"
	"log"

	pb "github.com/whale-net/everything/manman/protos"
)

// server implements the WrapperControl service
type server struct {
	pb.UnimplementedWrapperControlServer
	stateManager *StateManager
}

// newServer creates a new wrapper control server
func newServer() *server {
	return &server{
		stateManager: NewStateManager(),
	}
}

// Start starts a game server container
// NOTE: This is a placeholder implementation - returns success with dummy data
func (s *server) Start(ctx context.Context, req *pb.StartRequest) (*pb.StartResponse, error) {
	log.Printf("Start request received: session_id=%d, sgc_id=%d", req.SessionId, req.SgcId)

	// Create session state
	session := &SessionState{
		SessionID:       req.SessionId,
		SGCID:           req.SgcId,
		Status:          "pending",
		GameContainerID: fmt.Sprintf("placeholder-container-%d", req.SessionId),
	}
	s.stateManager.SetSession(session)

	// Update status to starting
	session.UpdateStatus("starting")
	log.Printf("Session %d: status updated to starting", req.SessionId)

	// TODO (future iteration): Start actual game container using Docker SDK
	// For now, just return success with placeholder data
	return &pb.StartResponse{
		Success:     true,
		ErrorMessage: "",
		ContainerId: session.GameContainerID,
	}, nil
}

// Stop stops a game server container gracefully
// NOTE: This is a placeholder implementation - returns success with exit code 0
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

	// Update status
	session.UpdateStatus("stopping")
	log.Printf("Session %d: status updated to stopping", req.SessionId)

	// TODO (future iteration): Stop actual game container
	// For now, just mark as stopped and return success
	session.UpdateStatus("stopped")
	session.ExitCode = 0

	return &pb.StopResponse{
		Success:      true,
		ErrorMessage: "",
		ExitCode:     int32(session.ExitCode),
	}, nil
}

// SendInput sends input to the game server process (stdin)
// NOTE: This is a placeholder implementation - just logs the input
func (s *server) SendInput(ctx context.Context, req *pb.SendInputRequest) (*pb.SendInputResponse, error) {
	log.Printf("SendInput request received: session_id=%d, input_length=%d bytes", req.SessionId, len(req.Input))

	_, exists := s.stateManager.GetSession(req.SessionId)
	if !exists {
		return &pb.SendInputResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("session %d not found", req.SessionId),
		}, nil
	}

	// TODO (future iteration): Send input to actual game container stdin
	log.Printf("Session %d: would send input: %s", req.SessionId, string(req.Input))

	return &pb.SendInputResponse{
		Success:      true,
		ErrorMessage: "",
	}, nil
}

// GetStatus returns the current status of the wrapper and game server
// NOTE: This is a placeholder implementation - returns current state
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

	// Determine if running based on status
	isRunning := session.Status == "running" || session.Status == "starting"

	return &pb.GetStatusResponse{
		Status:      session.Status,
		ContainerId: session.GameContainerID,
		ExitCode:    int32(session.ExitCode),
		IsRunning:   isRunning,
	}, nil
}

// StreamOutput streams stdout/stderr from the game server
// NOTE: This is a placeholder implementation - sends a single message and closes
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

	// TODO (future iteration): Stream actual stdout/stderr from game container
	// For now, send a placeholder message and close the stream
	message := fmt.Sprintf("Wrapper ready for session %d (container: %s)\n", 
		session.SessionID, session.GameContainerID)

	if req.Stdout {
		if err := stream.Send(&pb.StreamOutputResponse{
			Data:     []byte(message),
			IsStderr: false,
			Eof:      false,
		}); err != nil {
			return err
		}
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
