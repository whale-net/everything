package whagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-jose/go-jose/v4/jwt"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestWithExtra builds the minimal mcp.Request Middleware reads: only
// GetExtra() matters to it, so Session/Params are left zero -- mirrors
// audience_score_system/mcp/server/auth_test.go's requestWithExtra.
func requestWithExtra(extra *mcp.RequestExtra) mcp.Request {
	return &mcp.ServerRequest[*mcp.CallToolParams]{Extra: extra}
}

func newTestVerifier(t *testing.T, issuer string) *Verifier {
	t.Helper()
	_, pub, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	verifier, err := NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return verifier
}

// ── Middleware (MCP-protocol layer) ─────────────────────────────────────

func TestMiddleware_RejectsWhenNoVerifiedClaimIsStashed(t *testing.T) {
	verifier := newTestVerifier(t, "whagent-net-test")

	cases := []struct {
		name string
		req  mcp.Request
	}{
		{"no Extra at all", requestWithExtra(nil)},
		{"Extra with nil TokenInfo", requestWithExtra(&mcp.RequestExtra{})},
		{"TokenInfo with no stashed claim", requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{}})},
		{"TokenInfo.Extra has the wrong type under claimExtraKey", requestWithExtra(&mcp.RequestExtra{
			TokenInfo: &sdkauth.TokenInfo{Extra: map[string]any{claimExtraKey: "not-a-claim"}},
		})},
		{"TokenInfo.Extra has a nil *Claim under claimExtraKey", requestWithExtra(&mcp.RequestExtra{
			TokenInfo: &sdkauth.TokenInfo{Extra: map[string]any{claimExtraKey: (*Claim)(nil)}},
		})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nextCalled bool
			next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				nextCalled = true
				return nil, nil
			})

			_, err := Middleware(verifier, "target-domain")(next)(context.Background(), "tools/call", tc.req)
			require.Error(t, err)
			assert.ErrorIs(t, err, errUnauthenticated)
			assert.False(t, nextCalled, "%s: next must never run", tc.name)
		})
	}
}

func TestMiddleware_RejectsStashedClaimWithWrongIssuerOrAudience(t *testing.T) {
	verifier := newTestVerifier(t, "whagent-net-test")

	base := Claim{
		Claims: jwt.Claims{Issuer: "whagent-net-test", Subject: "person-1", Audience: jwt.Audience{"target-domain"}},
	}

	wrongIssuer := base
	wrongIssuer.Issuer = "some-other-issuer"

	wrongAudience := base
	wrongAudience.Audience = jwt.Audience{"a-different-domain"}

	cases := map[string]*Claim{
		"wrong issuer":   &wrongIssuer,
		"wrong audience": &wrongAudience,
	}

	for name, claim := range cases {
		t.Run(name, func(t *testing.T) {
			var nextCalled bool
			next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				nextCalled = true
				return nil, nil
			})

			req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{Extra: map[string]any{claimExtraKey: claim}}})
			_, err := Middleware(verifier, "target-domain")(next)(context.Background(), "tools/call", req)
			require.Error(t, err)
			assert.ErrorIs(t, err, errUnauthenticated)
			assert.False(t, nextCalled)
		})
	}
}

func TestMiddleware_ValidStashedClaim_InvokesNextWithClaimOnContext(t *testing.T) {
	verifier := newTestVerifier(t, "whagent-net-test")

	claim := &Claim{
		Claims:           jwt.Claims{Issuer: "whagent-net-test", Subject: "person-1", Audience: jwt.Audience{"target-domain"}},
		SubjectIssuer:    "https://keycloak.example.test/realms/humans",
		Actor:            Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		WhagentSessionID: "session-abc",
	}

	var nextCalled bool
	var gotClaim *Claim
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		nextCalled = true
		gotClaim = ClaimFromContext(ctx)
		return nil, nil
	})

	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{Extra: map[string]any{claimExtraKey: claim}}})
	_, err := Middleware(verifier, "target-domain")(next)(context.Background(), "tools/call", req)

	require.NoError(t, err)
	assert.True(t, nextCalled, "next must run once the stashed claim matches this Middleware's issuer/audience")
	require.NotNil(t, gotClaim, "ClaimFromContext must see the verified Claim inside next")
	assert.Equal(t, claim, gotClaim)
}

func TestClaimFromContext_NilWhenNothingResolved(t *testing.T) {
	assert.Nil(t, ClaimFromContext(context.Background()), "ClaimFromContext must return nil outside a request Middleware handled")
}

// ── HTTPMiddleware (HTTP layer) ──────────────────────────────────────────

func TestHTTPMiddleware_ValidToken_StashesClaimForMiddlewareToRead(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	token, err := signer.Mint(context.Background(), MintRequest{
		Subject:       "person-1",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "target-domain",
	})
	require.NoError(t, err)

	mw := HTTPMiddleware(verifier, "target-domain")

	var gotClaim *Claim
	handlerCalled := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		info := sdkauth.TokenInfoFromContext(r.Context())
		require.NotNil(t, info)
		gotClaim, _ = info.Extra[claimExtraKey].(*Claim)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, handlerCalled)
	require.NotNil(t, gotClaim, "HTTPMiddleware must stash the verified Claim under claimExtraKey")
	assert.Equal(t, "person-1", gotClaim.Subject)
}

func TestHTTPMiddleware_InvalidToken_Returns401AndNeverInvokesHandler(t *testing.T) {
	verifier := newTestVerifier(t, "whagent-net-test")
	mw := HTTPMiddleware(verifier, "target-domain")

	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not be invoked for an unverifiable token")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHTTPMiddleware_AbsentAuthorizationHeader_Returns401(t *testing.T) {
	verifier := newTestVerifier(t, "whagent-net-test")
	mw := HTTPMiddleware(verifier, "target-domain")

	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not be invoked with no Authorization header at all")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHTTPMiddleware_WrongAudienceToken_Returns401(t *testing.T) {
	signer, pub := newTestSigner(t, "whagent-net-test")
	verifier, err := NewVerifierFromKey(pub, "whagent-net-test")
	require.NoError(t, err)

	token, err := signer.Mint(context.Background(), MintRequest{
		Subject:       "person-1",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-abc",
		Audience:      "a-different-domain",
	})
	require.NoError(t, err)

	mw := HTTPMiddleware(verifier, "target-domain")
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not be invoked for a token minted for a different audience")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
