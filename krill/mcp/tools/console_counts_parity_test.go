// No-database unit test for the parts of the console count MCP surface
// (console_counts.go and task_product_list.go's count_product_tasks, FR
// c4ab6c68-3a20-4824-b29c-48922a209fd1) that
// console_filter_registration_test.go does not already pin: that each count
// tool returns its HTTP twin's exact document (LB7), that paging changes
// nothing observable, and the whole of count_product_tasks. The narrowing
// and error-mapping rules the five ops count tools share with their list
// tools are covered there; repeating them here would only be a second place
// to update.
//
// In-memory store fakes over a real in-memory MCP client/server connection,
// mirroring task_product_list_test.go's technique, so no Docker and part of
// `bazel test //...`. The real COUNT(*) queries and their agreement with the
// lists are covered by //krill/store:task_console_integration_test,
// //krill/store:task_note_console_integration_test and
// //krill/store:task_integration_test; the shared-SQL guard is
// //krill/store:console_count_clause_test.
package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
	libauth "github.com/whale-net/everything/libs/go/auth"
)

// consoleParityTaskStore returns one figure per count read, so a parity
// proof over two empty documents would not satisfy anything.
type consoleParityTaskStore struct {
	store.TaskStore

	claimed   int
	cancelled int
	escalated int
	notes     int
	product   int
	overview  store.ConsoleOverviewCounts
	err       error

	gotClaimed   store.ListClaimedTasksParams
	gotCancelled store.ListCancelledTasksParams
	gotEscalated store.ListEscalatedTasksParams
	gotNotes     store.ListOpenNotesParams
	gotOverview  store.ConsoleOverviewParams
}

// pagedCount models a count that wrongly honoured the page it was handed:
// a store that returned min(pageSize, total) rather than the whole filtered
// set. A tool that passed its page_size through would then report a
// one-row figure, which is exactly the mistake the FR forbids -- so the
// fake has to be capable of making that mistake visible.
func pagedCount(total int, page store.PageParams) int {
	if page.PageSize > 0 && page.PageSize < total {
		return page.PageSize
	}
	return total
}

func (f *consoleParityTaskStore) CountClaimedTasks(_ context.Context, p store.ListClaimedTasksParams) (int, error) {
	f.gotClaimed = p
	return pagedCount(f.claimed, p.Page), f.err
}

func (f *consoleParityTaskStore) CountCancelledTasks(_ context.Context, p store.ListCancelledTasksParams) (int, error) {
	f.gotCancelled = p
	return pagedCount(f.cancelled, p.Page), f.err
}

func (f *consoleParityTaskStore) CountEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (int, error) {
	f.gotEscalated = p
	return pagedCount(f.escalated, p.Page), f.err
}

func (f *consoleParityTaskStore) CountOpenNotes(_ context.Context, p store.ListOpenNotesParams) (int, error) {
	f.gotNotes = p
	return pagedCount(f.notes, p.Page), f.err
}

func (f *consoleParityTaskStore) CountConsoleOverview(_ context.Context, p store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	f.gotOverview = p
	if f.err != nil {
		return store.ConsoleOverviewCounts{}, f.err
	}
	overview := f.overview
	overview.Claimed = pagedCount(overview.Claimed, p.Claimed.Page)
	overview.Cancelled = pagedCount(overview.Cancelled, p.Cancelled.Page)
	overview.Escalated = pagedCount(overview.Escalated, p.Escalated.Page)
	overview.OpenNotes = pagedCount(overview.OpenNotes, p.Notes.Page)
	return overview, nil
}

func (f *consoleParityTaskStore) CountProductTasks(_ context.Context, p store.ListProductTasksParams) (int, error) {
	return pagedCount(f.product, p.Page), f.err
}

// consoleParityProductStore resolves the target Product's own scope_id, the
// way count_product_tasks' HTTP twin does.
type consoleParityProductStore struct {
	store.ProductStore

	id      uuid.UUID
	scopeID uuid.UUID
}

func (f *consoleParityProductStore) GetCurrentByID(_ context.Context, id uuid.UUID) (store.Product, error) {
	if id != f.id {
		return store.Product{}, store.ErrNotFound
	}
	return store.Product{ID: id, ScopeID: f.scopeID, Name: "krill"}, nil
}

// consoleParityPersona runs every tools/call through the real
// server.PersonaMiddleware as an mcpauth-verified operator, so the ops
// count tools resolve as PersonaSwarmOperator exactly as they do in
// mcp/main.go. Mirrors task_product_list_test.go's own helper --
// duplicated rather than imported, since that file is a separate go_test
// target whose unexported helpers this package cannot reach.
func consoleParityPersona(next mcp.MethodHandler) mcp.MethodHandler {
	gated := server.PersonaMiddleware()(next)
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if !ok {
			return next(ctx, method, req)
		}
		if call.Extra == nil {
			call.Extra = &mcp.RequestExtra{}
		}
		call.Extra.TokenInfo = &auth.TokenInfo{UserID: "operator-1", Extra: map[string]any{libauth.TokenInfoPersonaKey: "swarm_operator"}}
		return gated(ctx, method, req)
	}
}

// consoleParityTextOf concatenates a tool result's text blocks, so a
// rejection's message is assertable.
func consoleParityTextOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			out += text.Text
		}
	}
	return out
}

// callConsoleParityTool invokes one count tool; a rejected call is a tool
// error, not a protocol error.
func callConsoleParityTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// consoleParityFigures is the fixture both surfaces are driven with: a
// distinct figure per queue, so a tool serving another queue's number (or a
// page's length) fails rather than passing.
func consoleParityFigures() *consoleParityTaskStore {
	return &consoleParityTaskStore{claimed: 3, cancelled: 4, escalated: 5, notes: 6}
}

// consoleParityOverview is the Overview fixture: all seven figures distinct,
// so a sub-line served its queue's total would show.
func consoleParityOverview() store.ConsoleOverviewCounts {
	return store.ConsoleOverviewCounts{
		Escalated: 1, Claimed: 2, Cancelled: 3, OpenNotes: 4,
		EscalatedRecently: 5, ClaimsExpiringSoon: 6, OpenScopeNotes: 7,
	}
}

// consoleParityQueueTools pairs each ops count tool with the HTTP endpoint
// it mirrors and the figure the fixture gives it.
var consoleParityQueueTools = []struct {
	tool    string
	path    string
	handler func(store.TaskStore) http.HandlerFunc
	figure  func(*consoleParityTaskStore) int
	gotPage func(*consoleParityTaskStore) store.PageParams
}{
	{"count_claimed_tasks", "/console/claimed/count", handlers.CountClaimedTasksHandler,
		func(f *consoleParityTaskStore) int { return f.claimed },
		func(f *consoleParityTaskStore) store.PageParams { return f.gotClaimed.Page }},
	{"count_cancelled_tasks", "/console/cancelled/count", handlers.CountCancelledTasksHandler,
		func(f *consoleParityTaskStore) int { return f.cancelled },
		func(f *consoleParityTaskStore) store.PageParams { return f.gotCancelled.Page }},
	{"count_escalated_tasks", "/console/escalated/count", handlers.CountEscalatedTasksHandler,
		func(f *consoleParityTaskStore) int { return f.escalated },
		func(f *consoleParityTaskStore) store.PageParams { return f.gotEscalated.Page }},
	{"count_open_notes", "/console/notes/count", handlers.CountOpenNotesHandler,
		func(f *consoleParityTaskStore) int { return f.notes },
		func(f *consoleParityTaskStore) store.PageParams { return f.gotNotes.Page }},
}

// connectConsoleParityTools stands up the four queue count tools and the
// Overview on one registry, exactly as mcp/main.go's ops mount does.
func connectConsoleParityTools(t *testing.T, tasks store.TaskStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(consoleParityPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterCountClaimedTasks(reg, tasks)
	tools.RegisterCountCancelledTasks(reg, tasks)
	tools.RegisterCountEscalatedTasks(reg, tasks)
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

// connectConsoleParityProductTasks stands up count_product_tasks alone,
// the work mount's single count tool.
func connectConsoleParityProductTasks(t *testing.T, tasks store.TaskStore, products store.ProductStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(consoleParityPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterCountProductTasks(reg, tasks, products)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// ── LB7: each tool returns its HTTP twin's document ────────────────────────

// TestConsoleParityTools_HTTPTwinByteParity is LB7 on this surface: given
// the same figures, each count tool returns the identical JSON document
// its HTTP twin returns -- same keys, same values -- rather than an
// MCP-local mirror of the same data that could drift. Both documents are
// decoded into a generic tree and compared, since JSON key order is not
// part of the wire contract.
func TestConsoleParityTools_HTTPTwinByteParity(t *testing.T) {
	scopeID := uuid.New()

	for _, tc := range consoleParityQueueTools {
		t.Run(tc.tool, func(t *testing.T) {
			httpRec := serveConsoleParityOverHTTP(t, tc.path, tc.handler, consoleParityFigures(), "scope_id="+scopeID.String())

			cs := connectConsoleParityTools(t, consoleParityFigures())
			res := callConsoleParityTool(t, cs, tc.tool, map[string]any{"scope_id": scopeID.String()})
			require.False(t, res.IsError, consoleParityTextOf(res))

			assertSameJSONDocument(t, httpRec.Body.Bytes(), res.StructuredContent)

			var fromMCP handlers.ConsoleCountWire
			require.NoError(t, json.Unmarshal(marshalStructured(t, res.StructuredContent), &fromMCP))
			assert.Equal(t, tc.figure(consoleParityFigures()), fromMCP.Count,
				"the tool must carry its own queue's figure -- parity over two empty documents would prove nothing")
		})
	}

	t.Run("console_overview_counts", func(t *testing.T) {
		httpFigures := consoleParityFigures()
		httpFigures.overview = consoleParityOverview()
		httpRec := serveConsoleParityOverHTTP(t, "/console/overview",
			func(t store.TaskStore) http.HandlerFunc { return handlers.ConsoleOverviewHandler(t) },
			httpFigures, "scope_id="+scopeID.String())

		mcpFigures := consoleParityFigures()
		mcpFigures.overview = consoleParityOverview()
		cs := connectConsoleParityTools(t, mcpFigures)
		res := callConsoleParityTool(t, cs, "console_overview_counts", map[string]any{"scope_id": scopeID.String()})
		require.False(t, res.IsError, consoleParityTextOf(res))

		assertSameJSONDocument(t, httpRec.Body.Bytes(), res.StructuredContent)

		var fromMCP handlers.ConsoleOverviewWire
		require.NoError(t, json.Unmarshal(marshalStructured(t, res.StructuredContent), &fromMCP))
		assert.Equal(t, handlers.ToConsoleOverviewWire(consoleParityOverview()), fromMCP,
			"all seven figures reach the wire, each its own -- a sub-line served its queue's total would show here")
	})

	t.Run("count_product_tasks", func(t *testing.T) {
		productID, scopeID := uuid.New(), uuid.New()
		products := &consoleParityProductStore{id: productID, scopeID: scopeID}

		httpMux := http.NewServeMux()
		httpMux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(
			&consoleParityTaskStore{product: 12}, products))
		httpRec := httptest.NewRecorder()
		httpMux.ServeHTTP(httpRec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/tasks/count", nil))
		require.Equal(t, http.StatusOK, httpRec.Code, httpRec.Body.String())

		cs := connectConsoleParityProductTasks(t, &consoleParityTaskStore{product: 12}, products)
		res := callConsoleParityTool(t, cs, "count_product_tasks", map[string]any{"product_id": productID.String()})
		require.False(t, res.IsError, consoleParityTextOf(res))

		assertSameJSONDocument(t, httpRec.Body.Bytes(), res.StructuredContent)
	})
}

// TestConsoleParityTools_OutputSchemaIsTheHTTPTwinsWireType is the
// structural half of LB7, and the half document equality alone cannot
// reach: each count tool's declared output schema -- what a caller reads
// off tools/list -- must be exactly the shape of its HTTP twin's exported
// wire type, field for field and type for type. A tool answering with an
// MCP-local mirror serializes to the same JSON today, so document parity
// cannot see it; but a mirror that adds, drops, renames or retypes a field
// declares a shape the twin does not have, and this turns red on the day
// someone edits one side. A mirror that is field-for-field identical is
// indistinguishable from the real type at runtime -- that limit is why the
// tools are written to return handlers.ConsoleCountWire rather than a
// local struct, not something a test can assert.
func TestConsoleParityTools_OutputSchemaIsTheHTTPTwinsWireType(t *testing.T) {
	cs := connectConsoleParityTools(t, consoleParityFigures())

	schemas := map[string]any{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		raw, err := json.Marshal(tool.OutputSchema)
		require.NoError(t, err)
		var decoded any
		require.NoError(t, json.Unmarshal(raw, &decoded))
		schemas[tool.Name] = decoded
	}

	for _, twin := range []struct {
		tool string
		wire any
	}{
		{"count_claimed_tasks", handlers.ConsoleCountWire{}},
		{"count_cancelled_tasks", handlers.ConsoleCountWire{}},
		{"count_escalated_tasks", handlers.ConsoleCountWire{}},
		{"count_open_notes", handlers.ConsoleCountWire{}},
		{"console_overview_counts", handlers.ConsoleOverviewWire{}},
	} {
		t.Run(twin.tool, func(t *testing.T) {
			// The twin's own document keys, read off the exported type, are
			// what the declared schema's property set must be.
			raw, err := json.Marshal(twin.wire)
			require.NoError(t, err)
			var doc map[string]any
			require.NoError(t, json.Unmarshal(raw, &doc))

			declared, ok := schemas[twin.tool].(map[string]any)
			require.True(t, ok, "%s must declare an output schema", twin.tool)
			props, ok := declared["properties"].(map[string]any)
			require.True(t, ok, "%s's output schema must be an object: %v", twin.tool, declared)

			assert.Len(t, props, len(doc),
				"%s must advertise exactly its HTTP twin's fields -- an extra or missing one is an MCP-local shape drifting from the twin", twin.tool)
			for field, want := range doc {
				got, present := props[field]
				assert.True(t, present,
					"%s must advertise %s -- it is a field of its HTTP twin's wire type, so the twin's shape is the contract", twin.tool, field)
				assert.Equal(t, jsonschemaTypeOf(want), jsonschemaTypeOf(got),
					"%s's %s must have the JSON type its HTTP twin's wire type gives it", twin.tool, field)
			}

			// Every field is also required, since none of the wire type's
			// fields carries omitempty: a console reads a count that must be
			// there, and must not have to distinguish "absent" from "zero".
			required, ok := declared["required"].([]any)
			require.True(t, ok, "%s must declare its required fields: %v", twin.tool, declared)
			assert.Len(t, required, len(doc),
				"%s must require every field its HTTP twin always emits -- an omitempty on a count would render as a missing number", twin.tool)
		})
	}
}

// serveConsoleParityOverHTTP drives one count endpoint through a real
// ServeMux at its own route, as routes.go mounts it.
func serveConsoleParityOverHTTP(t *testing.T, path string, handler func(store.TaskStore) http.HandlerFunc, tasks store.TaskStore, query string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path, handler(tasks))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?"+query, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec
}

func assertSameJSONDocument(t *testing.T, httpBody []byte, structured any) {
	t.Helper()
	mcpRaw := marshalStructured(t, structured)

	var httpDoc, mcpDoc any
	require.NoError(t, json.Unmarshal(httpBody, &httpDoc))
	require.NoError(t, json.Unmarshal(mcpRaw, &mcpDoc))
	assert.Equal(t, httpDoc, mcpDoc, "the MCP tool must return its HTTP twin's exact document (LB7)")
}

// jsonschemaTypeOf is the JSON Schema type name a value carries: a decoded
// document's number is an integer figure, a declared property is the
// {"type": "..."} object naming it. Comparing the two is what makes a
// retyped mirror field visible -- a count that became a string on one
// surface only.
func jsonschemaTypeOf(v any) string {
	switch value := v.(type) {
	case map[string]any:
		if declared, ok := value["type"].(string); ok {
			return declared
		}
	}
	switch v.(type) {
	case float64:
		return "integer"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func marshalStructured(t *testing.T, structured any) []byte {
	t.Helper()
	raw, err := json.Marshal(structured)
	require.NoError(t, err)
	return raw
}

// ── a count is of the whole set, never a page ───────────────────────────────

// TestConsoleParityTools_IgnorePaging proves the paging arguments change
// nothing a caller can observe: with page_size=1 the figure is the same
// whole-set total. A count tool that honoured page_size would report a
// queue's first page as its size, which is the one number the FR exists to
// stop a console printing.
func TestConsoleParityTools_IgnorePaging(t *testing.T) {
	scopeID := uuid.New().String()
	for _, tc := range consoleParityQueueTools {
		t.Run(tc.tool, func(t *testing.T) {
			fake := consoleParityFigures()
			cs := connectConsoleParityTools(t, fake)

			res := callConsoleParityTool(t, cs, tc.tool, map[string]any{
				"scope_id": scopeID, "page_size": 1, "page_token": "some-token",
			})

			require.False(t, res.IsError, consoleParityTextOf(res))
			assert.JSONEq(t, fmt.Sprintf(`{"count": %d}`, tc.figure(consoleParityFigures())),
				string(marshalStructured(t, res.StructuredContent)),
				"a count is of the whole filtered set; the page a caller requested never narrows it")
			assert.Equal(t, store.PageParams{}, tc.gotPage(fake),
				"the page must not even reach the store, rather than being passed down and relied on to be ignored")
		})
	}

	t.Run("console_overview_counts", func(t *testing.T) {
		figures := consoleParityFigures()
		figures.overview = consoleParityOverview()
		cs := connectConsoleParityTools(t, figures)

		// The Overview takes no paging arguments -- its input schema is the
		// four queues' narrowing only -- so the rule it must hold is the
		// stronger one: with no page to send, every figure it hands the
		// store is already unpaged.
		res := callConsoleParityTool(t, cs, "console_overview_counts", map[string]any{"scope_id": scopeID})

		require.False(t, res.IsError, consoleParityTextOf(res))
		assert.JSONEq(t, `{"escalated":1,"claimed":2,"cancelled":3,"open_notes":4,"escalated_recently":5,"claims_expiring_soon":6,"open_scope_notes":7}`,
			string(marshalStructured(t, res.StructuredContent)))
		for name, page := range map[string]store.PageParams{
			"escalated": figures.gotOverview.Escalated.Page,
			"claimed":   figures.gotOverview.Claimed.Page,
			"cancelled": figures.gotOverview.Cancelled.Page,
			"notes":     figures.gotOverview.Notes.Page,
		} {
			assert.Equal(t, store.PageParams{}, page, "the Overview's %s figure must reach the store with no page", name)
		}
	})
}

// TestCountProductTasksParity_IgnoresPaging is the same rule for the "Y"
// behind "Showing X of Y tasks", whose tool accepts page_size/page_token
// for parity with list_product_tasks and ignores both.
func TestCountProductTasksParity_IgnoresPaging(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	recorder := &consoleParityProductTaskStore{consoleParityTaskStore: consoleParityTaskStore{product: 12}}
	cs := connectConsoleParityProductTasks(t, recorder,
		&consoleParityProductStore{id: productID, scopeID: scopeID})

	res := callConsoleParityTool(t, cs, "count_product_tasks", map[string]any{
		"product_id": productID.String(), "page_size": 1, "page_token": "abc",
	})

	require.False(t, res.IsError, consoleParityTextOf(res))
	assert.JSONEq(t, `{"count": 12}`, string(marshalStructured(t, res.StructuredContent)))
	assert.Equal(t, store.PageParams{}, recorder.gotParams.Page,
		"the page must not even reach the store, rather than being passed down and relied on to be ignored")
}

// ── count_product_tasks, end to end ─────────────────────────────────────────

// consoleParityProductTaskStore records the params count_product_tasks
// hands the store, so the filters it takes are assertable.
type consoleParityProductTaskStore struct {
	consoleParityTaskStore
	gotParams store.ListProductTasksParams
}

func (f *consoleParityProductTaskStore) CountProductTasks(_ context.Context, p store.ListProductTasksParams) (int, error) {
	f.gotParams = p
	return f.product, f.err
}

// TestCountProductTasksParity_RegistersExactlyOneTool is the structural
// proof for the work mount's count: the tool exists under exactly this name
// and nothing else comes with it.
func TestCountProductTasksParity_RegistersExactlyOneTool(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	cs := connectConsoleParityProductTasks(t, &consoleParityProductTaskStore{},
		&consoleParityProductStore{id: productID, scopeID: scopeID})

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}

	require.Len(t, registered, 1, "must register exactly one tool -- nothing more, nothing fewer")
	assert.True(t, registered["count_product_tasks"], "count_product_tasks must be registered")
}

// TestCountProductTasksParity_TakesItsListsFilters proves the work mount's
// count is asked the same question as list_product_tasks: the same
// container scope, lane and only-stuck narrowing, through the same input
// parser, with the product's own scope_id resolved from its row (LB2). A
// total printed above a filtered list that described different rows is the
// disagreement the FR forbids.
func TestCountProductTasksParity_TakesItsListsFilters(t *testing.T) {
	productID, scopeID, containerID := uuid.New(), uuid.New(), uuid.New()
	lane := store.LaneTesting

	for name, tc := range map[string]struct {
		args map[string]any
		want store.ListProductTasksParams
	}{
		"default": {
			args: map[string]any{"product_id": productID.String()},
			want: store.ListProductTasksParams{ScopeID: scopeID, ProductID: productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}},
		},
		"milestone": {
			args: map[string]any{"product_id": productID.String(), "scope": "milestone", "container_id": containerID.String()},
			want: store.ListProductTasksParams{ScopeID: scopeID, ProductID: productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: containerID}},
		},
		"milepebble": {
			args: map[string]any{"product_id": productID.String(), "scope": "milepebble", "container_id": containerID.String()},
			want: store.ListProductTasksParams{ScopeID: scopeID, ProductID: productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: containerID}},
		},
		"lane": {
			args: map[string]any{"product_id": productID.String(), "lane": "Testing"},
			want: store.ListProductTasksParams{ScopeID: scopeID, ProductID: productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, Lane: &lane},
		},
		"only_stuck": {
			args: map[string]any{"product_id": productID.String(), "only_stuck": true},
			want: store.ListProductTasksParams{ScopeID: scopeID, ProductID: productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, OnlyStuck: true},
		},
		"lane_and_only_stuck": {
			args: map[string]any{"product_id": productID.String(), "lane": "Testing", "only_stuck": true},
			want: store.ListProductTasksParams{ScopeID: scopeID, ProductID: productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, Lane: &lane, OnlyStuck: true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			tasks := &consoleParityProductTaskStore{}
			cs := connectConsoleParityProductTasks(t, tasks, &consoleParityProductStore{id: productID, scopeID: scopeID})

			res := callConsoleParityTool(t, cs, "count_product_tasks", tc.args)

			require.False(t, res.IsError, consoleParityTextOf(res))
			assert.Equal(t, tc.want, tasks.gotParams)
		})
	}
}

// TestCountProductTasksParity_ContainerIDIgnoredForIncompleteScope proves
// container_id on the product-wide scope is inert rather than silently
// narrowing the total -- the total would then describe fewer rows than the
// list it is printed above.
func TestCountProductTasksParity_ContainerIDIgnoredForIncompleteScope(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &consoleParityProductTaskStore{}
	cs := connectConsoleParityProductTasks(t, tasks, &consoleParityProductStore{id: productID, scopeID: scopeID})

	res := callConsoleParityTool(t, cs, "count_product_tasks", map[string]any{
		"product_id":   productID.String(),
		"scope":        "incomplete",
		"container_id": uuid.New().String(),
	})

	require.False(t, res.IsError, consoleParityTextOf(res))
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotParams.Scope)
	assert.Equal(t, uuid.Nil, tasks.gotParams.Scope.ContainerID)
}

// TestCountProductTasksParity_BadArgumentsAreRejected proves every
// malformed argument is a tool error naming the fixable rule, and never
// reaches the store -- a total of zero for a request the caller got wrong
// would read as "this product has no tasks".
func TestCountProductTasksParity_BadArgumentsAreRejected(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()

	for name, tc := range map[string]struct {
		args    map[string]any
		wantMsg string
	}{
		"missing product_id":             {args: map[string]any{}, wantMsg: `required: missing properties: ["product_id"]`},
		"non-UUID product_id":            {args: map[string]any{"product_id": "nope"}, wantMsg: "product_id: invalid or missing UUID"},
		"unknown scope":                  {args: map[string]any{"product_id": productID.String(), "scope": "everything"}, wantMsg: "scope: must be one of incomplete, milestone, milepebble"},
		"milestone without a container":  {args: map[string]any{"product_id": productID.String(), "scope": "milestone"}, wantMsg: `container_id: required for scope "milestone"; invalid or missing UUID`},
		"milepebble without a container": {args: map[string]any{"product_id": productID.String(), "scope": "milepebble"}, wantMsg: `container_id: required for scope "milepebble"; invalid or missing UUID`},
		"non-UUID container_id":          {args: map[string]any{"product_id": productID.String(), "scope": "milestone", "container_id": "nope"}, wantMsg: `container_id: required for scope "milestone"; invalid or missing UUID`},
		"unknown lane":                   {args: map[string]any{"product_id": productID.String(), "lane": "Nope"}, wantMsg: "lane: must be one of"},
	} {
		t.Run(name, func(t *testing.T) {
			tasks := &consoleParityProductTaskStore{}
			cs := connectConsoleParityProductTasks(t, tasks, &consoleParityProductStore{id: productID, scopeID: scopeID})

			res := callConsoleParityTool(t, cs, "count_product_tasks", tc.args)

			assert.True(t, res.IsError, "a malformed argument must be a tool error, never a total of zero")
			assert.Contains(t, consoleParityTextOf(res), tc.wantMsg)
			assert.Equal(t, store.ListProductTasksParams{}, tasks.gotParams, "a rejected call must never reach the store")
		})
	}
}

// TestCountProductTasksParity_FailuresAreToolErrors proves FR c4ab6c68's
// fail-don't-degrade rule and LB1's refusal both reach an MCP caller
// intact: a store failure, a container outside the product, an unknown
// product and a product-lookup failure are each a distinct tool error, none
// of them a successful total of zero. A krill-work agent handed
// {"count": 0} would conclude the product has no work.
func TestCountProductTasksParity_FailuresAreToolErrors(t *testing.T) {
	productID, scopeID, containerID := uuid.New(), uuid.New(), uuid.New()

	for name, tc := range map[string]struct {
		err      error
		args     map[string]any
		products store.ProductStore
		wantMsg  string
	}{
		"store failure": {
			err:      errors.New("connection reset by peer"),
			args:     map[string]any{"product_id": productID.String()},
			products: &consoleParityProductStore{id: productID, scopeID: scopeID},
			wantMsg:  "connection reset by peer",
		},
		"container outside the product": {
			err:      store.ErrMilestoneOutsideProduct,
			args:     map[string]any{"product_id": productID.String(), "scope": "milestone", "container_id": containerID.String()},
			products: &consoleParityProductStore{id: productID, scopeID: scopeID},
			wantMsg:  store.ErrMilestoneOutsideProduct.Error(),
		},
		"unknown product": {
			args:     map[string]any{"product_id": uuid.New().String()},
			products: &consoleParityProductStore{id: productID, scopeID: scopeID},
			wantMsg:  store.ErrNotFound.Error(),
		},
		"product lookup failure": {
			args:     map[string]any{"product_id": productID.String()},
			products: failingConsoleParityProductStore{},
			wantMsg:  "product row unavailable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			tasks := &consoleParityProductTaskStore{}
			tasks.err = tc.err
			cs := connectConsoleParityProductTasks(t, tasks, tc.products)

			res := callConsoleParityTool(t, cs, "count_product_tasks", tc.args)

			assert.True(t, res.IsError, "a failed or refused total must be a tool error, never a count of zero")
			assert.Contains(t, consoleParityTextOf(res), tc.wantMsg)
		})
	}
}

// failingConsoleParityProductStore is a product store whose lookup itself
// fails -- distinct from "the product does not exist", and a test that both
// stay distinguishable at this surface.
type failingConsoleParityProductStore struct {
	store.ProductStore
}

func (failingConsoleParityProductStore) GetCurrentByID(context.Context, uuid.UUID) (store.Product, error) {
	return store.Product{}, errors.New("product row unavailable")
}

// TestConsoleParityTools_StoreFailuresAreToolErrors proves the fail-don't-
// degrade rule for the five ops count tools, and that each store refusal
// stays distinguishable rather than being flattened: an agent must be able
// to tell "you named something that does not exist" from "the read broke".
func TestConsoleParityTools_StoreFailuresAreToolErrors(t *testing.T) {
	scopeID := uuid.New().String()

	for name, storeErr := range map[string]error{
		"a filter outside the scope":   store.ErrNotFound,
		"an unknown escalation reason": store.ErrUnknownEscalationReason,
		"a wrong-filter token":         store.ErrTokenFilterMismatch,
		"a plain store failure":        errors.New("connection reset by peer"),
	} {
		t.Run(name, func(t *testing.T) {
			cs := connectConsoleParityTools(t, &consoleParityTaskStore{err: storeErr})

			for _, tool := range consoleParityToolNames {
				t.Run(tool, func(t *testing.T) {
					res := callConsoleParityTool(t, cs, tool, map[string]any{"scope_id": scopeID})

					assert.True(t, res.IsError,
						"%s: a failed or refused count must be a tool error, never a count of zero", tool)
					assert.Contains(t, consoleParityTextOf(res), storeErr.Error())
				})
			}
		})
	}
}

// consoleParityToolNames is every ops count tool, the Overview included.
var consoleParityToolNames = []string{
	"count_claimed_tasks", "count_cancelled_tasks", "count_escalated_tasks",
	"count_open_notes", "console_overview_counts",
}
