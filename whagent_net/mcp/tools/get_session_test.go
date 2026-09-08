package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// TestGetSession_RendersEveryTerminalReasonVariant is FR3's own contract,
// proved for every case the issue enumerates: capped + cap kind, failed +
// category + detail, plain done/stopped, and a still-running session with
// no terminal-reason fields set at all (proto3 `optional` presence
// preserved -- CapKind/ErrorCategory/ErrorDetail must stay the Go
// zero-value/empty string, never a placeholder, whenever the underlying
// pointer on *pb.Session is nil).
func TestGetSession_RendersEveryTerminalReasonVariant(t *testing.T) {
	capKind := pb.CapKind_CAP_KIND_TURNS
	errCategory := pb.ErrorCategory_ERROR_CATEGORY_RETRYABLE
	errDetail := "upstream model provider timed out"

	cases := []struct {
		name    string
		session *pb.Session
		want    GetSessionOutput
	}{
		{
			name: "running: no terminal-reason fields set",
			session: &pb.Session{
				SessionId: "sess-1",
				State:     pb.SessionState_SESSION_STATE_RUNNING,
				AgentId:   "agent-1",
				Model:     "gpt-5",
			},
			want: GetSessionOutput{SessionID: "sess-1", State: "running", AgentID: "agent-1", Model: "gpt-5"},
		},
		{
			name: "done: no terminal-reason fields set",
			session: &pb.Session{
				SessionId: "sess-2",
				State:     pb.SessionState_SESSION_STATE_DONE,
				AgentId:   "agent-1",
				Model:     "gpt-5",
			},
			want: GetSessionOutput{SessionID: "sess-2", State: "done", AgentID: "agent-1", Model: "gpt-5"},
		},
		{
			name: "stopped: no terminal-reason fields set",
			session: &pb.Session{
				SessionId: "sess-3",
				State:     pb.SessionState_SESSION_STATE_STOPPED,
				AgentId:   "agent-1",
				Model:     "gpt-5",
			},
			want: GetSessionOutput{SessionID: "sess-3", State: "stopped", AgentID: "agent-1", Model: "gpt-5"},
		},
		{
			name: "capped: cap_kind set, error fields unset",
			session: &pb.Session{
				SessionId: "sess-4",
				State:     pb.SessionState_SESSION_STATE_CAPPED,
				AgentId:   "agent-1",
				Model:     "gpt-5",
				CapKind:   &capKind,
			},
			want: GetSessionOutput{SessionID: "sess-4", State: "capped", AgentID: "agent-1", Model: "gpt-5", CapKind: "turns"},
		},
		{
			name: "failed: error_category + error_detail set, cap_kind unset",
			session: &pb.Session{
				SessionId:     "sess-5",
				State:         pb.SessionState_SESSION_STATE_FAILED,
				AgentId:       "agent-1",
				Model:         "gpt-5",
				ErrorCategory: &errCategory,
				ErrorDetail:   &errDetail,
			},
			want: GetSessionOutput{
				SessionID: "sess-5", State: "failed", AgentID: "agent-1", Model: "gpt-5",
				ErrorCategory: "retryable", ErrorDetail: "upstream model provider timed out",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeSessionServiceClient{
				getSessionFunc: func(ctx context.Context, in *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
					assert.Equal(t, tc.session.SessionId, in.SessionId)
					return &pb.GetSessionResponse{Session: tc.session}, nil
				},
			}
			tool := &getSessionTool{client: fc}

			_, out, err := tool.call(context.Background(), nil, GetSessionInput{SessionID: tc.session.SessionId})

			require.NoError(t, err)
			assert.Equal(t, tc.want, out)
		})
	}
}
