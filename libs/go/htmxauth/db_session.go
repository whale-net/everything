package htmxauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

// DBSessionManager is a session store backed by PostgreSQL.
// The browser cookie contains only an opaque random session ID.
// User info, access tokens, and encrypted refresh tokens are stored in ui_sessions.
// Access tokens are refreshed automatically when they near expiry.
type DBSessionManager struct {
	pool       *pgxpool.Pool
	oauthStore *sessions.CookieStore // gorilla store for short-lived OAuth state only
	oauthName  string                // session name for OAuth state cookie
	cookieName string                // cookie name for the session ID
	encKey     [32]byte              // AES-256-GCM key derived from SECRET_KEY
	sessionTTL time.Duration
	oauth2Cfg  *oauth2.Config // set by NewAuthenticatorWithDB after OIDC init
}

// NewDBSessionManager creates a DB-backed session manager.
// secret must be the same SECRET_KEY used by the application.
// Call this before NewAuthenticatorWithDB.
// A background cleanup goroutine is started on ctx to prune expired sessions hourly.
func NewDBSessionManager(ctx context.Context, pool *pgxpool.Pool, secret, name string) *DBSessionManager {
	key := sha256.Sum256([]byte(secret))

	oauthStore := sessions.NewCookieStore(key[:])
	oauthStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   600, // 10 minutes — only needed during the login flow
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	}

	sm := &DBSessionManager{
		pool:       pool,
		oauthStore: oauthStore,
		oauthName:  name + "_oauth",
		cookieName: name,
		encKey:     key,
		sessionTTL: 24 * time.Hour,
	}

	go sm.cleanupLoop(ctx)
	return sm
}

// userInfoRecord mirrors what we store as JSONB in the DB.
type userInfoRecord struct {
	Sub               string `json:"sub"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Email             string `json:"email"`
}

// ── sessionStore interface ────────────────────────────────────────────────────

// SetUserInfo stores the token and user claims in the database and sets a
// session ID cookie on the response.
func (s *DBSessionManager) SetUserInfo(w http.ResponseWriter, r *http.Request, token *oauth2.Token, idToken *oidc.IDToken) error {
	var rawClaims map[string]interface{}
	if err := idToken.Claims(&rawClaims); err != nil {
		return fmt.Errorf("failed to parse ID token claims: %w", err)
	}

	rec := userInfoRecord{
		Sub:               stringClaim(rawClaims, "sub"),
		PreferredUsername: stringClaim(rawClaims, "preferred_username"),
		Name:              stringClaim(rawClaims, "name"),
		Email:             stringClaim(rawClaims, "email"),
	}
	userInfoJSON, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("failed to marshal user info: %w", err)
	}

	encRefresh, err := s.encryptToken(token.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to encrypt refresh token: %w", err)
	}

	sessionID, err := generateSessionID()
	if err != nil {
		return fmt.Errorf("failed to generate session ID: %w", err)
	}

	tokenExpiry := token.Expiry
	if tokenExpiry.IsZero() {
		tokenExpiry = time.Now().Add(5 * time.Minute)
	}

	_, err = s.pool.Exec(r.Context(), `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, userInfoJSON, token.AccessToken, encRefresh, tokenExpiry, time.Now().Add(s.sessionTTL))
	if err != nil {
		return fmt.Errorf("failed to persist session: %w", err)
	}

	http.SetCookie(w, s.newSessionCookie(sessionID, int(s.sessionTTL.Seconds())))
	return nil
}

// GetUserInfo retrieves user info from the database using the session ID cookie.
func (s *DBSessionManager) GetUserInfo(r *http.Request) (*UserInfo, error) {
	sessionID, err := s.sessionID(r)
	if err != nil {
		return nil, err
	}

	var raw []byte
	err = s.pool.QueryRow(r.Context(), `
		SELECT user_info FROM ui_sessions
		WHERE session_id = $1 AND expires_at > NOW()
	`, sessionID).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("not authenticated")
	}

	var rec userInfoRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("failed to parse user info: %w", err)
	}

	return &UserInfo{
		Sub:               rec.Sub,
		PreferredUsername: rec.PreferredUsername,
		Name:              rec.Name,
		Email:             rec.Email,
		RawClaims:         map[string]interface{}{},
	}, nil
}

// GetAccessToken returns a valid access token, refreshing it automatically if
// it is within 2 minutes of expiry. Uses SELECT FOR UPDATE so concurrent
// requests for the same session don't double-refresh.
func (s *DBSessionManager) GetAccessToken(r *http.Request) (string, error) {
	sessionID, err := s.sessionID(r)
	if err != nil {
		return "", err
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var accessToken, encRefresh string
	var tokenExpiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT access_token, refresh_token, token_expires_at
		FROM ui_sessions
		WHERE session_id = $1 AND expires_at > NOW()
		FOR UPDATE
	`, sessionID).Scan(&accessToken, &encRefresh, &tokenExpiresAt)
	if err != nil {
		return "", fmt.Errorf("session not found or expired")
	}

	// Token still has plenty of life left — no refresh needed.
	if time.Until(tokenExpiresAt) > 2*time.Minute {
		tx.Rollback(ctx)
		return accessToken, nil
	}

	// Token is stale; refresh it.
	if s.oauth2Cfg == nil {
		return "", fmt.Errorf("oauth2 config unavailable for token refresh")
	}

	refreshToken, err := s.decryptToken(encRefresh)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	ts := s.oauth2Cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	newToken, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("failed to refresh access token: %w", err)
	}

	newEncRefresh, err := s.encryptToken(newToken.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new refresh token: %w", err)
	}

	newExpiry := newToken.Expiry
	if newExpiry.IsZero() {
		newExpiry = time.Now().Add(5 * time.Minute)
	}

	_, err = tx.Exec(ctx, `
		UPDATE ui_sessions
		SET access_token = $1, refresh_token = $2, token_expires_at = $3
		WHERE session_id = $4
	`, newToken.AccessToken, newEncRefresh, newExpiry, sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to update tokens: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("failed to commit token refresh: %w", err)
	}

	return newToken.AccessToken, nil
}

// ClearSession deletes the database row and clears the session ID cookie.
func (s *DBSessionManager) ClearSession(w http.ResponseWriter, r *http.Request) error {
	if sessionID, err := s.sessionID(r); err == nil {
		s.pool.Exec(r.Context(), "DELETE FROM ui_sessions WHERE session_id = $1", sessionID)
	}
	http.SetCookie(w, s.newSessionCookie("", -1))
	return nil
}

// SetOAuthState stores the OAuth2 state and next URL in a short-lived cookie.
func (s *DBSessionManager) SetOAuthState(w http.ResponseWriter, r *http.Request, state string, nextURL string) error {
	sess, _ := s.oauthStore.Get(r, s.oauthName)
	sess.Values["oauth_state"] = state
	sess.Values["oauth_state_created"] = time.Now().Unix()
	if nextURL != "" {
		sess.Values["next_url"] = nextURL
	}
	return sess.Save(r, w)
}

// VerifyOAuthState validates the OAuth2 state parameter.
func (s *DBSessionManager) VerifyOAuthState(r *http.Request, state string) (bool, error) {
	sess, err := s.oauthStore.Get(r, s.oauthName)
	if err != nil {
		return false, err
	}
	saved, ok := sess.Values["oauth_state"].(string)
	if !ok || saved != state {
		return false, nil
	}
	created, ok := sess.Values["oauth_state_created"].(int64)
	if !ok || time.Now().Unix()-created > 600 {
		return false, nil
	}
	return true, nil
}

// GetNextURL retrieves and clears the post-login redirect URL.
func (s *DBSessionManager) GetNextURL(w http.ResponseWriter, r *http.Request) string {
	sess, err := s.oauthStore.Get(r, s.oauthName)
	if err != nil {
		return "/"
	}
	next, ok := sess.Values["next_url"].(string)
	if !ok || next == "" {
		return "/"
	}
	delete(sess.Values, "next_url")
	sess.Save(r, w)
	return next
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (s *DBSessionManager) sessionID(r *http.Request) (string, error) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		return "", fmt.Errorf("no session cookie")
	}
	return cookie.Value, nil
}

func (s *DBSessionManager) newSessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     s.cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	}
}

// encryptToken encrypts a token string with AES-256-GCM and returns a
// base64-encoded ciphertext (nonce prepended).
func (s *DBSessionManager) encryptToken(plaintext string) (string, error) {
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

// decryptToken reverses encryptToken.
func (s *DBSessionManager) decryptToken(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.encKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func stringClaim(claims map[string]interface{}, key string) string {
	v, _ := claims[key].(string)
	return v
}

// cleanupLoop deletes expired sessions hourly until ctx is cancelled.
func (s *DBSessionManager) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pool.Exec(ctx, "DELETE FROM ui_sessions WHERE expires_at < NOW()")
		}
	}
}
