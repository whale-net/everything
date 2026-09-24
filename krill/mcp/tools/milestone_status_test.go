//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for krill's
// milestone/milepebble status surface (issue #2685, FR8/FR9/FR12):
// set_milestone_status -> get_milestone_status -> get_milestone_status_history
// exercised through the MCP tool registration/dispatch layer -- the
// store-level coverage for these already lives in
// //krill/store:milestone_status_integration_test; this file is
// specifically about the RegisterMilestoneStatusAll wrapper layer, never
// the HTTP handlers (krill/api/handlers/milestone_status_test.go has its
// own coverage). See
// //krill/mcp/tools:milestone_status_registration_test for the
// no-database NFR2 tool-set proof.
//
// Mirrors milestone_test.go's seeding/HTTP/auth plumbing (duplicated here,
// not shared, since this file compiles into its own go_test target -- see
// that file's own doc comment for why the auth CredentialStore here is
// a hand-rolled fake and the whagent-net door is exercised against a real
// signed JWT).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:milestone_status_test --test_output=all
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

// ── seeding (mirrors milestone_test.go's world) ─────────────────────────────

func newMilestoneStatusToolsTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

func createMilestoneStatusToolsTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

// ── fake auth.CredentialStore (no real migration to preflight against yet) ─

type milestoneStatusFakeCredentialStore struct {
	validToken string
	identity   string
}

func (f milestoneStatusFakeCredentialStore) Mint(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, errors.New("milestoneStatusFakeCredentialStore.Mint is not used by this test")
}

func (f milestoneStatusFakeCredentialStore) Verify(_ context.Context, rawToken string) (string, auth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, auth.Credential{Identity: f.identity}, nil
	}
	return "", auth.Credential{}, auth.ErrInvalidCredential
}

func (f milestoneStatusFakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("milestoneStatusFakeCredentialStore.Revoke is not used by this test")
}

func (f milestoneStatusFakeCredentialStore) List(context.Context, string) ([]auth.Credential, error) {
	return nil, errors.New("milestoneStatusFakeCredentialStore.List is not used by this test")
}

var _ auth.CredentialStore = milestoneStatusFakeCredentialStore{}

// ── whagent fixture ──────────────────────────────────────────────────────────

const milestoneStatusTestWhagentAudience = "https://krill-mcp.example.test"

func newMilestoneStatusTestWhagentVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

func mintMilestoneStatusWhagentToken(t *testing.T, signer *whagent.Signer, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-milestone-status-agent-v1"},
		SessionID:     "session-1",
		Audience:      milestoneStatusTestWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type milestoneStatusBearerRoundTripper struct{ token string }

func (rt milestoneStatusBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connectMilestoneStatusMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: milestoneStatusBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// milestoneStatusTextOf concatenates every TextContent block in res.Content
// -- the error message a rejected call's Content carries.
func milestoneStatusTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// ── the end-to-end test ──────────────────────────────────────────────────────

// TestMCPMilestoneStatusSurface_EndToEnd is issue #2685's Testing
// section's MCP-layer re-verification of its Validation section's first
// bullet: a Requirement Contributor can, over MCP alone,
// set_milestone_status a real milestone through a
// planned -> in progress -> shipped sequence, read the current (derived)
// status back via get_milestone_status, and read the full chronological
// transition history -- with actor and timestamp -- via
// get_milestone_status_history.
func TestMCPMilestoneStatusSurface_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newMilestoneStatusToolsTestStore(t)
	scopeID := createMilestoneStatusToolsTestScope(t, ctx, pool, "whale-net/krill-mcp-milestone-status-e2e-test")
	sessions := store.NewSessionStore(pool)

	product, err := entities.Products().Create(ctx, scopeID, "krill", "milestone status e2e product")
	require.NoError(t, err)

	selfSubject := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	selfSessionID, err := sessions.InitSession(ctx, scopeID, selfSubject, selfSubject, nil)
	require.NoError(t, err)

	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship the status surface end to end", nil, selfSubject, selfSubject)
	require.NoError(t, err)

	credentials := milestoneStatusFakeCredentialStore{validToken: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba98765432", identity: "swarm-operator-3"}
	signer, verifier := newMilestoneStatusTestWhagentVerifier(t, "https://whagent.example.test")

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterMilestoneStatusAll(designReg, sessions, entities.MilestoneStatus())

	// Mirrors ../main.go's own construction order exactly (see
	// milestone_test.go's identical comment): every status write tool is
	// registered for PersonaRequirementContributor/PersonaAgent, and
	// PersonaRequirementContributor has no real front door yet -- only the
	// whagent-net (PersonaAgent) door can call these tools today.
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: milestoneStatusTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"

	humanToken := credentials.validToken
	agentToken := mintMilestoneStatusWhagentToken(t, signer, "human-e2e-3")

	t.Run("set_milestone_status with no krill_session_id fails cleanly, never a panic", func(t *testing.T) {
		cs, err := connectMilestoneStatusMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "set_milestone_status",
			Arguments: map[string]any{
				"krill_session_id": "",
				"milestone_id":     milestone.ID.String(),
				"status":           "planned",
				"note":             nil,
			},
		})
		require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
		assert.True(t, res.IsError)
		assert.Contains(t, milestoneStatusTextOf(res), "required")
	})

	t.Run("set_milestone_status rejects a value outside FR8's fixed seven", func(t *testing.T) {
		cs, err := connectMilestoneStatusMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "set_milestone_status",
			Arguments: map[string]any{
				"krill_session_id": selfSessionID.String(),
				"milestone_id":     milestone.ID.String(),
				"status":           "bogus-status",
				"note":             nil,
			},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError)
		assert.Contains(t, milestoneStatusTextOf(res), "fixed FR8 values")
	})

	t.Run("get_milestone_status on a milestone with no transitions reports \"not started\" (FR8)", func(t *testing.T) {
		cs, err := connectMilestoneStatusMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_milestone_status",
			Arguments: map[string]any{"milestone_id": milestone.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneStatusTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "not started", structured["status"])
	})

	// ── set_milestone_status (planned -> in progress -> shipped) -> ────────
	// ── get_milestone_status -> get_milestone_status_history, over MCP ──────

	for _, status := range []string{"planned", "in progress", "shipped"} {
		status := status
		t.Run("set_milestone_status records "+status, func(t *testing.T) {
			cs, err := connectMilestoneStatusMCP(t, designURL, agentToken)
			require.NoError(t, err)

			res, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name: "set_milestone_status",
				Arguments: map[string]any{
					"krill_session_id": selfSessionID.String(),
					"milestone_id":     milestone.ID.String(),
					"status":           status,
					"note":             nil,
				},
			})
			require.NoError(t, err)
			require.False(t, res.IsError, "unexpected error: %s", milestoneStatusTextOf(res))

			structured, ok := res.StructuredContent.(map[string]any)
			require.True(t, ok)
			id, ok := structured["id"].(string)
			require.True(t, ok, "response must carry an id field")
			_, err = uuid.Parse(id)
			require.NoError(t, err)
		})
	}

	t.Run("get_milestone_status now reports the latest recorded status (\"shipped\")", func(t *testing.T) {
		cs, err := connectMilestoneStatusMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_milestone_status",
			Arguments: map[string]any{"milestone_id": milestone.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneStatusTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "shipped", structured["status"])
	})

	t.Run("get_milestone_status_history returns all three transitions in chronological order with actor and timestamp (FR12)", func(t *testing.T) {
		cs, err := connectMilestoneStatusMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_milestone_status_history",
			Arguments: map[string]any{"milestone_id": milestone.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", milestoneStatusTextOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		transitions, ok := structured["transitions"].([]any)
		require.True(t, ok)
		require.Len(t, transitions, 3)

		wantStatuses := []string{"planned", "in progress", "shipped"}
		for i, raw := range transitions {
			entry, ok := raw.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, wantStatuses[i], entry["status"])

			acting, ok := entry["acting"].(map[string]any)
			require.True(t, ok, "every transition must carry its acting subject (NFR4)")
			assert.NotEmpty(t, acting["sub"])

			onBehalfOf, ok := entry["on_behalf_of"].(map[string]any)
			require.True(t, ok, "every transition must carry its on-behalf-of subject (NFR4)")
			assert.NotEmpty(t, onBehalfOf["sub"])

			assert.NotEmpty(t, entry["created_at"])
		}
	})

	// ── cross-check against the store layer directly, so this test proves ──
	// ── the MCP dispatch path produces the same result the store itself ────
	// ── would -- never a divergent MCP-local projection ─────────────────────

	t.Run("the MCP-recorded history matches what the store layer reports directly", func(t *testing.T) {
		transitions, err := entities.MilestoneStatus().ListTransitions(ctx, milestone.ID)
		require.NoError(t, err)
		require.Len(t, transitions, 3)
		assert.Equal(t, store.MilestoneStatusPlanned, transitions[0].Status)
		assert.Equal(t, store.MilestoneStatusInProgress, transitions[1].Status)
		assert.Equal(t, store.MilestoneStatusShipped, transitions[2].Status)

		status, err := entities.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
		require.NoError(t, err)
		assert.Equal(t, store.MilestoneStatusShipped, status)
	})
}
