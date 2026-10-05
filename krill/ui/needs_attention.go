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
	results, err := app.needsAttentionResults(r.Context(), product.ID, tab, page, opsSelfPath(r))
	if err != nil {
		app.writeConsoleQueryError(w, r, "Needs attention", err)
		return
	}

	// The results block alone: the claimed tab's poll and the Refresh
	// button both name it, and neither may take the strip with it.
	if htmx && !needsAttentionPanelSwap(r) {
		renderFragment(w, r, results)
		return
	}

	d := pages.NeedsAttentionData{
		Tabs:        app.needsAttentionTabs(product.ID, tab),
		Tab:         tab,
		RefreshHref: r.URL.RequestURI(),
		Results:     results,
	}
	if htmx {
		renderFragment(w, r, pages.NeedsAttention(d))
		return
	}
	app.renderShell(w, r, "Needs attention", r.URL.Path, pages.NeedsAttention(d))
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
// product-scoped URL, with the request's tab marked active.
func (app *App) needsAttentionTabs(productID uuid.UUID, active string) []pages.NeedsAttentionTab {
	tabs := make([]pages.NeedsAttentionTab, 0, len(needsAttentionTabOrder))
	for _, key := range needsAttentionTabOrder {
		tabs = append(tabs, pages.NeedsAttentionTab{
			Key:    key,
			Label:  needsAttentionTabLabels[key],
			Href:   needsAttentionTabHref(productID, key),
			Active: key == active,
		})
	}
	return tabs
}
