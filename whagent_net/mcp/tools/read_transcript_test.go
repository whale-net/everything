package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// TestReadTranscript_ForwardsPaginationAndPreservesOrder proves
// from_seq/limit are forwarded verbatim (FR2's pagination contract) and
// events come back rendered in exactly the order api returned them (this
// method must not re-sort).
func TestReadTranscript_ForwardsPaginationAndPreservesOrder(t *testing.T) {
	committedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fc := &fakeSessionServiceClient{
		readTranscriptFunc: func(ctx context.Context, in *pb.ReadTranscriptRequest) (*pb.ReadTranscriptResponse, error) {
			assert.Equal(t, "sess-1", in.SessionId)
			assert.Equal(t, int64(10), in.FromSeq)
			assert.Equal(t, int32(2), in.Limit)
			return &pb.ReadTranscriptResponse{
				Events: []*pb.TranscriptEvent{
					{EventId: "ev-10", Seq: 10, Turn: 3, Type: "user_turn", Payload: []byte(`{"text":"hi"}`), CommittedAt: timestamppb.New(committedAt)},
					{EventId: "ev-11", Seq: 11, Turn: 3, Type: "model_message", Payload: []byte(`{"text":"hello"}`), CommittedAt: timestamppb.New(committedAt.Add(time.Second))},
				},
				NextFromSeq: 12,
			}, nil
		},
	}
	tool := &readTranscriptTool{client: fc}

	_, out, err := tool.call(context.Background(), nil, ReadTranscriptInput{SessionID: "sess-1", FromSeq: 10, Limit: 2})

	require.NoError(t, err)
	require.Len(t, out.Events, 2)
	assert.Equal(t, "ev-10", out.Events[0].EventID)
	assert.Equal(t, int64(10), out.Events[0].Seq)
	assert.Equal(t, "ev-11", out.Events[1].EventID)
	assert.Equal(t, int64(11), out.Events[1].Seq)
	assert.Equal(t, []int64{10, 11}, []int64{out.Events[0].Seq, out.Events[1].Seq}, "events must be rendered in the exact order api returned them")
	assert.Equal(t, int64(12), out.NextFromSeq)
	assert.Equal(t, committedAt.Format(time.RFC3339), out.Events[0].CommittedAt)
	assert.JSONEq(t, `{"text":"hi"}`, out.Events[0].Payload)
	require.Len(t, fc.calls, 1)
	assert.Equal(t, "ReadTranscript", fc.calls[0].rpc)
}

// TestReadTranscript_ZeroFromSeqAndLimit_AreForwardedAsOmitted proves the
// omitempty defaults (0 == "from the beginning" / "server default") are
// forwarded as-is, not silently substituted.
func TestReadTranscript_ZeroFromSeqAndLimit_AreForwardedAsOmitted(t *testing.T) {
	fc := &fakeSessionServiceClient{
		readTranscriptFunc: func(ctx context.Context, in *pb.ReadTranscriptRequest) (*pb.ReadTranscriptResponse, error) {
			assert.Equal(t, int64(0), in.FromSeq)
			assert.Equal(t, int32(0), in.Limit)
			return &pb.ReadTranscriptResponse{}, nil
		},
	}
	tool := &readTranscriptTool{client: fc}

	_, out, err := tool.call(context.Background(), nil, ReadTranscriptInput{SessionID: "sess-1"})

	require.NoError(t, err)
	assert.Empty(t, out.Events)
	assert.Equal(t, int64(0), out.NextFromSeq)
}
