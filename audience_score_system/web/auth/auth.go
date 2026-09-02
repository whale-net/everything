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
package auth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/audience_score_system/store"
)

// googleIssuer is Google's fixed OIDC discovery issuer. Unlike htmxauth's
// Config.OIDCIssuer (configurable, since it targets any Keycloak realm),
// this package only ever talks to Google, so the issuer is a constant
// rather than a Config field.
const googleIssuer = "https://accounts.google.com"

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

// oauth2Exchanger is the subset of *oauth2.Config's behavior HandleLogin/
// HandleCallback depend on. *oauth2.Config satisfies this implicitly;
// tests (this task's Testing phase) can substitute a stub exchanger so no
// handler test makes a live call to Google, per this task's Testing
// section.
type oauth2Exchanger interface {
	AuthCodeURL(state string, opts ...oauth2.AuthCodeOption) string
	Exchange(ctx context.Context, code string, opts ...oauth2.AuthCodeOption) (*oauth2.Token, error)
}

// idTokenVerifier is the subset of *oidc.IDTokenVerifier's behavior
// HandleCallback depends on, factored out for the same stub-in-tests reason
// as oauth2Exchanger above.
type idTokenVerifier interface {
	Verify(ctx context.Context, rawIDToken string) (*oidc.IDToken, error)
}

// Authenticator drives Google sign-in/sign-up (C1, FR1/FR2): the
// /login -> Google consent -> /oauth/google/callback flow, resolving the
// signed-in identity through persons (store.PersonStore.
// UpsertByGoogleSubject, keyed on Google `sub`) and persisting the result
// through sessions (see session.go).
type Authenticator struct {
	config   Config
	persons  store.PersonStore
	sessions *SessionManager

	oauth2Config oauth2Exchanger
	verifier     idTokenVerifier
}

// NewAuthenticator wires config, the Person store, and the session manager
// into an Authenticator, performing Google's OIDC discovery (fetching
// https://accounts.google.com/.well-known/openid-configuration) so
// HandleLogin/HandleCallback have a working oauth2.Config and ID-token
// verifier before the server starts accepting traffic -- mirrors
// htmxauth.Authenticator.initOIDC's boot-time-not-first-request discovery.
func NewAuthenticator(ctx context.Context, config Config, persons store.PersonStore, sessions *SessionManager) (*Authenticator, error) {
	if len(config.Scopes) == 0 {
		config.Scopes = []string{"openid", "email", "profile"}
	}

	provider, err := oidc.NewProvider(ctx, googleIssuer)
	if err != nil {
		return nil, fmt.Errorf("google OIDC discovery failed: %w", err)
	}

	oauth2Config := &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		RedirectURL:  config.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       config.Scopes,
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: config.ClientID})

	return &Authenticator{
		config:       config,
		persons:      persons,
		sessions:     sessions,
		oauth2Config: oauth2Config,
		verifier:     verifier,
	}, nil
}

// HandleLogin redirects to Google's OAuth2 consent screen with scopes
// `openid email profile`, a CSRF state nonce persisted via
// SessionManager.SetOAuthState, and prompt=select_account (FR1/FR2).
func (a *Authenticator) HandleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateState()
	if err != nil {
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}

	nextURL := r.URL.Query().Get("next")
	if nextURL == "" {
		nextURL = "/"
	}
	if err := a.sessions.SetOAuthState(w, r, state, nextURL); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}

	authURL := a.oauth2Config.AuthCodeURL(state, oauth2.SetAuthURLParam("prompt", "select_account"))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// googleIDTokenClaims is the minimal subset of Google's ID token claims
// this flow needs. `sub` is the identity key (FR1/FR2); email/name are
// display-only and are re-synced on every sign-in via UpsertByGoogleSubject
// so a changed Google profile updates the Person without forking it.
type googleIDTokenClaims struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// HandleCallback verifies the OAuth2 state (CSRF), exchanges the
// authorization code, verifies the ID token, extracts the Google `sub`
// claim, and calls store.PersonStore.UpsertByGoogleSubject to resolve the
// signed-in Person -- a first-time sub creates one (FR1), a returning sub
// reuses it (FR2) -- then establishes a session and redirects to the
// post-login target.
func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	state := r.URL.Query().Get("state")
	valid, err := a.sessions.VerifyOAuthState(r, state)
	if err != nil || !valid {
		http.Error(w, "invalid state parameter", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	oauth2Token, err := a.oauth2Config.Exchange(ctx, code)
	if err != nil {
		http.Error(w, "failed to exchange token", http.StatusInternalServerError)
		return
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token in token response", http.StatusInternalServerError)
		return
	}

	idToken, err := a.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		http.Error(w, "failed to verify id token", http.StatusInternalServerError)
		return
	}

	var claims googleIDTokenClaims
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "failed to parse id token claims", http.StatusInternalServerError)
		return
	}
	if claims.Sub == "" {
		http.Error(w, "id token missing sub claim", http.StatusInternalServerError)
		return
	}

	person, _, err := a.persons.UpsertByGoogleSubject(ctx, claims.Sub, claims.Email, claims.Name)
	if err != nil {
		http.Error(w, "failed to resolve person", http.StatusInternalServerError)
		return
	}

	if err := a.sessions.Establish(ctx, w, person.ID.String(), oauth2Token.RefreshToken); err != nil {
		http.Error(w, "failed to establish session", http.StatusInternalServerError)
		return
	}

	nextURL := a.sessions.GetNextURL(w, r)
	http.Redirect(w, r, nextURL, http.StatusSeeOther)
}

// HandleLogout clears the caller's session (SessionManager.ClearSession)
// and redirects to `/`.
func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	// Best-effort: even if ClearSession fails (e.g. the DB row was already
	// gone), the cookie-clearing side effect still runs, and there is
	// nothing more useful to do than redirect home either way.
	_ = a.sessions.ClearSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// NewForTests returns an *Authenticator wired with only persons and
// sessions -- no Google OAuth2/OIDC configuration -- so other web
// sub-packages' tests (e.g. web/invite, web/channel) can exercise
// RequireSignedIn against a real session (real cookie, real store.Person)
// without live Google calls or NewAuthenticator's network OIDC discovery.
// Callers of the returned *Authenticator must never call HandleLogin or
// HandleCallback on it -- those two flows are covered exclusively by this
// package's own tests (auth_test.go/auth_integration_test.go).
func NewForTests(persons store.PersonStore, sessions *SessionManager) *Authenticator {
	return &Authenticator{persons: persons, sessions: sessions}
}

// RequireSignedIn is middleware that resolves the caller's session cookie
// to a store.Person and places it on the request context (readable via
// PersonFromContext), redirecting to /login when no valid session exists
// (no cookie, an unrecognized/tampered session ID, or an expired session).
func (a *Authenticator) RequireSignedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		personIDStr, err := a.sessions.PersonID(r)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		personID, err := uuid.Parse(personIDStr)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		person, err := a.persons.GetByID(r.Context(), personID)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		ctx := context.WithValue(r.Context(), personContextKey, &person)
		next(w, r.WithContext(ctx))
	}
}

// PersonFromContext returns the signed-in store.Person that
// RequireSignedIn placed on ctx, or nil when no session was resolved
// (unauthenticated request, or called outside RequireSignedIn).
func PersonFromContext(ctx context.Context) *store.Person {
	p, _ := ctx.Value(personContextKey).(*store.Person)
	return p
}

// ContextWithPerson returns a copy of ctx carrying p exactly as
// RequireSignedIn would have placed it, so PersonFromContext resolves it.
// This is a TEST-ONLY seam: it lets other packages' handler tests (e.g.
// web/channel's #1571 CanReconnect-gating and connect/reconnect coverage)
// exercise PersonFromContext-reading logic without a real signed-in
// session/DB round trip, mirroring this package's own oauth2Exchanger/
// idTokenVerifier stub-in-tests seams. Production code must never call
// this -- only RequireSignedIn ever populates this context value outside
// tests.
func ContextWithPerson(ctx context.Context, p *store.Person) context.Context {
	return context.WithValue(ctx, personContextKey, p)
}
