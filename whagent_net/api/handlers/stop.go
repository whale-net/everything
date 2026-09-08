// StopSession (issue #2117's write path, FR1). Requests immediate,
// best-effort interruption of whatever the session is doing right now: an
// in-flight tool call or model call is cancelled rather than allowed to run
// to completion (worker/workflow.go's runTurn implements the cancellation
// itself). This is an emergency brake, not a graceful drain.
package handlers

import (
	"context"

	"github.com/google/uuid"

	pb "github.com/whale-net/everything/whagent_net/protos"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StopSession signals the session's running SessionWorkflow to stop.
// NOT_FOUND for an unknown session id; PERMISSION_DENIED for a session
// belonging to a different subject than the caller. Stopping an
// already-terminal session is idempotent: it succeeds without re-signalling
// (there is no running workflow left to signal -- SessionWorkflow already
// exited when it wrote a terminal status) and never overwrites the
// session's existing terminal reason.
func (s *SessionServer) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	id, err := uuid.Parse(req.GetSessionId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid session_id: %v", err)
	}

	sess, err := s.store.Sessions().GetByID(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get session: %v", err)
	}
	if sess == nil {
		return nil, status.Error(codes.NotFound, "session not found")
	}

	caller, err := s.callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	if !subjectsEqual(sess.Subject, caller) {
		return nil, status.Error(codes.PermissionDenied, "session belongs to a different subject")
	}

	if sess.Status.IsTerminal() {
		// Idempotent success: the workflow already exited and wrote
		// whichever terminal status/reason got there first -- nothing left
		// to signal, and the existing terminal reason must not be touched.
		return &pb.StopSessionResponse{Session: sessionToProto(sess)}, nil
	}

	if err := s.temporalClient.SignalWorkflow(ctx, id.String(), "", signalStop, nil); err != nil {
		return nil, status.Errorf(codes.Internal, "signal session workflow: %v", err)
	}

	return &pb.StopSessionResponse{Session: sessionToProto(sess)}, nil
}
