// Coverage for the operator identity on this binary's write path: a UI
// write must be rejected outright when no real (iss, sub) resolves, and
// must be attributed to the signed-in operator's real pair when one does.
//
// The present-identity cases drive the genuine path -- a real
// htmxauth.Authenticator in OIDC mode, a real authorization-code callback
// against a fake Keycloak, a real signed session cookie -- so what is
// asserted is what the operator's identity resolution actually produces,
// not a stub's. The krill api side is an httptest server standing in for
// the two calls that matter: POST /sessions/init (what identity the write
// is minted under) and the write itself (that it carries the minted
// session's header). No database is needed: scope resolution is behind
// store.ScopeStore, faked here.
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

// fakeAPI stands in for krill api's HTTP surface, recording everything the
// UI sends so a test can assert both the identity a session was minted
// under and the header the write itself carried.
type fakeAPI struct {
	server    *httptest.Server
	sessionID string

	mu       sync.Mutex
	requests []recordedRequest
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()

	api := &fakeAPI{sessionID: uuid.NewString()}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		api.mu.Lock()
		api.requests = append(api.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   body,
		})
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sessions/init":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"session_id":%q,"scope_id":%q}`, api.sessionID, testScopeID)
		case "/design-sessions":
			// api's real status for an opened design session, so the
			// relay of a non-200 success is covered too.
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"`+uuid.NewString()+`"}`)
		default:
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

// ---------------------------------------------------------------------------
// present identity: attributed correctly
// ---------------------------------------------------------------------------

// TestUIWrite_Escalate_AttributedToSignedInOperator is the task
// intervention half of the NFR: a task escalation submitted from the
// signed-in operator's page is minted as, and carried by, a krill session
// whose acting and on-behalf-of subjects are that operator's real
// (iss, sub) -- never anything the request supplied.
func TestUIWrite_Escalate_AttributedToSignedInOperator(t *testing.T) {
	idp := newFakeIDP(t, testOperatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks/{id}/escalate", app.operatorRoute(app.handleEscalateTask))

	taskID := uuid.NewString()
	rec := serveWithCookie(mux, http.MethodPost, "/tasks/"+taskID+"/escalate",
		`{"reason":"blocked on an upstream dependency"}`, sessionCookie)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assertOperatorAttribution(t, api, idp.server.URL)

	recorded := api.recorded()
	require.Len(t, recorded, 2, "exactly one init and one write")
	assert.Equal(t, http.MethodPost, recorded[1].Method)
	assert.Equal(t, "/tasks/"+taskID+"/escalate", recorded[1].Path)
	assert.Equal(t, api.sessionID, recorded[1].Header.Get(sessionHeader),
		"the write must carry the session id init minted under the operator's identity")
	assert.JSONEq(t, `{"reason":"blocked on an upstream dependency"}`, string(recorded[1].Body))
}

// TestUIWrite_OpenDesignSession_AttributedToSignedInOperator is the
// design-session submission half of the NFR, and also covers a non-200
// success being relayed to the browser unchanged.
func TestUIWrite_OpenDesignSession_AttributedToSignedInOperator(t *testing.T) {
	idp := newFakeIDP(t, testOperatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /design-sessions", app.operatorRoute(app.handleOpenDesignSession))

	productID := uuid.NewString()
	rec := serveWithCookie(mux, http.MethodPost, "/design-sessions",
		fmt.Sprintf(`{"product_id":%q,"opening_submission":"the milestone needs a rollback story"}`, productID),
		sessionCookie)
	require.Equal(t, http.StatusCreated, rec.Code, "api's 201 must be relayed, not flattened: %s", rec.Body.String())

	assertOperatorAttribution(t, api, idp.server.URL)

	recorded := api.recorded()
	require.Len(t, recorded, 2)
	assert.Equal(t, "/design-sessions", recorded[1].Path)
	assert.Equal(t, api.sessionID, recorded[1].Header.Get(sessionHeader))
	assert.JSONEq(t,
		fmt.Sprintf(`{"product_id":%q,"opening_submission":"the milestone needs a rollback story"}`, productID),
		string(recorded[1].Body))
}

// ---------------------------------------------------------------------------
// missing identity: rejected, never written
// ---------------------------------------------------------------------------

// TestUIWrite_AuthModeNone_Rejected covers the dev-only-identity case the
// NFR names outright: with no Keycloak configured, htmxauth hands every
// request its fixed dev user, but with no issuer there is no real (iss,
// sub) to encode -- so the write is rejected and nothing reaches api.
func TestUIWrite_AuthModeNone_Rejected(t *testing.T) {
	authenticator, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: testSessionSecret,
		SessionName:   testSessionName,
	})
	require.NoError(t, err)

	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, "" /* no OIDC issuer configured */, api.server.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks/{id}/escalate", app.operatorRoute(app.handleEscalateTask))
	mux.HandleFunc("POST /design-sessions", app.operatorRoute(app.handleOpenDesignSession))

	escalate := serveWithCookie(mux, http.MethodPost, "/tasks/"+uuid.NewString()+"/escalate", `{}`)
	assert.Equal(t, http.StatusUnauthorized, escalate.Code)
	assert.Contains(t, escalate.Body.String(), "unresolved operator identity")

	open := serveWithCookie(mux, http.MethodPost, "/design-sessions", `{"product_id":"x","opening_submission":"y"}`)
	assert.Equal(t, http.StatusUnauthorized, open.Code)

	assert.Empty(t, api.recorded(), "a write with no real identity must never reach krill")
}

// TestUIWrite_NoSession_Rejected covers a browser that simply is not
// signed in: the request is turned away at the sign-in redirect, still
// before any krill call.
func TestUIWrite_NoSession_Rejected(t *testing.T) {
	idp := newFakeIDP(t, testOperatorSub)
	authenticator, _ := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks/{id}/escalate", app.operatorRoute(app.handleEscalateTask))

	rec := serveWithCookie(mux, http.MethodPost, "/tasks/"+uuid.NewString()+"/escalate", `{}`)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Empty(t, api.recorded(), "an unauthenticated write must never reach krill")
}

// TestUIWrite_UnknownSessionCookie_Rejected covers a tampered or expired
// session cookie: htmxauth cannot resolve a user, so no Subject exists
// and the write is rejected exactly as the unsigned case is.
func TestUIWrite_UnknownSessionCookie_Rejected(t *testing.T) {
	idp := newFakeIDP(t, testOperatorSub)
	authenticator, _ := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks/{id}/escalate", app.operatorRoute(app.handleEscalateTask))

	forged := &http.Cookie{Name: testSessionName, Value: "not-a-real-session"}
	rec := serveWithCookie(mux, http.MethodPost, "/tasks/"+uuid.NewString()+"/escalate", `{}`, forged)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Empty(t, api.recorded(), "a forged session cookie must never reach krill")
}

// TestUIWrite_RequestBodyCannotSpoofIdentity proves the browser has no
// field it could put an identity in: an extra acting/on_behalf_of pair is
// a 400 before a session is minted, not a silently-ignored or honoured
// override.
func TestUIWrite_RequestBodyCannotSpoofIdentity(t *testing.T) {
	idp := newFakeIDP(t, testOperatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks/{id}/escalate", app.operatorRoute(app.handleEscalateTask))

	body := `{"reason":"x","acting":{"iss":"https://evil.example","sub":"attacker","kind":"service"}}`
	rec := serveWithCookie(mux, http.MethodPost, "/tasks/"+uuid.NewString()+"/escalate", body, sessionCookie)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, api.recorded(), "a body carrying an identity must never reach krill")
}

// TestWithKrillSession_NoOperatorSubject_Errors is the innermost guard:
// withKrillSession is the only way a handler reaches api, so a request
// that somehow skipped requireOperator (no Subject on its context) must
// stop there rather than mint a session from nothing.
func TestWithKrillSession_NoOperatorSubject_Errors(t *testing.T) {
	api := newFakeAPI(t)
	app := newTestApp(t, nil, "https://keycloak.example.com/realms/krill", api.server.URL)

	called := false
	err := app.withKrillSession(context.Background(), func(context.Context, store.SessionID) error {
		called = true
		return nil
	})
	assert.ErrorIs(t, err, errNoOperator)
	assert.False(t, called, "the write callback must not run without an operator")
	assert.Empty(t, api.recorded())
}

// TestNewWriteClient_RequiresBaseURL keeps the UI from defaulting a write
// target it was never configured with.
func TestNewWriteClient_RequiresBaseURL(t *testing.T) {
	_, err := newWriteClient(writeClientConfig{})
	assert.Error(t, err)

	_, err = newWriteClient(writeClientConfig{BaseURL: "not-a-url"})
	assert.Error(t, err)
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
