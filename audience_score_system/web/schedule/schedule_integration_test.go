//go:build integration

// schedule_integration_test.go covers `web/schedule`'s HTTP surface against
// `video_script` (milestone video-script-model, FR48/FR49, issue #1834):
// HandleList's read side (Founder/Co-Creator/Analyst all see the same
// rows, a non-member 403s, an unknown Channel 404s, signed-out 401s, and
// the Analyst's rendered page carries no greenlight/deny/archive
// affordance), the three mutating routes' fresh-per-request
// store.CanApprove check (a forged POST from an Analyst 403s on every
// route, exercised through the real HTTP handler -- not just the rendered
// page's omitted affordance), Greenlight/Deny/Archive's happy paths for
// both Creator-tier roles (Founder and Co-Creator, symmetrically per
// FR32), FR40's invalid-transition 409s, FR39's publish freeze (409 +
// state unchanged + omitted archive affordance) through the web path, the
// retired /unapprove and /edit routes (404, no route registered), and a
// malformed script UUID (400). See //libs/go/dbtest's README and web/
// invite's own invite_integration_test.go for the harness pattern this
// file follows: spin up a throwaway Postgres via dbtest, apply the
// domain's own real embedded migrations, wire a real *store.Store and a
// real *auth.SessionManager against it, and drive schedule.Handlers
// through a small local http.ServeMux that mirrors `web`'s main.go route
// registrations for GET /channels/{id}/schedule and POST /schedule/
// {scriptID}/{approve,deny,archive} -- so PathValue resolution and
// auth.RequireSignedIn wrapping behave exactly as they do in production.
//
// A signed-in caller is simulated via auth.NewForTests + SessionManager.
// Establish, mirroring invite_integration_test.go's rationale: HandleLogin/
// HandleCallback are already covered by web/auth's own tests, so
// establishing a real session row directly here proves everything this
// package's own routes own, not auth's OAuth mechanics a second time.
package schedule_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/schedule"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "test_ass_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("schedule-integration-test-key"))
}

// scheduleTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), the
// schedule.Handlers under test (exposed directly for the signed-out
// direct-call test below), and a router that mirrors main.go's schedule
// route wiring (see this file's package doc comment).
type scheduleTestStack struct {
	store    *store.Store
	sessions *auth.SessionManager
	handlers *schedule.Handlers
	router   http.Handler
	db       *dbtest.Postgres
}

// newScheduleTestStack provisions dbtest Postgres, applies the domain's
// real embedded migrations, and wires a real store.Store/auth.
// SessionManager/schedule.Handlers into a router equivalent to main.go's
// setupRoutes for this package's routes -- deliberately NOT registering
// /unapprove or /edit, mirroring main.go, so a POST to either 404s via the
// mux's own "no such route" behavior rather than any code in this
// package.
func newScheduleTestStack(t *testing.T) *scheduleTestStack {
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
	sch := schedule.New(st)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/schedule", a.RequireSignedIn(sch.HandleList))
	mux.HandleFunc("POST /schedule/{scriptID}/approve", a.RequireSignedIn(sch.HandleGreenlight))
	mux.HandleFunc("POST /schedule/{scriptID}/deny", a.RequireSignedIn(sch.HandleDeny))
	mux.HandleFunc("POST /schedule/{scriptID}/archive", a.RequireSignedIn(sch.HandleArchive))

	return &scheduleTestStack{store: st, sessions: sessions, handlers: sch, router: mux, db: db}
}

// setupChannel creates a Channel with a live creator (Founder), mirroring
// store_integration_test.go's setupChannel fixture.
func (s *scheduleTestStack) setupChannel(t *testing.T, ctx context.Context) (store.Channel, store.Person) {
	t.Helper()
	creator, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-creator-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Test Channel", creator.ID)
	require.NoError(t, err)
	return ch, creator
}

// newPerson creates a fresh, role-less Person.
func (s *scheduleTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// setupVideoScriptFixture creates a Channel/Founder, a fresh Idea with a
// viable verdict, and one active Strategy grounded on that verdict --
// mirrors store/video_script_integration_test.go's
// setupVideoScriptChannel (duplicated here rather than shared, since
// schedule_test cannot import store's internal _test package): every
// propose/greenlight/deny/archive test below needs the same shape (FR36 --
// a video_script cannot exist without a grounding Strategy).
func (s *scheduleTestStack) setupVideoScriptFixture(t *testing.T, ctx context.Context, label string) (store.Channel, store.Person, store.Verdict, store.StrategyDetail) {
	t.Helper()
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea "+label, creator.ID)
	require.NoError(t, err)

	verdict, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: label + " looks viable", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	strategy, err := s.store.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: label + " Strategy", Active: true,
		VerdictIDs: []uuid.UUID{verdict.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	return ch, creator, verdict, strategy
}

// proposeScript proposes one fresh, `proposed`-status video_script on
// fixture -- the state every greenlight/deny/archive test below starts
// from.
func (s *scheduleTestStack) proposeScript(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, verdict store.Verdict, strategy store.StrategyDetail, title string) store.VideoScript {
	t.Helper()
	script, err := s.store.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: verdict.ID, StrategyID: strategy.ID,
		Title: title, ScriptText: "script text for " + title, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	return script
}

// recordPublishedMatch inserts a synced_video (published, when published
// == true) plus a video_schedule_match row wired to scriptID via
// video_script_id (migration 010's FR45 re-anchor column) -- lets a test
// construct exactly the "live match to a published video" state FR39's
// freeze predicate (store.VideoScriptStore.IsPublished) distinguishes.
func (s *scheduleTestStack) recordPublishedMatch(t *testing.T, ctx context.Context, ch store.Channel, scriptID uuid.UUID, matchState store.MatchState, published bool) {
	t.Helper()

	var publishedAt *time.Time
	if published {
		now := time.Now().UTC()
		publishedAt = &now
	}

	err := s.store.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-video-" + uuid.NewString(),
		Title:          "Published Video",
		PrivacyStatus:  store.PrivacyStatusPublic,
		PublishedAt:    publishedAt,
		LastSyncedAt:   time.Now().UTC(),
	}})
	require.NoError(t, err)

	var videoID uuid.UUID
	require.NoError(t, s.db.Pool.QueryRow(ctx, `
		SELECT id FROM synced_video WHERE channel_id = $1 ORDER BY last_synced_at DESC LIMIT 1
	`, ch.ID).Scan(&videoID))

	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO video_schedule_match (synced_video_id, video_script_id, confidence, state)
		VALUES ($1, $2, 0.9, $3)
	`, videoID, scriptID, matchState)
	require.NoError(t, err)
}

// sessionCookie establishes a real session row for personID and returns the
// resulting cookie, standing in for a completed sign-in (see this file's
// package doc comment).
func (s *scheduleTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
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

func (s *scheduleTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// ── HandleList (FR48): Founder, Co-Creator, and Analyst all read the same
// list; a non-member 403s, an unknown Channel 404s, signed-out 401s ────────

func TestHandleList_FounderCoCreatorAnalyst_SeeSameScripts(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "List")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script One")

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
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", s.sessionCookie(t, ctx, tc.person.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), script.Title, "%s must see the same script", tc.name)
		})
	}
}

func TestHandleList_NonMember_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, _, _, _ := s.setupVideoScriptFixture(t, ctx, "Outsider")
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", s.sessionCookie(t, ctx, outsider.ID))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleList_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	_, creator, _, _ := s.setupVideoScriptFixture(t, ctx, "UnknownChannel")

	w := s.do(t, http.MethodGet, "/channels/"+uuid.NewString()+"/schedule", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
}

// TestHandleList_NotSignedIn_Unauthorized calls HandleList directly,
// bypassing the router's RequireSignedIn wrapper (which redirects an
// unauthenticated request to /login rather than 401ing it) -- proving
// HandleList's own defensive auth.PersonFromContext check, mirroring
// web/channel/channel_test.go's TestHandleReconnect_NotSignedIn_Unauthorized.
func TestHandleList_NotSignedIn_Unauthorized(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	req := httptest.NewRequest(http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", nil)
	req.SetPathValue("id", ch.ID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleList(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestHandleList_Analyst_NoMutatingAffordances covers both status shapes
// entryActions renders for a Creator-tier caller (greenlight+deny on a
// proposed script, archive on a greenlit one) and proves an Analyst's
// rendered page contains none of them, though the Analyst can still read
// both scripts (store.CanRead).
func TestHandleList_Analyst_NoMutatingAffordances(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "AnalystView")
	proposedScript := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Proposed Script")
	greenlitScript := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Greenlit Script")
	require.NoError(t, s.store.VideoScripts().Greenlight(ctx, greenlitScript.ID, creator.ID))

	analyst := s.newPerson(t, ctx, "analyst")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", s.sessionCookie(t, ctx, analyst.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, proposedScript.Title, "an Analyst must still be able to read the schedule (store.CanRead)")
	assert.Contains(t, body, greenlitScript.Title)
	assert.NotContains(t, body, "/schedule/"+proposedScript.ID.String()+"/approve", "an Analyst's view must render no greenlight affordance")
	assert.NotContains(t, body, "/schedule/"+proposedScript.ID.String()+"/deny", "an Analyst's view must render no deny affordance")
	assert.NotContains(t, body, "/schedule/"+greenlitScript.ID.String()+"/archive", "an Analyst's view must render no archive affordance")
}

// ── Forged POST from an Analyst -- the load-bearing authorization test:
// store.CanApprove is re-derived fresh on every request, exercised via the
// actual HTTP handler, not just the rendered page's omitted button ────────

func TestForgedPOST_Analyst_Forbidden_NoStateChange(t *testing.T) {
	for _, route := range []string{"approve", "deny", "archive"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Forged-"+route)
			script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")
			wantStatus := store.VideoScriptStatusProposed
			if route == "archive" {
				require.NoError(t, s.store.VideoScripts().Greenlight(ctx, script.ID, creator.ID))
				wantStatus = store.VideoScriptStatusGreenlit
			}

			analyst := s.newPerson(t, ctx, "analyst")
			require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

			w := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/"+route, s.sessionCookie(t, ctx, analyst.ID))
			assert.Equal(t, http.StatusForbidden, w.Code, "an Analyst's forged POST to /%s must 403 -- store.CanApprove re-derived fresh, never from a hidden button", route)

			got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
			require.NoError(t, err)
			assert.Equal(t, wantStatus, got.Status, "an Analyst's forged POST must not change state")
		})
	}
}

// ── FR37/FR38/FR39 happy paths: Founder and Co-Creator hold identical
// authority (FR32), driving Greenlight/Deny/Archive through the real
// HTTP route ─────────────────────────────────────────────────────────────

func TestHandleGreenlight_FounderAndCoCreator_ProposedToGreenlit(t *testing.T) {
	for _, roleName := range []string{"Founder", "CoCreator"} {
		t.Run(roleName, func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Greenlight-"+roleName)
			script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")
			actor := s.actorForRole(t, ctx, ch, creator, roleName)

			w := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/approve", s.sessionCookie(t, ctx, actor.ID))
			assert.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

			got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
			require.NoError(t, err)
			assert.Equal(t, store.VideoScriptStatusGreenlit, got.Status)
			if assert.NotNil(t, got.DecidedByPersonID) {
				assert.Equal(t, actor.ID, *got.DecidedByPersonID, "the decider recorded must be the calling %s", roleName)
			}
		})
	}
}

func TestHandleDeny_FounderAndCoCreator_ProposedToDenied(t *testing.T) {
	for _, roleName := range []string{"Founder", "CoCreator"} {
		t.Run(roleName, func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Deny-"+roleName)
			script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")
			actor := s.actorForRole(t, ctx, ch, creator, roleName)

			w := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/deny", s.sessionCookie(t, ctx, actor.ID))
			assert.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

			got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
			require.NoError(t, err)
			assert.Equal(t, store.VideoScriptStatusDenied, got.Status)
		})
	}
}

func TestHandleArchive_FounderAndCoCreator_GreenlitToArchived(t *testing.T) {
	for _, roleName := range []string{"Founder", "CoCreator"} {
		t.Run(roleName, func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Archive-"+roleName)
			script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")
			require.NoError(t, s.store.VideoScripts().Greenlight(ctx, script.ID, creator.ID))
			actor := s.actorForRole(t, ctx, ch, creator, roleName)

			w := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/archive", s.sessionCookie(t, ctx, actor.ID))
			assert.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

			got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
			require.NoError(t, err)
			assert.Equal(t, store.VideoScriptStatusArchived, got.Status)
		})
	}
}

// actorForRole returns creator itself for roleName == "Founder", or a
// freshly-granted Co-Creator on ch for roleName == "CoCreator" -- shared by
// the three Founder/Co-Creator happy-path tests above.
func (s *scheduleTestStack) actorForRole(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, roleName string) store.Person {
	t.Helper()
	if roleName == "Founder" {
		return creator
	}
	coCreator := s.newPerson(t, ctx, "co-creator")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))
	return coCreator
}

// ── FR40: the exhaustive transition set rejects everything else, 409 with
// no state change ────────────────────────────────────────────────────────

func TestHandleGreenlight_AlreadyGreenlit_Conflict_NoStateChange(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "AlreadyGreenlit")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/approve", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	w = s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/approve", cookie)
	assert.Equal(t, http.StatusConflict, w.Code, "greenlighting an already-greenlit script must 409")

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusGreenlit, got.Status)
}

func TestHandleDeny_AlreadyDenied_Conflict_NoStateChange(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "AlreadyDenied")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/deny", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	w = s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/deny", cookie)
	assert.Equal(t, http.StatusConflict, w.Code, "denying an already-denied script must 409")

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusDenied, got.Status)
}

func TestHandleArchive_Proposed_Conflict_NoStateChange(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "ArchiveProposed")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/archive", cookie)
	assert.Equal(t, http.StatusConflict, w.Code, "archiving a proposed (not-yet-greenlit) script must 409")

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusProposed, got.Status)
}

// ── FR39's publish freeze, through the web path ─────────────────────────────

func TestHandleArchive_PublishedFreeze_Conflict_StateUnchanged_AffordanceOmitted(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Freeze")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Frozen Script")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/approve", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	// A live (confirmed) match to an actually-published video -- FR39's
	// exact "recorded as published" predicate.
	s.recordPublishedMatch(t, ctx, ch, script.ID, store.MatchStateConfirmed, true)

	archiveW := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/archive", cookie)
	assert.Equal(t, http.StatusConflict, archiveW.Code, "archiving a script whose matched video has published must 409 (FR39)")

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusGreenlit, got.Status, "the freeze must leave state unchanged")

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", cookie)
	require.Equal(t, http.StatusOK, listW.Code, "body: %s", listW.Body.String())
	body := listW.Body.String()
	assert.NotContains(t, body, "/schedule/"+script.ID.String()+"/archive", "a frozen script's page must render no archive affordance")
	assert.Contains(t, body, "frozen", "a frozen script's page must explain the freeze")
}

// ── Retired routes: /unapprove and /edit have no analog under video_script
// (FR40 defines no greenlit->proposed transition; FR36's target date is set
// once at propose time) and are simply not registered ──────────────────────

func TestRetiredRoutes_UnapproveAndEdit_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Retired")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	unapproveW := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/unapprove", cookie)
	assert.Equal(t, http.StatusNotFound, unapproveW.Code, "/unapprove has no analog under video_script (FR40) and must not be routed")

	editW := s.do(t, http.MethodPost, "/schedule/"+script.ID.String()+"/edit", cookie)
	assert.Equal(t, http.StatusNotFound, editW.Code, "/edit has no analog under video_script (FR36, no web edit surface) and must not be routed")
}

// ── Malformed input ─────────────────────────────────────────────────────────

func TestMutate_MalformedScriptUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	_, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/not-a-uuid/approve", cookie)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
