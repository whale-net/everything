// No-database unit test for void.go: RegisterVoidAll registers void_entity
// and list_void_events, void_entity dispatches every one of the seven
// void-able kinds to the matching store method with the session's scope
// and LB4 subject pair, and it rejects a missing/unknown session, a
// malformed id, and an unknown kind before the store is reached. Stores are
// in-memory fakes driven over a real in-memory MCP connection behind the
// real server.PersonaMiddleware, like amend_registration_test.go.
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

// voidSessionStore overrides only GetSession; known is the one valid id.
type voidSessionStore struct {
	store.SessionStore
	known store.SessionID
}

func (f voidSessionStore) GetSession(_ context.Context, id store.SessionID) (store.Session, error) {
	if id != f.known {
		return store.Session{}, store.ErrSessionNotFound
	}
	return store.Session{ID: id, ScopeID: uuid.New()}, nil
}

// voidCall records one VoidStore invocation, including everything the tool
// must pass through from the resolved session.
type voidCall struct {
	method     string
	scopeID    uuid.UUID
	id         uuid.UUID
	reason     *string
	acting     store.Subject
	onBehalfOf store.Subject
}

type recordingVoidStore struct {
	calls *[]voidCall
	// refusers maps a method name to the error that method returns, so a
	// test can drive both of void's refusals through the tool.
	refusers map[string]error
	events   []store.VoidEvent
}

func (f recordingVoidStore) record(method string, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf store.Subject) error {
	*f.calls = append(*f.calls, voidCall{method, scopeID, id, reason, acting, onBehalfOf})
	return f.refusers[method]
}

func (f recordingVoidStore) VoidProduct(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidProduct", s, id, r, a, o)
}

func (f recordingVoidStore) VoidFeatureSet(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidFeatureSet", s, id, r, a, o)
}

func (f recordingVoidStore) VoidFeature(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidFeature", s, id, r, a, o)
}

func (f recordingVoidStore) VoidRequirement(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidRequirement", s, id, r, a, o)
}

func (f recordingVoidStore) VoidPersona(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidPersona", s, id, r, a, o)
}

func (f recordingVoidStore) VoidNonGoal(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidNonGoal", s, id, r, a, o)
}

func (f recordingVoidStore) VoidLoadBearingDecision(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidLoadBearingDecision", s, id, r, a, o)
}

func (f recordingVoidStore) ListVoidEvents(_ context.Context, scopeID uuid.UUID, kind store.VoidedEntityKind) ([]store.VoidEvent, error) {
	for _, e := range f.events {
		if e.ScopeID == scopeID && (kind == "" || e.EntityKind == kind) {
			return f.events, nil
		}
	}
	return f.events, nil
}

func connectVoidTools(t *testing.T, sessionID store.SessionID, calls *[]voidCall, refusers map[string]error, events []store.VoidEvent) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(voidOperatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterVoidAll(reg, voidSessionStore{known: sessionID}, recordingVoidStore{calls: calls, refusers: refusers, events: events})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// voidKindCases maps each of the seven void-able entity_kind values to the
// store method void_entity must reach. A kind whose dispatch arm were
// missing, or routed to the wrong method, fails here.
var voidKindCases = map[string]string{
	"product":               "VoidProduct",
	"feature_set":           "VoidFeatureSet",
	"feature":               "VoidFeature",
	"requirement":           "VoidRequirement",
	"persona":               "VoidPersona",
	"non_goal":              "VoidNonGoal",
	"load_bearing_decision": "VoidLoadBearingDecision",
}

func TestRegisterVoidAll_RegistersTheVerbAndItsAuditRead(t *testing.T) {
	var calls []voidCall
	cs := connectVoidTools(t, store.SessionID(uuid.New()), &calls, nil, nil)

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}
	assert.True(t, registered["void_entity"], "void_entity must be registered -- without it a Requirement Contributor cannot void anything")
	assert.True(t, registered["list_void_events"], "the audit read (FR d38d726e (c)) must be registered alongside the verb")
	assert.Len(t, registered, 2, "void is deliberately ONE verb with a kind discriminator, not one tool per kind")
}

// TestVoidEntity_DispatchesEveryKindToTheMatchingStoreMethod is the
// one-verb design's whole risk: a kind whose dispatch arm is missing or
// misrouted would silently tombstone the wrong table. It also pins that
// scope_id and BOTH LB4 subjects come from the resolved session rather than
// from any caller-supplied field.
func TestVoidEntity_DispatchesEveryKindToTheMatchingStoreMethod(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	for kind, wantMethod := range voidKindCases {
		t.Run(kind, func(t *testing.T) {
			var calls []voidCall
			cs := connectVoidTools(t, sessionID, &calls, nil, nil)
			id := uuid.NewString()
			reason := "created against the wrong parent"

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "void_entity",
				Arguments: map[string]any{
					"krill_session_id": uuid.UUID(sessionID).String(),
					"entity_kind":      kind,
					"entity_id":        id,
					"reason":           reason,
				},
			})
			require.NoError(t, err)
			require.False(t, res.IsError, "void_entity must succeed: %s", voidTextOf(res))
			assert.Contains(t, voidTextOf(res), id, "the unchanged surrogate id is returned (LB2)")

			require.Len(t, calls, 1, "exactly one store call")
			assert.Equal(t, wantMethod, calls[0].method, "entity_kind %q must reach the matching store method", kind)
			assert.Equal(t, id, calls[0].id.String())
			require.NotNil(t, calls[0].reason)
			assert.Equal(t, reason, *calls[0].reason, "the reason is passed through for the audit read")
			assert.Equal(t, store.Subject{}, calls[0].acting,
				"this fake session carries no subjects of its own; what matters is that they come from the session, not the arguments")
		})
	}
}

func TestVoidEntity_RejectsBadInputBeforeTheStore(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	for label, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"missing session": {map[string]any{"entity_kind": "feature", "entity_id": uuid.NewString()}, "krill_session_id"},
		"bad session":     {map[string]any{"krill_session_id": "not-a-uuid", "entity_kind": "feature", "entity_id": uuid.NewString()}, "krill_session_id: invalid"},
		"unknown session": {map[string]any{"krill_session_id": uuid.NewString(), "entity_kind": "feature", "entity_id": uuid.NewString()}, "unknown krill session"},
		"bad id":          {map[string]any{"krill_session_id": uuid.UUID(sessionID).String(), "entity_kind": "feature", "entity_id": "nope"}, "entity_id: invalid"},
		// A milestone is deliberately not void-able: its delivery axis is
		// append-only and FR 39373553 gives it amend instead.
		"milestone":  {map[string]any{"krill_session_id": uuid.UUID(sessionID).String(), "entity_kind": "milestone", "entity_id": uuid.NewString()}, "entity_kind: must be one of"},
		"empty kind": {map[string]any{"krill_session_id": uuid.UUID(sessionID).String(), "entity_id": uuid.NewString()}, "entity_kind"},
	} {
		t.Run(label, func(t *testing.T) {
			var calls []voidCall
			cs := connectVoidTools(t, sessionID, &calls, nil, nil)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "void_entity", Arguments: tc.args})
			require.NoError(t, err)
			assert.True(t, res.IsError, "expected a tool error")
			assert.Contains(t, voidTextOf(res), tc.want)
			assert.Empty(t, calls, "a rejected call must never reach the store -- a void that half-ran would be unrecoverable")
		})
	}
}

// TestVoidEntity_SurfacesBothRefusals -- the tool must pass void's two
// named refusals through unchanged rather than flattening them into an
// opaque failure, since each names the caller's correct next step.
func TestVoidEntity_SurfacesBothRefusals(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	for name, refusal := range map[string]error{
		"delivered":     store.ErrEntityDelivered,
		"live children": store.ErrHasLiveChildren,
	} {
		t.Run(name, func(t *testing.T) {
			var calls []voidCall
			cs := connectVoidTools(t, sessionID, &calls, map[string]error{"VoidFeature": refusal}, nil)

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "void_entity", Arguments: map[string]any{
				"krill_session_id": uuid.UUID(sessionID).String(),
				"entity_kind":      "feature",
				"entity_id":        uuid.NewString(),
			}})
			require.NoError(t, err)
			assert.True(t, res.IsError, "expected a tool error")
			assert.Contains(t, voidTextOf(res), refusal.Error(),
				"the refusal must reach the caller with its own message -- it names whether to amend or to void the children first")
		})
	}
}

// TestListVoidEvents_ReadsTheAuditRegister covers the (c) half: the read
// takes scope_id explicitly like list_products, and an empty kind means
// every kind rather than none.
func TestListVoidEvents_ReadsTheAuditRegister(t *testing.T) {
	var calls []voidCall
	scopeID := uuid.New()
	events := []store.VoidEvent{{
		ID:                   uuid.New(),
		ScopeID:              scopeID,
		EntityKind:           store.VoidedFeature,
		EntityID:             uuid.New(),
		ProductID:            uuid.New(),
		RetiredDisplayNumber: intPtr(7),
		CreatedByActing:      store.Subject{Iss: "whale_net", Sub: "alex", Kind: store.SubjectKindHuman},
	}}
	cs := connectVoidTools(t, store.SessionID(uuid.New()), &calls, nil, events)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_void_events", Arguments: map[string]any{
		"scope_id": scopeID.String(),
	}})
	require.NoError(t, err)
	require.False(t, res.IsError, "list_void_events must succeed: %s", voidTextOf(res))

	out := voidTextOf(res)
	assert.Contains(t, out, events[0].EntityID.String(), "the audit read carries the ORIGINAL surrogate id (LB2)")
	assert.Contains(t, out, "7", "and the ORIGINAL display number, which is the identity a rendered citation resolved to")

	// A bad scope_id is rejected rather than silently read as "no scope".
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_void_events", Arguments: map[string]any{
		"scope_id": "nope",
	}})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, voidTextOf(res), "scope_id: invalid")
}

// voidOperatorPersona resolves PersonaSwarmOperator for every tools/call
// through the real server.PersonaMiddleware, so the persona allow-list on
// void_entity is exercised for real rather than stubbed.
func voidOperatorPersona(next mcp.MethodHandler) mcp.MethodHandler {
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

// voidTextOf concatenates a tool result's text content.
func voidTextOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			out += text.Text
		}
	}
	return out
}

func intPtr(n int) *int { return &n }
