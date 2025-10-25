package htmxauth

import (
	"context"
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

// AuthMode defines the authentication mode
type AuthMode string

const (
	AuthModeNone AuthMode = "none" // No authentication required
	AuthModeOIDC AuthMode = "oidc" // OIDC authentication
)

// Config holds authentication configuration
type Config struct {
	Mode AuthMode

	// Session configuration
	SessionSecret string
	SessionName   string

	// OIDC configuration (required if Mode == AuthModeOIDC)
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	OIDCScopes       []string // Defaults to ["openid", "profile", "email"]
}

// UserInfo holds authenticated user information
type UserInfo struct {
	Sub               string
	PreferredUsername string
	Name              string
	Email             string
	RawClaims         map[string]interface{}
}

// Authenticator handles authentication for an application
type Authenticator struct {
	config       Config
	sessions     *SessionManager
	provider     *oidc.Provider
	oauth2Config *oauth2.Config
	verifier     *oidc.IDTokenVerifier
}

// NewAuthenticator creates a new authenticator instance
func NewAuthenticator(ctx context.Context, config Config) (*Authenticator, error) {
	// Set defaults
	if config.SessionName == "" {
		config.SessionName = "htmx_session"
	}
	if len(config.OIDCScopes) == 0 {
		config.OIDCScopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}

	auth := &Authenticator{
		config:   config,
		sessions: NewSessionManager(config.SessionSecret, config.SessionName),
	}

	// Initialize OIDC if required
	if config.Mode == AuthModeOIDC {
		if err := auth.initOIDC(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize OIDC: %w", err)
		}
	}

	return auth, nil
}

// initOIDC initializes OIDC provider and configuration
func (a *Authenticator) initOIDC(ctx context.Context) error {
	if a.config.OIDCIssuer == "" {
		return fmt.Errorf("OIDC_ISSUER is required for OIDC mode")
	}
	if a.config.OIDCClientID == "" {
		return fmt.Errorf("OIDC_CLIENT_ID is required for OIDC mode")
	}
	if a.config.OIDCClientSecret == "" {
		return fmt.Errorf("OIDC_CLIENT_SECRET is required for OIDC mode")
	}
	if a.config.OIDCRedirectURL == "" {
		return fmt.Errorf("OIDC_REDIRECT_URL is required for OIDC mode")
	}

	// Initialize OIDC provider
	provider, err := oidc.NewProvider(ctx, a.config.OIDCIssuer)
	if err != nil {
		return fmt.Errorf("failed to create OIDC provider: %w", err)
	}
	a.provider = provider

	// Configure OAuth2
	a.oauth2Config = &oauth2.Config{
		ClientID:     a.config.OIDCClientID,
		ClientSecret: a.config.OIDCClientSecret,
		RedirectURL:  a.config.OIDCRedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       a.config.OIDCScopes,
	}

	// Create ID token verifier
	a.verifier = provider.Verifier(&oidc.Config{ClientID: a.config.OIDCClientID})

	return nil
}

// RequireAuth is a middleware that requires authentication
func (a *Authenticator) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// In no-auth mode, create a default user
		if a.config.Mode == AuthModeNone {
			user := &UserInfo{
				Sub:               "dev-user",
				PreferredUsername: "developer",
				Name:              "Development User",
				Email:             "dev@localhost",
				RawClaims:         map[string]interface{}{},
			}
			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// OIDC mode - check session
		user, err := a.sessions.GetUserInfo(r)
		if err != nil {
			// User not authenticated, redirect to login
			loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.Path)
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}

		// Add user to context
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAuthFunc wraps a HandlerFunc with authentication
func (a *Authenticator) RequireAuthFunc(next http.HandlerFunc) http.HandlerFunc {
	return a.RequireAuth(http.HandlerFunc(next)).ServeHTTP
}

type contextKey string

const userContextKey contextKey = "user"

// GetUser retrieves user info from request context
func GetUser(ctx context.Context) *UserInfo {
	user, ok := ctx.Value(userContextKey).(*UserInfo)
	if !ok {
		return nil
	}
	return user
}

// HandleLogin initiates the OIDC login flow
func (a *Authenticator) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if a.config.Mode == AuthModeNone {
		// No-auth mode - just redirect to home
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Generate and store state
	state, err := generateState()
	if err != nil {
		http.Error(w, "Failed to generate state", http.StatusInternalServerError)
		return
	}

	// Store state and next URL in session
	nextURL := r.URL.Query().Get("next")
	if nextURL == "" {
		nextURL = "/"
	}
	if err := a.sessions.SetOAuthState(w, r, state, nextURL); err != nil {
		http.Error(w, "Session error", http.StatusInternalServerError)
		return
	}

	// Redirect to authorization URL
	authURL := a.oauth2Config.AuthCodeURL(state)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleCallback handles the OIDC callback
func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if a.config.Mode == AuthModeNone {
		http.Error(w, "Callback not available in no-auth mode", http.StatusNotFound)
		return
	}

	ctx := r.Context()

	// Verify state
	state := r.URL.Query().Get("state")
	valid, err := a.sessions.VerifyOAuthState(r, state)
	if err != nil || !valid {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	// Exchange authorization code for token
	code := r.URL.Query().Get("code")
	oauth2Token, err := a.oauth2Config.Exchange(ctx, code)
	if err != nil {
		http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
		return
	}

	// Extract ID token from OAuth2 token
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "No id_token in token response", http.StatusInternalServerError)
		return
	}

	// Verify ID token
	idToken, err := a.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		http.Error(w, "Failed to verify ID token", http.StatusInternalServerError)
		return
	}

	// Store user info in session
	if err := a.sessions.SetUserInfo(w, r, oauth2Token, idToken); err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Redirect to original page or home
	nextURL := a.sessions.GetNextURL(w, r)
	http.Redirect(w, r, nextURL, http.StatusSeeOther)
}

// HandleLogout logs out the user
func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if a.config.Mode == AuthModeNone {
		// No-auth mode - just redirect to home
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if err := a.sessions.ClearSession(w, r); err != nil {
		// Log error but continue
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// SessionManager handles user sessions
type SessionManager struct {
	store *sessions.CookieStore
	name  string
	mu    sync.RWMutex
}

// NewSessionManager creates a new session manager
func NewSessionManager(secret string, name string) *SessionManager {
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
		name:  name,
	}
}

// GetSession retrieves a session
func (sm *SessionManager) GetSession(r *http.Request) (*sessions.Session, error) {
	return sm.store.Get(r, sm.name)
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
