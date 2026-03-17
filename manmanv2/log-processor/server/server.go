package server

import (
	"fmt"
	"log"

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

	log.Printf("[log-processor] client subscribed to session %d logs", sessionID)

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
			log.Printf("[log-processor] failed to send backlog message for session %d: %v", sessionID, err)
			return err
		}
	}

	// Stream live logs to client
	logCh := subscription.Channel()
	for {
		select {
		case <-stream.Context().Done():
			log.Printf("[log-processor] client disconnected from session %d logs", sessionID)
			return stream.Context().Err()
		case msg, ok := <-logCh:
			if !ok {
				// Channel closed
				log.Printf("[log-processor] log channel closed for session %d", sessionID)
				return nil
			}

			if err := stream.Send(msg); err != nil {
				log.Printf("[log-processor] failed to send log message: %v", err)
				return err
			}
		}
	}
}
