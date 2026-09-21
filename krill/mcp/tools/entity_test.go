//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for krill's
// top-of-chain entity creation surface (entity.go): create_product,
// create_feature_set, and create_load_bearing_decision exercised through
// the MCP tool registration/dispatch layer -- the store-level coverage for
// these already lives in the krill/store package tests; this file is
// specifically about the RegisterEntityCreateAll wrapper layer, never the
// HTTP handlers (krill/api/handlers/product.go, featureset.go, decision.go
// have their own coverage).
//
// Mirrors milestone_test.go's seeding/HTTP/auth plumbing (duplicated here,
// not shared, since this file compiles into its own go_test target -- see
// design_test.go's own doc comment for why the mcpauth CredentialStore
// here is a hand-rolled fake and the whagent-net door is exercised against
// a real signed JWT).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:entity_test --test_output=all
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
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/libs/go/whagent"
)

// ── seeding (mirrors milestone_test.go's world) ─────────────────────────────

func newEntityToolsTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

func createEntityToolsTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

// ── fake mcpauth.CredentialStore (no real migration to preflight against yet) ─

type entityFakeCredentialStore struct {
	validToken string
	identity   string
}

func (f entityFakeCredentialStore) Mint(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("entityFakeCredentialStore.Mint is not used by this test")
}

func (f entityFakeCredentialStore) Verify(_ context.Context, rawToken string) (string, mcpauth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, mcpauth.Credential{Identity: f.identity}, nil
	}
	return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
}

func (f entityFakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("entityFakeCredentialStore.Revoke is not used by this test")
}

func (f entityFakeCredentialStore) List(context.Context, string) ([]mcpauth.Credential, error) {
	return nil, errors.New("entityFakeCredentialStore.List is not used by this test")
}

var _ mcpauth.CredentialStore = entityFakeCredentialStore{}

// ── whagent fixture ──────────────────────────────────────────────────────────

const entityTestWhagentAudience = "https://krill-mcp.example.test"

func newEntityTestWhagentVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

func mintEntityWhagentToken(t *testing.T, signer *whagent.Signer, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-entity-agent-v1"},
		SessionID:     "session-1",
		Audience:      entityTestWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type entityBearerRoundTripper struct{ token string }

func (rt entityBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connectEntityMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: entityBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// entityTextOf concatenates every TextContent block in res.Content -- the
// error message a rejected call's Content carries.
func entityTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// ── the end-to-end test ──────────────────────────────────────────────────────

// TestMCPEntityCreateSurface_EndToEnd proves a Product, a FeatureSet under
// it, and a LoadBearingDecision attached to that FeatureSet can be created
// entirely over MCP -- before entity.go, this chain was only reachable via
// POST /products, POST /feature-sets, and POST /load-bearing-decisions
// (HTTP-only).
func TestMCPEntityCreateSurface_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newEntityToolsTestStore(t)
	scopeID := createEntityToolsTestScope(t, ctx, pool, "whale-net/krill-mcp-entity-create-e2e-test")
	sessions := store.NewSessionStore(pool)

	selfSubject := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	selfSessionID, err := sessions.InitSession(ctx, scopeID, selfSubject, selfSubject, nil)
	require.NoError(t, err)

	credentials := entityFakeCredentialStore{validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", identity: "swarm-operator-1"}
	signer, verifier := newEntityTestWhagentVerifier(t, "https://whagent.example.test")

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterEntityCreateAll(designReg, sessions, entities.Products(), entities.FeatureSets(), entities.Decisions())

	// Mirrors ../main.go's own construction order exactly (see
	// milestone_test.go's identical comment): every entity-create write
	// tool is registered for PersonaRequirementContributor/PersonaAgent,
	// and PersonaRequirementContributor has no real front door yet -- only
	// the whagent-net (PersonaAgent) door can call these tools today.
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: entityTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"
	agentToken := mintEntityWhagentToken(t, signer, "human-e2e-1")

	t.Run("create_product rejects a missing vision", func(t *testing.T) {
		cs, err := connectEntityMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "create_product",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"name":             "krill",
				"vision":           "",
			},
		})
		require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
		assert.True(t, res.IsError)
		assert.Contains(t, entityTextOf(res), "vision")
	})

	var productID string
	t.Run("create_product mints a new Product row", func(t *testing.T) {
		cs, err := connectEntityMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "create_product",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"name":             "krill",
				"vision":           "spec-of-record and work-tracking substrate for agent swarms",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", entityTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		productID, ok = structured["id"].(string)
		require.True(t, ok, "response must carry an id field")
		_, err = uuid.Parse(productID)
		require.NoError(t, err)
	})

	t.Run("create_feature_set rejects an unknown product_id", func(t *testing.T) {
		cs, err := connectEntityMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "create_feature_set",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"product_id":       uuid.New().String(),
				"name":             "orphan feature set",
			},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError, "LB2 parentage must be enforced even over MCP")
	})

	var featureSetID string
	t.Run("create_feature_set mints a new FeatureSet under the Product", func(t *testing.T) {
		cs, err := connectEntityMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "create_feature_set",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"product_id":       productID,
				"name":             "entity create e2e feature set",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", entityTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		featureSetID, ok = structured["id"].(string)
		require.True(t, ok, "response must carry an id field")
		_, err = uuid.Parse(featureSetID)
		require.NoError(t, err)
	})

	var decisionID string
	t.Run("create_load_bearing_decision attaches a new decision to the FeatureSet (C2)", func(t *testing.T) {
		cs, err := connectEntityMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "create_load_bearing_decision",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"feature_set_id":   featureSetID,
				"name":             "entity create e2e decision",
				"body":             "created directly over MCP, never only via HTTP",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", entityTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		decisionID, ok = structured["id"].(string)
		require.True(t, ok, "response must carry an id field")
		_, err = uuid.Parse(decisionID)
		require.NoError(t, err)
	})

	// ── cross-check against the store layer directly, so this test proves ──
	// ── the MCP dispatch path produces the same result the store itself ────
	// ── would -- never a divergent MCP-local projection ─────────────────────

	t.Run("every MCP-created row matches what the store layer reports directly", func(t *testing.T) {
		pID, err := uuid.Parse(productID)
		require.NoError(t, err)
		product, err := entities.Products().GetCurrentByID(ctx, pID)
		require.NoError(t, err)
		assert.Equal(t, "krill", product.Name)

		fsID, err := uuid.Parse(featureSetID)
		require.NoError(t, err)
		featureSet, err := entities.FeatureSets().GetCurrentByID(ctx, fsID)
		require.NoError(t, err)
		assert.Equal(t, pID, featureSet.ProductID)

		dID, err := uuid.Parse(decisionID)
		require.NoError(t, err)
		decision, err := entities.Decisions().GetCurrentByID(ctx, dID)
		require.NoError(t, err)
		assert.Equal(t, fsID, decision.FeatureSetID)
		require.NotNil(t, decision.Body)
		assert.Equal(t, "created directly over MCP, never only via HTTP", *decision.Body)
	})
}
