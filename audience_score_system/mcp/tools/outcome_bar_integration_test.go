//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README for the pattern this file follows:
// spin up a throwaway Postgres via dbtest, apply the real embedded
// migrations, host RegisterOutcomeBar's tools behind a real *mcp.Server
// over an httptest.Server, and drive them with a real in-process MCP
// client. Mirrors strategy_integration_test.go's fixture shape.
//
// outcome_bar_test.go's pure-Go suite (package tools, no build tag)
// already covers ChannelScopeID() for both input types, get_outcome_bar's
// handler (FR2 not-configured and fully-populated branches), and
// set_outcome_bar's mutate rejecting an unauthenticated/unparseable
// caller against an in-memory fake. What this file proves instead:
//   - NFR2 write authority: Creator AND Analyst can both call
//     set_outcome_bar; a Person with no open role is rejected and writes
//     nothing (the FR17-precedent assertion -- proving only Creator
//     access would not cover FR1's stated authority tier).
//   - NFR2 read authority: Creator and Analyst get byte-identical
//     get_outcome_bar output; a Person with no role is rejected.
//   - NFR1: two identical set_outcome_bar calls converge on exactly one
//     row and return identical output.
//   - set_outcome_bar's UpdatedByPersonID tracks whichever caller made
//     the call (the caller's person.ID pass-through that
//     outcome_bar_test.go cannot exercise -- see its header comment).
//   - set_outcome_bar/get_outcome_bar round trip, and a second
//     set_outcome_bar with a new threshold is reflected immediately.
//   - set_outcome_bar with an unsupported metric_name or a negative
//     threshold_value fails over the wire with a readable message and
//     leaves any previously configured bar untouched.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:outcome_bar_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
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

// outcomeBarBearerRoundTripper injects an "Authorization: Bearer <token>"
// header on every request -- this file's own copy, since each
// *_integration_test.go file is its own go_test target (see BUILD.bazel)
// and cannot share unexported helpers with a sibling file's target, per
// strategyBearerRoundTripper/verdict_integration_test.go's identical
// per-file copies.
type outcomeBarBearerRoundTripper struct{ token string }

func (rt outcomeBarBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

func obTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func obDecode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	require.False(t, res.IsError, "unexpected tool error: %s", obTextOf(res))
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// newOutcomeBarTestCredentialStore mirrors strategy_integration_test.go's
// newTestCredentialStore -- the same construction main.go does.
func newOutcomeBarTestCredentialStore(t *testing.T, pool *pgxpool.Pool) mcpauth.CredentialStore {
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

// outcomeBarFixture: a Channel with a live Creator and Analyst, plus an
// unassociated Person, hosted behind a real MCP server with
// RegisterOutcomeBar wired.
type outcomeBarFixture struct {
	st       *store.Store
	creds    mcpauth.CredentialStore
	ch       store.Channel
	creator  store.Person
	analyst  store.Person
	outsider store.Person
	url      string
}

func newOutcomeBarFixture(t *testing.T) *outcomeBarFixture {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	st := store.New(pg.Pool)
	creds := newOutcomeBarTestCredentialStore(t, pg.Pool)

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-ob-creator-"+uuid.NewString(), "ob-creator@example.com", "Creator Person")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-ob-analyst-"+uuid.NewString(), "ob-analyst@example.com", "Analyst Person")
	require.NoError(t, err)
	outsider, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-ob-outsider-"+uuid.NewString(), "ob-outsider@example.com", "Outsider Person")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-ob-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterOutcomeBar(reg, st.OutcomeBars())

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &outcomeBarFixture{st: st, creds: creds, ch: ch, creator: creator, analyst: analyst, outsider: outsider, url: ts.URL}
}

func (f *outcomeBarFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: outcomeBarBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (f *outcomeBarFixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// ── NFR2 write authority ─────────────────────────────────────────────────

func TestSetOutcomeBar_CreatorAndAnalystCanWrite_OutsiderDeniedWritesNothing(t *testing.T) {
	f := newOutcomeBarFixture(t)

	creatorCS := f.connect(t, f.creator.ID)
	res := f.call(t, creatorCS, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000,
	})
	require.False(t, res.IsError, "Creator must be able to set the outcome bar: %s", obTextOf(res))

	analystCS := f.connect(t, f.analyst.ID)
	res = f.call(t, analystCS, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 2000,
	})
	require.False(t, res.IsError, "Analyst must be able to set the outcome bar too (FR17-precedent tier, not Creator-only): %s", obTextOf(res))

	before, err := f.st.OutcomeBars().GetByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)

	outsiderCS := f.connect(t, f.outsider.ID)
	denied := f.call(t, outsiderCS, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 9999,
	})
	assert.True(t, denied.IsError, "a Person with no open role on the Channel must be denied")

	after, err := f.st.OutcomeBars().GetByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the denied outsider call must not have written anything")
}

// ── NFR2 read authority ──────────────────────────────────────────────────

func TestGetOutcomeBar_CreatorAndAnalystByteIdentical_OutsiderDenied(t *testing.T) {
	f := newOutcomeBarFixture(t)
	creatorCS := f.connect(t, f.creator.ID)
	f.call(t, creatorCS, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1500,
	})

	creatorOut := obDecode[tools.OutcomeBarOutput](t, f.call(t, creatorCS, "get_outcome_bar", tools.GetOutcomeBarInput{ChannelID: f.ch.ID.String()}))

	analystCS := f.connect(t, f.analyst.ID)
	analystOut := obDecode[tools.OutcomeBarOutput](t, f.call(t, analystCS, "get_outcome_bar", tools.GetOutcomeBarInput{ChannelID: f.ch.ID.String()}))
	assert.Equal(t, creatorOut, analystOut, "Creator and Analyst must see byte-identical output")

	outsiderCS := f.connect(t, f.outsider.ID)
	denied := f.call(t, outsiderCS, "get_outcome_bar", tools.GetOutcomeBarInput{ChannelID: f.ch.ID.String()})
	assert.True(t, denied.IsError, "a Person with no role must be denied")
}

// ── NFR1: natural-key upsert convergence ─────────────────────────────────

func TestSetOutcomeBar_IdenticalCallsConverge_OneRowIdenticalOutput(t *testing.T) {
	f := newOutcomeBarFixture(t)
	cs := f.connect(t, f.creator.ID)

	args := tools.SetOutcomeBarInput{ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 500}
	first := obDecode[tools.OutcomeBarOutput](t, f.call(t, cs, "set_outcome_bar", args))
	second := obDecode[tools.OutcomeBarOutput](t, f.call(t, cs, "set_outcome_bar", args))
	assert.Equal(t, first, second, "two identical calls must return identical output (NFR1)")

	_, err := f.st.OutcomeBars().GetByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err, "exactly one row must exist")
}

// ── UpdatedByPersonID tracks the calling Person ──────────────────────────

// TestSetOutcomeBar_UpdatedByPersonID_TracksCaller proves the
// person.ID -> UpdatedByPersonID pass-through outcome_bar_test.go's pure
// unit suite cannot exercise (see its header comment): whichever real,
// authenticated Person makes the call, that Person's id is what
// get_outcome_bar reads back.
func TestSetOutcomeBar_UpdatedByPersonID_TracksCaller(t *testing.T) {
	f := newOutcomeBarFixture(t)

	creatorCS := f.connect(t, f.creator.ID)
	out := obDecode[tools.OutcomeBarOutput](t, f.call(t, creatorCS, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 100,
	}))
	assert.Equal(t, f.creator.ID.String(), out.UpdatedByPersonID)

	analystCS := f.connect(t, f.analyst.ID)
	out = obDecode[tools.OutcomeBarOutput](t, f.call(t, analystCS, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 200,
	}))
	assert.Equal(t, f.analyst.ID.String(), out.UpdatedByPersonID, "the most recent caller's id must be reflected")
}

// ── round trip ────────────────────────────────────────────────────────────

func TestOutcomeBar_RoundTrip_SetThenGet_UpdateReflectedImmediately(t *testing.T) {
	f := newOutcomeBarFixture(t)
	cs := f.connect(t, f.creator.ID)

	set := obDecode[tools.OutcomeBarOutput](t, f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 750,
	}))
	got := obDecode[tools.OutcomeBarOutput](t, f.call(t, cs, "get_outcome_bar", tools.GetOutcomeBarInput{ChannelID: f.ch.ID.String()}))
	assert.Equal(t, set, got, "get_outcome_bar must return exactly what was just set")
	require.NotNil(t, got.ThresholdValue)
	assert.Equal(t, 750.0, *got.ThresholdValue)

	updated := obDecode[tools.OutcomeBarOutput](t, f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 900,
	}))
	require.NotNil(t, updated.ThresholdValue)
	assert.Equal(t, 900.0, *updated.ThresholdValue)

	gotAgain := obDecode[tools.OutcomeBarOutput](t, f.call(t, cs, "get_outcome_bar", tools.GetOutcomeBarInput{ChannelID: f.ch.ID.String()}))
	assert.Equal(t, updated, gotAgain, "the new threshold must be reflected immediately")
}

func TestGetOutcomeBar_NeverConfigured_ConfiguredFalseOverRealMCPCall(t *testing.T) {
	f := newOutcomeBarFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "get_outcome_bar", tools.GetOutcomeBarInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "FR2: not-configured is a successful response, not an error: %s", obTextOf(res))
	out := obDecode[tools.OutcomeBarOutput](t, res)
	assert.Equal(t, tools.OutcomeBarOutput{Configured: false}, out)
}

// ── invalid metric_name / threshold_value rejected over the wire ────────

// TestSetOutcomeBar_InvalidMetricAndThreshold_RejectedWithReadableMessage
// proves store.ErrUnsupportedOutcomeBarMetric and
// store.ErrInvalidOutcomeBarThreshold are mapped to a readable
// caller-facing message rather than surfacing as a raw pgx error --
// outcome_bar_test.go's pure unit suite cannot reach this mapping code
// (see its header comment: it lives behind bars.Upsert, itself only
// reached once a real Person is on ctx).
func TestSetOutcomeBar_InvalidMetricAndThreshold_RejectedWithReadableMessage(t *testing.T) {
	f := newOutcomeBarFixture(t)
	cs := f.connect(t, f.creator.ID)

	configured := obDecode[tools.OutcomeBarOutput](t, f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 42,
	}))

	badMetric := f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: "ctr", ThresholdValue: 42,
	})
	require.True(t, badMetric.IsError, "an unsupported metric_name must be rejected")
	assert.Contains(t, obTextOf(badMetric), "views", "the error must name the accepted value")

	badThreshold := f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: -1,
	})
	require.True(t, badThreshold.IsError, "a negative threshold_value must be rejected")

	stillConfigured := obDecode[tools.OutcomeBarOutput](t, f.call(t, cs, "get_outcome_bar", tools.GetOutcomeBarInput{ChannelID: f.ch.ID.String()}))
	assert.Equal(t, configured, stillConfigured, "neither rejected call must have changed the previously configured bar")
}
