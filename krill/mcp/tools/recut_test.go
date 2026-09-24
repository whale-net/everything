//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for krill's
// delivery-axis re-cut surface (issue #2687, FR5): move_delivery_scope ->
// get_backlog exercised through the MCP tool registration/dispatch
// layer -- the store-level coverage for these already lives in
// //krill/store:recut_integration_test and the slice-level typed-entity
// resolution in //krill/slice's own GetBacklog integration coverage; this
// file is specifically about the RegisterRecutAll wrapper layer, never the
// HTTP handlers (krill/api/handlers/recut_test.go has its own
// fake-store-backed coverage).
//
// Mirrors delivery_shipment_test.go's seeding/HTTP/auth plumbing
// (duplicated here, not shared, since this file compiles into its own
// go_test target -- see that file's own doc comment for why the auth
// CredentialStore here is a hand-rolled fake and the whagent-net door is
// exercised against a real signed JWT).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:recut_test --test_output=all
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
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/libs/go/whagent"
)

// ── seeding (mirrors delivery_shipment_test.go's world) ─────────────────────

func newRecutToolsTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

func createRecutToolsTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

// ── fake auth.CredentialStore (no real migration to preflight against yet) ─

type recutFakeCredentialStore struct {
	validToken string
	identity   string
}

func (f recutFakeCredentialStore) Mint(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, errors.New("recutFakeCredentialStore.Mint is not used by this test")
}

func (f recutFakeCredentialStore) Verify(_ context.Context, rawToken string) (string, auth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, auth.Credential{Identity: f.identity}, nil
	}
	return "", auth.Credential{}, auth.ErrInvalidCredential
}

func (f recutFakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("recutFakeCredentialStore.Revoke is not used by this test")
}

func (f recutFakeCredentialStore) List(context.Context, string) ([]auth.Credential, error) {
	return nil, errors.New("recutFakeCredentialStore.List is not used by this test")
}

var _ auth.CredentialStore = recutFakeCredentialStore{}

// ── whagent fixture ──────────────────────────────────────────────────────────

const recutTestWhagentAudience = "https://krill-mcp.example.test"

func newRecutTestWhagentVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

func mintRecutWhagentToken(t *testing.T, signer *whagent.Signer, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-recut-agent-v1"},
		SessionID:     "session-1",
		Audience:      recutTestWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type recutBearerRoundTripper struct{ token string }

func (rt recutBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connectRecutMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: recutBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// recutTextOf concatenates every TextContent block in res.Content -- the
// error message a rejected call's Content carries.
func recutTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// ── the end-to-end test ──────────────────────────────────────────────────────

// TestMCPRecutSurface_EndToEnd is issue #2687's Testing section's MCP-layer
// re-verification: over MCP alone, a Requirement Contributor can
// move_delivery_scope a real milestone's delivered Feature into the
// backlog bucket, a move naming an entity the source container does not
// deliver is rejected loudly (ErrEntityNotInContainer), and get_backlog
// reads back the bucket's own typed-entity contents (FR5/FR6's acceptance
// shape) -- matching the pattern delivery_shipment_test.go's own
// TestMCPDeliveryShipmentSurface_EndToEnd established.
func TestMCPRecutSurface_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newRecutToolsTestStore(t)
	scopeID := createRecutToolsTestScope(t, ctx, pool, "whale-net/krill-mcp-recut-e2e-test")
	sessions := store.NewSessionStore(pool)

	product, err := entities.Products().Create(ctx, scopeID, "krill", "recut e2e product")
	require.NoError(t, err)

	selfSubject := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	selfSessionID, err := sessions.InitSession(ctx, scopeID, selfSubject, selfSubject, nil)
	require.NoError(t, err)

	milestoneA, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "MA", "ship the recut surface end to end", nil, selfSubject, selfSubject)
	require.NoError(t, err)
	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	delivered, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "delivered-feature", nil)
	require.NoError(t, err)
	notDelivered, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "not-delivered-feature", nil)
	require.NoError(t, err)
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneA.ID, delivered.ID, selfSubject, selfSubject))

	// The backlog bucket has no MCP tool of its own to create -- only
	// get_backlog (read) and move_delivery_scope (which takes an existing
	// container id as `to`) are exposed. Seeding it via the store directly
	// mirrors how a real deployment's importer/other tooling would resolve
	// it ahead of a move.
	backlog, err := entities.Recut().GetOrCreateBacklog(ctx, scopeID, product.ID, selfSubject, selfSubject)
	require.NoError(t, err)

	credentials := recutFakeCredentialStore{validToken: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd", identity: "swarm-operator-4"}
	signer, verifier := newRecutTestWhagentVerifier(t, "https://whagent.example.test")

	querier := slice.NewQuerier(entities)

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterRecutAll(designReg, sessions, entities.Recut(), querier)

	// Mirrors ../main.go's own construction order exactly (see
	// delivery_shipment_test.go's identical comment): every write tool is
	// registered for PersonaRequirementContributor/PersonaAgent, and
	// PersonaRequirementContributor has no real front door yet -- only the
	// whagent-net (PersonaAgent) door can call these tools today.
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: recutTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"

	humanToken := credentials.validToken
	agentToken := mintRecutWhagentToken(t, signer, "human-e2e-4")

	t.Run("get_backlog before any move reports an empty bucket", func(t *testing.T) {
		cs, err := connectRecutMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_backlog",
			Arguments: map[string]any{"product_id": product.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", recutTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Empty(t, structured["features"])
	})

	t.Run("move_delivery_scope rejects an entity the from container does not deliver", func(t *testing.T) {
		cs, err := connectRecutMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "move_delivery_scope",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"entity_ids":       []string{notDelivered.ID.String()},
				"from":             milestoneA.ID.String(),
				"to":               backlog.ID.String(),
			},
		})
		require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
		assert.True(t, res.IsError)
		assert.Contains(t, recutTextOf(res), "not a delivers association")
	})

	t.Run("move_delivery_scope moves the delivered feature into the backlog bucket", func(t *testing.T) {
		cs, err := connectRecutMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "move_delivery_scope",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"entity_ids":       []string{delivered.ID.String()},
				"from":             milestoneA.ID.String(),
				"to":               backlog.ID.String(),
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", recutTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		entityIDs, ok := structured["entity_ids"].([]any)
		require.True(t, ok)
		require.Len(t, entityIDs, 1)
		assert.Equal(t, delivered.ID.String(), entityIDs[0])
		assert.Equal(t, backlog.ID.String(), structured["to"])
	})

	t.Run("get_backlog now reports the moved feature", func(t *testing.T) {
		cs, err := connectRecutMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_backlog",
			Arguments: map[string]any{"product_id": product.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", recutTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		features, ok := structured["features"].([]any)
		require.True(t, ok)
		require.Len(t, features, 1)
		feature, ok := features[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, delivered.ID.String(), feature["id"])
	})

	// ── cross-check against the store layer directly, so this test proves ──
	// ── the MCP dispatch path produces the same result the store itself ────
	// ── would -- never a divergent MCP-local projection ─────────────────────

	t.Run("the MCP-recorded move matches what the store layer reports directly", func(t *testing.T) {
		backlogIDs, err := entities.Recut().ListBacklog(ctx, scopeID, product.ID)
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{delivered.ID}, backlogIDs)
	})
}
