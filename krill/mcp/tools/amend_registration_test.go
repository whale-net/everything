// No-database unit test for amend.go: RegisterAmendAll registers one tool
// per spec-axis entity kind plus reparent_feature, every tool rejects a
// missing or unknown krill_session_id before reaching the store, a valid
// session passes the id and replacement content through to
// store.AmendStore unchanged, and the placement refusal names the verb that
// actually exists for the (kind, field) the caller tried to change.
// Stores are in-memory fakes driven over a real in-memory MCP connection
// behind the real server.PersonaMiddleware, like discovery_registration_test.go.
package tools_test

import (
	"context"
	"fmt"
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

// reparentCall records one ReparentStore invocation.
type reparentCall struct {
	id         uuid.UUID
	featureSet uuid.UUID
}

// recordingReparentStore records every call and echoes the feature id back.
type recordingReparentStore struct {
	calls *[]reparentCall
}

func (f recordingReparentStore) ReparentFeature(_ context.Context, id, featureSetID uuid.UUID) (store.Feature, error) {
	*f.calls = append(*f.calls, reparentCall{id: id, featureSet: featureSetID})
	return store.Feature{ID: id, FeatureSetID: featureSetID}, nil
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
	return connectAmendToolsWithReparent(t, sessionID, calls, &[]reparentCall{})
}

func connectAmendToolsWithReparent(t *testing.T, sessionID store.SessionID, calls *[]amendCall, reparents *[]reparentCall) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(amendOperatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterAmendAll(reg, amendSessionStore{known: sessionID}, recordingAmendStore{calls: calls}, recordingReparentStore{calls: reparents})

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
	"amend_product":               {"product", map[string]any{"vision": "a new vision"}},
	"amend_feature_set":           {"feature_set", nil},
	"amend_feature":               {"feature", nil},
	"amend_requirement":           {"requirement", map[string]any{"body": "amended body"}},
	"amend_persona":               {"persona", nil},
	"amend_non_goal":              {"non_goal", map[string]any{"body": "amended body"}},
	"amend_load_bearing_decision": {"load_bearing_decision", map[string]any{"body": "amended body"}},
	"amend_milestone":             {"milestone", map[string]any{"outcome": "an amended outcome"}},
}

// reparentFeatureToolName is registered by the same fan-out as the amends:
// an amend refusal that names a verb the mount does not have is a dead end.
const reparentFeatureToolName = "reparent_feature"

func TestRegisterAmendAll_RegistersEverySpecAxisKind(t *testing.T) {
	var calls []amendCall
	cs := connectAmendTools(t, store.SessionID(uuid.New()), &calls)

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}
	require.Len(t, registered, len(amendToolNames)+1)
	for name := range amendToolNames {
		assert.True(t, registered[name], "%s must be registered", name)
	}
	assert.True(t, registered[reparentFeatureToolName], "%s must be registered", reparentFeatureToolName)
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

func TestReparentFeature_RejectsBadSessionBeforeStore(t *testing.T) {
	for label, tc := range map[string]struct {
		session any
		want    string
	}{
		"missing":   {nil, "krill_session_id"},
		"malformed": {"not-a-uuid", "krill_session_id: invalid"},
		"unknown":   {uuid.NewString(), "unknown krill session"},
	} {
		t.Run(label, func(t *testing.T) {
			var calls []amendCall
			var reparents []reparentCall
			cs := connectAmendToolsWithReparent(t, store.SessionID(uuid.New()), &calls, &reparents)

			args := map[string]any{"feature_id": uuid.NewString(), "feature_set_id": uuid.NewString()}
			if tc.session != nil {
				args["krill_session_id"] = tc.session
			}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: reparentFeatureToolName, Arguments: args})
			require.NoError(t, err)
			assert.True(t, res.IsError, "expected a tool error")
			assert.Contains(t, amendTextOf(res), tc.want)
			assert.Empty(t, reparents, "a rejected session must never reach the store")
		})
	}
}

func TestReparentFeature_RejectsInvalidFieldsBeforeStore(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	for label, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"bad feature_id":       {map[string]any{"feature_id": "nope", "feature_set_id": uuid.NewString()}, "id: invalid"},
		"bad feature_set_id":   {map[string]any{"feature_id": uuid.NewString(), "feature_set_id": "nope"}, "feature_set_id: invalid"},
		"empty feature_set_id": {map[string]any{"feature_id": uuid.NewString(), "feature_set_id": ""}, "feature_set_id: invalid"},
	} {
		t.Run(label, func(t *testing.T) {
			var calls []amendCall
			var reparents []reparentCall
			cs := connectAmendToolsWithReparent(t, sessionID, &calls, &reparents)

			tc.args["krill_session_id"] = uuid.UUID(sessionID).String()
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: reparentFeatureToolName, Arguments: tc.args})
			require.NoError(t, err)
			assert.True(t, res.IsError, "expected a tool error")
			assert.Contains(t, amendTextOf(res), tc.want)
			assert.Empty(t, reparents)
		})
	}
}

func TestReparentFeature_ValidSessionReachesStore(t *testing.T) {
	var calls []amendCall
	var reparents []reparentCall
	sessionID := store.SessionID(uuid.New())
	cs := connectAmendToolsWithReparent(t, sessionID, &calls, &reparents)

	featureID, featureSetID := uuid.New(), uuid.New()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: reparentFeatureToolName,
		Arguments: map[string]any{
			"krill_session_id": uuid.UUID(sessionID).String(),
			"feature_id":       featureID.String(),
			"feature_set_id":   featureSetID.String(),
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error: %s", amendTextOf(res))
	assert.Equal(t, map[string]any{"id": featureID.String()}, res.StructuredContent)

	require.Len(t, reparents, 1)
	assert.Equal(t, featureID, reparents[0].id)
	assert.Equal(t, featureSetID, reparents[0].featureSet)
	assert.Empty(t, calls, "a reparent must never reach AmendStore")
}

// TestReparentFeature_DescriptionClaimsOnlyWhatItDelivers pins the claims
// the description is allowed to make: the same id, SCD2 rather than
// delete, the Cn number and children carried forward, and the same-product
// bound the store actually enforces.
func TestReparentFeature_DescriptionClaimsOnlyWhatItDelivers(t *testing.T) {
	var calls []amendCall
	cs := connectAmendTools(t, store.SessionID(uuid.New()), &calls)

	var description string
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		if tool.Name == reparentFeatureToolName {
			description = tool.Description
		}
	}
	require.NotEmpty(t, description)
	for _, claim := range []string{
		"same id", "SCD2", "closed, not deleted", "Cn display number",
		"requirements", "delivery associations", "another product is refused",
	} {
		assert.Contains(t, description, claim)
	}
	assert.NotContains(t, description, "across products")
	assert.NotContains(t, description, "any product")
}

// TestAmendFeature_PlacementChangeStillRefusedNamingReparentFeature: the
// amend still never reparents, but the refusal a Feature gets names the
// verb that does the move.
func TestAmendFeature_PlacementChangeStillRefusedNamingReparentFeature(t *testing.T) {
	var calls []amendCall
	var reparents []reparentCall
	cs := connectAmendToolsWithReparent(t, store.SessionID(uuid.New()), &calls, &reparents)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "amend_feature",
		Arguments: map[string]any{
			"krill_session_id": uuid.NewString(),
			"id":               uuid.NewString(),
			"name":             "renamed",
			"feature_set_id":   uuid.NewString(),
		},
	})
	require.NoError(t, err)
	require.True(t, res.IsError, "an amend must never reparent")
	assert.Contains(t, amendTextOf(res), "feature cannot change feature_set_id on amend")
	assert.Contains(t, amendTextOf(res), "reparent_feature")
	assert.Empty(t, calls, "a refused placement change must not reach the store")
	assert.Empty(t, reparents)
}

// TestAmendPlacementRefuse_AdvicePerKindAndField is the decision table the
// refusal message is built from, one case per (entity kind, field): which
// of the five fields is that kind's own parent, and what the message says
// when it is not. A kind that has no such parent must never be pointed at
// another kind's verb.
func TestAmendPlacementRefuse_AdvicePerKindAndField(t *testing.T) {
	const (
		notAPlacement  = "%s is not a placement of a %s, so there is nothing here to reparent"
		noReparentVerb = "a %s has no reparent verb"
		reparentVerb   = "reparent it with reparent_feature"
	)

	// want[kind][field] is the advice substring the message must carry.
	want := map[string]map[string]string{
		"product": {
			"product_id":          fmt.Sprintf(notAPlacement, "product_id", "product"),
			"feature_set_id":      fmt.Sprintf(notAPlacement, "feature_set_id", "product"),
			"feature_id":          fmt.Sprintf(notAPlacement, "feature_id", "product"),
			"parent_milestone_id": fmt.Sprintf(notAPlacement, "parent_milestone_id", "product"),
			"kind":                fmt.Sprintf(notAPlacement, "kind", "product"),
		},
		"feature set": {
			"product_id":          fmt.Sprintf(noReparentVerb, "feature set"),
			"feature_set_id":      fmt.Sprintf(notAPlacement, "feature_set_id", "feature set"),
			"feature_id":          fmt.Sprintf(notAPlacement, "feature_id", "feature set"),
			"parent_milestone_id": fmt.Sprintf(notAPlacement, "parent_milestone_id", "feature set"),
			"kind":                fmt.Sprintf(notAPlacement, "kind", "feature set"),
		},
		"feature": {
			"product_id":          fmt.Sprintf(notAPlacement, "product_id", "feature"),
			"feature_set_id":      reparentVerb,
			"feature_id":          fmt.Sprintf(notAPlacement, "feature_id", "feature"),
			"parent_milestone_id": fmt.Sprintf(notAPlacement, "parent_milestone_id", "feature"),
			"kind":                fmt.Sprintf(notAPlacement, "kind", "feature"),
		},
		"requirement": {
			"product_id":          fmt.Sprintf(notAPlacement, "product_id", "requirement"),
			"feature_set_id":      fmt.Sprintf(notAPlacement, "feature_set_id", "requirement"),
			"feature_id":          fmt.Sprintf(noReparentVerb, "requirement"),
			"parent_milestone_id": fmt.Sprintf(notAPlacement, "parent_milestone_id", "requirement"),
			"kind":                "FR/NFR kind is fixed at creation",
		},
		"persona": {
			"product_id":          fmt.Sprintf(noReparentVerb, "persona"),
			"feature_set_id":      fmt.Sprintf(notAPlacement, "feature_set_id", "persona"),
			"feature_id":          fmt.Sprintf(notAPlacement, "feature_id", "persona"),
			"parent_milestone_id": fmt.Sprintf(notAPlacement, "parent_milestone_id", "persona"),
			"kind":                fmt.Sprintf(notAPlacement, "kind", "persona"),
		},
		"non-goal": {
			"product_id":          fmt.Sprintf(noReparentVerb, "non-goal"),
			"feature_set_id":      fmt.Sprintf(notAPlacement, "feature_set_id", "non-goal"),
			"feature_id":          fmt.Sprintf(notAPlacement, "feature_id", "non-goal"),
			"parent_milestone_id": fmt.Sprintf(notAPlacement, "parent_milestone_id", "non-goal"),
			"kind":                "re-kind a non_goal with resolve_non_goal",
		},
		"load-bearing decision": {
			"product_id":          fmt.Sprintf(notAPlacement, "product_id", "load-bearing decision"),
			"feature_set_id":      fmt.Sprintf(noReparentVerb, "load-bearing decision"),
			"feature_id":          fmt.Sprintf(notAPlacement, "feature_id", "load-bearing decision"),
			"parent_milestone_id": fmt.Sprintf(notAPlacement, "parent_milestone_id", "load-bearing decision"),
			"kind":                fmt.Sprintf(notAPlacement, "kind", "load-bearing decision"),
		},
		"milestone": {
			"product_id":          "a milestone's parent is fixed at create",
			"feature_set_id":      fmt.Sprintf(notAPlacement, "feature_set_id", "milestone"),
			"feature_id":          fmt.Sprintf(notAPlacement, "feature_id", "milestone"),
			"parent_milestone_id": "only a milepebble is parented to a milestone",
			"kind":                fmt.Sprintf(notAPlacement, "kind", "milestone"),
		},
	}

	fields := []string{"product_id", "feature_set_id", "feature_id", "parent_milestone_id", "kind"}
	for kind, byField := range want {
		for _, field := range fields {
			t.Run(kind+"/"+field, func(t *testing.T) {
				value := uuid.NewString()
				placement := store.AmendPlacementChange{}
				switch field {
				case "product_id":
					placement.ProductID = &value
				case "feature_set_id":
					placement.FeatureSetID = &value
				case "feature_id":
					placement.FeatureID = &value
				case "parent_milestone_id":
					placement.ParentMilestoneID = &value
				case "kind":
					placement.Kind = &value
				}

				err := placement.Refuse(kind)
				require.Error(t, err)
				assert.ErrorIs(t, err, store.ErrPlacementChange)
				assert.Contains(t, err.Error(), fmt.Sprintf("%s cannot change %s on amend", kind, field))
				assert.Contains(t, err.Error(), byField[field])
				if byField[field] != reparentVerb {
					assert.NotContains(t, err.Error(), reparentVerb,
						"only a feature's feature_set_id change may name a feature's verb")
				}
			})
		}
	}

	// A change of none of them is not a refusal at all.
	assert.NoError(t, (store.AmendPlacementChange{}).Refuse("feature"))
}
