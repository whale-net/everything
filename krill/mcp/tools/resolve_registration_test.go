// No-database unit test for resolve.go: RegisterResolveAll registers
// resolve_non_goal and list_non_goal_promotions, resolve_non_goal forwards
// both outcomes with the session's scope and LB4 subject pair, and it
// rejects a missing/unknown session, a malformed id, and any outcome
// outside the two-value set before the store is reached. Stores are
// in-memory fakes driven over a real in-memory MCP connection behind the
// real server.PersonaMiddleware, like void_registration_test.go.
package tools_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// resolveTestTime is a fixed timestamp, so an assertion on a rendered
// value is about the conversion rather than about when the test ran.
var resolveTestTime = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// resolveSessionStore overrides only GetSession; known is the one valid id.
type resolveSessionStore struct {
	store.SessionStore
	known store.SessionID
}

func (f resolveSessionStore) GetSession(_ context.Context, id store.SessionID) (store.Session, error) {
	if id != f.known {
		return store.Session{}, store.ErrSessionNotFound
	}
	return store.Session{ID: id, ScopeID: uuid.New()}, nil
}

// resolveCall records one ResolveStore invocation, including everything
// the tool must pass through from the resolved session. Every field is
// asserted by TestResolveNonGoal_ForwardsBothOutcomesWithTheSessionScope
// -- a recorded-but-unread field is how a scope leak or a dropped reason
// would survive a green suite.
type resolveCall struct {
	method     string
	scopeID    uuid.UUID
	id         uuid.UUID
	outcome    store.ResolveOutcome
	reason     *string
	acting     store.Subject
	onBehalfOf store.Subject
	productID  *uuid.UUID
}

type recordingResolveStore struct {
	calls   *[]resolveCall
	refuser error
	// promotions is what ListNonGoalPromotions returns, so the audit read
	// can be checked against real data rather than an empty list.
	promotions []store.NonGoalPromotion
}

func (f recordingResolveStore) ResolveNonGoal(_ context.Context, scopeID, id uuid.UUID, outcome store.ResolveOutcome, reason *string, acting, onBehalfOf store.Subject) (store.NonGoal, error) {
	*f.calls = append(*f.calls, resolveCall{
		method: "ResolveNonGoal", scopeID: scopeID, id: id, outcome: outcome,
		reason: reason, acting: acting, onBehalfOf: onBehalfOf,
	})
	if f.refuser != nil {
		return store.NonGoal{}, f.refuser
	}
	if outcome == store.ResolveRetire {
		return store.NonGoal{}, nil
	}
	body := "carried across the re-kind"
	return store.NonGoal{
		ID: id, ScopeID: scopeID, ProductID: uuid.New(),
		Kind: store.NonGoalKindPermanent, Name: "A deferred Non-Goal", Body: &body,
		RevisionID: uuid.New(), ValidFrom: resolveTestTime,
	}, nil
}

func (f recordingResolveStore) ListNonGoalPromotions(_ context.Context, scopeID uuid.UUID, productID *uuid.UUID) ([]store.NonGoalPromotion, error) {
	*f.calls = append(*f.calls, resolveCall{method: "ListNonGoalPromotions", scopeID: scopeID, productID: productID})
	return f.promotions, nil
}

func connectResolveTools(t *testing.T, sessionID store.SessionID, calls *[]resolveCall, refuser error, promotions []store.NonGoalPromotion) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(resolveOperatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterResolveAll(reg, resolveSessionStore{known: sessionID}, recordingResolveStore{calls: calls, refuser: refuser, promotions: promotions})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestRegisterResolveAll_RegistersTheVerbAndItsAuditRead(t *testing.T) {
	var calls []resolveCall
	cs := connectResolveTools(t, store.SessionID(uuid.New()), &calls, nil, nil)

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}
	assert.True(t, registered["resolve_non_goal"], "resolve_non_goal must be registered -- without it a Requirement Contributor cannot settle a deferred Non-Goal, and the FR is unreachable")
	assert.True(t, registered["list_non_goal_promotions"], "the promote-side audit read (FR 19123858) must be registered alongside the verb")
	assert.Len(t, registered, 2, "resolve is ONE verb over a two-value outcome, not one tool per outcome")
}

// TestResolveNonGoal_ForwardsBothOutcomesWithTheSessionScope runs BOTH
// outcomes. They reach the store through one call, so a test covering only
// one would leave the other untested, and the two differ in what the
// caller gets back as well as in what the store does.
func TestResolveNonGoal_ForwardsBothOutcomesWithTheSessionScope(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	for _, outcome := range []store.ResolveOutcome{store.ResolvePromote, store.ResolveRetire} {
		t.Run(string(outcome), func(t *testing.T) {
			var calls []resolveCall
			cs := connectResolveTools(t, sessionID, &calls, nil, nil)
			id := uuid.NewString()
			reason := "settled at signoff"

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "resolve_non_goal",
				Arguments: map[string]any{
					"krill_session_id": uuid.UUID(sessionID).String(),
					"non_goal_id":      id,
					"outcome":          string(outcome),
					"reason":           reason,
				},
			})
			require.NoError(t, err)
			require.False(t, res.IsError, "resolve_non_goal must succeed: %s", resolveTextOf(res))

			require.Len(t, calls, 1, "exactly one store call")
			assert.Equal(t, "ResolveNonGoal", calls[0].method)
			assert.Equal(t, outcome, calls[0].outcome, "the outcome must reach the store untranslated")
			assert.Equal(t, id, calls[0].id.String())
			require.NotNil(t, calls[0].reason, "a supplied reason must be forwarded, not dropped")
			assert.Equal(t, reason, *calls[0].reason, "the reason is recorded in the resolution's history for the audit reads")
			assert.NotEqual(t, uuid.Nil, calls[0].scopeID, "scope_id must come from the resolved session, never from the arguments")
			assert.Equal(t, store.Subject{}, calls[0].acting,
				"this fake session carries no subjects of its own; what matters is that they come from the session, not the arguments")
			assert.Equal(t, calls[0].acting, calls[0].onBehalfOf)
		})
	}
}

// TestResolveNonGoal_PromoteReturnsTheRekindedRow pins the promote
// response: the caller can see the kind the row now carries without a
// second read, and the id is unchanged (LB2).
func TestResolveNonGoal_PromoteReturnsTheRekindedRow(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	var calls []resolveCall
	cs := connectResolveTools(t, sessionID, &calls, nil, nil)
	id := uuid.NewString()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "resolve_non_goal",
		Arguments: map[string]any{"krill_session_id": uuid.UUID(sessionID).String(), "non_goal_id": id, "outcome": "promote"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "resolve_non_goal must succeed: %s", resolveTextOf(res))

	out := resolveTextOf(res)
	assert.Contains(t, out, id, "the successor keeps the SAME surrogate id (LB2)")
	assert.Contains(t, out, string(store.NonGoalKindPermanent), "the caller can see the re-kind without a second read")
	assert.Contains(t, out, "carried across the re-kind", "and the body survives it -- FR d0021a0f requires the body be preserved")
}

// TestResolveNonGoal_RetireReturnsTheIDAlone pins the retire response. A
// retire leaves no current row, so returning a zero-valued NonGoal would
// serialise a row that does not exist.
func TestResolveNonGoal_RetireReturnsTheIDAlone(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	var calls []resolveCall
	cs := connectResolveTools(t, sessionID, &calls, nil, nil)
	id := uuid.NewString()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "resolve_non_goal",
		Arguments: map[string]any{"krill_session_id": uuid.UUID(sessionID).String(), "non_goal_id": id, "outcome": "retire"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "resolve_non_goal must succeed: %s", resolveTextOf(res))

	out := resolveTextOf(res)
	assert.Contains(t, out, id)
	assert.NotContains(t, out, string(store.NonGoalKindPermanent),
		"a retire must not answer with a row -- there is no current Non-Goal left to describe")
}

// TestResolveNonGoal_RejectsBadInputBeforeTheStore covers the two-value
// set at the tool boundary. Guessing between "the row survives" and "the
// row is tombstoned" is the one thing a resolution must never do, so an
// unrecognised outcome is refused rather than defaulted.
func TestResolveNonGoal_RejectsBadInputBeforeTheStore(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	sid := uuid.UUID(sessionID).String()
	for label, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"missing session": {map[string]any{"non_goal_id": uuid.NewString(), "outcome": "promote"}, "krill_session_id"},
		"bad session":     {map[string]any{"krill_session_id": "not-a-uuid", "non_goal_id": uuid.NewString(), "outcome": "promote"}, "krill_session_id: invalid"},
		"unknown session": {map[string]any{"krill_session_id": uuid.NewString(), "non_goal_id": uuid.NewString(), "outcome": "promote"}, "unknown krill session"},
		"bad id":          {map[string]any{"krill_session_id": sid, "non_goal_id": "nope", "outcome": "promote"}, "non_goal_id: invalid"},
		// A missing required field is caught by the tool's own JSON schema
		// before the handler runs, so the message is the schema's rather
		// than the tool's -- the call is still refused, which is the point.
		"missing id":      {map[string]any{"krill_session_id": sid, "outcome": "promote"}, "missing properties: [\"non_goal_id\"]"},
		"missing outcome": {map[string]any{"krill_session_id": sid, "non_goal_id": uuid.NewString()}, "outcome"},
		"unknown outcome": {map[string]any{"krill_session_id": sid, "non_goal_id": uuid.NewString(), "outcome": "delete"}, "outcome: must be one of"},
		// Case-sensitivity is load-bearing: the store accepts exactly two
		// values, and a tool that lower-cased first would accept an
		// outcome the store would then have to guess about.
		"wrong case": {map[string]any{"krill_session_id": sid, "non_goal_id": uuid.NewString(), "outcome": "PROMOTE"}, "outcome: must be one of"},
		// amend's and void's verbs are the two a caller is most likely to
		// reach for, and both are wrong: amend cannot re-kind, and a void
		// is a correction rather than a resolution.
		"amend": {map[string]any{"krill_session_id": sid, "non_goal_id": uuid.NewString(), "outcome": "amend"}, "outcome: must be one of"},
		"void":  {map[string]any{"krill_session_id": sid, "non_goal_id": uuid.NewString(), "outcome": "void"}, "outcome: must be one of"},
	} {
		t.Run(label, func(t *testing.T) {
			var calls []resolveCall
			cs := connectResolveTools(t, sessionID, &calls, nil, nil)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "resolve_non_goal", Arguments: tc.args})
			require.NoError(t, err)
			assert.True(t, res.IsError, "expected a tool error")
			assert.Contains(t, resolveTextOf(res), tc.want)
			assert.Empty(t, calls, "a rejected call must never reach the store -- a half-run resolution is unrecoverable")
		})
	}
}

// TestResolveNonGoal_SurfacesItsRefusal -- the tool must pass
// ErrNotDeferred through unchanged rather than flattening it, because the
// message names the kind it found and that is what tells a caller whether
// the row was already settled or never deferred.
func TestResolveNonGoal_SurfacesItsRefusal(t *testing.T) {
	sessionID := store.SessionID(uuid.New())
	for _, outcome := range []store.ResolveOutcome{store.ResolvePromote, store.ResolveRetire} {
		t.Run(string(outcome), func(t *testing.T) {
			var calls []resolveCall
			cs := connectResolveTools(t, sessionID, &calls, store.ErrNotDeferred, nil)

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "resolve_non_goal",
				Arguments: map[string]any{
					"krill_session_id": uuid.UUID(sessionID).String(),
					"non_goal_id":      uuid.NewString(),
					"outcome":          string(outcome),
				},
			})
			require.NoError(t, err)
			assert.True(t, res.IsError, "expected a tool error")
			assert.Contains(t, resolveTextOf(res), store.ErrNotDeferred.Error(),
				"the refusal must reach the caller with its own message -- it names the kind the row actually has")
		})
	}
}

// TestListNonGoalPromotions_ReadsTheAuditRegister covers the promote-side
// history read. It takes scope_id explicitly like list_products, and an
// omitted product_id must mean every product rather than none.
func TestListNonGoalPromotions_ReadsTheAuditRegister(t *testing.T) {
	reason := "settled at signoff"
	scopeID := uuid.New()
	promotions := []store.NonGoalPromotion{{
		ID:                  uuid.New(),
		ScopeID:             scopeID,
		NonGoalID:           uuid.New(),
		ProductID:           uuid.New(),
		FromKind:            store.NonGoalKindDeferred,
		ToKind:              store.NonGoalKindPermanent,
		Reason:              &reason,
		CreatedByActing:     store.Subject{Iss: "whale_net", Sub: "alex", Kind: store.SubjectKindHuman},
		CreatedByOnBehalfOf: store.Subject{Iss: "whale_net", Sub: "alex", Kind: store.SubjectKindHuman},
		CreatedAt:           resolveTestTime,
	}}

	t.Run("unfiltered", func(t *testing.T) {
		var calls []resolveCall
		cs := connectResolveTools(t, store.SessionID(uuid.New()), &calls, nil, promotions)

		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_non_goal_promotions", Arguments: map[string]any{
			"scope_id": scopeID.String(),
		}})
		require.NoError(t, err)
		require.False(t, res.IsError, "list_non_goal_promotions must succeed: %s", resolveTextOf(res))

		out := resolveTextOf(res)
		assert.Contains(t, out, promotions[0].NonGoalID.String(), "the audit read carries the ORIGINAL surrogate id (LB2)")
		assert.Contains(t, out, string(store.NonGoalKindDeferred), "and which way the row moved -- the SCD2 row alone says only that kind changed")
		assert.Contains(t, out, string(store.NonGoalKindPermanent))
		assert.Contains(t, out, "alex", "FR 19123858 requires the actor in the history")
		assert.Contains(t, out, "whale_net")

		require.Len(t, calls, 1)
		assert.Equal(t, scopeID, calls[0].scopeID)
		assert.Nil(t, calls[0].productID, "an omitted product_id must reach the store as nil, meaning every product -- not a zero UUID, which would match nothing")
	})

	t.Run("filtered by product", func(t *testing.T) {
		var calls []resolveCall
		cs := connectResolveTools(t, store.SessionID(uuid.New()), &calls, nil, promotions)
		productID := uuid.New()

		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_non_goal_promotions", Arguments: map[string]any{
			"scope_id":   scopeID.String(),
			"product_id": productID.String(),
		}})
		require.NoError(t, err)
		require.False(t, res.IsError, "list_non_goal_promotions must succeed: %s", resolveTextOf(res))

		require.Len(t, calls, 1)
		require.NotNil(t, calls[0].productID, "a supplied product_id must actually reach the store as a filter")
		assert.Equal(t, productID, *calls[0].productID)
	})

	t.Run("bad scope id", func(t *testing.T) {
		var calls []resolveCall
		cs := connectResolveTools(t, store.SessionID(uuid.New()), &calls, nil, nil)
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_non_goal_promotions", Arguments: map[string]any{
			"scope_id": "nope",
		}})
		require.NoError(t, err)
		assert.True(t, res.IsError)
		assert.Contains(t, resolveTextOf(res), "scope_id: invalid")
		assert.Empty(t, calls, "a malformed scope must be refused, not read as some other scope's register")
	})
}

// resolveOperatorPersona resolves PersonaSwarmOperator for every
// tools/call through the real server.PersonaMiddleware, so the persona
// allow-list on resolve_non_goal is exercised for real rather than stubbed.
func resolveOperatorPersona(next mcp.MethodHandler) mcp.MethodHandler {
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

// resolveTextOf concatenates a tool result's text content.
func resolveTextOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			out += text.Text
		}
	}
	return out
}
