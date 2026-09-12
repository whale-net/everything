// Pure-Go, no-Docker coverage for the /mcp/consent flow (issue #2428:
// FR2, FR3, FR5, FR6, FR9, the write half of FR12).
//
// This drives a full DelegatedGrantSource.BeginAuthorization ->
// (simulated browser) -> CompleteAuthorization round trip against
// fakeGrantIdP below -- a local, this-file-only fake authorization server.
// grpcauth/internal/keycloakfake (the library's own equivalent, used by
// libs/go/grpcauth's own tests) is not importable from here: it lives
// under grpcauth/internal, invisible outside that package tree. This is a
// deliberately minimal, functionally equivalent copy -- /authorize
// redirects with code+state, /token mints a signed JWT carrying a
// caller-set `sub` claim -- just enough surface for
// grpcauth.NewDelegatedGrantSource's Endpoints override (bypassing OIDC
// discovery entirely) and grpcauth's own subjectFromAccessToken (which
// parses, but never verifies, the returned access token's `sub` claim).
//
// The FR12 grant-bookkeeping *write* itself is not covered here:
// grantindex.Index is a concrete, pgx-backed type with no in-memory fake
// (unlike grpcauth.Store, which ships grpcauth.FakeStore for exactly this
// reason) -- see handlers_consent_integration_test.go for that half,
// against a real throwaway Postgres.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/whagent_net/delegatedgrant"
)

// testConsentOIDCIssuer is the fixed app.oidcIssuer every test below uses.
// AuthModeNone's dev user (htmxauth.Authenticator.CurrentUser) always
// resolves Sub "dev-user" -- see devUserSub below.
const testConsentOIDCIssuer = "https://keycloak.example.test/realms/whagent"

// devUserSub is exactly what handleMCPConsentConfirm passes as `subject`
// (user.Sub, the raw Keycloak `sub` claim -- never the
// mcpidentity-encoded iss|sub composite, see handlers_consent.go's
// comments and issue #2428 comment
// https://github.com/whale-net/everything/issues/2428#issuecomment-5630520687
// for why: grpcauth.CompleteAuthorization compares this literally against
// the `sub` claim the token exchange actually authenticates, which for
// any real Keycloak realm is always this raw, per-user value) for
// AuthModeNone's fixed dev user -- the value every test below expects
// grpcauth.Store to persist under, and the value handleMCPConsentCallback's
// own bound-to-session check re-derives to compare against
// pending.Subject.
const devUserSub = "dev-user"

// --- fakeGrantIdP: a local, minimal fake authorization server ------------

// fakeGrantIdP fakes just enough of a Keycloak realm's authorization-code +
// PKCE flow for DelegatedGrantSource.BeginAuthorization/CompleteAuthorization
// to round-trip against: GET /authorize redirects to redirect_uri with a
// scripted code+the caller's real state, and POST /token mints a signed JWT
// access token carrying a caller-set `sub` claim plus a scripted
// refresh_token (or none, to exercise ErrAuthorizationNoRefreshToken).
type fakeGrantIdP struct {
	*httptest.Server

	mu sync.Mutex

	subject      string
	refreshToken string
	code         string

	authorizeCalls int
	tokenCalls     int

	signingKey *ecdsa.PrivateKey
}

// newFakeGrantIdP starts a fake authorization server. Defaults: subject
// "fake-subject" (tests that care always call SetSubject explicitly),
// a fixed refresh token, and a fixed authorization code.
func newFakeGrantIdP(t *testing.T) *fakeGrantIdP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	f := &fakeGrantIdP{
		subject:      "fake-subject",
		refreshToken: "fake-refresh-token",
		code:         "fake-authorization-code",
		signingKey:   key,
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.route))
	t.Cleanup(f.Close)
	return f
}

// SetSubject configures the `sub` claim the next (and every subsequent)
// /token response's access token carries.
func (f *fakeGrantIdP) SetSubject(subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subject = subject
}

// SetNoRefreshToken makes /token respond with no refresh_token at all,
// exercising grpcauth.ErrAuthorizationNoRefreshToken.
func (f *fakeGrantIdP) SetNoRefreshToken() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshToken = ""
}

// TokenCalls returns how many times /token has been requested -- tests use
// this to prove a rejected callback (state mismatch, bound-to-session
// mismatch) never even attempts a token exchange.
func (f *fakeGrantIdP) TokenCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls
}

func (f *fakeGrantIdP) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/authorize":
		f.handleAuthorize(w, r)
	case "/token":
		f.handleToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGrantIdP) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.authorizeCalls++
	code := f.code
	f.mu.Unlock()

	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	sep := "?"
	if strings.Contains(redirectURI, "?") {
		sep = "&"
	}
	http.Redirect(w, r, fmt.Sprintf("%s%scode=%s&state=%s", redirectURI, sep, code, state), http.StatusFound)
}

func (f *fakeGrantIdP) handleToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.tokenCalls++
	subject := f.subject
	refreshToken := f.refreshToken
	f.mu.Unlock()

	accessToken, err := f.mintAccessToken(subject)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_in":    300,
	})
}

func (f *fakeGrantIdP) mintAccessToken(sub string) (string, error) {
	now := time.Now()
	claims := map[string]any{
		"iss": f.Server.URL,
		"sub": sub,
		"exp": now.Add(time.Hour).Unix(),
		"iat": now.Add(-time.Minute).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: f.signingKey}, nil)
	if err != nil {
		return "", err
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

// --- test App construction ------------------------------------------------

// newConsentTestApp builds a fully wired *App for the standalone
// /mcp/consent flow: AuthModeNone (so every request is already
// "signed in" as the fixed dev user -- FR6's own no-shortcut guarantee
// doesn't depend on which identity is signed in, only on there being no
// bypass), a real *grpcauth.DelegatedGrantSource pointed at fake via its
// Endpoints override (skips OIDC discovery entirely), and store as its
// Store. Deliberately no mcpProvider: routes are served via consentMux
// below, not app.setupRoutes, so nothing here needs a *mcpauth.Provider at
// all (authorizeConsentGate/mcpauth_test.go's own /authorize concern is
// unrelated to this file).
func newConsentTestApp(t *testing.T, fake *fakeGrantIdP, store grpcauth.Store) *App {
	t.Helper()

	src, err := grpcauth.NewDelegatedGrantSource(context.Background(), grpcauth.DelegatedGrantConfig{
		ClientID:     "test-grant-client",
		ClientSecret: "test-grant-client-secret",
		RedirectURI:  "http://ui.example.test/mcp/consent/callback",
		Store:        store,
		Endpoints: grpcauth.Endpoints{
			Authorization: fake.URL + "/authorize",
			Token:         fake.URL + "/token",
		},
	})
	require.NoError(t, err)

	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "test-secret-that-is-at-least-32-bytes-long",
		SessionName:   "test_consent_ui_session",
	})
	require.NoError(t, err)

	return &App{
		auth:         auth,
		oidcIssuer:   testConsentOIDCIssuer,
		grant:        delegatedgrant.Components{Source: src, Store: store},
		consentStore: newConsentStore("test-secret-that-is-at-least-32-bytes-long"),
	}
}

// consentCookie extracts the pendingConsentCookieName cookie a response set,
// failing the test if it's absent.
func consentCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == pendingConsentCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response (headers: %v)", pendingConsentCookieName, w.Header())
	return nil
}

// --- GET /mcp/consent: scope naming / no shortcuts -----------------------
//
// The unauthenticated-request case
// (TestMCPConsent_UnauthenticatedRequest_RedirectsToSignIn) lives in
// handlers_consent_unauth_test.go, not here: it needs
// newTestOIDCAuthenticator/requestWasAuthBlocked (main_test.go), and this
// file is shared with handlers_consent_integration_test.go (built without
// main_test.go) -- see that file's own doc comment.

// consentMux registers exactly the three /mcp/consent routes setupRoutes
// (main.go) does, wrapped in app.auth.RequireAuthFunc the same way -- built
// by hand here instead of via app.setupRoutes so this file needs no
// *mcpauth.Provider (app.mcpProvider.Mount's own construction is unrelated
// to anything this file -- or handlers_consent_integration_test.go, which
// reuses this same helper -- tests).
func consentMux(app *App) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mcp/consent", app.auth.RequireAuthFunc(app.handleMCPConsent))
	mux.HandleFunc("POST /mcp/consent", app.auth.RequireAuthFunc(app.handleMCPConsentConfirm))
	mux.HandleFunc("GET /mcp/consent/callback", app.auth.RequireAuthFunc(app.handleMCPConsentCallback))
	return mux
}

func TestMCPConsent_RendersScopeName(t *testing.T) {
	fake := newFakeGrantIdP(t)
	app := newConsentTestApp(t, fake, grpcauth.NewFakeStore())
	mux := consentMux(app)

	req := httptest.NewRequest(http.MethodGet, "/mcp/consent?scope=audience_score_system", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "audience_score_system", "the consent page must name the scope (FR2/FR6), sourced from the scope param, not inferred")
}

func TestMCPConsent_InvalidScope_Rejected(t *testing.T) {
	fake := newFakeGrantIdP(t)
	app := newConsentTestApp(t, fake, grpcauth.NewFakeStore())
	mux := consentMux(app)

	for _, scope := range []string{"", "not a valid scope!"} {
		req := httptest.NewRequest(http.MethodGet, "/mcp/consent?scope="+url.QueryEscape(scope), nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code, "scope=%q should be rejected", scope)
	}
}

// TestMCPConsent_ActiveGrantForDifferentScope_StillRequiresConsent is
// issue #2428's Testing section, FR5's exact scenario: an operator who
// already holds an active grant for one scope, targeting a second scope,
// is still shown the full consent step for the second scope -- never
// skipped because *some* scope is already granted.
func TestMCPConsent_ActiveGrantForDifferentScope_StillRequiresConsent(t *testing.T) {
	fake := newFakeGrantIdP(t)
	store := grpcauth.NewFakeStore()
	require.NoError(t, store.Persist(context.Background(), devUserSub, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "already-granted"}))

	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	req := httptest.NewRequest(http.MethodGet, "/mcp/consent?scope=manmanv2", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "manmanv2")
	assert.Contains(t, w.Body.String(), `action="/mcp/consent"`, "the confirm form must still be rendered, not skipped")
}

// TestMCPConsent_ActiveGrantForSameScope_StillRequiresConsent proves FR6's
// "no session shortcut" holds even for the exact scope already granted --
// re-routing an operator here (e.g. FR18's reauth routing, a future task)
// must never silently no-op past this step.
func TestMCPConsent_ActiveGrantForSameScope_StillRequiresConsent(t *testing.T) {
	fake := newFakeGrantIdP(t)
	store := grpcauth.NewFakeStore()
	require.NoError(t, store.Persist(context.Background(), devUserSub, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "already-granted"}))

	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	req := httptest.NewRequest(http.MethodGet, "/mcp/consent?scope=audience_score_system", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `action="/mcp/consent"`, "an already-granted scope must still render the confirm form, not a shortcut")
}

// --- Full BeginAuthorization/CompleteAuthorization round trip ------------

// driveConsentAuthorize performs a real (non-redirect-following) HTTP GET
// of authURL against fake's actual /authorize endpoint, returning the
// code+state its redirect carries -- exercising the real HTTP path rather
// than fabricating a callback.
func driveConsentAuthorize(t *testing.T, authURL string) (code, state string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	return loc.Query().Get("code"), loc.Query().Get("state")
}

// TestConsentRoundTrip_MatchingGrantSubject_PersistsActiveGrant proves the
// whole plumbing -- POST /mcp/consent -> BeginAuthorization -> a real
// browser GET of the returned authURL -> GET /mcp/consent/callback ->
// CompleteAuthorization -> Store.Persist -> redirect to return_to -- works
// end to end when the grant IdP's token response identifies the same
// subject the flow was started for (grpcauth's own documented contract:
// DelegatedGrantSource.BeginAuthorization's `subject` argument must equal
// what the exchange authenticates, see delegatedgrant_authcode.go's
// CompleteAuthorization step 4 and its own test suite's
// fake.SetSubject(...) + BeginAuthorization(ctx, thatSameSubject, ...)
// pairing).
func TestConsentRoundTrip_MatchingGrantSubject_PersistsActiveGrant(t *testing.T) {
	fake := newFakeGrantIdP(t)
	fake.SetSubject(devUserSub)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	// Step 1: POST /mcp/consent confirms consent for audience_score_system.
	confirmReq := httptest.NewRequest(http.MethodPost, "/mcp/consent", strings.NewReader(url.Values{
		"scope":    {"audience_score_system"},
		"return_to": {"/sessions/42"},
	}.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmW := httptest.NewRecorder()
	mux.ServeHTTP(confirmW, confirmReq)

	require.Equal(t, http.StatusFound, confirmW.Code)
	authURL := confirmW.Header().Get("Location")
	require.True(t, strings.HasPrefix(authURL, fake.URL+"/authorize"), "expected a redirect to the grant IdP's authorize endpoint, got %q", authURL)
	cookie := consentCookie(t, confirmW)

	// Step 2: a real browser hitting the grant IdP's /authorize.
	code, state := driveConsentAuthorize(t, authURL)
	require.NotEmpty(t, code)
	require.NotEmpty(t, state)

	// Step 3: GET /mcp/consent/callback, carrying the cookie Step 1 set.
	callbackReq := httptest.NewRequest(http.MethodGet, "/mcp/consent/callback?code="+code+"&state="+state, nil)
	callbackReq.AddCookie(cookie)
	callbackW := httptest.NewRecorder()
	mux.ServeHTTP(callbackW, callbackReq)

	require.Equal(t, http.StatusFound, callbackW.Code, "body: %s", callbackW.Body.String())
	assert.Equal(t, "/sessions/42", callbackW.Header().Get("Location"), "must return to return_to on success")

	status, err := store.Status(context.Background(), devUserSub, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, status)
}

// TestConsentRoundTrip_RealisticCrossClientSubject_MustSucceed is issue
// #2428's Testing section "full BeginAuthorization/CompleteAuthorization
// round-trip coverage" bullet, modeling what a *real* Keycloak realm
// actually does rather than what a permissive fake can be told to do: a
// user's `sub` claim is a stable per-user value tied to the realm, not the
// requesting OAuth2 client (https://www.keycloak.org -- `sub` identifies
// the user, never the client) -- so the shared delegated-grant confidential
// client (WHAGENT_GRANT_CLIENT_ID) and `ui`'s own sign-in client
// (WHAGENT_OIDC_CLIENT_ID) issue the *same* `sub` for the same
// already-signed-in operator. That per-user raw value is exactly
// AuthModeNone's fixed dev user's Sub, devUserSub ("dev-user").
//
// This test previously failed (see issue #2428 comment
// https://github.com/whale-net/everything/issues/2428#issuecomment-5630520687):
// handleMCPConsentConfirm used to pass mcpidentity.Encode(app.oidcIssuer,
// user.Sub) -- an iss|sub composite, meant for the unrelated
// mcpauth.CredentialStore identity format (whagent_net/mcpidentity's own
// package doc) -- as BeginAuthorization's `subject` argument. No real
// Keycloak realm ever mints a `sub` claim equal to that composite for an
// ordinary user login, so CompleteAuthorization's identity check
// (delegatedgrant_authcode.go step 4: the exchanged token's real `sub`
// claim must equal pending.Subject) could never succeed outside this
// test's own permissive fake. The fix: handleMCPConsentConfirm/Callback
// now pass the raw user.Sub -- see handlers_consent.go's comments on the
// same lines.
func TestConsentRoundTrip_RealisticCrossClientSubject_MustSucceed(t *testing.T) {
	fake := newFakeGrantIdP(t)
	fake.SetSubject(devUserSub) // the real, raw Keycloak sub AuthModeNone's dev user carries -- same value every client in the realm would see for this user.
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	confirmReq := httptest.NewRequest(http.MethodPost, "/mcp/consent", strings.NewReader(url.Values{
		"scope":    {"audience_score_system"},
		"return_to": {"/sessions/42"},
	}.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmW := httptest.NewRecorder()
	mux.ServeHTTP(confirmW, confirmReq)
	require.Equal(t, http.StatusFound, confirmW.Code)
	authURL := confirmW.Header().Get("Location")
	cookie := consentCookie(t, confirmW)

	code, state := driveConsentAuthorize(t, authURL)

	callbackReq := httptest.NewRequest(http.MethodGet, "/mcp/consent/callback?code="+code+"&state="+state, nil)
	callbackReq.AddCookie(cookie)
	callbackW := httptest.NewRecorder()
	mux.ServeHTTP(callbackW, callbackReq)

	require.Equal(t, http.StatusFound, callbackW.Code,
		"consent must succeed for the operator's own real (same-realm, cross-client) sub, not just when the grant IdP happens to echo back the mcpidentity-encoded composite verbatim; got body: %s", callbackW.Body.String())
	assert.Equal(t, "/sessions/42", callbackW.Header().Get("Location"))

	status, err := store.Status(context.Background(), devUserSub, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, status)
}

// --- Per-scope scoping (FR3) ---------------------------------------------

func TestConsentRoundTrip_ConsentForOneScope_LeavesOtherScopeAbsent(t *testing.T) {
	fake := newFakeGrantIdP(t)
	fake.SetSubject(devUserSub)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	confirmReq := httptest.NewRequest(http.MethodPost, "/mcp/consent", strings.NewReader(url.Values{
		"scope": {"audience_score_system"},
	}.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmW := httptest.NewRecorder()
	mux.ServeHTTP(confirmW, confirmReq)
	require.Equal(t, http.StatusFound, confirmW.Code)
	cookie := consentCookie(t, confirmW)
	code, state := driveConsentAuthorize(t, confirmW.Header().Get("Location"))

	callbackReq := httptest.NewRequest(http.MethodGet, "/mcp/consent/callback?code="+code+"&state="+state, nil)
	callbackReq.AddCookie(cookie)
	callbackW := httptest.NewRecorder()
	mux.ServeHTTP(callbackW, callbackReq)
	require.Equal(t, http.StatusFound, callbackW.Code)

	assert.Equal(t, grpcauth.FakeStoreCalls{Persist: 1}, store.Calls(), "exactly one grant must be created")

	assStatus, err := store.Status(context.Background(), devUserSub, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, assStatus)

	_, err = store.Status(context.Background(), devUserSub, "manmanv2")
	assert.ErrorIs(t, err, grpcauth.ErrGrantNotFound, "consenting to one scope must not create a grant for any other scope")
}

// --- Callback failure paths ------------------------------------------------

func TestConsentCallback_StateMismatch_PersistsNothing(t *testing.T) {
	fake := newFakeGrantIdP(t)
	fake.SetSubject(devUserSub)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	confirmReq := httptest.NewRequest(http.MethodPost, "/mcp/consent", strings.NewReader(url.Values{
		"scope": {"audience_score_system"},
	}.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmW := httptest.NewRecorder()
	mux.ServeHTTP(confirmW, confirmReq)
	require.Equal(t, http.StatusFound, confirmW.Code)
	cookie := consentCookie(t, confirmW)
	code, _ := driveConsentAuthorize(t, confirmW.Header().Get("Location"))

	callbackReq := httptest.NewRequest(http.MethodGet, "/mcp/consent/callback?code="+code+"&state=not-the-real-state", nil)
	callbackReq.AddCookie(cookie)
	callbackW := httptest.NewRecorder()
	mux.ServeHTTP(callbackW, callbackReq)

	assert.NotEqual(t, http.StatusFound, callbackW.Code, "a state mismatch must not complete the flow")
	assert.Equal(t, 0, fake.TokenCalls(), "a state mismatch must be rejected before any token exchange is attempted")
	assert.Equal(t, grpcauth.FakeStoreCalls{}, store.Calls(), "nothing must be persisted")
}

func TestConsentCallback_KeycloakErrorResponse_PersistsNothing(t *testing.T) {
	fake := newFakeGrantIdP(t)
	fake.SetSubject(devUserSub)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	confirmReq := httptest.NewRequest(http.MethodPost, "/mcp/consent", strings.NewReader(url.Values{
		"scope": {"audience_score_system"},
	}.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmW := httptest.NewRecorder()
	mux.ServeHTTP(confirmW, confirmReq)
	require.Equal(t, http.StatusFound, confirmW.Code)
	cookie := consentCookie(t, confirmW)

	// The operator declined at Keycloak: no code, an error param instead --
	// never even reaching driveConsentAuthorize's real /authorize hit.
	callbackReq := httptest.NewRequest(http.MethodGet, "/mcp/consent/callback?error=access_denied", nil)
	callbackReq.AddCookie(cookie)
	callbackW := httptest.NewRecorder()
	mux.ServeHTTP(callbackW, callbackReq)

	assert.NotEqual(t, http.StatusFound, callbackW.Code, "a declined/errored consent must not complete the flow")
	assert.Equal(t, 0, fake.TokenCalls(), "an error callback must never attempt a token exchange")
	assert.Equal(t, grpcauth.FakeStoreCalls{}, store.Calls(), "nothing must be persisted")
	assert.Contains(t, callbackW.Body.String(), "not completed", "operator must see a clear failure page")
}

func TestConsentCallback_NoPendingCookie_RendersFailureWithoutCrashing(t *testing.T) {
	fake := newFakeGrantIdP(t)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	req := httptest.NewRequest(http.MethodGet, "/mcp/consent/callback?code=whatever&state=whatever", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusFound, w.Code)
	assert.Equal(t, 0, fake.TokenCalls())
	assert.Equal(t, grpcauth.FakeStoreCalls{}, store.Calls())
}

// TestConsentCallback_SubjectDoesNotMatchSignedInSession_Rejected covers
// the Open Question note's bound-to-session requirement: a pending
// authorization only ever completes under the exact ui session that
// started it. This crafts a pendingConsent cookie for a *different*
// subject than the one currently signed in (AuthModeNone's fixed dev
// user) directly via app.savePendingConsent, standing in for a stolen or
// cross-session-replayed cookie.
func TestConsentCallback_SubjectDoesNotMatchSignedInSession_Rejected(t *testing.T) {
	fake := newFakeGrantIdP(t)
	store := grpcauth.NewFakeStore()
	app := newConsentTestApp(t, fake, store)
	mux := consentMux(app)

	saveReq := httptest.NewRequest(http.MethodGet, "/", nil)
	saveW := httptest.NewRecorder()
	require.NoError(t, app.savePendingConsent(saveW, saveReq, pendingConsent{
		PendingAuthorization: grpcauth.PendingAuthorization{
			Subject:      "some-other-operators-subject",
			Grant:        "audience_score_system",
			State:        "irrelevant-state",
			CodeVerifier: "irrelevant-verifier",
			CreatedAt:    time.Now(),
		},
		Scope:   "audience_score_system",
		ReturnTo: "/sessions/42",
	}))
	cookie := consentCookie(t, saveW)

	callbackReq := httptest.NewRequest(http.MethodGet, "/mcp/consent/callback?code=whatever&state=irrelevant-state", nil)
	callbackReq.AddCookie(cookie)
	callbackW := httptest.NewRecorder()
	mux.ServeHTTP(callbackW, callbackReq)

	assert.NotEqual(t, http.StatusFound, callbackW.Code)
	assert.Equal(t, 0, fake.TokenCalls(), "a cross-session pending authorization must be rejected before any token exchange")
	assert.Equal(t, grpcauth.FakeStoreCalls{}, store.Calls())
	assert.Contains(t, callbackW.Body.String(), "does not belong to your signed-in session")
}
