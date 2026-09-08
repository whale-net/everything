// SendTurn (issue #2117's write path, FR1). Scaffold phase stub: real
// implementation -- session ownership/existence checks (PERMISSION_DENIED/
// NOT_FOUND), FAILED_PRECONDITION on a terminal session, and signalling
// the running SessionWorkflow via s.temporalClient.SignalWorkflow on
// SignalSendTurn -- lands in #2117's Implementation phase. Asynchronous by
// contract: the call returns once the turn is accepted and queued, not
// once it completes -- no "wait for completion" mode, no completion field
// on the response.
package handlers

import (
	"context"

	pb "github.com/whale-net/everything/whagent_net/protos"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SendTurn is UNIMPLEMENTED until #2117's Implementation phase.
func (s *SessionServer) SendTurn(ctx context.Context, req *pb.SendTurnRequest) (*pb.SendTurnResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SendTurn: not yet implemented")
}
