//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// //audience_score_system/mcp/server/server_integration_test.go for the
// pattern this file follows: spin up a throwaway Postgres via dbtest,
// apply the real embedded migrations, host RegisterVerdict's tools behind
// a real *mcp.Server over an httptest.Server, and drive them with a real
// in-process MCP client (mcp.NewClient + StreamableClientTransport).
//
// verdict_test.go's pure-Go suite (package tools, no build tag) already
// covers parseVerdictValue and excerpt's truncation rule. What this file
// proves instead is exactly what a fake/unit test cannot: real caller-
// auth + Channel-scoping wired through RegisterRead/RegisterWrite, the
// FR12 append-only/never-UPDATE guarantee (byte-for-byte comparison of a
// prior version after a later save), version allocation under genuine
// concurrency, citation validation (same-Channel, exists) atomically
// rejecting the whole write, and NFR2 idempotency replay/conflict
// semantics via store.Idempotency.Do -- see issue #1578's Testing
// section, which every test function below is named after.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:verdict_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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

// newVerdictTestDB provisions an isolated Postgres database via dbtest and
// applies every migration in the package's own embedded schema, mirroring
// research_integration_test.go's newTestDB.
func newVerdictTestDB(t *testing.T) *dbtest.Postgres {
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

// verdictFixture is the common setup every test below needs: a Channel
// with a live Creator and Analyst, an unassociated Person with no role on
// it, an Idea to record verdicts against, a second Channel with its own
// Idea and research note (for the cross-Channel-citation rejection
// tests), and a real MCP client session per Person minted via
// mcpauth.CredentialStore (migration 006) -- the same mechanism `web`'s
// token-mint endpoint uses in production.
type verdictFixture struct {
	st           *store.Store
	creds        mcpauth.CredentialStore
	ch           store.Channel
	creator      store.Person
	analyst      store.Person
	outsider     store.Person
	idea         store.Idea
	note         store.ResearchNote // same-Channel note, citable
	otherChannel store.Channel
	otherCreator store.Person
	otherNote    store.ResearchNote // different-Channel note, must be rejected as a citation
	url          string
}

func newVerdictFixture(t *testing.T) *verdictFixture {
	t.Helper()
	ctx := context.Background()

	pg := newVerdictTestDB(t)
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
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst))

	idea, err := st.Ideas().FindOrCreate(ctx, ch.ID, "Test Idea", creator.ID)
	require.NoError(t, err)

	note, err := st.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID:      ch.ID,
		IdeaID:         &idea.ID,
		Text:           "a same-channel research note",
		AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	otherCreator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-other-creator-"+uuid.NewString(), "other-creator@example.com", "Other Creator")
	require.NoError(t, err)
	otherChannel, err := st.Channels().Create(ctx, "yt-other-"+uuid.NewString(), "Other Channel", otherCreator.ID)
	require.NoError(t, err)
	otherNote, err := st.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID:      otherChannel.ID,
		Text:           "a different-channel research note",
		AuthorPersonID: otherCreator.ID,
	})
	require.NoError(t, err)

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterVerdict(reg, st)
	tools.RegisterResearch(reg, st)

	handler := server.NewHTTPHandler(srv, creds)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &verdictFixture{
		st: st, creds: creds, ch: ch, creator: creator, analyst: analyst, outsider: outsider,
		idea: idea, note: note, otherChannel: otherChannel, otherCreator: otherCreator, otherNote: otherNote,
		url: ts.URL,
	}
}

// bearerRoundTripper injects an "Authorization: Bearer <token>" header on
// every request -- mirrors research_integration_test.go's.
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// connect opens a real streamable-HTTP MCP client session authenticated as
// personID.
func (f *verdictFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
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

func (f *verdictFixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// vtextOf concatenates every TextContent block in res.Content -- the error
// message a rejected call's Content carries.
func vtextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func vdecode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	require.False(t, res.IsError, "unexpected tool error: %s", vtextOf(res))
	require.NoError(t, vmapDecode(res.StructuredContent, &out))
	return out
}

// vmapDecode round-trips v (an mcp.CallToolResult.StructuredContent,
// already a Go value the SDK decoded from JSON) through encoding/json into
// out -- the simplest way to get typed access to a map[string]any without
// hand-rolling per-field assertions in every test below.
func vmapDecode(v any, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// ── one save -> version 1, current + one-entry history ─────────────────────

func TestSaveViabilityVerdict_FirstSave_IsVersion1_CurrentAndOneEntryHistory(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	saveRes := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:            f.ch.ID.String(),
		IdeaID:               f.idea.ID.String(),
		Verdict:              "viable",
		Reasoning:            "strong comps, good pacing fit",
		CitedResearchNoteIDs: []string{f.note.ID.String()},
		IdempotencyKeyArg:    "save-1",
	})
	saved := vdecode[tools.VerdictOutput](t, saveRes)
	assert.Equal(t, 1, saved.Version)
	assert.Equal(t, "viable", saved.Verdict)
	require.Len(t, saved.CitedResearchNotes, 1)
	assert.Equal(t, f.note.ID.String(), saved.CitedResearchNotes[0].ID)

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	require.NotNil(t, out.Current)
	assert.Equal(t, 1, out.Current.Version)
	require.Len(t, out.History, 1)
	assert.Equal(t, 1, out.History[0].Version)
}

// ── second save -> version 2 current, history has both, version 1 unchanged (FR12) ──

func TestSaveViabilityVerdict_SecondSave_Version2Current_PriorVersionByteForByteUnchanged(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	first := vdecode[tools.VerdictOutput](t, f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Verdict:           "needs-more-research",
		Reasoning:         "need another comp",
		IdempotencyKeyArg: "v1",
	}))
	require.Equal(t, 1, first.Version)

	second := vdecode[tools.VerdictOutput](t, f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Verdict:           "viable",
		Reasoning:         "found the comp, green light",
		IdempotencyKeyArg: "v2",
	}))
	require.Equal(t, 2, second.Version)

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	require.NotNil(t, out.Current)
	assert.Equal(t, 2, out.Current.Version, "current must be the highest version")
	require.Len(t, out.History, 2)
	assert.Equal(t, 1, out.History[0].Version)
	assert.Equal(t, 2, out.History[1].Version)

	// The FR12 assertion: re-read version 1 directly from the store (the
	// full row, not just what the tool rendered) and compare it
	// field-for-field against what the first save originally returned --
	// it must be byte-for-byte unchanged by the second save.
	v1ID, err := uuid.Parse(first.ID)
	require.NoError(t, err)
	reread, err := f.st.Verdicts().GetByID(context.Background(), v1ID)
	require.NoError(t, err)
	assert.Equal(t, "needs-more-research", string(reread.Verdict), "version 1's verdict value must be unchanged")
	assert.Equal(t, "need another comp", reread.Reasoning, "version 1's reasoning must be unchanged")
	assert.Equal(t, f.creator.ID, reread.AuthorPersonID, "version 1's author must be unchanged")
	assert.Equal(t, 1, reread.Version, "version 1's version number must be unchanged")

	// Also confirm the history's first entry (as rendered by get) matches
	// the original save's rendering exactly.
	assert.Equal(t, first.Verdict, out.History[0].Verdict)
	assert.Equal(t, first.Reasoning, out.History[0].Reasoning)
	assert.Equal(t, first.CreatedAt, out.History[0].CreatedAt)
	assert.Equal(t, first.AuthorPersonID, out.History[0].AuthorPersonID)
}

// ── concurrent saves -> versions 1 and 2, no unique-violation surfaced, no lost write ──

func TestSaveViabilityVerdict_ConcurrentSaves_ProduceVersions1And2_NoLostWriteNoSurfacedConflict(t *testing.T) {
	f := newVerdictFixture(t)

	const n = 2
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cs := f.connect(t, f.creator.ID)
			results[i] = f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
				ChannelID:         f.ch.ID.String(),
				IdeaID:            f.idea.ID.String(),
				Verdict:           "viable",
				Reasoning:         "concurrent save attempt",
				IdempotencyKeyArg: uuid.NewString(), // distinct keys: two genuinely distinct writes, not a replay
			})
		}(i)
	}
	wg.Wait()

	versions := map[int]bool{}
	for i, res := range results {
		require.False(t, res.IsError, "call %d unexpectedly errored: %s", i, vtextOf(res))
		out := vdecode[tools.VerdictOutput](t, res)
		versions[out.Version] = true
	}
	assert.Equal(t, map[int]bool{1: true, 2: true}, versions, "two concurrent saves must produce exactly versions 1 and 2, no collision and no lost write")

	setupCS := f.connect(t, f.creator.ID)
	getRes := f.call(t, setupCS, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	require.Len(t, out.History, 2, "both concurrent writes must have landed as distinct rows")
}

// ── invalid verdict enum value rejected, nothing written ───────────────────

func TestSaveViabilityVerdict_InvalidVerdictEnumRejected_NothingWritten(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Verdict:           "maybe-viable-ish",
		Reasoning:         "should never land",
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "an invalid verdict enum value must be rejected")

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	assert.Nil(t, out.Current)
	assert.Empty(t, out.History, "a rejected save must not have written a row")
}

// ── empty reasoning rejected ────────────────────────────────────────────────

func TestSaveViabilityVerdict_EmptyReasoningRejected_NothingWritten(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Verdict:           "viable",
		Reasoning:         "   ",
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "empty/whitespace-only reasoning must be rejected")

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	assert.Nil(t, out.Current)
	assert.Empty(t, out.History)
}

// ── citing a note from a different Channel rejected, atomically ────────────

func TestSaveViabilityVerdict_CitingDifferentChannelNoteRejected_NoVerdictNoCitationRowsWritten(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:            f.ch.ID.String(),
		IdeaID:               f.idea.ID.String(),
		Verdict:              "viable",
		Reasoning:            "cites a note that isn't ours",
		CitedResearchNoteIDs: []string{f.otherNote.ID.String()},
		IdempotencyKeyArg:    uuid.NewString(),
	})
	assert.True(t, res.IsError, "citing a research note from a different Channel must be rejected")
	assert.Contains(t, vtextOf(res), "different Channel")

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	assert.Nil(t, out.Current)
	assert.Empty(t, out.History, "a rejected cross-Channel citation must leave no verdict row at all")
}

// ── citing a nonexistent note id rejected atomically ────────────────────────

func TestSaveViabilityVerdict_CitingNonexistentNoteRejected_NoPartialVerdictLeftBehind(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:            f.ch.ID.String(),
		IdeaID:               f.idea.ID.String(),
		Verdict:              "viable",
		Reasoning:            "cites a note that doesn't exist",
		CitedResearchNoteIDs: []string{uuid.NewString()},
		IdempotencyKeyArg:    uuid.NewString(),
	})
	assert.True(t, res.IsError, "citing a nonexistent research note id must be rejected")

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	assert.Nil(t, out.Current)
	assert.Empty(t, out.History, "no partial verdict row must be left behind")
}

// ── idempotency replay/conflict (NFR2/LB4) ──────────────────────────────────

func TestSaveViabilityVerdict_Replay_SameKeySameArgsYieldsExactlyOneVersionIdenticalResult(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	args := tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Verdict:           "viable",
		Reasoning:         "replayed verdict",
		IdempotencyKeyArg: "replay-key-1",
	}

	out1 := vdecode[tools.VerdictOutput](t, f.call(t, cs, "save_viability_verdict", args))
	out2 := vdecode[tools.VerdictOutput](t, f.call(t, cs, "save_viability_verdict", args))

	assert.Equal(t, out1, out2, "an identical replay must return the exact same result")

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	assert.Len(t, out.History, 1, "a replay must not append a second version")
	assert.Equal(t, 1, out.Current.Version)
}

func TestSaveViabilityVerdict_Replay_SameKeyChangedReasoningIsConflictStillOneVersion(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	first := tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Verdict:           "viable",
		Reasoning:         "original reasoning",
		IdempotencyKeyArg: "conflict-key-1",
	}
	res1 := f.call(t, cs, "save_viability_verdict", first)
	require.False(t, res1.IsError, "unexpected error: %s", vtextOf(res1))

	second := first
	second.Reasoning = "a completely different reasoning"
	res2 := f.call(t, cs, "save_viability_verdict", second)
	assert.True(t, res2.IsError, "reusing the same idempotency_key with a changed reasoning must be rejected as a conflict")

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	require.Len(t, out.History, 1, "a conflicting replay must not append a second version")
	assert.Equal(t, "original reasoning", out.History[0].Reasoning, "the original version must be unchanged")
}

// ── Analyst can save, unassociated Person cannot and nothing is written ────

func TestSaveViabilityVerdict_AnalystCanSave_UnassociatedPersonDeniedAndWritesNothing(t *testing.T) {
	f := newVerdictFixture(t)

	analystCS := f.connect(t, f.analyst.ID)
	saveRes := f.call(t, analystCS, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Verdict:           "viable",
		Reasoning:         "analyst's own verdict",
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, saveRes.IsError, "unexpected error: %s", vtextOf(saveRes))
	saved := vdecode[tools.VerdictOutput](t, saveRes)
	assert.Equal(t, f.analyst.ID.String(), saved.AuthorPersonID, "the author must be the calling Analyst, not the Channel's Creator")

	outsiderCS := f.connect(t, f.outsider.ID)
	deniedRes := f.call(t, outsiderCS, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Verdict:           "not-viable",
		Reasoning:         "should never land",
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, deniedRes.IsError)
	assert.Contains(t, vtextOf(deniedRes), "permission denied")

	creatorCS := f.connect(t, f.creator.ID)
	getRes := f.call(t, creatorCS, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	require.Len(t, out.History, 1, "the denied call must not have written a row -- only the Analyst's version 1 exists")
	assert.Equal(t, 1, out.Current.Version)
}

// ── idea_id not belonging to channel_id rejected ────────────────────────────

func TestSaveViabilityVerdict_IdeaNotBelongingToChannelRejected(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	otherIdea, err := f.st.Ideas().FindOrCreate(context.Background(), f.otherChannel.ID, "Other Channel's Idea", f.otherCreator.ID)
	require.NoError(t, err)

	res := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            otherIdea.ID.String(),
		Verdict:           "viable",
		Reasoning:         "idea from a different channel",
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "an idea_id belonging to a different channel_id must be rejected")
	assert.Contains(t, vtextOf(res), "does not belong to channel_id")
}

// ── get on an idea with no verdicts returns null/empty, not an error ───────

func TestGetViabilityVerdict_IdeaWithNoVerdicts_ReturnsCurrentNullEmptyHistoryNotError(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	require.False(t, res.IsError, "an idea with no verdict yet must not be an error: %s", vtextOf(res))
	out := vdecode[tools.GetViabilityVerdictOutput](t, res)
	assert.Nil(t, out.Current)
	assert.Empty(t, out.History)
}

// ── cited notes rendered with excerpt/source_url/cited (FR10) ──────────────

func TestGetViabilityVerdict_CitedNotesRenderedWithExcerptSourceURLAndCitedFlag(t *testing.T) {
	f := newVerdictFixture(t)
	cs := f.connect(t, f.creator.ID)

	citedURL := "https://example.com/comp-video"
	citedNoteRes := f.call(t, cs, "save_research_note", tools.SaveResearchNoteInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		Text:              "a note with its own source",
		SourceURL:         citedURL,
		IdempotencyKeyArg: "cited-note-1",
	})
	citedNote := vdecode[tools.ResearchNoteOutput](t, citedNoteRes)

	saveRes := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:            f.ch.ID.String(),
		IdeaID:               f.idea.ID.String(),
		Verdict:              "viable",
		Reasoning:            "citing both an uncited and a cited note",
		CitedResearchNoteIDs: []string{f.note.ID.String(), citedNote.ID},
		IdempotencyKeyArg:    uuid.NewString(),
	})
	require.False(t, saveRes.IsError, "unexpected error: %s", vtextOf(saveRes))

	getRes := f.call(t, cs, "get_viability_verdict", tools.GetViabilityVerdictInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(),
	})
	out := vdecode[tools.GetViabilityVerdictOutput](t, getRes)
	require.NotNil(t, out.Current)
	require.Len(t, out.Current.CitedResearchNotes, 2)

	byID := map[string]tools.CitedNoteOutput{}
	for _, n := range out.Current.CitedResearchNotes {
		byID[n.ID] = n
	}

	uncited, ok := byID[f.note.ID.String()]
	require.True(t, ok)
	assert.False(t, uncited.Cited, "the fixture's note (no source_url) must render cited=false")
	assert.Nil(t, uncited.SourceURL)
	assert.Equal(t, "a same-channel research note", uncited.TextExcerpt)

	cited, ok := byID[citedNote.ID]
	require.True(t, ok)
	assert.True(t, cited.Cited, "a note with a source_url must render cited=true")
	require.NotNil(t, cited.SourceURL)
	assert.Equal(t, citedURL, *cited.SourceURL)
	assert.Equal(t, "a note with its own source", cited.TextExcerpt)
}
