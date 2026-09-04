//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// //audience_score_system/mcp/server/server_integration_test.go for the
// pattern this file follows: spin up a throwaway Postgres via dbtest,
// apply the real embedded migrations, host RegisterResearch's tools behind
// a real *mcp.Server over an httptest.Server, and drive them with a real
// in-process MCP client (mcp.NewClient + StreamableClientTransport).
//
// research_test.go's pure-Go suite (package tools, no build tag) already
// covers validateSourceURL and toResearchNoteOutput's Cited derivation.
// What this file proves instead is exactly what a fake/unit test cannot:
// real caller-auth + Channel-scoping wired through RegisterRead/
// RegisterWrite, real Postgres NULL-vs-empty-string storage for source_url (FR10),
// real idempotency replay/conflict semantics (NFR2/LB4) via
// store.Idempotency.Do, and create_idea's advisory-lock-guarded natural-key
// upsert under genuine concurrency.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:research_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newTestCredentialStore builds the mcpauth.CredentialStore against pool's
// mcp_credential table (migration 006) -- the same construction main.go
// does, mirrored here so tests mint/verify through the identical backing
// this task migrated onto (FR13/NFR3 parity).
func newTestCredentialStore(t *testing.T, pool *pgxpool.Pool) mcpauth.CredentialStore {
	t.Helper()
	creds, err := mcpauth.NewCredentialStore(context.Background(), mcpauth.StoreConfig{
		Pool:           pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	require.NoError(t, err)
	return creds
}

// newTestDB provisions an isolated Postgres database via dbtest and
// applies every migration in the package's own embedded schema, mirroring
// ../server/server_integration_test.go's newTestDB.
func newTestDB(t *testing.T) *dbtest.Postgres {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return pg
}

// fixture is the common setup every test below needs: a Channel with a
// live Creator and Analyst, plus an unassociated Person with no role on
// it, and a real MCP client session per Person minted via
// mcpauth.CredentialStore (migration 006) -- the same mechanism `web`'s
// token-mint endpoint uses in production.
type fixture struct {
	st       *store.Store
	creds    mcpauth.CredentialStore
	ch       store.Channel
	creator  store.Person
	analyst  store.Person
	outsider store.Person
	url      string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	pg := newTestDB(t)
	st := store.New(pg.Pool)
	creds := newTestCredentialStore(t, pg.Pool)

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-creator-"+uuid.NewString(), "creator@example.com", "Creator Person")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-analyst-"+uuid.NewString(), "analyst@example.com", "Analyst Person")
	require.NoError(t, err)
	outsider, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-outsider-"+uuid.NewString(), "outsider@example.com", "Outsider Person")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterResearch(reg, st)

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &fixture{st: st, creds: creds, ch: ch, creator: creator, analyst: analyst, outsider: outsider, url: ts.URL}
}

// bearerRoundTripper injects an "Authorization: Bearer <token>" header on
// every request -- mirrors ../server/server_integration_test.go's.
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// connect opens a real streamable-HTTP MCP client session authenticated as
// personID.
func (f *fixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (f *fixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// textOf concatenates every TextContent block in res.Content -- the error
// message a rejected call's Content carries.
func textOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func decode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	require.False(t, res.IsError, "unexpected tool error: %s", textOf(res))
	require.NoError(t, mapDecode(res.StructuredContent, &out))
	return out
}

// mapDecode round-trips v (an mcp.CallToolResult.StructuredContent,
// already a Go value the SDK decoded from JSON) through encoding/json into
// out -- the simplest way to get typed access to a map[string]any without
// hand-rolling per-field assertions in every test below.
func mapDecode(v any, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// ── save_research_note (FR9/FR10) ───────────────────────────────────────────

func TestSaveResearchNote_CitedVsUncited_StoresNULLNotEmptyString(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	uncitedRes := f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "no source yet, just a hunch",
		IdempotencyKeyArg: "note-uncited-1",
	})
	uncited := decode[tools.ResearchNoteOutput](t, uncitedRes)
	assert.False(t, uncited.Cited, "omitted source_url must render cited=false")
	assert.Nil(t, uncited.SourceURL)

	citedRes := f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "found a strong comp",
		SourceURL:         "https://example.com/comp-video",
		IdempotencyKeyArg: "note-cited-1",
	})
	cited := decode[tools.ResearchNoteOutput](t, citedRes)
	assert.True(t, cited.Cited, "a well-formed source_url must render cited=true")
	require.NotNil(t, cited.SourceURL)
	assert.Equal(t, "https://example.com/comp-video", *cited.SourceURL)

	listRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String()})
	list := decode[tools.ListResearchNotesOutput](t, listRes)
	require.Len(t, list.Notes, 2)

	byID := map[string]tools.ResearchNoteOutput{list.Notes[0].ID: list.Notes[0], list.Notes[1].ID: list.Notes[1]}
	assert.False(t, byID[uncited.ID].Cited)
	assert.Nil(t, byID[uncited.ID].SourceURL)
	assert.True(t, byID[cited.ID].Cited)
	require.NotNil(t, byID[cited.ID].SourceURL)
	assert.Equal(t, "https://example.com/comp-video", *byID[cited.ID].SourceURL)
}

func TestSaveResearchNote_EmptySourceURLIsTreatedAsUncitedNotEmptyStringCitation(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "text with explicit empty source_url",
		SourceURL:         "",
		IdempotencyKeyArg: "note-empty-source",
	})
	out := decode[tools.ResearchNoteOutput](t, res)
	assert.False(t, out.Cited, `source_url: "" must be treated as uncited (NULL), not a cited note with an empty URL`)
	assert.Nil(t, out.SourceURL)
}

func TestSaveResearchNote_MalformedSourceURLRejectedNothingPersisted(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	for i, bad := range []string{"not a url", "javascript:alert(1)"} {
		res := f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{
			ChannelID:         f.ch.ID.String(),
			Text:              "text",
			SourceURL:         bad,
			IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "case %d: %q must be rejected", i, bad)
	}

	listRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String()})
	list := decode[tools.ListResearchNotesOutput](t, listRes)
	assert.Empty(t, list.Notes, "a rejected source_url must persist nothing")
}

func TestSaveResearchNote_EmptyTextRejected(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "   ",
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "empty/whitespace-only text must be rejected")

	listRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String()})
	list := decode[tools.ListResearchNotesOutput](t, listRes)
	assert.Empty(t, list.Notes)
}

func TestSaveResearchNote_RecordsCallingPersonAsAuthorNotCreator(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.analyst.ID)

	res := f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "analyst's own note",
		IdempotencyKeyArg: uuid.NewString(),
	})
	out := decode[tools.ResearchNoteOutput](t, res)
	assert.Equal(t, f.analyst.ID.String(), out.AuthorPersonID, "the note's author must be the CALLING Person, not the Channel's Creator")
	assert.Equal(t, "Analyst Person", out.AuthorDisplayName)
}

func TestSaveResearchNote_AnalystCanSaveAndList_UnassociatedPersonDeniedAndWritesNothing(t *testing.T) {
	f := newFixture(t)

	// Analyst can save and immediately see it in list_research_notes
	// (mutual visibility).
	analystCS := f.connect(t, f.analyst.ID)
	saveRes := f.call(t, analystCS, "save_research_note", tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "analyst research",
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, saveRes.IsError, "unexpected error: %s", textOf(saveRes))

	creatorCS := f.connect(t, f.creator.ID)
	listRes := f.call(t, creatorCS, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String()})
	list := decode[tools.ListResearchNotesOutput](t, listRes)
	require.Len(t, list.Notes, 1, "the Creator must see the Analyst's note (mutual visibility)")

	// A Person with no role on the Channel gets a permission error and
	// nothing is written.
	outsiderCS := f.connect(t, f.outsider.ID)
	deniedRes := f.call(t, outsiderCS, "save_research_note", tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "should never land",
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, deniedRes.IsError)
	assert.Contains(t, textOf(deniedRes), "permission denied")

	listRes2 := f.call(t, creatorCS, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String()})
	list2 := decode[tools.ListResearchNotesOutput](t, listRes2)
	assert.Len(t, list2.Notes, 1, "the denied call must not have written a row")
}

// ── save_research_note idempotency replay/conflict (NFR2/LB4) ──────────────

func TestSaveResearchNote_Replay_SameKeySameArgsYieldsExactlyOneRowIdenticalResult(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	args := tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "replayed note",
		IdempotencyKeyArg: "replay-key-1",
	}

	res1 := f.call(t, cs, "save_research_note", args)
	out1 := decode[tools.ResearchNoteOutput](t, res1)

	res2 := f.call(t, cs, "save_research_note", args)
	out2 := decode[tools.ResearchNoteOutput](t, res2)

	assert.Equal(t, out1, out2, "an identical replay must return the exact same result")

	listRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String()})
	list := decode[tools.ListResearchNotesOutput](t, listRes)
	assert.Len(t, list.Notes, 1, "a replay must not create a second row")
}

func TestSaveResearchNote_Replay_SameKeyDifferentTextIsConflictAndStillOneRow(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	first := tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		Text:              "original text",
		IdempotencyKeyArg: "conflict-key-1",
	}
	res1 := f.call(t, cs, "save_research_note", first)
	require.False(t, res1.IsError, "unexpected error: %s", textOf(res1))

	second := first
	second.Text = "a completely different note"
	res2 := f.call(t, cs, "save_research_note", second)
	assert.True(t, res2.IsError, "reusing the same idempotency_key with different args must be rejected as a conflict")

	listRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String()})
	list := decode[tools.ListResearchNotesOutput](t, listRes)
	require.Len(t, list.Notes, 1, "a conflicting replay must not create a second row")
	assert.Equal(t, "original text", list.Notes[0].Text, "the original row must be unchanged")
}

// ── create_idea (natural-key upsert, FR9) ───────────────────────────────────

func TestCreateIdea_SameTitleTwiceConvergesOnOneIdea_CaseAndWhitespaceInsensitive(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	first := f.call(t, cs, "create_idea", tools.CreateIdeaInput{ChannelID: f.ch.ID.String(), Title: "Cats vs Dogs Reaction"})
	firstOut := decode[tools.IdeaOutput](t, first)

	second := f.call(t, cs, "create_idea", tools.CreateIdeaInput{ChannelID: f.ch.ID.String(), Title: "  cats VS dogs reaction  "})
	secondOut := decode[tools.IdeaOutput](t, second)

	assert.Equal(t, firstOut.IdeaID, secondOut.IdeaID, "a case/whitespace-insensitively matching title must converge on the same Idea")

	listRes := f.call(t, cs, "list_ideas", tools.ListIdeasInput{ChannelID: f.ch.ID.String()})
	list := decode[tools.ListIdeasOutput](t, listRes)
	require.Len(t, list.Ideas, 1, "exactly one Idea row must have been created")
}

// TestCreateIdea_ConcurrentSameTitleRace_ConvergesOnExactlyOneIdea drives N
// concurrent create_idea calls for the SAME (channel, title) through the
// real MCP transport, proving store.IdeaStore.FindOrCreate's transaction-
// scoped Postgres advisory lock (store/idea.go) actually serializes the
// race rather than letting concurrent callers each insert their own row.
func TestCreateIdea_ConcurrentSameTitleRace_ConvergesOnExactlyOneIdea(t *testing.T) {
	f := newFixture(t)

	const n = 10
	var wg sync.WaitGroup
	ideaIDs := make([]string, n)
	errs := make([]bool, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cs := f.connect(t, f.creator.ID)
			res := f.call(t, cs, "create_idea", tools.CreateIdeaInput{ChannelID: f.ch.ID.String(), Title: "Concurrent Idea Title"})
			if res.IsError {
				errs[i] = true
				return
			}
			ideaIDs[i] = decode[tools.IdeaOutput](t, res).IdeaID
		}(i)
	}
	wg.Wait()

	for i := range n {
		require.False(t, errs[i], "call %d unexpectedly errored", i)
	}
	for i := 1; i < n; i++ {
		assert.Equal(t, ideaIDs[0], ideaIDs[i], "call %d must converge on the same idea_id as call 0 under the advisory lock", i)
	}

	setupCS := f.connect(t, f.creator.ID)
	listRes := f.call(t, setupCS, "list_ideas", tools.ListIdeasInput{ChannelID: f.ch.ID.String()})
	list := decode[tools.ListIdeasOutput](t, listRes)
	require.Len(t, list.Ideas, 1, "N concurrent creates for the same natural key must leave exactly one Idea row, never one per caller")
}

// ── list_research_notes filters (FR10) / list_ideas stats ──────────────────

func TestListResearchNotes_CitedAndUncitedOnlyFiltersPartitionExactly(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{ChannelID: f.ch.ID.String(), Text: "uncited 1", IdempotencyKeyArg: "u1"})
	f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{ChannelID: f.ch.ID.String(), Text: "uncited 2", IdempotencyKeyArg: "u2"})
	f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{ChannelID: f.ch.ID.String(), Text: "cited 1", SourceURL: "https://example.com/a", IdempotencyKeyArg: "c1"})

	allRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String()})
	all := decode[tools.ListResearchNotesOutput](t, allRes)
	require.Len(t, all.Notes, 3)

	citedRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String(), CitedOnly: true})
	cited := decode[tools.ListResearchNotesOutput](t, citedRes)
	require.Len(t, cited.Notes, 1)
	assert.True(t, cited.Notes[0].Cited)
	assert.Equal(t, "cited 1", cited.Notes[0].Text)

	uncitedRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String(), UncitedOnly: true})
	uncited := decode[tools.ListResearchNotesOutput](t, uncitedRes)
	require.Len(t, uncited.Notes, 2)
	for _, n := range uncited.Notes {
		assert.False(t, n.Cited)
	}

	bothRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String(), CitedOnly: true, UncitedOnly: true})
	assert.True(t, bothRes.IsError, "cited_only and uncited_only together must be rejected")
}

func TestListResearchNotes_IdeaIDFilterRestrictsToThatIdea(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	idea1Res := f.call(t, cs, "create_idea", tools.CreateIdeaInput{ChannelID: f.ch.ID.String(), Title: "Idea One"})
	idea1 := decode[tools.IdeaOutput](t, idea1Res)
	idea2Res := f.call(t, cs, "create_idea", tools.CreateIdeaInput{ChannelID: f.ch.ID.String(), Title: "Idea Two"})
	idea2 := decode[tools.IdeaOutput](t, idea2Res)

	f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{ChannelID: f.ch.ID.String(), IdeaID: idea1.IdeaID, Text: "for idea one", IdempotencyKeyArg: "i1n1"})
	f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{ChannelID: f.ch.ID.String(), IdeaID: idea2.IdeaID, Text: "for idea two", IdempotencyKeyArg: "i2n1"})
	f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{ChannelID: f.ch.ID.String(), Text: "no idea at all", IdempotencyKeyArg: "noidea"})

	res := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String(), IdeaID: idea1.IdeaID})
	out := decode[tools.ListResearchNotesOutput](t, res)
	require.Len(t, out.Notes, 1)
	assert.Equal(t, "for idea one", out.Notes[0].Text)
}

// TestListResearchNotes_LimitTruncatedAndBeforePageBackwardExactly proves
// issue #1808's fix: limit caps the response with truncated set, and before
// (paired with the oldest returned note's created_at) retrieves the row(s)
// that fell off the page -- before this fix, list_research_notes had no
// limit at all and could exceed an MCP client's response-size cap outright.
func TestListResearchNotes_LimitTruncatedAndBeforePageBackwardExactly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	const seeded = 5
	for i := 0; i < seeded; i++ {
		_, err := f.st.Research().SaveNote(ctx, store.SaveNoteInput{
			ChannelID: f.ch.ID, Text: fmt.Sprintf("page note %d", i),
			AuthorPersonID: f.creator.ID, IdempotencyKey: fmt.Sprintf("page-note-%d", i),
		})
		require.NoError(t, err)
	}

	firstRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String(), Limit: 3})
	firstPage := decode[tools.ListResearchNotesOutput](t, firstRes)
	require.Len(t, firstPage.Notes, 3, "must be capped at the caller-supplied limit")
	assert.True(t, firstPage.Truncated, "more notes exist beyond limit")
	for _, n := range firstPage.Notes {
		assert.NotContains(t, []string{"page note 0", "page note 1"}, n.Text, "the two oldest notes must have fallen off the first (newest-first) page")
	}

	oldestOnFirstPage := firstPage.Notes[len(firstPage.Notes)-1]
	before, err := time.Parse(time.RFC3339, oldestOnFirstPage.CreatedAt)
	require.NoError(t, err)

	secondRes := f.call(t, cs, "list_research_notes", tools.ListResearchNotesInput{ChannelID: f.ch.ID.String(), Before: &before})
	secondPage := decode[tools.ListResearchNotesOutput](t, secondRes)
	require.Len(t, secondPage.Notes, 2, "before must surface exactly the notes that fell off the first page")
	assert.False(t, secondPage.Truncated)
	texts := []string{secondPage.Notes[0].Text, secondPage.Notes[1].Text}
	assert.ElementsMatch(t, []string{"page note 0", "page note 1"}, texts)
}

func TestListIdeas_NoteCountAndHasVerdictStats(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.creator.ID)

	ideaRes := f.call(t, cs, "create_idea", tools.CreateIdeaInput{ChannelID: f.ch.ID.String(), Title: "Stats Idea"})
	idea := decode[tools.IdeaOutput](t, ideaRes)
	ideaID, err := uuid.Parse(idea.IdeaID)
	require.NoError(t, err)

	f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{ChannelID: f.ch.ID.String(), IdeaID: idea.IdeaID, Text: "note 1", IdempotencyKeyArg: "s1"})
	f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{ChannelID: f.ch.ID.String(), IdeaID: idea.IdeaID, Text: "note 2", IdempotencyKeyArg: "s2"})

	beforeRes := f.call(t, cs, "list_ideas", tools.ListIdeasInput{ChannelID: f.ch.ID.String()})
	before := decode[tools.ListIdeasOutput](t, beforeRes)
	require.Len(t, before.Ideas, 1)
	assert.Equal(t, 2, before.Ideas[0].NoteCount)
	assert.False(t, before.Ideas[0].HasVerdict, "no verdict has been recorded yet")

	_, err = f.st.Verdicts().Append(context.Background(), store.AppendVerdictInput{
		IdeaID: ideaID, Verdict: store.VerdictViable, Reasoning: "green light", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)

	afterRes := f.call(t, cs, "list_ideas", tools.ListIdeasInput{ChannelID: f.ch.ID.String()})
	after := decode[tools.ListIdeasOutput](t, afterRes)
	require.Len(t, after.Ideas, 1)
	assert.Equal(t, 2, after.Ideas[0].NoteCount, "note count must be unaffected by the verdict append")
	assert.True(t, after.Ideas[0].HasVerdict, "list_ideas must reflect the newly recorded verdict")
}
