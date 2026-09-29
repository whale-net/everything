//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for init_session
// (session.go, issue #2827): the one MCP entry point that mints a
// krill_session_id, closing the gap where an MCP-only caller had no way to
// obtain one (nor a scope_id) at all.
//
// Mirrors design_test.go's seeding/HTTP/auth plumbing (duplicated here,
// not shared, since this file compiles into its own go_test target -- see
// that file's own doc comment for the same reasoning). Only the auth
// (human) front door is exercised here -- init_session has no
// persona-sensitivity of its own (no allowedPersonas restriction,
// session.go's RegisterInitSession doc comment), so a second whagent-net
// fixture would prove nothing this file's auth path does not already.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:session_test --test_output=all
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

// ── seeding (mirrors design_test.go's world) ────────────────────────────────

func newSessionToolsTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

// ── fake auth.CredentialStore (no real migration to preflight against yet) ─

type sessionToolsFakeCredentialStore struct {
	validToken string
	identity   string
}

func (f sessionToolsFakeCredentialStore) Mint(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, errors.New("Mint is not used by this test")
}

func (f sessionToolsFakeCredentialStore) Verify(_ context.Context, rawToken string) (string, auth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, auth.Credential{Identity: f.identity, Persona: "swarm_operator"}, nil
	}
	return "", auth.Credential{}, auth.ErrInvalidCredential
}

func (f sessionToolsFakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("Revoke is not used by this test")
}

func (f sessionToolsFakeCredentialStore) List(context.Context, string) ([]auth.Credential, error) {
	return nil, errors.New("List is not used by this test")
}

var _ auth.CredentialStore = sessionToolsFakeCredentialStore{}

// ── HTTP server + real MCP client plumbing ──────────────────────────────────

type sessionToolsBearerRoundTripper struct{ token string }

func (rt sessionToolsBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

func connectSessionToolsMCP(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: sessionToolsBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// textOfSessionToolsResult concatenates every TextContent block in res.Content.
func textOfSessionToolsResult(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// ── the end-to-end test ──────────────────────────────────────────────────────

const (
	sessionToolsHumanIss  = "https://keycloak.example.test/realms/humans"
	sessionToolsWhagentIs = "https://whagent.example.test"
	sessionToolsAudience  = "https://krill-mcp.example.test"
)

func TestMCPInitSession_EndToEnd(t *testing.T) {
	ctx := context.Background()
	entities, pool := newSessionToolsTestStore(t)

	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, "whale-net/krill-mcp-init-session-e2e-test", "main").Scan(&scopeID))

	sessions := store.NewSessionStore(pool)
	querier := slice.NewQuerier(entities)

	product, err := entities.Products().Create(ctx, scopeID, "krill", "init_session e2e product")
	require.NoError(t, err)

	credentials := sessionToolsFakeCredentialStore{
		validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
		identity:   sessionToolsHumanIss + "|human-1",
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, sessionToolsWhagentIs, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, sessionToolsWhagentIs)
	require.NoError(t, err)
	agentToken, err := signer.Mint(ctx, whagent.MintRequest{
		Subject:       "contributor-1",
		SubjectIssuer: sessionToolsHumanIss,
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-design-agent-v1"},
		SessionID:     "whagent-session-42",
		Audience:      sessionToolsAudience,
	})
	require.NoError(t, err)

	designSrv := server.New()
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
	designReg := server.NewRegistry(designSrv)
	tools.RegisterInitSession(designReg, sessions, entities.Scopes())
	tools.RegisterGetScope(designReg, entities.Scopes())
	tools.RegisterDesignAll(designReg, entities, sessions, querier)

	handler := server.NewDualAuthHTTPHandler(server.New(), designSrv, server.New(), server.New(), credentials,
		server.WhagentAuthConfig{Verifier: verifier, Audience: sessionToolsAudience}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"
	humanToken := credentials.validToken

	initSession := func(t *testing.T, cs *mcp.ClientSession) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "init_session", Arguments: map[string]any{}})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOfSessionToolsResult(res))
		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		sid, ok := structured["session_id"].(string)
		require.True(t, ok)
		assert.Equal(t, scopeID.String(), structured["scope_id"])
		return sid
	}

	t.Run("human init_session with no args records the verified identity and created_at", func(t *testing.T) {
		cs := connectSessionToolsMCP(t, designURL, humanToken)
		sid := initSession(t, cs)

		sess, err := sessions.GetSession(ctx, store.SessionID(uuid.MustParse(sid)))
		require.NoError(t, err)
		want := store.Subject{Iss: sessionToolsHumanIss, Sub: "human-1", Kind: store.SubjectKindHuman}
		assert.Equal(t, want, sess.Acting)
		assert.Equal(t, want, sess.OnBehalfOf)
		assert.Nil(t, sess.WhagentSessionID)
		assert.False(t, sess.CreatedAt.IsZero())

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "open_design_session",
			Arguments: map[string]any{
				"krill_session_id":   sid,
				"product_id":         product.ID.String(),
				"opening_submission": "human write",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOfSessionToolsResult(res))
	})

	t.Run("supplied identity and scope fields are rejected and never recorded", func(t *testing.T) {
		cs := connectSessionToolsMCP(t, designURL, humanToken)
		for _, field := range []string{"acting", "on_behalf_of", "scope_id", "whagent_session_id"} {
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name:      "init_session",
				Arguments: map[string]any{field: map[string]string{"iss": "https://evil.example.test", "sub": "mallory", "kind": "human"}},
			})
			if err == nil {
				assert.True(t, res.IsError, "supplying %s must be rejected", field)
			}
		}
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM krill_session WHERE acting_sub = 'mallory'`).Scan(&n))
		assert.Zero(t, n)
	})

	t.Run("whagent agent-kind session records the claim identity and writes a revision_event", func(t *testing.T) {
		cs := connectSessionToolsMCP(t, designURL, agentToken)
		sid := initSession(t, cs)

		sess, err := sessions.GetSession(ctx, store.SessionID(uuid.MustParse(sid)))
		require.NoError(t, err)
		assert.Equal(t, store.Subject{Iss: sessionToolsWhagentIs, Sub: "agent-actor-1", Kind: store.SubjectKindAgent}, sess.Acting)
		assert.Equal(t, store.Subject{Iss: sessionToolsHumanIss, Sub: "contributor-1", Kind: store.SubjectKindHuman}, sess.OnBehalfOf)
		require.NotNil(t, sess.WhagentSessionID)
		assert.Equal(t, "whagent-session-42", *sess.WhagentSessionID)
		assert.False(t, sess.CreatedAt.IsZero())

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "open_design_session",
			Arguments: map[string]any{
				"krill_session_id":   sid,
				"product_id":         product.ID.String(),
				"opening_submission": "agent write",
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOfSessionToolsResult(res))
		opened, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)

		res, err = cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "append_revision_event",
			Arguments: map[string]any{
				"krill_session_id":     sid,
				"design_session_id":    opened["id"],
				"event_type":           "answer",
				"entity_deltas":        []map[string]any{},
				"open_questions_delta": map[string]any{"opened": []map[string]any{}, "resolved": []string{}},
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOfSessionToolsResult(res))

		var n int
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT count(*) FROM revision_event WHERE acting_kind = 'agent' AND acting_sub = 'agent-actor-1'
		`).Scan(&n))
		assert.Positive(t, n, "an agent-kind session's write must land a revision_event")
	})

	t.Run("get_scope works without a session and unauthenticated callers are rejected", func(t *testing.T) {
		cs := connectSessionToolsMCP(t, designURL, humanToken)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_scope", Arguments: map[string]any{}})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOfSessionToolsResult(res))
		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, scopeID.String(), structured["scope_id"])

		transport := &mcp.StreamableClientTransport{Endpoint: designURL, HTTPClient: &http.Client{}}
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
		bad, err := client.Connect(ctx, transport, nil)
		if err == nil {
			defer bad.Close()
			_, err = bad.CallTool(ctx, &mcp.CallToolParams{Name: "get_scope", Arguments: map[string]any{}})
		}
		require.Error(t, err, "an unauthenticated get_scope must be rejected")
	})

	t.Run("multi-scope deployment refuses init_session and get_scope", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO scope (repo_full_name, default_branch) VALUES ('whale-net/second-scope', 'main')`)
		require.NoError(t, err)

		cs := connectSessionToolsMCP(t, designURL, humanToken)
		for _, name := range []string{"init_session", "get_scope"} {
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
			require.NoError(t, err)
			assert.True(t, res.IsError, "%s must refuse when more than one scope exists", name)
		}
	})
}
