//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for krill's
// abandon surface (issue #2688): abandon_milestone exercised through the
// MCP tool registration/dispatch layer -- the store-level coverage for
// the composed behavior already lives in
// //krill/store:abandon_integration_test; this file is specifically about
// the RegisterAbandonAll wrapper layer, never the HTTP handlers
// (krill/api/handlers/abandon_test.go has its own fake-store-backed
// coverage).
//
// Mirrors recut_test.go's seeding/HTTP/auth plumbing (duplicated here, not
// shared, since this file compiles into its own go_test target -- see
// that file's own doc comment for why the auth CredentialStore here is
// a hand-rolled fake and the whagent-net door is exercised against a real
// signed JWT).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:abandon_test --test_output=all
package tools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/libs/go/whagent"
)

// ── seeding (mirrors recut_test.go's world) ─────────────────────────────────

func newAbandonToolsTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	pool, err := pgxpool.New(ctx, db.ConnString)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return store.New(pool), pool
}

func createAbandonToolsTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

// ── fake auth.CredentialStore (no real migration to preflight against yet) ─

type abandonFakeCredentialStore struct {
	validToken string
	identity   string
}

func (f abandonFakeCredentialStore) Mint(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, errors.New("abandonFakeCredentialStore.Mint is not used by this test")
}

func (f abandonFakeCredentialStore) Verify(_ context.Context, rawToken string) (string, auth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, auth.Credential{Identity: f.identity}, nil
	}
	return "", auth.Credential{}, auth.ErrInvalidCredential
}

func (f abandonFakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("abandonFakeCredentialStore.Revoke is not used by this test")
}

func (f abandonFakeCredentialStore) List(context.Context, string) ([]auth.Credential, error) {
	return nil, errors.New("abandonFakeCredentialStore.List is not used by this test")
}

var _ auth.CredentialStore = abandonFakeCredentialStore{}

// ── whagent fixture ──────────────────────────────────────────────────────────

const abandonTestWhagentAudience = "https://krill-mcp.example.test"

func newAbandonTestWhagentVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

func mintAbandonWhagentToken(t *testing.T, signer *whagent.Signer, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-abandon-agent-v1"},
		SessionID:     "session-1",
		Audience:      abandonTestWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type abandonBearerRoundTripper struct{ token string }

func (rt abandonBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connectAbandonMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: abandonBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// abandonTextOf concatenates every TextContent block in res.Content -- the
// error message a rejected call's Content carries.
func abandonTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// ── the end-to-end test ──────────────────────────────────────────────────────

// TestMCPAbandonSurface_EndToEnd is issue #2688's Testing section's
// MCP-layer re-verification: over MCP alone, a Requirement Contributor can
// abandon_milestone a real milestone with a mix of shipped/unshipped
// delivered items, the response reports the swept and shipped ids and the
// new `abandoned` status, a second abandon_milestone call on the same
// container is rejected loudly (ErrAlreadyAbandoned), and the store layer
// directly confirms the same end state -- never a divergent MCP-local
// projection.
func TestMCPAbandonSurface_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newAbandonToolsTestStore(t)
	scopeID := createAbandonToolsTestScope(t, ctx, pool, "whale-net/krill-mcp-abandon-e2e-test")
	sessions := store.NewSessionStore(pool)

	product, err := entities.Products().Create(ctx, scopeID, "krill", "abandon e2e product")
	require.NoError(t, err)

	selfSubject := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	selfSessionID, err := sessions.InitSession(ctx, scopeID, selfSubject, selfSubject, nil)
	require.NoError(t, err)

	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship the abandon surface end to end", nil, selfSubject, selfSubject)
	require.NoError(t, err)
	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	unshipped, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "unshipped-feature", nil)
	require.NoError(t, err)
	shipped, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "shipped-feature", nil)
	require.NoError(t, err)
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, unshipped.ID, selfSubject, selfSubject))
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, shipped.ID, selfSubject, selfSubject))
	require.NoError(t, entities.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, shipped.ID, nil, selfSubject, selfSubject))

	credentials := abandonFakeCredentialStore{validToken: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd", identity: "swarm-operator-4"}
	signer, verifier := newAbandonTestWhagentVerifier(t, "https://whagent.example.test")

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterAbandonAll(designReg, sessions, entities.Abandon())

	// Mirrors ../main.go's own construction order exactly (see
	// recut_test.go's identical comment): every write tool is registered
	// for PersonaRequirementContributor, which has no real front door yet
	// -- only the whagent-net (PersonaAgent) door can call these tools
	// today, so PersonaRequirementContributor tool calls are exercised via
	// the same whagent-signed token, mirroring
	// WhagentPersonaMiddleware's own persona-widening posture.
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: abandonTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"
	agentToken := mintAbandonWhagentToken(t, signer, "human-e2e-4")

	var backlogID string

	t.Run("abandon_milestone sweeps the unshipped feature and keeps the shipped one", func(t *testing.T) {
		cs, err := connectAbandonMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "abandon_milestone",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestone.ID.String(),
				"note":             "stalled, sweeping remaining scope",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", abandonTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, milestone.ID.String(), structured["container_id"])
		assert.Equal(t, "abandoned", structured["status_event"].(map[string]any)["status"])

		moved, ok := structured["moved_to_backlog_ids"].([]any)
		require.True(t, ok)
		require.Len(t, moved, 1)
		assert.Equal(t, unshipped.ID.String(), moved[0])

		shippedIDs, ok := structured["shipped_ids"].([]any)
		require.True(t, ok)
		require.Len(t, shippedIDs, 1)
		assert.Equal(t, shipped.ID.String(), shippedIDs[0])

		backlogID, ok = structured["backlog_id"].(string)
		require.True(t, ok)
		require.NotEmpty(t, backlogID)
	})

	t.Run("a second abandon_milestone on the same container is rejected loudly", func(t *testing.T) {
		cs, err := connectAbandonMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "abandon_milestone",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestone.ID.String(),
				"note":             nil,
			},
		})
		require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
		assert.True(t, res.IsError)
		assert.Contains(t, abandonTextOf(res), "already abandoned")
	})

	// ── cross-check against the store layer directly, so this test proves ──
	// ── the MCP dispatch path produces the same result the store itself ────
	// ── would -- never a divergent MCP-local projection ─────────────────────

	t.Run("the MCP-recorded abandon matches what the store layer reports directly", func(t *testing.T) {
		status, err := entities.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
		require.NoError(t, err)
		assert.Equal(t, store.MilestoneStatusAbandoned, status)

		backlogIDs, err := entities.Recut().ListBacklog(ctx, scopeID, product.ID)
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{unshipped.ID}, backlogIDs)

		var count int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event WHERE milestone_id = $1`, milestone.ID).Scan(&count))
		assert.Equal(t, 1, count, "the rejected second abandon_milestone call must not have appended a second status_event row")
	})
}
