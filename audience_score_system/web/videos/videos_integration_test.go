//go:build integration

// videos_integration_test.go covers `web/videos`'s HTTP surface against
// `synced_video`/`video_metrics`/`video_schedule_match` (capability C21,
// issue #2031, milestone re-anchor of #1960's FR27-FR30): HandleList's
// Founder/Co-Creator/Analyst read (same rows for all three, store.CanRead
// admits all three), negatives asserted in the same load-bearing order the
// sibling web packages use (non-member 403, unknown Channel 404 -- must
// 404 BEFORE the 403 path, malformed {id} 400, signed-out 401 via a direct
// handler call bypassing RequireSignedIn's redirect), FR27's published-
// only listing with latest-metrics-wins and an explicit no-metrics
// placeholder (never a 500), FR28's title/date-range filters (case-
// insensitive substring, inclusive boundary-equal timestamps), FR29's
// load-bearing binary sync-status bucket across all six
// video_schedule_match shapes (no row, pending without a candidate,
// pending with a candidate, rejected, auto, confirmed), FR30's combined
// filters and empty-state 200, NFR3's bounded query count, and a cross-
// Channel guard.
//
// See //audience_score_system/web/outcomes:outcomes_integration_test for
// the harness/fixture patterns this file follows -- a throwaway Postgres
// via dbtest, the domain's real embedded migrations, a real *store.Store/
// *auth.SessionManager wired into a router equivalent to `web`'s main.go
// route registration for GET /channels/{id}/videos, and every fixture
// built through the store rather than raw SQL (LB5).
//
// A signed-in caller is simulated via auth.NewForTests + SessionManager.
// Establish, mirroring outcomes_integration_test.go's/
// research_integration_test.go's rationale: HandleLogin/HandleCallback are
// already covered by web/auth's own tests, so establishing a real session
// row directly here proves everything this package's own route owns, not
// auth's OAuth mechanics a second time.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/web/videos:videos_integration_test --test_output=all
package videos_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
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
	"github.com/whale-net/everything/audience_score_system/web/videos"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "test_ass_videos_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("videos-integration-test-key"))
}

// videosTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), the
// videos.Handlers under test (exposed directly for the signed-out
// direct-call test below), and a router that mirrors main.go's videos
// route wiring (see this file's package doc comment).
type videosTestStack struct {
	store    *store.Store
	db       *dbtest.Postgres
	sessions *auth.SessionManager
	handlers *videos.Handlers
	router   http.Handler
}

// newVideosTestStack provisions dbtest Postgres, applies the domain's real
// embedded migrations, and wires a real store.Store/auth.SessionManager/
// videos.Handlers into a router equivalent to main.go's setupRoutes for
// GET /channels/{id}/videos (#2031).
func newVideosTestStack(t *testing.T) *videosTestStack {
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
	v := videos.New(st)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/videos", a.RequireSignedIn(v.HandleList))

	return &videosTestStack{store: st, db: db, sessions: sessions, handlers: v, router: mux}
}

// setupChannel creates a Channel with a live creator (Founder), mirroring
// outcomes_integration_test.go's/research_integration_test.go's
// setupChannel fixture.
func (s *videosTestStack) setupChannel(t *testing.T, ctx context.Context) (store.Channel, store.Person) {
	t.Helper()
	creator, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-creator-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Test Channel", creator.ID)
	require.NoError(t, err)
	return ch, creator
}

// newPerson creates a fresh, role-less Person.
func (s *videosTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// sessionCookie establishes a real session row for personID and returns
// the resulting cookie, standing in for a completed sign-in (see this
// file's package doc comment).
func (s *videosTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
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

func (s *videosTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// addSyncedVideo upserts a single SyncedVideo with title/publishedAt on ch
// and returns it, resolved back through ListSchedule (UpsertVideos itself
// returns nothing) -- mirrors outcomes_integration_test.go's identically-
// named/documented helper, but takes publishedAt as a *time.Time directly
// (nil is a legitimate "not yet published" fixture -- FR27's "unpublished
// video never appears" case needs exactly that).
func (s *videosTestStack) addSyncedVideo(t *testing.T, ctx context.Context, ch store.Channel, title string, publishedAt *time.Time) store.SyncedVideo {
	t.Helper()
	require.NoError(t, s.store.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-" + uuid.NewString(), Title: title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: publishedAt, LastSyncedAt: time.Now(),
	}}))
	synced, _, err := s.store.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	for _, v := range synced {
		if v.Title == title {
			return v
		}
	}
	t.Fatalf("must have found just-inserted synced video %q", title)
	return store.SyncedVideo{}
}

// publishedNow returns a *time.Time an hour in the past -- the common
// "just published" fixture shape most tests in this file need, distinct
// from the FR28 boundary tests' explicit timestamps.
func publishedNow() *time.Time {
	t := time.Now().Add(-time.Hour)
	return &t
}

// addMetricsAt records one video_metrics snapshot for video at measuredAt
// with the given views -- mirrors outcomes_integration_test.go's addMetrics
// but takes an explicit measuredAt so FR27's "two metrics rows, latest
// wins" case can control ordering deterministically.
func (s *videosTestStack) addMetricsAt(t *testing.T, ctx context.Context, video store.SyncedVideo, views int64, measuredAt time.Time) {
	t.Helper()
	require.NoError(t, s.store.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: &views, MeasuredAt: measuredAt,
	}}))
}

// proposedVideoScript builds a full Idea -> viable Verdict -> Strategy ->
// Propose chain on ch and returns the resulting (unreleased) VideoScript's
// ID -- FR29's "pending with a candidate" match shape needs a real
// video_script_id to point at, but neither greenlighting nor any other
// script status matters for that fixture (MatchStore.Resolve/Record apply
// no status filter to video_script_id, match.go's doc comments).
func (s *videosTestStack) proposedVideoScript(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, title string) uuid.UUID {
	t.Helper()
	idea, err := s.store.Ideas().Create(ctx, ch.ID, title, creator.ID)
	require.NoError(t, err)
	v, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: title + " looks strong", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	strat, err := s.store.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: title + " Strategy", Active: true,
		VerdictIDs: []uuid.UUID{v.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	script, err := s.store.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: v.ID, StrategyID: strat.ID,
		Title: title, ScriptText: "script text for " + title, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	return script.ID
}

// recordAutoMatch records a live 'auto' video_schedule_match linking video
// to scriptID -- mirrors outcomes_integration_test.go's identically-named
// helper.
func (s *videosTestStack) recordAutoMatch(t *testing.T, ctx context.Context, video store.SyncedVideo, scriptID uuid.UUID) {
	t.Helper()
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &scriptID, Confidence: 0.9, State: store.MatchStateAuto,
	}))
}

// recordPendingMatch records a 'pending' video_schedule_match for video,
// optionally carrying a candidate scriptID (nil for FR29's "no candidate
// at all" pending shape, per MatchStore.HasMatch's doc comment).
func (s *videosTestStack) recordPendingMatch(t *testing.T, ctx context.Context, video store.SyncedVideo, scriptID *uuid.UUID) {
	t.Helper()
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: scriptID, Confidence: 0.5, State: store.MatchStatePending,
	}))
}

// findPendingMatchID resolves the pending video_schedule_match id just
// recorded for video, via MatchStore.ListPending -- mirrors
// outcomes_integration_test.go's recordConfirmedMatch/recordRejectedMatch
// inline lookup, factored out since both are needed here.
func (s *videosTestStack) findPendingMatchID(t *testing.T, ctx context.Context, ch store.Channel, video store.SyncedVideo) uuid.UUID {
	t.Helper()
	pending, _, err := s.store.Matches().ListPending(ctx, ch.ID, nil, 0)
	require.NoError(t, err)
	for _, p := range pending {
		if p.SyncedVideoID == video.ID {
			return p.ID
		}
	}
	t.Fatalf("must have found the just-recorded pending match for %s", video.Title)
	return uuid.Nil
}

// recordConfirmedMatch records a pending match against scriptID then
// resolves it with confirm=true, producing a live 'confirmed' state via
// the same path a human's resolve-form submission would (web/matches.
// HandleResolve) -- mirrors outcomes_integration_test.go's identically-
// named/documented helper.
func (s *videosTestStack) recordConfirmedMatch(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, video store.SyncedVideo, scriptID uuid.UUID) {
	t.Helper()
	s.recordPendingMatch(t, ctx, video, &scriptID)
	matchID := s.findPendingMatchID(t, ctx, ch, video)
	require.NoError(t, s.store.Matches().Resolve(ctx, matchID, creator.ID, true, nil))
}

// recordRejectedMatch records a pending match against scriptID then
// resolves it with confirm=false, producing a 'rejected' state -- mirrors
// outcomes_integration_test.go's identically-named/documented helper.
func (s *videosTestStack) recordRejectedMatch(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, video store.SyncedVideo, scriptID uuid.UUID) {
	t.Helper()
	s.recordPendingMatch(t, ctx, video, &scriptID)
	matchID := s.findPendingMatchID(t, ctx, ch, video)
	require.NoError(t, s.store.Matches().Resolve(ctx, matchID, creator.ID, false, nil))
}

// ── Auth: Founder/Co-Creator/Analyst see the same rows ──────────────────

func TestHandleList_FounderCoCreatorAnalyst_SeeSameRows(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.addSyncedVideo(t, ctx, ch, "Shared Published Video", publishedNow())

	coCreator := s.newPerson(t, ctx, "co-creator")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))
	analyst := s.newPerson(t, ctx, "analyst")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	for _, tc := range []struct {
		name   string
		person store.Person
	}{
		{"Founder", creator},
		{"CoCreator", coCreator},
		{"Analyst", analyst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos", s.sessionCookie(t, ctx, tc.person.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "Shared Published Video", "%s must see the same video row", tc.name)
		})
	}
}

// ── Negatives, asserted in the same load-bearing order the sibling web
// packages use: non-member 403, unknown Channel 404 (must 404 BEFORE the
// 403 path), malformed {id} 400, signed-out 401 ─────────────────────────

func TestHandleList_NonMember_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos", s.sessionCookie(t, ctx, outsider.ID))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestHandleList_UnknownChannel_NotFound is this issue's load-bearing
// ordering case: an unknown Channel must 404 BEFORE authorization can turn
// it into a 403 -- breaking the Channel lookup's placement ahead of
// store.CanRead must turn this test red.
func TestHandleList_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+uuid.NewString()+"/videos", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusNotFound, w.Code, "an unknown Channel must 404 before authorization runs, body: %s", w.Body.String())
}

func TestHandleList_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/not-a-uuid/videos", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleList_NotSignedIn_Unauthorized calls HandleList directly,
// bypassing the router's RequireSignedIn wrapper (which redirects an
// unauthenticated request to /login rather than 401ing it) -- proving
// HandleList's own defensive auth.PersonFromContext check, mirroring
// outcomes_integration_test.go's/research_integration_test.go's
// TestHandleList_NotSignedIn_Unauthorized.
func TestHandleList_NotSignedIn_Unauthorized(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	req := httptest.NewRequest(http.MethodGet, "/channels/"+ch.ID.String()+"/videos", nil)
	req.SetPathValue("id", ch.ID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleList(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ── FR27: published-only listing, latest metrics wins, no-metrics
// placeholder ────────────────────────────────────────────────────────────

func TestHandleList_FR27_UnpublishedVideo_NeverAppears(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.addSyncedVideo(t, ctx, ch, "Unpublished Draft Video", nil)
	s.addSyncedVideo(t, ctx, ch, "Published Companion Video", publishedNow())

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.NotContains(t, body, "Unpublished Draft Video", "a video with published_at IS NULL must never appear (FR27)")
	assert.Contains(t, body, "Published Companion Video", "a published video must still appear")
}

func TestHandleList_FR27_TwoMetricsRows_LatestWins(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	video := s.addSyncedVideo(t, ctx, ch, "Two Metrics Video", publishedNow())
	s.addMetricsAt(t, ctx, video, 10, time.Now().Add(-2*time.Hour))
	s.addMetricsAt(t, ctx, video, 99, time.Now().Add(-1*time.Hour))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "99 views", "the LATEST (by measured_at) metrics row must render")
	assert.NotContains(t, body, "10 views", "an older metrics row must never render once a newer one exists")
}

func TestHandleList_FR27_NoMetrics_RendersPlaceholderNot500(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.addSyncedVideo(t, ctx, ch, "No Metrics Yet Video", publishedNow())

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "a video with no metrics must render a placeholder, never a 500, body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "No Metrics Yet Video")
	assert.Contains(t, body, "No metrics recorded yet.", "FR27's explicit placeholder must render, never a zero value presented as data")
	assert.NotContains(t, body, "0 views", "a missing metrics row must never render as a zero-valued views count")
}

// ── FR28: title substring (case-insensitive) and publish-date range
// (inclusive, boundary-equal) filters ────────────────────────────────────

func TestHandleList_FR28_TitleSubstring_CaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.addSyncedVideo(t, ctx, ch, "Amazing Cats Compilation", publishedNow())
	s.addSyncedVideo(t, ctx, ch, "Boring Dogs Compilation", publishedNow())

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos?title=amazing", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Amazing Cats Compilation", "a lowercase substring must match a differently-cased title (FR28)")
	assert.NotContains(t, body, "Boring Dogs Compilation", "a non-matching title must be excluded")
}

// TestHandleList_FR28_PublishDateRange_InclusiveBoundaryEqual is this
// issue's load-bearing FR28 case: a video published EXACTLY at the from
// boundary and one published EXACTLY at the last instant of the to
// boundary day must both be included (inclusive on both ends), while a
// video one second before the from boundary and one exactly at the start
// of the day AFTER the to boundary must both be excluded.
func TestHandleList_FR28_PublishDateRange_InclusiveBoundaryEqual(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	dayStart := time.Date(2024, time.June, 15, 0, 0, 0, 0, time.UTC)
	dayEnd := time.Date(2024, time.June, 15, 23, 59, 59, 0, time.UTC)
	beforeDay := dayStart.Add(-time.Second)
	afterDay := dayStart.AddDate(0, 0, 1)

	atStart := s.addSyncedVideo(t, ctx, ch, "At Day Start Video", &dayStart)
	_ = atStart
	atEnd := s.addSyncedVideo(t, ctx, ch, "At Day End Video", &dayEnd)
	_ = atEnd
	before := s.addSyncedVideo(t, ctx, ch, "Before Day Video", &beforeDay)
	_ = before
	after := s.addSyncedVideo(t, ctx, ch, "After Day Video", &afterDay)
	_ = after

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos?from=2024-06-15&to=2024-06-15", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "At Day Start Video", "a video published exactly at the from boundary must be included (inclusive)")
	assert.Contains(t, body, "At Day End Video", "a video published at the last instant of the to boundary day must be included (inclusive)")
	assert.NotContains(t, body, "Before Day Video", "a video published one second before the from boundary must be excluded")
	assert.NotContains(t, body, "After Day Video", "a video published on the day after the to boundary must be excluded")
}

// ── FR29 (load-bearing): binary sync-status bucket across all six
// video_schedule_match shapes ────────────────────────────────────────────

// TestHandleList_FR29_SixMatchStates_SyncedBucketing is this issue's
// load-bearing FR29 case: one video per match-state shape -- no match row
// at all, pending without a candidate, pending with a candidate, rejected,
// auto, and confirmed. Synced=true must return EXACTLY the auto and
// confirmed videos; Synced=false must return EXACTLY the other four.
// Breaking the bucketing rule (e.g. treating a pending-with-candidate row
// as synced) must turn this test red.
func TestHandleList_FR29_SixMatchStates_SyncedBucketing(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	noMatchVideo := s.addSyncedVideo(t, ctx, ch, "No Match Video", publishedNow())

	pendingNoCandidateVideo := s.addSyncedVideo(t, ctx, ch, "Pending No Candidate Video", publishedNow())
	s.recordPendingMatch(t, ctx, pendingNoCandidateVideo, nil)

	pendingCandidateScript := s.proposedVideoScript(t, ctx, ch, creator, "Pending Candidate Idea")
	pendingCandidateVideo := s.addSyncedVideo(t, ctx, ch, "Pending Candidate Video", publishedNow())
	s.recordPendingMatch(t, ctx, pendingCandidateVideo, &pendingCandidateScript)

	rejectedScript := s.proposedVideoScript(t, ctx, ch, creator, "Rejected Idea")
	rejectedVideo := s.addSyncedVideo(t, ctx, ch, "Rejected Video", publishedNow())
	s.recordRejectedMatch(t, ctx, ch, creator, rejectedVideo, rejectedScript)

	autoScript := s.proposedVideoScript(t, ctx, ch, creator, "Auto Idea")
	autoVideo := s.addSyncedVideo(t, ctx, ch, "Auto Video", publishedNow())
	s.recordAutoMatch(t, ctx, autoVideo, autoScript)

	confirmedScript := s.proposedVideoScript(t, ctx, ch, creator, "Confirmed Idea")
	confirmedVideo := s.addSyncedVideo(t, ctx, ch, "Confirmed Video", publishedNow())
	s.recordConfirmedMatch(t, ctx, ch, creator, confirmedVideo, confirmedScript)

	_ = noMatchVideo

	// Unfiltered: all six must appear.
	wAll := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, wAll.Code, "body: %s", wAll.Body.String())
	bodyAll := wAll.Body.String()
	for _, title := range []string{"No Match Video", "Pending No Candidate Video", "Pending Candidate Video", "Rejected Video", "Auto Video", "Confirmed Video"} {
		assert.Contains(t, bodyAll, title, "unfiltered listing must include %q", title)
	}

	// Synced=true: exactly auto + confirmed.
	wSynced := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos?synced=true", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, wSynced.Code, "body: %s", wSynced.Body.String())
	bodySynced := wSynced.Body.String()
	assert.Contains(t, bodySynced, "Auto Video", "synced=true must include the auto-matched video")
	assert.Contains(t, bodySynced, "Confirmed Video", "synced=true must include the confirmed video")
	for _, title := range []string{"No Match Video", "Pending No Candidate Video", "Pending Candidate Video", "Rejected Video"} {
		assert.NotContains(t, bodySynced, title, "synced=true must exclude %q", title)
	}

	// Synced=false: exactly the other four.
	wUnsynced := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos?synced=false", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, wUnsynced.Code, "body: %s", wUnsynced.Body.String())
	bodyUnsynced := wUnsynced.Body.String()
	for _, title := range []string{"No Match Video", "Pending No Candidate Video", "Pending Candidate Video", "Rejected Video"} {
		assert.Contains(t, bodyUnsynced, title, "synced=false must include %q", title)
	}
	for _, title := range []string{"Auto Video", "Confirmed Video"} {
		assert.NotContains(t, bodyUnsynced, title, "synced=false must exclude %q", title)
	}
}

// ── FR30: combined filters (intersection) and empty-state 200 ───────────

func TestHandleList_FR30_CombinedFilters_ReturnIntersection(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	inRangeDate := time.Date(2024, time.March, 10, 12, 0, 0, 0, time.UTC)
	outOfRangeDate := time.Date(2024, time.January, 10, 12, 0, 0, 0, time.UTC)

	// Matches title AND date range AND sync status (unsynced) -- must appear.
	matchAll := s.addSyncedVideo(t, ctx, ch, "Combo Match Video", &inRangeDate)
	_ = matchAll

	// Matches title AND date range but IS synced -- must be excluded by the
	// sync-status leg of the intersection.
	syncedScript := s.proposedVideoScript(t, ctx, ch, creator, "Combo Synced Idea")
	comboSyncedVideo := s.addSyncedVideo(t, ctx, ch, "Combo Synced Video", &inRangeDate)
	s.recordAutoMatch(t, ctx, comboSyncedVideo, syncedScript)

	// Matches title and sync status but is OUTSIDE the date range -- must
	// be excluded by the date leg of the intersection.
	s.addSyncedVideo(t, ctx, ch, "Combo Out Of Range Video", &outOfRangeDate)

	// Matches date range and sync status but NOT the title -- must be
	// excluded by the title leg of the intersection.
	s.addSyncedVideo(t, ctx, ch, "Unrelated Title Video", &inRangeDate)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos?title=combo&from=2024-03-01&to=2024-03-31&synced=false", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Combo Match Video", "the row matching all three filters must appear")
	assert.NotContains(t, body, "Combo Synced Video", "a row failing the sync-status leg must be excluded")
	assert.NotContains(t, body, "Combo Out Of Range Video", "a row failing the date-range leg must be excluded")
	assert.NotContains(t, body, "Unrelated Title Video", "a row failing the title leg must be excluded")
}

func TestHandleList_FR30_NoMatchingRows_RendersEmptyStateNot200Error(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.addSyncedVideo(t, ctx, ch, "Some Published Video", publishedNow())

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos?title=nonexistent-title-xyz", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "a filter combination matching nothing must render 200, never an error, body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "No published videos match these filters yet", "an explicit empty state must render")
	assert.NotContains(t, body, "Some Published Video", "the non-matching video must not appear")
}

// ── Cross-Channel guard ──────────────────────────────────────────────────

func TestHandleList_CrossChannelVideo_NeverAppears(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	otherCh, otherCreator := s.setupChannel(t, ctx)

	s.addSyncedVideo(t, ctx, ch, "This Channel Video", publishedNow())
	s.addSyncedVideo(t, ctx, otherCh, "Other Channel Video", publishedNow())
	_ = otherCreator

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/videos", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "This Channel Video")
	assert.NotContains(t, body, "Other Channel Video", "a video on a different Channel must never appear")
}

// ── NFR3: bounded query count -- does not grow with video/metrics count ──

// videosQueryCounter is a pgx.QueryTracer that counts every SQL statement
// issued through the pool it's attached to -- mirrors
// channels_integration_test.go's channelsQueryCounter/store_integration_
// test.go's queryCounter (#1716).
type videosQueryCounter struct {
	n int64
}

func (c *videosQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n++
	return ctx
}

func (c *videosQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// tracedVideosStack builds a second app/router against the same database
// as s, but through a pool whose every query is counted by counter --
// mirrors channels_integration_test.go's tracedChannelsStack.
func (s *videosTestStack) tracedVideosStack(t *testing.T, ctx context.Context, counter *videosQueryCounter) *videosTestStack {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(s.db.ConnString)
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = counter

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	st := store.New(pool)
	sessions := auth.NewSessionManager(pool, testCookieName, "session-secret", testEncKey())
	a := auth.NewForTests(st.Persons(), sessions)
	v := videos.New(st)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/videos", a.RequireSignedIn(v.HandleList))

	return &videosTestStack{store: st, db: s.db, sessions: sessions, handlers: v, router: mux}
}

// TestHandleList_IssuesBoundedQueries proves NFR3: the total SQL statement
// count for GET /channels/{id}/videos is the same for a Channel with 2
// published videos (each with metrics and a match) as for one with 10 --
// the whole listing must be answered in one query (a LATERAL join for
// latest metrics, a LEFT JOIN for the sync bucket), never a Go-side loop
// calling LatestMetricsFor/HasMatch once per video.
func TestHandleList_IssuesBoundedQueries(t *testing.T) {
	ctx := context.Background()
	s := newVideosTestStack(t)

	makeChannel := func(videoCount int) (store.Channel, store.Person) {
		ch, creator := s.setupChannel(t, ctx)
		for i := 0; i < videoCount; i++ {
			title := fmt.Sprintf("Bounded Video %02d", i)
			script := s.proposedVideoScript(t, ctx, ch, creator, title+" Idea")
			video := s.addSyncedVideo(t, ctx, ch, title, publishedNow())
			s.addMetricsAt(t, ctx, video, int64(i), time.Now())
			s.recordAutoMatch(t, ctx, video, script)
		}
		return ch, creator
	}

	fewCh, fewCreator := makeChannel(2)
	manyCh, manyCreator := makeChannel(10)

	fewCounter := &videosQueryCounter{}
	fewStack := s.tracedVideosStack(t, ctx, fewCounter)
	fewCookie := fewStack.sessionCookie(t, ctx, fewCreator.ID)
	w := fewStack.do(t, http.MethodGet, "/channels/"+fewCh.ID.String()+"/videos", fewCookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	manyCounter := &videosQueryCounter{}
	manyStack := s.tracedVideosStack(t, ctx, manyCounter)
	manyCookie := manyStack.sessionCookie(t, ctx, manyCreator.ID)
	w = manyStack.do(t, http.MethodGet, "/channels/"+manyCh.ID.String()+"/videos", manyCookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, fewCounter.n, manyCounter.n,
		"GET /channels/{id}/videos must issue the same number of SQL statements regardless of video count (NFR3); 2 videos issued %d, 10 issued %d", fewCounter.n, manyCounter.n)
}
