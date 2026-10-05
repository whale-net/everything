// The ops console's four read views -- claimed tasks, escalated tasks,
// cancelled tasks, and open notes -- rendered inside the nav shell. Each
// page reads one scope-qualified page of a store console query: the same
// ListClaimedTasks/ListEscalatedTasks/ListCancelledTasks/ListOpenNotes the
// MCP ops mount and GET /console/* serve, over the same store/paging.go
// keyset-paging and scope-qualified-continuation-token contract.
//
// The view never re-implements the paging rule. It passes the caller's
// page_size/page_token straight through and renders whatever page (and
// next_token) the store returns, so a token minted in a different scope
// is rejected by store.DecodeContinuationToken exactly where it is for an
// MCP or api caller -- surfaced here as the same 400, never as a silently
// empty or wrong-scope page.
//
// The scope is the deployment's sole scope (ScopeStore.GetSole), never a
// value the browser supplies: a browser has no way to learn a scope id.
// Reads are role-gated (readerRoute: reader or operator), but -- unlike the
// write path in writes.go -- mint no krill session and need no operator
// identity.
//
// Each view is one route in two modes. This file holds the data half --
// the loaders that turn a store page into a view-model -- and the
// HX-Request branch; krill/ui/pages/ops.templ holds the rendering half.
// A request derives its view-model exactly once and both modes render it,
// so a fragment can never drift from the page it was swapped out of.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// The ops console's read-view routes, each hanging off the ops area root
// so navIsActive keeps the "Ops console" link marked across all of them.
const (
	opsClaimedPath   = opsPath + "/claimed"
	opsEscalatedPath = opsPath + "/escalated"
	opsCancelledPath = opsPath + "/cancelled"
	opsNotesPath     = opsPath + "/notes"
)

// The paging inputs each read view accepts, named exactly as the MCP
// tools and GET /console/* name them.
const (
	opsPageSizeParam  = "page_size"
	opsPageTokenParam = "page_token"
)

// parseOpsPageParams reads a view's paging inputs off the request. A
// non-numeric or negative page_size is the caller's error (400), matching
// api/handlers' parsePageSizeParam; the token is passed through verbatim
// for the store to validate against the scope.
func parseOpsPageParams(r *http.Request) (store.PageParams, error) {
	page := store.PageParams{ContinuationToken: r.URL.Query().Get(opsPageTokenParam)}
	raw := r.URL.Query().Get(opsPageSizeParam)
	if raw == "" {
		return page, nil
	}
	size, err := strconv.Atoi(raw)
	if err != nil || size < 0 {
		return page, errors.New("page_size: must be a non-negative integer")
	}
	page.PageSize = size
	return page, nil
}

// opsSelfPath is the view's own request URI -- path plus query -- which
// is what a view's poll and its manual Refresh must re-request.
//
// Not the bare route constant: an operator who has paged forward is on
// ?page_size=&page_token=, and a refresh that drops those would silently
// snap them back to page one. The poll re-reads from the store, so this
// also has to be the URI the handler itself was called with, not one
// rebuilt from the defaults.
func opsSelfPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "/"
	}
	return r.URL.RequestURI()
}

// soleScopeID resolves the one scope this deployment's seeder guarantees --
// the same resolution the write path's withKrillSession does -- so a view
// never takes a scope from the browser.
func (app *App) soleScopeID(ctx context.Context) (uuid.UUID, error) {
	scope, err := app.scopes.GetSole(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve scope: %w", err)
	}
	return scope.ID, nil
}

// writeOpsQueryError is writeConsoleQueryError for the ops console's own
// pages, which are titled "Ops console".
func (app *App) writeOpsQueryError(w http.ResponseWriter, r *http.Request, err error) {
	app.writeConsoleQueryError(w, r, "Ops console", err)
}

// writeConsoleQueryError maps a console query's store error onto the
// response: a cross-scope or malformed continuation token is the
// caller's error (400), never a genuine store failure (500).
//
// Neither branch hands the operator the store's own text. A sentinel
// error's message is a Go package-qualified string
// ("krill/store: continuation token was issued for a different scope"),
// which names an internal package and tells the operator nothing they can
// act on; a genuine failure can carry a connection string or an internal
// address. The operator gets krill's own wording either way, and on-call
// humans still get the real error from the logger.
//
// An htmx caller gets 200 with the message inline instead. htmx does not
// swap on a non-2xx, so a bare http.Error here would leave the operator
// looking at an unchanged table with no explanation -- and on the claimed
// view this path is reached by the always-on poll, so a database blip
// would silently freeze the console while the poll kept firing.
//
// A browser gets the same message inside the shell, at the status the
// error earns. A bare http.Error would answer text/plain with no nav, on
// exactly the page the operator most needs to navigate away from.
//
// title is the page's own name, so the in-shell answer names the page the
// operator was on rather than the console area it came from.
func (app *App) writeConsoleQueryError(w http.ResponseWriter, r *http.Request, title string, err error) {
	status, message := consoleQueryError(err)

	if isHtmxRequest(r) {
		renderFragment(w, r, pages.OpsInlineError(message))
		return
	}
	app.renderShellStatus(w, r, title, opsActivePath(r), pages.OpsQueryError(message, opsRecoveryPath(r)), status)
}

// consoleQueryError maps a console read's store error onto the status and
// the operator-facing message the response carries.
//
// A rejected continuation token is the caller's error (400), never a
// genuine store failure (500): the store names that rejection with three
// sentinels -- ErrTokenScopeMismatch for a token issued under another
// scope, ErrTokenFilterMismatch for one issued under a different filter
// set (the tab and its milestone/reason selections now bind the token), and
// ErrInvalidContinuationToken for one that does not decode at all. All three
// are the operator's own bad input and get the same wording, so the page
// does not tell them which internal check fired.
//
// Neither branch hands the operator the store's own text: a sentinel
// error's message is a Go package-qualified string naming an internal
// package, and a genuine failure can carry a connection string. The
// operator gets krill's own wording either way, and on-call humans still
// get the real error from the logger.
//
// It is a function rather than a branch inside the writer because a page
// with more than one response shape -- Needs attention's panel fragment,
// its results fragment, and its shell -- has to answer a failure in each
// of them, and the mapping must be one implementation rather than three
// that could disagree about which failures are the caller's.
func consoleQueryError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrTokenScopeMismatch),
		errors.Is(err, store.ErrTokenFilterMismatch),
		errors.Is(err, store.ErrInvalidContinuationToken):
		// The caller's own bad token; saying which input is wrong is
		// useful, and saying the store's package path is not.
		return http.StatusBadRequest,
			"This page's page_token is not valid for this view. Reload the view to start from the first page."
	default:
		logger.Error("failed to load an ops console view", "error", err)
		return http.StatusInternalServerError, "Failed to load console data. Try again."
	}
}

// opsActivePath is the path the nav marks active for a console page. The
// request's own path is what keeps the "Ops console" link lit across all
// four views, and opsPath is the fallback when a caller passed no request.
func opsActivePath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return opsPath
	}
	return r.URL.Path
}

// opsRecoveryPath is the way back to a working page from a rejected query:
// the view's own URI with the continuation token dropped. A rejected token
// is the one paging input that gets the operator back, and a store failure
// is no worse off without it.
func opsRecoveryPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return opsPath
	}
	q := r.URL.Query()
	if q.Get(opsPageTokenParam) == "" {
		return r.URL.RequestURI()
	}
	q.Del(opsPageTokenParam)
	if len(q) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + q.Encode()
}

// opsNextHref builds a view's "next page" link from the view's own
// request URI -- path plus query -- carrying the page size forward and the
// store-issued token as page_token. Empty when the page carried no token
// (the last page), so the view shows no next link.
//
// It takes the request URI rather than a bare route constant because the
// same builder serves the ops console's own pages and the Needs attention
// tabs, which live at different URLs and carry their own query (a tab, a
// page size). A link rebuilt from a route constant would silently drop
// whichever of those the URL had -- and following it would take the
// operator to another tab's first page rather than their own next one.
func opsNextHref(selfPath, nextToken string, pageSize int) string {
	if nextToken == "" {
		return ""
	}
	u, err := url.Parse(selfPath)
	if err != nil || u.Path == "" {
		u = &url.URL{Path: selfPath}
	}
	q := u.Query()
	if pageSize > 0 {
		q.Set(opsPageSizeParam, strconv.Itoa(pageSize))
	} else {
		// An absent or zero page size is the store's default, which the
		// link spells by leaving the parameter out rather than by writing
		// "page_size=0" -- a value the parser reads as the same default
		// but a reader would misread as a page of no rows.
		q.Del(opsPageSizeParam)
	}
	q.Set(opsPageTokenParam, nextToken)
	u.RawQuery = q.Encode()
	return u.RequestURI()
}

// isHtmxRequest reports whether this request came from htmx rather than a
// full page load. It is the one branch every read view and every
// intervention takes: the htmx half answers 200 with a bare fragment, the
// browser half answers with the shell.
func isHtmxRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") != ""
}

// Display helpers. Subjects and timestamps are pre-formatted here so the
// templates stay free of pointer/reach-into-store logic.
//
// opsTime is load-bearing beyond display: the claimed view's results
// block is polled, and a timestamp that shifted between two polls for no
// state change would make the fragment non-byte-stable. Absolute RFC3339
// only, never a relative "in 3m".

// opsSubject renders one subject triple readably. An unset subject (a
// claim taken with no on-behalf-of, or a cancelled-by that is itself the
// acting identity) renders as "-" rather than a bare space, so an empty
// cell is visibly empty and not a formatting bug.
func opsSubject(s store.Subject) string {
	if s.Iss == "" && s.Sub == "" {
		return "-"
	}
	return s.Iss + " " + s.Sub
}

// opsActor renders the acting subject with its kind ("human"/"service"),
// which is the part an operator scans for first -- a swarm-operator UI
// where a claim reads as a human acting for a service reads wrong.
func opsActor(s store.Subject) string {
	base := opsSubject(s)
	if base == "-" || s.Kind == "" {
		return base
	}
	return base + " (" + string(s.Kind) + ")"
}

func opsTime(t time.Time) string { return t.Format(time.RFC3339) }

// ---------------------------------------------------------------------------
// claimed view
// ---------------------------------------------------------------------------

// claimedPollingHorizon is how close to expiry a lease has to be for the
// claimed view to keep polling.
//
// This must be a FRACTION of the lease, not the lease itself. ClaimTask
// sets lease_expires_at to now+DefaultLeaseDuration and HeartbeatTask
// resets it to now+DefaultLeaseDuration, so every row the store can
// return satisfies LeaseExpiresAt-now <= DefaultLeaseDuration. A horizon
// of a whole lease would therefore make this predicate `len(rows) > 0`:
// any deployment with a claimed task -- including a swarm that heartbeats
// forever and never actually nears expiry -- would re-query Postgres
// every 3 seconds indefinitely.
//
// A third of the lease is the point at which an operator who is watching
// a claim wants to see it resolve, while a healthy heartbeat (well
// inside the window) keeps the poll off.
const claimedPollingHorizon = store.DefaultLeaseDuration / 3

// claimedPollingDue reports whether any claim on this page is inside its
// lease's near-expiry window -- the one transient state the ops console
// watches. It is decided here, server-side, from the freshly-read store
// rows; the browser contributes nothing to it.
func claimedPollingDue(rows []store.ClaimedTaskRow, now time.Time) bool {
	for _, r := range rows {
		// A lease already past expiry has a negative remaining time and so
		// matches here too: the task is still claimed, the row really is
		// about to disappear, and the operator should see that happen
		// rather than stare at a stale claim.
		if r.LeaseExpiresAt.Sub(now) <= claimedPollingHorizon {
			return true
		}
	}
	return false
}

// claimedResults reads one page of the claimed-task view (FR4) and returns
// its view-model -- every currently-claimed task, its claim, and
// claimed-since, equivalent to list_claimed_tasks / GET /console/claimed.
//
// It is the single derivation of this view's data: the GET handler, the
// poll, and the intervention handlers' post-write re-derivation all call
// it, so none of them can render a view the others would not. The Needs
// attention page's Claimed tab calls it too, with the product narrowing
// that page is scoped to -- the tab and the console view are the same
// rows derived the same way, never two implementations that could
// disagree.
//
// filter is the read's own narrowing: empty for the ops console's
// scope-wide view, the current product for a Needs attention tab.
func (app *App) claimedResults(ctx context.Context, filter store.ConsoleFilter, page store.PageParams, selfPath string) (pages.ClaimedData, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return pages.ClaimedData{}, err
	}
	result, err := app.tasks.ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter, Page: page})
	if err != nil {
		return pages.ClaimedData{}, err
	}
	// The detail link each row carries is spelled against the product the
	// read was narrowed to; a read with no product narrowing (the console's
	// own scope-wide view) leaves the rows unlinked.
	pid := uuid.Nil
	if filter.ProductID != nil {
		pid = *filter.ProductID
	}
	rows := make([]pages.ClaimedRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = newClaimedRow(row, pid, opsClaimedPath)
	}
	return pages.ClaimedData{
		Rows:     rows,
		NextHref: opsNextHref(selfPath, result.NextToken, page.PageSize),
		Href:     selfPath,
		// The poll decision is judged against the same clock the view's
		// handler reads at, so a test can hold time still and pin the
		// fragment's poll attributes against the lease it was given.
		Polling: claimedPollingDue(result.Items, app.clock()),
	}, nil
}

// handleClaimedTasks serves the claimed-task view in both of its modes:
// the whole page in the shell, or -- when htmx asked, which is also how
// the view's own poll reaches it -- the bare results fragment at 200.
func (app *App) handleClaimedTasks(w http.ResponseWriter, r *http.Request) {
	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d, err := app.claimedResults(r.Context(), store.ConsoleFilter{}, page, opsSelfPath(r))
	if err != nil {
		app.writeOpsQueryError(w, r, err)
		return
	}
	if isHtmxRequest(r) {
		renderFragment(w, r, pages.ClaimedResults(d))
		return
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Claimed tasks", opsClaimedPath, pages.ClaimedPage(d))
}

// ---------------------------------------------------------------------------
// escalated view
// ---------------------------------------------------------------------------

// escalatedResults reads one page of the escalated-task view (FR5):
// every escalated task and its escalation_reason, equivalent to
// list_escalated_tasks / GET /console/escalated, under the same filter
// contract claimedResults documents.
//
// reason is the read's optional escalation-reason narrowing, the one
// narrowing ConsoleFilter does not carry (ListEscalatedTasksParams' own
// field). nil keeps every reason -- what the ops console's own view asks
// for, since it offers no reason control; the Needs attention page's
// Escalated tab passes the filter bar's selection.
func (app *App) escalatedResults(ctx context.Context, filter store.ConsoleFilter, reason *store.EscalationReason, page store.PageParams, selfPath string) (pages.EscalatedData, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return pages.EscalatedData{}, err
	}
	result, err := app.tasks.ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: filter, Reason: reason, Page: page})
	if err != nil {
		return pages.EscalatedData{}, err
	}
	rows := make([]pages.EscalatedRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = newEscalatedRow(row)
	}
	return pages.EscalatedData{
		Rows:     rows,
		NextHref: opsNextHref(selfPath, result.NextToken, page.PageSize),
		Href:     selfPath,
	}, nil
}

func newEscalatedRow(r store.EscalatedTaskRow) pages.EscalatedRow {
	counter := "-"
	if r.CounterValue != nil && r.CapValue != nil {
		counter = fmt.Sprintf("%d/%d", *r.CounterValue, *r.CapValue)
	}
	verdict := "-"
	if r.MostRecentVerdict != nil {
		verdict = string(*r.MostRecentVerdict)
	}
	return pages.EscalatedRow{
		TaskID:     r.TaskID.String(),
		Title:      r.Title,
		Delivery:   string(r.DeliveryRef.Kind) + ": " + r.DeliveryRef.Title,
		Reason:     string(r.Reason),
		Counter:    counter,
		Lane:       string(r.Lane),
		At:         opsTime(r.EscalatedAt),
		Actor:      opsActor(r.EscalatedByActing),
		OnBehalfOf: opsSubject(r.EscalatedByOnBehalfOf),
		Summary:    fmt.Sprintf("attempts %d / failing %d / notes %d", r.AttemptCount, r.FailingVerdictCount, r.NoteCount),
		Verdict:    verdict,
		// The escalated view is where a Swarm Operator recovers a task back
		// to claimable (requeue) or gives up on it (cancel). Escalate is
		// absent here -- the task is already escalated -- and release has no
		// active claim to force-close on this view.
		Actions: renderTaskActions(r.TaskID.String(), opsEscalatedPath, actionRequeue, actionCancel),
	}
}

// handleEscalatedTasks serves the escalated-task view in both of its modes.
func (app *App) handleEscalatedTasks(w http.ResponseWriter, r *http.Request) {
	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d, err := app.escalatedResults(r.Context(), store.ConsoleFilter{}, nil, page, opsSelfPath(r))
	if err != nil {
		app.writeOpsQueryError(w, r, err)
		return
	}
	if isHtmxRequest(r) {
		renderFragment(w, r, pages.EscalatedResults(d))
		return
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Escalated tasks", opsEscalatedPath, pages.EscalatedPage(d))
}

// ---------------------------------------------------------------------------
// cancelled view
// ---------------------------------------------------------------------------

// cancelledResults reads one page of the cancelled-task view (FR10),
// equivalent to list_cancelled_tasks / GET /console/cancelled, under the
// same filter contract claimedResults documents.
func (app *App) cancelledResults(ctx context.Context, filter store.ConsoleFilter, page store.PageParams, selfPath string) (pages.CancelledData, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return pages.CancelledData{}, err
	}
	result, err := app.tasks.ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: filter, Page: page})
	if err != nil {
		return pages.CancelledData{}, err
	}
	rows := make([]pages.CancelledRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = newCancelledRow(row, productTaskLinkOf(filter.ProductID, row.TaskID))
	}
	return pages.CancelledData{
		Rows:     rows,
		NextHref: opsNextHref(selfPath, result.NextToken, page.PageSize),
		Href:     selfPath,
	}, nil
}

// handleCancelledTasks serves the cancelled-task view in both of its modes.
func (app *App) handleCancelledTasks(w http.ResponseWriter, r *http.Request) {
	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d, err := app.cancelledResults(r.Context(), store.ConsoleFilter{}, page, opsSelfPath(r))
	if err != nil {
		app.writeOpsQueryError(w, r, err)
		return
	}
	if isHtmxRequest(r) {
		renderFragment(w, r, pages.CancelledResults(d))
		return
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Cancelled tasks", opsCancelledPath, pages.CancelledPage(d))
}

// ---------------------------------------------------------------------------
// open-notes view
// ---------------------------------------------------------------------------

// openNotesResults reads one page of the open-notes view (FR12): every
// task note still in an open lifecycle status, equivalent to
// list_open_notes / GET /console/notes, under the same filter contract
// claimedResults documents.
func (app *App) openNotesResults(ctx context.Context, filter store.ConsoleFilter, page store.PageParams, selfPath string) (pages.NotesData, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return pages.NotesData{}, err
	}
	result, err := app.tasks.ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter, Page: page})
	if err != nil {
		return pages.NotesData{}, err
	}
	rows := make([]pages.NoteRow, len(result.Items))
	for i, row := range result.Items {
		href := ""
		if row.TaskContext != nil {
			href = productTaskLinkOf(filter.ProductID, row.TaskContext.TaskID)
		}
		rows[i] = newNoteRow(row, href)
	}
	return pages.NotesData{
		Rows:     rows,
		NextHref: opsNextHref(selfPath, result.NextToken, page.PageSize),
		Href:     selfPath,
	}, nil
}

// handleOpenNotes serves the open-notes view in both of its modes.
func (app *App) handleOpenNotes(w http.ResponseWriter, r *http.Request) {
	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d, err := app.openNotesResults(r.Context(), store.ConsoleFilter{}, page, opsSelfPath(r))
	if err != nil {
		app.writeOpsQueryError(w, r, err)
		return
	}
	if isHtmxRequest(r) {
		renderFragment(w, r, pages.NotesResults(d))
		return
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Open notes", opsNotesPath, pages.NotesPage(d))
}
