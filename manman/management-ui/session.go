package main
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"
)

func init() {
	// Register types for gob encoding
	gob.Register(&oauth2.Token{})
	gob.Register(&oidc.IDToken{})
	gob.Register(map[string]interface{}{})
}

// SessionManager handles user sessions
type SessionManager struct {
	store *sessions.CookieStore
	mu    sync.RWMutex
}

// NewSessionManager creates a new session manager
func NewSessionManager(secret string) *SessionManager {
	// Generate a random authentication key if secret is too short
	var authKey []byte
	if len(secret) < 32 {
		authKey = make([]byte, 32)
		rand.Read(authKey)
	} else {
		authKey = []byte(secret)[:32]
	}

	store := sessions.NewCookieStore(authKey)
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400, // 24 hours
		HttpOnly: true,
		Secure:   false, // Set to true in production with HTTPS
		SameSite: http.SameSiteLaxMode,
	}

	return &SessionManager{
		store: store,
	}
}

// UserInfo holds authenticated user information
type UserInfo struct {
	Sub               string
	PreferredUsername string
	Name              string
	Email             string
	RawClaims         map[string]interface{}
}

// GetSession retrieves a session
func (sm *SessionManager) GetSession(r *http.Request) (*sessions.Session, error) {
	return sm.store.Get(r, "management_ui_session")
}

// GetUserInfo retrieves user info from session
func (sm *SessionManager) GetUserInfo(r *http.Request) (*UserInfo, error) {
	session, err := sm.GetSession(r)
	if err != nil {
		return nil, err
	}

	// Check if user is authenticated
	authenticated, ok := session.Values["authenticated"].(bool)
	if !ok || !authenticated {
		return nil, fmt.Errorf("not authenticated")
	}

	// Extract user info
	sub, _ := session.Values["sub"].(string)
	username, _ := session.Values["preferred_username"].(string)
	name, _ := session.Values["name"].(string)
	email, _ := session.Values["email"].(string)
	claims, _ := session.Values["claims"].(map[string]interface{})

	return &UserInfo{
		Sub:               sub,
		PreferredUsername: username,
		Name:              name,
		Email:             email,
		RawClaims:         claims,
	}, nil
}

// SetUserInfo stores user info in session
func (sm *SessionManager) SetUserInfo(w http.ResponseWriter, r *http.Request, token *oauth2.Token, idToken *oidc.IDToken) error {
	session, err := sm.GetSession(r)
	if err != nil {
		return err
	}

	// Extract claims from ID token
	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return fmt.Errorf("failed to parse claims: %w", err)
	}

	// Store authentication data
	session.Values["authenticated"] = true
	session.Values["token"] = token
	session.Values["id_token"] = idToken
	session.Values["claims"] = claims

	// Extract standard claims
	if sub, ok := claims["sub"].(string); ok {
		session.Values["sub"] = sub
	}
	if username, ok := claims["preferred_username"].(string); ok {
		session.Values["preferred_username"] = username
	}
	if name, ok := claims["name"].(string); ok {
		session.Values["name"] = name
	}
	if email, ok := claims["email"].(string); ok {
		session.Values["email"] = email
	}

	return session.Save(r, w)
}

// ClearSession clears the session
func (sm *SessionManager) ClearSession(w http.ResponseWriter, r *http.Request) error {
	session, err := sm.GetSession(r)
	if err != nil {
		return err
	}

	// Clear all values
	for key := range session.Values {
		delete(session.Values, key)
	}

	session.Options.MaxAge = -1 // Delete cookie
	return session.Save(r, w)
}

// generateState generates a random state string for OAuth2
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// SetOAuthState stores OAuth state in session
func (sm *SessionManager) SetOAuthState(w http.ResponseWriter, r *http.Request, state string, nextURL string) error {
	session, err := sm.GetSession(r)
	if err != nil {
		return err
	}

	session.Values["oauth_state"] = state
	session.Values["oauth_state_created"] = time.Now().Unix()
	if nextURL != "" {
		session.Values["next_url"] = nextURL
	}

	return session.Save(r, w)
}

// VerifyOAuthState verifies the OAuth state parameter
func (sm *SessionManager) VerifyOAuthState(r *http.Request, state string) (bool, error) {
	session, err := sm.GetSession(r)
	if err != nil {
		return false, err
	}

	savedState, ok := session.Values["oauth_state"].(string)
	if !ok || savedState != state {
		return false, nil
	}

	// Check if state is expired (10 minutes)
	created, ok := session.Values["oauth_state_created"].(int64)
	if !ok || time.Now().Unix()-created > 600 {
		return false, nil
	}

	return true, nil
}

// GetNextURL retrieves and clears the next URL from session
func (sm *SessionManager) GetNextURL(w http.ResponseWriter, r *http.Request) string {
	session, err := sm.GetSession(r)
	if err != nil {
		return "/"
	}

	nextURL, ok := session.Values["next_url"].(string)
	if !ok || nextURL == "" {
		return "/"
	}

	// Clear the next URL
	delete(session.Values, "next_url")
	session.Save(r, w)

	return nextURL
}
