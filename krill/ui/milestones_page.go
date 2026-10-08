// The Milestones table at /products/{pid}/milestones: every milestone, highest position
// first. Rows come from app.spec's Delivery (the list_product_delivery read) plus progress
// under ProductTaskScopeAll; status filter and expansion live in the URL so reload/no-JS work.
package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// milestonesStatusQueryParam names the status filter; absent or empty means all statuses.
const milestonesStatusQueryParam = "status"

// milestonesExpandQueryParam names the one milestone whose milepebbles are shown. An id
// not on this page expands nothing; a second expand replaces the first.
const milestonesExpandQueryParam = "expand"

// parseMilestoneStatusFilter rejects a status outside the store's eight rather than
// silently showing all, so a typo is not mistaken for an applied filter.
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

// errUnknownMilestoneStatus is returned for a status the store does not define.
var errUnknownMilestoneStatus = errors.New("milestone status is not one of the store's eight")

// handleProductMilestones serves the product's Milestones table. A failed progress read
// costs only the Progress column, never the rows.
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
	app.renderMilestones(w, r, product, listing, milestoneExpansion{
		Path:     r.URL.Path,
		Status:   status,
		Expanded: parseMilestoneExpansion(r),
	})
}

// milestoneExpansion is this request's row state: path, status filter, expanded milestone.
// Bundled so expander hrefs always re-spell the filter from the same request.
type milestoneExpansion struct {
	// Path is this page's own URL, the base of every expander href.
	Path string

	// Status is the active filter, carried into every expander href.
	Status store.MilestoneStatus

	// Expanded is the milestone whose milepebbles are shown, or uuid.Nil for none.
	Expanded uuid.UUID
}

// parseMilestoneExpansion reads the expanded id. Absent or invalid is uuid.Nil, not an error:
// an unknown id just expands nothing.
func parseMilestoneExpansion(r *http.Request) uuid.UUID {
	raw := r.URL.Query().Get(milestonesExpandQueryParam)
	if raw == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// expandHref is the page URL with mid expanded; the status filter is re-spelled so
// expanding never drops it.
func (e milestoneExpansion) expandHref(mid uuid.UUID) string {
	return e.href(map[string][]string{
		milestonesExpandQueryParam: {mid.String()},
	})
}

// collapseHref is the page URL with the filter kept and the expansion dropped.
func (e milestoneExpansion) collapseHref(mid uuid.UUID) string {
	return e.href(nil)
}

// href builds the page URL with the status filter plus extra params, omitting empty values.
func (e milestoneExpansion) href(extra map[string][]string) string {
	q := url.Values{}
	if e.Status != "" {
		q.Set(milestonesStatusQueryParam, string(e.Status))
	}
	for key, values := range extra {
		for _, v := range values {
			q.Set(key, v)
		}
	}
	if len(q) == 0 {
		return e.Path
	}
	return e.Path + "?" + q.Encode()
}

// renderMilestones writes the full page, or the region fragment for htmx. The status select
// lives inside the swapped region so it always matches the rows.
func (app *App) renderMilestones(w http.ResponseWriter, r *http.Request, product store.Product, listing slice.DeliveryListing, expansion milestoneExpansion) {
	page := app.buildMilestonesPage(r, product, listing, expansion)
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.MilestonesRows(page))
		return
	}
	app.renderShell(w, r, "Milestones", r.URL.Path, pages.Milestones(page))
}

// buildMilestonesPage joins the delivery listing and progress read by milestone id only.
// A failed progress read sets ProgressError; only a failed listing fails the page.
func (app *App) buildMilestonesPage(r *http.Request, product store.Product, listing slice.DeliveryListing, expansion milestoneExpansion) pages.MilestonesPage {
	page := pages.MilestonesPage{
		Product:  productHeaderOf(product),
		Path:     r.URL.Path,
		Statuses: milestoneStatusOptions(expansion.Status),
	}

	progress, err := app.milestoneProgressByID(r.Context(), product.ID)
	if err != nil {
		logger.Error("milestones progress read failed", "product", product.ID.String(), "error", err)
		page.ProgressError = milestonesProgressError
		page.Rows = milestoneRowsWithoutProgress(product.ID, listing, expansion)
	} else {
		page.Rows = milestoneRowsOf(product.ID, listing, progress, expansion)
	}
	page.Empty = len(page.Rows) == 0
	page.EmptyDetail = milestonesEmptyDetail(expansion.Status)
	return page
}

// milestonesEmptyDetail names the active filter so "none in this status" is not mistaken
// for "no milestones at all".
func milestonesEmptyDetail(status store.MilestoneStatus) string {
	if status == "" {
		return "This product has no milestones yet."
	}
	return "This product has no milestone in status “" + string(status) +
		"”. Choose “All statuses” to see every milestone."
}

// milestonesProgressError is shared by the table alert and every row's cell.
const milestonesProgressError = "Task progress could not be read. See the logs."

// milestoneProgressByID reads progress under ProductTaskScopeAll (shipped milestones are rows
// too), keyed by the container's own id: a milepebble's row carries its parent's id in
// Milestone, so a milestone-keyed index would collide parent and children.
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
	byContainer := make(map[uuid.UUID]store.ContainerTaskProgress, len(progress.Containers))
	for _, c := range progress.Containers {
		if c.Milepebble != nil {
			byContainer[c.Milepebble.ID] = c
			continue
		}
		byContainer[c.Milestone.ID] = c
	}
	return byContainer, nil
}

// milestoneRowsOf builds rows sorted by position DESC then id; reversing the ASC listing
// would flip the tiebreak. Rows are re-filtered on each milestone's own status, since the
// delivery read keeps a milestone whose milepebble matched.
func milestoneRowsOf(productID uuid.UUID, listing slice.DeliveryListing, progress map[uuid.UUID]store.ContainerTaskProgress, expansion milestoneExpansion) []pages.MilestoneRow {
	// Copy before sorting: a caller may still hold the listing's slice.
	entries := make([]slice.MilestoneListingEntry, 0, len(listing.Milestones))
	for _, m := range listing.Milestones {
		if expansion.Status == "" || m.Status == expansion.Status {
			entries = append(entries, m)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Position != entries[j].Position {
			return entries[i].Position > entries[j].Position
		}
		// Matches the product task read's ORDER BY m.position DESC, m.id ASC.
		return entries[i].ID.String() < entries[j].ID.String()
	})

	rows := make([]pages.MilestoneRow, 0, len(entries))
	for _, m := range entries {
		rows = append(rows, milestoneRow(productID, m, progress, expansion))
	}
	return rows
}

// milestoneRowsWithoutProgress builds rows when the progress read failed.
func milestoneRowsWithoutProgress(productID uuid.UUID, listing slice.DeliveryListing, expansion milestoneExpansion) []pages.MilestoneRow {
	return milestoneRowsOf(productID, listing, nil, expansion)
}

// milestoneRow is one milestone's row. Figures are the read's own Done() and Total(),
// so cancelled-task counting has one definition.
func milestoneRow(productID uuid.UUID, m slice.MilestoneListingEntry, progress map[uuid.UUID]store.ContainerTaskProgress, expansion milestoneExpansion) pages.MilestoneRow {
	row := pages.MilestoneRow{
		ID:           m.ID.String(),
		Name:         m.Name,
		DetailPath:   milestoneDetailHref(productID, m.ID),
		TasksPath:    productTaskContainerHref(productID, tasksSuffix, milestoneRowContainer(m)),
		BoardPath:    productTaskContainerHref(productID, boardSuffix, milestoneRowContainer(m)),
		Status:       string(m.Status),
		FRBudget:     frBudgetString(m.FRBudget),
		Outcome:      deref(m.Outcome),
		Milepebbles:  milepebbleRows(productID, m, progress),
		Expanded:     expansion.Expanded == m.ID,
		ExpandHref:   expansion.expandHref(m.ID),
		CollapseHref: expansion.collapseHref(m.ID),
	}
	row.ProgressCell = progressCell(progress[m.ID], m.ID)
	return row
}

// milestoneRowContainer is a row's milestone as a taskContainer, built from the listing to
// avoid a second read.
func milestoneRowContainer(m slice.MilestoneListingEntry) taskContainer {
	return taskContainer{
		ID:     m.ID,
		Name:   m.Name,
		Kind:   string(store.MilestoneKindMilestone),
		Status: m.Status,
	}
}

// progressCell is a container's figures, or an error sentence when the read did not account
// for id; a zero-valued map entry is not proof the read covered it.
func progressCell(progress store.ContainerTaskProgress, id uuid.UUID) pages.ProgressCell {
	if !progressAccountsFor(progress, id) {
		// A container the read skipped is not the same as one counted as empty.
		return pages.ProgressCell{ProgressError: milestonesProgressError}
	}
	return pages.ProgressCell{Done: progress.Done(), Total: progress.Total()}
}

// progressAccountsFor reports whether this row is id's. For a milepebble it checks the
// Milepebble ref, since Milestone holds the parent's id.
func progressAccountsFor(progress store.ContainerTaskProgress, id uuid.UUID) bool {
	if id == uuid.Nil {
		return false
	}
	if progress.Milepebble != nil {
		return progress.Milepebble.ID == id
	}
	return progress.Milepebble == nil && progress.Milestone.ID == id
}

// milepebbleRows is a milestone's milepebbles in listing order; nil when none are cut.
func milepebbleRows(productID uuid.UUID, m slice.MilestoneListingEntry, progress map[uuid.UUID]store.ContainerTaskProgress) []pages.MilepebbleRow {
	if len(m.Milepebbles) == 0 {
		return nil
	}
	rows := make([]pages.MilepebbleRow, 0, len(m.Milepebbles))
	for _, mp := range m.Milepebbles {
		rows = append(rows, pages.MilepebbleRow{
			ID:     mp.ID.String(),
			Name:   mp.Name,
			Status: string(mp.Status),
			// Scoped to the milepebble itself, not its parent milestone.
			TasksPath: productTaskContainerHref(productID, tasksSuffix, taskContainer{
				ID:     mp.ID,
				Name:   mp.Name,
				Kind:   string(store.MilestoneKindMilepebble),
				Status: mp.Status,
			}),
			BoardPath: productTaskContainerHref(productID, boardSuffix, taskContainer{
				ID:     mp.ID,
				Name:   mp.Name,
				Kind:   string(store.MilestoneKindMilepebble),
				Status: mp.Status,
			}),
			ProgressCell: progressCell(progress[mp.ID], mp.ID),
		})
	}
	return rows
}

// milestoneStatusOptions is "All statuses" then store.MilestoneStatusOrder, so the select
// offers exactly what the parser accepts.
func milestoneStatusOptions(selected store.MilestoneStatus) []pages.MilestoneStatusOption {
	options := make([]pages.MilestoneStatusOption, 0, len(store.MilestoneStatusOrder)+1)
	options = append(options, pages.MilestoneStatusOption{
		Value: "", Label: "All statuses", Selected: selected == "",
	})
	for _, status := range store.MilestoneStatusOrder {
		options = append(options, pages.MilestoneStatusOption{
			Value:    string(status),
			Label:    string(status),
			Selected: status == selected,
		})
	}
	return options
}

// milestonesStatusFilter is the delivery read's filter: nil for all, else one status.
func milestonesStatusFilter(status store.MilestoneStatus) []store.MilestoneStatus {
	if status == "" {
		return nil
	}
	return []store.MilestoneStatus{status}
}

// renderMilestonesStatusProblem renders an in-shell 400 for an unknown status.
func (app *App) renderMilestonesStatusProblem(w http.ResponseWriter, r *http.Request, product store.Product) {
	app.renderShellStatus(w, r, "Unknown status", r.URL.Path, pages.SpecStatus(pages.StatusPage{
		Title:    "That is not one of the eight statuses",
		Detail:   milestoneStatusFilterDetail(),
		BackHref: milestonesPath(product.ID),
		BackText: "Back to milestones",
	}), http.StatusBadRequest)
}

// milestoneStatusFilterDetail lists the accepted statuses for the 400 message.
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

// renderMilestonesReadError renders store.ErrNotFound as a 404 and anything else as a
// logged 500, both inside the shell.
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
