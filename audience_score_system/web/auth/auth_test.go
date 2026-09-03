// Pure-Go tests for Google OAuth2 sign-in/sign-up (C1, FR1/FR2) -- no
// Docker, no live Google calls, runs as part of `bazel test //...`.
//
// HandleCallback's success path (persons.UpsertByGoogleSubject +
// sessions.Establish) needs a real Postgres row for the session it writes,
// so the "create/reuse Person + session" assertions live in
// auth_integration_test.go (dbtest-backed) instead. What's covered here is
// everything that does NOT require a database round trip: the CSRF state
// check (mismatched/missing/expired state, all resolved from the
// signed-but-not-DB-backed oauth cookie -- see SetOAuthState/
// VerifyOAuthState in session.go), and ID-token claim extraction up to (but
// not through) the point HandleCallback would need a live session row --
// proven by pairing a real *oidc.IDTokenVerifier (constructed with
// oidc.StaticKeySet against a locally-generated, self-signed test key, per
// oidc.NewVerifier's own doc comment "static key set (e.g. for testing)")
// against a fakePersonStore that errors, so the flow never reaches
// sessions.Establish.
package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/audience_score_system/store"
)

const (
	testIssuer      = "https://accounts.google.com"
	testClientID    = "test-client-id"
	testCookieName  = "test_session"
	testOAuthCookie = testCookieName + "_oauth" // mirrors NewSessionManager's cookieName + "_oauth"
)

// testEncKey is a stand-in for the AES-256-GCM key ASS_TOKEN_ENCRYPTION_KEY
// derives (see session.go's NewSessionManager); its value is irrelevant to
// every test in this file since none of them reach Establish's encryption
// path (that requires a real DB row -- see auth_integration_test.go).
func testEncKey() [32]byte {
	return sha256.Sum256([]byte("unit-test-token-encryption-key"))
}

// newTestVerifier returns a real *oidc.IDTokenVerifier backed by
// oidc.StaticKeySet against a freshly generated RSA key, plus that key --
// this is the "faked verifier" the Testing section calls for: it verifies
// real RS256 signatures with a key only this test process knows, so
// signTestIDToken can produce tokens Verify() genuinely accepts or rejects,
// without any network call to Google.
func newTestVerifier(t *testing.T) (*oidc.IDTokenVerifier, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	verifier := oidc.NewVerifier(testIssuer, &oidc.StaticKeySet{
		PublicKeys: []crypto.PublicKey{&key.PublicKey},
	}, &oidc.Config{ClientID: testClientID})
	return verifier, key
}

// signTestIDToken builds and RS256-signs a minimal JWT from claims using
// key, without depending on any JOSE library -- just the encoding stdlib,
// mirroring exactly what Verify() (verify.go, oidc.StaticKeySet) expects to
// parse: base64url(header) + "." + base64url(payload) + "." +
// base64url(signature).
func signTestIDToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	require.NoError(t, err)
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	require.NoError(t, err)

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// baseClaims returns a valid, non-expired claim set for testIssuer/
// testClientID, overridable per test via the sub/email/name arguments.
func baseClaims(sub, email, name string) map[string]any {
	return map[string]any{
		"iss":   testIssuer,
		"aud":   testClientID,
		"sub":   sub,
		"email": email,
		"name":  name,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
}

// ── stub oauth2Exchanger / fake store.PersonStore ──────────────────────────

// stubExchanger is a no-network oauth2Exchanger: AuthCodeURL/Exchange never
// leave the test process, per this task's "no live Google calls in tests"
// requirement.
type stubExchanger struct {
	authURL string
	token   *oauth2.Token
	err     error

	gotState string
	gotCode  string
}

func (s *stubExchanger) AuthCodeURL(state string, _ ...oauth2.AuthCodeOption) string {
	s.gotState = state
	return s.authURL
}

func (s *stubExchanger) Exchange(_ context.Context, code string, _ ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	s.gotCode = code
	return s.token, s.err
}

var _ oauth2Exchanger = (*stubExchanger)(nil)

// fakePersonStore is an in-memory store.PersonStore that records the
// arguments UpsertByGoogleSubject was called with, so tests can assert on
// ID-token claim extraction without a real Person table.
type fakePersonStore struct {
	upsertSub, upsertEmail, upsertName string
	upsertCalled                       bool
	upsertPerson                       store.Person
	upsertCreated                      bool
	upsertErr                          error

	getByIDPerson store.Person
	getByIDErr    error
}

func (f *fakePersonStore) UpsertByGoogleSubject(_ context.Context, sub, email, displayName string) (store.Person, bool, error) {
	f.upsertCalled = true
	f.upsertSub, f.upsertEmail, f.upsertName = sub, email, displayName
	if f.upsertErr != nil {
		return store.Person{}, false, f.upsertErr
	}
	return f.upsertPerson, f.upsertCreated, nil
}

func (f *fakePersonStore) GetByID(_ context.Context, _ uuid.UUID) (store.Person, error) {
	return f.getByIDPerson, f.getByIDErr
}

var _ store.PersonStore = (*fakePersonStore)(nil)

// findCookie fails the test if name is not among cookies.
func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not found among %d cookies", name, len(cookies))
	return nil
}

// ── HandleLogin ─────────────────────────────────────────────────────────────

func TestHandleLogin_RedirectsToGoogleAndSetsStateCookie(t *testing.T) {
	exch := &stubExchanger{authURL: "https://accounts.google.com/o/oauth2/v2/auth?client_id=" + testClientID}
	a := &Authenticator{
		config:       Config{ClientID: testClientID, Scopes: []string{"openid", "email", "profile"}},
		sessions:     NewSessionManager(nil, testCookieName, "session-secret", testEncKey()),
		oauth2Config: exch,
	}

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, exch.authURL, w.Header().Get("Location"))
	assert.NotEmpty(t, exch.gotState, "HandleLogin must pass a CSRF state nonce to AuthCodeURL")

	findCookie(t, w.Result().Cookies(), testOAuthCookie)
}

// ── HandleCallback: CSRF state (unit -- no DB touched on any of these paths) ─

func TestHandleCallback_StateMismatch_Rejected(t *testing.T) {
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{
		config:       Config{ClientID: testClientID},
		sessions:     sessions,
		oauth2Config: &stubExchanger{},
	}

	setupReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	setupW := httptest.NewRecorder()
	require.NoError(t, sessions.SetOAuthState(setupW, setupReq, "known-state", "/"))
	cookie := findCookie(t, setupW.Result().Cookies(), testOAuthCookie)

	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=wrong-state&code=abc", nil)
	cbReq.AddCookie(cookie)
	cbW := httptest.NewRecorder()
	a.HandleCallback(cbW, cbReq)

	assert.Equal(t, http.StatusBadRequest, cbW.Code, "a mismatched state parameter must be rejected (CSRF)")
}

func TestHandleCallback_MissingState_Rejected(t *testing.T) {
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{
		config:       Config{ClientID: testClientID},
		sessions:     sessions,
		oauth2Config: &stubExchanger{},
	}

	// No /login round trip at all -- no oauth state cookie on the request.
	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=whatever&code=abc", nil)
	cbW := httptest.NewRecorder()
	a.HandleCallback(cbW, cbReq)

	assert.Equal(t, http.StatusBadRequest, cbW.Code, "a callback with no stored state must be rejected")
}

func TestHandleCallback_EmptyStateParam_Rejected(t *testing.T) {
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{
		config:       Config{ClientID: testClientID},
		sessions:     sessions,
		oauth2Config: &stubExchanger{},
	}

	setupReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	setupW := httptest.NewRecorder()
	require.NoError(t, sessions.SetOAuthState(setupW, setupReq, "known-state", "/"))
	cookie := findCookie(t, setupW.Result().Cookies(), testOAuthCookie)

	// Google always echoes back a state param; a callback missing it
	// entirely must be rejected the same as a mismatch.
	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?code=abc", nil)
	cbReq.AddCookie(cookie)
	cbW := httptest.NewRecorder()
	a.HandleCallback(cbW, cbReq)

	assert.Equal(t, http.StatusBadRequest, cbW.Code)
}

func TestHandleCallback_ExpiredState_Rejected(t *testing.T) {
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{
		config:       Config{ClientID: testClientID},
		sessions:     sessions,
		oauth2Config: &stubExchanger{},
	}

	// Same-package white-box setup: mirror SetOAuthState's body but
	// back-date oauth_state_created past oauthStateTTL, since the public
	// API has no way to inject an already-expired state.
	setupReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	setupW := httptest.NewRecorder()
	sess, err := sessions.oauthStore.Get(setupReq, sessions.oauthName)
	require.NoError(t, err)
	sess.Values["oauth_state"] = "known-state"
	sess.Values["oauth_state_created"] = time.Now().Add(-(oauthStateTTL + time.Minute)).Unix()
	require.NoError(t, sess.Save(setupReq, setupW))
	cookie := findCookie(t, setupW.Result().Cookies(), testOAuthCookie)

	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=known-state&code=abc", nil)
	cbReq.AddCookie(cookie)
	cbW := httptest.NewRecorder()
	a.HandleCallback(cbW, cbReq)

	assert.Equal(t, http.StatusBadRequest, cbW.Code, "an expired state must be rejected just like a mismatch")
}

// ── HandleCallback: ID-token sub extraction (faked verifier, no DB) ────────

// callbackWithValidState builds a request carrying an oauth state cookie
// whose value matches the "state" query param, so HandleCallback's CSRF
// check passes and execution reaches token exchange / verification.
func callbackWithValidState(t *testing.T, sessions *SessionManager, code string) *http.Request {
	t.Helper()
	setupReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	setupW := httptest.NewRecorder()
	require.NoError(t, sessions.SetOAuthState(setupW, setupReq, "known-state", "/"))
	cookie := findCookie(t, setupW.Result().Cookies(), testOAuthCookie)

	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=known-state&code="+code, nil)
	req.AddCookie(cookie)
	return req
}

func TestHandleCallback_MissingSubClaim_Rejected(t *testing.T) {
	verifier, key := newTestVerifier(t)
	claims := baseClaims("", "a@example.com", "Alice") // sub deliberately empty
	rawToken := signTestIDToken(t, key, claims)

	exch := &stubExchanger{
		token: (&oauth2.Token{AccessToken: "at"}).WithExtra(map[string]any{"id_token": rawToken}),
	}
	persons := &fakePersonStore{}
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{
		config:       Config{ClientID: testClientID},
		persons:      persons,
		sessions:     sessions,
		oauth2Config: exch,
		verifier:     verifier,
	}

	req := callbackWithValidState(t, sessions, "abc123")
	w := httptest.NewRecorder()
	a.HandleCallback(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.False(t, persons.upsertCalled, "must not call UpsertByGoogleSubject when the ID token has no sub claim")
}

func TestHandleCallback_ValidSub_ExtractsClaimsAndCallsUpsert(t *testing.T) {
	verifier, key := newTestVerifier(t)
	claims := baseClaims("google-sub-123", "alice@example.com", "Alice")
	rawToken := signTestIDToken(t, key, claims)

	exch := &stubExchanger{
		token: (&oauth2.Token{AccessToken: "at"}).WithExtra(map[string]any{"id_token": rawToken}),
	}
	// persons.upsertErr set deliberately: this proves HandleCallback
	// extracted the right sub/email/name from the verified ID token and
	// passed them to UpsertByGoogleSubject, without needing to go on to
	// exercise sessions.Establish (which requires a real DB row -- see
	// auth_integration_test.go for the full FR1/FR2 success path).
	persons := &fakePersonStore{upsertErr: assertErr}
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{
		config:       Config{ClientID: testClientID},
		persons:      persons,
		sessions:     sessions,
		oauth2Config: exch,
		verifier:     verifier,
	}

	req := callbackWithValidState(t, sessions, "abc123")
	w := httptest.NewRecorder()
	a.HandleCallback(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code, "the fake store's error must surface as a failure response")
	assert.True(t, persons.upsertCalled)
	assert.Equal(t, "google-sub-123", persons.upsertSub, "must extract the sub claim, not any other field")
	assert.Equal(t, "alice@example.com", persons.upsertEmail)
	assert.Equal(t, "Alice", persons.upsertName)
}

func TestHandleCallback_InvalidSignature_Rejected(t *testing.T) {
	verifier, _ := newTestVerifier(t)
	// Sign with a *different* key than the one verifier trusts -- the
	// signature must not validate.
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rawToken := signTestIDToken(t, otherKey, baseClaims("google-sub-123", "a@example.com", "Alice"))

	exch := &stubExchanger{
		token: (&oauth2.Token{AccessToken: "at"}).WithExtra(map[string]any{"id_token": rawToken}),
	}
	persons := &fakePersonStore{}
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{
		config:       Config{ClientID: testClientID},
		persons:      persons,
		sessions:     sessions,
		oauth2Config: exch,
		verifier:     verifier,
	}

	req := callbackWithValidState(t, sessions, "abc123")
	w := httptest.NewRecorder()
	a.HandleCallback(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.False(t, persons.upsertCalled, "a token with an invalid signature must never reach UpsertByGoogleSubject")
}

func TestHandleCallback_ExchangeError_Rejected(t *testing.T) {
	exch := &stubExchanger{err: assertErr}
	persons := &fakePersonStore{}
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{
		config:       Config{ClientID: testClientID},
		persons:      persons,
		sessions:     sessions,
		oauth2Config: exch,
	}

	req := callbackWithValidState(t, sessions, "abc123")
	w := httptest.NewRecorder()
	a.HandleCallback(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.False(t, persons.upsertCalled)
}

// ── HandleLogout ─────────────────────────────────────────────────────────────

func TestHandleLogout_NoSession_ClearsCookieAndRedirects(t *testing.T) {
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{sessions: sessions}

	// No session cookie on the request: ClearSession's DB delete branch is
	// skipped entirely (sessionID(r) errors before any pool call), so this
	// exercises the no-DB path.
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	w := httptest.NewRecorder()
	a.HandleLogout(w, req)

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))

	cookie := findCookie(t, w.Result().Cookies(), testCookieName)
	assert.Equal(t, -1, cookie.MaxAge, "logout must clear the session cookie")
}

// ── RequireSignedIn: no-cookie path (no DB touched) ─────────────────────────
//
// The "valid session -> 200" and "tampered/expired cookie -> redirect"
// cases both require a real web_session row (or the absence/expiry of one)
// resolved through SessionManager.PersonID's SQL query, so they live in
// auth_integration_test.go. This is the one RequireSignedIn case that never
// reaches the database: PersonID's cookie-parsing step (sessionID(r))
// fails before any query runs.

func TestRequireSignedIn_NoCookie_RedirectsToLogin(t *testing.T) {
	sessions := NewSessionManager(nil, testCookieName, "session-secret", testEncKey())
	a := &Authenticator{persons: &fakePersonStore{}, sessions: sessions}

	called := false
	handler := a.RequireSignedIn(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	assert.False(t, called, "the protected handler must not run without a session")
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
}

// assertErr is a fixed sentinel error used by tests that only need
// UpsertByGoogleSubject/Exchange to fail, not any specific error value.
var assertErr = &sentinelError{"stub error"}

type sentinelError struct{ msg string }

func (e *sentinelError) Error() string { return e.msg }
