package main

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/whale-net/everything/libs/go/grpcauth"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
)

const bufSize = 1024 * 1024

// fakeSessionServer is a real whagentpb.SessionServiceServer -- reached
// over a real (in-memory, bufconn) gRPC connection -- that records the
// "authorization" metadata value it saw on the one GetSession call this
// test drives. Mirrors whagent_net/mcp/server/auth_pass_through_test.go's
// identically-named type, which proves the same forwarding contract one
// hop earlier in the chain (mcp -> api); this file proves it for ui -> api.
type fakeSessionServer struct {
	whagentpb.UnimplementedSessionServiceServer

	authHeader string
}

func (f *fakeSessionServer) GetSession(ctx context.Context, req *whagentpb.GetSessionRequest) (*whagentpb.GetSessionResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			f.authHeader = vals[0]
		}
	}
	return &whagentpb.GetSessionResponse{Session: &whagentpb.Session{
		SessionId: req.GetSessionId(),
	}}, nil
}

// newBufconnSessionClient dials fake exactly the way NewApp dials `api`:
// grpcclient.NewClient plus a caller-supplied dial option (here,
// grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC), matching
// production's cfg.GRPCAuthMode == "oidc" case) -- proving NewSessionClient
// itself does not swallow or override whatever dial options its caller
// passes in.
func newBufconnSessionClient(t *testing.T, fake *fakeSessionServer, extraOpts ...grpc.DialOption) *SessionClient {
	t.Helper()
	ctx := context.Background()

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	whagentpb.RegisterSessionServiceServer(grpcServer, fake)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	opts := append([]grpc.DialOption{grpc.WithContextDialer(dialer)}, extraOpts...)

	client, err := NewSessionClient(ctx, "passthrough:///bufnet", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestSessionClient_ForwardsOperatorsAccessTokenToAPI proves the exact
// dial-option wiring NewApp performs (grpcauth.NewUserTokenDialOption +
// grpcauth.WithUserToken on the call context) actually reaches `api`'s
// gRPC metadata as the operator's own bearer token -- issue #2236's
// Testing section: "the gRPC client attaches the caller's token (assert
// the dial option/metadata ...)".
//
// Red/green discipline: paired directly with
// TestSessionClient_NoDialOptionForwardsNoToken below, which drives the
// exact same call through NewSessionClient with no user-token dial option
// -- the shape NewApp's `NewSessionClient(ctx, cfg.APIAddr, userAuthOpt)`
// call in main.go would produce if the userAuthOpt argument were ever
// dropped. That test fails this test's assertion (a populated bearer
// header) were it applied there instead: run this file with
// TestSessionClient_ForwardsOperatorsAccessTokenToAPI's dial option
// dropped to reproduce the regression by hand.
func TestSessionClient_ForwardsOperatorsAccessTokenToAPI(t *testing.T) {
	fake := &fakeSessionServer{}
	client := newBufconnSessionClient(t, fake, grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC))

	const operatorToken = "operator-raw-token-xyz789"
	ctx := grpcauth.WithUserToken(context.Background(), operatorToken)

	_, err := client.Client().GetSession(ctx, &whagentpb.GetSessionRequest{SessionId: "sess-1"})
	require.NoError(t, err)

	assert.Equal(t, "Bearer "+operatorToken, fake.authHeader, "api must see the operator's own token, forwarded byte for byte -- not re-minted, not a shared service account")
}

// TestSessionClient_NoDialOptionForwardsNoToken is the negative control:
// with no user-token dial option at all (the shape a regression like the
// one described in the doc comment above would produce), no authorization
// header reaches api regardless of what's on the call context.
func TestSessionClient_NoDialOptionForwardsNoToken(t *testing.T) {
	fake := &fakeSessionServer{}
	client := newBufconnSessionClient(t, fake)

	ctx := grpcauth.WithUserToken(context.Background(), "operator-raw-token-xyz789")

	_, err := client.Client().GetSession(ctx, &whagentpb.GetSessionRequest{SessionId: "sess-1"})
	require.NoError(t, err)

	assert.Empty(t, fake.authHeader, "expected no authorization header without a user-token dial option")
}
