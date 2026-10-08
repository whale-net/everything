// The product-wide Tasks and Board routes: two views of one scope, served by the
// same handler with the view's name.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// View names: the region heading, nav key and template choice. Only Tasks offers
// the lane and only-stuck controls.
const (
	productTasksView = "Tasks"
	productBoardView = "Board"
)

func (app *App) handleProductTasks(w http.ResponseWriter, r *http.Request) {
	app.serveProductTaskRegion(w, r, productTasksView)
}

func (app *App) handleProductBoard(w http.ResponseWriter, r *http.Request) {
	app.serveProductTaskRegion(w, r, productBoardView)
}

// serveProductTaskRegion is the one handler behind both views. Full-page failures
// get an in-shell status page; htmx gets 200 with the message inline, since htmx
// does not swap non-2xx responses.
func (app *App) serveProductTaskRegion(w http.ResponseWriter, r *http.Request, view string) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	// Only real page views move the last-viewed cookie; htmx swaps fire on every
	// control change.
	if r.Header.Get("HX-Request") == "" {
		setLastViewedProductCookie(w, product.ID)
	}

	scope, problem := parseProductTaskScope(r.URL.Query())
	if problem != productTaskScopeOK {
		app.renderProductTaskScopeProblem(w, r, product, view, resolvedProductTaskScope{}, problem)
		return
	}

	resolved, problem := app.resolveProductTaskScope(r.Context(), product.ID, scope)
	if problem != productTaskScopeOK {
		app.renderProductTaskScopeProblem(w, r, product, view, resolved, problem)
		return
	}

	region := productTaskRegionOf(r, product, view, resolved)
	// One read instant for the whole region, so rows never disagree about lease expiry.
	readAt := time.Now()
	page, err := app.readProductTasks(r.Context(), product.ID, resolved)
	if err != nil {
		// A refused continuation token is the caller's stale link, not a read failure, and
		// must never be answered with rows the URL did not ask for.
		if problem := productTaskTokenProblemOf(err); problem != productTaskTokenOK {
			region.PageError = productTaskTokenProblemMessage(problem)
			region.PageRecoveryHref = productTaskPageRecoveryPath(r)
			region.PageRecoveryText = "Back to the first page"
			// 400 for a stale link, but 200 for htmx so the fragment actually swaps.
			status := http.StatusBadRequest
			if r.Header.Get("HX-Request") != "" {
				status = http.StatusOK
			}
			app.renderProductTaskRegion(w, r, view, region, status)
			return
		}
		// An empty region would read as "no tasks", so a failed read is a visible 500.
		logger.Error("product task read failed", "product", product.ID.String(), "error", err)
		region.Error = "The tasks could not be read. See the logs."
		app.renderProductTaskView(w, r, product, region, resolved, nil, http.StatusInternalServerError)
		return
	}
	region.Total = page.Total
	region.Rows = productTaskRowsOf(product.ID, page.Rows, readAt)
	region.UpdatedAt = readAt.UTC().Format(time.RFC3339)
	region.Empty = len(page.Rows) == 0
	// An empty page while the count says otherwise is past the end of a shrunken set;
	// the sentence blames the page, not the filters.
	if region.Empty && page.Total > 0 {
		region.EmptyDetail = productTaskPastEndDetailOf(resolved, page.Total)
	}
	if region.Empty {
		// Empty and failed answers have no rows, so no paging footer.
		region.Paging = nil
	} else if view == productTasksView {
		// Tasks view only; the Board has its own paging contract.
		region.Paging = productTaskPagingOf(r, page)
	}
	app.renderProductTaskView(w, r, product, region, resolved, page.Rows, http.StatusOK)
}

// productTaskPagingOf builds the "Showing X of Y" footer from the read's own count
// and token, never from counting rows. Paging is keyset forward-only, so Previous
// renders disabled rather than guessing a page.
func productTaskPagingOf(r *http.Request, page productTaskPage) *pages.ProductTaskPaging {
	paging := &pages.ProductTaskPaging{
		Shown:   len(page.Rows),
		Total:   page.Total,
		Summary: fmt.Sprintf("Showing %d of %d tasks", len(page.Rows), page.Total),
		PrevNote: "This list pages forward only, so there is no previous page to link to. " +
			"Use the browser's Back button, or change a filter to start again from the first page.",
	}
	if page.NextToken != "" {
		paging.NextHref = productTaskPagePath(r, page.NextToken)
		return paging
	}
	paging.NextNote = "This is the last page: every task matching these filters is already shown."
	return paging
}

// productTaskPagePath is this URL with page_token replaced; rebuilding from the
// raw query keeps every filter without having to know their names.
func productTaskPagePath(r *http.Request, token string) string {
	query := r.URL.Query()
	query.Set(productTaskPageTokenParam, token)
	return r.URL.Path + "?" + query.Encode()
}

// productTaskPageRecoveryPath drops a refused token but keeps every filter, since
// changing filters is what made the token stale.
func productTaskPageRecoveryPath(r *http.Request) string {
	query := r.URL.Query()
	query.Del(productTaskPageTokenParam)
	if len(query) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + query.Encode()
}

// productTaskPastEndDetailOf is the empty-state sentence for a page past the end.
func productTaskPastEndDetailOf(scope resolvedProductTaskScope, total int) string {
	return fmt.Sprintf("%s This page is past the last one: the %d task(s) matching these filters are on an earlier page.",
		productTaskEmptyDetailOf(scope), total)
}

// productTaskTokenProblem names a refused continuation token. Each is the caller's
// stale link, never a read failure to blame on the logs.
type productTaskTokenProblem int

const (
	productTaskTokenOK productTaskTokenProblem = iota
	productTaskTokenScope
	productTaskTokenFilter
	// productTaskTokenInvalid is a hand-edited or truncated page_token.
	productTaskTokenInvalid
)

// productTaskTokenProblemOf names which refusal err is, or productTaskTokenOK;
// unrecognised errors fall through to the 500 branch.
func productTaskTokenProblemOf(err error) productTaskTokenProblem {
	switch {
	case errors.Is(err, store.ErrTokenScopeMismatch):
		return productTaskTokenScope
	case errors.Is(err, store.ErrTokenFilterMismatch):
		return productTaskTokenFilter
	case errors.Is(err, store.ErrInvalidContinuationToken):
		return productTaskTokenInvalid
	}
	return productTaskTokenOK
}

// productTaskTokenProblemMessage is each refusal in the operator's words, never
// the store's Go error.
func productTaskTokenProblemMessage(problem productTaskTokenProblem) string {
	switch problem {
	case productTaskTokenScope, productTaskTokenFilter:
		return "This page's page_token was issued for a different scope or set of filters, so it cannot be used here."
	case productTaskTokenInvalid:
		return "This page's page_token is not a valid continuation token."
	}
	return "The page could not be read."
}

// productTaskRowsOf maps the read's rows in the read's own order, which the
// continuation token is bound to. A milepebble's task shows its parent milestone
// and names the milepebble in the next cell.
func productTaskRowsOf(productID uuid.UUID, rows []store.ProductTaskRow, now time.Time) []pages.ProductTaskRow {
	out := make([]pages.ProductTaskRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, pages.ProductTaskRow{
			ID:         row.TaskID.String(),
			Title:      row.Title,
			DetailPath: productTaskDetailPath(productID, row.TaskID),
			Milestone:  row.Milestone.Name,
			Lane:       string(row.CurrentLane),
			Attempts:   taskAttemptsOf(row.AttemptCount, row.AttemptCap),
			Badges:     productTaskBadgesOf(row, now),
		})
		if row.Milepebble != nil {
			out[len(out)-1].Milepebble = row.Milepebble.Name
		}
		// Set together or not at all: both name the same open claim.
		if row.ClaimID != nil && row.LeaseExpiresAt != nil {
			out[len(out)-1].ClaimID = row.ClaimID.String()
			out[len(out)-1].LeaseExpiresAt = row.LeaseExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// productTaskBadgesOf derives row badges from the product row's TaskState; it
// cannot reuse taskStateBadges, which expects an escalation id.
func productTaskBadgesOf(row store.ProductTaskRow, now time.Time) []pages.TaskBadge {
	var badges []pages.TaskBadge
	// A claim with no reported expiry cannot be judged live or lapsed, so no badge.
	if row.ClaimID != nil && row.LeaseExpiresAt != nil {
		if !row.LeaseExpiresAt.After(now) {
			badges = append(badges, pages.TaskBadge{Key: "lease-expired", Label: "Lease expired"})
		} else {
			badges = append(badges, pages.TaskBadge{Key: "claimed", Label: "Claimed"})
		}
	}
	if row.AttemptCount >= row.AttemptCap {
		badges = append(badges, pages.TaskBadge{Key: "capped", Label: "Capped"})
	}
	if row.State == store.TaskStateEscalated && row.CancelledAt == nil {
		badges = append(badges, pages.TaskBadge{Key: "escalated", Label: "Escalated"})
	}
	if row.CancelledAt != nil {
		badges = append(badges, pages.TaskBadge{Key: "cancelled", Label: "Cancelled"})
	}
	return badges
}

// productTaskRegionOf builds the region's view model from the resolved scope alone,
// so its self-description matches the read.
func productTaskRegionOf(r *http.Request, product store.Product, view string, scope resolvedProductTaskScope) pages.ProductTaskRegion {
	region := pages.ProductTaskRegion{
		Product:        productHeaderOf(product),
		View:           view,
		Path:           r.URL.Path,
		RefreshPath:    r.URL.RequestURI(),
		MilestonesPath: productTaskMilestonesPath(product.ID, scope),
		ScopeLabel:     productTaskScopeLabelOf(scope),
		OnlyStuck:      scope.Parsed.OnlyStuck,
		Scope:          productTaskScopeControlOf(r, scope, view == productTasksView, view),
		EmptyDetail:    productTaskEmptyDetailOf(scope),
	}
	if scope.Parsed.Lane != nil {
		region.Lane = string(*scope.Parsed.Lane)
	}
	return region
}

// productTaskMilestonesPath links back to the resolved container's detail, or to
// the Milestones table for the product-wide scope.
func productTaskMilestonesPath(productID uuid.UUID, scope resolvedProductTaskScope) string {
	if scope.Container.ID != uuid.Nil {
		return milestoneDetailHref(productID, scope.Container.ID)
	}
	return productHref(productID, milestonesSuffix)
}

// productTaskEmptyDetailOf names the scope and active filters, so an operator can
// tell "no work" from "a filter is hiding work".
func productTaskEmptyDetailOf(scope resolvedProductTaskScope) string {
	filters := make([]string, 0, 2)
	if scope.Parsed.Lane != nil {
		filters = append(filters, "lane "+string(*scope.Parsed.Lane))
	}
	if scope.Parsed.OnlyStuck {
		filters = append(filters, "only stuck")
	}
	scopeLabel := productTaskScopeLabelOf(scope)
	if len(filters) == 0 {
		return "No task in " + scopeLabel + " matches this scope."
	}
	return "No task in " + scopeLabel + " matches " + strings.Join(filters, " and ") + "."
}

// productTaskScopeControlOf builds the scope control from the resolved scope.
// Milepebble mode shows two selects, but only the milepebble submits container_id.
// The Board passes showFilters=false and carries filters as hidden fields.
func productTaskScopeControlOf(r *http.Request, scope resolvedProductTaskScope, showFilters bool, view string) pages.ProductTaskScopeControl {
	kind := scope.Parsed.Kind
	path := r.URL.Path
	control := pages.ProductTaskScopeControl{
		Path:           path,
		Modes:          productTaskScopeModes(kind),
		MilestoneParam: pages.ProductTaskMilestoneQueryParam,
		Lane:           productTaskLaneOf(scope),
		OnlyStuck:      scope.Parsed.OnlyStuck,
		ShowFilters:    showFilters,
		// Built from the raw query so every parameter reaches the sibling view.
		Views: productTaskViewToggleOf(path, productTaskViewQueryOf(r), view),
	}
	if showFilters {
		control.Lanes = markSelectedLane(productTaskLaneOptions(), productTaskLaneOf(scope))
	}
	if kind == store.ProductTaskScopeMilestone {
		control.MilestoneParam = pages.ProductTaskContainerQueryParam
	}
	if kind == store.ProductTaskScopeIncomplete {
		// The product-wide mode names no container, so it has no select.
		return control
	}

	control.Milestones = productTaskMilestoneOptionsOf(scope)
	control.ShowMilestone = len(control.Milestones) > 0
	if kind != store.ProductTaskScopeMilepebble {
		return control
	}

	control.Milepebbles = productTaskMilepebbleOptionsOf(scope)
	control.ShowMilepebble = len(control.Milepebbles) > 0
	return control
}

// productTaskViewToggleOf is the List/Board switch, carrying the full query to the
// sibling view except page_token: the views page differently, so the destination
// starts at its own first page.
func productTaskViewToggleOf(path string, query url.Values, view string) pages.ProductTaskViewToggle {
	suffix := ""
	if len(query) > 0 {
		suffix = "?" + query.Encode()
	}
	return pages.ProductTaskViewToggle{
		Views: []pages.ProductTaskViewLink{
			{
				Label:  "List",
				Href:   productTaskViewPath(path, tasksSuffix) + suffix,
				Active: view == productTasksView,
			},
			{
				Label:  "Board",
				Href:   productTaskViewPath(path, boardSuffix) + suffix,
				Active: view == productBoardView,
			},
		},
	}
}

// productTaskViewPath swaps the trailing view segment; any other path is returned
// unchanged rather than linking to an unserved URL.
func productTaskViewPath(path, siblingSuffix string) string {
	switch {
	case strings.HasSuffix(path, tasksSuffix):
		return strings.TrimSuffix(path, tasksSuffix) + siblingSuffix
	case strings.HasSuffix(path, boardSuffix):
		return strings.TrimSuffix(path, boardSuffix) + siblingSuffix
	}
	return path
}

// productTaskViewQueryOf is the request query minus page_token. r.URL.Query()
// returns a copy, so the request's own links keep their token.
func productTaskViewQueryOf(r *http.Request) url.Values {
	query := r.URL.Query()
	query.Del(productTaskPageTokenParam)
	return query
}

// productTaskMilestoneOptionsOf marks the resolved milestone; in milepebble mode
// that is the milepebble's parent.
func productTaskMilestoneOptionsOf(scope resolvedProductTaskScope) []pages.ProductTaskContainerOption {
	out := make([]pages.ProductTaskContainerOption, 0, len(scope.Milestones))
	for _, m := range scope.Milestones {
		out = append(out, pages.ProductTaskContainerOption{
			ID:               m.ID.String(),
			Name:             m.Name,
			Selected:         m.ID == scope.Milestone.ID,
			OutOfScopeSuffix: outOfScopeSuffix(m.Status),
		})
	}
	return out
}

// productTaskMilepebbleOptionsOf lists the resolved milestone's children; the
// resolved container is itself a milepebble in this mode, with none of its own.
func productTaskMilepebbleOptionsOf(scope resolvedProductTaskScope) []pages.ProductTaskContainerOption {
	out := make([]pages.ProductTaskContainerOption, 0, len(scope.Milestone.Milepebbles))
	for _, mp := range scope.Milestone.Milepebbles {
		out = append(out, pages.ProductTaskContainerOption{
			ID:               mp.ID.String(),
			Name:             mp.Name,
			Selected:         mp.ID == scope.Store.ContainerID,
			OutOfScopeSuffix: outOfScopeSuffix(mp.Status),
		})
	}
	return out
}

// productTaskNoMilepebblesLabel names the chosen milestone when it has no cuts.
func productTaskNoMilepebblesLabel(scope resolvedProductTaskScope) string {
	if scope.Milestone.Name == "" {
		return "No milepebbles here"
	}
	return "No milepebbles under " + scope.Milestone.Name
}

// outOfScopeSuffix marks a container the all-incomplete scope would exclude, using
// the store's own predicate. It stays selectable explicitly.
func outOfScopeSuffix(status store.MilestoneStatus) string {
	if store.IsIncompleteContainerStatus(status) {
		return ""
	}
	return " (outside the all-incomplete scope)"
}

func productTaskScopeLabelOf(scope resolvedProductTaskScope) string {
	if scope.Parsed.Kind == store.ProductTaskScopeIncomplete {
		return "All incomplete milestones"
	}
	return scope.Container.Name
}

// productTaskScopeModes is the three modes in control order; an empty active means
// the product-wide default.
func productTaskScopeModes(active store.ProductTaskScopeKind) []pages.ProductTaskScopeLabel {
	if active == "" {
		active = store.ProductTaskScopeIncomplete
	}
	return []pages.ProductTaskScopeLabel{
		{Mode: string(store.ProductTaskScopeIncomplete), Label: "All incomplete milestones", Active: active == store.ProductTaskScopeIncomplete},
		{Mode: string(store.ProductTaskScopeMilestone), Label: "Milestone", Active: active == store.ProductTaskScopeMilestone},
		{Mode: string(store.ProductTaskScopeMilepebble), Label: "Milepebble", Active: active == store.ProductTaskScopeMilepebble},
	}
}

// productTaskLaneOf is the lane filter's name, empty for every lane.
func productTaskLaneOf(scope resolvedProductTaskScope) string {
	if scope.Parsed.Lane == nil {
		return ""
	}
	return string(*scope.Parsed.Lane)
}

// productTaskLaneOptions is "Any lane" (empty value, parsed as no lane) followed by
// the store's canonical lanes, so new lanes appear without a UI edit.
func productTaskLaneOptions() []pages.ProductTaskLaneOption {
	out := make([]pages.ProductTaskLaneOption, 0, len(store.CanonicalLaneOrder)+1)
	out = append(out, pages.ProductTaskLaneOption{Value: "", Label: "Any lane", Selected: true})
	for _, lane := range store.CanonicalLaneOrder {
		out = append(out, pages.ProductTaskLaneOption{Value: string(lane), Label: string(lane)})
	}
	return out
}

// markSelectedLane marks the URL's lane so the select reflects the current filter.
func markSelectedLane(options []pages.ProductTaskLaneOption, selected string) []pages.ProductTaskLaneOption {
	for i := range options {
		options[i].Selected = options[i].Value == selected
	}
	return options
}

// renderProductTaskView is the one place choosing between the Tasks and Board
// bodies. rows is nil for scope problems that render as ordinary empty answers.
func (app *App) renderProductTaskView(w http.ResponseWriter, r *http.Request, product store.Product, region pages.ProductTaskRegion, resolved resolvedProductTaskScope, rows []store.ProductTaskRow, status int) {
	if region.View != productBoardView {
		app.renderProductTaskRegion(w, r, region.View, region, status)
		return
	}

	// Progress is read only when there are rows and no earlier failure.
	var progress store.ProductTaskProgress
	var progressErr error
	if len(rows) > 0 && region.Error == "" {
		progress, progressErr = app.boardProgressRead(r.Context(), product.ID, resolved)
		if progressErr != nil {
			logger.Error("board progress read failed", "product", product.ID.String(), "error", progressErr)
		}
	}
	board := productBoardPageOf(product, region, resolved, rows, progress, progressErr,
		boardTasksPath(product.ID, r.URL.Query()), time.Now())
	if region.Error != "" {
		board.Error = region.Error
	}

	body := pages.ProductBoard(board)
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	app.renderShellStatus(w, r, region.View, r.URL.Path, body, status)
}

// renderProductTaskRegion writes the Tasks region, as a fragment for htmx or inside
// the shell; each view is its own nav item.
func (app *App) renderProductTaskRegion(w http.ResponseWriter, r *http.Request, view string, region pages.ProductTaskRegion, status int) {
	body := pages.ProductTasks(region)
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	app.renderShellStatus(w, r, view, r.URL.Path, body, status)
}

// renderProductTaskScopeProblem maps a scope problem to a status page or inline
// htmx message. An out-of-product container is a 404; "no containers" and "no
// milepebbles" are ordinary empty answers.
func (app *App) renderProductTaskScopeProblem(w http.ResponseWriter, r *http.Request, product store.Product, view string, resolved resolvedProductTaskScope, problem productTaskScopeProblem) {
	if problem == productTaskScopeNoContainers {
		// Modes and filters stay offered: the control is the operator's way out.
		app.renderProductTaskView(w, r, product, pages.ProductTaskRegion{
			Product:     productHeaderOf(product),
			View:        view,
			Path:        r.URL.Path,
			ScopeLabel:  "No milestones yet",
			Empty:       true,
			EmptyDetail: "This product has no milestone or milepebble to scope to yet.",
			Scope:       productTaskScopeControlOf(r, resolved, view == productTasksView, view),
		}, resolved, nil, http.StatusOK)
		return
	}

	if problem == productTaskScopeNoMilepebbles {
		// Keep the milestone select: it is how the operator picks a milestone with cuts.
		app.renderProductTaskView(w, r, product, pages.ProductTaskRegion{
			Product:     productHeaderOf(product),
			View:        view,
			Path:        r.URL.Path,
			ScopeLabel:  productTaskNoMilepebblesLabel(resolved),
			Empty:       true,
			Lane:        productTaskLaneOf(resolved),
			OnlyStuck:   resolved.Parsed.OnlyStuck,
			Scope:       productTaskScopeControlOf(r, resolved, view == productTasksView, view),
			EmptyDetail: productTaskEmptyDetailOf(resolved),
		}, resolved, nil, http.StatusOK)
		return
	}

	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.ProductTasksInlineError(pages.ProductTasksAnchor,
			productTaskScopeProblemMessage(problem)))
		return
	}

	status := http.StatusBadRequest
	if problem == productTaskScopeNotFound {
		status = http.StatusNotFound
	}
	if problem == productTaskScopeUnreadable {
		status = http.StatusInternalServerError
	}
	app.renderShellStatus(w, r, view, r.URL.Path, pages.SpecStatus(pages.StatusPage{
		Title:    view + " scope",
		Detail:   productTaskScopeProblemMessage(problem),
		BackHref: productHref(product.ID, overviewSuffix),
		BackText: "Back to overview",
	}), status)
}

// productTaskScopeProblemMessage is each named problem in the operator's
// words -- never the store's Go error string, which names an internal
// package and tells the operator nothing they can act on.
func productTaskScopeProblemMessage(problem productTaskScopeProblem) string {
	switch problem {
	case productTaskScopeInvalid:
		return "That scope or filter is not one this page understands."
	case productTaskScopeNotFound:
		return "No milestone or milepebble with that id belongs to this product."
	case productTaskScopeUnreadable:
		return "The product's milestones could not be read. See the logs."
	}
	return "The scope could not be resolved."
}
