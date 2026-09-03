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
	"net/http"
	"net/http/httptest"
	"testing"

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

// newListChannelsTestStack mirrors schedule_read_integration_test.go's
// newScheduleTestStack: an isolated Postgres via dbtest with every real
// embedded migration applied.
func newListChannelsTestStack(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(pg.Pool)
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
func newListChannelsTestServer(t *testing.T, st *store.Store) *httptest.Server {
	t.Helper()

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterListChannels(reg, st.Roles())

	handler := server.NewHTTPHandler(srv, st.Credentials())
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
	st := newListChannelsTestStack(t)
	ctx := context.Background()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-creator", "lc-creator@example.com", "Creator")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-analyst", "lc-analyst@example.com", "Analyst")
	require.NoError(t, err)
	unassociated, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-unassoc", "lc-unassoc@example.com", "Unassociated")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-list-channels-1", "My Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst))

	ts := newListChannelsTestServer(t, st)

	t.Run("creator sees the channel with role creator", func(t *testing.T) {
		token, _, err := st.Credentials().Mint(ctx, creator.ID)
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
		token, _, err := st.Credentials().Mint(ctx, analyst.ID)
		require.NoError(t, err)
		cs := lcConnectAs(t, ts, token)
		res, out := lcCall(t, cs)
		require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))
		require.Len(t, out.Channels, 1)
		assert.Equal(t, ch.ID.String(), out.Channels[0].ChannelID)
		assert.Equal(t, []string{string(store.RoleAnalyst)}, out.Channels[0].Roles)
	})

	t.Run("unassociated Person sees an empty list, not an error", func(t *testing.T) {
		token, _, err := st.Credentials().Mint(ctx, unassociated.ID)
		require.NoError(t, err)
		cs := lcConnectAs(t, ts, token)
		res, out := lcCall(t, cs)
		require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))
		assert.Empty(t, out.Channels)
	})
}

func TestListChannels_MultipleChannels_AllReturned(t *testing.T) {
	st := newListChannelsTestStack(t)
	ctx := context.Background()

	person, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-multi", "lc-multi@example.com", "Multi")
	require.NoError(t, err)

	chA, err := st.Channels().Create(ctx, "yt-list-channels-a", "Channel A", person.ID)
	require.NoError(t, err)
	chB, err := st.Channels().Create(ctx, "yt-list-channels-b", "Channel B", person.ID)
	require.NoError(t, err)

	ts := newListChannelsTestServer(t, st)
	token, _, err := st.Credentials().Mint(ctx, person.ID)
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
