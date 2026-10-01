// No-database unit test for list_product_tasks (task_product_list.go, FR
// cfcd1104-b015-4ffd-a07a-abb7c6e435d3): the MCP surface's input validation,
// its param
// plumbing into store.ListProductTasksParams, and -- the point of LB7 --
// that its response is byte-for-byte the same JSON document its HTTP twin
// GET /products/{id}/tasks returns for the same store page, not an
// MCP-local mirror of it. Driven over a real in-memory MCP client/server
// connection (mcp.NewInMemoryTransports) with in-memory store fakes,
// mirroring bounded_list_reads_test.go's technique; the real server's
// server.PersonaMiddleware gates every call and operatorPersona stands in
// for the mcpauth bearer check.
//
// The handler's own validation and error mappings live in
// krill/api/handlers/task_product_list_test.go; the real query, its
// keyset paging and its incomplete-container predicate are covered by
// krill/store/task_integration_test.go against Postgres.
package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// productTaskTaskStore overrides only ListProductTasks; every other
// TaskStore method is unreachable through this read and panics via the nil
// embedded interface.
type productTaskTaskStore struct {
	store.TaskStore

	page store.Page[store.ProductTaskRow]
	err  error

	gotParams store.ListProductTasksParams
}

func (f *productTaskTaskStore) ListProductTasks(_ context.Context, params store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	f.gotParams = params
	return f.page, f.err
}

// productTaskProductStore overrides only GetCurrentByID -- the read
// resolves the target Product's own scope_id from the product row (LB2).
type productTaskProductStore struct {
	store.ProductStore

	id      uuid.UUID
	scopeID uuid.UUID
	gotID   uuid.UUID
}

func (f *productTaskProductStore) GetCurrentByID(_ context.Context, id uuid.UUID) (store.Product, error) {
	f.gotID = id
	if id != f.id {
		return store.Product{}, store.ErrNotFound
	}
	return store.Product{ID: id, ScopeID: f.scopeID, Name: "krill"}, nil
}

// productTaskPersona runs every tools/call through the real
// server.PersonaMiddleware as an mcpauth-verified caller, so it resolves
// PersonaSwarmOperator; other methods (initialize, tools/list) pass through.
// Mirrors discovery_registration_test.go's own operatorPersona -- duplicated
// rather than imported, since that file is a separate go_test target whose
// unexported helpers this package cannot reach.
func productTaskPersona(next mcp.MethodHandler) mcp.MethodHandler {
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

// productTaskTextOf concatenates a tool result's text blocks, so a
// rejection's message is assertable.
func productTaskTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

// connectListProductTasks registers exactly list_product_tasks over an
// in-memory transport and returns a connected client session.
func connectListProductTasks(t *testing.T, tasks store.TaskStore, products store.ProductStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(productTaskPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterListProductTasks(reg, tasks, products)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callListProductTasks(t *testing.T, cs *mcp.ClientSession, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_product_tasks",
		Arguments: args,
	})
	require.NoError(t, err, "a rejected call is a tool error, not a protocol error")
	return res
}

// productTaskPage is the fixture both surfaces are driven with, carrying
// one of every optional field the wire shape has.
func productTaskPage() store.Page[store.ProductTaskRow] {
	milestoneID, milepebbleID, claimID := uuid.New(), uuid.New(), uuid.New()
	escalationReason := store.EscalationReasonManual
	leaseExpiry := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	cancelledAt := time.Date(2026, 7, 9, 10, 11, 12, 0, time.UTC)
	return store.Page[store.ProductTaskRow]{
		Items: []store.ProductTaskRow{
			{
				TaskID:    uuid.New(),
				Title:     "an escalated, claimed, cut task",
				CreatedAt: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC),
				Milestone: store.ProductTaskMilestoneRef{
					ID: milestoneID, Name: "M3", Status: store.MilestoneStatusInProgress,
				},
				Milepebble: &store.ProductTaskMilepebbleRef{
					ID: milepebbleID, Name: "M3.1", Status: store.MilestoneStatusInDesign,
				},
				CurrentLane:      store.LaneValidation,
				State:            store.TaskStateEscalated,
				EscalationReason: &escalationReason,
				AttemptCount:     store.DefaultAttemptCap,
				AttemptCap:       store.DefaultAttemptCap,
				LeaseExpiresAt:   &leaseExpiry,
				ClaimID:          &claimID,
			},
			{
				TaskID:       uuid.New(),
				Title:        "a cancelled, unclaimed, uncut task",
				CreatedAt:    time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC),
				Milestone:    store.ProductTaskMilestoneRef{ID: milestoneID, Name: "M3", Status: store.MilestoneStatusInProgress},
				CurrentLane:  store.LaneTesting,
				State:        store.TaskStateActive,
				CancelledAt:  &cancelledAt,
				AttemptCount: 1,
				AttemptCap:   store.DefaultAttemptCap,
			},
		},
		NextToken: "cursor-2",
	}
}

// TestRegisterListProductTasks_RegistersExactlyOneTool is the file's
// structural proof: the work surface gains a read tool and nothing else.
func TestRegisterListProductTasks_RegistersExactlyOneTool(t *testing.T) {
	cs := connectListProductTasks(t, &productTaskTaskStore{}, &productTaskProductStore{id: uuid.New()})

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}

	require.Len(t, registered, 1, "must register exactly one tool -- nothing more, nothing fewer")
	assert.True(t, registered["list_product_tasks"], "list_product_tasks must be registered")
}

// TestListProductTasks_DefaultArgs pins the default call: no scope means
// every incomplete milestone of the product, and the rows are scoped by the
// product's own scope_id resolved from the product row (LB2) rather than by
// any caller-supplied scope.
func TestListProductTasks_DefaultArgs(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &productTaskTaskStore{}
	products := &productTaskProductStore{id: productID, scopeID: scopeID}
	cs := connectListProductTasks(t, tasks, products)

	res := callListProductTasks(t, cs, map[string]any{"product_id": productID.String()})

	require.False(t, res.IsError, productTaskTextOf(res))
	assert.Equal(t, productID, products.gotID, "the product is read back to resolve its own scope_id")
	assert.Equal(t, productID, tasks.gotParams.ProductID)
	assert.Equal(t, scopeID, tasks.gotParams.ScopeID, "the read is scoped by the product's own scope_id, never by a caller's session")
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotParams.Scope)
	assert.Nil(t, tasks.gotParams.Lane, "an absent lane must mean every lane, not no lanes")
	assert.False(t, tasks.gotParams.OnlyStuck)
	assert.Equal(t, store.PageParams{}, tasks.gotParams.Page)
}

// TestListProductTasks_AllArgsPlumb proves every recognized argument
// reaches ListProductTasksParams unchanged, for both single-container
// scopes.
func TestListProductTasks_AllArgsPlumb(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	containerID := uuid.New()

	for _, kind := range []store.ProductTaskScopeKind{store.ProductTaskScopeMilestone, store.ProductTaskScopeMilepebble} {
		t.Run(string(kind), func(t *testing.T) {
			tasks := &productTaskTaskStore{}
			cs := connectListProductTasks(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

			res := callListProductTasks(t, cs, map[string]any{
				"product_id":   productID.String(),
				"scope":        string(kind),
				"container_id": containerID.String(),
				"lane":         "Testing",
				"only_stuck":   true,
				"page_size":    float64(25),
				"page_token":   "cursor-1",
			})

			require.False(t, res.IsError, productTaskTextOf(res))
			assert.Equal(t, store.ProductTaskScope{Kind: kind, ContainerID: containerID}, tasks.gotParams.Scope)
			require.NotNil(t, tasks.gotParams.Lane)
			assert.Equal(t, store.LaneTesting, *tasks.gotParams.Lane)
			assert.True(t, tasks.gotParams.OnlyStuck)
			assert.Equal(t, store.PageParams{PageSize: 25, ContinuationToken: "cursor-1"}, tasks.gotParams.Page)
		})
	}
}

// TestListProductTasks_BadArgsAreRejected proves every malformed argument
// is a tool error naming the fixable rule, and never reaches the store --
// an empty page would read as "this product has no work".
func TestListProductTasks_BadArgsAreRejected(t *testing.T) {
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
			name:    "unknown lane",
			args:    map[string]any{"product_id": productID.String(), "lane": "Review"},
			wantMsg: "lane: must be one of Scaffold, Implementation, Testing, Validation, Done",
		},
		{
			name:    "non-UUID container_id on a single-container scope",
			args:    map[string]any{"product_id": productID.String(), "scope": "milestone", "container_id": "nope"},
			wantMsg: `container_id: required for scope "milestone"; invalid or missing UUID`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &productTaskTaskStore{}
			cs := connectListProductTasks(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

			res := callListProductTasks(t, cs, tc.args)

			assert.True(t, res.IsError, "a malformed argument must be a tool error")
			assert.Contains(t, productTaskTextOf(res), tc.wantMsg)
			assert.Equal(t, store.ListProductTasksParams{}, tasks.gotParams, "a rejected call must never reach the store")
		})
	}
}

// TestListProductTasks_ContainerIDIgnoredForIncompleteScope proves
// container_id on the product-wide scope is inert rather than silently
// narrowing the read -- the store's FilterSet ignores it too, so a token
// minted under one spelling still resumes under the other.
func TestListProductTasks_ContainerIDIgnoredForIncompleteScope(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &productTaskTaskStore{}
	cs := connectListProductTasks(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

	res := callListProductTasks(t, cs, map[string]any{
		"product_id":   productID.String(),
		"scope":        "incomplete",
		"container_id": uuid.New().String(),
	})

	require.False(t, res.IsError, productTaskTextOf(res))
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotParams.Scope)
	assert.Equal(t, uuid.Nil, tasks.gotParams.Scope.ContainerID)
}

// TestListProductTasks_UnknownProduct_IsAnError proves an unknown product
// fails rather than listing another product's tasks.
func TestListProductTasks_UnknownProduct_IsAnError(t *testing.T) {
	tasks := &productTaskTaskStore{}
	cs := connectListProductTasks(t, tasks, &productTaskProductStore{id: uuid.New(), scopeID: uuid.New()})

	res := callListProductTasks(t, cs, map[string]any{"product_id": uuid.New().String()})

	assert.True(t, res.IsError)
	assert.Contains(t, productTaskTextOf(res), store.ErrNotFound.Error())
	assert.Equal(t, store.ListProductTasksParams{}, tasks.gotParams)
}

// TestListProductTasks_StoreRefusalsSurface proves the store's own
// refusals reach the caller intact rather than being flattened into a
// generic failure: LB1's cross-product refusal and FR3's wrong-filter
// continuation-token refusal both stay distinguishable.
func TestListProductTasks_StoreRefusalsSurface(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "container outside the product", err: store.ErrMilestoneOutsideProduct},
		{name: "token issued under a different filter set", err: store.ErrTokenFilterMismatch},
		{name: "token issued in another scope", err: store.ErrTokenScopeMismatch},
		{name: "malformed token", err: store.ErrInvalidContinuationToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &productTaskTaskStore{err: tc.err}
			cs := connectListProductTasks(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

			res := callListProductTasks(t, cs, map[string]any{"product_id": productID.String()})

			assert.True(t, res.IsError)
			assert.Contains(t, productTaskTextOf(res), tc.err.Error())
		})
	}
}

// TestListProductTasks_HTTPTwinByteParity is LB7 on this read: given the
// same store page, list_product_tasks returns the identical JSON document
// GET /products/{id}/tasks returns -- same keys, same values, same omits
// -- rather than an MCP-local mirror of the same data that could drift.
func TestListProductTasks_HTTPTwinByteParity(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	page := productTaskPage()

	// The HTTP twin, through a real ServeMux so {id} is populated exactly
	// as routes.go's mount populates it.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/tasks", handlers.ListProductTasksHandler(
		&productTaskTaskStore{page: page}, &productTaskProductStore{id: productID, scopeID: scopeID}))
	httpRec := httptest.NewRecorder()
	mux.ServeHTTP(httpRec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/tasks", nil))
	require.Equal(t, http.StatusOK, httpRec.Code, httpRec.Body.String())

	// The MCP twin, over a real in-memory MCP session.
	tasks := &productTaskTaskStore{page: page}
	cs := connectListProductTasks(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})
	res := callListProductTasks(t, cs, map[string]any{"product_id": productID.String()})
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
	// a mirror would not survive a field added to ProductTaskWire.
	var fromHTTP, fromMCP handlers.ListProductTasksResponse
	require.NoError(t, json.Unmarshal(httpRec.Body.Bytes(), &fromHTTP))
	require.NoError(t, json.Unmarshal(mcpRaw, &fromMCP))
	assert.Equal(t, fromHTTP, fromMCP)
	require.Len(t, fromMCP.Tasks, 2)
	assert.Equal(t, "cursor-2", fromMCP.NextToken)
	assert.Equal(t, page.Items[0].TaskID.String(), fromMCP.Tasks[0].TaskID)
	assert.Equal(t, page.Items[0].Milestone.ID.String(), fromMCP.Tasks[0].Milestone.ID)
	require.NotNil(t, fromMCP.Tasks[0].Milepebble)
	assert.Equal(t, page.Items[0].Milepebble.Name, fromMCP.Tasks[0].Milepebble.Name)
	require.NotNil(t, fromMCP.Tasks[0].EscalationReason)
	assert.Equal(t, string(store.EscalationReasonManual), *fromMCP.Tasks[0].EscalationReason)
	require.NotNil(t, fromMCP.Tasks[0].ClaimID)
	assert.Equal(t, page.Items[0].ClaimID.String(), *fromMCP.Tasks[0].ClaimID)
	require.NotNil(t, fromMCP.Tasks[1].CancelledAt)
	assert.Nil(t, fromMCP.Tasks[1].Milepebble, "an uncut milestone's row carries no milepebble")
	assert.Nil(t, fromMCP.Tasks[1].ClaimID, "an unclaimed row carries no claim id")
}

// TestListProductTasks_EmptyPageIsAnEmptyArray proves a product with no
// in-scope tasks returns `[]` rather than null, on both surfaces.
func TestListProductTasks_EmptyPageIsAnEmptyArray(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	cs := connectListProductTasks(t, &productTaskTaskStore{}, &productTaskProductStore{id: productID, scopeID: scopeID})

	res := callListProductTasks(t, cs, map[string]any{"product_id": productID.String()})

	require.False(t, res.IsError, productTaskTextOf(res))
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	assert.JSONEq(t, `{"tasks": []}`, string(raw))
}

// TestListProductTasks_StoreFailureIsAnError proves a genuine store
// failure surfaces as a tool error rather than an empty successful page --
// a caller must never read a failed read as "no tasks".
func TestListProductTasks_StoreFailureIsAnError(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &productTaskTaskStore{err: errors.New("connection reset by peer")}
	cs := connectListProductTasks(t, tasks, &productTaskProductStore{id: productID, scopeID: scopeID})

	res := callListProductTasks(t, cs, map[string]any{"product_id": productID.String()})

	assert.True(t, res.IsError)
	assert.Contains(t, productTaskTextOf(res), "connection reset by peer")
}