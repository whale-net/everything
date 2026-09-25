// The write-path test harness, shared by every test target that drives a
// real signed-in operator's write against a real krill api stand-in.
//
// It lives in its own file (not in writes_test.go) because a go_test target
// compiles only its own srcs: both ui_test (writes_test.go) and
// design_write_test (design_write_test.go) list this file so the two prove
// the same round-trip through one harness rather than two copies of it.
//
// The pieces are a real htmxauth.Authenticator in OIDC mode driven through
// the genuine authorization-code callback against a fake Keycloak, and a
// fake krill api recording what the real write client actually sends. No
// database: scope resolution is behind store.ScopeStore, faked here.
package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

const (
	testSessionSecret = "krill-ui-test-session-secret-32-bytes-min"
	testSessionName   = "krill_ui_session"
	testClientID      = "krill-ui-test-client"
	testClientSecret  = "krill-ui-test-client-secret"
	testRedirectURL   = "http://krill-ui.test/auth/callback"
	testOperatorSub   = "3fa85f64-5717-4562-b3fc-2c963f66afa6"
)

// testScopeID is the id the fake scope store reports; krill seeds exactly
// one scope row, so a UI write is always minted under it.
var testScopeID = uuid.MustParse("11111111-2222-3333-4444-555555555555")

// ---------------------------------------------------------------------------
// fake Keycloak
// ---------------------------------------------------------------------------

// fakeIDP is the minimum a real Keycloak must serve for htmxauth to
// complete an authorization-code sign-in: OIDC discovery, a token endpoint
// that returns an RS256 id_token, and the JWKS go-oidc verifies it
// against. sub is the subject every id_token it mints carries -- i.e. the
// operator whose identity the UI must end up attributing writes to.
type fakeIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	sub    string
}

func newFakeIDP(t *testing.T, sub string) *fakeIDP {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	idp := &fakeIDP{key: key, kid: "krill-ui-test-key", sub: sub}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		writeTestJSON(w, http.StatusOK, map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/auth",
			"token_endpoint":                        base + "/token",
			"jwks_uri":                              base + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		pub := idp.key.PublicKey
		writeTestJSON(w, http.StatusOK, map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"kid": idp.kid,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if got := r.FormValue("client_id"); got != testClientID {
			http.Error(w, "unexpected client_id "+got, http.StatusBadRequest)
			return
		}
		idToken, err := idp.signIDToken("http://"+r.Host, sub)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{
			"access_token": "krill-ui-test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// signIDToken mints an RS256 JWT carrying sub as its subject, exactly the
// claim htmxauth copies into the session's UserInfo.Sub.
func (idp *fakeIDP) signIDToken(issuer, sub string) (string, error) {
	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": idp.kid})
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}
	claims, err := json.Marshal(map[string]any{
		"iss":                issuer,
		"sub":                sub,
		"aud":                testClientID,
		"iat":                time.Now().Add(-time.Minute).Unix(),
		"exp":                time.Now().Add(time.Hour).Unix(),
		"preferred_username": "operator",
		"name":               "Test Operator",
		"email":              "operator@example.com",
	})
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign id_token: %w", err)
	}
	return signingInput + "." + enc.EncodeToString(signature), nil
}

// newSignedInOperator returns a real OIDC-mode Authenticator plus the
// session cookie a real browser would hold after a real sign-in: it runs
// HandleCallback against idp, then keeps the cookie htmxauth's own
// CurrentUser resolves sub from.
func newSignedInOperator(t *testing.T, idp *fakeIDP) (*htmxauth.Authenticator, *http.Cookie) {
	t.Helper()

	authenticator, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:             htmxauth.AuthModeOIDC,
		SessionSecret:    testSessionSecret,
		SessionName:      testSessionName,
		OIDCIssuer:       idp.server.URL,
		OIDCClientID:     testClientID,
		OIDCClientSecret: testClientSecret,
		OIDCRedirectURL:  testRedirectURL,
	})
	require.NoError(t, err)

	// The state cookie HandleLogin would have set before redirecting to
	// the IdP, minted by a SessionManager with the same secret and name.
	states := htmxauth.NewSessionManager(testSessionSecret, testSessionName)
	stateRec := httptest.NewRecorder()
	require.NoError(t, states.SetOAuthState(stateRec, httptest.NewRequest(http.MethodGet, "/login", nil), "test-state", "/"))

	callback := httptest.NewRequest(http.MethodGet, "/auth/callback?state=test-state&code=test-code", nil)
	for _, c := range stateRec.Result().Cookies() {
		callback.AddCookie(c)
	}
	callbackRec := httptest.NewRecorder()
	authenticator.HandleCallback(callbackRec, callback)
	require.Equal(t, http.StatusSeeOther, callbackRec.Code, "sign-in callback failed: %s", callbackRec.Body.String())

	// Pick the cookie htmxauth itself resolves our subject from, rather
	// than assuming which Set-Cookie in the response is the session.
	for _, c := range callbackRec.Result().Cookies() {
		if c.Name != testSessionName {
			continue
		}
		probe := httptest.NewRequest(http.MethodGet, "/", nil)
		probe.AddCookie(c)
		if user, err := authenticator.CurrentUser(probe); err == nil && user.Sub == idp.sub {
			return authenticator, c
		}
	}
	t.Fatalf("no session cookie resolving sub %q: callback body %s", idp.sub, callbackRec.Body.String())
	return nil, nil
}

// ---------------------------------------------------------------------------
// fake krill api
// ---------------------------------------------------------------------------

// recordedRequest is one request the write client made of krill api.
type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// apiResponder overrides the fake api's canned replies: it receives each
// recorded request and returns the status and body to reply with, or
// (0, "") to fall through to the canned reply for that path.
type apiResponder func(req recordedRequest) (int, string)

// fakeAPI stands in for krill api's HTTP surface, recording everything the
// UI sends so a test can assert both the identity a session was minted
// under and the header the write itself carried.
type fakeAPI struct {
	server    *httptest.Server
	sessionID string

	// createdSessionID is the id the canned POST /design-sessions reply
	// hands back, so a test can assert the redirect lands on it.
	createdSessionID string

	// rejectStatus, when non-zero, makes every non-init, non-design-session
	// request (i.e. a task write) answer with that status and rejectBody
	// instead of 200 -- standing in for a real api refusal (stale claim,
	// already-cancelled task) so a route's rejection page can be exercised.
	// Zero (the default) keeps the 200 success path the other tests rely on.
	rejectStatus int
	rejectBody   string

	mu       sync.Mutex
	requests []recordedRequest
	respond  apiResponder
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()

	api := &fakeAPI{sessionID: uuid.NewString(), createdSessionID: uuid.NewString()}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		recorded := recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   body,
		}
		api.mu.Lock()
		api.requests = append(api.requests, recorded)
		responder := api.respond
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if responder != nil {
			if status, payload := responder(recorded); status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, payload)
				return
			}
		}
		switch r.URL.Path {
		case "/sessions/init":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"session_id":%q,"scope_id":%q}`, api.sessionID, testScopeID)
		case "/design-sessions":
			// api's real status for an opened design session, so the
			// relay of a non-200 success is covered too.
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%q}`, api.createdSessionID)
		default:
			api.mu.Lock()
			rejStatus, rejBody := api.rejectStatus, api.rejectBody
			api.mu.Unlock()
			if rejStatus != 0 {
				w.WriteHeader(rejStatus)
				fmt.Fprint(w, rejBody)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"status":"ok"}`)
		}
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeAPI) recorded() []recordedRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]recordedRequest(nil), a.requests...)
}

// rejectWrite makes every subsequent task write answer with status and body,
// standing in for an api refusal. Zero status restores the default 200.
func (a *fakeAPI) rejectWrite(status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rejectStatus = status
	a.rejectBody = body
}

// onRequest installs the responder that overrides the canned replies.
func (a *fakeAPI) onRequest(f apiResponder) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.respond = f
}

// initRequest returns the recorded POST /sessions/init body, failing if
// no such request was made.
func (a *fakeAPI) initRequest(t *testing.T) initSessionRequest {
	t.Helper()

	for _, req := range a.recorded() {
		if req.Path == "/sessions/init" {
			var parsed initSessionRequest
			require.NoError(t, json.Unmarshal(req.Body, &parsed), "init body: %s", req.Body)
			return parsed
		}
	}
	t.Fatalf("no POST /sessions/init reached api; recorded: %+v", a.recorded())
	return initSessionRequest{}
}

// writeRequest returns the one recorded request that is not the session
// init -- i.e. the write under test -- failing if there is not exactly one.
func (a *fakeAPI) writeRequest(t *testing.T) recordedRequest {
	t.Helper()

	var writes []recordedRequest
	for _, req := range a.recorded() {
		if req.Path != "/sessions/init" {
			writes = append(writes, req)
		}
	}
	require.Len(t, writes, 1, "exactly one write must reach api; recorded: %+v", a.recorded())
	return writes[0]
}

// ---------------------------------------------------------------------------
// wiring
// ---------------------------------------------------------------------------

// fakeScopeStore is the one scope row krill's seeder guarantees.
type fakeScopeStore struct{ scope store.Scope }

func (f fakeScopeStore) GetByID(context.Context, uuid.UUID) (store.Scope, error) { return f.scope, nil }
func (f fakeScopeStore) GetSole(context.Context) (store.Scope, error)            { return f.scope, nil }

func newTestApp(t *testing.T, authenticator *htmxauth.Authenticator, issuer, apiURL string) *App {
	t.Helper()

	writes, err := newWriteClient(writeClientConfig{BaseURL: apiURL})
	require.NoError(t, err)

	return &App{
		auth:       authenticator,
		oidcIssuer: issuer,
		writes:     writes,
		scopes:     fakeScopeStore{scope: store.Scope{ID: testScopeID}},
	}
}

// serveWithCookie issues one request through mux with the operator's
// session cookie attached, the way the browser would.
func serveWithCookie(mux *http.ServeMux, method, target, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// assertOperatorAttribution asserts the recorded init request minted the
// krill session under the operator's real (iss, sub) as BOTH subjects --
// the one assertion every UI write's attribution reduces to.
func assertOperatorAttribution(t *testing.T, api *fakeAPI, issuer string) {
	t.Helper()

	init := api.initRequest(t)
	assert.Equal(t, issuer, init.Acting.Iss, "acting issuer must be the configured Keycloak realm")
	assert.Equal(t, testOperatorSub, init.Acting.Sub, "acting sub must be the signed-in operator's subject")
	assert.Equal(t, string(store.SubjectKindHuman), init.Acting.Kind)
	assert.Equal(t, init.Acting, init.OnBehalfOf, "a signed-in operator acts for themselves")
	assert.Equal(t, testScopeID.String(), init.ScopeID)
}

// assertFreshOperatorAttribution is assertOperatorAttribution for a test
// that minted its own subject: iss and sub are the values that test's fake
// IdP signs with, sub being a uuid.NewString() minted per test so the
// assertion can never be satisfied by a hardcoded identity.
func assertFreshOperatorAttribution(t *testing.T, api *fakeAPI, iss, sub string) {
	t.Helper()

	init := api.initRequest(t)
	assert.Equal(t, iss, init.Acting.Iss, "acting issuer must be the fake IdP's real issuer")
	assert.Equal(t, sub, init.Acting.Sub, "acting sub must be the operator who just signed in")
	assert.Equal(t, string(store.SubjectKindHuman), init.Acting.Kind)
	assert.Equal(t, init.Acting, init.OnBehalfOf, "a signed-in operator acts for themselves")
	assert.Equal(t, testScopeID.String(), init.ScopeID)
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
