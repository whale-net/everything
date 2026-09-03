package server

import (
	"fmt"
	"log/slog"

	"github.com/whale-net/everything/manmanv2/log-processor/consumer"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Server implements the LogProcessor gRPC service
type Server struct {
	manmanpb.UnimplementedLogProcessorServer
	consumerManager *consumer.Manager
}

// NewServer creates a new log processor gRPC server
func NewServer(consumerManager *consumer.Manager) *Server {
	return &Server{
		consumerManager: consumerManager,
	}
}

// StreamSessionLogs streams logs for a session in real-time
func (s *Server) StreamSessionLogs(req *manmanpb.StreamSessionLogsRequest, stream manmanpb.LogProcessor_StreamSessionLogsServer) error {
	sessionID := req.SessionId
	if sessionID <= 0 {
		return fmt.Errorf("invalid session_id: %d", sessionID)
	}

	slog.Info("client subscribed to session logs", "session_id", sessionID)

	// Subscribe to consumer manager
	subscription, err := s.consumerManager.Subscribe(stream.Context(), sessionID, req.AfterSequenceNumber)
	if err != nil {
		return fmt.Errorf("failed to subscribe to logs: %w", err)
	}
	defer subscription.Unsubscribe()

	// Send backlog first so the client sees historical messages before live ones.
	// The backlog was snapshot atomically at subscribe time, so no messages are
	// missed or duplicated when we transition to the live channel below.
	for _, msg := range subscription.Backlog() {
		if err := stream.Send(msg); err != nil {
			slog.Warn("failed to send backlog message for session", "session_id", sessionID, "error", err)
			return err
		}
	}

	// Stream live logs to client
	logCh := subscription.Channel()
	for {
		select {
		case <-stream.Context().Done():
			slog.Info("client disconnected from session logs", "session_id", sessionID)
			return stream.Context().Err()
		case msg, ok := <-logCh:
			if !ok {
				// Channel closed
				slog.Info("log channel closed for session", "session_id", sessionID)
				return nil
			}

			if err := stream.Send(msg); err != nil {
				slog.Warn("failed to send log message", "session_id", sessionID, "error", err)
				return err
			}
		}
	}
}
