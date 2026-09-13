package server

// Pure-Go coverage for whagent_auth.go's Implementation-phase bodies:
// isWhagentShapedToken/bearerToken's shape split, DualAuthHTTPHandler's
// per-request routing and NFR4 rejection cases, and
// WhagentPersonMiddleware's resolution/fall-through behavior against
// fakePersonIdentityStore (fakes_test.go) -- all without a real HTTP
// listener or database. server_integration_test.go (Testing phase) covers
// the same wiring against a real Postgres-backed store.PersonIdentityStore
// and a real streamable-HTTP client, including the FR11 idempotency and
// auto-provisioning-race properties that genuinely require Postgres.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/whagent"
)

// ── isWhagentShapedToken / bearerToken ──────────────────────────────────────

func TestIsWhagentShapedToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"well-formed three-segment JWT", "eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJhIn0.c2ln", true},
		{"ASS mcp_credential (64-char hex, no dots)", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", false},
		{"empty string", "", false},
		{"one dot only", "a.b", false},
		{"three dots (four segments)", "a.b.c.d", false},
		{"empty middle segment", "a..c", false},
		{"empty final segment", "a.b.", false},
		{"empty leading segment", ".b.c", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isWhagentShapedToken(tc.token))
		})
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"well-formed bearer header", "Bearer abc.def.ghi", "abc.def.ghi"},
		{"case-insensitive scheme", "bearer abc.def.ghi", "abc.def.ghi"},
		{"no Authorization header", "", ""},
		{"wrong scheme", "Basic dXNlcjpwYXNz", ""},
		{"missing token", "Bearer", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			assert.Equal(t, tc.want, bearerToken(req))
		})
	}
}

// ── DualAuthHTTPHandler ──────────────────────────────────────────────────────

// newTestWhagentVerifier builds a whagent.Signer/Verifier pair for issuer,
// mirroring libs/go/whagent's own newTestSigner fixture (unexported there,
// so duplicated at the width this package's tests actually need).
func newTestWhagentVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

func TestDualAuthHTTPHandler(t *testing.T) {
	const (
		issuer   = "https://whagent.example.test"
		audience = "https://mcp.example.test"
	)
	signer, verifier := newTestWhagentVerifier(t, issuer)

	mintValid := func(t *testing.T) string {
		t.Helper()
		token, err := signer.Mint(context.Background(), whagent.MintRequest{
			Subject:       "human-1",
			SubjectIssuer: "https://keycloak.example.test/realms/humans",
			Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
			SessionID:     "session-1",
			Audience:      audience,
		})
		require.NoError(t, err)
		return token
	}

	credentials := fakeCredentialStore{validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", identity: "person-1"}
	cfg := WhagentAuthConfig{Verifier: verifier, Audience: audience}

	newHandler := func() (http.Handler, *bool, **sdkauth.TokenInfo) {
		called := false
		var gotTokenInfo *sdkauth.TokenInfo
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			gotTokenInfo = sdkauth.TokenInfoFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		return DualAuthHTTPHandler(inner, credentials, cfg, nil), &called, &gotTokenInfo
	}

	doRequest := func(handler http.Handler, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("valid whagent-shaped token routes through the whagent path and reaches the handler", func(t *testing.T) {
		handler, called, gotTokenInfo := newHandler()
		rec := doRequest(handler, mintValid(t))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, *called, "a valid whagent credential must reach the wrapped handler")
		require.NotNil(t, *gotTokenInfo)
		claim, ok := (*gotTokenInfo).Extra[whagentClaimExtraKey].(*whagent.Claim)
		require.True(t, ok, "TokenInfo.Extra must carry the verified *whagent.Claim under whagentClaimExtraKey")
		assert.Equal(t, "human-1", claim.Subject)
		assert.Equal(t, "https://keycloak.example.test/realms/humans", claim.SubjectIssuer)
	})

	t.Run("valid mcp_credential-shaped token routes through the credential path and reaches the handler", func(t *testing.T) {
		handler, called, gotTokenInfo := newHandler()
		rec := doRequest(handler, credentials.validToken)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, *called, "a valid mcp_credential must reach the wrapped handler")
		require.NotNil(t, *gotTokenInfo)
		assert.Equal(t, credentials.identity, (*gotTokenInfo).UserID)
		_, hasWhagentClaim := (*gotTokenInfo).Extra[whagentClaimExtraKey]
		assert.False(t, hasWhagentClaim, "a credential-path TokenInfo must never carry a whagent claim marker")
	})

	t.Run("an unrecognized mcp_credential-shaped token is rejected and the handler is never entered", func(t *testing.T) {
		// Not whagent-shaped (no dots), so this is routed to, and can only
		// be evaluated by, the credential branch -- it never reaches
		// cfg.Verifier at all.
		handler, called, _ := newHandler()
		rec := doRequest(handler, "0000000000000000000000000000000000000000000000000000000000000000")

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, *called, "the tool handler must never be entered on a rejected call (NFR4)")
	})

	t.Run("a whagent credential is shaped so it can only ever route to the whagent branch, never the credential one", func(t *testing.T) {
		token := mintValid(t)
		assert.True(t, isWhagentShapedToken(token), "a minted whagent Claim JWT must always route to the whagent branch")
		assert.NotEqual(t, credentials.validToken, token, "and must never coincide with a live mcp_credential value")
	})

	t.Run("wrong-audience whagent token is rejected and the handler is never entered", func(t *testing.T) {
		handler, called, _ := newHandler()
		token, err := signer.Mint(context.Background(), whagent.MintRequest{
			Subject:       "human-1",
			SubjectIssuer: "https://keycloak.example.test/realms/humans",
			Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
			SessionID:     "session-1",
			Audience:      "https://some-other-domain.example.test",
		})
		require.NoError(t, err)

		rec := doRequest(handler, token)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, *called)
	})

	t.Run("token signed by a non-whagent key is rejected and the handler is never entered", func(t *testing.T) {
		handler, called, _ := newHandler()
		otherSigner, _ := newTestWhagentVerifier(t, issuer)
		token, err := otherSigner.Mint(context.Background(), whagent.MintRequest{
			Subject:       "human-1",
			SubjectIssuer: "https://keycloak.example.test/realms/humans",
			Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
			SessionID:     "session-1",
			Audience:      audience,
		})
		require.NoError(t, err)

		rec := doRequest(handler, token)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, *called)
	})

	t.Run("no bearer token at all defaults to the credential path's own rejection", func(t *testing.T) {
		handler, called, _ := newHandler()
		rec := doRequest(handler, "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, *called)
	})
}

// ── WhagentPersonMiddleware ──────────────────────────────────────────────────

func TestWhagentPersonMiddleware_FallsThroughWhenNotWhagentRouted(t *testing.T) {
	identities := newFakePersonIdentityStore()

	cases := []struct {
		name string
		req  mcp.Request
	}{
		{"no Extra at all", requestWithExtra(nil)},
		{"Extra with nil TokenInfo", requestWithExtra(&mcp.RequestExtra{})},
		{"TokenInfo with no whagent claim marker (mcp_credential path)", requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{UserID: "person-1"}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nextCalled bool
			var gotMethod string
			next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				nextCalled = true
				gotMethod = method
				assert.Nil(t, PersonFromContext(ctx), "must not resolve a Person for a non-whagent-routed call")
				return nil, nil
			})

			_, err := WhagentPersonMiddleware(identities)(next)(context.Background(), "tools/call", tc.req)
			require.NoError(t, err)
			assert.True(t, nextCalled, "a non-whagent-routed call must fall through to next (PersonMiddleware) unchanged")
			assert.Equal(t, "tools/call", gotMethod)
		})
	}
	assert.Zero(t, identities.calls, "identity resolution must never run for a call this middleware didn't route")
}

func TestWhagentPersonMiddleware_ResolvesAndAutoProvisionsOnFirstSight(t *testing.T) {
	identities := newFakePersonIdentityStore()
	claim := &whagent.Claim{SubjectIssuer: "https://keycloak.example.test/realms/humans"}
	claim.Subject = "human-1"

	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{whagentClaimExtraKey: claim},
	}})

	var gotPerson1, gotPerson2 *store.Person
	var gotAuthPath1 AuthPath
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		gotPerson1 = PersonFromContext(ctx)
		gotAuthPath1 = AuthPathFromContext(ctx)
		return nil, nil
	})
	_, err := WhagentPersonMiddleware(identities)(next)(context.Background(), "tools/call", req)
	require.NoError(t, err)
	require.NotNil(t, gotPerson1)
	assert.Equal(t, AuthPathWhagent, gotAuthPath1, "WhagentPersonMiddleware must stamp AuthPathWhagent (FR12), not derive it from the token")

	// A second call for the exact same (iss, sub) pair must resolve to the
	// same Person and must not create a second one. This Person comes back
	// from the fake's existing-entry branch, not its "mint a new one"
	// branch -- proving the AuthPathWhagent marker comes from the
	// middleware calling withPerson itself, not from anything about how
	// the Person was resolved or what the claim/token contained.
	var gotAuthPath2 AuthPath
	next2 := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		gotPerson2 = PersonFromContext(ctx)
		gotAuthPath2 = AuthPathFromContext(ctx)
		return nil, nil
	})
	_, err = WhagentPersonMiddleware(identities)(next2)(context.Background(), "tools/call", req)
	require.NoError(t, err)
	require.NotNil(t, gotPerson2)

	assert.Equal(t, gotPerson1.ID, gotPerson2.ID, "the same (iss, sub) pair must resolve to the same person_id across calls")
	assert.Equal(t, AuthPathWhagent, gotAuthPath2, "AuthPathWhagent must still be set when resolving an already-existing Person, not just on first-sight auto-provisioning")
	assert.Equal(t, 2, identities.calls)
}

func TestWhagentPersonMiddleware_RejectsWhenResolutionFails(t *testing.T) {
	identities := newFakePersonIdentityStore()
	identities.err = assert.AnError

	claim := &whagent.Claim{SubjectIssuer: "https://keycloak.example.test/realms/humans"}
	claim.Subject = "human-1"
	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{whagentClaimExtraKey: claim},
	}})

	var nextCalled bool
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		nextCalled = true
		return nil, nil
	})

	_, err := WhagentPersonMiddleware(identities)(next)(context.Background(), "tools/call", req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthenticated")
	assert.False(t, nextCalled, "next must never run when identity resolution fails (NFR4)")
}
