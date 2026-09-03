//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// //audience_score_system/mcp/server/server_integration_test.go for the
// pattern this file follows: spin up a throwaway Postgres via dbtest,
// apply the real embedded migrations, host RegisterVerdict's and
// RegisterScheduleDraft's tools behind a real *mcp.Server over an
// httptest.Server, and drive them with a real in-process MCP client
// (mcp.NewClient + StreamableClientTransport).
//
// schedule_draft_test.go's pure-Go suite (package tools, no build tag)
// already covers parseWeekdayName, weekBounds, and computeScheduleFlags in
// isolation. What this file proves instead is exactly what a pure-function
// unit test cannot: real caller-auth + Channel-scoping wired through
// RegisterRead/RegisterWrite, save_schedule_draft's LB3 verdict-version
// binding (read back from the database, not just what the tool rendered),
// FR16's viable-verdict gate atomically writing nothing on rejection,
// set_pacing_policy's upsert convergence (FR17/NFR2), FR18's non-blocking
// flags computed fresh against real Postgres state -- including on an
// idempotent replay, where nothing about the flags may be cached from the
// first save -- and get_drafting_context/list_schedule_entries's real
// end-to-end rendering. See issue #1579's Testing section, which every
// test function below is named after.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:schedule_draft_integration_test --test_output=all
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

// newScheduleDraftTestDB provisions an isolated Postgres database via
// dbtest and applies every migration in the package's own embedded
// schema, mirroring verdict_integration_test.go's newVerdictTestDB.
func newScheduleDraftTestDB(t *testing.T) *dbtest.Postgres {
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

// scheduleDraftFixture is the common setup every test below needs: a
// Channel with a live Creator and Analyst, an unassociated Person with no
// role on it, and an Idea to attach verdicts/drafts to, hosted behind a
// real MCP server with RegisterVerdict + RegisterScheduleDraft wired.
type scheduleDraftFixture struct {
	st       *store.Store
	creds    mcpauth.CredentialStore
	ch       store.Channel
	creator  store.Person
	analyst  store.Person
	outsider store.Person
	idea     store.Idea
	url      string
}

func newScheduleDraftFixture(t *testing.T) *scheduleDraftFixture {
	t.Helper()
	ctx := context.Background()

	pg := newScheduleDraftTestDB(t)
	st := store.New(pg.Pool)
	creds := newTestCredentialStore(t, pg.Pool)

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-sd-creator-"+uuid.NewString(), "sd-creator@example.com", "Creator Person")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-sd-analyst-"+uuid.NewString(), "sd-analyst@example.com", "Analyst Person")
	require.NoError(t, err)
	outsider, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-sd-outsider-"+uuid.NewString(), "sd-outsider@example.com", "Outsider Person")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-sd-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst))

	idea, err := st.Ideas().FindOrCreate(ctx, ch.ID, "Schedule Draft Test Idea", creator.ID)
	require.NoError(t, err)

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterVerdict(reg, st)
	tools.RegisterScheduleDraft(reg, st)

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &scheduleDraftFixture{
		st: st, creds: creds, ch: ch, creator: creator, analyst: analyst, outsider: outsider,
		idea: idea,
		url:  ts.URL,
	}
}

// bearerRoundTripper injects an "Authorization: Bearer <token>" header on
// every request -- mirrors verdict_integration_test.go's.
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// connect opens a real streamable-HTTP MCP client session authenticated as
// personID.
func (f *scheduleDraftFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
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

func (f *scheduleDraftFixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// saveViableVerdict is a convenience wrapper: as personID (via cs), save a
// "viable" verdict on ideaID and return its rendered VerdictOutput.
func (f *scheduleDraftFixture) saveViableVerdict(t *testing.T, cs *mcp.ClientSession, ideaID uuid.UUID, reasoning string) tools.VerdictOutput {
	t.Helper()
	res := f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            ideaID.String(),
		Verdict:           "viable",
		Reasoning:         reasoning,
		IdempotencyKeyArg: uuid.NewString(),
	})
	return sdDecode[tools.VerdictOutput](t, res)
}

// sdTextOf concatenates every TextContent block in res.Content -- the
// error message a rejected call's Content carries.
func sdTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func sdDecode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	require.False(t, res.IsError, "unexpected tool error: %s", sdTextOf(res))
	require.NoError(t, sdMapDecode(res.StructuredContent, &out))
	return out
}

func sdMapDecode(v any, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// mondayAt returns an arbitrary but fixed far-future Monday (confirmed by
// construction, never by a hardcoded calendar date that could rot) at the
// given hour UTC -- the anchor every FR18 flag test below builds its
// proposed_publish_at/existing-entry timestamps from, so tests never
// depend on when they happen to run.
func mondayAt(hour int) time.Time {
	anchor := time.Date(2032, 6, 1, 0, 0, 0, 0, time.UTC)
	daysSinceMonday := (int(anchor.Weekday()) + 6) % 7
	monday := anchor.AddDate(0, 0, -daysSinceMonday)
	return time.Date(monday.Year(), monday.Month(), monday.Day(), hour, 0, 0, 0, time.UTC)
}

// ── save_schedule_draft: LB3 verdict-version binding ────────────────────

func TestSaveScheduleDraft_ViableVerdict_BindsExactVerdictVersionID_ReadBackFromDB(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	verdict := f.saveViableVerdict(t, cs, f.idea.ID, "strong comps, ready to schedule")
	require.Equal(t, 1, verdict.Version)

	saveRes := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		ProposedPublishAt: mondayAt(9).Format(time.RFC3339),
		IdempotencyKeyArg: "save-draft-1",
	})
	out := sdDecode[tools.SaveScheduleDraftOutput](t, saveRes)
	assert.Equal(t, verdict.ID, out.VerdictID)
	assert.Equal(t, 1, out.VerdictVersion)

	entryID, err := uuid.Parse(out.ScheduleEntryID)
	require.NoError(t, err)
	entry, err := f.st.Schedules().GetByID(context.Background(), entryID)
	require.NoError(t, err)
	verdictID, err := uuid.Parse(verdict.ID)
	require.NoError(t, err)
	assert.Equal(t, verdictID, entry.VerdictID, "schedule_entry.verdict_id must be the exact verdict version's id, read directly from the database (LB3)")
	assert.Equal(t, store.ScheduleStateDraft, entry.State)
}

func TestSaveScheduleDraft_NewerVerdictBindsNewer_ExplicitOlderVerdictIDBindsOlder(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	v1 := f.saveViableVerdict(t, cs, f.idea.ID, "first viable pass")
	require.Equal(t, 1, v1.Version)
	v2 := f.saveViableVerdict(t, cs, f.idea.ID, "second, refreshed viable pass")
	require.Equal(t, 2, v2.Version)

	t.Run("no verdict_id binds the current (newer) version", func(t *testing.T) {
		res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
			ChannelID:         f.ch.ID.String(),
			IdeaID:            f.idea.ID.String(),
			ProposedPublishAt: mondayAt(9).Format(time.RFC3339),
			IdempotencyKeyArg: "bind-current",
		})
		out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
		assert.Equal(t, v2.ID, out.VerdictID)
		assert.Equal(t, 2, out.VerdictVersion)
	})

	t.Run("explicit verdict_id pins the older version", func(t *testing.T) {
		res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
			ChannelID:         f.ch.ID.String(),
			IdeaID:            f.idea.ID.String(),
			ProposedPublishAt: mondayAt(9).Add(48 * time.Hour).Format(time.RFC3339),
			VerdictID:         v1.ID,
			IdempotencyKeyArg: "bind-older",
		})
		out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
		assert.Equal(t, v1.ID, out.VerdictID, "explicitly passing an older verdict_id must bind to that exact version, not the current one")
		assert.Equal(t, 1, out.VerdictVersion)
	})
}

// ── save_schedule_draft: FR16 non-viable/absent verdict rejected ────────

func TestSaveScheduleDraft_NonViableOrAbsentVerdict_RejectedNoRowWritten(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	t.Run("not-viable verdict rejected", func(t *testing.T) {
		idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Not viable idea "+uuid.NewString(), f.creator.ID)
		require.NoError(t, err)
		f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
			ChannelID:         f.ch.ID.String(),
			IdeaID:            idea.ID.String(),
			Verdict:           "not-viable",
			Reasoning:         "too niche",
			IdempotencyKeyArg: uuid.NewString(),
		})

		res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
			ChannelID:         f.ch.ID.String(),
			IdeaID:            idea.ID.String(),
			ProposedPublishAt: mondayAt(9).Format(time.RFC3339),
			IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "a not-viable verdict must be rejected")

		entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
		require.NoError(t, err)
		for _, e := range entries {
			assert.NotEqual(t, idea.ID, e.IdeaID, "no schedule_entry row must exist for a rejected idea")
		}
	})

	t.Run("needs-more-research verdict rejected", func(t *testing.T) {
		idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Needs research idea "+uuid.NewString(), f.creator.ID)
		require.NoError(t, err)
		f.call(t, cs, "save_viability_verdict", tools.SaveViabilityVerdictInput{
			ChannelID:         f.ch.ID.String(),
			IdeaID:            idea.ID.String(),
			Verdict:           "needs-more-research",
			Reasoning:         "need another comp",
			IdempotencyKeyArg: uuid.NewString(),
		})

		res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
			ChannelID:         f.ch.ID.String(),
			IdeaID:            idea.ID.String(),
			ProposedPublishAt: mondayAt(9).Format(time.RFC3339),
			IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "a needs-more-research verdict must be rejected")

		entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
		require.NoError(t, err)
		for _, e := range entries {
			assert.NotEqual(t, idea.ID, e.IdeaID)
		}
	})

	t.Run("idea with no verdict at all rejected", func(t *testing.T) {
		idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "No verdict idea "+uuid.NewString(), f.creator.ID)
		require.NoError(t, err)

		res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
			ChannelID:         f.ch.ID.String(),
			IdeaID:            idea.ID.String(),
			ProposedPublishAt: mondayAt(9).Format(time.RFC3339),
			IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "an idea with no verdict at all must be rejected")

		entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
		require.NoError(t, err)
		for _, e := range entries {
			assert.NotEqual(t, idea.ID, e.IdeaID)
		}
	})
}

func TestSaveScheduleDraft_VerdictIDBelongingToDifferentIdea_Rejected(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	otherIdea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "A different idea "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)
	otherVerdict := f.saveViableVerdict(t, cs, otherIdea.ID, "viable, but for the other idea")

	res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		ProposedPublishAt: mondayAt(9).Format(time.RFC3339),
		VerdictID:         otherVerdict.ID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "a verdict_id belonging to a different idea must be rejected")
	assert.Contains(t, sdTextOf(res), "different idea")

	entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	assert.Empty(t, entries, "a rejected cross-idea verdict_id must leave no schedule_entry row at all")
}

// ── set_pacing_policy: FR17 upsert convergence (NFR2) ────────────────────

func TestSetPacingPolicy_RepeatedCallsConverge_OneRowSameIDIdenticalContent_ThenUpdatedInPlace(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	first := f.call(t, cs, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 3,
		PreferredDays:        []string{"Monday", "Wednesday"},
	})
	firstOut := sdDecode[tools.PacingPolicyOutput](t, first)

	second := f.call(t, cs, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 3,
		PreferredDays:        []string{"Monday", "Wednesday"},
	})
	secondOut := sdDecode[tools.PacingPolicyOutput](t, second)
	assert.Equal(t, firstOut, secondOut, "identical repeated calls must converge to one row with identical content (NFR2)")

	policyAfterTwoCalls, ok, err := f.st.Pacing().Get(context.Background(), f.ch.ID)
	require.NoError(t, err)
	require.True(t, ok)

	third := f.call(t, cs, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 5,
		PreferredDays:        []string{"Friday"},
	})
	thirdOut := sdDecode[tools.PacingPolicyOutput](t, third)
	assert.Equal(t, float64(5), thirdOut.TargetUploadsPerWeek)
	assert.Equal(t, []string{"Friday"}, thirdOut.PreferredDays)

	policyAfterUpdate, ok, err := f.st.Pacing().Get(context.Background(), f.ch.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, policyAfterTwoCalls.ID, policyAfterUpdate.ID, "a value change must update the single existing row in place, not insert a second row")
}

func TestSetPacingPolicy_AnalystCanSet_UnassociatedPersonDeniedWritesNothing(t *testing.T) {
	f := newScheduleDraftFixture(t)

	analystCS := f.connect(t, f.analyst.ID)
	res := f.call(t, analystCS, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 2,
	})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))

	outsiderCS := f.connect(t, f.outsider.ID)
	deniedRes := f.call(t, outsiderCS, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 9,
	})
	assert.True(t, deniedRes.IsError)
	assert.Contains(t, sdTextOf(deniedRes), "permission denied")

	policy, ok, err := f.st.Pacing().Get(context.Background(), f.ch.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, float64(2), policy.TargetUploadsPerWeek, "the denied call must not have written anything -- the Analyst's value must still be current")
}

// ── save_schedule_draft: FR18 non-blocking flags ─────────────────────────

func TestSaveScheduleDraft_CadenceExceeded_FlaggedButStillPersisted(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	f.call(t, cs, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 2,
	})

	idea1, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Cadence idea 1 "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)
	idea2, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Cadence idea 2 "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)
	idea3, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Cadence idea 3 "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)

	v1 := f.saveViableVerdict(t, cs, idea1.ID, "viable 1")
	v2 := f.saveViableVerdict(t, cs, idea2.ID, "viable 2")
	v3 := f.saveViableVerdict(t, cs, idea3.ID, "viable 3")

	// Spread widely within the same [Monday, next Monday) week so nothing
	// here also triggers a collision flag -- this test is only about
	// cadence.
	f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: idea1.ID.String(), VerdictID: v1.ID,
		ProposedPublishAt: mondayAt(8).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: idea2.ID.String(), VerdictID: v2.ID,
		ProposedPublishAt: mondayAt(8).AddDate(0, 0, 2).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})

	thirdRes := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: idea3.ID.String(), VerdictID: v3.ID,
		ProposedPublishAt: mondayAt(8).AddDate(0, 0, 4).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	out := sdDecode[tools.SaveScheduleDraftOutput](t, thirdRes)
	assert.True(t, out.CadenceExceeded, "the third entry in a week with target=2 must be flagged")
	assert.False(t, out.Collision, "entries are spread far enough apart that collision must not also fire here")

	entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	assert.Len(t, entries, 3, "cadence_exceeded is advisory only -- the flagged draft must still be persisted")
}

func TestSaveScheduleDraft_Collision_WithSyncedScheduledDraft(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	verdict := f.saveViableVerdict(t, cs, f.idea.ID, "viable, colliding with a synced draft")

	proposed := mondayAt(10)
	syncedPublishAt := proposed.Add(2 * time.Hour)
	require.NoError(t, f.st.Sync().UpsertVideos(context.Background(), f.ch.ID, []store.SyncedVideo{{
		YouTubeVideoID:   "synced-scheduled-draft-1",
		Title:            "A scheduled private draft on YouTube",
		PrivacyStatus:    store.PrivacyStatusPrivate,
		PublishAt:        &syncedPublishAt,
		IsScheduledDraft: true,
		LastSyncedAt:     time.Now(),
	}}))

	res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(), VerdictID: verdict.ID,
		ProposedPublishAt: proposed.Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
	assert.True(t, out.Collision, "a slot 2h from a synced scheduled/private draft must collide")

	entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "a collision flag is advisory only -- the draft must still be saved")
}

func TestSaveScheduleDraft_Collision_WithCommittedEntry(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	committedIdea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Committed idea "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)
	committedVerdict := f.saveViableVerdict(t, cs, committedIdea.ID, "viable, will be committed")

	committedRes := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: committedIdea.ID.String(), VerdictID: committedVerdict.ID,
		ProposedPublishAt: mondayAt(10).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	committedOut := sdDecode[tools.SaveScheduleDraftOutput](t, committedRes)
	committedEntryID, err := uuid.Parse(committedOut.ScheduleEntryID)
	require.NoError(t, err)
	require.NoError(t, f.st.Schedules().Approve(context.Background(), committedEntryID, f.creator.ID))

	newIdea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "New idea near a committed slot "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)
	newVerdict := f.saveViableVerdict(t, cs, newIdea.ID, "viable, proposed near the committed slot")

	res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: newIdea.ID.String(), VerdictID: newVerdict.ID,
		ProposedPublishAt: mondayAt(12).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
	assert.True(t, out.Collision, "a slot 2h from an existing committed schedule_entry must collide")
}

func TestSaveScheduleDraft_OffPreferredDay(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	f.call(t, cs, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 100, // high enough that cadence never also fires in this test
		PreferredDays:        []string{"Monday"},
	})

	t.Run("non-preferred weekday flagged", func(t *testing.T) {
		idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Off day idea "+uuid.NewString(), f.creator.ID)
		require.NoError(t, err)
		verdict := f.saveViableVerdict(t, cs, idea.ID, "viable, proposed for Wednesday")

		res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
			ChannelID: f.ch.ID.String(), IdeaID: idea.ID.String(), VerdictID: verdict.ID,
			ProposedPublishAt: mondayAt(9).AddDate(0, 0, 2).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
		})
		out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
		assert.True(t, out.OffPreferredDay, "Wednesday is not in preferred_days=[Monday]")
	})

	t.Run("preferred weekday not flagged", func(t *testing.T) {
		idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "On day idea "+uuid.NewString(), f.creator.ID)
		require.NoError(t, err)
		verdict := f.saveViableVerdict(t, cs, idea.ID, "viable, proposed for Monday")

		res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
			ChannelID: f.ch.ID.String(), IdeaID: idea.ID.String(), VerdictID: verdict.ID,
			ProposedPublishAt: mondayAt(9).AddDate(0, 0, 21).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
		})
		out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
		assert.False(t, out.OffPreferredDay, "Monday is in preferred_days=[Monday]")
	})

	t.Run("empty preferred_days never flags", func(t *testing.T) {
		f.call(t, cs, "set_pacing_policy", tools.SetPacingPolicyInput{
			ChannelID:            f.ch.ID.String(),
			TargetUploadsPerWeek: 100,
			PreferredDays:        []string{},
		})

		idea, err := f.st.Ideas().Create(context.Background(), f.ch.ID, "Empty preferred idea "+uuid.NewString(), f.creator.ID)
		require.NoError(t, err)
		verdict := f.saveViableVerdict(t, cs, idea.ID, "viable, no day preference")

		res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
			ChannelID: f.ch.ID.String(), IdeaID: idea.ID.String(), VerdictID: verdict.ID,
			ProposedPublishAt: mondayAt(9).AddDate(0, 0, 44).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
		})
		out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
		assert.False(t, out.OffPreferredDay, "an empty preferred_days must mean no day preference, never a flag")
	})
}

func TestSaveScheduleDraft_CleanSlotNoPolicySet_NoFlagsNoError(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	verdict := f.saveViableVerdict(t, cs, f.idea.ID, "viable, clean slot")

	res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(), VerdictID: verdict.ID,
		ProposedPublishAt: mondayAt(9).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))
	out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
	assert.False(t, out.CadenceExceeded)
	assert.False(t, out.OffPreferredDay)
	assert.False(t, out.Collision)
	assert.Empty(t, out.Flags)
}

// ── save_schedule_draft: FR18 flags are freshly derived on replay (LB4) ──

func TestSaveScheduleDraft_Replay_FlagsRecomputedFreshEachCall_NotCachedFromFirstSave(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	verdict := f.saveViableVerdict(t, cs, f.idea.ID, "viable, replay flag freshness")

	args := tools.SaveScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            f.idea.ID.String(),
		VerdictID:         verdict.ID,
		ProposedPublishAt: mondayAt(9).AddDate(0, 0, 1).Format(time.RFC3339), // Tuesday
		IdempotencyKeyArg: "replay-flag-freshness",
	}

	first := sdDecode[tools.SaveScheduleDraftOutput](t, f.call(t, cs, "save_schedule_draft", args))
	assert.False(t, first.OffPreferredDay, "no policy exists yet, so the first save must not be flagged")

	// Change the world between calls: a pacing policy now excludes
	// Tuesday. The schedule_entry row itself is not touched by this --
	// only the render step, on the next call, should notice.
	f.call(t, cs, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 100,
		PreferredDays:        []string{"Monday"},
	})

	second := sdDecode[tools.SaveScheduleDraftOutput](t, f.call(t, cs, "save_schedule_draft", args))
	assert.True(t, second.OffPreferredDay, "an idempotent replay must recompute flags fresh against current state, not reuse the first call's cached flags")
	assert.Equal(t, first.ScheduleEntryID, second.ScheduleEntryID, "the replay must still resolve to the same, single persisted draft")

	entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "a replay must never duplicate the schedule_entry row")
}

// ── save_schedule_draft: Analyst can save, unassociated Person cannot ────

func TestSaveScheduleDraft_AnalystCanSave_UnassociatedPersonDeniedWritesNothing(t *testing.T) {
	f := newScheduleDraftFixture(t)

	analystCS := f.connect(t, f.analyst.ID)
	analystVerdict := f.saveViableVerdict(t, analystCS, f.idea.ID, "analyst's own viable call")

	analystRes := f.call(t, analystCS, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(), VerdictID: analystVerdict.ID,
		ProposedPublishAt: mondayAt(9).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, analystRes.IsError, "unexpected error: %s", sdTextOf(analystRes))

	outsiderCS := f.connect(t, f.outsider.ID)
	deniedRes := f.call(t, outsiderCS, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(), VerdictID: analystVerdict.ID,
		ProposedPublishAt: mondayAt(9).AddDate(0, 0, 7).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, deniedRes.IsError)
	assert.Contains(t, sdTextOf(deniedRes), "permission denied")

	entries, err := f.st.Schedules().ListByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the denied outsider call must not have written a row -- only the Analyst's draft exists")
}

// ── get_drafting_context ─────────────────────────────────────────────────

func TestGetDraftingContext_NoPolicySet_ReturnsExplicitNilNotZeroValue(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "get_drafting_context", tools.GetDraftingContextInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))
	out := sdDecode[tools.GetDraftingContextOutput](t, res)
	assert.Nil(t, out.Policy, "no policy has been set -- must be nil, not a zero-valued policy that would misread as 0 uploads/week")
}

func TestGetDraftingContext_ReturnsPolicySyncedScheduleAndExistingEntries(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	f.call(t, cs, "set_pacing_policy", tools.SetPacingPolicyInput{
		ChannelID:            f.ch.ID.String(),
		TargetUploadsPerWeek: 4,
		PreferredDays:        []string{"Friday"},
	})

	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, f.st.Sync().UpsertVideos(context.Background(), f.ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "ctx-video-1",
		Title:          "A synced video",
		PrivacyStatus:  store.PrivacyStatusPublic,
		PublishedAt:    &publishedAt,
		LastSyncedAt:   time.Now(),
	}}))

	verdict := f.saveViableVerdict(t, cs, f.idea.ID, "viable, drafting context entry")
	f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(), VerdictID: verdict.ID,
		ProposedPublishAt: mondayAt(9).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})

	res := f.call(t, cs, "get_drafting_context", tools.GetDraftingContextInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))
	out := sdDecode[tools.GetDraftingContextOutput](t, res)

	require.NotNil(t, out.Policy)
	assert.Equal(t, float64(4), out.Policy.TargetUploadsPerWeek)
	assert.Equal(t, []string{"Friday"}, out.Policy.PreferredDays)

	syncedIDs := make([]string, len(out.SyncedSchedule))
	for i, v := range out.SyncedSchedule {
		syncedIDs[i] = v.YouTubeVideoID
	}
	assert.Contains(t, syncedIDs, "ctx-video-1")

	require.Len(t, out.ScheduleEntries, 1)
	assert.Equal(t, f.idea.ID.String(), out.ScheduleEntries[0].IdeaID)
}

// ── list_schedule_entries ─────────────────────────────────────────────────

func TestListScheduleEntries_ReturnsIdeaVerdictVersionStateAndTimestamps(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	verdict := f.saveViableVerdict(t, cs, f.idea.ID, "viable, for listing")
	saveRes := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID: f.ch.ID.String(), IdeaID: f.idea.ID.String(), VerdictID: verdict.ID,
		ProposedPublishAt: mondayAt(9).Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	})
	saved := sdDecode[tools.SaveScheduleDraftOutput](t, saveRes)

	res := f.call(t, cs, "list_schedule_entries", tools.ListScheduleEntriesInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))
	out := sdDecode[tools.ListScheduleEntriesOutput](t, res)

	require.Len(t, out.Entries, 1)
	entry := out.Entries[0]
	assert.Equal(t, saved.ScheduleEntryID, entry.ScheduleEntryID)
	assert.Equal(t, f.idea.ID.String(), entry.IdeaID)
	assert.Equal(t, f.idea.Title, entry.IdeaTitle)
	assert.Equal(t, verdict.ID, entry.VerdictID)
	assert.Equal(t, 1, entry.VerdictVersion)
	assert.Equal(t, "draft", entry.State)
	assert.Equal(t, f.creator.ID.String(), entry.CreatedByPersonID)
	assert.Equal(t, f.creator.DisplayName, entry.CreatedByDisplayName)
	assert.Nil(t, entry.ApprovedByPersonID)
}

func TestListScheduleEntries_EmptyChannel_ReturnsEmptyListNotError(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	res := f.call(t, cs, "list_schedule_entries", tools.ListScheduleEntriesInput{ChannelID: f.ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))
	out := sdDecode[tools.ListScheduleEntriesOutput](t, res)
	assert.Empty(t, out.Entries)
}

// ── commit_schedule_draft: FR19 Creator-only approval (issue #1648) ─────

// saveDraft is a convenience wrapper mirroring saveViableVerdict: as
// personID (via cs), record a viable verdict on ideaID and immediately
// save a draft for it, returning the rendered SaveScheduleDraftOutput.
func (f *scheduleDraftFixture) saveDraft(t *testing.T, cs *mcp.ClientSession, ideaID uuid.UUID, proposedPublishAt time.Time) tools.SaveScheduleDraftOutput {
	t.Helper()
	verdict := f.saveViableVerdict(t, cs, ideaID, "viable, for commit_schedule_draft")
	res := f.call(t, cs, "save_schedule_draft", tools.SaveScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		IdeaID:            ideaID.String(),
		VerdictID:         verdict.ID,
		ProposedPublishAt: proposedPublishAt.Format(time.RFC3339),
		IdempotencyKeyArg: uuid.NewString(),
	})
	return sdDecode[tools.SaveScheduleDraftOutput](t, res)
}

func TestCommitScheduleDraft_Creator_TransitionsDraftToCommitted_ReadBackFromDB(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, cs, f.idea.ID, mondayAt(9))

	res := f.call(t, cs, "commit_schedule_draft", tools.CommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))
	out := sdDecode[tools.ScheduleEntryOutput](t, res)
	assert.Equal(t, "committed", out.State)
	require.NotNil(t, out.ApprovedByPersonID)
	assert.Equal(t, f.creator.ID.String(), *out.ApprovedByPersonID)
	require.NotNil(t, out.ApprovedAt)

	entryID, err := uuid.Parse(draft.ScheduleEntryID)
	require.NoError(t, err)
	entry, err := f.st.Schedules().GetByID(context.Background(), entryID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateCommitted, entry.State, "the entry must actually be committed in the database, not just in the rendered response")
	require.NotNil(t, entry.ApprovedByPersonID)
	assert.Equal(t, f.creator.ID, *entry.ApprovedByPersonID)
}

func TestCommitScheduleDraft_AnalystDenied_OutsiderDenied_EntryStaysDraft(t *testing.T) {
	f := newScheduleDraftFixture(t)
	creatorCS := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, creatorCS, f.idea.ID, mondayAt(9))

	analystCS := f.connect(t, f.analyst.ID)
	analystRes := f.call(t, analystCS, "commit_schedule_draft", tools.CommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, analystRes.IsError, "an Analyst must not be able to commit a draft (FR19)")
	assert.Contains(t, sdTextOf(analystRes), "permission denied")

	outsiderCS := f.connect(t, f.outsider.ID)
	outsiderRes := f.call(t, outsiderCS, "commit_schedule_draft", tools.CommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, outsiderRes.IsError, "an unassociated Person must not be able to commit a draft")

	entryID, err := uuid.Parse(draft.ScheduleEntryID)
	require.NoError(t, err)
	entry, err := f.st.Schedules().GetByID(context.Background(), entryID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateDraft, entry.State, "both denied calls must have left the entry untouched")
}

func TestCommitScheduleDraft_AlreadyCommitted_RejectedAsConflict(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, cs, f.idea.ID, mondayAt(9))

	first := f.call(t, cs, "commit_schedule_draft", tools.CommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, first.IsError, "unexpected error: %s", sdTextOf(first))

	second := f.call(t, cs, "commit_schedule_draft", tools.CommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: uuid.NewString(), // deliberately a different key -- not a replay
	})
	assert.True(t, second.IsError, "committing an already-committed entry under a different key must be rejected, never a silent no-op")
}

func TestCommitScheduleDraft_Replay_SameKeyReturnsOriginalResult_NoDoubleApprove(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, cs, f.idea.ID, mondayAt(9))

	args := tools.CommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: "commit-replay-1",
	}
	first := sdDecode[tools.ScheduleEntryOutput](t, f.call(t, cs, "commit_schedule_draft", args))
	second := sdDecode[tools.ScheduleEntryOutput](t, f.call(t, cs, "commit_schedule_draft", args))
	assert.Equal(t, first, second, "an identical replay must return the same committed result, not error or double-approve")
}

func TestCommitScheduleDraft_UnknownOrCrossChannelEntry_Rejected(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	t.Run("unknown schedule_entry_id", func(t *testing.T) {
		res := f.call(t, cs, "commit_schedule_draft", tools.CommitScheduleDraftInput{
			ChannelID:         f.ch.ID.String(),
			ScheduleEntryID:   uuid.NewString(),
			IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError)
	})

	t.Run("schedule_entry_id belongs to a different channel", func(t *testing.T) {
		ctx := context.Background()
		otherCreator, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-sd-other-creator-"+uuid.NewString(), "sd-other-creator@example.com", "Other Creator")
		require.NoError(t, err)
		otherCh, err := f.st.Channels().Create(ctx, "yt-sd-other-"+uuid.NewString(), "Other Channel", otherCreator.ID)
		require.NoError(t, err)
		otherIdea, err := f.st.Ideas().FindOrCreate(ctx, otherCh.ID, "Other channel idea "+uuid.NewString(), otherCreator.ID)
		require.NoError(t, err)
		otherVerdict, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
			IdeaID:         otherIdea.ID,
			Verdict:        store.VerdictViable,
			Reasoning:      "viable, other channel",
			AuthorPersonID: otherCreator.ID,
			IdempotencyKey: uuid.NewString(),
		})
		require.NoError(t, err)
		otherEntry, err := f.st.Schedules().SaveDraft(ctx, store.SaveDraftInput{
			ChannelID:         otherCh.ID,
			IdeaID:            otherIdea.ID,
			VerdictID:         otherVerdict.ID,
			ProposedPublishAt: mondayAt(9),
			CreatedByPersonID: otherCreator.ID,
			IdempotencyKey:    uuid.NewString(),
		})
		require.NoError(t, err)

		res := f.call(t, cs, "commit_schedule_draft", tools.CommitScheduleDraftInput{
			ChannelID:         f.ch.ID.String(),
			ScheduleEntryID:   otherEntry.ID.String(),
			IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "a schedule_entry_id belonging to a different channel_id must be rejected")
		assert.Contains(t, sdTextOf(res), "does not belong to channel_id")
	})
}

// ── uncommit_schedule_draft: FR20 Creator-only reversal ──────────────────

func (f *scheduleDraftFixture) commitDraft(t *testing.T, cs *mcp.ClientSession, entryID string) tools.ScheduleEntryOutput {
	t.Helper()
	res := f.call(t, cs, "commit_schedule_draft", tools.CommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   entryID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	return sdDecode[tools.ScheduleEntryOutput](t, res)
}

func TestUncommitScheduleDraft_Creator_TransitionsCommittedBackToDraft_ClearsApprover(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, cs, f.idea.ID, mondayAt(9))
	f.commitDraft(t, cs, draft.ScheduleEntryID)

	res := f.call(t, cs, "uncommit_schedule_draft", tools.UncommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))
	out := sdDecode[tools.ScheduleEntryOutput](t, res)
	assert.Equal(t, "draft", out.State)
	assert.Nil(t, out.ApprovedByPersonID)
	assert.Nil(t, out.ApprovedAt)

	entryID, err := uuid.Parse(draft.ScheduleEntryID)
	require.NoError(t, err)
	entry, err := f.st.Schedules().GetByID(context.Background(), entryID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateDraft, entry.State, "the entry must actually be back to draft in the database")
	assert.Nil(t, entry.ApprovedByPersonID)
}

func TestUncommitScheduleDraft_AnalystDenied_StaysCommitted(t *testing.T) {
	f := newScheduleDraftFixture(t)
	creatorCS := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, creatorCS, f.idea.ID, mondayAt(9))
	f.commitDraft(t, creatorCS, draft.ScheduleEntryID)

	analystCS := f.connect(t, f.analyst.ID)
	res := f.call(t, analystCS, "uncommit_schedule_draft", tools.UncommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "an Analyst must not be able to un-commit an entry (FR20)")
	assert.Contains(t, sdTextOf(res), "permission denied")

	entryID, err := uuid.Parse(draft.ScheduleEntryID)
	require.NoError(t, err)
	entry, err := f.st.Schedules().GetByID(context.Background(), entryID)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduleStateCommitted, entry.State, "the denied call must not have changed anything")
}

func TestUncommitScheduleDraft_AlreadyDraft_RejectedAsConflict(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, cs, f.idea.ID, mondayAt(9))

	res := f.call(t, cs, "uncommit_schedule_draft", tools.UncommitScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "un-committing an entry that is still a draft must be rejected as a conflict")
}

// ── update_schedule_draft: FR20 Creator-only reschedule ───────────────────

func TestUpdateScheduleDraft_Creator_ChangesProposedPublishAt_ReadBackFromDB(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, cs, f.idea.ID, mondayAt(9))
	newTime := mondayAt(9).AddDate(0, 0, 1)

	res := f.call(t, cs, "update_schedule_draft", tools.UpdateScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		ProposedPublishAt: newTime.Format(time.RFC3339),
		IdempotencyKeyArg: uuid.NewString(),
	})
	require.False(t, res.IsError, "unexpected error: %s", sdTextOf(res))
	out := sdDecode[tools.SaveScheduleDraftOutput](t, res)
	assert.Equal(t, newTime.Format(time.RFC3339), out.ProposedPublishAt)

	entryID, err := uuid.Parse(draft.ScheduleEntryID)
	require.NoError(t, err)
	entry, err := f.st.Schedules().GetByID(context.Background(), entryID)
	require.NoError(t, err)
	assert.True(t, newTime.Equal(entry.ProposedPublishAt), "the new proposed_publish_at must be persisted in the database")
}

func TestUpdateScheduleDraft_AnalystDenied_TimeUnchanged(t *testing.T) {
	f := newScheduleDraftFixture(t)
	creatorCS := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, creatorCS, f.idea.ID, mondayAt(9))

	analystCS := f.connect(t, f.analyst.ID)
	res := f.call(t, analystCS, "update_schedule_draft", tools.UpdateScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		ProposedPublishAt: mondayAt(9).AddDate(0, 0, 3).Format(time.RFC3339),
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "an Analyst must not be able to reschedule an existing draft, even though it may create one (FR20)")
	assert.Contains(t, sdTextOf(res), "permission denied")

	entryID, err := uuid.Parse(draft.ScheduleEntryID)
	require.NoError(t, err)
	entry, err := f.st.Schedules().GetByID(context.Background(), entryID)
	require.NoError(t, err)
	assert.True(t, mondayAt(9).Equal(entry.ProposedPublishAt), "the denied call must not have changed proposed_publish_at")
}

func TestUpdateScheduleDraft_CommittedEntry_RejectedAsConflict(t *testing.T) {
	f := newScheduleDraftFixture(t)
	cs := f.connect(t, f.creator.ID)

	draft := f.saveDraft(t, cs, f.idea.ID, mondayAt(9))
	f.commitDraft(t, cs, draft.ScheduleEntryID)

	res := f.call(t, cs, "update_schedule_draft", tools.UpdateScheduleDraftInput{
		ChannelID:         f.ch.ID.String(),
		ScheduleEntryID:   draft.ScheduleEntryID,
		ProposedPublishAt: mondayAt(9).AddDate(0, 0, 5).Format(time.RFC3339),
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "a committed entry must be un-committed before it can be rescheduled")
}
