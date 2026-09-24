//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for krill's
// per-delivered-item shipment surface (issue #2686, FR10):
// mark_delivered_item_shipped -> get_delivery_breakdown exercised through
// the MCP tool registration/dispatch layer -- the store-level coverage
// for these already lives in //krill/store:delivery_shipment_integration_test
// and the slice-level typed-entity resolution in
// //krill/slice:delivery_breakdown_integration_test; this file is
// specifically about the RegisterDeliveryShipmentAll wrapper layer, never
// the HTTP handlers (krill/api/handlers/delivery_shipment_test.go has its
// own fake-store-backed coverage).
//
// Mirrors milestone_status_test.go's seeding/HTTP/auth plumbing
// (duplicated here, not shared, since this file compiles into its own
// go_test target -- see that file's own doc comment for why the auth
// CredentialStore here is a hand-rolled fake and the whagent-net door is
// exercised against a real signed JWT).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:delivery_shipment_test --test_output=all
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

// ── seeding (mirrors milestone_status_test.go's world) ──────────────────────

func newDeliveryShipmentToolsTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

func createDeliveryShipmentToolsTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

// ── fake auth.CredentialStore (no real migration to preflight against yet) ─

type deliveryShipmentFakeCredentialStore struct {
	validToken string
	identity   string
}

func (f deliveryShipmentFakeCredentialStore) Mint(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, errors.New("deliveryShipmentFakeCredentialStore.Mint is not used by this test")
}

func (f deliveryShipmentFakeCredentialStore) Verify(_ context.Context, rawToken string) (string, auth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, auth.Credential{Identity: f.identity}, nil
	}
	return "", auth.Credential{}, auth.ErrInvalidCredential
}

func (f deliveryShipmentFakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("deliveryShipmentFakeCredentialStore.Revoke is not used by this test")
}

func (f deliveryShipmentFakeCredentialStore) List(context.Context, string) ([]auth.Credential, error) {
	return nil, errors.New("deliveryShipmentFakeCredentialStore.List is not used by this test")
}

var _ auth.CredentialStore = deliveryShipmentFakeCredentialStore{}

// ── whagent fixture ──────────────────────────────────────────────────────────

const deliveryShipmentTestWhagentAudience = "https://krill-mcp.example.test"

func newDeliveryShipmentTestWhagentVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

func mintDeliveryShipmentWhagentToken(t *testing.T, signer *whagent.Signer, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-delivery-shipment-agent-v1"},
		SessionID:     "session-1",
		Audience:      deliveryShipmentTestWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type deliveryShipmentBearerRoundTripper struct{ token string }

func (rt deliveryShipmentBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connectDeliveryShipmentMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: deliveryShipmentBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// deliveryShipmentTextOf concatenates every TextContent block in
// res.Content -- the error message a rejected call's Content carries.
func deliveryShipmentTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// ── the end-to-end test ──────────────────────────────────────────────────────

// TestMCPDeliveryShipmentSurface_EndToEnd is issue #2686's Testing
// section's MCP-layer re-verification: over MCP alone, a Requirement
// Contributor can mark_delivered_item_shipped a real milestone's
// delivered Feature, a second mark_delivered_item_shipped call against an
// entity the milestone does not deliver is rejected loudly
// (ErrEntityNotDelivered), and get_delivery_breakdown reads back the
// container's current status alongside the correct shipped/unshipped
// typed-entity partition (FR10's acceptance shape) -- matching the
// pattern milestone_status_test.go's own TestMCPMilestoneStatusSurface_EndToEnd
// established.
func TestMCPDeliveryShipmentSurface_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newDeliveryShipmentToolsTestStore(t)
	scopeID := createDeliveryShipmentToolsTestScope(t, ctx, pool, "whale-net/krill-mcp-delivery-shipment-e2e-test")
	sessions := store.NewSessionStore(pool)

	product, err := entities.Products().Create(ctx, scopeID, "krill", "delivery shipment e2e product")
	require.NoError(t, err)

	selfSubject := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	selfSessionID, err := sessions.InitSession(ctx, scopeID, selfSubject, selfSubject, nil)
	require.NoError(t, err)

	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship the delivery surface end to end", nil, selfSubject, selfSubject)
	require.NoError(t, err)
	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	delivered, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "delivered-feature", nil)
	require.NoError(t, err)
	notDelivered, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "not-delivered-feature", nil)
	require.NoError(t, err)
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, delivered.ID, selfSubject, selfSubject))

	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusPartiallyComplete, nil, selfSubject, selfSubject)
	require.NoError(t, err)

	credentials := deliveryShipmentFakeCredentialStore{validToken: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd", identity: "swarm-operator-4"}
	signer, verifier := newDeliveryShipmentTestWhagentVerifier(t, "https://whagent.example.test")

	querier := slice.NewQuerier(entities)

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterDeliveryShipmentAll(designReg, sessions, entities.DeliveryShipments(), entities.MilestoneStatus(), querier)

	// Mirrors ../main.go's own construction order exactly (see
	// milestone_status_test.go's identical comment): every write tool is
	// registered for PersonaRequirementContributor/PersonaAgent, and
	// PersonaRequirementContributor has no real front door yet -- only the
	// whagent-net (PersonaAgent) door can call these tools today.
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), server.New(), credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: deliveryShipmentTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"

	humanToken := credentials.validToken
	agentToken := mintDeliveryShipmentWhagentToken(t, signer, "human-e2e-4")

	t.Run("get_delivery_breakdown before any shipment reports the container's status with everything unshipped", func(t *testing.T) {
		cs, err := connectDeliveryShipmentMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_delivery_breakdown",
			Arguments: map[string]any{"milestone_id": milestone.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", deliveryShipmentTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "partially complete", structured["status"])

		shipped, ok := structured["shipped"].(map[string]any)
		require.True(t, ok)
		assert.Empty(t, shipped["features"])

		unshipped, ok := structured["unshipped"].(map[string]any)
		require.True(t, ok)
		unshippedFeatures, ok := unshipped["features"].([]any)
		require.True(t, ok)
		require.Len(t, unshippedFeatures, 1)
	})

	t.Run("mark_delivered_item_shipped rejects an entity the milestone does not deliver", func(t *testing.T) {
		cs, err := connectDeliveryShipmentMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "mark_delivered_item_shipped",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestone.ID.String(),
				"entity_id":        notDelivered.ID.String(),
				"note":             nil,
			},
		})
		require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
		assert.True(t, res.IsError)
		assert.Contains(t, deliveryShipmentTextOf(res), "not a delivers association")
	})

	t.Run("mark_delivered_item_shipped records the delivered feature as shipped", func(t *testing.T) {
		cs, err := connectDeliveryShipmentMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "mark_delivered_item_shipped",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestone.ID.String(),
				"entity_id":        delivered.ID.String(),
				"note":             "shipped in v1.0",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", deliveryShipmentTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		id, ok := structured["id"].(string)
		require.True(t, ok, "response must carry an id field")
		_, err = uuid.Parse(id)
		require.NoError(t, err)
	})

	t.Run("get_delivery_breakdown now reports the feature as shipped, none left unshipped", func(t *testing.T) {
		cs, err := connectDeliveryShipmentMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_delivery_breakdown",
			Arguments: map[string]any{"milestone_id": milestone.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", deliveryShipmentTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "partially complete", structured["status"])

		shipped, ok := structured["shipped"].(map[string]any)
		require.True(t, ok)
		shippedFeatures, ok := shipped["features"].([]any)
		require.True(t, ok)
		require.Len(t, shippedFeatures, 1)
		shippedFeature, ok := shippedFeatures[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, delivered.ID.String(), shippedFeature["id"])

		unshipped, ok := structured["unshipped"].(map[string]any)
		require.True(t, ok)
		assert.Empty(t, unshipped["features"])
	})

	// ── cross-check against the store layer directly, so this test proves ──
	// ── the MCP dispatch path produces the same result the store itself ────
	// ── would -- never a divergent MCP-local projection ─────────────────────

	t.Run("the MCP-recorded shipment matches what the store layer reports directly", func(t *testing.T) {
		shippedIDs, unshippedIDs, err := entities.DeliveryShipments().DeliveryBreakdown(ctx, milestone.ID)
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{delivered.ID}, shippedIDs)
		assert.Empty(t, unshippedIDs)
	})
}
