package server_test

// End-to-end coverage of this package's caller-identity design (issue
// #2120's Implementation section, "Identity pass-through"): a real
// streamable-HTTP mcp.Client (server_test.go's testServer.connect
// equivalent, mirrored here) against server.NewHTTPHandler, forwarding
// through to a real (bufconn-transported) fake SessionService gRPC server
// -- proving the caller's bearer token reaches api's metadata byte for
// byte, that a call with no credential never reaches the gRPC client at
// all, and that two different operators reach api as two different
// subjects rather than a shared service account (FR10).
import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/whagent_net/mcp/server"
	"github.com/whale-net/everything/whagent_net/mcp/tools"
	pb "github.com/whale-net/everything/whagent_net/protos"
)

const bufSize = 1024 * 1024

// fakeSessionServer is a real pb.SessionServiceServer -- reached over a
// real (in-memory, bufconn) gRPC connection -- that records the
// "authorization" metadata value it saw on each GetSession call, in
// order. It never touches Postgres or Temporal: it exists purely to prove
// what reaches it over the wire.
type fakeSessionServer struct {
	pb.UnimplementedSessionServiceServer

	mu           sync.Mutex
	authHeaders  []string // one entry per GetSession call, in order
	getSessionFn func(*pb.GetSessionRequest) (*pb.GetSessionResponse, error)
}

func (f *fakeSessionServer) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	var auth string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			auth = vals[0]
		}
	}
	f.mu.Lock()
	f.authHeaders = append(f.authHeaders, auth)
	f.mu.Unlock()

	if f.getSessionFn != nil {
		return f.getSessionFn(req)
	}
	return &pb.GetSessionResponse{Session: &pb.Session{
		SessionId: req.SessionId,
		State:     pb.SessionState_SESSION_STATE_RUNNING,
		AgentId:   "agent-1",
		Model:     "gpt-5",
	}}, nil
}

func (f *fakeSessionServer) recordedAuthHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.authHeaders))
	copy(out, f.authHeaders)
	return out
}

// newFakeBackend stands up fakeSessionServer over a bufconn listener and
// dials it exactly the way main.go dials api: grpcclient.NewClient plus
// grpcauth.NewUserTokenDialOption(AuthModeOIDC), which forwards whatever
// token grpcauth.WithUserToken placed on the call's context, byte for
// byte, as the outbound Authorization header.
func newFakeBackend(t *testing.T) (*fakeSessionServer, pb.SessionServiceClient) {
	t.Helper()
	ctx := context.Background()

	fake := &fakeSessionServer{}
	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	pb.RegisterSessionServiceServer(grpcServer, fake)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpcclient.NewClient(ctx, "passthrough:///bufnet",
		grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC),
		grpc.WithContextDialer(dialer),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return fake, pb.NewSessionServiceClient(conn.GetConnection())
}

// newTestMCPServer wires server.New() + tools.RegisterGetSession -- the
// exact production shape main.go builds -- over server.NewHTTPHandler,
// hosted by httptest.Server.
func newTestMCPServer(t *testing.T, client pb.SessionServiceClient) string {
	t.Helper()
	// nil Exchanger/credentials: this suite covers the manual-token path
	// only (issue #2120's original coverage) -- FR9's OAuth2 path (issue
	// #2249) has its own dedicated test file. A nil Exchanger is safe
	// here because AuthMiddleware only ever calls it on the OAuth2
	// branch, which nil credentials in NewHTTPHandler below never routes
	// a call onto.
	srv := server.New(nil)
	tools.RegisterGetSession(srv, client)

	ts := httptest.NewServer(server.NewHTTPHandler(srv, nil, server.ResourceMetadataConfig{}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// bearerRoundTripper injects "Authorization: Bearer <token>" on every
// request -- token == "" sends no header, simulating an unauthenticated
// caller (mirrors audience_score_system/mcp/server's own test helper).
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connectMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// TestAuthPassThrough_ForwardsCallersBearerTokenToAPI proves the exact
// token an operator authenticated to `mcp` with is what reaches api's
// gRPC metadata -- not re-minted, not a service-account token.
func TestAuthPassThrough_ForwardsCallersBearerTokenToAPI(t *testing.T) {
	fake, client := newFakeBackend(t)
	url := newTestMCPServer(t, client)

	const operatorToken = "operator-raw-token-abc123"
	cs, err := connectMCP(t, url, operatorToken)
	require.NoError(t, err)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_session",
		Arguments: tools.GetSessionInput{SessionID: "sess-1"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error")

	headers := fake.recordedAuthHeaders()
	require.Len(t, headers, 1)
	assert.Equal(t, "Bearer "+operatorToken, headers[0], "api must see the operator's own token, forwarded byte for byte")
}

// TestAuthPassThrough_RejectsCallWithNoCredentialBeforeReachingGRPCClient
// proves a call carrying no bearer token is rejected before any tool
// handler -- and therefore any gRPC call -- ever runs.
func TestAuthPassThrough_RejectsCallWithNoCredentialBeforeReachingGRPCClient(t *testing.T) {
	fake, client := newFakeBackend(t)
	url := newTestMCPServer(t, client)

	_, err := connectMCP(t, url, "")
	require.Error(t, err, "a call with no bearer token must be rejected before an MCP session is even established")

	assert.Empty(t, fake.recordedAuthHeaders(), "the gRPC backend must never be reached for an unauthenticated caller")
}

// TestAuthPassThrough_TwoOperatorsReachAPIAsTwoDifferentSubjects proves
// mcp never runs as a shared service account (FR10, issue #2120's
// Implementation section): two operators' calls, made concurrently-capable
// but asserted here sequentially for a deterministic recording order,
// reach api carrying their own, distinct tokens.
func TestAuthPassThrough_TwoOperatorsReachAPIAsTwoDifferentSubjects(t *testing.T) {
	fake, client := newFakeBackend(t)
	url := newTestMCPServer(t, client)

	const operatorA, operatorB = "operator-a-token", "operator-b-token"
	require.NotEqual(t, operatorA, operatorB)

	csA, err := connectMCP(t, url, operatorA)
	require.NoError(t, err)
	_, err = csA.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_session",
		Arguments: tools.GetSessionInput{SessionID: "sess-a"},
	})
	require.NoError(t, err)

	csB, err := connectMCP(t, url, operatorB)
	require.NoError(t, err)
	_, err = csB.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_session",
		Arguments: tools.GetSessionInput{SessionID: "sess-b"},
	})
	require.NoError(t, err)

	headers := fake.recordedAuthHeaders()
	require.Len(t, headers, 2)
	assert.Equal(t, "Bearer "+operatorA, headers[0])
	assert.Equal(t, "Bearer "+operatorB, headers[1])
	assert.NotEqual(t, headers[0], headers[1], "the two operators must never collapse onto the same forwarded identity")
}
