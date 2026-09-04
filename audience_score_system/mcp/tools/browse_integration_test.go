//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. Mirrors matches_integration_test.go's pattern: spin up a throwaway
// Postgres via dbtest, apply the real embedded migrations, host
// RegisterBrowse's tools (browse.go, issue #1582, FR24) behind a real
// *mcp.Server over HTTP, and drive them with a real in-process MCP client.
//
// The load-bearing assertion in this file is
// TestGetPredictionVsOutcome_BoundVerdictSurvivesNewerVerdictVersion: it
// proves store.BrowseStore.PredictionVsOutcome's documented deliberate
// deviation from migration 002's v_prediction_vs_outcome view (see
// ../../store/browse.go's PredictionVsOutcome doc) -- that a schedule_entry
// committed under an older viability_verdict version keeps showing that
// bound version in the comparison read even after a newer version is
// appended for the same Idea, rather than silently dropping the row or
// re-deriving via idea_id the way the view does.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:browse_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// newBrowseTestDB provisions an isolated Postgres database via dbtest and
// applies every migration in the package's own embedded schema -- mirrors
// matches_integration_test.go's newMatchesTestDB.
func newBrowseTestDB(t *testing.T) *dbtest.Postgres {
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

// browseFixture is the common setup every test below needs: a Channel
// with a live Creator and Analyst, an unassociated Person with no role on
// it, and RegisterBrowse's tools hosted behind a real *mcp.Server over
// HTTP. Individual tests seed whatever Ideas/verdicts/schedule/sync/match
// data their assertion needs on top of this.
type browseFixture struct {
	pg       *dbtest.Postgres
	st       *store.Store
	creds    mcpauth.CredentialStore
	ch       store.Channel
	creator  store.Person
	analyst  store.Person
	outsider store.Person
	url      string
}

func newBrowseFixture(t *testing.T) *browseFixture {
	t.Helper()
	ctx := context.Background()

	pg := newBrowseTestDB(t)
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
	tools.RegisterBrowse(reg, st)

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &browseFixture{pg: pg, st: st, creds: creds, ch: ch, creator: creator, analyst: analyst, outsider: outsider, url: ts.URL}
}

// bBearerRoundTripper injects an "Authorization: Bearer <token>" header --
// mirrors matches_integration_test.go's mBearerRoundTripper (separate
// go_test target/compilation unit, so it cannot reuse that file's copy).
type bBearerRoundTripper struct{ token string }

func (rt bBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

func (f *browseFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: bBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (f *browseFixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// mtextOf and mdecode mirror matches_integration_test.go's copies --
// separate go_test target/compilation unit, so this file cannot reuse
// that file's copy.
func mtextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func mdecode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	require.False(t, res.IsError, "unexpected tool error: %s", mtextOf(res))
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// fullChainFixture is a fully-populated Channel: one Idea with two
// viability_verdict versions, a committed schedule_entry bound to the
// OLDER version, a published SyncedVideo, one VideoMetrics snapshot, and
// an "auto" video_schedule_match linking them -- the chain both
// get_channel_overview and get_prediction_vs_outcome exist to render.
type fullChainFixture struct {
	*browseFixture
	idea        store.Idea
	verdictV1   store.Verdict
	verdictV2   store.Verdict
	entry       store.ScheduleEntry
	video       store.SyncedVideo
	publishedAt time.Time
}

func newFullChainFixture(t *testing.T) *fullChainFixture {
	t.Helper()
	f := newBrowseFixture(t)
	ctx := context.Background()

	idea, err := f.st.Ideas().Create(ctx, f.ch.ID, "Full Chain Idea", f.creator.ID)
	require.NoError(t, err)

	v1, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "v1 reasoning", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)

	entry, err := f.st.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: f.ch.ID, IdeaID: idea.ID, VerdictID: v1.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, f.st.Schedules().Approve(ctx, entry.ID, f.creator.ID))

	// A SECOND, newer verdict version is appended AFTER the schedule entry
	// was committed against v1 -- the concrete regression scenario for
	// BrowseStore.PredictionVsOutcome's documented "bound version, not
	// current" contract.
	v2, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "v2 reasoning, recorded after commit", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 2, v2.Version, "the newer verdict must be version 2")
	require.Equal(t, 1, v1.Version, "the entry was committed against version 1")

	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, f.st.Sync().UpsertVideos(ctx, f.ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-" + uuid.NewString(), Title: "Full Chain Video",
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	synced, err := f.st.Sync().ListSchedule(ctx, f.ch.ID)
	require.NoError(t, err)
	require.Len(t, synced, 1)
	video := synced[0]

	require.NoError(t, f.st.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: ptrInt64B(500), MeasuredAt: time.Now(),
	}}))

	require.NoError(t, f.st.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, ScheduleEntryID: &entry.ID, Confidence: 0.91, State: store.MatchStateAuto,
	}))

	return &fullChainFixture{
		browseFixture: f, idea: idea, verdictV1: v1, verdictV2: v2, entry: entry, video: video, publishedAt: publishedAt,
	}
}

func ptrInt64B(v int64) *int64 { return &v }

// ── get_prediction_vs_outcome: the bound-verdict regression test ──────────

// TestGetPredictionVsOutcome_BoundVerdictSurvivesNewerVerdictVersion is the
// concrete regression test for store.BrowseStore.PredictionVsOutcome's
// documented deviation from v_prediction_vs_outcome: appending a newer
// verdict version for an Idea AFTER its schedule_entry was committed under
// an older version must not change, drop, or otherwise disturb the
// comparison row -- it must keep reporting the OLDER, bound version.
func TestGetPredictionVsOutcome_BoundVerdictSurvivesNewerVerdictVersion(t *testing.T) {
	f := newFullChainFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()})
	out := mdecode[tools.GetPredictionVsOutcomeOutput](t, res)
	require.Len(t, out.Rows, 1)

	row := out.Rows[0]
	assert.Equal(t, f.idea.ID.String(), row.IdeaID)

	assert.Equal(t, f.verdictV1.ID.String(), row.Verdict.VerdictID, "must report the verdict bound to the schedule_entry (v1), not the idea's current verdict (v2)")
	assert.Equal(t, 1, row.Verdict.Version, "version must be the bound version (1), not the current one (2)")
	assert.Equal(t, "v1 reasoning", row.Verdict.Reasoning)
	assert.NotEqual(t, f.verdictV2.ID.String(), row.Verdict.VerdictID, "must NOT be the newer, current verdict version")
}

// TestGetPredictionVsOutcome_BoundVerdictSurvives_EvenWithManyLaterVersions
// extends the above by appending several more versions -- proving the
// binding is stable under an arbitrary number of subsequent appends, not
// just the very next one.
func TestGetPredictionVsOutcome_BoundVerdictSurvives_EvenWithManyLaterVersions(t *testing.T) {
	f := newFullChainFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	for i := 0; i < 5; i++ {
		_, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
			IdeaID: f.idea.ID, Verdict: store.VerdictViable,
			Reasoning: fmt.Sprintf("later reasoning %d", i), AuthorPersonID: f.creator.ID,
		})
		require.NoError(t, err)
	}

	res := f.call(t, cs, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()})
	out := mdecode[tools.GetPredictionVsOutcomeOutput](t, res)
	require.Len(t, out.Rows, 1)
	assert.Equal(t, f.verdictV1.ID.String(), out.Rows[0].Verdict.VerdictID, "must still be the originally-bound v1, regardless of how many later versions piled up")
	assert.Equal(t, 1, out.Rows[0].Verdict.Version)
}

// ── get_prediction_vs_outcome: matched/metrics/provenance rendering ───────

func TestGetPredictionVsOutcome_AutoMatch_IncludedWithProvenanceAndConfidence(t *testing.T) {
	f := newFullChainFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()})
	out := mdecode[tools.GetPredictionVsOutcomeOutput](t, res)
	require.Len(t, out.Rows, 1)

	row := out.Rows[0]
	assert.Equal(t, "auto", row.MatchProvenance)
	assert.InDelta(t, 0.91, row.MatchConfidence, 1e-9)
	assert.Equal(t, f.video.YouTubeVideoID, row.Video.YouTubeVideoID)
	require.NotNil(t, row.Metrics.Views)
	assert.Equal(t, int64(500), *row.Metrics.Views)
	assert.Equal(t, 0, out.PendingMatchCount)
}

func TestGetPredictionVsOutcome_ConfirmedMatch_IncludedWithProvenanceConfirmed(t *testing.T) {
	f := newFullChainFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	// Flip the auto match to pending, then confirm it via MatchStore
	// directly (resolve_pending_match is #1581's tool, not under test
	// here) -- proves "confirmed" state renders provenance "confirmed".
	_, err := f.pg.Pool.Exec(ctx, `UPDATE video_schedule_match SET state = 'pending' WHERE schedule_entry_id = $1`, f.entry.ID)
	require.NoError(t, err)
	pending, err := f.st.Matches().ListPending(ctx, f.ch.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, f.st.Matches().Resolve(ctx, pending[0].ID, f.creator.ID, true, nil))

	res := f.call(t, cs, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()})
	out := mdecode[tools.GetPredictionVsOutcomeOutput](t, res)
	require.Len(t, out.Rows, 1)
	assert.Equal(t, "confirmed", out.Rows[0].MatchProvenance)
}

func TestGetPredictionVsOutcome_PendingMatch_ExcludedButCounted(t *testing.T) {
	f := newFullChainFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	_, err := f.pg.Pool.Exec(ctx, `UPDATE video_schedule_match SET state = 'pending' WHERE schedule_entry_id = $1`, f.entry.ID)
	require.NoError(t, err)

	res := f.call(t, cs, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()})
	out := mdecode[tools.GetPredictionVsOutcomeOutput](t, res)
	assert.Empty(t, out.Rows, "a pending match must never qualify for the comparison")
	assert.Equal(t, 1, out.PendingMatchCount, "but it must be reflected in pending_match_count so a browsing agent knows the picture is incomplete")
}

func TestGetPredictionVsOutcome_RejectedMatch_ExcludedEntirely(t *testing.T) {
	f := newFullChainFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	_, err := f.pg.Pool.Exec(ctx, `UPDATE video_schedule_match SET state = 'rejected' WHERE schedule_entry_id = $1`, f.entry.ID)
	require.NoError(t, err)

	res := f.call(t, cs, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()})
	out := mdecode[tools.GetPredictionVsOutcomeOutput](t, res)
	assert.Empty(t, out.Rows, "a rejected match must never appear")
	assert.Equal(t, 0, out.PendingMatchCount, "a rejected match is not pending either")
}

func TestGetPredictionVsOutcome_VerdictWithNoPublishedVideo_DoesNotAppearOrError(t *testing.T) {
	f := newBrowseFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	idea, err := f.st.Ideas().Create(ctx, f.ch.ID, "Verdict Only Idea", f.creator.ID)
	require.NoError(t, err)
	_, err = f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "no video yet", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)

	res := f.call(t, cs, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", mtextOf(res))
	out := mdecode[tools.GetPredictionVsOutcomeOutput](t, res)
	assert.Empty(t, out.Rows)
}

// ── get_channel_overview: full population + empty channel ─────────────────

func TestGetChannelOverview_FullyPopulatedChannel_EveryomeSectionRendered(t *testing.T) {
	f := newFullChainFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	_, err := f.st.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: f.ch.ID, IdeaID: &f.idea.ID, Text: "cited note", SourceURL: strPtrB("https://example.com/a"),
		AuthorPersonID: f.creator.ID, IdempotencyKey: "note-cited-1",
	})
	require.NoError(t, err)
	_, err = f.st.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: f.ch.ID, IdeaID: &f.idea.ID, Text: "uncited note",
		AuthorPersonID: f.creator.ID, IdempotencyKey: "note-uncited-1",
	})
	require.NoError(t, err)

	res := f.call(t, cs, "get_channel_overview", tools.GetChannelOverviewInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", mtextOf(res))
	out := mdecode[tools.GetChannelOverviewOutput](t, res)

	assert.Equal(t, f.ch.ID.String(), out.Channel.ChannelID)
	assert.Equal(t, "connected", out.Channel.ConnectionState)

	require.Len(t, out.Ideas, 1)
	assert.Equal(t, "Full Chain Idea", out.Ideas[0].Title)
	require.NotNil(t, out.Ideas[0].CurrentVerdict, "the idea has a recorded verdict")
	assert.Equal(t, 2, out.Ideas[0].CurrentVerdict.Version, "overview's ideas section reports the CURRENT verdict, unlike prediction_vs_outcome's bound one")

	require.Len(t, out.ResearchNotes, 2)
	var sawCited, sawUncited bool
	for _, n := range out.ResearchNotes {
		if n.Cited {
			sawCited = true
		} else {
			sawUncited = true
		}
	}
	assert.True(t, sawCited, "the cited note must render cited=true")
	assert.True(t, sawUncited, "the uncited note must render cited=false")

	require.Len(t, out.ScheduleEntries, 1)
	assert.Equal(t, "committed", out.ScheduleEntries[0].State)
	assert.Equal(t, 1, out.ScheduleEntries[0].VerdictVersion, "schedule section also reports the bound version (LB3), not the current one")

	assert.Equal(t, 1, out.SyncedSchedule.TotalVideos)
	assert.Equal(t, 1, out.SyncedSchedule.Published)
	assert.Equal(t, 0, out.PendingMatchCount)

	require.Len(t, out.PredictionVsOutcome, 1)
	assert.Equal(t, "Full Chain Idea", out.PredictionVsOutcome[0].IdeaTitle)

	assert.Empty(t, out.Truncated, "nothing should be truncated for this small a fixture")
}

func strPtrB(s string) *string { return &s }

func TestGetChannelOverview_FreshlyConnectedChannel_EmptySectionsNotNullsOrErrors(t *testing.T) {
	f := newBrowseFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "get_channel_overview", tools.GetChannelOverviewInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", mtextOf(res))
	out := mdecode[tools.GetChannelOverviewOutput](t, res)

	assert.NotNil(t, out.Ideas)
	assert.Empty(t, out.Ideas)
	assert.NotNil(t, out.ResearchNotes)
	assert.Empty(t, out.ResearchNotes)
	assert.NotNil(t, out.ScheduleEntries)
	assert.Empty(t, out.ScheduleEntries)
	assert.NotNil(t, out.PredictionVsOutcome)
	assert.Empty(t, out.PredictionVsOutcome)
	assert.Equal(t, 0, out.SyncedSchedule.TotalVideos)
	assert.Empty(t, out.Truncated)
}

// ── get_channel_overview: truncation ────────────────────────────────────

func TestGetChannelOverview_ResearchNotesTruncatedPastDefaultLimit(t *testing.T) {
	f := newBrowseFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	// defaultNotesOverviewLimit is 20 (browse.go); seed one more than that.
	const seeded = 21
	for i := 0; i < seeded; i++ {
		_, err := f.st.Research().SaveNote(ctx, store.SaveNoteInput{
			ChannelID: f.ch.ID, Text: fmt.Sprintf("note %d", i),
			AuthorPersonID: f.creator.ID, IdempotencyKey: fmt.Sprintf("trunc-note-%d", i),
		})
		require.NoError(t, err)
	}

	res := f.call(t, cs, "get_channel_overview", tools.GetChannelOverviewInput{ChannelID: f.ch.ID.String()})
	out := mdecode[tools.GetChannelOverviewOutput](t, res)

	assert.Len(t, out.ResearchNotes, 20, "must be capped at the documented default")
	assert.Contains(t, out.Truncated, "research_notes", "the response must flag which section was cut")
}

// ── get_channel_overview: needs_reauth still browses ───────────────────────

func TestGetChannelOverview_NeedsReauthChannel_StillBrowsesAndSurfacesState(t *testing.T) {
	f := newFullChainFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	require.NoError(t, f.st.Channels().SetConnectionState(ctx, f.ch.ID, store.ConnectionStateNeedsReauth))

	res := f.call(t, cs, "get_channel_overview", tools.GetChannelOverviewInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "a needs_reauth channel must still browse fine (FR4 retains data)")
	out := mdecode[tools.GetChannelOverviewOutput](t, res)

	assert.Equal(t, "needs_reauth", out.Channel.ConnectionState)
	require.Len(t, out.Ideas, 1, "existing data must still be readable")
}

// ── Creator + Analyst mutual visibility, unassociated Person denied ───────

func TestGetChannelOverview_CreatorAndAnalystGetIdenticalResponses(t *testing.T) {
	f := newFullChainFixture(t)
	creatorCS := f.connect(t, f.creator.ID)
	analystCS := f.connect(t, f.analyst.ID)

	creatorOut := mdecode[tools.GetChannelOverviewOutput](t, f.call(t, creatorCS, "get_channel_overview", tools.GetChannelOverviewInput{ChannelID: f.ch.ID.String()}))
	analystOut := mdecode[tools.GetChannelOverviewOutput](t, f.call(t, analystCS, "get_channel_overview", tools.GetChannelOverviewInput{ChannelID: f.ch.ID.String()}))

	assert.Equal(t, creatorOut, analystOut, "Creator and Analyst must see byte-identical get_channel_overview data -- mutual visibility is C10's point")
}

func TestGetChannelOverview_UnassociatedPersonDenied(t *testing.T) {
	f := newFullChainFixture(t)
	cs := f.connect(t, f.outsider.ID)

	res := f.call(t, cs, "get_channel_overview", tools.GetChannelOverviewInput{ChannelID: f.ch.ID.String()})
	assert.True(t, res.IsError)
	assert.Contains(t, mtextOf(res), "permission denied")
}

func TestGetPredictionVsOutcome_CreatorAndAnalystGetIdenticalResponses(t *testing.T) {
	f := newFullChainFixture(t)
	creatorCS := f.connect(t, f.creator.ID)
	analystCS := f.connect(t, f.analyst.ID)

	creatorOut := mdecode[tools.GetPredictionVsOutcomeOutput](t, f.call(t, creatorCS, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()}))
	analystOut := mdecode[tools.GetPredictionVsOutcomeOutput](t, f.call(t, analystCS, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()}))

	assert.Equal(t, creatorOut, analystOut, "Creator and Analyst must see byte-identical get_prediction_vs_outcome data")
}

func TestGetPredictionVsOutcome_UnassociatedPersonDenied(t *testing.T) {
	f := newFullChainFixture(t)
	cs := f.connect(t, f.outsider.ID)

	res := f.call(t, cs, "get_prediction_vs_outcome", tools.GetPredictionVsOutcomeInput{ChannelID: f.ch.ID.String()})
	assert.True(t, res.IsError)
	assert.Contains(t, mtextOf(res), "permission denied")
}
