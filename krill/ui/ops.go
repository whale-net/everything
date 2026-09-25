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
// Reads are ungated (NFR6's gate is write-only), so -- unlike the write
// path in writes.go -- no krill session is minted and no operator identity
// is required beyond sign-in.
package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
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

// writeOpsQueryError maps a console query's store error onto an HTML page,
// mirroring api/handlers' writeConsoleQueryError: a cross-scope or
// malformed continuation token is the caller's error (400), never a
// genuine store failure (500).
func writeOpsQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrTokenScopeMismatch), errors.Is(err, store.ErrInvalidContinuationToken):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		logger.Error("failed to load an ops console view", "error", err)
		http.Error(w, "failed to load console data", http.StatusInternalServerError)
	}
}

// opsPage is the furniture every read view renders inside the shell: the
// view's heading and one-line description, its own rendered table, and a
// "Next page" link present exactly when the store's page carried a
// continuation token.
type opsPage struct {
	Heading     string
	Description string
	Table       template.HTML
	NextHref    string
}

var opsPageTemplate = template.Must(template.New("opspage").Parse(`<h2>{{.Heading}}</h2>
<p>{{.Description}}</p>
{{.Table}}{{if .NextHref}}<p><a href="{{.NextHref}}">Next page &rarr;</a></p>{{end}}`))

// opsNextHref builds a view's "next page" link, carrying the page size
// forward and the store-issued token as page_token. Empty when the page
// carried no token (the last page), so the view shows no next link.
func opsNextHref(path, nextToken string, pageSize int) string {
	if nextToken == "" {
		return ""
	}
	q := url.Values{}
	if pageSize > 0 {
		q.Set(opsPageSizeParam, strconv.Itoa(pageSize))
	}
	q.Set(opsPageTokenParam, nextToken)
	return path + "?" + q.Encode()
}

// Display helpers. Subjects and timestamps are pre-formatted here so the
// templates stay free of pointer/reach-into-store logic.

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

// handleClaimedTasks renders the claimed-task console view (FR4): every
// currently-claimed task, its claim, and claimed-since, equivalent to
// list_claimed_tasks / GET /console/claimed.
func (app *App) handleClaimedTasks(w http.ResponseWriter, r *http.Request) {
	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scopeID, err := app.soleScopeID(r.Context())
	if err != nil {
		writeOpsQueryError(w, err)
		return
	}
	result, err := app.tasks.ListClaimedTasks(r.Context(), store.ListClaimedTasksParams{ScopeID: scopeID, Page: page})
	if err != nil {
		writeOpsQueryError(w, err)
		return
	}
	rows := make([]claimedRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = newClaimedRow(row)
	}
	renderShell(w, r, "Claimed tasks", opsClaimedPath, renderPage(opsPageTemplate, opsPage{
		Heading:     "Claimed tasks",
		Description: "Every task that currently holds a claim, with its claimant, lane, and lease expiry.",
		Table:       renderPage(claimedTableTemplate, rows),
		NextHref:    opsNextHref(opsClaimedPath, result.NextToken, page.PageSize),
	}))
}

type claimedRow struct {
	TaskID     string
	Title      string
	Delivery   string
	Session    string
	Claimant   string
	OnBehalfOf string
	Lane       string
	Lease      string
	Attempts   int
}

func newClaimedRow(r store.ClaimedTaskRow) claimedRow {
	return claimedRow{
		TaskID:     r.TaskID.String(),
		Title:      r.Title,
		Delivery:   string(r.DeliveryRef.Kind) + ": " + r.DeliveryRef.Title,
		Session:    r.ClaimantSessionID.String(),
		Claimant:   opsActor(r.ClaimantActing),
		OnBehalfOf: opsSubject(r.ClaimantOnBehalfOf),
		Lane:       string(r.CurrentLane),
		Lease:      opsTime(r.LeaseExpiresAt),
		Attempts:   r.AttemptCount,
	}
}

var claimedTableTemplate = template.Must(template.New("claimed").Parse(`<table>
<thead><tr><th>Task</th><th>Delivery</th><th>Claimant</th><th>On behalf of</th><th>Lane</th><th>Lease expires</th><th>Attempts</th></tr></thead>
<tbody>
{{range .}}<tr><td>{{.TaskID}}<br>{{.Title}}</td><td>{{.Delivery}}</td><td>{{.Claimant}}<br><small>session {{.Session}}</small></td><td>{{.OnBehalfOf}}</td><td>{{.Lane}}</td><td>{{.Lease}}</td><td>{{.Attempts}}</td></tr>
{{else}}<tr><td colspan="7">No claimed tasks.</td></tr>
{{end}}</tbody></table>`))

// handleEscalatedTasks renders the escalated-task console view (FR5): every
// escalated task and its escalation_reason, equivalent to
// list_escalated_tasks / GET /console/escalated.
func (app *App) handleEscalatedTasks(w http.ResponseWriter, r *http.Request) {
	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scopeID, err := app.soleScopeID(r.Context())
	if err != nil {
		writeOpsQueryError(w, err)
		return
	}
	result, err := app.tasks.ListEscalatedTasks(r.Context(), store.ListEscalatedTasksParams{ScopeID: scopeID, Page: page})
	if err != nil {
		writeOpsQueryError(w, err)
		return
	}
	rows := make([]escalatedRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = newEscalatedRow(row)
	}
	renderShell(w, r, "Escalated tasks", opsEscalatedPath, renderPage(opsPageTemplate, opsPage{
		Heading:     "Escalated tasks",
		Description: "Every task with an active escalation, and why it escalated.",
		Table:       renderPage(escalatedTableTemplate, rows),
		NextHref:    opsNextHref(opsEscalatedPath, result.NextToken, page.PageSize),
	}))
}

type escalatedRow struct {
	TaskID     string
	Title      string
	Delivery   string
	Reason     string
	Counter    string
	Lane       string
	At         string
	Actor      string
	OnBehalfOf string
	Summary    string
	Verdict    string
}

func newEscalatedRow(r store.EscalatedTaskRow) escalatedRow {
	counter := "-"
	if r.CounterValue != nil && r.CapValue != nil {
		counter = fmt.Sprintf("%d/%d", *r.CounterValue, *r.CapValue)
	}
	verdict := "-"
	if r.MostRecentVerdict != nil {
		verdict = string(*r.MostRecentVerdict)
	}
	return escalatedRow{
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
	}
}

var escalatedTableTemplate = template.Must(template.New("escalated").Parse(`<table>
<thead><tr><th>Task</th><th>Delivery</th><th>Reason</th><th>Counter/cap</th><th>Lane</th><th>Escalated</th><th>By</th><th>On behalf of</th><th>Summary</th><th>Last verdict</th></tr></thead>
<tbody>
{{range .}}<tr><td>{{.TaskID}}<br>{{.Title}}</td><td>{{.Delivery}}</td><td>{{.Reason}}</td><td>{{.Counter}}</td><td>{{.Lane}}</td><td>{{.At}}</td><td>{{.Actor}}</td><td>{{.OnBehalfOf}}</td><td>{{.Summary}}</td><td>{{.Verdict}}</td></tr>
{{else}}<tr><td colspan="10">No escalated tasks.</td></tr>
{{end}}</tbody></table>`))

// handleCancelledTasks renders the cancelled-task console view (FR10),
// equivalent to list_cancelled_tasks / GET /console/cancelled.
func (app *App) handleCancelledTasks(w http.ResponseWriter, r *http.Request) {
	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scopeID, err := app.soleScopeID(r.Context())
	if err != nil {
		writeOpsQueryError(w, err)
		return
	}
	result, err := app.tasks.ListCancelledTasks(r.Context(), store.ListCancelledTasksParams{ScopeID: scopeID, Page: page})
	if err != nil {
		writeOpsQueryError(w, err)
		return
	}
	rows := make([]cancelledRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = newCancelledRow(row)
	}
	renderShell(w, r, "Cancelled tasks", opsCancelledPath, renderPage(opsPageTemplate, opsPage{
		Heading:     "Cancelled tasks",
		Description: "Every cancelled (dead-lettered) task, and who cancelled it.",
		Table:       renderPage(cancelledTableTemplate, rows),
		NextHref:    opsNextHref(opsCancelledPath, result.NextToken, page.PageSize),
	}))
}

type cancelledRow struct {
	TaskID     string
	Title      string
	Delivery   string
	By         string
	OnBehalfOf string
	At         string
}

func newCancelledRow(r store.CancelledTaskRow) cancelledRow {
	return cancelledRow{
		TaskID:     r.TaskID.String(),
		Title:      r.Title,
		Delivery:   string(r.DeliveryRef.Kind) + ": " + r.DeliveryRef.Title,
		By:         opsActor(r.CancelledByActing),
		OnBehalfOf: opsSubject(r.CancelledByOnBehalfOf),
		At:         opsTime(r.CancelledAt),
	}
}

var cancelledTableTemplate = template.Must(template.New("cancelled").Parse(`<table>
<thead><tr><th>Task</th><th>Delivery</th><th>Cancelled by</th><th>On behalf of</th><th>Cancelled at</th></tr></thead>
<tbody>
{{range .}}<tr><td>{{.TaskID}}<br>{{.Title}}</td><td>{{.Delivery}}</td><td>{{.By}}</td><td>{{.OnBehalfOf}}</td><td>{{.At}}</td></tr>
{{else}}<tr><td colspan="5">No cancelled tasks.</td></tr>
{{end}}</tbody></table>`))

// handleOpenNotes renders the open-notes console view (FR12): every task
// note still in an open lifecycle status, equivalent to list_open_notes /
// GET /console/notes.
func (app *App) handleOpenNotes(w http.ResponseWriter, r *http.Request) {
	page, err := parseOpsPageParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scopeID, err := app.soleScopeID(r.Context())
	if err != nil {
		writeOpsQueryError(w, err)
		return
	}
	result, err := app.tasks.ListOpenNotes(r.Context(), store.ListOpenNotesParams{ScopeID: scopeID, Page: page})
	if err != nil {
		writeOpsQueryError(w, err)
		return
	}
	rows := make([]noteRow, len(result.Items))
	for i, row := range result.Items {
		rows[i] = newNoteRow(row)
	}
	renderShell(w, r, "Open notes", opsNotesPath, renderPage(opsPageTemplate, opsPage{
		Heading:     "Open notes",
		Description: "Every note still at 'noted', and the task or spec entity it targets.",
		Table:       renderPage(notesTableTemplate, rows),
		NextHref:    opsNextHref(opsNotesPath, result.NextToken, page.PageSize),
	}))
}

type noteRow struct {
	NoteID    string
	Kind      string
	Target    string
	CreatedAt string
	Body      string
}

func newNoteRow(r store.OpenNoteRow) noteRow {
	// Exactly one of TaskContext/EntityContext is set (task_note's own
	// exactly-one-target CHECK), so at most one branch fills Target. The
	// target's id is rendered next to its title so the row names the same
	// entity the note points at, not just its human label.
	target := "-"
	if r.TaskContext != nil {
		target = "task: " + r.TaskContext.Title + " (" + r.TaskContext.TaskID.String() + ")"
	} else if r.EntityContext != nil {
		target = string(r.EntityContext.EntityKind) + ": " + r.EntityContext.Title + " (" + r.EntityContext.EntityID.String() + ")"
	}
	return noteRow{
		NoteID:    r.NoteID.String(),
		Kind:      string(r.Kind),
		Target:    target,
		CreatedAt: opsTime(r.CreatedAt),
		Body:      r.Body,
	}
}

var notesTableTemplate = template.Must(template.New("notes").Parse(`<table>
<thead><tr><th>Note</th><th>Kind</th><th>Target</th><th>Created</th><th>Body</th></tr></thead>
<tbody>
{{range .}}<tr><td>{{.NoteID}}</td><td>{{.Kind}}</td><td>{{.Target}}</td><td>{{.CreatedAt}}</td><td>{{.Body}}</td></tr>
{{else}}<tr><td colspan="5">No open notes.</td></tr>
{{end}}</tbody></table>`))
