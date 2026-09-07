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
	sessions *auth.SessionManager
	handlers *outcomes.Handlers
	router   http.Handler
}

// newOutcomesTestStack provisions dbtest Postgres, applies the domain's
// real embedded migrations, and wires a real store.Store/auth.
// SessionManager/outcomes.Handlers into a router equivalent to main.go's
// setupRoutes for this package's route.
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

	return &outcomesTestStack{store: st, sessions: sessions, handlers: o, router: mux}
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
