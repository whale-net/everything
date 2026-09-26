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

// renderOpenFormFailure re-renders the "open a session" form after a
// rejected write, with the operator's opening text preserved so a rejection
// is never a data-loss event. A *writeRejection is shown inline as the
// api's status + named message; anything else (no resolved operator,
// unresolvable scope, unreachable api) never reached krill and falls back
// to writeWriteError's 502. If the page's own read fails to re-render,
// fall back to a plain status page.
//
// Both modes answer 200, never the rejection's status: the no-JS path
// re-renders the whole page in-shell, and the htmx path answers the form
// fragment alone so hx-swap="outerHTML" can drop the error in place.
func (app *App) renderOpenFormFailure(w http.ResponseWriter, r *http.Request, productID uuid.UUID, opening string, err error) {
	var rejection *writeRejection
	if !errors.As(err, &rejection) {
		// A transport failure (api unreachable, session mint failed) is
		// not a rejection and has no status to render. An htmx caller
		// must still see something: htmx does not swap on a non-2xx, so
		// writeWriteError's 401/502 would leave the operator with a form
		// that appears to do nothing at all -- the one case they most
		// need to be told about. Re-render the form with the message and
		// their typed submission intact.
		if isHXRequest(r) {
			renderFragment(w, r, pages.OpenSessionForm(pages.DesignSessionListPage{
				ProductID:         productID.String(),
				OpeningSubmission: opening,
				FormAction:        designProductSessionsPath(productID),
				Error:             "Could not reach krill: " + transportFailureMessage(err),
			}))
			return
		}
		writeWriteError(w, err)
		return
	}
	sessions, listErr := app.listDesignSessions(r.Context(), productID)
	if listErr != nil {
		logger.Error("failed to re-render open-session form after rejection", "product_id", productID, "error", listErr)
		if isHXRequest(r) {
			// The session list could not be re-read, so it is genuinely
			// unavailable -- but the operator's typed submission is still
			// known, and handing the form back with it costs nothing.
			renderFragment(w, r, pages.OpenSessionForm(pages.DesignSessionListPage{
				ProductID:         productID.String(),
				OpeningSubmission: opening,
				FormAction:        designProductSessionsPath(productID),
				Error:             fmt.Sprintf("%d: %s (the session list could not be reloaded)", rejection.status, rejection.message),
			}))
			return
		}
		http.Error(w, rejection.message, rejection.status)
		return
	}
	page := pages.DesignSessionListPage{
		ProductID:         productID.String(),
		Sessions:          sessions,
		Error:             fmt.Sprintf("%d: %s", rejection.status, rejection.message),
		OpeningSubmission: opening,
		FormAction:        designProductSessionsPath(productID),
	}
	if isHXRequest(r) {
		renderFragment(w, r, pages.OpenSessionForm(page))
		return
	}
	renderShell(w, r, "Design sessions", designPath, pages.DesignSessionList(page))
}

// transportFailureMessage is the operator-facing half of a non-rejection
// write failure. The specific cause is logged, not rendered: it can carry
// an internal api URL or a driver message.
func transportFailureMessage(err error) string {
	logger.Error("design write failed before reaching krill", "error", err)
	return "the request did not complete. Check the logs, then try again."
}

// renderAnswerFormFailure re-renders the "submit follow-up" form after a
// rejected write, preserving the follow-up text and which resolve boxes
// were ticked. Same 200-both-modes rule as renderOpenFormFailure.
func (app *App) renderAnswerFormFailure(w http.ResponseWriter, r *http.Request, id uuid.UUID, err error, followUp string, resolved []string) {
	checked := make(map[string]bool, len(resolved))
	for _, qid := range resolved {
		checked[qid] = true
	}
	// degradedForm is the form handed back when the detail read behind it
	// failed: the open questions are genuinely unavailable, but the
	// follow-up text and the ticked question ids are still known, so the
	// operator's work is not lost.
	degradedForm := func(message string) pages.DesignSessionDetailPage {
		return pages.DesignSessionDetailPage{
			ID:             id.String(),
			Error:          message,
			FollowUp:       followUp,
			CheckedResolve: checked,
			AnswersPath:    designAnswersPath(id),
			OpenQuestions:  nil,
		}
	}

	var rejection *writeRejection
	if !errors.As(err, &rejection) {
		if isHXRequest(r) {
			renderFragment(w, r, pages.FollowUpForm(degradedForm("Could not reach krill: "+transportFailureMessage(err))))
			return
		}
		writeWriteError(w, err)
		return
	}
	detail, detailErr := app.buildDesignSessionDetail(r.Context(), id)
	if detailErr != nil {
		logger.Error("failed to re-render answer form after rejection", "design_session_id", id, "error", detailErr)
		if isHXRequest(r) {
			renderFragment(w, r, pages.FollowUpForm(degradedForm(
				fmt.Sprintf("%d: %s (this session's open questions could not be reloaded)", rejection.status, rejection.message))))
			return
		}
		http.Error(w, rejection.message, rejection.status)
		return
	}
	detail.Error = fmt.Sprintf("%d: %s", rejection.status, rejection.message)
	detail.FollowUp = followUp
	detail.CheckedResolve = checked
	if isHXRequest(r) {
		renderFragment(w, r, pages.FollowUpForm(detail))
		return
	}
	renderShell(w, r, "Design session", designPath, pages.DesignSessionDetail(detail))
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

// handleOpenDesignSessionForm is the browser form action behind the product
// session list's "open a session" form. productID is the path value; the
// form's only field is opening_submission. On success it redirects
// (POST/Redirect/Get) to the new session's detail page, so a refresh cannot
// re-open the session. A rejected write re-renders the list in-shell with the
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
		if isHXRequest(r) {
			renderFragment(w, r, pages.OpenSessionForm(pages.DesignSessionListPage{
				ProductID:  productID.String(),
				FormAction: designProductSessionsPath(productID),
				Error:      "The form could not be read, so nothing was submitted. Try again.",
			}))
			return
		}
		http.Error(w, "invalid form body", http.StatusBadRequest)
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
	if isHXRequest(r) {
		hxRedirect(w, designSessionPath(id))
		return
	}
	http.Redirect(w, r, designSessionPath(id), http.StatusSeeOther)
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
	id, err := parseUUIDPathValue(w, r, "id", "design session")
	if err != nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Error("could not parse the follow-up form body", "design_session_id", id, "error", err)
		if isHXRequest(r) {
			renderFragment(w, r, pages.FollowUpForm(pages.DesignSessionDetailPage{
				ID:          id.String(),
				AnswersPath: designAnswersPath(id),
				Error:       "The form could not be read, so nothing was submitted. Try again.",
			}))
			return
		}
		http.Error(w, "invalid form body", http.StatusBadRequest)
		return
	}

	followUp := strings.TrimSpace(r.PostFormValue("follow_up"))
	resolved := nonEmptyValues(r.PostForm["resolve"])
	if followUp == "" && len(resolved) == 0 {
		// An answer round with no follow-up text and nothing resolved records
		// nothing meaningful; reject rather than append an empty `answer`.
		app.renderAnswerFormFailure(w, r, id, &writeRejection{
			status:  http.StatusBadRequest,
			message: "Write a follow-up, or tick an open question your answer closes.",
		}, followUp, resolved)
		return
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
		app.renderAnswerFormFailure(w, r, id, err, followUp, resolved)
		return
	}

	if isHXRequest(r) {
		hxRedirect(w, designSessionPath(id))
		return
	}
	http.Redirect(w, r, designSessionPath(id), http.StatusSeeOther)
}

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
