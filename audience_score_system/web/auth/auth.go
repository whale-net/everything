// Package auth is `web`'s Google OAuth2 sign-in/sign-up flow (C1: FR1,
// FR2) -- GET /login, GET /oauth/google/callback, POST /logout, plus the
// RequireSignedIn middleware that protects every other web route.
//
// libs/go/htmxauth is a *structural* reference, not a drop-in: it is
// generic OIDC-against-Keycloak for signing in to an internal tool, and its
// shared-library home means it must stay ignorant of any one adopter's
// signup semantics. This package borrows its shapes (Config/
// NewAuthenticator, HandleLogin/HandleCallback/HandleLogout, OAuth2 state
// CSRF, a DB-backed SessionManager with AES-256-GCM-encrypted refresh
// tokens and SELECT FOR UPDATE single-flight refresh -- see session.go) but
// copies the mechanics locally rather than adding Google-signup-specific
// branches to the shared package. In particular, unlike htmxauth's
// SetUserInfo (which only records claims), HandleCallback here calls
// store.PersonStore.UpsertByGoogleSubject directly -- Person identity is
// keyed on the Google `sub` claim, never on email, so a changed email never
// forks a Person (FR1/FR2).
//
// Scaffold only (issue #1570): every method below is a stub returning "not
// implemented" (or, for RequireSignedIn, unconditionally redirecting to
// /login) except the plain field-assignment in NewAuthenticator. Real
// Google OIDC discovery, the CSRF state exchange, the ID-token verify +
// UpsertByGoogleSubject call, and session establishment are filled in
// during this issue's Implementation phase.
package auth

import (
	"context"
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Config holds Google OAuth2 + session configuration for the web binary,
// read from ASS_GOOGLE_CLIENT_ID/ASS_GOOGLE_CLIENT_SECRET/
// ASS_OAUTH_REDIRECT_BASE_URL/ASS_SESSION_SECRET/ASS_TOKEN_ENCRYPTION_KEY
// (see ../../ENV.md). Deliberately Google-only -- there is no OIDCIssuer
// field like htmxauth.Config, because this package never talks to any
// provider but Google.
type Config struct {
	// ClientID/ClientSecret are this app's Google OAuth2 client
	// credentials (Google Cloud Console "OAuth client ID").
	ClientID     string
	ClientSecret string

	// RedirectURL is the full callback URL Google redirects back to --
	// ASS_OAUTH_REDIRECT_BASE_URL + "/oauth/google/callback".
	RedirectURL string

	// Scopes defaults to ["openid", "email", "profile"] when empty --
	// identity scopes only. Never YouTube or Analytics scopes: those
	// belong to the separate Channel-connect consent (#1571, FR3), kept
	// deliberately out of this package per this task's Summary.
	Scopes []string

	// SessionName is both the DB session store's row-scoping key and the
	// browser cookie name it derives from that argument (mirrors
	// htmxauth.Config.SessionName / htmxauth.DBSessionManager).
	SessionName string
}

// contextKey namespaces this package's context values so they can't
// collide with keys any other package sets on the same request context.
type contextKey string

const personContextKey contextKey = "audience_score_system/web/auth.person"

// Authenticator drives Google sign-in/sign-up (C1, FR1/FR2): the
// /login -> Google consent -> /oauth/google/callback flow, resolving the
// signed-in identity through persons (store.PersonStore.
// UpsertByGoogleSubject, keyed on Google `sub`) and persisting the result
// through sessions (see session.go).
type Authenticator struct {
	config   Config
	persons  store.PersonStore
	sessions *SessionManager
}

// NewAuthenticator wires config, the Person store, and the session manager
// into an Authenticator.
//
// Scaffold only: this constructor does plain field assignment and does not
// yet perform Google OIDC discovery (the oauth2.Config/oidc.IDTokenVerifier
// construction htmxauth.Authenticator.initOIDC does for Keycloak) -- that
// lands in the Implementation phase alongside HandleLogin/HandleCallback.
func NewAuthenticator(config Config, persons store.PersonStore, sessions *SessionManager) *Authenticator {
	if len(config.Scopes) == 0 {
		config.Scopes = []string{"openid", "email", "profile"}
	}
	return &Authenticator{config: config, persons: persons, sessions: sessions}
}

// HandleLogin redirects to Google's OAuth2 consent screen with scopes
// `openid email profile`, a CSRF state nonce persisted via
// SessionManager.SetOAuthState, and prompt=select_account (FR1/FR2).
//
// Stub only -- filled in during the Implementation phase.
func (a *Authenticator) HandleLogin(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleCallback verifies the OAuth2 state (CSRF), exchanges the
// authorization code, verifies the ID token, extracts the Google `sub`
// claim, and calls store.PersonStore.UpsertByGoogleSubject to resolve the
// signed-in Person -- a first-time sub creates one (FR1), a returning sub
// reuses it (FR2) -- then establishes a session and redirects to `/`.
//
// Stub only -- filled in during the Implementation phase.
func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleLogout clears the caller's session (SessionManager.ClearSession)
// and redirects to `/`.
//
// Stub only -- filled in during the Implementation phase.
func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// RequireSignedIn is middleware that resolves the caller's session cookie
// to a store.Person and places it on the request context (readable via
// PersonFromContext), redirecting to /login when no valid session exists.
//
// Stub only: unconditionally redirects to /login -- filled in during the
// Implementation phase (see this task's Testing section: "RequireSignedIn
// returns 302->/login with no session, 200 with a valid one, and rejects a
// tampered/expired session cookie").
func (a *Authenticator) RequireSignedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// PersonFromContext returns the signed-in store.Person that
// RequireSignedIn placed on ctx, or nil when no session was resolved
// (unauthenticated request, or called outside RequireSignedIn).
func PersonFromContext(ctx context.Context) *store.Person {
	p, _ := ctx.Value(personContextKey).(*store.Person)
	return p
}
