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
// covers parseCadence, advanceCadence, and rollToWeekday in isolation.
// What this file proves instead: save_strategy's FR16-style viable-verdict
// gate (rejecting an idea_id whose current verdict is not viable, nothing
// written), create-vs-update (strategy_id) semantics including wholesale
// idea-link replacement, idempotent replay convergence, Channel-scoping
// (Creator+Analyst write, unassociated Person denied), and
// generate_schedule_plan's read-only cadence-driven proposal derivation
// (including skipping an Idea whose verdict has since gone non-viable) --
// all against the real embedded schema and a real HTTP-hosted MCP server,
// mirroring schedule_draft_integration_test.go's pattern (issue #1637).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:strategy_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
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

// strategyFixture mirrors scheduleDraftFixture: a Channel with a live
// Creator and Analyst, an unassociated Person, and a viable-verdict Idea
// ready to build a Strategy from, hosted behind a real MCP server with
// RegisterVerdict + RegisterScheduleDraft + RegisterStrategy wired.
type strategyFixture struct {
	st       *store.Store
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

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-strat-creator-"+uuid.NewString(), "strat-creator@example.com", "Creator Person")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-strat-analyst-"+uuid.NewString(), "strat-analyst@example.com", "Analyst Person")
	require.NoError(t, err)
	outsider, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-strat-outsider-"+uuid.NewString(), "strat-outsider@example.com", "Outsider Person")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-strat-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst))

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterVerdict(reg, st)
	tools.RegisterScheduleDraft(reg, st)
	tools.RegisterStrategy(reg, st)

	handler := server.NewHTTPHandler(srv, st.Credentials())
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &strategyFixture{st: st, ch: ch, creator: creator, analyst: analyst, outsider: outsider, url: ts.URL}
}

func (f *strategyFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	token, _, err := f.st.Credentials().Mint(ctx, personID)
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

func TestSaveStrategy_ViableIdeas_CreatesWithLinkedIdeasAndVerdictVersions(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	idea1, v1 := f.viableIdea(t, cs, f.creator.ID, "Weekly clip idea")
	idea2, v2 := f.viableIdea(t, cs, f.creator.ID, "Second weekly idea")

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Short themed clips",
		Cadence:           "weekly",
		PreferredWeekday:  "Monday",
		IdeaIDs:           []string{idea1.ID.String(), idea2.ID.String()},
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "unexpected error: %s", stTextOf(res))
	out := stDecode[tools.StrategyOutput](t, res)

	assert.Equal(t, f.ch.ID.String(), out.ChannelID)
	assert.Equal(t, "weekly", out.Cadence)
	assert.Equal(t, "Monday", out.PreferredWeekday)
	assert.True(t, out.Active, "active must default to true when omitted")
	require.Len(t, out.Ideas, 2)

	byID := map[string]tools.StrategyIdeaOutput{}
	for _, i := range out.Ideas {
		byID[i.IdeaID] = i
	}
	require.Contains(t, byID, idea1.ID.String())
	assert.Equal(t, v1.ID, byID[idea1.ID.String()].VerdictID)
	require.Contains(t, byID, idea2.ID.String())
	assert.Equal(t, v2.ID, byID[idea2.ID.String()].VerdictID)
}

func TestSaveStrategy_NonViableIdea_RejectedNoRowWritten(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Not viable idea "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)
	f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            idea.ID.String(),
		Verdict:           "not-viable",
		Reasoning:         "too niche",
		IdempotencyKeyArg: uuid.NewString(),
	})

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Should fail",
		Cadence:           "monthly",
		IdeaIDs:           []string{idea.ID.String()},
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "a not-viable idea must be rejected")

	strategies, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false)
	require.NoError(t, err)
	assert.Empty(t, strategies, "no strategy row must exist after a rejected save")
}

func TestSaveStrategy_IdeaWithNoVerdict_Rejected(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "No verdict idea "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Should fail",
		Cadence:           "weekly",
		IdeaIDs:           []string{idea.ID.String()},
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "an idea with no verdict at all must be rejected")
}

// ── save_strategy: create vs. update ──────────────────────────────────────

func TestSaveStrategy_StrategyIDSupplied_UpdatesAndReplacesLinkedIdeasWholesale(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)

	idea1, _ := f.viableIdea(t, cs, f.creator.ID, "Original idea")
	created := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Original title",
		Cadence:           "weekly",
		IdeaIDs:           []string{idea1.ID.String()},
		IdempotencyKeyArg: uuid.NewString(),
	}))

	idea2, _ := f.viableIdea(t, cs, f.creator.ID, "Replacement idea")
	updated := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		StrategyID:        created.StrategyID,
		Title:             "Updated title",
		Cadence:           "monthly",
		IdeaIDs:           []string{idea2.ID.String()},
		IdempotencyKeyArg: uuid.NewString(),
	}))

	assert.Equal(t, created.StrategyID, updated.StrategyID, "supplying strategy_id must update the same row, not create a new one")
	assert.Equal(t, "Updated title", updated.Title)
	assert.Equal(t, "monthly", updated.Cadence)
	require.Len(t, updated.Ideas, 1, "the linked idea set must be replaced wholesale, not merged")
	assert.Equal(t, idea2.ID.String(), updated.Ideas[0].IdeaID)

	all, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false)
	require.NoError(t, err)
	assert.Len(t, all, 1, "an update must not create a second strategy row")
}

func TestSaveStrategy_UnknownStrategyID_Rejected(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	idea, _ := f.viableIdea(t, cs, f.creator.ID, "Idea")

	res := f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		StrategyID:        uuid.NewString(),
		Title:             "Should fail",
		Cadence:           "weekly",
		IdeaIDs:           []string{idea.ID.String()},
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError)
}

// ── save_strategy: idempotent replay ──────────────────────────────────────

func TestSaveStrategy_IdempotentReplay_ConvergesOnSameRow(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	idea, _ := f.viableIdea(t, cs, f.creator.ID, "Replay idea")

	args := tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Replay strategy",
		Cadence:           "weekly",
		IdeaIDs:           []string{idea.ID.String()},
		IdempotencyKeyArg: "replay-key-strategy",
	}
	first := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", args))
	second := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", args))
	assert.Equal(t, first.StrategyID, second.StrategyID)

	all, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false)
	require.NoError(t, err)
	assert.Len(t, all, 1, "a replay must never duplicate the strategy row")
}

// ── save_strategy: Analyst can write, unassociated Person cannot ─────────

func TestSaveStrategy_AnalystCanSave_UnassociatedPersonDeniedWritesNothing(t *testing.T) {
	f := newStrategyFixture(t)
	analystCS := f.connect(t, f.analyst.ID)
	idea, _ := f.viableIdea(t, analystCS, f.analyst.ID, "Analyst idea")

	res := f.call(t, analystCS, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Analyst strategy",
		Cadence:           "weekly",
		IdeaIDs:           []string{idea.ID.String()},
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "unexpected error: %s", stTextOf(res))

	outsiderCS := f.connect(t, f.outsider.ID)
	denied := f.call(t, outsiderCS, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Should be denied",
		Cadence:           "weekly",
		IdeaIDs:           []string{idea.ID.String()},
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, denied.IsError)
	assert.Contains(t, stTextOf(denied), "permission denied")

	all, err := f.st.Strategies().ListByChannel(context.Background(), f.ch.ID, false)
	require.NoError(t, err)
	assert.Len(t, all, 1, "the denied outsider call must not have written anything")
}

// ── get_strategy / list_strategies ────────────────────────────────────────

func TestGetStrategy_ReturnsSavedStrategy_WrongChannelRejected(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	idea, _ := f.viableIdea(t, cs, f.creator.ID, "Get idea")

	saved := stDecode[tools.StrategyOutput](t, f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID:         f.ch.ID.String(),
		Title:             "Gettable strategy",
		Cadence:           "biweekly",
		IdeaIDs:           []string{idea.ID.String()},
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

	idea1, _ := f.viableIdea(t, cs, f.creator.ID, "Active idea")
	idea2, _ := f.viableIdea(t, cs, f.creator.ID, "Inactive idea")

	f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Active strategy", Cadence: "weekly",
		IdeaIDs: []string{idea1.ID.String()}, IdempotencyKeyArg: uuid.NewString(),
	})
	inactive := false
	f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Inactive strategy", Cadence: "monthly",
		Active: &inactive, IdeaIDs: []string{idea2.ID.String()}, IdempotencyKeyArg: uuid.NewString(),
	})

	all := stDecode[tools.ListStrategiesOutput](t, f.call(t, cs, "list_strategies", tools.ListStrategiesInput{ChannelID: f.ch.ID.String()}))
	assert.Len(t, all.Strategies, 2)

	activeOnly := stDecode[tools.ListStrategiesOutput](t, f.call(t, cs, "list_strategies", tools.ListStrategiesInput{ChannelID: f.ch.ID.String(), ActiveOnly: true}))
	require.Len(t, activeOnly.Strategies, 1)
	assert.Equal(t, "Active strategy", activeOnly.Strategies[0].Title)
}

// ── generate_schedule_plan ────────────────────────────────────────────────

func TestGenerateSchedulePlan_ProposesCadenceSlotsWithoutWriting(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	idea, verdict := f.viableIdea(t, cs, f.creator.ID, "Plan idea")

	f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Weekly plan strategy", Cadence: "weekly",
		IdeaIDs: []string{idea.ID.String()}, IdempotencyKeyArg: uuid.NewString(),
	})

	res := f.call(t, cs, "generate_schedule_plan", tools.GenerateSchedulePlanInput{ChannelID: f.ch.ID.String(), CountPerIdea: 3})
	require.False(t, res.IsError, "unexpected error: %s", stTextOf(res))
	out := stDecode[tools.GenerateSchedulePlanOutput](t, res)

	require.Len(t, out.Proposals, 3)
	assert.Empty(t, out.Skipped)
	for i, p := range out.Proposals {
		assert.Equal(t, idea.ID.String(), p.IdeaID)
		assert.Equal(t, verdict.ID, p.VerdictID)
		assert.Equal(t, "weekly", p.Cadence)
		assert.Equal(t, i+1, p.SequenceIndex)
	}
	first, err := time.Parse(time.RFC3339, out.Proposals[0].ProposedPublishAt)
	require.NoError(t, err)
	second, err := time.Parse(time.RFC3339, out.Proposals[1].ProposedPublishAt)
	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, second.Sub(first), "consecutive weekly proposals must be exactly 7 days apart")

	entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	assert.Empty(t, entries, "generate_schedule_plan must not write any schedule_entry rows")
}

func TestGenerateSchedulePlan_SkipsIdeaWhoseVerdictIsNoLongerViable(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	idea, _ := f.viableIdea(t, cs, f.creator.ID, "Now not viable idea")

	f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Strategy about to be undermined", Cadence: "weekly",
		IdeaIDs: []string{idea.ID.String()}, IdempotencyKeyArg: uuid.NewString(),
	})

	// A newer verdict version flips the idea to not-viable after the
	// Strategy was saved -- generate_schedule_plan re-checks the current
	// verdict on every call rather than trusting the pinned link.
	f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            idea.ID.String(),
		Verdict:           "not-viable",
		Reasoning:         "changed our mind",
		IdempotencyKeyArg: uuid.NewString(),
	})

	res := f.call(t, cs, "generate_schedule_plan", tools.GenerateSchedulePlanInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", stTextOf(res))
	out := stDecode[tools.GenerateSchedulePlanOutput](t, res)

	assert.Empty(t, out.Proposals)
	require.Len(t, out.Skipped, 1)
	assert.Equal(t, idea.ID.String(), out.Skipped[0].IdeaID)
}

func TestGenerateSchedulePlan_IgnoresInactiveStrategies(t *testing.T) {
	f := newStrategyFixture(t)
	cs := f.connect(t, f.creator.ID)
	idea, _ := f.viableIdea(t, cs, f.creator.ID, "Inactive plan idea")

	inactive := false
	f.call(t, cs, "save_strategy", tools.SaveStrategyInput{
		ChannelID: f.ch.ID.String(), Title: "Inactive strategy", Cadence: "weekly",
		Active: &inactive, IdeaIDs: []string{idea.ID.String()}, IdempotencyKeyArg: uuid.NewString(),
	})

	res := f.call(t, cs, "generate_schedule_plan", tools.GenerateSchedulePlanInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", stTextOf(res))
	out := stDecode[tools.GenerateSchedulePlanOutput](t, res)
	assert.Empty(t, out.Proposals)
	assert.Empty(t, out.Skipped, "an inactive strategy's ideas are simply not considered -- not reported as skipped")
}
