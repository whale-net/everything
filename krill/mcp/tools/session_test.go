//go:build integration

// Real-Postgres + real-HTTP-transport MCP client coverage for init_session
// (session.go, issue #2827): the one MCP entry point that mints a
// krill_session_id, closing the gap where an MCP-only caller had no way to
// obtain one (nor a scope_id) at all.
//
// Mirrors design_test.go's seeding/HTTP/auth plumbing (duplicated here,
// not shared, since this file compiles into its own go_test target -- see
// that file's own doc comment for the same reasoning). Only the mcpauth
// (human) front door is exercised here -- init_session has no
// persona-sensitivity of its own (no allowedPersonas restriction,
// session.go's RegisterInitSession doc comment), so a second whagent-net
// fixture would prove nothing this file's mcpauth path does not already.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/mcp/tools:session_test --test_output=all
package tools_test

import (
	"context"
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

// ── fake mcpauth.CredentialStore (no real migration to preflight against yet) ─

type sessionToolsFakeCredentialStore struct {
	validToken string
	identity   string
}

func (f sessionToolsFakeCredentialStore) Mint(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("Mint is not used by this test")
}

func (f sessionToolsFakeCredentialStore) Verify(_ context.Context, rawToken string) (string, mcpauth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, mcpauth.Credential{Identity: f.identity}, nil
	}
	return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
}

func (f sessionToolsFakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("Revoke is not used by this test")
}

func (f sessionToolsFakeCredentialStore) List(context.Context, string) ([]mcpauth.Credential, error) {
	return nil, errors.New("List is not used by this test")
}

var _ mcpauth.CredentialStore = sessionToolsFakeCredentialStore{}

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

	credentials := sessionToolsFakeCredentialStore{validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", identity: "swarm-operator-1"}

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterInitSession(designReg, sessions, entities.Scopes())
	tools.RegisterDesignAll(designReg, entities, sessions, querier)

	handler := server.NewHTTPHandler(server.New(), designSrv, server.New(), server.New(), credentials, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	designURL := ts.URL + "/mcp/design"
	humanToken := credentials.validToken

	t.Run("mints a session usable by another write tool on the same mount, with no scope_id ever supplied", func(t *testing.T) {
		cs := connectSessionToolsMCP(t, designURL, humanToken)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "init_session",
			Arguments: map[string]any{
				"acting":       map[string]string{"iss": "https://keycloak.example.test/realms/humans", "sub": "human-1", "kind": "human"},
				"on_behalf_of": map[string]string{"iss": "https://keycloak.example.test/realms/humans", "sub": "human-1", "kind": "human"},
			},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOfSessionToolsResult(res))

		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		sessionID, ok := structured["session_id"].(string)
		require.True(t, ok, "response must carry session_id, not a bare id field")
		require.NotEmpty(t, sessionID)
		assert.Equal(t, scopeID.String(), structured["scope_id"], "response must hand back the resolved scope_id")

		res, err = cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "open_design_session",
			Arguments: map[string]any{
				"krill_session_id":   sessionID,
				"product_id":         product.ID.String(),
				"opening_submission": "minted entirely over MCP, no direct Postgres access",
			},
		})
		require.NoError(t, err)
		assert.False(t, res.IsError, "unexpected error: %s", textOfSessionToolsResult(res))
	})

	t.Run("rejects a missing acting.sub before ever calling the store", func(t *testing.T) {
		cs := connectSessionToolsMCP(t, designURL, humanToken)

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "init_session",
			Arguments: map[string]any{
				"acting":       map[string]string{"iss": "https://keycloak.example.test/realms/humans", "kind": "human"},
				"on_behalf_of": map[string]string{"iss": "https://keycloak.example.test/realms/humans", "sub": "human-1", "kind": "human"},
			},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError)
		assert.Contains(t, textOfSessionToolsResult(res), "acting")
	})
}
