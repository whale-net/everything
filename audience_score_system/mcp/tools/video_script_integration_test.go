//go:build integration

// video_script_integration_test.go covers video_script.go's MCP surface
// (migration 010, milestone video-script-model, issues #1823/#1825):
// save_video_script's viable-verdict gate (FR36) and derived idea_id
// (LB3), the greenlight/deny/archive lifecycle trio's explicit
// store.CanApprove gate on top of RegisterWrite's automatic CanWrite gate
// (FR37/FR38/FR39), FR40's exhaustive transition matrix, FR39's publish
// freeze, NFR12 idempotency replay/conflict semantics on all four tools,
// and the Channel scope check on a cross-Channel video_script_id. See
// //audience_score_system/mcp/server/server_integration_test.go for the
// dbtest + real-*mcp.Server + real-in-process-MCP-client pattern this
// file follows, mirrored most directly from schedule_draft_integration_
// test.go.
//
// Deliberately its own file/self-contained build target (not linked with
// any other _integration_test.go file -- see BUILD.bazel, each such file
// is its own go_test with only itself in srcs), so every helper below
// (newVideoScriptTestDB, newTestCredentialStore, bearerRoundTripper,
// vsDecode/vsTextOf) is a same-named-but-independent copy of the pattern
// schedule_draft_integration_test.go/verdict_integration_test.go/etc.
// already established, not a shared import.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:video_script_integration_test --test_output=all
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

// newVideoScriptTestDB provisions an isolated Postgres database via dbtest
// and applies every migration in the package's own embedded schema,
// mirroring schedule_draft_integration_test.go's newScheduleDraftTestDB.
func newVideoScriptTestDB(t *testing.T) *dbtest.Postgres {
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

// bearerRoundTripper injects an "Authorization: Bearer <token>" header on
// every request -- mirrors schedule_draft_integration_test.go's.
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// videoScriptFixture is the common setup every test below needs: a
// Channel with a live Creator, Co-Creator, and Analyst, an unassociated
// Person with no role on it, and one Idea with a bound viable verdict and
// grounding Strategy already saved directly through the store (mirroring
// store/video_script_integration_test.go's setupVideoScriptChannel --
// there is no MCP tool for recording a verdict or strategy needed to
// prove this file's scope), hosted behind a real MCP server with
// RegisterVideoScript wired.
type videoScriptFixture struct {
	st        *store.Store
	creds     mcpauth.CredentialStore
	pg        *dbtest.Postgres
	ch        store.Channel
	creator   store.Person
	coCreator store.Person
	analyst   store.Person
	outsider  store.Person
	idea      store.Idea
	verdict   store.Verdict // viable, bound to idea
	strategy  store.StrategyDetail
	url       string
}

func newVideoScriptFixture(t *testing.T) *videoScriptFixture {
	t.Helper()
	ctx := context.Background()

	pg := newVideoScriptTestDB(t)
	st := store.New(pg.Pool)
	creds := newTestCredentialStore(t, pg.Pool)

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-vs-creator-"+uuid.NewString(), "vs-creator@example.com", "Creator Person")
	require.NoError(t, err)
	coCreator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-vs-co-creator-"+uuid.NewString(), "vs-co-creator@example.com", "Co-Creator Person")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-vs-analyst-"+uuid.NewString(), "vs-analyst@example.com", "Analyst Person")
	require.NoError(t, err)
	outsider, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-vs-outsider-"+uuid.NewString(), "vs-outsider@example.com", "Outsider Person")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-vs-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	idea, err := st.Ideas().Create(ctx, ch.ID, "Video Script Test Idea "+uuid.NewString(), creator.ID)
	require.NoError(t, err)

	verdict, err := st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "strong comps, ready to script", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	strategy, err := st.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: "Video Script Test Strategy", PreferredWeekday: "Tuesday", Active: true,
		VerdictIDs: []uuid.UUID{verdict.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterVideoScript(reg, st)

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &videoScriptFixture{
		st: st, creds: creds, pg: pg, ch: ch,
		creator: creator, coCreator: coCreator, analyst: analyst, outsider: outsider,
		idea: idea, verdict: verdict, strategy: strategy,
		url: ts.URL,
	}
}

// connect opens a real streamable-HTTP MCP client session authenticated as
// personID.
func (f *videoScriptFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
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

func (f *videoScriptFixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// propose is the shared save_video_script call every Greenlight/Deny/
// Archive test below starts from: as personID (via cs), propose a
// video_script against f.verdict/f.strategy and return its rendered
// VideoScriptOutput.
func (f *videoScriptFixture) propose(t *testing.T, cs *mcp.ClientSession, title, key string) tools.VideoScriptOutput {
	t.Helper()
	res := f.call(t, cs, "save_video_script", tools.SaveVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VerdictID:         f.verdict.ID.String(),
		StrategyID:        f.strategy.ID.String(),
		Title:             title,
		ScriptText:        "script text for " + title,
		IdempotencyKeyArg: key,
	})
	return vsDecode[tools.VideoScriptOutput](t, res)
}

// vsTextOf concatenates every TextContent block in res.Content -- the
// error message a rejected call's Content carries.
func vsTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func vsDecode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	require.False(t, res.IsError, "unexpected tool error: %s", vsTextOf(res))
	require.NoError(t, vsMapDecode(res.StructuredContent, &out))
	return out
}

func vsMapDecode(v any, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// syncPublishedVideo upserts one already-published synced_video for f.ch
// and returns it by looking it back up via its unique youtube_video_id.
func (f *videoScriptFixture) syncPublishedVideo(t *testing.T, title string) store.SyncedVideo {
	t.Helper()
	ctx := context.Background()

	ytID := "yt-" + uuid.NewString()
	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, f.st.Sync().UpsertVideos(ctx, f.ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))

	synced, _, err := f.st.Sync().ListSchedule(ctx, f.ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	for _, sv := range synced {
		if sv.YouTubeVideoID == ytID {
			return sv
		}
	}
	t.Fatalf("synced video %s not found after upsert", ytID)
	return store.SyncedVideo{}
}

// recordMatch inserts a video_schedule_match row wired to scriptID via
// video_script_id (migration 010's FR45 re-anchor column) -- directly by
// SQL rather than through MatchStore.Record, mirroring store/video_
// script_integration_test.go's recordVideoScriptMatch (that file's own
// helper is unexported to package store_test and not reachable from
// here).
func (f *videoScriptFixture) recordMatch(t *testing.T, scriptID, syncedVideoID uuid.UUID, state store.MatchState) {
	t.Helper()
	_, err := f.pg.Pool.Exec(context.Background(), `
		INSERT INTO video_schedule_match (synced_video_id, video_script_id, confidence, state)
		VALUES ($1, $2, 0.9, $3)
	`, syncedVideoID, scriptID, state)
	require.NoError(t, err)
}

// ── save_video_script (FR36, C18) ────────────────────────────────────────

func TestSaveVideoScript_ViableVerdict_ReturnsCreatedScript_ProposedStatus_DerivedIdeaID(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)

	out := f.propose(t, cs, "Viable Verdict Script", uuid.NewString())
	assert.Equal(t, "proposed", out.Status)
	assert.Equal(t, f.idea.ID.String(), out.IdeaID, "idea_id must be derived from verdict_id (LB3), never a caller-supplied field")
	assert.Equal(t, f.idea.Title, out.IdeaTitle)
	assert.Equal(t, f.verdict.ID.String(), out.VerdictID)
	assert.Equal(t, 1, out.VerdictVersion)
	assert.Equal(t, "viable", out.VerdictValue)
	assert.Equal(t, f.strategy.ID.String(), out.StrategyID)

	scriptID, err := uuid.Parse(out.VideoScriptID)
	require.NoError(t, err)
	script, err := f.st.VideoScripts().GetByID(context.Background(), scriptID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusProposed, script.Status)
	assert.Equal(t, f.idea.ID, script.IdeaID)
}

func TestSaveVideoScript_NonViableVerdict_ErrorsNothingWritten(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)
	ctx := context.Background()

	notViableIdea, err := f.st.Ideas().Create(ctx, f.ch.ID, "Not Viable Idea "+uuid.NewString(), f.creator.ID)
	require.NoError(t, err)
	notViable, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: notViableIdea.ID, Verdict: store.VerdictNotViable, Reasoning: "too niche", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)

	res := f.call(t, cs, "save_video_script", tools.SaveVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VerdictID:         notViable.ID.String(),
		StrategyID:        f.strategy.ID.String(),
		Title:             "Should Not Be Created",
		ScriptText:        "text",
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "a not-viable verdict must be rejected (FR36)")
	assert.Contains(t, vsTextOf(res), notViable.ID.String())

	scripts, err := f.st.VideoScripts().ListByChannel(ctx, f.ch.ID)
	require.NoError(t, err)
	for _, s := range scripts {
		assert.NotEqual(t, notViableIdea.ID, s.IdeaID, "no video_script row must exist for a rejected non-viable verdict")
	}
}

func TestSaveVideoScript_Replay_SameKeySameArgs_ReturnsOriginal_ExactlyOneRow(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)

	args := tools.SaveVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VerdictID:         f.verdict.ID.String(),
		StrategyID:        f.strategy.ID.String(),
		Title:             "Replay Script",
		ScriptText:        "replay script text",
		IdempotencyKeyArg: "save-video-script-replay-1",
	}
	first := vsDecode[tools.VideoScriptOutput](t, f.call(t, cs, "save_video_script", args))
	second := vsDecode[tools.VideoScriptOutput](t, f.call(t, cs, "save_video_script", args))
	assert.Equal(t, first, second, "an identical replay must return the original result, not a second write (NFR12)")

	scripts, err := f.st.VideoScripts().ListByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	count := 0
	for _, s := range scripts {
		if s.Title == "Replay Script" {
			count++
		}
	}
	assert.Equal(t, 1, count, "a replayed save_video_script must create exactly one row")
}

func TestSaveVideoScript_Replay_SameKeyDifferentArgs_IsConflict_StillOneRow(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)

	key := "save-video-script-conflict-1"
	first := f.call(t, cs, "save_video_script", tools.SaveVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VerdictID:         f.verdict.ID.String(),
		StrategyID:        f.strategy.ID.String(),
		Title:             "Conflict Script Original",
		ScriptText:        "original text",
		IdempotencyKeyArg: key,
	})
	require.False(t, first.IsError, "unexpected error: %s", vsTextOf(first))

	second := f.call(t, cs, "save_video_script", tools.SaveVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VerdictID:         f.verdict.ID.String(),
		StrategyID:        f.strategy.ID.String(),
		Title:             "Conflict Script Different Title",
		ScriptText:        "different text",
		IdempotencyKeyArg: key,
	})
	assert.True(t, second.IsError, "reusing the same idempotency_key with different args must be rejected as a conflict, never a silent second write")

	scripts, err := f.st.VideoScripts().ListByChannel(context.Background(), f.ch.ID)
	require.NoError(t, err)
	count := 0
	for _, s := range scripts {
		if s.Title == "Conflict Script Original" || s.Title == "Conflict Script Different Title" {
			count++
		}
	}
	assert.Equal(t, 1, count, "a same-key-different-args conflict must not create a second row")
}

func TestSaveVideoScript_Analyst_Succeeds(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.analyst.ID)

	out := f.propose(t, cs, "Analyst Proposed Script", uuid.NewString())
	assert.Equal(t, "proposed", out.Status, "an Analyst may propose a video_script -- propose is CanWrite, not CanApprove-gated (NFR13)")
}

// ── greenlight_video_script (FR37, C19) ──────────────────────────────────

func TestGreenlightVideoScript_Analyst_Rejected_Founder_And_CoCreator_Succeed(t *testing.T) {
	f := newVideoScriptFixture(t)
	creatorCS := f.connect(t, f.creator.ID)

	t.Run("Analyst rejected, status unchanged", func(t *testing.T) {
		script := f.propose(t, creatorCS, "Greenlight Analyst Denied Script", uuid.NewString())
		analystCS := f.connect(t, f.analyst.ID)
		res := f.call(t, analystCS, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID:         f.ch.ID.String(),
			VideoScriptID:     script.VideoScriptID,
			IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "an Analyst must not be able to greenlight a video_script (FR37)")
		assert.Contains(t, vsTextOf(res), "Founder or Co-Creator")

		scriptID, err := uuid.Parse(script.VideoScriptID)
		require.NoError(t, err)
		got, err := f.st.VideoScripts().GetByID(context.Background(), scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusProposed, got.Status, "a denied greenlight attempt must leave status unchanged")
	})

	t.Run("Founder succeeds", func(t *testing.T) {
		script := f.propose(t, creatorCS, "Greenlight Founder Script", uuid.NewString())
		res := f.call(t, creatorCS, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID:         f.ch.ID.String(),
			VideoScriptID:     script.VideoScriptID,
			IdempotencyKeyArg: uuid.NewString(),
		})
		out := vsDecode[tools.VideoScriptOutput](t, res)
		assert.Equal(t, "greenlit", out.Status)
		require.NotNil(t, out.DecidedByPersonID)
		assert.Equal(t, f.creator.ID.String(), *out.DecidedByPersonID)
	})

	t.Run("Co-Creator succeeds (FR37 Founder/Co-Creator symmetry)", func(t *testing.T) {
		script := f.propose(t, creatorCS, "Greenlight Co-Creator Script", uuid.NewString())
		coCreatorCS := f.connect(t, f.coCreator.ID)
		res := f.call(t, coCreatorCS, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID:         f.ch.ID.String(),
			VideoScriptID:     script.VideoScriptID,
			IdempotencyKeyArg: uuid.NewString(),
		})
		out := vsDecode[tools.VideoScriptOutput](t, res)
		assert.Equal(t, "greenlit", out.Status)
		require.NotNil(t, out.DecidedByPersonID)
		assert.Equal(t, f.coCreator.ID.String(), *out.DecidedByPersonID, "the recorded decider must be the calling Co-Creator, not the Founder")
	})
}

func TestGreenlightVideoScript_Replay_SameKeyReturnsOriginal_NoDoubleTransition(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)

	script := f.propose(t, cs, "Greenlight Replay Script", uuid.NewString())
	args := tools.GreenlightVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VideoScriptID:     script.VideoScriptID,
		IdempotencyKeyArg: "greenlight-replay-1",
	}
	first := vsDecode[tools.VideoScriptOutput](t, f.call(t, cs, "greenlight_video_script", args))
	second := vsDecode[tools.VideoScriptOutput](t, f.call(t, cs, "greenlight_video_script", args))
	assert.Equal(t, first, second, "an identical replay must return the same greenlit result, not error or re-transition")
}

// ── deny_video_script (FR38, C19) ────────────────────────────────────────

func TestDenyVideoScript_Analyst_Rejected_Creator_MovesProposedToDenied(t *testing.T) {
	f := newVideoScriptFixture(t)
	creatorCS := f.connect(t, f.creator.ID)

	t.Run("Analyst rejected, status unchanged", func(t *testing.T) {
		script := f.propose(t, creatorCS, "Deny Analyst Denied Script", uuid.NewString())
		analystCS := f.connect(t, f.analyst.ID)
		res := f.call(t, analystCS, "deny_video_script", tools.DenyVideoScriptInput{
			ChannelID:         f.ch.ID.String(),
			VideoScriptID:     script.VideoScriptID,
			IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "an Analyst must not be able to deny a video_script (FR38)")

		scriptID, err := uuid.Parse(script.VideoScriptID)
		require.NoError(t, err)
		got, err := f.st.VideoScripts().GetByID(context.Background(), scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusProposed, got.Status)
	})

	t.Run("Creator denies, proposed -> denied", func(t *testing.T) {
		script := f.propose(t, creatorCS, "Deny Creator Script", uuid.NewString())
		res := f.call(t, creatorCS, "deny_video_script", tools.DenyVideoScriptInput{
			ChannelID:         f.ch.ID.String(),
			VideoScriptID:     script.VideoScriptID,
			IdempotencyKeyArg: uuid.NewString(),
		})
		out := vsDecode[tools.VideoScriptOutput](t, res)
		assert.Equal(t, "denied", out.Status)

		scriptID, err := uuid.Parse(script.VideoScriptID)
		require.NoError(t, err)
		got, err := f.st.VideoScripts().GetByID(context.Background(), scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusDenied, got.Status, "the transition must be persisted, not just rendered")
	})
}

func TestDenyVideoScript_Replay_SameKeyReturnsOriginal_NoDoubleTransition(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)

	script := f.propose(t, cs, "Deny Replay Script", uuid.NewString())
	args := tools.DenyVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VideoScriptID:     script.VideoScriptID,
		IdempotencyKeyArg: "deny-replay-1",
	}
	first := vsDecode[tools.VideoScriptOutput](t, f.call(t, cs, "deny_video_script", args))
	second := vsDecode[tools.VideoScriptOutput](t, f.call(t, cs, "deny_video_script", args))
	assert.Equal(t, first, second)
}

// ── archive_video_script (FR39, C19) ─────────────────────────────────────

func TestArchiveVideoScript_GreenlitNoLiveMatch_Succeeds(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)

	script := f.propose(t, cs, "Archive No Match Script", uuid.NewString())
	f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
		ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
	})

	res := f.call(t, cs, "archive_video_script", tools.ArchiveVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VideoScriptID:     script.VideoScriptID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	out := vsDecode[tools.VideoScriptOutput](t, res)
	assert.Equal(t, "archived", out.Status)
}

func TestArchiveVideoScript_LiveMatchToPublishedVideo_ErrorsFrozen_StatusStaysGreenlit(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)

	for _, state := range []store.MatchState{store.MatchStateAuto, store.MatchStateConfirmed} {
		t.Run(string(state), func(t *testing.T) {
			script := f.propose(t, cs, "Archive Frozen Script "+string(state), uuid.NewString())
			f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
				ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
			})

			video := f.syncPublishedVideo(t, "Frozen Video "+string(state))
			scriptID, err := uuid.Parse(script.VideoScriptID)
			require.NoError(t, err)
			f.recordMatch(t, scriptID, video.ID, state)

			res := f.call(t, cs, "archive_video_script", tools.ArchiveVideoScriptInput{
				ChannelID:         f.ch.ID.String(),
				VideoScriptID:     script.VideoScriptID,
				IdempotencyKeyArg: uuid.NewString(),
			})
			assert.True(t, res.IsError, "archiving a video_script with a live match to an already-published video must be rejected (FR39)")
			assert.Contains(t, vsTextOf(res), "already been published")

			got, err := f.st.VideoScripts().GetByID(context.Background(), scriptID)
			require.NoError(t, err)
			assert.Equal(t, store.VideoScriptStatusGreenlit, got.Status, "a frozen archive attempt must leave status unchanged (no partial write)")
		})
	}
}

func TestArchiveVideoScript_Analyst_Rejected(t *testing.T) {
	f := newVideoScriptFixture(t)
	creatorCS := f.connect(t, f.creator.ID)

	script := f.propose(t, creatorCS, "Archive Analyst Denied Script", uuid.NewString())
	f.call(t, creatorCS, "greenlight_video_script", tools.GreenlightVideoScriptInput{
		ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
	})

	analystCS := f.connect(t, f.analyst.ID)
	res := f.call(t, analystCS, "archive_video_script", tools.ArchiveVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VideoScriptID:     script.VideoScriptID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "an Analyst must not be able to archive a video_script (FR39)")

	scriptID, err := uuid.Parse(script.VideoScriptID)
	require.NoError(t, err)
	got, err := f.st.VideoScripts().GetByID(context.Background(), scriptID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusGreenlit, got.Status)
}

func TestArchiveVideoScript_Replay_SameKeyReturnsOriginal_NoDoubleTransition(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)

	script := f.propose(t, cs, "Archive Replay Script", uuid.NewString())
	f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
		ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
	})

	args := tools.ArchiveVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VideoScriptID:     script.VideoScriptID,
		IdempotencyKeyArg: "archive-replay-1",
	}
	first := vsDecode[tools.VideoScriptOutput](t, f.call(t, cs, "archive_video_script", args))
	second := vsDecode[tools.VideoScriptOutput](t, f.call(t, cs, "archive_video_script", args))
	assert.Equal(t, first, second)
}

// ── FR40: exhaustive invalid transition matrix ───────────────────────────

func TestVideoScriptLifecycle_InvalidTransitions_ErrorNoStateChange(t *testing.T) {
	f := newVideoScriptFixture(t)
	cs := f.connect(t, f.creator.ID)
	ctx := context.Background()

	assertUnchanged := func(t *testing.T, scriptIDStr string, want store.VideoScriptStatus) {
		t.Helper()
		scriptID, err := uuid.Parse(scriptIDStr)
		require.NoError(t, err)
		got, err := f.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		assert.Equal(t, want, got.Status, "an invalid transition must leave status unchanged (FR40)")
	}

	t.Run("greenlit -> greenlit rejected", func(t *testing.T) {
		script := f.propose(t, cs, "FR40 Greenlit Twice Script", uuid.NewString())
		f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		res := f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(), // different key: not a replay
		})
		assert.True(t, res.IsError, "greenlighting an already-greenlit script must be rejected")
		assertUnchanged(t, script.VideoScriptID, store.VideoScriptStatusGreenlit)
	})

	t.Run("denied -> greenlight rejected", func(t *testing.T) {
		script := f.propose(t, cs, "FR40 Denied Then Greenlight Script", uuid.NewString())
		f.call(t, cs, "deny_video_script", tools.DenyVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		res := f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "denied is terminal -- greenlighting a denied script must be rejected")
		assertUnchanged(t, script.VideoScriptID, store.VideoScriptStatusDenied)
	})

	t.Run("denied -> deny rejected", func(t *testing.T) {
		script := f.propose(t, cs, "FR40 Denied Twice Script", uuid.NewString())
		f.call(t, cs, "deny_video_script", tools.DenyVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		res := f.call(t, cs, "deny_video_script", tools.DenyVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "denied is terminal -- denying an already-denied script must be rejected")
		assertUnchanged(t, script.VideoScriptID, store.VideoScriptStatusDenied)
	})

	t.Run("denied -> archive rejected", func(t *testing.T) {
		script := f.propose(t, cs, "FR40 Denied Then Archive Script", uuid.NewString())
		f.call(t, cs, "deny_video_script", tools.DenyVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		res := f.call(t, cs, "archive_video_script", tools.ArchiveVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "denied is terminal -- archiving a denied script must be rejected")
		assertUnchanged(t, script.VideoScriptID, store.VideoScriptStatusDenied)
	})

	t.Run("archived -> greenlight rejected", func(t *testing.T) {
		script := f.propose(t, cs, "FR40 Archived Then Greenlight Script", uuid.NewString())
		f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		f.call(t, cs, "archive_video_script", tools.ArchiveVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		res := f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "archived is terminal -- greenlighting an archived script must be rejected")
		assertUnchanged(t, script.VideoScriptID, store.VideoScriptStatusArchived)
	})

	t.Run("archived -> deny rejected", func(t *testing.T) {
		script := f.propose(t, cs, "FR40 Archived Then Deny Script", uuid.NewString())
		f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		f.call(t, cs, "archive_video_script", tools.ArchiveVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		res := f.call(t, cs, "deny_video_script", tools.DenyVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "archived is terminal -- denying an archived script must be rejected")
		assertUnchanged(t, script.VideoScriptID, store.VideoScriptStatusArchived)
	})

	t.Run("archived -> archive rejected", func(t *testing.T) {
		script := f.propose(t, cs, "FR40 Archived Twice Script", uuid.NewString())
		f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		f.call(t, cs, "archive_video_script", tools.ArchiveVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		res := f.call(t, cs, "archive_video_script", tools.ArchiveVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "archiving an already-archived script must be rejected")
		assertUnchanged(t, script.VideoScriptID, store.VideoScriptStatusArchived)
	})

	t.Run("proposed -> archive rejected", func(t *testing.T) {
		script := f.propose(t, cs, "FR40 Proposed Direct Archive Script", uuid.NewString())
		res := f.call(t, cs, "archive_video_script", tools.ArchiveVideoScriptInput{
			ChannelID: f.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "only greenlit -> archived is allowed -- archiving a merely-proposed script must be rejected")
		assertUnchanged(t, script.VideoScriptID, store.VideoScriptStatusProposed)
	})
}

// ── Channel scope check ──────────────────────────────────────────────────

func TestGreenlightVideoScript_CrossChannelVideoScriptID_Rejected(t *testing.T) {
	f := newVideoScriptFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.creator.ID)

	// A second Channel/Creator/Idea/verdict/strategy in the SAME store as
	// f -- otherScript is proposed against otherCh, never f.ch, so this
	// proves the explicit Channel scope check inside the mutate function
	// (not RegisterWrite's automatic gate, which only ever authorizes
	// against the CALLER-SUPPLIED channel_id, f.ch here, and would pass
	// f.creator through as f.ch's Founder regardless).
	otherCreator, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-vs-other-creator-"+uuid.NewString(), "vs-other-creator@example.com", "Other Creator")
	require.NoError(t, err)
	otherCh, err := f.st.Channels().Create(ctx, "yt-vs-other-"+uuid.NewString(), "Other Channel", otherCreator.ID)
	require.NoError(t, err)
	otherIdea, err := f.st.Ideas().Create(ctx, otherCh.ID, "Other Channel Idea "+uuid.NewString(), otherCreator.ID)
	require.NoError(t, err)
	otherVerdict, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: otherIdea.ID, Verdict: store.VerdictViable, Reasoning: "viable, other channel", AuthorPersonID: otherCreator.ID,
	})
	require.NoError(t, err)
	otherStrategy, err := f.st.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: otherCh.ID, Title: "Other Channel Strategy", PreferredWeekday: "Tuesday", Active: true,
		VerdictIDs: []uuid.UUID{otherVerdict.ID}, CreatedByPersonID: otherCreator.ID,
	})
	require.NoError(t, err)
	otherCS := f.connect(t, otherCreator.ID)
	otherScript := vsDecode[tools.VideoScriptOutput](t, f.call(t, otherCS, "save_video_script", tools.SaveVideoScriptInput{
		ChannelID:         otherCh.ID.String(),
		VerdictID:         otherVerdict.ID.String(),
		StrategyID:        otherStrategy.ID.String(),
		Title:             "Other Channel Script",
		ScriptText:        "other channel script text",
		IdempotencyKeyArg: uuid.NewString(),
	}))

	// f.creator has Founder authority on f.ch (CanWrite and CanApprove both
	// pass for f.ch), but otherScript.VideoScriptID belongs to otherCh --
	// the explicit Channel scope check inside the mutate function must
	// still reject this.
	res := f.call(t, cs, "greenlight_video_script", tools.GreenlightVideoScriptInput{
		ChannelID:         f.ch.ID.String(),
		VideoScriptID:     otherScript.VideoScriptID,
		IdempotencyKeyArg: uuid.NewString(),
	})
	assert.True(t, res.IsError, "a video_script_id belonging to a different Channel must be rejected by the Channel scope check")
	assert.Contains(t, vsTextOf(res), "does not belong to channel_id")

	otherScriptID, err := uuid.Parse(otherScript.VideoScriptID)
	require.NoError(t, err)
	got, err := f.st.VideoScripts().GetByID(ctx, otherScriptID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusProposed, got.Status, "the rejected cross-channel call must not have touched the other Channel's script")
}
