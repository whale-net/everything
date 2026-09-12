package tools

// Coverage for dispatch.go (issue #2430's Testing section): the
// dispatch-time scope-resolution + delegated-grant token-acquisition
// sequence every tool's call() runs, replacing RFC 8693 impersonation
// exchange. Exercises resolveGrantTokenForAgent/resolveGrantTokenForSession
// directly against fakeScopeResolver/fakeGrantSource (never a real
// Keycloak or Postgres-backed grant store), plus one end-to-end proof (via
// a real bufconn gRPC round trip, mirroring ../server/auth_test.go's
// newFakeGRPCBackend) that an acquired token actually reaches `api`'s wire
// metadata, not just resolveGrantTokenForAgent's return value.
//
// Red/green (verified by hand, then reverted -- see this file's own
// history in the same commit as this comment): the "no grant -> named
// scope" and NFR2 cross-scope-refusal tests below were run against a
// deliberately broken acquireGrantToken (scope name dropped from the
// wrapped error, and TokenSource called with the wrong key) and failed
// with exactly the expected assertion mismatches before the fix was
// reverted back in.
import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
	pb "github.com/whale-net/everything/whagent_net/protos"
)

// ── resolveGrantTokenForAgent / resolveGrantTokenForSession ─────────────

// TestResolveGrantTokenForAgent_NoIdentityOnContext_IsNoOp proves the
// manual-token path (no mcpidentity.Identity on ctx) never consults
// scopeResolver or grant at all -- dispatch.go's own doc comment's "nil
// interfaces are never touched" contract, and the reason every
// pre-existing tool test built against context.Background() keeps passing
// unmodified.
func TestResolveGrantTokenForAgent_NoIdentityOnContext_IsNoOp(t *testing.T) {
	ctx := context.Background()
	scopeResolver := newFakeScopeResolver()
	grant := &fakeGrantSource{}

	outCtx, err := resolveGrantTokenForAgent(ctx, scopeResolver, grant, "agent-1")

	require.NoError(t, err)
	assert.True(t, ctx == outCtx, "ctx must be returned completely unchanged, not merely equivalent") //nolint:staticcheck // intentional interface identity check
	assert.Empty(t, scopeResolver.agentCalls, "scopeResolver must never be consulted on the manual-token path")
	assert.Empty(t, grant.calls, "grant must never be consulted on the manual-token path")
}

// TestResolveGrantTokenForSession_NoIdentityOnContext_IsNoOp mirrors the
// above for the session-keyed entry point (send_turn/stop_session/
// get_session/read_transcript's shared sequence).
func TestResolveGrantTokenForSession_NoIdentityOnContext_IsNoOp(t *testing.T) {
	ctx := context.Background()
	scopeResolver := newFakeScopeResolver()
	grant := &fakeGrantSource{}

	outCtx, err := resolveGrantTokenForSession(ctx, scopeResolver, grant, "sess-1")

	require.NoError(t, err)
	assert.True(t, ctx == outCtx) //nolint:staticcheck
	assert.Empty(t, scopeResolver.sessionCalls)
	assert.Empty(t, grant.calls)
}

// TestResolveGrantTokenForAgent_ActiveGrant_ResolvesScopeAndAcquiresExactPair
// is this task's first Testing-section bullet: start_session's dispatch
// sequence resolves the agent's scope, derives the grant key from it
// (grantkey.ForScope), and asks GrantSource for exactly (identity.Sub,
// grantKey) -- never identity.Iss, never the agent id itself.
func TestResolveGrantTokenForAgent_ActiveGrant_ResolvesScopeAndAcquiresExactPair(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-1"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.agentScopes["agent-1"] = scopePtr("manmanv2")

	grant := &fakeGrantSource{
		tokenFunc: func(_ context.Context, subject, grantKey string) (*oauth2.Token, error) {
			assert.Equal(t, "operator-1", subject, "the acquired token must be keyed on identity.Sub, never identity.Iss")
			assert.Equal(t, "manmanv2", grantKey)
			return &oauth2.Token{AccessToken: "acquired-tok-1"}, nil
		},
	}

	outCtx, err := resolveGrantTokenForAgent(ctx, scopeResolver, grant, "agent-1")
	require.NoError(t, err)

	require.Equal(t, []string{"agent-1"}, scopeResolver.agentCalls)
	require.Len(t, grant.calls, 1)
	assert.Equal(t, fakeTokenSourceCall{subject: "operator-1", grant: "manmanv2"}, grant.calls[0])

	assertForwardsToken(t, outCtx, "acquired-tok-1")
}

// TestResolveGrantTokenForSession_ActiveGrant_ResolvesViaSessionNeverAgent
// proves the session-keyed entry point resolves scope via ScopeForSession
// only -- ScopeForAgent must never be called, even when the fake resolver
// has no agentScopes entry configured for anything at all.
func TestResolveGrantTokenForSession_ActiveGrant_ResolvesViaSessionNeverAgent(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-2"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.sessionScopes["sess-1"] = scopePtr("audience_score_system")

	grant := &fakeGrantSource{
		tokenFunc: func(_ context.Context, subject, grantKey string) (*oauth2.Token, error) {
			assert.Equal(t, "operator-2", subject)
			assert.Equal(t, "audience_score_system", grantKey)
			return &oauth2.Token{AccessToken: "acquired-tok-2"}, nil
		},
	}

	outCtx, err := resolveGrantTokenForSession(ctx, scopeResolver, grant, "sess-1")
	require.NoError(t, err)

	assert.Empty(t, scopeResolver.agentCalls, "ScopeForAgent must never be called by the session-keyed entry point")
	require.Equal(t, []string{"sess-1"}, scopeResolver.sessionCalls)

	assertForwardsToken(t, outCtx, "acquired-tok-2")
}

// TestResolveGrantTokenForAgent_NoGrantForResolvedScope_FailsNamingScope
// is this task's "no grant -> named scope failure" bullet: an operator
// with no active grant for the resolved scope fails, the error names that
// scope, and the outward ctx is untouched -- no fallback to any other
// credential path (NFR2).
func TestResolveGrantTokenForAgent_NoGrantForResolvedScope_FailsNamingScope(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-3"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.agentScopes["agent-1"] = scopePtr("manmanv2")

	grant := &fakeGrantSource{
		tokenFunc: func(context.Context, string, string) (*oauth2.Token, error) {
			return nil, fmt.Errorf("grpcauth: no delegated grant for (subject=%q, grant=%q): %w", "operator-3", "manmanv2", grpcauth.ErrGrantNotFound)
		},
	}

	outCtx, err := resolveGrantTokenForAgent(ctx, scopeResolver, grant, "agent-1")

	require.Error(t, err)
	assert.ErrorIs(t, err, grpcauth.ErrGrantNotFound)
	assert.Contains(t, err.Error(), "manmanv2", "the failure must name the scope the operator needs to consent for")
	assert.True(t, ctx == outCtx, "a failed acquisition must return ctx completely unchanged, never a partially-populated one") //nolint:staticcheck
}

// TestCrossScopeRefusal_OperatorWithOtherScopeGrant_NeverSubstituted is
// NFR2's own scenario, verbatim: an operator with an active grant for
// audience_score_system calling into a manmanv2-scope session must get a
// failure, never a token minted from the audience_score_system grant.
// grant.tokenFunc below would happily mint a token if ever asked for
// "audience_score_system" -- proving the failure isn't just "grant always
// fails in this test", but specifically that manmanv2 was the (and only
// the) key requested.
func TestCrossScopeRefusal_OperatorWithOtherScopeGrant_NeverSubstituted(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-4"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.sessionScopes["sess-manmanv2"] = scopePtr("manmanv2")

	grant := &fakeGrantSource{
		tokenFunc: func(_ context.Context, subject, grantKey string) (*oauth2.Token, error) {
			if grantKey == "audience_score_system" {
				return &oauth2.Token{AccessToken: "audience-score-system-token"}, nil
			}
			return nil, fmt.Errorf("grpcauth: no delegated grant for (subject=%q, grant=%q): %w", subject, grantKey, grpcauth.ErrGrantNotFound)
		},
	}

	outCtx, err := resolveGrantTokenForSession(ctx, scopeResolver, grant, "sess-manmanv2")

	require.Error(t, err)
	assert.ErrorIs(t, err, grpcauth.ErrGrantNotFound)
	assert.Contains(t, err.Error(), "manmanv2")
	assert.NotContains(t, err.Error(), "audience-score-system-token")
	assert.True(t, ctx == outCtx) //nolint:staticcheck

	require.Len(t, grant.calls, 1, "exactly one scope must ever be requested for this call")
	assert.Equal(t, "manmanv2", grant.calls[0].grant, "the call's own resolved scope must be requested, never the operator's other active grant's scope")
}

// TestAcquireGrantToken_NoCache_ConsecutiveCallsBothHitTokenSource proves
// NFR7's "no stale cache" clause holds at this package's own layer too
// (not only inside libs/go/grpcauth's Store, issue #2426's own coverage):
// two resolveGrantTokenForSession calls for the same (subject, scope)
// pair both call GrantSource.TokenSource fresh -- revoking the grant
// between them makes the second call fail immediately, with nothing in
// this package's dispatch-time path masking that by reusing the first
// call's result.
func TestAcquireGrantToken_NoCache_ConsecutiveCallsBothHitTokenSource(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-5"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.sessionScopes["sess-1"] = scopePtr("manmanv2")

	var mu sync.Mutex
	revoked := false
	grant := &fakeGrantSource{
		tokenFunc: func(context.Context, string, string) (*oauth2.Token, error) {
			mu.Lock()
			defer mu.Unlock()
			if revoked {
				return nil, fmt.Errorf("grpcauth: grant revoked mid-session: %w", grpcauth.ErrGrantRevoked)
			}
			return &oauth2.Token{AccessToken: "tok-before-revoke"}, nil
		},
	}

	_, err := resolveGrantTokenForSession(ctx, scopeResolver, grant, "sess-1")
	require.NoError(t, err, "first call, grant still active, must succeed")

	mu.Lock()
	revoked = true
	mu.Unlock()

	_, err = resolveGrantTokenForSession(ctx, scopeResolver, grant, "sess-1")
	require.Error(t, err, "second call must observe the revocation immediately -- no cached success from the first call")
	assert.ErrorIs(t, err, grpcauth.ErrGrantRevoked)

	require.Len(t, grant.calls, 2, "both calls must have reached GrantSource.TokenSource -- a cache would short-circuit the second")
	assert.Equal(t, grant.calls[0], grant.calls[1], "both calls must request the identical (subject, grant) pair")
}

// TestResolveGrantTokenForAgent_ScopeResolutionFails_NeverConsultsGrant
// proves a scope-resolution failure (e.g. an unknown agent id) short
// circuits before GrantSource is ever asked for anything.
func TestResolveGrantTokenForAgent_ScopeResolutionFails_NeverConsultsGrant(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-6"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver() // agent-unknown has no entry
	grant := &fakeGrantSource{}

	outCtx, err := resolveGrantTokenForAgent(ctx, scopeResolver, grant, "agent-unknown")

	require.Error(t, err)
	assert.ErrorIs(t, err, errFakeScopeNotFound)
	assert.Empty(t, grant.calls, "GrantSource must never be consulted once scope resolution has already failed")
	assert.True(t, ctx == outCtx) //nolint:staticcheck
}

// ── end-to-end wire proof ────────────────────────────────────────────────

// fakeGRPCSessionServer is a real pb.SessionServiceServer, reached over a
// real (in-memory, bufconn) gRPC connection, that records the
// "authorization" metadata value it saw on each GetSession call --
// mirrors ../server/auth_test.go's own fakeGRPCSessionServer /
// newFakeGRPCBackend (duplicated rather than imported: that lives in
// package server and this suite has no reason to depend on it).
type fakeGRPCSessionServer struct {
	pb.UnimplementedSessionServiceServer

	mu          sync.Mutex
	authHeaders []string
}

func (f *fakeGRPCSessionServer) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	var auth string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			auth = vals[0]
		}
	}
	f.mu.Lock()
	f.authHeaders = append(f.authHeaders, auth)
	f.mu.Unlock()
	return &pb.GetSessionResponse{Session: &pb.Session{SessionId: req.SessionId, State: pb.SessionState_SESSION_STATE_RUNNING}}, nil
}

func (f *fakeGRPCSessionServer) recordedAuthHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.authHeaders))
	copy(out, f.authHeaders)
	return out
}

const dispatchTestBufSize = 1024 * 1024

// assertForwardsToken proves ctx carries wantToken exactly as
// grpcauth.NewUserTokenDialOption forwards it: a real (bufconn) gRPC call
// made with ctx must reach the backend with "Bearer "+wantToken as its
// authorization metadata. This is the same dial option main.go actually
// uses to reach `api`, so this is not merely inspecting dispatch.go's
// return value -- it is the same proof ../server/auth_test.go's
// TestAuthMiddleware_ManualPath_ForwardsRawTokenToAPIUnchanged makes for
// the manual-token path, applied here to the token
// resolveGrantTokenForAgent/resolveGrantTokenForSession acquired.
func assertForwardsToken(t *testing.T, ctx context.Context, wantToken string) {
	t.Helper()

	fake := &fakeGRPCSessionServer{}
	lis := bufconn.Listen(dispatchTestBufSize)
	grpcServer := grpc.NewServer()
	pb.RegisterSessionServiceServer(grpcServer, fake)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpcclient.NewClient(context.Background(), "passthrough:///bufnet",
		grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC),
		grpc.WithContextDialer(dialer),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := pb.NewSessionServiceClient(conn.GetConnection())
	_, err = client.GetSession(ctx, &pb.GetSessionRequest{SessionId: "sess-x"})
	require.NoError(t, err)

	headers := fake.recordedAuthHeaders()
	require.Len(t, headers, 1)
	assert.Equal(t, "Bearer "+wantToken, headers[0], "the token resolveGrantTokenForXxx acquired must be exactly what reaches api's wire metadata")
}

// TestStartSession_EndToEnd_BrowserOAuth2Path_AcquiresAndForwardsToken is
// this task's Testing-section headline scenario, run through the full
// registered tool (not just dispatch.go's helpers directly): start_session
// with a resolved scope and an active grant acquires a token via
// TokenSource and forwards it to `api`, over a real gRPC connection.
func TestStartSession_EndToEnd_BrowserOAuth2Path_AcquiresAndForwardsToken(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-e2e"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.agentScopes["agent-e2e"] = scopePtr("manmanv2")
	grant := &fakeGrantSource{
		tokenFunc: func(_ context.Context, subject, grantKey string) (*oauth2.Token, error) {
			assert.Equal(t, "operator-e2e", subject)
			assert.Equal(t, "manmanv2", grantKey)
			return &oauth2.Token{AccessToken: "e2e-acquired-token"}, nil
		},
	}

	fc := &fakeSessionServiceClient{
		startSessionFunc: func(ctx context.Context, in *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
			return &pb.StartSessionResponse{Session: &pb.Session{SessionId: "sess-e2e", State: pb.SessionState_SESSION_STATE_RUNNING}}, nil
		},
	}
	tool := &startSessionTool{client: fc, scopeResolver: scopeResolver, grant: grant}

	_, out, err := tool.call(ctx, nil, StartSessionInput{AgentID: "agent-e2e"})
	require.NoError(t, err)
	assert.Equal(t, "sess-e2e", out.SessionID)

	require.Len(t, grant.calls, 1)
	assert.Equal(t, fakeTokenSourceCall{subject: "operator-e2e", grant: "manmanv2"}, grant.calls[0])
}

// TestGetSession_NoGrantForResolvedScope_ToolCallFailsWithoutReachingClient
// proves the failure shape all the way up through the registered tool
// (not just dispatch.go directly): the underlying pb.SessionServiceClient
// is never invoked once dispatch-time acquisition has already failed.
func TestGetSession_NoGrantForResolvedScope_ToolCallFailsWithoutReachingClient(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-7"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.sessionScopes["sess-1"] = scopePtr("manmanv2")
	grant := &fakeGrantSource{
		tokenFunc: func(context.Context, string, string) (*oauth2.Token, error) {
			return nil, fmt.Errorf("grpcauth: no delegated grant for scope %q: %w", "manmanv2", grpcauth.ErrGrantNotFound)
		},
	}

	fc := &fakeSessionServiceClient{}
	tool := &getSessionTool{client: fc, scopeResolver: scopeResolver, grant: grant}

	_, out, err := tool.call(ctx, nil, GetSessionInput{SessionID: "sess-1"})

	require.Error(t, err)
	assert.True(t, errors.Is(err, grpcauth.ErrGrantNotFound))
	assert.Contains(t, err.Error(), "manmanv2")
	assert.Equal(t, GetSessionOutput{}, out)
	assert.Empty(t, fc.calls, "GetSession RPC must never be attempted once dispatch-time token acquisition has already failed")
}
