//go:build integration

// Real-Postgres + real-HTTP-transport coverage for whagent_auth.go's
// DualAuthHTTPHandler/WhagentPersonMiddleware (issue #2116, FR12) -- the
// end-to-end properties server_test.go's pure-Go suite cannot prove: a
// real whagent Claim JWT verified over a real HTTP connection resolves to
// a real, auto-provisioned Person row; the existing mcp_credential path
// keeps authenticating correctly with the whagent path also mounted
// (FR12(a) coexistence); and the FR11 idempotency guard is genuinely one
// Postgres-backed store shared by both paths, not two. See
// server_integration_test.go's package doc for the general pattern this
// file follows (dbtest Postgres, real embedded migrations, a real
// mcp.NewClient over httptest.Server).
package server_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/whagent"
)

// whoamiProbeInput is a plain, non-ChannelScoped read-tool input -- unlike
// scopedInput (server_integration_test.go), which implements ChannelScoped
// and so always requires a live channel_person row. This file's tests need
// an unscoped probe: whether the caller resolves to A Person at all is
// exactly what these tests are proving, independent of any Channel-role
// authorization, which is orthogonal to FR12.
type whoamiProbeInput struct{}

// whoamiProbeHandler counts invocations of an unscoped probe read tool --
// mirrors server_integration_test.go's countingReadHandler, but for
// whoamiProbeInput rather than scopedInput.
func whoamiProbeHandler(counter *int32) mcp.ToolHandlerFor[whoamiProbeInput, countOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ whoamiProbeInput) (*mcp.CallToolResult, countOutput, error) {
		n := atomic.AddInt32(counter, 1)
		return nil, countOutput{Calls: int(n)}, nil
	}
}

const testWhagentAudience = "https://mcp.example.com"

// newTestWhagentSignerVerifier builds a whagent.Signer/Verifier pair for a
// fresh Ed25519 key and issuer -- mirrors libs/go/whagent's own unexported
// newTestSigner fixture, duplicated here at the width this package's
// integration tests need (minting real Claim JWTs against a Verifier the
// test server is actually configured with).
func newTestWhagentSignerVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

// newTestDualAuthServer mirrors newTestServer (server_integration_test.go)
// exactly, except it mounts BOTH caller-authentication paths (FR12(a)) --
// server.NewDualAuthHTTPHandler plus server.WhagentPersonMiddleware wired
// in the same order mcp/main.go wires them (Whagent added after server.New,
// so it runs before auth.go's PersonMiddleware -- see whagent_auth.go's
// package doc comment).
func newTestDualAuthServer(t *testing.T, st *store.Store, pool *pgxpool.Pool, verifier *whagent.Verifier, register func(*server.Registry)) *testServer {
	t.Helper()

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	register(reg)

	srv.AddReceivingMiddleware(server.WhagentPersonMiddleware(st.PersonIdentities()))

	handler := server.NewDualAuthHTTPHandler(srv, newTestCredentialStore(t, pool), server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: testWhagentAudience,
	}, server.ResourceMetadataConfig{
		Resource:            testWhagentAudience,
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &testServer{url: ts.URL, st: st}
}

// mintWhagentToken mints a real whagent Claim JWT via signer, on behalf of
// (iss, sub), for testWhagentAudience.
func mintWhagentToken(t *testing.T, signer *whagent.Signer, iss, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: iss,
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
		SessionID:     "session-1",
		Audience:      testWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

func TestWhagentAuth_EndToEnd_ValidClaimAutoProvisionsAndResolvesSamePersonOnReuse(t *testing.T) {
	pg := newTestDB(t)
	st := store.New(pg.Pool)
	ctx := context.Background()
	signer, verifier := newTestWhagentSignerVerifier(t, "https://whagent.example.test")
	const iss, sub = "https://keycloak.example.test/realms/humans", "human-e2e-1"

	var calls int32
	ts := newTestDualAuthServer(t, st, pg.Pool, verifier, func(reg *server.Registry) {
		server.RegisterRead(reg, &mcp.Tool{Name: "whoami_probe"}, whoamiProbeHandler(&calls))
	})

	callWhoami := func(cs *mcp.ClientSession) (*mcp.CallToolResult, error) {
		return cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami_probe", Arguments: whoamiProbeInput{}})
	}

	cs1, err := ts.connect(t, mintWhagentToken(t, signer, iss, sub))
	require.NoError(t, err)
	res1, err := callWhoami(cs1)
	require.NoError(t, err)
	require.False(t, res1.IsError, "unexpected error: %s", textOf(res1))
	_ = cs1.Close()

	var personCount int
	require.NoError(t, pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM person p JOIN person_oidc_identity i ON i.person_id = p.id
		WHERE i.iss = $1 AND i.sub = $2
	`, iss, sub).Scan(&personCount))
	assert.Equal(t, 1, personCount, "the first whagent-authenticated call for an unseen (iss, sub) must auto-provision exactly one Person")

	// A second call minting a FRESH token for the exact same (iss, sub)
	// pair must resolve to the same person_id and must not create a
	// second Person row.
	cs2, err := ts.connect(t, mintWhagentToken(t, signer, iss, sub))
	require.NoError(t, err)
	res2, err := callWhoami(cs2)
	require.NoError(t, err)
	require.False(t, res2.IsError, "unexpected error: %s", textOf(res2))
	_ = cs2.Close()

	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM person_oidc_identity WHERE iss = $1 AND sub = $2`, iss, sub).Scan(&personCount))
	assert.Equal(t, 1, personCount, "a second call for the same (iss, sub) pair must not create a second Person")
	assert.Equal(t, int32(2), calls, "both calls must actually reach the tool handler")
}

func TestWhagentAuth_McpCredentialPathStillWorksAlongsideWhagentPath(t *testing.T) {
	pg := newTestDB(t)
	st := store.New(pg.Pool)
	ctx := context.Background()
	_, verifier := newTestWhagentSignerVerifier(t, "https://whagent.example.test")

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-creator", "creator@example.com", "Creator")
	require.NoError(t, err)

	var calls int32
	ts := newTestDualAuthServer(t, st, pg.Pool, verifier, func(reg *server.Registry) {
		server.RegisterRead(reg, &mcp.Tool{Name: "whoami_probe"}, whoamiProbeHandler(&calls))
	})

	// The pre-#2116 mcp_credential path must authenticate correctly with
	// no change in behavior even though the whagent path is now also
	// mounted (FR12(a): neither path is built on the other).
	cs, err := ts.connect(t, mintToken(t, pg.Pool, creator.ID))
	require.NoError(t, err)
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami_probe", Arguments: whoamiProbeInput{}})
	require.NoError(t, err)
	assert.False(t, res.IsError, "unexpected error: %s", textOf(res))
	assert.Equal(t, int32(1), calls)
}

func TestWhagentAuth_RejectsUnverifiableCredentials(t *testing.T) {
	pg := newTestDB(t)
	st := store.New(pg.Pool)
	signer, verifier := newTestWhagentSignerVerifier(t, "https://whagent.example.test")
	otherSigner, _ := newTestWhagentSignerVerifier(t, "https://whagent.example.test")

	var calls int32
	ts := newTestDualAuthServer(t, st, pg.Pool, verifier, func(reg *server.Registry) {
		server.RegisterRead(reg, &mcp.Tool{Name: "whoami_probe"}, whoamiProbeHandler(&calls))
	})

	cases := []struct {
		name  string
		token string
	}{
		{"token signed by a non-whagent key", mintWhagentToken(t, otherSigner, "https://keycloak.example.test/realms/humans", "human-1")},
		{"unrecognized mcp_credential-shaped token", "0000000000000000000000000000000000000000000000000000000000000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			_, err := ts.connect(t, tc.token)
			require.Error(t, err, "an unverifiable credential must be rejected before the MCP session even opens")
			assert.Equal(t, before, calls, "the tool handler must never be entered on a rejected call (NFR4)")
		})
	}

	t.Run("wrong-audience whagent token", func(t *testing.T) {
		before := calls
		token, err := signer.Mint(context.Background(), whagent.MintRequest{
			Subject:       "human-1",
			SubjectIssuer: "https://keycloak.example.test/realms/humans",
			Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "research-agent-v3"},
			SessionID:     "session-1",
			Audience:      "https://some-other-domain.example.test",
		})
		require.NoError(t, err)
		_, connErr := ts.connect(t, token)
		require.Error(t, connErr)
		assert.Equal(t, before, calls)
	})
}

// ── FR11: one idempotency store, correct across both auth paths ────────────

// whagentFR11Input is a minimal IdempotencyKeyed (but NOT ChannelScoped)
// write-tool input -- deliberately not channel-scoped so this test can
// isolate the idempotency guard itself from Channel-role authorization,
// which is orthogonal to FR11.
type whagentFR11Input struct {
	Key string `json:"idempotency_key,omitempty" jsonschema:"optional idempotency key"`
}

func (i whagentFR11Input) IdempotencyKey() string { return i.Key }

// TestWhagentAuth_FR11_SameResolvedPersonAcrossBothPaths_IdempotencyAppliesAtMostOnce
// is FR11's own acceptance case: "the same human calling one mutating tool
// with the same idempotency_key once via each authentication path applies
// the mutation at most once -- one idempotency store, keyed on the
// resolved person_id". M1 has no pre-linking flow between a whagent (iss,
// sub) identity and an existing mcp_credential-authenticated Person (FR12
// (b) is explicit that this is out of scope for M1), so this test
// constructs the "same human, same person_id, reached via both paths"
// precondition directly: auto-provision a Person via the whagent identity
// store, then mint an mcp_credential for that EXACT person_id (mcpauth
// treats identity as an opaque string -- nothing about Mint requires the
// identity to have signed in via Google). What this proves is the part
// that is genuinely this task's own responsibility: once two calls
// resolve to the same person_id, regardless of which auth path resolved
// them, they hit the exact same (tool, personID, key) row in
// mcp_idempotency (migration 002) -- there is no second, whagent-specific
// idempotency store to accidentally diverge from it.
func TestWhagentAuth_FR11_SameResolvedPersonAcrossBothPaths_IdempotencyAppliesAtMostOnce(t *testing.T) {
	pg := newTestDB(t)
	st := store.New(pg.Pool)
	ctx := context.Background()
	signer, verifier := newTestWhagentSignerVerifier(t, "https://whagent.example.test")
	const iss, sub = "https://keycloak.example.test/realms/humans", "human-fr11-1"

	// Auto-provision the Person the whagent path will resolve to, up
	// front, so the mcp_credential minted below shares its exact
	// person_id.
	person, created, err := st.PersonIdentities().FindOrCreateByIssSub(ctx, iss, sub)
	require.NoError(t, err)
	require.True(t, created)

	handler := &whagentPgWriteHandler{pool: pg.Pool}
	ts := newTestDualAuthServer(t, st, pg.Pool, verifier, func(reg *server.Registry) {
		server.RegisterWrite(reg, &mcp.Tool{Name: "fr11_write"}, handler.mutate, handler.render)
	})

	const idempotencyKey = "fr11-shared-key-1"

	whagentCS, err := ts.connect(t, mintWhagentToken(t, signer, iss, sub))
	require.NoError(t, err)
	res1, err := whagentCS.CallTool(ctx, &mcp.CallToolParams{
		Name:      "fr11_write",
		Arguments: whagentFR11Input{Key: idempotencyKey},
	})
	require.NoError(t, err)
	require.False(t, res1.IsError, "unexpected error: %s", textOf(res1))
	_ = whagentCS.Close()

	credCS, err := ts.connect(t, mintToken(t, pg.Pool, person.ID))
	require.NoError(t, err)
	res2, err := credCS.CallTool(ctx, &mcp.CallToolParams{
		Name:      "fr11_write",
		Arguments: whagentFR11Input{Key: idempotencyKey},
	})
	require.NoError(t, err)
	require.False(t, res2.IsError, "unexpected error: %s", textOf(res2))
	_ = credCS.Close()

	assert.Equal(t, 1, handler.invokedCount(), "the mutation must run at most once across both authentication paths for the same person_id + idempotency_key (FR11)")

	out1 := decodeCount(t, res1)
	out2 := decodeCount(t, res2)
	assert.Equal(t, out1.Calls, out2.Calls, "the replayed call (whichever path it came through) must return the SAME recorded outcome, not re-execute")
}

// whagentPgWriteHandler is FR11's own scratch write-tool handler, backed by
// the same mcp_test_write_result table server_integration_test.go's
// newTestDB provisions -- deliberately separate from that file's
// pgWriteHandler because writeInput (that file) implements ChannelScoped,
// which this test must not exercise (see whagentFR11Input's doc comment).
type whagentPgWriteHandler struct {
	pool    *pgxpool.Pool
	invoked int32
}

func (h *whagentPgWriteHandler) mutate(ctx context.Context, _ whagentFR11Input) (uuid.UUID, error) {
	h.invoked++
	id := uuid.New()
	_, err := h.pool.Exec(ctx, `
		INSERT INTO mcp_test_write_result (id, calls)
		SELECT $1, count(*) + 1 FROM mcp_test_write_result
	`, id)
	return id, err
}

func (h *whagentPgWriteHandler) render(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, countOutput, error) {
	var calls int
	err := h.pool.QueryRow(ctx, `SELECT calls FROM mcp_test_write_result WHERE id = $1`, ref).Scan(&calls)
	if err != nil {
		return nil, countOutput{}, err
	}
	return nil, countOutput{Calls: calls}, nil
}

func (h *whagentPgWriteHandler) invokedCount() int { return int(h.invoked) }
