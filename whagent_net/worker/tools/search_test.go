package tools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// TestSearchToolsNameLiteral guards against an accidental rename of the
// reserved constant -- ListToolDefinitions' enforcement (FR8) and any
// domain server's own tool naming both depend on this exact literal.
func TestSearchToolsNameLiteral(t *testing.T) {
	assert.Equal(t, "search_tools", tools.SearchToolsName)
}

// probeInput/probeOutput are a minimal, argument-free tool shape: these
// tests only care about tool *names* surfacing through ListToolDefinitions,
// never about actually invoking a tool.
type probeInput struct{}

type probeOutput struct{}

// newListDefsTestServer starts a plain, unauthenticated in-process MCP
// server exposing one no-op probe tool per name in names -- everything
// ListToolDefinitions needs to exercise FR8's reserved-name enforcement,
// without any of a real domain server's auth/store machinery.
func newListDefsTestServer(t *testing.T, names ...string) string {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "listdefs-test-server", Version: "0.1.0"}, nil)
	for _, name := range names {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: "probe tool " + name},
			func(_ context.Context, _ *mcp.CallToolRequest, _ probeInput) (*mcp.CallToolResult, probeOutput, error) {
				return nil, probeOutput{}, nil
			})
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// newListDefsTestFixture builds the whagent-net-side pieces
// ListToolDefinitions needs (a persona.Issuer over a throwaway signer, and a
// session) -- no ledger, no auth verification, since this file's fake
// servers have neither.
func newListDefsTestFixture(t *testing.T) (*persona.Issuer, *session.Session, string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, "https://whagent.example.test", "test-key-1")
	require.NoError(t, err)

	sess := &session.Session{
		SessionID: uuid.New(),
		Subject: session.Subject{
			Iss:  "https://keycloak.example.test/realms/humans",
			Sub:  "human-worker-actor-1",
			Kind: session.SubjectKindHuman,
		},
		OnBehalfOf: session.Subject{
			Iss:  "https://keycloak.example.test/realms/humans",
			Sub:  "human-listdefs-test-1",
			Kind: session.SubjectKindHuman,
		},
		AgentID: "listdefs-test-agent",
		Model:   "test-model",
		Status:  session.StatusRunning,
	}

	return persona.NewIssuer(signer), sess, sess.AgentID
}

// TestListToolDefinitions_ReservedNameRejected proves FR8's core case: a
// server exposing a tool literally named search_tools fails the whole call
// (naming that server) rather than silently including or omitting it.
func TestListToolDefinitions_ReservedNameRejected(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "other_tool", tools.SearchToolsName)
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet)

	require.Error(t, err)
	assert.Contains(t, err.Error(), serverURL)
	assert.Contains(t, err.Error(), tools.SearchToolsName)
	assert.Empty(t, defs, "a reserved-name violation must not return a partial tool list")
}

// TestListToolDefinitions_ReservedNameRejected_EvenWhenAllowedToolsExcludesIt
// is the case a post-isAllowed check would wrongly let through: FR8 reserves
// the name against the server's own exposed catalog, not against what
// AllowedTools would have let the model see.
func TestListToolDefinitions_ReservedNameRejected_EvenWhenAllowedToolsExcludesIt(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "other_tool", tools.SearchToolsName)
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL, AllowedTools: []string{"other_tool"}}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet)

	require.Error(t, err, "AllowedTools excluding search_tools must not grant a pass")
	assert.Contains(t, err.Error(), serverURL)
	assert.Empty(t, defs)
}

// TestListToolDefinitions_ReservedNameOnSecondServer proves the reservation
// is enforced per-server across the whole tool set: a violation on the
// second server must fail the call outright, not silently merge the first
// server's already-collected tools into the result.
func TestListToolDefinitions_ReservedNameOnSecondServer(t *testing.T) {
	ctx := context.Background()
	firstURL := newListDefsTestServer(t, "first_tool")
	secondURL := newListDefsTestServer(t, tools.SearchToolsName)
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: firstURL}, {ServerURL: secondURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet)

	require.Error(t, err)
	assert.Contains(t, err.Error(), secondURL)
	assert.Empty(t, defs, "must not silently merge the first server's tools when a later server violates FR8")
}

// TestListToolDefinitions_SimilarNamesNotReserved proves the check is exact,
// not prefix or case-insensitive -- a domain server is free to use names
// that merely resemble the reserved one.
func TestListToolDefinitions_SimilarNamesNotReserved(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "search_tools_v2", "Search_Tools")
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet)

	require.NoError(t, err)
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	assert.ElementsMatch(t, []string{"search_tools_v2", "Search_Tools"}, names)
}
