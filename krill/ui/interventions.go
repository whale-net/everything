// The task-intervention actions a Swarm Operator takes from a console view:
// release, requeue, escalate, and cancel (FR 1f44e461). Each row of the
// claimed / escalated views renders a form per verb; submitting one POSTs to
// the matching route here, which mints a krill session for the signed-in
// operator (withKrillSession) and forwards the form's reason to the *same*
// krill api endpoint the ops-mount MCP tools drive (POST
// tasks/{id}/{action}) -- so an intervention taken in the browser and one
// taken by an MCP client are one state transition, one storage path, and one
// attribution. The browser's form carries only the action's own argument
// (the optional free-text reason); scope and both subjects always come from
// the minted session, never from the request.
//
// All four verbs take the same single argument: an optional free-text
// rationale. The api handlers' release/requeue/escalate/cancel Request types
// (krill/api/handlers/task_*.go) are each exactly {Reason *string
// `json:"reason"`}, and the four MCP tools' inputs (krill/mcp/tools/
// task_*.go) are each exactly {task_id, reason} with reason optional -- so
// one uniform {reason} forwarding is the faithful shape for all four, not a
// shortcut. ScopeID and both subjects come from the gated session on both
// sides (NFR6).
package main

import (
	"context"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// The four intervention verbs, named exactly as the krill api endpoints and
// the ops-mount MCP tools name them, so the path segment the browser posts to
// is the operation it performs.
const (
	actionRelease  = "release"
	actionRequeue  = "requeue"
	actionEscalate = "escalate"
	actionCancel   = "cancel"
)

// interventionAction describes one verb's console affordance: the button or
// link text, the reason prompt shown beside it, and whether the verb is
// destructive enough to require a confirmation step before it posts. The
// reason prompt is per-verb because the rationale an operator gives for
// force-closing a claim, escalating for attention, recovering to claimable,
// and dead-lettering are different questions.
type interventionAction struct {
	Label string
	// ReasonHint is the placeholder / aria-label for the verb's optional
	// reason field.
	ReasonHint string
	// Destructive marks a verb that posts only after an explicit
	// confirmation step. Only cancel is: it is the one intervention the
	// task can never be reopened from.
	Destructive bool
}

// interventionActions is the per-verb presentation, keyed by the same verb
// strings the routes and the api endpoints use.
var interventionActions = map[string]interventionAction{
	actionRelease:  {Label: "Release", ReasonHint: "why release the claim? (optional)"},
	actionRequeue:  {Label: "Requeue", ReasonHint: "why safe to retry? (optional)"},
	actionEscalate: {Label: "Escalate", ReasonHint: "why does it need attention? (optional)"},
	actionCancel:   {Label: "Cancel", ReasonHint: "why dead-letter it? (required)", Destructive: true},
}

// actionLabel is the human label for a verb, falling back to the raw verb
// for a wiring mistake rather than rendering an empty button.
func actionLabel(action string) string {
	if a, ok := interventionActions[action]; ok {
		return a.Label
	}
	return action
}

// opsTaskActionBase is the console's per-task intervention URL prefix; a
// route is "POST "+opsTaskActionBase+"{id}/"+verb. The routes hang off the
// ops area so navIsActive keeps "Ops console" marked while an operator is
// acting on a task.
const opsTaskActionBase = opsPath + "/tasks/"

// cancelConfirmSuffix is appended to the per-task intervention base to build
// the confirmation page a destructive cancel is reached through before it
// ever posts.
const cancelConfirmSuffix = "/" + actionCancel + "/confirm"

// taskInterventionRequest mirrors the four api request bodies (handlers'
// release/requeue/escalate/cancel Request types), which are all exactly the
// same: an optional free-text rationale. ScopeID and both subjects are
// supplied by the gated session, never this body (NFR6).
type taskInterventionRequest struct {
	Reason *string `json:"reason"`
}

// handleTaskIntervention is the console's intervention endpoint for one verb.
// Mounted behind operatorRoute (RequireAuth + requireOperator), so the
// handler always has the operator's Subject on context for withKrillSession
// to mint the krill session from.
func (app *App) handleTaskIntervention(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := interventionActions[action]; !ok {
			// Only the four routes are registered, each with a literal verb;
			// anything else is a wiring mistake, not a runtime case.
			http.NotFound(w, r)
			return
		}
		taskID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid task id: must be a UUID", http.StatusBadRequest)
			return
		}

		// A form POST carries the rationale as an optional urlencoded field.
		// An empty reason is sent as a null, which the api handler accepts
		// (its Reason is a *string) exactly as an omitted reason would.
		req := taskInterventionRequest{}
		if reason := strings.TrimSpace(r.FormValue("reason")); reason != "" {
			req.Reason = &reason
		}

		var resp *http.Response
		if err := app.withKrillSession(r.Context(), func(ctx context.Context, sessionID store.SessionID) error {
			var err error
			resp, err = app.writes.Write(ctx, sessionID, http.MethodPost, "tasks/"+taskID.String()+"/"+action, req)
			return err
		}); err != nil {
			writeWriteError(w, err)
			return
		}

		// A success is the Post/Redirect/Get the browser needs: the operator
		// lands back on the console view they acted from, re-read fresh, so a
		// refresh cannot replay the write. A rejection is api's own status and
		// message, but rendered as a page in the shell rather than raw JSON,
		// so the operator reads the same refusal a direct api caller would.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close() //nolint:errcheck
			http.Redirect(w, r, interventionReturnTo(r), http.StatusSeeOther)
			return
		}
		status, message := interventionRejection(resp)
		renderShellStatus(w, r, "Intervention rejected", opsPath, renderPage(interventionErrorTemplate, interventionErrorPage{
			Heading:  "krill rejected the " + actionLabel(action) + ".",
			Detail:   message,
			ReturnTo: interventionReturnTo(r),
		}), status)
	}
}

// apiError mirrors api/handlers' jsonError: the single {"error": "..."} shape
// every handler in that package returns for a rejected write.
type apiError struct {
	Error string `json:"error"`
}

// interventionRejection reads a rejected write's response into the status
// and message to present. The api's own status is preserved verbatim; its
// body is the one {"error": "..."} shape, and a body that is empty or is not
// that shape falls back to the status's own text so the page still says
// something meaningful. This is a normal outcome (unknown task, stale claim,
// already-cancelled task), not a failure of this binary, so nothing is
// logged above INFO.
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

var interventionErrorTemplate = template.Must(template.New("interventionerror").Parse(`<h2>{{.Heading}}</h2>
<p>{{.Detail}}</p>
<p><a href="{{.ReturnTo}}">Back to the console</a></p>`))

type interventionErrorPage struct {
	Heading  string
	Detail   string
	ReturnTo string
}

// interventionReturnTo is the console view a successful intervention
// redirects back to, read from the form's return_to field. Only this
// binary's own ops paths are honored: an open redirect needs a value
// starting with a scheme or "//", neither of which starts with opsPath, so a
// crafted return_to can never bounce the operator off-site. Anything
// unrecognized (or absent) falls back to the ops root.
func interventionReturnTo(r *http.Request) string {
	to := r.FormValue("return_to")
	if to == "" || !strings.HasPrefix(to, opsPath) {
		return opsPath
	}
	return to
}

// taskActionControl is one rendered control on a console row: either an
// inline non-destructive form (Kind "form") or a link to a destructive
// verb's confirmation page (Kind "confirm"). Rendering the destructive verb
// as a link rather than a form is the confirm affordance -- nothing posts
// until the operator confirms on the next page.
type taskActionControl struct {
	Kind       string
	Label      string
	Action     string
	ReasonHint string
	ReturnTo   string
}

var taskActionsTemplate = template.Must(template.New("taskactions").Parse(`{{range .}}{{if eq .Kind "form"}}<form method="post" action="{{.Action}}" style="display:inline">
<input type="hidden" name="return_to" value="{{.ReturnTo}}">
<input type="text" name="reason" placeholder="{{.ReasonHint}}" size="18" aria-label="{{.ReasonHint}}">
<button type="submit">{{.Label}}</button>
</form>{{else}}<a href="{{.Action}}" class="danger">{{.Label}}&hellip;</a>{{end}} {{end}}`))

// renderTaskActions renders the intervention controls for one console row, in
// the order the verbs are passed. A non-destructive verb renders as an inline
// form carrying its own reason prompt; a destructive verb renders as a link
// to its confirmation page. reason is the free-text rationale the four ops
// record; scope and identity never appear in a form.
func renderTaskActions(taskID, returnTo string, actions ...string) template.HTML {
	controls := make([]taskActionControl, 0, len(actions))
	for _, action := range actions {
		a, ok := interventionActions[action]
		if !ok {
			continue
		}
		control := taskActionControl{
			Label:      a.Label,
			ReasonHint: a.ReasonHint,
			ReturnTo:   returnTo,
		}
		if a.Destructive {
			control.Kind = "confirm"
			control.Action = cancelConfirmHref(taskID, returnTo)
		} else {
			control.Kind = "form"
			control.Action = opsTaskActionBase + taskID + "/" + action
		}
		controls = append(controls, control)
	}
	return renderPage(taskActionsTemplate, controls)
}

// cancelConfirmHref builds the link to a task's cancel confirmation page,
// carrying the view to return to as a query parameter.
func cancelConfirmHref(taskID, returnTo string) string {
	q := url.Values{}
	q.Set("return_to", returnTo)
	return opsTaskActionBase + taskID + cancelConfirmSuffix + "?" + q.Encode()
}

// handleCancelConfirm renders the confirmation step for the destructive
// cancel verb. Cancel dead-letters a task irreversibly -- requeue cannot
// reopen it and claim never returns it -- so it is the one intervention that
// posts only after the operator confirms, here, on a page that restates the
// consequence. The reason is required on this form (the api itself keeps it
// optional, matching the MCP tool; this is a console-side guard on the
// irreversible path). Mounted behind operatorRoute like every other
// operator-only route.
func (app *App) handleCancelConfirm(w http.ResponseWriter, r *http.Request) {
	taskID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid task id: must be a UUID", http.StatusBadRequest)
		return
	}
	returnTo := interventionReturnTo(r)
	renderShell(w, r, "Confirm cancel", opsPath, renderPage(cancelConfirmTemplate, cancelConfirmPage{
		TaskID:   taskID.String(),
		Action:   opsTaskActionBase + taskID.String() + "/" + actionCancel,
		ReturnTo: returnTo,
	}))
}

type cancelConfirmPage struct {
	TaskID   string
	Action   string
	ReturnTo string
}

var cancelConfirmTemplate = template.Must(template.New("cancelconfirm").Parse(`<h2>Cancel task {{.TaskID}}?</h2>
<p>Canceling dead-letters this task. It is never claimable again and a later
requeue cannot reopen it. This cannot be undone.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="return_to" value="{{.ReturnTo}}">
<p><label for="reason">Reason (required):<br>
<textarea id="reason" name="reason" rows="3" cols="48" required></textarea></label></p>
<button type="submit">Confirm cancel</button>
</form>
<p><a href="{{.ReturnTo}}">Back to the console</a></p>`))
