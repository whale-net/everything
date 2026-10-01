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
	"encoding/json"
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

// TestConsoleQueueTools_FilterAndReason_ReachStoreTogether is the MCP
// surface's half of the same two-filter intersection the API test proves:
// a call naming a product, a container and a reason hands all three to the
// store together, so the tool layer cannot drop the reason when a
// container filter is also present (the shape a console filter plus a
// reason chip produces in one call).
func TestConsoleQueueTools_FilterAndReason_ReachStoreTogether(t *testing.T) {
	scopeID, productID, milestoneID := uuid.New(), uuid.New(), uuid.New()
	tasks := &consoleFilterTaskStore{}
	cs := connectConsoleFilterTools(t, tasks)

	callConsoleTool(t, cs, "list_escalated_tasks", "tasks", map[string]any{
		"scope_id":     scopeID.String(),
		"product_id":   productID.String(),
		"milestone_id": milestoneID.String(),
		"reason":       "manual",
	})

	assert.Equal(t, store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}, tasks.escalatedParams.ConsoleFilter)
	require.NotNil(t, tasks.escalatedParams.Reason)
	assert.Equal(t, store.EscalationReasonManual, *tasks.escalatedParams.Reason)
	assert.Equal(t, scopeID, tasks.escalatedParams.ScopeID)
}

// TestConsoleQueueTools_OptionalNarrowingIsNotRequiredInSchema is the
// structural half of "optional": each console queue read's tool schema
// marks only scope_id required. Without omitempty on the optional fields
// the generated schema listed them as required, so every list_*_tasks call
// demanded a product and a milestone -- a caller with no narrowing to
// apply could not make the call at all. A handler-level call with the
// arguments absent already exercises that (the SDK validates against the
// schema before the handler runs); this asserts the schema itself, so the
// contract is legible as a schema fact rather than only as a call that
// happens to fail.
func TestConsoleQueueTools_OptionalNarrowingIsNotRequiredInSchema(t *testing.T) {
	cs := connectConsoleFilterTools(t, &consoleFilterTaskStore{})

	schemas := map[string]*mcp.Tool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		schemas[tool.Name] = tool
	}

	for _, tool := range consoleQueueTools {
		found, ok := schemas[tool.name]
		require.True(t, ok, "%s must be registered", tool.name)
		raw, err := json.Marshal(found.InputSchema)
		require.NoError(t, err)

		var decoded struct {
			Required   []string       `json:"required"`
			Properties map[string]any `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(raw, &decoded))
		assert.Equal(t, []string{"scope_id"}, decoded.Required,
			"%s must require only scope_id: product_id, milestone_id and page_size are all optional narrowings, and a schema that demands them makes an unnarrowed read uncallable", tool.name)

		// The narrowing must still be advertised, optional or not -- a
		// caller cannot pass an argument the schema never declares.
		for _, field := range []string{"product_id", "milestone_id", "page_size", "page_token"} {
			assert.Contains(t, decoded.Properties, field, "%s must advertise %s", tool.name, field)
		}
	}
	assert.Contains(t, mustSchemaFields(t, schemas["list_escalated_tasks"]), "reason",
		"list_escalated_tasks must advertise the reason narrowing")
}

// mustSchemaFields is the property-name set of one tool's input schema, for
// the one assertion that needs the field list rather than the required list.
func mustSchemaFields(t *testing.T, tool *mcp.Tool) []string {
	t.Helper()
	require.NotNil(t, tool, "the tool must be registered")
	raw, err := json.Marshal(tool.InputSchema)
	require.NoError(t, err)
	var decoded struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	fields := make([]string, 0, len(decoded.Properties))
	for name := range decoded.Properties {
		fields = append(fields, name)
	}
	return fields
}

// consoleCountTaskStore records what each count_* tool hands the store, so
// the count tools can be pinned to the same filter set their list tools
// take -- the FR c4ab6c68 criterion that a count and the list beside it
// are asked the same question, on the MCP surface as well as the HTTP one.
type consoleCountTaskStore struct {
	store.TaskStore

	claimed   store.ListClaimedTasksParams
	escalated store.ListEscalatedTasksParams
	cancelled store.ListCancelledTasksParams
	notes     store.ListOpenNotesParams
	overview  store.ConsoleOverviewParams
	countErr  error
}

func (f *consoleCountTaskStore) CountClaimedTasks(_ context.Context, params store.ListClaimedTasksParams) (int, error) {
	f.claimed = params
	return 3, f.countErr
}

func (f *consoleCountTaskStore) CountEscalatedTasks(_ context.Context, params store.ListEscalatedTasksParams) (int, error) {
	f.escalated = params
	return 3, f.countErr
}

func (f *consoleCountTaskStore) CountCancelledTasks(_ context.Context, params store.ListCancelledTasksParams) (int, error) {
	f.cancelled = params
	return 3, f.countErr
}

func (f *consoleCountTaskStore) CountOpenNotes(_ context.Context, params store.ListOpenNotesParams) (int, error) {
	f.notes = params
	return 3, f.countErr
}

func (f *consoleCountTaskStore) CountConsoleOverview(_ context.Context, params store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	f.overview = params
	if f.countErr != nil {
		return store.ConsoleOverviewCounts{}, f.countErr
	}
	return store.ConsoleOverviewCounts{Claimed: 3}, nil
}

// connectConsoleCountTools stands up every count tool on one registry
// behind the real operator persona, exactly as mcp/main.go mounts them.
func connectConsoleCountTools(t *testing.T, tasks store.TaskStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(operatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterCountClaimedTasks(reg, tasks)
	tools.RegisterCountEscalatedTasks(reg, tasks)
	tools.RegisterCountCancelledTasks(reg, tasks)
	tools.RegisterCountOpenNotes(reg, tasks)
	tools.RegisterConsoleOverviewCounts(reg, tasks)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// The five count tools that take the four-queue narrowing set.
var consoleCountQueueTools = []string{
	"count_claimed_tasks",
	"count_cancelled_tasks",
	"count_open_notes",
}

// TestConsoleCountTools_ProductMilestoneFilterPassThrough is FR c4ab6c68
// on the MCP surface: each count tool accepts the same product_id and
// milestone_id narrowing its list tool takes and hands it to the store
// unchanged. A count tool that took scope_id alone would answer a
// scope-wide figure for a list the operator is reading narrowed, which is
// exactly the disagreement the FR forbids.
func TestConsoleCountTools_ProductMilestoneFilterPassThrough(t *testing.T) {
	scopeID, productID, milestoneID := uuid.New(), uuid.New(), uuid.New()
	fake := &consoleCountTaskStore{}
	cs := connectConsoleCountTools(t, fake)

	args := map[string]any{
		"scope_id":     scopeID.String(),
		"product_id":   productID.String(),
		"milestone_id": milestoneID.String(),
	}
	for _, tool := range consoleCountQueueTools {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected tool error: %s", discoveryTextOf(res))
	}

	want := store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}
	assert.Equal(t, scopeID, fake.claimed.ScopeID)
	assert.Equal(t, want, fake.claimed.ConsoleFilter)
	assert.Equal(t, want, fake.cancelled.ConsoleFilter)
	assert.Equal(t, want, fake.notes.ConsoleFilter)
}

// TestConsoleCountTools_EscalatedReasonPassThrough is the escalated
// count's own second filter: count_escalated_tasks takes the reason
// narrowing list_escalated_tasks takes, and an absent one leaves the read
// unnarrowed -- so the two are asked the same question either way.
func TestConsoleCountTools_EscalatedReasonPassThrough(t *testing.T) {
	fake := &consoleCountTaskStore{}
	cs := connectConsoleCountTools(t, fake)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "count_escalated_tasks",
		Arguments: map[string]any{"scope_id": uuid.New().String(), "reason": "manual"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error: %s", discoveryTextOf(res))
	require.NotNil(t, fake.escalated.Reason)
	assert.Equal(t, store.EscalationReasonManual, *fake.escalated.Reason)

	_, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "count_escalated_tasks",
		Arguments: map[string]any{"scope_id": uuid.New().String()},
	})
	require.NoError(t, err)
	assert.Nil(t, fake.escalated.Reason, "an absent reason must keep every reason, exactly as the list read does")
}

// TestConsoleCountTools_NoFilterArgs_LeaveReadUnnarrowed proves the
// optional narrowings really are optional: a caller naming only scope_id
// gets the same unnarrowed read the list tool would return.
func TestConsoleCountTools_NoFilterArgs_LeaveReadUnnarrowed(t *testing.T) {
	fake := &consoleCountTaskStore{}
	cs := connectConsoleCountTools(t, fake)

	for _, tool := range append(append([]string{}, consoleCountQueueTools...), "count_escalated_tasks") {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: tool, Arguments: map[string]any{"scope_id": uuid.New().String()},
		})
		require.NoError(t, err)
		require.False(t, res.IsError, "unexpected tool error: %s", discoveryTextOf(res))
	}
	assert.True(t, fake.claimed.ConsoleFilter.IsZero())
	assert.True(t, fake.escalated.ConsoleFilter.IsZero())
	assert.True(t, fake.cancelled.ConsoleFilter.IsZero())
	assert.True(t, fake.notes.ConsoleFilter.IsZero())
}

// TestConsoleCountTools_MalformedFilterArg_IsToolError proves a narrowing
// that is present but not a UUID is a tool error rather than a silently
// unnarrowed count -- the same rule the list tools hold to, and the reason
// the count tools share their list's parser.
func TestConsoleCountTools_MalformedFilterArg_IsToolError(t *testing.T) {
	fake := &consoleCountTaskStore{}
	cs := connectConsoleCountTools(t, fake)

	for _, tool := range consoleCountQueueTools {
		for _, key := range []string{"product_id", "milestone_id"} {
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: tool,
				Arguments: map[string]any{
					"scope_id": uuid.New().String(),
					key:        "not-a-uuid",
				},
			})
			require.NoError(t, err)
			assert.True(t, res.IsError, "%s with a malformed %s must be a tool error, not a widened count", tool, key)
		}
	}
	assert.True(t, fake.claimed.ConsoleFilter.IsZero(), "a refused call must not reach the store with a half-applied filter")
}

// TestConsoleCountTools_FailedCountIsAToolError is FR c4ab6c68's
// fail-don't-degrade rule reaching the MCP surface: a store error is
// surfaced as a tool error, never as a count of zero, which a console
// would render as an empty queue.
func TestConsoleCountTools_FailedCountIsAToolError(t *testing.T) {
	cs := connectConsoleCountTools(t, &consoleCountTaskStore{countErr: store.ErrNotFound})

	for _, tool := range append(append([]string{}, consoleCountQueueTools...), "count_escalated_tasks", "console_overview_counts") {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: tool, Arguments: map[string]any{"scope_id": uuid.New().String()},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError, "%s: a failed count must be a tool error, never a zero", tool)
	}
}

// TestConsoleOverviewCounts_AppliesTheFilterToEveryQueue proves the
// Overview tool narrows all four of its queues at once, the way its HTTP
// twin applies one query string to all four -- an Overview filtered to
// one product must not show the other product's queue sizes.
func TestConsoleOverviewCounts_AppliesTheFilterToEveryQueue(t *testing.T) {
	scopeID, productID, milestoneID := uuid.New(), uuid.New(), uuid.New()
	fake := &consoleCountTaskStore{}
	cs := connectConsoleCountTools(t, fake)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "console_overview_counts",
		Arguments: map[string]any{
			"scope_id":     scopeID.String(),
			"product_id":   productID.String(),
			"milestone_id": milestoneID.String(),
			"reason":       "attempt-cap",
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error: %s", discoveryTextOf(res))

	want := store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}
	assert.Equal(t, scopeID, fake.overview.Escalated.ScopeID)
	assert.Equal(t, scopeID, fake.overview.Claimed.ScopeID)
	assert.Equal(t, scopeID, fake.overview.Cancelled.ScopeID)
	assert.Equal(t, scopeID, fake.overview.Notes.ScopeID)
	assert.Equal(t, want, fake.overview.Escalated.ConsoleFilter)
	assert.Equal(t, want, fake.overview.Claimed.ConsoleFilter)
	assert.Equal(t, want, fake.overview.Cancelled.ConsoleFilter)
	assert.Equal(t, want, fake.overview.Notes.ConsoleFilter)
	require.NotNil(t, fake.overview.Escalated.Reason)
	assert.Equal(t, store.EscalationReasonAttemptCap, *fake.overview.Escalated.Reason)
}

// TestConsoleCountTools_OptionalNarrowingIsNotRequiredInSchema is the
// structural half of "optional" for the count tools, mirroring the same
// assertion for the list tools: without omitempty on the narrowing fields
// the generated schema would demand them, making an unnarrowed count
// uncallable -- and the FR's rule that an absent narrowing must leave the
// read exactly as it was.
func TestConsoleCountTools_OptionalNarrowingIsNotRequiredInSchema(t *testing.T) {
	cs := connectConsoleCountTools(t, &consoleCountTaskStore{})

	schemas := map[string]*mcp.Tool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		schemas[tool.Name] = tool
	}

	for _, tool := range append(append([]string{}, consoleCountQueueTools...), "count_escalated_tasks") {
		found, ok := schemas[tool]
		require.True(t, ok, "%s must be registered", tool)
		raw, err := json.Marshal(found.InputSchema)
		require.NoError(t, err)
		var decoded struct {
			Required   []string       `json:"required"`
			Properties map[string]any `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(raw, &decoded))
		assert.Equal(t, []string{"scope_id"}, decoded.Required,
			"%s must require only scope_id", tool)
		for _, field := range []string{"product_id", "milestone_id", "page_size", "page_token"} {
			assert.Contains(t, decoded.Properties, field, "%s must advertise %s", tool, field)
		}
	}
	assert.Contains(t, mustSchemaFields(t, schemas["count_escalated_tasks"]), "reason",
		"count_escalated_tasks must advertise the reason narrowing")
	assert.Contains(t, mustSchemaFields(t, schemas["console_overview_counts"]), "product_id",
		"console_overview_counts must advertise the narrowing it applies to all four queues")
}
