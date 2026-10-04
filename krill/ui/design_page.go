// The design-session read surface: a product's session list, one session's
// full ordered revision-event log, and its currently-open questions.
//
// The list reads DesignSessionStore.SummarizeByProduct -- the one
// product-wide aggregate -- rather than the accessors the MCP tools call
// one at a time. That is the same read overview_tiles.go already makes, so
// the Overview's blocking-question tile and this table cannot disagree
// about a session's stage or its open blocking count, and neither costs a
// query per session. The detail reads the exact accessors get_design_session
// and list_open_questions call (GetByID, ListBySession, ListOpenQuestions),
// so a browser and an MCP client still see one session, one ordering, and
// one last-event-wins open-question derivation.
//
// Unlike a write, a read carries no attribution, so it needs no krill
// session. These pages are behind readerRoute (sign-in plus a reader or
// operator role), like the rest of the read pages.
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

// designSessionPath is one session's detail page -- product-scoped, which is
// what makes it canonical (FR a77852a9): a session id alone cannot answer
// "is this session under the product in the URL?", so the pid the reader
// arrived with has to be part of the address.
func designSessionPath(productID, id uuid.UUID) string {
	return designProductSessionsPath(productID) + "/" + id.String()
}

// legacyDesignSessionPath is the pre-redesign, unscoped detail URL. It is
// spelled here rather than assembled from designSessionPath because it is a
// different page's address, not this one's: it names no product, so it
// cannot be the successor any URL redirects into.
func legacyDesignSessionPath(id uuid.UUID) string {
	return designPath + "/design-sessions/" + id.String()
}

// designProductSessionsPath is a product's session list.
func designProductSessionsPath(productID uuid.UUID) string {
	return designPath + "/products/" + productID.String() + "/design-sessions"
}

// designAnswersPath is one session's follow-up answer action. It hangs off
// the canonical, product-scoped detail for the same reason that detail
// does: the handler answers 303 to the session it just wrote to, and a
// redirect that then had to 302 again would make the write's own outcome a
// two-hop navigation.
func designAnswersPath(productID, id uuid.UUID) string {
	return designSessionPath(productID, id) + "/answers"
}

// designNewSessionBladePath is the new-session blade's own URL (FR 44d7f1e2).
//
// It is a literal segment under the list, so it outranks the {id} wildcard
// the session detail registers at the same position in the same Go 1.22
// mux -- a session can never be addressed as "new".
//
// The blade being an ADDRESS rather than only a pane is the point: the
// list's primary action carries this as a real href, so a no-JS click, a
// reload and a shared link all open it, and a browser Back returns to the
// list underneath.
func designNewSessionBladePath(productID uuid.UUID) string {
	return designProductSessionsPath(productID) + "/new"
}

// designGoPath is the design root's product-id browse target.
const designGoPath = designPath + "/go"

// ── read helpers ─────────────────────────────────────────────────────────────

// designSessionRows builds productID's session list rows from the
// product-wide aggregate read (FR d0a63ffb), newest first, exactly as
// DesignSessionStore.SummarizeByProduct orders them.
//
// One call, never the loop this replaced: the aggregate already carries
// each session's derived Stage and its open blocking-question count, so
// re-deriving either here would be a second notion of the two facts this
// table is read for. Re-deriving the stage in particular is what would let
// a list and a session's own detail disagree about what stage it is in.
//
// An unknown product is an empty list rather than an error, which is what
// this page has always answered and what its read of the aggregate's
// ErrNotFound preserves: the URL resolved and there is nothing behind it,
// which the empty state says in as many words. Every other failure -- the
// one that is a broken read rather than an empty product -- is returned.
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

// firstLine is an opening submission's first line: what the list shows,
// where the full submission is the detail page's subject. Trimmed, because
// a submission that opens with a blank line would otherwise render a row
// whose title is empty.
func firstLine(s string) string {
	line := s
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// buildDesignSessionDetail assembles one session's read view.
//
// Three reads, each named for what it is the browser's twin of:
//
//   - GetSummaryByID is the aggregate the sessions LIST already reads this
//     same session out of, narrowed to one row. It supplies the derived
//     stage and the opening identity, so the badge beside this page's h1
//     is the same badge the row linking here showed. Reading the stage any
//     other way -- a ListLatestSignoffBySessionIDs call, or a second
//     derivation here -- is how a list and a detail come to disagree about
//     the same session's stage.
//   - ListBySession and ListOpenQuestions are the exact accessors
//     get_design_session and list_open_questions call, so the log and the
//     question set an operator reads are the ones an MCP client reads.
//
// The product's name and link come from the request the route already
// resolved (productNameFor), not from a read of the product here: the pid
// is in the path, so a second product lookup would be the same row asked
// for twice.
//
// only the SESSION read is fatal. ListBySession and ListOpenQuestions are
// read independently and independently tolerated (FR e5ad1a5b, NFR
// ca90dc03): a log that cannot be read costs the operator the timeline and
// nothing else, so the page renders with an alert where the timeline was
// and the rail still answers. An unknown or foreign session -- which is
// what GetSummaryByID returns ErrNotFound for -- stays the 404 path, because
// that is a missing page rather than a degraded one.
//
// now is passed rather than read from the clock so every event's relative
// age is measured against one instant: a timeline whose entries disagree
// about "now" by the time the page took to render reads as a history
// recorded in more than one timeline.
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
	page := pages.DesignSessionDetailPage{
		ID:                     summary.ID.String(),
		ProductID:              summary.ProductID.String(),
		ProductName:            app.productNameFor(ctx, productID),
		ProductOverviewPath:    productHref(productID, overviewSuffix),
		OpeningRequest:         firstLine(summary.OpeningSubmission),
		OpeningSubmission:      summary.OpeningSubmission,
		Stage:                  string(summary.Stage),
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

// openingOperatorLabel renders the identity that opened a session for the
// properties card: the bare subject an operator recognises, with the full
// (issuer, subject) pair in the element's title for the case where the sub
// alone is ambiguous across issuers.
//
// An unreadable krill_session row is the zero Subject, and it renders as
// two empty strings -- not as a placeholder identity. The card then shows
// the session id chip with no "Opened by" line at all, which is the honest
// rendering: the page records that a session was opened and by which krill
// session, and says nothing at all about a person it could not read.
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

// revisionEventSummary composes the one sentence a timeline box shows for
// one round.
//
// It has to be composed rather than read: migration 008's revision_event has
// no prose column at all -- a round is an event_type, two identity triples,
// an entity_deltas array and an open_questions_delta object, and nothing
// else. So the sentence an operator reads is derived here from the round's
// OWN content, in the round's own store order, and derived in exactly one
// place: a summary assembled at three call sites is three summaries that can
// disagree.
//
// What each part contributes, in the order the schema stores them:
//
//   - entity_deltas: each entry's summary_line, which is the only human text
//     an entity change carries. A delta with no summary_line contributes the
//     bare count rather than an empty clause, so a round that touched three
//     entities and wrote nothing down says "3 entity changes" -- which is
//     true -- instead of saying nothing at all.
//   - opened questions: the count, then each question's text, falling back to
//     its id when the text is empty (migration 008 does not require one).
//   - resolved question ids: there is no text for a resolution to restate,
//     because resolving a question never mutates it -- so the ids ARE the
//     content.
//
// Nothing here reads anything the round does not carry. A revision_event has
// no persona column, so the acting identity rendered beside this sentence
// comes from the recorded triples and is not repeated or re-interpreted here.
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
		// A round that recorded nothing is a real state -- a signoff, or an
		// answer that only closed a question elsewhere -- and saying so is
		// better than rendering an empty line where a sentence belongs.
		return "No further detail was recorded for this round."
	}
	return joinSentences(parts)
}

// entityDeltaSummary is the entity-change clause: the round's summary lines
// verbatim, or a bare count when the round wrote none.
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

// openedQuestionsSummary is the clause for the questions this round opened:
// how many, then what each asks.
func openedQuestionsSummary(opened []store.OpenQuestionOpened) string {
	if len(opened) == 0 {
		return ""
	}
	texts := make([]string, 0, len(opened))
	for _, q := range opened {
		if t := strings.TrimSpace(q.Text); t != "" {
			texts = append(texts, t)
		} else {
			// A question with no text is still a question; naming its id is
			// the only honest description of it.
			texts = append(texts, q.QuestionID)
		}
	}
	if len(opened) == 1 {
		return fmt.Sprintf("1 question opened: %s", texts[0])
	}
	return fmt.Sprintf("%d questions opened: %s", len(opened), strings.Join(texts, "; "))
}

// resolvedQuestionsSummary is the clause for the questions this round
// closed: their ids, joined the way a sentence lists them.
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

// joinSentences terminates each clause and separates them, leaving a clause
// that already ends in its own punctuation -- an opened question's text ends
// in "?" -- alone rather than following it with a stray period.
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

// formatTime renders a store timestamp in UTC; a zero time renders as the
// empty string rather than a misleading year-1 date.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleDesignGo is the design root's old product-id browse target: a
// JS-free GET /design/go?product_id=X that validates the id and 302s to
// that product's session list. Nothing links here any more -- the root
// resolves a product itself (FR c4bd4bf8) -- but the route stays
// registered so an already-bookmarked URL keeps landing (FR 2544224c).
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
// open and approved ones alike -- so a contributor can navigate into one
// without already holding its id. One route, two modes: an htmx request
// gets the page body as a bare fragment at 200, everything else gets it
// inside the shell.
func (app *App) handleDesignSessionList(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("productID"))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	app.renderDesignSessionList(w, r, productID, r.URL.Path)
}

// designSessionListPage builds the list page's view model around rows the
// caller has already read, so the list URL, the un-prefixed design root and
// the new-session blade's full-page render are one page rather than three.
//
// Path shapes come from here and nowhere else: the blade URL the primary
// action links at, and the POST target the blade's own form carries, are
// both owned by package main.
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

// newDesignSessionBlade builds the blade view for productID: the product the
// blade's URL named, resolved server-side and shown read-only, plus the two
// paths the blade's controls go to.
//
// Error and OpeningSubmission stay empty here. A blade opened fresh has
// nothing to report and nothing typed yet; the write handler fills both in
// when it hands the blade back after a refusal.
func (app *App) newDesignSessionBlade(r *http.Request, productID uuid.UUID) *pages.DesignSessionNewBlade {
	return &pages.DesignSessionNewBlade{
		ProductID:   productID.String(),
		ProductName: app.productNameFor(r.Context(), productID),
		FormAction:  designProductSessionsPath(productID),
		ListPath:    designProductSessionsPath(productID),
	}
}

// handleDesignSessionNew is the new-session blade over the list (FR
// 44d7f1e2). One route, two modes, exactly as the Spec feature blade
// answers.
//
// An htmx request gets the blade REGION alone, because the list's action
// names that region as its swap target and the list underneath it must NOT
// be re-rendered -- the rows the operator was reading are what they chose.
// A browser request gets the whole Design sessions page with the blade open
// over it, so a reload, a shared link and a no-JavaScript click all reach
// the same thing the action did.
//
// The list is read only for that second mode. Opening the blade is a page
// the operator can reach even when the session list cannot be read, so the
// fragment branch never depends on the read succeeding.
//
// Blades go one level deep: this renders the blade and nothing below it
// opens another one.
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
	// now is read once, here, so every row's relative age is measured
	// against one instant: a table whose rows disagree about "now" by the
	// time the page took to render reads as a table of different ages.
	rows, err := app.designSessionRows(r.Context(), productID, time.Now())
	if err != nil {
		logger.Error("failed to list design sessions", "product_id", productID, "error", err)
		http.Error(w, "failed to list design sessions", http.StatusInternalServerError)
		return
	}
	page := app.designSessionListPage(r, productID, rows)
	page.NewBlade = blade
	setLastViewedProductCookie(w, productID)
	// The nav key is the LIST's own path, not this blade's: the blade is a
	// view over the sessions page, so that is the item the operator is on.
	app.renderShell(w, r, "Design sessions", designProductSessionsPath(productID), pages.DesignSessionList(page))
}

// renderDesignSessionList is the session list, shared by the product-scoped
// URL and by the un-prefixed /design root that resolved this product --
// one page at two URLs, exactly as "/" and /products/{pid}/overview are one
// Overview at two URLs.
//
// activePath is passed rather than read from r.URL.Path because the two
// callers own different nav keys: the product-scoped URL is the Design
// sessions nav item's own path, and /design is the area root, which owns no
// nav item.
func (app *App) renderDesignSessionList(w http.ResponseWriter, r *http.Request, productID uuid.UUID, activePath string) {
	// now is read once, here, so every row's relative age is measured
	// against one instant: a table whose rows disagree about "now" by the
	// time the page took to render reads as a table of different ages.
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
	// The id in the path becomes the last-viewed product so the operator's
	// next un-prefixed page stays on the product they are reading. An id
	// that turns out to be outside the scope is simply dropped when the
	// cookie is read back, so this page needs no scope check of its own.
	setLastViewedProductCookie(w, productID)
	app.renderShell(w, r, "Design sessions", activePath, pages.DesignSessionList(page))
}

// productNameFor is the product's name for a page header. The design area's
// URLs hang off /design rather than off the product prefix, so the request
// carries no resolved product and the name is read from the scope listing.
//
// A listing that cannot be read yields the empty string rather than a
// failure: the table's own rows are this page's subject, and a header line
// is not worth taking the page down for. The empty header then names no
// product, which is the honest rendering of "the name is unavailable".
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

// handleDesignSessionDetail renders one session's full ordered
// revision-event log and its currently-open questions. One route, two
// modes, exactly as handleDesignSessionList.
//
// The pid is in the path rather than inferred, because "is this session
// under the product the reader is looking at?" is a question the reader
// asked and the answer may be no (FR a77852a9). A session that is unknown,
// or that belongs to another product, is an in-shell 404 rather than a
// bare http.Error: an operator who followed a stale or hand-edited link
// lands on a page that looks like the rest of the UI and says what is
// missing, not on a plain-text error with no way back.
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
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Design session", r.URL.Path, pages.DesignSessionDetail(detail))
}

// isHXRequest reports whether the caller is htmx. One route serves both
// modes: the header is what selects chrome vs. bare fragment.
func isHXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") != ""
}
