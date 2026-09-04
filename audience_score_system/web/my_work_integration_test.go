//go:build integration

// Postgres-backed coverage for GET /my-work (M2: FR27/FR28, #1725): one
// section per Channel the signed-in Person holds an open role on, each
// with its own latest research notes/verdict/schedule-state/outcome, no
// cross-Channel bleed, explicit empty-block placeholders for a Channel
// with no data yet, the empty state for a Person with no associations, the
// unauthenticated redirect, and FR28's revoked-role-drops-on-next-load
// behavior against the SAME session cookie. Also proves NFR9: the query
// count for GET /my-work does not grow with Channel count.
//
// Mirrors channels_integration_test.go's harness shape (dbtest Postgres +
// real embedded migrations + a router mirroring main.go's route wiring),
// living in `package main` so it can call the unexported handleMyWork
// directly, exactly like that file does for handleChannels.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/web:web_my_work_integration_test --test_output=all
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

const myWorkTestCookieName = "test_ass_my_work_session"

func myWorkTestEncKey() [32]byte {
	return sha256.Sum256([]byte("my-work-integration-test-key"))
}

// myWorkTestStack bundles a real Store/SessionManager over an isolated
// Postgres (via dbtest) and a router mirroring main.go's setupRoutes for
// GET /my-work.
type myWorkTestStack struct {
	app      *app
	store    *store.Store
	sessions *auth.SessionManager
	router   http.Handler
	db       *dbtest.Postgres
}

func newMyWorkTestStack(t *testing.T) *myWorkTestStack {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply the real embedded schema")

	st := store.New(db.Pool)
	sessions := auth.NewSessionManager(db.Pool, myWorkTestCookieName, "session-secret", myWorkTestEncKey())
	authenticator := auth.NewForTests(st.Persons(), sessions)

	application := &app{store: st, auth: authenticator}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /my-work", authenticator.RequireSignedIn(application.handleMyWork))

	return &myWorkTestStack{app: application, store: st, sessions: sessions, router: mux, db: db}
}

// tracedMyWorkStack builds a second app/router against the same database
// as s, but through a pool whose every query is counted by counter --
// mirrors channels_integration_test.go's tracedChannelsStack.
func (s *myWorkTestStack) tracedMyWorkStack(t *testing.T, ctx context.Context, counter *myWorkQueryCounter) *myWorkTestStack {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(s.db.ConnString)
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = counter

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	st := store.New(pool)
	sessions := auth.NewSessionManager(pool, myWorkTestCookieName, "session-secret", myWorkTestEncKey())
	authenticator := auth.NewForTests(st.Persons(), sessions)
	application := &app{store: st, auth: authenticator}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /my-work", authenticator.RequireSignedIn(application.handleMyWork))

	return &myWorkTestStack{app: application, store: st, sessions: sessions, router: mux, db: s.db}
}

// myWorkQueryCounter is a pgx.QueryTracer that counts every SQL statement
// issued through the pool it's attached to -- mirrors
// channels_integration_test.go's channelsQueryCounter.
type myWorkQueryCounter struct{ n int64 }

func (c *myWorkQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n++
	return ctx
}

func (c *myWorkQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// newPerson creates a fresh, role-less Person.
func (s *myWorkTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// sessionCookie establishes a real session row for personID, standing in
// for a completed sign-in (see channels_integration_test.go's identical
// rationale).
func (s *myWorkTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	require.NoError(t, s.sessions.Establish(ctx, w, personID.String(), ""))
	return findCookie(t, w.Result().Cookies(), myWorkTestCookieName)
}

// findCookie is duplicated (rather than shared) from channels_integration_
// test.go's identical helper: each hand-written `go_test` target here
// compiles its own distinct srcs list (see this package's BUILD.bazel),
// so a helper is not visible across test files unless it lives in
// web_lib itself.
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

func (s *myWorkTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// setupMyWorkOutcomeChain builds one viable Idea on ch with a committed
// schedule entry, a published SyncedVideo, one VideoMetrics snapshot, and
// an "auto" video_schedule_match linking them -- the qualifying-row shape
// the outcome section requires, mirroring store_integration_test.go's
// identically-named helper (#1717's own test coverage) at the web layer.
func (s *myWorkTestStack) setupMyWorkOutcomeChain(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, ideaTitle string) (store.Idea, store.Verdict) {
	t.Helper()

	idea, err := s.store.Ideas().Create(ctx, ch.ID, ideaTitle, creator.ID)
	require.NoError(t, err)

	v, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: ideaTitle + " reasoning", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	entry, err := s.store.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: idea.ID, VerdictID: v.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, s.store.Schedules().Approve(ctx, entry.ID, creator.ID))

	videoTitle := ideaTitle + " Video"
	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, s.store.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-" + uuid.NewString(), Title: videoTitle,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	synced, err := s.store.Sync().ListSchedule(ctx, ch.ID)
	require.NoError(t, err)
	var video store.SyncedVideo
	for _, sv := range synced {
		if sv.Title == videoTitle {
			video = sv
		}
	}
	require.NotEqual(t, uuid.Nil, video.ID, "the just-synced video must be found by title")

	views := int64(500)
	require.NoError(t, s.store.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: &views, MeasuredAt: time.Now(),
	}}))

	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, ScheduleEntryID: &entry.ID, Confidence: 0.9, State: store.MatchStateAuto,
	}))

	return idea, v
}

// ── FR27: per-Channel section content ───────────────────────────────────

func TestHandleMyWork_RendersSectionPerAssociatedChannel_WithAllFourBlocks(t *testing.T) {
	ctx := context.Background()
	s := newMyWorkTestStack(t)

	person := s.newPerson(t, ctx, "person")

	// A: person is Founder -- note + verdict + a draft-only schedule entry,
	// no outcome yet.
	chA, err := s.store.Channels().Create(ctx, "yt-a-"+uuid.NewString(), "Channel A", person.ID)
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chA.ID, Text: "A research note text", AuthorPersonID: person.ID})
	require.NoError(t, err)
	ideaA, err := s.store.Ideas().Create(ctx, chA.ID, "A Idea", person.ID)
	require.NoError(t, err)
	verdictA, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaA.ID, Verdict: store.VerdictViable, Reasoning: "A verdict reasoning", AuthorPersonID: person.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: chA.ID, IdeaID: ideaA.ID, VerdictID: verdictA.ID,
		ProposedPublishAt: time.Now().Add(48 * time.Hour), CreatedByPersonID: person.ID,
	})
	require.NoError(t, err)

	// B: person is Co-Creator, granted by B's own Founder -- full outcome
	// chain (committed schedule entry, published matched video).
	founderB := s.newPerson(t, ctx, "founder-b")
	chB, err := s.store.Channels().Create(ctx, "yt-b-"+uuid.NewString(), "Channel B", founderB.ID)
	require.NoError(t, err)
	require.NoError(t, s.store.Roles().AddRole(ctx, chB.ID, person.ID, store.RoleCoCreator, founderB.ID))
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chB.ID, Text: "B research note text", AuthorPersonID: founderB.ID})
	require.NoError(t, err)
	s.setupMyWorkOutcomeChain(t, ctx, chB, founderB, "B Idea")

	// C: person is Analyst, granted by C's own Founder -- a note only, no
	// verdict/schedule/outcome data at all.
	founderC := s.newPerson(t, ctx, "founder-c")
	chC, err := s.store.Channels().Create(ctx, "yt-c-"+uuid.NewString(), "Channel C", founderC.ID)
	require.NoError(t, err)
	require.NoError(t, s.store.Roles().AddRole(ctx, chC.ID, person.ID, store.RoleAnalyst, founderC.ID))
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chC.ID, Text: "C research note text", AuthorPersonID: founderC.ID})
	require.NoError(t, err)

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/my-work", cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	// Every Channel appears, with its own tier.
	assert.Contains(t, body, "Channel A")
	assert.Contains(t, body, "Channel B")
	assert.Contains(t, body, "Channel C")
	assert.Contains(t, body, "Founder")
	assert.Contains(t, body, "Co-Creator")
	assert.Contains(t, body, "Analyst")

	// A: its own note/verdict/schedule content, no outcome content.
	assert.Contains(t, body, "A research note text")
	assert.Contains(t, body, "A verdict reasoning")
	assert.Contains(t, body, "1 draft, 0 committed")

	// B: its own note/schedule/outcome content.
	assert.Contains(t, body, "B research note text")
	assert.Contains(t, body, "0 draft, 1 committed")
	assert.Contains(t, body, "B Idea Video")
	assert.Contains(t, body, "500 views")

	// C: its own note content, and its empty-block placeholders.
	assert.Contains(t, body, "C research note text")

	// No cross-Channel bleed: A's note text must not appear anywhere near
	// B or C's sections and vice versa (content isolation).
	assert.NotContains(t, body, "A research note text\n\t\t\t\t\t\tB research note text", "sanity: sections must not be concatenated without their own headers")

	// Links into each Channel's own detail page.
	assert.Contains(t, body, `href="/channels/`+chA.ID.String()+`"`)
	assert.Contains(t, body, `href="/channels/`+chB.ID.String()+`"`)
	assert.Contains(t, body, `href="/channels/`+chC.ID.String()+`"`)

	// No access-management or audit affordance anywhere on this page.
	assert.NotContains(t, body, "/access", "the my-work page must show no access-management affordance for any tier")
}

// TestHandleMyWork_ChannelWithNoContent_RendersEmptyPlaceholders proves a
// freshly connected Channel with no notes/verdict/schedule/outcome yet
// still renders its section, with explicit "none yet" placeholders rather
// than omitting any block.
func TestHandleMyWork_ChannelWithNoContent_RendersEmptyPlaceholders(t *testing.T) {
	ctx := context.Background()
	s := newMyWorkTestStack(t)

	founder := s.newPerson(t, ctx, "founder")
	_, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Empty Channel", founder.ID)
	require.NoError(t, err)

	cookie := s.sessionCookie(t, ctx, founder.ID)
	w := s.do(t, http.MethodGet, "/my-work", cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Empty Channel", "an empty Channel must still render its own section, not be omitted")
	assert.Contains(t, body, "No research notes yet.")
	assert.Contains(t, body, "No viability verdict recorded yet.")
	assert.Contains(t, body, "No schedule drafts yet.")
	assert.Contains(t, body, "No outcome comparison yet.")
}

// TestHandleMyWork_RevokedRoleRemovesSectionOnNextLoad proves FR28: load,
// revoke the role, reload with the SAME session cookie -- the section is
// gone and the response is still 200, with no re-login.
func TestHandleMyWork_RevokedRoleRemovesSectionOnNextLoad(t *testing.T) {
	ctx := context.Background()
	s := newMyWorkTestStack(t)

	person := s.newPerson(t, ctx, "person")
	founder := s.newPerson(t, ctx, "founder")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Revocable Channel", founder.ID)
	require.NoError(t, err)
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))

	cookie := s.sessionCookie(t, ctx, person.ID)

	before := s.do(t, http.MethodGet, "/my-work", cookie)
	require.Equal(t, http.StatusOK, before.Code, "body: %s", before.Body.String())
	assert.Contains(t, before.Body.String(), "Revocable Channel", "the section must appear while the role is open")

	removed, err := s.store.Roles().RemoveRole(ctx, ch.ID, person.ID, founder.ID)
	require.NoError(t, err)
	require.True(t, removed)

	after := s.do(t, http.MethodGet, "/my-work", cookie)
	assert.Equal(t, http.StatusOK, after.Code, "the same session cookie must still be valid, body: %s", after.Body.String())
	assert.NotContains(t, after.Body.String(), "Revocable Channel", "a revoked role must drop the Channel's section on the very next load (FR28)")
}

// TestHandleMyWork_NoAssociations_RendersEmptyState is the brand-new,
// role-less Person path.
func TestHandleMyWork_NoAssociations_RendersEmptyState(t *testing.T) {
	ctx := context.Background()
	s := newMyWorkTestStack(t)

	person := s.newPerson(t, ctx, "brand-new")
	cookie := s.sessionCookie(t, ctx, person.ID)

	w := s.do(t, http.MethodGet, "/my-work", cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "You have no associated Channels yet.")
}

func TestHandleMyWork_UnauthenticatedRedirectsToLogin(t *testing.T) {
	s := newMyWorkTestStack(t)

	w := s.do(t, http.MethodGet, "/my-work", nil)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
}

// ── NFR9: query count does not grow with Channel count ──────────────────

// TestHandleMyWork_IssuesBoundedQueries proves NFR9 for the web page
// itself, on top of #1717's own store-level proof: the total SQL
// statement count for GET /my-work is the same for a Person on 2 Channels
// as for a Person on 10.
func TestHandleMyWork_IssuesBoundedQueries(t *testing.T) {
	ctx := context.Background()
	s := newMyWorkTestStack(t)

	makePerson := func(channelCount int) store.Person {
		p := s.newPerson(t, ctx, "p-"+uuid.NewString())
		for i := 0; i < channelCount; i++ {
			founder := s.newPerson(t, ctx, "f-"+uuid.NewString())
			ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel "+uuid.NewString(), founder.ID)
			require.NoError(t, err)
			require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, p.ID, store.RoleAnalyst, founder.ID))
			_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, Text: "note", AuthorPersonID: founder.ID})
			require.NoError(t, err)
		}
		return p
	}

	fewPerson := makePerson(2)
	manyPerson := makePerson(10)

	fewCounter := &myWorkQueryCounter{}
	fewStack := s.tracedMyWorkStack(t, ctx, fewCounter)
	fewCookie := fewStack.sessionCookie(t, ctx, fewPerson.ID)
	w := fewStack.do(t, http.MethodGet, "/my-work", fewCookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	manyCounter := &myWorkQueryCounter{}
	manyStack := s.tracedMyWorkStack(t, ctx, manyCounter)
	manyCookie := manyStack.sessionCookie(t, ctx, manyPerson.ID)
	w = manyStack.do(t, http.MethodGet, "/my-work", manyCookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, fewCounter.n, manyCounter.n,
		"GET /my-work must issue the same number of SQL statements regardless of Channel count (NFR9); 2 Channels issued %d, 10 issued %d", fewCounter.n, manyCounter.n)
}
