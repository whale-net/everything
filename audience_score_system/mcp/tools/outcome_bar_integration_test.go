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
// handler (FR2 not-configured and fully-populated branches),
// set_outcome_bar's mutate rejecting an unauthenticated/unparseable
// caller against an in-memory fake, and get_calibration_trend's handler
// against fakes for both stores. What this file proves instead:
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
//   - get_calibration_trend (FR5/FR6/FR7, issue #1885), over a real MCP
//     call with real Postgres-backed candidates: the full seed ->
//     set_outcome_bar -> get_calibration_trend path; FR4 reclassification
//     when the bar changes; NFR2 byte-identical Creator/Analyst output and
//     outsider rejection; FR6's not-configured result on a Channel with
//     candidates but no bar; FR7 truncation with most-recent-first
//     selection kept chronological; and multi-tenant isolation.
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
	tools.RegisterOutcomeBar(reg, st.OutcomeBars(), st.Calibration())

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

// ── get_calibration_trend (FR5/FR6/FR7, issue #1885) ─────────────────────

// obMonthStart mirrors calibration_integration_test.go's monthStart
// (store package) -- separate go_test target/compilation unit, so this
// file needs its own copy, using the same "ob" prefix convention as
// obTextOf/obDecode above.
func obMonthStart(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

// calibrationCandidate builds a full FR3 calibration candidate on f.ch: a
// viable-verdict-bound greenlit video_script, a confirmed match to a
// synced video published at publishedAt, and a video_metrics snapshot
// carrying views. Mirrors calibration_integration_test.go's helper of the
// same name/shape (store package) built here against f.st directly --
// ListSchedule locates the freshly-synced video by its unique YouTube id
// rather than a raw SQL lookup, since outcomeBarFixture keeps no
// *dbtest.Postgres handle of its own.
func (f *outcomeBarFixture) calibrationCandidate(t *testing.T, ctx context.Context, title string, publishedAt time.Time, views *int64) store.VideoScript {
	t.Helper()

	idea, err := f.st.Ideas().Create(ctx, f.ch.ID, title, f.creator.ID)
	require.NoError(t, err)
	v, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: title + " looks strong", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	strat, err := f.st.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: f.ch.ID, Title: title + " Strategy", Active: true,
		VerdictIDs: []uuid.UUID{v.ID}, CreatedByPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	script, err := f.st.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: f.ch.ID, VerdictID: v.ID, StrategyID: strat.ID,
		Title: title, ScriptText: "script text for " + title, CreatedByPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, f.st.VideoScripts().Greenlight(ctx, script.ID, f.creator.ID))

	ytID := "yt-ob-cal-" + uuid.NewString()
	require.NoError(t, f.st.Sync().UpsertVideos(ctx, f.ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	synced, _, err := f.st.Sync().ListSchedule(ctx, f.ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	var video store.SyncedVideo
	for _, sv := range synced {
		if sv.YouTubeVideoID == ytID {
			video = sv
			break
		}
	}
	require.NotEqual(t, uuid.Nil, video.ID, "the just-synced video must be found by its unique YouTube id")

	require.NoError(t, f.st.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &script.ID, Confidence: 0.9, State: store.MatchStateConfirmed,
	}))
	require.NoError(t, f.st.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: views, MeasuredAt: time.Now(),
	}}))

	got, err := f.st.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	return got
}

// TestGetCalibrationTrend_FullPath_OneBucketWithMixedCalibration is test
// 5: seed two calibration candidates in the same calendar month, one
// above and one below the bar, set the bar, then call
// get_calibration_trend over the wire and assert the single resulting
// bucket's counts and rate.
func TestGetCalibrationTrend_FullPath_OneBucketWithMixedCalibration(t *testing.T) {
	f := newOutcomeBarFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	f.calibrationCandidate(t, ctx, "Above Bar", obMonthStart(2024, time.May, 5), ptrInt64OB(2000))
	f.calibrationCandidate(t, ctx, "Below Bar", obMonthStart(2024, time.May, 15), ptrInt64OB(500))

	setRes := f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000,
	})
	require.False(t, setRes.IsError, "set_outcome_bar must succeed: %s", obTextOf(setRes))

	res := f.call(t, cs, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "get_calibration_trend must succeed: %s", obTextOf(res))
	out := obDecode[tools.GetCalibrationTrendOutput](t, res)

	assert.True(t, out.OutcomeBar.Configured)
	assert.False(t, out.Truncated)
	require.Len(t, out.Buckets, 1, "both candidates published in May 2024 must collapse into one bucket")
	b := out.Buckets[0]
	assert.Equal(t, 2, b.Candidates)
	assert.Equal(t, 1, b.Calibrated)
	assert.Equal(t, 1, b.Miscalibrated)
	assert.InDelta(t, 0.5, b.CalibrationRate, 1e-9)
}

// ptrInt64OB avoids colliding with ptrInt64B (browse_integration_test.go)
// or ptrInt64 (store package) -- each *_integration_test.go file is its
// own go_test target/compilation unit, but this repo's convention keeps
// per-file helper names distinct anyway for grep-ability.
func ptrInt64OB(v int64) *int64 { return &v }

// TestGetCalibrationTrend_ChangingBarReclassifiesSameData is test 6
// (FR4): re-calling set_outcome_bar with a different threshold and then
// re-calling get_calibration_trend reclassifies the identical underlying
// candidates -- proving FR4's "no historical snapshot" over the wire, not
// just at store level (already covered by
// TestCalibrationStore_MonthlyTrend_FR4_NoStoredHistory_ReclassifiesOnRerunWithDifferentBar
// in calibration_integration_test.go).
func TestGetCalibrationTrend_ChangingBarReclassifiesSameData(t *testing.T) {
	f := newOutcomeBarFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	f.calibrationCandidate(t, ctx, "Reclassified", obMonthStart(2024, time.August, 1), ptrInt64OB(1500))

	f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000,
	})
	low := obDecode[tools.GetCalibrationTrendOutput](t, f.call(t, cs, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f.ch.ID.String()}))
	require.Len(t, low.Buckets, 1)
	assert.Equal(t, 1, low.Buckets[0].Calibrated, "1500 views clears a 1000 threshold")

	f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 2000,
	})
	high := obDecode[tools.GetCalibrationTrendOutput](t, f.call(t, cs, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f.ch.ID.String()}))
	require.Len(t, high.Buckets, 1)
	assert.Equal(t, 0, high.Buckets[0].Calibrated, "the same seeded data must reclassify as miscalibrated against a higher threshold")
	assert.Equal(t, 1, high.Buckets[0].Candidates, "the candidate count itself does not change with the bar")
	assert.Equal(t, 2000.0, *high.OutcomeBar.ThresholdValue, "outcome_bar must echo the CURRENT bar")
}

// TestGetCalibrationTrend_CreatorAndAnalystByteIdentical_OutsiderDenied is
// test 7 (NFR2): Creator and Analyst get byte-identical output; a Person
// with no open role on the Channel is rejected.
func TestGetCalibrationTrend_CreatorAndAnalystByteIdentical_OutsiderDenied(t *testing.T) {
	f := newOutcomeBarFixture(t)
	ctx := context.Background()
	creatorCS := f.connect(t, f.creator.ID)

	f.calibrationCandidate(t, ctx, "NFR2 Candidate", obMonthStart(2024, time.September, 1), ptrInt64OB(1000))
	f.call(t, creatorCS, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 500,
	})

	creatorOut := obDecode[tools.GetCalibrationTrendOutput](t, f.call(t, creatorCS, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f.ch.ID.String()}))

	analystCS := f.connect(t, f.analyst.ID)
	analystOut := obDecode[tools.GetCalibrationTrendOutput](t, f.call(t, analystCS, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f.ch.ID.String()}))
	assert.Equal(t, creatorOut, analystOut, "Creator and Analyst must see byte-identical output")

	outsiderCS := f.connect(t, f.outsider.ID)
	denied := f.call(t, outsiderCS, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f.ch.ID.String()})
	assert.True(t, denied.IsError, "a Person with no role must be denied")
}

// TestGetCalibrationTrend_NoBarConfigured_NotConfiguredOverRealMCPCall is
// test 8 (FR6): a Channel with calibration candidates but no outcome bar
// ever set returns configured:false, no buckets, and succeeds -- never an
// error.
func TestGetCalibrationTrend_NoBarConfigured_NotConfiguredOverRealMCPCall(t *testing.T) {
	f := newOutcomeBarFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	f.calibrationCandidate(t, ctx, "No Bar Yet", obMonthStart(2024, time.October, 1), ptrInt64OB(1000))

	res := f.call(t, cs, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "FR6: not-configured is a successful response, not an error: %s", obTextOf(res))
	out := obDecode[tools.GetCalibrationTrendOutput](t, res)
	assert.Equal(t, tools.OutcomeBarOutput{Configured: false}, out.OutcomeBar)
	assert.Empty(t, out.Buckets)
	assert.False(t, out.Truncated)
}

// TestGetCalibrationTrend_FR7_LimitTruncatesToMostRecentButStaysChronological
// is test 9: seed candidates across several months, call with a limit
// below that count, and assert truncated:true with the most-recent
// buckets returned, still chronologically ordered among themselves.
func TestGetCalibrationTrend_FR7_LimitTruncatesToMostRecentButStaysChronological(t *testing.T) {
	f := newOutcomeBarFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	f.call(t, cs, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000,
	})

	months := []time.Month{time.January, time.February, time.March, time.April, time.May}
	for _, m := range months {
		f.calibrationCandidate(t, ctx, m.String(), obMonthStart(2024, m, 10), ptrInt64OB(1000))
	}

	res := f.call(t, cs, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f.ch.ID.String(), Limit: 2})
	require.False(t, res.IsError, "get_calibration_trend must succeed: %s", obTextOf(res))
	out := obDecode[tools.GetCalibrationTrendOutput](t, res)

	assert.True(t, out.Truncated, "5 months exist, limit 2 must report truncation")
	require.Len(t, out.Buckets, 2)
	april := time.Date(2024, time.April, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	may := time.Date(2024, time.May, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	assert.Equal(t, april, out.Buckets[0].BucketStart, "the two MOST RECENT months, kept chronological between themselves")
	assert.Equal(t, may, out.Buckets[1].BucketStart)
}

// TestGetCalibrationTrend_MultiTenantIsolation is test 10: another
// Channel's calibration candidates must never appear in this Channel's
// trend.
func TestGetCalibrationTrend_MultiTenantIsolation(t *testing.T) {
	f1 := newOutcomeBarFixture(t)
	f2 := newOutcomeBarFixture(t)
	ctx := context.Background()

	f1.calibrationCandidate(t, ctx, "Channel 1 Candidate", obMonthStart(2024, time.November, 1), ptrInt64OB(1000))
	f2.calibrationCandidate(t, ctx, "Channel 2 Candidate", obMonthStart(2024, time.November, 1), ptrInt64OB(1000))

	cs1 := f1.connect(t, f1.creator.ID)
	f1.call(t, cs1, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f1.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 500,
	})
	out1 := obDecode[tools.GetCalibrationTrendOutput](t, f1.call(t, cs1, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f1.ch.ID.String()}))
	require.Len(t, out1.Buckets, 1)
	assert.Equal(t, 1, out1.Buckets[0].Candidates, "ch1's bucket must not include ch2's candidate")

	cs2 := f2.connect(t, f2.creator.ID)
	f2.call(t, cs2, "set_outcome_bar", tools.SetOutcomeBarInput{
		ChannelID: f2.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 500,
	})
	out2 := obDecode[tools.GetCalibrationTrendOutput](t, f2.call(t, cs2, "get_calibration_trend", tools.GetCalibrationTrendInput{ChannelID: f2.ch.ID.String()}))
	require.Len(t, out2.Buckets, 1)
	assert.Equal(t, 1, out2.Buckets[0].Candidates)
}
