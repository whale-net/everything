//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for krill's
// milestone-authoring surface (issue #2683, FR1/FR2, C13): add_delivers,
// add_must_not_foreclose, and add_deferral (milestone.go) exercised
// through the MCP tool registration/dispatch layer -- the store-level
// coverage for these already lives in
// //krill/store:milestone_authoring_integration_test; this file is
// specifically about the RegisterMilestoneAll wrapper layer, never the
// HTTP handlers (krill/api/handlers/milestone.go has its own coverage).
//
// Mirrors design_test.go's seeding/HTTP/auth plumbing (duplicated here,
// not shared, since this file compiles into its own go_test target --
// see that file's own doc comment for why the mcpauth CredentialStore
// here is a hand-rolled fake and the whagent-net door is exercised
// against a real signed JWT).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:milestone_test --test_output=all
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
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/libs/go/whagent"
)

// ── seeding (mirrors design_test.go's world) ────────────────────────────────

func newMilestoneToolsTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

func createMilestoneToolsTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

// ── fake mcpauth.CredentialStore (no real migration to preflight against yet) ─

type milestoneFakeCredentialStore struct {
	validToken string
	identity   string
}

func (f milestoneFakeCredentialStore) Mint(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("milestoneFakeCredentialStore.Mint is not used by this test")
}

func (f milestoneFakeCredentialStore) Verify(_ context.Context, rawToken string) (string, mcpauth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, mcpauth.Credential{Identity: f.identity}, nil
	}
	return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
}

func (f milestoneFakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("milestoneFakeCredentialStore.Revoke is not used by this test")
}

func (f milestoneFakeCredentialStore) List(context.Context, string) ([]mcpauth.Credential, error) {
	return nil, errors.New("milestoneFakeCredentialStore.List is not used by this test")
}

var _ mcpauth.CredentialStore = milestoneFakeCredentialStore{}

// ── whagent fixture ──────────────────────────────────────────────────────────

const milestoneTestWhagentAudience = "https://krill-mcp.example.test"

func newMilestoneTestWhagentVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

func mintMilestoneWhagentToken(t *testing.T, signer *whagent.Signer, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-milestone-agent-v1"},
		SessionID:     "session-1",
		Audience:      milestoneTestWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type milestoneBearerRoundTripper struct{ token string }

func (rt milestoneBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connectMilestoneMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: milestoneBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// milestoneTextOf concatenates every TextContent block in res.Content --
// the error message a rejected call's Content carries.
func milestoneTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// ── the end-to-end test ──────────────────────────────────────────────────────

// TestMCPMilestoneSurface_EndToEnd is issue #2683's Testing section's
// re-verification of its Validation section's first bullet: a Requirement
// Contributor (here, the whagent PersonaAgent door -- PersonaRequirementContributor
// has no real front door yet, see krill/mcp/server/auth.go) can, over MCP
// alone, create a milestone recording an outcome sentence, its delivered
// features/FRs, its must-not-foreclose decisions, and a deferred list
// whose every entry names a destination.
func TestMCPMilestoneSurface_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newMilestoneToolsTestStore(t)
	scopeID := createMilestoneToolsTestScope(t, ctx, pool, "whale-net/krill-mcp-milestone-e2e-test")
	sessions := store.NewSessionStore(pool)

	product, err := entities.Products().Create(ctx, scopeID, "krill", "milestone authoring e2e product")
	require.NoError(t, err)
	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "e2e feature set", nil)
	require.NoError(t, err)
	feature, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "e2e feature", nil)
	require.NoError(t, err)
	decision, err := entities.Decisions().Create(ctx, scopeID, featureSet.ID, "e2e load-bearing decision", nil)
	require.NoError(t, err)

	// selfSession is an ordinary "acting for itself" krill session -- the
	// same shape create_milestone/add_delivers/add_must_not_foreclose/
	// add_deferral all accept (none of these require a mediated FR10
	// session, unlike propose_entities).
	selfSubject := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	selfSessionID, err := sessions.InitSession(ctx, scopeID, selfSubject, selfSubject, nil)
	require.NoError(t, err)

	credentials := milestoneFakeCredentialStore{validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", identity: "swarm-operator-1"}
	signer, verifier := newMilestoneTestWhagentVerifier(t, "https://whagent.example.test")

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterMilestoneAll(designReg, sessions, entities.MilestoneAuthoring(), entities.Products(), slice.NewQuerier(entities))

	// Mirrors ../main.go's own construction order exactly (see
	// design_test.go's identical comment): every milestone write tool is
	// registered for PersonaRequirementContributor/PersonaAgent/
	// PersonaSwarmOperator (issue #2926), and PersonaRequirementContributor
	// has no real front door yet -- both the whagent-net (PersonaAgent) and
	// mcpauth (PersonaSwarmOperator) doors can call these tools today. This
	// test still exercises writes through agentToken/PersonaAgent below,
	// matching the Requirement Contributor scenario its own doc comment
	// names; entity_test.go/design_test.go cover the mcpauth-door write
	// path.
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: milestoneTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"

	humanToken := credentials.validToken
	agentToken := mintMilestoneWhagentToken(t, signer, "human-e2e-1")

	t.Run("add_delivers with no krill_session_id fails cleanly, never a panic", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "add_delivers",
			Arguments: map[string]any{
				"krill_session_id": "",
				"milestone_id":     uuid.New().String(),
				"entity_id":        feature.ID.String(),
			},
		})
		require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
		assert.True(t, res.IsError)
		assert.Contains(t, milestoneTextOf(res), "required")
	})

	t.Run("get_milestone on an unknown id fails cleanly", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_milestone",
			Arguments: map[string]any{"id": uuid.New().String()},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError)
	})

	// ── create_milestone -> add_delivers -> add_must_not_foreclose -> ──────
	// ── add_deferral -> get_milestone, entirely over MCP ────────────────────

	var milestoneID string
	t.Run("create_milestone records an outcome sentence and optional FR budget", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "create_milestone",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"product_id":       product.ID.String(),
				"name":             "M1",
				"outcome":          "ship the milestone authoring MCP surface end to end",
				"fr_budget":        2,
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		milestoneID, ok = structured["id"].(string)
		require.True(t, ok, "response must carry an id field")
		_, err = uuid.Parse(milestoneID)
		require.NoError(t, err)
	})

	t.Run("add_delivers records a delivered feature (LB6)", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "add_delivers",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestoneID,
				"entity_id":        feature.ID.String(),
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))
	})

	t.Run("add_must_not_foreclose records a decision the milestone must not foreclose (LB6)", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "add_must_not_foreclose",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestoneID,
				"entity_id":        decision.ID.String(),
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))
	})

	t.Run("add_deferral rejects an empty destination (FR1)", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "add_deferral",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestoneID,
				"body":             "cut for M1",
				"destination":      "",
			},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError, "FR1 requires every deferred entry to cite where it went")
		assert.Contains(t, milestoneTextOf(res), "destination")
	})

	t.Run("add_deferral records a deferred item naming its destination (FR1)", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "add_deferral",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestoneID,
				"body":             "cut for M1",
				"destination":      "M4",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))
	})

	t.Run("get_milestone shows the outcome, delivers, must-not-foreclose, and deferral lists together", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_milestone",
			Arguments: map[string]any{"id": milestoneID},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)

		assert.Equal(t, milestoneID, structured["id"])
		assert.Equal(t, "ship the milestone authoring MCP surface end to end", structured["outcome"])
		assert.InDelta(t, 2, structured["fr_budget"], 0)

		delivers, ok := structured["delivers"].([]any)
		require.True(t, ok)
		require.Len(t, delivers, 1, "the feature added via add_delivers must show up")
		assert.Equal(t, feature.ID.String(), delivers[0])

		mustNotForeclose, ok := structured["must_not_foreclose"].([]any)
		require.True(t, ok)
		require.Len(t, mustNotForeclose, 1, "the decision added via add_must_not_foreclose must show up")
		assert.Equal(t, decision.ID.String(), mustNotForeclose[0])

		deferrals, ok := structured["deferrals"].([]any)
		require.True(t, ok)
		require.Len(t, deferrals, 1, "only the successful add_deferral call must have written a row -- the empty-destination call above must not have")
		deferral, ok := deferrals[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "cut for M1", deferral["body"])
		assert.Equal(t, "M4", deferral["destination"], "every deferred entry must name its destination (FR1)")
	})

	// ── cross-check against the store layer directly, so this test proves ──
	// ── the MCP dispatch path produces the same result the store itself ────
	// ── would -- never a divergent MCP-local projection ─────────────────────

	t.Run("the MCP-created milestone matches what the store layer reports directly", func(t *testing.T) {
		id, err := uuid.Parse(milestoneID)
		require.NoError(t, err)
		ref, delivers, mustNotForeclose, deferrals, err := entities.MilestoneAuthoring().GetMilestone(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "M1", ref.Name)
		require.Len(t, delivers, 1)
		assert.Equal(t, feature.ID, delivers[0].EntityID)
		require.Len(t, mustNotForeclose, 1)
		assert.Equal(t, decision.ID, mustNotForeclose[0].EntityID)
		require.Len(t, deferrals, 1)
		assert.Equal(t, "M4", deferrals[0].Destination)
	})
}

// TestMCPMilepebbleSurface_EndToEnd is issue #2684's Testing section's own
// MCP-layer re-verification of its Validation section's first bullet: a
// Requirement Contributor can cut a milestone into a milepebble and drop a
// mid-milestone discovery onto one, entirely over MCP, via
// create_milepebble, add_milepebble_scope, add_discovered_scope, and
// list_milepebbles (RegisterMilestoneAll already wires all four -- see
// milestone.go). Mirrors TestMCPMilestoneSurface_EndToEnd's seeding/HTTP/
// auth plumbing (same package, same helpers).
func TestMCPMilepebbleSurface_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newMilestoneToolsTestStore(t)
	scopeID := createMilestoneToolsTestScope(t, ctx, pool, "whale-net/krill-mcp-milepebble-e2e-test")
	sessions := store.NewSessionStore(pool)

	product, err := entities.Products().Create(ctx, scopeID, "krill", "milepebble e2e product")
	require.NoError(t, err)
	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "e2e feature set", nil)
	require.NoError(t, err)
	inSet, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "in the parent's delivers set", nil)
	require.NoError(t, err)
	notInSet, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "not in the parent's delivers set", nil)
	require.NoError(t, err)

	selfSubject := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	selfSessionID, err := sessions.InitSession(ctx, scopeID, selfSubject, selfSubject, nil)
	require.NoError(t, err)

	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship it incrementally", nil, selfSubject, selfSubject)
	require.NoError(t, err)
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, inSet.ID, selfSubject, selfSubject))

	credentials := milestoneFakeCredentialStore{validToken: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd", identity: "swarm-operator-2"}
	signer, verifier := newMilestoneTestWhagentVerifier(t, "https://whagent.example.test")

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterMilestoneAll(designReg, sessions, entities.MilestoneAuthoring(), entities.Products(), slice.NewQuerier(entities))
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: milestoneTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"
	agentToken := mintMilestoneWhagentToken(t, signer, "human-e2e-2")

	var milepebbleID string
	t.Run("create_milepebble cuts a milestone into a new sub-milestone container (FR3)", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "create_milepebble",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestone.ID.String(),
				"name":             "cut 1",
				"outcome":          "ship the first slice",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		milepebbleID, ok = structured["id"].(string)
		require.True(t, ok, "response must carry an id field")
		_, err = uuid.Parse(milepebbleID)
		require.NoError(t, err)
	})

	t.Run("add_milepebble_scope rejects an entity outside the parent milestone's delivers set (FR3)", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "add_milepebble_scope",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milepebble_id":    milepebbleID,
				"entity_id":        notInSet.ID.String(),
			},
		})
		require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
		assert.True(t, res.IsError, "an entity outside the parent milestone's delivers set must be rejected loudly")
		assert.Contains(t, milestoneTextOf(res), "delivers set")
	})

	t.Run("add_milepebble_scope accepts an entity already in the parent milestone's delivers set (FR3)", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "add_milepebble_scope",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milepebble_id":    milepebbleID,
				"entity_id":        inSet.ID.String(),
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))
	})

	var discoveredEntityID string
	t.Run("add_discovered_scope creates a real Requirement and associates it without touching the parent milestone (FR4)", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "add_discovered_scope",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milepebble_id":    milepebbleID,
				"feature_set_id":   nil,
				"feature_id":       inSet.ID.String(),
				"requirement_kind": "FR",
				"name":             "discovered mid-milestone requirement",
				"description":      nil,
				"body":             "the system must do the discovered thing",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		discoveredEntityID, ok = structured["entity_id"].(string)
		require.True(t, ok, "response must carry an entity_id field")
		assert.Equal(t, "requirement", structured["kind"])

		// The parent milestone's own outcome is untouched by the discovery.
		gotMilestone, delivers, _, _, err := entities.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
		require.NoError(t, err)
		require.NotNil(t, gotMilestone.Outcome)
		assert.Equal(t, "ship it incrementally", *gotMilestone.Outcome)

		discoveredID, err := uuid.Parse(discoveredEntityID)
		require.NoError(t, err)
		found := false
		for _, d := range delivers {
			if d.EntityID == discoveredID {
				found = true
			}
		}
		assert.True(t, found, "the discovered requirement must also land in the parent milestone's own Delivers set")
	})

	t.Run("list_milepebbles returns the cut milepebble", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "list_milepebbles",
			Arguments: map[string]any{"milestone_id": milestone.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		milepebbles, ok := structured["milepebbles"].([]any)
		require.True(t, ok)
		require.Len(t, milepebbles, 1)
		entry, ok := milepebbles[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, milepebbleID, entry["id"])
		assert.Equal(t, "cut 1", entry["name"])
	})
}

// TestMCPListProductDelivery_EndToEnd is issue #2689's Testing section's
// own MCP-layer re-verification of its Validation section's first bullet
// (FR11, C28): "what is planned versus what is merely spec'd" for a whole
// product answered over MCP alone, via list_product_delivery -- the
// milestone/milepebble containers themselves are seeded directly through
// the store (create_milestone/create_milepebble/status transitions are
// already covered end to end by TestMCPMilestoneSurface_EndToEnd/
// TestMCPMilepebbleSurface_EndToEnd above), so this test is specifically
// about list_product_delivery's own status-filter/hierarchy/output-schema
// behavior through the real MCP dispatch path -- a successful structured
// response here also proves item 9 (the tool's declared output schema
// validates against a real response): the go-sdk's own AddTool wrapper
// validates StructuredContent against OutputSchema before ever returning
// it, so a schema mismatch would surface as a call error, not a silent
// pass.
func TestMCPListProductDelivery_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newMilestoneToolsTestStore(t)
	scopeID := createMilestoneToolsTestScope(t, ctx, pool, "whale-net/krill-mcp-list-product-delivery-e2e-test")

	product, err := entities.Products().Create(ctx, scopeID, "krill", "list_product_delivery e2e product")
	require.NoError(t, err)

	self := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}

	planned, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "planned milestone", "ship the planned slice", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, planned.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	mp, err := entities.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, planned.ID, "planned's own milepebble", "ship the first cut", self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, mp.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	shipped, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "shipped milestone", "already out the door", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, shipped.ID, store.MilestoneStatusShipped, nil, self, self)
	require.NoError(t, err)

	credentials := milestoneFakeCredentialStore{validToken: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba98765432", identity: "swarm-operator-3"}
	_, verifier := newMilestoneTestWhagentVerifier(t, "https://whagent.example.test")

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterMilestoneAll(designReg, store.NewSessionStore(pool), entities.MilestoneAuthoring(), entities.Products(), slice.NewQuerier(entities))
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: milestoneTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"
	humanToken := credentials.validToken

	t.Run("filtered to planned, returns only the planned milestone with its milepebble nested underneath", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "list_product_delivery",
			Arguments: map[string]any{
				"product_id": product.ID.String(),
				"statuses":   []string{"planned"},
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		milestones, ok := structured["milestones"].([]any)
		require.True(t, ok)
		require.Len(t, milestones, 1, "only the planned milestone, never the shipped one")

		entry, ok := milestones[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, planned.ID.String(), entry["id"])
		assert.Equal(t, "planned", entry["status"])

		milepebbles, ok := entry["milepebbles"].([]any)
		require.True(t, ok)
		require.Len(t, milepebbles, 1, "the milestone's own milepebble is nested underneath, not a flat peer")
		mpEntry, ok := milepebbles[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, mp.ID.String(), mpEntry["id"])
	})

	t.Run("no filter returns every milestone under the product", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "list_product_delivery",
			Arguments: map[string]any{"product_id": product.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		milestones, ok := structured["milestones"].([]any)
		require.True(t, ok)
		require.Len(t, milestones, 2, "both the planned and the shipped milestone, no filter dropping either")

		var gotIDs []string
		for _, m := range milestones {
			entry, ok := m.(map[string]any)
			require.True(t, ok)
			gotIDs = append(gotIDs, entry["id"].(string))
		}
		assert.Contains(t, gotIDs, planned.ID.String())
		assert.Contains(t, gotIDs, shipped.ID.String())
	})

	t.Run("an unknown status value fails cleanly, never a silent empty result", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "list_product_delivery",
			Arguments: map[string]any{
				"product_id": product.ID.String(),
				"statuses":   []string{"bogus-status"},
			},
		})
		require.NoError(t, err, "a rejected read is a tool error, not a protocol error")
		assert.True(t, res.IsError)
		assert.Contains(t, milestoneTextOf(res), "bogus-status")
	})

	t.Run("an unknown product_id fails cleanly", func(t *testing.T) {
		cs, err := connectMilestoneMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "list_product_delivery",
			Arguments: map[string]any{"product_id": uuid.New().String()},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError)
	})
}
