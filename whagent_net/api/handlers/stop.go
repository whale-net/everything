// StopSession (issue #2117's write path, FR1). Scaffold phase stub: real
// implementation -- session ownership/existence checks (PERMISSION_DENIED/
// NOT_FOUND), idempotent success on an already-terminal session (never
// overwriting the terminal reason), and signalling the running
// SessionWorkflow via s.temporalClient.SignalWorkflow on SignalStop to
// drive the immediate, best-effort interruption #2114's workflow
// implements -- lands in #2117's Implementation phase.
package handlers

import (
	"context"

	pb "github.com/whale-net/everything/whagent_net/protos"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StopSession is UNIMPLEMENTED until #2117's Implementation phase.
func (s *SessionServer) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "StopSession: not yet implemented")
}
