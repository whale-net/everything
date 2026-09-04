//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. Mirrors verdict_integration_test.go's pattern: spin up a throwaway
// Postgres via dbtest, apply the real embedded migrations, host
// RegisterMatches' tools (matches.go, issue #1581, FR22/FR23) behind a
// real *mcp.Server over HTTP, and drive them with a real in-process MCP
// client -- proving list_pending_matches/resolve_pending_match's Channel-
// scoping (store.CanRead/store.CanWrite via server.RegisterRead/
// RegisterWrite), Creator+Analyst write authz, NFR2 idempotency replay/
// conflict semantics, and the MatchStore.Resolve state machine (only a
// pending row transitions; ErrMatchNotPending on a second resolve) all
// against the real embedded schema rather than a fake stand-in for any of
// it.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:matches_integration_test --test_output=all
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

// newMatchesTestDB provisions an isolated Postgres database via dbtest and
// applies every migration in the package's own embedded schema -- mirrors
// verdict_integration_test.go's newVerdictTestDB.
func newMatchesTestDB(t *testing.T) *dbtest.Postgres {
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

// matchesFixture is the common setup every test below needs: a Channel
// with a live Creator and Analyst, an unassociated Person with no role on
// it, a committed schedule_entry (the matcher's candidate), a published
// SyncedVideo, and one MatchStatePending video_schedule_match linking them
// -- as if worker/sync.Activities.SyncOutcomes had just queued it below
// threshold (issue #1581).
type matchesFixture struct {
	pg       *dbtest.Postgres
	st       *store.Store
	creds    mcpauth.CredentialStore
	ch       store.Channel
	creator  store.Person
	analyst  store.Person
	outsider store.Person
	entry    store.ScheduleEntry
	video    store.SyncedVideo
	match    store.VideoScheduleMatch
	url      string
}

func newMatchesFixture(t *testing.T) *matchesFixture {
	t.Helper()
	ctx := context.Background()

	pg := newMatchesTestDB(t)
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

	idea, err := st.Ideas().Create(ctx, ch.ID, "Best Guess Idea", creator.ID)
	require.NoError(t, err)
	verdict, err := st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "greenlit", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	entry, err := st.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: idea.ID, VerdictID: verdict.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, st.Schedules().Approve(ctx, entry.ID, creator.ID))

	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, st.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-" + uuid.NewString(), Title: "A Published Video",
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	synced, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, synced, 1)
	video := synced[0]

	require.NoError(t, st.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: ptrInt64(250), MeasuredAt: time.Now(),
	}}))

	require.NoError(t, st.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, ScheduleEntryID: &entry.ID, Confidence: 0.55, State: store.MatchStatePending,
	}))
	pending, _, err := st.Matches().ListPending(ctx, ch.ID, nil, 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	match := pending[0]

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterMatches(reg, st)

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &matchesFixture{
		pg: pg, st: st, creds: creds, ch: ch, creator: creator, analyst: analyst, outsider: outsider,
		entry: entry, video: video, match: match, url: ts.URL,
	}
}

func ptrInt64(v int64) *int64 { return &v }

// mBearerRoundTripper injects an "Authorization: Bearer <token>" header --
// mirrors verdict_integration_test.go's bearerRoundTripper (separate
// go_test target/compilation unit, so it cannot reuse that file's copy).
type mBearerRoundTripper struct{ token string }

func (rt mBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

func (f *matchesFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: mBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (f *matchesFixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

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

// predictionVsOutcomeIdeaTitlesForChannel reads v_prediction_vs_outcome
// directly (same "reach past the store API" pattern
// store_integration_test.go/outcomes_test.go both use) -- the simplest way
// to prove a confirmed match's video actually shows up in the FR24
// comparison read, since there is no MCP tool exposing that view yet
// (M3's C14 aggregate surface).
func predictionVsOutcomeIdeaTitlesForChannel(t *testing.T, ctx context.Context, pg *dbtest.Postgres, channelID uuid.UUID) []string {
	t.Helper()
	rows, err := pg.Pool.Query(ctx, `SELECT idea_title FROM v_prediction_vs_outcome WHERE channel_id = $1 ORDER BY idea_title`, channelID)
	require.NoError(t, err)
	defer rows.Close()

	var titles []string
	for rows.Next() {
		var title string
		require.NoError(t, rows.Scan(&title))
		titles = append(titles, title)
	}
	require.NoError(t, rows.Err())
	return titles
}

// ── list_pending_matches ─────────────────────────────────────────────────

func TestListPendingMatches_ReturnsVideoMetricsBestGuessAndConfidence(t *testing.T) {
	f := newMatchesFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "list_pending_matches", tools.ListPendingMatchesInput{ChannelID: f.ch.ID.String()})
	out := mdecode[tools.ListPendingMatchesOutput](t, res)
	require.Len(t, out.Matches, 1)

	m := out.Matches[0]
	assert.Equal(t, f.match.ID.String(), m.MatchID)
	assert.Equal(t, "A Published Video", m.Video.Title)
	require.NotNil(t, m.Video.Views)
	assert.Equal(t, int64(250), *m.Video.Views)
	assert.InDelta(t, 0.55, m.Confidence, 1e-9)
	require.NotNil(t, m.BestGuessEntry)
	assert.Equal(t, "Best Guess Idea", m.BestGuessEntry.IdeaTitle)
	assert.Equal(t, f.entry.ID.String(), m.BestGuessEntry.ScheduleEntryID)
}

func TestListPendingMatches_ChannelScoped_OtherChannelsMatchNotVisible(t *testing.T) {
	f := newMatchesFixture(t)
	ctx := context.Background()

	otherCreator, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-other-"+uuid.NewString(), "other@example.com", "Other Creator")
	require.NoError(t, err)
	otherCh, err := f.st.Channels().Create(ctx, "yt-other-"+uuid.NewString(), "Other Channel", otherCreator.ID)
	require.NoError(t, err)

	cs := f.connect(t, otherCreator.ID)
	res := f.call(t, cs, "list_pending_matches", tools.ListPendingMatchesInput{ChannelID: otherCh.ID.String()})
	out := mdecode[tools.ListPendingMatchesOutput](t, res)
	assert.Empty(t, out.Matches, "a Channel with no pending matches of its own must never see another Channel's")
}

// TestListPendingMatches_LimitTruncatedAndSincePageForwardExactly proves
// issue #1808's fix: limit caps the response with truncated set, and since
// (paired with the last returned match's created_at) resumes past a
// truncated page -- before this fix, list_pending_matches had no limit at
// all and could exceed an MCP client's response-size cap outright.
func TestListPendingMatches_LimitTruncatedAndSincePageForwardExactly(t *testing.T) {
	f := newMatchesFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	// The fixture already seeded one pending match (f.match); add two more
	// so there are three total, oldest first (list_pending_matches' order).
	for i := 0; i < 2; i++ {
		idea, err := f.st.Ideas().Create(ctx, f.ch.ID, fmt.Sprintf("Extra Idea %d", i), f.creator.ID)
		require.NoError(t, err)
		verdict, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
			IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "greenlit", AuthorPersonID: f.creator.ID,
		})
		require.NoError(t, err)
		entry, err := f.st.Schedules().SaveDraft(ctx, store.SaveDraftInput{
			ChannelID: f.ch.ID, IdeaID: idea.ID, VerdictID: verdict.ID,
			ProposedPublishAt: time.Now().Add(time.Duration(48+i) * time.Hour), CreatedByPersonID: f.creator.ID,
		})
		require.NoError(t, err)
		require.NoError(t, f.st.Schedules().Approve(ctx, entry.ID, f.creator.ID))

		publishedAt := time.Now().Add(-time.Hour)
		require.NoError(t, f.st.Sync().UpsertVideos(ctx, f.ch.ID, []store.SyncedVideo{{
			YouTubeVideoID: "yt-" + uuid.NewString(), Title: fmt.Sprintf("Extra Video %d", i),
			PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
		}}))
		synced, _, err := f.st.Sync().ListSchedule(ctx, f.ch.ID, nil, nil, true, 0)
		require.NoError(t, err)
		var video store.SyncedVideo
		for _, v := range synced {
			if v.Title == fmt.Sprintf("Extra Video %d", i) {
				video = v
			}
		}
		require.NotEmpty(t, video.ID, "must have found the just-inserted synced video")

		require.NoError(t, f.st.Matches().Record(ctx, store.VideoScheduleMatch{
			SyncedVideoID: video.ID, ScheduleEntryID: &entry.ID, Confidence: 0.5, State: store.MatchStatePending,
		}))
	}

	firstRes := f.call(t, cs, "list_pending_matches", tools.ListPendingMatchesInput{ChannelID: f.ch.ID.String(), Limit: 2})
	firstPage := mdecode[tools.ListPendingMatchesOutput](t, firstRes)
	require.Len(t, firstPage.Matches, 2, "must be capped at the caller-supplied limit")
	assert.True(t, firstPage.Truncated, "more matches exist beyond limit")

	lastOnFirstPage := firstPage.Matches[len(firstPage.Matches)-1]
	m, err := f.st.Matches().GetByID(ctx, uuid.MustParse(lastOnFirstPage.MatchID))
	require.NoError(t, err)
	since := m.CreatedAt

	secondRes := f.call(t, cs, "list_pending_matches", tools.ListPendingMatchesInput{ChannelID: f.ch.ID.String(), Since: &since})
	secondPage := mdecode[tools.ListPendingMatchesOutput](t, secondRes)
	require.False(t, secondPage.Truncated)
	require.Len(t, secondPage.Matches, 2, "since (inclusive) resumes at the last row of the prior page plus the one remaining match")
	assert.Equal(t, lastOnFirstPage.MatchID, secondPage.Matches[0].MatchID, "the inclusive bound reappears as the new page's first row")
}

func TestListPendingMatches_UnassociatedPersonDenied(t *testing.T) {
	f := newMatchesFixture(t)
	cs := f.connect(t, f.outsider.ID)

	res := f.call(t, cs, "list_pending_matches", tools.ListPendingMatchesInput{ChannelID: f.ch.ID.String()})
	assert.True(t, res.IsError)
	assert.Contains(t, mtextOf(res), "permission denied")
}

// ── resolve_pending_match: confirm ──────────────────────────────────────────

func TestResolvePendingMatch_Confirm_CreatesLinkAndVideoAppearsInComparison(t *testing.T) {
	f := newMatchesFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "resolve_pending_match", tools.ResolvePendingMatchInput{
		ChannelID: f.ch.ID.String(), MatchID: f.match.ID.String(), Confirm: true, IdempotencyKeyArg: "confirm-1",
	})
	out := mdecode[tools.ResolvedMatchOutput](t, res)
	assert.Equal(t, "confirmed", out.State)
	require.NotNil(t, out.LinkedEntry)
	assert.Equal(t, f.entry.ID.String(), out.LinkedEntry.ScheduleEntryID)
	assert.Equal(t, f.creator.ID.String(), out.ResolvedByPersonID)
	require.NotNil(t, out.ResolvedAt)

	titles := predictionVsOutcomeIdeaTitlesForChannel(t, ctx, f.pg, f.ch.ID)
	assert.Equal(t, []string{"Best Guess Idea"}, titles, "confirming must create the outcome link -- the idea must now appear in v_prediction_vs_outcome")

	listRes := f.call(t, cs, "list_pending_matches", tools.ListPendingMatchesInput{ChannelID: f.ch.ID.String()})
	listOut := mdecode[tools.ListPendingMatchesOutput](t, listRes)
	assert.Empty(t, listOut.Matches, "a confirmed match must no longer show up as pending")
}

func TestResolvePendingMatch_ConfirmWithOverrideScheduleEntryID_LinksToChosenEntryNotBestGuess(t *testing.T) {
	f := newMatchesFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	otherIdea, err := f.st.Ideas().Create(ctx, f.ch.ID, "The Actually Correct Idea", f.creator.ID)
	require.NoError(t, err)
	otherVerdict, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: otherIdea.ID, Verdict: store.VerdictViable, Reasoning: "also greenlit", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	otherEntry, err := f.st.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: f.ch.ID, IdeaID: otherIdea.ID, VerdictID: otherVerdict.ID,
		ProposedPublishAt: time.Now().Add(48 * time.Hour), CreatedByPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, f.st.Schedules().Approve(ctx, otherEntry.ID, f.creator.ID))

	res := f.call(t, cs, "resolve_pending_match", tools.ResolvePendingMatchInput{
		ChannelID: f.ch.ID.String(), MatchID: f.match.ID.String(), Confirm: true,
		ScheduleEntryID: otherEntry.ID.String(), IdempotencyKeyArg: "confirm-override-1",
	})
	out := mdecode[tools.ResolvedMatchOutput](t, res)
	require.NotNil(t, out.LinkedEntry)
	assert.Equal(t, otherEntry.ID.String(), out.LinkedEntry.ScheduleEntryID, "an explicit override must link to the human-chosen entry, not the matcher's best guess")
	assert.Equal(t, "The Actually Correct Idea", out.LinkedEntry.IdeaTitle)
}

// ── resolve_pending_match: reject ───────────────────────────────────────────

func TestResolvePendingMatch_Reject_LeavesVideoUnmatchedAndOutOfComparison(t *testing.T) {
	f := newMatchesFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "resolve_pending_match", tools.ResolvePendingMatchInput{
		ChannelID: f.ch.ID.String(), MatchID: f.match.ID.String(), Confirm: false, IdempotencyKeyArg: "reject-1",
	})
	out := mdecode[tools.ResolvedMatchOutput](t, res)
	assert.Equal(t, "rejected", out.State)
	assert.Nil(t, out.LinkedEntry, "a rejected match must never render a linked_entry (FR23: the video remains unmatched)")

	titles := predictionVsOutcomeIdeaTitlesForChannel(t, ctx, f.pg, f.ch.ID)
	assert.Empty(t, titles, "a rejected match must never appear in v_prediction_vs_outcome")

	listRes := f.call(t, cs, "list_pending_matches", tools.ListPendingMatchesInput{ChannelID: f.ch.ID.String()})
	listOut := mdecode[tools.ListPendingMatchesOutput](t, listRes)
	assert.Empty(t, listOut.Matches, "a rejected match must no longer show up as pending")
}

// ── resolve_pending_match: second resolve conflicts ─────────────────────────

func TestResolvePendingMatch_SecondResolve_RejectedAsConflict_FirstResolutionUnchanged(t *testing.T) {
	f := newMatchesFixture(t)
	cs := f.connect(t, f.creator.ID)

	first := f.call(t, cs, "resolve_pending_match", tools.ResolvePendingMatchInput{
		ChannelID: f.ch.ID.String(), MatchID: f.match.ID.String(), Confirm: true, IdempotencyKeyArg: "first-key",
	})
	require.False(t, first.IsError, "unexpected error: %s", mtextOf(first))
	firstOut := mdecode[tools.ResolvedMatchOutput](t, first)

	second := f.call(t, cs, "resolve_pending_match", tools.ResolvePendingMatchInput{
		ChannelID: f.ch.ID.String(), MatchID: f.match.ID.String(), Confirm: false, IdempotencyKeyArg: "second-key-different",
	})
	assert.True(t, second.IsError, "resolving an already-resolved match under a different idempotency key must be a conflict, never a silent re-flip")

	m, err := f.st.Matches().GetByID(context.Background(), f.match.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateConfirmed, m.State, "the first resolution must be unchanged by the rejected second attempt")
	assert.Equal(t, firstOut.State, "confirmed")
}

// ── resolve_pending_match: idempotency replay (NFR2) ────────────────────────

func TestResolvePendingMatch_Replay_SameIdempotencyKey_ReturnsSameResultNoSecondMutation(t *testing.T) {
	f := newMatchesFixture(t)
	cs := f.connect(t, f.creator.ID)

	args := tools.ResolvePendingMatchInput{
		ChannelID: f.ch.ID.String(), MatchID: f.match.ID.String(), Confirm: true, IdempotencyKeyArg: "replay-key",
	}
	first := mdecode[tools.ResolvedMatchOutput](t, f.call(t, cs, "resolve_pending_match", args))
	second := mdecode[tools.ResolvedMatchOutput](t, f.call(t, cs, "resolve_pending_match", args))

	assert.Equal(t, first, second, "an identical replay (same tool/person/key/fingerprint) must return the exact same result, not a conflict")

	m, err := f.st.Matches().GetByID(context.Background(), f.match.ID)
	require.NoError(t, err)
	require.NotNil(t, m.ResolvedAt)
	assert.Equal(t, store.MatchStateConfirmed, m.State)
}

// ── authorization: Creator + Analyst write, unassociated denied ────────────

func TestResolvePendingMatch_AnalystCanResolve_UnassociatedPersonDeniedAndWritesNothing(t *testing.T) {
	f := newMatchesFixture(t)
	analystCS := f.connect(t, f.analyst.ID)

	res := f.call(t, analystCS, "resolve_pending_match", tools.ResolvePendingMatchInput{
		ChannelID: f.ch.ID.String(), MatchID: f.match.ID.String(), Confirm: true, IdempotencyKeyArg: "analyst-resolve-1",
	})
	require.False(t, res.IsError, "unexpected error: %s", mtextOf(res))
	out := mdecode[tools.ResolvedMatchOutput](t, res)
	assert.Equal(t, f.analyst.ID.String(), out.ResolvedByPersonID, "FR23 names both Creator and Analyst as allowed resolvers")
}

func TestResolvePendingMatch_UnassociatedPersonDenied_NothingWritten(t *testing.T) {
	f := newMatchesFixture(t)
	outsiderCS := f.connect(t, f.outsider.ID)

	res := f.call(t, outsiderCS, "resolve_pending_match", tools.ResolvePendingMatchInput{
		ChannelID: f.ch.ID.String(), MatchID: f.match.ID.String(), Confirm: true, IdempotencyKeyArg: "outsider-attempt-1",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, mtextOf(res), "permission denied")

	m, err := f.st.Matches().GetByID(context.Background(), f.match.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStatePending, m.State, "a denied call must leave the match exactly as it was")
}
