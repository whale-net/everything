// The design-session write surface: the two Requirement-Contributor
// actions the design-session read views (design_page.go) hang off -- open a
// new session from a plain-language opening_submission, and submit
// follow-up input to an open session as an `answer` revision round.
//
// Both actions go through withKrillSession (writes.go), the same path every
// other write in this binary takes: the krill session is minted from the
// signed-in operator's real (iss, sub) pair (identity.go), and the browser's
// form carries only the action's own arguments -- no identity, scope, or
// session id is ever read from the request. Each action calls the exact krill
// api operation its MCP twin wraps (OpenDesignSessionHandler /
// AppendRevisionEventHandler), so a browser and an MCP client produce the same
// stored rows and the same rejections.
//
// Neither action writes a Feature/Requirement entity. The open path sends
// only product_id + opening_submission -- the api body has no entity-reference
// field at all (FR8), so there is nothing for the browser to point at a
// Feature or Requirement with. The answer path always sends an empty
// entity_deltas, so the UI proposes and amends no spec entity; turning a
// submission into entities stays the mediated (propose_entities) path an
// Agent drives, not a browser write.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ── wire types ───────────────────────────────────────────────────────────────

// The request/response types below mirror api/handlers' appendRevisionEvent*
// shapes field for field, redeclared because this binary speaks to `api` over
// HTTP rather than importing its handler package -- exactly as writes.go
// redeclares openDesignSessionRequest. Keeping them in step is what makes a
// browser's write byte-identical to the same write issued through the
// open_design_session / append_revision_event MCP tools.

// answerEntityDelta mirrors api/handlers' entityDeltaRequest. The answer path
// never populates it -- it always sends an empty slice -- and exists only so
// the request body matches AppendRevisionEventHandler's shape field for field.
type answerEntityDelta struct {
	EntityID    string `json:"entity_id"`
	Change      string `json:"change"`
	SummaryLine string `json:"summary_line"`
}

// answerOpenedQuestion mirrors api/handlers' openQuestionOpenedRequest.
type answerOpenedQuestion struct {
	QuestionID string `json:"question_id"`
	Blocking   bool   `json:"blocking"`
	Text       string `json:"text"`
}

// answerQuestionsDelta mirrors api/handlers' openQuestionsDeltaRequest.
type answerQuestionsDelta struct {
	Opened   []answerOpenedQuestion `json:"opened"`
	Resolved []string               `json:"resolved"`
}

// answerRevisionEventRequest mirrors api/handlers' appendRevisionEventRequest.
// It deliberately has no field for acting/on_behalf_of/scope_id: those are
// always taken from the gating krill session, never from this body.
type answerRevisionEventRequest struct {
	EventType          string               `json:"event_type"`
	EntityDeltas       []answerEntityDelta  `json:"entity_deltas"`
	OpenQuestionsDelta answerQuestionsDelta `json:"open_questions_delta"`
	VerifiedAgainst    *string              `json:"verified_against"`
	SignoffStatus      *string              `json:"signoff_status"`
}

// createdResponse is api's IDResponse (an opened design session's new id) and
// createdRevisionEvent is its RevisionEventCreatedResponse (an appended
// round's id and store-allocated seq_no). Redeclared for the same reason as
// the request types above.
type createdResponse struct {
	ID string `json:"id"`
}

type createdRevisionEvent struct {
	ID    string `json:"id"`
	SeqNo int    `json:"seq_no"`
}

// ── write plumbing ───────────────────────────────────────────────────────────

// writeRejection is a non-2xx response from api: a rejected write (unknown
// product, unopened question id, malformed id) is a normal outcome, not a
// failure of this binary, so its status and api's own named message are
// carried here and relayed to the browser verbatim.
type writeRejection struct {
	status  int
	message string
}

func (w *writeRejection) Error() string { return w.message }

// writeAndDecode issues one krill write under the given session and, on a 2xx,
// JSON-decodes api's response body into out. A non-2xx becomes a
// *writeRejection carrying api's status and named error message; a transport
// failure surfaces as the underlying error (which renderWriteFailure maps to a
// 502, never attributing a write that never reached krill).
func (app *App) writeAndDecode(ctx context.Context, sessionID store.SessionID, method, path string, body, out any) error {
	resp, err := app.writes.Write(ctx, sessionID, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var parsed struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&parsed)
		if parsed.Error == "" {
			parsed.Error = http.StatusText(resp.StatusCode)
		}
		return &writeRejection{status: resp.StatusCode, message: parsed.Error}
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode api response: %w", err)
	}
	return nil
}

// renderOpenFormFailure hands the new-session blade back after a refused
// write, with the operator's opening text preserved so a refusal is never a
// data-loss event (FR 4304fe60).
//
// Both modes answer 200, never the rejection's status: htmx does not swap on
// an error status, so a 4xx or a 502 would leave the operator with a blade
// that appears to do nothing at all -- the one case they most need to be
// told about. The no-JS path gets the same blade inside the shell, never a
// bare http.StatusText page.
//
// A *writeRejection is shown inline as api's own status + named message.
// Anything else (no resolved operator, unresolvable scope, unreachable api)
// never reached krill at all, so it gets the operator-facing half only --
// the specific cause is logged, never rendered (transportFailureMessage).
func (app *App) renderOpenFormFailure(w http.ResponseWriter, r *http.Request, productID uuid.UUID, opening string, err error) {
	blade := app.newDesignSessionBlade(r, productID)
	blade.OpeningSubmission = opening

	var rejection *writeRejection
	if errors.As(err, &rejection) {
		blade.Error = fmt.Sprintf("%d: %s", rejection.status, rejection.message)
	} else {
		blade.Error = "Could not reach krill: " + transportFailureMessage(err)
	}

	page := app.designSessionListPage(r, productID, nil)
	page.NewBlade = blade

	// The list behind the page is a nicety; the blade is the subject. When
	// the read fails the blade still comes back -- saying so inline, rather
	// than rendering an empty table as though it were the whole answer --
	// and the typed text survives either way.
	if rejection != nil {
		sessions, listErr := app.designSessionRows(r.Context(), productID, time.Now())
		if listErr != nil {
			logger.Error("failed to re-read design sessions after a refusal", "product_id", productID, "error", listErr)
			blade.Error += " (the session list could not be reloaded)"
		} else {
			page.Sessions = sessions
		}
	}

	if isHXRequest(r) {
		renderFragment(w, r, pages.NewDesignSessionBlade(blade))
		return
	}
	app.renderShell(w, r, "Design sessions", r.URL.Path, pages.DesignSessionList(page))
}

// transportFailureMessage is the operator-facing half of a non-rejection
// write failure. The specific cause is logged, not rendered: it can carry
// an internal api URL or a driver message.
func transportFailureMessage(err error) string {
	logger.Error("design write failed before reaching krill", "error", err)
	return "the request did not complete. Check the logs, then try again."
}

// renderAnswerFormFailure re-renders the follow-up round after a rejected
// write, preserving the follow-up text and which resolve boxes were
// ticked. Same 200-both-modes rule as renderOpenFormFailure.
//
// What comes back is the WHOLE round region -- timeline, rail, form --
// not the form alone. The ticked boxes live in the rail, so an answer that
// swapped only the form would silently drop the operator's own ticks on
// exactly the request where they must not be dropped (FR 1942d934). The
// rail inside it is re-derived from a FRESH read, so a question krill has
// since resolved is gone rather than offered again.
func (app *App) renderAnswerFormFailure(w http.ResponseWriter, r *http.Request, productID, id uuid.UUID, err error, followUp string, resolved []string) {
	checked := make(map[string]bool, len(resolved))
	for _, qid := range resolved {
		checked[qid] = true
	}

	// The reason, in the one sentence rule the two failure shapes share: a
	// rejection that reached krill shows api's own status and named
	// message, and anything else never reached krill at all, so it gets
	// the operator-facing half only -- the specific cause is logged, never
	// rendered (transportFailureMessage).
	var rejection *writeRejection
	var message string
	if errors.As(err, &rejection) {
		message = fmt.Sprintf("%d: %s", rejection.status, rejection.message)
	} else {
		message = "Could not reach krill: " + transportFailureMessage(err)
	}

	// The rail is re-read for EVERY refusal, not only the rejected-write
	// one: the read rides a different client from the write, so it can
	// still succeed when the write never reached krill, and a question
	// krill has resolved in the meantime must not come back as a box to
	// tick again.
	detail, detailErr := app.buildDesignSessionDetail(r.Context(), productID, id, time.Now())
	if detailErr != nil {
		// The re-read failed too. The operator's work is still not lost:
		// both regions say so in place rather than rendering as empty --
		// an unread question list is not a session with nothing waiting on
		// it (NFR ca90dc03) -- and the ticked ids ride as hidden inputs,
		// because there is no rail left to render a box in.
		logger.Error("failed to re-render the answer round after a refusal", "design_session_id", id, "error", detailErr)
		detail = pages.DesignSessionDetailPage{
			ID:             id.String(),
			Error:          message + " (this session's timeline and open questions could not be reloaded)",
			FollowUp:       followUp,
			CheckedResolve: checked,
			AnswersPath:    designAnswersPath(productID, id),
			LogError:       "This session's timeline could not be loaded.",
			QuestionsError: "This session's open questions could not be loaded.",
		}
	} else {
		detail.Error = message
		detail.FollowUp = followUp
		detail.CheckedResolve = checked
	}

	// Both modes answer 200, never the rejection's status: htmx does not
	// swap on an error status, and a bare status page would cost the
	// operator the paragraph they just typed.
	//
	// An unresolved operator identity is the one exception. A 401 is the
	// signal a browser acts on -- it is what tells the operator to sign in
	// again -- and no amount of prose in a 200 page replaces it.
	if errors.Is(err, errNoOperator) && !isHXRequest(r) {
		writeWriteError(w, err)
		return
	}
	if isHXRequest(r) {
		renderFragment(w, r, pages.DesignSessionRound(detail))
		return
	}
	app.renderShell(w, r, "Design session", r.URL.Path, pages.DesignSessionDetail(detail))
}

// renderAnswerSuccess answers a successful htmx follow-up in place: 200,
// and the same round region re-derived from a fresh read, so the timeline
// carries the appended event, the rail no longer offers the questions the
// round closed, and the textarea is empty. The confirmation is an
// out-of-band toast rather than a navigation -- a success here is not a
// move to a different page, and htmx cannot do a partial navigation.
//
// If the fresh read fails the write still landed, so the region comes back
// degraded with that said out loud, and the toast still confirms: telling
// an operator their answer was lost when it was not would cost them the
// round they just spent.
func (app *App) renderAnswerSuccess(w http.ResponseWriter, r *http.Request, productID, id uuid.UUID) {
	detail, err := app.buildDesignSessionDetail(r.Context(), productID, id, time.Now())
	if err != nil {
		logger.Error("follow-up saved, but the session could not be re-read", "design_session_id", id, "error", err)
		detail = pages.DesignSessionDetailPage{
			ID:             id.String(),
			Error:          "Your follow-up was saved, but this session could not be reloaded.",
			AnswersPath:    designAnswersPath(productID, id),
			LogError:       "This session's timeline could not be loaded.",
			QuestionsError: "This session's open questions could not be loaded.",
		}
	}
	renderFragment(w, r, withToast(answerSuccessToast, pages.DesignSessionRound(detail)))
}

// hxRedirect answers a successful doubled-form write for an htmx caller:
// 200 with an HX-Redirect, so the browser performs the same
// POST/Redirect/Get navigation the no-JS branch performs with a 303. This
// is the one HX-Redirect in this surface, and it earns its place -- the
// success outcome navigates to a different page (the new session's detail
// page), which an hx-swap fragment cannot express. The no-HX branch is
// untouched, so its 303 + Location behaviour is unchanged.
func hxRedirect(w http.ResponseWriter, to string) {
	w.Header().Set("HX-Redirect", to)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleOpenDesignSessionForm is the browser form action behind the
// new-session blade. productID is the path value; the form's only field is
// opening_submission, so the write body stays product_id +
// opening_submission -- no entity reference, no identity, no session id
// read from the browser (LB4). On success it redirects
// (POST/Redirect/Get) to the new session's detail page, so a refresh cannot
// re-open the session. A refused write re-renders the blade with the
// operator's text preserved (renderOpenFormFailure).
//
// The form is doubled, so one route serves both a no-JS browser and an htmx
// one; the no-HX half of that pair is the 303 + Location below, left
// exactly as it was.
func (app *App) handleOpenDesignSessionForm(w http.ResponseWriter, r *http.Request) {
	productID, err := parseUUIDPathValue(w, r, "productID", "product")
	if err != nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Error("could not parse the open-session form body", "product_id", productID, "error", err)
		blade := app.newDesignSessionBlade(r, productID)
		blade.Error = "The form could not be read, so nothing was submitted. Try again."
		if isHXRequest(r) {
			renderFragment(w, r, pages.NewDesignSessionBlade(blade))
			return
		}
		page := app.designSessionListPage(r, productID, nil)
		page.NewBlade = blade
		app.renderShell(w, r, "Design sessions", r.URL.Path, pages.DesignSessionList(page))
		return
	}
	opening := strings.TrimSpace(r.PostFormValue("opening_submission"))
	if opening == "" {
		// Client-side `required` catches the empty case, but a whitespace-only
		// submission slips past it; re-render the form with the message rather
		// than a bare 400 so the operator stays in context.
		app.renderOpenFormFailure(w, r, productID, "", &writeRejection{
			status:  http.StatusBadRequest,
			message: "Describe your idea in plain language before opening the session.",
		})
		return
	}

	var created createdResponse
	err = app.withKrillSession(r.Context(), func(ctx context.Context, sessionID store.SessionID) error {
		return app.writeAndDecode(ctx, sessionID, http.MethodPost, "design-sessions",
			openDesignSessionRequest{ProductID: productID.String(), OpeningSubmission: opening}, &created)
	})
	if err != nil {
		app.renderOpenFormFailure(w, r, productID, opening, err)
		return
	}

	id, err := parseUUIDOrFail(w, r, created.ID, "design session id")
	if err != nil {
		return
	}
	// Both outcomes below are navigations -- a 303 for a no-JS browser, an
	// HX-Redirect for an htmx one -- and neither carries a body to state
	// the outcome in, so the confirmation rides the same one-shot cookie
	// every other redirect-after-post uses. The landing page is the new
	// session's own detail page, which is the page the operator needs to
	// read next anyway.
	flashSuccess(w, "Design session opened.")
	if isHXRequest(r) {
		hxRedirect(w, designSessionPath(productID, id))
		return
	}
	http.Redirect(w, r, designSessionPath(productID, id), http.StatusSeeOther)
}

// handleDesignSessionAnswerForm is the browser form action behind a session
// detail page's "submit follow-up" form. It appends one `answer` revision
// round via api's AppendRevisionEventHandler, then redirects back to the
// session's detail page (POST/Redirect/Get). A rejected write re-renders the
// detail page in-shell with the operator's text and ticks preserved
// (renderAnswerFormFailure).
//
// Doubled form, one route: the no-HX half answers the 303 + Location below,
// untouched; the HX half answers 200 and either HX-Redirects on success or
// re-renders this form with the error inline.
func (app *App) handleDesignSessionAnswerForm(w http.ResponseWriter, r *http.Request) {
	productID, err := parseUUIDPathValue(w, r, "productID", "product")
	if err != nil {
		return
	}
	id, err := parseUUIDPathValue(w, r, "id", "design session")
	if err != nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		// Nothing was submitted, so there is no operator work to preserve --
		// but the answer is still the round region rather than a bare form,
		// because that region is the form's hx-target and a fragment without
		// its id would leave htmx deleting the element it was meant to
		// replace (htmxui ARCHITECTURE, the swap-target rule).
		logger.Error("could not parse the follow-up form body", "design_session_id", id, "error", err)
		app.renderAnswerFormFailure(w, r, productID, id, &writeRejection{
			status:  http.StatusBadRequest,
			message: "The form could not be read, so nothing was submitted. Try again.",
		}, "", nil)
		return
	}

	followUp := strings.TrimSpace(r.PostFormValue("follow_up"))
	resolved := nonEmptyValues(r.PostForm["resolve"])
	if followUp == "" && len(resolved) == 0 {
		// An answer round with no follow-up text and nothing resolved records
		// nothing meaningful; reject rather than append an empty `answer`.
		app.renderAnswerFormFailure(w, r, productID, id, &writeRejection{
			status:  http.StatusBadRequest,
			message: "Write a follow-up, or tick an open question your answer closes.",
		}, followUp, resolved)
		return
	}

	// A tick naming a question that is no longer open is refused HERE, not
	// by krill. The store validates a resolve against the questions EVER
	// opened in the session -- re-resolving an already-resolved one is a
	// deliberate no-op, not a 400 -- so a stale tick posted as-is is
	// accepted, the round is recorded, and the operator is told their
	// resolve saved while it was silently dropped (FR 1942d934).
	//
	// The WHOLE round is refused: a partially applied one, where the text
	// lands and the resolve does not, is worse than none.
	if len(resolved) > 0 {
		stale, staleErr := app.staleResolveTicks(r.Context(), id, resolved)
		if staleErr != nil {
			// The open set could not be read, so staleness cannot be ruled
			// out. Refusing rather than posting: a second failed read costs
			// the operator one retry, while posting blind reopens exactly
			// the silent drop this check exists to prevent.
			app.renderAnswerFormFailure(w, r, productID, id, staleErr, followUp, resolved)
			return
		}
		if len(stale) > 0 {
			app.renderAnswerFormFailure(w, r, productID, id, staleTickRejection(len(stale)), followUp, resolved)
			return
		}
	}

	// The revision_event schema (migration 008) has no free-text prose column,
	// and FR 1ff1c1e9 forbids the answer from proposing or amending a
	// Feature/Requirement entity -- which rules out entity_deltas[].summary_line,
	// the only other text carrier. So non-empty follow-up text is recorded as a
	// non-blocking opened question in this same answer round; a resolve-only
	// round sends no opened question. entity_deltas is always empty: the UI
	// touches no spec entity, and the mediated propose_entities path stays the
	// only thing that turns a submission into one.
	body := answerRevisionEventRequest{
		EventType:    string(store.EventTypeAnswer),
		EntityDeltas: []answerEntityDelta{}, // the UI never proposes/amends an entity
		OpenQuestionsDelta: answerQuestionsDelta{
			Opened:   []answerOpenedQuestion{},
			Resolved: resolved,
		},
		// An `answer` round must leave both nil: FR3 requires verified_against
		// only for draft/reconciliation and forbids it otherwise, and FR4 does
		// the same for signoff_status on non-signoff rounds.
		VerifiedAgainst: nil,
		SignoffStatus:   nil,
	}
	if followUp != "" {
		body.OpenQuestionsDelta.Opened = append(body.OpenQuestionsDelta.Opened, answerOpenedQuestion{
			QuestionID: newAnswerQuestionID(),
			Blocking:   false,
			Text:       followUp,
		})
	}

	var appended createdRevisionEvent
	err = app.withKrillSession(r.Context(), func(ctx context.Context, sessionID store.SessionID) error {
		return app.writeAndDecode(ctx, sessionID, http.MethodPost,
			"design-sessions/"+id.String()+"/revision-events", body, &appended)
	})
	if err != nil {
		app.renderAnswerFormFailure(w, r, productID, id, err, followUp, resolved)
		return
	}

	// The two outcomes answer differently, and both are right for their
	// mode. A no-JS browser navigated, so it gets the POST/Redirect/Get it
	// needs and the confirmation rides the flash cookie (a 303 has no body
	// to carry a message in). An htmx browser does NOT navigate -- a round
	// updates the page it is already on -- so it gets the round region
	// re-derived from a fresh read, in place, plus an out-of-band toast.
	//
	// The htmx success used to HX-Redirect to this same page, which cannot
	// express what actually happened: "the timeline gained an event, the
	// rail lost a question, the textarea cleared" is a partial update, and
	// a redirect is a full page load that throws the swap away.
	if isHXRequest(r) {
		app.renderAnswerSuccess(w, r, productID, id)
		return
	}
	flashSuccess(w, answerSuccessToast)
	http.Redirect(w, r, designSessionPath(productID, id), http.StatusSeeOther)
}

// staleResolveTicks returns the ticked question ids the session does not
// currently have OPEN, counting each id once.
//
// It reads the same accessor the rail is rendered from
// (store.RevisionEventStore.ListOpenQuestions, the one list_open_questions
// calls), so "open" means the same thing here as it does a few lines above on
// the page the operator ticked the box on. The store's own validation is
// deliberately NOT the oracle: it accepts an id resolved by any earlier round,
// which is what makes the check necessary rather than redundant.
func (app *App) staleResolveTicks(ctx context.Context, id uuid.UUID, ticked []string) ([]string, error) {
	open, err := app.revisionEvents.ListOpenQuestions(ctx, id)
	if err != nil {
		return nil, err
	}
	isOpen := make(map[string]bool, len(open))
	for _, q := range open {
		isOpen[q.QuestionID] = true
	}
	seen := make(map[string]bool, len(ticked))
	stale := make([]string, 0, len(ticked))
	for _, qid := range ticked {
		if isOpen[qid] || seen[qid] {
			continue
		}
		seen[qid] = true
		stale = append(stale, qid)
	}
	return stale, nil
}

// staleTickRejection is the operator-facing reason a round is refused
// because one or more of its ticks names a question another round has closed
// since the page was rendered.
//
// It names what happened rather than echoing the id: an operator who ticked a
// box does not recognise "q-flag-store", but does recognise "already closed
// since this page loaded". One and several read differently because they
// call for different operator action -- one is a stray tick, several mean the
// page is far enough out of date that its rail should not be trusted.
//
// The status is this binary's own conflict classification. krill sent no
// status: the round never reached it, which is the whole point -- the store
// would have accepted it and dropped the resolve.
func staleTickRejection(stale int) *writeRejection {
	if stale == 1 {
		return &writeRejection{
			status:  http.StatusConflict,
			message: "A question you ticked has already been closed since this page loaded, so nothing was sent. It is no longer listed above -- send again to record the rest.",
		}
	}
	return &writeRejection{
		status: http.StatusConflict,
		message: fmt.Sprintf("%d questions you ticked have already been closed since this page loaded, so nothing was sent. "+
			"They are no longer listed above -- reload the page to see the questions still open.", stale),
	}
}

// answerSuccessToast is the one message a submitted follow-up reports, on
// both paths: as a flash across the 303 for a no-JS browser, and as an
// out-of-band toast in the swapped region for an htmx one. One string, so
// the two modes cannot come to describe the same outcome differently.
const answerSuccessToast = "Follow-up submitted."

// newAnswerQuestionID mints the question id a follow-up answer opens. A UUID
// keeps it unique within the session's ever-opened set (FR6 validates only
// that the id is non-empty and, for a resolution, was opened somewhere in the
// session), so concurrent answers can never collide on one id.
func newAnswerQuestionID() string {
	return "a-" + uuid.NewString()
}

// ── small form helpers ───────────────────────────────────────────────────────

// parseUUIDPathValue parses a UUID path value, writing a 400 and returning an
// error if it is malformed, so every form handler rejects a bad id the same
// named way.
func parseUUIDPathValue(w http.ResponseWriter, r *http.Request, name, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid %s id: must be a UUID", what), http.StatusBadRequest)
		return uuid.Nil, err
	}
	return id, nil
}

// parseUUIDOrFail parses an id api returned, writing a 500 and returning an
// error if it is unusable. A malformed id in a 2xx is a contract break between
// this binary and `api`, not something to redirect with.
func parseUUIDOrFail(w http.ResponseWriter, r *http.Request, value, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		logger.Error("api returned an unusable id on a 2xx", "what", what, "error", err)
		if isHXRequest(r) {
			// The write landed; only the link back is unusable. Saying
			// that is far more useful than a silent 500 the operator
			// would resolve by resubmitting -- which would create a second
			// session.
			renderFragment(w, r, pages.OpsInlineError(
				"The session was created, but krill returned an id this UI could not link to. Find it under Design sessions."))
			return uuid.Nil, err
		}
		http.Error(w, fmt.Sprintf("api returned an unusable %s", what), http.StatusInternalServerError)
		return uuid.Nil, err
	}
	return id, nil
}

// nonEmptyValues drops empty checkbox values (an unchecked box contributes
// nothing, but keeps the slice free of blanks) while preserving order.
func nonEmptyValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
