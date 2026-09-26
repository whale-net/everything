// The design-session read surface: a product's session list, one session's
// full ordered revision-event log, and its currently-open questions. Every
// read here goes through the exact store accessors the MCP tools'
// get_design_session and list_open_questions call
// (store.DesignSessionStore.GetByID/ListByProduct and
// store.RevisionEventStore.ListBySession/ListOpenQuestions), so a browser
// and an MCP client see one session, one ordering, and one last-event-wins
// open-question derivation -- never a parallel query.
//
// Unlike a write, a read carries no attribution, so it needs no krill
// session: api's own GET /design-sessions/{id} and
// GET /design-sessions/{id}/open-questions are ungated for the same reason
// (krill/ARCHITECTURE/12-design-session-revision-event-http.md). These
// pages are behind the sign-in gate only, exactly like the rest of the
// nav shell.
//
// The view models themselves (pages.DesignSessionListPage and friends) are
// declared in krill/ui/pages/design.templ beside the components that
// render them; the pure builders that populate them stay here.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// designSessionPath is one session's detail page.
func designSessionPath(id uuid.UUID) string {
	return designPath + "/design-sessions/" + id.String()
}

// designProductSessionsPath is a product's session list.
func designProductSessionsPath(productID uuid.UUID) string {
	return designPath + "/products/" + productID.String() + "/design-sessions"
}

// designAnswersPath is one session's follow-up answer action.
func designAnswersPath(id uuid.UUID) string {
	return designSessionPath(id) + "/answers"
}

// designGoPath is the design root's product-id browse target.
const designGoPath = designPath + "/go"

// ── read helpers ─────────────────────────────────────────────────────────────

// listDesignSessions returns productID's design sessions as list rows, ordered
// oldest-first exactly as DesignSessionStore.ListByProduct returns them. Each
// row's signed-off state is derived from that session's own revision-event
// log, the one source of truth for a session's current state (the
// design_session row itself is never updated -- FR1's boundary comment).
func (app *App) listDesignSessions(ctx context.Context, productID uuid.UUID) ([]pages.DesignSessionRow, error) {
	sessions, err := app.designSessions.ListByProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	rows := make([]pages.DesignSessionRow, 0, len(sessions))
	for _, ds := range sessions {
		events, err := app.revisionEvents.ListBySession(ctx, ds.ID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, pages.DesignSessionRow{
			ID:                ds.ID.String(),
			OpeningSubmission: ds.OpeningSubmission,
			CreatedAt:         formatTime(ds.CreatedAt),
			SignedOff:         isSignedOff(events),
			DetailPath:        designSessionPath(ds.ID),
		})
	}
	return rows, nil
}

// isSignedOff reports whether a session's log ends in an approved signoff --
// FR4's closed outcome, read last-signoff-wins. A later changes_requested
// signoff reopens the session.
func isSignedOff(events []store.RevisionEvent) bool {
	signedOff := false
	for _, ev := range events {
		if ev.EventType != store.EventTypeSignoff {
			continue
		}
		signedOff = ev.SignoffStatus != nil && *ev.SignoffStatus == store.SignoffStatusApproved
	}
	return signedOff
}

// buildDesignSessionDetail assembles one session's read view from the same
// three store reads get_design_session (GetByID + ListBySession) and
// list_open_questions (GetByID + ListOpenQuestions) perform.
func (app *App) buildDesignSessionDetail(ctx context.Context, id uuid.UUID) (pages.DesignSessionDetailPage, error) {
	ds, err := app.designSessions.GetByID(ctx, id)
	if err != nil {
		return pages.DesignSessionDetailPage{}, err
	}
	events, err := app.revisionEvents.ListBySession(ctx, id)
	if err != nil {
		return pages.DesignSessionDetailPage{}, err
	}
	questions, err := app.revisionEvents.ListOpenQuestions(ctx, id)
	if err != nil {
		return pages.DesignSessionDetailPage{}, err
	}
	return pages.DesignSessionDetailPage{
		ID:                     ds.ID.String(),
		ProductID:              ds.ProductID.String(),
		OpeningSubmission:      ds.OpeningSubmission,
		OpenedByKrillSessionID: ds.OpenedByKrillSessionID.String(),
		CreatedAt:              formatTime(ds.CreatedAt),
		ProductSessionsPath:    designProductSessionsPath(ds.ProductID),
		Events:                 revisionEventRows(events),
		OpenQuestions:          openQuestionRows(questions),
		AnswersPath:            designAnswersPath(ds.ID),
	}, nil
}

func revisionEventRows(events []store.RevisionEvent) []pages.RevisionEventRow {
	rows := make([]pages.RevisionEventRow, 0, len(events))
	for _, ev := range events {
		deltas := make([]pages.EntityDeltaRow, 0, len(ev.EntityDeltas))
		for _, d := range ev.EntityDeltas {
			deltas = append(deltas, pages.EntityDeltaRow{
				EntityID:    d.EntityID.String(),
				Change:      string(d.Change),
				SummaryLine: d.SummaryLine,
			})
		}
		opened := make([]pages.OpenQuestionDeltaRow, 0, len(ev.OpenQuestionsDelta.Opened))
		for _, q := range ev.OpenQuestionsDelta.Opened {
			opened = append(opened, pages.OpenQuestionDeltaRow{
				QuestionID: q.QuestionID,
				Text:       q.Text,
				Blocking:   blockingTag(q.Blocking),
			})
		}
		rows = append(rows, pages.RevisionEventRow{
			ID:                ev.ID.String(),
			SeqNo:             ev.SeqNo,
			EventType:         string(ev.EventType),
			Acting:            subjectLabel(ev.Acting),
			OnBehalfOf:        subjectLabel(ev.OnBehalfOf),
			VerifiedAgainst:   derefString(ev.VerifiedAgainst),
			SignoffStatus:     derefSignoff(ev.SignoffStatus),
			CreatedAt:         formatTime(ev.CreatedAt),
			EntityDeltas:      deltas,
			OpenedQuestions:   opened,
			ResolvedQuestions: ev.OpenQuestionsDelta.Resolved,
		})
	}
	return rows
}

func openQuestionRows(questions []store.OpenQuestion) []pages.OpenQuestionRow {
	rows := make([]pages.OpenQuestionRow, 0, len(questions))
	for _, q := range questions {
		rows = append(rows, pages.OpenQuestionRow{
			QuestionID:    q.QuestionID,
			Text:          q.Text,
			Blocking:      blockingTag(q.Blocking),
			OpenedAtSeqNo: q.OpenedAtSeqNo,
		})
	}
	return rows
}

func subjectLabel(s store.Subject) string {
	return fmt.Sprintf("%s %s@%s", s.Kind, s.Sub, s.Iss)
}

func blockingTag(blocking bool) string {
	if blocking {
		return "blocking"
	}
	return "non-blocking"
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefSignoff(s *store.SignoffStatus) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

// formatTime renders a store timestamp in UTC; a zero time renders as the
// empty string rather than a misleading year-1 date.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleDesignGo is the design root's product-id browse target: a JS-free
// GET /design/go?product_id=X that validates the id and 302s to that
// product's session list. It replaces an inline <script> that read a text
// input and assigned window.location, duplicating in the browser a rule
// the server owns.
//
// The only user-controlled path segment is the product id, and it is
// parsed as a UUID before it is interpolated into a path this package
// builds -- so a malformed or hostile value is a 400, and there is no
// open-redirect surface.
func (app *App) handleDesignGo(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("product_id")))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, designProductSessionsPath(productID), http.StatusFound)
}

// handleDesignSessionList renders a product's design sessions -- the
// open and signed-off ones alike -- so a contributor can navigate into one
// without already holding its id. One route, two modes: an htmx request
// gets the page body as a bare fragment at 200, everything else gets it
// inside the shell.
func (app *App) handleDesignSessionList(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("productID"))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	sessions, err := app.listDesignSessions(r.Context(), productID)
	if err != nil {
		logger.Error("failed to list design sessions", "product_id", productID, "error", err)
		http.Error(w, "failed to list design sessions", http.StatusInternalServerError)
		return
	}
	page := pages.DesignSessionListPage{
		ProductID:  productID.String(),
		Sessions:   sessions,
		FormAction: designProductSessionsPath(productID),
	}
	if isHXRequest(r) {
		renderFragment(w, r, pages.DesignSessionList(page))
		return
	}
	renderShell(w, r, "Design sessions", designPath, pages.DesignSessionList(page))
}

// handleDesignSessionDetail renders one session's full ordered
// revision-event log and its currently-open questions. One route, two
// modes, exactly as handleDesignSessionList.
func (app *App) handleDesignSessionDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid design session id: must be a UUID", http.StatusBadRequest)
		return
	}
	detail, err := app.buildDesignSessionDetail(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "design session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		logger.Error("failed to load design session", "design_session_id", id, "error", err)
		http.Error(w, "failed to load design session", http.StatusInternalServerError)
		return
	}
	if isHXRequest(r) {
		renderFragment(w, r, pages.DesignSessionDetail(detail))
		return
	}
	renderShell(w, r, "Design session", designPath, pages.DesignSessionDetail(detail))
}

// isHXRequest reports whether the caller is htmx. One route serves both
// modes: the header is what selects chrome vs. bare fragment.
func isHXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") != ""
}
