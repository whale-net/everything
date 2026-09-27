// No-database unit test for amend.go: RegisterAmendAll registers one tool
// per spec-axis entity kind, every tool rejects a missing or unknown
// krill_session_id before reaching the store, and a valid session passes
// the id and replacement content through to store.AmendStore unchanged.
// Stores are in-memory fakes driven over a real in-memory MCP connection
// behind the real server.PersonaMiddleware, like discovery_registration_test.go.
package tools_test

import (
	"context"
	"encoding/json"
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

// amendCall records one AmendStore invocation. name is the replacement name
// for the named kinds; amend_deferral has none and records its body/destination
// instead.
type amendCall struct {
	entity      string
	id          uuid.UUID
	name        string
	body        *string
	destination string
}

// recordingAmendStore records every call and echoes the id back.
type recordingAmendStore struct {
	calls *[]amendCall
}

func (f recordingAmendStore) AmendRequirement(_ context.Context, id uuid.UUID, name string, body *string) (store.Requirement, error) {
	*f.calls = append(*f.calls, amendCall{entity: "requirement", id: id, name: name, body: body})
	return store.Requirement{ID: id, Name: name, Body: body}, nil
}

func (f recordingAmendStore) AmendLoadBearingDecision(_ context.Context, id uuid.UUID, name string, body *string) (store.LoadBearingDecision, error) {
	*f.calls = append(*f.calls, amendCall{entity: "load_bearing_decision", id: id, name: name, body: body})
	return store.LoadBearingDecision{ID: id, Name: name, Body: body}, nil
}

func (f recordingAmendStore) AmendProduct(_ context.Context, id uuid.UUID, name, vision string) (store.Product, error) {
	*f.calls = append(*f.calls, amendCall{entity: "product", id: id, name: name, body: nil})
	return store.Product{ID: id, Name: name, Vision: vision}, nil
}

func (f recordingAmendStore) AmendFeatureSet(_ context.Context, id uuid.UUID, name string, description *string) (store.FeatureSet, error) {
	*f.calls = append(*f.calls, amendCall{entity: "feature_set", id: id, name: name, body: nil})
	return store.FeatureSet{ID: id, Name: name, Description: description}, nil
}

func (f recordingAmendStore) AmendFeature(_ context.Context, id uuid.UUID, name string, description *string) (store.Feature, error) {
	*f.calls = append(*f.calls, amendCall{entity: "feature", id: id, name: name, body: nil})
	return store.Feature{ID: id, Name: name, Description: description}, nil
}

func (f recordingAmendStore) AmendPersona(_ context.Context, id uuid.UUID, name string, description *string) (store.Persona, error) {
	*f.calls = append(*f.calls, amendCall{entity: "persona", id: id, name: name, body: nil})
	return store.Persona{ID: id, Name: name, Description: description}, nil
}

func (f recordingAmendStore) AmendNonGoal(_ context.Context, id uuid.UUID, name string, body *string) (store.NonGoal, error) {
	*f.calls = append(*f.calls, amendCall{entity: "non_goal", id: id, name: name, body: body})
	return store.NonGoal{ID: id, Name: name, Body: body}, nil
}

func (f recordingAmendStore) AmendMilestone(_ context.Context, id uuid.UUID, name string, outcome *string) (store.MilestoneRef, error) {
	*f.calls = append(*f.calls, amendCall{entity: "milestone", id: id, name: name, body: nil})
	return store.MilestoneRef{ID: id, Name: name, Outcome: outcome}, nil
}

func (f recordingAmendStore) AmendDeferral(_ context.Context, id uuid.UUID, body, destination string) (store.MilestoneDeferral, error) {
	*f.calls = append(*f.calls, amendCall{entity: "deferral", id: id, body: &body, destination: destination})
	return store.MilestoneDeferral{ID: id, Body: body, Destination: destination}, nil
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
// noName marks the one amend whose schema has no name field at all, so the
// shared name assertions do not apply to it.
var amendToolNames = map[string]struct {
	entity  string
	content map[string]any
	noName  bool
}{
	"amend_product":               {entity: "product", content: map[string]any{"vision": "a new vision"}},
	"amend_feature_set":           {entity: "feature_set"},
	"amend_feature":               {entity: "feature"},
	"amend_requirement":           {entity: "requirement", content: map[string]any{"body": "amended body"}},
	"amend_persona":               {entity: "persona"},
	"amend_non_goal":              {entity: "non_goal", content: map[string]any{"body": "amended body"}},
	"amend_load_bearing_decision": {entity: "load_bearing_decision", content: map[string]any{"body": "amended body"}},
	"amend_milestone":             {entity: "milestone", content: map[string]any{"outcome": "an amended outcome"}},
	"amend_deferral":              {entity: "deferral", content: map[string]any{"body": "amended body", "destination": "M2"}, noName: true},
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

				args := map[string]any{"id": uuid.NewString()}
				if !tool.noName {
					args["name"] = "renamed"
				}
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
		cases := map[string]struct {
			args map[string]any
			want string
		}{}

		if tool.noName {
			// amend_deferral has no name to refuse on; FR1's destination
			// rule is the field check it carries instead.
			cases["bad id"] = struct {
				args map[string]any
				want string
			}{args: map[string]any{"id": "nope", "body": "b", "destination": "M2"}, want: "id: invalid"}
			cases["empty destination"] = struct {
				args map[string]any
				want string
			}{args: map[string]any{"id": uuid.NewString(), "body": "b", "destination": ""}, want: "destination: required"}
		} else {
			cases["bad id"] = struct {
				args map[string]any
				want string
			}{args: map[string]any{"id": "nope", "name": "x"}, want: "id: invalid"}
			cases["empty name"] = struct {
				args map[string]any
				want string
			}{args: map[string]any{"id": uuid.NewString(), "name": ""}, want: "name"}
		}

		for label, tc := range cases {
			t.Run(name+"/"+label, func(t *testing.T) {
				var calls []amendCall
				cs := connectAmendTools(t, sessionID, &calls)

				// content fills in the tool's other required fields; the
				// case's own args then override, so a case can blank one.
				args := map[string]any{}
				for k, v := range tool.content {
					args[k] = v
				}
				for k, v := range tc.args {
					args[k] = v
				}
				args["krill_session_id"] = uuid.UUID(sessionID).String()
				tc.args = args
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
			}
			if !tc.noName {
				args["name"] = "amended name"
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
			if tc.noName {
				require.NotNil(t, calls[0].body)
				assert.Equal(t, "amended body", *calls[0].body)
				assert.Equal(t, "M2", calls[0].destination)
			} else {
				assert.Equal(t, "amended name", calls[0].name)
			}
		})
	}
}

// TestAmendDeferral_SchemaKeysOnTheDeferralIDAndTakesNoSubject is
// amend_deferral's two schema contracts: it is keyed on the deferral's own
// id (a milestone carries many, so milestone_id cannot identify one), and
// the subject pair is never a caller-supplied field (LB4) -- it comes from
// the resolved krill session alone.
func TestAmendDeferral_SchemaKeysOnTheDeferralIDAndTakesNoSubject(t *testing.T) {
	var calls []amendCall
	cs := connectAmendTools(t, store.SessionID(uuid.New()), &calls)

	var inputSchema any
	found := false
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		if tool.Name == "amend_deferral" {
			inputSchema = tool.InputSchema
			found = true
		}
	}
	require.True(t, found, "amend_deferral must be registered")

	raw, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "schema has no properties object: %s", raw)

	for _, field := range []string{"acting", "on_behalf_of", "created_by_acting", "created_by_on_behalf_of", "created_by"} {
		assert.NotContains(t, props, field,
			"amend_deferral must not accept a caller-supplied %s: the subject pair comes from the resolved krill session (LB4)", field)
	}

	// Keyed on the deferral, not the milestone: a milestone carries several.
	_, hasDeferralID := props["id"]
	assert.True(t, hasDeferralID, "amend_deferral must be keyed on the deferral's own id: %s", raw)
	assert.NotContains(t, props, "milestone_id",
		"milestone_id cannot say which of a milestone's several deferrals is being amended: %s", raw)
	_, hasName := props["name"]
	assert.False(t, hasName, "a deferral has no name to replace: %s", raw)

	required, ok := schema["required"].([]any)
	require.True(t, ok, "schema has no required list: %s", raw)
	for _, field := range []string{"krill_session_id", "id", "body", "destination"} {
		assert.Contains(t, required, field, "amend_deferral must require %q", field)
	}
}
