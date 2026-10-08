// Console task interventions: release, requeue, escalate, cancel. Each forwards
// the optional reason to the same krill api endpoint the MCP tools use, under a
// krill session minted for the operator; scope and subjects never come from the form.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// The four intervention verbs, derived from store.InterventionAction* so the
// posted path segment is the operation the api performs.
const (
	actionRelease  = string(store.InterventionActionRelease)
	actionRequeue  = string(store.InterventionActionRequeue)
	actionEscalate = string(store.InterventionActionEscalate)
	actionCancel   = string(store.InterventionActionCancel)
)

// interventionAction is one verb's console affordance: label, reason prompt,
// and whether it needs a confirmation step.
type interventionAction struct {
	Label string
	// ReasonHint is the placeholder and aria-label for the optional reason field.
	ReasonHint string
	// Destructive marks a verb that posts only after explicit confirmation; only
	// cancel, which cannot be undone.
	Destructive bool
}

var interventionActions = map[string]interventionAction{
	actionRelease:  {Label: "Release", ReasonHint: "why release the claim? (optional)"},
	actionRequeue:  {Label: "Requeue", ReasonHint: "why safe to retry? (optional)"},
	actionEscalate: {Label: "Escalate", ReasonHint: "why does it need attention? (optional)"},
	actionCancel:   {Label: "Cancel", ReasonHint: "why dead-letter it? (optional)", Destructive: true},
}

// actionLabel is a verb's label, falling back to the raw verb.
func actionLabel(action string) string {
	if a, ok := interventionActions[action]; ok {
		return a.Label
	}
	return action
}

// opsTaskActionBase prefixes per-task intervention routes:
// "POST "+opsTaskActionBase+"{id}/"+verb.
const opsTaskActionBase = opsPath + "/tasks/"

// cancelConfirmSuffix builds the confirmation page a cancel goes through.
const cancelConfirmSuffix = "/" + actionCancel + "/confirm"

// taskInterventionRequest mirrors the api's intervention request bodies: an
// optional reason plus the observed-state guard. Scope and subjects come from
// the session.
type taskInterventionRequest struct {
	Reason *string `json:"reason"`

	// ExpectedClaimID and ExpectedEscalationID are the ids the acting row saw, so
	// the api refuses an intervention whose state changed. Omitted when none observed.
	ExpectedClaimID      *string `json:"expected_claim_id,omitempty"`
	ExpectedEscalationID *string `json:"expected_escalation_id,omitempty"`
}

// handleTaskIntervention handles one verb's POST. Behind operatorRoute, so the
// operator's Subject is on the context. htmx requests get the re-derived view at
// 200; plain posts get Post/Redirect/Get.
func (app *App) handleTaskIntervention(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := interventionActions[action]; !ok {
			// Only the four literal verbs are registered; anything else is a wiring bug.
			http.NotFound(w, r)
			return
		}
		taskID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid task id: must be a UUID", http.StatusBadRequest)
			return
		}

		// Resolve return_to before the write; it picks both the redirect and the view
		// re-derived for htmx.
		returnTo := interventionReturnTo(r)

		// An empty reason is sent as null, which the api treats as omitted.
		req := taskInterventionRequest{}
		if reason := strings.TrimSpace(r.FormValue("reason")); reason != "" {
			req.Reason = &reason
		}

		// The guard comes from the row the table rendered, never typed input. An empty
		// value is an omitted guard, not an all-zero id.
		if v := strings.TrimSpace(r.FormValue(expectedClaimIDParam)); v != "" {
			req.ExpectedClaimID = &v
		}
		if v := strings.TrimSpace(r.FormValue(escalatedGuardField)); v != "" {
			req.ExpectedEscalationID = &v
		}

		card := cancelConfirmData(taskID.String(), returnTo, "", cancelObservedFrom(r))

		var resp *http.Response
		if err := app.withKrillSession(r.Context(), func(ctx context.Context, sessionID store.SessionID) error {
			var err error
			resp, err = app.writes.Write(ctx, sessionID, http.MethodPost, "tasks/"+taskID.String()+"/"+action, req)
			return err
		}); err != nil {
			// The write never reached krill api, so nothing is attributed. Answer like a
			// refusal so the nav stays; details are logged, not shown.
			logger.Error("failed to issue an operator write", "error", err)
			app.writeInterventionRefusal(w, r, taskID, action, returnTo,
				interventionNotIssuedRefusal(), interventionNotIssuedStatus(err))
			return
		}

		// On success, plain posts redirect back to the acting view so a refresh cannot
		// replay the write; htmx re-derives it in place.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close() //nolint:errcheck
			if isHtmxRequest(r) {
				app.renderInterventionSuccess(w, r, taskID, action, card)
				return
			}
			// A 303 has no body, so the message rides the flash cookie.
			flashSuccess(w, interventionSuccessMessage(action))
			http.Redirect(w, r, returnTo, http.StatusSeeOther)
			return
		}

		// Map api's status and store-sentinel message into operator wording. no-JS
		// shows a shell page at that status; htmx shows it inline at 200.
		status, message := interventionRejection(resp)
		refusal := interventionRefusalOf(status, message)
		logInterventionRefusal(action, status, refusal, message)

		// A refusal posted from the cancel confirm card re-renders the card from fresh
		// state, never from the refused ids. Other origins re-derive their own view.
		if isHtmxRequest(r) && action == actionCancel && !cancelRefusalFromTheRow(r) && cancelRefusalFromTheCard(r) {
			if fresh, ok := app.freshCancelConfirmData(r.Context(), taskID, returnTo, refusal.message(action)); ok {
				renderFragment(w, r, pages.CancelConfirmCard(fresh))
				return
			}
			// Fresh state could not be read, so fall through to the shared reload warning
			// rather than guess at a guard.
		}
		app.writeInterventionRefusal(w, r, taskID, action, returnTo, refusal, status)
	}
}

// renderInterventionSuccess answers a successful htmx intervention at 200 by
// re-deriving the acting view. Cancel instead HX-Redirects from the confirm
// card to return_to, since the card has no row left to swap.
func (app *App) renderInterventionSuccess(w http.ResponseWriter, r *http.Request, taskID uuid.UUID, action string, card pages.CancelConfirmData) {
	message := interventionSuccessMessage(action)
	if action == actionCancel {
		// HX-Redirect is a full page load, so the message goes via the flash cookie.
		flashSuccess(w, message)
		w.Header().Set("HX-Redirect", card.ReturnTo)
		renderFragment(w, r, pages.CancelConfirmCard(card))
		return
	}
	app.renderInterventionResults(w, r, taskID, card.ReturnTo, nil, action, message)
}

// interventionSuccessMessage is a verb's success toast; the wording for the
// in-place verbs is specified.
func interventionSuccessMessage(action string) string {
	label := strings.ToLower(actionLabel(action))
	switch action {
	case actionRelease:
		return "Claim released"
	case actionRequeue:
		return "Task requeued"
	case actionEscalate:
		return "Task escalated"
	case actionCancel:
		return "Task cancelled."
	default:
		return "krill " + label + " applied."
	}
}

// interventionReloadFailure is the success path's message when the view could
// not be rebuilt. Refusals use their own wording so they never imply success.
const interventionReloadFailure = "The intervention was applied, but this view could not be reloaded."

// renderInterventionResults re-reads the view named by return_to (a Needs
// attention tab or task detail) and writes it at 200, using the same loader a
// GET would. A refusal shows inline; a success shows as a toast.
func (app *App) renderInterventionResults(w http.ResponseWriter, r *http.Request, taskID uuid.UUID, returnTo string, refusal *interventionRefusal, action, toast string) {
	reloadFailure := interventionReloadFailure
	message := ""
	if refusal != nil {
		reloadFailure = refusal.reloadFailure(action)
		message = refusal.message(action)
	}
	ctx := r.Context()
	if u, err := url.Parse(returnTo); err == nil {
		if pid, tid, ok := productTaskDetailIDsOfPath(u.Path); ok {
			if tid != taskID {
				// A return_to naming another task would render, and expose controls for, a
				// task this write never touched.
				logger.Warn("intervention: return_to names a task other than the one acted on; rendering the reload warning",
					"acted_on", taskID.String(), "return_to", returnTo)
				renderFragment(w, r, withToast(toast, pages.OpsInlineError(reloadFailure)))
				return
			}
			app.renderTaskDetailRegion(w, r, pid, tid, u, message, reloadFailure, toast)
			return
		}
	}
	target, ok := app.interventionReturnTargetOf(r, returnTo)
	if !ok {
		// A same-origin path with no view behind it: keep the swap target with the
		// reload warning.
		logger.Warn("intervention: return_to names no view; rendering the reload warning", "return_to", returnTo)
		renderFragment(w, r, withToast(toast, pages.OpsInlineError(reloadFailure)))
		return
	}
	// Build the filter sentence the same way the tab's GET does.
	containers := app.needsAttentionMilestoneContainers(ctx, target.productID)
	view, err := app.needsAttentionResults(ctx, target.productID, target.filter,
		needsAttentionFilterSentence(target.filter, containers), target.tab,
		target.page, target.selfPath, app.clock(), message)
	if err != nil {
		// A failed read must not render as an empty view (which would also stop the
		// claimed tab's poll).
		logger.Error("failed to reload the needs attention view after an intervention", "error", err)
		renderFragment(w, r, withToast(toast, pages.OpsInlineError(reloadFailure)))
		return
	}
	renderFragment(w, r, withToast(toast, view.Results))
}

// renderTaskDetailRegion answers a detail-page intervention by re-deriving the
// whole detail section, since badges, rail, and actions all change. self is the
// detail page's address from return_to, preserving tab and query.
func (app *App) renderTaskDetailRegion(w http.ResponseWriter, r *http.Request, pid, tid uuid.UUID, self *url.URL, message, reloadFailure, toast string) {
	ctx := r.Context()
	page, ok := app.reloadTaskDetail(ctx, r, pid, tid, self)
	if !ok {
		logger.Error("failed to reload the task detail after an intervention",
			"task", tid.String(), "product", pid.String())
		renderFragment(w, r, withToast(toast, pages.OpsInlineError(reloadFailure)))
		return
	}
	page.ActionError = message
	renderFragment(w, r, withToast(toast, pages.TaskDetail(page)))
}

// reloadTaskDetail re-reads the task and recomposes its detail section; ok is
// false when the task or its container can no longer be read.
func (app *App) reloadTaskDetail(ctx context.Context, r *http.Request, pid, tid uuid.UUID, self *url.URL) (pages.TaskDetailPage, bool) {
	task, err := app.tasks.GetTaskByID(ctx, tid)
	if err != nil {
		logger.Error("failed to re-read a task for a detail-page intervention", "task", tid.String(), "error", err)
		return pages.TaskDetailPage{}, false
	}
	c, ok := app.taskDetailContainerOf(ctx, pid, task)
	if !ok {
		return pages.TaskDetailPage{}, false
	}
	return app.taskDetailViewFor(ctx, r, pid, task, c, self), true
}

// interventionReturnTarget is the Needs attention view re-derived after an
// intervention: product, tab, filter, and page.
type interventionReturnTarget struct {
	productID uuid.UUID
	tab       string
	filter    needsAttentionFilter
	page      store.PageParams
	selfPath  string
}

// interventionReturnTargetOf resolves return_to to a Needs attention tab URL
// or a legacy /ops URL (mapped to its tab under the remembered product). ok is
// false when neither shape matches.
func (app *App) interventionReturnTargetOf(r *http.Request, returnTo string) (interventionReturnTarget, bool) {
	u, err := url.Parse(returnTo)
	if err != nil {
		return interventionReturnTarget{}, false
	}
	if pid, ok := needsAttentionProductOfPath(u.Path); ok {
		// Parse with the same functions the GET uses.
		req := &http.Request{URL: u}
		filter, err := parseNeedsAttentionFilter(req)
		if err != nil {
			return interventionReturnTarget{}, false
		}
		page, err := parseOpsPageParams(req)
		if err != nil {
			return interventionReturnTarget{}, false
		}
		return interventionReturnTarget{
			productID: pid,
			tab:       needsAttentionTabOf(req),
			filter:    filter,
			page:      page,
			selfPath:  returnTo,
		}, true
	}
	tab, ok := legacyOpsTab(u.Path)
	if !ok {
		return interventionReturnTarget{}, false
	}
	product, err := app.resolveProductForUnprefixed(r)
	if err != nil || product.ID == uuid.Nil {
		return interventionReturnTarget{}, false
	}
	page, err := parseOpsPageParams(&http.Request{URL: u})
	if err != nil {
		return interventionReturnTarget{}, false
	}
	return interventionReturnTarget{productID: product.ID, tab: tab, page: page, selfPath: returnTo}, true
}

// needsAttentionProductOfPath returns the product of an exact
// /products/{pid}/needs-attention path.
func needsAttentionProductOfPath(path string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(path, productsPath+"/")
	if !ok {
		return uuid.Nil, false
	}
	rawPID, suffix, ok := strings.Cut(rest, "/")
	if !ok || "/"+suffix != needsAttentionSuffix {
		return uuid.Nil, false
	}
	pid, err := uuid.Parse(rawPID)
	if err != nil {
		return uuid.Nil, false
	}
	return pid, true
}

// productTaskDetailOfPath returns the product of an exact
// /products/{pid}/tasks/{tid} path.
func productTaskDetailOfPath(path string) (uuid.UUID, bool) {
	pid, _, ok := productTaskDetailIDsOfPath(path)
	return pid, ok
}

// productTaskDetailIDsOfPath is productTaskDetailOfPath returning the task id too.
func productTaskDetailIDsOfPath(path string) (uuid.UUID, uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(path, productsPath+"/")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	rawPID, suffix, ok := strings.Cut(rest, "/")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	rawTID, ok := strings.CutPrefix(suffix, strings.TrimPrefix(tasksSuffix, "/")+"/")
	if !ok || rawTID == "" || strings.Contains(rawTID, "/") {
		return uuid.Nil, uuid.Nil, false
	}
	pid, err := uuid.Parse(rawPID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	tid, err := uuid.Parse(rawTID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	return pid, tid, true
}

// legacyOpsTab maps a retired ops console URL to its Needs attention tab.
func legacyOpsTab(path string) (string, bool) {
	switch path {
	case opsPath:
		return needsAttentionTabEscalated, true
	case opsClaimedPath:
		return needsAttentionTabClaimed, true
	case opsEscalatedPath:
		return needsAttentionTabEscalated, true
	case opsCancelledPath:
		return needsAttentionTabCancelled, true
	case opsNotesPath:
		return needsAttentionTabNotes, true
	default:
		return "", false
	}
}

// apiError mirrors api/handlers' {"error": "..."} rejection body.
type apiError struct {
	Error string `json:"error"`
}

// interventionRejection reads a rejected write's status and message, falling
// back to the status text when the body is not the expected shape.
func interventionRejection(resp *http.Response) (int, string) {
	defer resp.Body.Close() //nolint:errcheck
	status := resp.StatusCode
	var e apiError
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e); err != nil || e.Error == "" {
		if text := http.StatusText(status); text != "" {
			return status, text
		}
		return status, "the request was refused"
	}
	return status, e.Error
}

// interventionReturnTo reads return_to, honouring only /ops paths, a
// product-scoped Needs attention tab, or a product-scoped task detail; anything
// else falls back to the ops root.
func interventionReturnTo(r *http.Request) string {
	fallback := opsPath
	to := strings.TrimSpace(r.FormValue("return_to"))
	if to == "" {
		return fallback
	}
	// Reject absolute URLs, ".." in the decoded path (catching %2e%2e), and
	// non-segment prefix matches like "/opsarchive", so return_to resolves to a
	// view this binary serves.
	u, err := url.Parse(to)
	if err != nil || u.IsAbs() || u.Host != "" || u.Scheme != "" {
		return fallback
	}
	if strings.Contains(u.Path, "..") {
		return fallback
	}
	if u.Path != opsPath && !strings.HasPrefix(u.Path, opsPath+"/") {
		if _, ok := needsAttentionProductOfPath(u.Path); !ok {
			if _, ok := productTaskDetailOfPath(u.Path); !ok {
				return fallback
			}
		}
	}
	return to
}

// expectedClaimIDParam is the api's own wire name for the claim guard.
const expectedClaimIDParam = "expected_claim_id"

// renderTaskActions renders one row's intervention controls in the given
// order. Destructive verbs confirm first (hx-confirm, or a confirmation page
// without JS). title is what the confirmation names.
func renderTaskActions(taskID, title, returnTo string, actions ...string) templ.Component {
	return renderTaskActionsWith(taskActionOptions{}, taskID, title, returnTo, actions...)
}

// taskActionOptions adjusts taskActionControls; the zero value is a Needs
// attention row. Only the task detail sets them.
type taskActionOptions struct {
	// target is the region the controls swap; empty means the results block.
	target string
	// primary names the verb rendered as the primary action.
	primary string
}

func renderTaskActionsWith(opts taskActionOptions, taskID, title, returnTo string, actions ...string) templ.Component {
	return pages.TaskActions(taskActionControls(opts, taskID, title, returnTo, actions...))
}

// renderClaimedTaskActions is renderTaskActions guarded by the claim the row
// observed. A zero claim id renders unguarded controls.
func renderClaimedTaskActions(taskID, title string, claimID uuid.UUID, returnTo string, actions ...string) templ.Component {
	return renderClaimedTaskActionsWith(taskActionOptions{}, taskID, title, claimID, returnTo, actions...)
}

func renderClaimedTaskActionsWith(opts taskActionOptions, taskID, title string, claimID uuid.UUID, returnTo string, actions ...string) templ.Component {
	controls := taskActionControls(opts, taskID, title, returnTo, actions...)
	if claimID == uuid.Nil {
		return pages.TaskActions(controls)
	}
	observed := claimID.String()
	for i := range controls {
		// The no-JS half hands the guard to the confirmation page, which forwards it.
		controls[i].ObservedClaimID = observed
	}
	return pages.TaskActions(controls)
}

// renderEscalatedTaskActions is renderTaskActions guarded by the escalation
// the row observed. An empty id renders unguarded controls.
func renderEscalatedTaskActions(taskID, title, escalationID, returnTo string, actions ...string) templ.Component {
	return renderEscalatedTaskActionsWith(taskActionOptions{}, taskID, title, escalationID, returnTo, actions...)
}

func renderEscalatedTaskActionsWith(opts taskActionOptions, taskID, title, escalationID, returnTo string, actions ...string) templ.Component {
	controls := taskActionControls(opts, taskID, title, returnTo, actions...)
	if escalationID == "" {
		return pages.TaskActions(controls)
	}
	for i := range controls {
		controls[i].ObservedField = escalatedGuardField
		controls[i].ObservedID = escalationID
	}
	return pages.TaskActions(controls)
}

// renderTaskActionPopovers renders the reason popovers for the same controls
// as renderTaskActions. They render outside the table so no row contains a text
// input, and share ids with the row's forms.
func renderTaskActionPopovers(taskID, title, returnTo string, actions ...string) templ.Component {
	return pages.TaskActionPopovers(taskActionControls(taskActionOptions{}, taskID, title, returnTo, actions...))
}

// taskActionControls builds a row's verb controls. FormID, PopoverID, and
// ReasonID are pure functions of task id and verb so separately built triggers
// and popovers always agree.
func taskActionControls(opts taskActionOptions, taskID, title, returnTo string, actions ...string) []pages.TaskActionControl {
	controls := make([]pages.TaskActionControl, 0, len(actions))
	for _, action := range actions {
		a, ok := interventionActions[action]
		if !ok {
			continue
		}
		control := pages.TaskActionControl{
			Label:      a.Label,
			ReasonHint: a.ReasonHint,
			ReturnTo:   returnTo,
			Target:     opts.target,
			Primary:    action == opts.primary,
			FormID:     taskActionFormID(taskID, action),
			PopoverID:  taskActionPopoverID(taskID, action),
			ReasonID:   taskActionReasonID(taskID, action),
		}
		if a.Destructive {
			// Cancel's htmx half POSTs after hx-confirm; its no-JS half GETs the
			// confirmation page. Browsers drop a GET form's action query string, so
			// return_to and the guard ride hidden inputs.
			control.Kind = "confirm"
			control.Action = opsTaskActionBase + taskID + cancelConfirmSuffix
			control.PostAction = opsTaskActionBase + taskID + "/" + action
			control.Confirm = cancelConfirmMessage(title)
			control.SubmitLabel = "Confirm cancel"
		} else {
			control.Kind = "form"
			control.Action = opsTaskActionBase + taskID + "/" + action
			control.SubmitLabel = a.Label
		}
		controls = append(controls, control)
	}
	return controls
}

// IDs pairing one row control with its reason popover, namespaced by task id
// and verb so they never collide.
func taskActionFormID(taskID, action string) string {
	return "krill-action-form-" + taskID + "-" + action
}

func taskActionPopoverID(taskID, action string) string {
	return "krill-reason-popover-" + taskID + "-" + action
}

func taskActionReasonID(taskID, action string) string {
	return taskActionPopoverID(taskID, action) + "-reason"
}

// cancelConfirmMessage is the confirmation cancel shows: the task and the
// irreversible consequence.
func cancelConfirmMessage(title string) string {
	return "Cancel " + title + "? It moves to Cancelled and cannot be claimed again."
}

// handleCancelConfirm renders the cancel confirmation page. The reason is
// required here, a console-side guard on the irreversible path. The row's
// observed guard rides the query so the confirmed cancel stays guarded.
func (app *App) handleCancelConfirm(w http.ResponseWriter, r *http.Request) {
	taskID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid task id: must be a UUID", http.StatusBadRequest)
		return
	}
	card := cancelConfirmData(taskID.String(), interventionReturnTo(r), "", cancelObservedFrom(r))
	// Pre-fill the reason the row's popover carried so the no-JS path keeps it.
	card.Reason = strings.TrimSpace(r.FormValue("reason"))
	app.renderShell(w, r, "Confirm cancel", opsPath, pages.CancelConfirmCard(card))
}

// cancelObservedIDs is the claim or escalation id the acting row saw; at most
// one is set, and a ready task carries neither.
type cancelObservedIDs struct {
	claimID      string
	escalationID string
}

// cancelObservedFrom reads the guard from the query (no-JS confirm page) or
// the form body (the page's own post).
func cancelObservedFrom(r *http.Request) cancelObservedIDs {
	return cancelObservedIDs{
		claimID:      strings.TrimSpace(r.FormValue(expectedClaimIDParam)),
		escalationID: strings.TrimSpace(r.FormValue(escalatedGuardField)),
	}
}

// cancelRefusalFromTheCard reports whether a refused cancel came from the
// confirm card. htmx omits HX-Target when the target is the posting element,
// so an empty target or the card's id means the card.
func cancelRefusalFromTheCard(r *http.Request) bool {
	switch hxTargetID(r) {
	case "", pages.CancelConfirmAnchor:
		return true
	default:
		return false
	}
}

// cancelRefusalFromTheRow reports whether a refused cancel came from a console
// row, whose control targets the results block.
func cancelRefusalFromTheRow(r *http.Request) bool {
	return hxTargetID(r) == pages.OpsResultsAnchor
}

// freshCancelConfirmData rebuilds the confirm card from the task's current
// state, since the refused ids are exactly what the store rejected. ok is false
// when the task cannot be read.
func (app *App) freshCancelConfirmData(ctx context.Context, taskID uuid.UUID, returnTo, refusal string) (pages.CancelConfirmData, bool) {
	task, err := app.tasks.GetTaskByID(ctx, taskID)
	if err != nil {
		logger.Error("failed to re-read a task for a refused cancel", "task", taskID.String(), "error", err)
		return pages.CancelConfirmData{}, false
	}
	observed := cancelObservedIDs{}
	if task.CurrentClaimID != nil {
		observed.claimID = task.CurrentClaimID.String()
	}
	if task.CurrentEscalationID != nil {
		observed.escalationID = task.CurrentEscalationID.String()
	}
	return cancelConfirmData(taskID.String(), returnTo, refusal, observed), true
}

// cancelConfirmData builds the confirm card's view model; identity is never
// included.
func cancelConfirmData(taskID, returnTo, refusal string, observed cancelObservedIDs) pages.CancelConfirmData {
	card := pages.CancelConfirmData{
		TaskID:   taskID,
		Action:   opsTaskActionBase + taskID + "/" + actionCancel,
		ReturnTo: returnTo,
		Error:    refusal,
	}
	// Claims and escalations use different api guard field names.
	if observed.claimID != "" {
		card.ObservedClaimID = observed.claimID
	}
	if observed.escalationID != "" {
		card.ObservedID = observed.escalationID
		card.ObservedField = escalatedGuardField
	}
	return card
}
