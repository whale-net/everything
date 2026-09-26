// No-database unit test for amend.go: RegisterAmendAll registers one tool
// per spec-axis entity kind, every tool rejects a missing or unknown
// krill_session_id before reaching the store, and a valid session passes
// the id and replacement content through to store.AmendStore unchanged.
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

func (f recordingAmendStore) AmendProduct(_ context.Context, id uuid.UUID, name, vision string) (store.Product, error) {
	*f.calls = append(*f.calls, amendCall{"product", id, name, nil})
	return store.Product{ID: id, Name: name, Vision: vision}, nil
}

func (f recordingAmendStore) AmendFeatureSet(_ context.Context, id uuid.UUID, name string, description *string) (store.FeatureSet, error) {
	*f.calls = append(*f.calls, amendCall{"feature_set", id, name, nil})
	return store.FeatureSet{ID: id, Name: name, Description: description}, nil
}

func (f recordingAmendStore) AmendFeature(_ context.Context, id uuid.UUID, name string, description *string) (store.Feature, error) {
	*f.calls = append(*f.calls, amendCall{"feature", id, name, nil})
	return store.Feature{ID: id, Name: name, Description: description}, nil
}

func (f recordingAmendStore) AmendPersona(_ context.Context, id uuid.UUID, name string, description *string) (store.Persona, error) {
	*f.calls = append(*f.calls, amendCall{"persona", id, name, nil})
	return store.Persona{ID: id, Name: name, Description: description}, nil
}

func (f recordingAmendStore) AmendNonGoal(_ context.Context, id uuid.UUID, name string, body *string) (store.NonGoal, error) {
	*f.calls = append(*f.calls, amendCall{"non_goal", id, name, body})
	return store.NonGoal{ID: id, Name: name, Body: body}, nil
}

func (f recordingAmendStore) AmendMilestone(_ context.Context, id uuid.UUID, name string, outcome *string) (store.MilestoneRef, error) {
	*f.calls = append(*f.calls, amendCall{"milestone", id, name, nil})
	return store.MilestoneRef{ID: id, Name: name, Outcome: outcome}, nil
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

// amendToolNames maps each registered tool to the store method it must
// reach. content names the extra required argument that kind's schema
// carries beyond id/name -- an amend that omits it is rejected before the
// store, so the "valid session reaches store" case has to supply it.
var amendToolNames = map[string]struct {
	entity  string
	content map[string]any
}{
	"amend_product":              {"product", map[string]any{"vision": "a new vision"}},
	"amend_feature_set":          {"feature_set", nil},
	"amend_feature":              {"feature", nil},
	"amend_requirement":          {"requirement", map[string]any{"body": "amended body"}},
	"amend_persona":              {"persona", nil},
	"amend_non_goal":             {"non_goal", map[string]any{"body": "amended body"}},
	"amend_load_bearing_decision": {"load_bearing_decision", map[string]any{"body": "amended body"}},
	"amend_milestone":            {"milestone", map[string]any{"outcome": "an amended outcome"}},
}

func TestRegisterAmendAll_RegistersEverySpecAxisKind(t *testing.T) {
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
	for name, tool := range amendToolNames {
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
				for k, v := range tool.content {
					args[k] = v
				}
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
	for name, tool := range amendToolNames {
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

				for k, v := range tool.content {
					tc.args[k] = v
				}
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
	for name, tc := range amendToolNames {
		t.Run(name, func(t *testing.T) {
			var calls []amendCall
			cs := connectAmendTools(t, sessionID, &calls)
			entityID := uuid.New()

			args := map[string]any{
				"krill_session_id": uuid.UUID(sessionID).String(),
				"id":               entityID.String(),
				"name":             "amended name",
			}
			for k, v := range tc.content {
				args[k] = v
			}

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
			require.NoError(t, err)
			require.False(t, res.IsError, "unexpected tool error: %s", amendTextOf(res))
			assert.Equal(t, map[string]any{"id": entityID.String()}, res.StructuredContent)

			require.Len(t, calls, 1)
			assert.Equal(t, tc.entity, calls[0].entity)
			assert.Equal(t, entityID, calls[0].id)
			assert.Equal(t, "amended name", calls[0].name)
		})
	}
}
