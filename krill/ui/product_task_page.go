// The two product-scoped routes the product-wide task read serves:
// GET /products/{pid}/tasks and GET /products/{pid}/board. Both are two
// views of one scope (FR ab5f4936), so both go through the same handler
// with the view's own name.
//
// What they render today is the scope the URL resolved to and the total
// behind it, not rows: the read layer they sit on is the whole of what has
// shipped, and the region's own template says so rather than rendering a
// table with nothing in it.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// The two views' own names. They are the region's heading and its nav key
// as well as the choice between two templates, so they are named once here
// rather than spelled at each call site.
//
// productTasksView is also the one that offers the lane and only-stuck
// controls (FR 61d7fb7b); the Board reuses the same parsed scope without
// them.
const (
	productTasksView = "Tasks"
	productBoardView = "Board"
)

// handleProductTasks serves the product-wide Tasks region.
func (app *App) handleProductTasks(w http.ResponseWriter, r *http.Request) {
	app.serveProductTaskRegion(w, r, productTasksView)
}

// handleProductBoard serves the Board view of the same scope.
func (app *App) handleProductBoard(w http.ResponseWriter, r *http.Request) {
	app.serveProductTaskRegion(w, r, productBoardView)
}

// serveProductTaskRegion is the one handler behind both views: resolve the
// product, parse and resolve the scope, read the page and its total, and
// render the region -- or the named failure that scope resolved to.
//
// Every failure has a defined answer here rather than at each call site,
// and each one differs by request mode. A full-page request gets the
// in-shell status page the scope deserves; an htmx request gets 200 with
// the same message inline, because htmx does not swap on a non-2xx and a
// status-coded body would leave the operator clicking Refresh with nothing
// to read (spec_page.go's renderSpecError rule, which handleTaskList
// already follows).
func (app *App) serveProductTaskRegion(w http.ResponseWriter, r *http.Request, view string) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	// The last-viewed cookie is recorded only for a real page view. An
	// htmx request is a swap inside a page the operator is already on, and
	// a fragment response has no business moving where their next
	// un-prefixed page lands -- especially since a swap fires on every
	// control change, so it would rewrite the cookie repeatedly for a
	// navigation the operator never made.
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
	// The read instant is taken once, here, and is the only clock the
	// region consults: a lease judged against a second, later time.Now()
	// would let two rows of one page disagree about whether the same
	// instant is in the past.
	readAt := time.Now()
	page, err := app.readProductTasks(r.Context(), product.ID, resolved)
	if err != nil {
		// A continuation token the store refused is the caller's own stale
		// link, not a broken read, so it is answered BEFORE the read-failure
		// branch below can call it one. Both refusals -- another scope and
		// another filter set -- reach here as themselves rather than as an
		// empty or wrong-scope page, which is the outcome FR 7bff09fe rules
		// out: a rejected token answered with rows would be rows this URL
		// did not ask for.
		if problem := productTaskTokenProblemOf(err); problem != productTaskTokenOK {
			region.PageError = productTaskTokenProblemMessage(problem)
			region.PageRecoveryHref = productTaskPageRecoveryPath(r)
			region.PageRecoveryText = "Back to the first page"
			// A stale link is the caller's, so 400 -- the same status the
			// ops console's own token refusals use (ops.go's
			// writeOpsQueryError). An htmx request still gets 200, for the
			// reason it does everywhere else here: htmx does not swap on a
			// non-2xx, so a 400 fragment would leave the operator looking
			// at an unchanged table.
			status := http.StatusBadRequest
			if r.Header.Get("HX-Request") != "" {
				status = http.StatusOK
			}
			app.renderProductTaskRegion(w, r, view, region, status)
			return
		}
		// A failed read is a 500 the operator can act on, and an empty
		// region would read as "this scope has no tasks" -- the one answer
		// it must never be mistaken for.
		logger.Error("product task read failed", "product", product.ID.String(), "error", err)
		region.Error = "The tasks could not be read. See the logs."
		app.renderProductTaskView(w, r, product, region, resolved, nil, http.StatusInternalServerError)
		return
	}
	region.Total = page.Total
	region.Rows = productTaskRowsOf(product.ID, page.Rows, readAt)
	region.UpdatedAt = readAt.UTC().Format(time.RFC3339)
	region.Empty = len(page.Rows) == 0
	// A page that came back empty while the count for the SAME filters says
	// otherwise is not a scope with no work: it is a position past the end
	// of a set that has since shrunk, reached by a token that was valid
	// when the operator followed it. Blaming the filters there would point
	// them at a control that is not the problem, so the sentence names the
	// page -- and still names the scope and filters, the way every other
	// empty state does.
	if region.Empty && page.Total > 0 {
		region.EmptyDetail = productTaskPastEndDetailOf(resolved, page.Total)
	}
	if region.Empty {
		// The footer belongs to the table and only to the table: an empty
		// answer and a failure have no rows to truncate, and a "Showing 0
		// of 0" under either would be a count dressed as a page.
		region.Paging = nil
	} else if view == productTasksView {
		// The Tasks view only. The Board is a different view of the same
		// scope with its own paging requirement (FR cf000440), and giving
		// it this footer's Next/Previous -- links into a board that renders
		// no lanes yet -- would be answering for it here.
		region.Paging = productTaskPagingOf(r, page)
	}
	app.renderProductTaskView(w, r, product, region, resolved, page.Rows, http.StatusOK)
}

// productTaskPagingOf is the table footer's state for one page: the
// explicit "Showing X of Y tasks" and the one control that moves on.
//
// Both numbers come from the read rather than from the rows. Y is the
// count the store answered for the identical parameters the rows were read
// with, so the footer cannot describe a different filter set than the table
// above it; X is the length of the page the store actually returned. A
// footer that derived Y by counting what it had would read "Showing 25 of
// 25" on the first of four pages -- the silently truncated table FR
// 7bff09fe exists to rule out.
//
// NextHref is present exactly when the store issued a token, which it does
// exactly when a row remained beyond this page -- so "more rows remain" is
// the store's own answer, not an inference from comparing X against Y.
//
// PrevHref is always empty. The store's keyset paging is forward-only: a
// token names a position to resume AFTER, and nothing in this layer can
// name the position before the one being shown. So Previous is rendered
// disabled rather than pointed at a guessed page -- guessing would land the
// operator on a page they did not ask for, which is the failure this whole
// task exists to prevent. The disabled control's title says so, and the
// browser's own Back button remains the way back.
//
// The links are rebuilt from the request's own query with only the token
// replaced, which is what keeps every active filter -- scope mode,
// container, lane, only-stuck and page size -- in the URL of the page they
// move to.
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

// productTaskPagePath is this view's own URL at a given page position: the
// request's query with page_token replaced and every other parameter left
// exactly as it arrived.
//
// Rebuilding from r.URL.Query() rather than re-deriving the filters from the
// resolved scope is deliberate. A re-derivation would have to know which of
// scope/container_id/milestone/lane/only_stuck/page_size the URL carried,
// and a parameter it forgot to carry forward would silently drop a filter
// the operator set -- the exact drift the scope control's one-form shape
// exists to prevent.
func productTaskPagePath(r *http.Request, token string) string {
	query := r.URL.Query()
	query.Set(productTaskPageTokenParam, token)
	return r.URL.Path + "?" + query.Encode()
}

// productTaskPageRecoveryPath is the way back out of a refused
// continuation token: this view at this scope and these filters, with the
// token dropped.
//
// It keeps every filter rather than resetting to the product-wide default.
// A token goes stale when the scope or the filters CHANGE -- which is
// exactly what the operator did to get here -- so dropping their filters on
// the way out would discard the thing that caused the refusal and land them
// on a different answer than the one they were reading.
func productTaskPageRecoveryPath(r *http.Request) string {
	query := r.URL.Query()
	query.Del(productTaskPageTokenParam)
	if len(query) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + query.Encode()
}

// productTaskPastEndDetailOf is the empty state's sentence for a page past
// the end of its set: the filters named as usual, plus the fact that the
// PAGE -- not the scope -- is what came back with nothing.
func productTaskPastEndDetailOf(scope resolvedProductTaskScope, total int) string {
	return fmt.Sprintf("%s This page is past the last one: the %d task(s) matching these filters are on an earlier page.",
		productTaskEmptyDetailOf(scope), total)
}

// productTaskTokenProblem is a continuation token the store refused, named
// rather than string-matched at the call site.
//
// It is its own set because every member is the caller's stale link and none
// of them is a read failure: answering any of them with "see the logs"
// would send an operator hunting a database problem they do not have.
type productTaskTokenProblem int

const (
	// productTaskTokenOK is the absence of a token refusal.
	productTaskTokenOK productTaskTokenProblem = iota
	// productTaskTokenScope is a token issued for another scope.
	productTaskTokenScope
	// productTaskTokenFilter is a token issued for a different filter set.
	productTaskTokenFilter
	// productTaskTokenInvalid is a token that is not a token at all -- a
	// hand-edited or truncated page_token. It earns its own name rather
	// than being lumped in with the two mismatches because it says
	// something different to an operator debugging a shared link, and it
	// is grouped HERE rather than left to the read-failure branch because
	// that branch blames the logs for something the URL did.
	productTaskTokenInvalid
)

// productTaskTokenProblemOf names which refusal err is, or
// productTaskTokenOK for a read failure that has nothing to do with
// paging.
//
// errors.Is rather than a string match, and no default case claiming an
// arbitrary error: an unrecognized failure is the read's own problem and
// belongs in the 500 branch, never here.
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

// productTaskTokenProblemMessage is each refusal in the operator's words.
// Never the store's own Go error, which names an internal package and tells
// an operator nothing they can act on.
func productTaskTokenProblemMessage(problem productTaskTokenProblem) string {
	switch problem {
	case productTaskTokenScope, productTaskTokenFilter:
		return "This page's page_token was issued for a different scope or set of filters, so it cannot be used here."
	case productTaskTokenInvalid:
		return "This page's page_token is not a valid continuation token."
	}
	return "The page could not be read."
}

// productTaskRowsOf is the read's page as the table's rows.
//
// The order is the read's own, passed straight through: the store's sort is
// what a continuation token is bound to, so re-ordering here would leave the
// next page starting from a row this page did not end on. A milepebble's
// task keeps its PARENT milestone in the Milestone cell and names its own
// in the next one -- which is the aggregation FR f41a352d asks for: a cut
// milestone's tasks appear under the milestone, each naming the milepebble
// they came from, rather than behind a link the operator has to follow.
//
// now is the read instant, so a lapsed lease reads as lapsed against the
// same moment every other row on the page was judged by.
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
		// The two are set together or not at all: store.ProductTaskRow
		// guarantees ClaimID and LeaseExpiresAt name the same open claim, and
		// a row carrying one without the other would claim a lease it does
		// not have -- or hide a claim it does.
		if row.ClaimID != nil && row.LeaseExpiresAt != nil {
			out[len(out)-1].ClaimID = row.ClaimID.String()
			out[len(out)-1].LeaseExpiresAt = row.LeaseExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// productTaskBadgesOf is the same state derivation the per-container list
// makes, over the product read's own row. It is a separate function rather
// than a converted TaskSummary because the product row reports its state
// differently -- as the derived TaskState, not as an escalation id -- and
// wrapping it to reuse taskStateBadges would mean inventing the uuid that
// derivation tests for.
func productTaskBadgesOf(row store.ProductTaskRow, now time.Time) []pages.TaskBadge {
	var badges []pages.TaskBadge
	// Both halves, or neither -- the same rule the row's data attributes
	// use, and for the same reason: a claim whose expiry the read did not
	// report cannot be judged live or lapsed, so calling it "Claimed" would
	// contradict the claim id the row does or does not carry.
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
	if row.State == store.TaskStateEscalated {
		badges = append(badges, pages.TaskBadge{Key: "escalated", Label: "Escalated"})
	}
	if row.CancelledAt != nil {
		badges = append(badges, pages.TaskBadge{Key: "cancelled", Label: "Cancelled"})
	}
	return badges
}

// productTaskRegionOf assembles the region's view model from the resolved
// scope alone -- no rows -- so what the page says about itself cannot
// disagree with the scope the read was built from.
func productTaskRegionOf(r *http.Request, product store.Product, view string, scope resolvedProductTaskScope) pages.ProductTaskRegion {
	region := pages.ProductTaskRegion{
		Product:     productHeaderOf(product),
		View:        view,
		Path:        r.URL.Path,
		RefreshPath: r.URL.RequestURI(),
		ScopeLabel:  productTaskScopeLabelOf(scope),
		OnlyStuck:   scope.Parsed.OnlyStuck,
		Scope:       productTaskScopeControlOf(r.URL.Path, scope, view == productTasksView),
		// The empty state's sentence is built here, from the same scope, so
		// it names the filters the read was actually built from rather than
		// a second description of them that could drift.
		EmptyDetail: productTaskEmptyDetailOf(scope),
	}
	if scope.Parsed.Lane != nil {
		region.Lane = string(*scope.Parsed.Lane)
	}
	return region
}

// productTaskEmptyDetailOf is the sentence the empty state shows under its
// title, naming the scope and every active filter.
//
// It is the third of the three states that must never be confused, and the
// one that most easily is: a bare "no tasks" cannot tell an operator whether
// the scope holds no work or a filter they set is hiding work that IS there.
// Naming the lane and the only-stuck flag is what tells them which control
// to change, so the sentence is assembled from the parsed scope -- the same
// value the read was given.
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

// productTaskScopeControlOf builds the control both views render, from the
// resolved scope -- so the marked mode, the selected option and the read
// the rows come from are one answer rather than three.
//
// The two single-container modes reveal different pairs of selects, and
// the difference is deliberate rather than incidental:
//
//   - Milestone mode names one container, so its milestone select submits
//     container_id -- the same parameter the api and the store read.
//   - Milepebble mode names two: the milestone whose milepebbles are on
//     offer, and the chosen milepebble itself. Only the second is the
//     read's container, so only the second is container_id.
//
// Either way the operator chooses from a select of the product's own
// containers; nothing here is a field to type an id into.
//
// showFilters is the Tasks view's own pair of filters (FR 61d7fb7b). The
// Board passes false and keeps carrying them as hidden fields instead: it
// reads the same parsed scope but offers no control that changes it, so a
// scope change there must still not drop a filter the operator set on the
// Tasks page.
func productTaskScopeControlOf(path string, scope resolvedProductTaskScope, showFilters bool) pages.ProductTaskScopeControl {
	kind := scope.Parsed.Kind
	control := pages.ProductTaskScopeControl{
		Path:           path,
		Modes:          productTaskScopeModes(kind),
		MilestoneParam: pages.ProductTaskMilestoneQueryParam,
		Lane:           productTaskLaneOf(scope),
		OnlyStuck:      scope.Parsed.OnlyStuck,
		ShowFilters:    showFilters,
	}
	if showFilters {
		control.Lanes = markSelectedLane(productTaskLaneOptions(), productTaskLaneOf(scope))
	}
	if kind == store.ProductTaskScopeMilestone {
		control.MilestoneParam = pages.ProductTaskContainerQueryParam
	}
	if kind == store.ProductTaskScopeIncomplete {
		// The product-wide mode names no container, so it reveals no
		// select. The listing is not read here either, so there is nothing
		// to offer -- which is why this branch cannot be reached with
		// options attached.
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

// productTaskMilestoneOptionsOf is the milestone select's options, marking
// the one this request resolved to. In milepebble mode the marked one is
// the resolved milepebble's PARENT -- the milestone whose milepebbles the
// second select offers -- rather than a container this scope does not
// name, so the control shows where the choices in the next select come
// from.
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

// productTaskMilepebbleOptionsOf is the milepebble select's options: the
// selected milestone's own children, marking the resolved one. Sourced
// from the resolved milestone rather than from scope.Container's
// Milepebbles, because the resolved container IS a milepebble in this mode
// and carries no children of its own.
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

// productTaskNoMilepebblesLabel is the scope in prose for a milepebble
// scope whose milestone has nothing cut under it. It names the milestone
// the operator chose rather than the mode, because the mode's own claim --
// "one milepebble" -- is what could not be honoured.
func productTaskNoMilepebblesLabel(scope resolvedProductTaskScope) string {
	if scope.Milestone.Name == "" {
		return "No milepebbles here"
	}
	return "No milepebbles under " + scope.Milestone.Name
}

// outOfScopeSuffix marks a container the product-wide all-incomplete scope
// would not have included, judged by the store's own predicate rather than
// a UI copy of it. FR 7191dba1 still lets the operator pick such a
// container explicitly -- it shows its tasks whatever its status -- so it
// stays in the list; saying so is what keeps it from reading as a
// contradiction. Empty for a container that scope would include.
func outOfScopeSuffix(status store.MilestoneStatus) string {
	if store.IsIncompleteContainerStatus(status) {
		return ""
	}
	return " (outside the all-incomplete scope)"
}

// productTaskScopeLabelOf is the scope in prose: the product-wide mode's
// own name, or the resolved container's.
func productTaskScopeLabelOf(scope resolvedProductTaskScope) string {
	if scope.Parsed.Kind == store.ProductTaskScopeIncomplete {
		return "All incomplete milestones"
	}
	return scope.Container.Name
}

// productTaskScopeModes is the three modes in the order the control offers
// them, with active marked. active is uuid.Nil's absence expressed as the
// product-wide default, so a caller that has no resolved scope still gets
// the same three names rather than a second list.
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

// productTaskLaneOf is the lane filter's name for the control to carry
// through, empty for "every lane".
func productTaskLaneOf(scope resolvedProductTaskScope) string {
	if scope.Parsed.Lane == nil {
		return ""
	}
	return string(*scope.Parsed.Lane)
}

// productTaskLaneOptions is the lane select's options: "Any lane" first,
// then the store's five canonical lanes in store.CanonicalLaneOrder's own
// order.
//
// The lane set is the STORE's, not a UI copy of it: a lane the store adds
// appears here without a second edit, and the "Any lane" option submits an
// empty value, which parseProductTaskScope reads as no lane -- the same
// absence a bare URL means, so the control never produces a filter the
// parser would refuse.
func productTaskLaneOptions() []pages.ProductTaskLaneOption {
	out := make([]pages.ProductTaskLaneOption, 0, len(store.CanonicalLaneOrder)+1)
	out = append(out, pages.ProductTaskLaneOption{Value: "", Label: "Any lane", Selected: true})
	for _, lane := range store.CanonicalLaneOrder {
		out = append(out, pages.ProductTaskLaneOption{Value: string(lane), Label: string(lane)})
	}
	return out
}

// markSelectedLane is productTaskLaneOptions with the request's own lane
// marked, so the select shows what the URL currently names rather than
// always opening on "Any lane" while the URL filters to Testing.
func markSelectedLane(options []pages.ProductTaskLaneOption, selected string) []pages.ProductTaskLaneOption {
	for i := range options {
		options[i].Selected = options[i].Value == selected
	}
	return options
}

// renderProductTaskView writes whichever of the two views the URL named:
// the Tasks table's rows, or the Board's swimlanes. rows is nil for the
// scope problems that are ordinary empty answers, which render as the
// empty state in whichever view was asked for.
//
// The two views share one scope, one control and one anchor, and this is
// the only place that chooses between their bodies -- so a URL can never
// serve the Tasks view under the Board path or the other way round. The
// region's own EmptyDetail travels on the region, so a read-driven empty
// Tasks state still names the filters the read was built from.
func (app *App) renderProductTaskView(w http.ResponseWriter, r *http.Request, product store.Product, region pages.ProductTaskRegion, resolved resolvedProductTaskScope, rows []store.ProductTaskRow, status int) {
	if region.View != productBoardView {
		app.renderProductTaskRegion(w, r, region.View, region, status)
		return
	}

	// The progress read is only asked for once there are rows to hang it
	// on. An empty scope has nothing for it to describe, and a scope whose
	// tasks could not be read has already said so -- asking again would
	// only risk replacing one failure sentence with another.
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

// renderProductTaskRegion writes the Tasks region, as a bare fragment for
// an htmx request and inside the shell otherwise. The nav key is the
// request's own path, which is the Tasks or Board item's own URL -- the
// two views are separate nav items, so each marks itself.
func (app *App) renderProductTaskRegion(w http.ResponseWriter, r *http.Request, view string, region pages.ProductTaskRegion, status int) {
	body := pages.ProductTasks(region)
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	app.renderShellStatus(w, r, view, r.URL.Path, body, status)
}

// renderProductTaskScopeProblem maps a named scope problem onto the two
// answers a caller can receive: the in-shell status page for a browser, and
// the same sentence inline for htmx.
//
// A container outside the product is a 404, never another product's rows
// and never an empty page that would read as "this milestone has no work".
// The two empty outcomes are not failures at all -- they are ordinary
// answers, so each renders the region with nothing in it rather than an
// error. They are kept apart because their controls differ: a product with
// no milestones has nothing to put in a select, while a product whose
// milestones are simply not cut still has a milestone select, and that
// select is how the operator picks a cut one.
func (app *App) renderProductTaskScopeProblem(w http.ResponseWriter, r *http.Request, product store.Product, view string, resolved resolvedProductTaskScope, problem productTaskScopeProblem) {
	if problem == productTaskScopeNoContainers {
		// The modes are still offered even with nothing to scope to: the
		// operator's way out of this page is the control, not a back link,
		// so a control that disappeared here would be the one case where
		// they cannot leave. The filters come with them for the same reason
		// -- a lane the operator set is still set once they pick a mode that
		// has a container to apply it to.
		app.renderProductTaskView(w, r, product, pages.ProductTaskRegion{
			Product:     productHeaderOf(product),
			View:        view,
			Path:        r.URL.Path,
			ScopeLabel:  "No milestones yet",
			Empty:       true,
			EmptyDetail: "This product has no milestone or milepebble to scope to yet.",
			Scope:       productTaskScopeControlOf(r.URL.Path, resolved, view == productTasksView),
		}, resolved, nil, http.StatusOK)
		return
	}

	if problem == productTaskScopeNoMilepebbles {
		// Milepebble mode over a milestone with nothing cut under it. The
		// product does have milestones, and the milestone select -- which
		// is how the operator picks a cut one -- is exactly what must not
		// disappear here. So the control is built from the resolved scope,
		// which carries the product's milestones and the one named.
		app.renderProductTaskView(w, r, product, pages.ProductTaskRegion{
			Product:    productHeaderOf(product),
			View:       view,
			Path:       r.URL.Path,
			ScopeLabel: productTaskNoMilepebblesLabel(resolved),
			Empty:      true,
			Lane:       productTaskLaneOf(resolved),
			OnlyStuck:  resolved.Parsed.OnlyStuck,
			Scope:      productTaskScopeControlOf(r.URL.Path, resolved, view == productTasksView),
			// An ordinary empty answer, so it names its filters the same way
			// the read-driven empty state does -- a lane the operator set is
			// still the lane their next change has to keep.
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
