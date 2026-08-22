package htmxauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// ── HandleLogout: RP-initiated logout (issue #763) ─────────────────────────────

// When the OIDC provider advertises an end_session_endpoint, HandleLogout
// must clear the local session AND redirect to the provider's RP-initiated
// logout endpoint (client_id + post_logout_redirect_uri) — not just redirect
// home — so the upstream SSO session ends too.
func TestHandleLogout_OIDC_RPInitiatedLogout_RedirectsToEndSessionEndpoint(t *testing.T) {
	config := Config{
		Mode:            AuthModeOIDC,
		SessionSecret:   "test-secret-that-is-at-least-32-bytes-long",
		OIDCClientID:    "app-registry-ui",
		OIDCRedirectURL: "https://app-registry.example.com/auth/callback",
	}
	auth := &Authenticator{
		config:             config,
		sessions:           NewSessionManager(config.SessionSecret, "test_session"),
		endSessionEndpoint: "https://keycloak.example.com/realms/r/protocol/openid-connect/logout",
	}

	req := httptest.NewRequest("GET", "/auth/logout", nil)
	w := httptest.NewRecorder()

	auth.HandleLogout(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "keycloak.example.com", loc.Host)
	assert.Equal(t, "/realms/r/protocol/openid-connect/logout", loc.Path)
	assert.Equal(t, "app-registry-ui", loc.Query().Get("client_id"))
	assert.Equal(t, "https://app-registry.example.com/", loc.Query().Get("post_logout_redirect_uri"))
}

// A provider that does not advertise end_session_endpoint (empty string,
// the zero value before/without discovery) must degrade to the old
// local-only redirect — never send the browser to an empty URL.
func TestHandleLogout_OIDC_NoEndSessionEndpoint_FallsBackLocalOnly(t *testing.T) {
	config := Config{
		Mode:          AuthModeOIDC,
		SessionSecret: "test-secret-that-is-at-least-32-bytes-long",
	}
	auth := &Authenticator{
		config:   config,
		sessions: NewSessionManager(config.SessionSecret, "test_session"),
		// endSessionEndpoint intentionally left unset.
	}

	req := httptest.NewRequest("GET", "/auth/logout", nil)
	w := httptest.NewRecorder()

	auth.HandleLogout(w, req)

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))
}

// An explicit OIDCPostLogoutRedirectURL must win over the derived
// OIDCRedirectURL origin — the escape hatch for when the derived value
// isn't a registered post-logout redirect URI with the provider.
func TestHandleLogout_OIDC_ExplicitPostLogoutRedirectURL_Wins(t *testing.T) {
	config := Config{
		Mode:                      AuthModeOIDC,
		SessionSecret:             "test-secret-that-is-at-least-32-bytes-long",
		OIDCClientID:              "app-registry-ui",
		OIDCRedirectURL:           "https://app-registry.example.com/auth/callback",
		OIDCPostLogoutRedirectURL: "https://app-registry.example.com/logged-out",
	}
	auth := &Authenticator{
		config:             config,
		sessions:           NewSessionManager(config.SessionSecret, "test_session"),
		endSessionEndpoint: "https://keycloak.example.com/realms/r/protocol/openid-connect/logout",
	}

	req := httptest.NewRequest("GET", "/auth/logout", nil)
	w := httptest.NewRecorder()

	auth.HandleLogout(w, req)

	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "https://app-registry.example.com/logged-out", loc.Query().Get("post_logout_redirect_uri"))
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

