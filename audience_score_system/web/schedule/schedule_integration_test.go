//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and web/invite's own
// invite_integration_test.go for the pattern this file follows: spin up a
// throwaway Postgres via dbtest, apply the domain's own real embedded
// migrations, wire a real *store.Store and a real *auth.SessionManager
// against it, and drive schedule.Handlers through a small local
// http.ServeMux that mirrors `web`'s main.go route registrations for
// GET /channels/{id}/schedule and POST /schedule/{entryID}/{approve,
// unapprove,edit} -- so PathValue resolution and auth.RequireSignedIn
// wrapping behave exactly as they do in production.
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
	"github.com/whale-net/everything/audience_score_system/web/schedule"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "test_ass_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("schedule-integration-test-key"))
}

// scheduleTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), and a router
// that mirrors main.go's schedule route wiring (see this file's package doc
// comment).
type scheduleTestStack struct {
	store    *store.Store
	sessions *auth.SessionManager
	router   http.Handler
	db       *dbtest.Postgres
}

// newScheduleTestStack provisions dbtest Postgres, applies the domain's
// real embedded migrations, and wires a real store.Store/auth.
// SessionManager/schedule.Handlers into a router equivalent to main.go's
// setupRoutes for this package's routes.
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
	mux.HandleFunc("POST /schedule/{entryID}/approve", a.RequireSignedIn(sch.HandleApprove))
	mux.HandleFunc("POST /schedule/{entryID}/unapprove", a.RequireSignedIn(sch.HandleUnapprove))
	mux.HandleFunc("POST /schedule/{entryID}/edit", a.RequireSignedIn(sch.HandleEdit))

	return &scheduleTestStack{store: st, sessions: sessions, router: mux, db: db}
}

// setupChannel creates a Channel with a live creator, mirroring
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

// doForm POSTs target as a application/x-www-form-urlencoded body -- used
// by HandleEdit's proposed_publish_at field.
func (s *scheduleTestStack) doForm(t *testing.T, target string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
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

// draftEntry creates a fresh Idea, a VerdictViable version for it, and a
// draft schedule_entry proposing proposedAt -- the fixture every
// approve/unapprove/edit test in this file starts from.
func (s *scheduleTestStack) draftEntry(t *testing.T, ctx context.Context, ch store.Channel, creator store.Person, proposedAt time.Time) store.ScheduleEntry {
	t.Helper()
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea "+uuid.NewString(), creator.ID)
	require.NoError(t, err)

	v, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID:         idea.ID,
		Verdict:        store.VerdictViable,
		Reasoning:      "looks viable",
		AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	entry, err := s.store.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID:         ch.ID,
		IdeaID:            idea.ID,
		VerdictID:         v.ID,
		ProposedPublishAt: proposedAt,
		CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	return entry
}

// recordMatch inserts a synced_video (published, when published == true)
// plus a video_schedule_match row in matchState referencing entryID --
// lets a test construct exactly the "live match to a published video" state
// (or its near-misses: a pending/rejected match, or a live match to a
// not-yet-published video) that FR20's freeze predicate (store.IsPublished)
// distinguishes between.
func (s *scheduleTestStack) recordMatch(t *testing.T, ctx context.Context, ch store.Channel, entryID uuid.UUID, matchState store.MatchState, published bool) {
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

	require.NoError(t, s.store.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID:   videoID,
		ScheduleEntryID: &entryID,
		Confidence:      1.0,
		State:           matchState,
	}))
}

// ── FR19: Creator approves a draft ──────────────────────────────────────────

func TestHandleApprove_Creator_ApprovesDraft_SetsStateApproverApprovedAt(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	assert.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateCommitted, got.State)
	if assert.NotNil(t, got.ApprovedByPersonID) {
		assert.Equal(t, creator.ID, *got.ApprovedByPersonID)
	}
	assert.NotNil(t, got.ApprovedAt, "approved_at must be stamped (FR19)")
}

// ── FR19's negative half, NFR5: only a Creator may approve ─────────────────

func TestHandleApprove_Analyst_Forbidden_StateUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))

	analyst := s.newPerson(t, ctx, "analyst")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))
	cookie := s.sessionCookie(t, ctx, analyst.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	assert.Equal(t, http.StatusForbidden, w.Code)

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateDraft, got.State, "an analyst's approve attempt must not change state")
	assert.Nil(t, got.ApprovedByPersonID)
}

func TestHandleApprove_NoRoleOnChannel_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))

	outsider := s.newPerson(t, ctx, "outsider")
	cookie := s.sessionCookie(t, ctx, outsider.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	assert.Equal(t, http.StatusForbidden, w.Code)

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateDraft, got.State)
}

// TestHandleApprove_ClosedCreatorRow_Forbidden proves mutate's CanApprove
// check is re-derived live from channel_person on every call, not cached or
// inferred from a "used to be creator" fact -- the same assertion
// store_integration_test.go's TestAuthz_CreatorOnlyChecks_AgainstRealRoleStore
// makes against authz.go directly, exercised here through the actual HTTP
// route.
func TestHandleApprove_ClosedCreatorRow_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)

	// former is granted as ch's creator (via Channels().Create, which
	// self-grants role=creator), then that row is closed on ch itself --
	// NOT a separate Channel -- and a new Person is granted the current
	// creator role on the same ch afterward. Migration 009's
	// channel_person_channel_id_founder_current partial unique index only
	// forbids two simultaneously-OPEN creator rows on one Channel; a
	// closed row followed by a new open row on the same Channel is fine,
	// which is exactly what this sequence needs to prove a closed row on
	// THIS Channel (not just "no relationship to it at all", already
	// covered by TestHandleApprove_NoRoleOnChannel_Forbidden above) can't
	// approve. Mirrors store_integration_test.go's setupChannelWithRoles,
	// which scopes the closed row and the checked channel to the same
	// formerChannelID field.
	former := s.newPerson(t, ctx, "former-creator")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Test Channel", former.ID)
	require.NoError(t, err)
	// Close former's row directly (bypassing the store API, which has no
	// "revoke" method wired through AddRole's close-and-open pattern
	// here) to simulate a Person whose creator role has lapsed --
	// mirrors store_integration_test.go's setupChannelWithRoles.
	_, err = s.db.Pool.Exec(ctx, `
		UPDATE channel_person SET valid_to = NOW()
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, ch.ID, former.ID)
	require.NoError(t, err)

	// Now that former's row on ch is closed, grant a fresh Person the
	// current (open) creator role on the same ch -- the entry approved
	// below is drafted and owned by this current creator, on the exact
	// Channel former used to (but no longer) hold a creator row on.
	creator := s.newPerson(t, ctx, "creator")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, creator.ID, store.RoleCreator, former.ID))
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))

	cookie := s.sessionCookie(t, ctx, former.ID)
	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	assert.Equal(t, http.StatusForbidden, w.Code, "a closed creator row on the target Channel must not authorize approve -- CanApprove must read live join-table state")

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateDraft, got.State)
}

// ── FR32: Co-Creator holds the exact same approve authority as Founder ──────

// TestHandleApprove_CoCreator_ApprovesDraft_SetsStateApproverApprovedAt
// mirrors TestHandleApprove_Creator_ApprovesDraft_... above with a
// role=co_creator caller instead of role=creator, proving FR32's
// symmetric authority through the real HTTP route (not just store/authz_
// test.go's pure-Go fake or store_integration_test.go's direct authz.go
// call) -- no consensus or Founder-tiebreak logic exists anywhere
// (NFR6's explicit non-goal).
func TestHandleApprove_CoCreator_ApprovesDraft_SetsStateApproverApprovedAt(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))

	coCreator := s.newPerson(t, ctx, "co-creator")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))
	cookie := s.sessionCookie(t, ctx, coCreator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	assert.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateCommitted, got.State)
	if assert.NotNil(t, got.ApprovedByPersonID) {
		assert.Equal(t, coCreator.ID, *got.ApprovedByPersonID, "the approver recorded must be the calling Co-Creator, not the Founder")
	}
	assert.NotNil(t, got.ApprovedAt, "approved_at must be stamped (FR19) for a Co-Creator's approval same as a Founder's")
}

// TestHandleUnapproveEdit_CoCreator_SameAuthorityAsCreator extends the FR32
// symmetry proof to un-approve and edit, not just approve -- a Co-Creator
// must be able to drive the full approve/un-approve/edit cycle
// TestApproveUnapproveEditCycle_... above proves for a Founder.
func TestHandleUnapproveEdit_CoCreator_SameAuthorityAsCreator(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))

	coCreator := s.newPerson(t, ctx, "co-creator")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))
	cookie := s.sessionCookie(t, ctx, coCreator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "approve, body: %s", w.Body.String())

	w = s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/unapprove", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "unapprove, body: %s", w.Body.String())

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	require.Equal(t, store.ScheduleStateDraft, got.State, "unapprove must return to draft for a Co-Creator same as a Founder")
	require.Nil(t, got.ApprovedByPersonID, "unapprove must clear the approver")

	newSlot := entry.ProposedPublishAt.Add(2 * time.Hour).Format(time.RFC3339)
	form := url.Values{"proposed_publish_at": {newSlot}}
	w = s.doForm(t, "/schedule/"+entry.ID.String()+"/edit", cookie, form)
	require.Equal(t, http.StatusSeeOther, w.Code, "edit, body: %s", w.Body.String())

	got, err = s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339, newSlot)
	require.NoError(t, err)
	assert.True(t, got.ProposedPublishAt.Equal(parsed), "edit must persist the Co-Creator's new slot")
}

// TestHandleApprove_ClosedCoCreatorRow_Forbidden mirrors
// TestHandleApprove_ClosedCreatorRow_Forbidden above for role=co_creator:
// a co_creator row that has been closed (valid_to set) must not authorize
// approve, proving CanApprove reads live join-table state for the
// co_creator tier too, not just the creator tier.
func TestHandleApprove_ClosedCoCreatorRow_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))

	former := s.newPerson(t, ctx, "former-co-creator")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, former.ID, store.RoleCoCreator, creator.ID))
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE channel_person SET valid_to = NOW()
		WHERE person_id = $1 AND valid_to IS NULL
	`, former.ID)
	require.NoError(t, err)

	cookie := s.sessionCookie(t, ctx, former.ID)
	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	assert.Equal(t, http.StatusForbidden, w.Code, "a closed co_creator row on the target Channel must not authorize approve -- CanApprove must read live join-table state")

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateDraft, got.State)
}

// ── FR20: approve -> un-approve -> edit -> re-approve, repeated three times ─

func TestApproveUnapproveEditCycle_RepeatedThreeTimes_EndsCommittedWithEditedSlot(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))
	cookie := s.sessionCookie(t, ctx, creator.ID)

	// slot tracks the canonical value the server will actually persist:
	// parsed back from the exact RFC3339 string submitted, since
	// parseProposedPublishAt (schedule.go) -- and RFC3339's lack of a
	// fractional-seconds placeholder -- truncates anything finer than
	// whole seconds, so comparing against a Go-side time.Time that never
	// round-tripped through that format would be flaky.
	slot := entry.ProposedPublishAt
	for i := 0; i < 3; i++ {
		w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
		require.Equal(t, http.StatusSeeOther, w.Code, "cycle %d approve, body: %s", i, w.Body.String())

		w = s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/unapprove", cookie)
		require.Equal(t, http.StatusSeeOther, w.Code, "cycle %d unapprove, body: %s", i, w.Body.String())

		got, err := s.store.Schedules().GetByID(ctx, entry.ID)
		require.NoError(t, err)
		require.Equal(t, store.ScheduleStateDraft, got.State, "cycle %d must return to draft after unapprove", i)
		require.Nil(t, got.ApprovedByPersonID, "cycle %d unapprove must clear the approver", i)

		submitted := slot.Add(time.Hour).Format(time.RFC3339)
		form := url.Values{"proposed_publish_at": {submitted}}
		w = s.doForm(t, "/schedule/"+entry.ID.String()+"/edit", cookie, form)
		require.Equal(t, http.StatusSeeOther, w.Code, "cycle %d edit, body: %s", i, w.Body.String())
		parsed, err := time.Parse(time.RFC3339, submitted)
		require.NoError(t, err)
		slot = parsed

		w = s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
		require.Equal(t, http.StatusSeeOther, w.Code, "cycle %d re-approve, body: %s", i, w.Body.String())

		w = s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/unapprove", cookie)
		require.Equal(t, http.StatusSeeOther, w.Code, "cycle %d unapprove-before-next-cycle, body: %s", i, w.Body.String())
	}

	// Final approve, mirroring the task's "ends committed with the edited
	// slot" wording.
	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateCommitted, got.State)
	assert.True(t, got.ProposedPublishAt.Equal(slot), "the final committed entry must carry the last edited slot, got %s want %s", got.ProposedPublishAt, slot)
	if assert.NotNil(t, got.ApprovedByPersonID) {
		assert.Equal(t, creator.ID, *got.ApprovedByPersonID)
	}
}

// ── FR20's published freeze ─────────────────────────────────────────────────

func TestPublishedFreeze_UnapproveAndEdit_Return409_StateUnchanged_AffordancesOmitted(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	proposedAt := time.Now().UTC().Add(24 * time.Hour)
	entry := s.draftEntry(t, ctx, ch, creator, proposedAt)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	// A live (confirmed) match to an actually-published video -- FR20's
	// exact "recorded as published" predicate.
	s.recordMatch(t, ctx, ch, entry.ID, store.MatchStateConfirmed, true)

	unapproveW := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/unapprove", cookie)
	assert.Equal(t, http.StatusConflict, unapproveW.Code, "un-approving a published entry must 409 (FR20)")

	editForm := url.Values{"proposed_publish_at": {proposedAt.Add(48 * time.Hour).Format(time.RFC3339)}}
	editW := s.doForm(t, "/schedule/"+entry.ID.String()+"/edit", cookie, editForm)
	assert.Equal(t, http.StatusConflict, editW.Code, "editing a published entry must 409 (FR20)")

	got, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateCommitted, got.State, "the freeze must leave state unchanged")
	// Compare against entry.ProposedPublishAt (already round-tripped
	// through Postgres by SaveDraft's RETURNING), not the raw proposedAt
	// Go value passed in, since timestamptz's microsecond precision would
	// otherwise make this comparison flaky.
	assert.True(t, got.ProposedPublishAt.Equal(entry.ProposedPublishAt), "the freeze must leave the slot unchanged")
	require.NotNil(t, got.ApprovedByPersonID, "the freeze must leave the approver unchanged")
	assert.Equal(t, creator.ID, *got.ApprovedByPersonID)

	listW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", cookie)
	require.Equal(t, http.StatusOK, listW.Code, "body: %s", listW.Body.String())
	body := listW.Body.String()
	assert.NotContains(t, body, "/unapprove", "a published entry's page must render no un-approve affordance")
	assert.NotContains(t, body, "/schedule/"+entry.ID.String()+"/edit", "a published entry's page must render no edit affordance")
	assert.Contains(t, body, "no longer be un-approved or edited", "a published entry's page must explain the freeze")
}

// A committed entry whose match is pending or rejected -- i.e. not
// published -- must still be un-approvable: the freeze triggers on a live
// match to a published video, not merely on the existence of a match row.
func TestUnpublishedMatch_PendingOrRejected_StillUnapprovable(t *testing.T) {
	for _, matchState := range []store.MatchState{store.MatchStatePending, store.MatchStateRejected} {
		t.Run(string(matchState), func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator := s.setupChannel(t, ctx)
			entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))
			cookie := s.sessionCookie(t, ctx, creator.ID)

			w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
			require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

			// The match's video IS published, but the match row itself is
			// not auto/confirmed -- must not freeze the entry.
			s.recordMatch(t, ctx, ch, entry.ID, matchState, true)

			unapproveW := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/unapprove", cookie)
			assert.Equal(t, http.StatusSeeOther, unapproveW.Code, "a %s match must not freeze the entry, body: %s", matchState, unapproveW.Body.String())

			got, err := s.store.Schedules().GetByID(ctx, entry.ID)
			require.NoError(t, err)
			assert.Equal(t, store.ScheduleStateDraft, got.State)
		})
	}
}

// A live (confirmed) match whose video has not published yet must also not
// freeze the entry -- Published requires BOTH a live match state AND a
// non-null synced_video.published_at.
func TestLiveMatch_VideoNotYetPublished_StillUnapprovable(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	s.recordMatch(t, ctx, ch, entry.ID, store.MatchStateConfirmed, false)

	unapproveW := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/unapprove", cookie)
	assert.Equal(t, http.StatusSeeOther, unapproveW.Code, "a confirmed match to an unpublished video must not freeze the entry, body: %s", unapproveW.Body.String())
}

// ── Approving an already-committed entry ────────────────────────────────────
//
// The store's Approve requires state = 'draft' (schedule.go's mutate
// translates any other failure to 409, since GetByID already confirmed the
// entry exists) -- this task's Testing section allows either an idempotent
// no-op or an explicit rejection; this codebase rejects, so that's what's
// asserted here (no duplicate state change, no approver overwrite).
func TestApprovingAlreadyCommittedEntry_Rejected_NoDuplicateStateChange(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	first, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, first.ApprovedAt)

	secondW := s.do(t, http.MethodPost, "/schedule/"+entry.ID.String()+"/approve", cookie)
	assert.Equal(t, http.StatusConflict, secondW.Code, "approving an already-committed entry must not silently succeed a second time")

	second, err := s.store.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateCommitted, second.State)
	require.NotNil(t, second.ApprovedAt)
	assert.True(t, first.ApprovedAt.Equal(*second.ApprovedAt), "a rejected re-approve must not overwrite approved_at")
}

// ── HandleList (FR19/FR20's read side): Creator + Analyst read, others not ──

func TestHandleList_CreatorAndAnalyst_CanRead_OutsiderForbidden(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	entry := s.draftEntry(t, ctx, ch, creator, time.Now().UTC().Add(24*time.Hour))

	// entryCard (views.templ) never renders the entry's raw uuid as text --
	// only inside the Creator-tier affordance form actions -- so the read
	// side is asserted via the bound idea's title, which IS rendered
	// unconditionally for both roles.
	var ideaTitle string
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT title FROM idea WHERE id = $1`, entry.IdeaID).Scan(&ideaTitle))

	analyst := s.newPerson(t, ctx, "analyst")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))
	outsider := s.newPerson(t, ctx, "outsider")

	creatorW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, creatorW.Code, "body: %s", creatorW.Body.String())
	creatorBody := creatorW.Body.String()
	assert.Contains(t, creatorBody, ideaTitle, "a Creator's view must render the entry")
	assert.Contains(t, creatorBody, "/schedule/"+entry.ID.String()+"/approve", "a Creator's view must render the approve affordance")

	analystW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", s.sessionCookie(t, ctx, analyst.ID))
	require.Equal(t, http.StatusOK, analystW.Code, "body: %s", analystW.Body.String())
	analystBody := analystW.Body.String()
	assert.Contains(t, analystBody, ideaTitle, "an Analyst must still be able to read the schedule (store.CanRead)")
	assert.NotContains(t, analystBody, "/schedule/"+entry.ID.String()+"/approve", "an Analyst's view must render no approve affordance")
	assert.NotContains(t, analystBody, "/schedule/"+entry.ID.String()+"/edit", "an Analyst's view must render no edit affordance")

	outsiderW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/schedule", s.sessionCookie(t, ctx, outsider.ID))
	assert.Equal(t, http.StatusForbidden, outsiderW.Code, "a Person with no role on the Channel must not be able to read its schedule")
}
