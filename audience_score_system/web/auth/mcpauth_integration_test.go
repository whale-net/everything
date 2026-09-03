//go:build integration

// See auth_integration_test.go's package doc for the dbtest pattern this
// file follows. It covers the three MCPCallerResolver (issue #1646, FR12)
// cases that need a real web_session row: a live session resolves to the
// Person's UUID string, a tampered (unrecognized) session id fails, and an
// expired session fails -- exactly mirroring
// TestRequireSignedIn_ValidSession_PutsPersonOnContextAndReturns200/
// TestRequireSignedIn_TamperedCookie_RedirectsToLogin/
// TestRequireSignedIn_ExpiredSession_RedirectsToLogin, since
// MCPCallerResolver and RequireSignedIn resolve the exact same session
// state (SessionManager.PersonID) and must therefore agree on every one of
// these outcomes.
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPCallerResolver_ValidSession_ReturnsPersonIDString(t *testing.T) {
	ctx := context.Background()
	st, sessions, _ := newAuthTestStack(t)

	person, created, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-mcp-resolver", "a@example.com", "Alice")
	require.NoError(t, err)
	require.True(t, created)

	w := httptest.NewRecorder()
	require.NoError(t, sessions.Establish(ctx, w, person.ID.String(), ""))
	sessionCookie := findCookie(t, w.Result().Cookies(), testCookieName)

	a := NewForTests(st.Persons(), sessions)
	resolver := a.MCPCallerResolver()

	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.AddCookie(sessionCookie)

	identity, ok := resolver(req)
	require.True(t, ok)
	assert.Equal(t, person.ID.String(), identity, "the resolved identity must be the Person's UUID in string form")
}

func TestMCPCallerResolver_TamperedCookie_ReturnsFalse(t *testing.T) {
	_, sessions, _ := newAuthTestStack(t)
	a := NewForTests(&fakePersonStore{}, sessions)
	resolver := a.MCPCallerResolver()

	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: "this-session-id-does-not-exist-in-web_session"})

	identity, ok := resolver(req)
	assert.False(t, ok)
	assert.Empty(t, identity)
}

func TestMCPCallerResolver_ExpiredSession_ReturnsFalse(t *testing.T) {
	ctx := context.Background()
	st, sessions, db := newAuthTestStack(t)
	a := NewForTests(st.Persons(), sessions)
	resolver := a.MCPCallerResolver()

	person, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-mcp-resolver-expired", "a@example.com", "Alice")
	require.NoError(t, err)

	const expiredSessionID = "expired-session-id-mcp-resolver"
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO web_session (session_id, person_id, expires_at)
		VALUES ($1, $2, $3)
	`, expiredSessionID, person.ID, time.Now().Add(-time.Hour))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: expiredSessionID})

	identity, ok := resolver(req)
	assert.False(t, ok)
	assert.Empty(t, identity)
}
