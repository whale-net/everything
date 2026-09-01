package auth

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionManager is a Postgres-backed session store for `web`'s signed-in
// sessions (C1): the browser cookie carries only an opaque session ID
// (HttpOnly, Secure, SameSite=Lax); the resolved Person, and any Google
// refresh token (AES-256-GCM encrypted at rest with encKey, derived from
// ASS_TOKEN_ENCRYPTION_KEY), live server-side. Mirrors
// libs/go/htmxauth.DBSessionManager's shape -- SELECT FOR UPDATE
// single-flight refresh, encrypted refresh tokens, a short-lived signed
// cookie for OAuth2 state -- copied here rather than reused directly per
// auth.go's package doc comment.
//
// Scaffold only: the backing table is added by this issue's Implementation
// phase (migration 003_web_session, see this task's Implementation
// section) -- every method below is a stub until then.
type SessionManager struct {
	pool       *pgxpool.Pool
	cookieName string
	encKey     [32]byte
}

// NewSessionManager constructs a SessionManager backed by pool, naming its
// cookie cookieName and encrypting any stored refresh token with encKey
// (see auth.Config.SessionName / ../../ENV.md's ASS_TOKEN_ENCRYPTION_KEY).
//
// Scaffold only: does not yet probe for the session table -- that
// preflight (mirroring htmxauth.NewDBSessionManager's boot-time check)
// lands with migration 003 in the Implementation phase.
func NewSessionManager(pool *pgxpool.Pool, cookieName string, encKey [32]byte) *SessionManager {
	return &SessionManager{pool: pool, cookieName: cookieName, encKey: encKey}
}

// SetOAuthState persists the CSRF state nonce and post-login redirect
// target for the in-flight /login -> Google consent -> callback round trip.
//
// Stub only -- filled in during the Implementation phase.
func (s *SessionManager) SetOAuthState(w http.ResponseWriter, r *http.Request, state, nextURL string) error {
	return errNotImplemented
}

// VerifyOAuthState validates the state parameter Google echoes back on
// /oauth/google/callback against what SetOAuthState persisted, rejecting a
// mismatched or expired value (CSRF).
//
// Stub only -- filled in during the Implementation phase.
func (s *SessionManager) VerifyOAuthState(r *http.Request, state string) (bool, error) {
	return false, errNotImplemented
}

// GetNextURL retrieves and clears the post-login redirect target
// SetOAuthState stored.
//
// Stub only -- filled in during the Implementation phase.
func (s *SessionManager) GetNextURL(w http.ResponseWriter, r *http.Request) string {
	return "/"
}

// Establish creates a new signed-in session row for personID, optionally
// persisting an encrypted Google refresh token, and sets the session ID
// cookie on the response.
//
// Stub only -- filled in during the Implementation phase.
func (s *SessionManager) Establish(ctx context.Context, w http.ResponseWriter, personID string, refreshToken string) error {
	return errNotImplemented
}

// PersonID resolves the caller's session cookie to the signed-in Person's
// ID, or an error if there is no valid, unexpired session.
//
// Stub only -- filled in during the Implementation phase.
func (s *SessionManager) PersonID(r *http.Request) (string, error) {
	return "", errNotImplemented
}

// ClearSession deletes the session row (if any) and clears the session ID
// cookie on the response.
//
// Stub only -- filled in during the Implementation phase.
func (s *SessionManager) ClearSession(w http.ResponseWriter, r *http.Request) error {
	return errNotImplemented
}

var errNotImplemented = notImplementedError{}

// notImplementedError is a trivial sentinel distinct from errors.New so
// every stub above returns the exact same comparable value.
type notImplementedError struct{}

func (notImplementedError) Error() string { return "not implemented" }
