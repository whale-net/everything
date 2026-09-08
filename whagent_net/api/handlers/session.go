// Package handlers implements whagent-net's SessionServiceServer
// (whagent_net/protos/session.proto, issue #2113). Scaffold phase: every
// RPC is wired up and reachable through the authenticated gRPC server
// (whagent_net/api/main.go, auth.go), but every method here returns
// codes.Unimplemented -- GetSession and ReadTranscript's real logic
// (FR2/FR3, reading through store) and the three write RPCs' Temporal
// wiring land in this issue's Implementation phase.
package handlers

import (
	"context"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SessionServer implements pb.SessionServiceServer over a
// *session.Store -- the same store package `worker` will import directly
// (ARCHITECTURE.md "Service boundary vs. package boundary": no RPC hop
// between api and worker).
type SessionServer struct {
	pb.UnimplementedSessionServiceServer

	store *session.Store
}

var _ pb.SessionServiceServer = (*SessionServer)(nil)

// NewSessionServer returns a SessionServer backed by store.
func NewSessionServer(store *session.Store) *SessionServer {
	return &SessionServer{store: store}
}

// StartSession is UNIMPLEMENTED until the SessionWorkflow exists (follow-up
// task to #2113 per whagent_net/ARCHITECTURE.md "Session workflow").
func (s *SessionServer) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "StartSession: session workflow not yet implemented")
}

// SendTurn is UNIMPLEMENTED until the SessionWorkflow exists.
func (s *SessionServer) SendTurn(ctx context.Context, req *pb.SendTurnRequest) (*pb.SendTurnResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SendTurn: session workflow not yet implemented")
}

// StopSession is UNIMPLEMENTED until the SessionWorkflow exists.
func (s *SessionServer) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "StopSession: session workflow not yet implemented")
}

// GetSession is this issue's Implementation-phase read path (FR3): map a
// `sessions` row (session.Store.Sessions().GetByID) to a Session proto,
// translating Status/CapKind/ErrorCategory to their proto enums and
// NOT_FOUND/PERMISSION_DENIED per the issue's Implementation section.
// Scaffold leaves it UNIMPLEMENTED.
func (s *SessionServer) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetSession: not yet implemented")
}

// ListSessions may be minimal in M1 per the issue body -- either list the
// caller's own sessions or stay UNIMPLEMENTED. Scaffold leaves it
// UNIMPLEMENTED; the Implementation phase decides which.
func (s *SessionServer) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListSessions: not yet implemented")
}

// ReadTranscript is this issue's Implementation-phase read path (FR2):
// page through session.Store.Transcript().Read in commit order, mapping
// each Event to a TranscriptEvent proto. Scaffold leaves it UNIMPLEMENTED.
func (s *SessionServer) ReadTranscript(ctx context.Context, req *pb.ReadTranscriptRequest) (*pb.ReadTranscriptResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ReadTranscript: not yet implemented")
}
