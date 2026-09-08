// StartSession (issue #2117's write path, FR1/FR5/FR9/NFR3). Scaffold
// phase stub: real implementation -- caller authentication, the FR9 role
// check, the FR5 model-override validation against the provider catalogue,
// the `sessions`/`session_agent` row writes, and starting the
// SessionWorkflow (workflow ID == session ID, LB2) via s.temporalClient on
// s.taskQueue -- lands in #2117's Implementation phase. Every failure must
// stay fail-closed with no session row, no session_agent row, and no
// Temporal workflow created; see the issue body's ordered StartSession
// steps.
package handlers

import (
	"context"

	pb "github.com/whale-net/everything/whagent_net/protos"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StartSession is UNIMPLEMENTED until #2117's Implementation phase.
func (s *SessionServer) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "StartSession: not yet implemented")
}
