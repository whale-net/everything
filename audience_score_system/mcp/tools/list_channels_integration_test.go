//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// //audience_score_system/mcp/server/server_integration_test.go for the
// pattern this file follows: spin up a throwaway Postgres via dbtest,
// apply the real embedded migrations, host a real *mcp.Server over HTTP
// (server.NewHTTPHandler) via httptest.Server, and drive it with a real
// in-process MCP client -- so list_channels' identity-derived-from-context
// behavior (issue #1631) is proven against the real auth stack
// (server.PersonMiddleware), not a fake stand-in for it. list_channels has
// no Channel-scoping to test (it takes no channel_id -- see its own doc
// comment), so this file is deliberately smaller than the other
// *_integration_test.go files in this package.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:list_channels_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
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

// newListChannelsTestStack mirrors schedule_read_integration_test.go's
// newScheduleTestStack: an isolated Postgres via dbtest with every real
// embedded migration applied. Returns pg (not just its Pool) so callers
// that need a second, separately-traced pool against the same database
// (e.g. lcQueryCounter below) have its ConnString available.
func newListChannelsTestStack(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(pg.Pool), pg
}

// lcQueryCounter is a pgx.QueryTracer that counts every SQL statement
// issued through the pool it's attached to -- mirrors store_integration_
// test.go's queryCounter/tracedStore pattern (issues #1716/#1717), used
// here to prove FR26/NFR9: list_channels issues a bounded (not per-Channel)
// number of queries.
type lcQueryCounter struct{ n atomic.Int64 }

func (q *lcQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.n.Add(1)
	return ctx
}

func (q *lcQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// tracedListChannelsStore builds a second *store.Store against pg's
// database through a pool whose every query is counted by counter.
func tracedListChannelsStore(t *testing.T, pg *dbtest.Postgres, counter *lcQueryCounter) *store.Store {
	t.Helper()
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(pg.ConnString)
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = counter

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return store.New(pool)
}

// newTestCredentialStore builds the mcpauth.CredentialStore against pool's
// mcp_credential table (migration 006) -- mirrors
// schedule_read_integration_test.go's helper of the same name.
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

type lcBearerRoundTripper struct{ token string }

func (rt lcBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

func lcConnectAs(t *testing.T, ts *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: lcBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func lcTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// newListChannelsTestServer wires a real *mcp.Server with list_channels
// registered (mirroring ../main.go's production wiring), hosted over HTTP.
func newListChannelsTestServer(t *testing.T, st *store.Store, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterListChannels(reg, st.Access())

	handler := server.NewHTTPHandler(srv, newTestCredentialStore(t, pool), server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func lcCall(t *testing.T, cs *mcp.ClientSession) (*mcp.CallToolResult, tools.ListChannelsOutput) {
	t.Helper()
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_channels", Arguments: struct{}{}})
	require.NoError(t, err)
	if res.IsError {
		return res, tools.ListChannelsOutput{}
	}

	var out tools.ListChannelsOutput
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &out))
	return res, out
}

// ── list_channels ────────────────────────────────────────────────────────

func TestListChannels_ReturnsOnlyCallersChannelsWithRoleAndConnectionState(t *testing.T) {
	st, pg := newListChannelsTestStack(t)
	ctx := context.Background()
	creds := newTestCredentialStore(t, pg.Pool)

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-creator", "lc-creator@example.com", "Creator")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-analyst", "lc-analyst@example.com", "Analyst")
	require.NoError(t, err)
	coCreator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-cocreator", "lc-cocreator@example.com", "CoCreator")
	require.NoError(t, err)
	unassociated, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-unassoc", "lc-unassoc@example.com", "Unassociated")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-list-channels-1", "My Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))

	ts := newListChannelsTestServer(t, st, pg.Pool)

	t.Run("creator sees the channel with role creator", func(t *testing.T) {
		token, _, err := creds.Mint(ctx, creator.ID.String())
		require.NoError(t, err)
		cs := lcConnectAs(t, ts, token)
		res, out := lcCall(t, cs)
		require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))
		require.Len(t, out.Channels, 1)
		assert.Equal(t, ch.ID.String(), out.Channels[0].ChannelID)
		assert.Equal(t, "My Channel", out.Channels[0].Title)
		assert.Equal(t, string(store.ConnectionStateConnected), out.Channels[0].ConnectionState)
		assert.Equal(t, []string{string(store.RoleCreator)}, out.Channels[0].Roles)
	})

	t.Run("analyst sees the same channel with role analyst", func(t *testing.T) {
		token, _, err := creds.Mint(ctx, analyst.ID.String())
		require.NoError(t, err)
		cs := lcConnectAs(t, ts, token)
		res, out := lcCall(t, cs)
		require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))
		require.Len(t, out.Channels, 1)
		assert.Equal(t, ch.ID.String(), out.Channels[0].ChannelID)
		assert.Equal(t, []string{string(store.RoleAnalyst)}, out.Channels[0].Roles)
	})

	t.Run("co-creator sees the same channel with role co_creator (FR26)", func(t *testing.T) {
		token, _, err := creds.Mint(ctx, coCreator.ID.String())
		require.NoError(t, err)
		cs := lcConnectAs(t, ts, token)
		res, out := lcCall(t, cs)
		require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))
		require.Len(t, out.Channels, 1)
		assert.Equal(t, ch.ID.String(), out.Channels[0].ChannelID)
		assert.Equal(t, []string{string(store.RoleCoCreator)}, out.Channels[0].Roles)
	})

	t.Run("unassociated Person sees an empty list, not an error", func(t *testing.T) {
		token, _, err := creds.Mint(ctx, unassociated.ID.String())
		require.NoError(t, err)
		cs := lcConnectAs(t, ts, token)
		res, out := lcCall(t, cs)
		require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))
		assert.Empty(t, out.Channels)
	})
}

func TestListChannels_MultipleChannels_AllReturned(t *testing.T) {
	st, pg := newListChannelsTestStack(t)
	ctx := context.Background()
	creds := newTestCredentialStore(t, pg.Pool)

	person, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-multi", "lc-multi@example.com", "Multi")
	require.NoError(t, err)

	chA, err := st.Channels().Create(ctx, "yt-list-channels-a", "Channel A", person.ID)
	require.NoError(t, err)
	chB, err := st.Channels().Create(ctx, "yt-list-channels-b", "Channel B", person.ID)
	require.NoError(t, err)

	ts := newListChannelsTestServer(t, st, pg.Pool)
	token, _, err := creds.Mint(ctx, person.ID.String())
	require.NoError(t, err)

	cs := lcConnectAs(t, ts, token)
	res, out := lcCall(t, cs)
	require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))

	gotIDs := make([]string, len(out.Channels))
	for i, c := range out.Channels {
		gotIDs[i] = c.ChannelID
	}
	assert.ElementsMatch(t, []string{chA.ID.String(), chB.ID.String()}, gotIDs)
}

// TestListChannels_IssuesBoundedQueryCount_NFR9 is the concrete regression
// test for issue #1719's fix: the pre-#1719 implementation called
// store.RoleStore.RolesFor once per Channel inside a `for` loop, so its
// query count scaled with how many Channels the caller held a role on. The
// AccessStore.ChannelsWithRoleForPerson-backed implementation must issue
// the SAME number of queries whether the caller holds a role on one
// Channel or five.
func TestListChannels_IssuesBoundedQueryCount_NFR9(t *testing.T) {
	st, pg := newListChannelsTestStack(t)
	ctx := context.Background()
	creds := newTestCredentialStore(t, pg.Pool)

	person, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-nfr9", "lc-nfr9@example.com", "NFR9 Person")
	require.NoError(t, err)

	founder1, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-nfr9-f1", "lc-nfr9-f1@example.com", "Founder 1")
	require.NoError(t, err)
	ch1, err := st.Channels().Create(ctx, "yt-lc-nfr9-1", "Channel 1", founder1.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch1.ID, person.ID, store.RoleAnalyst, founder1.ID))

	counter := &lcQueryCounter{}
	tracedSt := tracedListChannelsStore(t, pg, counter)
	ts := newListChannelsTestServer(t, tracedSt, pg.Pool)

	token, _, err := creds.Mint(ctx, person.ID.String())
	require.NoError(t, err)
	cs := lcConnectAs(t, ts, token)

	counter.n.Store(0)
	res, out := lcCall(t, cs)
	require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))
	require.Len(t, out.Channels, 1)
	queriesForOneChannel := counter.n.Load()
	require.Greater(t, queriesForOneChannel, int64(0), "sanity: the call must issue at least one query")

	// Add four more Channels for the same Person (5 total) -- an N+1
	// implementation would issue proportionally more queries for the next
	// call; AccessStore.ChannelsWithRoleForPerson must not.
	for i := 0; i < 4; i++ {
		founder, _, err := st.Persons().UpsertByGoogleSubject(ctx,
			fmt.Sprintf("sub-lc-nfr9-f%d", i+2), fmt.Sprintf("lc-nfr9-f%d@example.com", i+2), fmt.Sprintf("Founder %d", i+2))
		require.NoError(t, err)
		ch, err := st.Channels().Create(ctx, fmt.Sprintf("yt-lc-nfr9-%d", i+2), fmt.Sprintf("Channel %d", i+2), founder.ID)
		require.NoError(t, err)
		require.NoError(t, st.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))
	}

	counter.n.Store(0)
	res2, out2 := lcCall(t, cs)
	require.False(t, res2.IsError, "unexpected error: %s", lcTextOf(res2))
	require.Len(t, out2.Channels, 5)
	queriesForFiveChannels := counter.n.Load()

	assert.Equal(t, queriesForOneChannel, queriesForFiveChannels,
		"list_channels must issue the same number of queries regardless of how many Channels the caller holds a role on (NFR9) -- a per-Channel RolesFor loop would scale this with Channel count")
}
