//go:build integration

// Postgres-backed coverage for FR25/FR26/NFR9's Channel list/switcher
// (#1722): GET /channels lists every Channel the signed-in Person holds an
// open role on with the right tier label and connection state, excludes a
// closed association, renders an empty state with a Connect action for a
// brand-new Person, redirects an unauthenticated caller to /login, and
// issues a query count that does not grow with Channel count. Also proves
// GET /channels/{id} still rejects a Person with no role on that Channel,
// so the switcher grants nothing on its own.
//
// Mirrors web/schedule/schedule_integration_test.go's harness shape
// (dbtest Postgres + real embedded migrations + a router mirroring
// main.go's route wiring), but lives in `package main` (like
// oauth_integration_test.go) so it can call the unexported handleChannels/
// handleChannelDetail directly.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/web:web_channels_integration_test --test_output=all
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const channelsTestCookieName = "test_ass_channels_session"

func channelsTestEncKey() [32]byte {
	return sha256.Sum256([]byte("channels-integration-test-key"))
}

// channelsTestStack bundles a real Store/SessionManager over an isolated
// Postgres (via dbtest) and a router mirroring main.go's setupRoutes for
// GET /channels and GET /channels/{id}.
type channelsTestStack struct {
	app      *app
	store    *store.Store
	sessions *auth.SessionManager
	router   http.Handler
	db       *dbtest.Postgres
}

func newChannelsTestStack(t *testing.T) *channelsTestStack {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply the real embedded schema")

	st := store.New(db.Pool)
	sessions := auth.NewSessionManager(db.Pool, channelsTestCookieName, "session-secret", channelsTestEncKey())
	authenticator := auth.NewForTests(st.Persons(), sessions)

	application := &app{store: st, auth: authenticator}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels", authenticator.RequireSignedIn(application.handleChannels))
	mux.HandleFunc("GET /channels/{id}", authenticator.RequireSignedIn(application.handleChannelDetail))

	return &channelsTestStack{app: application, store: st, sessions: sessions, router: mux, db: db}
}

// newPerson creates a fresh, role-less Person.
func (s *channelsTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// sessionCookie establishes a real session row for personID, standing in
// for a completed sign-in (see schedule_integration_test.go's identical
// rationale).
func (s *channelsTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	require.NoError(t, s.sessions.Establish(ctx, w, personID.String(), ""))
	return findCookie(t, w.Result().Cookies(), channelsTestCookieName)
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

func (s *channelsTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// ── FR26: list/switcher rows ────────────────────────────────────────────────

func TestHandleChannels_ListsEveryAssociatedChannelWithTierLabel(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "person")

	// A: person is Founder -- self-granted by Channels().Create.
	chA, err := s.store.Channels().Create(ctx, "yt-a-"+uuid.NewString(), "Channel A", person.ID)
	require.NoError(t, err)

	// B: person is Co-Creator, granted by B's own Founder.
	founderB := s.newPerson(t, ctx, "founder-b")
	chB, err := s.store.Channels().Create(ctx, "yt-b-"+uuid.NewString(), "Channel B", founderB.ID)
	require.NoError(t, err)
	require.NoError(t, s.store.Roles().AddRole(ctx, chB.ID, person.ID, store.RoleCoCreator, founderB.ID))

	// C: person is Analyst, granted by C's own Founder; also set
	// needs_reauth to prove connection state renders per row too.
	founderC := s.newPerson(t, ctx, "founder-c")
	chC, err := s.store.Channels().Create(ctx, "yt-c-"+uuid.NewString(), "Channel C", founderC.ID)
	require.NoError(t, err)
	require.NoError(t, s.store.Roles().AddRole(ctx, chC.ID, person.ID, store.RoleAnalyst, founderC.ID))
	require.NoError(t, s.store.Channels().SetConnectionState(ctx, chC.ID, store.ConnectionStateNeedsReauth))

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/channels", cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "Channel A")
	assert.Contains(t, body, "Channel B")
	assert.Contains(t, body, "Channel C")
	assert.Contains(t, body, "Founder")
	assert.Contains(t, body, "Co-Creator")
	assert.Contains(t, body, "Analyst")
	assert.Contains(t, body, "Needs re-authentication")
	// A and B are connected (default state) -- the label must appear too.
	assert.Contains(t, body, "Connected")
	// person is Founder on A -- showConnect must be true.
	assert.Contains(t, body, "Connect another Channel")
	// Channel A's row links into its own per-Channel detail page.
	assert.Contains(t, body, `href="/channels/`+chA.ID.String()+`"`)
}

// TestHandleChannels_ExcludesRevokedAssociation proves FR28's web-side
// counterpart: a closed channel_person row's Channel is absent from the
// list.
func TestHandleChannels_ExcludesRevokedAssociation(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "person")
	founder := s.newPerson(t, ctx, "founder")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Revoked Channel", founder.ID)
	require.NoError(t, err)
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))

	removed, err := s.store.Roles().RemoveRole(ctx, ch.ID, person.ID, founder.ID)
	require.NoError(t, err)
	require.True(t, removed)

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/channels", cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.NotContains(t, body, "Revoked Channel", "a closed channel_person row's Channel must not appear (FR28)")
	// person now has zero open associations -- the empty state, not a
	// stale row, must render.
	assert.Contains(t, body, "No connected Channels yet.")
}

// TestHandleChannels_NoAssociations_RendersEmptyStateWithConnectAction is
// the M1 first-run path (a brand-new signed-in Person): it must not
// regress.
func TestHandleChannels_NoAssociations_RendersEmptyStateWithConnectAction(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "brand-new")
	cookie := s.sessionCookie(t, ctx, person.ID)

	w := s.do(t, http.MethodGet, "/channels", cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "No connected Channels yet.")
	assert.Contains(t, body, `href="/channels/connect"`)
	assert.Contains(t, body, "Connect a Channel")
}

// TestHandleChannels_AnalystOnEveryChannel_HidesConnectAction proves FR25's
// strict reading of #1709's Analyst persona text: a Person whose every
// open role is Analyst sees no "Connect another Channel" action.
func TestHandleChannels_AnalystOnEveryChannel_HidesConnectAction(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "analyst-only")
	founder := s.newPerson(t, ctx, "founder")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Analyst Target Channel", founder.ID)
	require.NoError(t, err)
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/channels", cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "Analyst Target Channel")
	assert.NotContains(t, body, "Connect another Channel",
		"an Analyst-only Person must not see Connect another Channel (FR25)")
}

func TestHandleChannels_UnauthenticatedRedirectsToLogin(t *testing.T) {
	s := newChannelsTestStack(t)

	w := s.do(t, http.MethodGet, "/channels", nil)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
}

// ── NFR9: query count does not grow with Channel count ──────────────────────

// channelsQueryCounter is a pgx.QueryTracer that counts every SQL
// statement issued through the pool it's attached to -- mirrors
// store_integration_test.go's queryCounter (#1716).
type channelsQueryCounter struct {
	n int64
}

func (c *channelsQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n++
	return ctx
}

func (c *channelsQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// tracedChannelsStack builds a second app/router against the same
// database as s, but through a pool whose every query is counted by
// counter.
func (s *channelsTestStack) tracedChannelsStack(t *testing.T, ctx context.Context, counter *channelsQueryCounter) *channelsTestStack {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(s.db.ConnString)
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = counter

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	st := store.New(pool)
	sessions := auth.NewSessionManager(pool, channelsTestCookieName, "session-secret", channelsTestEncKey())
	authenticator := auth.NewForTests(st.Persons(), sessions)
	application := &app{store: st, auth: authenticator}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels", authenticator.RequireSignedIn(application.handleChannels))
	mux.HandleFunc("GET /channels/{id}", authenticator.RequireSignedIn(application.handleChannelDetail))

	return &channelsTestStack{app: application, store: st, sessions: sessions, router: mux, db: s.db}
}

// TestHandleChannels_IssuesBoundedQueries proves NFR9 for the web page
// itself, on top of #1716's own store-level proof: the total SQL
// statement count for GET /channels is the same for a Person on 2
// Channels as for a Person on 10.
func TestHandleChannels_IssuesBoundedQueries(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	makePerson := func(channelCount int) store.Person {
		p := s.newPerson(t, ctx, "p-"+uuid.NewString())
		for i := 0; i < channelCount; i++ {
			founder := s.newPerson(t, ctx, "f-"+uuid.NewString())
			ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel "+uuid.NewString(), founder.ID)
			require.NoError(t, err)
			require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, p.ID, store.RoleAnalyst, founder.ID))
		}
		return p
	}

	fewPerson := makePerson(2)
	manyPerson := makePerson(10)

	fewCounter := &channelsQueryCounter{}
	fewStack := s.tracedChannelsStack(t, ctx, fewCounter)
	fewCookie := fewStack.sessionCookie(t, ctx, fewPerson.ID)
	w := fewStack.do(t, http.MethodGet, "/channels", fewCookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	manyCounter := &channelsQueryCounter{}
	manyStack := s.tracedChannelsStack(t, ctx, manyCounter)
	manyCookie := manyStack.sessionCookie(t, ctx, manyPerson.ID)
	w = manyStack.do(t, http.MethodGet, "/channels", manyCookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, fewCounter.n, manyCounter.n,
		"GET /channels must issue the same number of SQL statements regardless of Channel count (NFR9); 2 Channels issued %d, 10 issued %d", fewCounter.n, manyCounter.n)
}

// ── the switcher grants nothing on its own ──────────────────────────────────

// TestHandleChannelDetail_NoRoleOnChannel_Forbidden proves the switcher
// introduces no session-held "current channel": GET /channels/{id} still
// re-authorizes independently and rejects a Person with no role on that
// Channel.
func TestHandleChannelDetail_NoRoleOnChannel_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	founder := s.newPerson(t, ctx, "founder")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Someone Else's Channel", founder.ID)
	require.NoError(t, err)

	outsider := s.newPerson(t, ctx, "outsider")
	cookie := s.sessionCookie(t, ctx, outsider.ID)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String(), cookie)
	assert.Equal(t, http.StatusForbidden, w.Code, "a Person with no open role on the Channel must be rejected, body: %s", w.Body.String())
}
