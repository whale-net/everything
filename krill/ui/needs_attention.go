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
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
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

// The filter bar's query parameters, named once in the page package that
// writes them (pages.NeedsAttention*QueryParam) and aliased here for the
// parse, so the select that submits a value and the parser that reads it
// cannot disagree about its spelling. They are the names krill api's
// console views read too, so one filter set has one spelling everywhere.
const (
	// needsAttentionTabParam is the query parameter that carries the
	// selected tab, so the page an operator shares is the tab they were
	// reading.
	needsAttentionTabParam = pages.NeedsAttentionTabQueryParam

	// needsAttentionMilestoneParam narrows every tab to one milestone_ref
	// container -- a milestone or a milepebble.
	needsAttentionMilestoneParam = pages.NeedsAttentionMilestoneQueryParam

	// needsAttentionReasonParam narrows the Escalated tab to one
	// EscalationReason.
	needsAttentionReasonParam = pages.NeedsAttentionReasonQueryParam
)

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

// needsAttentionTabHref is one tab's own URL under a product with no
// filters in force. An empty tab is the page's bare address, which is what
// a successor with no matching tab (/ops, the console root) redirects to.
func needsAttentionTabHref(pid uuid.UUID, tab string) string {
	return needsAttentionTabHrefFor(pid, tab, needsAttentionFilter{})
}

// needsAttentionTabHrefFor is the tab strip's link builder: each tab's own
// address carrying the filters in force, so switching tabs keeps the
// milestone and the reason the operator selected (FR 6369e312's
// "selections survive tab switching"). The tab is one query parameter
// among the filters rather than the whole query.
//
// page_size and page_token are deliberately not carried: the first is a
// page's size rather than a filter, and the second is a POSITION in one
// read's keyset whose token binds the filter set that issued it
// (store.DecodeFilteredContinuationToken), so carrying it into another
// tab's read would be refused rather than resumed.
func needsAttentionTabHrefFor(pid uuid.UUID, tab string, f needsAttentionFilter) string {
	base := productHref(pid, needsAttentionSuffix)
	q := url.Values{}
	if tab != "" {
		q.Set(needsAttentionTabParam, tab)
	}
	if f.MilestoneID != nil {
		q.Set(needsAttentionMilestoneParam, f.MilestoneID.String())
	}
	if f.Reason != nil {
		q.Set(needsAttentionReasonParam, string(*f.Reason))
	}
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

// needsAttentionFilter is the filter bar's state, parsed from one request's
// query string and nowhere else. It is the page's whole narrowing beyond
// the current product: a milestone_ref container of either kind, and -- on
// the Escalated tab -- an escalation reason.
type needsAttentionFilter struct {
	MilestoneID *uuid.UUID
	Reason      *store.EscalationReason
}

// narrows reports whether either selection is in force. The product is not
// a filter in this sense: it is the page's scope, always set, so a whole
// product's empty queue is an empty queue rather than a filter excluding
// everything.
func (f needsAttentionFilter) narrows() bool {
	return f.MilestoneID != nil || f.Reason != nil
}

// console is the store narrowing the product and the milestone selection
// render as (store.ConsoleFilter). The reason is not part of it: it is
// ListEscalatedTasksParams' own field, meaningful only to the escalated
// read.
func (f needsAttentionFilter) console(productID uuid.UUID) store.ConsoleFilter {
	return store.ConsoleFilter{ProductID: &productID, MilestoneID: f.MilestoneID}
}

// needsAttentionReasonValues is the reason select's values, in the order
// the FR names them. The values ARE the store's own enumeration constants
// (store.EscalationReasonThrashCap and friends) -- there is no UI-local
// spelling of a reason -- and the labels are the shared
// components.EscalationReasonLabel wording, so this select and the reason
// badges on the rows below it cannot word one reason two ways.
var needsAttentionReasonValues = []store.EscalationReason{
	store.EscalationReasonThrashCap,
	store.EscalationReasonAttemptCap,
	store.EscalationReasonManual,
}

// needsAttentionReasonOf is membership in that fixed set by wire value.
func needsAttentionReasonOf(raw string) (store.EscalationReason, bool) {
	for _, reason := range needsAttentionReasonValues {
		if string(reason) == raw {
			return reason, true
		}
	}
	return "", false
}

// needsAttentionReasonOptions is the reason select's options: "Any reason"
// -- the empty value, which is the read's nil Reason -- then one per
// store.EscalationReason.
func needsAttentionReasonOptions(selected *store.EscalationReason) []pages.NeedsAttentionFilterOption {
	out := make([]pages.NeedsAttentionFilterOption, 0, len(needsAttentionReasonValues)+1)
	out = append(out, pages.NeedsAttentionFilterOption{Label: "Any reason", Selected: selected == nil})
	for _, reason := range needsAttentionReasonValues {
		out = append(out, pages.NeedsAttentionFilterOption{
			Value:    string(reason),
			Label:    components.EscalationReasonLabel(string(reason)),
			Selected: selected != nil && *selected == reason,
		})
	}
	return out
}

// needsAttentionMilestoneOptions is the milestone select's options: "All
// milestones" -- the empty value, which is the read's nil MilestoneID --
// then the product's own containers. A milestone is offered by name with
// each of its milepebbles beneath it, because a milestone filter narrows
// on a milestone_ref id of EITHER kind (store.ConsoleFilter's own rule)
// and the select has to be able to echo either back.
func needsAttentionMilestoneOptions(containers []taskContainer, selected *uuid.UUID) []pages.NeedsAttentionFilterOption {
	out := make([]pages.NeedsAttentionFilterOption, 0, len(containers)+1)
	out = append(out, pages.NeedsAttentionFilterOption{Label: "All milestones", Selected: selected == nil})
	for _, m := range containers {
		out = append(out, pages.NeedsAttentionFilterOption{
			Value:    m.ID.String(),
			Label:    m.Name,
			Selected: selected != nil && *selected == m.ID,
		})
		for _, mp := range m.Milepebbles {
			out = append(out, pages.NeedsAttentionFilterOption{
				Value:    mp.ID.String(),
				Label:    m.Name + " · " + mp.Name,
				Selected: selected != nil && *selected == mp.ID,
			})
		}
	}
	return out
}

// needsAttentionContainerLabel names one milestone_ref id for the empty
// state's sentence: the container's own name when the product still
// carries it, the raw id otherwise -- a hand-edited link can name a
// container this page has no label for, and saying so is better than
// dropping the filter from the sentence.
func needsAttentionContainerLabel(containers []taskContainer, id uuid.UUID) string {
	for _, m := range containers {
		if m.ID == id {
			return m.Name
		}
		for _, mp := range m.Milepebbles {
			if mp.ID == id {
				return m.Name + " · " + mp.Name
			}
		}
	}
	return id.String()
}

// needsAttentionFilterSentence names the filters in force, in the
// operator's words, for the empty state to read back.
func needsAttentionFilterSentence(f needsAttentionFilter, containers []taskContainer) string {
	var parts []string
	if f.MilestoneID != nil {
		parts = append(parts, "milestone "+needsAttentionContainerLabel(containers, *f.MilestoneID))
	}
	if f.Reason != nil {
		parts = append(parts, "reason "+components.EscalationReasonLabel(string(*f.Reason)))
	}
	return strings.Join(parts, " and ")
}

// parseNeedsAttentionFilter reads the filter bar's two selections off the
// request.
//
// A malformed milestone id or an unrecognised reason is the caller's error
// (400), matching parseOpsPageParams: both are values the URL spelled
// wrongly, and silently ignoring one would render a table whose rows
// disagree with the filter the page still claims is in force.
func parseNeedsAttentionFilter(r *http.Request) (needsAttentionFilter, error) {
	var f needsAttentionFilter
	q := r.URL.Query()
	if raw := strings.TrimSpace(q.Get(needsAttentionMilestoneParam)); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return f, fmt.Errorf("%s: must be a UUID", needsAttentionMilestoneParam)
		}
		f.MilestoneID = &id
	}
	if raw := strings.TrimSpace(q.Get(needsAttentionReasonParam)); raw != "" {
		reason, ok := needsAttentionReasonOf(raw)
		if !ok {
			return f, fmt.Errorf("%s: unknown escalation reason", needsAttentionReasonParam)
		}
		f.Reason = &reason
	}
	return f, nil
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
	filter, err := parseNeedsAttentionFilter(r)
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
	// The product's own containers are read once and shared by the tab
	// strip's links, the filter bar's options and the empty state's
	// sentence, so the three cannot describe different containers.
	containers := app.needsAttentionMilestoneContainers(r.Context(), product.ID)
	results, err := app.needsAttentionResults(r.Context(), product.ID, filter,
		needsAttentionFilterSentence(filter, containers), tab, page, opsSelfPath(r))
	if err != nil {
		app.writeNeedsAttentionQueryError(w, r, product.ID, filter, containers, tab, err)
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

	d := app.needsAttentionPage(r, product.ID, filter, containers, tab, readAt, results)
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
func (app *App) writeNeedsAttentionQueryError(w http.ResponseWriter, r *http.Request, productID uuid.UUID, filter needsAttentionFilter, containers []taskContainer, tab string, err error) {
	status, message := consoleQueryError(err)
	failure := pages.OpsInlineError(message)

	if isHtmxRequest(r) {
		if needsAttentionPanelSwap(r) {
			renderFragment(w, r, pages.NeedsAttention(
				app.needsAttentionPage(r, productID, filter, containers, tab, app.clock(), failure)))
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
func (app *App) needsAttentionPage(r *http.Request, productID uuid.UUID, filter needsAttentionFilter, containers []taskContainer, tab string, readAt time.Time, results templ.Component) pages.NeedsAttentionData {
	return pages.NeedsAttentionData{
		Tabs:            app.needsAttentionTabs(r.Context(), productID, tab, filter),
		Tab:             tab,
		UpdatedAt:       readAt.UTC().Format(time.RFC3339),
		UpdatedRelative: relativeTime(readAt, app.clock()),
		RefreshHref:     r.URL.RequestURI(),
		Filter:          app.needsAttentionFilterBar(r, filter, containers, tab),
		Results:         results,
	}
}

// needsAttentionFilterBar assembles the filter bar: the selects' options
// from the product's own containers and the store's reason enumeration,
// and the Open notes sentence a milestone filter turns on.
//
// The reason select is offered on the Escalated tab alone -- it is the one
// queue whose rows carry a reason -- but a reason in the URL still rides
// along on the other tabs' links, so a round trip through another tab
// brings the operator back to the selection they made.
func (app *App) needsAttentionFilterBar(r *http.Request, filter needsAttentionFilter, containers []taskContainer, tab string) pages.NeedsAttentionFilterBar {
	bar := pages.NeedsAttentionFilterBar{
		Path:       r.URL.Path,
		Milestones: needsAttentionMilestoneOptions(containers, filter.MilestoneID),
		ShowReason: tab == needsAttentionTabEscalated,
	}
	if bar.ShowReason {
		bar.Reasons = needsAttentionReasonOptions(filter.Reason)
	}
	if tab == needsAttentionTabNotes && filter.MilestoneID != nil {
		bar.Notice = "Notes attached to a spec entity have no milestone, so a milestone filter shows only notes on that milestone's tasks."
	}
	return bar
}

// needsAttentionMilestoneContainers reads the product's own delivery
// listing and shapes it as the filter bar's containers -- the same read,
// through the same taskContainersOf shaping, behind
// product_task_scope.go's milestone and milepebble option builders, so the
// filter bar offers the product's own containers rather than a second
// milestones query.
//
// A listing that could not be read is logged and leaves the select with
// its "All milestones" default alone: the tabs still read, and a filter
// bar that failed the page would cost the operator the queues over an
// option list.
func (app *App) needsAttentionMilestoneContainers(ctx context.Context, productID uuid.UUID) []taskContainer {
	listing, err := app.spec.Delivery(ctx, productID, nil)
	if err != nil {
		logger.Warn("needs attention: could not read the product's containers for the filter bar",
			"product", productID, "error", err)
		return nil
	}
	return taskContainersOf(listing)
}

// needsAttentionResults reads the selected tab's rows and renders them with
// the matching console view's own component, so the tab and the console
// view are one derivation (ops.go's four loaders) and one table.
//
// filter is the filter bar's narrowing, applied to every tab's read: the
// milestone selection rides on the store.ConsoleFilter all four reads take,
// and the reason on ListEscalatedTasksParams' own field, which only the
// escalated read has. filterLabel names those filters in the operator's
// words, for the empty state a filtered tab renders instead of its generic
// "No claimed tasks."
func (app *App) needsAttentionResults(ctx context.Context, productID uuid.UUID, filter needsAttentionFilter, filterLabel, tab string, page store.PageParams, selfPath string) (templ.Component, error) {
	console := filter.console(productID)
	switch tab {
	case needsAttentionTabClaimed:
		d, err := app.claimedResults(ctx, console, page, selfPath)
		if err != nil {
			return nil, err
		}
		if empty, ok := needsAttentionEmptyData(len(d.Rows), filter, filterLabel, "claimed tasks"); ok {
			return pages.NeedsAttentionFilteredEmpty(empty), nil
		}
		return pages.ClaimedResults(d), nil
	case needsAttentionTabCancelled:
		d, err := app.cancelledResults(ctx, console, page, selfPath)
		if err != nil {
			return nil, err
		}
		if empty, ok := needsAttentionEmptyData(len(d.Rows), filter, filterLabel, "cancelled tasks"); ok {
			return pages.NeedsAttentionFilteredEmpty(empty), nil
		}
		return pages.CancelledResults(d), nil
	case needsAttentionTabNotes:
		d, err := app.openNotesResults(ctx, console, page, selfPath)
		if err != nil {
			return nil, err
		}
		if empty, ok := needsAttentionEmptyData(len(d.Rows), filter, filterLabel, "open notes"); ok {
			return pages.NeedsAttentionFilteredEmpty(empty), nil
		}
		return pages.NotesResults(d), nil
	default:
		d, err := app.escalatedResults(ctx, console, filter.Reason, page, selfPath)
		if err != nil {
			return nil, err
		}
		if empty, ok := needsAttentionEmptyData(len(d.Rows), filter, filterLabel, "escalated tasks"); ok {
			return pages.NeedsAttentionFilteredEmpty(empty), nil
		}
		return pages.EscalatedResults(d), nil
	}
}

// needsAttentionEmptyData is the empty state a filtered tab renders, or ok
// false when the tab's own table (with its generic empty state) is the
// right answer.
//
// Two conditions, both required: the read came back empty, and a filter is
// in force. An empty tab with no filter is the queue genuinely being
// empty, which the tab's own "No escalated tasks." already says honestly;
// naming filters that are not in force would invent a cause for it.
func needsAttentionEmptyData(rows int, filter needsAttentionFilter, filterLabel, queue string) (pages.NeedsAttentionEmptyData, bool) {
	if rows > 0 || !filter.narrows() {
		return pages.NeedsAttentionEmptyData{}, false
	}
	return pages.NeedsAttentionEmptyData{
		Headline: "No " + queue + " match these filters.",
		Detail:   "Filtering by " + filterLabel + ". Clear a filter to see the whole queue.",
	}, true
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
func (app *App) needsAttentionTabs(ctx context.Context, productID uuid.UUID, active string, filter needsAttentionFilter) []pages.NeedsAttentionTab {
	tabs := make([]pages.NeedsAttentionTab, 0, len(needsAttentionTabOrder))
	scopeID, scopeErr := app.soleScopeID(ctx)
	if scopeErr != nil {
		logger.Warn("needs attention: could not resolve scope for the tab counts", "error", scopeErr)
	}
	for _, key := range needsAttentionTabOrder {
		tab := pages.NeedsAttentionTab{
			Key:    key,
			Label:  needsAttentionTabLabels[key],
			Href:   needsAttentionTabHrefFor(productID, key, filter),
			Active: key == active,
		}
		if scopeErr == nil {
			count, err := app.needsAttentionCount(ctx, scopeID, productID, filter, key)
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
// matching list read takes, narrowed the SAME way -- the same
// ConsoleFilter, and for the escalated queue the same reason -- so the
// count and the list are the same question asked of the same store query
// (store.CountClaimedTasks shares its FROM/JOIN/WHERE with
// ListClaimedTasks, and so on for the other three).
func (app *App) needsAttentionCount(ctx context.Context, scopeID, productID uuid.UUID, filter needsAttentionFilter, tab string) (int, error) {
	console := filter.console(productID)
	switch tab {
	case needsAttentionTabClaimed:
		return app.tasks.CountClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: console})
	case needsAttentionTabCancelled:
		return app.tasks.CountCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: console})
	case needsAttentionTabNotes:
		return app.tasks.CountOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: console})
	default:
		return app.tasks.CountEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: console, Reason: filter.Reason})
	}
}
