//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// //audience_score_system/mcp/server/server_integration_test.go for the
// pattern this file follows: spin up a throwaway Postgres via dbtest,
// apply the real embedded migrations, host a real *mcp.Server over HTTP
// (server.NewHTTPHandler) via httptest.Server, and drive it with a real
// in-process MCP client -- so get_channel_schedule's Channel-scoping
// (store.CanRead, via server.RegisterRead) and from/to/include_drafts
// filtering are proven against the real auth/authz stack, not a fake
// stand-in for it (this task's #1576 Testing section).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:schedule_read_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
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

// newScheduleTestStack provisions an isolated Postgres database via
// dbtest, applies every migration in the real embedded schema, and
// returns a ready *store.Store plus the *pgxpool.Pool backing it (needed
// to construct mcpauth.CredentialStore) -- mirrors
// server_integration_test.go's newTestDB.
func newScheduleTestStack(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(pg.Pool), pg.Pool
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
// every request -- mirrors server_integration_test.go's own helper of the
// same name (this package cannot reach that one, package server_test).
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// connectAs opens a real streamable-HTTP MCP client session against ts,
// authenticated as token, and registers a t.Cleanup to close it.
func connectAs(t *testing.T, ts *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// textOf concatenates every TextContent block in res.Content -- the error
// message a rejected call's Content carries.
func textOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// videoIDs extracts YouTubeVideoID from out.Videos, in order -- the
// assertion shape every test below reads.
func videoIDs(out tools.GetChannelScheduleOutput) []string {
	ids := make([]string, len(out.Videos))
	for i, v := range out.Videos {
		ids[i] = v.YouTubeVideoID
	}
	return ids
}

// callSchedule calls get_channel_schedule via cs and decodes its
// StructuredContent into tools.GetChannelScheduleOutput.
func callSchedule(t *testing.T, cs *mcp.ClientSession, args tools.GetChannelScheduleInput) (*mcp.CallToolResult, tools.GetChannelScheduleOutput) {
	t.Helper()
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_channel_schedule", Arguments: args})
	require.NoError(t, err)
	if res.IsError {
		return res, tools.GetChannelScheduleOutput{}
	}

	var out tools.GetChannelScheduleOutput
	m, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "StructuredContent must decode to an object")
	videosRaw, _ := m["videos"].([]any)
	for _, vRaw := range videosRaw {
		v, ok := vRaw.(map[string]any)
		require.True(t, ok)
		id, _ := v["youtube_video_id"].(string)
		out.Videos = append(out.Videos, tools.ScheduleVideo{YouTubeVideoID: id})
	}
	return res, out
}

// newScheduleTestServer wires a real *mcp.Server with get_channel_schedule
// registered (mirroring ../main.go's production wiring), hosted over HTTP.
func newScheduleTestServer(t *testing.T, st *store.Store, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterGetChannelSchedule(reg, st.Sync())

	handler := server.NewHTTPHandler(srv, newTestCredentialStore(t, pool), server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// ── Channel-scoping: creator + analyst read, unassociated Person denied ────

func TestGetChannelSchedule_ChannelScoping_CreatorAndAnalystRead_UnassociatedDenied(t *testing.T) {
	st, pool := newScheduleTestStack(t)
	ctx := context.Background()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-creator", "creator@example.com", "Creator")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-analyst", "analyst@example.com", "Analyst")
	require.NoError(t, err)
	unassociated, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-unassoc", "unassoc@example.com", "Unassociated")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-schedule-scope-1", "Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	require.NoError(t, st.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "vid-1",
		Title:          "A video",
		PrivacyStatus:  store.PrivacyStatusPublic,
		LastSyncedAt:   time.Now(),
	}}))

	ts := newScheduleTestServer(t, st, pool)

	t.Run("creator reads the schedule", func(t *testing.T) {
		cs := connectAs(t, ts, mintScheduleToken(t, pool, creator.ID))
		res, out := callSchedule(t, cs, tools.GetChannelScheduleInput{ChannelID: ch.ID.String()})
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))
		assert.Equal(t, []string{"vid-1"}, videoIDs(out))
	})

	t.Run("analyst reads the schedule", func(t *testing.T) {
		cs := connectAs(t, ts, mintScheduleToken(t, pool, analyst.ID))
		res, out := callSchedule(t, cs, tools.GetChannelScheduleInput{ChannelID: ch.ID.String()})
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))
		assert.Equal(t, []string{"vid-1"}, videoIDs(out))
	})

	t.Run("unassociated Person is denied, sees no rows", func(t *testing.T) {
		cs := connectAs(t, ts, mintScheduleToken(t, pool, unassociated.ID))
		res, _ := callSchedule(t, cs, tools.GetChannelScheduleInput{ChannelID: ch.ID.String()})
		assert.True(t, res.IsError)
		assert.Contains(t, textOf(res), "permission denied")
	})
}

// ── include_drafts filtering ────────────────────────────────────────────

func TestGetChannelSchedule_IncludeDrafts_DefaultTrue_FalseOmitsDrafts(t *testing.T) {
	st, pool := newScheduleTestStack(t)
	ctx := context.Background()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-creator-2", "creator2@example.com", "Creator")
	require.NoError(t, err)
	ch, err := st.Channels().Create(ctx, "yt-schedule-drafts-1", "Channel", creator.ID)
	require.NoError(t, err)

	publishAt := time.Now().Add(48 * time.Hour)
	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, st.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{
		{
			YouTubeVideoID:   "vid-draft",
			Title:            "Scheduled draft",
			PrivacyStatus:    store.PrivacyStatusPrivate,
			PublishAt:        &publishAt,
			IsScheduledDraft: true,
			LastSyncedAt:     time.Now(),
		},
		{
			YouTubeVideoID: "vid-public",
			Title:          "Published",
			PrivacyStatus:  store.PrivacyStatusPublic,
			PublishedAt:    &publishedAt,
			LastSyncedAt:   time.Now(),
		},
	}))

	ts := newScheduleTestServer(t, st, pool)
	token := mintScheduleToken(t, pool, creator.ID)

	t.Run("default (unset) includes drafts", func(t *testing.T) {
		cs := connectAs(t, ts, token)
		res, out := callSchedule(t, cs, tools.GetChannelScheduleInput{ChannelID: ch.ID.String()})
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))
		assert.ElementsMatch(t, []string{"vid-draft", "vid-public"}, videoIDs(out))
	})

	t.Run("include_drafts=false omits scheduled drafts", func(t *testing.T) {
		includeDrafts := false
		cs := connectAs(t, ts, token)
		res, out := callSchedule(t, cs, tools.GetChannelScheduleInput{ChannelID: ch.ID.String(), IncludeDrafts: &includeDrafts})
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))
		assert.Equal(t, []string{"vid-public"}, videoIDs(out))
	})

	t.Run("include_drafts=true explicitly still includes drafts", func(t *testing.T) {
		includeDrafts := true
		cs := connectAs(t, ts, token)
		res, out := callSchedule(t, cs, tools.GetChannelScheduleInput{ChannelID: ch.ID.String(), IncludeDrafts: &includeDrafts})
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))
		assert.ElementsMatch(t, []string{"vid-draft", "vid-public"}, videoIDs(out))
	})
}

// ── from/to window filtering ────────────────────────────────────────────

func TestGetChannelSchedule_FromToWindow_FiltersByEffectiveTimestamp(t *testing.T) {
	st, pool := newScheduleTestStack(t)
	ctx := context.Background()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-creator-3", "creator3@example.com", "Creator")
	require.NoError(t, err)
	ch, err := st.Channels().Create(ctx, "yt-schedule-window-1", "Channel", creator.ID)
	require.NoError(t, err)

	now := time.Now()
	early := now.Add(-72 * time.Hour)
	mid := now.Add(-24 * time.Hour)
	late := now.Add(72 * time.Hour) // future publish_at (a scheduled draft)

	require.NoError(t, st.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{
		{YouTubeVideoID: "vid-early", Title: "Early", PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &early, LastSyncedAt: now},
		{YouTubeVideoID: "vid-mid", Title: "Mid", PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &mid, LastSyncedAt: now},
		{YouTubeVideoID: "vid-late", Title: "Late draft", PrivacyStatus: store.PrivacyStatusPrivate, PublishAt: &late, IsScheduledDraft: true, LastSyncedAt: now},
	}))

	ts := newScheduleTestServer(t, st, pool)
	token := mintScheduleToken(t, pool, creator.ID)

	from := now.Add(-48 * time.Hour)
	to := now.Add(48 * time.Hour)
	cs := connectAs(t, ts, token)
	res, out := callSchedule(t, cs, tools.GetChannelScheduleInput{ChannelID: ch.ID.String(), From: &from, To: &to})
	require.False(t, res.IsError, "unexpected error: %s", textOf(res))
	assert.Equal(t, []string{"vid-mid"}, videoIDs(out), "only the video whose effective timestamp falls within [from, to] must be returned")
}

// ── empty Channel: empty list, not an error ─────────────────────────────

func TestGetChannelSchedule_EmptyChannel_ReturnsEmptyListNotError(t *testing.T) {
	st, pool := newScheduleTestStack(t)
	ctx := context.Background()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-creator-4", "creator4@example.com", "Creator")
	require.NoError(t, err)
	ch, err := st.Channels().Create(ctx, "yt-schedule-empty-1", "Channel", creator.ID)
	require.NoError(t, err)

	ts := newScheduleTestServer(t, st, pool)
	cs := connectAs(t, ts, mintScheduleToken(t, pool, creator.ID))

	res, out := callSchedule(t, cs, tools.GetChannelScheduleInput{ChannelID: ch.ID.String()})
	require.False(t, res.IsError, "unexpected error: %s", textOf(res))
	assert.Empty(t, out.Videos, "a Channel with no synced videos must return an empty list, not an error")
}

// mintScheduleToken mints a real bearer credential for personID via
// mcpauth.CredentialStore (migration 006).
func mintScheduleToken(t *testing.T, pool *pgxpool.Pool, personID uuid.UUID) string {
	t.Helper()
	raw, _, err := newTestCredentialStore(t, pool).Mint(context.Background(), personID.String())
	require.NoError(t, err)
	return raw
}
