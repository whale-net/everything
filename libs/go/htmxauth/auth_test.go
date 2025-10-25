package htmxauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthModeNone(t *testing.T) {
	// Create authenticator in no-auth mode
	config := Config{
		Mode:          AuthModeNone,
		SessionSecret: "test-secret",
	}

	auth, err := NewAuthenticator(nil, config)
	require.NoError(t, err)

	// Test that RequireAuth provides a default user
	called := false
	handler := auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		user := GetUser(r.Context())
		assert.NotNil(t, user)
		assert.Equal(t, "dev-user", user.Sub)
		assert.Equal(t, "developer", user.PreferredUsername)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.True(t, called)
}

func TestHandleLoginNoAuth(t *testing.T) {
	config := Config{
		Mode:          AuthModeNone,
		SessionSecret: "test-secret",
	}

	auth, err := NewAuthenticator(nil, config)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()

	auth.HandleLogin(w, req)

	// Should redirect to home
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))
}

func TestHandleLogoutNoAuth(t *testing.T) {
	config := Config{
		Mode:          AuthModeNone,
		SessionSecret: "test-secret",
	}

	auth, err := NewAuthenticator(nil, config)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/auth/logout", nil)
	w := httptest.NewRecorder()

	auth.HandleLogout(w, req)

	// Should redirect to home
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))
}
