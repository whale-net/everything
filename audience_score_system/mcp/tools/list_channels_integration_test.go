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
	"crypto/ed25519"
	"crypto/rand"
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
	"github.com/whale-net/everything/libs/go/whagent"
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
	tools.RegisterListChannels(reg, st.Access(), st.Roles())

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

	// Root cause of #1855/#1854's intermittent failure here: the MCP
	// client sends "notifications/initialized" right after the initialize
	// handshake (mcp.Client.Connect), and go-sdk's PersonMiddleware
	// (server.New, auth.go) -- wired via mcp.Server.AddReceivingMiddleware
	// -- runs on EVERY received method, not just "tools/call", so that
	// notification ALSO issues a persons.GetByID query against this same
	// traced pool. go-sdk processes notifications synchronously and calls
	// synchronously within one session (jsonrpc2 handleAsync's queue,
	// go-sdk/mcp/server.go's "handle calls asynchronously, and
	// notifications synchronously" comment), so that query is guaranteed
	// to complete before any later call's response -- but Connect returns
	// as soon as the notification is *enqueued* (a 202 Accepted), not once
	// the server has actually processed it, so whether it lands before or
	// after our counter reset below is a genuine goroutine-scheduling
	// race, not an artifact of connection-pool warm-up or CI load (this
	// reproduced locally, standalone, with no concurrent store package
	// tests running, ruling out the added-CI-load lead in #1855/#1854).
	// A throwaway round trip here forces that notification's processing
	// (and its one-time query) to finish before we ever touch the
	// counter -- go-sdk's handleAsync loop dequeues session requests
	// strictly in the order they were enqueued and won't start this
	// call's handling until the notification's has completed -- so a
	// synchronous response to this call is proof the notification is
	// done. Every query below is then a real query list_channels itself
	// (or its identity-resolving PersonMiddleware) issues per call, with
	// no leftover protocol bookkeeping in either measurement.
	_, warmupOut := lcCall(t, cs)
	require.Len(t, warmupOut.Channels, 1, "warm-up call: sanity, same caller/data as the first measured call")

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

// ── FR11/FR12: whagent-path whole-Person zero-role linking discoverability ──

const lcWhagentAudience = "https://mcp.example.com"

// newListChannelsWhagentSignerVerifier mirrors
// whagent_auth_integration_test.go's newTestWhagentSignerVerifier, at the
// width this package's tests need to mint real Claim JWTs.
func newListChannelsWhagentSignerVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

// newListChannelsDualAuthTestServer mirrors newListChannelsTestServer, but
// additionally mounts the whagent-net auth path (server.WhagentPersonMiddleware
// + server.NewDualAuthHTTPHandler) alongside the existing mcp_credential
// path -- list_channels' own FR11 call to server.RequireChannelAccess only
// ever fires for a caller resolved via AuthPathWhagent, so exercising it
// requires a real whagent-authenticated call, not just an mcp_credential
// one.
func newListChannelsDualAuthTestServer(t *testing.T, st *store.Store, pool *pgxpool.Pool, verifier *whagent.Verifier) *httptest.Server {
	t.Helper()

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterListChannels(reg, st.Access(), st.Roles())

	srv.AddReceivingMiddleware(server.WhagentPersonMiddleware(st.PersonIdentities()))

	handler := server.NewDualAuthHTTPHandler(srv, newTestCredentialStore(t, pool), server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: lcWhagentAudience,
	}, server.ResourceMetadataConfig{
		Resource:            lcWhagentAudience,
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func lcMintWhagentToken(t *testing.T, signer *whagent.Signer, iss, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: iss,
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-1",
		Audience:      lcWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// TestListChannels_FR11_WhagentZeroRoleGetsLinkingMessage is FR11's core
// acceptance case for list_channels specifically (the other ChannelScoped
// tool path is covered by mcp/server's own registry-level tests): a
// whagent-authenticated caller who holds zero channel_person rows across
// every Channel gets a discoverable pointer to the linking flow, not a
// silent empty list.
func TestListChannels_FR11_WhagentZeroRoleGetsLinkingMessage(t *testing.T) {
	st, pg := newListChannelsTestStack(t)
	signer, verifier := newListChannelsWhagentSignerVerifier(t, "https://whagent.example.test")
	const iss, sub = "https://keycloak.example.test/realms/humans", "human-lc-fr11-1"

	ts := newListChannelsDualAuthTestServer(t, st, pg.Pool, verifier)
	cs := lcConnectAs(t, ts, lcMintWhagentToken(t, signer, iss, sub))

	res, out := lcCall(t, cs)
	require.True(t, res.IsError, "a whagent caller with zero roles anywhere must get an error result, not a silent empty list")
	assert.Empty(t, out.Channels)
	assert.Contains(t, lcTextOf(res), "Link ASS identity", "the message must name the linking action")
	assert.Contains(t, lcTextOf(res), "not linked", "the message must explain why the result is empty")
}

// TestListChannels_FR11_WhagentWithRoleOnSomeChannelSucceedsNormally proves
// the check is whole-Person, not per-call: a whagent-authenticated caller
// who holds a role on at least one Channel sees the ordinary result, with
// no linking-flow message anywhere in it.
func TestListChannels_FR11_WhagentWithRoleOnSomeChannelSucceedsNormally(t *testing.T) {
	st, pg := newListChannelsTestStack(t)
	ctx := context.Background()
	signer, verifier := newListChannelsWhagentSignerVerifier(t, "https://whagent.example.test")
	const iss, sub = "https://keycloak.example.test/realms/humans", "human-lc-fr11-2"

	// Auto-provision the whagent-resolved Person up front so a role can be
	// granted to it before the tool call.
	person, created, err := st.PersonIdentities().FindOrCreateByIssSub(ctx, iss, sub)
	require.NoError(t, err)
	require.True(t, created)

	founder, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-fr11-founder", "lc-fr11-founder@example.com", "Founder")
	require.NoError(t, err)
	ch, err := st.Channels().Create(ctx, "yt-lc-fr11-2", "FR11 Channel", founder.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))

	ts := newListChannelsDualAuthTestServer(t, st, pg.Pool, verifier)
	cs := lcConnectAs(t, ts, lcMintWhagentToken(t, signer, iss, sub))

	res, out := lcCall(t, cs)
	require.False(t, res.IsError, "unexpected error: %s", lcTextOf(res))
	require.Len(t, out.Channels, 1)
	assert.Equal(t, ch.ID.String(), out.Channels[0].ChannelID)
}

// TestListChannels_FR12_McpCredentialZeroRoleNeverGetsLinkingMessage is
// FR12's exclusion for list_channels: an mcp_credential caller with zero
// roles everywhere keeps getting the ordinary empty list (already proven
// generally by TestListChannels_ReturnsOnlyCallersChannelsWithRoleAndConnectionState's
// "unassociated" case above) -- this test additionally asserts the FR11
// message text is nowhere in that response, even with the whagent path
// also mounted on the same server.
func TestListChannels_FR12_McpCredentialZeroRoleNeverGetsLinkingMessage(t *testing.T) {
	st, pg := newListChannelsTestStack(t)
	ctx := context.Background()
	_, verifier := newListChannelsWhagentSignerVerifier(t, "https://whagent.example.test")
	creds := newTestCredentialStore(t, pg.Pool)

	unassociated, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-lc-fr12-unassoc", "lc-fr12-unassoc@example.com", "Unassociated")
	require.NoError(t, err)

	ts := newListChannelsDualAuthTestServer(t, st, pg.Pool, verifier)
	token, _, err := creds.Mint(ctx, unassociated.ID.String())
	require.NoError(t, err)
	cs := lcConnectAs(t, ts, token)

	res, out := lcCall(t, cs)
	require.False(t, res.IsError, "an mcp_credential caller with zero roles must get an ordinary empty list, not an error (FR12)")
	assert.Empty(t, out.Channels)
	assert.NotContains(t, lcTextOf(res), "Link ASS identity")
}
