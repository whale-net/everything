package tools

// fakeSessionServiceClient is a hand-rolled pb.SessionServiceClient double
// shared by every test in this package: each RPC's behavior is supplied by
// a caller-set func field (nil means "must not be called" -- calling it
// panics, so a wrongly-invoked RPC fails the test loudly instead of
// returning a misleading zero value), and every call is recorded in
// order in calls so a test can assert exactly which request reached which
// RPC, how many times, and in what order -- e.g. start_session's two-RPC
// composition (issue #2120's Implementation section). This package's tools
// are pure pass-throughs to pb.SessionServiceClient (no business logic, no
// store access), so an interface-level fake proves that contract directly
// without standing up a gRPC server at all.
import (
	"context"

	"google.golang.org/grpc"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

type fakeCall struct {
	rpc string
	req any
}

type fakeSessionServiceClient struct {
	startSessionFunc   func(ctx context.Context, in *pb.StartSessionRequest) (*pb.StartSessionResponse, error)
	sendTurnFunc       func(ctx context.Context, in *pb.SendTurnRequest) (*pb.SendTurnResponse, error)
	stopSessionFunc    func(ctx context.Context, in *pb.StopSessionRequest) (*pb.StopSessionResponse, error)
	getSessionFunc     func(ctx context.Context, in *pb.GetSessionRequest) (*pb.GetSessionResponse, error)
	readTranscriptFunc func(ctx context.Context, in *pb.ReadTranscriptRequest) (*pb.ReadTranscriptResponse, error)

	calls []fakeCall
}

func (f *fakeSessionServiceClient) StartSession(ctx context.Context, in *pb.StartSessionRequest, _ ...grpc.CallOption) (*pb.StartSessionResponse, error) {
	f.calls = append(f.calls, fakeCall{"StartSession", in})
	if f.startSessionFunc == nil {
		panic("fakeSessionServiceClient: StartSession called but no startSessionFunc set")
	}
	return f.startSessionFunc(ctx, in)
}

func (f *fakeSessionServiceClient) SendTurn(ctx context.Context, in *pb.SendTurnRequest, _ ...grpc.CallOption) (*pb.SendTurnResponse, error) {
	f.calls = append(f.calls, fakeCall{"SendTurn", in})
	if f.sendTurnFunc == nil {
		panic("fakeSessionServiceClient: SendTurn called but no sendTurnFunc set")
	}
	return f.sendTurnFunc(ctx, in)
}

func (f *fakeSessionServiceClient) StopSession(ctx context.Context, in *pb.StopSessionRequest, _ ...grpc.CallOption) (*pb.StopSessionResponse, error) {
	f.calls = append(f.calls, fakeCall{"StopSession", in})
	if f.stopSessionFunc == nil {
		panic("fakeSessionServiceClient: StopSession called but no stopSessionFunc set")
	}
	return f.stopSessionFunc(ctx, in)
}

func (f *fakeSessionServiceClient) GetSession(ctx context.Context, in *pb.GetSessionRequest, _ ...grpc.CallOption) (*pb.GetSessionResponse, error) {
	f.calls = append(f.calls, fakeCall{"GetSession", in})
	if f.getSessionFunc == nil {
		panic("fakeSessionServiceClient: GetSession called but no getSessionFunc set")
	}
	return f.getSessionFunc(ctx, in)
}

// ListSessions has no matching tool (issue #2120's Implementation section:
// no list_agents/list_sessions tool in M1) -- any test that reaches this is
// exercising a code path this package must never have.
func (f *fakeSessionServiceClient) ListSessions(context.Context, *pb.ListSessionsRequest, ...grpc.CallOption) (*pb.ListSessionsResponse, error) {
	panic("fakeSessionServiceClient: ListSessions called -- no whagent-net mcp tool forwards to this RPC")
}

func (f *fakeSessionServiceClient) ReadTranscript(ctx context.Context, in *pb.ReadTranscriptRequest, _ ...grpc.CallOption) (*pb.ReadTranscriptResponse, error) {
	f.calls = append(f.calls, fakeCall{"ReadTranscript", in})
	if f.readTranscriptFunc == nil {
		panic("fakeSessionServiceClient: ReadTranscript called but no readTranscriptFunc set")
	}
	return f.readTranscriptFunc(ctx, in)
}

// ListAgentDefinitionScopes has no matching tool -- like ListSessions
// above, any test that reaches this is exercising a code path this
// package must never have.
func (f *fakeSessionServiceClient) ListAgentDefinitionScopes(context.Context, *pb.ListAgentDefinitionScopesRequest, ...grpc.CallOption) (*pb.ListAgentDefinitionScopesResponse, error) {
	panic("fakeSessionServiceClient: ListAgentDefinitionScopes called -- no whagent-net mcp tool forwards to this RPC")
}

// GetSessionUsage has no matching tool yet (issue #2238 is the RPC's
// read-path task; a UI/tool consumer is separate scope) -- any test that
// reaches this is exercising a code path this package must never have.
func (f *fakeSessionServiceClient) GetSessionUsage(context.Context, *pb.GetSessionUsageRequest, ...grpc.CallOption) (*pb.GetSessionUsageResponse, error) {
	panic("fakeSessionServiceClient: GetSessionUsage called -- no whagent-net mcp tool forwards to this RPC")
}

// StreamEvents (issue #2239) has no matching tool -- like ListSessions
// above, any test that reaches this is exercising a code path this
// package must never have.
func (f *fakeSessionServiceClient) StreamEvents(context.Context, *pb.StreamEventsRequest, ...grpc.CallOption) (pb.SessionService_StreamEventsClient, error) {
	panic("fakeSessionServiceClient: StreamEvents called -- no whagent-net mcp tool forwards to this RPC")
}
