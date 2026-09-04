//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. Mirrors browse_integration_test.go's pattern: spin up a throwaway
// Postgres via dbtest, apply the real embedded migrations, host
// RegisterMyWork's get_my_work (my_work.go, issue #1719, FR27/FR28) behind
// a real *mcp.Server over HTTP, and drive it with a real in-process MCP
// client.
//
// The load-bearing test in this file is
// TestGetMyWork_RoleRevokedBetweenCalls_DropsChannelNextCall_NoReauth: the
// concrete regression FR28 exists for -- a role revoked between two calls,
// on the SAME server instance and credential (no reconnect, no re-mint),
// must drop that Channel from the very next call.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:my_work_integration_test --test_output=all
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
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
)

// myWorkFixture wires an isolated Postgres (dbtest, real embedded
// migrations) and a real *mcp.Server hosting get_my_work over HTTP.
type myWorkFixture struct {
	pg    *dbtest.Postgres
	st    *store.Store
	creds mcpauth.CredentialStore
	url   string
}

func newMyWorkFixture(t *testing.T) *myWorkFixture {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	st := store.New(pg.Pool)

	creds, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{
		Pool:           pg.Pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	require.NoError(t, err)

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterMyWork(reg, st.MyWork())

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &myWorkFixture{pg: pg, st: st, creds: creds, url: ts.URL}
}

type mwBearerRoundTripper struct{ token string }

func (rt mwBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

func (f *myWorkFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: mwBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (f *myWorkFixture) call(t *testing.T, cs *mcp.ClientSession, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_my_work", Arguments: args})
	require.NoError(t, err)
	return res
}

func mwTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func mwDecode(t *testing.T, res *mcp.CallToolResult) tools.GetMyWorkOutput {
	t.Helper()
	require.False(t, res.IsError, "unexpected tool error: %s", mwTextOf(res))
	var out tools.GetMyWorkOutput
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// channelByID finds the entry in out.Channels whose channel_id matches id,
// or nil if absent -- get_my_work's response order is not under test here.
func channelByID(out tools.GetMyWorkOutput, id uuid.UUID) *tools.ChannelWorkSummaryOutput {
	for i := range out.Channels {
		if out.Channels[i].Channel.ChannelID == id.String() {
			return &out.Channels[i]
		}
	}
	return nil
}

// ── content coverage: FR27's four sections, three tiers, a fourth excluded Channel ──

// TestGetMyWork_ThreeChannelsThreeTiers_FourthChannelExcluded is the
// concrete test for FR27/FR28's Channel-set contract: a Person on three
// Channels at three different tiers gets all three back, each correct,
// while a fourth Channel they hold no role on never appears.
func TestGetMyWork_ThreeChannelsThreeTiers_FourthChannelExcluded(t *testing.T) {
	f := newMyWorkFixture(t)
	ctx := context.Background()

	person, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-mw-person", "mw-person@example.com", "MW Person")
	require.NoError(t, err)

	// Channel A: person is Founder. Fully populated: a research note, a
	// verdict, a greenlit video_script, and a full published-outcome
	// chain -- so all four FR27 content areas are reachable in one place.
	chA, err := f.st.Channels().Create(ctx, "yt-mw-a", "Channel A", person.ID)
	require.NoError(t, err)

	_, err = f.st.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: chA.ID, Text: "note on A", AuthorPersonID: person.ID, IdempotencyKey: "mw-note-a",
	})
	require.NoError(t, err)

	ideaA, err := f.st.Ideas().Create(ctx, chA.ID, "Idea A", person.ID)
	require.NoError(t, err)
	verdictA, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaA.ID, Verdict: store.VerdictViable, Reasoning: "viable reasoning A", AuthorPersonID: person.ID,
	})
	require.NoError(t, err)

	// loadVideoScriptState (store/mywork.go, retargeted from schedule_entry
	// by #1835) aggregates video_script directly -- the greenlit script
	// proposed below (grounding the outcome chain, FR44's re-anchor) is
	// what covers a.VideoScriptState.GreenlitCount==1; it's bound to the
	// SAME verdictA so it does not disturb a.LatestVerdict's
	// "most-recently-created" ordering.
	stratA, err := f.st.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: chA.ID, Title: "Idea A Strategy", Active: true,
		VerdictIDs: []uuid.UUID{verdictA.ID}, CreatedByPersonID: person.ID,
	})
	require.NoError(t, err)
	scriptA, err := f.st.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: chA.ID, VerdictID: verdictA.ID, StrategyID: stratA.ID,
		Title: "Idea A", ScriptText: "script text for Idea A", CreatedByPersonID: person.ID,
	})
	require.NoError(t, err)
	require.NoError(t, f.st.VideoScripts().Greenlight(ctx, scriptA.ID, person.ID))

	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, f.st.Sync().UpsertVideos(ctx, chA.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-mw-video-a", Title: "Video A",
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	syncedA, _, err := f.st.Sync().ListSchedule(ctx, chA.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, syncedA, 1)
	require.NoError(t, f.st.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: syncedA[0].ID, Views: ptrInt64MW(1000), MeasuredAt: time.Now(),
	}}))
	require.NoError(t, f.st.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: syncedA[0].ID, VideoScriptID: &scriptA.ID, Confidence: 0.8, State: store.MatchStateAuto,
	}))

	// Channel B: person is Co-Creator, granted by B's own Founder. No
	// content -- proves an empty Channel still appears with zero-valued
	// sections, never a missing entry.
	founderB, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-mw-founder-b", "mw-founder-b@example.com", "Founder B")
	require.NoError(t, err)
	chB, err := f.st.Channels().Create(ctx, "yt-mw-b", "Channel B", founderB.ID)
	require.NoError(t, err)
	require.NoError(t, f.st.Roles().AddRole(ctx, chB.ID, person.ID, store.RoleCoCreator, founderB.ID))

	// Channel C: person is Analyst, granted by C's own Founder.
	founderC, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-mw-founder-c", "mw-founder-c@example.com", "Founder C")
	require.NoError(t, err)
	chC, err := f.st.Channels().Create(ctx, "yt-mw-c", "Channel C", founderC.ID)
	require.NoError(t, err)
	require.NoError(t, f.st.Roles().AddRole(ctx, chC.ID, person.ID, store.RoleAnalyst, founderC.ID))

	// Channel D: person holds NO role at all -- must never appear.
	founderD, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-mw-founder-d", "mw-founder-d@example.com", "Founder D")
	require.NoError(t, err)
	_, err = f.st.Channels().Create(ctx, "yt-mw-d", "Channel D", founderD.ID)
	require.NoError(t, err)

	cs := f.connect(t, person.ID)
	res := f.call(t, cs, tools.GetMyWorkInput{})
	out := mwDecode(t, res)

	require.Len(t, out.Channels, 3, "must return exactly A/B/C -- Channel D must be excluded")

	a := channelByID(out, chA.ID)
	require.NotNil(t, a, "Channel A must be present")
	assert.Equal(t, string(store.RoleCreator), a.Role)
	require.Len(t, a.ResearchNotes, 1)
	assert.Equal(t, "note on A", a.ResearchNotes[0].Text)
	require.NotNil(t, a.LatestVerdict, "Channel A's verdict must be reachable")
	assert.Equal(t, ideaA.ID.String(), a.LatestVerdict.IdeaID)
	assert.Equal(t, "viable", a.LatestVerdict.Verdict)
	assert.Equal(t, 0, a.VideoScriptState.ProposedCount)
	assert.Equal(t, 1, a.VideoScriptState.GreenlitCount)
	assert.Equal(t, 0, a.VideoScriptState.DeniedCount)
	assert.Equal(t, 0, a.VideoScriptState.ArchivedCount)
	require.NotNil(t, a.LatestOutcome, "Channel A's outcome must be reachable")
	assert.Equal(t, "yt-mw-video-a", a.LatestOutcome.Video.YouTubeVideoID)
	require.NotNil(t, a.LatestOutcome.Metrics.Views)
	assert.Equal(t, int64(1000), *a.LatestOutcome.Metrics.Views)

	b := channelByID(out, chB.ID)
	require.NotNil(t, b, "Channel B must be present")
	assert.Equal(t, string(store.RoleCoCreator), b.Role)
	assert.Empty(t, b.ResearchNotes)
	assert.Nil(t, b.LatestVerdict)
	assert.Equal(t, tools.MyWorkVideoScriptStateOutput{}, b.VideoScriptState)
	assert.Nil(t, b.LatestOutcome)

	c := channelByID(out, chC.ID)
	require.NotNil(t, c, "Channel C must be present")
	assert.Equal(t, string(store.RoleAnalyst), c.Role)
}

func ptrInt64MW(v int64) *int64 { return &v }

// ── FR28: no re-auth, no reconnect -- the revoke drops out on the very next call ──

// TestGetMyWork_RoleRevokedBetweenCalls_DropsChannelNextCall_NoReauth is
// the concrete regression test FR28 exists for: a role revoked between two
// get_my_work calls, on the SAME *mcp.ClientSession and credential (no
// re-mint, no reconnect), must drop that Channel out of the very next
// call's result.
func TestGetMyWork_RoleRevokedBetweenCalls_DropsChannelNextCall_NoReauth(t *testing.T) {
	f := newMyWorkFixture(t)
	ctx := context.Background()

	person, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-mw-fr28", "mw-fr28@example.com", "FR28 Person")
	require.NoError(t, err)
	founder, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-mw-fr28-founder", "mw-fr28-founder@example.com", "FR28 Founder")
	require.NoError(t, err)

	ch, err := f.st.Channels().Create(ctx, "yt-mw-fr28", "FR28 Channel", founder.ID)
	require.NoError(t, err)
	require.NoError(t, f.st.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))

	cs := f.connect(t, person.ID)

	res1 := f.call(t, cs, tools.GetMyWorkInput{})
	out1 := mwDecode(t, res1)
	require.NotNil(t, channelByID(out1, ch.ID), "Channel must appear before the role is revoked")

	removed, err := f.st.Roles().RemoveRole(ctx, ch.ID, person.ID, founder.ID)
	require.NoError(t, err)
	require.True(t, removed)

	// Same session, same credential -- no reconnect, no re-mint.
	res2 := f.call(t, cs, tools.GetMyWorkInput{})
	out2 := mwDecode(t, res2)
	assert.Nil(t, channelByID(out2, ch.ID), "revoked Channel must drop out of the very next call with no re-auth (FR28)")
	assert.Empty(t, out2.Channels)
}

// ── no roles anywhere: empty list, not an error ──────────────────────────

func TestGetMyWork_NoRoles_ReturnsEmptyListNotError(t *testing.T) {
	f := newMyWorkFixture(t)
	ctx := context.Background()

	person, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-mw-none", "mw-none@example.com", "No Roles")
	require.NoError(t, err)

	cs := f.connect(t, person.ID)
	res := f.call(t, cs, tools.GetMyWorkInput{})
	require.False(t, res.IsError, "unexpected error: %s", mwTextOf(res))
	out := mwDecode(t, res)
	assert.Empty(t, out.Channels)
}

// ── notes_per_channel ─────────────────────────────────────────────────────

func TestGetMyWork_NotesPerChannel_CapsResearchNotes(t *testing.T) {
	f := newMyWorkFixture(t)
	ctx := context.Background()

	person, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-mw-notes", "mw-notes@example.com", "Notes Person")
	require.NoError(t, err)
	ch, err := f.st.Channels().Create(ctx, "yt-mw-notes", "Notes Channel", person.ID)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		_, err := f.st.Research().SaveNote(ctx, store.SaveNoteInput{
			ChannelID: ch.ID, Text: "note", AuthorPersonID: person.ID, IdempotencyKey: uuid.NewString(),
		})
		require.NoError(t, err)
	}

	cs := f.connect(t, person.ID)
	res := f.call(t, cs, tools.GetMyWorkInput{NotesPerChannel: 2})
	out := mwDecode(t, res)

	got := channelByID(out, ch.ID)
	require.NotNil(t, got)
	assert.Len(t, got.ResearchNotes, 2, "notes_per_channel must cap the per-Channel research notes")
}
