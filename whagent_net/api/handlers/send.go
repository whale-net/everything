// SendTurn (issue #2117's write path, FR1). Asynchronous by contract: the
// call returns once the turn is accepted and queued onto the workflow, not
// once it completes -- no "wait for completion" mode, no completion field
// on the response. The operator learns of progress via ReadTranscript (FR2)
// and GetSession (FR3).
package handlers

import (
	"context"

	"github.com/google/uuid"

	pb "github.com/whale-net/everything/whagent_net/protos"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SendTurn signals the session's running SessionWorkflow with the
// operator's turn input. NOT_FOUND for an unknown session id;
// PERMISSION_DENIED for a session belonging to a different subject than the
// caller; FAILED_PRECONDITION for a session that has already reached a
// terminal status (done/stopped/failed/capped) -- there is no running
// workflow left to signal.
func (s *SessionServer) SendTurn(ctx context.Context, req *pb.SendTurnRequest) (*pb.SendTurnResponse, error) {
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
		return nil, status.Errorf(codes.FailedPrecondition, "session %s is already %s", id, sess.Status)
	}

	// Fire-and-forget: SignalWorkflow queues the turn onto the running
	// workflow and returns as soon as Temporal has accepted it, well before
	// the workflow actually processes it -- exactly the asynchronous
	// contract this RPC promises.
	if err := s.temporalClient.SignalWorkflow(ctx, id.String(), "", signalSendTurn, sendTurnSignal{Input: req.GetInput()}); err != nil {
		return nil, status.Errorf(codes.Internal, "signal session workflow: %v", err)
	}

	return &pb.SendTurnResponse{Session: sessionToProto(sess)}, nil
}

// sendTurnSignal mirrors worker.SendTurnSignal (whagent_net/worker/
// workflow.go) exactly -- duplicated here for the same `package main`
// reason as sessionWorkflowInput (start.go). Temporal's default JSON data
// converter serializes by field name, so the two types must keep identical
// exported field names, not identical Go types.
type sendTurnSignal struct {
	Input string
}
