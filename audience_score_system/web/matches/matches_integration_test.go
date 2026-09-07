//go:build integration

// matches_integration_test.go covers `web/matches`'s HTTP surface against
// `video_schedule_match` (milestone M4.3, FR7/FR8/FR11/NFR2, issue #1926):
// HandleList's Founder/Co-Creator/Analyst read (same rows for all three,
// store.CanRead admits all three), negatives asserted in the load-bearing
// order this issue's body names -- non-member 403, unknown Channel 404
// (must 404 BEFORE the 403 path), malformed {id} 400, signed-out 401 -- a
// pending match with a best-guess script rendering its title/status/
// target-publish-date and, LB3's load-bearing case, the BOUND verdict
// version even after a newer verdict version is appended to the same
// Idea; a pending match with video_script_id IS NULL rendering no
// best-guess script and confidence 0 without a 500; a match whose video
// has no video_metrics row rendering "no metrics synced yet" rather than
// a bare 0; non-pending (auto/confirmed/rejected) matches never
// appearing; NFR2's 51-pending-match 50-row truncation with a static note
// and no paging control; and FR8's message-plus-pointer empty state.
//
// HandleResolve (issue #1927, FR9/FR10/NFR1/NFR3): a plain confirm links
// the matcher's best-guess script and removes the row from the pending
// list; this issue's LOAD-BEARING case, confirming WITH an override links
// to an ARCHIVED, UNDATED script (the primary resolution path for a
// script that can never auto-link, FR40/FR44); a reject sets state to
// 'rejected' and leaves video_script_id untouched; video_script_id on a
// reject 400s with nothing written; an override on a different Channel is
// rejected with nothing written; FR10's replay (same key twice resolves
// once) and conflict (a second attempt against an already-resolved match,
// with either a different key or no key, is rejected with no further
// state change) cases; NFR3's authz (Analyst may resolve via the shared
// Creator-or-Analyst store.CanWrite authority -- deliberately not the
// stricter Creator-tier-only approval gate schedule/script decisions use;
// a non-member's forged POST 403s even though their own GET never
// rendered the form; signed-out 401s); and the cross-Channel matchID 404
// guard mirroring resolvePendingMatchMutate exactly.
//
// See
// //audience_score_system/web/research:research_integration_test for the
// harness pattern this file follows: spin up a throwaway Postgres via
// dbtest, apply the domain's own real embedded migrations, wire a real
// *store.Store and a real *auth.SessionManager against it, and drive
// matches.Handlers through a small local http.ServeMux that mirrors
// `web`'s main.go route registrations for GET /channels/{id}/matches and
// POST /channels/{id}/matches/{matchID}/resolve -- so PathValue
// resolution and auth.RequireSignedIn wrapping behave exactly as they do
// in production.
//
// A signed-in caller is simulated via auth.NewForTests + SessionManager.
// Establish, mirroring research_integration_test.go's rationale:
// HandleLogin/HandleCallback are already covered by web/auth's own tests,
// so establishing a real session row directly here proves everything this
// package's own route owns, not auth's OAuth mechanics a second time.
package matches_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	"github.com/whale-net/everything/audience_score_system/web/matches"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "test_ass_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("matches-integration-test-key"))
}

// matchesTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), the
// matches.Handlers under test (exposed directly for the signed-out
// direct-call test below), and a router that mirrors main.go's matches
// route wiring (see this file's package doc comment).
type matchesTestStack struct {
	store    *store.Store
	sessions *auth.SessionManager
	handlers *matches.Handlers
	router   http.Handler
}

// newMatchesTestStack provisions dbtest Postgres, applies the domain's
// real embedded migrations, and wires a real store.Store/auth.
// SessionManager/matches.Handlers into a router equivalent to main.go's
// setupRoutes for this package's route.
func newMatchesTestStack(t *testing.T) *matchesTestStack {
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
	m := matches.New(st)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/matches", a.RequireSignedIn(m.HandleList))
	mux.HandleFunc("POST /channels/{id}/matches/{matchID}/resolve", a.RequireSignedIn(m.HandleResolve))

	return &matchesTestStack{store: st, sessions: sessions, handlers: m, router: mux}
}

// setupChannel creates a Channel with a live creator (Founder), mirroring
// research_integration_test.go's setupChannel fixture.
func (s *matchesTestStack) setupChannel(t *testing.T, ctx context.Context) (store.Channel, store.Person) {
	t.Helper()
	creator, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-creator-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Test Channel", creator.ID)
	require.NoError(t, err)
	return ch, creator
}

// newPerson creates a fresh, role-less Person.
func (s *matchesTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// sessionCookie establishes a real session row for personID and returns
// the resulting cookie, standing in for a completed sign-in (see this
// file's package doc comment).
func (s *matchesTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
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

func (s *matchesTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
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
// router, mirroring what a rendered confirm/reject <form method="post">
// would submit (research_integration_test.go's identically-named/
// documented helper) -- HandleResolve's r.ParseForm() reads it exactly
// like a real browser submission.
func (s *matchesTestStack) doForm(t *testing.T, target string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
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

// resolveIdempotencyKeyPattern extracts a row's hidden idempotency_key
// input value from a rendered pending-matches list, mirroring
// research_integration_test.go's idempotencyKeyPattern.
var resolveIdempotencyKeyPattern = regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`)

// extractResolveIdempotencyKeys returns every hidden idempotency_key
// input value found in body, in document order -- for a page with one
// pending match this is a length-1 slice; asserting length-N proves
// every row's form got a distinct render-time key.
func extractResolveIdempotencyKeys(t *testing.T, body string) []string {
	t.Helper()
	matches := resolveIdempotencyKeyPattern.FindAllStringSubmatch(body, -1)
	keys := make([]string, len(matches))
	for i, m := range matches {
		keys[i] = m[1]
	}
	return keys
}

// extractResolveIdempotencyKey returns the sole hidden idempotency_key
// value in body, failing the test if there is not exactly one -- for use
// against a page rendered with a single pending match.
func extractResolveIdempotencyKey(t *testing.T, body string) string {
	t.Helper()
	keys := extractResolveIdempotencyKeys(t, body)
	require.Len(t, keys, 1, "expected exactly one rendered resolve form, body: %s", body)
	return keys[0]
}

// greenlitVideoScript builds a full Idea -> viable Verdict -> Strategy ->
// Propose -> Greenlight chain on ch (FR36/FR37), mirroring
// mcp/tools/matches_integration_test.go's identically-named helper
// (separate go_test target/compilation unit, so it cannot reuse that
// package's copy). Returns the greenlit VideoScript plus the Verdict
// version it is bound to (LB3).
func (s *matchesTestStack) greenlitVideoScript(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, title string) (store.VideoScript, store.Verdict) {
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
	require.NoError(t, s.store.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	return got, v
}

// addSyncedVideo upserts a single published SyncedVideo with title on ch
// and returns it, resolved back through ListSchedule (UpsertVideos itself
// returns nothing).
func (s *matchesTestStack) addSyncedVideo(t *testing.T, ctx context.Context, ch store.Channel, title string) store.SyncedVideo {
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

// ── HandleList (FR7): Founder, Co-Creator, and Analyst all see the same
// rows ────────────────────────────────────────────────────────────────

func TestHandleList_FounderCoCreatorAnalyst_SeeSameRows(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Shared Idea")
	video := s.addSyncedVideo(t, ctx, ch, "Shared Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &script.ID, Confidence: 0.5, State: store.MatchStatePending,
	}))

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
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, tc.person.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "Shared Video", "%s must see the same pending match", tc.name)
		})
	}
}

// ── Negatives, asserted in the load-bearing order this issue's body
// names: non-member 403, unknown Channel 404 (must 404 BEFORE the 403
// path), malformed {id} 400, signed-out 401 ─────────────────────────────

func TestHandleList_NonMember_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, outsider.ID))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestHandleList_UnknownChannel_NotFound is this issue's load-bearing
// ordering case: an unknown Channel must 404 BEFORE authorization can turn
// it into a 403 -- breaking the Channel lookup's placement ahead of
// store.CanRead must turn this test red.
func TestHandleList_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+uuid.NewString()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusNotFound, w.Code, "an unknown Channel must 404 before authorization runs, body: %s", w.Body.String())
}

func TestHandleList_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/not-a-uuid/matches", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleList_NotSignedIn_Unauthorized calls HandleList directly,
// bypassing the router's RequireSignedIn wrapper (which redirects an
// unauthenticated request to /login rather than 401ing it) -- proving
// HandleList's own defensive auth.PersonFromContext check, mirroring
// research_integration_test.go's TestHandleChannelIndex_NotSignedIn_Unauthorized.
func TestHandleList_NotSignedIn_Unauthorized(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	req := httptest.NewRequest(http.MethodGet, "/channels/"+ch.ID.String()+"/matches", nil)
	req.SetPathValue("id", ch.ID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleList(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ── LB3's load-bearing red/green case: a pending match's best-guess
// script renders the BOUND verdict version, never a moving target ───────

// TestHandleList_BestGuessScript_RendersBoundVerdictVersion_EvenAfterNewerVerdictAppended
// is this issue's load-bearing case: the script's title/status/target
// publish date render, AND the verdict version bound to the script at
// Propose time (v1) still renders after a NEWER verdict version (v2) is
// appended to the same Idea -- proving renderMatchScript reads through
// script.VerdictID (the bound version) rather than re-deriving the Idea's
// current verdict. Breaking this (e.g. looking up the Idea's latest
// verdict instead of the script's bound VerdictID) must turn this test
// red by rendering "v2"/the v2 verdict value instead of "v1"/viable.
func TestHandleList_BestGuessScript_RendersBoundVerdictVersion_EvenAfterNewerVerdictAppended(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, v1 := s.greenlitVideoScript(t, ctx, ch, creator, "Bound Verdict Idea")
	require.Equal(t, 1, v1.Version)
	require.Equal(t, store.VerdictViable, v1.Verdict)

	// Append a newer verdict version to the SAME Idea -- the script's
	// VerdictID must remain bound to v1, never re-derived as "current".
	v2, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: script.IdeaID, Verdict: store.VerdictNotViable, Reasoning: "reconsidered", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 2, v2.Version)
	require.NotEqual(t, v1.ID, v2.ID)

	video := s.addSyncedVideo(t, ctx, ch, "Bound Verdict Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &script.ID, Confidence: 0.75, State: store.MatchStatePending,
	}))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Bound Verdict Idea", "the script's title must render")
	assert.Contains(t, body, "Greenlit", "the script's status must render")
	assert.Contains(t, body, "Undated", "the script has no target_publish_date, so it must render as undated (FR36), not an error")
	assert.Contains(t, body, "Verdict v1: viable", "the BOUND verdict version (v1) must render")
	assert.NotContains(t, body, "Verdict v2", "the Idea's newer verdict version (v2) must never render here -- LB3's bound version, not a moving target")
	assert.NotContains(t, body, "not-viable", "v2's verdict value must never leak into this render")
}

// TestHandleList_ScriptWithTargetDate_RendersDate covers the non-undated
// half of best-guess script rendering (Undated is covered by the LB3 test
// above), proving TargetPublishDate itself renders when set.
func TestHandleList_ScriptWithTargetDate_RendersDate(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Dated Idea", creator.ID)
	require.NoError(t, err)
	v, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "dated", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	strat, err := s.store.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: "Dated Strategy", Active: true,
		VerdictIDs: []uuid.UUID{v.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	target := time.Now().Add(72 * time.Hour)
	script, err := s.store.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: v.ID, StrategyID: strat.ID,
		Title: "Dated Idea", ScriptText: "script text", CreatedByPersonID: creator.ID,
		TargetPublishDate: &target,
	})
	require.NoError(t, err)
	require.NoError(t, s.store.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	video := s.addSyncedVideo(t, ctx, ch, "Dated Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &script.ID, Confidence: 0.6, State: store.MatchStatePending,
	}))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), target.UTC().Format("2006-01-02"), "the script's target_publish_date must render")
}

// ── A pending match with video_script_id IS NULL renders no best-guess
// script and confidence 0, without a 500 ────────────────────────────────

func TestHandleList_NoBestGuessScript_RendersNoScriptAndZeroConfidence_NotError(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	video := s.addSyncedVideo(t, ctx, ch, "No Candidate Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "a nil video_script_id must render 200, never a 500, body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "No Candidate Video")
	assert.Contains(t, body, "No best-guess script found.")
	assert.Contains(t, body, "Confidence: 0.00")
}

// ── A match whose video has no video_metrics row renders "no metrics
// synced yet", never a zero ──────────────────────────────────────────────

func TestHandleList_NoMetricsRow_RendersNoMetricsSyncedYet_NotZero(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	video := s.addSyncedVideo(t, ctx, ch, "No Metrics Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "No metrics synced yet.")
	assert.NotContains(t, body, "0 views", "an absent video_metrics row must never render as a zero")
}

// ── Non-pending matches (auto, confirmed, rejected) never appear ────────

func TestHandleList_NonPendingMatches_NeverAppear(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	for _, tc := range []struct {
		state store.MatchState
		title string
	}{
		{store.MatchStateAuto, "Auto Video"},
		{store.MatchStateConfirmed, "Confirmed Video"},
		{store.MatchStateRejected, "Rejected Video"},
	} {
		video := s.addSyncedVideo(t, ctx, ch, tc.title)
		require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
			SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: tc.state,
		}))
	}
	pendingVideo := s.addSyncedVideo(t, ctx, ch, "Pending Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: pendingVideo.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "Pending Video")
	assert.NotContains(t, body, "Auto Video")
	assert.NotContains(t, body, "Confirmed Video")
	assert.NotContains(t, body, "Rejected Video")
}

// ── NFR2: with 51 pending matches, exactly 50 render, truncated is a
// static note, and no paging/"load more" control exists ─────────────────

func TestHandleList_FiftyOnePendingMatches_TruncatedNoPagingControl(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	for i := 0; i < 51; i++ {
		video := s.addSyncedVideo(t, ctx, ch, fmt.Sprintf("Pending Video %d", i))
		require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
			SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
		}))
	}

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Equal(t, 50, strings.Count(body, "Pending Video "), "exactly 50 pending matches must render")
	assert.Contains(t, body, "Older pending matches exist beyond the newest 50.", "a static truncation note must appear (NFR2)")
	assert.NotContains(t, strings.ToLower(body), "load more", "no load-more control may appear (NFR2)")
	// NFR2 bans a PAGING form/control specifically -- not every <form>: since
	// #1927, a Founder's (canWrite) GET legitimately renders one confirm/
	// reject form per row (FR9). Assert on the paging-specific affordances
	// instead of a bare "<form" ban, which #1927's write path makes stale.
	assert.NotContains(t, body, `name="since"`, "no since query parameter control may appear (NFR2)")
	assert.NotContains(t, body, "since=", "no since/page query parameter control may appear (NFR2)")
}

// ── FR8: a Channel with no pending matches returns 200 with the
// pointer text, not a bare "no pending matches" ─────────────────────────

func TestHandleList_NoPendingMatches_RendersMessagePlusPointer(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "No pending matches.")
	assert.Contains(t, body, "confidence", "the empty state must name the confidence-threshold queueing rule, not just state the list is empty")
	assert.Contains(t, body, "threshold")
}

// ── HandleResolve (FR9, FR10, NFR1, NFR3) ───────────────────────────────

// singlePendingMatchID returns the ID of ch's sole pending match --
// convenience for tests that record exactly one and then resolve it.
func (s *matchesTestStack) singlePendingMatchID(t *testing.T, ctx context.Context, ch store.Channel) uuid.UUID {
	t.Helper()
	pending, _, err := s.store.Matches().ListPending(ctx, ch.ID, nil, 0)
	require.NoError(t, err)
	require.Len(t, pending, 1, "expected exactly one pending match")
	return pending[0].ID
}

// resolveForm builds the confirm/reject form.Values HandleResolve expects,
// mirroring resolveForm's rendered <form> fields (views.templ).
func resolveFormValues(key string, confirm bool, videoScriptID string) url.Values {
	v := url.Values{"idempotency_key": {key}}
	if confirm {
		v.Set("confirm", "true")
	} else {
		v.Set("confirm", "false")
	}
	if videoScriptID != "" {
		v.Set("video_script_id", videoScriptID)
	}
	return v
}

func resolveURL(ch store.Channel, matchID uuid.UUID) string {
	return "/channels/" + ch.ID.String() + "/matches/" + matchID.String() + "/resolve"
}

// TestHandleResolve_ConfirmNoOverride_LinksBestGuessScript_LeavesPendingList
// covers the plain-confirm path: no video_script_id override submitted,
// so the matcher's best-guess script (already on the row) is what
// Resolve links -- the row leaves the pending list and the underlying
// row's state becomes 'confirmed' with video_script_id unchanged.
func TestHandleResolve_ConfirmNoOverride_LinksBestGuessScript_LeavesPendingList(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Confirm Idea")
	video := s.addSyncedVideo(t, ctx, ch, "Confirm Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &script.ID, Confidence: 0.9, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, listW.Code, "body: %s", listW.Body.String())
	key := extractResolveIdempotencyKey(t, listW.Body.String())

	w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), resolveFormValues(key, true, ""))
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "/channels/"+ch.ID.String()+"/matches", w.Header().Get("Location"))

	m, err := s.store.Matches().GetByID(ctx, matchID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateConfirmed, m.State)
	require.NotNil(t, m.VideoScriptID)
	assert.Equal(t, script.ID, *m.VideoScriptID, "no override submitted -- video_script_id must remain the matcher's best guess")

	afterW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, afterW.Code, "body: %s", afterW.Body.String())
	assert.NotContains(t, afterW.Body.String(), "Confirm Video", "a confirmed match must leave the pending list")
}

// TestHandleResolve_ConfirmWithOverride_LinksArchivedAndUndatedScripts is
// this issue's LOAD-BEARING FR9 case: confirming with an explicit
// video_script_id override links to that script even when it is
// ARCHIVED and UNDATED -- the primary resolution path for a script that
// can never auto-link (no target_publish_date). Breaking Resolve's
// deliberate lack of a status filter (e.g. adding a greenlit-only check)
// must turn this test red.
func TestHandleResolve_ConfirmWithOverride_LinksArchivedAndUndatedScripts(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	// An undated, then archived, greenlit script -- never a plausible
	// best-guess candidate for the matcher, but still a valid override
	// target per FR40/FR44.
	undatedArchived, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Undated Archived Idea")
	require.Nil(t, undatedArchived.TargetPublishDate, "fixture script must be undated")
	require.NoError(t, s.store.VideoScripts().Archive(ctx, undatedArchived.ID, creator.ID))
	archived, err := s.store.VideoScripts().GetByID(ctx, undatedArchived.ID)
	require.NoError(t, err)
	require.Equal(t, store.VideoScriptStatusArchived, archived.Status)

	// A match recorded with NO best-guess candidate at all (video_script_id
	// IS NULL) -- proving the override is what supplies the link, not a
	// pre-existing best guess.
	video := s.addSyncedVideo(t, ctx, ch, "Override Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, listW.Code, "body: %s", listW.Body.String())
	key := extractResolveIdempotencyKey(t, listW.Body.String())

	w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), resolveFormValues(key, true, archived.ID.String()))
	require.Equal(t, http.StatusSeeOther, w.Code, "an archived, undated script must be a VALID override target, body: %s", w.Body.String())

	m, err := s.store.Matches().GetByID(ctx, matchID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateConfirmed, m.State)
	require.NotNil(t, m.VideoScriptID)
	assert.Equal(t, archived.ID, *m.VideoScriptID, "the override must link to the archived/undated script")
}

// TestHandleResolve_Reject_SetsRejectedState_LeavesVideoUnmatched proves
// reject leaves video_script_id untouched (nil here, since no best-guess
// candidate existed) and sets state to 'rejected'.
func TestHandleResolve_Reject_SetsRejectedState_LeavesVideoUnmatched(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	video := s.addSyncedVideo(t, ctx, ch, "Reject Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	key := extractResolveIdempotencyKey(t, listW.Body.String())

	w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), resolveFormValues(key, false, ""))
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	m, err := s.store.Matches().GetByID(ctx, matchID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateRejected, m.State)
	assert.Nil(t, m.VideoScriptID, "a reject must leave video_script_id untouched")
}

// TestHandleResolve_VideoScriptIDOnReject_BadRequest_NothingWritten proves
// the "video_script_id may only be set when confirm is true" rule this
// handler shares with resolve_pending_match.
func TestHandleResolve_VideoScriptIDOnReject_BadRequest_NothingWritten(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Reject Override Idea")
	video := s.addSyncedVideo(t, ctx, ch, "Reject Override Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	key := extractResolveIdempotencyKey(t, listW.Body.String())

	w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), resolveFormValues(key, false, script.ID.String()))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	m, err := s.store.Matches().GetByID(ctx, matchID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStatePending, m.State, "nothing must be written on this validation failure")
	assert.Nil(t, m.VideoScriptID)
}

// TestHandleResolve_OverrideOnDifferentChannel_Rejected_NothingWritten
// proves the override's Channel-membership check: a video_script that
// exists but belongs to a DIFFERENT Channel than the match's own must be
// rejected, with nothing written.
func TestHandleResolve_OverrideOnDifferentChannel_Rejected_NothingWritten(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	otherCh, otherCreator := s.setupChannel(t, ctx)
	otherScript, _ := s.greenlitVideoScript(t, ctx, otherCh, otherCreator, "Other Channel Idea")

	video := s.addSyncedVideo(t, ctx, ch, "Cross Channel Override Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	key := extractResolveIdempotencyKey(t, listW.Body.String())

	w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), resolveFormValues(key, true, otherScript.ID.String()))
	assert.Equal(t, http.StatusBadRequest, w.Code, "an override on a different Channel must be rejected, body: %s", w.Body.String())

	m, err := s.store.Matches().GetByID(ctx, matchID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStatePending, m.State, "nothing must be written")
	assert.Nil(t, m.VideoScriptID)
}

// TestHandleResolve_SameIdempotencyKey_Twice_ResolvesOnce is FR10/NFR1's
// load-bearing replay case: POSTing the identical form twice with the
// same key must resolve exactly once -- the second call is a no-op
// redirect, not a second state transition or an error.
func TestHandleResolve_SameIdempotencyKey_Twice_ResolvesOnce(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Replay Idea")
	video := s.addSyncedVideo(t, ctx, ch, "Replay Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &script.ID, Confidence: 0.8, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	key := extractResolveIdempotencyKey(t, listW.Body.String())
	form := resolveFormValues(key, true, "")

	w1 := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), form)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	w2 := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), form)
	assert.Equal(t, http.StatusSeeOther, w2.Code, "a replay with the identical key must still redirect as a no-op, not error, body: %s", w2.Body.String())

	m, err := s.store.Matches().GetByID(ctx, matchID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateConfirmed, m.State, "exactly one state transition must have occurred")
}

// TestHandleResolve_AlreadyResolved_DifferentKeyAndNoKey_Rejected_NoStateChange
// is FR10/NFR1's load-bearing conflict case: once a match is resolved, a
// second POST against it -- whether carrying a fresh, different key, or
// carrying NO key at all -- must be rejected with an explanatory error
// and no further state change (store.ErrMatchNotPending surfaced, never a
// silent second resolution).
func TestHandleResolve_AlreadyResolved_DifferentKeyAndNoKey_Rejected_NoStateChange(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	script, _ := s.greenlitVideoScript(t, ctx, ch, creator, "Conflict Idea")
	video := s.addSyncedVideo(t, ctx, ch, "Conflict Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &script.ID, Confidence: 0.8, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, creator.ID))
	firstKey := extractResolveIdempotencyKey(t, listW.Body.String())

	w1 := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), resolveFormValues(firstKey, true, ""))
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	for _, tc := range []struct {
		name string
		key  string
	}{
		{"DifferentKey", uuid.NewString()},
		{"NoKey", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), resolveFormValues(tc.key, true, ""))
			assert.NotEqual(t, http.StatusSeeOther, w.Code, "a second resolve attempt against an already-resolved match must never silently succeed, body: %s", w.Body.String())

			m, err := s.store.Matches().GetByID(ctx, matchID)
			require.NoError(t, err)
			assert.Equal(t, store.MatchStateConfirmed, m.State, "the already-resolved state must not change again")
		})
	}
}

// ── NFR3 authz: Analyst may resolve (shared Creator-or-Analyst
// store.CanWrite authority, deliberately not the stricter Creator-tier
// approval gate schedule/script decisions use); a non-member POST 403s
// even though the form was never rendered for them; a signed-out POST
// 401s ──────────────────────────────────────────────────────────────────

func TestHandleResolve_Analyst_CanResolve(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	analyst := s.newPerson(t, ctx, "resolve-analyst")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	video := s.addSyncedVideo(t, ctx, ch, "Analyst Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/matches", s.sessionCookie(t, ctx, analyst.ID))
	require.Equal(t, http.StatusOK, listW.Code, "body: %s", listW.Body.String())
	key := extractResolveIdempotencyKey(t, listW.Body.String())

	w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, analyst.ID), resolveFormValues(key, false, ""))
	require.Equal(t, http.StatusSeeOther, w.Code, "an Analyst must be able to resolve (shared Creator-or-Analyst store.CanWrite authority), body: %s", w.Body.String())

	m, err := s.store.Matches().GetByID(ctx, matchID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateRejected, m.State)
}

// TestHandleResolve_NonMember_Forbidden_EvenWithoutRenderedForm proves
// NFR3: a non-member's forged POST 403s even though the form was never
// rendered for them (their GET of the list would omit it -- canWrite is
// presentation only), with nothing written.
func TestHandleResolve_NonMember_Forbidden_EvenWithoutRenderedForm(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "resolve-outsider")

	video := s.addSyncedVideo(t, ctx, ch, "Outsider Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, outsider.ID), resolveFormValues(uuid.NewString(), true, ""))
	assert.Equal(t, http.StatusForbidden, w.Code, "a non-member's forged POST must 403 even though their GET never rendered this form")

	m, err := s.store.Matches().GetByID(ctx, matchID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStatePending, m.State, "nothing must be written")
}

// TestHandleResolve_SignedOut_Unauthorized calls HandleResolve directly,
// bypassing the router's RequireSignedIn wrapper (which redirects to
// /login), mirroring TestHandleList_NotSignedIn_Unauthorized.
func TestHandleResolve_SignedOut_Unauthorized(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	video := s.addSyncedVideo(t, ctx, ch, "Signed Out Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, ch)

	form := resolveFormValues(uuid.NewString(), true, "")
	req := httptest.NewRequest(http.MethodPost, resolveURL(ch, matchID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", ch.ID.String())
	req.SetPathValue("matchID", matchID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleResolve(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestHandleResolve_CrossChannelMatchID_NotFound proves the Channel guard
// mirroring resolvePendingMatchMutate exactly: a match that exists but
// belongs to a different Channel than the path's {id} must 404, never a
// 403 or a distinguishable response from an unknown match.
func TestHandleResolve_CrossChannelMatchID_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newMatchesTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	otherCh, _ := s.setupChannel(t, ctx)

	video := s.addSyncedVideo(t, ctx, otherCh, "Cross Channel Match Video")
	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: nil, Confidence: 0, State: store.MatchStatePending,
	}))
	matchID := s.singlePendingMatchID(t, ctx, otherCh)

	w := s.doForm(t, resolveURL(ch, matchID), s.sessionCookie(t, ctx, creator.ID), resolveFormValues(uuid.NewString(), true, ""))
	assert.Equal(t, http.StatusNotFound, w.Code, "a match belonging to a different Channel must 404, body: %s", w.Body.String())
}
