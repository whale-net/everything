//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and schedule_draft_integration_test.go
// for the pattern this file follows: spin up a throwaway Postgres via
// dbtest, apply the real embedded migrations, host RegisterVerdict's,
// RegisterScheduleDraft's, and RegisterStrategy's tools behind a real
// *mcp.Server over an httptest.Server, and drive them with a real
// in-process MCP client.
//
// strategy_test.go's pure-Go suite (package tools, no build tag) already
// covers SaveStrategyInput/GetStrategyInput/ListStrategiesInput.
// ChannelScopeID() and SaveStrategyInput.IdempotencyKey() in isolation.
// What this file proves instead: save_strategy's FR16-style viable-verdict
// gate (rejecting an idea_id whose current verdict is not viable, nothing
// written), create-vs-update (strategy_id) semantics including wholesale
// idea-link replacement, idempotent replay convergence, Channel-scoping
// (Creator+Analyst write, unassociated Person denied), and -- FR47, issue
// #1833 -- that a Strategy no longer carries any cadence: save_strategy
// succeeds without one, rejects a caller that supplies one (struct-derived
// input schemas set additionalProperties: false, so an unknown "cadence"
// property fails schema validation before the handler ever runs), and
// get_strategy/list_strategies output carries no cadence key. It also
// proves generate_schedule_plan (retired by FR41, doubly dead under FR47)
// is gone from the registry entirely -- all against the real embedded
// schema and a real HTTP-hosted MCP server, mirroring
// schedule_draft_integration_test.go's pattern (issue #1637).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:strategy_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// strategyBearerRoundTripper injects an "Authorization: Bearer <token>"
// header on every request -- this file's own copy, since each
// *_integration_test.go file is its own go_test target (see BUILD.bazel)
// and cannot share unexported helpers with a sibling file's target, per
// verdict_integration_test.go/research_integration_test.go's identical
// per-file bearerRoundTripper copies.
type strategyBearerRoundTripper struct{ token string }

func (rt strategyBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// stTextOf concatenates every TextContent block in res.Content -- the
// error message a rejected call's Content carries.
func stTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func stDecode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	require.False(t, res.IsError, "unexpected tool error: %s", stTextOf(res))
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// stDecodeMap decodes res.StructuredContent into a raw
// map[string]any -- unlike stDecode[T], this surfaces every wire key
// actually present (including one a typed struct like StrategyOutput
// would silently drop), which is what proves "no cadence key" rather
// than merely "cadence didn't unmarshal into a field that no longer
// exists" (FR47, issue #1833).
func stDecodeMap(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	var out map[string]any
	require.False(t, res.IsError, "unexpected tool error: %s", stTextOf(res))
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// newTestCredentialStore builds the mcpauth.CredentialStore against pool's
// mcp_credential table (migration 006) -- the same construction main.go
// does, mirrored here so tests mint/verify through the identical backing
// production uses (FR13/NFR3 parity), per verdict_integration_test.go's
// identical helper.
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

// strategyFixture: a Channel with a live Creator and Analyst, an
// unassociated Person, and a viable-verdict Idea ready to build a
// Strategy from, hosted behind a real MCP server with RegisterVerdict +
// RegisterStrategy wired (RegisterScheduleDraft was retired outright by
// #1832, before schedule_draft.go itself was deleted).
type strategyFixture struct {
	st       *store.Store
	creds    mcpauth.CredentialStore
	ch       store.Channel
	creator  store.Person
	analyst  store.Person
	outsider store.Person
	url      string
}

func newStrategyFixture(t *testing.T) *strategyFixture {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	st := store.New(pg.Pool)
	creds := newTestCredentialStore(t, pg.Pool)

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-strat-creator-"+uuid.NewString(), "strat-creator@example.com", "Creator Person")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-strat-analyst-"+uuid.NewString(), "strat-analyst@example.com", "Analyst Person")
	require.NoError(t, err)
	outsider, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-strat-outsider-"+uuid.NewString(), "strat-outsider@example.com", "Outsider Person")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-strat-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterVerdict(reg, st)
	tools.RegisterStrategy(reg, st)

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &strategyFixture{st: st, creds: creds, ch: ch, creator: creator, analyst: analyst, outsider: outsider, url: ts.URL}
}

func (f *strategyFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: strategyBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (f *strategyFixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// viableIdea creates a fresh Idea on f.ch and records a viable verdict for
// it as personID (via cs), returning both.
func (f *strategyFixture) viableIdea(t *testing.T, cs *mcp.ClientSession, personID uuid.UUID, title string) (store.Idea, tools.VerdictOutput) {
	t.Helper()
	idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, title+" "+uuid.NewString(), personID)
	require.NoError(t, err)

	res := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            idea.ID.String(),
		Verdict:           "viable",
		Reasoning:         "viable for strategy testing",
		IdempotencyKeyArg: uuid.NewString(),
	})
	return idea, stDecode[tools.VerdictOutput](t, res)
}

// ── save_strategy: FR16-style viable-verdict gate ────────────────────────

func TestSaveStrategy_ViableVerdicts_CreatesWithLinkedVerdictsAndIdeas(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	idea1, v1 := f.viableIdea(t, cs, f.creator.ID, "Weekly clip idea")
	idea2, v2 := f.viableIdea(t, cs, f.creator.ID, "Second weekly idea")

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Short themed clips",
		PreferredWeekday:  "Monday",
		VerdictIDs:        []string{v1.ID, v2.ID},
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "unexpected error: %s", stTextOf(res))
	out := stDecode[tools.StrategyOutput](t, res)

	assert.Equal(t, f.ch.ID.String(), out.ChannelID)
	assert.Equal(t, "Monday", out.PreferredWeekday)
	assert.True(t, out.Active, "active must default to true when omitted")
	require.Len(t, out.Verdicts, 2)

	byVerdictID := map[string]tools.StrategyVerdictOutput{}
	for _, v := range out.Verdicts {
		byVerdictID[v.VerdictID] = v
	}
	require.Contains(t, byVerdictID, v1.ID)
	assert.Equal(t, idea1.ID.String(), byVerdictID[v1.ID].IdeaID)
	require.Contains(t, byVerdictID, v2.ID)
	assert.Equal(t, idea2.ID.String(), byVerdictID[v2.ID].IdeaID)
}

func TestSaveStrategy_NonViableVerdict_RejectedNoRowWritten(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Not viable idea "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)
	verdictRes := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            idea.ID.String(),
		Verdict:           "not-viable",
		Reasoning:         "too niche",
		IdempotencyKeyArg: uuid.NewString(),
	})
	notViable := stDecode[tools.VerdictOutput](t, verdictRes)

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Should fail",
		VerdictIDs:        []string{notViable.ID},
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "a not-viable verdict must be rejected")

	strategies, _, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false, 0)
	require.NoError(t, err)
	assert.Empty(t, strategies, "no strategy row must exist after a rejected save")
}

func TestSaveStrategy_UnknownVerdictID_Rejected(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Should fail",
		VerdictIDs:        []string{uuid.NewString()},
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "a verdict_id that does not exist at all must be rejected")
}

func TestSaveStrategy_SameVerdictLinkedToMultipleStrategies(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	idea, v := f.viableIdea(t, cs, f.creator.ID, "Shared verdict idea")

	first := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "First strategy",
		VerdictIDs: []string{v.ID}, IdempotencyKeyArg: uuid.NewString(),
	}))
	second := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Second strategy",
		VerdictIDs: []string{v.ID}, IdempotencyKeyArg: uuid.NewString(),
	}))

	assert.NotEqual(t, first.StrategyID, second.StrategyID, "two distinct Strategies must be created")
	require.Len(t, first.Verdicts, 1)
	require.Len(t, second.Verdicts, 1)
	assert.Equal(t, v.ID, first.Verdicts[0].VerdictID)
	assert.Equal(t, v.ID, second.Verdicts[0].VerdictID)
	assert.Equal(t, idea.ID.String(), first.Verdicts[0].IdeaID)
	assert.Equal(t, idea.ID.String(), second.Verdicts[0].IdeaID)
}

// ── save_strategy: create vs. update ──────────────────────────────────────

func TestSaveStrategy_StrategyIDSupplied_UpdatesAndReplacesLinkedVerdictsWholesale(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	_, v1 := f.viableIdea(t, cs, f.creator.ID, "Original idea")
	created := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Original title",
		VerdictIDs:        []string{v1.ID},
		IdempotencyKeyArg: uuid.NewString(),
	}))

	idea2, v2 := f.viableIdea(t, cs, f.creator.ID, "Replacement idea")
	updated := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		StrategyID:        created.StrategyID,
		Title:             "Updated title",
		VerdictIDs:        []string{v2.ID},
		IdempotencyKeyArg: uuid.NewString(),
	}))

	assert.Equal(t, created.StrategyID, updated.StrategyID, "supplying strategy_id must update the same row, not create a new one")
	assert.Equal(t, "Updated title", updated.Title)
	require.Len(t, updated.Verdicts, 1, "the linked verdict set must be replaced wholesale, not merged")
	assert.Equal(t, idea2.ID.String(), updated.Verdicts[0].IdeaID)

	all, _, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false, 0)
	require.NoError(t, err)
	assert.Len(t, all, 1, "an update must not create a second strategy row")
}

func TestSaveStrategy_UnknownStrategyID_Rejected(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	_, v := f.viableIdea(t, cs, f.creator.ID, "Idea")

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		StrategyID:        uuid.NewString(),
		Title:             "Should fail",
		VerdictIDs:        []string{v.ID},
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError)
}

// ── save_strategy: idempotent replay ──────────────────────────────────────

func TestSaveStrategy_IdempotentReplay_ConvergesOnSameRow(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	_, v := f.viableIdea(t, cs, f.creator.ID, "Replay idea")

	args := tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Replay strategy",
		VerdictIDs:        []string{v.ID},
		IdempotencyKeyArg: "replay-key-strategy",
	}
	first := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", args))
	second := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", args))
	assert.Equal(t, first.StrategyID, second.StrategyID)

	all, _, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false, 0)
	require.NoError(t, err)
	assert.Len(t, all, 1, "a replay must never duplicate the strategy row")
}

// ── save_strategy: Analyst can write, unassociated Person cannot ─────────

func TestSaveStrategy_AnalystCanSave_UnassociatedPersonDeniedWritesNothing(t *testing.T) {
	f := newStrategyFixture(t)
	analystCS := f.connect(t, f.analyst.ID)
	_, v := f.viableIdea(t, analystCS, f.analyst.ID, "Analyst idea")

	res := f.call(t, analystCS, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Analyst strategy",
		VerdictIDs:        []string{v.ID},
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "unexpected error: %s", stTextOf(res))

	outsiderCS := f.connect(t, f.outsider.ID)
	denied := f.call(t, outsiderCS, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Should be denied",
		VerdictIDs:        []string{v.ID},
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, denied.IsError)
	assert.Contains(t, stTextOf(denied), "permission denied")

	all, _, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false, 0)
	require.NoError(t, err)
	assert.Len(t, all, 1, "the denied outsider call must not have written anything")
}

// ── get_strategy / list_strategies ────────────────────────────────────────

func TestGetStrategy_ReturnsSavedStrategy_WrongChannelRejected(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	_, v := f.viableIdea(t, cs, f.creator.ID, "Get idea")

	saved := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Gettable strategy",
		VerdictIDs:        []string{v.ID},
		IdempotencyKeyArg: uuid.NewString(),
	}))

	res := f.call(t, cs, "get_strategy", tools.GetStrategyInput{ChannelID: f.ch.ID.String(), StrategyID: saved.StrategyID})
	require.False(t, res.IsError, "unexpected error: %s", stTextOf(res))
	out := stDecode[tools.StrategyOutput](t, res)
	assert.Equal(t, saved, out)

	otherCh, err := f.st.Channels().Create(context.Background(), "yt-other-"+uuid.NewString(), "Other Channel", f.creator.ID)
	require.NoError(t, err)
	wrongChannel := f.call(t, cs, "get_strategy", tools.GetStrategyInput{ChannelID: otherCh.ID.String(), StrategyID: saved.StrategyID})
	assert.True(t, wrongChannel.IsError, "a strategy_id from a different channel_id must be rejected")
}

func TestListStrategies_ActiveOnlyFilter(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	_, v1 := f.viableIdea(t, cs, f.creator.ID, "Active idea")
	_, v2 := f.viableIdea(t, cs, f.creator.ID, "Inactive idea")

	f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Active strategy",
		VerdictIDs: []string{v1.ID}, IdempotencyKeyArg: uuid.NewString(),
	})
	inactive := false
	f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Inactive strategy",
		Active: &inactive, VerdictIDs: []string{v2.ID}, IdempotencyKeyArg: uuid.NewString(),
	})

	all := stDecode[tools.ListStrategiesOutput](t, f.call(t, cs, "list_strategies", tools.ListStrategiesInput{ChannelID: f.ch.ID.String()}))
	assert.Len(t, all.Strategies, 2)

	activeOnly := stDecode[tools.ListStrategiesOutput](t, f.call(t, cs, "list_strategies", tools.ListStrategiesInput{ChannelID: f.ch.ID.String(), ActiveOnly: true}))
	require.Len(t, activeOnly.Strategies, 1)
	assert.Equal(t, "Active strategy", activeOnly.Strategies[0].Title)
}

// TestListStrategies_LimitTruncated proves issue #1813's fix: limit caps
// the response with truncated set once a Channel has more Strategies than
// the caller-supplied (or default) limit -- before this fix, list_strategies
// had no limit at all. Strategies are a deliberately curated construct
// (issue #1813 calls it "single digits in practice"), so unlike list_ideas
// this only needs limit/truncated, not a since/before paging cursor.
func TestListStrategies_LimitTruncated(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	const seeded = 3
	for i := 0; i < seeded; i++ {
		_, v := f.viableIdea(t, cs, f.creator.ID, fmt.Sprintf("Idea %d", i))
		f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
			ChannelID: f.ch.ID.String(), Title: fmt.Sprintf("Strategy %d", i),
			VerdictIDs: []string{v.ID}, IdempotencyKeyArg: uuid.NewString(),
		})
	}

	limited := stDecode[tools.ListStrategiesOutput](t, f.call(t, cs, "list_strategies", tools.ListStrategiesInput{ChannelID: f.ch.ID.String(), Limit: 2}))
	require.Len(t, limited.Strategies, 2, "must be capped at the caller-supplied limit")
	assert.True(t, limited.Truncated, "more strategies exist beyond limit")

	all := stDecode[tools.ListStrategiesOutput](t, f.call(t, cs, "list_strategies", tools.ListStrategiesInput{ChannelID: f.ch.ID.String()}))
	require.Len(t, all.Strategies, seeded, "the default limit (50) comfortably covers a handful of strategies")
	assert.False(t, all.Truncated)
}

// ── FR47 (issue #1833): no more cadence ──────────────────────────────────
//
// A Strategy no longer carries any recurrence/pacing field. These tests
// prove: save_strategy succeeds with no cadence argument at all (every
// test above already does this implicitly; this one asserts it
// explicitly and inspects the raw wire response); a caller that supplies
// cadence anyway is rejected outright, not silently ignored (struct-
// derived input schemas set additionalProperties: false, so an unknown
// property fails schema validation before saveStrategyMutate ever runs);
// and get_strategy/list_strategies carry no cadence key in their
// responses.

func TestSaveStrategy_NoCadenceArgument_Succeeds_ResponseHasNoCadenceKey(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	_, v := f.viableIdea(t, cs, f.creator.ID, "No cadence idea")

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "No cadence strategy",
		VerdictIDs: []string{v.ID}, IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "save_strategy must succeed with no cadence argument at all: %s", stTextOf(res))

	out := stDecodeMap(t, res)
	assert.NotContains(t, out, "cadence", "save_strategy's response must carry no cadence key (FR47)")
}

func TestSaveStrategy_CadenceArgument_Rejected(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	_, v := f.viableIdea(t, cs, f.creator.ID, "Cadence-supplied idea")

	res := f.call(t, cs, "save_strategy", map[string]any{
		"channel_id":      f.ch.ID.String(),
		"title":           "Should be rejected",
		"cadence":         "weekly",
		"verdict_ids":     []string{v.ID},
		"idempotency_key": uuid.NewString(),
	})
	assert.True(t, res.IsError, "save_strategy must reject a caller-supplied cadence argument, not silently ignore it (FR47)")

	strategies, _, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false, 0)
	require.NoError(t, err)
	assert.Empty(t, strategies, "a rejected cadence argument must leave nothing written")
}

func TestGetStrategy_ResponseHasNoCadenceKey(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	_, v := f.viableIdea(t, cs, f.creator.ID, "Get no-cadence idea")

	saved := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Get no-cadence strategy",
		VerdictIDs: []string{v.ID}, IdempotencyKeyArg: uuid.NewString(),
	}))

	res := f.call(t, cs, "get_strategy", tools.GetStrategyInput{ChannelID: f.ch.ID.String(), StrategyID: saved.StrategyID})
	out := stDecodeMap(t, res)
	assert.NotContains(t, out, "cadence", "get_strategy's response must carry no cadence key (FR47)")
}

func TestListStrategies_ResponseHasNoCadenceKey(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	_, v := f.viableIdea(t, cs, f.creator.ID, "List no-cadence idea")

	f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "List no-cadence strategy",
		VerdictIDs: []string{v.ID}, IdempotencyKeyArg: uuid.NewString(),
	})

	res := f.call(t, cs, "list_strategies", tools.ListStrategiesInput{ChannelID: f.ch.ID.String()})
	out := stDecodeMap(t, res)
	strategies, ok := out["strategies"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, strategies)
	for _, s := range strategies {
		entry, ok := s.(map[string]any)
		require.True(t, ok)
		assert.NotContains(t, entry, "cadence", "list_strategies' per-Strategy response must carry no cadence key (FR47)")
	}
}

// TestGenerateSchedulePlan_NotRegistered proves generate_schedule_plan
// (retired by FR41, doubly dead under FR47 since both its only input --
// cadence + pacing_policy -- and its only output -- a schedule_entry slot
// proposal -- are gone) is not registered against this fixture's server,
// which wires RegisterStrategy the same way production does.
func TestGenerateSchedulePlan_NotRegistered(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	assert.NotContains(t, names, "generate_schedule_plan", "generate_schedule_plan must not be registered (FR41/FR47)")
}
