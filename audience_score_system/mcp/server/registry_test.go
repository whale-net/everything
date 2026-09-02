package server

// Pure-Go coverage for RegisterRead/RegisterWrite (registry.go) against
// fakeRoleStore/fakeIdempotency (fakes_test.go) -- no Docker required, runs
// as part of `bazel test //...`. Complements (does not replace)
// server_integration_test.go's real-Postgres coverage of the same wiring
// end to end via a real HTTP-transport MCP client and real caller-auth
// credentials, plus the concurrent-contention and cross-instance
// statelessness scenarios that genuinely require Postgres.
//
// This task's Implementation-phase note: whoami (mcp/tools/whoami.go) is
// the only real product tool registered so far, and it takes no channel_id
// and no idempotency_key, so it alone cannot exercise the Channel-scoping
// or idempotency middleware. scopedInput/writeInput (fakes_test.go) stand
// in for a real product tool's input type here.
import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// countingReadHandler returns a ToolHandlerFor that increments counter on
// every actual invocation and reports the running total -- the signal
// these tests use to prove RegisterRead's auth/scope gate ran (or didn't)
// before the handler.
func countingReadHandler(counter *int32) mcp.ToolHandlerFor[scopedInput, countOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ scopedInput) (*mcp.CallToolResult, countOutput, error) {
		n := atomic.AddInt32(counter, 1)
		return nil, countOutput{Calls: int(n)}, nil
	}
}

func TestRegisterRead_Unauthenticated_HandlerNotInvoked(t *testing.T) {
	var calls int32
	srv, reg := newTestRegistry(nil, newFakeRoleStore(), newFakeIdempotency())
	RegisterRead(reg, &mcp.Tool{Name: "scoped_read"}, countingReadHandler(&calls))
	cs := connectClient(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "scoped_read",
		Arguments: scopedInput{ChannelID: uuid.NewString()},
	})
	require.NoError(t, err, "an unauthenticated call is a tool error, not a protocol error")
	assert.True(t, res.IsError, "unauthenticated call must be reported as a tool error")
	assert.Contains(t, textOf(res), "unauthenticated")
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls), "the handler must never run for an unauthenticated caller")
}

func TestRegisterRead_ChannelScoping_DeniesUnrelatedPersonAllowsCreatorAndAnalyst(t *testing.T) {
	channelID := uuid.New()
	creator := store.Person{ID: uuid.New()}
	analyst := store.Person{ID: uuid.New()}
	unassociated := store.Person{ID: uuid.New()}

	roles := newFakeRoleStore()
	roles.grant(channelID, creator.ID, store.RoleCreator)
	roles.grant(channelID, analyst.ID, store.RoleAnalyst)

	cases := []struct {
		name    string
		person  store.Person
		wantErr bool
	}{
		{"creator authorized", creator, false},
		{"analyst authorized", analyst, false},
		{"unassociated denied", unassociated, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			srv, reg := newTestRegistry(&tc.person, roles, newFakeIdempotency())
			RegisterRead(reg, &mcp.Tool{Name: "scoped_read"}, countingReadHandler(&calls))
			cs := connectClient(t, srv)

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "scoped_read",
				Arguments: scopedInput{ChannelID: channelID.String()},
			})
			require.NoError(t, err)

			if tc.wantErr {
				assert.True(t, res.IsError, "a Person with no live role on the Channel must get a permission error")
				assert.Contains(t, textOf(res), "permission denied")
				assert.Equal(t, int32(0), atomic.LoadInt32(&calls), "the handler must never run when Channel-scope authorization fails")
			} else {
				assert.False(t, res.IsError, "unexpected error: %s", textOf(res))
				assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "the handler must run exactly once for an authorized caller")
			}
		})
	}
}

// fakeWriteHandler is the counting fake write-tool handler this task's
// Implementation-phase note calls for: WriteMutate increments calls and
// records a durable-enough (in-memory, per-instance) mapping from the ref
// it hands back to the call count at mutate time, so WriteRender -- which
// registry.go documents as running on every call, replay included -- can
// reconstruct Out purely from ref, exactly like a real product tool would
// reconstruct it from Postgres. It carries no idempotency logic of its
// own: RegisterWrite is what's under test here.
type fakeWriteHandler struct {
	calls     int32
	resultsMu sync.Mutex
	results   map[uuid.UUID]int
}

func newFakeWriteHandler() *fakeWriteHandler {
	return &fakeWriteHandler{results: map[uuid.UUID]int{}}
}

func (h *fakeWriteHandler) mutate(_ context.Context, _ writeInput) (uuid.UUID, error) {
	n := int(atomic.AddInt32(&h.calls, 1))
	ref := uuid.New()
	h.resultsMu.Lock()
	h.results[ref] = n
	h.resultsMu.Unlock()
	return ref, nil
}

func (h *fakeWriteHandler) render(_ context.Context, ref uuid.UUID) (*mcp.CallToolResult, countOutput, error) {
	h.resultsMu.Lock()
	n := h.results[ref]
	h.resultsMu.Unlock()
	return nil, countOutput{Calls: n}, nil
}

func (h *fakeWriteHandler) callCount() int { return int(atomic.LoadInt32(&h.calls)) }

func TestRegisterWrite_Unauthenticated_HandlerNotInvoked(t *testing.T) {
	handler := newFakeWriteHandler()
	srv, reg := newTestRegistry(nil, newFakeRoleStore(), newFakeIdempotency())
	RegisterWrite(reg, &mcp.Tool{Name: "scoped_write"}, handler.mutate, handler.render)
	cs := connectClient(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "scoped_write",
		Arguments: writeInput{ChannelID: uuid.NewString(), Key: "k"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(res), "unauthenticated")
	assert.Equal(t, 0, handler.callCount(), "mutate must never run for an unauthenticated caller")
}

func TestRegisterWrite_ChannelScoping_DeniesUnrelatedPerson(t *testing.T) {
	channelID := uuid.New()
	unassociated := store.Person{ID: uuid.New()}

	handler := newFakeWriteHandler()
	srv, reg := newTestRegistry(&unassociated, newFakeRoleStore(), newFakeIdempotency())
	RegisterWrite(reg, &mcp.Tool{Name: "scoped_write"}, handler.mutate, handler.render)
	cs := connectClient(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "scoped_write",
		Arguments: writeInput{ChannelID: channelID.String(), Key: "k"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(res), "permission denied")
	assert.Equal(t, 0, handler.callCount(), "mutate must never run when Channel-scope authorization fails")
}

// TestRegisterWrite_IdempotencyKeyed_ReplayConflictAndDistinctKeySemantics
// is this task's central idempotency-middleware proof: a tool registered
// via RegisterWrite with a counting fake handler that contains NO
// idempotency logic of its own still gets exactly the semantics NFR2/LB4
// require, because RegisterWrite wraps every write tool automatically.
func TestRegisterWrite_IdempotencyKeyed_ReplayConflictAndDistinctKeySemantics(t *testing.T) {
	channelID := uuid.New()
	creator := store.Person{ID: uuid.New()}
	roles := newFakeRoleStore()
	roles.grant(channelID, creator.ID, store.RoleCreator)

	handler := newFakeWriteHandler()
	srv, reg := newTestRegistry(&creator, roles, newFakeIdempotency())
	RegisterWrite(reg, &mcp.Tool{Name: "scoped_write"}, handler.mutate, handler.render)
	cs := connectClient(t, srv)
	ctx := context.Background()

	call := func(key, value string) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "scoped_write",
			Arguments: writeInput{ChannelID: channelID.String(), Key: key, Value: value},
		})
		require.NoError(t, err)
		return res
	}

	first := call("key-1", "a")
	require.False(t, first.IsError, "unexpected error: %s", textOf(first))
	assert.Equal(t, 1, handler.callCount())

	second := call("key-1", "a")
	require.False(t, second.IsError, "unexpected error: %s", textOf(second))
	assert.Equal(t, 1, handler.callCount(), "same key + same args must not re-run mutate")
	assert.Equal(t, decodeCount(t, first), decodeCount(t, second), "a replay must return the original result")

	conflict := call("key-1", "b")
	assert.True(t, conflict.IsError, "same key + different args must conflict")
	assert.Contains(t, textOf(conflict), "idempotency key reused")
	assert.Equal(t, 1, handler.callCount(), "a conflict must not run mutate")

	third := call("key-2", "a")
	require.False(t, third.IsError, "unexpected error: %s", textOf(third))
	assert.Equal(t, 2, handler.callCount(), "a different key must run mutate again")
	assert.NotEqual(t, decodeCount(t, first), decodeCount(t, third))
}

// TestRegisterWrite_NoIdempotencyKey_RunsMutateEveryCall documents the
// Implementation section's other sanctioned mechanism: a write tool call
// carrying no idempotency_key runs mutate directly every time, relying on
// the tool's own natural-key upsert for safety rather than this
// middleware.
func TestRegisterWrite_NoIdempotencyKey_RunsMutateEveryCall(t *testing.T) {
	channelID := uuid.New()
	creator := store.Person{ID: uuid.New()}
	roles := newFakeRoleStore()
	roles.grant(channelID, creator.ID, store.RoleCreator)

	handler := newFakeWriteHandler()
	srv, reg := newTestRegistry(&creator, roles, newFakeIdempotency())
	RegisterWrite(reg, &mcp.Tool{Name: "scoped_write"}, handler.mutate, handler.render)
	cs := connectClient(t, srv)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "scoped_write",
			Arguments: writeInput{ChannelID: channelID.String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected error: %s", textOf(res))
		assert.Equal(t, i, handler.callCount(), "a call with no idempotency_key must run mutate every time")
	}
}
