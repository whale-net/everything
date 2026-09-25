// Coverage for the operator identity on this binary's write path: a UI
// write must be rejected outright when no real (iss, sub) resolves, and
// must be attributed to the signed-in operator's real pair when one does.
//
// The present-identity cases drive the genuine path through the shared
// harness (harness_test.go): a real htmxauth.Authenticator in OIDC mode, a
// real authorization-code callback against a fake Keycloak, a real signed
// session cookie, and the real write client against a fake api. No
// database is needed: scope resolution is behind store.ScopeStore, faked
// there.
package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

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
