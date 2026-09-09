package server

// Pure-Go coverage for auth.go's dual-path verifier and middleware (issue
// #2249's Testing section): isCredentialShaped's exact shape split,
// NewVerifier's classification of an opaque mcpauth credential vs.
// everything else (including the nil-credentials degrade-to-pre-FR9
// behavior), and AuthMiddleware's two forwarding branches -- all against
// fakeCredentialStore/fakeExchanger (below), never a real database or
// Keycloak. newFakeGRPCBackend proves what actually reaches a gRPC call's
// wire metadata, mirroring auth_pass_through_test.go's own
// fakeSessionServer/newFakeBackend one level down (that file cannot be
// reused directly here: it lives in package server_test, and these tests
// need this package's unexported identityExtraKey/tokenExtraKey/
// resolvedIdentity to build fixtures).
import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
	pb "github.com/whale-net/everything/whagent_net/protos"
)

// ── requestWithExtra ─────────────────────────────────────────────────────

// requestWithExtra builds the minimal mcp.Request AuthMiddleware reads:
// only GetExtra() matters to it (mirrors
// audience_score_system/mcp/server/auth_test.go's own helper of the same
// name/shape).
func requestWithExtra(extra *mcp.RequestExtra) mcp.Request {
	return &mcp.ServerRequest[*mcp.CallToolParams]{Extra: extra}
}

// ── fake mcpauth.CredentialStore ────────────────────────────────────────

// fakeCredentialStore implements mcpauth.CredentialStore in memory, keyed
// on the exact raw token presented -- enough to drive NewVerifier's
// credential-shaped branch without a real database. Mint/List are not used
// by these tests.
type fakeCredentialStore struct {
	mu          sync.Mutex
	identities  map[string]string // raw token -> encoded identity
	revoked     map[string]bool
	verifyCalls int
}

func newFakeCredentialStore() *fakeCredentialStore {
	return &fakeCredentialStore{identities: map[string]string{}, revoked: map[string]bool{}}
}

func (f *fakeCredentialStore) Mint(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("fakeCredentialStore.Mint is not used by these tests")
}

func (f *fakeCredentialStore) Verify(_ context.Context, rawToken string) (string, mcpauth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyCalls++
	if f.revoked[rawToken] {
		return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
	}
	identity, ok := f.identities[rawToken]
	if !ok {
		return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
	}
	return identity, mcpauth.Credential{Identity: identity}, nil
}

func (f *fakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("fakeCredentialStore.Revoke is not used by these tests")
}

func (f *fakeCredentialStore) List(context.Context, string) ([]mcpauth.Credential, error) {
	return nil, errors.New("fakeCredentialStore.List is not used by these tests")
}

var _ mcpauth.CredentialStore = (*fakeCredentialStore)(nil)

// ── fake Exchanger ───────────────────────────────────────────────────────

// fakeExchanger implements Exchanger, recording every (iss, sub) pair it
// was asked to exchange and returning either a fixed jwt or a fixed error.
type fakeExchanger struct {
	mu    sync.Mutex
	jwt   string
	err   error
	calls []identityKey
}

func (f *fakeExchanger) Exchange(_ context.Context, iss, sub string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, identityKey{iss: iss, sub: sub})
	if f.err != nil {
		return "", f.err
	}
	return f.jwt, nil
}

var _ Exchanger = (*fakeExchanger)(nil)

// hexToken returns a 64-character lowercase hex string (isCredentialShaped's
// exact shape) built by repeating r -- deterministic and readable in test
// failures, never generated via crypto/rand (these tests don't need real
// entropy, only a fixed shape/value).
func hexToken(r byte) string {
	return strings.Repeat(string(r), 64)
}

// ── isCredentialShaped ───────────────────────────────────────────────────

func TestIsCredentialShaped(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"64 lowercase hex chars", hexToken('a'), true},
		{"64 hex chars all digits", hexToken('7'), true},
		{"a JWT compact serialization", "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhIn0.c2ln", false},
		{"63 hex chars (one short)", strings.Repeat("a", 63), false},
		{"65 hex chars (one too many)", strings.Repeat("a", 65), false},
		{"uppercase hex", strings.Repeat("A", 64), false},
		{"64 chars but not hex", strings.Repeat("g", 64), false},
		{"empty string", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isCredentialShaped(tc.token))
		})
	}
}

// ── NewVerifier ──────────────────────────────────────────────────────────

func TestNewVerifier_EmptyOrWhitespaceToken_Rejected(t *testing.T) {
	verifier := NewVerifier(nil)
	for _, token := range []string{"", "   ", "\t\n"} {
		_, err := verifier(context.Background(), token, nil)
		assert.ErrorIs(t, err, sdkauth.ErrInvalidToken)
	}
}

func TestNewVerifier_NilCredentials_TreatsEveryTokenAsManualPathRegardlessOfShape(t *testing.T) {
	verifier := NewVerifier(nil)

	for _, token := range []string{hexToken('a'), "some-raw-keycloak-jwt.like.this"} {
		info, err := verifier(context.Background(), token, nil)
		require.NoError(t, err)
		got, ok := info.Extra[tokenExtraKey].(string)
		require.True(t, ok, "credentials==nil must reproduce pre-FR9 behavior: every token forwarded byte for byte")
		assert.Equal(t, token, got)
		_, hasIdentity := info.Extra[identityExtraKey]
		assert.False(t, hasIdentity)
	}
}

func TestNewVerifier_NonCredentialShapedToken_FallsToManualPathWithoutTouchingTheStore(t *testing.T) {
	store := newFakeCredentialStore()
	verifier := NewVerifier(store)

	const rawKeycloakLikeToken = "not-hex-and-has.dots.like-a-jwt"
	info, err := verifier(context.Background(), rawKeycloakLikeToken, nil)
	require.NoError(t, err)
	got, ok := info.Extra[tokenExtraKey].(string)
	require.True(t, ok)
	assert.Equal(t, rawKeycloakLikeToken, got)
	assert.Zero(t, store.verifyCalls, "a non-credential-shaped token must never be checked against the credential store")
}

func TestNewVerifier_CredentialShaped_Valid_DecodesToExactIssSub(t *testing.T) {
	const iss = "https://keycloak.example.test/realms/whagent"
	const sub = "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	encoded, err := mcpidentity.Encode(iss, sub)
	require.NoError(t, err)

	token := hexToken('a')
	store := newFakeCredentialStore()
	store.identities[token] = encoded
	verifier := NewVerifier(store)

	info, err := verifier(context.Background(), token, nil)
	require.NoError(t, err)
	identity, ok := info.Extra[identityExtraKey].(resolvedIdentity)
	require.True(t, ok, "TokenInfo.Extra must carry the decoded resolvedIdentity under identityExtraKey")
	assert.Equal(t, iss, identity.iss)
	assert.Equal(t, sub, identity.sub)
	_, hasRawToken := info.Extra[tokenExtraKey]
	assert.False(t, hasRawToken, "the OAuth2 path must never also carry the raw presented credential")
}

func TestNewVerifier_CredentialShaped_UnknownOrRevoked_Rejected(t *testing.T) {
	store := newFakeCredentialStore()
	verifier := NewVerifier(store)

	unknown := hexToken('b')
	_, err := verifier(context.Background(), unknown, nil)
	assert.ErrorIs(t, err, sdkauth.ErrInvalidToken, "an unrecognized credential-shaped token must be rejected outright")

	revoked := hexToken('c')
	encoded, err := mcpidentity.Encode("https://keycloak.example.test/realms/whagent", "some-sub")
	require.NoError(t, err)
	store.identities[revoked] = encoded
	store.revoked[revoked] = true
	_, err = verifier(context.Background(), revoked, nil)
	assert.ErrorIs(t, err, sdkauth.ErrInvalidToken, "a revoked credential must be rejected identically to an unknown one (NFR1)")
}

func TestNewVerifier_CredentialShaped_CorruptStoredIdentity_RejectedNotGuessed(t *testing.T) {
	token := hexToken('d')
	store := newFakeCredentialStore()
	store.identities[token] = "this-is-not-a-valid-encoded-iss-sub-pair"
	verifier := NewVerifier(store)

	_, err := verifier(context.Background(), token, nil)
	assert.ErrorIs(t, err, sdkauth.ErrInvalidToken)
}

// ── AuthMiddleware ───────────────────────────────────────────────────────

func TestAuthMiddleware_NoTokenInfo_RejectsBeforeNextRuns(t *testing.T) {
	cases := []struct {
		name string
		req  mcp.Request
	}{
		{"no Extra at all", requestWithExtra(nil)},
		{"Extra with nil TokenInfo", requestWithExtra(&mcp.RequestExtra{})},
		{"TokenInfo with neither key present", requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var called bool
			next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				called = true
				return nil, nil
			})
			_, err := AuthMiddleware(&fakeExchanger{})(next)(context.Background(), "tools/call", tc.req)
			require.Error(t, err)
			assert.False(t, called, "next must never run for a call carrying no bearer-derived identity")
		})
	}
}

func TestAuthMiddleware_OAuth2Path_ExchangesIdentityAndForwardsResultToAPI(t *testing.T) {
	fake, client := newFakeGRPCBackend(t)
	exchanger := &fakeExchanger{jwt: "exchanged-jwt-xyz"}

	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		_, err := client.GetSession(ctx, &pb.GetSessionRequest{SessionId: "sess-1"})
		return nil, err
	})

	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{identityExtraKey: resolvedIdentity{iss: "https://keycloak.example.test/realms/whagent", sub: "operator-sub-1"}},
	}})

	_, err := AuthMiddleware(exchanger)(next)(context.Background(), "tools/call", req)
	require.NoError(t, err)

	require.Len(t, exchanger.calls, 1)
	assert.Equal(t, identityKey{iss: "https://keycloak.example.test/realms/whagent", sub: "operator-sub-1"}, exchanger.calls[0])

	headers := fake.recordedAuthHeaders()
	require.Len(t, headers, 1)
	assert.Equal(t, "Bearer exchanged-jwt-xyz", headers[0], "api must see the EXCHANGED jwt, never the opaque credential or the raw identity")

	// Red/green (verified by hand, then reverted): changing this branch to
	// forward identity.sub (or the raw presented credential) directly --
	// skipping exchanger.Exchange entirely -- made this assertion fail
	// (api recorded something other than "Bearer exchanged-jwt-xyz").
	// Reverting restored it to green.
}

func TestAuthMiddleware_OAuth2Path_ExchangeFailure_RejectsWithoutCallingNext(t *testing.T) {
	exchanger := &fakeExchanger{err: errors.New("token endpoint unreachable")}
	var called bool
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		called = true
		return nil, nil
	})
	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{identityExtraKey: resolvedIdentity{iss: "iss", sub: "sub"}},
	}})

	_, err := AuthMiddleware(exchanger)(next)(context.Background(), "tools/call", req)
	require.Error(t, err)
	assert.False(t, called, "a failed exchange must reject the call outright, never fall back to another path")
}

func TestAuthMiddleware_ManualPath_ForwardsRawTokenToAPIUnchanged(t *testing.T) {
	fake, client := newFakeGRPCBackend(t)
	exchanger := &fakeExchanger{} // must never be consulted on this path

	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		_, err := client.GetSession(ctx, &pb.GetSessionRequest{SessionId: "sess-1"})
		return nil, err
	})
	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{tokenExtraKey: "operator-raw-keycloak-token"},
	}})

	_, err := AuthMiddleware(exchanger)(next)(context.Background(), "tools/call", req)
	require.NoError(t, err)

	assert.Empty(t, exchanger.calls, "the manual-token path must never call the exchanger")
	headers := fake.recordedAuthHeaders()
	require.Len(t, headers, 1)
	assert.Equal(t, "Bearer operator-raw-keycloak-token", headers[0])
}

func TestAuthMiddleware_ManualPath_EmptyToken_Rejected(t *testing.T) {
	var called bool
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		called = true
		return nil, nil
	})
	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{tokenExtraKey: ""},
	}})
	_, err := AuthMiddleware(&fakeExchanger{})(next)(context.Background(), "tools/call", req)
	require.Error(t, err)
	assert.False(t, called)
}

// TestAuthMiddleware_APIRejection_PropagatesIdenticallyOnBothPaths is this
// task's "role parity" coverage: an operator who fails some api-side check
// (modeled here as a fixed gRPC error api's GetSession returns -- api
// itself is unchanged by this task) must see that exact rejection
// regardless of which auth path resolved their identity. AuthMiddleware
// must never translate, wrap, or otherwise vary next's error by path.
func TestAuthMiddleware_APIRejection_PropagatesIdenticallyOnBothPaths(t *testing.T) {
	wantErr := status.Error(codes.PermissionDenied, "missing required_role for this agent")

	fake, client := newFakeGRPCBackend(t)
	fake.getSessionFn = func(*pb.GetSessionRequest) (*pb.GetSessionResponse, error) { return nil, wantErr }

	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		_, err := client.GetSession(ctx, &pb.GetSessionRequest{SessionId: "sess-1"})
		return nil, err
	})

	manualReq := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{tokenExtraKey: "operator-raw-token"},
	}})
	_, manualErr := AuthMiddleware(&fakeExchanger{})(next)(context.Background(), "tools/call", manualReq)

	oauthReq := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{identityExtraKey: resolvedIdentity{iss: "iss", sub: "sub"}},
	}})
	_, oauthErr := AuthMiddleware(&fakeExchanger{jwt: "exchanged-jwt"})(next)(context.Background(), "tools/call", oauthReq)

	require.Error(t, manualErr)
	require.Error(t, oauthErr)
	assert.Equal(t, codes.PermissionDenied, status.Code(manualErr))
	assert.Equal(t, codes.PermissionDenied, status.Code(oauthErr))
	assert.Equal(t, manualErr.Error(), oauthErr.Error(), "api's rejection must reach the caller identically regardless of which auth path resolved the identity")
}

// ── fake gRPC backend (bufconn) ──────────────────────────────────────────

const bufSize = 1024 * 1024

// fakeGRPCSessionServer is a real pb.SessionServiceServer, reached over a
// real (in-memory, bufconn) gRPC connection, that records the
// "authorization" metadata value it saw on each GetSession call. Mirrors
// auth_pass_through_test.go's fakeSessionServer -- duplicated here rather
// than imported because that file lives in package server_test and these
// tests need direct access to this package's unexported identity/token
// extra keys.
type fakeGRPCSessionServer struct {
	pb.UnimplementedSessionServiceServer

	mu           sync.Mutex
	authHeaders  []string
	getSessionFn func(*pb.GetSessionRequest) (*pb.GetSessionResponse, error)
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

func (f *fakeGRPCSessionServer) recordedAuthHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.authHeaders))
	copy(out, f.authHeaders)
	return out
}

// newFakeGRPCBackend stands up fakeGRPCSessionServer over a bufconn
// listener and dials it exactly the way main.go dials api:
// grpcauth.NewUserTokenDialOption(AuthModeOIDC), which forwards whatever
// token grpcauth.WithUserToken placed on the call's context, byte for
// byte, as the outbound Authorization header -- the same mechanism
// AuthMiddleware uses.
func newFakeGRPCBackend(t *testing.T) (*fakeGRPCSessionServer, pb.SessionServiceClient) {
	t.Helper()
	ctx := context.Background()

	fake := &fakeGRPCSessionServer{}
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
