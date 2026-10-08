// Design-session writes: open a session and submit an `answer` round. Both go
// through withKrillSession and call the same api operations as their MCP twins.
// Neither writes a spec entity; entity_deltas is always empty.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ── wire types ───────────────────────────────────────────────────────────────

// These types mirror api/handlers' appendRevisionEvent* shapes field for
// field, so a browser write matches the same MCP tool write.

// answerEntityDelta mirrors entityDeltaRequest; always sent empty.
type answerEntityDelta struct {
	EntityID    string `json:"entity_id"`
	Change      string `json:"change"`
	SummaryLine string `json:"summary_line"`
}

type answerOpenedQuestion struct {
	QuestionID string `json:"question_id"`
	Blocking   bool   `json:"blocking"`
	Text       string `json:"text"`
}

type answerQuestionsDelta struct {
	Opened   []answerOpenedQuestion `json:"opened"`
	Resolved []string               `json:"resolved"`
}

// answerRevisionEventRequest has no identity or scope fields: those always
// come from the krill session.
type answerRevisionEventRequest struct {
	EventType          string               `json:"event_type"`
	EntityDeltas       []answerEntityDelta  `json:"entity_deltas"`
	OpenQuestionsDelta answerQuestionsDelta `json:"open_questions_delta"`
	VerifiedAgainst    *string              `json:"verified_against"`
	SignoffStatus      *string              `json:"signoff_status"`
}

// createdResponse and createdRevisionEvent mirror api's IDResponse and
// RevisionEventCreatedResponse.
type createdResponse struct {
	ID string `json:"id"`
}

type createdRevisionEvent struct {
	ID    string `json:"id"`
	SeqNo int    `json:"seq_no"`
}

// ── write plumbing ───────────────────────────────────────────────────────────

// writeRejection is a non-2xx api response: a normal outcome carrying api's
// status and message, shown to operators only via operatorRejectionText.
type writeRejection struct {
	status  int
	message string
}

func (w *writeRejection) Error() string { return w.message }

// writeAndDecode issues one write and decodes a 2xx body into out. Non-2xx
// becomes *writeRejection; a transport failure returns the underlying error.
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

// ── refusal text ────────────────────────────────────────────────────────────

// operatorRejectionText renders a rejection for an operator. api's error body
// is a verbatim contract for MCP clients, so store text is translated here
// instead; unrecognised store text degrades to a generic refusal.
func operatorRejectionText(rejection *writeRejection) string {
	cause, isStoreValidation := strings.CutPrefix(rejection.message, store.ErrInvalidRevisionEvent.Error()+": ")
	if !isStoreValidation {
		return fmt.Sprintf("%d: %s", rejection.status, rejection.message)
	}

	if id, ok := neverOpenedQuestionID(cause); ok {
		// Quote the id: an unopened question is not in the rail to see.
		return fmt.Sprintf("%d: Question %s was never opened in this session, so nothing was sent. "+
			"It is not one of the open questions listed above -- reload the page to see the questions still open.",
			rejection.status, id)
	}

	return fmt.Sprintf("%d: krill could not accept this round, so nothing was sent. "+
		"Reload the page to see this session's current state, then try again.", rejection.status)
}

// neverOpenedQuestionID extracts the id from the store's "never opened"
// validation error by its format string; a reword falls back to the generic
// refusal.
func neverOpenedQuestionID(cause string) (string, bool) {
	const (
		head = "open_questions_delta.resolved names "
		tail = ", which was never opened in this session"
	)
	rest, ok := strings.CutPrefix(cause, head)
	if !ok {
		return "", false
	}
	quoted, ok := strings.CutSuffix(rest, tail)
	if !ok {
		return "", false
	}
	id, err := strconv.Unquote(quoted)
	if err != nil {
		return "", false
	}
	return id, true
}

// renderOpenFormFailure re-renders the new-session blade with the operator's
// text preserved. Always 200, since htmx does not swap on error statuses;
// non-rejection causes are logged, never rendered.
func (app *App) renderOpenFormFailure(w http.ResponseWriter, r *http.Request, productID uuid.UUID, opening string, err error) {
	blade := app.newDesignSessionBlade(r, productID)
	blade.OpeningSubmission = opening

	var rejection *writeRejection
	if errors.As(err, &rejection) {
		blade.Error = operatorRejectionText(rejection)
	} else {
		blade.Error = "Could not reach krill: " + transportFailureMessage(err)
	}

	page := app.designSessionListPage(r, productID, nil)
	page.NewBlade = blade

	// If the list read fails, still return the blade and say so inline.
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

// transportFailureMessage is the operator-facing text for a non-rejection
// failure; the cause can carry internal URLs, so it is logged only.
func transportFailureMessage(err error) string {
	logger.Error("design write failed before reaching krill", "error", err)
	return "the request did not complete. Check the logs, then try again."
}

// renderAnswerFormFailure re-renders the whole round region (timeline, rail,
// form) so the operator's ticks and text survive; the rail is re-read so
// resolved questions are not offered again.
func (app *App) renderAnswerFormFailure(w http.ResponseWriter, r *http.Request, productID, id uuid.UUID, err error, followUp string, resolved []string) {
	checked := make(map[string]bool, len(resolved))
	for _, qid := range resolved {
		checked[qid] = true
	}

	// Check errors.As first: transportFailureMessage logs at ERROR, which would
	// be wrong for an ordinary rejection.
	var rejection *writeRejection
	var message string
	if errors.As(err, &rejection) {
		message = operatorRejectionText(rejection)
	} else {
		message = "Could not reach krill: " + transportFailureMessage(err)
	}

	// Re-read the rail for every refusal; the read may succeed even when the
	// write never reached krill.
	detail, detailErr := app.buildDesignSessionDetail(r.Context(), productID, id, time.Now())
	if detailErr != nil {
		// The re-read failed too: both regions say so, and ticked ids ride as hidden
		// inputs since there is no rail to hold them.
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

	// Always 200 so htmx swaps and typed text survives, except a missing
	// operator on the no-JS path: its 401 prompts re-sign-in.
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

// renderAnswerSuccess re-renders the round region from a fresh read plus an
// out-of-band toast. If the read fails the write still landed, so the toast
// still confirms.
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

// hxRedirect is the htmx counterpart of a 303: 200 with HX-Redirect, for a
// success that navigates to a different page.
func hxRedirect(w http.ResponseWriter, to string) {
	w.Header().Set("HX-Redirect", to)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleOpenDesignSessionForm backs the new-session blade for both no-JS and
// htmx. The body is only product_id + opening_submission; success redirects
// to the new session (POST/Redirect/Get).
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
		// `required` misses whitespace-only input; re-render in context.
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
	// Both outcomes are navigations with no body, so confirm via the flash cookie.
	flashSuccess(w, "Design session opened.")
	if isHXRequest(r) {
		hxRedirect(w, designSessionPath(productID, id))
		return
	}
	http.Redirect(w, r, designSessionPath(productID, id), http.StatusSeeOther)
}

// handleDesignSessionAnswerForm appends one `answer` round. No-JS gets a 303
// back to the session; htmx gets the round region in place. Rejections
// preserve text and ticks.
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
		// Return the round region, not a bare form: it is the hx-target, and a
		// fragment without its id would delete the element.
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
		// No text and nothing resolved: reject rather than append an empty round.
		app.renderAnswerFormFailure(w, r, productID, id, &writeRejection{
			status:  http.StatusBadRequest,
			message: "Write a follow-up, or tick an open question your answer closes.",
		}, followUp, resolved)
		return
	}

	// Refuse stale ticks here: the store accepts re-resolving an already-resolved
	// question as a no-op, which would silently drop the resolve. The whole round
	// is refused rather than partially applied.
	if len(resolved) > 0 {
		stale, staleErr := app.staleResolveTicks(r.Context(), id, resolved)
		if staleErr != nil {
			// Staleness cannot be ruled out, so refuse rather than post blind.
			app.renderAnswerFormFailure(w, r, productID, id, staleErr, followUp, resolved)
			return
		}
		if len(stale) > 0 {
			app.renderAnswerFormFailure(w, r, productID, id, staleTickRejection(len(stale)), followUp, resolved)
			return
		}
	}

	// revision_event has no free-text column and answers may not touch spec
	// entities, so follow-up text is recorded as a non-blocking opened question.
	body := answerRevisionEventRequest{
		EventType:    string(store.EventTypeAnswer),
		EntityDeltas: []answerEntityDelta{}, // the UI never proposes/amends an entity
		OpenQuestionsDelta: answerQuestionsDelta{
			Opened:   []answerOpenedQuestion{},
			Resolved: resolved,
		},
		// `answer` rounds must leave verified_against and signoff_status nil.
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

	// No-JS navigated, so it gets a 303 and a flash; htmx stays on the page and
	// gets the round region in place plus a toast.
	if isHXRequest(r) {
		app.renderAnswerSuccess(w, r, productID, id)
		return
	}
	flashSuccess(w, answerSuccessToast)
	http.Redirect(w, r, designSessionPath(productID, id), http.StatusSeeOther)
}

// staleResolveTicks returns ticked ids not currently open, using the same
// accessor the rail renders from. The store's validation accepts ids closed
// by any earlier round, so it cannot be the oracle.
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

// staleTickRejection refuses a round whose ticks name questions closed since
// the page loaded. The conflict status is ours; the round never reached krill.
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

// answerSuccessToast is shared by the flash and the toast so both modes say
// the same thing.
const answerSuccessToast = "Follow-up submitted."

// newAnswerQuestionID mints a UUID so concurrent answers never collide within
// the session's ever-opened question set.
func newAnswerQuestionID() string {
	return "a-" + uuid.NewString()
}

// ── small form helpers ───────────────────────────────────────────────────────

// parseUUIDPathValue parses a UUID path value, writing a 400 if malformed.
func parseUUIDPathValue(w http.ResponseWriter, r *http.Request, name, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid %s id: must be a UUID", what), http.StatusBadRequest)
		return uuid.Nil, err
	}
	return id, nil
}

// parseUUIDOrFail parses an id api returned, writing a 500 if unusable: a
// malformed id in a 2xx is a contract break.
func parseUUIDOrFail(w http.ResponseWriter, r *http.Request, value, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		logger.Error("api returned an unusable id on a 2xx", "what", what, "error", err)
		if isHXRequest(r) {
			// The write landed; say so, or a resubmit would create a second session.
			renderFragment(w, r, pages.OpsInlineError(
				"The session was created, but krill returned an id this UI could not link to. Find it under Design sessions."))
			return uuid.Nil, err
		}
		http.Error(w, fmt.Sprintf("api returned an unusable %s", what), http.StatusInternalServerError)
		return uuid.Nil, err
	}
	return id, nil
}

// nonEmptyValues drops empty checkbox values, preserving order.
func nonEmptyValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
