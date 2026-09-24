// No-database, real-HTTP-transport coverage of release_task/escalate_task
// (task_release.go, task_escalate.go, issue #2872's Testing section,
// "MCP: a non-swarm_operator persona is refused on both tools"). Both
// tools are registered via server.RegisterOpsWrite (registry.go, issue
// #2867), which restricts the /mcp/ops mount to PersonaSwarmOperator
// only; this file proves that restriction actually reaches these two
// tools specifically, not merely the generic mechanism
// (krill/mcp/server/registry_test.go already covers registerOpsGated
// itself in isolation).
//
// This needs a real HTTP round trip (not the bare in-memory transport
// registration_test.go files in this package use) because
// WhagentPersonaMiddleware only ever resolves PersonaAgent -- the one
// realistic non-operator persona a real caller can present today
// (PersonaRequirementContributor has no code path yet, per auth.go's own
// doc comment) -- from a genuinely verified whagent-net bearer token
// carried over the wire, mirroring design_test.go's own dual-auth
// plumbing (duplicated here at the width this file needs, not imported:
// that file is `//go:build integration`-gated and lives in a different
// build). No Postgres is required: rejection happens in the registry gate
// before any tool handler -- and therefore any store -- is ever reached,
// so sessions/tasks/assembler are passed as nil throughout, mirroring
// :task_release_escalate_registration_test.go's own "registration never
// queries" reasoning extended to "a rejected call never reaches the
// store" here.
package tools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/whagent"
)

// personaTestCredentialStore is a hand-rolled auth.CredentialStore
// standing in for a real one, resolving exactly one fixed token to one
// fixed identity -- enough to drive the auth door, which auth.go's
// PersonaMiddleware always resolves to PersonaSwarmOperator. Mint/Revoke/
// List are not used by this file's tests.
type personaTestCredentialStore struct {
	validToken string
	identity   string
}

func (f personaTestCredentialStore) Mint(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, errors.New("personaTestCredentialStore.Mint is not used by these tests")
}

func (f personaTestCredentialStore) Verify(_ context.Context, rawToken string) (string, auth.Credential, error) {
	if rawToken == f.validToken {
		return f.identity, auth.Credential{Identity: f.identity}, nil
	}
	return "", auth.Credential{}, auth.ErrInvalidCredential
}

func (f personaTestCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return errors.New("personaTestCredentialStore.Revoke is not used by these tests")
}

func (f personaTestCredentialStore) List(context.Context, string) ([]auth.Credential, error) {
	return nil, errors.New("personaTestCredentialStore.List is not used by these tests")
}

var _ auth.CredentialStore = personaTestCredentialStore{}

const personaTestWhagentAudience = "https://krill-mcp.example.test"

// personaTestBearerRoundTripper attaches a fixed bearer token to every
// outgoing request -- the minimal client-side half of the
// auth/whagent-net dual-auth handshake.
type personaTestBearerRoundTripper struct{ token string }

func (rt personaTestBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func personaTestConnectMCP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: personaTestBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

func personaTestTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// TestReleaseTaskAndEscalateTask_NonOperatorPersona_Refused is issue
// #2872's Testing section "MCP: a non-swarm_operator persona is refused
// on both tools": a genuinely whagent-authenticated caller (PersonaAgent)
// is rejected calling either release_task or escalate_task on the ops
// mount with a "forbidden" error, and a genuinely auth-authenticated
// caller (PersonaSwarmOperator) reaches past the persona gate (its own
// distinct, non-persona rejection -- "required" from
// requireKrillSession's empty-krill_session_id check -- proves the gate
// let it through to the handler, without needing a real store to drive
// the tool all the way to success).
func TestReleaseTaskAndEscalateTask_NonOperatorPersona_Refused(t *testing.T) {
	ctx := context.Background()

	credentials := personaTestCredentialStore{validToken: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567", identity: "swarm-operator-1"}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, "https://whagent.example.test", "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, "https://whagent.example.test")
	require.NoError(t, err)

	// specSrv/designSrv/workSrv exist only so NewDualAuthHTTPHandler's
	// four-mount signature is satisfied -- no tool is registered on any of
	// them, and this file's own coverage stays scoped to /mcp/ops,
	// mirroring design_test.go's own "opsSrv exists only so..." precedent
	// inverted.
	specSrv := server.New()
	designSrv := server.New()
	workSrv := server.New()

	opsSrv := server.New()
	opsReg := server.NewRegistry(opsSrv)
	tools.RegisterReleaseTask(opsReg, nil, nil, nil)
	tools.RegisterEscalateTask(opsReg, nil, nil, nil)

	// Mirrors main.go's own construction order: WhagentPersonaMiddleware
	// added AFTER server.New() (which already wired PersonaMiddleware) so
	// it runs BEFORE it.
	specSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
	designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
	workSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
	opsSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())

	handler := server.NewDualAuthHTTPHandler(specSrv, designSrv, workSrv, opsSrv, credentials, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: personaTestWhagentAudience,
	}, server.ResourceMetadataConfig{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	opsURL := ts.URL + "/mcp/ops"

	agentToken, err := signer.Mint(ctx, whagent.MintRequest{
		Subject:       "human-1",
		SubjectIssuer: "https://keycloak.example.test/realms/humans",
		Actor:         whagent.Actor{Subject: "agent-actor-1", AgentID: "krill-ops-agent-v1"},
		SessionID:     "session-1",
		Audience:      personaTestWhagentAudience,
	})
	require.NoError(t, err)

	for _, toolName := range []string{"release_task", "escalate_task"} {
		t.Run(toolName+" rejects PersonaAgent (whagent door)", func(t *testing.T) {
			cs, err := personaTestConnectMCP(t, opsURL, agentToken)
			require.NoError(t, err)

			res, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name:      toolName,
				Arguments: map[string]any{"krill_session_id": uuid.New().String(), "task_id": uuid.New().String()},
			})
			require.NoError(t, err, "a rejected call is a tool error, not a protocol error")
			assert.True(t, res.IsError, "PersonaAgent must never be allowed to call %s -- the /mcp/ops mount is PersonaSwarmOperator-only", toolName)
			assert.Contains(t, personaTestTextOf(res), "forbidden")
		})

		t.Run(toolName+" lets PersonaSwarmOperator (auth door) past the persona gate", func(t *testing.T) {
			cs, err := personaTestConnectMCP(t, opsURL, credentials.validToken)
			require.NoError(t, err)

			res, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name:      toolName,
				Arguments: map[string]any{"krill_session_id": "", "task_id": uuid.New().String()},
			})
			require.NoError(t, err)
			require.True(t, res.IsError, "an empty krill_session_id must still be rejected -- this asserts the FAILURE REASON differs from the persona rejection above")
			assert.NotContains(t, personaTestTextOf(res), "forbidden", "%s must not be persona-rejected for a PersonaSwarmOperator caller", toolName)
			assert.Contains(t, personaTestTextOf(res), "required", "the gate must have let this call reach the handler, which then rejected the empty krill_session_id")
		})
	}
}
