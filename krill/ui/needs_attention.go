// The Needs attention page (FR 5fd47f4d): one product-scoped page whose
// tabs are the ops console's four queues -- Escalated, Claimed, Cancelled
// and Open notes -- each labelled with its count, the selected tab carried
// in the URL, and a tab switch swapping the results in place.
//
// The page IS the four queues, not a second rendering of them: each tab is
// one of krill/ui/ops.go's four read views, called with the product
// narrowing this page is scoped to (the current product across all its
// milestones), and rendered by the same krill/ui/pages/ops.templ results
// component the console view renders. A tab click therefore cannot show a
// row set the matching console view would not, and the row shapes stay
// where they are -- the tab-specific shapes are separate requirements
// (772b044b, a149d28f, b22e1d60) and separate tasks.
//
// The Escalated tab has since taken its own shape (772b044b,
// needsAttentionEscalatedResults): its columns are the ones an operator
// scans for a stuck task, and they are not the console view's. Its rows
// still come from the one store read, so "the same rows" holds even where
// "the same table" no longer does.
//
// The four legacy /ops URLs are retired into these tabs (krill/ui/routes.go
// names each one's successor), so a bookmarked /ops/escalated link opens
// the Escalated tab of the resolved product rather than a page of its own.
package main

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// The four tabs, in strip order. They are also the legacy URL's successor
// tabs (legacyNeedsAttentionSuccessor), so one string names both the tab
// and the URL that resolves to it.
const (
	needsAttentionTabEscalated = "escalated"
	needsAttentionTabClaimed   = "claimed"
	needsAttentionTabCancelled = "cancelled"
	needsAttentionTabNotes     = "notes"
)

// needsAttentionTabParam is the query parameter that carries the selected
// tab, so the page an operator shares is the tab they were reading.
const needsAttentionTabParam = "tab"

// needsAttentionTabOrder is the strip's order, which is also the order the
// FR names the tabs in. Escalated is first and is therefore the default:
// it is the queue that means "something is stuck".
var needsAttentionTabOrder = []string{
	needsAttentionTabEscalated,
	needsAttentionTabClaimed,
	needsAttentionTabCancelled,
	needsAttentionTabNotes,
}

// needsAttentionTabLabels is each tab's operator-facing name. "Open notes"
// rather than the URL value "notes", which is the wire spelling.
var needsAttentionTabLabels = map[string]string{
	needsAttentionTabEscalated: "Escalated",
	needsAttentionTabClaimed:   "Claimed",
	needsAttentionTabCancelled: "Cancelled",
	needsAttentionTabNotes:     "Open notes",
}

// needsAttentionTabOf resolves the request's ?tab= to one of the four tabs.
//
// An ABSENT value and an UNRECOGNISED one both resolve to Escalated, the
// same degradation rule the task detail's tabs follow: the tab is
// URL-carried, so a hand-edited or stale link reaches here as readily as a
// copied one, and a value this build does not know must render the page
// rather than 404 or render an empty region. Nothing here errors.
func needsAttentionTabOf(r *http.Request) string {
	tab := r.URL.Query().Get(needsAttentionTabParam)
	for _, known := range needsAttentionTabOrder {
		if tab == known {
			return known
		}
	}
	return needsAttentionTabEscalated
}

// needsAttentionTabHref is one tab's own URL under a product. An empty tab
// is the page's bare address, which is what a successor with no matching
// tab (/ops, the console root) redirects to.
func needsAttentionTabHref(pid uuid.UUID, tab string) string {
	base := productHref(pid, needsAttentionSuffix)
	if tab == "" {
		return base
	}
	return base + "?" + url.Values{needsAttentionTabParam: {tab}}.Encode()
}

// needsAttentionPanelSwap reports whether this htmx request asked for the
// whole tab region rather than the results inside it.
//
// htmx sends the resolved target's id in HX-Target, so the target is the
// request's own statement of which region it is replacing: a tab names the
// region (strip and results travel together), while the claimed tab's poll
// and the Refresh button name the results block alone.
func needsAttentionPanelSwap(r *http.Request) bool {
	return r.Header.Get("HX-Target") == pages.NeedsAttentionAnchor
}

// handleNeedsAttention serves GET /products/{pid}/needs-attention in both
// of its modes, mirroring the ops console's per-view structure: an htmx
// request gets a bare fragment at 200 and a browser gets the shell.
//
// A request derives its rows exactly once, whichever branch it takes, so a
// fragment cannot drift from the page it was swapped out of.
func (app *App) handleNeedsAttention(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	htmx := isHtmxRequest(r)
	// The browser flags a page view; a fragment swap inside a page the
	// operator is already on is not one, and must not move where their next
	// un-prefixed link lands (product_scope.go's own rule).
	if !htmx {
		setLastViewedProductCookie(w, product.ID)
	}

	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	tab := needsAttentionTabOf(r)
	// The read instant is taken once, before the reads, and is the only
	// clock the page's freshness stamp consults: a stamp judged against a
	// second, later time.Now() would report an age the rows it describes
	// were never read at.
	readAt := app.clock()
	results, err := app.needsAttentionResults(r.Context(), product.ID, tab, page, opsSelfPath(r), readAt)
	if err != nil {
		app.writeNeedsAttentionQueryError(w, r, product.ID, tab, err)
		return
	}

	// The results block alone: the claimed tab's poll and the Refresh
	// button both name it, and neither may take the strip with it. The
	// freshness stamp rides with the strip for that reason as well: its
	// instant moves on every read, and a polled fragment owes byte-stable
	// bytes for unchanged state.
	if htmx && !needsAttentionPanelSwap(r) {
		renderFragment(w, r, results)
		return
	}

	d := app.needsAttentionPage(r, product.ID, tab, readAt, results)
	if htmx {
		renderFragment(w, r, pages.NeedsAttention(d))
		return
	}
	app.renderShell(w, r, "Needs attention", r.URL.Path, pages.NeedsAttention(d))
}

// writeNeedsAttentionQueryError answers a failed tab read in each of the
// page's response shapes, so a failure never costs the operator the strip
// they navigate with.
//
//   - A htmx request naming the results block (the claimed tab's poll, or
//     Refresh) gets the bare refusal, exactly the block it asked for.
//   - A htmx request naming the region gets the region, with the refusal in
//     the results slot -- so the strip, its counts and the freshness stamp
//     survive and the operator can still click another tab. Replacing the
//     region with a bare fragment would delete the swap target itself, and
//     the next tab click would silently no-op (ops.templ's OpsInlineError
//     note, applied to the outer region).
//   - A browser keeps the shell and gets the message with a way back, which
//     is what the console's own query-error page is for.
func (app *App) writeNeedsAttentionQueryError(w http.ResponseWriter, r *http.Request, productID uuid.UUID, tab string, err error) {
	status, message := consoleQueryError(err)
	failure := pages.OpsInlineError(message)

	if isHtmxRequest(r) {
		if needsAttentionPanelSwap(r) {
			renderFragment(w, r, pages.NeedsAttention(
				app.needsAttentionPage(r, productID, tab, app.clock(), failure)))
			return
		}
		renderFragment(w, r, failure)
		return
	}
	app.renderShellStatus(w, r, "Needs attention", r.URL.Path,
		pages.OpsQueryError(message, opsRecoveryPath(r)), status)
}

// needsAttentionPage is the one assembly of the page's view model, so the
// success and failure answers cannot describe different strips. readAt is
// the instant the freshness stamp reports as when this view was read.
func (app *App) needsAttentionPage(r *http.Request, productID uuid.UUID, tab string, readAt time.Time, results templ.Component) pages.NeedsAttentionData {
	return pages.NeedsAttentionData{
		Tabs:            app.needsAttentionTabs(r.Context(), productID, tab),
		Tab:             tab,
		UpdatedAt:       readAt.UTC().Format(time.RFC3339),
		UpdatedRelative: relativeTime(readAt, app.clock()),
		RefreshHref:     r.URL.RequestURI(),
		Results:         results,
	}
}

// needsAttentionResults reads the selected tab's rows and renders them with
// the matching console view's own component, so the tab and the console
// view are one derivation (ops.go's four loaders) and one table.
//
// The Escalated tab is the exception: its row shape is its own (FR
// 772b044b, needsAttentionEscalatedResults below), because the columns the
// operator scans there are not the console view's. Its rows are still the
// one store read.
//
// now is the request's read instant, so the escalated tab's relative
// timestamps are judged against the same moment the freshness stamp
// reports.
func (app *App) needsAttentionResults(ctx context.Context, productID uuid.UUID, tab string, page store.PageParams, selfPath string, now time.Time) (templ.Component, error) {
	filter := store.ConsoleFilter{ProductID: &productID}
	switch tab {
	case needsAttentionTabClaimed:
		d, err := app.claimedResults(ctx, filter, page, selfPath)
		if err != nil {
			return nil, err
		}
		return pages.ClaimedResults(d), nil
	case needsAttentionTabCancelled:
		d, err := app.cancelledResults(ctx, filter, page, selfPath)
		if err != nil {
			return nil, err
		}
		return pages.CancelledResults(d), nil
	case needsAttentionTabNotes:
		d, err := app.openNotesResults(ctx, filter, page, selfPath)
		if err != nil {
			return nil, err
		}
		return pages.NotesResults(d), nil
	default:
		d, err := app.needsAttentionEscalatedResults(ctx, productID, page, selfPath, now)
		if err != nil {
			return nil, err
		}
		return pages.EscalatedQueueResults(d), nil
	}
}

// needsAttentionEscalatedResults reads one page of the Escalated tab (FR
// 772b044b) and builds its own row contract.
//
// It is the one derivation of this tab's data, called by the tab's GET and
// -- once the intervention path accepts this page's URL -- by a post-write
// re-derivation, so neither can render a row set the other would not. The
// read is ListEscalatedTasks under the product narrowing the whole page is
// scoped to, so the tab's content matches list_escalated_tasks for the same
// filters.
func (app *App) needsAttentionEscalatedResults(ctx context.Context, productID uuid.UUID, page store.PageParams, selfPath string, now time.Time) (pages.NeedsAttentionEscalatedData, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return pages.NeedsAttentionEscalatedData{}, err
	}
	result, err := app.tasks.ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &productID},
		Page:          page,
	})
	if err != nil {
		return pages.NeedsAttentionEscalatedData{}, err
	}
	rows := make([]pages.NeedsAttentionEscalatedRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = needsAttentionEscalatedRowOf(row, productID, selfPath, now)
	}
	return pages.NeedsAttentionEscalatedData{
		Rows:     rows,
		NextHref: opsNextHref(selfPath, result.NextToken, page.PageSize),
		Href:     selfPath,
	}, nil
}

// needsAttentionEscalatedRowOf builds one Escalated-tab row from the read's
// own row.
//
// returnTo is the tab's own URL: the row's controls carry it so an
// intervention knows which view to re-derive, and it is read back from the
// request rather than rebuilt, so a paged or filtered tab returns to itself.
//
// now is the read instant, so relativeTime answers against the same clock
// the freshness stamp does.
func needsAttentionEscalatedRowOf(r store.EscalatedTaskRow, productID uuid.UUID, returnTo string, now time.Time) pages.NeedsAttentionEscalatedRow {
	return pages.NeedsAttentionEscalatedRow{
		TaskID:     r.TaskID.String(),
		Title:      r.Title,
		DetailHref: productTaskDetailPath(productID, r.TaskID),
		ByLine:     escalatedByLine(r),
		Milestone:  r.DeliveryRef.Title,
		Lane:       string(r.Lane),
		Reason:     string(r.Reason),
		// The count against the ATTEMPT cap, never the escalation's own
		// CapValue: for a thrash-cap escalation that figure is the thrash
		// cap, and labelling an attempt count with it would misreport every
		// such row. There is no per-task cap column, so the package-wide
		// default is the cap -- the same fallback taskAttemptsLabel makes
		// for every other row (task_page.go).
		Attempts:            taskAttemptsLabel(r.AttemptCount),
		EscalatedAt:         r.EscalatedAt.UTC().Format(time.RFC3339),
		EscalatedAtRelative: relativeTime(r.EscalatedAt, now),
		EscalationID:        observedEscalationID(r),
		Actions:             escalatedRowActions(r.TaskID.String(), observedEscalationID(r), string(r.Lane), returnTo),
	}
}

// escalatedGuardField is the form field the Escalated tab's controls submit
// their observed escalation id under. It is the api request body's own
// field name (handlers.requeueTaskRequest.ExpectedEscalationID and
// handlers.cancelTaskRequest.ExpectedEscalationID), so a submitted guard
// reaches krill api unchanged rather than being renamed on the way through.
const escalatedGuardField = "expected_escalation_id"

// escalatedRowActions builds one escalated row's controls (FR 772b044b):
// Requeue and Cancel, and never Release -- an escalated task holds no
// claim, so there is nothing to force-close.
//
// Both controls carry the escalation id THIS row observed: Requeue as a
// hidden form field, Cancel as a query parameter on its confirmation link,
// since the destructive verb's control is a link rather than a form. The id
// is taken from the row and never typed, so an escalation that changed
// since the page loaded is refused against what the operator saw rather
// than against whatever is current.
//
// A Done-lane task is offered no Cancel (FR af61631d): the task is
// finished, and dead-lettering it is not an intervention this queue may
// offer. Requeue stays, because returning a finished-but-escalated task to
// claimable is the recovery the queue exists for.
func escalatedRowActions(taskID, escalationID, lane, returnTo string) templ.Component {
	requeue := interventionActions[actionRequeue]
	controls := []pages.TaskActionControl{{
		Kind:          "form",
		Label:         requeue.Label,
		ReasonHint:    requeue.ReasonHint,
		Action:        opsTaskActionBase + taskID + "/" + actionRequeue,
		ReturnTo:      returnTo,
		ObservedField: escalatedGuardField,
		ObservedID:    escalationID,
	}}
	if lane != string(store.LaneDone) {
		cancel := interventionActions[actionCancel]
		href := cancelConfirmHref(taskID, returnTo)
		if escalationID != "" {
			href += "&" + url.Values{escalatedGuardField: {escalationID}}.Encode()
		}
		controls = append(controls, pages.TaskActionControl{
			Kind:     "confirm",
			Label:    cancel.Label,
			Action:   href,
			ReturnTo: returnTo,
		})
	}
	return pages.TaskActions(controls)
}

// observedEscalationID is the row's observed escalation id as a string,
// empty when the read somehow reported none. An escalated row always
// carries one (ListEscalatedTasks reads current_escalation_id IS NOT
// NULL), and the empty spelling is what keeps a row that did not from
// rendering a zero-UUID guard -- a guard that would refuse every action.
func observedEscalationID(r store.EscalatedTaskRow) string {
	if r.EscalationID == uuid.Nil {
		return ""
	}
	return r.EscalationID.String()
}

// escalatedByLine is the escalation's own subjects, as the row's sub-line:
// who escalated it, and who they were acting for when they did. A
// subject-less side reads as "-" through opsActor/opsSubject, and an
// escalation with no on-behalf-of states only the acting half rather than
// "for -".
func escalatedByLine(r store.EscalatedTaskRow) string {
	by := opsActor(r.EscalatedByActing)
	if of := opsSubject(r.EscalatedByOnBehalfOf); of != "-" {
		return "by " + by + " for " + of
	}
	return "by " + by
}

// needsAttentionTabs builds the strip: the four tabs, each at its own
// product-scoped URL and each labelled with its count, with the request's
// tab marked active.
//
// Each count is the store's dedicated count read (CountClaimedTasks and
// friends) under the SAME params type -- and therefore the same
// ConsoleFilter -- the matching list was read with, so a badge and the
// table beneath it cannot disagree about how many rows there are. The
// figure is the whole filtered set, never the current page's length.
//
// A count that could not be read carries NO badge rather than a zero. Zero
// is a claim about the queue and we could not read the queue; a tab with no
// badge says nothing, which is the honest state (nav.go's unreadableBadge,
// applied to a tab).
func (app *App) needsAttentionTabs(ctx context.Context, productID uuid.UUID, active string) []pages.NeedsAttentionTab {
	tabs := make([]pages.NeedsAttentionTab, 0, len(needsAttentionTabOrder))
	scopeID, scopeErr := app.soleScopeID(ctx)
	if scopeErr != nil {
		logger.Warn("needs attention: could not resolve scope for the tab counts", "error", scopeErr)
	}
	for _, key := range needsAttentionTabOrder {
		tab := pages.NeedsAttentionTab{
			Key:    key,
			Label:  needsAttentionTabLabels[key],
			Href:   needsAttentionTabHref(productID, key),
			Active: key == active,
		}
		if scopeErr == nil {
			count, err := app.needsAttentionCount(ctx, scopeID, productID, key)
			if err != nil {
				logger.Warn("needs attention: tab count unreadable, omitting the badge",
					"tab", key, "product", productID, "error", err)
			} else {
				tab.Count, tab.HasCount = count, true
			}
		}
		tabs = append(tabs, tab)
	}
	return tabs
}

// needsAttentionCount reads one tab's count through the params type the
// matching list read takes, narrowed to the same product, so the count and
// the list are the same question asked of the same store query
// (store.CountClaimedTasks shares its FROM/JOIN/WHERE with
// ListClaimedTasks, and so on for the other three).
func (app *App) needsAttentionCount(ctx context.Context, scopeID, productID uuid.UUID, tab string) (int, error) {
	filter := store.ConsoleFilter{ProductID: &productID}
	switch tab {
	case needsAttentionTabClaimed:
		return app.tasks.CountClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
	case needsAttentionTabCancelled:
		return app.tasks.CountCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
	case needsAttentionTabNotes:
		return app.tasks.CountOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter})
	default:
		return app.tasks.CountEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
	}
}
