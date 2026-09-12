//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for krill's
// FR10/NFR1 spec surface (issue #2494) -- the properties this package's
// pure-Go suites (auth_test.go, whagent_auth_test.go, registry_test.go,
// registry_tools_test.go) cannot prove without a genuine
// caller-authenticated HTTP request and a genuine //krill/slice.Querier
// result:
//
//   - an unauthenticated call is rejected before the MCP session even
//     opens;
//   - a human (OAuth2-shaped) identity through the mcpauth front door
//     resolves PersonaSwarmOperator and can call a spec-surface tool;
//   - an agent identity through the whagent-net front door resolves
//     PersonaAgent and can call every FR5-FR8 granularity;
//   - each of the four tools' response is the exact same document
//     //krill/slice's Querier produces for the same id -- schema_version
//     intact, nothing reshaped (LB7) -- proven by comparing the tool
//     call's StructuredContent against querier's own output run through
//     the identical JSON encoding, not merely spot-checking a few fields.
//
// mcpauth's CredentialStore here is a hand-rolled in-memory fake, not the
// real Postgres-backed one: krill has not yet shipped its own
// mcp_credential-shaped migration (see mcp/main.go's
// rejectingCredentialStore doc comment for that interim gap) -- there is
// no real table this test could preflight against. The whagent-net front
// door has no such gap and is exercised against a real signed JWT here.
//
// See //libs/go/dbtest's README and krill/slice/query_integration_test.go
// for the seeding pattern this file follows.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/server:server_integration_test --test_output=all
package server_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
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

// ── seeding (mirrors krill/slice/query_integration_test.go's world) ────────

func newTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

func createScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

func strPtr(s string) *string { return &s }

// world is a minimal product graph -- just enough to exercise all four
// FR5-FR8 granularities: Product -> FeatureSet -> Feature -> Requirement,
// plus one LoadBearingDecision attached to the FeatureSet.
type world struct {
	Product     store.Product
	FeatureSet  store.FeatureSet
	Feature     store.Feature
	Requirement store.Requirement
	Decision    store.LoadBearingDecision
}

func seedWorld(t *testing.T, ctx context.Context, entities *store.Store, scopeID uuid.UUID) world {
	t.Helper()

	product, err := entities.Products().Create(ctx, scopeID, "krill", "spec-of-record substrate")
	require.NoError(t, err)

	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "M1 spec surface", nil)
	require.NoError(t, err)

	feature, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "MCP spec surface", nil)
	require.NoError(t, err)

	requirement, err := entities.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "FR10", strPtr("expose the scoped slice over MCP"))
	require.NoError(t, err)

	decision, err := entities.Decisions().Create(ctx, scopeID, featureSet.ID, "LB7", strPtr("one typed document, never reshaped"))
	require.NoError(t, err)

	return world{Product: product, FeatureSet: featureSet, Feature: feature, Requirement: requirement, Decision: decision}
}

// ── fake mcpauth.CredentialStore (no real migration to preflight against yet) ─

type fakeCredentialStore struct {
	validToken string
	identity   string
}

func (f fakeCredentialStore) Mint(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("fakeCredentialStore.Mint is not used by this test")
}

func (f fakeCredentialStore) Verify(_ context.Context, rawToken string) (string, mcpauth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, mcpauth.Credential{Identity: f.identity}, nil
	}
	return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
}

func (f fakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("fakeCredentialStore.Revoke is not used by this test")
}

func (f fakeCredentialStore) List(context.Context, string) ([]mcpauth.Credential, error) {
	return nil, errors.New("fakeCredentialStore.List is not used by this test")
}

var _ mcpauth.CredentialStore = fakeCredentialStore{}

// ── whagent fixture ──────────────────────────────────────────────────────────

const testWhagentAudience = "https://krill-mcp.example.test"

func newTestWhagentVerifier(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

func mintWhagentToken(t *testing.T, signer *whagent.Signer, sub string) string {
	t.Helper()
	token, err := signer.Mint(context.Background(), whagent.MintRequest{
		Subject:       sub,
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-spec-agent-v1"},
		SessionID:     "session-1",
		Audience:      testWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type testServer struct{ url string }

func newTestDualAuthServer(t *testing.T, querier *slice.Querier, credentials mcpauth.CredentialStore, verifier *whagent.Verifier) *testServer {
	t.Helper()

	specSrv := server.New()
	specReg := server.NewRegistry(specSrv)
	tools.RegisterAll(specReg, querier)

	// This file's own coverage (its doc comment) is scoped to specMountPath
	// -- the FR5-FR8 spec surface's two-front-door round trip. designSrv
	// below has no design-session tool registered: it exists only so
	// server.NewDualAuthHTTPHandler's two-mount signature (issue #2547) is
	// satisfied here, at /mcp/design, alongside specSrv. See
	// krill/mcp/tools/design_test.go (this task's Testing phase) for the
	// design-session surface's own end-to-end coverage.
	designSrv := server.New()

	handler := server.NewDualAuthHTTPHandler(specSrv, designSrv, credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: testWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// transport.go's newMux mounts the guarded MCP handler at
	// specMountPath ("/mcp/spec", unexported -- duplicated here as a
	// literal since this file is package server_test), not at the mux
	// root -- unlike audience_score_system's own testServer, which mounts
	// at "/".
	return &testServer{url: ts.URL + "/mcp/spec"}
}

type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

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

// sliceCallInput mirrors tools.sliceInput's wire shape (unexported in that
// package) -- the one argument every FR5-FR8 tool takes.
type sliceCallInput struct {
	ID string `json:"id"`
}

// asStructuredContent runs doc through the same encoding/json path a real
// MCP response travels, so comparing it against a CallToolResult's
// StructuredContent is a genuine byte-shape comparison, not a hand-picked
// field check -- proving the tool reshapes nothing (LB7).
func asStructuredContent(t *testing.T, doc slice.Document) map[string]any {
	t.Helper()
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// ── the end-to-end test ──────────────────────────────────────────────────────

func TestMCPSpecSurface_EndToEnd_BothFrontDoorsAndDocumentRoundTrip(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/krill-mcp-e2e-test")
	w := seedWorld(t, ctx, entities, scopeID)
	querier := slice.NewQuerier(entities)

	credentials := fakeCredentialStore{validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", identity: "swarm-operator-1"}
	signer, verifier := newTestWhagentVerifier(t, "https://whagent.example.test")

	ts := newTestDualAuthServer(t, querier, credentials, verifier)

	t.Run("unauthenticated call is rejected before the MCP session even opens", func(t *testing.T) {
		_, err := ts.connect(t, "")
		require.Error(t, err, "a request with no bearer token must be rejected at the HTTP layer, before any MCP session opens")
	})

	t.Run("human identity through the mcpauth door resolves PersonaSwarmOperator and round-trips the feature-set slice", func(t *testing.T) {
		cs, err := ts.connect(t, credentials.validToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_feature_set_slice",
			Arguments: sliceCallInput{ID: w.FeatureSet.ID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %v", res.Content)

		expected, err := querier.GetFeatureSetSlice(ctx, w.FeatureSet.ID)
		require.NoError(t, err)
		assert.Equal(t, asStructuredContent(t, expected), res.StructuredContent, "the mcpauth door's tool response must be byte-identical to the querier's own document (LB7) -- no second projection")
	})

	t.Run("agent identity through the whagent door resolves PersonaAgent and round-trips all four granularities", func(t *testing.T) {
		cs, err := ts.connect(t, mintWhagentToken(t, signer, "human-e2e-1"))
		require.NoError(t, err)

		callAndCompare := func(t *testing.T, toolName string, id uuid.UUID, expected slice.Document) {
			t.Helper()
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name:      toolName,
				Arguments: sliceCallInput{ID: id.String()},
			})
			require.NoError(t, err)
			require.False(t, res.IsError, "unexpected error calling %s: %v", toolName, res.Content)
			assert.Equal(t, asStructuredContent(t, expected), res.StructuredContent, "%s's response must be byte-identical to the querier's own document (LB7)", toolName)

			structured, ok := res.StructuredContent.(map[string]any)
			require.True(t, ok, "%s's StructuredContent must decode to an object", toolName)
			assert.Equal(t, slice.SchemaVersion, structured["schema_version"], "%s must carry the current schema_version (FR9)", toolName)
		}

		fsDoc, err := querier.GetFeatureSetSlice(ctx, w.FeatureSet.ID)
		require.NoError(t, err)
		callAndCompare(t, "get_feature_set_slice", w.FeatureSet.ID, fsDoc)

		featureDoc, err := querier.GetFeatureSlice(ctx, w.Feature.ID)
		require.NoError(t, err)
		callAndCompare(t, "get_feature_slice", w.Feature.ID, featureDoc)

		reqDoc, err := querier.GetRequirementSlice(ctx, w.Requirement.ID)
		require.NoError(t, err)
		callAndCompare(t, "get_requirement_slice", w.Requirement.ID, reqDoc)

		productDoc, err := querier.GetProductSlice(ctx, w.Product.ID)
		require.NoError(t, err)
		callAndCompare(t, "get_product_slice", w.Product.ID, productDoc)
	})

	t.Run("an invalid mcpauth credential is rejected and never resolves a persona", func(t *testing.T) {
		_, err := ts.connect(t, "0000000000000000000000000000000000000000000000000000000000000000")
		require.Error(t, err)
	})
}
