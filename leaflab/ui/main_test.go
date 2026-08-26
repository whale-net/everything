package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// ── Unit tests for LoadConfig ──────────────────────────────────────────────

func TestLoadConfig_DefaultValues(t *testing.T) {
	// Clear environment
	for _, key := range []string{"HOST", "PORT", "AUTH_MODE", "OIDC_ISSUER", "OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URI", "OIDC_POST_LOGOUT_REDIRECT_URI", "SECRET_KEY",
		"LEAFLAB_API_URL", "GRPC_AUTH_MODE", "PG_DATABASE_URL"} {
		t.Setenv(key, "")
	}

	config := LoadConfig()

	assert.Equal(t, "0.0.0.0", config.Host)
	assert.Equal(t, "8000", config.Port)
	assert.Equal(t, "none", config.AuthMode)
	assert.Equal(t, "dev-secret-key-change-in-production", config.SessionSecret)
	assert.Equal(t, "leaflab-api:50051", config.LeafLabAPIURL)
	assert.Equal(t, "none", config.GRPCAuthMode)
}

func TestLoadConfig_CustomValues(t *testing.T) {
	t.Setenv("HOST", "localhost")
	t.Setenv("PORT", "9000")
	t.Setenv("AUTH_MODE", "oidc")
	t.Setenv("OIDC_ISSUER", "https://idp.example.com")
	t.Setenv("OIDC_CLIENT_ID", "test-client")
	t.Setenv("PG_DATABASE_URL", "postgresql://localhost/test")

	config := LoadConfig()

	assert.Equal(t, "localhost", config.Host)
	assert.Equal(t, "9000", config.Port)
	assert.Equal(t, "oidc", config.AuthMode)
	assert.Equal(t, "https://idp.example.com", config.OIDCIssuer)
	assert.Equal(t, "test-client", config.OIDCClientID)
	assert.Equal(t, "postgresql://localhost/test", config.DatabaseURL)
}

// ── Unit tests for NewApp ──────────────────────────────────────────────────

func TestNewApp_NoDatabaseURL_Fails(t *testing.T) {
	t.Setenv("PG_DATABASE_URL", "")
	config := LoadConfig()

	app, err := NewApp(context.Background(), config)

	require.Error(t, err)
	assert.Nil(t, app)
	assert.Contains(t, err.Error(), "PG_DATABASE_URL is required")
}

func TestNewApp_InvalidAuthMode_Fails(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	config := &Config{
		AuthMode:      "invalid",
		SessionSecret: "test-secret-key-at-least-32-bytes-long",
		LeafLabAPIURL: "localhost:50051",
		DatabaseURL:   testDB.ConnString,
	}

	app, err := NewApp(ctx, config)

	require.Error(t, err)
	assert.Nil(t, app)
	assert.Contains(t, err.Error(), "invalid AUTH_MODE")
}

// ── Integration tests for auth flow ────────────────────────────────────────

const uiSessionsSchema = `
	CREATE TABLE ui_sessions (
		session_id TEXT PRIMARY KEY,
		user_info JSONB NOT NULL DEFAULT '{}',
		access_token TEXT NOT NULL,
		refresh_token TEXT NOT NULL,
		token_expires_at TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at TIMESTAMPTZ NOT NULL
	);
	CREATE INDEX idx_ui_sessions_expires_at ON ui_sessions(expires_at);
`

// generateTestSessionID generates a random session ID for testing.
func generateTestSessionID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// createTestApp creates a test app with only the auth part initialized.
func createTestApp(ctx context.Context, t testing.TB, testDB *dbtest.Postgres) *App {
	t.Helper()

	authConfig := htmxauth.Config{
		Mode:            htmxauth.AuthModeNone,
		SessionSecret:   "test-secret-key-at-least-32-bytes-long",
		SessionName:     "leaflab_ui_session",
	}

	store, err := htmxauth.NewDBSessionManager(ctx, testDB.Pool, authConfig.SessionSecret, authConfig.SessionName)
	require.NoError(t, err)

	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, authConfig, store)
	require.NoError(t, err)

	return &App{
		config: &Config{
			AuthMode:      "none",
			SessionSecret: "test-secret-key-at-least-32-bytes-long",
			DatabaseURL:   testDB.ConnString,
		},
		auth:      auth,
		apiClient: nil,
	}
}

// TestAuthFlow_SessionPersistsAcrossRequests verifies that a session cookie
// can be replayed after a simulated browser restart and still authenticate the user.
func TestAuthFlow_SessionPersistsAcrossRequests(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	app := createTestApp(ctx, t, testDB)

	sessionID := generateTestSessionID()

	userInfo := map[string]interface{}{
		"sub":                  "test-user",
		"preferred_username":   "testuser",
		"name":                 "Test User",
		"email":                "test@example.com",
		"realm_access":         map[string]interface{}{"roles": []interface{}{"user"}},
	}
	userInfoJSON, err := json.Marshal(userInfo)
	require.NoError(t, err)

	expiresAt := time.Now().Add(24 * time.Hour)
	tokenExpiresAt := time.Now().Add(1 * time.Hour)

	_, err = testDB.Pool.Exec(ctx, `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, userInfoJSON, "test-access-token", "test-refresh-token", tokenExpiresAt, expiresAt)
	require.NoError(t, err)

	// First request with the session cookie
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  "leaflab_ui_session",
		Value: sessionID,
	})

	w := httptest.NewRecorder()
	handler := app.auth.RequireAuthFunc(app.handleDashboard)
	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "first request with valid session should succeed")
	assert.Contains(t, w.Body.String(), "Dashboard", "dashboard should be rendered")

	// Simulate browser restart: same session cookie in a new request
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(&http.Cookie{
		Name:  "leaflab_ui_session",
		Value: sessionID,
	})

	w2 := httptest.NewRecorder()
	handler(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code, "replayed session cookie should still authenticate")
	assert.Contains(t, w2.Body.String(), "Dashboard", "dashboard should still render after browser restart")
}

// TestAuthFlow_SessionQueryFromDB verifies that sessions can be retrieved from the database.
func TestAuthFlow_SessionQueryFromDB(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	sessionID := generateTestSessionID()

	userInfo := map[string]interface{}{
		"sub":                  "test-user",
		"preferred_username":   "testuser",
		"name":                 "Test User",
		"email":                "test@example.com",
		"realm_access":         map[string]interface{}{"roles": []interface{}{"user"}},
	}
	userInfoJSON, err := json.Marshal(userInfo)
	require.NoError(t, err)

	expiresAt := time.Now().Add(24 * time.Hour)
	tokenExpiresAt := time.Now().Add(1 * time.Hour)

	_, err = testDB.Pool.Exec(ctx, `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, userInfoJSON, "test-access-token", "test-refresh-token", tokenExpiresAt, expiresAt)
	require.NoError(t, err)

	// Verify we can retrieve the session from the database
	var retrievedUserInfo string
	err = testDB.Pool.QueryRow(ctx, `
		SELECT user_info FROM ui_sessions WHERE session_id = $1
	`, sessionID).Scan(&retrievedUserInfo)
	require.NoError(t, err)

	var retrieved map[string]interface{}
	err = json.Unmarshal([]byte(retrievedUserInfo), &retrieved)
	require.NoError(t, err)

	assert.Equal(t, "test-user", retrieved["sub"])
	assert.Equal(t, "testuser", retrieved["preferred_username"])
}

// TestAuthFlow_SignOut_ClearsSessionCookie verifies that sign-out clears the cookie.
func TestAuthFlow_SignOut_ClearsSessionCookie(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	app := createTestApp(ctx, t, testDB)

	sessionID := generateTestSessionID()

	userInfo := map[string]interface{}{
		"sub": "test-user",
	}
	userInfoJSON, err := json.Marshal(userInfo)
	require.NoError(t, err)

	expiresAt := time.Now().Add(24 * time.Hour)
	tokenExpiresAt := time.Now().Add(1 * time.Hour)

	_, err = testDB.Pool.Exec(ctx, `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, userInfoJSON, "test-access-token", "test-refresh-token", tokenExpiresAt, expiresAt)
	require.NoError(t, err)

	// Call logout handler
	req := httptest.NewRequest("GET", "/auth/logout", nil)
	req.AddCookie(&http.Cookie{
		Name:  "leaflab_ui_session",
		Value: sessionID,
	})

	w := httptest.NewRecorder()
	app.auth.HandleLogout(w, req)

	// Verify logout redirects
	assert.Equal(t, http.StatusSeeOther, w.Code, "logout should redirect")
	assert.NotEmpty(t, w.Header().Get("Location"), "logout should set location header")
}

// TestAuthFlow_AuthModeNone_DevUser verifies that in AuthModeNone, requests
// automatically receive a dev user without explicit login.
func TestAuthFlow_AuthModeNone_DevUser(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	app := createTestApp(ctx, t, testDB)

	// In AuthModeNone, a request without session should still authenticate
	req := httptest.NewRequest("GET", "/", nil)

	var capturedUser *htmxauth.UserInfo
	handler := app.auth.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = htmxauth.GetUser(r.Context())
	})

	w := httptest.NewRecorder()
	handler(w, req)

	assert.NotNil(t, capturedUser, "AuthModeNone should inject a dev user")
	assert.Equal(t, "dev-user", capturedUser.Sub, "dev user subject should be 'dev-user'")
}

// TestDashboard_SignOutLinkPresent verifies that the sign-out link is
// present on the dashboard and plainly worded.
func TestDashboard_SignOutLinkPresent(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	app := createTestApp(ctx, t, testDB)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	// Since AuthModeNone injects a dev user, this should render the dashboard
	handler := app.auth.RequireAuthFunc(app.handleDashboard)
	handler(w, req)

	body := w.Body.String()
	assert.Contains(t, body, "/auth/logout", "dashboard should contain logout link")
	assert.Contains(t, body, "Logout", "logout link should be plainly worded")
}

// TestHandleHealth_ReturnsStatusOK tests health endpoint without auth
func TestHandleHealth_ReturnsStatusOK(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	app := createTestApp(ctx, t, testDB)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	app.handleHealth(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Equal(t, "ok", response["status"])
}

// TestDashboard_UserLabelDisplayed verifies that the dashboard displays the user's label.
func TestDashboard_UserLabelDisplayed(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	app := createTestApp(ctx, t, testDB)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler := app.auth.RequireAuthFunc(app.handleDashboard)
	handler(w, req)

	body := w.Body.String()
	// In AuthModeNone, user label defaults to "User" (no preferred_username)
	assert.Contains(t, body, "developer", "dashboard should display user label (dev user in AuthModeNone)")
	assert.Contains(t, body, "Sensor Board Management", "dashboard should have main title")
}
