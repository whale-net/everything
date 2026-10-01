// This file (FR 59f664ff-3aa9-4d90-a861-d7758ecee040) is the product-wide
// per-container task-progress read's HTTP surface: GET
// /products/{id}/task-progress -- every milestone and milepebble of the
// product with its task count, per-lane counts, and Done count, in one
// request. Ungated like every other read endpoint in this package (FR3's
// write-only gate, root plan issue #2485); the caller-supplied {id} is the
// Product whose scope the read resolves from, exactly like
// GetProductDeliveryHandler's.
//
// The scope and container_id query parameters are the very same pair
// GET /products/{id}/tasks takes, so a progress header and the task list
// beneath it are always describing the same containers.
//
// LB7 parity: the wire types below are exported and the constructor is
// exported, so krill/mcp/tools' get_product_task_progress returns this
// exact value rather than an MCP-local mirror of the same data.
package handlers

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// TaskLaneCountsWire is the wire shape of one store.TaskLaneCounts -- the
// five fixed lane counts, keyed by store.Lane's own vocabulary verbatim so
// the wire and the store can never spell a lane differently. A count is
// always present, including a zero: a caller rendering a stacked bar needs
// every segment's width, and an absent key would have to be special-cased
// back to zero.
type TaskLaneCountsWire struct {
	Scaffold       int `json:"scaffold"`
	Implementation int `json:"implementation"`
	Testing        int `json:"testing"`
	Validation     int `json:"validation"`
	Done           int `json:"done"`
}

// ToTaskLaneCountsWire converts one store.TaskLaneCounts to its wire shape.
func ToTaskLaneCountsWire(c store.TaskLaneCounts) TaskLaneCountsWire {
	return TaskLaneCountsWire{
		Scaffold:       c.Scaffold,
		Implementation: c.Implementation,
		Testing:        c.Testing,
		Validation:     c.Validation,
		Done:           c.Done,
	}
}

// ContainerTaskProgressWire is the wire shape of one
// store.ContainerTaskProgress: the container (milestone, plus milepebble
// when the row IS one), the per-lane breakdown, and the two figures a
// progress bar renders from it. Total and done are carried alongside
// per_lane rather than left for the client to add up, so a client never
// has to re-derive the "N of M" pair and cannot disagree with the
// breakdown it renders above it.
//
// has_tasks is what separates a container with no tasks (rendered "No
// tasks yet", never "0 of 0") from one whose tasks have all been
// cancelled.
type ContainerTaskProgressWire struct {
	Milestone  ProductTaskContainerWire  `json:"milestone"`
	Milepebble *ProductTaskContainerWire `json:"milepebble,omitempty"`

	PerLane   TaskLaneCountsWire `json:"per_lane"`
	Total     int                `json:"total"`
	Done      int                `json:"done"`
	Cancelled int                `json:"cancelled"`
	HasTasks  bool               `json:"has_tasks"`
}

// ToContainerTaskProgressWire converts one store.ContainerTaskProgress to
// its wire shape.
func ToContainerTaskProgressWire(p store.ContainerTaskProgress) ContainerTaskProgressWire {
	wire := ContainerTaskProgressWire{
		Milestone: ProductTaskContainerWire{
			ID:     p.Milestone.ID.String(),
			Name:   p.Milestone.Name,
			Status: string(p.Milestone.Status),
		},
		PerLane:   ToTaskLaneCountsWire(p.PerLane),
		Total:     p.Total(),
		Done:      p.Done(),
		Cancelled: p.Cancelled,
		HasTasks:  p.Total() > 0,
	}
	if p.Milepebble != nil {
		wire.Milepebble = &ProductTaskContainerWire{
			ID:     p.Milepebble.ID.String(),
			Name:   p.Milepebble.Name,
			Status: string(p.Milepebble.Status),
		}
	}
	return wire
}

// ProductTaskProgressWire is the wire shape of one
// store.ProductTaskProgress: the product's per-container rows, roadmap
// ordered. Containers is always non-nil (an empty JSON array, never null)
// so a client can index it without a nil check.
type ProductTaskProgressWire struct {
	ProductID  string                      `json:"product_id"`
	Containers []ContainerTaskProgressWire `json:"containers"`
}

// NewProductTaskProgressWire converts a store.ProductTaskProgress to its
// wire shape. Exported so krill/mcp/tools builds the identical value
// (LB7).
func NewProductTaskProgressWire(p store.ProductTaskProgress) ProductTaskProgressWire {
	out := ProductTaskProgressWire{
		ProductID:  p.ProductID.String(),
		Containers: make([]ContainerTaskProgressWire, len(p.Containers)),
	}
	for i, row := range p.Containers {
		out.Containers[i] = ToContainerTaskProgressWire(row)
	}
	return out
}

// GetProductTaskProgressHandler returns the product-wide per-container
// task-progress read: GET /products/{id}/task-progress?scope=...&container_id=...
//
// scope absent (or "incomplete") is every milestone and milepebble of the
// product that is neither shipped nor abandoned -- the same default the
// task list above it uses, so the two never describe different
// containers. "milestone" and "milepebble" are the two single-container
// scopes and require container_id.
//
// 404s on an unknown product id, and on a container that belongs to
// another product (store.ErrMilestoneOutsideProduct) -- never another
// product's counts and never an empty listing that would read as "this
// product has no milestones".
func GetProductTaskProgressHandler(tasks store.TaskStore, products store.ProductStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		productID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		// The same parser the paged task read's handler uses, so the two
		// surfaces accept exactly the same scope vocabulary.
		scope, err := parseProductTaskScope(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		// `task` and `milestone_ref` rows are both scope-qualified, so
		// this read needs productID's own scope_id even though the caller
		// supplied no session -- resolved from the product row itself, the
		// same LB2 parentage the task read's handler relies on.
		product, err := products.GetCurrentByID(r.Context(), productID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "product not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		progress, err := tasks.SummarizeProductTaskProgress(r.Context(), store.ProductTaskProgressParams{
			ScopeID:   product.ScopeID,
			ProductID: productID,
			Scope:     scope,
		})
		if err != nil {
			if errors.Is(err, store.ErrMilestoneOutsideProduct) {
				writeJSONError(w, http.StatusNotFound, "milestone not found in this product")
				return
			}
			writeConsoleQueryError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, NewProductTaskProgressWire(progress))
	}
}
