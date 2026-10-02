// This file (FR cfcd1104-b015-4ffd-a07a-abb7c6e435d3) is the
// product-wide paged task read's HTTP surface: GET /products/{id}/tasks --
// one page of a product's tasks across a scope of its delivery containers,
// with optional lane and only-stuck filters. Ungated like every other read
// endpoint in this package (NFR6's gate is write-only); the {id} is the
// Product whose own scope the read resolves from, exactly like
// GetProductDeliveryHandler's.
//
// The per-milestone read above (task_list.go, GET /milestones/{id}/tasks)
// is unchanged and stays the unpaginated per-container read.
//
// LB7 parity: the wire types below are exported and the constructor is
// exported, so krill/mcp/tools' list_product_tasks returns this exact
// value rather than an MCP-local mirror of the same data.
package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

const (
	scopeKindQueryParam = "scope"
	containerQueryParam = "container_id"
	laneQueryParam      = "lane"
	onlyStuckQueryParam = "only_stuck"
)

// ProductTaskContainerWire is the wire shape of one
// store.ProductTaskMilestoneRef / store.ProductTaskMilepebbleRef -- the
// id, name and derived current status of one delivery container. Shared by
// both, since they carry exactly the same three fields.
type ProductTaskContainerWire struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ProductTaskWire is the wire shape of one store.ProductTaskRow (FR2).
// Exported (mirrors ClaimedTaskWire's own precedent, console.go) so
// krill/mcp/tools' list_product_tasks tool returns this exact shape.
// LeaseExpiresAt and ClaimID omit when nil (no open claim), EscalationReason
// omits unless the task is escalated, and CancelledAt omits unless it is
// cancelled -- rather than serializing as a literal `null`.
type ProductTaskWire struct {
	TaskID     string                    `json:"task_id"`
	Title      string                    `json:"title"`
	Milestone  ProductTaskContainerWire  `json:"milestone"`
	Milepebble *ProductTaskContainerWire `json:"milepebble,omitempty"`

	CurrentLane      string     `json:"current_lane"`
	State            string     `json:"state"`
	EscalationReason *string    `json:"escalation_reason,omitempty"`
	CancelledAt      *time.Time `json:"cancelled_at,omitempty"`

	AttemptCount   int        `json:"attempt_count"`
	AttemptCap     int        `json:"attempt_cap"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	ClaimID        *string    `json:"claim_id,omitempty"`
}

// ToProductTaskWire converts one store.ProductTaskRow to its wire shape.
func ToProductTaskWire(row store.ProductTaskRow) ProductTaskWire {
	wire := ProductTaskWire{
		TaskID: row.TaskID.String(),
		Title:  row.Title,
		Milestone: ProductTaskContainerWire{
			ID:     row.Milestone.ID.String(),
			Name:   row.Milestone.Name,
			Status: string(row.Milestone.Status),
		},
		CurrentLane:    string(row.CurrentLane),
		State:          string(row.State),
		CancelledAt:    row.CancelledAt,
		AttemptCount:   row.AttemptCount,
		AttemptCap:     row.AttemptCap,
		LeaseExpiresAt: row.LeaseExpiresAt,
	}
	if row.Milepebble != nil {
		wire.Milepebble = &ProductTaskContainerWire{
			ID:     row.Milepebble.ID.String(),
			Name:   row.Milepebble.Name,
			Status: string(row.Milepebble.Status),
		}
	}
	if row.EscalationReason != nil {
		reason := string(*row.EscalationReason)
		wire.EscalationReason = &reason
	}
	if row.ClaimID != nil {
		claimID := row.ClaimID.String()
		wire.ClaimID = &claimID
	}
	return wire
}

// ListProductTasksResponse is ListProductTasksHandler's response body
// (NFR6): a bounded page plus a continuation token, present exactly when
// more rows remain. Tasks is always a non-nil array so a client can index
// it without a nil check.
type ListProductTasksResponse struct {
	Tasks     []ProductTaskWire `json:"tasks"`
	NextToken string            `json:"next_token,omitempty"`
}

// NewListProductTasksResponse maps store.TaskStore.ListProductTasks'
// result onto its wire shape -- shared by ListProductTasksHandler and the
// list_product_tasks MCP tool.
func NewListProductTasksResponse(page store.Page[store.ProductTaskRow]) ListProductTasksResponse {
	wire := make([]ProductTaskWire, len(page.Items))
	for i, row := range page.Items {
		wire[i] = ToProductTaskWire(row)
	}
	return ListProductTasksResponse{Tasks: wire, NextToken: page.NextToken}
}

// ListProductTasksHandler returns the product-wide paged task read: GET
// /products/{id}/tasks?scope=...&container_id=...&lane=...&only_stuck=...
// &page_size=...&page_token=....
//
// scope absent (or "incomplete") is every incomplete milestone of the
// product; "milestone" and "milepebble" are the two single-container
// scopes and require container_id. An unrecognized scope or lane is a 400
// naming the valid values rather than a silent empty page, and a container
// belonging to another product is a 404, never another product's tasks.
func ListProductTasksHandler(tasks store.TaskStore, products store.ProductStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := parseListProductTasksParams(r, products)
		if err != nil {
			writeProductTasksParamError(w, err)
			return
		}

		page, err := tasks.ListProductTasks(r.Context(), params)
		if err != nil {
			// A container outside the product is a 404 naming the product
			// not found, never another product's tasks and never an empty
			// page that would read as "this milestone has no work".
			if errors.Is(err, store.ErrMilestoneOutsideProduct) {
				writeJSONError(w, http.StatusNotFound, "milestone not found in this product")
				return
			}
			writeConsoleQueryError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, NewListProductTasksResponse(page))
	}
}

// CountProductTasksHandler returns the "Y" in "Showing X of Y tasks": GET
// /products/{id}/tasks/count, taking the same path value and the same
// scope/container_id/lane/only_stuck filters as
// ListProductTasksHandler, and returning how many rows that list would
// return unpaged. It shares that handler's query-string parser, so the
// total and the page beneath it can never take different filters; the
// page_size/page_token parameters it also accepts are ignored, a count
// being of the whole filtered set.
func CountProductTasksHandler(tasks store.TaskStore, products store.ProductStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := parseListProductTasksParams(r, products)
		if err != nil {
			writeProductTasksParamError(w, err)
			return
		}

		// The shared parser reads the paging pair so both endpoints accept
		// the same query string; a count is of the whole filtered set, so it
		// drops the page here rather than relying on the store to ignore
		// it. The same rule the four console count endpoints apply.
		params.Page = store.PageParams{}

		count, err := tasks.CountProductTasks(r.Context(), params)
		if err != nil {
			if errors.Is(err, store.ErrMilestoneOutsideProduct) {
				writeJSONError(w, http.StatusNotFound, "milestone not found in this product")
				return
			}
			writeConsoleQueryError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, ConsoleCountWire{Count: count})
	}
}

// errProductTasksScopeUnresolvable marks the one parse failure that is a
// 404 rather than a 400: the product the {id} names does not exist, so
// there is nothing whose scope the read could resolve.
var errProductTasksScopeUnresolvable = errors.New("product not found")

// productLookupError marks a failure resolving the product row itself --
// distinct from a malformed filter. The request named a well-formed
// product the store could not answer for, which is a 500 rather than
// anything the caller can fix.
type productLookupError struct{ err error }

func (e productLookupError) Error() string { return e.err.Error() }

func (e productLookupError) Unwrap() error { return e.err }

// parseListProductTasksParams is the one reader both
// ListProductTasksHandler and CountProductTasksHandler use for this read's
// path value and query string, so the list and the total printed beside it
// are always built from the same scope, container, lane and only-stuck
// filters.
func parseListProductTasksParams(r *http.Request, products store.ProductStore) (store.ListProductTasksParams, error) {
	productID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return store.ListProductTasksParams{}, errors.New("invalid id: must be a UUID")
	}

	scope, err := parseProductTaskScope(r)
	if err != nil {
		return store.ListProductTasksParams{}, err
	}

	lane, err := parseTaskLaneFilter(r)
	if err != nil {
		return store.ListProductTasksParams{}, err
	}

	onlyStuck, err := parseBoolParam(r, onlyStuckQueryParam)
	if err != nil {
		return store.ListProductTasksParams{}, err
	}

	pageSize, err := parsePageSizeParam(r)
	if err != nil {
		return store.ListProductTasksParams{}, err
	}

	// `task` and `milestone_ref` rows are both scope-qualified, so this read
	// needs productID's own scope_id even though the caller supplied no
	// session -- resolved from the product row itself, the same LB2
	// parentage GetProductDeliveryHandler relies on.
	product, err := products.GetCurrentByID(r.Context(), productID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ListProductTasksParams{}, errProductTasksScopeUnresolvable
		}
		return store.ListProductTasksParams{}, productLookupError{err: err}
	}

	return store.ListProductTasksParams{
		ScopeID:   product.ScopeID,
		ProductID: productID,
		Scope:     scope,
		Lane:      lane,
		OnlyStuck: onlyStuck,
		Page: store.PageParams{
			PageSize:          pageSize,
			ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
		},
	}, nil
}

// writeProductTasksParamError maps this read's parse failures onto the
// three statuses they can mean: a 404 for a product that does not exist, a
// 400 for any malformed filter, and a 500 for a product lookup that
// failed on its own terms.
func writeProductTasksParamError(w http.ResponseWriter, err error) {
	if errors.Is(err, errProductTasksScopeUnresolvable) {
		writeJSONError(w, http.StatusNotFound, "product not found")
		return
	}
	var lookup productLookupError
	if errors.As(err, &lookup) {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSONError(w, http.StatusBadRequest, err.Error())
}

// parseProductTaskScope parses the scope/container_id pair this read's
// single-container scopes need. An absent scope means
// store.ProductTaskScopeIncomplete; the two single-container kinds require
// a parseable container_id.
func parseProductTaskScope(r *http.Request) (store.ProductTaskScope, error) {
	raw := r.URL.Query().Get(scopeKindQueryParam)
	if raw == "" {
		raw = string(store.ProductTaskScopeIncomplete)
	}
	var kind store.ProductTaskScopeKind
	switch store.ProductTaskScopeKind(raw) {
	case store.ProductTaskScopeIncomplete, store.ProductTaskScopeMilestone, store.ProductTaskScopeMilepebble:
		kind = store.ProductTaskScopeKind(raw)
	default:
		return store.ProductTaskScope{}, queryParamError(scopeKindQueryParam,
			"must be one of "+joinScopeKinds(store.ValidProductTaskScopeKinds))
	}

	scope := store.ProductTaskScope{Kind: kind}
	if !scope.RequiresContainer() {
		return scope, nil
	}

	containerID, err := uuid.Parse(r.URL.Query().Get(containerQueryParam))
	if err != nil {
		return store.ProductTaskScope{}, queryParamError(containerQueryParam,
			"required for scope "+string(kind)+"; invalid or missing UUID")
	}
	scope.ContainerID = containerID
	return scope, nil
}

// parseTaskLaneFilter parses lane's query value into the optional
// *store.Lane filter. An absent lane means every lane, never no lanes.
func parseTaskLaneFilter(r *http.Request) (*store.Lane, error) {
	raw := r.URL.Query().Get(laneQueryParam)
	if raw == "" {
		return nil, nil
	}
	for _, lane := range store.CanonicalLaneOrder {
		if string(lane) == raw {
			out := lane
			return &out, nil
		}
	}
	return nil, queryParamError(laneQueryParam, "must be one of "+joinLanes(store.CanonicalLaneOrder))
}

// parseBoolParam parses a boolean query parameter, absent meaning false.
func parseBoolParam(r *http.Request, name string) (bool, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, queryParamError(name, "must be a boolean")
	}
	return v, nil
}

func joinScopeKinds(kinds []store.ProductTaskScopeKind) string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return strings.Join(out, ", ")
}

func joinLanes(lanes []store.Lane) string {
	out := make([]string, len(lanes))
	for i, l := range lanes {
		out[i] = string(l)
	}
	return strings.Join(out, ", ")
}

func queryParamError(name, detail string) error {
	return errors.New(name + ": " + detail)
}
