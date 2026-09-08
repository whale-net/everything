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
// /channels/{id}/research/notes, POST /channels/{id}/research/ideas
// (HandleCreateIdea, #2032, FR33-FR35), POST
// /channels/{id}/research/ideas/{ideaID}/verdicts, and POST
// /channels/{id}/research/ideas/{ideaID}/video-scripts -- so PathValue
// resolution and auth.RequireSignedIn wrapping behave exactly as they do
// in production.
//
// HandleCreateIdea (#2032, FR33-FR35): Founder/Co-Creator/Analyst parity
// creating an Idea through the add-idea inline row's submit target, the
// load-bearing FR34 convergence case (a title matching an existing Idea
// case/whitespace-insensitively returns that SAME Idea rather than
// forking identity, via the identical store.IdeaStore.FindOrCreate the
// create_idea MCP tool uses -- NFR1/LB5) and its cross-Channel guard (a
// same-titled Idea on a DIFFERENT Channel never converges), the FR7-style
// forged-POST 403 from a signed-in non-member (and a signed-out reject)
// with no row created, an unknown Channel 404 and malformed Channel UUID
// 400 in the same order as HandleSaveNote, empty/whitespace-title
// validation (400, no row, row re-renders expanded with an error), the
// FR33 affordance's presence/absence gated on canWrite, FR35's
// new-Idea-visible-after-redirect behavior, and NFR2's
// same-idempotency-key double-submit producing exactly one Idea.
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
	mux.HandleFunc("POST /channels/{id}/research/ideas", a.RequireSignedIn(res.HandleCreateIdea))
	mux.HandleFunc("POST /channels/{id}/research/ideas/{ideaID}/verdicts", a.RequireSignedIn(res.HandleSaveVerdict))
	mux.HandleFunc("GET /channels/{id}/research/ideas/{ideaID}/verdicts", a.RequireSignedIn(res.HandleVerdictDetail))
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
	mux.HandleFunc("GET /channels/{id}/research/ideas/{ideaID}/verdicts", a.RequireSignedIn(res.HandleVerdictDetail))

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

// unattachedNotesDetailsStart locates the START of the unattached-notes
// section's OWN <details> element specifically -- scoped by walking
// backward from the "Unattached notes (" toggle-label text to the nearest
// preceding "<details" tag -- rather than the page's FIRST "<details"
// occurrence. Since #2032 (FR33) the add-idea row (views.templ's
// addIdeaRow) renders its OWN "<details" disclosure earlier in the Idea
// table, ABOVE this section, so an unscoped strings.Index(body,
// "<details") now finds the wrong element; every pre-existing FR36/FR37
// test below that used to assume "the first (and only) <details> on the
// page IS the unattached-notes section" needs this instead.
func unattachedNotesDetailsStart(t *testing.T, body string) int {
	t.Helper()
	headingIdx := strings.Index(body, "Unattached notes (")
	require.Greater(t, headingIdx, 0, "the unattached notes toggle label must render, body: %s", body)
	detailsStart := strings.LastIndex(body[:headingIdx], "<details")
	require.GreaterOrEqual(t, detailsStart, 0, "the unattached notes section's own <details> element must render, body: %s", body)
	return detailsStart
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
	// The Founder's save-note form (FR3, issue #1900) and the add-idea
	// inline row's form (#2032, FR33) legitimately render TWO <form>
	// elements on this page now; NFR2's actual guarantee is that no
	// SEPARATE paging/load-more control exists alongside them.
	assert.Equal(t, 2, strings.Count(body, "<form"), "exactly the save-note form and the add-idea form may appear -- no paging control")
	assert.NotContains(t, strings.ToLower(body), "load more", "no load-more control may appear (NFR2)")
	// The collapse-by-default behavior (FR36, #2033) is unaffected by NFR2's
	// truncation: the section still renders behind a closed <details> whose
	// label carries the truncated 50-row count, not 51.
	sectionStart := unattachedNotesDetailsStart(t, body)
	sectionTag := body[sectionStart:strings.Index(body[sectionStart:], ">")+sectionStart]
	assert.NotContains(t, sectionTag, "open", "the disclosure must still be collapsed by default with 51 notes present")
	assert.Contains(t, body, "Unattached notes (50)", "the toggle label must carry the truncated page's count (NFR2), not the true underlying count")
}

// ── HandleChannelIndex unattached-notes disclosure (FR36, FR37, #2033):
// collapsed-by-default toggle with a hidden-note count, and proof the
// collapse never changes what HandleChannelIndex queries or returns ──────

// TestHandleChannelIndex_UnattachedNotesSection_CollapsedByDefault_ToggleLabelHasCount
// proves FR36: the unattached-notes section renders behind a <details>
// element with no "open" attribute (collapsed by default) and its
// <summary> toggle label carries the exact count of unattached notes.
func TestHandleChannelIndex_UnattachedNotesSection_CollapsedByDefault_ToggleLabelHasCount(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	for i := 0; i < 3; i++ {
		_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID, IdeaID: nil, Text: fmt.Sprintf("hidden note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
	}

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	// Since #2032 (FR33) the add-idea row also renders its own <details>
	// disclosure on this page, so the page-wide count is now 2, not 1; the
	// load-bearing assertion here is that the UNATTACHED-NOTES section's OWN
	// <details> (located by unattachedNotesDetailsStart, not by page-wide
	// position) still renders collapsed with the right count -- unaffected
	// by the unrelated add-idea disclosure existing alongside it.
	assert.Equal(t, 2, strings.Count(body, "<details"), "the unattached-notes disclosure and the add-idea row's disclosure (#2032) must both render")
	detailsStart := unattachedNotesDetailsStart(t, body)
	openTagEnd := strings.Index(body[detailsStart:], ">") + detailsStart
	require.Greater(t, openTagEnd, detailsStart)
	detailsOpenTag := body[detailsStart:openTagEnd]
	assert.NotContains(t, detailsOpenTag, "open", "the <details> element must render collapsed by default (FR36) -- no \"open\" attribute")
	assert.Contains(t, body, "Unattached notes (3)", "the toggle label must show the exact hidden-note count (FR36)")
}

// TestHandleChannelIndex_UnattachedNotesSection_CollapsedButDataStillReturned
// proves FR37 (load-bearing): the collapse is purely presentational --
// HandleChannelIndex still queries and renders the unattached notes' full
// text into the response body even though the <details> element itself is
// collapsed. If a future change gated the query on the toggle state (an
// htmx lazy-load round trip, a query param, etc.), this test would fail
// because the note text would no longer appear in the initial GET response.
func TestHandleChannelIndex_UnattachedNotesSection_CollapsedButDataStillReturned(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	unattached, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: nil, Text: "text that must survive collapse", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	detailsStart := unattachedNotesDetailsStart(t, body)
	openTagEnd := strings.Index(body[detailsStart:], ">") + detailsStart
	assert.NotContains(t, body[detailsStart:openTagEnd], "open", "the section must render collapsed by default")
	detailsEnd := strings.Index(body[detailsStart:], "</details>")
	require.Greater(t, detailsEnd, 0, "the disclosure must close")
	detailsSection := body[detailsStart : detailsStart+detailsEnd]
	// Scope this assertion to inside the <details>...</details> element
	// itself (not merely somewhere in the page) -- the note's text also
	// legitimately appears a second time in the save-note form's
	// relation-picker candidate list (#1945, FR15), so an unscoped
	// assert.Contains(body, ...) would pass even if the collapse-content
	// rendering itself were broken/gated.
	assert.Contains(t, detailsSection, unattached.Text, "the unattached note's text must still be present INSIDE the disclosure while collapsed (FR37) -- the query is unaffected by the toggle's presentation state")
}

// TestHandleChannelIndex_UnattachedNotesSection_ZeroNotes_RendersSensibly
// proves a Channel with zero unattached notes still renders a coherent
// disclosure -- an explicit "(0)" count and an empty-state message, not a
// broken or blank section.
func TestHandleChannelIndex_UnattachedNotesSection_ZeroNotes_RendersSensibly(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Only Idea", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "attached note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, "Unattached notes (0)", "the toggle must show an explicit 0 count, not a broken/blank section")
	assert.Contains(t, body, "No unattached notes.", "an explicit empty-state message must render inside the disclosure")
	// unattachedNotesDetailsStart itself already requires the unattached
	// notes section's own <details> to be present; this is here as an
	// explicit page-wide sanity check that it (plus the add-idea row's own
	// disclosure, #2032) both render.
	unattachedNotesDetailsStart(t, body)
	assert.Equal(t, 2, strings.Count(body, "<details"), "the unattached-notes disclosure must still render (even with zero notes) alongside the add-idea row's own disclosure (#2032)")
}

// TestHandleSaveNote_TargetingUnattachedNote_SucceedsWithSectionCollapsedByDefault
// proves FR37: posting a new unattached note (no idea_id) still succeeds
// through HandleSaveNote regardless of the toggle's presentation state, and
// the following render of the Channel index shows the disclosure still
// collapsed by default with the updated hidden-note count -- the
// save-note path is untouched by the collapse.
func TestHandleSaveNote_TargetingUnattachedNote_SucceedsWithSectionCollapsedByDefault(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/notes", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"text":            {"a note saved while the section is collapsed"},
		"thread_title":    {"Research"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "the save-note POST must still succeed regardless of the toggle's state (FR37), body: %s", w.Body.String())
	assert.Equal(t, "/channels/"+ch.ID.String()+"/research", w.Header().Get("Location"))

	notes := s.allNotes(t, ctx, ch.ID)
	require.Len(t, notes, 1)
	assert.Nil(t, notes[0].IdeaID, "the saved note must be unattached")

	// Follow the redirect: the next render must still show the section
	// collapsed by default (per-navigation default, no persisted expanded
	// state -- root plan out-of-scope) with the count now including the
	// just-saved note.
	w2 := s.do(t, http.MethodGet, w.Header().Get("Location"), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w2.Code, "body: %s", w2.Body.String())
	body := w2.Body.String()
	detailsStart := unattachedNotesDetailsStart(t, body)
	openTagEnd := strings.Index(body[detailsStart:], ">") + detailsStart
	assert.NotContains(t, body[detailsStart:openTagEnd], "open", "the section must render collapsed by default on the next navigation too")
	assert.Contains(t, body, "Unattached notes (1)", "the toggle count must reflect the just-saved unattached note")
	detailsEnd := strings.Index(body[detailsStart:], "</details>")
	require.Greater(t, detailsEnd, 0, "the disclosure must close")
	// Scoped to inside the disclosure itself, for the same reason as
	// TestHandleChannelIndex_UnattachedNotesSection_CollapsedButDataStillReturned
	// above -- the note's text also legitimately appears a second time in
	// the relation-picker candidate list.
	assert.Contains(t, body[detailsStart:detailsStart+detailsEnd], "a note saved while the section is collapsed", "the saved note's text must render INSIDE the (collapsed) disclosure")
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
	body := w.Body.String()
	assert.Contains(t, body, "No verdict recorded yet")
	// FR5: with zero verdicts, no link to the verdict-details page renders
	// at all (no verdict section content beyond the empty-state line).
	// "View verdict history" is the link's own text, distinct from the
	// save-verdict form's POST action (which legitimately also targets
	// the .../verdicts path and renders regardless of verdict count).
	assert.NotContains(t, body, "View verdict history", "FR5: no link to the verdict-details page may render when the Idea has zero verdicts")
}

// TestHandleIdeaDetail_ThreeVerdictVersions_ShowsCurrentOnly_NotOlderVersions
// covers FR4 (#2034): with 3 verdict versions recorded, the Idea page
// renders ONLY the current (highest-version) verdict's reasoning/source/
// glyph -- v1's and v2's reasoning text must NOT appear anywhere on this
// page (the full oldest-to-newest history moved to the verdict-details
// page, FR7, covered separately by
// TestHandleVerdictDetail_HistorySelect_ListsEveryVersionOldestToNewest
// below). Before #2034 this page rendered the full inline history list;
// this test's own git history is the deliberate-break/fix proof for that
// regression -- see this task's Testing-phase status comment.
func TestHandleIdeaDetail_ThreeVerdictVersions_ShowsCurrentOnly_NotOlderVersions(t *testing.T) {
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

	assert.NotContains(t, body, "reasoning v1", "FR4: only the current verdict may render on the Idea page")
	assert.NotContains(t, body, "reasoning v2", "FR4: only the current verdict may render on the Idea page")
	assert.Contains(t, body, "reasoning v3", "the current (highest-version) verdict must still render")

	// v1's source (Agent) is the SAME as v3's own source, so a plain
	// Contains(body, "Agent") can't tell v1 leaking in from v3's own
	// legitimate render -- count instead: v3's card must be the ONLY
	// occurrence, never a second one from a leaked v1 history entry.
	assert.Equal(t, 1, strings.Count(body, "Agent"), "the current verdict's own Agent source must render exactly once, never once more per leaked history entry")
	assert.NotContains(t, body, "Human", "v2's Human source must never leak in -- v2 is not the current verdict")

	// FR5: a link to the verdict-details page must be present whenever at
	// least one verdict exists.
	assert.Contains(t, body, `/channels/`+ch.ID.String()+`/research/ideas/`+idea.ID.String()+`/verdicts`, "FR5: a link to the verdict-details page must render whenever at least one verdict exists")

	// FR31/FR32 (#2028): the current verdict's status renders as a single
	// glyph (via components.VerdictGlyph), never the old badge-warning/
	// badge-error verdict box.
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

	assert.Contains(t, body, "Cited notes", `current's verdict must render a "Cited notes" heading`)
	assert.Contains(t, body, uncitedNote.Text)
	assert.Contains(t, body, citedNote.Text)
	assert.Contains(t, body, "https://example.com/verdict-citation")

	// Since #2034 (FR4) this page renders the current verdict ONLY, but
	// each note's FULL text also legitimately appears earlier on the page
	// (the plain research-note list, and the save-verdict form's citation
	// multi-select) -- neither of which carries a Cited/Uncited badge.
	// Scope past the "Current" heading (which starts the verdict section,
	// after both of those) so the FIRST occurrence found here is the
	// verdict's own citedNoteBody entry, not an earlier one.
	currentIdx := strings.Index(body, "Current")
	require.Greater(t, currentIdx, 0)
	currentSection := body[currentIdx:]

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

// TestHandleIdeaDetail_HistoryVerdictCitedNotes_NeverRenderOnIdeaPage is
// FR4's citation-scoped counterpart to
// TestHandleIdeaDetail_ThreeVerdictVersions_ShowsCurrentOnly_NotOlderVersions
// above: a note cited ONLY by an earlier (history-only) verdict version
// must never appear anywhere on the Idea page at all -- since #2034 this
// page resolves citations for the current verdict only (history passed as
// nil to citedResearchNotes), so there is no "History" section left to
// scope against. The per-version citation-switch behavior this test used
// to cover moved to the verdict-details page's FR8 coverage,
// TestHandleVerdictDetail_SelectingVersion_ShowsThatVersionsCitedNotes
// below.
func TestHandleIdeaDetail_HistoryVerdictCitedNotes_NeverRenderOnIdeaPage(t *testing.T) {
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

	// Both notes legitimately still render elsewhere on the page (the
	// plain research-note list lists every note on this Idea regardless
	// of citation, and the save-verdict form's citation multi-select
	// lists every note as an <option>) -- scope specifically to the
	// verdict's OWN citedNoteBody entries (citedNoteExcerpts) so this
	// can't be vacuously satisfied by either of those.
	excerpts := citedNoteExcerpts(body)
	assert.Contains(t, excerpts, currentNote.Text, "current (v2) must render its own cited note in its Cited notes section")
	assert.NotContains(t, excerpts, historyOnlyNote.Text, "FR4: v1's cited note, which current (v2) doesn't cite, must never render in a Cited notes section on the Idea page")
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

// TestHandleVerdictDetail_RetiredWarning_RenderedOnSelectedOlderVersion is
// FR8's retired-warning counterpart to
// TestHandleIdeaDetail_HistoryVerdictCitedNotes_NeverRenderOnIdeaPage: since
// #2034 moved version-history rendering off the Idea page (FR4) onto the
// verdict-details page, this proves the warning still renders for a
// history-only citation once its OWN version is selected there (FR7) --
// the note superseding a note cited ONLY by an earlier (v1) verdict
// version must surface the warning when v1 is selected.
func TestHandleVerdictDetail_RetiredWarning_RenderedOnSelectedOlderVersion(t *testing.T) {
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

	cookie := s.sessionCookie(t, ctx, creator.ID)

	// Current (v2) cites nothing -- the warning/note must not appear by
	// default.
	currentW := s.doVerdictDetail(t, ch.ID, idea.ID, cookie, "")
	require.Equal(t, http.StatusOK, currentW.Code, "body: %s", currentW.Body.String())
	assert.NotContains(t, currentW.Body.String(), historyNote.Text, "v2 (current) cites nothing, so v1's note must not render by default")

	// Selecting v1 must render its own cited note plus the Superseded
	// warning.
	v1W := s.doVerdictDetail(t, ch.ID, idea.ID, cookie, "1")
	require.Equal(t, http.StatusOK, v1W.Code, "body: %s", v1W.Body.String())
	body := v1W.Body.String()
	assert.Contains(t, body, historyNote.Text, "selecting v1 must render its own cited note")
	assert.Contains(t, body, "Superseded", "selecting v1 must render the warning against its own retired citation")
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
// (later renamed by #1900 to expect exactly one form, then by #2032 to
// expect exactly two): the Channel index now renders exactly TWO <form>s
// -- the save-note form (#1900, FR3) AND the add-idea inline row's form
// (#2032, FR33) -- and Idea detail renders exactly TWO -- the save-note
// form AND the save-verdict form (#1901, FR4) -- both pages' forms
// method="post", for a store.CanWrite member -- what must still never
// appear is any THIRD form or paging/load-more control on either page.
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
	// The save-note form (#1900, FR3) and the add-idea inline row's form
	// (#2032, FR33) legitimately render two <form>s on this page now; what
	// must still never appear is any THIRD form or paging/load-more control.
	assert.Equal(t, 2, strings.Count(indexBody, "<form"), "exactly the save-note and add-idea forms -- no third form -- may appear")
	assert.Equal(t, 2, strings.Count(strings.ToLower(indexBody), `method="post"`))
	assert.Contains(t, indexBody, `action="/channels/`+ch.ID.String()+`/research/notes"`)
	assert.Contains(t, indexBody, `action="/channels/`+ch.ID.String()+`/research/ideas"`)

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

// doVerdictDetail GETs the verdict-details page (#2034, FR6-FR9). version,
// when non-empty, is passed through as the "version" query param (FR7's
// version-select re-render) exactly as a real <select> submit would.
func (s *researchTestStack) doVerdictDetail(t *testing.T, channelID, ideaID uuid.UUID, cookie *http.Cookie, version string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/channels/" + channelID.String() + "/research/ideas/" + ideaID.String() + "/verdicts"
	if version != "" {
		target += "?version=" + version
	}
	return s.do(t, http.MethodGet, target, cookie)
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
// both visible via store.VerdictStore.Current/History. Since #2034 (FR4)
// the Idea page itself renders the current (human) verdict only -- the
// agent-sourced version is now visible via the verdict-details page
// instead (FR6/FR7), asserted here too so this test still proves both
// sources are reachable from the web surface, just via different pages.
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

	// FR4: the Idea page shows the current (human) verdict only.
	detailW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), cookie)
	require.Equal(t, http.StatusOK, detailW.Code, "body: %s", detailW.Body.String())
	detailBody := detailW.Body.String()
	assert.Contains(t, detailBody, "human-authored reasoning")
	assert.Contains(t, detailBody, "Human", "the current human-sourced version must render its source")
	assert.NotContains(t, detailBody, "agent-authored reasoning", "FR4: the older agent-sourced version must not render on the Idea page")

	// FR6/FR7: both sources are still reachable via the verdict-details
	// page -- current (v2, human) by default, v1 (agent) via the version
	// selector.
	verdictsW := s.doVerdictDetail(t, ch.ID, idea.ID, cookie, "")
	require.Equal(t, http.StatusOK, verdictsW.Code, "body: %s", verdictsW.Body.String())
	assert.Contains(t, verdictsW.Body.String(), "human-authored reasoning")
	assert.Contains(t, verdictsW.Body.String(), "Human")

	v1W := s.doVerdictDetail(t, ch.ID, idea.ID, cookie, "1")
	require.Equal(t, http.StatusOK, v1W.Code, "body: %s", v1W.Body.String())
	assert.Contains(t, v1W.Body.String(), "agent-authored reasoning")
	assert.Contains(t, v1W.Body.String(), "Agent")
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

// ── HandleVerdictDetail (#2034, FR4-FR9) ────────────────────────────────
//
// The verdict-details page at GET .../verdicts. FR4/FR5's Idea-page-side
// coverage lives above, alongside HandleIdeaDetail's own tests; this
// section covers FR6 (current verdict prominent), FR7 (version-select,
// oldest-to-newest, and its three negative/fallback cases), FR8 (the
// selected version's own cited notes, including the retired-warning case
// covered above by TestHandleVerdictDetail_RetiredWarning_
// RenderedOnSelectedOlderVersion), FR9 (the zero-verdict empty state,
// load-bearing per the issue's Testing section), NFR4's auth ordering
// (identical to HandleIdeaDetail's), and a bounded-query-count proof as
// version count grows.

// TestHandleVerdictDetail_NoVerdicts_RendersEmptyState200_WithLinkBack is
// FR9's load-bearing case: an Idea with ZERO verdicts must render an
// explicit 200 empty state with a link back to the Idea page -- never a
// 404 or 500 (store.VerdictStore.Current's pgx.ErrNoRows must be handled
// as the empty state, not as a missing page). The status code is
// asserted explicitly since 404/500 is exactly the regression this
// guards against.
func TestHandleVerdictDetail_NoVerdicts_RendersEmptyState200_WithLinkBack(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Verdict", creator.ID)
	require.NoError(t, err)

	w := s.doVerdictDetail(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), "")
	require.Equal(t, http.StatusOK, w.Code, "FR9: zero verdicts must render 200, never 404 or 500, body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "No verdict recorded yet")
	assert.Contains(t, body, `/channels/`+ch.ID.String()+`/research/ideas/`+idea.ID.String()+`"`, "the empty state must link back to the Idea page")
}

// TestHandleVerdictDetail_CurrentVerdict_RendersGlyphReasoningAuthorTimestamp
// covers FR6: the details page renders the Idea's current verdict's value
// (via components.VerdictGlyph), reasoning, author display name, and
// timestamp.
func TestHandleVerdictDetail_CurrentVerdict_RendersGlyphReasoningAuthorTimestamp(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea FR6", creator.ID)
	require.NoError(t, err)

	v, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "FR6 reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)

	w := s.doVerdictDetail(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), "")
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	assert.Contains(t, body, components.VerdictGlyph(store.VerdictViable), "the current verdict's value must render as its glyph")
	assert.Contains(t, body, "FR6 reasoning")
	assert.Contains(t, body, "Creator", "the current verdict's author display name must render")
	assert.Contains(t, body, v.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), "the current verdict's timestamp must render")
}

// TestHandleVerdictDetail_VersionSelect_ListsEveryVersionOldestToNewest
// covers FR7: the version-select lists every version oldest-to-newest,
// selecting an older version re-renders that version's reasoning/author/
// timestamp in place of current's, and the response never renders both
// versions' reasoning at once (i.e. no diff/comparison view, #1953's
// explicit exclusion).
func TestHandleVerdictDetail_VersionSelect_ListsEveryVersionOldestToNewest(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea FR7", creator.ID)
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "fr7 reasoning v1", AuthorPersonID: creator.ID, Source: store.VerdictSourceAgent,
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNotViable, Reasoning: "fr7 reasoning v2", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)
	v3, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "fr7 reasoning v3", AuthorPersonID: creator.ID, Source: store.VerdictSourceAgent,
	})
	require.NoError(t, err)
	require.Equal(t, 3, v3.Version)

	cookie := s.sessionCookie(t, ctx, creator.ID)

	// Default (no "version" param): current (v3) renders; v1/v2 reasoning
	// does not.
	defaultW := s.doVerdictDetail(t, ch.ID, idea.ID, cookie, "")
	require.Equal(t, http.StatusOK, defaultW.Code, "body: %s", defaultW.Body.String())
	defaultBody := defaultW.Body.String()
	assert.Contains(t, defaultBody, "fr7 reasoning v3")
	assert.NotContains(t, defaultBody, "fr7 reasoning v1", "no diff/comparison view: only the selected (current) version's reasoning renders")
	assert.NotContains(t, defaultBody, "fr7 reasoning v2", "no diff/comparison view: only the selected (current) version's reasoning renders")

	// The version-select lists every version, oldest to newest.
	idx1 := strings.Index(defaultBody, "Version 1")
	idx2 := strings.Index(defaultBody, "Version 2")
	idx3 := strings.Index(defaultBody, "Version 3")
	require.Greater(t, idx1, 0, "the select must list version 1")
	require.Greater(t, idx2, 0, "the select must list version 2")
	require.Greater(t, idx3, 0, "the select must list version 3")
	assert.Less(t, idx1, idx2, "versions must list oldest to newest")
	assert.Less(t, idx2, idx3, "versions must list oldest to newest")

	// Selecting v1 re-renders v1's own reasoning, not v3's -- and still no
	// diff/comparison view (v3's reasoning must not also appear).
	v1W := s.doVerdictDetail(t, ch.ID, idea.ID, cookie, "1")
	require.Equal(t, http.StatusOK, v1W.Code, "body: %s", v1W.Body.String())
	v1Body := v1W.Body.String()
	assert.Contains(t, v1Body, "fr7 reasoning v1")
	assert.NotContains(t, v1Body, "fr7 reasoning v3", "no diff/comparison view: selecting v1 must not also render v3's reasoning")
	assert.Contains(t, v1Body, "Agent", "v1's own source must render")
}

// TestHandleVerdictDetail_SelectVersion_OutOfRange_FallsBackToCurrent is
// one of FR7's three negative cases: a version number with no matching
// entry in this Idea's own history must fall back to current -- never a
// 500.
func TestHandleVerdictDetail_SelectVersion_OutOfRange_FallsBackToCurrent(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea FR7 Out Of Range", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "only version", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)

	w := s.doVerdictDetail(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), "999")
	require.Equal(t, http.StatusOK, w.Code, "an out-of-range version must fall back to current, never 500, body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "only version")
}

// TestHandleVerdictDetail_SelectVersion_NonNumeric_FallsBackToCurrent is
// FR7's second negative case: an unparseable "version" value must also
// fall back to current.
func TestHandleVerdictDetail_SelectVersion_NonNumeric_FallsBackToCurrent(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea FR7 Non Numeric", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "only version", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)

	w := s.doVerdictDetail(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, creator.ID), "not-a-number")
	require.Equal(t, http.StatusOK, w.Code, "a non-numeric version must fall back to current, never 500, body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "only version")
}

// TestHandleVerdictDetail_SelectVersion_CrossIdeaVersionNumber_FallsBackToCurrent_NeverLeaks
// is FR7's third, load-bearing negative case: a version number that
// legitimately identifies a real verdict belonging to a DIFFERENT Idea
// must never render that other Idea's verdict here -- it falls back to
// THIS Idea's own current, exactly like an out-of-range or non-numeric
// value. ideaB has 3 versions (so "?version=3" is a real version number
// system-wide); ideaA has only 1. Requesting ideaA's page with
// "?version=3" must render ideaA's own current verdict, never ideaB's v3
// reasoning.
func TestHandleVerdictDetail_SelectVersion_CrossIdeaVersionNumber_FallsBackToCurrent_NeverLeaks(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	ideaA, err := s.store.Ideas().Create(ctx, ch.ID, "Idea A", creator.ID)
	require.NoError(t, err)
	ideaB, err := s.store.Ideas().Create(ctx, ch.ID, "Idea B", creator.ID)
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaA.ID, Verdict: store.VerdictViable, Reasoning: "ideaA current reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
			IdeaID: ideaB.ID, Verdict: store.VerdictViable, Reasoning: fmt.Sprintf("ideaB reasoning v%d SECRET", i+1), AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		})
		require.NoError(t, err)
	}

	w := s.doVerdictDetail(t, ch.ID, ideaA.ID, s.sessionCookie(t, ctx, creator.ID), "3")
	require.Equal(t, http.StatusOK, w.Code, "a cross-Idea version number must fall back to current, never 500, body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "ideaA current reasoning", "must fall back to ideaA's own current verdict")
	assert.NotContains(t, body, "SECRET", "must never render ANY of ideaB's verdicts")
}

// TestHandleVerdictDetail_SelectingVersion_ShowsThatVersionsCitedNotes
// covers FR8: the cited notes rendered change with the selected version --
// a note cited only by v1 appears when v1 is selected, and a DIFFERENT
// note cited only by v2 (current) appears when v2 is selected (default),
// never both at once.
func TestHandleVerdictDetail_SelectingVersion_ShowsThatVersionsCitedNotes(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea FR8", creator.ID)
	require.NoError(t, err)

	v1Note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "note cited only by v1", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	v2Note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "note cited only by v2", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "v1 reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{v1Note.ID},
	})
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "v2 reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
		CitedResearchNoteIDs: []uuid.UUID{v2Note.ID},
	})
	require.NoError(t, err)

	cookie := s.sessionCookie(t, ctx, creator.ID)

	currentW := s.doVerdictDetail(t, ch.ID, idea.ID, cookie, "")
	require.Equal(t, http.StatusOK, currentW.Code, "body: %s", currentW.Body.String())
	currentExcerpts := citedNoteExcerpts(currentW.Body.String())
	assert.Contains(t, currentExcerpts, v2Note.Text, "current (v2) must render its own cited note")
	assert.NotContains(t, currentExcerpts, v1Note.Text, "current (v2) must not render v1's cited note")

	v1W := s.doVerdictDetail(t, ch.ID, idea.ID, cookie, "1")
	require.Equal(t, http.StatusOK, v1W.Code, "body: %s", v1W.Body.String())
	v1Excerpts := citedNoteExcerpts(v1W.Body.String())
	assert.Contains(t, v1Excerpts, v1Note.Text, "selecting v1 must render its own cited note")
	assert.NotContains(t, v1Excerpts, v2Note.Text, "selecting v1 must not render v2's cited note")
}

// TestHandleVerdictDetail_MemberRoles_SeeSameRows proves all three
// store.CanRead tiers see the verdict-details page identically, mirroring
// TestHandleIdeaDetail_MemberRoles_SeeSameRows.
func TestHandleVerdictDetail_MemberRoles_SeeSameRows(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "shared reasoning", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
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
			w := s.doVerdictDetail(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, tc.person.ID), "")
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "shared reasoning")
		})
	}
}

func TestHandleVerdictDetail_NonMember_Forbidden(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.doVerdictDetail(t, ch.ID, idea.ID, s.sessionCookie(t, ctx, outsider.ID), "")
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleVerdictDetail_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	w := s.doVerdictDetail(t, uuid.New(), idea.ID, s.sessionCookie(t, ctx, creator.ID), "")
	assert.Equal(t, http.StatusNotFound, w.Code, "an unknown Channel must 404 before authorization runs, body: %s", w.Body.String())
}

// TestHandleVerdictDetail_NotSignedIn_Unauthorized mirrors
// TestHandleIdeaDetail_NotSignedIn_Unauthorized: a direct call with no
// Person in context must 401.
func TestHandleVerdictDetail_NotSignedIn_Unauthorized(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea One", creator.ID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String()+"/verdicts", nil)
	req.SetPathValue("id", ch.ID.String())
	req.SetPathValue("ideaID", idea.ID.String())
	w := httptest.NewRecorder()
	s.handlers.HandleVerdictDetail(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleVerdictDetail_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/not-a-uuid/research/ideas/"+uuid.NewString()+"/verdicts", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleVerdictDetail_MalformedIdeaUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/not-a-uuid/verdicts", s.sessionCookie(t, ctx, creator.ID))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleVerdictDetail_UnknownIdea_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doVerdictDetail(t, ch.ID, uuid.New(), s.sessionCookie(t, ctx, creator.ID), "")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandleVerdictDetail_CrossChannelIdea_NotFound is NFR4's load-bearing
// guard, mirroring TestHandleIdeaDetail_CrossChannelIdea_NotFound: an
// Idea that exists, but under a DIFFERENT Channel than the path's {id},
// must 404 exactly like an unknown Idea -- never 403, never rendered
// under the wrong Channel's URL.
func TestHandleVerdictDetail_CrossChannelIdea_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	chA, creatorA := s.setupChannel(t, ctx)
	chB, creatorB := s.setupChannel(t, ctx)
	ideaOnB, err := s.store.Ideas().Create(ctx, chB.ID, "Idea On B", creatorB.ID)
	require.NoError(t, err)
	_, err = s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaOnB.ID, Verdict: store.VerdictViable, Reasoning: "idea B verdict reasoning", AuthorPersonID: creatorB.ID, Source: store.VerdictSourceHuman,
	})
	require.NoError(t, err)

	w := s.doVerdictDetail(t, chA.ID, ideaOnB.ID, s.sessionCookie(t, ctx, creatorA.ID), "")
	assert.Equal(t, http.StatusNotFound, w.Code, "an Idea belonging to a different Channel must 404, not render, body: %s", w.Body.String())
	assert.NotContains(t, w.Body.String(), "idea B verdict reasoning", "the cross-Channel Idea's verdict must never render")
}

// TestHandleVerdictDetail_QueryCount_SelectedVersionCitationsOnly_
// BoundedNotPerCitation proves the Performance requirement's bound on
// THIS task's own added resolution -- verdictAuthorDisplayNames (dedup'd
// by author) and citedResearchNotes/retiredCitedResearchNotes (resolved
// for the SELECTED version only, history passed as nil) -- isolated from
// store.VerdictStore.History's own pre-existing (M1, #1606) per-row
// citedResearchNoteIDs query, which already scales with version count
// independently of this task and is out of scope here.
//
// Both Ideas below have the IDENTICAL version count (4) and the SAME
// single author, so History's own cost is equal on both sides; the ONLY
// difference is whether versions carry citations at all. Citing an
// overlapping pair of notes across every history-only version, while the
// SELECTED (current) version cites nothing, must add ZERO extra
// statements -- proving citedResearchNotes/retiredCitedResearchNotes
// really do resolve the selected version's own citations only, never
// unioning in history's. A second comparison then cites the SAME pair
// from current itself, which must add exactly the two batched calls
// (GetByIDs, RetiredNoteIDs) -- never one query per citation.
func TestHandleVerdictDetail_QueryCount_SelectedVersionCitationsOnly_BoundedNotPerCitation(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	appendFourVersions := func(ideaID uuid.UUID, historyCites, currentCites []uuid.UUID) {
		for i := 0; i < 3; i++ {
			_, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
				IdeaID: ideaID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: fmt.Sprintf("history v%d", i+1), AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
				CitedResearchNoteIDs: historyCites,
			})
			require.NoError(t, err)
		}
		_, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
			IdeaID: ideaID, Verdict: store.VerdictViable, Reasoning: "current v4", AuthorPersonID: creator.ID, Source: store.VerdictSourceHuman,
			CitedResearchNoteIDs: currentCites,
		})
		require.NoError(t, err)
	}

	var noteIDs []uuid.UUID
	for i := 0; i < 2; i++ {
		n, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID, Text: fmt.Sprintf("shared pool note %d", i), AuthorPersonID: creator.ID,
		})
		require.NoError(t, err)
		noteIDs = append(noteIDs, n.ID)
	}

	noCitationsIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea No Citations", creator.ID)
	require.NoError(t, err)
	appendFourVersions(noCitationsIdea.ID, nil, nil)

	historyOnlyCitationsIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea History-Only Citations", creator.ID)
	require.NoError(t, err)
	appendFourVersions(historyOnlyCitationsIdea.ID, noteIDs, nil)

	currentCitesIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea Current Cites", creator.ID)
	require.NoError(t, err)
	appendFourVersions(currentCitesIdea.ID, nil, noteIDs)

	countFor := func(ideaID uuid.UUID) int64 {
		counter := &researchQueryCounter{}
		traced := s.tracedResearchStack(t, ctx, counter)
		w := traced.doVerdictDetail(t, ch.ID, ideaID, traced.sessionCookie(t, ctx, creator.ID), "")
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		return counter.n
	}

	noCitationsN := countFor(noCitationsIdea.ID)
	historyOnlyN := countFor(historyOnlyCitationsIdea.ID)
	currentCitesN := countFor(currentCitesIdea.ID)

	assert.Equal(t, noCitationsN, historyOnlyN,
		"citations on history-only versions (never the selected/current one) must add ZERO extra queries -- citedResearchNotes/retiredCitedResearchNotes must resolve the selected version's own citations only; no-citations issued %d, history-only-citations issued %d", noCitationsN, historyOnlyN)
	assert.Equal(t, noCitationsN+2, currentCitesN,
		"citing 2 overlapping notes from the SELECTED (current) version must add exactly TWO additional statements (the batched GetByIDs plus the batched RetiredNoteIDs call) -- never one query per citation; no-citations issued %d, current-cites issued %d", noCitationsN, currentCitesN)
}

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

// TestHandleProposeVideoScript_HappyPath_CreatesProposedRow_RedirectsToScripts
// is FR3's happy path: a valid POST 303s to /channels/{id}/scripts and
// writes a `video_script` row bound to the Idea's current verdict, the
// picked Strategy, and the poster.
func TestHandleProposeVideoScript_HappyPath_CreatesProposedRow_RedirectsToScripts(t *testing.T) {
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
	assert.Equal(t, "/channels/"+ch.ID.String()+"/scripts", w.Header().Get("Location"))

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

// ── Issue #2029 (FR1/FR2/FR3/NFR6): note:<uuid> auto-linking + visible
// note UUID coverage. anchorFor is "note-<uuid>" everywhere (notelink.go),
// and every assertion below checks for a rendered <a href="..."> whose
// target is that exact anchor on the exact page the referenced note
// renders as itself -- never merely that SOME anchor exists. ─────────────

// noteAnchor is the stable "note-<uuid>" fragment id notelink.go's
// anchorFor and views.templ's noteBody both key off of (issue #2029).
func noteAnchor(id uuid.UUID) string { return "note-" + id.String() }

func TestHandleChannelIndex_NoteReference_SameChannel_RendersLinkToTargetAnchor(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Target Idea", creator.ID)
	require.NoError(t, err)
	target, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "the target note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Unattached",
		ChannelID: ch.ID, Text: fmt.Sprintf("see note:%s for context", target.ID), AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	wantHRef := "/channels/" + ch.ID.String() + "/research/ideas/" + idea.ID.String() + "#" + noteAnchor(target.ID)
	assert.Contains(t, w.Body.String(), `href="`+wantHRef+`"`, "an unattached note's note:<uuid> reference to a note on the same Channel must render as a link to that note's Idea-detail anchor")
}

func TestHandleIdeaDetail_NoteReference_SameIdea_RendersLinkToTargetAnchorOnSamePage(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Shared Idea", creator.ID)
	require.NoError(t, err)
	target, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "the target note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: fmt.Sprintf("as noted in NOTE:%s", target.ID), AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	wantHRef := "/channels/" + ch.ID.String() + "/research/ideas/" + idea.ID.String() + "#" + noteAnchor(target.ID)
	assert.Contains(t, w.Body.String(), `href="`+wantHRef+`"`, "a same-Idea note:<uuid> reference must link to the target note's own anchor on this same page")
}

// TestHandleIdeaDetail_NoteReference_ToNoteOnDifferentIdea_LinksToThatIdeasPage
// proves resolveNoteRefTargets' one-additional-batched-GetByIDs path
// (research.go): the referenced note is NOT among the referencing Idea's
// own loaded notes, so it can only resolve via that extra call -- and it
// must still link to the OTHER Idea's own detail page, not the page
// currently being viewed.
func TestHandleIdeaDetail_NoteReference_ToNoteOnDifferentIdea_LinksToThatIdeasPage(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	ideaA, err := s.store.Ideas().Create(ctx, ch.ID, "Idea A", creator.ID)
	require.NoError(t, err)
	ideaB, err := s.store.Ideas().Create(ctx, ch.ID, "Idea B", creator.ID)
	require.NoError(t, err)

	target, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &ideaB.ID, Text: "idea B's own note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &ideaA.ID, Text: fmt.Sprintf("related to note:%s", target.ID), AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaA.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	wantHRef := "/channels/" + ch.ID.String() + "/research/ideas/" + ideaB.ID.String() + "#" + noteAnchor(target.ID)
	assert.Contains(t, w.Body.String(), `href="`+wantHRef+`"`, "a reference to a note on a DIFFERENT Idea (resolved via the extra batched GetByIDs call) must link to that Idea's own detail page, not the page being viewed")
}

// TestHandleChannelIndex_NoteReference_UnresolvableID_RendersPlainText is
// FR3's first negative case: a well-formed uuid that names no real note.
func TestHandleChannelIndex_NoteReference_UnresolvableID_RendersPlainText(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	randomID := uuid.New()
	_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Unattached",
		ChannelID: ch.ID, Text: fmt.Sprintf("dangling reference note:%s here", randomID), AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "note:"+randomID.String(), "an unresolvable reference must still render its literal text")
	assert.NotContains(t, body, `href="`+"/channels/"+ch.ID.String()+"/research#"+noteAnchor(randomID)+`"`, "an unresolvable reference must never render as a link")
	assert.NotContains(t, body, `href="/channels/`+ch.ID.String()+`/research/ideas/`, "an unresolvable reference must not link into any Idea page either")
}

// TestHandleChannelIndex_NoteReference_CrossChannel_RendersPlainText_NoDataLeak
// is FR3's load-bearing negative case: a note on Channel A references a
// REAL note on a DIFFERENT Channel B. It must render as plain text -- and
// Channel B's note text must never appear in Channel A's response body at
// all (the cross-Channel leak guard, resolveNoteRefTargets' ChannelID
// check in research.go).
//
// This is this file's load-bearing red/green case for issue #2029: see
// this task's Testing-phase status comment for the deliberate-break
// verification -- temporarily removing resolveNoteRefTargets' `if
// n.ChannelID != channelID { continue }` guard turned this test red
// (Channel B's secret note text leaked into Channel A's response, and the
// reference rendered as a live link), and restoring the guard turned it
// green again.
func TestHandleChannelIndex_NoteReference_CrossChannel_RendersPlainText_NoDataLeak(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	chA, creatorA := s.setupChannel(t, ctx)
	chB, creatorB := s.setupChannel(t, ctx)

	const secretText = "SECRET-CHANNEL-B-ONLY-CONTENT-should-never-leak-to-A"
	noteB, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "B Research",
		ChannelID: chB.ID, Text: secretText, AuthorPersonID: creatorB.ID,
	})
	require.NoError(t, err)

	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "A Research",
		ChannelID: chA.ID, Text: fmt.Sprintf("cross-channel reference note:%s", noteB.ID), AuthorPersonID: creatorA.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+chA.ID.String()+"/research", s.sessionCookie(t, ctx, creatorA.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "note:"+noteB.ID.String(), "a cross-Channel reference must still render its literal text")
	assert.NotContains(t, body, secretText, "Channel B's note text must NEVER appear in Channel A's response body")
	assert.NotContains(t, body, `href="/channels/`+chB.ID.String(), "Channel A's response must never link into Channel B at all")
	assert.NotContains(t, body, `#`+noteAnchor(noteB.ID), "Channel A's response must never anchor-link to Channel B's note id")
}

// TestHandleIdeaDetail_NoteReference_MalformedUUID_RendersPlainText is
// FR3's third negative case: text shaped like a reference but whose
// "uuid" is not one (noteRefPattern's hex-digit character class never
// matches it, so it falls straight through as literal text).
func TestHandleIdeaDetail_NoteReference_MalformedUUID_RendersPlainText(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Malformed Ref Idea", creator.ID)
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "see note:not-a-uuid for details", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "note:not-a-uuid", "a malformed reference must render its literal text unchanged")
	assert.NotContains(t, body, `href="/channels/`+ch.ID.String()+`/research/ideas/`+idea.ID.String()+`#note-not-a-uuid"`, "a malformed reference must never render as a link")
}

// TestHandleIdeaDetail_NoteReference_TextEscaped_EvenAlongsideValidReference
// proves FR3's escaping requirement holds for BOTH the literal-text
// segments AND the linked segment of the same note's Text: templ's
// automatic escaping must apply to every segment linkifyNoteRefs produces,
// never a raw string-concatenation shortcut.
func TestHandleIdeaDetail_NoteReference_TextEscaped_EvenAlongsideValidReference(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Escaping Idea", creator.ID)
	require.NoError(t, err)
	target, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "target note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: fmt.Sprintf("<script>alert(1)</script> note:%s", target.ID), AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;", "note text must stay HTML-escaped even when it also contains a valid note:<uuid> reference")
	assert.NotContains(t, body, "<script>alert(1)</script>", "the raw, unescaped script tag from note text must never appear in the response body")
	assert.Contains(t, body, `href="/channels/`+ch.ID.String()+`/research/ideas/`+idea.ID.String()+`#`+noteAnchor(target.ID)+`"`, "the valid reference alongside the escaped text must still render as a link")
}

// TestHandleChannelIndexAndIdeaDetail_NFR6_EveryNoteUUIDVisibleInBodyText
// proves NFR6: every note rendered as itself shows its own uuid as
// visible, selectable BODY text (inside an element, e.g. `>#<uuid><`),
// never only inside an attribute value like an <option value="...">.
func TestHandleChannelIndexAndIdeaDetail_NFR6_EveryNoteUUIDVisibleInBodyText(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "NFR6 Idea", creator.ID)
	require.NoError(t, err)
	attached, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, IdeaID: &idea.ID, Text: "attached note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	unattached, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Unattached",
		ChannelID: ch.ID, Text: "unattached note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	wIndex := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, wIndex.Code, "body: %s", wIndex.Body.String())
	assert.Contains(t, wIndex.Body.String(), ">#"+unattached.ID.String()+"<", "the unattached note's own uuid must appear as visible element text on the Channel index, not only inside an attribute value")

	wIdea := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), s.sessionCookie(t, ctx, creator.ID))
	require.Equal(t, http.StatusOK, wIdea.Code, "body: %s", wIdea.Body.String())
	assert.Contains(t, wIdea.Body.String(), ">#"+attached.ID.String()+"<", "the attached note's own uuid must appear as visible element text on the Idea-detail page, not only inside an attribute value")
}

// TestHandleIdeaDetail_NoteReference_BoundedQueryCount_DoesNotScaleWithN is
// the NFR3-style performance requirement (issue #2029): resolving many
// note:<uuid> references, each naming a DIFFERENT note outside the page's
// already-loaded set, must still issue at most ONE additional batched
// query -- never one round trip per reference. fewRefs (3 notes, 3
// distinct off-page targets) and manyRefs (12 notes, 12 distinct off-page
// targets) must therefore issue the IDENTICAL total query count as each
// other, both exactly one more than a same-shape Idea with no references
// at all.
func TestHandleIdeaDetail_NoteReference_BoundedQueryCount_DoesNotScaleWithN(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	otherIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Off-Page Targets", creator.ID)
	require.NoError(t, err)

	makeOffPageTargets := func(n int) []uuid.UUID {
		ids := make([]uuid.UUID, n)
		for i := 0; i < n; i++ {
			note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Off-Page Targets",
				ChannelID: ch.ID, IdeaID: &otherIdea.ID, Text: fmt.Sprintf("off-page target %d", i), AuthorPersonID: creator.ID,
			})
			require.NoError(t, err)
			ids[i] = note.ID
		}
		return ids
	}

	buildIdeaWithReferences := func(label string, targets []uuid.UUID) store.Idea {
		idea, err := s.store.Ideas().Create(ctx, ch.ID, label, creator.ID)
		require.NoError(t, err)
		for i, target := range targets {
			text := fmt.Sprintf("note %d, no reference", i)
			if target != uuid.Nil {
				text = fmt.Sprintf("note %d references note:%s", i, target)
			}
			_, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
				ChannelID: ch.ID, IdeaID: &idea.ID, Text: text, AuthorPersonID: creator.ID,
			})
			require.NoError(t, err)
		}
		return idea
	}

	noRefsIdea := buildIdeaWithReferences("No Refs", make([]uuid.UUID, 3))
	fewRefsIdea := buildIdeaWithReferences("Few Refs", makeOffPageTargets(3))
	manyRefsIdea := buildIdeaWithReferences("Many Refs", makeOffPageTargets(12))

	runTraced := func(idea store.Idea) int64 {
		counter := &researchQueryCounter{}
		stack := s.tracedResearchStack(t, ctx, counter)
		w := stack.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), stack.sessionCookie(t, ctx, creator.ID))
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		return counter.n
	}

	noRefsCount := runTraced(noRefsIdea)
	fewRefsCount := runTraced(fewRefsIdea)
	manyRefsCount := runTraced(manyRefsIdea)

	assert.Equal(t, noRefsCount+1, fewRefsCount, "3 references to 3 distinct off-page notes must add exactly ONE batched query versus no references at all")
	assert.Equal(t, fewRefsCount, manyRefsCount, "12 references to 12 distinct off-page notes must issue the SAME query count as 3 references -- resolution must not scale per-reference")
}

// ── HandleCreateIdea (#2032, FR33-FR35, NFR1/NFR2, issue #2032) ─────────

// TestHandleCreateIdea_FounderCoCreatorAnalyst_CanCreate proves all three
// store.CanWrite tiers -- not just the Founder -- can create an Idea
// through this handler (FR34), matching store.CanWrite's actual role set
// (RoleCreator, RoleCoCreator, RoleAnalyst -- store/authz.go), not an
// assumed subset of it.
func TestHandleCreateIdea_FounderCoCreatorAnalyst_CanCreate(t *testing.T) {
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
		{"Founder", creator},
		{"CoCreator", coCreator},
		{"Analyst", analyst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", s.sessionCookie(t, ctx, tc.person.ID), url.Values{
				"idempotency_key": {uuid.NewString()},
				"title":           {tc.name + "'s idea"},
			})
			assert.Equal(t, http.StatusSeeOther, w.Code, "%s must be able to create an idea, body: %s", tc.name, w.Body.String())
		})
	}

	ideas, err := s.store.Ideas().ListByChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Len(t, ideas, 3, "all three CanWrite tiers must have created a row")
}

// TestHandleCreateIdea_NonMember_Forbidden_NoRowCreated is FR34's
// load-bearing authorization test, mirroring TestHandleSaveNote_
// NonMember_Forbidden_NoRowCreated: a forged POST from a signed-in
// non-member is 403 (re-derived fresh via authorizeWrite, never from
// which affordance the client was shown) and creates no Idea row.
func TestHandleCreateIdea_NonMember_Forbidden_NoRowCreated(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, _ := s.setupChannel(t, ctx)
	outsider := s.newPerson(t, ctx, "outsider")

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", s.sessionCookie(t, ctx, outsider.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"forged idea"},
	})
	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())

	ideas, err := s.store.Ideas().ListByChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Empty(t, ideas, "a forbidden POST must not create a row")
}

// TestHandleCreateIdea_SignedOut_Rejected_NoRowCreated covers the
// signed-out half of FR34's authorization, mirroring TestHandleSaveNote_
// SignedOut_Rejected_NoRowCreated: through the router (RequireSignedIn) a
// signed-out POST never reaches the handler and creates no row; calling
// the handler directly proves authorizeWrite's own defensive 401.
func TestHandleCreateIdea_SignedOut_Rejected_NoRowCreated(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, _ := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", nil, url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"signed-out idea"},
	})
	assert.NotEqual(t, http.StatusSeeOther, w.Code, "a signed-out POST must never succeed, body: %s", w.Body.String())

	req := httptest.NewRequest(http.MethodPost, "/channels/"+ch.ID.String()+"/research/ideas", strings.NewReader(url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"direct signed-out idea"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", ch.ID.String())
	rec := httptest.NewRecorder()
	s.handlers.HandleCreateIdea(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	ideas, err := s.store.Ideas().ListByChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Empty(t, ideas, "a signed-out POST must not create a row")
}

// TestHandleCreateIdea_UnknownChannel_NotFound mirrors
// TestHandleSaveNote_UnknownChannel_NotFound: an unknown Channel must 404
// before authorization runs.
func TestHandleCreateIdea_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+uuid.NewString()+"/research/ideas", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"idea"},
	})
	assert.Equal(t, http.StatusNotFound, w.Code, "an unknown Channel must 404 before authorization runs, body: %s", w.Body.String())
}

// TestHandleCreateIdea_MalformedChannelUUID_BadRequest mirrors
// TestHandleSaveNote_MalformedChannelUUID_BadRequest.
func TestHandleCreateIdea_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	_, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/not-a-uuid/research/ideas", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"idea"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleCreateIdea_EmptyOrWhitespaceTitle_BadRequest_NoRowWritten
// proves FR34's validation: an empty or whitespace-only title re-renders
// the Channel index (400, not 500) with the row expanded and an error,
// and calls IdeaStore.FindOrCreate not at all.
func TestHandleCreateIdea_EmptyOrWhitespaceTitle_BadRequest_NoRowWritten(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	for _, tc := range []struct {
		name  string
		title string
	}{
		{"Empty", ""},
		{"WhitespaceOnly", "   \n\t  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", cookie, url.Values{
				"idempotency_key": {uuid.NewString()},
				"title":           {tc.title},
			})
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "idea title is required")
		})
	}

	ideas, err := s.store.Ideas().ListByChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Empty(t, ideas, "an empty/whitespace title must not create a row")
}

// TestHandleCreateIdea_ConvergesOnExistingTitle_CaseWhitespaceInsensitive
// is FR34's load-bearing convergence case: submitting a title that
// differs from an existing Idea's title only by case and/or surrounding
// whitespace must converge on the SAME Idea (via IdeaStore.FindOrCreate's
// natural-key upsert) rather than forking a duplicate -- the Channel's
// Idea count must stay unchanged.
func TestHandleCreateIdea_ConvergesOnExistingTitle_CaseWhitespaceInsensitive(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	existing, err := s.store.Ideas().Create(ctx, ch.ID, "Cooking Channel Revamp", creator.ID)
	require.NoError(t, err)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"  cooking channel REVAMP  "},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	ideas, err := s.store.Ideas().ListByChannel(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, ideas, 1, "a case/whitespace-insensitive title match must converge, not fork a second Idea")
	assert.Equal(t, existing.ID, ideas[0].ID)
	assert.Equal(t, "Cooking Channel Revamp", ideas[0].Title, "the ORIGINAL title must be preserved, not overwritten by the converged-to submission")
}

// TestHandleCreateIdea_CrossChannelSameTitle_DoesNotConverge_CreatesNewIdea
// proves FR34's convergence is scoped per-Channel: an Idea with the same
// title on a DIFFERENT Channel must never cause convergence -- a new
// Idea is created on the viewed Channel instead.
func TestHandleCreateIdea_CrossChannelSameTitle_DoesNotConverge_CreatesNewIdea(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	chA, creatorA := s.setupChannel(t, ctx)
	chB, creatorB := s.setupChannel(t, ctx)
	_, err := s.store.Ideas().Create(ctx, chB.ID, "Shared Title", creatorB.ID)
	require.NoError(t, err)

	w := s.doForm(t, "/channels/"+chA.ID.String()+"/research/ideas", s.sessionCookie(t, ctx, creatorA.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"Shared Title"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

	ideasA, err := s.store.Ideas().ListByChannel(ctx, chA.ID)
	require.NoError(t, err)
	require.Len(t, ideasA, 1, "Channel A must get its OWN new Idea, not converge cross-Channel")
	assert.Equal(t, "Shared Title", ideasA[0].Title)

	ideasB, err := s.store.Ideas().ListByChannel(ctx, chB.ID)
	require.NoError(t, err)
	require.Len(t, ideasB, 1, "Channel B's original Idea must be untouched")
	assert.NotEqual(t, ideasA[0].ID, ideasB[0].ID, "the two Channels must end up with DIFFERENT Idea rows despite the identical title")
}

// TestChannelIndex_AddIdeaAffordance_PresentForFounderCoCreatorAnalyst is
// FR33's render-gate test: the "+ Add idea" affordance (and its
// underlying form) appears for every canWrite viewer -- Founder,
// Co-Creator, and Analyst alike. There is no read-but-not-write viewer to
// exercise an "absent" case against today: store.CanRead and
// store.CanWrite share the identical role set (store/authz.go), and a
// non-member cannot even GET this page (403, covered by
// TestHandleChannelIndex_NonMember_Forbidden) -- so canWrite's gate has no
// distinct false-but-visible case to assert on this page as things stand.
func TestChannelIndex_AddIdeaAffordance_PresentForFounderCoCreatorAnalyst(t *testing.T) {
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
		{"Founder", creator},
		{"CoCreator", coCreator},
		{"Analyst", analyst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/research", s.sessionCookie(t, ctx, tc.person.ID))
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "+ Add idea", "%s must see the add-idea affordance", tc.name)
			assert.Contains(t, w.Body.String(), `action="/channels/`+ch.ID.String()+`/research/ideas"`, "%s must see the add-idea form", tc.name)
		})
	}
}

// TestHandleCreateIdea_Success_NewIdeaVisible_RowReturnsToInertAffordance
// proves FR35: after a successful create, redirecting back to the
// Channel index (or a fresh GET of it) shows the new Idea in the list,
// and the inline row's form.Open is back to false (rendered as the inert
// "+ Add idea" affordance, not pre-expanded).
func TestHandleCreateIdea_Success_NewIdeaVisible_RowReturnsToInertAffordance(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", cookie, url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"Brand New Idea"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "/channels/"+ch.ID.String()+"/research", w.Header().Get("Location"))

	follow := s.do(t, http.MethodGet, w.Header().Get("Location"), cookie)
	require.Equal(t, http.StatusOK, follow.Code, "body: %s", follow.Body.String())
	body := follow.Body.String()
	assert.Contains(t, body, "Brand New Idea", "the new Idea must be visible in the re-rendered list")
	assert.NotContains(t, body, `<details class="collapse collapse-arrow bg-base-100" open`, "the add-idea row must be back to its inert (collapsed) state after a successful create")
}

// TestHandleCreateIdea_ValidationFailure_RowRendersExpandedWithError
// proves the Open/Error half of the validation-failure re-render contract
// (ideaFormWithError): a rejected submission's row re-renders expanded
// with the submitted (empty) title's error, rather than collapsing back
// to the inert affordance.
func TestHandleCreateIdea_ValidationFailure_RowRendersExpandedWithError(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	w := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", s.sessionCookie(t, ctx, creator.ID), url.Values{
		"idempotency_key": {uuid.NewString()},
		"title":           {"   "},
	})
	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "idea title is required")
	assert.Contains(t, body, `<details class="collapse collapse-arrow bg-base-100" open`, "a validation-failure re-render must render the row expanded, not collapsed")
}

// TestHandleCreateIdea_SameIdempotencyKey_Twice_CreatesOneIdea is NFR2's
// load-bearing double-submit case, mirroring TestHandleSaveNote_
// SameIdempotencyKey_Twice_CreatesOneRow: a browser back-button/refresh
// replaying the exact same POST (same server-minted idempotency_key,
// identical title) must not create a second Idea. FindOrCreate's own
// natural-key convergence is what actually enforces this (see
// HandleCreateIdea's doc comment) -- this test proves that guarantee
// holds end-to-end through the real handler, not just at the store layer.
func TestHandleCreateIdea_SameIdempotencyKey_Twice_CreatesOneIdea(t *testing.T) {
	ctx := context.Background()
	s := newResearchTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)
	key := uuid.NewString()
	form := url.Values{
		"idempotency_key": {key},
		"title":           {"Replayed Idea"},
	}

	w1 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", cookie, form)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	ideas, err := s.store.Ideas().ListByChannel(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, ideas, 1)

	w2 := s.doForm(t, "/channels/"+ch.ID.String()+"/research/ideas", cookie, form)
	assert.Equal(t, http.StatusSeeOther, w2.Code, "a replayed submit must still redirect, not error, body: %s", w2.Body.String())

	ideas, err = s.store.Ideas().ListByChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Len(t, ideas, 1, "a replayed idempotency_key (and identical title) must not create a second Idea")
}
