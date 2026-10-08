// The ops console's four read views (claimed, escalated, cancelled tasks and
// open notes). Paging inputs pass straight to the store, which rejects foreign
// tokens; the scope is always the deployment's sole scope, never the browser's.
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

// The read-view routes hang off the ops root so the nav link stays active.
const (
	opsClaimedPath   = opsPath + "/claimed"
	opsEscalatedPath = opsPath + "/escalated"
	opsCancelledPath = opsPath + "/cancelled"
	opsNotesPath     = opsPath + "/notes"
)

// The paging inputs, named as the MCP tools and GET /console/* name them.
const (
	opsPageSizeParam  = "page_size"
	opsPageTokenParam = "page_token"
)

// parseOpsPageParams reads paging inputs; a bad page_size is a 400 and the
// token passes through for the store to validate.
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

// opsSelfPath is the view's request URI (path plus query), so polling and
// Refresh keep the operator on the page they paged to.
func opsSelfPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "/"
	}
	return r.URL.RequestURI()
}

// soleScopeID resolves the deployment's one scope, so a view never takes a
// scope from the browser.
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

// writeConsoleQueryError writes a console read failure: inline at 200 for htmx
// (which ignores non-2xx), in the shell otherwise. Store text is never shown;
// the real error goes to the log.
func (app *App) writeConsoleQueryError(w http.ResponseWriter, r *http.Request, title string, err error) {
	status, message := consoleQueryError(err)

	if isHtmxRequest(r) {
		renderFragment(w, r, pages.OpsInlineError(message))
		return
	}
	app.renderShellStatus(w, r, title, opsActivePath(r), pages.OpsQueryError(message, opsRecoveryPath(r)), status)
}

// consoleQueryError maps a store error to a status and operator message. Any
// rejected token is a 400 with one wording; other failures are 500. Shared by
// every response shape so the mapping cannot diverge.
func consoleQueryError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrTokenScopeMismatch),
		errors.Is(err, store.ErrTokenFilterMismatch),
		errors.Is(err, store.ErrInvalidContinuationToken):
		// Name the bad input, not the store's package path.
		return http.StatusBadRequest,
			"This page's page_token is not valid for this view. Reload the view to start from the first page."
	default:
		logger.Error("failed to load an ops console view", "error", err)
		return http.StatusInternalServerError, "Failed to load console data. Try again."
	}
}

// opsActivePath is the request path the nav marks active, or opsPath when
// there is no request.
func opsActivePath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return opsPath
	}
	return r.URL.Path
}

// opsRecoveryPath is the view's URI with the continuation token dropped.
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

// opsNextHref builds the next-page link from the view's own URI, keeping its
// query (tab, page size). Empty on the last page.
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
		// A default page size is left out rather than written as page_size=0.
		q.Del(opsPageSizeParam)
	}
	q.Set(opsPageTokenParam, nextToken)
	u.RawQuery = q.Encode()
	return u.RequestURI()
}

// isHtmxRequest reports an htmx request, answered with a bare 200 fragment
// instead of the shell.
func isHtmxRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") != ""
}

// Display helpers. opsTime is absolute RFC3339 only, so the polled claimed
// fragment stays byte-stable between polls.

// opsSubject renders a subject triple; an unset one renders as "-".
func opsSubject(s store.Subject) string {
	if s.Iss == "" && s.Sub == "" {
		return "-"
	}
	return s.Iss + " " + s.Sub
}

// opsActor renders the acting subject with its kind (human/service).
func opsActor(s store.Subject) string {
	base := opsSubject(s)
	if base == "-" || s.Kind == "" {
		return base
	}
	return base + " (" + string(s.Kind) + ")"
}

func opsTime(t time.Time) string { return t.Format(time.RFC3339) }

// claimed view

// claimedPollingHorizon must be a fraction of the lease: every claimed row is
// within one lease of expiry, so a whole-lease horizon would poll forever.
const claimedPollingHorizon = store.DefaultLeaseDuration / 3

// claimedPollingDue reports whether any claim is near lease expiry, decided
// server-side from fresh rows.
func claimedPollingDue(rows []store.ClaimedTaskRow, now time.Time) bool {
	for _, r := range rows {
		// An already-expired lease matches too, so the operator sees it disappear.
		if r.LeaseExpiresAt.Sub(now) <= claimedPollingHorizon {
			return true
		}
	}
	return false
}

// claimedResults is the single derivation of the claimed view, used by the
// handler, the poll, post-write re-renders and Needs attention. selfPath is
// each row action's return_to, so interventions land back on the same view.
func (app *App) claimedResults(ctx context.Context, filter store.ConsoleFilter, page store.PageParams, selfPath string) (pages.ClaimedData, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return pages.ClaimedData{}, err
	}
	result, err := app.tasks.ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter, Page: page})
	if err != nil {
		return pages.ClaimedData{}, err
	}
	// Rows link to detail only when the read was narrowed to a product.
	pid := uuid.Nil
	if filter.ProductID != nil {
		pid = *filter.ProductID
	}
	rows := make([]pages.ClaimedRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = newClaimedRow(row, pid, selfPath)
	}
	return pages.ClaimedData{
		Rows:     rows,
		NextHref: opsNextHref(selfPath, result.NextToken, page.PageSize),
		Href:     selfPath,
		// Uses app.clock so tests can pin the poll decision.
		Polling: claimedPollingDue(result.Items, app.clock()),
	}, nil
}

// handleClaimedTasks serves the claimed view as a full page, or as a bare
// fragment for htmx and the poll.
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

// escalated view

// escalatedResults reads one page of the escalated view. reason optionally
// narrows by escalation reason; nil keeps every reason.
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
		// Escalated tasks can be requeued or cancelled; escalate and release do not apply.
		Actions: renderTaskActions(r.TaskID.String(), r.Title, opsEscalatedPath, actionRequeue, actionCancel),
		// Reason popovers render after the table so no row contains a text input.
		Popovers: renderTaskActionPopovers(r.TaskID.String(), r.Title, opsEscalatedPath, actionRequeue, actionCancel),
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

// cancelled view

// cancelledResults reads one page of the cancelled-task view.
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

// open-notes view

// openNotesResults reads one page of task notes still in an open status.
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
