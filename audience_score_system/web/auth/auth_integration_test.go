//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and audience_score_system/store's
// store_integration_test.go for the pattern this file follows: spin up a
// throwaway Postgres via dbtest, apply the domain's own real embedded
// migrations (001_identity + 003_web_session, not a hand-copied schema),
// then exercise this package's public API -- Authenticator.HandleCallback
// and RequireSignedIn -- against it.
//
// These tests exercise exactly what auth_test.go's pure-Go tests cannot:
// FR1/FR2 end to end (a real store.PersonStore.UpsertByGoogleSubject
// creating/reusing a Person row, and SessionManager.Establish writing a
// real web_session row for it -- migration 003, #1570's own table), and
// RequireSignedIn resolving a live/tampered/expired session_id through a
// real SQL query rather than failing before ever reaching the database (see
// auth_test.go's TestRequireSignedIn_NoCookie_RedirectsToLogin for the one
// RequireSignedIn case that doesn't need Postgres).
//
// The Google side is still never called: verifier is a real
// *oidc.IDTokenVerifier built from oidc.StaticKeySet against a locally
// generated key (see auth_test.go's newTestVerifier/signTestIDToken, which
// this file also uses), and oauth2Config is the same no-network
// stubExchanger.
package auth

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newAuthTestStack provisions an isolated Postgres database via dbtest,
// applies the domain's real embedded migrations (person, web_session), and
// returns a ready *store.Store, a *SessionManager backed by the same pool,
// and the underlying dbtest.Postgres for tests that need to read rows
// directly (e.g. to prove exactly one Person/session row was written).
func newAuthTestStack(t *testing.T) (*store.Store, *SessionManager, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply the real embedded schema (001_identity + 003_web_session)")

	sessions := NewSessionManager(db.Pool, testCookieName, "session-secret", testEncKey())
	return store.New(db.Pool), sessions, db
}

// newCallbackRequest builds a request carrying an oauth state cookie whose
// value matches the "state" query param, so HandleCallback's CSRF check
// passes -- same helper shape as auth_test.go's callbackWithValidState, but
// against this file's own SessionManager instance.
func newCallbackRequest(t *testing.T, sessions *SessionManager, code string) *http.Request {
	t.Helper()
	setupReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	setupW := httptest.NewRecorder()
	require.NoError(t, sessions.SetOAuthState(setupW, setupReq, "known-state", "/"))
	cookie := findCookie(t, setupW.Result().Cookies(), testOAuthCookie)

	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=known-state&code="+code, nil)
	req.AddCookie(cookie)
	return req
}

func newAuthenticator(persons store.PersonStore, sessions *SessionManager, verifier idTokenVerifier, rawIDToken string) *Authenticator {
	exch := &stubExchanger{
		token: (&oauth2.Token{AccessToken: "at", RefreshToken: "refresh-token-plaintext"}).WithExtra(map[string]any{"id_token": rawIDToken}),
	}
	return &Authenticator{
		config:       Config{ClientID: testClientID},
		persons:      persons,
		sessions:     sessions,
		oauth2Config: exch,
		verifier:     verifier,
	}
}

// ── FR1: new sub creates exactly one Person and one session ────────────────

func TestHandleCallback_NewSub_CreatesExactlyOnePersonAndSession(t *testing.T) {
	ctx := context.Background()
	st, sessions, db := newAuthTestStack(t)
	verifier, key := newTestVerifier(t)

	rawToken := signTestIDToken(t, key, baseClaims("sub-new", "a@example.com", "Alice"))
	a := newAuthenticator(st.Persons(), sessions, verifier, rawToken)

	req := newCallbackRequest(t, sessions, "code-1")
	w := httptest.NewRecorder()
	a.HandleCallback(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code, "a successful callback must redirect (body: %s)", w.Body.String())
	assert.Equal(t, "/", w.Header().Get("Location"))
	sessionCookie := findCookie(t, w.Result().Cookies(), testCookieName)
	assert.NotEmpty(t, sessionCookie.Value)

	var personCount int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM person WHERE google_subject = $1`, "sub-new",
	).Scan(&personCount))
	assert.Equal(t, 1, personCount, "a never-before-seen sub must create exactly one Person (FR1)")

	var sessionCount int
	var storedRefresh string
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(max(refresh_token), '') FROM web_session WHERE session_id = $1`, sessionCookie.Value,
	).Scan(&sessionCount, &storedRefresh))
	assert.Equal(t, 1, sessionCount, "HandleCallback must establish exactly one session row")
	assert.NotEmpty(t, storedRefresh)
	assert.NotEqual(t, "refresh-token-plaintext", storedRefresh, "the stored refresh token must be encrypted, never plaintext")
}

// ── FR2: same sub with a changed email/name reuses the Person, no dup ───────

func TestHandleCallback_SameSub_ChangedProfile_ReusesPersonNoDuplicate(t *testing.T) {
	ctx := context.Background()
	st, sessions, db := newAuthTestStack(t)
	verifier, key := newTestVerifier(t)

	firstToken := signTestIDToken(t, key, baseClaims("sub-existing", "a@example.com", "Alice"))
	a1 := newAuthenticator(st.Persons(), sessions, verifier, firstToken)
	req1 := newCallbackRequest(t, sessions, "code-1")
	w1 := httptest.NewRecorder()
	a1.HandleCallback(w1, req1)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	var firstID string
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT id FROM person WHERE google_subject = $1`, "sub-existing",
	).Scan(&firstID))

	secondToken := signTestIDToken(t, key, baseClaims("sub-existing", "b@example.com", "Alice B."))
	a2 := newAuthenticator(st.Persons(), sessions, verifier, secondToken)
	req2 := newCallbackRequest(t, sessions, "code-2")
	w2 := httptest.NewRecorder()
	a2.HandleCallback(w2, req2)
	require.Equal(t, http.StatusSeeOther, w2.Code, "body: %s", w2.Body.String())

	var secondID, email, name string
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT id, email, display_name FROM person WHERE google_subject = $1`, "sub-existing",
	).Scan(&secondID, &email, &name))
	assert.Equal(t, firstID, secondID, "a returning sub must resolve to the same Person id (FR2)")
	assert.Equal(t, "b@example.com", email, "email must update on the existing row")
	assert.Equal(t, "Alice B.", name, "display_name must update on the existing row")

	var personCount int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM person WHERE google_subject = $1`, "sub-existing",
	).Scan(&personCount))
	assert.Equal(t, 1, personCount, "a second sign-in with the same sub must not create a duplicate Person")

	var sessionCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM web_session WHERE person_id = $1`, secondID).Scan(&sessionCount))
	assert.Equal(t, 2, sessionCount, "each sign-in still establishes its own session row -- FR2 dedupes the Person, not sessions")
}

// ── two distinct subs create two Persons ────────────────────────────────────

func TestHandleCallback_DifferentSubs_CreateTwoPeople(t *testing.T) {
	ctx := context.Background()
	st, sessions, db := newAuthTestStack(t)
	verifier, key := newTestVerifier(t)

	tokenA := signTestIDToken(t, key, baseClaims("sub-a", "a@example.com", "A"))
	aA := newAuthenticator(st.Persons(), sessions, verifier, tokenA)
	reqA := newCallbackRequest(t, sessions, "code-a")
	wA := httptest.NewRecorder()
	aA.HandleCallback(wA, reqA)
	require.Equal(t, http.StatusSeeOther, wA.Code)

	tokenB := signTestIDToken(t, key, baseClaims("sub-b", "b@example.com", "B"))
	aB := newAuthenticator(st.Persons(), sessions, verifier, tokenB)
	reqB := newCallbackRequest(t, sessions, "code-b")
	wB := httptest.NewRecorder()
	aB.HandleCallback(wB, reqB)
	require.Equal(t, http.StatusSeeOther, wB.Code)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM person WHERE google_subject IN ('sub-a', 'sub-b')`,
	).Scan(&count))
	assert.Equal(t, 2, count, "two different subs must create two distinct Person rows")
}

// ── RequireSignedIn against real session rows ───────────────────────────────

func TestRequireSignedIn_ValidSession_PutsPersonOnContextAndReturns200(t *testing.T) {
	ctx := context.Background()
	st, sessions, _ := newAuthTestStack(t)

	person, created, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-signed-in", "a@example.com", "Alice")
	require.NoError(t, err)
	require.True(t, created)

	w := httptest.NewRecorder()
	require.NoError(t, sessions.Establish(ctx, w, person.ID.String(), ""))
	sessionCookie := findCookie(t, w.Result().Cookies(), testCookieName)

	a := &Authenticator{persons: st.Persons(), sessions: sessions}

	var gotPerson *store.Person
	handler := a.RequireSignedIn(func(w http.ResponseWriter, r *http.Request) {
		gotPerson = PersonFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	handler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, gotPerson, "RequireSignedIn must place the resolved Person on the request context")
	assert.Equal(t, person.ID, gotPerson.ID)
}

func TestRequireSignedIn_TamperedCookie_RedirectsToLogin(t *testing.T) {
	st, sessions, _ := newAuthTestStack(t)
	a := &Authenticator{persons: st.Persons(), sessions: sessions}

	called := false
	handler := a.RequireSignedIn(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: "this-session-id-does-not-exist-in-web_session"})
	w := httptest.NewRecorder()
	handler(w, req)

	assert.False(t, called, "the protected handler must not run for an unrecognized session id")
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
}

func TestRequireSignedIn_ExpiredSession_RedirectsToLogin(t *testing.T) {
	ctx := context.Background()
	st, sessions, db := newAuthTestStack(t)
	a := &Authenticator{persons: st.Persons(), sessions: sessions}

	person, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-expired", "a@example.com", "Alice")
	require.NoError(t, err)

	// Insert a session row directly with expires_at already in the past --
	// bypassing Establish, which always sets a future expiry -- to prove
	// PersonID's "expires_at > NOW()" clause actually rejects a lapsed
	// session rather than trusting the cookie alone.
	const expiredSessionID = "expired-session-id"
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO web_session (session_id, person_id, expires_at)
		VALUES ($1, $2, $3)
	`, expiredSessionID, person.ID, time.Now().Add(-time.Hour))
	require.NoError(t, err)

	called := false
	handler := a.RequireSignedIn(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: expiredSessionID})
	w := httptest.NewRecorder()
	handler(w, req)

	assert.False(t, called, "the protected handler must not run for an expired session")
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
}

// ── HandleLogout deletes the real session row ───────────────────────────────

func TestHandleLogout_DeletesSessionRow(t *testing.T) {
	ctx := context.Background()
	st, sessions, db := newAuthTestStack(t)
	a := &Authenticator{sessions: sessions}

	person, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-logout", "a@example.com", "Alice")
	require.NoError(t, err)

	setupW := httptest.NewRecorder()
	require.NoError(t, sessions.Establish(ctx, setupW, person.ID.String(), ""))
	sessionCookie := findCookie(t, setupW.Result().Cookies(), testCookieName)

	var before int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM web_session WHERE session_id = $1`, sessionCookie.Value).Scan(&before))
	require.Equal(t, 1, before)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(sessionCookie)
	w := httptest.NewRecorder()
	a.HandleLogout(w, req)

	assert.Equal(t, http.StatusSeeOther, w.Code)

	var after int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM web_session WHERE session_id = $1`, sessionCookie.Value).Scan(&after))
	assert.Equal(t, 0, after, "HandleLogout must delete the session row, not just clear the cookie")
}
