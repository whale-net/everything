package server

// Pure-Go coverage for whagent_auth.go's bodies: isWhagentShapedToken/
// bearerToken's shape split, DualAuthHTTPHandler's per-request routing and
// rejection cases (both front doors, NFR1), and WhagentPersonaMiddleware's
// resolution/fall-through behavior -- all without a real HTTP listener or
// database. Mirrors audience_score_system/mcp/server/whagent_auth_test.go,
// adapted for krill's simpler (no per-caller identity store) agent door:
// a whagent Claim always resolves to the fixed PersonaAgent, so there is
// no resolution-failure branch to cover here the way that package's
// PersonIdentityStore lookup has.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/whagent"
)

// ── isWhagentShapedToken / bearerToken ───────────────────────────────────────

func TestIsWhagentShapedToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"well-formed three-segment JWT", "eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJhIn0.c2ln", true},
		{"mcpauth credential (64-char hex, no dots)", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", false},
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

// ── DualAuthHTTPHandler ───────────────────────────────────────────────────────

func TestDualAuthHTTPHandler(t *testing.T) {
	const (
		issuer   = "https://whagent.example.test"
		audience = "https://krill-mcp.example.test"
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

	credentials := fakeCredentialStore{validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", identity: "swarm-operator-1"}
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

	t.Run("valid mcpauth-credential-shaped token routes through the credential path and reaches the handler", func(t *testing.T) {
		handler, called, gotTokenInfo := newHandler()
		rec := doRequest(handler, credentials.validToken)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, *called, "a valid mcpauth credential must reach the wrapped handler")
		require.NotNil(t, *gotTokenInfo)
		assert.Equal(t, credentials.identity, (*gotTokenInfo).UserID)
		_, hasWhagentClaim := (*gotTokenInfo).Extra[whagentClaimExtraKey]
		assert.False(t, hasWhagentClaim, "a credential-path TokenInfo must never carry a whagent claim marker")
	})

	t.Run("an unrecognized mcpauth-credential-shaped token is rejected and the handler is never entered", func(t *testing.T) {
		// Not whagent-shaped (no dots), so this is routed to, and can only
		// be evaluated by, the credential branch -- it never reaches
		// cfg.Verifier at all.
		handler, called, _ := newHandler()
		rec := doRequest(handler, "0000000000000000000000000000000000000000000000000000000000000000")

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, *called, "the tool handler must never be entered on a rejected call (NFR1)")
	})

	t.Run("a whagent credential is shaped so it can only ever route to the whagent branch, never the credential one", func(t *testing.T) {
		token := mintValid(t)
		assert.True(t, isWhagentShapedToken(token), "a minted whagent Claim JWT must always route to the whagent branch")
		assert.NotEqual(t, credentials.validToken, token, "and must never coincide with a live mcpauth credential value")
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

// ── WhagentPersonaMiddleware ──────────────────────────────────────────────────

func TestWhagentPersonaMiddleware_FallsThroughWhenNotWhagentRouted(t *testing.T) {
	cases := []struct {
		name string
		req  mcp.Request
	}{
		{"no Extra at all", requestWithExtra(nil)},
		{"Extra with nil TokenInfo", requestWithExtra(&mcp.RequestExtra{})},
		{"TokenInfo with no whagent claim marker (mcpauth path)", requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{UserID: "swarm-operator-1"}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nextCalled bool
			var gotMethod string
			next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				nextCalled = true
				gotMethod = method
				assert.Equal(t, Persona(""), PersonaFromContext(ctx), "must not resolve a Persona for a non-whagent-routed call")
				return nil, nil
			})

			_, err := WhagentPersonaMiddleware()(next)(context.Background(), "tools/call", tc.req)
			require.NoError(t, err)
			assert.True(t, nextCalled, "a non-whagent-routed call must fall through to next (PersonaMiddleware) unchanged")
			assert.Equal(t, "tools/call", gotMethod)
		})
	}
}

func TestWhagentPersonaMiddleware_ResolvesPersonaAgentUnconditionally(t *testing.T) {
	claim := &whagent.Claim{SubjectIssuer: "https://keycloak.example.test/realms/humans"}
	claim.Subject = "human-1"

	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{whagentClaimExtraKey: claim},
	}})

	var nextCalled bool
	var gotPersona Persona
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		nextCalled = true
		gotPersona = PersonaFromContext(ctx)
		return nil, nil
	})

	_, err := WhagentPersonaMiddleware()(next)(context.Background(), "tools/call", req)
	require.NoError(t, err)
	assert.True(t, nextCalled)
	assert.Equal(t, PersonaAgent, gotPersona, "every whagent-routed call resolves to PersonaAgent unconditionally -- a whagent Claim never carries a human profile")
}
