// No-database unit test for the console queue reads' narrowing and row
// identity on the MCP surface (FR a6cd917f): list_claimed_tasks and
// list_escalated_tasks take the optional product_id/milestone_id (and
// list_escalated_tasks the reason) narrowing and hand it to the store as a
// ConsoleFilter, an absent argument leaves the read unnarrowed, and the
// rows carry claim_id / escalation_id under exactly the field names the
// HTTP surface uses -- the same ToClaimedTaskWire/ToEscalatedTaskWire
// conversion renders both, so one field name here and one there is the
// whole difference between the two surfaces agreeing and not.
//
// The store is a fake that records the params it was handed and returns
// one fixed row; no Docker and no Postgres (store.New(nil) never dials,
// see this package's other registration tests' own reasoning). The
// real-Postgres proof that the predicates themselves narrow correctly
// lives in krill/store/task_console_integration_test.go.
package tools_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// consoleFilterTaskStore records the last call to each console queue read
// and returns a fixed row for the two the row-identity assertions need.
type consoleFilterTaskStore struct {
	store.TaskStore

	claimedParams   store.ListClaimedTasksParams
	escalatedParams store.ListEscalatedTasksParams
	cancelledParams store.ListCancelledTasksParams
	notesParams     store.ListOpenNotesParams

	claimID      uuid.UUID
	escalationID uuid.UUID
	listErr      error
}

func (f *consoleFilterTaskStore) ListClaimedTasks(_ context.Context, params store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	f.claimedParams = params
	if f.listErr != nil {
		return store.Page[store.ClaimedTaskRow]{}, f.listErr
	}
	return store.Page[store.ClaimedTaskRow]{Items: []store.ClaimedTaskRow{
		{TaskID: uuid.New(), Title: "claimed task", ClaimID: f.claimID},
	}}, nil
}

func (f *consoleFilterTaskStore) ListEscalatedTasks(_ context.Context, params store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	f.escalatedParams = params
	if f.listErr != nil {
		return store.Page[store.EscalatedTaskRow]{}, f.listErr
	}
	return store.Page[store.EscalatedTaskRow]{Items: []store.EscalatedTaskRow{
		{TaskID: uuid.New(), Title: "stuck task", EscalationID: f.escalationID, Reason: store.EscalationReasonManual},
	}}, nil
}

func (f *consoleFilterTaskStore) ListCancelledTasks(_ context.Context, params store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	f.cancelledParams = params
	if f.listErr != nil {
		return store.Page[store.CancelledTaskRow]{}, f.listErr
	}
	return store.Page[store.CancelledTaskRow]{Items: []store.CancelledTaskRow{
		{TaskID: uuid.New(), Title: "cancelled task"},
	}}, nil
}

func (f *consoleFilterTaskStore) ListOpenNotes(_ context.Context, params store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	f.notesParams = params
	if f.listErr != nil {
		return store.Page[store.OpenNoteRow]{}, f.listErr
	}
	return store.Page[store.OpenNoteRow]{Items: []store.OpenNoteRow{
		{NoteID: uuid.New(), Kind: store.NoteKindScopeNote, Body: "an open note"},
	}}, nil
}

// connectConsoleFilterTools stands up all four console queue reads on one
// registry behind the real operator persona, so a tools/call resolves as
// PersonaSwarmOperator exactly as it does in mcp/main.go.
func connectConsoleFilterTools(t *testing.T, tasks store.TaskStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(operatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterListClaimedTasks(reg, tasks)
	tools.RegisterListEscalatedTasks(reg, tasks)
	tools.RegisterListCancelledTasks(reg, tasks)
	tools.RegisterListOpenNotes(reg, tasks)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callConsoleTool invokes one console read and returns its single row as
// a JSON object. list_open_notes names its array "notes" where the three
// task reads name theirs "tasks", so the key is the caller's to pass.
func callConsoleTool(t *testing.T, cs *mcp.ClientSession, name, rowsKey string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error: %s", discoveryTextOf(res))

	structured, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	rows, ok := structured[rowsKey].([]any)
	require.True(t, ok, "%s must return a %s array: %v", name, rowsKey, structured)
	require.Len(t, rows, 1)
	row, ok := rows[0].(map[string]any)
	require.True(t, ok)
	return row
}

// The four console queue reads, each with the key its response names its
// rows under.
var consoleQueueTools = []struct{ name, rowsKey string }{
	{"list_claimed_tasks", "tasks"},
	{"list_escalated_tasks", "tasks"},
	{"list_cancelled_tasks", "tasks"},
	{"list_open_notes", "notes"},
}

// TestConsoleQueueTools_ProductMilestoneFilterPassThrough proves all four
// console queue reads accept the optional product_id/milestone_id
// narrowing and hand it to the store unchanged.
func TestConsoleQueueTools_ProductMilestoneFilterPassThrough(t *testing.T) {
	scopeID, productID, milestoneID := uuid.New(), uuid.New(), uuid.New()
	tasks := &consoleFilterTaskStore{}
	cs := connectConsoleFilterTools(t, tasks)

	args := map[string]any{
		"scope_id":     scopeID.String(),
		"product_id":   productID.String(),
		"milestone_id": milestoneID.String(),
	}
	for _, tool := range consoleQueueTools {
		callConsoleTool(t, cs, tool.name, tool.rowsKey, args)
	}

	want := store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}
	assert.Equal(t, want, tasks.claimedParams.ConsoleFilter)
	assert.Equal(t, want, tasks.escalatedParams.ConsoleFilter)
	assert.Equal(t, want, tasks.cancelledParams.ConsoleFilter)
	assert.Equal(t, want, tasks.notesParams.ConsoleFilter)
	assert.Equal(t, scopeID, tasks.claimedParams.ScopeID, "the narrowing narrows the scope it is given, never replaces it")
}

// TestConsoleQueueTools_ReasonPassThrough proves list_escalated_tasks takes
// the reason narrowing and that an absent one leaves the read unnarrowed.
func TestConsoleQueueTools_ReasonPassThrough(t *testing.T) {
	tasks := &consoleFilterTaskStore{}
	cs := connectConsoleFilterTools(t, tasks)

	callConsoleTool(t, cs, "list_escalated_tasks", "tasks", map[string]any{
		"scope_id": uuid.New().String(),
		"reason":   "attempt-cap",
	})
	require.NotNil(t, tasks.escalatedParams.Reason)
	assert.Equal(t, store.EscalationReasonAttemptCap, *tasks.escalatedParams.Reason)

	callConsoleTool(t, cs, "list_escalated_tasks", "tasks", map[string]any{"scope_id": uuid.New().String()})
	assert.Nil(t, tasks.escalatedParams.Reason, "an absent reason must keep every reason, exactly as before the filter existed")
}

// TestConsoleQueueTools_NoFilterArgs_LeaveReadUnnarrowed proves a caller
// that passes neither narrowing gets exactly today's scope-wide read.
func TestConsoleQueueTools_NoFilterArgs_LeaveReadUnnarrowed(t *testing.T) {
	tasks := &consoleFilterTaskStore{}
	cs := connectConsoleFilterTools(t, tasks)

	for _, tool := range consoleQueueTools {
		callConsoleTool(t, cs, tool.name, tool.rowsKey, map[string]any{"scope_id": uuid.New().String()})
	}
	assert.True(t, tasks.claimedParams.ConsoleFilter.IsZero())
	assert.True(t, tasks.escalatedParams.ConsoleFilter.IsZero())
	assert.True(t, tasks.cancelledParams.ConsoleFilter.IsZero())
	assert.True(t, tasks.notesParams.ConsoleFilter.IsZero())
}

// TestConsoleQueueTools_CarriesRowIdentityIDs is FR a6cd917f's row-identity
// half on the MCP surface: a claimed row carries claim_id and no
// escalation_id, an escalated row carries escalation_id and no claim_id --
// under exactly the field names the HTTP surface uses, which is what makes
// the two surfaces' DTOs interchangeable for an optimistic guard.
func TestConsoleQueueTools_CarriesRowIdentityIDs(t *testing.T) {
	tasks := &consoleFilterTaskStore{claimID: uuid.New(), escalationID: uuid.New()}
	cs := connectConsoleFilterTools(t, tasks)

	claimed := callConsoleTool(t, cs, "list_claimed_tasks", "tasks", map[string]any{"scope_id": uuid.New().String()})
	assert.Equal(t, tasks.claimID.String(), claimed["claim_id"])
	assert.NotContains(t, claimed, "escalation_id", "a claimed row carries no escalation id")

	escalated := callConsoleTool(t, cs, "list_escalated_tasks", "tasks", map[string]any{"scope_id": uuid.New().String()})
	assert.Equal(t, tasks.escalationID.String(), escalated["escalation_id"])
	assert.NotContains(t, escalated, "claim_id", "an escalated row carries no claim id")
}

// TestConsoleQueueTools_MalformedFilterArg_IsToolError proves a narrowing
// that is present but not a UUID is a tool error, never a silently
// unnarrowed read.
func TestConsoleQueueTools_MalformedFilterArg_IsToolError(t *testing.T) {
	tasks := &consoleFilterTaskStore{}
	cs := connectConsoleFilterTools(t, tasks)

	for _, args := range []map[string]any{
		{"scope_id": uuid.New().String(), "product_id": "not-a-uuid"},
		{"scope_id": uuid.New().String(), "milestone_id": "not-a-uuid"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_claimed_tasks", Arguments: args})
		require.NoError(t, err)
		assert.True(t, res.IsError, "a malformed narrowing must be a tool error, not a widened read")
	}
	assert.True(t, tasks.claimedParams.ConsoleFilter.IsZero(), "a refused call must not reach the store with a half-applied filter")
}

// TestConsoleQueueTools_StoreRefusal_IsToolError proves the store's own
// refusals (a narrowing outside the caller's scope, a wrong-filter
// continuation token) surface as tool errors rather than as an empty page.
func TestConsoleQueueTools_StoreRefusal_IsToolError(t *testing.T) {
	for name, refusal := range map[string]error{
		"filter_outside_scope": store.ErrNotFound,
		"wrong_filter_token":   store.ErrTokenFilterMismatch,
	} {
		t.Run(name, func(t *testing.T) {
			cs := connectConsoleFilterTools(t, &consoleFilterTaskStore{listErr: refusal})
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "list_claimed_tasks",
				Arguments: map[string]any{"scope_id": uuid.New().String()},
			})
			require.NoError(t, err)
			assert.True(t, res.IsError, "a store refusal must be a tool error, never an empty page")
		})
	}
}
