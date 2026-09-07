//go:build integration

// outcomes_integration_test.go covers `web/outcomes`'s HTTP surface
// against `idea`/`video_script`/`viability_verdict`/
// `video_schedule_match`/`synced_video`/`video_metrics` (milestone M4.3,
// FR1/FR2/FR11/NFR2, issue #1928): HandleList's Founder/Co-Creator/
// Analyst read (same rows for all three, store.CanRead admits all
// three), negatives asserted in the load-bearing order this issue's body
// names -- non-member 403, unknown Channel 404 (must 404 BEFORE the 403
// path), malformed {id} 400, signed-out 401 -- LB3's load-bearing case (a
// script bound to verdict version 1 renders v1's verdict value/reasoning
// even after a newer version 2 is appended to the same Idea), an archived
// script with a live match still rendering (FR40), auto and confirmed
// match states rendering distinguishably while pending/rejected never
// appear, a video with no metrics row never appearing at all (the store's
// inner LATERAL join excludes it -- asserted here so the empty-state
// pointer stays accurate), NFR2's 26-row/25-rendered truncation with a
// static note and no paging control, and FR2's message-plus-pointer empty
// state with a working link to /channels/{id}/matches.
//
// See //audience_score_system/web/matches:matches_integration_test and
// //audience_score_system/mcp/tools:browse_integration_test for the
// harness/fixture patterns this file follows -- a throwaway Postgres via
// dbtest, the domain's real embedded migrations, a real *store.Store/
// *auth.SessionManager wired into a router equivalent to `web`'s main.go
// route registration for GET /channels/{id}/outcomes, and a real
// Idea -> viability_verdict -> Strategy -> greenlit-or-archived
// VideoScript -> published SyncedVideo -> VideoMetrics ->
// VideoScheduleMatch chain built directly through the store rather than
// re-deriving it from SQL (LB5: this file never issues its own SQL
// against those tables).
//
// A signed-in caller is simulated via auth.NewForTests + SessionManager.
// Establish, mirroring matches_integration_test.go's/
// research_integration_test.go's rationale: HandleLogin/HandleCallback
// are already covered by web/auth's own tests, so establishing a real
// session row directly here proves everything this package's own route
// owns, not auth's OAuth mechanics a second time.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/web/outcomes:outcomes_integration_test --test_output=all
package outcomes_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/outcomes"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "test_ass_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("outcomes-integration-test-key"))
}

// outcomesTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), the
// outcomes.Handlers under test (exposed directly for the signed-out
// direct-call test below), and a router that mirrors main.go's outcomes
// route wiring (see this file's package doc comment).
type outcomesTestStack struct {
	store    *store.Store
	db       *dbtest.Postgres
	sessions *auth.SessionManager
	handlers *outcomes.Handlers
	router   http.Handler
}

// newOutcomesTestStack provisions dbtest Postgres, applies the domain's
// real embedded migrations, and wires a real store.Store/auth.
// SessionManager/outcomes.Handlers into a router equivalent to main.go's
// setupRoutes for this package's two routes: GET /channels/{id}/outcomes
// (#1928) and this task's (#1929) POST /channels/{id}/outcome-bar.
func newOutcomesTestStack(t *testing.T) *outcomesTestStack {
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
	o := outcomes.New(st)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/outcomes", a.RequireSignedIn(o.HandleList))
	mux.HandleFunc("POST /channels/{id}/outcome-bar", a.RequireSignedIn(o.HandleSetOutcomeBar))

	return &outcomesTestStack{store: st, db: db, sessions: sessions, handlers: o, router: mux}
}

// setupChannel creates a Channel with a live creator (Founder), mirroring
// matches_integration_test.go's/research_integration_test.go's
// setupChannel fixture.
func (s *outcomesTestStack) setupChannel(t *testing.T, ctx context.Context) (store.Channel, store.Person) {
	t.Helper()
	creator, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-creator-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Test Channel", creator.ID)
	require.NoError(t, err)
	return ch, creator
}

// newPerson creates a fresh, role-less Person.
func (s *outcomesTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// sessionCookie establishes a real session row for personID and returns
// the resulting cookie, standing in for a completed sign-in (see this
// file's package doc comment).
func (s *outcomesTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
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

func (s *outcomesTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// doForm POSTs an application/x-www-form-urlencoded body through the
// router, mirroring what a rendered set-outcome-bar <form method="post">
// would submit -- HandleSetOutcomeBar's r.ParseForm() reads it exactly
// like a real browser submission (this issue's FR5), same pattern as
// web/research's/web/matches's identically-named/documented doForm.
func (s *outcomesTestStack) doForm(t *testing.T, target string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
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

// greenlitVideoScript builds a full Idea -> viable Verdict -> Strategy ->
// Propose -> Greenlight chain on ch (FR36/FR37), mirroring
// matches_integration_test.go's/mcp/tools/matches_integration_test.go's
// identically-named helper (separate go_test target/compilation unit, so
// it cannot reuse those packages' copies). Returns the greenlit
// VideoScript plus the Verdict version it is bound to (LB3).
func (s *outcomesTestStack) greenlitVideoScript(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, title string) (store.VideoScript, store.Verdict) {
	t.Helper()

	idea, err := s.store.Ideas().Create(ctx, ch.ID, title, creator.ID)
	require.NoError(t, err)
	v, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: title + " looks strong (v1)", AuthorPersonID: creator.ID,
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
	require.NoError(t, s.store.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	return got, v
}

// addSyncedVideo upserts a single published SyncedVideo with title on ch
// and returns it, resolved back through ListSchedule (UpsertVideos itself
// returns nothing) -- mirrors matches_integration_test.go's identically-
// named/documented helper.
func (s *outcomesTestStack) addSyncedVideo(t *testing.T, ctx context.Context, ch store.Channel, title string) store.SyncedVideo {
	t.Helper()
	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, s.store.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-" + uuid.NewString(), Title: title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
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

// addMetrics records one video_metrics snapshot for video -- required for
// a row to qualify for PredictionVsOutcome's inner LATERAL join (see
// TestHandleList_VideoWithNoMetrics_NeverAppears below, which deliberately
// omits this call).
func (s *outcomesTestStack) addMetrics(t *testing.T, ctx context.Context, video store.SyncedVideo, views int64) {
	t.Helper()
	require.NoError(t, s.store.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: &views, MeasuredAt: time.Now(),
	}}))
}

// addSyncedVideoAt is addSyncedVideo with an explicit publishedAt, so
// calibration-trend fixtures (this task, #1929) can place a candidate's
// video into a specific calendar month -- CalibrationStore.MonthlyTrend
// buckets by sv.published_at (store/calibration.go), so addSyncedVideo's
// own fixed "now minus an hour" can never exercise more than a single
// bucket.
func (s *outcomesTestStack) addSyncedVideoAt(t *testing.T, ctx context.Context, ch store.Channel, title string, publishedAt time.Time) store.SyncedVideo {
	t.Helper()
	require.NoError(t, s.store.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-" + uuid.NewString(), Title: title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
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

// addMetricsPtr is addMetrics but taking a *int64 views value directly --
// FR4's load-bearing "views IS NULL counts as miscalibrated" case (this
// task's Testing section) needs a metrics snapshot with NO recorded views
// value, which addMetrics's int64 signature cannot express.
func (s *outcomesTestStack) addMetricsPtr(t *testing.T, ctx context.Context, video store.SyncedVideo, views *int64) {
	t.Helper()
	require.NoError(t, s.store.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: views, MeasuredAt: time.Now(),
	}}))
}

// buildCalibrationCandidate builds one full FR3 calibration candidate on
// ch, published in publishedAt's calendar month with the given views
// (nil is a legitimate "no views value recorded" snapshot -- FR4's
// NULL-is-a-miss case): a greenlit VideoScript bound to a viable verdict,
// a published SyncedVideo, a video_metrics snapshot, and a confirmed
// match -- the same candidate shape store/calibration_integration_test.go's
// calibrationCandidate helper builds, reproduced here since this file
// cannot import that unexported helper from a different go_test
// compilation unit (store_test, not outcomes_test).
func (s *outcomesTestStack) buildCalibrationCandidate(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, title string, publishedAt time.Time, views *int64) {
	t.Helper()
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, title)
	video := s.addSyncedVideoAt(t, ctx, ch, title, publishedAt)
	s.addMetricsPtr(t, ctx, video, views)
	s.recordConfirmedMatch(t, ctx, ch, creator, video, script.ID, 0.9)
}

// setOutcomeBar upserts ch's outcome bar directly via
// store.OutcomeBarStore (bypassing the HTTP form) -- used to set up FR3/
// FR4 fixtures; FR5's own write path is exercised separately through
// doForm/HandleSetOutcomeBar.
func (s *outcomesTestStack) setOutcomeBar(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, threshold float64) store.OutcomeBar {
	t.Helper()
	b, err := s.store.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: threshold, UpdatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	return b
}

// countOutcomeBarRows queries `outcome_bar` directly (raw SQL, not
// through OutcomeBarStore, which exposes no count/list method) -- FR6's
// double-submit assertion (this task's Testing section) needs to prove
// EXACTLY one row exists for channelID, not merely that GetByChannel
// still succeeds.
func (s *outcomesTestStack) countOutcomeBarRows(t *testing.T, ctx context.Context, channelID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM outcome_bar WHERE channel_id = $1`, channelID).Scan(&n))
	return n
}

// recordAutoMatch records a live 'auto' video_schedule_match linking video
// to scriptID.
func (s *outcomesTestStack) recordAutoMatch(t *testing.T, ctx context.Context, video store.SyncedVideo, scriptID uuid.UUID, confidence float64) {
	t.Helper()
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &scriptID, Confidence: confidence, State: store.MatchStateAuto,
	}))
}

// recordConfirmedMatch records a pending match then resolves it with
// confirm=true, producing a live 'confirmed' state via the same path a
// human's resolve-form submission would (web/matches.HandleResolve,
// issue #1927) rather than writing 'confirmed' directly -- proving
// HandleList renders whatever MatchStore.Resolve actually produces.
func (s *outcomesTestStack) recordConfirmedMatch(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, video store.SyncedVideo, scriptID uuid.UUID, confidence float64) {
	t.Helper()
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &scriptID, Confidence: confidence, State: store.MatchStatePending,
	}))
	pending, _, err := s.store.Matches().ListPending(ctx, ch.ID, nil, 0)
	require.NoError(t, err)
	var matchID uuid.UUID
	for _, p := range pending {
		if p.SyncedVideoID == video.ID {
			matchID = p.ID
		}
	}
	require.NotEqual(t, uuid.Nil, matchID, "must have found the just-recorded pending match for %s", video.Title)
	require.NoError(t, s.store.Matches().Resolve(ctx, matchID, creator.ID, true, nil))
}

// recordRejectedMatch records a pending match then resolves it with
// confirm=false, producing a 'rejected' state -- PredictionVsOutcome's
// join must never surface it (only 'auto'/'confirmed' qualify).
func (s *outcomesTestStack) recordRejectedMatch(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, video store.SyncedVideo, scriptID uuid.UUID) {
	t.Helper()
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &scriptID, Confidence: 0.4, State: store.MatchStatePending,
	}))
	pending, _, err := s.store.Matches().ListPending(ctx, ch.ID, nil, 0)
	require.NoError(t, err)
	var matchID uuid.UUID
	for _, p := range pending {
		if p.SyncedVideoID == video.ID {
			matchID = p.ID
		}
	}
	require.NotEqual(t, uuid.Nil, matchID, "must have found the just-recorded pending match for %s", video.Title)
	require.NoError(t, s.store.Matches().Resolve(ctx, matchID, creator.ID, false, nil))
}

// buildFullOutcomeRow builds one complete, auto-matched, metrics-backed
// qualifying row -- an Idea -> greenlit VideoScript -> published
// SyncedVideo -> VideoMetrics -> 'auto' VideoScheduleMatch chain -- for
// tests that just need N distinguishable rows to exist, not any
// particular match/script state.
func (s *outcomesTestStack) buildFullOutcomeRow(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, title string, views int64) {
	t.Helper()
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, title)
	video := s.addSyncedVideo(t, ctx, ch, title)
	s.addMetrics(t, ctx, video, views)
	s.recordAutoMatch(t, ctx, video, script.ID, 0.9)
}

// ── HandleList (FR1): Founder, Co-Creator, and Analyst all see the same
// rows ────────────────────────────────────────────────────────────────

func TestHandleList_FounderCoCreatorAnalyst_SeeSameRows(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.buildFullOutcomeRow(t, ctx, ch, creator, "Shared Outcome Video", 100)

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
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, tc.person.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "Shared Outcome Video", "%s must see the same outcome row", tc.name)
		})
	}
}

// ── Negatives, asserted in the load-bearing order this issue's body
// names: non-member 403, unknown Channel 404 (must 404 BEFORE the 403
// path), malformed {id} 400, signed-out 401 ─────────────────────────────

func TestHandleList_NonMember_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, outsider.ID))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestHandleList_UnknownChannel_NotFound is this issue's load-bearing
// ordering case: an unknown Channel must 404 BEFORE authorization can turn
// it into a 403 -- breaking the Channel lookup's placement ahead of
// store.CanRead must turn this test red.
func TestHandleList_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+uuid.NewString()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusNotFound, w.Code, "an unknown Channel must 404 before authorization runs, body: %s", w.Body.String())
}

func TestHandleList_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/not-a-uuid/outcomes", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleList_NotSignedIn_Unauthorized calls HandleList directly,
// bypassing the router's RequireSignedIn wrapper (which redirects an
// unauthenticated request to /login rather than 401ing it) -- proving
// HandleList's own defensive auth.PersonFromContext check, mirroring
// matches_integration_test.go's/research_integration_test.go's
// TestHandleList_NotSignedIn_Unauthorized.
func TestHandleList_NotSignedIn_Unauthorized(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	req := httptest.NewRequest(http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", nil)
	req.SetPathValue("id", ch.ID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleList(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ── LB3's load-bearing red/green case: the bound verdict version, never
// the Idea's current verdict ────────────────────────────────────────────

// TestHandleList_BoundVerdictVersion_SurvivesNewerVerdictAppended is this
// issue's load-bearing case: a script bound to verdict version 1, with
// version 2 later appended to the same Idea, must render version 1's
// verdict value/reasoning -- the row must not disappear and must not show
// version 2. Breaking this (e.g. re-deriving the Idea's CURRENT verdict
// instead of reading through the script's bound VerdictID) must turn this
// test red by rendering "v2"/the v2 verdict value instead of "v1"/viable.
func TestHandleList_BoundVerdictVersion_SurvivesNewerVerdictAppended(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, v1 := s.greenlitVideoScript(t, ctx, ch, creator, "Bound Verdict Idea")
	require.Equal(t, 1, v1.Version)
	require.Equal(t, store.VerdictViable, v1.Verdict)

	// Append a newer verdict version to the SAME Idea -- the script's
	// VerdictID must remain bound to v1, never re-derived as "current".
	v2, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: script.IdeaID, Verdict: store.VerdictNotViable, Reasoning: "reconsidered (v2)", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 2, v2.Version)
	require.NotEqual(t, v1.ID, v2.ID)

	video := s.addSyncedVideo(t, ctx, ch, "Bound Verdict Video")
	s.addMetrics(t, ctx, video, 42)
	s.recordAutoMatch(t, ctx, video, script.ID, 0.75)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Bound Verdict Idea", "the idea's title must render")
	assert.Contains(t, body, "Bound Verdict Video", "the video's title must render")
	assert.Contains(t, body, "Bound verdict (v1", "the BOUND verdict version (v1) must render")
	assert.Contains(t, body, "looks strong (v1)", "v1's reasoning must render")
	assert.NotContains(t, body, "Bound verdict (v2", "the Idea's newer verdict version (v2) must never render here -- LB3's bound version, not a moving target")
	assert.NotContains(t, body, "reconsidered (v2)", "v2's reasoning must never leak into this render")
}

// ── FR40: an archived script with a live match still renders ───────────

// TestHandleList_ArchivedScriptWithLiveMatch_StillRenders proves that
// dropping archived scripts would lose exactly the comparison this page
// exists for (FR40) -- the script is archived BEFORE its match/video
// exist (Archive's freeze predicate only fires once a live match to a
// PUBLISHED video exists), then matched afterward, mirroring the
// realistic sequence: a Channel archives a script it no longer plans to
// pursue, then later discovers a video was in fact matched/published
// under it.
func TestHandleList_ArchivedScriptWithLiveMatch_StillRenders(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Archived Outcome Idea")
	require.NoError(t, s.store.VideoScripts().Archive(ctx, script.ID, creator.ID))

	video := s.addSyncedVideo(t, ctx, ch, "Archived Outcome Video")
	s.addMetrics(t, ctx, video, 10)
	s.recordAutoMatch(t, ctx, video, script.ID, 0.8)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Archived Outcome Idea", "an archived script's row must still render (FR40)")
	assert.Contains(t, body, "Archived", "the script's status must render as Archived, not be filtered out")
}

// ── Auto and confirmed match states render distinguishably; pending and
// rejected never appear ─────────────────────────────────────────────────

func TestHandleList_AutoAndConfirmedMatches_Render_PendingAndRejectedNeverAppear(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	autoScript, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Auto Outcome Idea")
	autoVideo := s.addSyncedVideo(t, ctx, ch, "Auto Outcome Video")
	s.addMetrics(t, ctx, autoVideo, 10)
	s.recordAutoMatch(t, ctx, autoVideo, autoScript.ID, 0.9)

	confirmedScript, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Confirmed Outcome Idea")
	confirmedVideo := s.addSyncedVideo(t, ctx, ch, "Confirmed Outcome Video")
	s.addMetrics(t, ctx, confirmedVideo, 20)
	s.recordConfirmedMatch(t, ctx, ch, creator, confirmedVideo, confirmedScript.ID, 0.6)

	pendingScript, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Pending Outcome Idea")
	pendingVideo := s.addSyncedVideo(t, ctx, ch, "Pending Outcome Video")
	s.addMetrics(t, ctx, pendingVideo, 5)
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: pendingVideo.ID, VideoScriptID: &pendingScript.ID, Confidence: 0.5, State: store.MatchStatePending,
	}))

	rejectedScript, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Rejected Outcome Idea")
	rejectedVideo := s.addSyncedVideo(t, ctx, ch, "Rejected Outcome Video")
	s.addMetrics(t, ctx, rejectedVideo, 5)
	s.recordRejectedMatch(t, ctx, ch, creator, rejectedVideo, rejectedScript.ID)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Auto Outcome Video")
	assert.Contains(t, body, "Auto-matched", "an 'auto' match's provenance must render distinguishably")
	assert.Contains(t, body, "Confirmed Outcome Video")
	assert.Contains(t, body, "Confirmed", "a 'confirmed' match's provenance must render distinguishably")
	assert.NotContains(t, body, "Pending Outcome Video", "a pending match must never appear")
	assert.NotContains(t, body, "Rejected Outcome Video", "a rejected match must never appear")
}

// ── A video with no metrics row does not appear at all ─────────────────

// TestHandleList_VideoWithNoMetrics_NeverAppears proves the store's inner
// LATERAL join excludes a video with no video_metrics row -- asserted
// here (not just in store/mcp tests) so this page's empty-state pointer
// stays accurate: a caller told "a row appears once a video is matched"
// must never be shown a page that silently omits an unmet-metrics video
// without saying so.
func TestHandleList_VideoWithNoMetrics_NeverAppears(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, "No Metrics Idea")
	video := s.addSyncedVideo(t, ctx, ch, "No Metrics Video")
	// Deliberately no s.addMetrics call.
	s.recordAutoMatch(t, ctx, video, script.ID, 0.9)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.NotContains(t, body, "No Metrics Video", "a video with no metrics row must never appear")
	assert.NotContains(t, body, "No Metrics Idea", "a video with no metrics row must never appear")
	assert.Contains(t, body, "/channels/"+ch.ID.String()+"/matches", "with the only candidate row excluded, the empty-state pointer must still render")
}

// ── NFR2: with 26 qualifying rows, exactly 25 render, truncated is a
// static note, and no paging control exists ─────────────────────────────

func TestHandleList_TwentySixQualifyingRows_TruncatedNoPagingControl(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	for i := 0; i < 26; i++ {
		s.buildFullOutcomeRow(t, ctx, ch, creator, fmt.Sprintf("Outcome Video %02d", i), int64(i))
	}

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	// Each outcomeCard renders "Outcome Video NN" four times over (Idea
	// title, script title, verdict reasoning, and video title all reuse
	// the same seeded title) -- count the confidence line instead, which
	// renders exactly once per card, to get an accurate row count.
	assert.Equal(t, 25, strings.Count(body, "confidence 0.90"), "exactly 25 outcome rows must render")
	assert.Contains(t, body, "Older outcomes exist beyond the newest 25.", "a static truncation note must appear (NFR2)")
	assert.NotContains(t, strings.ToLower(body), "load more", "no load-more control may appear (NFR2)")
	assert.NotContains(t, body, `name="since"`, "no since query parameter control may appear (NFR2)")
	assert.NotContains(t, body, "since=", "no since/page query parameter control may appear (NFR2)")
}

// ── FR2: a Channel with no qualifying rows returns 200 with the pointer
// text and a working link to /channels/{id}/matches ────────────────────

func TestHandleList_NoQualifyingRows_RendersMessagePlusPointer(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "No prediction-vs-outcome rows yet", "the empty state must be a message-plus-pointer, not a bare 'no data'")
	assert.Contains(t, body, "auto-matched", "the empty state must name the auto-match arrival path")
	assert.Contains(t, body, "manually confirmed", "the empty state must name the manual-confirm arrival path")
	assert.Contains(t, body, `href="/channels/`+ch.ID.String()+`/matches"`, "the empty state must link to the pending-matches page")
}

// ── #1929: calibration-trend chart (FR3, FR4) and inline set-outcome-bar
// form (FR5, FR6) ────────────────────────────────────────────────────────

// TestHandleList_FR4_NoOutcomeBar_RendersPointerNeverChart proves FR4: a
// Channel with no outcome bar ever set renders a 200 with the
// message-plus-pointer to the inline FR5 form, never a 500, never a
// defaulted threshold, and never any chart markup -- MonthlyTrend is
// never consulted (loadCalibration returns before calling it), so no
// <svg>/chart output can appear even though a qualifying candidate
// exists.
func TestHandleList_FR4_NoOutcomeBar_RendersPointerNeverChart(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	// A real calibration candidate exists, but with no bar configured
	// there is nothing to classify it against.
	s.buildCalibrationCandidate(t, ctx, ch, creator, "Unclassified Candidate", time.Now(), ptrInt64Outcomes(500))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "No outcome bar is configured for this Channel yet", "FR4's not-configured state must be a message, never a bare omission")
	assert.Contains(t, body, `action="/channels/`+ch.ID.String()+`/outcome-bar"`, "the not-configured message must point at the inline FR5 form on this same page")
	assert.NotContains(t, body, "<svg", "FR4's not-configured state must never render a chart -- MonthlyTrend is never consulted")
	assert.NotContains(t, body, "Classified against the current outcome bar", "no classification header may render with no bar configured")
}

// TestHandleList_FR3_ConfiguredBar_RendersChartNotJustTable proves FR3:
// once an outcome bar is configured and at least one calibration
// candidate exists, the calibration-trend section renders as a CHART
// (inline SVG, per this issue's Scaffold section), not merely a table --
// a supplementary table readout alongside the chart is fine, but the
// chart itself must be present.
func TestHandleList_FR3_ConfiguredBar_RendersChartNotJustTable(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.setOutcomeBar(t, ctx, ch, creator, 1000)
	s.buildCalibrationCandidate(t, ctx, ch, creator, "Chart Candidate", time.Now(), ptrInt64Outcomes(2000))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "<svg", "FR3 requires an actual chart, not just a table")
	assert.Contains(t, body, "<polyline", "the trend line must render as an SVG polyline")
	assert.Contains(t, body, `aria-label="Calibration rate by month"`, "the chart must be a real, labeled chart element")
	// The supplementary numeric table is fine alongside the chart.
	assert.Contains(t, body, "<table", "a supplementary table readout may accompany the chart")
}

// TestHandleList_FR3_UnsupportedMetric_RendersMessageNot500 proves the
// bar's ErrUnsupportedOutcomeBarMetric case surfaces as a rendered
// message (FR3), never a 500. The only way to have a bar whose
// MetricName isn't "views" is to bypass OutcomeBarStore.Upsert's own
// validation with a direct SQL write -- Upsert itself rejects the value
// server-side (see FR5 validation tests below).
func TestHandleList_FR3_UnsupportedMetric_RendersMessageNot500(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO outcome_bar (channel_id, metric_name, threshold_value, updated_by_person_id)
		VALUES ($1, 'ctr', 1, $2)
	`, ch.ID, creator.ID)
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "an unsupported metric must render a message, never a 500, body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "unsupported metric_name", "store.ErrUnsupportedOutcomeBarMetric's own message must render")
}

// TestHandleList_NFR4_BucketsRenderInMonthlyTrendOrder is this issue's
// load-bearing NFR4 case: buckets are seeded across calendar months out
// of natural insertion order (April, then January, then March), with
// deliberately DIFFERENT rates per month so that any accidental
// re-sorting (e.g. by Rate) would produce a detectably different
// sequence than MonthlyTrend's own chronological (oldest -> newest)
// order. The rendered sequence must match MonthlyTrend's own return
// order exactly.
func TestHandleList_NFR4_BucketsRenderInMonthlyTrendOrder(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.setOutcomeBar(t, ctx, ch, creator, 1000)

	// Seeded out of chronological order, with rates that DESCEND
	// chronologically (January 0%, March 50%, April 100%) -- the exact
	// reverse of chronological order, so a rate-based sort (ascending OR
	// descending) is guaranteed to disagree with MonthlyTrend's own
	// chronological order and this test would catch either direction.
	april := time.Date(2024, time.April, 15, 12, 0, 0, 0, time.UTC)
	january := time.Date(2024, time.January, 15, 12, 0, 0, 0, time.UTC)
	march := time.Date(2024, time.March, 15, 12, 0, 0, 0, time.UTC)
	s.buildCalibrationCandidate(t, ctx, ch, creator, "April Candidate", april, ptrInt64Outcomes(5000))
	s.buildCalibrationCandidate(t, ctx, ch, creator, "January Candidate", january, ptrInt64Outcomes(500))
	s.buildCalibrationCandidate(t, ctx, ch, creator, "March Candidate A", march, ptrInt64Outcomes(2000))
	s.buildCalibrationCandidate(t, ctx, ch, creator, "March Candidate B", march, ptrInt64Outcomes(200))

	bar, err := s.store.OutcomeBars().GetByChannel(ctx, ch.ID)
	require.NoError(t, err)
	canonical, truncated, err := s.store.Calibration().MonthlyTrend(ctx, ch.ID, bar, nil, nil, 12)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, canonical, 3, "January, March, April -- three distinct buckets")

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	// Scope the search to the calibration-trend card: the prediction-vs-
	// outcome cards ABOVE it also render each candidate's published date
	// as "2006-01-02" (e.g. "2024-01-15"), which contains a bucket's
	// "2006-01" label as a substring and, since those cards render
	// newest-published-first, would corrupt an unscoped order check with
	// a false ordering signal from an entirely different section.
	calibIdx := strings.Index(body, "Calibration trend")
	require.NotEqual(t, -1, calibIdx, "the calibration-trend section must render")
	section := body[calibIdx:]

	lastIdx := -1
	for _, b := range canonical {
		label := b.BucketStart.Format("2006-01")
		idx := strings.Index(section, label)
		require.NotEqual(t, -1, idx, "bucket label %q must render somewhere in the calibration-trend section", label)
		assert.Greater(t, idx, lastIdx, "bucket %q must render AFTER the previous bucket in MonthlyTrend's own order -- a re-sort (e.g. by Rate) would break this", label)
		lastIdx = idx
	}
}

// TestHandleList_FR4_ClassificationUsesCurrentBar_ReclassifiesOnChange
// proves FR4's "always the CURRENT bar, no historical snapshot" rule: a
// candidate classified as calibrated against one threshold must
// reclassify as miscalibrated once the SAME Channel's bar is updated to a
// higher threshold, with no re-request of any historical state.
func TestHandleList_FR4_ClassificationUsesCurrentBar_ReclassifiesOnChange(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.setOutcomeBar(t, ctx, ch, creator, 1000)
	s.buildCalibrationCandidate(t, ctx, ch, creator, "Reclassified Candidate", time.Now(), ptrInt64Outcomes(1500))

	w1 := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w1.Code, "body: %s", w1.Body.String())
	// Scoped to the exact Rate table cell -- Layout's shared CSS (every
	// page) already contains the bare substrings "100%" and "0%" (a
	// gradient stop, a height rule), so an unscoped Contains/NotContains
	// on those substrings is a false signal regardless of what the
	// calibration table actually rendered.
	assert.Contains(t, w1.Body.String(), "<td>100%</td>", "1500 views clears a 1000 threshold -- must render fully calibrated")

	s.setOutcomeBar(t, ctx, ch, creator, 2000)

	w2 := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w2.Code, "body: %s", w2.Body.String())
	assert.Contains(t, w2.Body.String(), "<td>0%</td>", "the SAME candidate must reclassify as miscalibrated against the new, higher threshold")
	assert.NotContains(t, w2.Body.String(), "<td>100%</td>", "the stale 100% classification must never survive a bar change -- there is no historical snapshot")
}

// TestHandleList_FR4_NullViewsCountsAsMiscalibrated is this issue's other
// load-bearing case: a candidate whose latest metrics snapshot has
// views IS NULL must count as MISCALIBRATED, never calibrated, even
// against a threshold of 0 -- easy to get backwards, per
// CalibrationStore.MonthlyTrend's own doc comment.
func TestHandleList_FR4_NullViewsCountsAsMiscalibrated(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.setOutcomeBar(t, ctx, ch, creator, 0)
	s.buildCalibrationCandidate(t, ctx, ch, creator, "No Views Value Candidate", time.Now(), nil)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "<td>0%</td>", "a NULL views value must render as 0% calibrated (miscalibrated), even against threshold 0")
	assert.NotContains(t, body, "<td>100%</td>", "a NULL views value must never count as calibrated")
}

// TestHandleList_NFR2_ThirteenMonths_RendersTwelveBucketsTruncated proves
// NFR2: with candidates spanning 13 distinct calendar months, exactly 12
// buckets render, truncated surfaces as a static note, and no paging
// control (since/before/limit, "load more") ever appears.
func TestHandleList_NFR2_ThirteenMonths_RendersTwelveBucketsTruncated(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.setOutcomeBar(t, ctx, ch, creator, 1000)

	base := time.Date(2023, time.January, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 13; i++ {
		month := base.AddDate(0, i, 0)
		s.buildCalibrationCandidate(t, ctx, ch, creator, fmt.Sprintf("Month %02d Candidate", i), month, ptrInt64Outcomes(2000))
	}

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Equal(t, 12, strings.Count(body, "<td>100%</td>"), "exactly 12 calibration buckets must render (each fully calibrated)")
	assert.Contains(t, body, "Older months exist beyond the last 12.", "a static truncation note must appear (NFR2)")
	assert.NotContains(t, strings.ToLower(body), "load more", "no load-more control may appear (NFR2)")
	assert.NotContains(t, body, `name="since"`, "no since query parameter control may appear for the calibration trend (NFR2)")
}

// ── FR5: inline set-outcome-bar form -- authorization/routing negatives
// (non-member 403, unknown Channel 404, malformed {id} 400, signed-out
// 401), reproducing HandleList's own load-bearing ordering per this
// issue's body ─────────────────────────────────────────────────────────

func TestHandleSetOutcomeBar_Analyst_CanSubmit(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	analyst := s.newPerson(t, ctx, "analyst")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/outcome-bar", s.sessionCookie(t, ctx, analyst.ID), url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"1000"},
	})
	assert.Equal(t, http.StatusSeeOther, w.Code, "an Analyst shares store.CanWrite authority (FR5), body: %s", w.Body.String())

	bar, err := s.store.OutcomeBars().GetByChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, 1000.0, bar.ThresholdValue)
}

func TestHandleSetOutcomeBar_NonMember_Forbidden_NoRowWritten(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/outcome-bar", s.sessionCookie(t, ctx, outsider.ID), url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"1000"},
	})
	assert.Equal(t, http.StatusForbidden, w.Code, "a non-member POST must 403 even though the form itself never rendered for them")
	assert.Equal(t, 0, s.countOutcomeBarRows(t, ctx, ch.ID))
}

// TestHandleSetOutcomeBar_SignedOut_Unauthorized_NoRowWritten calls the
// handler directly, bypassing the router's RequireSignedIn redirect,
// mirroring TestHandleList_NotSignedIn_Unauthorized's rationale.
func TestHandleSetOutcomeBar_SignedOut_Unauthorized_NoRowWritten(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	req := httptest.NewRequest(http.MethodPost, "/channels/"+ch.ID.String()+"/outcome-bar", strings.NewReader(url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"1000"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", ch.ID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleSetOutcomeBar(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, 0, s.countOutcomeBarRows(t, ctx, ch.ID))
}

func TestHandleSetOutcomeBar_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+uuid.NewString()+"/outcome-bar", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"1000"},
	})
	assert.Equal(t, http.StatusNotFound, w.Code, "an unknown Channel must 404 before authorization runs, body: %s", w.Body.String())
}

func TestHandleSetOutcomeBar_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/not-a-uuid/outcome-bar", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"1000"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── FR5 validation: negative threshold, forged metric_name, unparseable
// threshold -- all form errors (400), never a 500, and none writes ─────────

func TestHandleSetOutcomeBar_NegativeThreshold_FormErrorNoRowWritten(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/outcome-bar", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"-5"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "threshold_value must not be negative", "store.ErrInvalidOutcomeBarThreshold's own message must render")
	assert.Equal(t, 0, s.countOutcomeBarRows(t, ctx, ch.ID))
}

func TestHandleSetOutcomeBar_ForgedMetricName_FormErrorNoRowWritten(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/outcome-bar", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"metric_name":     {"ctr"},
		"threshold_value": {"1000"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "unsupported metric_name", "store.ErrUnsupportedOutcomeBarMetric's own message must render")
	assert.Equal(t, 0, s.countOutcomeBarRows(t, ctx, ch.ID))
}

func TestHandleSetOutcomeBar_UnparseableThreshold_FormErrorNot500(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/outcome-bar", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"not-a-number"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "an unparseable threshold must be a form error, never a 500, body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "threshold_value must be a number")
	assert.Equal(t, 0, s.countOutcomeBarRows(t, ctx, ch.ID))
}

// ── FR6: double-submit natural-key convergence ───────────────────────────

// TestHandleSetOutcomeBar_DoubleSubmit_ExactlyOneRow is FR6's load-bearing
// case: submitting the IDENTICAL form twice (simulating a back-button or
// refresh-after-POST double submit, with no idempotency_key field at all)
// must leave exactly one outcome_bar row for the Channel -- the natural-
// key ON CONFLICT (channel_id) convergence this form deliberately relies
// on instead of LB4's idempotency-key mechanism.
func TestHandleSetOutcomeBar_DoubleSubmit_ExactlyOneRow(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)
	form := url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"1500"},
	}

	w1 := s.doForm(t, "/channels/"+ch.ID.String()+"/outcome-bar", cookie, form)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())
	w2 := s.doForm(t, "/channels/"+ch.ID.String()+"/outcome-bar", cookie, form)
	require.Equal(t, http.StatusSeeOther, w2.Code, "body: %s", w2.Body.String())

	assert.Equal(t, 1, s.countOutcomeBarRows(t, ctx, ch.ID), "a double submit with no idempotency_key must still converge on exactly one row (FR6)")
	assert.NotContains(t, w1.Body.String()+w2.Body.String(), "idempotency_key", "this form must carry NO idempotency_key field anywhere (FR6)")

	bar, err := s.store.OutcomeBars().GetByChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, 1500.0, bar.ThresholdValue)
}

// TestHandleSetOutcomeBar_FirstTimeSet_FlipsFR4StateToFR3Chart proves the
// end-to-end FR4->FR5->FR3 flow: a Channel with no bar shows FR4's
// not-configured pointer; submitting the inline form for the first time
// flips the next GET to FR3's chart state.
func TestHandleSetOutcomeBar_FirstTimeSet_FlipsFR4StateToFR3Chart(t *testing.T) {
	ctx := context.Background()
	s := newOutcomesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	s.buildCalibrationCandidate(t, ctx, ch, creator, "First Set Candidate", time.Now(), ptrInt64Outcomes(2000))

	before := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, before.Code)
	assert.Contains(t, before.Body.String(), "No outcome bar is configured for this Channel yet")
	assert.NotContains(t, before.Body.String(), "<svg")

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/outcome-bar", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"metric_name":     {store.OutcomeBarMetricViews},
		"threshold_value": {"1000"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	after := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/outcomes", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, after.Code)
	assert.NotContains(t, after.Body.String(), "No outcome bar is configured", "the not-configured state must be gone once a bar is set")
	assert.Contains(t, after.Body.String(), "<svg", "the FR3 chart must render once a bar is set and a candidate exists")
}

// ptrInt64Outcomes mirrors store/store_integration_test.go's ptrInt64,
// reproduced here since this file's package (outcomes_test) cannot import
// an unexported helper from a different go_test compilation unit
// (store_test).
func ptrInt64Outcomes(v int64) *int64 { return &v }
