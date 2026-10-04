// The Milestones table: every milestone of a product, highest position
// first, with its status, its task progress, its FR budget and its outcome
// (FR 5aec68f6).
//
// It is served at /products/{pid}/milestones, the console's own answer to
// "where is this product's delivery" -- the view the pre-redesign delivery
// page is superseded by, and the one a milestone's name links into.
//
// The rows come from two reads that are deliberately not merged. The
// listing is app.spec's Delivery -- the same //krill/slice.Querier the MCP
// tool list_product_delivery calls underneath, so a row's name, status,
// outcome and FR budget are the delivery scope's own answers and cannot
// drift from the tool's. The progress figures come from
// SummarizeProductTaskProgress under ProductTaskScopeAll, because this
// table lists EVERY milestone including the shipped ones whose bars the
// in-flight scope would leave out (task_progress.go).
//
// The status filter is a query parameter, not a client-side hide: the rows
// come back already filtered, so a reload, a shared link and a no-JS submit
// all land on the same table. htmx layers an in-place swap on top of that
// GET form, so changing the select never reloads the page around it.
package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// milestonesStatusQueryParam names the status filter. It is absent (or
// empty) for "All statuses", mirroring the delivery read's own contract
// that an empty statuses slice means every status rather than none.
const milestonesStatusQueryParam = "status"

// milestonesStatusFilterError is the in-shell 400 a status outside the
// store's eight is answered with. The refusal is deliberate rather than a
// silent fallback to "All statuses": a hand-edited URL naming a status
// that does not exist has a typo in it, and quietly showing the whole
// table instead would leave the operator believing their filter applied
// when it did not.
func (app *App) parseMilestoneStatusFilter(r *http.Request, productID uuid.UUID) (store.MilestoneStatus, error) {
	raw := r.URL.Query().Get(milestonesStatusQueryParam)
	if raw == "" {
		return "", nil
	}
	status := store.MilestoneStatus(raw)
	for _, valid := range store.MilestoneStatusOrder {
		if status == valid {
			return status, nil
		}
	}
	return "", errUnknownMilestoneStatus
}

// errUnknownMilestoneStatus is the sentinel parseMilestoneStatusFilter
// returns for a status the store's enumeration does not carry. It is
// package-level so no caller can construct a different error for the same
// refusal and answer it differently.
var errUnknownMilestoneStatus = errors.New("milestone status is not one of the store's eight")

// handleProductMilestones serves the product's Milestones table.
//
// The product is resolved from the URL first, so an id outside the
// caller's scope is already an in-shell 404 before any read. The two reads
// that follow are independent: a progress read that fails costs the
// Progress column alone, never the names and statuses beside it, because a
// table that dropped every row to report one unreadable figure would hide
// the roadmap an operator came for.
func (app *App) handleProductMilestones(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	setLastViewedProductCookie(w, product.ID)

	status, err := app.parseMilestoneStatusFilter(r, product.ID)
	if err != nil {
		app.renderMilestonesStatusProblem(w, r, product)
		return
	}

	listing, err := app.spec.Delivery(r.Context(), product.ID, milestonesStatusFilter(status))
	if err != nil {
		app.renderMilestonesReadError(w, r, product, err)
		return
	}
	app.renderMilestones(w, r, product, listing, status)
}

// renderMilestones writes the table: the whole page for a browser, and
// the region's own fragment for an htmx request -- which is what the
// status select's swap asks for, so changing the filter replaces the
// region rather than the page around it.
func (app *App) renderMilestones(w http.ResponseWriter, r *http.Request, product store.Product, listing slice.DeliveryListing, status store.MilestoneStatus) {
	page := app.buildMilestonesPage(r, product, listing, status)
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.MilestonesRows(page))
		return
	}
	app.renderShell(w, r, "Milestones", r.URL.Path, pages.Milestones(page))
}

// buildMilestonesPage assembles the region's view model from the delivery
// listing and the product-wide progress read.
//
// The two reads are joined by milestone id, and only by milestone id: a
// row's name, status, outcome and FR budget come from the listing, its
// progress figures from the progress read. Neither read's fields are
// re-derived from the other, so a row cannot show a status from one read
// beside progress counted by a different rule.
//
// A failed progress read is not a failed page: ProgressError carries the
// sentence every row shows in place of its bar, and the rest of the table
// still answers. Only the listing read failing takes the page down,
// because the table IS the listing.
func (app *App) buildMilestonesPage(r *http.Request, product store.Product, listing slice.DeliveryListing, status store.MilestoneStatus) pages.MilestonesPage {
	page := pages.MilestonesPage{
		Product:  productHeaderOf(product),
		Path:     r.URL.Path,
		Statuses: milestoneStatusOptions(status),
	}

	// A failed progress read costs the Progress column, not the table: the
	// rows are still built from the listing alone, so the operator keeps
	// the names, statuses, budgets and outcomes and each Progress cell
	// says what could not be read. A table that vanished entirely would
	// leave them with neither the roadmap nor the reason it is missing.
	progress, err := app.milestoneProgressByID(r.Context(), product.ID)
	if err != nil {
		logger.Error("milestones progress read failed", "product", product.ID.String(), "error", err)
		page.ProgressError = milestonesProgressError
		page.Rows = milestoneRowsWithoutProgress(product.ID, listing, status)
	} else {
		page.Rows = milestoneRowsOf(product.ID, listing, progress, status)
	}
	page.Empty = len(page.Rows) == 0
	page.EmptyDetail = milestonesEmptyDetail(status)
	return page
}

// milestonesEmptyDetail is the empty state's second sentence, naming the
// filter that produced it.
//
// The two cases are genuinely different and must not read alike: a product
// with no milestone in the chosen status has plenty of milestones under
// "All statuses", while a product with no milestones at all is a
// different page. Without the filter named, both render as "no
// milestones", and an operator filtering to "shipped" concludes their
// product was never built.
func milestonesEmptyDetail(status store.MilestoneStatus) string {
	if status == "" {
		return "This product has no milestones yet."
	}
	return "This product has no milestone in status “" + string(status) +
		"”. Choose “All statuses” to see every milestone."
}

// milestonesProgressError is the sentence the Progress column shows when
// the read failed. It is a package-level constant so the alert above the
// table and every row's cell say one thing.
const milestonesProgressError = "Task progress could not be read. See the logs."

// milestoneProgressByID reads every milestone and milepebble of the
// product's progress under the all-containers scope, indexed by the
// milestone's own id.
//
// The scope is ProductTaskScopeAll, not the default incomplete one: this
// table lists the WHOLE roadmap, and a shipped milestone is a row here --
// its bar at full is the answer an operator wants, and the incomplete
// scope would answer "No tasks yet" for work that is finished.
//
// A row is indexed by its milestone's id rather than by its container id
// because the table's rows ARE milestones. A milepebble's own progress
// belongs under its parent (the FR's inline expansion, and this page's
// detail sibling), and the milestone row's own figures already cover the
// whole cut -- store.ContainerTaskProgress documents that its PerLane
// spans the milestone's direct tasks plus every milepebble's -- so nothing
// is lost by indexing on the milestone.
func (app *App) milestoneProgressByID(ctx context.Context, productID uuid.UUID) (map[uuid.UUID]store.ContainerTaskProgress, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return nil, err
	}
	progress, err := app.tasks.SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
		ScopeID:   scopeID,
		ProductID: productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeAll},
	})
	if err != nil {
		return nil, err
	}
	byMilestone := make(map[uuid.UUID]store.ContainerTaskProgress, len(progress.Containers))
	for _, c := range progress.Containers {
		if c.Milepebble != nil {
			continue
		}
		byMilestone[c.Milestone.ID] = c
	}
	return byMilestone, nil
}

// milestoneRowsOf builds one row per milestone, with its figures from the
// progress read, in the FR's order: highest roadmap position first, then
// id.
//
// The sort is here rather than inherited because the listing arrives
// position ASC -- ascending is the renderer's creation order, and this
// table reads newest-first. Position is carried on the listing entry for
// exactly this: reversing a sorted slice would also reverse the id
// tiebreak within one position, which is not the same order.
//
// The rows are the listing's milestones, re-filtered on each milestone's
// OWN status. The delivery read's filter deliberately keeps a milestone
// whose milepebble matched even when the milestone itself did not (see
// //krill/slice's ListProductDelivery), which is right for a wire shape
// that nests milepebbles under their parent and wrong for this table: a
// row badged "in progress" under a "shipped" filter would be the one
// obvious way for the select to lie. So the read narrows the work and
// this filter decides the rows -- and it is the milestone's own status
// either way, never a milepebble's.
func milestoneRowsOf(productID uuid.UUID, listing slice.DeliveryListing, progress map[uuid.UUID]store.ContainerTaskProgress, status store.MilestoneStatus) []pages.MilestoneRow {
	// The listing's own slice is not sorted: a caller may still be
	// holding it, and sorting it in place would reorder the delivery
	// page's rows under this one.
	entries := make([]slice.MilestoneListingEntry, 0, len(listing.Milestones))
	for _, m := range listing.Milestones {
		if status == "" || m.Status == status {
			entries = append(entries, m)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Position != entries[j].Position {
			return entries[i].Position > entries[j].Position
		}
		// The FR's tiebreak, and the same pair the product task read
		// sorts its own rows by (task_product_list.go's ORDER BY
		// m.position DESC, m.id ASC), so a milestone and its tasks appear
		// in one consistent order. The uuid's own bytes compare the way
		// Postgres compares the id column.
		return entries[i].ID.String() < entries[j].ID.String()
	})

	rows := make([]pages.MilestoneRow, 0, len(entries))
	for _, m := range entries {
		rows = append(rows, milestoneRow(productID, m, progress[m.ID]))
	}
	return rows
}

// milestoneRowsWithoutProgress is milestoneRowsOf with the figures the
// failed read could not supply, for the case where it failed at all.
func milestoneRowsWithoutProgress(productID uuid.UUID, listing slice.DeliveryListing, status store.MilestoneStatus) []pages.MilestoneRow {
	return milestoneRowsOf(productID, listing, nil, status)
}

// milestoneRow is one milestone's row: the listing entry's own fields, and
// the progress figures for that milestone.
//
// Figures are read off the progress read's own Done() and Total() rather
// than summed from the lane counts, for the reason the read documents: it
// owns how a cancelled task counts, and re-deriving the arithmetic here is
// how a progress column comes to disagree with the lane breakdown beside
// it.
func milestoneRow(productID uuid.UUID, m slice.MilestoneListingEntry, progress store.ContainerTaskProgress) pages.MilestoneRow {
	row := pages.MilestoneRow{
		ID:         m.ID.String(),
		Name:       m.Name,
		DetailPath: milestoneDetailHref(productID, m.ID),
		Status:     string(m.Status),
		FRBudget:   frBudgetString(m.FRBudget),
		Outcome:    deref(m.Outcome),
	}
	if progress.Milestone.ID == m.ID {
		row.Done = progress.Done()
		row.Total = progress.Total()
	} else {
		// No progress row for this milestone. The figures stay zero, which
		// renders as "No tasks yet" -- the honest reading of a container the
		// read did not account for is not the same as one it counted as
		// empty, so the cell says which: a read that came back short is a
		// different fact from a milestone with no work, and this branch
		// exists so the two are not silently the same.
		row.ProgressError = milestonesProgressError
	}
	return row
}

// milestoneStatusOptions is the status select's options: "All statuses"
// first, then the store's eight in its own order, with the one this
// request filtered to marked.
//
// The set is store.MilestoneStatusOrder rather than a UI copy, so the
// select offers exactly what the parser accepts and a ninth status appears
// in both without a second edit.
func milestoneStatusOptions(selected store.MilestoneStatus) []pages.MilestoneStatusOption {
	options := make([]pages.MilestoneStatusOption, 0, len(store.MilestoneStatusOrder)+1)
	options = append(options, pages.MilestoneStatusOption{
		Value: "", Label: "All statuses", Selected: selected == "",
	})
	for _, status := range store.MilestoneStatusOrder {
		options = append(options, pages.MilestoneStatusOption{
			Value:     string(status),
			Label:     string(status),
			Selected:  status == selected,
		})
	}
	return options
}

// milestonesStatusFilter is the filter the delivery read takes: nil for
// every status, the one-element slice for a chosen one -- the querier's
// own contract treats an empty slice as "all" too, but nil is what says
// "no filter was chosen" in the shape every other caller of the read uses.
func milestonesStatusFilter(status store.MilestoneStatus) []store.MilestoneStatus {
	if status == "" {
		return nil
	}
	return []store.MilestoneStatus{status}
}

// renderMilestonesStatusProblem answers a status the store's enumeration
// does not carry: a 400 naming the eight that exist, rendered inside the
// shell with a way back to the unfiltered table.
func (app *App) renderMilestonesStatusProblem(w http.ResponseWriter, r *http.Request, product store.Product) {
	app.renderShellStatus(w, r, "Unknown status", r.URL.Path, pages.SpecStatus(pages.StatusPage{
		Title:    "That is not one of the eight statuses",
		Detail:   milestoneStatusFilterDetail(),
		BackHref: milestonesPath(product.ID),
		BackText: "Back to milestones",
	}), http.StatusBadRequest)
}

// milestoneStatusFilterDetail names the values the select accepts, so a
// mistyped status says which spellings would have worked.
func milestoneStatusFilterDetail() string {
	quoted := make([]string, 0, len(store.MilestoneStatusOrder))
	for _, status := range store.MilestoneStatusOrder {
		quoted = append(quoted, `"`+string(status)+`"`)
	}
	return "The status filter must be one of " + strings.Join(quoted, ", ") + "."
}

// milestonesPath is the Milestones table's own URL for a product.
func milestonesPath(productID uuid.UUID) string {
	return productHref(productID, milestonesSuffix)
}

// renderMilestonesReadError maps the delivery read's failure onto a page.
//
// store.ErrNotFound -- an unknown or superseded product -- is a 404 the
// operator can act on; anything else is a genuine read failure, logged at
// ERROR before a 500. Both render inside the shell rather than as a bare
// http.Error string, for the reason every other route here does it.
func (app *App) renderMilestonesReadError(w http.ResponseWriter, r *http.Request, product store.Product, err error) {
	if errors.Is(err, store.ErrNotFound) {
		app.renderShellStatus(w, r, "Not found", r.URL.Path, pages.SpecStatus(pages.StatusPage{
			Title:    "Not found",
			Detail:   "No current product matches that id.",
			BackHref: productsPath,
			BackText: "Back to products",
		}), http.StatusNotFound)
		return
	}
	logger.Error("milestones delivery read failed", "product", product.ID.String(), "error", err)
	app.renderShellStatus(w, r, "Could not load milestones", r.URL.Path,
		pages.SpecStatus(pages.StatusPage{
			Title:    "Could not load the milestones",
			Detail:   "The delivery store could not be read. See the logs.",
			BackHref: productPath(product.ID),
			BackText: "Back to the capability map",
		}), http.StatusInternalServerError)
}