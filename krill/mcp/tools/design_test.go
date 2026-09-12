//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for krill's
// design-session surface (issue #2547, FR1-FR10 over MCP). This is the
// file krill/mcp/server/server_integration_test.go's own doc comment
// points to for the design-session surface's end-to-end coverage, and
// this task's Testing section's numbered criteria 2-8 (criterion 1 --
// exactly six tools on /mcp/design, none on /mcp/spec -- is covered by
// ../server/registry_tools_test.go's pure-Go negative test, which needs
// no database at all since tool registration never queries).
//
// Mirrors server_integration_test.go's seeding/HTTP/auth plumbing
// (duplicated here, not imported: that file's helpers are unexported in
// package server_test, a different package). See that file's own doc
// comment for why the mcpauth CredentialStore here is a hand-rolled fake
// and the whagent-net door is exercised against a real signed JWT.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:design_test --test_output=all
package tools_test

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

	"github.com/whale-net/everything/krill/api/handlers"
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

// ── seeding (mirrors krill/mcp/server/server_integration_test.go's world) ──

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
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-design-agent-v1"},
		SessionID:     "session-1",
		Audience:      testWhagentAudience,
	})
	require.NoError(t, err)
	return token
}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connectMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
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

// asMap runs v through the same encoding/json path a real MCP response
// travels, so comparing it against a CallToolResult's StructuredContent is
// a genuine byte-shape comparison (LB7), not a hand-picked field check.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// ── the end-to-end test ──────────────────────────────────────────────────────

func TestMCPDesignSurface_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/krill-mcp-design-e2e-test")
	sessions := store.NewSessionStore(pool)
	querier := slice.NewQuerier(entities)

	product, err := entities.Products().Create(ctx, scopeID, "krill", "design session e2e product")
	require.NoError(t, err)
	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "e2e feature set", nil)
	require.NoError(t, err)

	// selfSession is an ordinary "acting for itself" krill session (a human
	// self-attributing) -- enough for every write tool except
	// propose_entities, which store.MediatedWriteStore.ProposeEntities
	// itself rejects (ErrMediatedIdentitySame) when acting == on_behalf_of.
	selfSubject := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	selfSessionID, err := sessions.InitSession(ctx, scopeID, selfSubject, selfSubject, nil)
	require.NoError(t, err)

	// mediatedSession is a genuine agent-acting-for-a-contributor session
	// (FR10): acting != on_behalf_of.
	actingAgent := store.Subject{Iss: "https://whagent.example.test", Sub: "agent-1", Kind: store.SubjectKindService}
	onBehalfOfHuman := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "contributor-1", Kind: store.SubjectKindHuman}
	mediatedSessionID, err := sessions.InitSession(ctx, scopeID, actingAgent, onBehalfOfHuman, nil)
	require.NoError(t, err)

	credentials := fakeCredentialStore{validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", identity: "swarm-operator-1"}
	signer, verifier := newTestWhagentVerifier(t, "https://whagent.example.test")

	specSrv := server.New()
	specReg := server.NewRegistry(specSrv)
	tools.RegisterAll(specReg, querier)

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterDesignAll(designReg, entities, sessions, querier)

	// Mirrors ../main.go's own construction order exactly: WhagentPersonaMiddleware
	// added AFTER server.New() (which already wired PersonaMiddleware) so it
	// runs BEFORE it -- see that middleware's own doc comment for the
	// coexistence contract. Without this, every caller (whagent- or
	// mcpauth-authenticated) resolves PersonaSwarmOperator, and
	// propose_entities' Agent-only allow-list (criterion 3) could never be
	// satisfied by anyone.
	specSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(specSrv, designSrv, credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: testWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"
	specURL := ts.URL + "/mcp/spec"

	humanToken := credentials.validToken
	agentToken := mintWhagentToken(t, signer, "human-e2e-1")

	// ── criterion 8: both front doors still work on /mcp/spec (regression) ──

	t.Run("both front doors still work on /mcp/spec after adding /mcp/design", func(t *testing.T) {
		fsDoc, err := querier.GetFeatureSetSlice(ctx, featureSet.ID)
		require.NoError(t, err)

		for _, tok := range []string{humanToken, agentToken} {
			cs, err := connectMCP(t, specURL, tok)
			require.NoError(t, err)
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name:      "get_feature_set_slice",
				Arguments: map[string]string{"id": featureSet.ID.String()},
			})
			require.NoError(t, err)
			require.False(t, res.IsError, "unexpected error: %v", res.Content)
			assert.Equal(t, asMap(t, fsDoc), res.StructuredContent)
		}
	})

	// ── criterion 2: no/unknown krill session id fails cleanly ──────────────

	t.Run("write tool with no krill_session_id fails cleanly, never a panic", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "open_design_session",
			Arguments: map[string]any{
				"krill_session_id":   "",
				"product_id":         product.ID.String(),
				"opening_submission": "no session id here",
			},
		})
		require.NoError(t, err, "a rejected write is a tool error, not a protocol error")
		assert.True(t, res.IsError)
		assert.Contains(t, textOf(res), "required")
	})

	t.Run("write tool with an unknown krill_session_id fails cleanly, never a panic", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "open_design_session",
			Arguments: map[string]any{
				"krill_session_id":   uuid.New().String(),
				"product_id":         product.ID.String(),
				"opening_submission": "an unknown session id",
			},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError)
		assert.Contains(t, textOf(res), "unknown krill session")
	})

	// ── criterion 7: FR8, a plain-language submission, no entity reference ──

	var designSessionID string
	t.Run("open_design_session accepts a plain-language submission with no entity reference (FR8)", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "open_design_session",
			Arguments: map[string]any{
				"krill_session_id":   selfSessionID.String(),
				"product_id":         product.ID.String(),
				"opening_submission": "Users need to bulk-export their data as CSV -- no entity ids, just a sentence.",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		designSessionID, ok = structured["id"].(string)
		require.True(t, ok, "response must carry an id field")
		_, err = uuid.Parse(designSessionID)
		require.NoError(t, err)
	})

	// ── criterion 6: append_revision_event's validation errors match the ────
	// ── HTTP handler's named messages exactly (no divergent validation) ─────

	t.Run("append_revision_event's FR3 validation error matches the HTTP handler's named message", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "append_revision_event",
			Arguments: map[string]any{
				"krill_session_id":  selfSessionID.String(),
				"design_session_id": designSessionID,
				"event_type":        "draft",
				"entity_deltas":     []map[string]any{},
				"open_questions_delta": map[string]any{
					"opened":   []map[string]any{},
					"resolved": []string{},
				},
				// verified_against deliberately omitted -- FR3 requires it
				// for event_type draft/reconciliation.
			},
		})
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, textOf(res), `verified_against is required for event_type "draft"`,
			"must be the exact store.ErrInvalidRevisionEvent message the HTTP handler surfaces verbatim, not a reworded MCP-local message")
	})

	var appendedSeqNo float64
	t.Run("append_revision_event succeeds with a valid draft round", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, agentToken)
		require.NoError(t, err)

		verifiedAgainst := "main@deadbeef"
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "append_revision_event",
			Arguments: map[string]any{
				"krill_session_id":  selfSessionID.String(),
				"design_session_id": designSessionID,
				"event_type":        "draft",
				"verified_against":  verifiedAgainst,
				"entity_deltas": []map[string]any{
					{"entity_id": featureSet.ID.String(), "change": "updated", "summary_line": "touched by the e2e round"},
				},
				"open_questions_delta": map[string]any{
					"opened": []map[string]any{
						{"question_id": "q1", "blocking": true, "text": "does this need a new endpoint?"},
					},
					"resolved": []string{},
				},
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		appendedSeqNo, ok = structured["seq_no"].(float64)
		require.True(t, ok)
		assert.Equal(t, float64(1), appendedSeqNo, "first event on a fresh session must be seq_no 1")
	})

	// ── criterion 4: a read tool requires a resolved persona but no krill ───
	// ── session, and its response matches the HTTP handler's exactly ────────

	t.Run("get_design_session requires only a resolved persona, no krill session, and matches the HTTP handler", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_design_session",
			Arguments: map[string]string{"id": designSessionID},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))

		id, err := uuid.Parse(designSessionID)
		require.NoError(t, err)
		ds, err := entities.DesignSessions().GetByID(ctx, id)
		require.NoError(t, err)
		revisionEvents, err := entities.RevisionEvents().ListBySession(ctx, id)
		require.NoError(t, err)
		expected := handlers.NewDesignSessionResponse(ds, revisionEvents)

		assert.Equal(t, asMap(t, expected), res.StructuredContent)
	})

	t.Run("list_open_questions matches the HTTP handler's response", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "list_open_questions",
			Arguments: map[string]any{"id": designSessionID},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))

		id, err := uuid.Parse(designSessionID)
		require.NoError(t, err)
		questions, err := entities.RevisionEvents().ListOpenQuestions(ctx, id)
		require.NoError(t, err)
		expected := handlers.NewListOpenQuestionsResponse(questions, false)

		assert.Equal(t, asMap(t, expected), res.StructuredContent)
	})

	// ── criterion 5: LB7 shape identity -- get_design_session_slice's ───────
	// ── output is byte-identical to GET /design-sessions/{id}/slice's ───────

	t.Run("get_design_session_slice is byte-identical to its HTTP twin (LB7)", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_design_session_slice",
			Arguments: map[string]string{"id": designSessionID},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))

		// Drive the real HTTP handler directly (session_slice.go's
		// SessionSlice), the same construction main.go's routes.go wires --
		// not a hand re-derivation of the slice document.
		mux := http.NewServeMux()
		handlers.NewSessionSlice(entities.DesignSessions(), entities.RevisionEvents(), querier).Register(mux)
		req := httptest.NewRequest(http.MethodGet, "/design-sessions/"+designSessionID+"/slice", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "unexpected HTTP status: %s", rec.Body.String())

		var expected slice.Document
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &expected))

		assert.Equal(t, asMap(t, expected), res.StructuredContent, "the MCP tool's response must be byte-identical to the HTTP handler's for the same session (LB7) -- no second projection")

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, slice.SchemaVersion, structured["schema_version"])
		// featureSet.ID was named by the append_revision_event round above,
		// so the slice must be non-empty -- proving this isn't a vacuous
		// comparison of two empty documents.
		featureSets, ok := structured["feature_sets"].([]any)
		require.True(t, ok)
		assert.Len(t, featureSets, 1)
	})

	// ── criterion 3: propose_entities is Agent-persona-restricted ───────────

	t.Run("propose_entities is rejected for the mcpauth (PersonaSwarmOperator) door", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, humanToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "propose_entities",
			Arguments: map[string]any{
				"krill_session_id":  mediatedSessionID.String(),
				"design_session_id": designSessionID,
				"verified_against":  "main@deadbeef",
				"proposals": []map[string]any{
					{"kind": "feature", "parent_id": featureSet.ID.String(), "name": "Bulk CSV export", "position": 1, "summary_line": "mediated proposal"},
				},
			},
		})
		require.NoError(t, err)
		require.True(t, res.IsError, "PersonaSwarmOperator must never be allowed to call propose_entities (FR9/FR10)")
		assert.Contains(t, textOf(res), "forbidden")
	})

	t.Run("propose_entities succeeds for the whagent (PersonaAgent) door with a genuinely mediated session", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "propose_entities",
			Arguments: map[string]any{
				"krill_session_id":  mediatedSessionID.String(),
				"design_session_id": designSessionID,
				"verified_against":  "main@deadbeef",
				"proposals": []map[string]any{
					{"kind": "feature", "parent_id": featureSet.ID.String(), "name": "Bulk CSV export", "position": 1, "summary_line": "mediated proposal"},
				},
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		entitiesOut, ok := structured["entities"].([]any)
		require.True(t, ok)
		require.Len(t, entitiesOut, 1)
		entity, ok := entitiesOut[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "feature", entity["kind"])
		_, err = uuid.Parse(entity["id"].(string))
		require.NoError(t, err)
	})

	t.Run("propose_entities rejects a session with acting == on_behalf_of even for PersonaAgent (FR10)", func(t *testing.T) {
		cs, err := connectMCP(t, designURL, agentToken)
		require.NoError(t, err)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "propose_entities",
			Arguments: map[string]any{
				"krill_session_id":  selfSessionID.String(),
				"design_session_id": designSessionID,
				"verified_against":  "main@deadbeef",
				"proposals": []map[string]any{
					{"kind": "feature", "parent_id": featureSet.ID.String(), "name": "Should never be created", "position": 2, "summary_line": "self-attributed"},
				},
			},
		})
		require.NoError(t, err)
		require.True(t, res.IsError, "an agent acting on its own behalf can never satisfy FR10's identity-differs rule")
	})
}
