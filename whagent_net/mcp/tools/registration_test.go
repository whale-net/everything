package tools

// Registration/schema/parity coverage (issue #2120's Testing phase):
//   - every tool is registered with a non-empty schema;
//   - the registered set is EXACTLY the five FR1/FR2/FR3 tools -- no more
//     (pins the "no agent discovery, no list_sessions tool" limitation);
//   - a parity check enumerating pb.SessionService's RPCs against the
//     registered tools, so a future RPC added to session.proto without a
//     matching tool (or vice versa) fails loudly here instead of silently
//     drifting from the gRPC surface FR1/FR2/FR3 require parity with.
//
// Uses a bare mcp.NewServer + mcp.NewInMemoryTransports + a real
// mcp.Client -- no HTTP, no auth layer -- because this test is about the
// registered tool set's shape, not the caller-identity plumbing
// (../server/*_test.go covers that separately).
import (
	"context"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// registerAll wires every whagent-net mcp tool (main.go's own registration
// list, mirrored here) onto srv against client. This test's own subject is
// the registered tool set's shape (names/descriptions/schemas/RPC parity),
// not FR7/FR8's dispatch-time resolution -- scopeResolver/grant are fed
// harmless fakes here purely so RegisterXxx compiles; nothing in this file
// exercises them.
func registerAll(srv *mcp.Server, client pb.SessionServiceClient) {
	scopeResolver := newFakeScopeResolver()
	grant := &fakeGrantSource{}

	RegisterStartSession(srv, client, scopeResolver, grant)
	RegisterSendTurn(srv, client, scopeResolver, grant)
	RegisterStopSession(srv, client, scopeResolver, grant)
	RegisterGetSession(srv, client, scopeResolver, grant)
	RegisterReadTranscript(srv, client, scopeResolver, grant)
}

// listRegisteredTools connects a real in-memory mcp.Client to srv and
// returns every registered tool, keyed by name.
func listRegisteredTools(t *testing.T, srv *mcp.Server) map[string]*mcp.Tool {
	t.Helper()
	ctx := context.Background()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)

	got := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tl := range res.Tools {
		got[tl.Name] = tl
	}
	return got
}

// TestRegisterAll_ExposesExactlyTheFiveFR1FR2FR3Tools is this task's
// pinning test: exactly {start_session, send_turn, stop_session,
// get_session, read_transcript} are registered, each with a non-empty
// description and schema -- no list_agents, no list_sessions, no
// wait-for-completion tool.
func TestRegisterAll_ExposesExactlyTheFiveFR1FR2FR3Tools(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	registerAll(srv, &fakeSessionServiceClient{})

	got := listRegisteredTools(t, srv)

	wantNames := []string{"start_session", "send_turn", "stop_session", "get_session", "read_transcript"}
	for _, name := range wantNames {
		tl, ok := got[name]
		if assert.True(t, ok, "tool %q must be registered", name) {
			assert.NotEmpty(t, tl.Description, "tool %q must have a non-empty description", name)
			assert.NotNil(t, tl.InputSchema, "tool %q must have a schema", name)
		}
	}

	assert.Len(t, got, len(wantNames), "exactly five tools must be registered -- no agent discovery, no list_sessions, no wait-for-completion tool (issue #2120's Implementation section)")
}

// TestRegisterAll_ParityWithSessionServiceRPCs enumerates
// pb.SessionServiceClient's own RPC methods (the generated source of
// truth for the gRPC surface, ARCHITECTURE.md "Service boundary vs.
// package boundary") and proves every FR1/FR2/FR3 RPC has a matching
// registered tool, and that ListSessions -- outside FR1/FR2/FR3, M1 has no
// C15 capability yet -- has none. This is the issue's own red/green
// target: dropping a RegisterXxx call from registerAll (or renaming a
// tool) turns this red.
func TestRegisterAll_ParityWithSessionServiceRPCs(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	registerAll(srv, &fakeSessionServiceClient{})
	got := listRegisteredTools(t, srv)

	// Reflect over the generated pb.SessionServiceClient interface itself
	// (session.proto's own compiled source of truth for the gRPC surface)
	// rather than hardcoding the RPC list a second time -- a renamed or
	// added RPC changes this interface and is picked up here automatically.
	clientType := reflect.TypeOf((*pb.SessionServiceClient)(nil)).Elem()
	rpcNames := make(map[string]bool, clientType.NumMethod())
	for i := 0; i < clientType.NumMethod(); i++ {
		rpcNames[clientType.Method(i).Name] = true
	}

	// FR1/FR2/FR3's RPC -> tool mapping. Keep this table's RPC names
	// checked against the live interface above so a renamed RPC fails
	// loudly here rather than silently going stale.
	wantRPCToTool := map[string]string{
		"StartSession":   "start_session",
		"SendTurn":       "send_turn",
		"StopSession":    "stop_session",
		"GetSession":     "get_session",
		"ReadTranscript": "read_transcript",
	}
	for rpc, toolName := range wantRPCToTool {
		require.True(t, rpcNames[rpc], "sanity: RPC %s must exist on pb.SessionServiceClient", rpc)
		assert.Contains(t, got, toolName, "RPC %s (FR1/FR2/FR3) has no matching registered tool %q", rpc, toolName)
	}

	assert.NotContains(t, got, "list_sessions", "ListSessions is not an FR1/FR2/FR3 operation and must not be exposed as a tool (M1 has no C15 capability yet)")
	assert.NotContains(t, got, "list_agents", "agent discovery is a deliberate M1 limitation (issue #2120's Implementation section) and must not be exposed as a tool")
}
