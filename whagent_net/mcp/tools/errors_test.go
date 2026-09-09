package tools

// Coverage for gRPC-status -> MCP-tool-error translation (errors.go,
// issue #2120's Implementation section, "Error mapping"), driven through
// every tool that reaches an RPC directly (start_session's own error
// mapping for a StartSession-level failure is covered in
// start_session_test.go alongside its two-RPC composition). Each case
// proves the status CODE name and api's own MESSAGE both survive into the
// tool error text -- legible in Claude Code without reading api's logs.
import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

func TestToolError_SurfacesGRPCCodeAndMessage(t *testing.T) {
	cases := []struct {
		code codes.Code
		msg  string
	}{
		{codes.PermissionDenied, "caller lacks the required role"},
		{codes.FailedPrecondition, "session has already ended"},
		{codes.NotFound, "no session with that id"},
		{codes.InvalidArgument, "input must not be empty"},
	}

	for _, tc := range cases {
		t.Run(tc.code.String(), func(t *testing.T) {
			grpcErr := status.Error(tc.code, tc.msg)

			t.Run("send_turn", func(t *testing.T) {
				fc := &fakeSessionServiceClient{
					sendTurnFunc: func(context.Context, *pb.SendTurnRequest) (*pb.SendTurnResponse, error) {
						return nil, grpcErr
					},
				}
				tool := &sendTurnTool{client: fc}
				_, out, err := tool.call(context.Background(), nil, SendTurnInput{SessionID: "s", Input: "x"})
				require.Error(t, err)
				assert.Equal(t, SendTurnOutput{}, out)
				assert.Contains(t, err.Error(), tc.code.String())
				assert.Contains(t, err.Error(), tc.msg)
			})

			t.Run("stop_session", func(t *testing.T) {
				fc := &fakeSessionServiceClient{
					stopSessionFunc: func(context.Context, *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
						return nil, grpcErr
					},
				}
				tool := &stopSessionTool{client: fc}
				_, out, err := tool.call(context.Background(), nil, StopSessionInput{SessionID: "s"})
				require.Error(t, err)
				assert.Equal(t, StopSessionOutput{}, out)
				assert.Contains(t, err.Error(), tc.code.String())
				assert.Contains(t, err.Error(), tc.msg)
			})

			t.Run("get_session", func(t *testing.T) {
				fc := &fakeSessionServiceClient{
					getSessionFunc: func(context.Context, *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
						return nil, grpcErr
					},
				}
				tool := &getSessionTool{client: fc}
				_, out, err := tool.call(context.Background(), nil, GetSessionInput{SessionID: "s"})
				require.Error(t, err)
				assert.Equal(t, GetSessionOutput{}, out)
				assert.Contains(t, err.Error(), tc.code.String())
				assert.Contains(t, err.Error(), tc.msg)
			})

			t.Run("read_transcript", func(t *testing.T) {
				fc := &fakeSessionServiceClient{
					readTranscriptFunc: func(context.Context, *pb.ReadTranscriptRequest) (*pb.ReadTranscriptResponse, error) {
						return nil, grpcErr
					},
				}
				tool := &readTranscriptTool{client: fc}
				_, out, err := tool.call(context.Background(), nil, ReadTranscriptInput{SessionID: "s"})
				require.Error(t, err)
				assert.Equal(t, ReadTranscriptOutput{}, out)
				assert.Contains(t, err.Error(), tc.code.String())
				assert.Contains(t, err.Error(), tc.msg)
			})

			t.Run("start_session", func(t *testing.T) {
				fc := &fakeSessionServiceClient{
					startSessionFunc: func(context.Context, *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
						return nil, grpcErr
					},
				}
				tool := &startSessionTool{client: fc}
				_, out, err := tool.call(context.Background(), nil, StartSessionInput{AgentID: "a"})
				require.Error(t, err)
				assert.Equal(t, StartSessionOutput{}, out)
				assert.Contains(t, err.Error(), tc.code.String())
				assert.Contains(t, err.Error(), tc.msg)
			})
		})
	}
}

// TestToolError_NonStatusErrorFallsBackToPlainMessage proves a
// transport-level failure that never reached api (not a gRPC status error
// at all) still surfaces legibly, prefixed with the RPC name.
func TestToolError_NonStatusErrorFallsBackToPlainMessage(t *testing.T) {
	err := toolError("GetSession", assertErr("connection refused"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GetSession")
	assert.Contains(t, err.Error(), "connection refused")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
