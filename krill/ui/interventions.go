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
package main

import (
	"context"
	"html/template"
	"net/http"
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

// actionLabels is the submit-button text for each verb. A successful
// submission redirects back to the console view, so the label is the only
// place the verb is spelled out for the operator.
var actionLabels = map[string]string{
	actionRelease:  "Release",
	actionRequeue:  "Requeue",
	actionEscalate: "Escalate",
	actionCancel:   "Cancel",
}

// opsTaskActionBase is the console's per-task intervention URL prefix; a
// route is "POST "+opsTaskActionBase+"{id}/"+verb. The routes hang off the
// ops area so navIsActive keeps "Ops console" marked while an operator is
// acting on a task.
const opsTaskActionBase = opsPath + "/tasks/"

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
		if _, ok := actionLabels[action]; !ok {
			// Only the four routes below are registered, each with a literal
			// verb; anything else is a wiring mistake, not a runtime case.
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
		if reason := r.FormValue("reason"); reason != "" {
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
		// message relayed unchanged, so the operator reads the same refusal an
		// MCP or api caller would.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close() //nolint:errcheck
			http.Redirect(w, r, interventionReturnTo(r), http.StatusSeeOther)
			return
		}
		relayWriteResponse(w, resp) //nolint:errcheck
	}
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

// taskActionForm is one intervention button on a console row.
type taskActionForm struct {
	Label    string
	Action   string
	ReturnTo string
}

var taskActionsTemplate = template.Must(template.New("taskactions").Parse(`{{range .}}<form method="post" action="{{.Action}}" style="display:inline">
<input type="hidden" name="return_to" value="{{.ReturnTo}}">
<input type="text" name="reason" placeholder="reason (optional)" size="16" aria-label="reason">
<button type="submit">{{.Label}}</button>
</form>{{end}}`))

// renderTaskActions renders the intervention forms for one console row: a
// form per verb, each carrying an optional reason field and the view the
// operator should return to. reason is the free-text rationale the four ops
// record; scope and identity never appear in a form.
func renderTaskActions(taskID, returnTo string, actions ...string) template.HTML {
	forms := make([]taskActionForm, 0, len(actions))
	for _, action := range actions {
		forms = append(forms, taskActionForm{
			Label:    actionLabels[action],
			Action:   opsTaskActionBase + taskID + "/" + action,
			ReturnTo: returnTo,
		})
	}
	return renderPage(taskActionsTemplate, forms)
}
