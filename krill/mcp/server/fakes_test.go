package server

// Shared fakes and helpers for this package's pure-Go unit tests
// (auth_test.go, whagent_auth_test.go, registry_test.go) -- no Docker
// required, runs as part of `bazel test //...`. Mirrors
// audience_score_system/mcp/server/fakes_test.go's own split: being in
// package server (not server_test) lets these tests reach withPersona and
// Registry's unexported fields directly.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/whagent"
)

// ── fake mcpauth.CredentialStore ─────────────────────────────────────────────

// fakeCredentialStore implements mcpauth.CredentialStore against a single
// fixed valid token -- enough to drive whagent_auth.go's
// DualAuthHTTPHandler's mcpauth branch without krill's not-yet-migrated
// real credential table (see mcp/main.go's rejectingCredentialStore doc
// comment for that gap). Mint/Revoke/List are not used by these tests.
type fakeCredentialStore struct {
	validToken string
	identity   string
}

func (f fakeCredentialStore) Mint(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("fakeCredentialStore.Mint is not used by these tests")
}

func (f fakeCredentialStore) Verify(_ context.Context, rawToken string) (string, mcpauth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, mcpauth.Credential{Identity: f.identity}, nil
	}
	return "", mcpauth.Credential{}, mcpauth.ErrInvalidCredential
}

func (f fakeCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("fakeCredentialStore.Revoke is not used by these tests")
}

func (f fakeCredentialStore) List(context.Context, string) ([]mcpauth.Credential, error) {
	return nil, errors.New("fakeCredentialStore.List is not used by these tests")
}

var _ mcpauth.CredentialStore = fakeCredentialStore{}

// ── mcp.Request construction ─────────────────────────────────────────────────

// requestWithExtra builds the minimal mcp.Request PersonaMiddleware and
// WhagentPersonaMiddleware read: only GetExtra() matters to either, so
// Session/Params are left zero.
func requestWithExtra(extra *mcp.RequestExtra) mcp.Request {
	return &mcp.ServerRequest[*mcp.CallToolParams]{Extra: extra}
}

// ── whagent Signer/Verifier fixture ──────────────────────────────────────────

// newTestWhagentVerifier builds a whagent.Signer/Verifier pair for issuer,
// mirroring libs/go/whagent's own unexported newTestSigner fixture
// (duplicated at the width this package's tests actually need, exactly
// like audience_score_system/mcp/server/whagent_auth_test.go's own copy).
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

// ── in-memory client/server plumbing ─────────────────────────────────────────

// newTestServer builds a bare *mcp.Server plus a Registry constructed
// directly from an unexported field literal (bypassing NewRegistry, which
// takes no other dependency but is spelled out here for parity with
// audience_score_system's own newTestRegistry). If persona is non-empty,
// every request is authenticated as that Persona via a fixed receiving
// middleware standing in for PersonaMiddleware/WhagentPersonaMiddleware
// (those two are covered directly by auth_test.go/whagent_auth_test.go);
// if empty, no middleware runs and PersonaFromContext sees nothing, exactly
// like an unauthenticated caller.
func newTestServer(persona Persona) (*mcp.Server, *Registry) {
	srv := mcp.NewServer(Implementation, nil)
	if persona != "" {
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				return next(withPersona(ctx, persona), method, req)
			}
		})
	}
	return srv, &Registry{server: srv}
}

// connectClient connects an in-memory client to srv (see
// mcp.NewInMemoryTransports) and returns the client session, ready to
// CallTool against whatever was registered on srv before this was called.
func connectClient(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// textOf concatenates every TextContent block in res.Content -- the error
// message a rejected call's Content carries (see ToolHandlerFor's doc: "an
// error result is ... packed into CallToolResult.Content, with IsError
// set").
func textOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}
