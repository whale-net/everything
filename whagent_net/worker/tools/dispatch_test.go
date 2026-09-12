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
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// readProbeInput mirrors a real domain read tool's input shape (e.g.
// audience_score_system's list_ideas): a plain Go struct with no
// idempotency_key field, so the go-sdk's reflection-derived schema sets
// additionalProperties: false and declares no idempotency_key property --
// exactly the shape that made an unconditional idempotency_key injection
// fail schema validation before this fix (dispatch.go's
// acceptsIdempotencyKey).
type readProbeInput struct {
	ChannelID string `json:"channel_id"`
}

type readProbeOutput struct {
	ChannelID string `json:"channel_id"`
}

// writeProbeInput mirrors a real domain write tool's input shape: it
// implements whagent.IdempotencyKeyed by declaring idempotency_key as a
// JSON field, so its generated schema does carry that property and
// dispatch.go's Dispatch is expected to attach the key.
type writeProbeInput struct {
	Key string `json:"idempotency_key,omitempty"`
}

func (i writeProbeInput) IdempotencyKey() string { return i.Key }

type writeProbeOutput struct {
	ReceivedKey string `json:"received_key"`
}

// newDispatchTestServer starts a plain, unauthenticated in-process MCP
// server exposing one read tool (no idempotency_key in its schema) and one
// write tool (idempotency_key in its schema) -- everything Dispatch needs
// to exercise acceptsIdempotencyKey's read/write distinction, without any
// of audience_score_system's store/auth machinery this package must not
// depend on.
func newDispatchTestServer(t *testing.T) string {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "dispatch-test-server", Version: "0.1.0"}, nil)

	mcp.AddTool(srv, &mcp.Tool{Name: "read_probe", Description: "read-only probe with a strict schema"},
		func(_ context.Context, _ *mcp.CallToolRequest, in readProbeInput) (*mcp.CallToolResult, readProbeOutput, error) {
			return nil, readProbeOutput{ChannelID: in.ChannelID}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "write_probe", Description: "write probe expecting idempotency_key"},
		func(_ context.Context, _ *mcp.CallToolRequest, in writeProbeInput) (*mcp.CallToolResult, writeProbeOutput, error) {
			return nil, writeProbeOutput{ReceivedKey: in.Key}, nil
		})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// newDispatchTestFixture builds the whagent-net-side pieces Dispatch needs
// (a persona.Issuer over a throwaway signer, and a session) -- no ledger,
// no auth verification, since this file's server has neither.
func newDispatchTestFixture(t *testing.T, serverURL string) (*tools.Dispatcher, tools.DispatchInput) {
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
			Sub:  "human-dispatch-test-1",
			Kind: session.SubjectKindHuman,
		},
		AgentID: "dispatch-test-agent",
		Model:   "test-model",
		Status:  session.StatusRunning,
	}

	dispatcher := &tools.Dispatcher{Issuer: persona.NewIssuer(signer)}
	in := tools.DispatchInput{
		Session: sess,
		AgentID: sess.AgentID,
		ToolSet: []session.ToolServerRef{{ServerURL: serverURL}},
	}
	return dispatcher, in
}

// TestDispatch_ReadTool_NoIdempotencyKeyAttached is this fix's regression
// proof: a read tool whose schema has no idempotency_key property (and,
// like a real domain server's RegisterRead-generated schema,
// additionalProperties: false) must not have the call fail schema
// validation -- which is exactly what happened when Dispatch attached
// idempotency_key to every call unconditionally (see dispatch.go's
// acceptsIdempotencyKey).
func TestDispatch_ReadTool_NoIdempotencyKeyAttached(t *testing.T) {
	ctx := context.Background()
	serverURL := newDispatchTestServer(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id":"chan-1"}`}

	result, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	assert.False(t, result.IsError, "a read tool with a strict schema must not fail validation: %s", result.Content)
	assert.Contains(t, result.Content, "chan-1")
}

// TestDispatch_WriteTool_IdempotencyKeyAttached proves the flip side: a
// tool whose schema does declare idempotency_key (whagent.IdempotencyKeyed)
// still gets FR11's deterministic key attached.
func TestDispatch_WriteTool_IdempotencyKeyAttached(t *testing.T) {
	ctx := context.Background()
	serverURL := newDispatchTestServer(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "write_probe", Arguments: `{}`}

	result, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	require.False(t, result.IsError, "unexpected tool error: %s", result.Content)

	wantKey := whagent.DeriveIdempotencyKey(in.Session.SessionID.String(), in.Turn, in.CallIndex)
	assert.Contains(t, result.Content, wantKey)
}
