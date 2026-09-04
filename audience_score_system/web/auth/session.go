package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/sessions"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionTTL is how long an established sign-in session lives (mirrors
// htmxauth.DBSessionManager.sessionTTL).
const sessionTTL = 24 * time.Hour

// oauthStateTTL is how long an in-flight /login -> callback round trip has
// to complete before its CSRF state is considered expired (mirrors
// htmxauth's 600-second oauth_state window).
const oauthStateTTL = 10 * time.Minute

// SessionManager is a Postgres-backed session store for `web`'s signed-in
// sessions (C1): the browser cookie carries only an opaque session ID
// (HttpOnly, Secure, SameSite=Lax); the resolved Person, and any Google
// refresh token (AES-256-GCM encrypted at rest with encKey, derived from
// ASS_TOKEN_ENCRYPTION_KEY), live server-side in `web_session` (migration
// 003). Mirrors libs/go/htmxauth.DBSessionManager's shape -- an opaque
// session-ID cookie, encrypted refresh tokens, a short-lived signed cookie
// for OAuth2 state -- copied here rather than reused directly per auth.go's
// package doc comment.
type SessionManager struct {
	pool       *pgxpool.Pool
	cookieName string
	encKey     [32]byte

	// oauthStore is a short-lived, signed (not DB-backed) cookie store
	// used only to carry the CSRF state nonce + post-login redirect target
	// across the /login -> Google consent -> /oauth/google/callback round
	// trip -- mirrors htmxauth.DBSessionManager.oauthStore. It is signed
	// with a key derived from sessionSecret (ASS_SESSION_SECRET) passed to
	// NewSessionManager -- deliberately a *different* secret than encKey
	// (ASS_TOKEN_ENCRYPTION_KEY, which only ever encrypts stored refresh
	// tokens) per ../../ENV.md's documented split between the two
	// variables.
	oauthStore *sessions.CookieStore
	oauthName  string
}

// NewSessionManager constructs a SessionManager backed by pool, naming its
// cookie cookieName, signing the short-lived OAuth2 state cookie with a key
// derived from sessionSecret (ASS_SESSION_SECRET), and encrypting any
// stored refresh token with encKey (see auth.Config.SessionName /
// ../../ENV.md's ASS_SESSION_SECRET and ASS_TOKEN_ENCRYPTION_KEY).
func NewSessionManager(pool *pgxpool.Pool, cookieName, sessionSecret string, encKey [32]byte) *SessionManager {
	oauthKey := sha256.Sum256([]byte(sessionSecret))
	oauthStore := sessions.NewCookieStore(oauthKey[:])
	oauthStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   int(oauthStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}

	return &SessionManager{
		pool:       pool,
		cookieName: cookieName,
		encKey:     encKey,
		oauthStore: oauthStore,
		oauthName:  cookieName + "_oauth",
	}
}

// SetOAuthState persists the CSRF state nonce and post-login redirect
// target for the in-flight /login -> Google consent -> callback round trip
// in the short-lived signed oauthStore cookie.
func (s *SessionManager) SetOAuthState(w http.ResponseWriter, r *http.Request, state, nextURL string) error {
	sess, _ := s.oauthStore.Get(r, s.oauthName)
	sess.Values["oauth_state"] = state
	sess.Values["oauth_state_created"] = time.Now().Unix()
	if nextURL != "" {
		sess.Values["next_url"] = nextURL
	}
	return sess.Save(r, w)
}

// VerifyOAuthState validates the state parameter Google echoes back on
// /oauth/google/callback against what SetOAuthState persisted, rejecting a
// mismatched or expired value (CSRF).
func (s *SessionManager) VerifyOAuthState(r *http.Request, state string) (bool, error) {
	sess, err := s.oauthStore.Get(r, s.oauthName)
	if err != nil {
		return false, err
	}
	saved, ok := sess.Values["oauth_state"].(string)
	if !ok || saved == "" || state == "" || saved != state {
		return false, nil
	}
	created, ok := sess.Values["oauth_state_created"].(int64)
	if !ok || time.Now().Unix()-created > int64(oauthStateTTL.Seconds()) {
		return false, nil
	}
	return true, nil
}

// GetNextURL retrieves and clears the post-login redirect target
// SetOAuthState stored, defaulting to "/" if none was set.
func (s *SessionManager) GetNextURL(w http.ResponseWriter, r *http.Request) string {
	sess, err := s.oauthStore.Get(r, s.oauthName)
	if err != nil {
		return "/"
	}
	next, ok := sess.Values["next_url"].(string)
	if !ok || next == "" {
		return "/"
	}
	delete(sess.Values, "next_url")
	_ = sess.Save(r, w)
	return next
}

// Establish creates a new signed-in session row for personID, optionally
// persisting an encrypted Google refresh token (empty refreshToken is
// stored as SQL NULL, never as ciphertext-of-empty-string), and sets the
// session ID cookie (HttpOnly, Secure, SameSite=Lax) on the response.
func (s *SessionManager) Establish(ctx context.Context, w http.ResponseWriter, personID string, refreshToken string) error {
	sessionID, err := generateSessionID()
	if err != nil {
		return fmt.Errorf("generate session id: %w", err)
	}

	var encRefresh *string
	if refreshToken != "" {
		enc, err := s.encryptToken(refreshToken)
		if err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
		encRefresh = &enc
	}

	expiresAt := time.Now().Add(sessionTTL)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO web_session (session_id, person_id, refresh_token, expires_at)
		VALUES ($1, $2, $3, $4)
	`, sessionID, personID, encRefresh, expiresAt); err != nil {
		return fmt.Errorf("persist session: %w", err)
	}

	http.SetCookie(w, s.newSessionCookie(sessionID, int(sessionTTL.Seconds())))
	return nil
}

// PersonID resolves the caller's session cookie to the signed-in Person's
// ID, or an error if there is no cookie, the session ID is unrecognized
// (e.g. tampered), or the session has expired.
func (s *SessionManager) PersonID(r *http.Request) (string, error) {
	sessionID, err := s.sessionID(r)
	if err != nil {
		return "", err
	}

	var personID string
	err = s.pool.QueryRow(r.Context(), `
		SELECT person_id FROM web_session
		WHERE session_id = $1 AND expires_at > NOW()
	`, sessionID).Scan(&personID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("no valid session")
		}
		return "", fmt.Errorf("resolve session: %w", err)
	}
	return personID, nil
}

// ClearSession deletes the session row (if any) and clears the session ID
// cookie on the response.
func (s *SessionManager) ClearSession(w http.ResponseWriter, r *http.Request) error {
	var execErr error
	if sessionID, err := s.sessionID(r); err == nil {
		_, execErr = s.pool.Exec(r.Context(), "DELETE FROM web_session WHERE session_id = $1", sessionID)
	}
	http.SetCookie(w, s.newSessionCookie("", -1))
	return execErr
}

// sessionID reads the session ID cookie's raw value -- no DB lookup, just
// cookie parsing (see PersonID for the DB-backed resolution).
func (s *SessionManager) sessionID(r *http.Request) (string, error) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		return "", fmt.Errorf("no session cookie")
	}
	return cookie.Value, nil
}

// newSessionCookie builds the opaque session-ID cookie: HttpOnly, Secure,
// SameSite=Lax per this task's Validation criteria. A maxAge of -1 (used by
// ClearSession) deletes the cookie.
func (s *SessionManager) newSessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     s.cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

// encryptToken encrypts a token string with AES-256-GCM and returns a
// base64-encoded ciphertext (nonce prepended). Mirrors
// htmxauth.DBSessionManager.encryptToken -- copied per auth.go's package
// doc comment rather than shared, since libs/go/htmxauth must stay ignorant
// of this package's Google-signup semantics.
func (s *SessionManager) encryptToken(plaintext string) (string, error) {
	block, err := aes.NewCipher(s.encKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// generateSessionID returns a random 256-bit hex-encoded opaque session
// identifier (mirrors htmxauth.generateSessionID).
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// generateState returns a random base64url-encoded OAuth2 CSRF state nonce
// (mirrors htmxauth.generateState).
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
