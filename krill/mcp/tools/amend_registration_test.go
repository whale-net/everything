// No-database unit test for amend.go: RegisterAmendAll registers exactly
// {amend_requirement, amend_load_bearing_decision}, both reject a missing
// or unknown krill_session_id before reaching the store, and a valid
// session passes id/name/body through to store.AmendStore unchanged.
// Stores are in-memory fakes driven over a real in-memory MCP connection
// behind the real server.PersonaMiddleware, like discovery_registration_test.go.
package tools_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// amendSessionStore overrides only GetSession; known is the one valid id.
type amendSessionStore struct {
	store.SessionStore
	known store.SessionID
}

func (f amendSessionStore) GetSession(_ context.Context, id store.SessionID) (store.Session, error) {
	if id != f.known {
		return store.Session{}, store.ErrSessionNotFound
	}
	return store.Session{ID: id, ScopeID: uuid.New()}, nil
}

// amendCall records one AmendStore invocation.
type amendCall struct {
	entity string
	id     uuid.UUID
	name   string
	body   *string
}

// recordingAmendStore records every call and echoes the id back.
type recordingAmendStore struct {
	calls *[]amendCall
}

func (f recordingAmendStore) AmendRequirement(_ context.Context, id uuid.UUID, name string, body *string) (store.Requirement, error) {
	*f.calls = append(*f.calls, amendCall{"requirement", id, name, body})
	return store.Requirement{ID: id, Name: name, Body: body}, nil
}

func (f recordingAmendStore) AmendLoadBearingDecision(_ context.Context, id uuid.UUID, name string, body *string) (store.LoadBearingDecision, error) {
	*f.calls = append(*f.calls, amendCall{"load_bearing_decision", id, name, body})
	return store.LoadBearingDecision{ID: id, Name: name, Body: body}, nil
}

// amendOperatorPersona resolves PersonaSwarmOperator for every tools/call
// through the real server.PersonaMiddleware.
func amendOperatorPersona(next mcp.MethodHandler) mcp.MethodHandler {
	gated := server.PersonaMiddleware()(next)
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if !ok {
			return next(ctx, method, req)
		}
		if call.Extra == nil {
			call.Extra = &mcp.RequestExtra{}
		}
		call.Extra.TokenInfo = &auth.TokenInfo{UserID: "operator-1"}
		return gated(ctx, method, req)
	}
}

func connectAmendTools(t *testing.T, sessionID store.SessionID, calls *[]amendCall) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(amendOperatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterAmendAll(reg, amendSessionStore{known: sessionID}, recordingAmendStore{calls: calls})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func amendTextOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			out += text.Text
		}
	}
	return out
}

var amendToolNames = map[string]string{
	"amend_requirement":           "requirement",
	"amend_load_bearing_decision": "load_bearing_decision",
}

func TestRegisterAmendAll_RegistersExactlyTwoTools(t *testing.T) {
	var calls []amendCall
	cs := connectAmendTools(t, store.SessionID(uuid.New()), &calls)

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}
	require.Len(t, registered, len(amendToolNames))
	for name := range amendToolNames {
		assert.True(t, registered[name], "%s must be registered", name)
	}
}

func TestAmendTools_RejectMissingOrUnknownSession(t *testing.T) {
	for name := range amendToolNames {
		for label, tc := range map[string]struct {
			session any
			want    string
		}{
			// Rejected by the input schema's required list before the handler runs.
			"missing":   {nil, "krill_session_id"},
			"malformed": {"not-a-uuid", "krill_session_id: invalid"},
			"unknown":   {uuid.NewString(), "unknown krill session"},
		} {
			t.Run(name+"/"+label, func(t *testing.T) {
				var calls []amendCall
				cs := connectAmendTools(t, store.SessionID(uuid.New()), &calls)

				args := map[string]any{"id": uuid.NewString(), "name": "renamed"}
				if tc.session != nil {
					args["krill_session_id"] = tc.session
				}
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
				require.NoError(t, err)
				assert.True(t, res.IsError, "expected a tool error")
				assert.Contains(t, amendTextOf(res), tc.want)
				assert.Empty(t, calls, "a rejected session must never reach the store")
			})
		}
	}
}

func TestAmendTools_RejectInvalidFieldsBeforeStore(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	for name := range amendToolNames {
		for label, tc := range map[string]struct {
			args map[string]any
			want string
		}{
			"bad id":     {map[string]any{"id": "nope", "name": "x"}, "id: invalid"},
			"empty name": {map[string]any{"id": uuid.NewString(), "name": ""}, "name"},
		} {
			t.Run(name+"/"+label, func(t *testing.T) {
				var calls []amendCall
				cs := connectAmendTools(t, sessionID, &calls)

				tc.args["krill_session_id"] = uuid.UUID(sessionID).String()
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: tc.args})
				require.NoError(t, err)
				assert.True(t, res.IsError, "expected a tool error")
				assert.Contains(t, amendTextOf(res), tc.want)
				assert.Empty(t, calls)
			})
		}
	}
}

func TestAmendTools_ValidSessionReachesStore(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	for name, entity := range amendToolNames {
		t.Run(name, func(t *testing.T) {
			var calls []amendCall
			cs := connectAmendTools(t, sessionID, &calls)
			entityID := uuid.New()

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{
				"krill_session_id": uuid.UUID(sessionID).String(),
				"id":               entityID.String(),
				"name":             "amended name",
				"body":             "amended body",
			}})
			require.NoError(t, err)
			require.False(t, res.IsError, "unexpected tool error: %s", amendTextOf(res))
			assert.Equal(t, map[string]any{"id": entityID.String()}, res.StructuredContent)

			require.Len(t, calls, 1)
			assert.Equal(t, entity, calls[0].entity)
			assert.Equal(t, entityID, calls[0].id)
			assert.Equal(t, "amended name", calls[0].name)
			require.NotNil(t, calls[0].body)
			assert.Equal(t, "amended body", *calls[0].body)
		})
	}
}
