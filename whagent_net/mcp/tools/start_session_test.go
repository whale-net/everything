package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// TestStartSession_NoFirstTurn_ForwardsArgsAndMakesExactlyOneRPC proves
// start_session with no first_turn forwards agent_id/model_override
// verbatim to StartSessionRequest, calls StartSession exactly once, and
// never calls SendTurn (issue #2120's Implementation section: "only when
// in.FirstTurn is non-empty").
func TestStartSession_NoFirstTurn_ForwardsArgsAndMakesExactlyOneRPC(t *testing.T) {
	fc := &fakeSessionServiceClient{
		startSessionFunc: func(ctx context.Context, in *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
			assert.Equal(t, "agent-1", in.AgentId)
			require.NotNil(t, in.ModelOverride)
			assert.Equal(t, "gpt-5", *in.ModelOverride)
			return &pb.StartSessionResponse{Session: &pb.Session{
				SessionId: "sess-1",
				State:     pb.SessionState_SESSION_STATE_RUNNING,
			}}, nil
		},
	}
	tool := &startSessionTool{client: fc}

	_, out, err := tool.call(context.Background(), nil, StartSessionInput{
		AgentID:       "agent-1",
		ModelOverride: "gpt-5",
	})

	require.NoError(t, err)
	assert.Equal(t, "sess-1", out.SessionID)
	assert.Equal(t, "running", out.State)
	require.Len(t, fc.calls, 1, "no first_turn means exactly one RPC (StartSession), never SendTurn")
	assert.Equal(t, "StartSession", fc.calls[0].rpc)
}

// TestStartSession_NoModelOverride_LeavesFieldUnset proves an empty
// ModelOverride is never sent as an empty-string override -- the field
// must stay proto3-optional-unset, matching pb.StartSessionRequest's
// "overrides the default when set" semantics.
func TestStartSession_NoModelOverride_LeavesFieldUnset(t *testing.T) {
	fc := &fakeSessionServiceClient{
		startSessionFunc: func(ctx context.Context, in *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
			assert.Nil(t, in.ModelOverride, "model_override must stay unset, not an empty string, when the operator didn't ask for an override")
			return &pb.StartSessionResponse{Session: &pb.Session{SessionId: "sess-1", State: pb.SessionState_SESSION_STATE_RUNNING}}, nil
		},
	}
	tool := &startSessionTool{client: fc}

	_, _, err := tool.call(context.Background(), nil, StartSessionInput{AgentID: "agent-1"})
	require.NoError(t, err)
}

// TestStartSession_WithFirstTurn_ComposesStartSessionThenSendTurn proves
// the two-RPC composition (issue #2120's Implementation section): when
// FirstTurn is set, StartSession runs first, then SendTurn against the
// just-started session_id, in that order, and the returned state reflects
// SendTurn's response (the state immediately after the turn was accepted),
// not StartSession's.
func TestStartSession_WithFirstTurn_ComposesStartSessionThenSendTurn(t *testing.T) {
	fc := &fakeSessionServiceClient{
		startSessionFunc: func(ctx context.Context, in *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
			return &pb.StartSessionResponse{Session: &pb.Session{
				SessionId: "sess-1",
				State:     pb.SessionState_SESSION_STATE_RUNNING,
			}}, nil
		},
		sendTurnFunc: func(ctx context.Context, in *pb.SendTurnRequest) (*pb.SendTurnResponse, error) {
			assert.Equal(t, "sess-1", in.SessionId, "SendTurn must target the session_id StartSession just returned")
			assert.Equal(t, "hello agent", in.Input)
			return &pb.SendTurnResponse{Session: &pb.Session{
				SessionId: "sess-1",
				State:     pb.SessionState_SESSION_STATE_AWAITING_INPUT,
			}}, nil
		},
	}
	tool := &startSessionTool{client: fc}

	_, out, err := tool.call(context.Background(), nil, StartSessionInput{
		AgentID:   "agent-1",
		FirstTurn: "hello agent",
	})

	require.NoError(t, err)
	assert.Equal(t, "sess-1", out.SessionID)
	assert.Equal(t, "awaiting_input", out.State, "the returned state must be SendTurn's, not StartSession's")

	require.Len(t, fc.calls, 2, "first_turn set means exactly two RPCs")
	assert.Equal(t, "StartSession", fc.calls[0].rpc, "StartSession must run first")
	assert.Equal(t, "SendTurn", fc.calls[1].rpc, "SendTurn must run second, against the session StartSession just created")
}

// TestStartSession_StartSessionFails_NeverCallsSendTurn proves a
// StartSession failure (e.g. FAILED_PRECONDITION for FR5's unserved
// model) short-circuits before any SendTurn call, and surfaces as a
// legible tool error.
func TestStartSession_StartSessionFails_NeverCallsSendTurn(t *testing.T) {
	fc := &fakeSessionServiceClient{
		startSessionFunc: func(ctx context.Context, in *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "model not served by any provider")
		},
	}
	tool := &startSessionTool{client: fc}

	_, out, err := tool.call(context.Background(), nil, StartSessionInput{
		AgentID:       "agent-1",
		ModelOverride: "unserved-model",
		FirstTurn:     "hello agent",
	})

	require.Error(t, err)
	assert.Equal(t, StartSessionOutput{}, out)
	assert.Contains(t, err.Error(), "FailedPrecondition")
	assert.Contains(t, err.Error(), "model not served by any provider")
	require.Len(t, fc.calls, 1, "SendTurn must never be called when StartSession itself failed")
}

// TestStartSession_SendTurnFailsAfterStartSessionSucceeded_ReportsPartialFailure
// proves the "session was created, but its first turn was not queued"
// contract (issue #2120's Implementation section): the error must name the
// session_id that WAS created and say explicitly the failure was in
// queuing the first turn, not that start_session failed outright -- an
// operator reading this should retry with send_turn against session_id,
// not call start_session again.
func TestStartSession_SendTurnFailsAfterStartSessionSucceeded_ReportsPartialFailure(t *testing.T) {
	fc := &fakeSessionServiceClient{
		startSessionFunc: func(ctx context.Context, in *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
			return &pb.StartSessionResponse{Session: &pb.Session{
				SessionId: "sess-99",
				State:     pb.SessionState_SESSION_STATE_RUNNING,
			}}, nil
		},
		sendTurnFunc: func(ctx context.Context, in *pb.SendTurnRequest) (*pb.SendTurnResponse, error) {
			return nil, status.Error(codes.Internal, "queue unavailable")
		},
	}
	tool := &startSessionTool{client: fc}

	_, out, err := tool.call(context.Background(), nil, StartSessionInput{
		AgentID:   "agent-1",
		FirstTurn: "hello agent",
	})

	require.Error(t, err)
	assert.Equal(t, StartSessionOutput{}, out)
	assert.Contains(t, err.Error(), "sess-99", "the error must name the session that WAS created")
	assert.Contains(t, err.Error(), "was started but its first turn was not queued")
	assert.Contains(t, err.Error(), "queue unavailable", "the underlying SendTurn failure's own message must still be visible")
	require.Len(t, fc.calls, 2, "both RPCs must have been attempted -- this is a partial failure, not a short circuit")
}
