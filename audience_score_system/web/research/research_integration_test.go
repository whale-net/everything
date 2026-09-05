//go:build integration

// research_integration_test.go covers `web/research`'s HTTP surface
// against `research_note`/`viability_verdict` (milestone M4.1,
// FR1/FR2/FR8/FR9/FR10/NFR2, issue #1899): HandleChannelIndex's
// Founder/Co-Creator/Analyst read (same rows for all three), a
// non-member 403, an unknown Channel 404, a signed-out 401, a malformed
// Channel UUID 400, the FR9 unattached-notes section rendering an
// idea_id IS NULL note outside any Idea's row, and NFR2's 50-row default
// page with its truncation note and no paging control;
// HandleIdeaDetail's identical auth/404/400 ordering, its FR10
// cited/uncited indicator (read from store.ResearchNote.Cited(), never a
// local nil-check), its FR9 current + oldest-to-newest history rendering
// (each version's source, FR5), the empty-verdict 200 (never a 500), the
// cross-Channel Idea 404 guard (this file's one load-bearing red/green
// case), and a malformed Idea UUID 400; and HandleSaveNote (issue #1900,
// FR3/FR6/FR7/NFR1/NFR3): Founder/Co-Creator/Analyst parity, saving
// with/without idea_id and the resulting redirect target, FR6's
// same-idempotency-key double-submit producing exactly one row (a
// different key producing two), the FR7/NFR3 load-bearing forged-POST
// 403 from a signed-in non-member (and a signed-out reject) with no row
// created, source_url validation delegated entirely to
// store.ResearchStore.SaveNote (FR12 -- no second copy of the rule in
// this package), empty/whitespace text and cross-Channel idea_id
// rejections, and the save-note form's presence/hidden idempotency_key
// rendering. See
// //audience_score_system/web/schedule:schedule_integration_test for the
// harness pattern this file follows: spin up a throwaway Postgres via
// dbtest, apply the domain's own real embedded migrations, wire a real
// *store.Store and a real *auth.SessionManager against it, and drive
// research.Handlers through a small local http.ServeMux that mirrors
// `web`'s main.go route registrations for GET /channels/{id}/research,
// GET /channels/{id}/research/ideas/{ideaID}, and POST
// /channels/{id}/research/notes -- so PathValue resolution and
// auth.RequireSignedIn wrapping behave exactly as they do in production.
//
// A signed-in caller is simulated via auth.NewForTests + SessionManager.
// Establish, mirroring schedule_integration_test.go's rationale:
// HandleLogin/HandleCallback are already covered by web/auth's own tests,
// so establishing a real session row directly here proves everything this
// package's own routes own, not auth's OAuth mechanics a second time.
package research_test

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

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/research"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "test_ass_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("research-integration-test-key"))
}

// researchTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), the
// research.Handlers under test (exposed directly for the signed-out
// direct-call tests below), and a router that mirrors main.go's research
// route wiring (see this file's package doc comment).
type researchTestStack struct {
	store    *store.Store
	sessions *auth.SessionManager
	handlers *research.Handlers
	router   http.Handler
	db       *dbtest.Postgres
}

// newResearchTestStack provisions dbtest Postgres, applies the domain's
// real embedded migrations, and wires a real store.Store/auth.
// SessionManager/research.Handlers into a router equivalent to main.go's
// setupRoutes for this package's routes.
func newResearchTestStack(t *testing.T) *researchTestStack {
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
	res := research.New(st)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/research", a.RequireSignedIn(res.HandleChannelIndex))
	mux.HandleFunc("GET /channels/{id}/research/ideas/{ideaID}", a.RequireSignedIn(res.HandleIdeaDetail))
	mux.HandleFunc("POST /channels/{id}/research/notes", a.RequireSignedIn(res.HandleSaveNote))

	return &researchTestStack{store: st, sessions: sessions, handlers: res, router: mux, db: db}
}

// setupChannel creates a Channel with a live creator (Founder), mirroring
// store_integration_test.go's setupChannel fixture.
func (s *researchTestStack) setupChannel(t *testing.T, ctx context.Context) (store.Channel, store.Person) {
	t.Helper()
	creator, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-creator-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Test Channel", creator.ID)
	require.NoError(t, err)
	return ch, creator
}

// newPerson creates a fresh, role-less Person.
func (s *researchTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// sessionCookie establishes a real session row for personID and returns
// the resulting cookie, standing in for a completed sign-in (see this
// file's package doc comment).
func (s *researchTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
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

func (s *researchTestStack) do(t *testing.T, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
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
// router, mirroring what a rendered save-note <form method="post"> would
// submit -- HandleSaveNote's r.ParseForm() reads it exactly like a real
// browser submission.
func (s *researchTestStack) doForm(t *testing.T, target string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
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

// idempotencyKeyPattern extracts the hidden idempotency_key input's value
// from a rendered save-note form (both saveNoteFormChannelIndex and
// saveNoteFormIdeaDetail render it via the shared saveNoteFields), so
// tests can assert it is non-empty and that two separate GETs mint two
// different keys (FR6).
var idempotencyKeyPattern = regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`)

func extractIdempotencyKey(t *testing.T, body string) string {
	t.Helper()
	m := idempotencyKeyPattern.FindStringSubmatch(body)
	require.Len(t, m, 2, "hidden idempotency_key input must be present in the rendered form, body: %s", body)
	return m[1]
}

// ── HandleChannelIndex (FR1): Founder, Co-Creator, and Analyst all read
// the same rows; a non-member 403s, an unknown Channel 404s, signed-out
// 401s, a malformed Channel UUID 400s ────────────────────────────────────

func TestHandleChannelIndex_FounderCoCreatorAnalyst_SeeSameRows(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "a note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

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
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, tc.person.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), idea.Title, "%s must see the same idea", tc.name)
		})
	}
}

func TestHandleChannelIndex_NonMember_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, outsider.ID))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleChannelIndex_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+uuid.NewString()+"/research", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
}

// TestHandleChannelIndex_NotSignedIn_Unauthorized calls HandleChannelIndex
// directly, bypassing the router's RequireSignedIn wrapper (which
// redirects an unauthenticated request to /login rather than 401ing it)
// -- proving HandleChannelIndex's own defensive auth.PersonFromContext
// check, mirroring schedule_integration_test.go's
// TestHandleList_NotSignedIn_Unauthorized.
func TestHandleChannelIndex_NotSignedIn_Unauthorized(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	req := httptest.NewRequest(http.MethodGet, "/channels/"+ch.ID.String()+"/research", nil)
	req.SetPathValue("id", ch.ID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleChannelIndex(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleChannelIndex_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/not-a-uuid/research", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── HandleChannelIndex: FR1 note count / verdict presence, FR9 unattached
// notes, NFR2 truncation ────────────────────────────────────────────────

func TestHandleChannelIndex_RendersNoteCountVerdictPresence_AndUnattachedNotesSection(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	ideaWithVerdict, err := s.store.Ideas().Create(ctx, ch.ID, "Idea With Verdict", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &ideaWithVerdict.ID, Text: "note one", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &ideaWithVerdict.ID, Text: "note two", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaWithVerdict.ID, Verdict: store.VerdictViable, Reasoning: "looks good", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	ideaNoVerdict, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Verdict", creator.ID)
	require.NoError(t, err)

	unattached, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: nil, Text: "predates any idea", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, ideaWithVerdict.Title)
	assert.Contains(t, body, ideaNoVerdict.Title)
	assert.Contains(t, body, unattached.Text, "the unattached note must render in its own section")

	// The two-note idea must show "2" as its note count, adjacent to its
	// title's row -- a crude but effective check that ListByChannelWithStats'
	// count made it to the page.
	assert.Regexp(t, `Idea With Verdict[\s\S]{0,400}>2<`, body, "note count for the two-note idea must render")
	assert.Contains(t, body, "Verdict recorded", "the idea with a verdict must show verdict-recorded")
	assert.Contains(t, body, "None yet", "the idea without a verdict must show none-yet")

	// The unattached note must not be rendered as though it belonged to
	// either idea's row -- split the body at the unattached-notes section
	// heading and require the note text only appears after it.
	sectionIdx := strings.Index(body, "Unattached notes")
	require.Greater(t, sectionIdx, 0, "the unattached notes section heading must render")
	noteIdx := strings.Index(body, unattached.Text)
	assert.Greater(t, noteIdx, sectionIdx, "the unattached note must render inside the unattached-notes section, not under an idea")
}

func TestHandleChannelIndex_FiftyOneNotes_TruncatedNoteAppearsInUnattachedSection_NoPagingControl(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	for i := 0; i < 51; i++ {
		_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
			ChannelID: ch.ID, IdeaID: nil, Text: fmt.Sprintf("unattached note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
	}

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Equal(t, 50, strings.Count(body, "unattached note "), "exactly 50 notes must render")
	assert.Contains(t, body, "most recent", "a truncation note must appear when the 50-row default page is hit")
	// The Founder's save-note form (FR3, issue #1900) legitimately renders
	// one <form> on this page now; NFR2's actual guarantee is that no
	// SEPARATE paging/load-more control exists alongside it.
	assert.Equal(t, 1, strings.Count(body, "<form"), "exactly the save-note form may appear -- no paging control")
	assert.NotContains(t, strings.ToLower(body), "load more", "no load-more control may appear (NFR2)")
}

// ── HandleIdeaDetail (FR2, FR9, FR10): auth ordering, cross-Channel 404
// guard, cited/uncited rendering, verdict source/history ────────────────

func TestHandleIdeaDetail_MemberRoles_SeeSameRows(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "cited note", AuthorPersonID: creator.ID, SourceURL: strPtr("https://example.com/a"),
	})
	require.NoError(t, err)

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
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, tc.person.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "cited note")
		})
	}
}

func TestHandleIdeaDetail_NonMember_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, outsider.ID))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleIdeaDetail_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+uuid.NewString()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusNotFound, w.Code, "an unknown Channel must 404 before authorization runs, body: %s", w.Body.String())
}

// TestHandleIdeaDetail_NotSignedIn_Unauthorized proves the 401 -> 400 ->
// 404 -> 403 ordering starts with auth.PersonFromContext, mirroring
// TestHandleChannelIndex_NotSignedIn_Unauthorized.
func TestHandleIdeaDetail_NotSignedIn_Unauthorized(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), nil)
	req.SetPathValue("id", ch.ID.String())
	req.SetPathValue("ideaID", idea.ID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleIdeaDetail(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleIdeaDetail_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/not-a-uuid/research/ideas/"+uuid.NewString(), s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleIdeaDetail_MalformedIdeaUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/not-a-uuid", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleIdeaDetail_UnknownIdea_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+uuid.NewString(), s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandleIdeaDetail_CrossChannelIdea_NotFound is this file's one
// load-bearing red/green case: an Idea that exists, but under a
// DIFFERENT Channel than the path's {id}, must 404 exactly like an
// unknown Idea -- never 403, never rendered under the wrong Channel's
// URL. Breaking the ChannelID comparison in HandleIdeaDetail (e.g.
// dropping the guard, or returning the Idea regardless of Channel) must
// turn this test red.
func TestHandleIdeaDetail_CrossChannelIdea_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	chA, creatorA := s.setupChannel(t, ctx)
	chB, creatorB := s.setupChannel(t, ctx)
	ideaOnB, err := s.store.Ideas().Create(ctx, chB.ID, "Idea On B", creatorB.ID)
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+chA.ID.String()+"/research/ideas/"+ideaOnB.ID.String(), s.sessionCookie(t, ctx, creatorA.ID))
	assert.Equal(t, http.StatusNotFound, w.Code, "an Idea belonging to a different Channel must 404, not render, body: %s", w.Body.String())
	assert.NotContains(t, w.Body.String(), ideaOnB.Title, "the cross-Channel Idea's title must never render")
}

func TestHandleIdeaDetail_NoVerdict_RendersEmptySection_NotError(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Verdict", creator.ID)
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "no verdict yet must render 200, never a 500, body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "No verdict recorded yet")
}

// TestHandleIdeaDetail_ThreeVerdictVersions_OldestToNewest_WithSource
// covers FR9: all 3 versions render oldest-to-newest, each showing its
// source, and the highest version renders as current.
func TestHandleIdeaDetail_ThreeVerdictVersions_OldestToNewest_WithSource(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Versions", creator.ID)
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "reasoning v1", AuthorPersonID: creator.ID, Source: store.VerdictSourceAgent,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNotViable, Reasoning: "reasoning v2", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)
	v3, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "reasoning v3", AuthorPersonID: creator.ID, Source: store.VerdictSourceAgent,
	})
	require.NoError(t, err)
	require.Equal(t, 3, v3.Version)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "reasoning v1")
	assert.Contains(t, body, "reasoning v2")
	assert.Contains(t, body, "reasoning v3")

	// Current section renders v3 (the highest version) ahead of the
	// "History" heading; History then lists all 3 versions oldest to
	// newest, so v3's reasoning appears TWICE (once as current, once as
	// the last history entry) while v1/v2 appear once, inside History.
	currentIdx := strings.Index(body, "Current")
	historyIdx := strings.Index(body, "History")
	require.Greater(t, currentIdx, 0)
	require.Greater(t, historyIdx, 0)
	assert.Less(t, currentIdx, historyIdx, `"Current" must render before the "History" heading`)

	firstV3Idx := strings.Index(body, "reasoning v3")
	require.Greater(t, firstV3Idx, 0)
	assert.Less(t, firstV3Idx, historyIdx, "the current verdict's reasoning (v3, the highest version) must render before the History heading")

	// Within the History section: v1 before v2 before v3 (oldest to
	// newest).
	historySection := body[historyIdx:]
	idx1 := strings.Index(historySection, "reasoning v1")
	idx2 := strings.Index(historySection, "reasoning v2")
	idx3 := strings.Index(historySection, "reasoning v3")
	require.Greater(t, idx1, 0, "v1 must render in the History section")
	require.Greater(t, idx2, 0, "v2 must render in the History section")
	require.Greater(t, idx3, 0, "v3 must render in the History section")
	assert.Less(t, idx1, idx2, "v1 must render before v2 (oldest to newest)")
	assert.Less(t, idx2, idx3, "v2 must render before v3 (oldest to newest)")

	assert.Contains(t, body, "Agent", "an agent-sourced verdict must render its source")
	assert.Contains(t, body, "Human", "a human-sourced verdict must render its source")
}

// TestHandleIdeaDetail_CitedAndUncitedNotes_RenderFromCited proves the
// cited/uncited indicator is read from store.ResearchNote.Cited(): a note
// with SourceURL set renders as cited (with its URL), and a note with a
// nil SourceURL renders as uncited.
func TestHandleIdeaDetail_CitedAndUncitedNotes_RenderFromCited(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Notes", creator.ID)
	require.NoError(t, err)

	cited, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "cited note text", AuthorPersonID: creator.ID, SourceURL: strPtr("https://example.com/source"),
	})
	require.NoError(t, err)
	require.True(t, cited.Cited())

	uncited, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "uncited note text", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.False(t, uncited.Cited())

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "cited note text")
	assert.Contains(t, body, "uncited note text")
	assert.Contains(t, body, "https://example.com/source", "a cited note's source_url must render as a link")
	assert.Contains(t, body, "Cited")
	assert.Contains(t, body, "Uncited")
}

func TestHandleIdeaDetail_FiftyOneNotes_TruncatedNoPagingControl(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Many Notes", creator.ID)
	require.NoError(t, err)

	for i := 0; i < 51; i++ {
		_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
			ChannelID: ch.ID, IdeaID: &idea.ID, Text: fmt.Sprintf("idea note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
	}

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Equal(t, 50, strings.Count(body, "idea note "), "exactly 50 notes must render")
	assert.Contains(t, body, "most recent", "a truncation note must appear")
	// The Founder's save-note form (FR3, issue #1900) legitimately renders
	// one <form> on this page now; NFR2's actual guarantee is that no
	// SEPARATE paging/load-more control exists alongside it.
	assert.Equal(t, 1, strings.Count(body, "<form"), "exactly the save-note form may appear -- no paging control")
	assert.NotContains(t, strings.ToLower(body), "load more", "no load-more control may appear (NFR2)")
}

// ── No PAGING form/affordance anywhere on either page (superseded by
// issue #1900's save-note form, which is the one and only <form> either
// page may legitimately render for a store.CanWrite member) ─────────────

// TestChannelIndexAndIdeaDetail_ExactlyOneForm_TheSaveNoteForm_NoPagingAffordance
// replaces this file's original #1899-era "no form anywhere" assertion,
// which predated FR3's save-note form and is no longer the correct
// expectation: both pages now legitimately render exactly one <form>
// (the save-note form, method="post", pointed at
// /channels/{id}/research/notes) for a store.CanWrite member -- what
// must still never appear is any SECOND form or paging/load-more
// control.
func TestChannelIndexAndIdeaDetail_ExactlyOneForm_TheSaveNoteForm_NoPagingAffordance(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	indexW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", cookie)
	require.Equal(t, http.StatusOK, indexW.Code, "body: %s", indexW.Body.String())
	indexBody := indexW.Body.String()
	assert.Equal(t, 1, strings.Count(indexBody, "<form"), "exactly one form -- the save-note form -- may appear")
	assert.Equal(t, 1, strings.Count(strings.ToLower(indexBody), `method="post"`))
	assert.Contains(t, indexBody, `action="/channels/`+ch.ID.String()+`/research/notes"`)

	detailW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), cookie)
	require.Equal(t, http.StatusOK, detailW.Code, "body: %s", detailW.Body.String())
	detailBody := detailW.Body.String()
	assert.Equal(t, 1, strings.Count(detailBody, "<form"), "exactly one form -- the save-note form -- may appear")
	assert.Equal(t, 1, strings.Count(strings.ToLower(detailBody), `method="post"`))
	assert.Contains(t, detailBody, `action="/channels/`+ch.ID.String()+`/research/notes"`)
}

// ── HandleSaveNote (FR3, FR6, FR7, NFR1, NFR3, issue #1900) ─────────────

// allNotes is a small helper that returns every research_note row on ch,
// used to assert row counts/contents after a POST rather than re-deriving
// them from the response body.
func (s *researchTestStack) allNotes(t *testing.T, ctx context.Context, channelID uuid.UUID) []store.ResearchNoteWithAuthor {
	t.Helper()
	notes, _, err := s.store.Research().ListFiltered(ctx, channelID, nil, nil, nil, nil, 0)
	require.NoError(t, err)
	return notes
}

func TestHandleSaveNote_TextOnly_CreatesUnattachedNote_RedirectsToChannelIndex(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"a plain research note"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "/channels/"+ch.ID.String()+"/research", w.Header().Get("Location"))

	notes := s.allNotes(t, ctx, ch.ID)
	require.Len(t, notes, 1)
	assert.Equal(t, "a plain research note", notes[0].Text)
	assert.Nil(t, notes[0].IdeaID, "no idea_id was submitted, so the note must be unattached (M1 FR9)")
	assert.False(t, notes[0].Cited(), "no source_url was submitted")
}

// TestHandleSaveNote_CoCreatorAndAnalyst_CanSave proves all three
// store.CanWrite roles -- not just the Founder -- can save a note through
// this handler.
func TestHandleSaveNote_CoCreatorAndAnalyst_CanSave(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	coCreator := s.newPerson(t, ctx, "co-creator")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))
	analyst := s.newPerson(t, ctx, "analyst")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	for _, tc := range []struct {
		name   string
		person store.Person
	}{
		{"CoCreator", coCreator},
		{"Analyst", analyst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, tc.person.ID), url.Values{
				"idempotency_key": {uuid.NewString()},
				"text":            {tc.name + "'s note"},
			})
			assert.Equal(t, http.StatusSeeOther, w.Code, "%s must be able to save, body: %s", tc.name, w.Body.String())
		})
	}

	notes := s.allNotes(t, ctx, ch.ID)
	assert.Len(t, notes, 2)
}

func TestHandleSaveNote_WithIdeaID_AttachesAndRedirectsToIdeaDetail(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"attached note"},
		"idea_id":         {idea.ID.String()},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), w.Header().Get("Location"))

	notes := s.allNotes(t, ctx, ch.ID)
	require.Len(t, notes, 1)
	require.NotNil(t, notes[0].IdeaID)
	assert.Equal(t, idea.ID, *notes[0].IdeaID)
}

// TestHandleSaveNote_SameIdempotencyKey_Twice_CreatesOneRow is FR6/NFR1's
// load-bearing double-submit case: the browser back-button/refresh
// replaying the exact same POST (same server-generated idempotency_key,
// identical content) must not create a second row, using
// store.ResearchStore.SaveNote's existing (channel, author, key) dedupe
// with no separate web-side mechanism -- and the replayed response is
// still a 303, not an error.
func TestHandleSaveNote_SameIdempotencyKey_Twice_CreatesOneRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)
	key := uuid.NewString()
	form := url.Values{
		"idempotency_key": {key},
		"text":            {"replayed note"},
	}

	w1 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, form)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())
	require.Len(t, s.allNotes(t, ctx, ch.ID), 1)

	w2 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, form)
	assert.Equal(t, http.StatusSeeOther, w2.Code, "a replayed submit must still redirect, not error, body: %s", w2.Body.String())
	assert.Len(t, s.allNotes(t, ctx, ch.ID), 1, "a replayed idempotency_key must not create a second row")
}

// TestHandleSaveNote_DifferentIdempotencyKeys_CreatesTwoRows proves dedupe
// is keyed by idempotency_key, not by content: two different keys with
// identical text/idea_id must both persist.
func TestHandleSaveNote_DifferentIdempotencyKeys_CreatesTwoRows(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w1 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"identical content"},
	})
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	w2 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"identical content"},
	})
	require.Equal(t, http.StatusSeeOther, w2.Code, "body: %s", w2.Body.String())

	assert.Len(t, s.allNotes(t, ctx, ch.ID), 2, "different idempotency keys with identical content must both persist")
}

// TestHandleSaveNote_NonMember_Forbidden_NoRowCreated is FR7/NFR3's
// load-bearing authorization test: a forged POST from a signed-in
// non-member is 403 even though the save-note form was never rendered
// for them (they cannot even GET the page, let alone see the form) --
// asserted through the real handler/router, not a unit-level check, and
// the response carries no note row.
func TestHandleSaveNote_NonMember_Forbidden_NoRowCreated(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, outsider.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"forged note"},
	})
	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	assert.NotContains(t, w.Body.String(), "forged note")
	assert.Empty(t, s.allNotes(t, ctx, ch.ID), "a forbidden POST must not create a row")
}

// TestHandleSaveNote_SignedOut_Rejected_NoRowCreated covers the
// signed-out half of FR7/NFR3: through the router (RequireSignedIn), a
// signed-out POST never reaches the handler's own logic and redirects to
// /login rather than creating a row; calling the handler directly (as
// the other HandleXxx_NotSignedIn_Unauthorized tests in this file do)
// proves authorizeWrite's own defensive 401 as well.
func TestHandleSaveNote_SignedOut_Rejected_NoRowCreated(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", nil, url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"signed-out note"},
	})
	assert.NotEqual(t, http.StatusSeeOther, w.Code, "a signed-out POST must never succeed, body: %s", w.Body.String())
	assert.Empty(t, s.allNotes(t, ctx, ch.ID), "a signed-out POST must not create a row")

	req := httptest.NewRequest(http.MethodPost, "/channels/"+ch.ID.String()+"/research/notes", strings.NewReader(url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"direct signed-out note"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", ch.ID.String())
	rec := httptest.NewRecorder()
	s.handlers.HandleSaveNote(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, s.allNotes(t, ctx, ch.ID))
}

// TestHandleSaveNote_ValidSourceURL_SavesAndRendersCited and
// TestHandleSaveNote_InvalidSourceURL_BadRequest_NoRow together prove
// source_url validation is delegated entirely to
// store.ResearchStore.SaveNote (FR12): a valid https:// URL saves and
// renders as cited, and "not a url" is rejected with the STORE's own
// message (never a copy validated in web/research), inserting no row.
func TestHandleSaveNote_ValidSourceURL_SavesAndRendersCited(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"cited note"},
		"source_url":      {"https://example.com/source"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	notes := s.allNotes(t, ctx, ch.ID)
	require.Len(t, notes, 1)
	assert.True(t, notes[0].Cited())
	require.NotNil(t, notes[0].SourceURL)
	assert.Equal(t, "https://example.com/source", *notes[0].SourceURL)
}

func TestHandleSaveNote_InvalidSourceURL_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"has a bad source"},
		"source_url":      {"not a url"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "must be an absolute", "the STORE's own validateSourceURL message must render, proving web carries no second copy of the rule")
	assert.Empty(t, s.allNotes(t, ctx, ch.ID))
}

func TestHandleSaveNote_EmptyOrWhitespaceText_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	for _, tc := range []struct {
		name string
		text string
	}{
		{"Empty", ""},
		{"WhitespaceOnly", "   \n\t  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, url.Values{
				"idempotency_key": {uuid.NewString()},
				"text":            {tc.text},
			})
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "note text is required")
		})
	}
	assert.Empty(t, s.allNotes(t, ctx, ch.ID))
}

// TestHandleSaveNote_CrossChannelIdeaID_BadRequest_NoRow mirrors
// HandleIdeaDetail's cross-Channel 404 guard, but as a 400 here since
// idea_id arrives as a form field (client input), not a URL path
// segment: an Idea that exists under a DIFFERENT Channel than the POST's
// {id} must never attach a note to it.
func TestHandleSaveNote_CrossChannelIdeaID_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	chA, creatorA := s.setupChannel(t, ctx)
	chB, creatorB := s.setupChannel(t, ctx)
	ideaOnB, err := s.store.Ideas().Create(ctx, chB.ID, "Idea On B", creatorB.ID)
	require.NoError(t, err)

	w := s.doForm(t, "/channels/"+chA.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creatorA.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"cross-channel attempt"},
		"idea_id":         {ideaOnB.ID.String()},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid idea selection")
	assert.Empty(t, s.allNotes(t, ctx, chA.ID))
	assert.Empty(t, s.allNotes(t, ctx, chB.ID))
}

func TestHandleSaveNote_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/not-a-uuid/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"note"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSaveNote_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+uuid.NewString()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"note"},
	})
	assert.Equal(t, http.StatusNotFound, w.Code, "an unknown Channel must 404 before authorization runs, body: %s", w.Body.String())
}

// ── Save-note form rendering (FR3, FR6, FR7) ────────────────────────────

// TestSaveNoteForm_PresentForFounderCoCreatorAnalyst_OnBothPages proves
// the save-note form renders for every store.CanWrite role on both
// ChannelIndex and IdeaDetail -- in this domain CanRead and CanWrite grant
// the exact same three-role set (store.CanRead/CanWrite, store/authz.go),
// so there is no "can read but not write" member to render the negative
// case against; a non-member cannot even reach a 200 render (403s on the
// GET itself, see TestHandleChannelIndex_NonMember_Forbidden/
// TestHandleIdeaDetail_NonMember_Forbidden), which is exactly why the
// form's absence is documented as presentation-only rather than the
// actual authorization boundary (see research.go's package doc comment).
func TestSaveNoteForm_PresentForFounderCoCreatorAnalyst_OnBothPages(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

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
			cookie := s.sessionCookie(t, ctx, tc.person.ID)

			indexW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", cookie)
			require.Equal(t, http.StatusOK, indexW.Code, "body: %s", indexW.Body.String())
			assert.Contains(t, indexW.Body.String(), `action="/channels/`+ch.ID.String()+`/research/notes"`, "%s must see the save-note form on the Channel index", tc.name)

			detailW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), cookie)
			require.Equal(t, http.StatusOK, detailW.Code, "body: %s", detailW.Body.String())
			assert.Contains(t, detailW.Body.String(), `action="/channels/`+ch.ID.String()+`/research/notes"`, "%s must see the save-note form on the Idea detail page", tc.name)
		})
	}
}

// TestSaveNoteForm_HiddenIdempotencyKey_NonEmpty_DiffersAcrossGETs proves
// FR6's server-generated, render-time idempotency key: present and
// non-empty on both pages, and two separate GETs of the same page mint
// two DIFFERENT keys (never derived from form content, never reused).
func TestSaveNoteForm_HiddenIdempotencyKey_NonEmpty_DiffersAcrossGETs(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	indexW1 := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", cookie)
	require.Equal(t, http.StatusOK, indexW1.Code)
	key1 := extractIdempotencyKey(t, indexW1.Body.String())
	assert.NotEmpty(t, key1)

	indexW2 := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", cookie)
	require.Equal(t, http.StatusOK, indexW2.Code)
	key2 := extractIdempotencyKey(t, indexW2.Body.String())
	assert.NotEmpty(t, key2)
	assert.NotEqual(t, key1, key2, "two separate GETs must mint two different idempotency keys")

	detailW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), cookie)
	require.Equal(t, http.StatusOK, detailW.Code)
	detailKey := extractIdempotencyKey(t, detailW.Body.String())
	assert.NotEmpty(t, detailKey)
}

func strPtr(s string) *string { return &s }
