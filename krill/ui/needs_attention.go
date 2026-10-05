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
	results, err := app.needsAttentionResults(r.Context(), product.ID, tab, page, opsSelfPath(r))
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
func (app *App) needsAttentionResults(ctx context.Context, productID uuid.UUID, tab string, page store.PageParams, selfPath string) (templ.Component, error) {
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
		d, err := app.escalatedResults(ctx, filter, page, selfPath)
		if err != nil {
			return nil, err
		}
		return pages.EscalatedResults(d), nil
	}
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

// ---------------------------------------------------------------------------
// the Claimed tab's rows (FR a149d28f)
// ---------------------------------------------------------------------------

// newClaimedRow builds one Claimed tab row from the store's claimed-task
// row (store.ClaimedTaskRow), which is the one read every field here comes
// from -- nothing is derived from a second query, so the row and
// list_claimed_tasks cannot disagree.
//
// pid is the product the read was narrowed to; it is what the task's detail
// link is spelled against, and uuid.Nil (a read with no product narrowing,
// such as the retired console view) leaves the title unlinked rather than
// inventing a product id.
//
// returnTo is the view a no-JS action form returns to, exactly as the
// console's own rows spell it.
func newClaimedRow(r store.ClaimedTaskRow, pid uuid.UUID, returnTo string) pages.ClaimedRow {
	// A read that observed no claim (a zero id) carries none: an all-zero
	// uuid is not a claim any write could be guarded against, so the row
	// states nothing rather than stating a false id.
	claimID := ""
	if r.ClaimID != uuid.Nil {
		claimID = r.ClaimID.String()
	}
	return pages.ClaimedRow{
		TaskID:         r.TaskID.String(),
		Title:          r.Title,
		TaskHref:       claimedTaskHref(pid, r.TaskID),
		Milestone:      r.DeliveryRef.Title,
		Lane:           string(r.CurrentLane),
		Claimant:       claimedBy(r.ClaimantActing, r.ClaimantOnBehalfOf),
		ClaimedSince:   opsTime(r.ClaimedAt),
		LeaseExpiresAt: opsTime(r.LeaseExpiresAt),
		ClaimID:        claimID,
		Actions:        claimedRowActions(r, returnTo),
	}
}

// claimedTaskHref is the product-scoped detail URL for a claimed task, the
// same address the product-wide Tasks table's rows link to. An unresolved
// product (uuid.Nil) yields no link at all.
func claimedTaskHref(pid, taskID uuid.UUID) string {
	if pid == uuid.Nil {
		return ""
	}
	return productTaskDetailPath(pid, taskID)
}

// claimedBy renders a claim's holder the way the tab's claimant column
// reads it: "by <acting> for <on-behalf-of>" (the FR's own wording). The
// on-behalf-of half is dropped when the claim names none -- a claim taken
// for nobody reads "by worker-3", never "by worker-3 for -".
//
// The acting subject keeps its kind ("(human)"/"(service)") for the reason
// opsActor gives: an operator scanning for who is holding a claim needs to
// tell a person from a service, and the on-behalf-of subject does not carry
// that question (opsSubject's rule).
func claimedBy(acting, onBehalfOf store.Subject) string {
	by := "by " + opsActor(acting)
	if forWhom := opsSubject(onBehalfOf); forWhom != "-" {
		by += " for " + forWhom
	}
	return by
}

// claimedRowVerbs is the intervention legality of a claimed row, which is
// task detail's own: Release is always offered (the task holds a claim),
// while Escalate and Cancel are not offered on a task in the Done lane --
// a finished task is neither flagged for attention nor dead-lettered,
// whichever lane it reached Done from.
//
// One function rather than a condition inside the row builder, so the
// order the FR names the verbs in ("Release, Escalate and Cancel") is
// stated once.
func claimedRowVerbs(lane store.Lane) []string {
	verbs := []string{actionRelease}
	if lane == store.LaneDone {
		return verbs
	}
	return append(verbs, actionEscalate, actionCancel)
}

// claimedRowActions renders a claimed row's controls with the verbs legal
// for its lane, each carrying the claim the row observed so the action's
// guard is checked against the state the operator actually saw.
func claimedRowActions(r store.ClaimedTaskRow, returnTo string) templ.Component {
	return renderClaimedTaskActions(r.TaskID.String(), r.ClaimID, returnTo, claimedRowVerbs(r.CurrentLane)...)
}
