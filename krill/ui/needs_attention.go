// The Needs attention page: one product-scoped page whose tabs are the ops
// console's four queues (Escalated, Claimed, Cancelled, Open notes), each read
// through ops.go's loaders so a tab and its console view show the same rows.
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

// The four tabs, in strip order; also the successor tabs of the legacy /ops
// URLs.
const (
	needsAttentionTabEscalated = "escalated"
	needsAttentionTabClaimed   = "claimed"
	needsAttentionTabCancelled = "cancelled"
	needsAttentionTabNotes     = "notes"
)

// Filter query parameters, aliased from the pages package so the form and
// the parser share one spelling (also krill api's console spelling).
const (
	// needsAttentionTabParam carries the selected tab, so a shared URL keeps it.
	needsAttentionTabParam = pages.NeedsAttentionTabQueryParam

	// needsAttentionMilestoneParam narrows every tab to one milestone_ref
	// (milestone or milepebble).
	needsAttentionMilestoneParam = pages.NeedsAttentionMilestoneQueryParam

	needsAttentionReasonParam = pages.NeedsAttentionReasonQueryParam
)

// needsAttentionTabOrder is the strip order; Escalated is first and default.
var needsAttentionTabOrder = []string{
	needsAttentionTabEscalated,
	needsAttentionTabClaimed,
	needsAttentionTabCancelled,
	needsAttentionTabNotes,
}

// needsAttentionTabLabels maps wire values to operator-facing tab names.
var needsAttentionTabLabels = map[string]string{
	needsAttentionTabEscalated: "Escalated",
	needsAttentionTabClaimed:   "Claimed",
	needsAttentionTabCancelled: "Cancelled",
	needsAttentionTabNotes:     "Open notes",
}

// needsAttentionTabOf resolves ?tab=; absent or unrecognised values fall back
// to Escalated, since stale or hand-edited links must still render.
func needsAttentionTabOf(r *http.Request) string {
	tab := r.URL.Query().Get(needsAttentionTabParam)
	for _, known := range needsAttentionTabOrder {
		if tab == known {
			return known
		}
	}
	return needsAttentionTabEscalated
}

// needsAttentionTabHref is a tab's unfiltered URL; an empty tab is the page's
// bare address.
func needsAttentionTabHref(pid uuid.UUID, tab string) string {
	return needsAttentionTabHrefFor(pid, tab, needsAttentionFilter{})
}

// needsAttentionTabHrefFor builds a tab link that keeps the filters in force.
// page_token is dropped: it binds the filter set and tab that issued it.
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

// needsAttentionFilter is the filter bar's state, parsed from the query: a
// milestone_ref container and, on Escalated, a reason.
type needsAttentionFilter struct {
	MilestoneID *uuid.UUID
	Reason      *store.EscalationReason
}

// narrows reports whether a filter is in force; the product is the page's
// scope, not a filter.
func (f needsAttentionFilter) narrows() bool {
	return f.MilestoneID != nil || f.Reason != nil
}

// console maps the product and milestone onto store.ConsoleFilter; the reason
// belongs to ListEscalatedTasksParams.
func (f needsAttentionFilter) console(productID uuid.UUID) store.ConsoleFilter {
	return store.ConsoleFilter{ProductID: &productID, MilestoneID: f.MilestoneID}
}

// needsAttentionReasonValues are the store's reason constants, labelled via
// components.EscalationReasonLabel so select and badges agree.
var needsAttentionReasonValues = []store.EscalationReason{
	store.EscalationReasonThrashCap,
	store.EscalationReasonAttemptCap,
	store.EscalationReasonManual,
}

func needsAttentionReasonOf(raw string) (store.EscalationReason, bool) {
	for _, reason := range needsAttentionReasonValues {
		if string(reason) == raw {
			return reason, true
		}
	}
	return "", false
}

// needsAttentionReasonOptions is "Any reason" (nil) then one per reason.
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

// needsAttentionMilestoneOptions is "All milestones" (nil) then each milestone
// with its milepebbles beneath, since the filter accepts either kind.
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

// needsAttentionContainerLabel names a container for the empty-state
// sentence, falling back to the raw id for an unknown container.
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

// needsAttentionFilterSentence names the filters in force for the empty state.
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

// parseNeedsAttentionFilter reads the filter selections. A malformed id or
// unknown reason is a 400: ignoring it would show rows that contradict the
// filter the page claims.
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

// needsAttentionPanelSwap reports whether htmx targeted the whole tab region
// (a tab click) rather than the results block (poll or Refresh).
func needsAttentionPanelSwap(r *http.Request) bool {
	return hxTargetID(r) == pages.NeedsAttentionAnchor
}

// handleNeedsAttention serves the page: a fragment at 200 for htmx, the
// shell for a browser. Rows are derived once per request.
func (app *App) handleNeedsAttention(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	htmx := isHtmxRequest(r)
	// Only a full page view records the last-viewed product, not a swap.
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
	// One read instant for the whole page, so the freshness stamp matches the
	// rows.
	readAt := app.clock()
	// Containers are read once and shared by tab links, filter options, and the
	// empty state.
	containers := app.needsAttentionMilestoneContainers(r.Context(), product.ID)
	view, err := app.needsAttentionResults(r.Context(), product.ID, filter,
		needsAttentionFilterSentence(filter, containers), tab, page, opsSelfPath(r), readAt, "")
	if err != nil {
		app.writeNeedsAttentionQueryError(w, r, product.ID, filter, containers, tab, err)
		return
	}

	// Results block only (poll or Refresh): the strip and freshness stamp stay
	// out so polled bytes stay stable, and tab counts are not re-read.
	if htmx && !needsAttentionPanelSwap(r) {
		renderFragment(w, r, view.Results)
		return
	}

	// The strip's active-tab count is also the footer total, so they agree.
	tabs := app.needsAttentionTabs(r.Context(), product.ID, tab, filter)
	total, hasTotal := needsAttentionActiveTotal(tabs, tab)

	d := app.needsAttentionPage(r, filter, containers, tab, readAt, tabs, view.Results,
		needsAttentionFooter(view.Shown, total, hasTotal, view.NextHref))
	if htmx {
		renderFragment(w, r, pages.NeedsAttention(d))
		return
	}
	app.renderShell(w, r, "Needs attention", r.URL.Path, pages.NeedsAttention(d))
}

// writeNeedsAttentionQueryError answers a failed tab read in each response
// shape while keeping the strip: htmx results-only gets the bare refusal,
// htmx region gets the region with the refusal inline, a browser gets the shell.
func (app *App) writeNeedsAttentionQueryError(w http.ResponseWriter, r *http.Request, productID uuid.UUID, filter needsAttentionFilter, containers []taskContainer, tab string, err error) {
	status, message := consoleQueryError(err)
	failure := pages.OpsInlineError(message)

	if isHtmxRequest(r) {
		if needsAttentionPanelSwap(r) {
			// The strip survives; no footer, since a refused read has no rows.
			tabs := app.needsAttentionTabs(r.Context(), productID, tab, filter)
			renderFragment(w, r, pages.NeedsAttention(
				app.needsAttentionPage(r, filter, containers, tab, app.clock(), tabs, failure, nil)))
			return
		}
		renderFragment(w, r, failure)
		return
	}
	app.renderShellStatus(w, r, "Needs attention", r.URL.Path,
		pages.OpsQueryError(message, opsRecoveryPath(r)), status)
}

// needsAttentionPage assembles the view model for both success and failure.
// paging is nil where a refusal left no table.
func (app *App) needsAttentionPage(r *http.Request, filter needsAttentionFilter, containers []taskContainer, tab string, readAt time.Time, tabs []pages.NeedsAttentionTab, results templ.Component, paging *pages.NeedsAttentionPaging) pages.NeedsAttentionData {
	return pages.NeedsAttentionData{
		Tabs:            tabs,
		Tab:             tab,
		UpdatedAt:       readAt.UTC().Format(time.RFC3339),
		UpdatedRelative: relativeTime(readAt, app.clock()),
		RefreshHref:     r.URL.RequestURI(),
		Filter:          app.needsAttentionFilterBar(r, filter, containers, tab),
		Results:         results,
		Paging:          paging,
	}
}

// needsAttentionFilterBar assembles the filter bar. The reason select shows
// only on Escalated, but a reason in the URL rides along on other tabs' links.
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

// needsAttentionMilestoneContainers reads the product's delivery listing as
// filter options. A failed read is logged and leaves only "All milestones".
func (app *App) needsAttentionMilestoneContainers(ctx context.Context, productID uuid.UUID) []taskContainer {
	listing, err := app.spec.Delivery(ctx, productID, nil)
	if err != nil {
		logger.Warn("needs attention: could not read the product's containers for the filter bar",
			"product", productID, "error", err)
		return nil
	}
	return taskContainersOf(listing)
}

// needsAttentionTabView is one tab's results plus what a paging footer needs.
// Shown is 0 where no table rendered; the caller supplies the total.
type needsAttentionTabView struct {
	Results  templ.Component
	Shown    int
	NextHref string
}

// needsAttentionResults reads the selected tab with the filter applied and
// renders it with the console view's component (Escalated has its own
// columns). message is an inline refusal to show above the rows.
func (app *App) needsAttentionResults(ctx context.Context, productID uuid.UUID, filter needsAttentionFilter, filterLabel, tab string, page store.PageParams, selfPath string, now time.Time, message string) (needsAttentionTabView, error) {
	console := filter.console(productID)
	switch tab {
	case needsAttentionTabClaimed:
		d, err := app.claimedResults(ctx, console, page, selfPath)
		if err != nil {
			return needsAttentionTabView{}, err
		}
		if empty, ok := needsAttentionEmptyData(len(d.Rows), filter, filterLabel, "claimed tasks"); ok && message == "" {
			return needsAttentionTabView{Results: pages.NeedsAttentionFilteredEmpty(empty)}, nil
		}
		d.Error = message
		return needsAttentionTabView{
			Results:  pages.ClaimedResults(d),
			Shown:    len(d.Rows),
			NextHref: d.NextHref,
		}, nil
	case needsAttentionTabCancelled:
		d, err := app.cancelledResults(ctx, console, page, selfPath)
		if err != nil {
			return needsAttentionTabView{}, err
		}
		if empty, ok := needsAttentionEmptyData(len(d.Rows), filter, filterLabel, "cancelled tasks"); ok && message == "" {
			return needsAttentionTabView{Results: pages.NeedsAttentionFilteredEmpty(empty)}, nil
		}
		d.Error = message
		return needsAttentionTabView{
			Results:  pages.NeedsAttentionCancelledResults(d),
			Shown:    len(d.Rows),
			NextHref: d.NextHref,
		}, nil
	case needsAttentionTabNotes:
		d, err := app.openNotesResults(ctx, console, page, selfPath)
		if err != nil {
			return needsAttentionTabView{}, err
		}
		if empty, ok := needsAttentionEmptyData(len(d.Rows), filter, filterLabel, "open notes"); ok && message == "" {
			return needsAttentionTabView{Results: pages.NeedsAttentionFilteredEmpty(empty)}, nil
		}
		d.Error = message
		return needsAttentionTabView{
			Results:  pages.NeedsAttentionNotesResults(d),
			Shown:    len(d.Rows),
			NextHref: d.NextHref,
		}, nil
	default:
		d, err := app.needsAttentionEscalatedResults(ctx, productID, filter, page, selfPath, now)
		if err != nil {
			return needsAttentionTabView{}, err
		}
		if empty, ok := needsAttentionEmptyData(len(d.Rows), filter, filterLabel, "escalated tasks"); ok && message == "" {
			return needsAttentionTabView{Results: pages.NeedsAttentionFilteredEmpty(empty)}, nil
		}
		d.Error = message
		return needsAttentionTabView{
			Results:  pages.EscalatedQueueResults(d),
			Shown:    len(d.Rows),
			NextHref: d.NextHref,
		}, nil
	}
}

// needsAttentionFooter builds a tab's footer, or nil when no table rendered.
// total is the active badge's count; !hasTotal names only what is on screen.
func needsAttentionFooter(shown, total int, hasTotal bool, nextHref string) *pages.NeedsAttentionPaging {
	if shown == 0 {
		return nil
	}
	return needsAttentionPagingOf(shown, total, hasTotal, nextHref)
}

// needsAttentionActiveTotal reads the active tab's count off the strip, so
// footer and badge share one read.
func needsAttentionActiveTotal(tabs []pages.NeedsAttentionTab, tab string) (int, bool) {
	for _, t := range tabs {
		if t.Key == tab {
			return t.Count, t.HasCount
		}
	}
	return 0, false
}

// needsAttentionPagingOf builds the footer. Previous is disabled: keyset
// tokens only go forward, so there is no address for the prior page.
func needsAttentionPagingOf(shown, total int, hasTotal bool, nextHref string) *pages.NeedsAttentionPaging {
	paging := &pages.NeedsAttentionPaging{
		Shown:    shown,
		Total:    total,
		HasTotal: hasTotal,
		Summary:  needsAttentionPagingSummary(shown, total, hasTotal),
		PrevNote: "This list pages forward only, so there is no previous page to link to. " +
			"Use the browser's Back button, or change a filter to start again from the first page.",
		NextHref: nextHref,
	}
	if nextHref == "" {
		paging.NextNote = "This is the last page: every task matching these filters is already shown."
	}
	return paging
}

// needsAttentionPagingSummary reads "Showing X of Y tasks", or only X when
// the count could not be read.
func needsAttentionPagingSummary(shown, total int, hasTotal bool) string {
	if !hasTotal {
		return fmt.Sprintf("Showing %d tasks", shown)
	}
	return fmt.Sprintf("Showing %d of %d tasks", shown, total)
}

// needsAttentionEscalatedResults reads one page of the Escalated tab under
// the same filters as list_escalated_tasks. productID scopes both the read
// and each row's links.
func (app *App) needsAttentionEscalatedResults(ctx context.Context, productID uuid.UUID, filter needsAttentionFilter, page store.PageParams, selfPath string, now time.Time) (pages.NeedsAttentionEscalatedData, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return pages.NeedsAttentionEscalatedData{}, err
	}
	console := filter.console(productID)
	result, err := app.tasks.ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: console,
		Reason:        filter.Reason,
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

// needsAttentionEscalatedRowOf builds one Escalated row. returnTo is the
// tab's own URL, so a paged or filtered tab returns to itself.
func needsAttentionEscalatedRowOf(r store.EscalatedTaskRow, productID uuid.UUID, returnTo string, now time.Time) pages.NeedsAttentionEscalatedRow {
	return pages.NeedsAttentionEscalatedRow{
		TaskID:     r.TaskID.String(),
		Title:      r.Title,
		DetailHref: productTaskDetailPath(productID, r.TaskID),
		ByLine:     escalatedByLine(r),
		Milestone:  r.DeliveryRef.Title,
		Lane:       string(r.Lane),
		Reason:     string(r.Reason),
		// Use the attempt cap, never the escalation's CapValue, which for a
		// thrash-cap escalation is the thrash cap.
		Attempts:            taskAttemptsLabel(r.AttemptCount),
		EscalatedAt:         r.EscalatedAt.UTC().Format(time.RFC3339),
		EscalatedAtRelative: relativeTime(r.EscalatedAt, now),
		EscalationID:        observedEscalationID(r),
		Actions:             escalatedRowActions(r.TaskID.String(), r.Title, observedEscalationID(r), r.Lane, returnTo),
		Popovers:            escalatedRowPopovers(r.TaskID.String(), r.Title, r.Lane, returnTo),
	}
}

// escalatedGuardField matches api's ExpectedEscalationID field name.
const escalatedGuardField = "expected_escalation_id"

// escalatedRowActions offers Requeue, and Cancel where legal; never Release,
// since an escalated task holds no claim. Each carries the observed
// escalation id so a changed escalation is refused.
func escalatedRowActions(taskID, title, escalationID string, lane store.Lane, returnTo string) templ.Component {
	return renderEscalatedTaskActions(taskID, title, escalationID, returnTo,
		legalInterventions(taskInterventionEscalated, lane)...)
}

// escalatedRowPopovers renders the reason popovers for the same legal verbs,
// after the table.
func escalatedRowPopovers(taskID, title string, lane store.Lane, returnTo string) templ.Component {
	return renderTaskActionPopovers(taskID, title, returnTo,
		legalInterventions(taskInterventionEscalated, lane)...)
}

// observedEscalationID returns "" rather than a zero-UUID guard, which would
// refuse every action.
func observedEscalationID(r store.EscalatedTaskRow) string {
	if r.EscalationID == uuid.Nil {
		return ""
	}
	return r.EscalationID.String()
}

// escalatedByLine renders who escalated the task and for whom, omitting
// "for -" when there is no on-behalf-of.
func escalatedByLine(r store.EscalatedTaskRow) string {
	by := opsActor(r.EscalatedByActing)
	if of := opsSubject(r.EscalatedByOnBehalfOf); of != "-" {
		return "by " + by + " for " + of
	}
	return "by " + by
}

// needsAttentionEmptyData returns the filtered empty state only when the read
// is empty and a filter is in force; otherwise the tab's own empty state is
// correct.
func needsAttentionEmptyData(rows int, filter needsAttentionFilter, filterLabel, queue string) (pages.NeedsAttentionEmptyData, bool) {
	if rows > 0 || !filter.narrows() {
		return pages.NeedsAttentionEmptyData{}, false
	}
	return pages.NeedsAttentionEmptyData{
		Headline: "No " + queue + " match these filters.",
		Detail:   "Filtering by " + filterLabel + ". Clear a filter to see the whole queue.",
	}, true
}

// needsAttentionTabs builds the strip with per-tab counts read under the same
// filter as the lists. An unreadable count shows no badge rather than zero.
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

// needsAttentionCount reads one tab's count with the same params as its list
// read; each Count* shares its query with the matching List*.
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

// ---------------------------------------------------------------------------
// cancelled and open-notes row shapes
// ---------------------------------------------------------------------------

// Cancelled and Open notes row view-models, shared by the tabs and the ops
// console loaders.

// productTaskLinkOf is the product-scoped task link for a row, or "" when the
// read was not product-scoped.
func productTaskLinkOf(pid *uuid.UUID, tid uuid.UUID) string {
	if pid == nil {
		return ""
	}
	return productTaskDetailPath(*pid, tid)
}

// newCancelledRow builds one Cancelled tab row.
func newCancelledRow(r store.CancelledTaskRow, taskHref string) pages.CancelledRow {
	reason := ""
	if r.Reason != nil {
		reason = *r.Reason
	}
	return pages.CancelledRow{
		TaskID:     r.TaskID.String(),
		Title:      r.Title,
		TaskHref:   taskHref,
		Delivery:   string(r.DeliveryRef.Kind) + ": " + r.DeliveryRef.Title,
		Lane:       string(r.Lane),
		By:         opsActor(r.CancelledByActing),
		OnBehalfOf: opsSubject(r.CancelledByOnBehalfOf),
		At:         opsTime(r.CancelledAt),
		Reason:     reason,
	}
}

// newNoteRow builds one Open notes row. targetHref is set only for
// task-targeted notes, which have a page to link to.
func newNoteRow(r store.OpenNoteRow, taskHref string) pages.NoteRow {
	// task_note's CHECK guarantees exactly one target, so at most one branch
	// fills Target.
	target := "-"
	if r.TaskContext != nil {
		target = "task: " + r.TaskContext.Title + " (" + r.TaskContext.TaskID.String() + ")"
	} else if r.EntityContext != nil {
		target = string(r.EntityContext.EntityKind) + ": " + r.EntityContext.Title + " (" + r.EntityContext.EntityID.String() + ")"
	}
	return pages.NoteRow{
		NoteID:    r.NoteID.String(),
		Kind:      string(r.Kind),
		Status:    string(store.NoteLifecycleStatusNoted),
		Target:    target,
		TaskHref:  taskHref,
		CreatedAt: opsTime(r.CreatedAt),
		Body:      r.Body,
	}
}

// ---------------------------------------------------------------------------
// the Claimed tab's rows
// ---------------------------------------------------------------------------

// newClaimedRow builds one Claimed row from store.ClaimedTaskRow alone. A nil
// pid leaves the title unlinked.
func newClaimedRow(r store.ClaimedTaskRow, pid uuid.UUID, returnTo string) pages.ClaimedRow {
	// A zero claim id cannot guard a write, so the row carries none.
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
		Popovers:       claimedRowPopovers(r, returnTo),
	}
}

// claimedTaskHref is the product-scoped task URL, or "" for uuid.Nil.
func claimedTaskHref(pid, taskID uuid.UUID) string {
	if pid == uuid.Nil {
		return ""
	}
	return productTaskDetailPath(pid, taskID)
}

// claimedBy renders "by <acting> for <on-behalf-of>", dropping the second
// half when absent. The acting subject keeps its (human)/(service) kind.
func claimedBy(acting, onBehalfOf store.Subject) string {
	by := "by " + opsActor(acting)
	if forWhom := opsSubject(onBehalfOf); forWhom != "-" {
		by += " for " + forWhom
	}
	return by
}

// claimedRowActions renders the legal verbs for a claimed task, each guarded
// by the claim the row observed.
func claimedRowActions(r store.ClaimedTaskRow, returnTo string) templ.Component {
	return renderClaimedTaskActions(r.TaskID.String(), r.Title, r.ClaimID, returnTo,
		legalInterventions(taskInterventionClaimed, r.CurrentLane)...)
}

// claimedRowPopovers renders the reason popovers for the same legal verbs,
// after the table.
func claimedRowPopovers(r store.ClaimedTaskRow, returnTo string) templ.Component {
	return renderTaskActionPopovers(r.TaskID.String(), r.Title, returnTo,
		legalInterventions(taskInterventionClaimed, r.CurrentLane)...)
}
