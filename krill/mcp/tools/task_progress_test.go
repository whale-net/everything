// No-database unit test for get_product_task_progress (task_progress.go,
// FR 59f664ff-3aa9-4d90-a861-d7758ecee040): the MCP surface's input
// validation, its param plumbing into store.ProductTaskProgressParams, and
// -- the point of LB7 -- that its response is byte-for-byte the same JSON
// document its HTTP twin GET /products/{id}/task-progress returns for the
// same aggregate, not an MCP-local mirror of it. Driven over a real
// in-memory MCP client/server connection (mcp.NewInMemoryTransports) with
// in-memory store fakes, mirroring task_product_list_test.go's technique;
// the real server's server.PersonaMiddleware gates every call and
// productTaskPersona stands in for the mcpauth bearer check.
//
// The handler's own validation and error mappings live in
// krill/api/handlers/task_progress_test.go; the real aggregate query and
// its incomplete-container predicate are covered by
// krill/store/task_integration_test.go against Postgres.
package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// progressTaskStore overrides only SummarizeProductTaskProgress; every
// other TaskStore method is unreachable through this read and panics via
// the nil embedded interface.
type progressTaskStore struct {
	store.TaskStore

	progress store.ProductTaskProgress
	err      error

	gotParams store.ProductTaskProgressParams
}

func (f *progressTaskStore) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	f.gotParams = params
	return f.progress, f.err
}

// connectGetProductTaskProgress registers exactly get_product_task_progress
// over an in-memory transport and returns a connected client session.
func connectGetProductTaskProgress(t *testing.T, tasks store.TaskStore, products store.ProductStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(productTaskPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterGetProductTaskProgress(reg, tasks, products)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callGetProductTaskProgress(t *testing.T, cs *mcp.ClientSession, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_product_task_progress",
		Arguments: args,
	})
	require.NoError(t, err, "a rejected call is a tool error, not a protocol error")
	return res
}

// progressAggregate is the fixture both surfaces are driven with: one
// milestone row carrying a cancellation and a Done task, one milepebble
// row, and one empty container -- every branch the wire shape has.
func progressAggregate(productID uuid.UUID) store.ProductTaskProgress {
	milestoneID, milepebbleID, emptyID := uuid.New(), uuid.New(), uuid.New()
	return store.ProductTaskProgress{
		ProductID: productID,
		Containers: []store.ContainerTaskProgress{
			{
				Milestone: store.ProductTaskMilestoneRef{
					ID: milestoneID, Name: "M2", Status: store.MilestoneStatusInProgress,
				},
				PerLane:   store.TaskLaneCounts{Scaffold: 1, Testing: 1, Done: 2},
				Cancelled: 1,
			},
			{
				Milestone: store.ProductTaskMilestoneRef{
					ID: milestoneID, Name: "M2", Status: store.MilestoneStatusInProgress,
				},
				Milepebble: &store.ProductTaskMilepebbleRef{
					ID: milepebbleID, Name: "MP1", Status: store.MilestoneStatusInDesign,
				},
				PerLane: store.TaskLaneCounts{Implementation: 1},
			},
			{
				Milestone: store.ProductTaskMilestoneRef{
					ID: emptyID, Name: "M3", Status: store.MilestoneStatusNotStarted,
				},
			},
		},
	}
}

// TestRegisterGetProductTaskProgress_RegistersExactlyOneTool is the file's
// structural proof: the work surface gains a read tool and nothing else.
func TestRegisterGetProductTaskProgress_RegistersExactlyOneTool(t *testing.T) {
	cs := connectGetProductTaskProgress(t, &progressTaskStore{}, &productTaskProductStore{id: uuid.New()})

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}

	require.Len(t, registered, 1, "must register exactly one tool -- nothing more, nothing fewer")
	assert.True(t, registered["get_product_task_progress"], "get_product_task_progress must be registered")
}

// TestGetProductTaskProgress_DefaultArgs pins the default call: no scope
// means every incomplete container of the product, and the read is scoped
// by the product's own scope_id resolved from the product row (LB2)
// rather than by any caller-supplied scope.
func TestGetProductTaskProgress_DefaultArgs(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &progressTaskStore{}
	products := &productTaskProductStore{id: productID, scopeID: scopeID}
	cs := connectGetProductTaskProgress(t, tasks, products)

	res := callGetProductTaskProgress(t, cs, map[string]any{"product_id": productID.String()})

	require.False(t, res.IsError, productTaskTextOf(res))
	assert.Equal(t, productID, products.gotID, "the product is read back to resolve its own scope_id")
	assert.Equal(t, productID, tasks.gotParams.ProductID)
	assert.Equal(t, scopeID, tasks.gotParams.ScopeID)
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotParams.Scope)
}

// TestGetProductTaskProgress_AllArgsPlumb proves every recognized argument
// reaches ProductTaskProgressParams unchanged, for both single-container
// scopes.
func TestGetProductTaskProgress_AllArgsPlumb(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	containerID := uuid.New()

	for _, kind := range []store.ProductTaskScopeKind{store.ProductTaskScopeMilestone, store.ProductTaskScopeMilepebble} {
		t.Run(string(kind), func(t *testing.T) {
			tasks := &progressTaskStore{}
			cs := connectGetProductTaskProgress(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

			res := callGetProductTaskProgress(t, cs, map[string]any{
				"product_id":   productID.String(),
				"scope":        string(kind),
				"container_id": containerID.String(),
			})

			require.False(t, res.IsError, productTaskTextOf(res))
			assert.Equal(t, store.ProductTaskScope{Kind: kind, ContainerID: containerID}, tasks.gotParams.Scope)
		})
	}
}

// TestGetProductTaskProgress_ContainerIDIgnoredForIncompleteScope proves
// container_id on the product-wide scope is inert rather than silently
// narrowing the read.
func TestGetProductTaskProgress_ContainerIDIgnoredForIncompleteScope(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &progressTaskStore{}
	cs := connectGetProductTaskProgress(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

	res := callGetProductTaskProgress(t, cs, map[string]any{
		"product_id":   productID.String(),
		"scope":        "incomplete",
		"container_id": uuid.New().String(),
	})

	require.False(t, res.IsError, productTaskTextOf(res))
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotParams.Scope)
	assert.Equal(t, uuid.Nil, tasks.gotParams.Scope.ContainerID)
}

// TestGetProductTaskProgress_BadArgsAreRejected proves every malformed
// argument is a tool error naming the fixable rule, and never reaches the
// store -- an empty aggregate would read as "this product has no
// milestones".
func TestGetProductTaskProgress_BadArgsAreRejected(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()

	for _, tc := range []struct {
		name    string
		args    map[string]any
		wantMsg string
	}{
		{
			// An entirely absent product_id never reaches the handler: the
			// field is required in the tool's input schema, so the SDK
			// rejects it during argument validation. A present-but-
			// unparseable one is the handler's own refusal, below.
			name:    "missing product_id",
			args:    map[string]any{},
			wantMsg: `required: missing properties: ["product_id"]`,
		},
		{
			name:    "non-UUID product_id",
			args:    map[string]any{"product_id": "krill"},
			wantMsg: "product_id: invalid or missing UUID",
		},
		{
			name:    "unknown scope",
			args:    map[string]any{"product_id": productID.String(), "scope": "everything"},
			wantMsg: "scope: must be one of incomplete, milestone, milepebble",
		},
		{
			name:    "milestone scope without container_id",
			args:    map[string]any{"product_id": productID.String(), "scope": "milestone"},
			wantMsg: `container_id: required for scope "milestone"; invalid or missing UUID`,
		},
		{
			name:    "milepebble scope without container_id",
			args:    map[string]any{"product_id": productID.String(), "scope": "milepebble"},
			wantMsg: `container_id: required for scope "milepebble"; invalid or missing UUID`,
		},
		{
			name:    "non-UUID container_id on a single-container scope",
			args:    map[string]any{"product_id": productID.String(), "scope": "milestone", "container_id": "nope"},
			wantMsg: `container_id: required for scope "milestone"; invalid or missing UUID`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &progressTaskStore{}
			cs := connectGetProductTaskProgress(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

			res := callGetProductTaskProgress(t, cs, tc.args)

			assert.True(t, res.IsError, "a malformed argument must be a tool error")
			assert.Contains(t, productTaskTextOf(res), tc.wantMsg)
			assert.Equal(t, store.ProductTaskProgressParams{}, tasks.gotParams, "a rejected call must never reach the store")
		})
	}
}

// TestGetProductTaskProgress_UnknownProduct_IsAnError proves an unknown
// product fails rather than reporting another product's containers as
// empty.
func TestGetProductTaskProgress_UnknownProduct_IsAnError(t *testing.T) {
	tasks := &progressTaskStore{}
	cs := connectGetProductTaskProgress(t, tasks, &productTaskProductStore{id: uuid.New(), scopeID: uuid.New()})

	res := callGetProductTaskProgress(t, cs, map[string]any{"product_id": uuid.New().String()})

	assert.True(t, res.IsError)
	assert.Contains(t, productTaskTextOf(res), store.ErrNotFound.Error())
	assert.Equal(t, store.ProductTaskProgressParams{}, tasks.gotParams)
}

// TestGetProductTaskProgress_StoreRefusalsSurface proves the store's own
// refusals reach the caller intact rather than being flattened into a
// generic failure -- LB1's cross-product refusal and the aggregate's own
// unknown-product refusal both stay distinguishable.
func TestGetProductTaskProgress_StoreRefusalsSurface(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "container outside the product", err: store.ErrMilestoneOutsideProduct},
		{name: "product with no current row", err: store.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &progressTaskStore{err: tc.err}
			cs := connectGetProductTaskProgress(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

			res := callGetProductTaskProgress(t, cs, map[string]any{"product_id": productID.String()})

			assert.True(t, res.IsError)
			assert.Contains(t, productTaskTextOf(res), tc.err.Error())
		})
	}
}

// TestGetProductTaskProgress_StoreFailureIsAnError proves a genuine store
// failure surfaces as a tool error rather than an empty successful
// aggregate -- a caller must never read a failed read as "this product has
// no work".
func TestGetProductTaskProgress_StoreFailureIsAnError(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &progressTaskStore{err: errors.New("connection reset by peer")}
	cs := connectGetProductTaskProgress(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

	res := callGetProductTaskProgress(t, cs, map[string]any{"product_id": productID.String()})

	assert.True(t, res.IsError)
	assert.Contains(t, productTaskTextOf(res), "connection reset by peer")
}

// TestGetProductTaskProgress_NoContainersIsAnEmptyArray proves a product
// with no in-scope container returns `[]` rather than null, so a caller
// can index the array without a nil check.
func TestGetProductTaskProgress_NoContainersIsAnEmptyArray(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &progressTaskStore{progress: store.ProductTaskProgress{
		ProductID:  productID,
		Containers: []store.ContainerTaskProgress{},
	}}
	cs := connectGetProductTaskProgress(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

	res := callGetProductTaskProgress(t, cs, map[string]any{"product_id": productID.String()})

	require.False(t, res.IsError, productTaskTextOf(res))
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	assert.JSONEq(t, `{"product_id": "`+productID.String()+`", "containers": []}`, string(raw))
}

// TestGetProductTaskProgress_HTTPTwinByteParity is LB7 on this read: given
// the same aggregate, get_product_task_progress returns the identical JSON
// document GET /products/{id}/task-progress returns -- same keys, same
// values, same omits -- rather than an MCP-local mirror of the same data
// that could drift.
func TestGetProductTaskProgress_HTTPTwinByteParity(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	aggregate := progressAggregate(productID)

	// The HTTP twin, through a real ServeMux so {id} is populated exactly
	// as routes.go's mount populates it.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/task-progress", handlers.GetProductTaskProgressHandler(
		&progressTaskStore{progress: aggregate}, &productTaskProductStore{id: productID, scopeID: scopeID}))
	httpRec := httptest.NewRecorder()
	mux.ServeHTTP(httpRec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/task-progress", nil))
	require.Equal(t, http.StatusOK, httpRec.Code, httpRec.Body.String())

	// The MCP twin, over a real in-memory MCP session.
	cs := connectGetProductTaskProgress(t, &progressTaskStore{progress: aggregate},
		&productTaskProductStore{id: productID, scopeID: scopeID})
	res := callGetProductTaskProgress(t, cs, map[string]any{"product_id": productID.String()})
	require.False(t, res.IsError, productTaskTextOf(res))

	mcpRaw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)

	// Both documents decoded into the same generic tree and compared: JSON
	// object key order is not part of the wire contract, so this is
	// document equality rather than literal byte equality of two encoders.
	var httpDoc, mcpDoc any
	require.NoError(t, json.Unmarshal(httpRec.Body.Bytes(), &httpDoc))
	require.NoError(t, json.Unmarshal(mcpRaw, &mcpDoc))
	assert.Equal(t, httpDoc, mcpDoc, "the MCP tool must return its HTTP twin's exact document (LB7)")

	// And the document is the shared exported wire type, not a lookalike:
	// a mirror would not survive a field added to ProductTaskProgressWire.
	var fromHTTP, fromMCP handlers.ProductTaskProgressWire
	require.NoError(t, json.Unmarshal(httpRec.Body.Bytes(), &fromHTTP))
	require.NoError(t, json.Unmarshal(mcpRaw, &fromMCP))
	assert.Equal(t, fromHTTP, fromMCP)

	// Spot-check the figures themselves, so parity over two empty
	// aggregates would not satisfy this test.
	require.Len(t, fromMCP.Containers, 3)
	assert.Equal(t, productID.String(), fromMCP.ProductID)
	assert.Equal(t, 4, fromMCP.Containers[0].Total, "total is the per-lane sum")
	assert.Equal(t, 2, fromMCP.Containers[0].Done, "done IS the Done lane's count")
	assert.Equal(t, 1, fromMCP.Containers[0].Cancelled)
	assert.True(t, fromMCP.Containers[0].HasTasks)
	assert.Nil(t, fromMCP.Containers[0].Milepebble, "a milestone's own row carries no milepebble")
	assert.Equal(t, 1, fromMCP.Containers[1].Total)
	require.NotNil(t, fromMCP.Containers[1].Milepebble)
	assert.Equal(t, "MP1", fromMCP.Containers[1].Milepebble.Name)
	assert.Equal(t, string(store.MilestoneStatusInProgress), fromMCP.Containers[1].Milestone.Status)
	assert.Equal(t, 0, fromMCP.Containers[2].Total)
	assert.False(t, fromMCP.Containers[2].HasTasks, "a zero total is what a caller renders as \"No tasks yet\"")
}
