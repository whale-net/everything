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
// rendering; and HandleSaveVerdict (issue #1901, FR4/FR5/FR6/FR7/NFR1/
// NFR3): Founder/Co-Creator/Analyst parity appending a new `source =
// human` version through the identical store.VerdictStore.Append method
// save_viability_verdict calls (LB5), the append-only guarantee (earlier
// versions unchanged after a new append, M1 FR12), cited_note_ids
// populating verdict_citation with exactly the submitted notes and
// rejecting a note ID belonging to a different Idea (400, nothing
// written), FR6's same-idempotency-key double-submit producing exactly
// one version (a different key producing two), the FR7/NFR3 load-bearing
// forged-POST 403 from a signed-in non-member (History left unchanged)
// and a signed-out reject, invalid verdict/empty reasoning/cross-Channel
// Idea rejections, cross-surface agreement between a human-sourced and an
// agent-sourced version on the same Idea, and the save-verdict form's
// citation multi-select scoping/hidden idempotency_key rendering; and
// HandleProposeVideoScript (issue #1915, FR1-FR5/NFR1-NFR3): the FR1
// render gate (a viable current verdict AND store.CanWrite) both positive
// and negative, FR2's active-Strategy-only picker (excluding an inactive
// Strategy and a different Channel's Strategy) and its empty-Channel
// explanatory-line/no-submit case, FR3's happy path through the IDENTICAL
// store.VideoScriptStore.Propose method save_video_script calls (LB5) and
// its "verdict_id is never a form field" guarantee (a forged verdict_id
// form value is ignored, the row still binds to the Idea's own current
// verdict), the FR4 load-bearing forged-POST 403 from a signed-in
// non-member plus the cross-Channel Idea 404 guard, FR5/NFR1's
// same-idempotency-key double-submit producing exactly one row (a
// different key producing a second), plain title/script_text validation
// with the same idempotency_key echoed on re-render, and NFR3's
// stale-at-submit cases (a Strategy deleted, and a verdict superseded,
// between render and submit -- see this file's own two FAILING tests
// covering that pair, `TestHandleProposeVideoScript_
// StrategyDeletedBeforeSubmit_BadRequest_NoRow` and
// `TestHandleProposeVideoScript_VerdictBecomesNonViableBeforeSubmit_
// BadRequest_NoRow`, which reveal a real Implementation-phase defect:
// IdeaDetail's/proposeVideoScriptForm's render gates re-evaluate
// current/activeStrategies fresh on the error re-render too, so when
// either one no longer holds by submit time the propose section's error
// message and preserved field values are silently dropped even though the
// response is still a 400, not a 500 or a redirect -- see this task's
// (#1915) Testing-phase status comment for the routed-back defect
// report). See //audience_score_system/web/schedule:schedule_integration_test
// for the harness pattern this file follows: spin up a throwaway Postgres
// via dbtest, apply the domain's own real embedded migrations, wire a
// real *store.Store and a real *auth.SessionManager against it, and drive
// research.Handlers through a small local http.ServeMux that mirrors
// `web`'s main.go route registrations for GET /channels/{id}/research,
// GET /channels/{id}/research/ideas/{ideaID}, POST
// /channels/{id}/research/notes, POST
// /channels/{id}/research/ideas/{ideaID}/verdicts, and POST
// /channels/{id}/research/ideas/{ideaID}/video-scripts -- so PathValue
// resolution and auth.RequireSignedIn wrapping behave exactly as they do
// in production.
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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpserver "github.com/whale-net/everything/audience_score_system/mcp/server"
	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
	"github.com/whale-net/everything/audience_score_system/web/research"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
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
	mux.HandleFunc("POST /channels/{id}/research/ideas/{ideaID}/verdicts", a.RequireSignedIn(res.HandleSaveVerdict))
	mux.HandleFunc("POST /channels/{id}/research/ideas/{ideaID}/video-scripts", a.RequireSignedIn(res.HandleProposeVideoScript))

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

// ── #1943 (FR9, FR16, NFR2) test infrastructure: a SQL query counter for
// the batching assertion, and a real in-process MCP client/server wired
// against this SAME researchTestStack's Postgres for the web/MCP excerpt
// parity assertion ──────────────────────────────────────────────────────

// researchQueryCounter is a pgx.QueryTracer that counts every SQL
// statement issued through the pool it's attached to -- mirrors
// channels_integration_test.go's channelsQueryCounter (#1716's pattern).
type researchQueryCounter struct{ n int64 }

func (c *researchQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n++
	return ctx
}

func (c *researchQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// tracedResearchStack builds a second router against the SAME database as
// s, but through a pool whose every query is counted by counter --
// mirrors channels_integration_test.go's tracedChannelsStack.
func (s *researchTestStack) tracedResearchStack(t *testing.T, ctx context.Context, counter *researchQueryCounter) *researchTestStack {
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
	res := research.New(st)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/research", a.RequireSignedIn(res.HandleChannelIndex))
	mux.HandleFunc("GET /channels/{id}/research/ideas/{ideaID}", a.RequireSignedIn(res.HandleIdeaDetail))

	return &researchTestStack{store: st, sessions: sessions, handlers: res, router: mux, db: s.db}
}

// mcpFixture is a real MCP server (mcptools.RegisterVerdict) hosted over a
// real in-process HTTP transport, backed by the SAME *store.Store (and
// thus the SAME Postgres data) as the researchTestStack it's built from --
// so a get_viability_verdict call and a GET .../research/ideas/{id} call
// can be compared against the identical underlying row for the excerpt
// parity test (FR16/NFR2). Mirrors mcp/tools/verdict_integration_test.go's
// verdictFixture, trimmed to just what parity needs.
type mcpFixture struct {
	creds mcpauth.CredentialStore
	url   string
}

func newMCPFixture(t *testing.T, s *researchTestStack) *mcpFixture {
	t.Helper()
	ctx := context.Background()

	creds, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{
		Pool:           s.db.Pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	require.NoError(t, err)

	srv := mcpserver.New(s.store)
	reg := mcpserver.NewRegistry(srv, s.store)
	mcptools.RegisterVerdict(reg, s.store)
	// RegisterResearch backs listResearchNotes below -- FR11/NFR2's
	// (#1942) web/MCP relations-parity test.
	mcptools.RegisterResearch(reg, s.store)

	handler := mcpserver.NewHTTPHandler(srv, creds, mcpserver.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &mcpFixture{creds: creds, url: ts.URL}
}

// mcpBearerRoundTripper injects an "Authorization: Bearer <token>" header
// on every request -- mirrors verdict_integration_test.go's
// bearerRoundTripper.
type mcpBearerRoundTripper struct{ token string }

func (rt mcpBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// connect opens a real streamable-HTTP MCP client session authenticated
// as personID.
func (f *mcpFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: mcpBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// getViabilityVerdict calls get_viability_verdict and decodes its
// structured result.
func (f *mcpFixture) getViabilityVerdict(t *testing.T, cs *mcp.ClientSession, channelID, ideaID uuid.UUID) mcptools.GetViabilityVerdictOutput {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_viability_verdict",
		Arguments: mcptools.GetViabilityVerdictInput{
			ChannelID: channelID.String(),
			IdeaID:    ideaID.String(),
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected get_viability_verdict error")

	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out mcptools.GetViabilityVerdictOutput
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// listResearchNotes calls list_research_notes and decodes its structured
// result -- backs FR11/NFR2's (#1942) web/MCP relations-parity test.
func (f *mcpFixture) listResearchNotes(t *testing.T, cs *mcp.ClientSession, channelID uuid.UUID) mcptools.ListResearchNotesOutput {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_research_notes",
		Arguments: mcptools.ListResearchNotesInput{ChannelID: channelID.String()},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected list_research_notes error")

	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out mcptools.ListResearchNotesOutput
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// saveResearchNote calls save_research_note and decodes its structured
// result -- backs FR14/FR15/FR16's (#1945) web/MCP save-path parity test:
// the same store.ResearchStore.SaveNote call, driven from the identical
// wire shape (thread_id/thread_title, relations) this handler's form
// posts, must produce equivalent rows regardless of which surface drove
// it.
func (f *mcpFixture) saveResearchNote(t *testing.T, cs *mcp.ClientSession, in mcptools.SaveResearchNoteInput) mcptools.ResearchNoteOutput {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "save_research_note",
		Arguments: in,
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected save_research_note error: %v", res.Content)

	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out mcptools.ResearchNoteOutput
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// idempotencyKeyPattern extracts the hidden idempotency_key input's value
// from a rendered save-note form (both saveNoteFormChannelIndex and
// saveNoteFormIdeaDetail render it via the shared saveNoteFields), so
// tests can assert it is non-empty and that two separate GETs mint two
// different keys (FR6).
var idempotencyKeyPattern = regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`)

// citedNoteTextPattern matches ONLY views.templ's citedNoteBody's own text
// paragraph (`<p class="text-sm">{ excerpt }</p>`) -- distinct from
// noteBody's plain `<p>{ note.Text }</p>` (no class) and the save-verdict
// form's citation multi-select `<option>` (which also renders a note's
// FULL untruncated text as its label) -- so a truncation assertion can
// scope to exactly the cited-notes-section renders and never be satisfied
// by the SAME note's full text legitimately appearing elsewhere on the
// same page.
var citedNoteTextPattern = regexp.MustCompile(`<p class="text-sm">([^<]*)</p>`)

// citedNoteExcerpts extracts every citedNoteBody text excerpt rendered
// anywhere on the page (see citedNoteTextPattern).
func citedNoteExcerpts(body string) []string {
	matches := citedNoteTextPattern.FindAllStringSubmatch(body, -1)
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m[1]
	}
	return out
}

func extractIdempotencyKey(t *testing.T, body string) string {
	t.Helper()
	m := idempotencyKeyPattern.FindStringSubmatch(body)
	require.Len(t, m, 2, "hidden idempotency_key input must be present in the rendered form, body: %s", body)
	return m[1]
}

// extractVerdictFormIdempotencyKey extracts the hidden idempotency_key
// input's value from the save-verdict form specifically (#1901): Idea
// detail renders the save-note form's key BEFORE the save-verdict form's
// (see views.templ's IdeaDetail -- research notes section, then
// "Save a viability verdict"), so this slices body to the substring
// starting at that heading before applying idempotencyKeyPattern, rather
// than risking extractIdempotencyKey's first-match picking up the note
// form's key instead.
func extractVerdictFormIdempotencyKey(t *testing.T, body string) string {
	t.Helper()
	idx := strings.Index(body, "Save a viability verdict")
	require.Greater(t, idx, 0, "the save-verdict form's heading must render, body: %s", body)
	return extractIdempotencyKey(t, body[idx:])
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
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
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
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &ideaWithVerdict.ID, Text: "note one", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &ideaWithVerdict.ID, Text: "note two", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaWithVerdict.ID, Verdict: store.VerdictViable, Reasoning: "looks good", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	ideaNoVerdict, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Verdict", creator.ID)
	require.NoError(t, err)

	unattached, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
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
	// FR31/FR32 (#2028): the verdict-presence indicator is a single glyph
	// (with an accessible title/aria-label carrying the old text), never a
	// badge/box. Scope the badge-absence check to the ideas table itself
	// (between its own header row and closing tag) -- the page's
	// unattached-notes section legitimately renders unrelated "badge
	// badge-ghost"/"badge badge-success" Cited/Uncited indicators
	// (citedBadge), which must not make this assertion a false negative.
	assert.Contains(t, body, `title="Verdict recorded"`, "the idea with a verdict must show the verdict-recorded glyph's accessible title")
	assert.Contains(t, body, `title="No verdict yet"`, "the idea without a verdict must show the no-verdict glyph's accessible title")
	assert.Contains(t, body, components.NoVerdictGlyph, "the idea without a verdict must render the shared no-verdict glyph")
	ideasTableStart := strings.Index(body, "<th>Idea</th>")
	require.Greater(t, ideasTableStart, 0, "the ideas table header must render")
	ideasTableEnd := strings.Index(body[ideasTableStart:], "</table>")
	require.Greater(t, ideasTableEnd, 0, "the ideas table must close")
	ideasTable := body[ideasTableStart : ideasTableStart+ideasTableEnd]
	assert.NotContains(t, ideasTable, "badge", "the ideas table's verdict-presence indicator must never render as a badge/box (FR31)")

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
		_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID, IdeaID: nil, Text: fmt.Sprintf("unattached note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
	}

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	// Each of the 50 rendered notes appears TWICE: once in the unattached-
	// notes section itself, and once as a relation-picker candidate in the
	// save-note form's single thread group (#1945, FR15) -- both populated
	// from the SAME Channel-wide notes page, no extra store call.
	assert.Equal(t, 100, strings.Count(body, "unattached note "), "exactly 50 notes must render, each once in the unattached section and once in the relation picker")
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
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
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

	// FR31/FR32 (#2028): each verdict's status renders as a single glyph
	// (via components.VerdictGlyph), never the old badge-warning/
	// badge-error verdict box -- needs-more-research and not-viable are
	// exactly the two verdict values whose old badge classes would appear
	// here if the badge/box rendering had regressed.
	assert.Contains(t, body, components.VerdictGlyph(store.VerdictNeedsMoreResearch), "v1's needs-more-research verdict must render its glyph")
	assert.Contains(t, body, components.VerdictGlyph(store.VerdictNotViable), "v2's not-viable verdict must render its glyph")
	assert.Contains(t, body, components.VerdictGlyph(store.VerdictViable), "v3's viable verdict must render its glyph")
	assert.NotContains(t, body, "badge badge-warning", "no verdict status may render as the old needs-more-research badge/box (FR31)")
	assert.NotContains(t, body, "badge badge-error", "no verdict status may render as the old not-viable badge/box (FR31)")
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

	cited, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "cited note text", AuthorPersonID: creator.ID, SourceURL: strPtr("https://example.com/source"),
	})
	require.NoError(t, err)
	require.True(t, cited.Cited())

	uncited, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
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

// ── FR9/FR16/NFR2 (#1943): a verdict's CitedResearchNoteIDs render, for
// BOTH current and every history entry, resolved via ONE batched read and
// truncated at the SAME bound get_viability_verdict uses ─────────────────

// TestHandleIdeaDetail_CurrentVerdictCitedNotes_TextSourceURLAndBadge
// covers the base FR9 rendering on the current verdict: an uncited note's
// text (no source_url, "Uncited" badge) and a cited note's text, its
// source_url, and its "Cited" badge -- scoped to JUST current's own
// "Cited notes" section (the slice between the "Current" and "History"
// headings, mirroring TestHandleIdeaDetail_ThreeVerdictVersions_
// OldestToNewest_WithSource's currentIdx/historyIdx technique), so this
// can never be satisfied by the separate research-note list's own
// (differently-derived-but-identical) badge rendering instead.
func TestHandleIdeaDetail_CurrentVerdictCitedNotes_TextSourceURLAndBadge(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Cited Verdict", creator.ID)
	require.NoError(t, err)

	uncitedNote, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "verdict-cited note with no source", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	citedNote, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "verdict-cited note with a source", AuthorPersonID: creator.ID, SourceURL: strPtr("https://example.com/verdict-citation"),
	})
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "citing both notes", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{uncitedNote.ID, citedNote.ID},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	currentIdx := strings.Index(body, "Current")
	historyIdx := strings.Index(body, "History")
	require.Greater(t, currentIdx, 0)
	require.Greater(t, historyIdx, currentIdx)
	currentSection := body[currentIdx:historyIdx]

	assert.Contains(t, currentSection, "Cited notes", `current's verdict must render a "Cited notes" heading`)
	assert.Contains(t, currentSection, uncitedNote.Text)
	assert.Contains(t, currentSection, citedNote.Text)
	assert.Contains(t, currentSection, "https://example.com/verdict-citation")

	uncitedIdx := strings.Index(currentSection, uncitedNote.Text)
	citedIdx := strings.Index(currentSection, citedNote.Text)
	require.Greater(t, uncitedIdx, 0)
	require.Greater(t, citedIdx, 0)
	// citedBadge renders immediately after each note's body (views.templ's
	// citedNoteBody); a window past each note's text is enough to catch
	// its own badge without spilling into the next <li>.
	window := 300
	uncitedWindow := currentSection[uncitedIdx:min(uncitedIdx+window, len(currentSection))]
	citedWindow := currentSection[citedIdx:min(citedIdx+window, len(currentSection))]
	assert.Contains(t, uncitedWindow, "Uncited", "the note with no source_url must render the Uncited badge in its own cited-notes entry")
	assert.Contains(t, citedWindow, "Cited", "the note with a source_url must render the Cited badge in its own cited-notes entry")
}

// TestHandleIdeaDetail_HistoryVerdictCitedNotes_RenderedOnItsOwnVersionOnly
// proves FR9's "for history entries too, not only current": a note cited
// ONLY by an earlier (history-only) version must render inside the
// History section but never inside Current's own section, which cites a
// different note entirely.
func TestHandleIdeaDetail_HistoryVerdictCitedNotes_RenderedOnItsOwnVersionOnly(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Version Citations", creator.ID)
	require.NoError(t, err)

	historyOnlyNote, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "note cited only by the superseded v1 verdict", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	currentNote, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "note cited only by the current v2 verdict", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "v1 reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{historyOnlyNote.ID},
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "v2 reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{currentNote.ID},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	currentIdx := strings.Index(body, "Current")
	historyIdx := strings.Index(body, "History")
	require.Greater(t, currentIdx, 0)
	require.Greater(t, historyIdx, currentIdx)
	currentSection := body[currentIdx:historyIdx]
	historySection := body[historyIdx:]

	assert.Contains(t, currentSection, currentNote.Text, "current (v2) must render its own cited note")
	assert.NotContains(t, currentSection, historyOnlyNote.Text, "current (v2) must never render v1's cited note")

	assert.Contains(t, historySection, historyOnlyNote.Text, "the History section must render v1's cited note against v1's own entry")
	assert.Contains(t, historySection, currentNote.Text, "the History section must also render v2's cited note against v2's own entry (v2 appears in History too)")
}

// TestHandleIdeaDetail_CitedNoteTextExceeding200Runes_TruncatedAtSharedBound
// proves the 200-rune truncation applied to a verdict's cited-note text is
// the SAME bound/helper get_viability_verdict uses (mcptools.Excerpt) --
// asserted against that shared helper's actual output, never a hardcoded
// string, so the two can never drift apart (NFR2).
func TestHandleIdeaDetail_CitedNoteTextExceeding200Runes_TruncatedAtSharedBound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Long Note", creator.ID)
	require.NoError(t, err)

	longText := strings.Repeat("z", mcptools.CitationExcerptRunes+75)
	note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: longText, AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "citing the long note", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{note.ID},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	wantExcerpt := mcptools.Excerpt(longText)
	excerpts := citedNoteExcerpts(body)
	require.NotEmpty(t, excerpts, "the cited-notes section must render at least one citedNoteBody entry")
	assert.Contains(t, excerpts, wantExcerpt, "the cited-notes section must render EXACTLY mcptools.Excerpt's output")
	assert.NotContains(t, excerpts, longText, "the FULL untruncated note text must never appear as a citedNoteBody excerpt (it legitimately still appears elsewhere on the page: the research-note list and the save-verdict form's citation multi-select, neither of which this task truncates)")
}

// TestHandleIdeaDetail_VerdictWithNoCitations_RendersNoCitedNotesSection
// proves an empty CitedResearchNoteIDs renders NOTHING -- no "Cited
// notes" heading at all, never an empty one.
func TestHandleIdeaDetail_VerdictWithNoCitations_RendersNoCitedNotesSection(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Citations", creator.ID)
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "no citations here", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "no citations here")
	assert.NotContains(t, body, "Cited notes", "a verdict with zero citations must render no cited-notes section at all")
}

// TestHandleIdeaDetail_CitedNotesResolution_IssuesOneBatchedQuery is the
// concrete batching regression test for research.go's citedResearchNotes
// AND retiredCitedResearchNotes: the SAME Idea shape (a current verdict
// plus 3 history versions) issues exactly two MORE SQL statements when
// those 4 versions cite notes (overlapping across versions) than when
// they cite none at all -- one store.ResearchStore.GetByIDs call (FR9)
// plus one store.ResearchStore.RetiredNoteIDs call (FR10, #1944), each
// resolving the WHOLE page's citation union in a single query, never one
// query per citation per verdict version (FR16/NFR2). The delta was ONE
// statement before #1944 added the second (RetiredNoteIDs) batched call;
// it is TWO now that both derivations exist, each independently O(1).
//
// Both Ideas below carry the SAME 3-note pool (issue #1942 added a third
// always-issued batched query, relationsForNotes -- since it also
// short-circuits on an empty note list, an Idea with a citations-only
// difference but a DIFFERENT note-list shape would confound this
// assertion by adding relationsForNotes' own query to only one side).
// Giving both Ideas an identical note list isolates this test back to the
// two citation-driven batched calls' own diff, exactly as before #1942.
func TestHandleIdeaDetail_CitedNotesResolution_IssuesOneBatchedQuery(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	// noCitations: 4 verdict versions (1 current + 3 history), none cites
	// anything, but the Idea still carries its own 3-note pool (see this
	// test's doc comment above) with no relations among them.
	noCitationsIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Citations Query", creator.ID)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID, IdeaID: &noCitationsIdea.ID, Text: fmt.Sprintf("uncited pool note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
	}
	for i := 0; i < 4; i++ {
		_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
			IdeaID: noCitationsIdea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: fmt.Sprintf("no citations v%d", i+1), AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		})
		require.NoError(t, err)
	}

	// withCitations: the identical 4-version shape, but each version cites
	// an overlapping set of notes drawn from a pool of 3.
	withCitationsIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea With Citations Query", creator.ID)
	require.NoError(t, err)
	var noteIDs []uuid.UUID
	for i := 0; i < 3; i++ {
		n, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID, IdeaID: &withCitationsIdea.ID, Text: fmt.Sprintf("citation pool note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
		noteIDs = append(noteIDs, n.ID)
	}
	for i := 0; i < 4; i++ {
		// Every version cites the same overlapping pair, so GetByIDs must
		// deduplicate across versions too, not just within one.
		_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
			IdeaID: withCitationsIdea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: fmt.Sprintf("with citations v%d", i+1), AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
			CitedResearchNoteIDs: []uuid.UUID{noteIDs[0], noteIDs[1]},
		})
		require.NoError(t, err)
	}

	noCitationsCounter := &researchQueryCounter{}
	noCitationsStack := s.tracedResearchStack(t, ctx, noCitationsCounter)
	w := noCitationsStack.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+noCitationsIdea.ID.String(), noCitationsStack.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	withCitationsCounter := &researchQueryCounter{}
	withCitationsStack := s.tracedResearchStack(t, ctx, withCitationsCounter)
	w = withCitationsStack.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+withCitationsIdea.ID.String(), withCitationsStack.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, noCitationsCounter.n+2, withCitationsCounter.n,
		"citing overlapping notes across a current verdict plus 3 history versions must issue exactly TWO additional SQL statements (the batched GetByIDs read for FR9 plus the batched RetiredNoteIDs read for FR10, #1944) versus an identically-shaped Idea with no citations at all; no-citations issued %d, with-citations issued %d", noCitationsCounter.n, withCitationsCounter.n)
}

// TestHandleIdeaDetail_CitedNoteExcerpt_MatchesGetViabilityVerdictMCP is
// NFR2's cross-surface parity proof: the SAME fixture verdict's cited-note
// excerpt, read back through get_viability_verdict (a real in-process MCP
// call, mcpFixture above) and through the web Idea detail page, against
// the SAME underlying Postgres row, must be byte-for-byte identical. This
// is this file's second load-bearing red/green case (after the
// cross-Channel Idea 404 guard): see this task's Testing-phase commit for
// the deliberate-break verification -- temporarily hardcoding a different
// truncation bound on the web side alone turned this test red with a
// excerpt-mismatch failure, and reverting turned it green again.
func TestHandleIdeaDetail_CitedNoteExcerpt_MatchesGetViabilityVerdictMCP(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Parity", creator.ID)
	require.NoError(t, err)

	longText := strings.Repeat("parity ", 40) // well over 200 runes
	note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: longText, AuthorPersonID: creator.ID, SourceURL: strPtr("https://example.com/parity"),
	})
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "parity fixture verdict", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{note.ID},
	})
	require.NoError(t, err)

	// MCP side.
	mcpFix := newMCPFixture(t, s)
	cs := mcpFix.connect(t, creator.ID)
	mcpOut := mcpFix.getViabilityVerdict(t, cs, ch.ID, idea.ID)
	require.NotNil(t, mcpOut.Current)
	require.Len(t, mcpOut.Current.CitedResearchNotes, 1)
	mcpExcerpt := mcpOut.Current.CitedResearchNotes[0].TextExcerpt
	require.NotEmpty(t, mcpExcerpt)

	// Web side.
	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	excerpts := citedNoteExcerpts(body)
	require.NotEmpty(t, excerpts, "the cited-notes section must render at least one citedNoteBody entry")
	assert.Contains(t, excerpts, mcpExcerpt, "the web page's citedNoteBody excerpt must be the SAME text get_viability_verdict returned for this note")
	// Guard against a vacuous pass: the excerpt must actually be truncated
	// (this fixture's note text is well over the bound), so this assertion
	// couldn't be satisfied by accident via the untruncated full text.
	assert.True(t, strings.HasSuffix(mcpExcerpt, "..."), "the fixture's note text exceeds the truncation bound, so the shared excerpt must be truncated")
	assert.NotContains(t, excerpts, longText, "the FULL untruncated note text must never appear as a citedNoteBody excerpt (it legitimately still appears elsewhere on the page: the research-note list and the save-verdict form's citation multi-select)")
}

// ── FR10/FR16/NFR2 (#1944): superseded/excluded staleness warning on cited notes ──

// TestHandleIdeaDetail_CitedNoteSuperseded_RendersSupersededWarning proves
// FR10 on the web surface: a verdict cites note A; A is later superseded
// by note B (same thread); the Idea detail page must render A's citation
// with the "Superseded" warning, while still showing A's own text --
// never re-resolving the citation to B.
func TestHandleIdeaDetail_CitedNoteSuperseded_RendersSupersededWarning(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Stale Note Warning Supersede Case", creator.ID)
	require.NoError(t, err)

	note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "the original cited note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "citing the note that will be superseded", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{note.ID},
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &note.ThreadID, Text: "a newer note that supersedes it", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: note.ID, RelationType: store.RelationSupersedes}},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "the original cited note", "the citation must still name the original note")
	assert.Contains(t, body, "Superseded", "the cited-notes section must render a Superseded warning")
	assert.NotContains(t, body, "Excluded", "an only-superseded note must not also show Excluded")
}

// TestHandleIdeaDetail_CitedNoteExcluded_RendersExcludedWarning mirrors the
// superseded test for the excludes relation type.
func TestHandleIdeaDetail_CitedNoteExcluded_RendersExcludedWarning(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Stale Note Warning Exclude Case", creator.ID)
	require.NoError(t, err)

	note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "the original cited note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "citing the note that will be excluded", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{note.ID},
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &note.ThreadID, Text: "a note that excludes it", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: note.ID, RelationType: store.RelationExcludes}},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "the original cited note")
	assert.Contains(t, body, "Excluded", "the cited-notes section must render an Excluded warning")
	assert.NotContains(t, body, "Superseded", "an only-excluded note must not also show Superseded")
}

// TestHandleIdeaDetail_CitedNoteNonRetiringRelations_RenderNoWarning is the
// FR7 distinction test on the web surface: caveats, follows_up, and
// summarizes name a prior note without retiring it -- one subtest per
// type, per the issue's Testing section ("one test per type").
func TestHandleIdeaDetail_CitedNoteNonRetiringRelations_RenderNoWarning(t *testing.T) {
	for _, relationType := range []store.RelationType{store.RelationCaveats, store.RelationFollowsUp, store.RelationSummarizes} {
		t.Run(string(relationType), func(t *testing.T) {
			ctx := context.Background()
			s := newResearchTestStack(t)
			ch, creator := s.setupChannel(t, ctx)
			idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Non-Retiring "+string(relationType), creator.ID)
			require.NoError(t, err)

			note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
				ChannelID: ch.ID, IdeaID: &idea.ID, Text: "a note related via " + string(relationType), AuthorPersonID: creator.ID,
			})
			require.NoError(t, err)
			_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
				IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "citing a non-retired note", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
				CitedResearchNoteIDs: []uuid.UUID{note.ID},
			})
			require.NoError(t, err)
			_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
				ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &note.ThreadID, Text: "a related note", AuthorPersonID: creator.ID,
				Relations: []store.SaveNoteRelationInput{{RelatedNoteID: note.ID, RelationType: relationType}},
			})
			require.NoError(t, err)

			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			body := w.Body.String()

			assert.Contains(t, body, "a note related via "+string(relationType))
			assert.NotContains(t, body, "Superseded", "relation_type %q must never render a Superseded warning", relationType)
			assert.NotContains(t, body, "Excluded", "relation_type %q must never render an Excluded warning", relationType)
		})
	}
}

// TestHandleIdeaDetail_CitedLiveNoteNoRelation_RendersNoWarning proves a
// cited note with no relation at all renders no warning markup.
func TestHandleIdeaDetail_CitedLiveNoteNoRelation_RendersNoWarning(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Live Citation", creator.ID)
	require.NoError(t, err)

	note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "a live cited note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "citing a live note", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{note.ID},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "a live cited note")
	assert.NotContains(t, body, "Superseded")
	assert.NotContains(t, body, "Excluded")
}

// TestHandleIdeaDetail_CitedNoteBothSupersededAndExcluded_RendersBothWarning
// proves a note targeted by both a supersedes and an excludes relation
// renders the combined "Superseded and excluded" label.
func TestHandleIdeaDetail_CitedNoteBothSupersededAndExcluded_RendersBothWarning(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Both Retired Citation", creator.ID)
	require.NoError(t, err)

	note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "the doubly-retired note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "citing a note that will be both superseded and excluded", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{note.ID},
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &note.ThreadID, Text: "supersedes it", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: note.ID, RelationType: store.RelationSupersedes}},
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &note.ThreadID, Text: "also excludes it", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: note.ID, RelationType: store.RelationExcludes}},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "the doubly-retired note")
	assert.Contains(t, body, "Superseded and excluded", "a note retired by both relation types must render the combined label")
}

// TestHandleIdeaDetail_RetiredWarning_RenderedOnHistoryEntryToo proves the
// warning renders for a history-only citation too, not just current -- the
// note superseding a note cited ONLY by an earlier (history-only) verdict
// version must still surface the warning against that history entry.
func TestHandleIdeaDetail_RetiredWarning_RenderedOnHistoryEntryToo(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Past-Version Retired Citation", creator.ID)
	require.NoError(t, err)

	historyNote, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "note cited only by the superseded v1 verdict", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "v1 reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{historyNote.ID},
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "v2 reasoning, no citations", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &historyNote.ThreadID, Text: "supersedes the history-only note", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: historyNote.ID, RelationType: store.RelationSupersedes}},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	currentIdx := strings.Index(body, "Current")
	historyIdx := strings.Index(body, "History")
	require.Greater(t, currentIdx, 0)
	require.Greater(t, historyIdx, currentIdx)
	historySection := body[historyIdx:]

	assert.Contains(t, historySection, historyNote.Text)
	assert.Contains(t, historySection, "Superseded", "the History section must render the warning against v1's own retired citation")
}

// TestHandleIdeaDetail_RetiredWarningParity_MatchesGetViabilityVerdictMCP
// is NFR2's cross-surface parity proof for FR10: the SAME fixture
// verdict's cited-note retired-by set, read through get_viability_verdict
// (a real in-process MCP call) and through the web Idea detail page, must
// agree -- a note retired by BOTH supersedes and excludes must render as
// "supersedes"+"excludes" via MCP and as the combined "Superseded and
// excluded" label on the web page; neither surface may disagree about
// which relation types retired the note.
func TestHandleIdeaDetail_RetiredWarningParity_MatchesGetViabilityVerdictMCP(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Retired Parity", creator.ID)
	require.NoError(t, err)

	note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "the parity fixture's cited note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "parity fixture verdict", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{note.ID},
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &note.ThreadID, Text: "supersedes the parity note", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: note.ID, RelationType: store.RelationSupersedes}},
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &note.ThreadID, Text: "also excludes the parity note", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: note.ID, RelationType: store.RelationExcludes}},
	})
	require.NoError(t, err)

	// MCP side.
	mcpFix := newMCPFixture(t, s)
	cs := mcpFix.connect(t, creator.ID)
	mcpOut := mcpFix.getViabilityVerdict(t, cs, ch.ID, idea.ID)
	require.NotNil(t, mcpOut.Current)
	require.Len(t, mcpOut.Current.CitedResearchNotes, 1)
	assert.ElementsMatch(t, []string{"supersedes", "excludes"}, mcpOut.Current.CitedResearchNotes[0].RetiredBy,
		"get_viability_verdict must report both relation types")

	// Web side.
	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Superseded and excluded", "the web page must render the SAME combined retired-by set get_viability_verdict reported")
}

func TestHandleIdeaDetail_FiftyOneNotes_TruncatedNoPagingControl(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Many Notes", creator.ID)
	require.NoError(t, err)

	for i := 0; i < 51; i++ {
		_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID, IdeaID: &idea.ID, Text: fmt.Sprintf("idea note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
	}

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	// Each of the 50 rendered notes appears THREE times: once in the note
	// list itself, once as an <option> in the save-verdict form's citation
	// multi-select (#1901, FR4), and once as a relation-picker candidate in
	// the save-note form's thread group (#1945, FR15) -- all three
	// populated from the SAME notes slice, no extra store call and no
	// paging of its own.
	assert.Equal(t, 150, strings.Count(body, "idea note "), "exactly 50 notes must render, each once in the list, once in the citation multi-select, and once in the relation picker")
	assert.Contains(t, body, "most recent", "a truncation note must appear")
	// The Founder's save-note form (FR3, issue #1900) and save-verdict form
	// (FR4, issue #1901) legitimately render two <form>s on this page now;
	// NFR2's actual guarantee is that no SEPARATE paging/load-more control
	// exists alongside them.
	assert.Equal(t, 2, strings.Count(body, "<form"), "exactly the save-note and save-verdict forms may appear -- no paging control")
	assert.NotContains(t, strings.ToLower(body), "load more", "no load-more control may appear (NFR2)")
}

// ── No PAGING form/affordance anywhere on either page (superseded by
// issue #1900's save-note form, which is the one and only <form> either
// page may legitimately render for a store.CanWrite member) ─────────────

// TestChannelIndexAndIdeaDetail_OnlyTheSaveFormsAppear_NoPagingAffordance
// replaces this file's original #1899-era "no form anywhere" assertion
// (later renamed by #1900 to expect exactly one form): the Channel index
// still renders exactly one <form> (the save-note form), but Idea detail
// now renders exactly TWO -- the save-note form (#1900, FR3) AND the
// save-verdict form (#1901, FR4), both method="post", for a
// store.CanWrite member -- what must still never appear is any THIRD
// form or paging/load-more control.
func TestChannelIndexAndIdeaDetail_OnlyTheSaveFormsAppear_NoPagingAffordance(t *testing.T) {
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
	assert.Equal(t, 2, strings.Count(detailBody, "<form"), "exactly the save-note and save-verdict forms -- no third form -- may appear")
	assert.Equal(t, 2, strings.Count(strings.ToLower(detailBody), `method="post"`))
	assert.Contains(t, detailBody, `action="/channels/`+ch.ID.String()+`/research/notes"`)
	assert.Contains(t, detailBody, `action="/channels/`+ch.ID.String()+`/research/ideas/`+idea.ID.String()+`/verdicts"`)
}

// ── HandleSaveNote (FR3, FR6, FR7, NFR1, NFR3, issue #1900) ─────────────

// allNotes is a small helper that returns every research_note row on ch,
// used to assert row counts/contents after a POST rather than re-deriving
// them from the response body.
func (s *researchTestStack) allNotes(t *testing.T, ctx context.Context, channelID uuid.UUID) []store.ResearchNoteWithAuthor {
	t.Helper()
	notes, _, err := s.store.Research().ListFiltered(ctx, channelID, nil, nil, nil, false, nil, nil, 0)
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
		"thread_title":    {"Research"},
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
				"thread_title":    {"Research"},
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
		"thread_title":    {"Research"},
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
		"thread_title":    {"Research"},
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
		"thread_title":    {"Research"},
	})
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	w2 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"identical content"},
		"thread_title":    {"Research"},
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
		"thread_title":    {"Research"},
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

// ── HandleSaveNote thread selection + relation authoring (FR14, FR15,
// FR16, NFR2, issue #1945) ──────────────────────────────────────────────

// findNoteByText locates the single note in notes whose Text equals text,
// failing the test if it is missing or ambiguous -- a small helper so the
// tests below can assert on a specific saved row without re-deriving it
// from the response body.
func findNoteByText(t *testing.T, notes []store.ResearchNoteWithAuthor, text string) store.ResearchNoteWithAuthor {
	t.Helper()
	var found []store.ResearchNoteWithAuthor
	for _, n := range notes {
		if n.Text == text {
			found = append(found, n)
		}
	}
	require.Len(t, found, 1, "expected exactly one note with text %q, notes: %+v", text, notes)
	return found[0]
}

// TestHandleSaveNote_ExistingThreadID_AttachesToThread proves FR14's
// thread select: posting a valid thread_id attaches the new note to that
// EXISTING thread (no second thread row created) and still applies the
// thread's own resolved idea_id, exactly as store.ResearchStore.SaveNote's
// ThreadID resolution path documents.
func TestHandleSaveNote_ExistingThreadID_AttachesToThread(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	thread, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Title: "Existing thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"attaches to existing thread"},
		"idea_id":         {idea.ID.String()},
		"thread_id":       {thread.ID.String()},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	notes := s.allNotes(t, ctx, ch.ID)
	note := findNoteByText(t, notes, "attaches to existing thread")
	assert.Equal(t, thread.ID, note.ThreadID)
	require.NotNil(t, note.IdeaID)
	assert.Equal(t, idea.ID, *note.IdeaID)

	threads, err := s.store.Threads().ListByChannel(ctx, ch.ID, nil)
	require.NoError(t, err)
	assert.Len(t, threads, 1, "posting with an existing thread_id must not create a second thread row")
}

// TestHandleSaveNote_NewThreadTitle_FindsOrCreates_ReusesOnSecondPost
// proves FR14's new-thread-title path: the first post creates exactly one
// research_thread row, and a second post with the SAME title reuses it
// (find-or-create) rather than creating a duplicate.
func TestHandleSaveNote_NewThreadTitle_FindsOrCreates_ReusesOnSecondPost(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w1 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"first note in new thread"},
		"thread_title":    {"Growth ideas"},
	})
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	w2 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"second note reusing the thread"},
		"thread_title":    {"Growth ideas"},
	})
	require.Equal(t, http.StatusSeeOther, w2.Code, "body: %s", w2.Body.String())

	threads, err := s.store.Threads().ListByChannel(ctx, ch.ID, nil)
	require.NoError(t, err)
	require.Len(t, threads, 1, "the same thread_title posted twice must find-or-create ONE thread row, not two")

	notes := s.allNotes(t, ctx, ch.ID)
	note1 := findNoteByText(t, notes, "first note in new thread")
	note2 := findNoteByText(t, notes, "second note reusing the thread")
	assert.Equal(t, note1.ThreadID, note2.ThreadID, "both notes must resolve to the SAME thread row")
	assert.Equal(t, threads[0].ID, note1.ThreadID)
}

// TestHandleSaveNote_NeitherThreadIDNorThreadTitle_BadRequest_NoRow is
// FR14's required-selection case: submitting neither a thread_id nor a
// thread_title re-renders the originating page with 400 and
// store.ResearchStore.SaveNote's own "exactly one of" message (no second
// copy of that rule in this handler -- see HandleSaveNote's doc comment),
// and creates no row.
func TestHandleSaveNote_NeitherThreadIDNorThreadTitle_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"no thread selected"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "exactly one of thread_id or thread_title must be supplied")
	assert.Empty(t, s.allNotes(t, ctx, ch.ID))
}

// TestHandleSaveNote_UnresolvableThreadID_BadRequest_NoRow proves an
// unknown thread_id is rejected (400, store's own "does not exist"
// message) with no row created.
func TestHandleSaveNote_UnresolvableThreadID_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"unresolvable thread"},
		"thread_id":       {uuid.NewString()},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "does not exist")
	assert.Empty(t, s.allNotes(t, ctx, ch.ID))
}

// TestHandleSaveNote_ThreadFromAnotherChannel_BadRequest_NoRow mirrors
// TestHandleSaveNote_CrossChannelIdeaID_BadRequest_NoRow for thread_id: a
// thread that exists but belongs to a DIFFERENT Channel than the POST's
// {id} must never attach a note to it.
func TestHandleSaveNote_ThreadFromAnotherChannel_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	chA, creatorA := s.setupChannel(t, ctx)
	chB, creatorB := s.setupChannel(t, ctx)
	threadOnB, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: chB.ID, Title: "Thread on B", CreatedByPersonID: creatorB.ID,
	})
	require.NoError(t, err)

	w := s.doForm(t, "/channels/"+chA.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creatorA.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"cross-channel thread attempt"},
		"thread_id":       {threadOnB.ID.String()},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "does not belong to channel")
	assert.Empty(t, s.allNotes(t, ctx, chA.ID))
	assert.Empty(t, s.allNotes(t, ctx, chB.ID))
}

// TestHandleSaveNote_ValidRelation_WritesNoteAndRelationRow is FR15's
// happy path: picking a prior note in the SAME resolved thread with a
// relation_type writes both the note and the research_note_relation row,
// readable back through store.ResearchStore.ListRelationsForNotes with the
// correct direction from each end.
func TestHandleSaveNote_ValidRelation_WritesNoteAndRelationRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Relations", creator.ID)
	require.NoError(t, err)

	target, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadTitle: "Relation thread", Text: "target note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key":                     {uuid.NewString()},
		"text":                                {"declaring note via form"},
		"idea_id":                             {idea.ID.String()},
		"thread_id":                           {target.ThreadID.String()},
		"relation_type_" + target.ID.String(): {"supersedes"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	notes := s.allNotes(t, ctx, ch.ID)
	declaring := findNoteByText(t, notes, "declaring note via form")

	rels, err := s.store.Research().ListRelationsForNotes(ctx, []uuid.UUID{declaring.ID, target.ID})
	require.NoError(t, err)
	require.Len(t, rels[declaring.ID], 1)
	assert.Equal(t, target.ID, rels[declaring.ID][0].RelatedNoteID)
	assert.Equal(t, store.RelationSupersedes, rels[declaring.ID][0].RelationType)
	assert.Equal(t, store.RelationOutgoing, rels[declaring.ID][0].Direction)
	require.Len(t, rels[target.ID], 1)
	assert.Equal(t, declaring.ID, rels[target.ID][0].RelatedNoteID)
	assert.Equal(t, store.RelationIncoming, rels[target.ID][0].Direction)
}

// TestHandleSaveNote_RelationTargetInDifferentThread_BadRequest_NoRowNoRelation
// is FR15/NFR4's rejection case: a relation naming a note that belongs to
// a DIFFERENT thread than the one this submission resolves to is rejected
// by store.ResearchStore.SaveNote's own whole-call rollback -- 400, no note
// row, no relation row.
func TestHandleSaveNote_RelationTargetInDifferentThread_BadRequest_NoRowNoRelation(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Cross Thread", creator.ID)
	require.NoError(t, err)

	threadA, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Title: "Thread A", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	noteInB, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadTitle: "Thread B", Text: "note in thread B", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	before := s.allNotes(t, ctx, ch.ID)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key":                      {uuid.NewString()},
		"text":                                 {"declaring note, bad relation"},
		"idea_id":                              {idea.ID.String()},
		"thread_id":                            {threadA.ID.String()},
		"relation_type_" + noteInB.ID.String(): {"summarizes"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "is not in the resolved thread")

	after := s.allNotes(t, ctx, ch.ID)
	assert.Len(t, after, len(before), "a rejected relation must leave no note row behind")

	rels, err := s.store.Research().ListRelationsForNotes(ctx, []uuid.UUID{noteInB.ID})
	require.NoError(t, err)
	assert.Empty(t, rels[noteInB.ID], "a rejected relation must leave no relation row behind")
}

// TestHandleSaveNote_ValidationFailure_PreservesThreadAndRelationSelections
// proves the re-render after a rejected relation (this file's
// TestHandleSaveNote_RelationTargetInDifferentThread_BadRequest_NoRowNoRelation
// scenario) preserves EXACTLY what was submitted: the same thread_id
// selected in the thread <select>, the same relation_type_<noteID> pick
// re-selected, and the submitted text/source_url echoed back -- mirroring
// noteFormData's doc comment contract for IdeaID.
func TestHandleSaveNote_ValidationFailure_PreservesThreadAndRelationSelections(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Preserve Selections", creator.ID)
	require.NoError(t, err)

	threadA, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Title: "Thread A Preserve", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	noteInB, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadTitle: "Thread B Preserve", Text: "note in thread B preserve", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key":                      {uuid.NewString()},
		"text":                                 {"preserved text on failed submit"},
		"source_url":                           {"https://example.com/preserved"},
		"idea_id":                              {idea.ID.String()},
		"thread_id":                            {threadA.ID.String()},
		"relation_type_" + noteInB.ID.String(): {"summarizes"},
	})
	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "preserved text on failed submit", "the submitted note text must survive the re-render")
	assert.Contains(t, body, `value="https://example.com/preserved"`, "the submitted source_url must survive the re-render")
	assert.Contains(t, body, `<option value="`+threadA.ID.String()+`" selected`, "the submitted thread_id selection must be re-selected on the re-render")
	assert.Contains(t, body, `<select name="relation_type_`+noteInB.ID.String()+`"`, "the rejected relation's own picker must still be rendered")
	assert.Contains(t, body, `<option value="summarizes" selected`, "the submitted relation_type pick must be re-selected on the re-render")
}

// TestSaveNoteForm_ThreadPicker_ChannelIndexListsChannelWide_IdeaDetailListsIdeaOnly
// scopes narrowly to the save-note form's OWN thread <select
// name="thread_id"> (not merely the page's separate "Research threads"
// list, TestHandleChannelIndex_RendersEveryThreadOnChannel_IncludingNullIdea/
// TestHandleIdeaDetail_RendersOnlyThatIdeasThreads' own scope): the
// Channel index's picker must offer every thread on the Channel, while an
// Idea detail page's picker must offer only that Idea's own threads.
func TestSaveNoteForm_ThreadPicker_ChannelIndexListsChannelWide_IdeaDetailListsIdeaOnly(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea1, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One Picker", creator.ID)
	require.NoError(t, err)
	idea2, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Two Picker", creator.ID)
	require.NoError(t, err)

	thread1, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea1.ID, Title: "Idea one picker thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	thread2, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea2.ID, Title: "Idea two picker thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	threadSelectPattern := regexp.MustCompile(`(?s)<select name="thread_id".*?</select>`)

	indexW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, indexW.Code, "body: %s", indexW.Body.String())
	indexSelect := threadSelectPattern.FindString(indexW.Body.String())
	require.NotEmpty(t, indexSelect, "the Channel index must render the thread_id select")
	assert.Contains(t, indexSelect, thread1.Title, "the Channel index's thread picker must offer every thread on the Channel")
	assert.Contains(t, indexSelect, thread2.Title, "the Channel index's thread picker must offer every thread on the Channel")

	detailW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea1.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, detailW.Code, "body: %s", detailW.Body.String())
	detailSelect := threadSelectPattern.FindString(detailW.Body.String())
	require.NotEmpty(t, detailSelect, "the Idea detail page must render the thread_id select")
	assert.Contains(t, detailSelect, thread1.Title, "Idea One's thread picker must offer Idea One's own thread")
	assert.NotContains(t, detailSelect, thread2.Title, "Idea One's thread picker must NOT offer Idea Two's thread")
}

// TestHandleSaveNote_WebFormAndMCP_ProduceEquivalentRows is NFR2/FR16's
// parity proof for the save path itself (distinct from FR11's existing
// read-side relations parity, TestHandleIdeaDetail_NoteRelations_
// MatchListResearchNotesMCP): equivalent input -- same channel/idea, same
// resolved thread, a relation to the SAME prior note with the SAME
// relation_type -- submitted once through this handler's web form and
// once through save_research_note, must produce equivalent research_note
// and research_note_relation rows, since both call the IDENTICAL
// store.ResearchStore.SaveNote method (LB5).
func TestHandleSaveNote_WebFormAndMCP_ProduceEquivalentRows(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Parity", creator.ID)
	require.NoError(t, err)

	seed, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadTitle: "Parity save thread", Text: "seed note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	// Web side: the save-note form posts thread_id + a relation_type_
	// pick, exactly as HandleSaveNote's doc comment describes.
	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key":                   {uuid.NewString()},
		"text":                              {"web parity note"},
		"idea_id":                           {idea.ID.String()},
		"thread_id":                         {seed.ThreadID.String()},
		"relation_type_" + seed.ID.String(): {"caveats"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	webNote := findNoteByText(t, s.allNotes(t, ctx, ch.ID), "web parity note")

	// MCP side: save_research_note with the equivalent wire shape --
	// same idea, same resolved thread, same relation to the SAME seed
	// note with the SAME relation_type.
	mcpFix := newMCPFixture(t, s)
	cs := mcpFix.connect(t, creator.ID)
	mcpOut := mcpFix.saveResearchNote(t, cs, mcptools.SaveResearchNoteInput{
		ChannelID:         ch.ID.String(),
		Text:              "mcp parity note",
		IdeaID:            idea.ID.String(),
		ThreadID:          seed.ThreadID.String(),
		Relations:         []mcptools.SaveResearchNoteRelationInput{{RelatedNoteID: seed.ID.String(), RelationType: "caveats"}},
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.NotEmpty(t, mcpOut.ID)
	mcpNoteID, err := uuid.Parse(mcpOut.ID)
	require.NoError(t, err)

	// Both notes must resolve to the SAME thread and Idea (thread
	// resolution is identical regardless of caller).
	require.NotNil(t, mcpOut.ThreadID)
	assert.Equal(t, seed.ThreadID.String(), webNote.ThreadID.String())
	assert.Equal(t, webNote.ThreadID, seed.ThreadID)
	assert.Equal(t, seed.ThreadID.String(), *mcpOut.ThreadID)
	require.NotNil(t, webNote.IdeaID)
	require.NotNil(t, mcpOut.IdeaID)
	assert.Equal(t, idea.ID, *webNote.IdeaID)
	assert.Equal(t, idea.ID.String(), *mcpOut.IdeaID)

	// Both notes' relation to the seed note must be equivalent: same
	// related note, same type, same direction, whichever surface wrote
	// it.
	rels, err := s.store.Research().ListRelationsForNotes(ctx, []uuid.UUID{webNote.ID, mcpNoteID})
	require.NoError(t, err)
	require.Len(t, rels[webNote.ID], 1)
	require.Len(t, rels[mcpNoteID], 1)
	assert.Equal(t, rels[webNote.ID][0].RelatedNoteID, rels[mcpNoteID][0].RelatedNoteID)
	assert.Equal(t, rels[webNote.ID][0].RelationType, rels[mcpNoteID][0].RelationType)
	assert.Equal(t, store.RelationCaveats, rels[webNote.ID][0].RelationType)
	assert.Equal(t, rels[webNote.ID][0].Direction, rels[mcpNoteID][0].Direction)
	assert.Equal(t, store.RelationOutgoing, rels[webNote.ID][0].Direction)

	require.Len(t, mcpOut.Relations, 1, "save_research_note's own response must reflect the outgoing relation it just wrote")
	assert.Equal(t, seed.ID.String(), mcpOut.Relations[0].RelatedNoteID)
	assert.Equal(t, "caveats", mcpOut.Relations[0].RelationType)
	assert.Equal(t, "outgoing", mcpOut.Relations[0].Direction)
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

// ── HandleSaveVerdict (FR4, FR5, FR6, FR7, NFR1, NFR3, issue #1901) ─────

// doVerdictForm POSTs to ch/idea's verdicts route, mirroring doForm above
// for the save-note route.
func (s *researchTestStack) doVerdictForm(t *testing.T, channelID, ideaID uuid.UUID, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return s.doForm(t, "/channels/"+channelID.String()+"/research/ideas/"+ideaID.String()+"/verdicts", cookie, form)
}

// allVerdictHistory is a small helper mirroring allNotes above: every
// viability_verdict version for ideaID, oldest to newest.
func (s *researchTestStack) allVerdictHistory(t *testing.T, ctx context.Context, ideaID uuid.UUID) []store.Verdict {
	t.Helper()
	history, err := s.store.Verdicts().History(ctx, ideaID)
	require.NoError(t, err)
	return history
}

// TestHandleSaveVerdict_Founder_AppendsHumanVerdict_EarlierVersionUnchanged
// is FR4/FR5's happy path: a Founder posts viable + reasoning, gets a 303
// back to the Idea detail page, store.VerdictStore.Current returns the
// new version with Source == store.VerdictSourceHuman, and History shows
// it appended after the earlier agent-sourced version with that earlier
// version byte-identical (value, reasoning, version number) before and
// after -- the append-only assertion (M1 FR12).
func TestHandleSaveVerdict_Founder_AppendsHumanVerdict_EarlierVersionUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	v1, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "initial agent take", AuthorPersonID: creator.ID, Source: store.VerdictSourceAgent,
	})
	require.NoError(t, err)

	w := s.doVerdictForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"human review says viable"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), w.Header().Get("Location"))

	current, err := s.store.Verdicts().Current(ctx, idea.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VerdictViable, current.Verdict)
	assert.Equal(t, "human review says viable", current.Reasoning)
	assert.Equal(t, store.VerdictSourceHuman, current.Source, "a web-authored verdict must carry source = human (FR5)")
	assert.Equal(t, 2, current.Version)

	history := s.allVerdictHistory(t, ctx, idea.ID)
	require.Len(t, history, 2, "the new version must be APPENDED, never overwrite the prior one (M1 FR12)")
	assert.Equal(t, v1.ID, history[0].ID)
	assert.Equal(t, v1.Version, history[0].Version)
	assert.Equal(t, v1.Verdict, history[0].Verdict)
	assert.Equal(t, v1.Reasoning, history[0].Reasoning)
	assert.Equal(t, v1.Source, history[0].Source, "the earlier version's source must be unchanged")
	assert.Equal(t, current.ID, history[1].ID)
}

// TestHandleSaveVerdict_CoCreatorAndAnalyst_CanSave proves all three
// store.CanWrite roles -- not just the Founder -- can append a verdict
// through this handler.
func TestHandleSaveVerdict_CoCreatorAndAnalyst_CanSave(t *testing.T) {
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
		{"CoCreator", coCreator},
		{"Analyst", analyst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.doVerdictForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, tc.person.ID), url.Values{
				"idempotency_key": {uuid.NewString()},
				"verdict":         {string(store.VerdictViable)},
				"reasoning":       {tc.name + "'s reasoning"},
			})
			assert.Equal(t, http.StatusSeeOther, w.Code, "%s must be able to save, body: %s", tc.name, w.Body.String())
		})
	}

	assert.Len(t, s.allVerdictHistory(t, ctx, idea.ID), 2)
}

// TestHandleSaveVerdict_CitedNoteIDs_PopulatesExactlyThose proves
// cited_note_ids populates CitedResearchNoteIDs with exactly the
// submitted notes -- no more, no fewer.
func TestHandleSaveVerdict_CitedNoteIDs_PopulatesExactlyThose(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	note1, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research", ChannelID: ch.ID, IdeaID: &idea.ID, Text: "note one", AuthorPersonID: creator.ID})
	require.NoError(t, err)
	note2, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research", ChannelID: ch.ID, IdeaID: &idea.ID, Text: "note two", AuthorPersonID: creator.ID})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research", ChannelID: ch.ID, IdeaID: &idea.ID, Text: "note three (uncited)", AuthorPersonID: creator.ID})
	require.NoError(t, err)

	w := s.doVerdictForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"citing two of three notes"},
		"cited_note_ids":  {note1.ID.String(), note2.ID.String()},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	current, err := s.store.Verdicts().Current(ctx, idea.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{note1.ID, note2.ID}, current.CitedResearchNoteIDs, "exactly the two submitted notes must be cited")
}

// TestHandleSaveVerdict_CitedNoteFromDifferentIdea_BadRequest_NoRow is
// FR4's load-bearing citation-ownership guard: a forged note ID belonging
// to a DIFFERENT Idea must never end up in verdict_citation -- the whole
// submission 400s, writing no verdict row.
func TestHandleSaveVerdict_CitedNoteFromDifferentIdea_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	ideaA, err := s.store.Ideas().Create(ctx, ch.ID, "Idea A", creator.ID)
	require.NoError(t, err)
	ideaB, err := s.store.Ideas().Create(ctx, ch.ID, "Idea B", creator.ID)
	require.NoError(t, err)
	noteOnB, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research", ChannelID: ch.ID, IdeaID: &ideaB.ID, Text: "note on B", AuthorPersonID: creator.ID})
	require.NoError(t, err)

	w := s.doVerdictForm(t, ch.ID, ideaA.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"forged citation attempt"},
		"cited_note_ids":  {noteOnB.ID.String()},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid cited note selection")
	assert.Empty(t, s.allVerdictHistory(t, ctx, ideaA.ID), "no verdict row may be written")
}

// TestHandleSaveVerdict_SameIdempotencyKey_Twice_CreatesOneVersion is
// FR6/NFR1's load-bearing double-submit case: replaying the exact same
// POST (same server-generated idempotency_key) must not append a second
// version, using store.VerdictStore.Append's existing (idea, key) dedupe
// -- and the replayed response is still a 303.
func TestHandleSaveVerdict_SameIdempotencyKey_Twice_CreatesOneVersion(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	cookie := s.sessionCookie(t, ctx, creator.ID)
	key := uuid.NewString()
	form := url.Values{
		"idempotency_key": {key},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"replayed verdict"},
	}

	w1 := s.doVerdictForm(t, ch.ID, idea.ID, cookie, form)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())
	history1 := s.allVerdictHistory(t, ctx, idea.ID)
	require.Len(t, history1, 1)
	require.Equal(t, 1, history1[0].Version)

	w2 := s.doVerdictForm(t, ch.ID, idea.ID, cookie, form)
	assert.Equal(t, http.StatusSeeOther, w2.Code, "a replayed submit must still redirect, not error, body: %s", w2.Body.String())
	history2 := s.allVerdictHistory(t, ctx, idea.ID)
	assert.Len(t, history2, 1, "a replayed idempotency_key must not append a second version")
	assert.Equal(t, 1, history2[0].Version, "the version counter must not advance on a replay")
}

// TestHandleSaveVerdict_DifferentIdempotencyKeys_CreatesTwoVersions proves
// dedupe is keyed by idempotency_key, not by content: two different keys
// with identical verdict/reasoning must both append (an intentional
// re-record is a new version).
func TestHandleSaveVerdict_DifferentIdempotencyKeys_CreatesTwoVersions(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w1 := s.doVerdictForm(t, ch.ID, idea.ID, cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"identical content"},
	})
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	w2 := s.doVerdictForm(t, ch.ID, idea.ID, cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"identical content"},
	})
	require.Equal(t, http.StatusSeeOther, w2.Code, "body: %s", w2.Body.String())

	history := s.allVerdictHistory(t, ctx, idea.ID)
	require.Len(t, history, 2, "different idempotency keys with identical content must both append")
	assert.Equal(t, 1, history[0].Version)
	assert.Equal(t, 2, history[1].Version)
}

// TestHandleSaveVerdict_NonMember_Forbidden_HistoryUnchanged is FR7/NFR3's
// load-bearing authorization test: a forged POST from a signed-in
// non-member is 403 even though they cannot even GET the page, and
// History is left completely unchanged.
func TestHandleSaveVerdict_NonMember_Forbidden_HistoryUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	existing, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "pre-existing", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.doVerdictForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, outsider.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"forged verdict"},
	})
	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	assert.NotContains(t, w.Body.String(), "forged verdict")

	history := s.allVerdictHistory(t, ctx, idea.ID)
	require.Len(t, history, 1, "a forbidden POST must not append a version")
	assert.Equal(t, existing.ID, history[0].ID)
	assert.Equal(t, existing.Reasoning, history[0].Reasoning)
}

// TestHandleSaveVerdict_SignedOut_Rejected_NoRowCreated covers the
// signed-out half of FR7/NFR3, mirroring
// TestHandleSaveNote_SignedOut_Rejected_NoRowCreated.
func TestHandleSaveVerdict_SignedOut_Rejected_NoRowCreated(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	w := s.doVerdictForm(t, ch.ID, idea.ID, nil, url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"signed-out verdict"},
	})
	assert.NotEqual(t, http.StatusSeeOther, w.Code, "a signed-out POST must never succeed, body: %s", w.Body.String())
	assert.Empty(t, s.allVerdictHistory(t, ctx, idea.ID))

	req := httptest.NewRequest(http.MethodPost, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String()+"/verdicts", strings.NewReader(url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"direct signed-out verdict"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", ch.ID.String())
	req.SetPathValue("ideaID", idea.ID.String())
	rec := httptest.NewRecorder()
	s.handlers.HandleSaveVerdict(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, s.allVerdictHistory(t, ctx, idea.ID))
}

func TestHandleSaveVerdict_InvalidVerdictValue_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	w := s.doVerdictForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {"definitely-not-a-real-value"},
		"reasoning":       {"whatever"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid verdict selection")
	assert.Empty(t, s.allVerdictHistory(t, ctx, idea.ID))
}

func TestHandleSaveVerdict_EmptyOrWhitespaceReasoning_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	for _, tc := range []struct {
		name      string
		reasoning string
	}{
		{"Empty", ""},
		{"WhitespaceOnly", "   \n\t  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.doVerdictForm(t, ch.ID, idea.ID, cookie, url.Values{
				"idempotency_key": {uuid.NewString()},
				"verdict":         {string(store.VerdictViable)},
				"reasoning":       {tc.reasoning},
			})
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "reasoning is required")
		})
	}
	assert.Empty(t, s.allVerdictHistory(t, ctx, idea.ID))
}

// TestHandleSaveVerdict_CrossChannelIdea_NotFound_NoRow mirrors
// HandleIdeaDetail's cross-Channel 404 guard, applied to the verdicts
// POST route: an Idea that exists under a DIFFERENT Channel than the
// path's {id} must 404, never append a version.
func TestHandleSaveVerdict_CrossChannelIdea_NotFound_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	chA, creatorA := s.setupChannel(t, ctx)
	chB, creatorB := s.setupChannel(t, ctx)
	ideaOnB, err := s.store.Ideas().Create(ctx, chB.ID, "Idea On B", creatorB.ID)
	require.NoError(t, err)

	w := s.doVerdictForm(t, chA.ID, ideaOnB.ID, s.sessionCookie(t, ctx, creatorA.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"cross-channel attempt"},
	})
	assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
	assert.Empty(t, s.allVerdictHistory(t, ctx, ideaOnB.ID))
}

// TestHandleSaveVerdict_CrossSurfaceAgreement_HumanVsAgentSource proves
// FR5: a verdict written here (source = human) and an agent-sourced
// version already on the same Idea (standing in for an MCP-authored
// version -- both call the IDENTICAL store.VerdictStore.Append, LB5) are
// both visible via store.VerdictStore.Current/History AND on the
// rendered Idea detail page, with "Human"/"Agent" labels matching each.
func TestHandleSaveVerdict_CrossSurfaceAgreement_HumanVsAgentSource(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	agentV, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "agent-authored reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceAgent,
	})
	require.NoError(t, err)

	cookie := s.sessionCookie(t, ctx, creator.ID)
	w := s.doVerdictForm(t, ch.ID, idea.ID, cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {"human-authored reasoning"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	current, err := s.store.Verdicts().Current(ctx, idea.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VerdictSourceHuman, current.Source)
	history := s.allVerdictHistory(t, ctx, idea.ID)
	require.Len(t, history, 2)
	assert.Equal(t, store.VerdictSourceAgent, history[0].Source)
	assert.Equal(t, agentV.ID, history[0].ID)
	assert.Equal(t, store.VerdictSourceHuman, history[1].Source)

	detailW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), cookie)
	require.Equal(t, http.StatusOK, detailW.Code, "body: %s", detailW.Body.String())
	body := detailW.Body.String()
	assert.Contains(t, body, "agent-authored reasoning")
	assert.Contains(t, body, "human-authored reasoning")
	assert.Contains(t, body, "Agent", "the agent-sourced version must render its source")
	assert.Contains(t, body, "Human", "the human-sourced version must render its source")
}

// ── Save-verdict form rendering (FR4, FR6, FR7) ─────────────────────────

// TestSaveVerdictForm_MultiSelect_ListsExactlyThisIdeaNotes proves the
// citation multi-select is populated ONLY from this page's own Idea's
// notes -- a note belonging to a different Idea on the same Channel must
// never appear as an option (or anywhere else on the page).
func TestSaveVerdictForm_MultiSelect_ListsExactlyThisIdeaNotes(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	ideaA, err := s.store.Ideas().Create(ctx, ch.ID, "Idea A", creator.ID)
	require.NoError(t, err)
	ideaB, err := s.store.Ideas().Create(ctx, ch.ID, "Idea B", creator.ID)
	require.NoError(t, err)

	noteOnA, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research", ChannelID: ch.ID, IdeaID: &ideaA.ID, Text: "note on idea A", AuthorPersonID: creator.ID})
	require.NoError(t, err)
	noteOnB, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research", ChannelID: ch.ID, IdeaID: &ideaB.ID, Text: "note on idea B", AuthorPersonID: creator.ID})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaA.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, `value="`+noteOnA.ID.String()+`"`, "idea A's own note must appear as a citation option")
	assert.NotContains(t, body, `value="`+noteOnB.ID.String()+`"`, "idea B's note must never appear as a citation option on idea A's page")
	assert.NotContains(t, body, "note on idea B", "idea B's note text must never render on idea A's page at all")
}

// TestSaveVerdictForm_AbsentWithoutCanWrite documents the same reality
// TestSaveNoteForm_PresentForFounderCoCreatorAnalyst_OnBothPages's doc
// comment does for the note form: in this domain store.CanRead and
// store.CanWrite grant the exact same three-role set (store/authz.go), so
// there is no "can read but not write" member to render the negative case
// against -- a non-member cannot even reach a 200 to check the form's
// absence (403s on the GET itself, TestHandleIdeaDetail_NonMember_
// Forbidden). What this proves instead is the POSITIVE case for all three
// store.CanWrite roles, and that the real authorization boundary is
// authorizeWrite's fresh store.CanWrite check on the POST (see
// TestHandleSaveVerdict_NonMember_Forbidden_HistoryUnchanged), never the
// form's presence/absence.
func TestSaveVerdictForm_AbsentWithoutCanWrite(t *testing.T) {
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
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, tc.person.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), `action="/channels/`+ch.ID.String()+`/research/ideas/`+idea.ID.String()+`/verdicts"`, "%s must see the save-verdict form", tc.name)
		})
	}
}

// TestSaveVerdictForm_HiddenIdempotencyKey_NonEmpty_DiffersAcrossGETs
// proves FR6's server-generated, render-time idempotency key for the
// save-verdict form specifically: present and non-empty, and two separate
// GETs mint two DIFFERENT keys.
func TestSaveVerdictForm_HiddenIdempotencyKey_NonEmpty_DiffersAcrossGETs(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w1 := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), cookie)
	require.Equal(t, http.StatusOK, w1.Code)
	key1 := extractVerdictFormIdempotencyKey(t, w1.Body.String())
	assert.NotEmpty(t, key1)

	w2 := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), cookie)
	require.Equal(t, http.StatusOK, w2.Code)
	key2 := extractVerdictFormIdempotencyKey(t, w2.Body.String())
	assert.NotEmpty(t, key2)
	assert.NotEqual(t, key1, key2, "two separate GETs must mint two different verdict-form idempotency keys")
}

// TestSaveVerdictForm_ValidationFailure_RerendersWithSameIdempotencyKey
// proves the SAME idempotency_key survives a validation-failure
// re-render (FR6/FR7): a corrected resubmit must still be treated as the
// same logical write.
func TestSaveVerdictForm_ValidationFailure_RerendersWithSameIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	key := uuid.NewString()

	w := s.doVerdictForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {key},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {""},
	})
	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	rerenderedKey := extractVerdictFormIdempotencyKey(t, w.Body.String())
	assert.Equal(t, key, rerenderedKey, "a validation-failure re-render must carry the SAME idempotency_key as the failed submit")
}

func strPtr(s string) *string { return &s }

// ── HandleProposeVideoScript (#1915, FR1-FR5, NFR1-NFR3) ────────────────

// doProposeForm POSTs to ch/idea's video-scripts route, mirroring
// doVerdictForm above for the verdicts route.
func (s *researchTestStack) doProposeForm(t *testing.T, channelID, ideaID uuid.UUID, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return s.doForm(t, "/channels/"+channelID.String()+"/research/ideas/"+ideaID.String()+"/video-scripts", cookie, form)
}

// allVideoScripts is a small helper mirroring allNotes/allVerdictHistory
// above: every video_script row on channelID.
func (s *researchTestStack) allVideoScripts(t *testing.T, ctx context.Context, channelID uuid.UUID) []store.VideoScript {
	t.Helper()
	scripts, err := s.store.VideoScripts().ListByChannel(ctx, channelID)
	require.NoError(t, err)
	return scripts
}

// setupViableVerdict appends a VerdictViable version on ideaID, authored
// by authorID, and returns it -- HandleProposeVideoScript's own
// server-side gate (FR1/FR4) requires a current viable verdict before the
// propose form renders, or a submit succeeds, at all.
func (s *researchTestStack) setupViableVerdict(t *testing.T, ctx context.Context, ideaID, authorID uuid.UUID) store.Verdict {
	t.Helper()
	v, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaID, Verdict: store.VerdictViable, Reasoning: "viable for propose test", AuthorPersonID: authorID,
	})
	require.NoError(t, err)
	return v
}

// setupActiveStrategy creates a Strategy (active per the active param) on
// channelID with no linked verdicts (StrategyStore.Save accepts an empty
// VerdictIDs slice) -- backing the propose form's picker (FR2).
func (s *researchTestStack) setupStrategy(t *testing.T, ctx context.Context, channelID, authorID uuid.UUID, title string, active bool) store.StrategyDetail {
	t.Helper()
	detail, err := s.store.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: channelID, Title: title, Active: active, CreatedByPersonID: authorID, IdempotencyKey: uuid.NewString(),
	})
	require.NoError(t, err)
	return detail
}

// extractProposeFormIdempotencyKey mirrors extractVerdictFormIdempotencyKey
// for the propose-video-script form specifically -- it renders after both
// the save-note and save-verdict forms on IdeaDetail (see views.templ),
// so this slices body to the substring starting at the propose form's own
// heading before applying idempotencyKeyPattern.
func extractProposeFormIdempotencyKey(t *testing.T, body string) string {
	t.Helper()
	idx := strings.Index(body, "Propose a video script")
	require.Greater(t, idx, 0, "the propose-video-script form's heading must render, body: %s", body)
	return extractIdempotencyKey(t, body[idx:])
}

// TestProposeVideoScriptForm_ViableVerdict_CanWrite_Renders is FR1's
// positive gate: a viable current verdict plus store.CanWrite renders the
// propose form at its correct action path with all five field names.
func TestProposeVideoScriptForm_ViableVerdict_CanWrite_Renders(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, `action="/channels/`+ch.ID.String()+`/research/ideas/`+idea.ID.String()+`/video-scripts"`)
	for _, field := range []string{"strategy_id", "title", "script_text", "target_publish_date", "idempotency_key"} {
		assert.Contains(t, body, `name="`+field+`"`, "field %q must render", field)
	}
}

// TestProposeVideoScriptForm_NotViableOrNoVerdict_Absent is FR1's negative
// gate: a non-viable current verdict, and separately an Idea with no
// verdict at all, must never render the propose form.
func TestProposeVideoScriptForm_NotViableOrNoVerdict_Absent(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)
	s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)

	notViable, err := s.store.Ideas().Create(ctx, ch.ID, "Not Viable Idea", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: notViable.ID, Verdict: store.VerdictNotViable, Reasoning: "not viable", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	noVerdict, err := s.store.Ideas().Create(ctx, ch.ID, "No Verdict Idea", creator.ID)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		idea store.Idea
	}{
		{"NotViable", notViable},
		{"NoVerdict", noVerdict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+tc.idea.ID.String(), cookie)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.NotContains(t, w.Body.String(), "/video-scripts\"", "%s must not render the propose form", tc.name)
		})
	}
}

// TestProposeVideoScriptForm_PresentForAllCanWriteRoles documents the same
// domain reality TestSaveVerdictForm_AbsentWithoutCanWrite's doc comment
// does: store.CanRead and store.CanWrite grant the exact same three-role
// set (store/authz.go) here, so there is no "can read but not write"
// member who can reach a 200 to check the form's absence against -- a
// non-member 403s on the GET itself (TestHandleIdeaDetail_NonMember_
// Forbidden), which is exactly why the form's presence is documented as
// presentation-only rather than the real authorization boundary (see
// research.go's/views.templ's IdeaDetail doc comments). What this proves
// instead is the positive case for all three store.CanWrite roles; the
// real boundary is HandleProposeVideoScript's own authorizeWrite, proven
// by TestHandleProposeVideoScript_NonMember_Forbidden_NoRowCreated below
// (FR4).
func TestProposeVideoScriptForm_PresentForAllCanWriteRoles(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)

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
			assert.Contains(t, w.Body.String(), `action="/channels/`+ch.ID.String()+`/research/ideas/`+idea.ID.String()+`/video-scripts"`, "%s must see the propose form", tc.name)
		})
	}
}

// TestProposeVideoScriptForm_Picker_OnlyActiveStrategiesOnThisChannel is
// FR2: only a Channel's ACTIVE Strategies appear as options -- an inactive
// Strategy on the same Channel, and any Strategy on a different Channel,
// must never appear.
func TestProposeVideoScriptForm_Picker_OnlyActiveStrategiesOnThisChannel(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)

	active := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Active Strategy", true)
	inactive := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Inactive Strategy", false)

	otherCh, otherCreator := s.setupChannel(t, ctx)
	otherStrategy := s.setupStrategy(t, ctx, otherCh.ID, otherCreator.ID, "Other Channel Strategy", true)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, `value="`+active.ID.String()+`"`, "the active strategy must appear as an option")
	assert.NotContains(t, body, `value="`+inactive.ID.String()+`"`, "the inactive strategy must never appear as an option")
	assert.NotContains(t, body, "Inactive Strategy")
	assert.NotContains(t, body, `value="`+otherStrategy.ID.String()+`"`, "a strategy on a different channel must never appear")
	assert.NotContains(t, body, "Other Channel Strategy")
}

// TestProposeVideoScriptForm_NoActiveStrategy_ExplanatoryLine_NoSubmit is
// FR2's empty case: no active Strategy on the Channel renders an
// explanatory line and no submit control, but the page still 200s.
func TestProposeVideoScriptForm_NoActiveStrategy_ExplanatoryLine_NoSubmit(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "a Strategy is required to propose a video script")
	assert.NotContains(t, body, "/video-scripts\"", "no submit form may render without an active Strategy")
}

// TestHandleProposeVideoScript_HappyPath_CreatesProposedRow_RedirectsToSchedule
// is FR3's happy path: a valid POST 303s to /channels/{id}/schedule and
// writes a `video_script` row bound to the Idea's current verdict, the
// picked Strategy, and the poster.
func TestHandleProposeVideoScript_HappyPath_CreatesProposedRow_RedirectsToSchedule(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	verdict := s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	strategy := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)

	w := s.doProposeForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"My video script"},
		"script_text":     {"the script body"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "/channels/"+ch.ID.String()+"/schedule", w.Header().Get("Location"))

	scripts := s.allVideoScripts(t, ctx, ch.ID)
	require.Len(t, scripts, 1)
	script := scripts[0]
	assert.Equal(t, store.VideoScriptStatusProposed, script.Status)
	assert.Equal(t, verdict.ID, script.VerdictID)
	assert.Equal(t, idea.ID, script.IdeaID)
	assert.Equal(t, strategy.ID, script.StrategyID)
	assert.Equal(t, creator.ID, script.CreatedByPersonID)
}

// TestHandleProposeVideoScript_ForgedVerdictID_IgnoredServerDerivesCurrent
// is FR3's load-bearing "verdict_id is never a form field" case: even a
// forged verdict_id form value pointing at a DIFFERENT Idea's verdict must
// be ignored entirely -- the written row still binds to the Idea's own
// current verdict, proving the field is never read.
func TestHandleProposeVideoScript_ForgedVerdictID_IgnoredServerDerivesCurrent(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	verdict := s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	strategy := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)

	otherIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Other Idea", creator.ID)
	require.NoError(t, err)
	otherVerdict := s.setupViableVerdict(t, ctx, otherIdea.ID, creator.ID)

	w := s.doProposeForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"My video script"},
		"script_text":     {"the script body"},
		"verdict_id":      {otherVerdict.ID.String()},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	scripts := s.allVideoScripts(t, ctx, ch.ID)
	require.Len(t, scripts, 1)
	assert.Equal(t, verdict.ID, scripts[0].VerdictID, "the forged verdict_id form value must be ignored entirely")
	assert.Equal(t, idea.ID, scripts[0].IdeaID)
}

// TestHandleProposeVideoScript_NonMember_Forbidden_NoRowCreated is FR4's
// load-bearing authorization test: a forged POST from a signed-in
// non-member is 403 even though they cannot even GET the page, and writes
// no row.
func TestHandleProposeVideoScript_NonMember_Forbidden_NoRowCreated(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	strategy := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.doProposeForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, outsider.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"forged proposal"},
		"script_text":     {"forged body"},
	})
	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	assert.NotContains(t, w.Body.String(), "forged proposal")
	assert.Empty(t, s.allVideoScripts(t, ctx, ch.ID), "a forbidden POST must not create a row")
}

// TestHandleProposeVideoScript_CrossChannelIdea_NotFound_NoRow mirrors
// HandleSaveVerdict's/HandleIdeaDetail's cross-Channel 404 guard: an Idea
// that exists but under a DIFFERENT Channel than the path's {id} must
// 404, never 403, and never write a row.
func TestHandleProposeVideoScript_CrossChannelIdea_NotFound_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	chA, creatorA := s.setupChannel(t, ctx)
	chB, creatorB := s.setupChannel(t, ctx)
	ideaOnB, err := s.store.Ideas().Create(ctx, chB.ID, "Idea On B", creatorB.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, ideaOnB.ID, creatorB.ID)
	strategy := s.setupStrategy(t, ctx, chA.ID, creatorA.ID, "Strategy A", true)

	w := s.doProposeForm(t, chA.ID, ideaOnB.ID, s.sessionCookie(t, ctx, creatorA.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"cross-channel attempt"},
		"script_text":     {"body"},
	})
	assert.Equal(t, http.StatusNotFound, w.Code, "an Idea belonging to a different Channel must 404, not 403, body: %s", w.Body.String())
	assert.Empty(t, s.allVideoScripts(t, ctx, chA.ID))
	assert.Empty(t, s.allVideoScripts(t, ctx, chB.ID))
}

// TestHandleProposeVideoScript_SameIdempotencyKey_Twice_CreatesOneRow_DifferentKeyCreatesSecond
// is FR5/NFR1's load-bearing double-submit case: replaying the exact same
// POST (same server-generated idempotency_key) must not create a second
// row, using store.VideoScriptStore.Propose's existing (channel, author,
// key) dedupe with no separate web-side mechanism -- and a THIRD POST with
// a DIFFERENT key proves the dedupe is keyed, not content-hashed.
func TestHandleProposeVideoScript_SameIdempotencyKey_Twice_CreatesOneRow_DifferentKeyCreatesSecond(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	strategy := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)
	cookie := s.sessionCookie(t, ctx, creator.ID)
	key := uuid.NewString()
	form := url.Values{
		"idempotency_key": {key},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"replayed proposal"},
		"script_text":     {"body"},
	}

	w1 := s.doProposeForm(t, ch.ID, idea.ID, cookie, form)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())
	require.Len(t, s.allVideoScripts(t, ctx, ch.ID), 1)

	w2 := s.doProposeForm(t, ch.ID, idea.ID, cookie, form)
	assert.Equal(t, http.StatusSeeOther, w2.Code, "a replayed submit must still redirect, not error, body: %s", w2.Body.String())
	assert.Len(t, s.allVideoScripts(t, ctx, ch.ID), 1, "a replayed idempotency_key must not create a second row")

	w3 := s.doProposeForm(t, ch.ID, idea.ID, cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"replayed proposal"},
		"script_text":     {"body"},
	})
	require.Equal(t, http.StatusSeeOther, w3.Code, "body: %s", w3.Body.String())
	assert.Len(t, s.allVideoScripts(t, ctx, ch.ID), 2, "a different idempotency_key with identical content must persist as a second row")
}

// TestHandleProposeVideoScript_StrategyDeletedBeforeSubmit_BadRequest_NoRow
// covers NFR3's stale-Strategy case via DELETION: store.VideoScriptStore.
// Propose's own ErrStrategyNotFound check (video_script.go) is "does this
// strategy_id still exist on this channel_id" -- it does not re-check the
// active flag, so a Strategy merely deactivated between render and submit
// remains a valid strategy_id Propose would still accept (Propose's own
// existing, unmodified contract; #1915 does not touch
// store/video_script.go). Deleting the row is what actually exercises
// ErrStrategyNotFound.
func TestHandleProposeVideoScript_StrategyDeletedBeforeSubmit_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	strategy := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)

	_, err = s.db.Pool.Exec(ctx, `DELETE FROM strategy WHERE id = $1`, strategy.ID)
	require.NoError(t, err)

	w := s.doProposeForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"deleted strategy attempt"},
		"script_text":     {"body"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "strategy not found", "the store's own ErrStrategyNotFound message must render")
	assert.Contains(t, w.Body.String(), "deleted strategy attempt", "the submitted title must be preserved on re-render")
	assert.Empty(t, s.allVideoScripts(t, ctx, ch.ID))
}

// TestHandleProposeVideoScript_VerdictBecomesNonViableBeforeSubmit_BadRequest_NoRow
// covers NFR3's stale-verdict case: a NEW verdict version superseding the
// viable one between render and submit must reject with a 400, never
// write a row -- HandleProposeVideoScript always resolves the Idea's
// CURRENT verdict server-side (LB3) at submit time, never the version that
// was current when the page was rendered.
func TestHandleProposeVideoScript_VerdictBecomesNonViableBeforeSubmit_BadRequest_NoRow(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	strategy := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNotViable, Reasoning: "changed my mind", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.doProposeForm(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"stale verdict attempt"},
		"script_text":     {"body"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.NotEqual(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "not viable", "the re-render must surface a form error, never silently drop it")
	assert.Contains(t, w.Body.String(), "stale verdict attempt", "the submitted title must be preserved on re-render")
	assert.Empty(t, s.allVideoScripts(t, ctx, ch.ID))
}

// TestHandleProposeVideoScript_EmptyTitleOrScriptText_BadRequest_NoRow_SameIdempotencyKeyEchoed
// covers the plain validation case: empty title, and separately empty
// script_text, 400 with the submitted values preserved and the SAME
// idempotency_key echoed back in the hidden field (so a corrected resubmit
// stays one logical write, FR5).
func TestHandleProposeVideoScript_EmptyTitleOrScriptText_BadRequest_NoRow_SameIdempotencyKeyEchoed(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	s.setupViableVerdict(t, ctx, idea.ID, creator.ID)
	strategy := s.setupStrategy(t, ctx, ch.ID, creator.ID, "Strategy A", true)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	for _, tc := range []struct {
		name       string
		title      string
		scriptText string
		wantEcho   string
	}{
		{"EmptyTitle", "", "a script body to preserve", "a script body to preserve"},
		{"EmptyScriptText", "a title to preserve", "", "a title to preserve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := uuid.NewString()
			w := s.doProposeForm(t, ch.ID, idea.ID, cookie, url.Values{
				"idempotency_key": {key},
				"strategy_id":     {strategy.ID.String()},
				"title":           {tc.title},
				"script_text":     {tc.scriptText},
			})
			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			body := w.Body.String()
			assert.Contains(t, body, "is required")
			assert.Contains(t, body, tc.wantEcho, "the other submitted value must be preserved on re-render")
			rerenderedKey := extractProposeFormIdempotencyKey(t, body)
			assert.Equal(t, key, rerenderedKey, "a validation-failure re-render must carry the SAME idempotency_key as the failed submit")
		})
	}
	assert.Empty(t, s.allVideoScripts(t, ctx, ch.ID))
}

// ── Research thread lists (FR3, root plan #1934, issue #1937) ──────────────
//
// HandleChannelIndex/HandleIdeaDetail's existing 401/404/403 ordering
// tests above (TestHandleChannelIndex_NonMember_Forbidden,
// TestHandleIdeaDetail_NonMember_Forbidden, and their signed-out/unknown-
// Channel siblings) already cover the denial path for the whole page --
// the thread-list query added by this task runs strictly after that same
// store.CanRead check (renderChannelIndex/renderIdeaDetail), so a non-
// reader never reaches it. What's new here is proving the thread-list
// section itself renders correctly, and renders the correct SCOPE on each
// page.

func TestHandleChannelIndex_RendersEveryThreadOnChannel_IncludingNullIdea(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	attached, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Title: "Attached research thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	unattached, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, Title: "Pre-idea research thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, attached.Title, "the Channel index must render every thread, including ones attached to an Idea")
	assert.Contains(t, body, unattached.Title, "the Channel index must render threads that predate any Idea (idea_id IS NULL)")
	assert.Contains(t, body, "Research threads", "the thread-list section heading must render")
}

func TestHandleIdeaDetail_RendersOnlyThatIdeasThreads(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea1, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	idea2, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Two", creator.ID)
	require.NoError(t, err)

	thread1, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea1.ID, Title: "Idea one thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	thread2, err := s.store.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea2.ID, Title: "Idea two thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea1.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, thread1.Title, "Idea One's detail page must render its own thread")
	assert.NotContains(t, body, thread2.Title, "Idea One's detail page must NOT render Idea Two's thread")
}

// ── FR11/FR16/NFR2 (issue #1942): a note's own relations render inline on
// BOTH ordinary browse pages -- Idea detail's note list and the Channel
// index's unattached-notes list -- via ONE batched
// ResearchStore.ListRelationsForNotes call per page, and agree with MCP ──

// TestHandleIdeaDetail_RelationsRenderSupersedesAndSupersededByLines proves
// the core FR11 rendering on Idea detail: the declaring note's own list
// entry shows its outgoing "supersedes" line, and the target note's own
// entry shows the incoming "superseded by" line -- from the SAME
// underlying relation row.
func TestHandleIdeaDetail_RelationsRenderSupersedesAndSupersededByLines(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Relations", creator.ID)
	require.NoError(t, err)

	target, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadTitle: "Relations thread", Text: "target note text", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	declaring, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &target.ThreadID, Text: "declaring note text", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: target.ID, RelationType: store.RelationSupersedes}},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	wantOutgoing := "supersedes note " + declaringShortID(target.ID)
	wantIncoming := "superseded by note " + declaringShortID(declaring.ID)
	assert.Contains(t, body, wantOutgoing, "the declaring note must render its own outgoing 'supersedes' line")
	assert.Contains(t, body, wantIncoming, "the target note must render the incoming 'superseded by' line from the same relation row")
}

// declaringShortID mirrors views.templ's shortNoteID (8 hex chars plus
// ellipsis) so this file's assertions never hardcode a second copy of the
// truncation rule.
func declaringShortID(id uuid.UUID) string {
	s := id.String()
	return s[:8] + "…"
}

// TestHandleChannelIndex_UnattachedNoteRelationsRender proves FR11 renders
// on the Channel index's unattached-notes list too, not only Idea detail.
func TestHandleChannelIndex_UnattachedNoteRelationsRender(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	target, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, ThreadTitle: "Unattached thread", Text: "unattached target", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, ThreadID: &target.ThreadID, Text: "unattached declaring", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: target.ID, RelationType: store.RelationCaveats}},
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "caveats note "+declaringShortID(target.ID), "the Channel index's unattached-notes list must render the declaring note's outgoing relation")
	assert.Contains(t, body, "caveated by note", "the Channel index's unattached-notes list must render the target note's incoming relation")
}

// TestNoteWithNoRelations_RendersNoRelatedLine proves a note with zero
// relations renders NOTHING extra -- never an empty "Related" wrapper --
// in the note list itself. Scoped to the body BEFORE the "Save a research
// note" heading (issue #1945): that save-note form's own FR15 relation
// picker legitimately offers every store.RelationType as a
// relation_type_<noteID> <select> option regardless of whether ANY note
// on the page has a relation yet, so a whole-body check for these verbs
// would false-positive on the picker's own options, not on a spurious
// relatedNotesSection render -- exactly the section this test actually
// guards against.
func TestNoteWithNoRelations_RendersNoRelatedLine(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Relations", creator.ID)
	require.NoError(t, err)

	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadTitle: "No relations thread", Text: "a lone note with no relations", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "a lone note with no relations")
	noteListSection := body
	if idx := strings.Index(body, "Save a research note"); idx > 0 {
		noteListSection = body[:idx]
	}
	for _, verb := range []string{"supersedes", "superseded by", "excludes", "excluded by", "caveats", "caveated by", "follows up", "followed up by", "summarizes", "summarized by"} {
		assert.NotContains(t, noteListSection, verb, "a note with no relations must render no Related line at all")
	}
}

// TestHandleIdeaDetail_RelationsResolution_IssuesOneBatchedQuery proves
// FR16/NFR2's batching contract: resolving relations for an Idea's WHOLE
// note list issues exactly ONE additional SQL statement versus an
// identically-shaped Idea whose notes carry no relations at all -- never
// one query per note.
func TestHandleIdeaDetail_RelationsResolution_IssuesOneBatchedQuery(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	noRelationsIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Relations Query", creator.ID)
	require.NoError(t, err)
	seed, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &noRelationsIdea.ID, ThreadTitle: "Thread", Text: "seed", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	for i := 0; i < 4; i++ {
		_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
			ChannelID: ch.ID, IdeaID: &noRelationsIdea.ID, ThreadID: &seed.ThreadID, Text: fmt.Sprintf("no relation note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
	}

	withRelationsIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea With Relations Query", creator.ID)
	require.NoError(t, err)
	wrSeed, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &withRelationsIdea.ID, ThreadTitle: "Thread", Text: "seed", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	prior := wrSeed
	for i := 0; i < 4; i++ {
		n, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
			ChannelID: ch.ID, IdeaID: &withRelationsIdea.ID, ThreadID: &wrSeed.ThreadID, Text: fmt.Sprintf("with relation note %d", i), AuthorPersonID: creator.ID,
			Relations: []store.SaveNoteRelationInput{{RelatedNoteID: prior.ID, RelationType: store.RelationFollowsUp}},
		})
		require.NoError(t, err)
		prior = n
	}

	noRelationsCounter := &researchQueryCounter{}
	noRelationsStack := s.tracedResearchStack(t, ctx, noRelationsCounter)
	w := noRelationsStack.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+noRelationsIdea.ID.String(), noRelationsStack.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	withRelationsCounter := &researchQueryCounter{}
	withRelationsStack := s.tracedResearchStack(t, ctx, withRelationsCounter)
	w = withRelationsStack.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+withRelationsIdea.ID.String(), withRelationsStack.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, noRelationsCounter.n, withRelationsCounter.n,
		"resolving relations for a whole note list must issue the SAME query count whether or not any note actually carries a relation; no-relations issued %d, with-relations issued %d", noRelationsCounter.n, withRelationsCounter.n)
}

// TestHandleIdeaDetail_NoteRelations_MatchListResearchNotesMCP is NFR2's
// parity proof: the SAME fixture note's relation set, read back through
// list_research_notes (a real in-process MCP call) and through the web
// Idea detail page, against the SAME underlying Postgres rows, must be
// the same relation (related note, type, direction) on both surfaces.
func TestHandleIdeaDetail_NoteRelations_MatchListResearchNotesMCP(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Relations Parity", creator.ID)
	require.NoError(t, err)

	target, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadTitle: "Parity thread", Text: "parity target", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	declaring, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, ThreadID: &target.ThreadID, Text: "parity declaring", AuthorPersonID: creator.ID,
		Relations: []store.SaveNoteRelationInput{{RelatedNoteID: target.ID, RelationType: store.RelationSummarizes}},
	})
	require.NoError(t, err)

	// MCP side.
	mcpFix := newMCPFixture(t, s)
	cs := mcpFix.connect(t, creator.ID)
	mcpOut := mcpFix.listResearchNotes(t, cs, ch.ID)
	var mcpDeclaring mcptools.ResearchNoteOutput
	for _, n := range mcpOut.Notes {
		if n.ID == declaring.ID.String() {
			mcpDeclaring = n
		}
	}
	require.NotEmpty(t, mcpDeclaring.ID, "sanity: the declaring note must be present in list_research_notes")
	require.Len(t, mcpDeclaring.Relations, 1)
	assert.Equal(t, target.ID.String(), mcpDeclaring.Relations[0].RelatedNoteID)
	assert.Equal(t, "summarizes", mcpDeclaring.Relations[0].RelationType)
	assert.Equal(t, "outgoing", mcpDeclaring.Relations[0].Direction)

	// Web side.
	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "summarizes note "+declaringShortID(target.ID), "the web page's relation line must agree with MCP's relation (same related note, type, direction)")
}

// ── FR2 Stage 2c: web/research fully thread-derived (issue #1946) ──────────
//
// #1939 (store) and #1940 (mcp/tools) already made every reader resolve a
// note's Idea via research_note.thread_id -> research_thread.idea_id
// (researchNoteColumns/researchNoteWithAuthorColumns, store/research.go);
// web/research's own call sites (HandleSaveVerdict's cited-note same-Idea
// check, renderChannelIndex's unattached-notes partition) were already
// reading that same thread-derived value, so #1946 only restated their doc
// comments in thread terms. These four tests are #1946's own Testing-phase
// evidence that the *behaviour* those comments describe actually holds,
// not just the prose above it.

// TestHandleSaveVerdict_CitedNoteFromDifferentIdea_ThreadDerivedNotColumn_BadRequest_NoRow
// (issue #1946) proved the same-Idea citation guard reads the cited
// note's THREAD's Idea, not research_note.idea_id directly, by tampering
// research_note.idea_id via raw SQL to a different Idea than the note's
// real (thread-derived) one and asserting the tampered value never leaks
// through. Removed by migration 018/#1947 (FR2 Stage 3): the column this
// test tampered no longer exists, so the scenario it guarded against --
// a stale/forged idea_id disagreeing with the note's actual thread -- is
// now impossible by construction, the same category as
// TestHandleSaveNote_ReplayWithDifferentIdeaAndThread_OriginalNoteIdeaUnchanged
// above. The guard itself (research.go's
// `if note.IdeaID == nil || *note.IdeaID != ideaID`) is still exercised,
// on real (non-tampered) data, by
// TestHandleSaveVerdict_CitedNoteFromDifferentIdea_BadRequest_NoRow.

// TestHandleIdeaDetail_NullIdeaThreadNote_NotRenderedOnAnyIdeaDetailPage
// proves a note on a NULL-Idea thread (predates any Idea, M1 FR9) lands
// only in the Channel index's unattached-notes section (already covered by
// TestHandleChannelIndex_RendersNoteCountVerdictPresence_AndUnattachedNotesSection)
// and never appears on ANY Idea's detail page -- the partition
// renderChannelIndex's `if n.IdeaID == nil` performs is exhaustive, not
// merely additive.
func TestHandleIdeaDetail_NullIdeaThreadNote_NotRenderedOnAnyIdeaDetailPage(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	ideaA, err := s.store.Ideas().Create(ctx, ch.ID, "Idea A", creator.ID)
	require.NoError(t, err)
	ideaB, err := s.store.Ideas().Create(ctx, ch.ID, "Idea B", creator.ID)
	require.NoError(t, err)

	unattached, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, IdeaID: nil, ThreadTitle: "Pre-idea research", Text: "a note on a NULL-idea thread", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.Nil(t, unattached.IdeaID, "sanity: the note's resolved Idea must be nil before checking either detail page")

	cookie := s.sessionCookie(t, ctx, creator.ID)

	wIndex := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", cookie)
	require.Equal(t, http.StatusOK, wIndex.Code, "body: %s", wIndex.Body.String())
	assert.Contains(t, wIndex.Body.String(), unattached.Text, "the Channel index's unattached-notes section must render it")

	wA := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaA.ID.String(), cookie)
	require.Equal(t, http.StatusOK, wA.Code, "body: %s", wA.Body.String())
	assert.NotContains(t, wA.Body.String(), unattached.Text, "a NULL-Idea-thread note must not render on Idea A's detail page")

	wB := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaB.ID.String(), cookie)
	require.Equal(t, http.StatusOK, wB.Code, "body: %s", wB.Body.String())
	assert.NotContains(t, wB.Body.String(), unattached.Text, "a NULL-Idea-thread note must not render on Idea B's detail page either")
}

// TestHandleSaveNote_ReplayWithDifferentIdeaAndThread_OriginalNoteIdeaUnchanged
// is this task's "impossible by construction" case: a thread's idea_id has
// no update path (store.ThreadStore exposes only FindOrCreate/GetByID/
// ListByChannel, no setter), and web/research exposes no edit-note
// endpoint at all -- the ONLY web-facing call that could even be suspected
// of moving an existing note to a different Idea is a POST replay under the
// note's own idempotency_key. store.researchStore.SaveNote's idempotency
// lookup (store/research.go) returns the existing row BEFORE thread
// resolution ever runs, so a replay with a different idea_id/thread_title
// must be a complete no-op: same row, same Idea.
func TestHandleSaveNote_ReplayWithDifferentIdeaAndThread_OriginalNoteIdeaUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)
	ideaA, err := s.store.Ideas().Create(ctx, ch.ID, "Idea A", creator.ID)
	require.NoError(t, err)
	ideaB, err := s.store.Ideas().Create(ctx, ch.ID, "Idea B", creator.ID)
	require.NoError(t, err)

	key := uuid.NewString()
	w1 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, url.Values{
		"idempotency_key": {key},
		"text":            {"attached to A first"},
		"thread_title":    {"Thread on A"},
		"idea_id":         {ideaA.ID.String()},
	})
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	notes := s.allNotes(t, ctx, ch.ID)
	require.Len(t, notes, 1)
	require.NotNil(t, notes[0].IdeaID)
	require.Equal(t, ideaA.ID, *notes[0].IdeaID)
	originalID := notes[0].ID

	// Replay under the SAME key, but pointed at a completely different
	// Idea/thread -- a forged attempt to "move" the note.
	w2 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", cookie, url.Values{
		"idempotency_key": {key},
		"text":            {"attempted move to B"},
		"thread_title":    {"A different thread on B"},
		"idea_id":         {ideaB.ID.String()},
	})
	assert.Equal(t, http.StatusSeeOther, w2.Code, "a replayed submit must still redirect, not error, body: %s", w2.Body.String())

	notesAfter := s.allNotes(t, ctx, ch.ID)
	require.Len(t, notesAfter, 1, "a replayed idempotency_key must not create a second row")
	assert.Equal(t, originalID, notesAfter[0].ID)
	require.NotNil(t, notesAfter[0].IdeaID)
	assert.Equal(t, ideaA.ID, *notesAfter[0].IdeaID, "a note's Idea must never change after creation -- the replay's different idea_id/thread_title must be ignored entirely")
}

// TestHandleIdeaDetail_BackfilledPreMigrationNote_RendersOnSameIdeaPage
// proves a note that existed BEFORE migration 016 (#1936) introduced
// research_thread/thread_id -- and was therefore only ever backfilled onto
// a synthetic thread, never saved through SaveNote's thread-resolution path
// -- still renders on the exact same Idea detail page it did before #1936,
// now read via the thread join (researchNoteColumns' LEFT JOIN, not an
// INNER JOIN, specifically to not silently drop a row like this one -- see
// that column list's doc comment in store/research.go).
func TestHandleIdeaDetail_BackfilledPreMigrationNote_RendersOnSameIdeaPage(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(15), "apply migrations 1-15, before research_thread/thread_id existed (issue #1936)")

	st := store.New(db.Pool)
	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-backfill-"+uuid.NewString(), "backfill@example.com", "Backfill Creator")
	require.NoError(t, err)
	ch, err := st.Channels().Create(ctx, "yt-backfill-"+uuid.NewString(), "Backfill Channel", creator.ID)
	require.NoError(t, err)
	idea, err := st.Ideas().Create(ctx, ch.ID, "Pre-Migration Idea", creator.ID)
	require.NoError(t, err)

	// Insert the note using only the columns that existed before migration
	// 016 -- no thread_id column to set even if we wanted to.
	const noteText = "a note that predates research_thread entirely"
	var noteID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO research_note (channel_id, idea_id, text, author_person_id) VALUES ($1, $2, $3, $4) RETURNING id
	`, ch.ID, idea.ID, noteText, creator.ID).Scan(&noteID))

	require.NoError(t, runner.Up(), "apply migration 016's backfill (and every migration after it)")

	sessions := auth.NewSessionManager(db.Pool, testCookieName, "session-secret", testEncKey())
	a := auth.NewForTests(st.Persons(), sessions)
	res := research.New(st)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{id}/research/ideas/{ideaID}", a.RequireSignedIn(res.HandleIdeaDetail))
	stack := &researchTestStack{store: st, sessions: sessions, handlers: res, router: mux, db: db}

	w := stack.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), stack.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), noteText, "a note backfilled by migration 016 must render on the same Idea detail page it did before #1936, via its resolved thread's idea_id")
}
