package htmxauth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
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

func TestAuthModeNone_RolesIsAllRoles(t *testing.T) {
	// In AuthModeNone the dev user must carry AllRoles (non-nil) so consuming
	// apps can treat them as holding every role.
	config := Config{
		Mode:          AuthModeNone,
		SessionSecret: "test-secret",
	}
	auth, err := NewAuthenticator(nil, config)
	require.NoError(t, err)

	var capturedUser *UserInfo
	handler := auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = GetUser(r.Context())
	}))

	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, capturedUser)
	// Must be non-nil (not absent)
	assert.NotNil(t, capturedUser.Roles, "AuthModeNone: Roles must be non-nil (all-roles sentinel)")
	// Must be exactly AllRoles
	assert.Equal(t, AllRoles, capturedUser.Roles)
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

// ── parseRealmRoles tests ─────────────────────────────────────────────────────

// Case 1: realm_access absent → nil (not empty slice)
func TestParseRealmRoles_Absent(t *testing.T) {
	claims := map[string]interface{}{
		"sub":   "user123",
		"email": "user@example.com",
	}
	roles, err := parseRealmRoles(claims)
	require.NoError(t, err)
	// CRITICAL: absent must be nil, never collapsed to []
	assert.Nil(t, roles, "absent realm_access must produce nil Roles, not an empty slice")
}

// Case 2: realm_access present with empty roles array → non-nil empty slice
func TestParseRealmRoles_PresentEmpty(t *testing.T) {
	claims := map[string]interface{}{
		"realm_access": map[string]interface{}{
			"roles": []interface{}{},
		},
	}
	roles, err := parseRealmRoles(claims)
	require.NoError(t, err)
	// Must be non-nil (claim was present) but zero length (no roles)
	assert.NotNil(t, roles, "present-but-empty realm_access must produce non-nil Roles")
	assert.Len(t, roles, 0)
}

// Case 3: realm_access present with roles
func TestParseRealmRoles_PresentWithRoles(t *testing.T) {
	claims := map[string]interface{}{
		"realm_access": map[string]interface{}{
			"roles": []interface{}{"admin", "viewer"},
		},
	}
	roles, err := parseRealmRoles(claims)
	require.NoError(t, err)
	assert.NotNil(t, roles)
	assert.Equal(t, []string{"admin", "viewer"}, roles)
}

// Case 4a: realm_access is not an object (malformed)
func TestParseRealmRoles_MalformedNotObject(t *testing.T) {
	claims := map[string]interface{}{
		"realm_access": "not-an-object",
	}
	roles, err := parseRealmRoles(claims)
	assert.Error(t, err, "malformed realm_access must return an error")
	assert.Nil(t, roles)
}

// Case 4b: realm_access.roles is not an array (malformed)
func TestParseRealmRoles_MalformedRolesNotArray(t *testing.T) {
	claims := map[string]interface{}{
		"realm_access": map[string]interface{}{
			"roles": "not-an-array",
		},
	}
	roles, err := parseRealmRoles(claims)
	assert.Error(t, err, "malformed roles field must return an error")
	assert.Nil(t, roles)
}

// Case 4c: realm_access.roles contains a non-string element (malformed)
func TestParseRealmRoles_MalformedRolesNonString(t *testing.T) {
	claims := map[string]interface{}{
		"realm_access": map[string]interface{}{
			"roles": []interface{}{"admin", 42},
		},
	}
	roles, err := parseRealmRoles(claims)
	assert.Error(t, err)
	assert.Nil(t, roles)
}

// ── nil-vs-empty invariant (the regression this work prevents) ────────────────

// Absent and present-empty must be distinguishable by a nil check (never len).
func TestParseRealmRoles_AbsentVsEmpty_Distinguishable(t *testing.T) {
	absentClaims := map[string]interface{}{"sub": "u1"}
	emptyClaims := map[string]interface{}{
		"realm_access": map[string]interface{}{"roles": []interface{}{}},
	}

	absentRoles, err := parseRealmRoles(absentClaims)
	require.NoError(t, err)

	emptyRoles, err := parseRealmRoles(emptyClaims)
	require.NoError(t, err)

	// Both have length 0 — length alone cannot distinguish them.
	assert.Equal(t, 0, len(absentRoles))
	assert.Equal(t, 0, len(emptyRoles))

	// nil check is the only correct way.
	assert.Nil(t, absentRoles, "absent must be nil")
	assert.NotNil(t, emptyRoles, "present-empty must be non-nil")
}

// ── JSONB round-trip: nil vs empty slice ──────────────────────────────────────

// This test exercises the JSON encoding/decoding of userInfoRecord to confirm
// that the nil/non-nil Roles distinction survives a JSONB persist→reload cycle,
// matching what PostgreSQL json.Unmarshal does with missing vs explicit [] keys.
func TestUserInfoRecord_JSONRoundTrip_NilVsEmpty(t *testing.T) {
	t.Run("nil_roles_roundtrip", func(t *testing.T) {
		// A record without roles (e.g., old row or absent claim)
		rec := userInfoRecord{
			Sub:   "u1",
			Roles: nil, // absent
		}
		data, err := json.Marshal(rec)
		require.NoError(t, err)

		var out userInfoRecord
		require.NoError(t, json.Unmarshal(data, &out))
		// nil marshal as null → unmarshal as nil
		assert.Nil(t, out.Roles, "nil Roles must survive JSON round-trip as nil")
	})

	t.Run("empty_roles_roundtrip", func(t *testing.T) {
		rec := userInfoRecord{
			Sub:   "u2",
			Roles: []string{}, // present-but-empty
		}
		data, err := json.Marshal(rec)
		require.NoError(t, err)

		var out userInfoRecord
		require.NoError(t, json.Unmarshal(data, &out))
		// [] marshal as [] → unmarshal as non-nil empty
		assert.NotNil(t, out.Roles, "empty Roles must survive JSON round-trip as non-nil")
		assert.Len(t, out.Roles, 0)
	})

	t.Run("pre_existing_row_no_roles_key", func(t *testing.T) {
		// Simulate an old DB row that has no "roles" key at all.
		// json.Unmarshal must leave Roles as nil (not collapse to empty).
		raw := `{"sub":"u3","preferred_username":"alice","name":"Alice","email":"a@b.com"}`
		var out userInfoRecord
		require.NoError(t, json.Unmarshal([]byte(raw), &out))
		assert.Nil(t, out.Roles, "pre-existing row with no roles key must deserialise Roles as nil")
	})
}

// ── WithAccessToken (hoisted from manmanv2/ui and tools/app_registry/ui,
// convergence spike #998 FR9 point 1) ──────────────────────────────────────

// stubSessionStore is a minimal sessionStore whose GetAccessToken behavior is
// controlled per-test; every other method is unused by these tests.
type stubSessionStore struct {
	token string
	err   error
}

func (s *stubSessionStore) GetUserInfo(r *http.Request) (*UserInfo, error) { return nil, nil }
func (s *stubSessionStore) SetUserInfo(w http.ResponseWriter, r *http.Request, token *oauth2.Token, idToken *oidc.IDToken) error {
	return nil
}
func (s *stubSessionStore) GetAccessToken(r *http.Request) (string, error) {
	return s.token, s.err
}
func (s *stubSessionStore) ClearSession(w http.ResponseWriter, r *http.Request) error { return nil }
func (s *stubSessionStore) SetOAuthState(w http.ResponseWriter, r *http.Request, state string, nextURL string) error {
	return nil
}
func (s *stubSessionStore) VerifyOAuthState(r *http.Request, state string) (bool, error) {
	return false, nil
}
func (s *stubSessionStore) GetNextURL(w http.ResponseWriter, r *http.Request) string { return "/" }

func TestWithAccessToken_AuthModeNone_PassesThroughWithDevToken(t *testing.T) {
	auth, err := NewAuthenticator(nil, Config{Mode: AuthModeNone, SessionSecret: "test-secret"})
	require.NoError(t, err)

	called := false
	handler := auth.WithAccessToken(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/sessions", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	assert.True(t, called, "next handler must be invoked when a token is available")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestWithAccessToken_MissingToken_RedirectsToLogin(t *testing.T) {
	auth := &Authenticator{
		config:   Config{Mode: AuthModeOIDC},
		sessions: &stubSessionStore{err: fmt.Errorf("no access token in session")},
	}

	called := false
	handler := auth.WithAccessToken(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/sessions/42", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	assert.False(t, called, "next handler must not be invoked when the token is missing")
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/auth/login?next=/sessions/42", w.Header().Get("Location"))
}

func TestWithAccessToken_MissingToken_HXRequest_SendsHXRedirect(t *testing.T) {
	auth := &Authenticator{
		config:   Config{Mode: AuthModeOIDC},
		sessions: &stubSessionStore{err: fmt.Errorf("access token expired")},
	}

	called := false
	handler := auth.WithAccessToken(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/sessions/42", nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	handler(w, req)

	assert.False(t, called, "next handler must not be invoked when the token is missing")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "/auth/login?next=/sessions/42", w.Header().Get("HX-Redirect"))
}

