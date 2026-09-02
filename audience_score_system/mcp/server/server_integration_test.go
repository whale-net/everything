//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// //audience_score_system/store/store_integration_test.go for the pattern
// this file follows: spin up a throwaway Postgres via dbtest, apply the
// real embedded migrations, then exercise this package's public API
// against it.
//
// registry_test.go's pure-Go suite (package server, no build tag) already
// covers RegisterRead/RegisterWrite's wiring logic against fakes. What
// this file proves instead is exactly what a fake cannot:
//   - the full caller-auth stack end to end -- a real bearer credential
//     minted via store.CredentialStore, verified by auth.RequireBearerToken
//     over a real HTTP connection, resolved to a Person by PersonMiddleware
//     -- using a real in-process MCP client (mcp.NewClient +
//     StreamableClientTransport) against an httptest.Server wrapping
//     server.NewHTTPHandler, per this task's Testing criteria;
//   - that concurrent calls carrying the same idempotency key are
//     serialized by Postgres itself, not by any in-memory lock this
//     package might otherwise be tempted to add (NFR2/LB4); and
//   - that two INDEPENDENTLY CONSTRUCTED server instances (separate
//     *pgxpool.Pool connections, separate *mcp.Server, separate
//     httptest.Server) sharing only the same underlying database correctly
//     replay a write across each other -- the test that actually proves
//     LB4's statelessness invariant rather than merely asserting it in a
//     doc comment.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/server:server_integration_test --test_output=all
package server_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// mcpTestWriteResultSchema is a scratch table, self-contained and unrelated
// to any real migration, purely so this file's fake write tool can persist
// its "how many times has mutate actually run, globally" state IN
// POSTGRES rather than in an in-memory map -- the whole point of the
// cross-instance statelessness test below is that a second, completely
// independent server instance's render step must be able to read this
// back with no in-process state shared with the first instance.
const mcpTestWriteResultSchema = `
CREATE TABLE mcp_test_write_result (
    id    UUID PRIMARY KEY,
    calls INT  NOT NULL
);
`

// newTestDB provisions an isolated Postgres database via dbtest (plus the
// mcpTestWriteResultSchema scratch table) and applies every migration in
// the package's own embedded schema, mirroring
// store_integration_test.go's newStore.
func newTestDB(t *testing.T) *dbtest.Postgres {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: mcpTestWriteResultSchema})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return pg
}

// newIndependentStore opens a brand new *pgxpool.Pool against pg's
// database (NOT pg.Pool, which store_integration_test.go's tests reach
// past to assert on raw rows) and wraps it in a *store.Store -- used by
// the statelessness test to construct two server instances that share
// nothing but the underlying database, exactly like two independent
// process instances in production would. Returns the raw pool alongside
// the Store since pgWriteHandler needs direct SQL access to the
// mcp_test_write_result scratch table, which Store has no method for.
func newIndependentStore(t *testing.T, pg *dbtest.Postgres) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	pool, err := db.NewPool(ctx, pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return store.New(pool), pool
}

// ── fake write tool, backed by Postgres (not an in-memory map) ─────────────

type scopedInput struct {
	ChannelID string `json:"channel_id" jsonschema:"channel to scope to, as a UUID string"`
}

func (i scopedInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

type writeInput struct {
	ChannelID string `json:"channel_id" jsonschema:"channel to scope to, as a UUID string"`
	Key       string `json:"idempotency_key,omitempty" jsonschema:"optional idempotency key"`
}

func (i writeInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}
func (i writeInput) IdempotencyKey() string { return i.Key }

type countOutput struct {
	Calls int `json:"calls" jsonschema:"total mcp_test_write_result rows -- how many times mutate has ever actually run, across every server instance sharing this database"`
}

// pgWriteHandler's WriteMutate inserts a new mcp_test_write_result row
// (directly via pool, since store.Store has no method for this
// intentionally-scratch, non-product table) whose calls column is the
// row's 1-based insertion order -- i.e. the total number of times ANY
// instance's mutate has genuinely run against this database, not a local
// in-memory counter. WriteRender reads it back by id. invoked is a
// local-to-this-instance atomic counter used only by the tests to assert
// "did the Go closure registered on THIS *mcp.Server actually run" --
// deliberately kept separate from the Postgres-durable calls value.
type pgWriteHandler struct {
	pool    *pgxpool.Pool
	invoked int32
}

func newPGWriteHandler(pool *pgxpool.Pool) *pgWriteHandler { return &pgWriteHandler{pool: pool} }

func (h *pgWriteHandler) mutate(ctx context.Context, _ writeInput) (uuid.UUID, error) {
	atomic.AddInt32(&h.invoked, 1)
	id := uuid.New()
	_, err := h.pool.Exec(ctx, `
		INSERT INTO mcp_test_write_result (id, calls)
		SELECT $1, count(*) + 1 FROM mcp_test_write_result
	`, id)
	return id, err
}

func (h *pgWriteHandler) render(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, countOutput, error) {
	var calls int
	err := h.pool.QueryRow(ctx, `SELECT calls FROM mcp_test_write_result WHERE id = $1`, ref).Scan(&calls)
	if err != nil {
		return nil, countOutput{}, err
	}
	return nil, countOutput{Calls: calls}, nil
}

func (h *pgWriteHandler) invokedCount() int { return int(atomic.LoadInt32(&h.invoked)) }

// countingReadHandler counts invocations of a scoped read tool -- used by
// the end-to-end auth/scoping test to prove the handler was (or wasn't)
// entered.
func countingReadHandler(counter *int32) mcp.ToolHandlerFor[scopedInput, countOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ scopedInput) (*mcp.CallToolResult, countOutput, error) {
		n := atomic.AddInt32(counter, 1)
		return nil, countOutput{Calls: int(n)}, nil
	}
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

// testServer wraps a real *mcp.Server (server.New) plus its
// server.Registry, hosted over HTTP (server.NewHTTPHandler) via
// httptest.Server -- exactly `mcp`'s production wiring (see main.go),
// minus binding to ASS_MCP_ADDR.
type testServer struct {
	url string
	st  *store.Store
}

func newTestServer(t *testing.T, st *store.Store, register func(*server.Registry)) *testServer {
	t.Helper()

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	register(reg)

	handler := server.NewHTTPHandler(srv, st.Credentials())
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &testServer{url: ts.URL, st: st}
}

// bearerRoundTripper injects an "Authorization: Bearer <token>" header on
// every request -- token == "" sends no header at all, simulating an
// unauthenticated caller.
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// connect opens a real streamable-HTTP MCP client session against ts,
// authenticated as token (or unauthenticated if token == "").
func (ts *testServer) connect(t *testing.T, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   ts.url,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// mintToken mints a real bearer credential for personID via
// store.CredentialStore (migration 005) -- the same mechanism `web`'s
// token-mint endpoint uses in production.
func mintToken(t *testing.T, st *store.Store, personID uuid.UUID) string {
	t.Helper()
	raw, _, err := st.Credentials().Mint(context.Background(), personID)
	require.NoError(t, err)
	return raw
}

// ── end-to-end auth + Channel-scoping (this task's core Testing criteria) ──

func TestMCP_EndToEnd_AuthAndChannelScoping(t *testing.T) {
	pg := newTestDB(t)
	st := store.New(pg.Pool)
	ctx := context.Background()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-creator", "creator@example.com", "Creator")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-analyst", "analyst@example.com", "Analyst")
	require.NoError(t, err)
	unassociated, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-unassoc", "unassoc@example.com", "Unassociated")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-e2e-1", "Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst))

	var calls int32
	ts := newTestServer(t, st, func(reg *server.Registry) {
		server.RegisterRead(reg, &mcp.Tool{Name: "scoped_read"}, countingReadHandler(&calls))
	})

	callTool := func(cs *mcp.ClientSession) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "scoped_read",
			Arguments: scopedInput{ChannelID: ch.ID.String()},
		})
		require.NoError(t, err)
		return res
	}

	t.Run("unauthenticated call is rejected before the MCP session even opens", func(t *testing.T) {
		_, err := ts.connect(t, "")
		require.Error(t, err, "auth.RequireBearerToken must reject a request with no bearer token at the HTTP layer")
		assert.Equal(t, int32(0), atomic.LoadInt32(&calls))
	})

	t.Run("authenticated Person with no role on the Channel gets a permission error, handler not invoked", func(t *testing.T) {
		cs, err := ts.connect(t, mintToken(t, st, unassociated.ID))
		require.NoError(t, err)
		before := atomic.LoadInt32(&calls)

		res := callTool(cs)
		assert.True(t, res.IsError)
		assert.Contains(t, textOf(res), "permission denied")
		assert.Equal(t, before, atomic.LoadInt32(&calls), "the handler must not run when Channel-scope authorization fails")
	})

	t.Run("creator and analyst both pass the CanRead gate", func(t *testing.T) {
		for _, personID := range []uuid.UUID{creator.ID, analyst.ID} {
			before := atomic.LoadInt32(&calls)
			cs, err := ts.connect(t, mintToken(t, st, personID))
			require.NoError(t, err)

			res := callTool(cs)
			require.False(t, res.IsError, "unexpected error: %s", textOf(res))
			assert.Equal(t, before+1, atomic.LoadInt32(&calls))
		}
	})
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

// ── concurrent-same-key idempotency: Postgres, not an in-memory lock ───────

// TestMCP_Idempotency_ConcurrentSameKey_HandlerRunsExactlyOnce is the
// concurrent-same-key scenario this task's Testing section calls out by
// name: N goroutines call the same write tool with the SAME idempotency
// key at the same time. The middleware itself (registry.go/idempotency.go)
// adds no mutex of its own -- whatever safety exists comes entirely from
// store.Idempotency.Do's real SQL against mcp_idempotency (migration 002),
// so this genuinely exercises Postgres's own contention handling, not an
// application-level lock.
func TestMCP_Idempotency_ConcurrentSameKey_HandlerRunsExactlyOnce(t *testing.T) {
	pg := newTestDB(t)
	st := store.New(pg.Pool)
	ctx := context.Background()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-creator", "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := st.Channels().Create(ctx, "yt-concurrent-1", "Channel", creator.ID)
	require.NoError(t, err)

	handler := newPGWriteHandler(pg.Pool)
	ts := newTestServer(t, st, func(reg *server.Registry) {
		server.RegisterWrite(reg, &mcp.Tool{Name: "scoped_write"}, handler.mutate, handler.render)
	})

	token := mintToken(t, st, creator.ID)

	const n = 10
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cs, err := ts.connect(t, token)
			if err != nil {
				errs[i] = err
				return
			}
			results[i], errs[i] = cs.CallTool(ctx, &mcp.CallToolParams{
				Name:      "scoped_write",
				Arguments: writeInput{ChannelID: ch.ID.String(), Key: "concurrent-key"},
			})
		}(i)
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i], "call %d", i)
	}

	assert.Equal(t, 1, handler.invokedCount(), "N concurrent calls with the same idempotency key must run mutate exactly once")

	// Every caller must see the SAME result (the one genuine run's, replayed
	// to everyone else), not their own distinct row.
	first := decodeCount(t, results[0])
	for i := 1; i < n; i++ {
		require.False(t, results[i].IsError, "call %d: unexpected error: %s", i, textOf(results[i]))
		assert.Equal(t, first, decodeCount(t, results[i]), "call %d must see the same replayed result as call 0", i)
	}

	var rowCount int
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT count(*) FROM mcp_test_write_result`).Scan(&rowCount))
	assert.Equal(t, 1, rowCount, "exactly one row must have been written, not one per concurrent caller")
}

func decodeCount(t *testing.T, res *mcp.CallToolResult) countOutput {
	t.Helper()
	require.False(t, res.IsError, "unexpected error: %s", textOf(res))
	m, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "StructuredContent must decode to an object")
	calls, ok := m["calls"].(float64)
	require.True(t, ok, "StructuredContent must carry a numeric calls field")
	return countOutput{Calls: int(calls)}
}

// ── statelessness: two independently constructed server instances ─────────

// TestMCP_Statelessness_ReplayAcrossTwoIndependentServerInstances is LB4's
// central proof: server1 and server2 are two separate *pgxpool.Pool
// connections, two separate *mcp.Server instances, two separate
// httptest.Server instances -- nothing in-process is shared between them,
// only the underlying Postgres database. A write via server1 followed by a
// replay of the SAME idempotency key via server2 must return server1's
// result WITHOUT server2's own handler ever running -- proving the replay
// guard, and the write tool's own render step, both work purely through
// Postgres state (mcp_idempotency's result_ref plus, here,
// mcp_test_write_result), never through anything held in server1's
// process.
func TestMCP_Statelessness_ReplayAcrossTwoIndependentServerInstances(t *testing.T) {
	pg := newTestDB(t)
	ctx := context.Background()

	// Person/Channel setup via one throwaway store handle -- irrelevant to
	// which of the two independent instances below serves later calls,
	// since it's all just rows in the one shared database.
	setupStore := store.New(pg.Pool)
	creator, _, err := setupStore.Persons().UpsertByGoogleSubject(ctx, "sub-creator", "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := setupStore.Channels().Create(ctx, "yt-stateless-1", "Channel", creator.ID)
	require.NoError(t, err)
	token := mintToken(t, setupStore, creator.ID)

	st1, pool1 := newIndependentStore(t, pg)
	handler1 := newPGWriteHandler(pool1)
	ts1 := newTestServer(t, st1, func(reg *server.Registry) {
		server.RegisterWrite(reg, &mcp.Tool{Name: "scoped_write"}, handler1.mutate, handler1.render)
	})

	st2, pool2 := newIndependentStore(t, pg)
	handler2 := newPGWriteHandler(pool2)
	ts2 := newTestServer(t, st2, func(reg *server.Registry) {
		server.RegisterWrite(reg, &mcp.Tool{Name: "scoped_write"}, handler2.mutate, handler2.render)
	})

	args := writeInput{ChannelID: ch.ID.String(), Key: "cross-instance-key"}

	cs1, err := ts1.connect(t, token)
	require.NoError(t, err)
	res1, err := cs1.CallTool(ctx, &mcp.CallToolParams{Name: "scoped_write", Arguments: args})
	require.NoError(t, err)
	first := decodeCount(t, res1)
	assert.Equal(t, 1, handler1.invokedCount(), "server1's own handler must have run exactly once")
	assert.Equal(t, 0, handler2.invokedCount(), "server2 has not been called yet")

	cs2, err := ts2.connect(t, token)
	require.NoError(t, err)
	res2, err := cs2.CallTool(ctx, &mcp.CallToolParams{Name: "scoped_write", Arguments: args})
	require.NoError(t, err)
	second := decodeCount(t, res2)

	assert.Equal(t, 0, handler2.invokedCount(), "server2's OWN handler must never run for a replay of a key server1 already recorded -- this is the statelessness proof: the replay guard lives entirely in Postgres, not in either process")
	assert.Equal(t, first, second, "server2 must replay server1's exact result, reconstructed purely from Postgres (mcp_test_write_result via the shared ref)")

	var rowCount int
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT count(*) FROM mcp_test_write_result`).Scan(&rowCount))
	assert.Equal(t, 1, rowCount, "only server1's single genuine mutate call must have written a row")
}
