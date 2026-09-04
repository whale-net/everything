//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and web/schedule's own
// schedule_integration_test.go for the pattern this file follows: spin up
// a throwaway Postgres via dbtest, apply the domain's own real embedded
// migrations, wire a real *store.Store and a real *auth.SessionManager
// against it, and drive access.Handlers through a small local
// http.ServeMux that mirrors `web`'s main.go route registrations for
// GET /channels/{id}/access and POST /channels/{id}/access/{invites,
// promote,remove} -- so PathValue resolution and auth.RequireSignedIn
// wrapping behave exactly as they do in production.
//
// A signed-in caller is simulated via auth.NewForTests + SessionManager.
// Establish, mirroring invite_integration_test.go/schedule_integration_
// test.go's rationale: HandleLogin/HandleCallback are already covered by
// web/auth's own tests, so establishing a real session row directly here
// proves everything this package's own routes own, not auth's OAuth
// mechanics a second time.
package access_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/access"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "test_ass_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("access-integration-test-key"))
}

// accessTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), and a
// router that mirrors main.go's access route wiring (see this file's
// package doc comment).
type accessTestStack struct {
	store    *store.Store
	sessions *auth.SessionManager
	router   http.Handler
	db       *dbtest.Postgres
}

// newAccessTestStack provisions dbtest Postgres, applies the domain's real
// embedded migrations, and wires a real store.Store/auth.SessionManager/
// access.Handlers into a router equivalent to main.go's setupRoutes for
// this package's routes.
func newAccessTestStack(t *testing.T) *accessTestStack {
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
	h := access.New(st)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/access", a.RequireSignedIn(h.HandleShow))
	mux.HandleFunc("POST /channels/{id}/access/invites", a.RequireSignedIn(h.HandleInviteCoCreator))
	mux.HandleFunc("POST /channels/{id}/access/promote", a.RequireSignedIn(h.HandlePromote))
	mux.HandleFunc("POST /channels/{id}/access/remove", a.RequireSignedIn(h.HandleRemove))

	return &accessTestStack{store: st, sessions: sessions, router: mux, db: db}
}

// setupChannel creates a Channel with a live Founder, mirroring
// store_integration_test.go's setupChannel fixture.
func (s *accessTestStack) setupChannel(t *testing.T, ctx context.Context) (store.Channel, store.Person) {
	t.Helper()
	founder, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-founder-"+uuid.NewString(), "founder@example.com", "Founder")
	require.NoError(t, err)
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Test Channel", founder.ID)
	require.NoError(t, err)
	return ch, founder
}

// newPerson creates a fresh, role-less Person.
func (s *accessTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// addRole grants role to p on ch, attributed to grantedBy -- a thin
// wrapper over RoleStore.AddRole for fixture setup.
func (s *accessTestStack) addRole(t *testing.T, ctx context.Context, ch store.Channel, p store.Person, role store.Role, grantedBy store.Person) {
	t.Helper()
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, p.ID, role, grantedBy.ID))
}

// sessionCookie establishes a real session row for personID and returns
// the resulting cookie, standing in for a completed sign-in (see this
// file's package doc comment).
func (s *accessTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
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

func (s *accessTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// doForm POSTs target as an application/x-www-form-urlencoded body --
// used by HandlePromote/HandleRemove's "person_id" field.
func (s *accessTestStack) doForm(t *testing.T, target string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// channelPersonRow is one raw channel_person row, read directly for the
// "byte-identical" assertions FR33's Testing section calls for -- a
// disallowed Remove must leave every column of the target's row
// untouched, not merely return the right status code.
type channelPersonRow struct {
	Role      string
	ValidFrom int64 // unix nanos, for exact equality without timestamp fuzz
	ValidTo   *int64
}

func (s *accessTestStack) channelPersonRow(t *testing.T, ctx context.Context, channelID, personID uuid.UUID) channelPersonRow {
	t.Helper()
	var row channelPersonRow
	var validFrom, validTo sql.NullTime
	err := s.db.Pool.QueryRow(ctx, `
		SELECT role, valid_from, valid_to FROM channel_person
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID).Scan(&row.Role, &validFrom, &validTo)
	require.NoError(t, err, "expected exactly one open channel_person row for this (channel, person) pair")
	row.ValidFrom = validFrom.Time.UnixNano()
	if validTo.Valid {
		ns := validTo.Time.UnixNano()
		row.ValidTo = &ns
	}
	return row
}

func (s *accessTestStack) openRoleCount(t *testing.T, ctx context.Context, channelID, personID uuid.UUID) int {
	t.Helper()
	var count int
	require.NoError(t, s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM channel_person WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID).Scan(&count))
	return count
}

func (s *accessTestStack) liveInviteCount(t *testing.T, ctx context.Context, channelID uuid.UUID, role store.Role) int {
	t.Helper()
	var count int
	require.NoError(t, s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM channel_invite
		WHERE channel_id = $1 AND role = $2 AND consumed_at IS NULL AND invalidated_at IS NULL
	`, channelID, role).Scan(&count))
	return count
}

// ── HandleShow: page gate ───────────────────────────────────────────────

func TestHandleShow_FounderAndCoCreator_See200WithFullRoster_AnalystAndOutsider_403(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)

	coCreator := s.newPerson(t, ctx, "cocreator")
	s.addRole(t, ctx, ch, coCreator, store.RoleCoCreator, founder)
	analyst := s.newPerson(t, ctx, "analyst")
	s.addRole(t, ctx, ch, analyst, store.RoleAnalyst, founder)
	outsider := s.newPerson(t, ctx, "outsider")

	founderW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/access", s.sessionCookie(t, ctx, founder.ID))
	require.Equal(t, http.StatusOK, founderW.Code, "body: %s", founderW.Body.String())
	founderBody := founderW.Body.String()
	assert.Contains(t, founderBody, "Founder", "Founder's own roster view must render the Founder row")
	assert.Contains(t, founderBody, coCreator.Email, "the roster must list the Co-Creator")
	assert.Contains(t, founderBody, analyst.Email, "the roster must list the Analyst")

	coCreatorW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/access", s.sessionCookie(t, ctx, coCreator.ID))
	require.Equal(t, http.StatusOK, coCreatorW.Code, "body: %s", coCreatorW.Body.String())
	assert.Contains(t, coCreatorW.Body.String(), analyst.Email)

	analystW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/access", s.sessionCookie(t, ctx, analyst.ID))
	assert.Equal(t, http.StatusForbidden, analystW.Code, "an Analyst must not be able to view the access page")

	outsiderW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/access", s.sessionCookie(t, ctx, outsider.ID))
	assert.Equal(t, http.StatusForbidden, outsiderW.Code, "a non-member must not be able to view the access page")
}

// ── Affordances (FR33's matrix, rendered) ───────────────────────────────

func TestHandleShow_CoCreatorViewer_NoRemoveOnFounderOrOtherCoCreatorRow(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)

	coCreator := s.newPerson(t, ctx, "cocreator")
	s.addRole(t, ctx, ch, coCreator, store.RoleCoCreator, founder)
	otherCoCreator := s.newPerson(t, ctx, "othercocreator")
	s.addRole(t, ctx, ch, otherCoCreator, store.RoleCoCreator, founder)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/access", s.sessionCookie(t, ctx, coCreator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.NotContains(t, body, `value="`+founder.ID.String()+`"`, "a Co-Creator viewer must see no per-Founder-row form action referencing the Founder's person_id")
	assert.NotContains(t, body, `value="`+otherCoCreator.ID.String()+`"`, "a Co-Creator viewer must see no per-Co-Creator-row form action referencing another Co-Creator's person_id")
}

func TestHandleShow_FounderViewer_RemoveOnCoCreatorAndAnalystRows_NeverOnOwnRow(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)

	coCreator := s.newPerson(t, ctx, "cocreator")
	s.addRole(t, ctx, ch, coCreator, store.RoleCoCreator, founder)
	analyst := s.newPerson(t, ctx, "analyst")
	s.addRole(t, ctx, ch, analyst, store.RoleAnalyst, founder)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/access", s.sessionCookie(t, ctx, founder.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, `action="/channels/`+ch.ID.String()+`/access/remove"`, "a Founder's view must render at least one Remove action")
	assert.Contains(t, body, `value="`+coCreator.ID.String()+`"`, "a Founder must be able to remove a Co-Creator")
	assert.Contains(t, body, `value="`+analyst.ID.String()+`"`, "a Founder must be able to remove an Analyst")
	assert.NotContains(t, body, `value="`+founder.ID.String()+`"`, "a Founder must never see any per-row form action referencing their own person_id (never Remove, never Promote)")
}

func TestHandleShow_PromoteAppearsOnlyOnAnalystRows(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)

	coCreator := s.newPerson(t, ctx, "cocreator")
	s.addRole(t, ctx, ch, coCreator, store.RoleCoCreator, founder)
	analyst := s.newPerson(t, ctx, "analyst")
	s.addRole(t, ctx, ch, analyst, store.RoleAnalyst, founder)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/access", s.sessionCookie(t, ctx, founder.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, `action="/channels/`+ch.ID.String()+`/access/promote"`, "the Analyst row must render a Promote action")
	// Exactly one promote form -- the Co-Creator and Founder rows must not
	// each contribute one too.
	assert.Equal(t, 1, strings.Count(body, `action="/channels/`+ch.ID.String()+`/access/promote"`))
}

// ── Invite (FR30, LB4, NFR11) ────────────────────────────────────────────

func TestHandleInviteCoCreator_FounderAndCoCreator_Succeed(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)
	coCreator := s.newPerson(t, ctx, "cocreator")
	s.addRole(t, ctx, ch, coCreator, store.RoleCoCreator, founder)

	for _, actor := range []store.Person{founder, coCreator} {
		w := s.do(t, http.MethodPost, "/channels/"+ch.ID.String()+"/access/invites", s.sessionCookie(t, ctx, actor.ID))
		require.Equal(t, http.StatusOK, w.Code, "actor %s, body: %s", actor.DisplayName, w.Body.String())
		assert.Contains(t, w.Body.String(), "/invites/")
	}
	assert.Equal(t, 1, s.liveInviteCount(t, ctx, ch.ID, store.RoleCoCreator), "idempotent generate must not create a second live Co-Creator invite")
}

func TestHandleInviteCoCreator_SecondSubmit_ReturnsSameCode(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, founder.ID)

	first := s.do(t, http.MethodPost, "/channels/"+ch.ID.String()+"/access/invites", cookie)
	require.Equal(t, http.StatusOK, first.Code, "body: %s", first.Body.String())
	second := s.do(t, http.MethodPost, "/channels/"+ch.ID.String()+"/access/invites", cookie)
	require.Equal(t, http.StatusOK, second.Code, "body: %s", second.Body.String())

	assert.Equal(t, first.Body.String(), second.Body.String(), "re-submitting while a Co-Creator invite is live must return the same code (LB4)")
	assert.Equal(t, 1, s.liveInviteCount(t, ctx, ch.ID, store.RoleCoCreator))
}

func TestHandleInviteCoCreator_LiveAnalystInvite_StaysLive(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)

	analystInvite, err := s.store.Invites().Generate(ctx, ch.ID, founder.ID, store.RoleAnalyst)
	require.NoError(t, err)

	w := s.do(t, http.MethodPost, "/channels/"+ch.ID.String()+"/access/invites", s.sessionCookie(t, ctx, founder.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, 1, s.liveInviteCount(t, ctx, ch.ID, store.RoleAnalyst), "a live Analyst invite must be unaffected by generating a Co-Creator invite (NFR11)")
	assert.Equal(t, 1, s.liveInviteCount(t, ctx, ch.ID, store.RoleCoCreator))

	stillLive, err := s.store.Invites().Lookup(ctx, analystInvite.Code)
	require.NoError(t, err)
	assert.Nil(t, stillLive.ConsumedAt)
	assert.Nil(t, stillLive.InvalidatedAt)
}

func TestHandleInviteCoCreator_Analyst_Forbidden_NoInviteRowCreated(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)
	analyst := s.newPerson(t, ctx, "analyst")
	s.addRole(t, ctx, ch, analyst, store.RoleAnalyst, founder)

	w := s.do(t, http.MethodPost, "/channels/"+ch.ID.String()+"/access/invites", s.sessionCookie(t, ctx, analyst.ID))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 0, s.liveInviteCount(t, ctx, ch.ID, store.RoleCoCreator), "an Analyst's forbidden invite attempt must create no channel_invite row")
}

// ── Promote (FR31) ───────────────────────────────────────────────────────

func TestHandlePromote_Founder_PromotesAnalyst_ExactlyOneOpenRow(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)
	analyst := s.newPerson(t, ctx, "analyst")
	s.addRole(t, ctx, ch, analyst, store.RoleAnalyst, founder)
	cookie := s.sessionCookie(t, ctx, founder.ID)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/access/promote", cookie, url.Values{"person_id": {analyst.ID.String()}})
	assert.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	roles, err := s.store.Roles().RolesFor(ctx, ch.ID, analyst.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleCoCreator}, roles)
	assert.Equal(t, 1, s.openRoleCount(t, ctx, ch.ID, analyst.ID), "promote must close-and-open, never leave two open rows")
}

func TestHandlePromote_RepeatSubmit_StillOneRow_NoDuplicate(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)
	analyst := s.newPerson(t, ctx, "analyst")
	s.addRole(t, ctx, ch, analyst, store.RoleAnalyst, founder)
	cookie := s.sessionCookie(t, ctx, founder.ID)

	form := url.Values{"person_id": {analyst.ID.String()}}
	first := s.doForm(t, "/channels/"+ch.ID.String()+"/access/promote", cookie, form)
	require.Equal(t, http.StatusSeeOther, first.Code, "body: %s", first.Body.String())

	second := s.doForm(t, "/channels/"+ch.ID.String()+"/access/promote", cookie, form)
	assert.Equal(t, http.StatusSeeOther, second.Code, "repeat promote of an already-Co-Creator target must be an idempotent success (FR31), body: %s", second.Body.String())

	roles, err := s.store.Roles().RolesFor(ctx, ch.ID, analyst.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleCoCreator}, roles)
	assert.Equal(t, 1, s.openRoleCount(t, ctx, ch.ID, analyst.ID), "repeat promote must not create a duplicate open row")
}

func TestHandlePromote_Analyst_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)
	analyst := s.newPerson(t, ctx, "analyst")
	s.addRole(t, ctx, ch, analyst, store.RoleAnalyst, founder)
	other := s.newPerson(t, ctx, "other-analyst")
	s.addRole(t, ctx, ch, other, store.RoleAnalyst, founder)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/access/promote", s.sessionCookie(t, ctx, analyst.ID), url.Values{"person_id": {other.ID.String()}})
	assert.Equal(t, http.StatusForbidden, w.Code)

	roles, err := s.store.Roles().RolesFor(ctx, ch.ID, other.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleAnalyst}, roles, "an Analyst's forbidden promote attempt must not change the target's role")
}

// ── Remove (FR33) ─────────────────────────────────────────────────────────

func TestHandleRemove_MatrixAllowedCells_Succeed(t *testing.T) {
	type target struct {
		name string
		role store.Role
	}
	for _, tc := range []struct {
		name      string
		actorRole store.Role
		target    target
	}{
		{"founder removes co-creator", store.RoleCreator, target{"cocreator", store.RoleCoCreator}},
		{"founder removes analyst", store.RoleCreator, target{"analyst", store.RoleAnalyst}},
		{"co-creator removes analyst", store.RoleCoCreator, target{"analyst", store.RoleAnalyst}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newAccessTestStack(t)
			ch, founder := s.setupChannel(t, ctx)

			var actor store.Person
			if tc.actorRole == store.RoleCreator {
				actor = founder
			} else {
				actor = s.newPerson(t, ctx, "actor")
				s.addRole(t, ctx, ch, actor, tc.actorRole, founder)
			}

			targetPerson := s.newPerson(t, ctx, tc.target.name)
			s.addRole(t, ctx, ch, targetPerson, tc.target.role, founder)

			w := s.doForm(t, "/channels/"+ch.ID.String()+"/access/remove", s.sessionCookie(t, ctx, actor.ID), url.Values{"person_id": {targetPerson.ID.String()}})
			assert.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, 0, s.openRoleCount(t, ctx, ch.ID, targetPerson.ID), "an allowed removal must close the target's open row")
		})
	}
}

func TestHandleRemove_MatrixDisallowedCells_403_RowByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name      string
		actorRole store.Role // "" means Analyst
	}{
		{"co-creator cannot remove founder", store.RoleCoCreator},
		{"co-creator cannot remove another co-creator", store.RoleCoCreator},
		{"founder cannot remove self", store.RoleCreator},
		{"analyst cannot remove anyone", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newAccessTestStack(t)
			ch, founder := s.setupChannel(t, ctx)

			var actor, targetPerson store.Person
			switch tc.name {
			case "co-creator cannot remove founder":
				actor = s.newPerson(t, ctx, "actor")
				s.addRole(t, ctx, ch, actor, store.RoleCoCreator, founder)
				targetPerson = founder
			case "co-creator cannot remove another co-creator":
				actor = s.newPerson(t, ctx, "actor")
				s.addRole(t, ctx, ch, actor, store.RoleCoCreator, founder)
				targetPerson = s.newPerson(t, ctx, "target-cocreator")
				s.addRole(t, ctx, ch, targetPerson, store.RoleCoCreator, founder)
			case "founder cannot remove self":
				actor = founder
				targetPerson = founder
			case "analyst cannot remove anyone":
				actor = s.newPerson(t, ctx, "actor")
				s.addRole(t, ctx, ch, actor, store.RoleAnalyst, founder)
				targetPerson = s.newPerson(t, ctx, "target-analyst")
				s.addRole(t, ctx, ch, targetPerson, store.RoleAnalyst, founder)
			}

			before := s.channelPersonRow(t, ctx, ch.ID, targetPerson.ID)

			w := s.doForm(t, "/channels/"+ch.ID.String()+"/access/remove", s.sessionCookie(t, ctx, actor.ID), url.Values{"person_id": {targetPerson.ID.String()}})
			assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())

			after := s.channelPersonRow(t, ctx, ch.ID, targetPerson.ID)
			assert.Equal(t, before, after, "a disallowed remove must leave the target's channel_person row byte-identical")
		})
	}
}

func TestHandleRemove_NoOpenRole_SuccessNoOp(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	ch, founder := s.setupChannel(t, ctx)
	analyst := s.newPerson(t, ctx, "analyst")
	s.addRole(t, ctx, ch, analyst, store.RoleAnalyst, founder)
	_, err := s.store.Roles().RemoveRole(ctx, ch.ID, analyst.ID, founder.ID)
	require.NoError(t, err)
	require.Equal(t, 0, s.openRoleCount(t, ctx, ch.ID, analyst.ID))

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/access/remove", s.sessionCookie(t, ctx, founder.ID), url.Values{"person_id": {analyst.ID.String()}})
	assert.Equal(t, http.StatusSeeOther, w.Code, "removing a Person with no open role must be an idempotent success (FR33), body: %s", w.Body.String())
	assert.Equal(t, 0, s.openRoleCount(t, ctx, ch.ID, analyst.ID))
}

// TestHandleRemove_ForgedCrossChannelActor_Rejected proves an actor with
// no role at all on the Channel being posted to cannot remove anyone
// there, even when both the actor AND the forged person_id are real,
// legitimate identities that just belong to a DIFFERENT Channel (chB).
// This is deliberately NOT the same as TestHandleRemove_NoOpenRole_
// SuccessNoOp's scenario (a target with no open role on THIS Channel is
// otherwise an idempotent success, FR33): here the actor also holds no
// role on this Channel, which is what the coarse "does the actor hold any
// authority on THIS Channel" check in HandleRemove exists to reject
// outright (see its doc comment) -- without that check, store.CanRemove
// alone cannot distinguish this forged request from the legitimate
// no-op, since both read as "target has no open role on this Channel".
func TestHandleRemove_ForgedCrossChannelActor_Rejected(t *testing.T) {
	ctx := context.Background()
	s := newAccessTestStack(t)
	chA, _ := s.setupChannel(t, ctx)

	// chB and its Co-Creator are entirely unrelated to chA: personOnB
	// holds no role at all on chA, and founderB (the forging actor) holds
	// no role at all on chA either.
	chB, founderB := s.setupChannel(t, ctx)
	personOnB := s.newPerson(t, ctx, "person-on-b")
	s.addRole(t, ctx, chB, personOnB, store.RoleCoCreator, founderB)
	require.Equal(t, 0, s.openRoleCount(t, ctx, chA.ID, personOnB.ID), "personOnB must hold no role on chA for this scenario to be meaningful")

	w := s.doForm(t, "/channels/"+chA.ID.String()+"/access/remove", s.sessionCookie(t, ctx, founderB.ID), url.Values{"person_id": {personOnB.ID.String()}})
	assert.Equal(t, http.StatusForbidden, w.Code, "an actor with no role on this Channel must be rejected outright, not treated as an idempotent no-op, body: %s", w.Body.String())

	assert.Equal(t, 0, s.openRoleCount(t, ctx, chA.ID, personOnB.ID), "the forged request must not create any channel_person row on chA")
	assert.Equal(t, 1, s.openRoleCount(t, ctx, chB.ID, personOnB.ID), "personOnB's real role on chB must be untouched")
}
