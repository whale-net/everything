// Design-session read surface: a product's session list, one session's
// revision-event log, and its open questions. Reads match what the Overview
// tiles and MCP tools read, so all surfaces agree.
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

// designSessionPath is a session's canonical, product-scoped detail URL; the
// pid lets the page check the session belongs to that product.
func designSessionPath(productID, id uuid.UUID) string {
	return designProductSessionsPath(productID) + "/" + id.String()
}

// legacyDesignSessionPath is the unscoped detail URL, kept for old links.
func legacyDesignSessionPath(id uuid.UUID) string {
	return designPath + "/design-sessions/" + id.String()
}

func designProductSessionsPath(productID uuid.UUID) string {
	return designPath + "/products/" + productID.String() + "/design-sessions"
}

// designAnswersPath is a session's follow-up answer action, under the
// canonical detail so its 303 lands without a second redirect.
func designAnswersPath(productID, id uuid.UUID) string {
	return designSessionPath(productID, id) + "/answers"
}

// designNewSessionBladePath is the new-session blade's URL. A literal segment
// outranks the {id} wildcard, so no session is addressed as "new".
func designNewSessionBladePath(productID uuid.UUID) string {
	return designProductSessionsPath(productID) + "/new"
}

const designGoPath = designPath + "/go"

// ── read helpers ─────────────────────────────────────────────────────────────

// designSessionRows builds the session list rows from the product-wide
// aggregate, newest first, using its derived stage so list and detail agree.
// An unknown product yields an empty list, not an error.
func (app *App) designSessionRows(ctx context.Context, productID uuid.UUID, now time.Time) ([]pages.DesignSessionRow, error) {
	summary, err := app.designSessions.SummarizeByProduct(ctx, productID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows := make([]pages.DesignSessionRow, 0, len(summary.Sessions))
	for _, s := range summary.Sessions {
		rows = append(rows, pages.DesignSessionRow{
			ID:                    s.ID.String(),
			OpeningRequest:        firstLine(s.OpeningSubmission),
			DetailPath:            designSessionPath(productID, s.ID),
			Stage:                 string(s.Stage),
			OpenBlockingQuestions: s.OpenBlockingQuestions,
			OpenedRelative:        relativeTime(s.CreatedAt, now),
			OpenedExact:           s.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return rows, nil
}

// firstLine is the trimmed first line of an opening submission.
func firstLine(s string) string {
	line := s
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// buildDesignSessionDetail assembles one session's view. Only the session read
// is fatal (ErrNotFound means 404); log and question read failures degrade to
// inline alerts. now is passed so all relative ages share one instant.
func (app *App) buildDesignSessionDetail(ctx context.Context, productID, id uuid.UUID, now time.Time) (pages.DesignSessionDetailPage, error) {
	summary, err := app.designSessions.GetSummaryByID(ctx, id)
	if err != nil {
		return pages.DesignSessionDetailPage{}, err
	}
	events, logErr := app.revisionEvents.ListBySession(ctx, id)
	questions, questionsErr := app.revisionEvents.ListOpenQuestions(ctx, id)
	if logErr != nil {
		logger.Error("design session timeline read failed", "design_session_id", id, "error", logErr)
	}
	if questionsErr != nil {
		logger.Error("design session open questions read failed", "design_session_id", id, "error", questionsErr)
	}
	openedBy, openedByTitle := openingOperatorLabel(summary.OpenedBy)
	// SignedOff reuses the store-derived stage so the badge and form agree.
	page := pages.DesignSessionDetailPage{
		ID:                     summary.ID.String(),
		ProductID:              summary.ProductID.String(),
		ProductName:            app.productNameFor(ctx, productID),
		ProductOverviewPath:    productHref(productID, overviewSuffix),
		OpeningRequest:         firstLine(summary.OpeningSubmission),
		OpeningSubmission:      summary.OpeningSubmission,
		Stage:                  string(summary.Stage),
		SignedOff:              summary.Stage == store.StageApproved,
		OpenedBy:               openedBy,
		OpenedByTitle:          openedByTitle,
		OpenedByKrillSessionID: summary.OpenedByKrillSessionID.String(),
		CreatedAt:              formatTime(summary.CreatedAt),
		ProductSessionsPath:    designProductSessionsPath(summary.ProductID),
		AnswersPath:            designAnswersPath(summary.ProductID, summary.ID),
	}
	if logErr == nil {
		page.Events = revisionEventRows(events, now)
	} else {
		page.LogError = "This session's timeline could not be loaded."
	}
	if questionsErr == nil {
		page.OpenQuestions = openQuestionRows(questions)
	} else {
		page.QuestionsError = "This session's open questions could not be loaded."
	}
	return page, nil
}

// openingOperatorLabel returns the opener's sub, with iss@sub as the title. An
// unreadable opener renders as empty rather than a placeholder identity.
func openingOperatorLabel(s store.Subject) (label, title string) {
	if s.Sub == "" {
		return "", ""
	}
	return s.Sub, s.Sub + "@" + s.Iss
}

func revisionEventRows(events []store.RevisionEvent, now time.Time) []pages.RevisionEventRow {
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
		acting := subjectLabel(ev.Acting)
		onBehalfOf := subjectLabel(ev.OnBehalfOf)
		rows = append(rows, pages.RevisionEventRow{
			ID:                ev.ID.String(),
			SeqNo:             ev.SeqNo,
			EventType:         string(ev.EventType),
			Acting:            acting,
			OnBehalfOf:        onBehalfOf,
			OnBehalfOfDiffers: ev.OnBehalfOf.Sub != "" && onBehalfOf != acting,
			VerifiedAgainst:   derefString(ev.VerifiedAgainst),
			SignoffStatus:     derefSignoff(ev.SignoffStatus),
			CreatedAt:         formatTime(ev.CreatedAt),
			AtRelative:        relativeTime(ev.CreatedAt, now),
			AtExact:           ev.CreatedAt.UTC().Format(time.RFC3339),
			Summary:           revisionEventSummary(ev),
			EntityDeltas:      deltas,
			OpenedQuestions:   opened,
			ResolvedQuestions: ev.OpenQuestionsDelta.Resolved,
		})
	}
	return rows
}

// revisionEventSummary composes a timeline entry's sentence from the round's
// own entity deltas, opened questions, and resolved ids, since revision_event
// has no prose column.
func revisionEventSummary(ev store.RevisionEvent) string {
	parts := make([]string, 0, 3)
	if clause := entityDeltaSummary(ev.EntityDeltas); clause != "" {
		parts = append(parts, clause)
	}
	if clause := openedQuestionsSummary(ev.OpenQuestionsDelta.Opened); clause != "" {
		parts = append(parts, clause)
	}
	if clause := resolvedQuestionsSummary(ev.OpenQuestionsDelta.Resolved); clause != "" {
		parts = append(parts, clause)
	}
	if len(parts) == 0 {
		// A round with nothing recorded (e.g. a signoff) still gets a sentence.
		return "No further detail was recorded for this round."
	}
	return joinSentences(parts)
}

// entityDeltaSummary returns the round's summary lines, or a bare count when
// none were written.
func entityDeltaSummary(deltas []store.EntityDelta) string {
	if len(deltas) == 0 {
		return ""
	}
	lines := make([]string, 0, len(deltas))
	for _, d := range deltas {
		if s := strings.TrimSpace(d.SummaryLine); s != "" {
			lines = append(lines, s)
		}
	}
	if len(lines) == 0 {
		if len(deltas) == 1 {
			return "1 entity change"
		}
		return fmt.Sprintf("%d entity changes", len(deltas))
	}
	return strings.Join(lines, "; ")
}

func openedQuestionsSummary(opened []store.OpenQuestionOpened) string {
	if len(opened) == 0 {
		return ""
	}
	texts := make([]string, 0, len(opened))
	for _, q := range opened {
		if t := strings.TrimSpace(q.Text); t != "" {
			texts = append(texts, t)
		} else {
			// A question with no text is described by its id.
			texts = append(texts, q.QuestionID)
		}
	}
	if len(opened) == 1 {
		return fmt.Sprintf("1 question opened: %s", texts[0])
	}
	return fmt.Sprintf("%d questions opened: %s", len(opened), strings.Join(texts, "; "))
}

func resolvedQuestionsSummary(resolved []string) string {
	switch len(resolved) {
	case 0:
		return ""
	case 1:
		return "resolved " + resolved[0]
	case 2:
		return "resolved " + resolved[0] + " and " + resolved[1]
	default:
		return "resolved " + strings.Join(resolved[:len(resolved)-1], ", ") + " and " + resolved[len(resolved)-1]
	}
}

// joinSentences ends each clause with a period unless it already ends in "."
// or "?".
func joinSentences(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p)
		if !strings.HasSuffix(p, ".") && !strings.HasSuffix(p, "?") {
			b.WriteByte('.')
		}
	}
	return b.String()
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

// formatTime renders a timestamp in UTC; the zero time renders as "".
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleDesignGo redirects a legacy /design/go?product_id=X bookmark to that
// product's session list. The id is parsed as a UUID, so there is no
// open-redirect surface.
func (app *App) handleDesignGo(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("product_id")))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, designProductSessionsPath(productID), http.StatusFound)
}

// handleDesignSessionList renders a product's design sessions, as a bare
// fragment for htmx or inside the shell otherwise.
func (app *App) handleDesignSessionList(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("productID"))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	app.renderDesignSessionList(w, r, productID, r.URL.Path)
}

// designSessionListPage builds the list view model around already-read rows,
// shared by the list URL, the /design root, and the blade's full-page render.
func (app *App) designSessionListPage(r *http.Request, productID uuid.UUID, rows []pages.DesignSessionRow) pages.DesignSessionListPage {
	return pages.DesignSessionListPage{
		ProductID:    productID.String(),
		ProductName:  app.productNameFor(r.Context(), productID),
		Path:         designProductSessionsPath(productID),
		NewBladePath: designNewSessionBladePath(productID),
		Sessions:     rows,
		FormAction:   designProductSessionsPath(productID),
	}
}

// newDesignSessionBlade builds a fresh blade view; the write handler fills
// Error and OpeningSubmission after a refusal.
func (app *App) newDesignSessionBlade(r *http.Request, productID uuid.UUID) *pages.DesignSessionNewBlade {
	return &pages.DesignSessionNewBlade{
		ProductID:   productID.String(),
		ProductName: app.productNameFor(r.Context(), productID),
		FormAction:  designProductSessionsPath(productID),
		ListPath:    designProductSessionsPath(productID),
	}
}

// handleDesignSessionNew serves the new-session blade: the blade region alone
// for htmx, or the full list page with the blade open. The fragment path never
// reads the list, so the blade opens even if that read fails.
func (app *App) handleDesignSessionNew(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("productID"))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	blade := app.newDesignSessionBlade(r, productID)
	if isHXRequest(r) {
		renderFragment(w, r, pages.NewDesignSessionBlade(blade))
		return
	}
	// Read now once so every row's relative age shares one instant.
	rows, err := app.designSessionRows(r.Context(), productID, time.Now())
	if err != nil {
		logger.Error("failed to list design sessions", "product_id", productID, "error", err)
		http.Error(w, "failed to list design sessions", http.StatusInternalServerError)
		return
	}
	page := app.designSessionListPage(r, productID, rows)
	page.NewBlade = blade
	setLastViewedProductCookie(w, productID)
	// The nav key is the list's path; the blade is a view over it.
	app.renderShell(w, r, "Design sessions", designProductSessionsPath(productID), pages.DesignSessionList(page))
}

// renderDesignSessionList renders the session list for the product URL and the
// /design root. activePath is passed because the two own different nav keys.
func (app *App) renderDesignSessionList(w http.ResponseWriter, r *http.Request, productID uuid.UUID, activePath string) {
	// Read now once so every row's relative age shares one instant.
	rows, err := app.designSessionRows(r.Context(), productID, time.Now())
	if err != nil {
		logger.Error("failed to list design sessions", "product_id", productID, "error", err)
		http.Error(w, "failed to list design sessions", http.StatusInternalServerError)
		return
	}
	page := app.designSessionListPage(r, productID, rows)
	if isHXRequest(r) {
		renderFragment(w, r, pages.DesignSessionList(page))
		return
	}
	// Remember the product for later un-prefixed pages; an out-of-scope id is
	// dropped when the cookie is read.
	setLastViewedProductCookie(w, productID)
	app.renderShell(w, r, "Design sessions", activePath, pages.DesignSessionList(page))
}

// productNameFor returns the product's name for a page header, or "" if the
// scope listing cannot be read; a header is not worth failing the page.
func (app *App) productNameFor(ctx context.Context, productID uuid.UUID) string {
	if product, ok := currentProduct(ctx); ok && product.ID == productID {
		return product.Name
	}
	products, err := app.scopeProducts(ctx)
	if err != nil {
		logger.Warn("design sessions: product name read failed", "product_id", productID, "error", err)
		return ""
	}
	for _, p := range products {
		if p.ID == productID {
			return p.Name
		}
	}
	return ""
}

// handleDesignSessionDetail renders one session's log and open questions. An
// unknown session or one under another product is an in-shell 404.
func (app *App) handleDesignSessionDetail(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("productID"))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid design session id: must be a UUID", http.StatusBadRequest)
		return
	}
	detail, err := app.buildDesignSessionDetail(r.Context(), productID, id, time.Now())
	if errors.Is(err, store.ErrNotFound) || (err == nil && detail.ProductID != productID.String()) {
		if err == nil {
			logger.Info("design session not under the product the URL names",
				"product_id", productID, "design_session_id", id)
		}
		app.renderShellStatus(w, r, "Not found", r.URL.Path, pages.SpecStatus(pages.StatusPage{
			Title:  "Design session not found",
			Detail: "No design session of this id exists under this product. It may have been opened under another product, or the link may be out of date.",
		}), http.StatusNotFound)
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
	setLastViewedProductCookie(w, productID)
	app.renderShell(w, r, "Design session", r.URL.Path, pages.DesignSessionDetail(detail))
}

// isHXRequest reports whether the request came from htmx.
func isHXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") != ""
}
