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
//
// Every form here is doubled -- method="post" + action= AND hx-post +
// hx-target + hx-swap -- so the two branches below are the same route
// serving two modes rather than two routes. The no-JS branch is the
// Post/Redirect/Get the console has always taken and is unchanged. The
// htmx branch always answers 200 with the whole results block of the view
// the operator acted from, re-derived from freshly observed state, because
// the intervention moves a row between views (release removes it from
// /ops/claimed) and a status code or a row-level swap would leave a stale
// row behind.
//
// Cancel is the one exception to "one route, two modes", and deliberately:
// it is irreversible, so its two branches are two routes. The htmx branch
// is hx-confirm-then-post (a browser confirmation naming the task, then the
// same 200-with-the-results-block as the other verbs), and the no-JS branch
// is a GET to the confirmation PAGE, which posts nothing until its own form
// is submitted. Both branches carry the id the acting row observed, so a
// claim or escalation that changed since the row was read is refused on
// either path.
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

// The four intervention verbs, named exactly as the krill api endpoints and
// the ops-mount MCP tools name them, so the path segment the browser posts to
// is the operation it performs. Derived from store.InterventionAction* rather
// than redeclared as their own string literals, so this package can never
// drift from task_escalation.go's enum.
const (
	actionRelease  = string(store.InterventionActionRelease)
	actionRequeue  = string(store.InterventionActionRequeue)
	actionEscalate = string(store.InterventionActionEscalate)
	actionCancel   = string(store.InterventionActionCancel)
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
// release/requeue/escalate/cancel Request types): an optional free-text
// rationale plus the observed-state guard the acting row carried. ScopeID and
// both subjects are supplied by the gated session, never this body (NFR6).
type taskInterventionRequest struct {
	Reason *string `json:"reason"`

	// ExpectedClaimID and ExpectedEscalationID are the observed-state guard:
	// the id the acting row saw, forwarded under the api's own wire names so
	// the api's store guard (store.ErrObservedStateMismatch) refuses an
	// intervention whose claim or escalation changed since the page loaded.
	// Each is omitted when the row observed none -- a ready task carries
	// neither, and a claimed row carries only the claim.
	ExpectedClaimID      *string `json:"expected_claim_id,omitempty"`
	ExpectedEscalationID *string `json:"expected_escalation_id,omitempty"`
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

		// The open-redirect guard runs first, on both branches: it decides
		// where a no-JS browser is redirected to and which view an htmx
		// browser has re-derived underneath it, so it is settled before the
		// write rather than after.
		returnTo := interventionReturnTo(r)

		// A form POST carries the rationale as an optional urlencoded field.
		// An empty reason is sent as a null, which the api handler accepts
		// (its Reason is a *string) exactly as an omitted reason would.
		req := taskInterventionRequest{}
		if reason := strings.TrimSpace(r.FormValue("reason")); reason != "" {
			req.Reason = &reason
		}

		// The observed-state guard rides the form under the api's own wire
		// names, so the value a row rendered is the value the api's store
		// guard checks (store.ErrObservedStateMismatch, 409). It is taken
		// from the row the table read and never from typed input: the
		// operator cannot type it and cannot be asked to. A control that
		// observed none carries an empty value, which is forwarded as an
		// omitted guard rather than an all-zero id no write could match.
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
			// The write never reached krill api. htmx still gets a 200 the
			// operator can read -- a refusal rides inside the fragment,
			// never in a status code a swap target would discard -- but it
			// says exactly what the no-JS branch says, and the detail is
			// logged rather than shown, so a transport error never leaks
			// an internal URL into the page.
			logger.Error("failed to issue an operator write", "error", err)
			if isHtmxRequest(r) {
				app.renderInterventionResults(w, r, returnTo, "the write could not be issued as the signed-in operator", "")
				return
			}
			writeWriteError(w, err)
			return
		}

		// A success is the Post/Redirect/Get the no-JS browser needs: the
		// operator lands back on the console view they acted from, re-read
		// fresh, so a refresh cannot replay the write. The htmx browser gets
		// the same view re-derived in place.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close() //nolint:errcheck
			if isHtmxRequest(r) {
				app.renderInterventionSuccess(w, r, action, card)
				return
			}
			// A 303 has no body to carry the confirmation in, so the
			// message rides a one-shot cookie that the landing page
			// renders as a success alert.
			flashSuccess(w, interventionSuccessMessage(action))
			http.Redirect(w, r, returnTo, http.StatusSeeOther)
			return
		}

		// A rejection is api's own status and message. The no-JS browser
		// reads it as a page in the shell, so the operator sees the same
		// refusal a direct api caller would; the htmx browser reads it
		// inline, because it is swapping a fragment and never sees a status.
		status, message := interventionRejection(resp)
		if isHtmxRequest(r) {
			refusal := "krill rejected the " + actionLabel(action) + ". " + message
			// A refused cancel is answered into whichever of its two regions
			// the request came from. The confirm card swaps ITSELF, so a
			// refusal posted from the card re-renders the card -- rebuilt from
			// freshly read state, never from the ids the refused request
			// carried, because those are exactly the ids the store just
			// refused. The row's control, by contrast, swaps the results
			// block, so a refusal there is answered like the other three verbs:
			// the view re-derived from fresh state with the refusal inline.
			if action == actionCancel && !cancelRefusalFromTheRow(r) {
				if fresh, ok := app.freshCancelConfirmData(r.Context(), taskID, returnTo, refusal); ok {
					renderFragment(w, r, pages.CancelConfirmCard(fresh))
					return
				}
				// The state a confirmation would re-offer could not be
				// re-read, so re-offering one at all would be guessing at the
				// guard. Say the view could not be reloaded instead, the same
				// answer the three in-place verbs give when their view cannot
				// be rebuilt.
			}
			app.renderInterventionResults(w, r, returnTo, refusal, "")
			return
		}
		app.renderShellStatus(w, r, "Intervention rejected", opsPath, pages.InterventionError(pages.InterventionErrorData{
			Heading:  "krill rejected the " + actionLabel(action) + ".",
			Detail:   message,
			ReturnTo: returnTo,
		}), status)
	}
}

// renderInterventionSuccess answers a successful htmx intervention, always
// at 200 and never with a redirect, by re-deriving the view the operator
// acted from. Cancelling is the one verb that navigates instead of
// swapping.
//
// HX-Redirect is the single legitimate redirect on an htmx path, and the
// reason it is not the rule the other three follow is that it is a
// navigation, not a swap: the operator has left the cancel-confirm card
// for the console view named by return_to, and htmx performs a full page
// load there. A release or escalate has nowhere to navigate to -- the row
// it acted on is still on the page -- so those swap the results block in
// place, which is also what removes the row a release just orphaned.
// (An HX-Redirect is keyed on the verb, not on where the request came
// from, because cancel is the only verb whose hx-post originates on the
// confirm card: its console-row control is a link, never a form.)
func (app *App) renderInterventionSuccess(w http.ResponseWriter, r *http.Request, action string, card pages.CancelConfirmData) {
	message := interventionSuccessMessage(action)
	if action == actionCancel {
		// HX-Redirect is a full page load, so the target page is rendered
		// from scratch and the toast host arrives with it. The message
		// therefore takes the same no-JS path a plain form post does.
		flashSuccess(w, message)
		w.Header().Set("HX-Redirect", card.ReturnTo)
		renderFragment(w, r, pages.CancelConfirmCard(card))
		return
	}
	app.renderInterventionResults(w, r, card.ReturnTo, "", message)
}

// interventionSuccessMessage is the confirmation a successful
// intervention states. It is derived from the verb's own label rather
// than declared per call site, so the four verbs cannot drift into four
// differently-worded confirmations.
//
// The three in-place verbs word their confirmation exactly as FR
// 43e39aae names it ("Task requeued", "Claim released", "Task
// escalated"): the toast is the operator's only confirmation, so its
// wording is part of the requirement rather than free prose.
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

// interventionReloadFailure is the message a post-write re-derivation shows
// when the region could not be rebuilt -- a read that failed, or a return_to
// that names no view this binary serves. One wording for both: either way
// the write may have landed and the view around it could not be re-read, and
// the operator needs the same warning rather than a distinction between two
// causes they can do nothing different about.
const interventionReloadFailure = "The intervention was applied, but this view could not be reloaded."

// renderInterventionResults re-reads the Needs attention view the operator
// acted from and writes its results block at 200, with any message carried
// inline above the rows.
//
// The region is re-derived by the tab's OWN loader (needsAttentionResults),
// the same function a GET of that tab calls, so the rows an operator sees
// after an intervention are exactly the rows a reload would show them --
// never a second reading of the same store query that could drift from it.
// That is also what makes a row which left its tab (a released claim, a
// requeued escalation) disappear from the table it was in, and what makes
// the counts and the escalated shape come along with it.
//
// message and toast are separate because they answer to opposite
// outcomes: a refusal rides inline in message (it must stay on the page
// until read) while a success rides in toast (it is transient). Callers
// pass at most one of the two -- a response that is both a success and a
// refusal does not exist, and giving it a way to be both would put a
// dismissible toast next to the record of its own opposite.
func (app *App) renderInterventionResults(w http.ResponseWriter, r *http.Request, returnTo, message, toast string) {
	ctx := r.Context()
	target, ok := app.interventionReturnTargetOf(r, returnTo)
	if !ok {
		// The guard has already refused anything off-site, so this is a
		// same-origin path with no tab behind it. Answering 200 with the
		// reload warning keeps the swap target in place rather than
		// rendering another view's rows under a path that never named them.
		logger.Warn("intervention: return_to names no view; rendering the reload warning", "return_to", returnTo)
		renderFragment(w, r, withToast(toast, pages.OpsInlineError(interventionReloadFailure)))
		return
	}
	// The filter bar's sentence is built from the product's own containers,
	// exactly as the tab's GET builds it, so a filtered-empty tab reads back
	// the same filters whichever request emptied it.
	containers := app.needsAttentionMilestoneContainers(ctx, target.productID)
	view, err := app.needsAttentionResults(ctx, target.productID, target.filter,
		needsAttentionFilterSentence(target.filter, containers), target.tab,
		target.page, target.selfPath, app.clock(), message)
	if err != nil {
		// A read failure must not render as an empty view: the intervention
		// itself may well have succeeded, and "nothing escalated" would be a
		// confident, wrong answer. Say the view could not be reloaded instead.
		logger.Error("failed to reload the needs attention view after an intervention", "error", err)
		renderFragment(w, r, withToast(toast, pages.OpsInlineError(interventionReloadFailure)))
		return
	}
	renderFragment(w, r, withToast(toast, view.Results))
}

// interventionReturnTarget is the Needs attention view a successful
// intervention re-derives in place: the product the acting row's page was
// scoped to, the tab it was on, and the filter and page its own URL carried.
type interventionReturnTarget struct {
	productID uuid.UUID
	tab       string
	filter    needsAttentionFilter
	page      store.PageParams
	selfPath  string
}

// interventionReturnTargetOf resolves the view a successful intervention
// re-derives from the form's return_to -- the address the acting row's
// control carried.
//
// Two shapes resolve, and both are paths this binary itself serves:
//
//   - A Needs attention tab URL, /products/{pid}/needs-attention?tab=...,
//     which is what every row control posts. Its {pid} is the page's scope
//     and its query is the tab, the filters and any paging the operator had
//     in force.
//   - A legacy ops console URL (/ops, /ops/claimed, /ops/escalated, ...),
//     which a form built before the cutover still carries. It resolves to
//     the tab it retires into, under the product the un-prefixed URL would
//     have redirected to, so an older form re-derives the right view rather
//     than falling back to a fixed one.
//
// ok is false when neither shape names a view this binary serves, which the
// caller answers with the same reload warning it uses for a failed read.
func (app *App) interventionReturnTargetOf(r *http.Request, returnTo string) (interventionReturnTarget, bool) {
	u, err := url.Parse(returnTo)
	if err != nil {
		return interventionReturnTarget{}, false
	}
	if pid, ok := needsAttentionProductOfPath(u.Path); ok {
		// The return_to URL is the request the tab's query parsers would have
		// read, so the tab, the filters and the paging are parsed by the very
		// functions the GET uses rather than by a second reading here.
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

// needsAttentionProductOfPath returns the product a Needs attention path is
// scoped to, when the path is exactly one of this binary's own:
// /products/{pid}/needs-attention. Any other shape -- a different sub-page,
// a missing or malformed id, a trailing segment -- is not this page and
// answers false, so a caller never honours a path this binary does not
// serve.
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

// productTaskDetailOfPath returns the product a product-scoped task detail
// path is scoped to, when the path is exactly one of this binary's own:
// /products/{pid}/tasks/{tid} (the route main.go registers for
// handleProductTaskDetail). Any other shape -- a different sub-page, a
// missing or malformed task id, a trailing segment -- answers false, so a
// detail page's return path is honoured only when it is a page this binary
// actually serves.
func productTaskDetailOfPath(path string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(path, productsPath+"/")
	if !ok {
		return uuid.Nil, false
	}
	rawPID, suffix, ok := strings.Cut(rest, "/")
	if !ok {
		return uuid.Nil, false
	}
	rawTID, ok := strings.CutPrefix(suffix, strings.TrimPrefix(tasksSuffix, "/")+"/")
	if !ok || rawTID == "" || strings.Contains(rawTID, "/") {
		return uuid.Nil, false
	}
	pid, err := uuid.Parse(rawPID)
	if err != nil {
		return uuid.Nil, false
	}
	if _, err := uuid.Parse(rawTID); err != nil {
		return uuid.Nil, false
	}
	return pid, true
}

// legacyOpsTab is the Needs attention tab a retired ops console URL becomes
// (routes.go's legacyURLs), or ok false for a path that was never one of the
// console's views. The console root names no queue of its own, so it is the
// default tab -- the same address it redirects to.
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

// interventionReturnTo is the view a successful intervention returns to,
// read from the form's return_to field. Three of this binary's own path
// shapes are honored, and nothing else: the ops console's own paths
// (/ops/...), a product-scoped Needs attention tab
// (/products/{pid}/needs-attention), which is where every row control now
// posts from so the operator lands back on the tab they acted from, and a
// product-scoped task detail (/products/{pid}/tasks/{tid}), which is where
// the detail page's own controls post from so they land back on the detail
// rather than on a queue (FR af61631d). Anything unrecognized (or absent)
// falls back to the ops root.
func interventionReturnTo(r *http.Request) string {
	fallback := opsPath
	to := strings.TrimSpace(r.FormValue("return_to"))
	if to == "" {
		return fallback
	}
	// Only a path this binary actually serves is honoured, and it must
	// still be one after the browser has percent-decoded and
	// path-normalised it. Three checks, because each alone misses a case:
	//
	//   - Rejecting an absolute or host-bearing URL rejects off-site
	//     values outright.
	//   - Rejecting ".." in the DECODED path rejects
	//     "/ops/../../etc/passwd" and its percent-encoded spelling
	//     "/ops/%2e%2e/%2e%2e/etc/passwd". The check has to run on
	//     u.Path, which url.Parse has already decoded -- testing the raw
	//     string would pass the encoded form straight through, and the
	//     browser would then normalise it to a path outside /ops.
	//   - Matching at segment boundaries rejects "/opsarchive", which
	//     shares the raw prefix but is not a view this binary serves, and
	//     the same boundary rule is what admits a Needs attention tab URL
	//     or a task detail URL without admitting
	//     "/products/{pid}/needs-attention/anything" or
	//     "/products/{pid}/tasks/{tid}/anything".
	//
	// None of these is an open-redirect defence on its own: the value is
	// only ever used as a same-origin Location or HX-Redirect. They are
	// here so a return_to resolves to a view the operator asked for
	// rather than a 404 or somewhere else on this origin.
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

// expectedClaimIDParam is the field name a row's controls carry the claim
// they observed under. It is the api's own wire name
// (releaseTaskRequest.ExpectedClaimID's json tag, krill/api/handlers/
// task_release.go), so the value a row posts is the value the api's guard
// reads, with no translation in between.
const expectedClaimIDParam = "expected_claim_id"

// renderTaskActions renders the intervention controls for one console row, in
// the order the verbs are passed. A non-destructive verb renders as an inline
// doubled form carrying its own reason prompt; a destructive verb renders as
// a doubled control whose htmx half confirms (hx-confirm, naming the task)
// and posts in place, and whose no-JS half is a GET to the verb's
// confirmation page. reason is the free-text rationale the four ops record;
// scope and identity never appear in a form, because the write resolves the
// operator's real (iss, sub) server-side from the gated krill session
// (withKrillSession), not from anything the browser sent.
//
// title is what the destructive verb's confirmation names. It comes from the
// row the operator is looking at, so "Cancel <title>?" cannot confirm a
// different row than the one the operator read.
func renderTaskActions(taskID, title, returnTo string, actions ...string) templ.Component {
	return pages.TaskActions(taskActionControls(taskID, title, returnTo, actions...))
}

// renderClaimedTaskActions is renderTaskActions for a row that holds a
// claim: the same controls, each carrying the claim id the row observed so
// the action is guarded against the state the operator actually saw. The
// id comes from the store row the table rendered, never from typed input --
// the operator cannot type it and cannot be asked to.
//
// A row whose read observed no claim (a zero id) renders the plain unguarded
// controls rather than carrying an all-zero uuid, which is not a claim any
// write could match.
func renderClaimedTaskActions(taskID, title string, claimID uuid.UUID, returnTo string, actions ...string) templ.Component {
	controls := taskActionControls(taskID, title, returnTo, actions...)
	if claimID == uuid.Nil {
		return pages.TaskActions(controls)
	}
	observed := claimID.String()
	for i := range controls {
		// Both halves carry it as the same hidden expected_claim_id input: the
		// htmx half posts it to the verb, the no-JS half hands it to the
		// confirmation page, which carries it on to the verb's own form.
		controls[i].ObservedClaimID = observed
	}
	return pages.TaskActions(controls)
}

// renderEscalatedTaskActions is renderTaskActions for an escalated row: the
// same controls, each carrying the escalation id the row observed so Requeue
// and Cancel are guarded against the escalation the operator actually saw.
// It is the escalated counterpart of renderClaimedTaskActions, and the same
// rule holds: the id is the store row's, never typed.
//
// A row whose read reported no escalation renders the plain unguarded
// controls rather than an all-zero uuid, which is not an escalation any write
// could match.
func renderEscalatedTaskActions(taskID, title, escalationID, returnTo string, actions ...string) templ.Component {
	controls := taskActionControls(taskID, title, returnTo, actions...)
	if escalationID == "" {
		return pages.TaskActions(controls)
	}
	for i := range controls {
		controls[i].ObservedField = escalatedGuardField
		controls[i].ObservedID = escalationID
	}
	return pages.TaskActions(controls)
}

// taskActionControls is the one builder of a row's verb controls: the four
// verbs' labels and reason prompts, the route a form posts to, and the
// confirmation a destructive verb is reached through. Splitting it out keeps
// one verb vocabulary while letting a row attach the claim or escalation id
// it observed to every control it renders.
func taskActionControls(taskID, title, returnTo string, actions ...string) []pages.TaskActionControl {
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
		}
		if a.Destructive {
			// The destructive verb's two halves are two routes: the htmx half
			// POSTs the cancel after hx-confirm, and the no-JS half GETs the
			// confirmation page, which posts nothing until its own form is
			// submitted. A bare confirm route, not a pre-parameterised one:
			// the browser drops an action's query string on a GET form, so
			// return_to and the guard ride hidden inputs instead.
			control.Kind = "confirm"
			control.Action = opsTaskActionBase + taskID + cancelConfirmSuffix
			control.PostAction = opsTaskActionBase + taskID + "/" + action
			control.Confirm = cancelConfirmMessage(title)
		} else {
			control.Kind = "form"
			control.Action = opsTaskActionBase + taskID + "/" + action
		}
		controls = append(controls, control)
	}
	return controls
}

// cancelConfirmMessage is the browser confirmation the destructive cancel
// verb shows before it posts anything (FR 336335f1): it names the task, and
// it states the consequence, because a cancel is the one intervention the task
// can never be reopened from. It is declared once, so the copy cannot drift
// between the rows that render it.
func cancelConfirmMessage(title string) string {
	return "Cancel " + title + "? It moves to Cancelled and cannot be claimed again."
}

// handleCancelConfirm renders the confirmation step for the destructive
// cancel verb. Cancel dead-letters a task irreversibly -- requeue cannot
// reopen it and claim never returns it -- so it is the one intervention that
// posts only after the operator confirms, here, on a page that restates the
// consequence. The reason is required on this form (the api itself keeps it
// optional, matching the MCP tool; this is a console-side guard on the
// irreversible path). Mounted behind operatorRoute like every other
// operator-only route.
//
// This stays a GET page. Its card's form is doubled, so an htmx browser
// posts the same route a no-JS browser does and gets the card back at 200
// with a refusal inline, or HX-Redirect to the console view on success.
//
// The guard the acting row handed this page rides the query string, because
// this page IS the no-JS half of that row's Cancel: it must carry the id the
// row observed onto the card's form, or the confirm would post an unguarded
// cancel and lose the guard the row rendered.
func (app *App) handleCancelConfirm(w http.ResponseWriter, r *http.Request) {
	taskID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid task id: must be a UUID", http.StatusBadRequest)
		return
	}
	app.renderShell(w, r, "Confirm cancel", opsPath, pages.CancelConfirmCard(
		cancelConfirmData(taskID.String(), interventionReturnTo(r), "", cancelObservedFrom(r))))
}

// cancelObservedIDs is the observed-state guard a cancel request carries: the
// claim or escalation id the acting row saw. At most one is set -- a claimed
// row observed a claim, an escalated row an escalation, a ready task neither,
// which is the one unguarded shape the FR names.
type cancelObservedIDs struct {
	claimID      string
	escalationID string
}

// cancelObservedFrom reads that guard off a request, from the same hidden
// inputs a row's control renders. Both the query string and a form body are
// read, because the two halves of the control carry the ids in those two
// places: the no-JS half hands them to the confirmation page in the query,
// and that page's own form posts them as a body field.
func cancelObservedFrom(r *http.Request) cancelObservedIDs {
	return cancelObservedIDs{
		claimID:      strings.TrimSpace(r.FormValue(expectedClaimIDParam)),
		escalationID: strings.TrimSpace(r.FormValue(escalatedGuardField)),
	}
}

// cancelRefusalFromTheRow reports whether a refused cancel was posted by a
// console row's Cancel control rather than by the confirm card's own form.
//
// Both halves of a row's Cancel POST the same route, so the only thing that
// tells the two origins apart is which region htmx was swapping: the row's
// control targets the results block, while the card's form targets the card
// itself. htmx sends the resolved target's id in HX-Target, so a request that
// names the results block is the row's.
func cancelRefusalFromTheRow(r *http.Request) bool {
	return r.Header.Get("HX-Target") == pages.OpsResultsAnchor
}

// freshCancelConfirmData rebuilds the cancel-confirm card from the task's
// CURRENT state rather than from the ids the refused request carried.
//
// A refusal means the state the acting row observed is not the state the
// store now holds -- that is why the write was refused -- so rebuilding the
// card from the request's own ids would re-offer the operator the very guard
// that was just rejected. The card is instead built from a fresh read of the
// task, carrying whatever claim or escalation it holds now, so the retry is
// guarded against the state the operator is about to see (FR 336335f1).
//
// ok is false when the task could not be read; the caller then answers with
// the reload warning rather than re-offering a confirmation built from no
// fresh state at all.
func (app *App) freshCancelConfirmData(ctx context.Context, taskID uuid.UUID, returnTo, refusal string) (pages.CancelConfirmData, bool) {
	task, err := app.tasks.GetTaskByID(ctx, taskID)
	if err != nil {
		logger.Error("failed to re-read a task for a refused cancel", "task", taskID.String(), "error", err)
		return pages.CancelConfirmData{}, false
	}
	// The fresh row's own claim or escalation is the guard the retry must
	// carry. A row that holds neither -- a ready task -- is the one unguarded
	// shape and carries nothing, exactly as an unguarded row's control does.
	observed := cancelObservedIDs{}
	if task.CurrentClaimID != nil {
		observed.claimID = task.CurrentClaimID.String()
	}
	if task.CurrentEscalationID != nil {
		observed.escalationID = task.CurrentEscalationID.String()
	}
	return cancelConfirmData(taskID.String(), returnTo, refusal, observed), true
}

// cancelConfirmData builds the confirm card's view-model. It carries the
// task's id, the routes the form posts to, and the observed-state guard the
// acting row handed it -- never an identity, which withKrillSession resolves
// server-side.
func cancelConfirmData(taskID, returnTo, refusal string, observed cancelObservedIDs) pages.CancelConfirmData {
	card := pages.CancelConfirmData{
		TaskID:   taskID,
		Action:   opsTaskActionBase + taskID + "/" + actionCancel,
		ReturnTo: returnTo,
		Error:    refusal,
	}
	// A claim and an escalation are guarded under different api field names,
	// so the one the row observed decides which field carries it.
	if observed.claimID != "" {
		card.ObservedClaimID = observed.claimID
	}
	if observed.escalationID != "" {
		card.ObservedID = observed.escalationID
		card.ObservedField = escalatedGuardField
	}
	return card
}
