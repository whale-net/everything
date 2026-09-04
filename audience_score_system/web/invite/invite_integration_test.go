//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and audience_score_system/web/auth's
// auth_integration_test.go for the pattern this file follows: spin up a
// throwaway Postgres via dbtest, apply the domain's own real embedded
// migrations, wire a real *store.Store and a real *auth.SessionManager
// against it, and drive invite.Handlers through a small local
// http.ServeMux that mirrors `web`'s main.go route registrations for
// /invites/... and /channels/{id}/invites -- so PathValue resolution and
// auth.RequireSignedIn wrapping behave exactly as they do in production.
//
// A signed-in caller is simulated via auth.NewForTests + SessionManager.
// Establish rather than a full Google OAuth2 round trip: HandleLogin/
// HandleCallback (the OAuth mechanics themselves) are already covered by
// web/auth's own auth_test.go/auth_integration_test.go, so establishing a
// real session row directly here proves everything this package's FR6 (new-
// visitor path) test actually owns -- that landing on
// GET /invites/{code}/resume with a freshly-established session consumes
// the code and grants role=analyst -- without re-testing auth's OAuth
// mechanics a second time.
package invite_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/invite"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "test_ass_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("invite-integration-test-key"))
}

// inviteTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), and a router
// that mirrors main.go's invite route wiring (see this file's package doc
// comment).
type inviteTestStack struct {
	store    *store.Store
	sessions *auth.SessionManager
	router   http.Handler
	db       *dbtest.Postgres
}

// newInviteTestStack provisions dbtest Postgres, applies the domain's real
// embedded migrations, and wires a real store.Store/auth.SessionManager/
// invite.Handlers into a router equivalent to main.go's setupRoutes for
// this package's routes.
func newInviteTestStack(t *testing.T) *inviteTestStack {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply the real embedded schema")

	st := store.New(db.Pool)
	sessions := auth.NewSessionManager(db.Pool, testCookieName, "session-secret", testEncKey())
	a := auth.NewForTests(st.Persons(), sessions)
	inv := invite.New(st, sessions)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /invites/{code}", inv.HandleShow)
	mux.HandleFunc("POST /channels/{id}/invites", a.RequireSignedIn(inv.HandleGenerate))
	mux.HandleFunc("GET /invites/{code}/resume", a.RequireSignedIn(inv.HandleResume))
	mux.HandleFunc("POST /invites/{code}/accept", a.RequireSignedIn(inv.HandleAccept))
	mux.HandleFunc("POST /invites/{code}/decline", a.RequireSignedIn(inv.HandleDecline))

	return &inviteTestStack{store: st, sessions: sessions, router: mux, db: db}
}

// setupChannel creates a Channel with a live creator, mirroring
// store_integration_test.go's setupChannel fixture.
func (s *inviteTestStack) setupChannel(t *testing.T, ctx context.Context) (store.Channel, store.Person) {
	t.Helper()
	creator, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-creator-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Test Channel", creator.ID)
	require.NoError(t, err)
	return ch, creator
}

// newPerson creates a fresh, role-less Person.
func (s *inviteTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// sessionCookie establishes a real session row for personID and returns the
// resulting cookie, standing in for a completed sign-in (see this file's
// package doc comment).
func (s *inviteTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	require.NoError(t, s.sessions.Establish(ctx, w, personID.String(), ""))
	return findCookie(t, w.Result().Cookies(), testCookieName)
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not found among %d cookies", name, len(cookies))
	return nil
}

func (s *inviteTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// liveChannelPersonRole returns the single currently-held role for
// (channelID, personID), or "" if none is held.
func liveChannelPersonRole(t *testing.T, ctx context.Context, s *inviteTestStack, channelID, personID uuid.UUID) string {
	t.Helper()
	var role string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT role FROM channel_person WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID).Scan(&role)
	if err != nil {
		return ""
	}
	return role
}

func liveChannelPersonCount(t *testing.T, ctx context.Context, s *inviteTestStack, channelID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_person WHERE channel_id = $1 AND valid_to IS NULL
	`, channelID).Scan(&n))
	return n
}

// ── FR30: generate is idempotent per tier -- a repeat call returns the
// SAME live code rather than invalidating and replacing it ─────────────────

func TestHandleGenerate_Twice_ReturnsSameLiveCode(t *testing.T) {
	ctx := context.Background()
	s := newInviteTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w1 := s.do(t, http.MethodPost, "/channels/"+ch.ID.String()+"/invites", cookie)
	require.Equal(t, http.StatusOK, w1.Code, "body: %s", w1.Body.String())

	var firstCode string
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT code FROM channel_invite WHERE channel_id = $1`, ch.ID).Scan(&firstCode))

	w2 := s.do(t, http.MethodPost, "/channels/"+ch.ID.String()+"/invites", cookie)
	require.Equal(t, http.StatusOK, w2.Code, "body: %s", w2.Body.String())
	assert.Contains(t, w2.Body.String(), firstCode, "a repeat generate must hand back the SAME live code (FR30), not a new one")

	var liveCount int
	require.NoError(t, s.db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_invite WHERE channel_id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL
	`, ch.ID).Scan(&liveCount))
	assert.Equal(t, 1, liveCount, "exactly one live code must exist after generating twice")

	var invalidatedAt sql.NullTime
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT invalidated_at FROM channel_invite WHERE code = $1`, firstCode).Scan(&invalidatedAt))
	assert.False(t, invalidatedAt.Valid, "FR30: the existing live code must NOT be invalidated by a repeat generate")

	// The original code must still be redeemable.
	analyst := s.newPerson(t, ctx, "analyst")
	acceptW := s.do(t, http.MethodPost, "/invites/"+firstCode+"/accept", s.sessionCookie(t, ctx, analyst.ID))
	assert.Equal(t, http.StatusSeeOther, acceptW.Code, "body: %s", acceptW.Body.String())
	assert.Equal(t, string(store.RoleAnalyst), liveChannelPersonRole(t, ctx, s, ch.ID, analyst.ID))
}

// ── NFR5: only a Creator may generate a code ────────────────────────────────

func TestHandleGenerate_NonCreator_Forbidden_NoCodeCreated(t *testing.T) {
	ctx := context.Background()
	s := newInviteTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.do(t, http.MethodPost, "/channels/"+ch.ID.String()+"/invites", s.sessionCookie(t, ctx, outsider.ID))

	assert.Equal(t, http.StatusForbidden, w.Code)
	var count int
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM channel_invite WHERE channel_id = $1`, ch.ID).Scan(&count))
	assert.Equal(t, 0, count, "a forbidden generate attempt must create no code")
}

// ── FR6: new-visitor path -- no session, then resume after sign-in ─────────

func TestNewVisitorPath_ShowRedirectsToLogin_ResumeConsumesAndGrantsRole(t *testing.T) {
	ctx := context.Background()
	s := newInviteTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	inv, err := s.store.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)

	showW := s.do(t, http.MethodGet, "/invites/"+inv.Code, nil)
	require.Equal(t, http.StatusFound, showW.Code)
	wantNext := "/invites/" + url.PathEscape(inv.Code) + "/resume"
	assert.Equal(t, "/login?next="+url.QueryEscape(wantNext), showW.Header().Get("Location"))

	// Simulate a brand-new Person completing the Google sign-in flow this
	// redirect sent them into -- HandleLogin/HandleCallback's own mechanics
	// are covered by web/auth's tests (see this file's package doc
	// comment); what FR6 adds on top is that landing on the resume URL
	// with that freshly-established session consumes the code.
	newVisitor := s.newPerson(t, ctx, "new-visitor")
	cookie := s.sessionCookie(t, ctx, newVisitor.ID)

	resumeW := s.do(t, http.MethodGet, wantNext, cookie)
	assert.Equal(t, http.StatusSeeOther, resumeW.Code, "body: %s", resumeW.Body.String())

	var consumedBy uuid.UUID
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT consumed_by_person_id FROM channel_invite WHERE code = $1`, inv.Code).Scan(&consumedBy))
	assert.Equal(t, newVisitor.ID, consumedBy)
	assert.Equal(t, string(store.RoleAnalyst), liveChannelPersonRole(t, ctx, s, ch.ID, newVisitor.ID))
	assert.Equal(t, 2, liveChannelPersonCount(t, ctx, s, ch.ID), "exactly one new live row (creator's + the new analyst's)")
}

// ── FR7: existing signed-in Person -- accept prompt, then accept ───────────

func TestExistingPersonPath_ShowRendersPrompt_AcceptConsumesAndAssociates(t *testing.T) {
	ctx := context.Background()
	s := newInviteTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	inv, err := s.store.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)

	analyst := s.newPerson(t, ctx, "analyst")
	cookie := s.sessionCookie(t, ctx, analyst.ID)

	showW := s.do(t, http.MethodGet, "/invites/"+inv.Code, cookie)
	require.Equal(t, http.StatusOK, showW.Code)
	body := showW.Body.String()
	assert.Contains(t, body, "Test Channel")
	assert.Contains(t, body, "/invites/"+inv.Code+"/accept")
	assert.Contains(t, body, "/invites/"+inv.Code+"/decline")

	acceptW := s.do(t, http.MethodPost, "/invites/"+inv.Code+"/accept", cookie)
	assert.Equal(t, http.StatusSeeOther, acceptW.Code, "body: %s", acceptW.Body.String())

	var consumedAt sql.NullTime
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT consumed_at FROM channel_invite WHERE code = $1`, inv.Code).Scan(&consumedAt))
	assert.True(t, consumedAt.Valid)
	assert.Equal(t, string(store.RoleAnalyst), liveChannelPersonRole(t, ctx, s, ch.ID, analyst.ID))
}

// ── FR7: decline is a state-change-free no-op -- a SEPARATE test, per this
// task's Testing section, from the accept test above ─────────────────────

func TestDecline_LeavesCodeLiveAndCreatesNoAssociation(t *testing.T) {
	ctx := context.Background()
	s := newInviteTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	inv, err := s.store.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)

	analyst := s.newPerson(t, ctx, "decliner")
	cookie := s.sessionCookie(t, ctx, analyst.ID)

	declineW := s.do(t, http.MethodPost, "/invites/"+inv.Code+"/decline", cookie)
	assert.Equal(t, http.StatusSeeOther, declineW.Code, "body: %s", declineW.Body.String())

	var consumedAt, invalidatedAt sql.NullTime
	require.NoError(t, s.db.Pool.QueryRow(ctx, `
		SELECT consumed_at, invalidated_at FROM channel_invite WHERE code = $1
	`, inv.Code).Scan(&consumedAt, &invalidatedAt))
	assert.False(t, consumedAt.Valid, "declining must leave consumed_at NULL")
	assert.False(t, invalidatedAt.Valid, "declining must leave invalidated_at NULL")
	assert.Equal(t, "", liveChannelPersonRole(t, ctx, s, ch.ID, analyst.ID), "declining must create no association")
	assert.Equal(t, 1, liveChannelPersonCount(t, ctx, s, ch.ID), "only the creator's original row should exist")

	// The code must still be live and redeemable by someone else later.
	showW := s.do(t, http.MethodGet, "/invites/"+inv.Code, nil)
	assert.Equal(t, http.StatusFound, showW.Code, "a declined-but-still-live code must still route an anonymous visitor into sign-in, not the terminal Invalid view")
}

// ── FR8: an already-consumed code is a terminal error, no association ──────

func TestAlreadyConsumedCode_AcceptIsTerminal_NoAssociationCreated(t *testing.T) {
	ctx := context.Background()
	s := newInviteTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	inv, err := s.store.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)

	first := s.newPerson(t, ctx, "first")
	require.NoError(t, s.store.Invites().Consume(ctx, inv.Code, first.ID))

	second := s.newPerson(t, ctx, "second")
	w := s.do(t, http.MethodPost, "/invites/"+inv.Code+"/accept", s.sessionCookie(t, ctx, second.ID))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "no longer valid")
	assert.Equal(t, "", liveChannelPersonRole(t, ctx, s, ch.ID, second.ID))
	assert.Equal(t, 2, liveChannelPersonCount(t, ctx, s, ch.ID), "creator's row plus first's -- second must not add a row")
}

// ── Concurrency: two simultaneous accepts race for one code ────────────────

func TestConcurrentAccept_ExactlyOneSucceeds_ExactlyOneLiveRoleGranted(t *testing.T) {
	ctx := context.Background()
	s := newInviteTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	inv, err := s.store.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)

	personA := s.newPerson(t, ctx, "racer-a")
	personB := s.newPerson(t, ctx, "racer-b")
	cookieA := s.sessionCookie(t, ctx, personA.ID)
	cookieB := s.sessionCookie(t, ctx, personB.ID)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	bodies := make([]string, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		w := s.do(t, http.MethodPost, "/invites/"+inv.Code+"/accept", cookieA)
		codes[0], bodies[0] = w.Code, w.Body.String()
	}()
	go func() {
		defer wg.Done()
		w := s.do(t, http.MethodPost, "/invites/"+inv.Code+"/accept", cookieB)
		codes[1], bodies[1] = w.Code, w.Body.String()
	}()
	wg.Wait()

	var wins, losses int
	for i, code := range codes {
		switch code {
		case http.StatusSeeOther:
			wins++
		case http.StatusOK:
			require.Contains(t, bodies[i], "no longer valid", "a non-redirect outcome must be the terminal Invalid view (FR8)")
			losses++
		default:
			t.Fatalf("unexpected status %d (body: %s)", code, bodies[i])
		}
	}
	assert.Equal(t, 1, wins, "exactly one concurrent accept must succeed")
	assert.Equal(t, 1, losses, "exactly one concurrent accept must lose with the FR8 terminal error")

	assert.Equal(t, 2, liveChannelPersonCount(t, ctx, s, ch.ID), "creator's row plus exactly one new analyst row -- never two")

	roleA := liveChannelPersonRole(t, ctx, s, ch.ID, personA.ID)
	roleB := liveChannelPersonRole(t, ctx, s, ch.ID, personB.ID)
	grantedCount := 0
	if roleA == string(store.RoleAnalyst) {
		grantedCount++
	}
	if roleB == string(store.RoleAnalyst) {
		grantedCount++
	}
	assert.Equal(t, 1, grantedCount, "exactly one of the two racers must hold role=analyst")
}

// ── Edge case: redeeming your own Channel's invite as its Creator ──────────

func TestRedeemAsExistingCreator_NoDuplicateLiveRow_NoServerError(t *testing.T) {
	ctx := context.Background()
	s := newInviteTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	inv, err := s.store.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)

	w := s.do(t, http.MethodPost, "/invites/"+inv.Code+"/accept", s.sessionCookie(t, ctx, creator.ID))

	require.Equal(t, http.StatusSeeOther, w.Code, "redeeming your own invite must not 500 (body: %s)", w.Body.String())
	assert.Equal(t, 1, liveChannelPersonCount(t, ctx, s, ch.ID), "addRoleTx's SCD2 close-and-open must not leave a second live row for the creator")
	assert.Equal(t, string(store.RoleAnalyst), liveChannelPersonRole(t, ctx, s, ch.ID, creator.ID), "addRoleTx closes the prior open row and opens a new one for the role being granted, per AGENTS.md SCD2")
}
